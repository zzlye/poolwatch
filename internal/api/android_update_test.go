package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"poolwatch/internal/events"
)

func TestGitHubReleaseUpdateProviderReadsCachesAndDownloads(t *testing.T) {
	apkContent := []byte("fake-signed-android-package")
	apkDigest := fmt.Sprintf("%x", sha256.Sum256(apkContent))
	var releaseCalls, manifestCalls, packageCalls int
	var receivedRange, receivedIfRange, receivedMethod string
	failLatest := false
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/releases":
			releaseCalls++
			if failLatest {
				response.WriteHeader(http.StatusBadGateway)
				return
			}
			writeJSON(response, http.StatusOK, []githubRelease{{
				TagName: "v9.9.9", HTMLURL: upstream.URL + "/releases/v9.9.9",
				PublishedAt: "2026-07-21T09:00:00Z",
				Assets:      []githubReleaseAsset{{Name: "server.zip", BrowserDownloadURL: upstream.URL + "/server.zip", Size: 10}},
			}, {
				TagName: "android-v1.2.3", HTMLURL: upstream.URL + "/releases/android-v1.2.3",
				PublishedAt: "2026-07-21T08:00:00Z",
				Assets: []githubReleaseAsset{
					{Name: androidUpdateAssetName, BrowserDownloadURL: upstream.URL + "/android-update.json", Size: 512},
					{Name: "poolwatch-android-v1.2.3.apk", BrowserDownloadURL: upstream.URL + "/poolwatch.apk", Size: int64(len(apkContent)), Digest: "sha256:" + apkDigest},
				},
			}})
		case "/android-update.json":
			manifestCalls++
			writeJSON(response, http.StatusOK, AndroidUpdateMetadata{
				VersionCode: 12, VersionName: "1.2.3", Tag: "android-v1.2.3",
				DownloadURL: upstream.URL + "/poolwatch.apk", SHA256: strings.ToUpper(apkDigest),
				SizeBytes: int64(len(apkContent)), Mandatory: true, ReleaseURL: upstream.URL + "/releases/android-v1.2.3",
				ReleaseNotes: "修复更新问题", PublishedAt: "2026-07-21T16:00:00+08:00",
			})
		case "/poolwatch.apk":
			packageCalls++
			receivedMethod = request.Method
			receivedRange = request.Header.Get("Range")
			receivedIfRange = request.Header.Get("If-Range")
			response.Header().Set("Accept-Ranges", "bytes")
			response.Header().Set("ETag", `"apk-etag"`)
			response.Header().Set("Last-Modified", "Tue, 21 Jul 2026 08:00:00 GMT")
			if receivedRange != "" {
				response.Header().Set("Content-Range", fmt.Sprintf("bytes 2-5/%d", len(apkContent)))
				response.WriteHeader(http.StatusPartialContent)
				_, _ = response.Write(apkContent[2:6])
				return
			}
			_, _ = response.Write(apkContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer upstream.Close()

	provider := newGitHubReleaseUpdateProvider(upstream.Client(), upstream.URL+"/releases", true)
	provider.cacheTTL = time.Hour
	provider.failureTTL = time.Minute
	currentTime := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	provider.now = func() time.Time { return currentTime }
	metadata, err := provider.LatestAndroid(context.Background())
	if err != nil {
		t.Fatalf("读取安卓更新清单失败: %v", err)
	}
	if metadata.VersionCode != 12 || metadata.SHA256 != apkDigest || metadata.PublishedAt != "2026-07-21T08:00:00Z" {
		t.Fatalf("更新清单归一化结果不正确: %#v", metadata)
	}
	if !metadata.Mandatory || metadata.SizeBytes != int64(len(apkContent)) {
		t.Fatalf("更新清单缺少强制更新或安装包大小: %#v", metadata)
	}
	if _, err := provider.LatestAndroid(context.Background()); err != nil {
		t.Fatalf("读取缓存更新清单失败: %v", err)
	}
	if releaseCalls != 1 || manifestCalls != 1 {
		t.Fatalf("有效期内不应重复请求 GitHub: release=%d manifest=%d", releaseCalls, manifestCalls)
	}
	currentTime = currentTime.Add(provider.cacheTTL + time.Minute)
	failLatest = true
	staleMetadata, err := provider.LatestAndroid(context.Background())
	if err != nil || staleMetadata.VersionCode != metadata.VersionCode || releaseCalls != 2 || manifestCalls != 1 {
		t.Fatalf("GitHub 短暂故障时没有沿用有效旧清单: metadata=%#v release=%d manifest=%d err=%v", staleMetadata, releaseCalls, manifestCalls, err)
	}
	if _, err := provider.LatestAndroid(context.Background()); err != nil || releaseCalls != 2 {
		t.Fatalf("失败退避期内不应重复访问 GitHub: release=%d err=%v", releaseCalls, err)
	}
	currentTime = currentTime.Add(provider.failureTTL + time.Second)
	if _, err := provider.LatestAndroid(context.Background()); err != nil || releaseCalls != 3 {
		t.Fatalf("失败退避结束后应重新尝试并继续使用旧清单: release=%d err=%v", releaseCalls, err)
	}

	packageFile, err := provider.OpenAndroidPackage(context.Background(), metadata, http.MethodGet, "bytes=2-5", `"apk-etag"`)
	if err != nil {
		t.Fatalf("读取安装包范围失败: %v", err)
	}
	defer packageFile.Body.Close()
	content, err := io.ReadAll(packageFile.Body)
	if err != nil {
		t.Fatalf("读取安装包内容失败: %v", err)
	}
	if packageCalls != 1 || receivedMethod != http.MethodGet || receivedRange != "bytes=2-5" || receivedIfRange != `"apk-etag"` {
		t.Fatalf("安装包续传请求没有完整转发: method=%s range=%s if-range=%s", receivedMethod, receivedRange, receivedIfRange)
	}
	if packageFile.StatusCode != http.StatusPartialContent || packageFile.ContentRange == "" || packageFile.ETag != `"apk-etag"` || !bytes.Equal(content, apkContent[2:6]) {
		t.Fatalf("安装包范围响应不正确: %#v, %q", packageFile, content)
	}
}

func TestGitHubReleaseUpdateProviderRejectsUnsafeManifest(t *testing.T) {
	validDigest := strings.Repeat("a", 64)
	validURL := "https://github.com/zzlye/poolwatch/releases/download/android-v1.2.3/poolwatch.apk"
	release := githubRelease{
		TagName: "android-v1.2.3", HTMLURL: "https://github.com/zzlye/poolwatch/releases/tag/android-v1.2.3",
		PublishedAt: "2026-07-21T08:00:00Z",
		Assets:      []githubReleaseAsset{{Name: "poolwatch.apk", BrowserDownloadURL: validURL, Size: 123, Digest: "sha256:" + validDigest}},
	}
	valid := AndroidUpdateMetadata{
		VersionCode: 12, VersionName: "1.2.3", Tag: release.TagName, DownloadURL: validURL,
		SHA256: validDigest, SizeBytes: 123, ReleaseURL: release.HTMLURL, PublishedAt: release.PublishedAt,
	}
	provider := NewGitHubReleaseUpdateProvider()
	tests := []struct {
		name   string
		mutate func(*AndroidUpdateMetadata)
	}{
		{name: "外部下载地址", mutate: func(value *AndroidUpdateMetadata) { value.DownloadURL = "https://evil.example/poolwatch.apk" }},
		{name: "错误校验值", mutate: func(value *AndroidUpdateMetadata) { value.SHA256 = "not-a-sha256" }},
		{name: "标签不一致", mutate: func(value *AndroidUpdateMetadata) { value.Tag = "android-v9.9.9" }},
		{name: "大小不一致", mutate: func(value *AndroidUpdateMetadata) { value.SizeBytes = 124 }},
		{name: "文件过大", mutate: func(value *AndroidUpdateMetadata) { value.SizeBytes = androidAPKMaxBytes + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if _, err := provider.validateMetadata(candidate, release); err == nil {
				t.Fatalf("危险或不一致的更新清单应被拒绝: %#v", candidate)
			}
		})
	}
}

type androidUpdateHandlerProvider struct {
	metadata     AndroidUpdateMetadata
	latestErr    error
	payload      []byte
	openCalls    int
	received     struct{ method, byteRange, ifRange string }
	packageState AndroidPackage
}

func (provider *androidUpdateHandlerProvider) LatestAndroid(context.Context) (AndroidUpdateMetadata, error) {
	return provider.metadata, provider.latestErr
}

func (provider *androidUpdateHandlerProvider) OpenAndroidPackage(_ context.Context, _ AndroidUpdateMetadata, method, byteRange, ifRange string) (AndroidPackage, error) {
	provider.openCalls++
	provider.received.method = method
	provider.received.byteRange = byteRange
	provider.received.ifRange = ifRange
	result := provider.packageState
	result.Body = io.NopCloser(bytes.NewReader(provider.payload))
	if result.ContentLength == 0 && len(provider.payload) > 0 {
		result.ContentLength = int64(len(provider.payload))
	}
	return result, nil
}

func TestAndroidUpdateAndDownloadEndpointsArePublicAndProxyPackage(t *testing.T) {
	payload := []byte("3456")
	provider := &androidUpdateHandlerProvider{
		metadata: AndroidUpdateMetadata{
			VersionCode: 12, VersionName: "1.2.3", Tag: "android-v1.2.3",
			DownloadURL: "https://github.com/zzlye/poolwatch/releases/download/android-v1.2.3/poolwatch.apk",
			SHA256:      strings.Repeat("a", 64), SizeBytes: 26,
		},
		payload: payload,
		packageState: AndroidPackage{
			StatusCode: http.StatusPartialContent, ContentType: "application/vnd.android.package-archive",
			ContentRange: "bytes 2-5/26", AcceptRanges: "bytes", ETag: `"apk-etag"`,
			LastModified: "Tue, 21 Jul 2026 08:00:00 GMT", FileName: "poolwatch-android-v1.2.3.apk", MaximumBytes: 26,
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(NewServer(Dependencies{
		Events: events.NewHub(), AndroidUpdates: provider, PublicBaseURL: "https://jiance.example", Logger: logger,
	}).Handler())
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/api/app/update/android")
	if err != nil {
		t.Fatalf("读取公开更新接口失败: %v", err)
	}
	defer response.Body.Close()
	var metadata AndroidUpdateMetadata
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		t.Fatalf("解析公开更新接口失败: %v", err)
	}
	expectedProxyURL := "/api/app/update/android/apk?versionCode=12"
	if response.StatusCode != http.StatusOK || metadata.DownloadURL != expectedProxyURL || strings.Contains(metadata.DownloadURL, "github.com") {
		t.Fatalf("公开更新接口没有返回服务端代理地址: status=%d metadata=%#v", response.StatusCode, metadata)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("更新信息不应被中间缓存: %s", response.Header.Get("Cache-Control"))
	}

	request, err := http.NewRequest(http.MethodGet, server.URL+androidAPKProxyPath+"?versionCode=12", nil)
	if err != nil {
		t.Fatalf("创建安装包请求失败: %v", err)
	}
	request.Header.Set("Range", "bytes=2-5")
	request.Header.Set("If-Range", `"apk-etag"`)
	packageResponse, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("请求安装包代理失败: %v", err)
	}
	defer packageResponse.Body.Close()
	packageContent, err := io.ReadAll(packageResponse.Body)
	if err != nil {
		t.Fatalf("读取安装包代理响应失败: %v", err)
	}
	if packageResponse.StatusCode != http.StatusPartialContent || !bytes.Equal(packageContent, payload) {
		t.Fatalf("安装包代理内容不正确: status=%d body=%q", packageResponse.StatusCode, packageContent)
	}
	if provider.received.method != http.MethodGet || provider.received.byteRange != "bytes=2-5" || provider.received.ifRange != `"apk-etag"` {
		t.Fatalf("安装包代理没有转发续传信息: %#v", provider.received)
	}
	if packageResponse.Header.Get("Content-Range") != "bytes 2-5/26" || packageResponse.Header.Get("ETag") != `"apk-etag"` ||
		packageResponse.Header.Get("Last-Modified") == "" || packageResponse.Header.Get("X-Content-SHA256") != strings.Repeat("a", 64) {
		t.Fatalf("安装包代理响应头不完整: %#v", packageResponse.Header)
	}
	if !strings.Contains(packageResponse.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("固定版本安装包应允许设备缓存: %s", packageResponse.Header.Get("Cache-Control"))
	}
	if !strings.Contains(packageResponse.Header.Get("Content-Disposition"), "poolwatch-android-v1.2.3.apk") {
		t.Fatalf("安装包下载文件名不正确: %s", packageResponse.Header.Get("Content-Disposition"))
	}

	provider.packageState.StatusCode = http.StatusOK
	provider.payload = bytes.Repeat([]byte{'x'}, 26)
	fullResponse, err := server.Client().Get(server.URL + androidAPKProxyPath + "?versionCode=12")
	if err != nil {
		t.Fatalf("请求完整安装包失败: %v", err)
	}
	fullContent, err := io.ReadAll(fullResponse.Body)
	fullResponse.Body.Close()
	if err != nil || fullResponse.StatusCode != http.StatusOK || len(fullContent) != 26 ||
		fullResponse.Header.Get("Content-Type") != "application/vnd.android.package-archive" || fullResponse.Header.Get("Content-Length") != "26" {
		t.Fatalf("完整安装包响应不正确: status=%d length=%s body=%d err=%v", fullResponse.StatusCode, fullResponse.Header.Get("Content-Length"), len(fullContent), err)
	}

	headRequest, _ := http.NewRequest(http.MethodHead, server.URL+androidAPKProxyPath+"?versionCode=12", nil)
	headResponse, err := server.Client().Do(headRequest)
	if err != nil {
		t.Fatalf("请求安装包 HEAD 失败: %v", err)
	}
	headBody, _ := io.ReadAll(headResponse.Body)
	headResponse.Body.Close()
	if headResponse.StatusCode != http.StatusOK || len(headBody) != 0 || headResponse.Header.Get("Content-Length") != "26" {
		t.Fatalf("安装包 HEAD 响应不正确: status=%d length=%s body=%d", headResponse.StatusCode, headResponse.Header.Get("Content-Length"), len(headBody))
	}

	callsBeforeReject := provider.openCalls
	status, _ := requestJSON(t, server.Client(), http.MethodGet, server.URL+androidAPKProxyPath+"?versionCode=11", nil, "")
	if status != http.StatusConflict || provider.openCalls != callsBeforeReject {
		t.Fatalf("旧版本下载请求应在访问上游前被拒绝: status=%d calls=%d", status, provider.openCalls)
	}
	invalidRangeRequest, _ := http.NewRequest(http.MethodGet, server.URL+androidAPKProxyPath+"?versionCode=12", nil)
	invalidRangeRequest.Header.Set("Range", "bytes=0-1,4-5")
	invalidRangeResponse, err := server.Client().Do(invalidRangeRequest)
	if err != nil {
		t.Fatalf("请求非法范围测试失败: %v", err)
	}
	invalidRangeResponse.Body.Close()
	if invalidRangeResponse.StatusCode != http.StatusRequestedRangeNotSatisfiable || provider.openCalls != callsBeforeReject {
		t.Fatalf("多段范围下载应在访问上游前被拒绝: status=%d calls=%d", invalidRangeResponse.StatusCode, provider.openCalls)
	}
}

func TestAndroidUpdateEndpointHidesProviderErrors(t *testing.T) {
	provider := &androidUpdateHandlerProvider{latestErr: errors.New("TOKEN=should-not-leak")}
	server := httptest.NewServer(NewServer(Dependencies{
		Events: events.NewHub(), AndroidUpdates: provider, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}).Handler())
	defer server.Close()
	status, body := requestJSON(t, server.Client(), http.MethodGet, server.URL+"/api/app/update/android", nil, "")
	if status != http.StatusServiceUnavailable || strings.Contains(body, "TOKEN") || strings.Contains(body, "should-not-leak") {
		t.Fatalf("更新接口泄漏了上游错误: status=%d body=%s", status, body)
	}
}

func TestSingleByteRangeAndIfRangeValidation(t *testing.T) {
	if value, err := singleByteRange("bytes=100-"); err != nil || value != "bytes=100-" {
		t.Fatalf("有效范围未通过: %q, %v", value, err)
	}
	if _, err := singleByteRange("bytes=0-1,3-4"); err == nil {
		t.Fatal("多段范围不应通过")
	}
	if value, err := safeIfRange(`"etag"`); err != nil || value != `"etag"` {
		t.Fatalf("有效实体标签未通过: %q, %v", value, err)
	}
	if _, err := safeIfRange("invalid-value"); err == nil {
		t.Fatal("无效 If-Range 不应通过")
	}
	if err := validatePartialContentRange("bytes=2-5", "bytes 2-5/26", 4, 26); err != nil {
		t.Fatalf("有效范围响应未通过: %v", err)
	}
	if err := validatePartialContentRange("bytes=2-5", "bytes 3-5/26", 3, 26); err == nil {
		t.Fatal("与请求不一致的范围响应不应通过")
	}
	if err := validatePartialContentRange("bytes=2-5", "bytes 2-5/27", 4, 26); err == nil {
		t.Fatal("总长度不一致的范围响应不应通过")
	}
	if err := validateUnsatisfiedContentRange("bytes */26", 26); err != nil {
		t.Fatalf("有效 416 范围响应未通过: %v", err)
	}
}

func TestGitHubRedirectPolicyRejectsUntrustedAddress(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://evil.example/download.apk", nil)
	if err != nil {
		t.Fatalf("创建重定向测试请求失败: %v", err)
	}
	if err := githubRedirectPolicy(request, nil); err == nil {
		t.Fatal("重定向到非 GitHub 地址应被拒绝")
	}
	trustedRequest, err := http.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/download.apk", nil)
	if err != nil {
		t.Fatalf("创建受信重定向测试请求失败: %v", err)
	}
	if err := githubRedirectPolicy(trustedRequest, nil); err != nil {
		t.Fatalf("受信 GitHub 资源地址不应被拒绝: %v", err)
	}
}
