package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	githubAndroidReleasesURL = "https://api.github.com/repos/zzlye/poolwatch/releases?per_page=20"
	androidUpdateAssetName   = "android-update.json"
	githubReleaseMaxBytes    = 1 << 20
	androidManifestMaxBytes  = 64 << 10
	androidAPKMaxBytes       = 256 << 20
	androidUpdateCacheTTL    = 10 * time.Minute
	androidUpdateFailureTTL  = time.Minute
	androidUpdateStaleWindow = 24 * time.Hour
	androidAPKProxyPath      = "/api/app/update/android/apk"
)

var (
	// ErrAndroidUpdateUnavailable 表示尚未发布可供安卓端安装的正式版本。
	ErrAndroidUpdateUnavailable = errors.New("暂无安卓更新")
	sha256Pattern               = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// AndroidUpdateMetadata 是安卓端检查更新时使用的公开元数据。
type AndroidUpdateMetadata struct {
	VersionCode  int64  `json:"versionCode"`
	VersionName  string `json:"versionName"`
	Tag          string `json:"tag,omitempty"`
	DownloadURL  string `json:"downloadUrl"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"sizeBytes,omitempty"`
	Mandatory    bool   `json:"mandatory"`
	ReleaseURL   string `json:"releaseUrl,omitempty"`
	ReleaseNotes string `json:"releaseNotes,omitempty"`
	PublishedAt  string `json:"publishedAt,omitempty"`
}

// AndroidUpdateProvider 抽象安卓更新来源，便于服务端测试和后续切换发布渠道。
type AndroidUpdateProvider interface {
	LatestAndroid(context.Context) (AndroidUpdateMetadata, error)
}

// AndroidPackage 是更新安装包代理使用的响应体和基础响应头。
type AndroidPackage struct {
	Body          io.ReadCloser
	ContentLength int64
	ContentType   string
	ContentRange  string
	AcceptRanges  string
	ETag          string
	LastModified  string
	StatusCode    int
	FileName      string
	MaximumBytes  int64
}

// AndroidPackageProvider 在更新清单之外提供安装包流。
type AndroidPackageProvider interface {
	AndroidUpdateProvider
	OpenAndroidPackage(context.Context, AndroidUpdateMetadata, string, string, string) (AndroidPackage, error)
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
}

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	HTMLURL     string               `json:"html_url"`
	Draft       bool                 `json:"draft"`
	Prerelease  bool                 `json:"prerelease"`
	PublishedAt string               `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

// GitHubReleaseUpdateProvider 从项目的正式安卓 Release 中读取最新更新清单。
type GitHubReleaseUpdateProvider struct {
	client            *http.Client
	downloadClient    *http.Client
	releaseURL        string
	allowInsecureURLs bool
	cacheTTL          time.Duration
	failureTTL        time.Duration
	staleWindow       time.Duration
	now               func() time.Time

	mu            sync.Mutex
	cached        AndroidUpdateMetadata
	fetchedAt     time.Time
	lastAttemptAt time.Time
	lastError     error
}

// NewGitHubReleaseUpdateProvider 创建生产环境使用的 GitHub Release 更新源。
func NewGitHubReleaseUpdateProvider() *GitHubReleaseUpdateProvider {
	provider := newGitHubReleaseUpdateProvider(newGitHubMetadataHTTPClient(), githubAndroidReleasesURL, false)
	provider.downloadClient = newGitHubDownloadHTTPClient()
	return provider
}

func newGitHubReleaseUpdateProvider(client *http.Client, releaseURL string, allowInsecureURLs bool) *GitHubReleaseUpdateProvider {
	if client == nil {
		client = newGitHubMetadataHTTPClient()
	}
	return &GitHubReleaseUpdateProvider{
		client: client, downloadClient: client, releaseURL: releaseURL, allowInsecureURLs: allowInsecureURLs,
		cacheTTL: androidUpdateCacheTTL, failureTTL: androidUpdateFailureTTL,
		staleWindow: androidUpdateStaleWindow, now: time.Now,
	}
}

func newGitHubMetadataHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       12 * time.Second,
		CheckRedirect: githubRedirectPolicy,
	}
}

func newGitHubDownloadHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 20 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: transport, CheckRedirect: githubRedirectPolicy}
}

func githubRedirectPolicy(request *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("GitHub 下载重定向次数过多")
	}
	if !isTrustedGitHubURL(request.URL) {
		return errors.New("GitHub 下载重定向到了非受信地址")
	}
	return nil
}

// LatestAndroid 返回最新正式版本；短暂上游故障时继续使用最近一次有效清单。
func (provider *GitHubReleaseUpdateProvider) LatestAndroid(ctx context.Context) (AndroidUpdateMetadata, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()

	now := provider.now().UTC()
	if provider.cached.VersionCode > 0 && now.Sub(provider.fetchedAt) < provider.cacheTTL {
		return provider.cached, nil
	}
	if provider.lastError != nil && now.Sub(provider.lastAttemptAt) < provider.failureTTL {
		if provider.cached.VersionCode > 0 && now.Sub(provider.fetchedAt) < provider.staleWindow {
			return provider.cached, nil
		}
		return AndroidUpdateMetadata{}, provider.lastError
	}
	provider.lastAttemptAt = now
	metadata, err := provider.fetch(ctx)
	if err != nil {
		provider.lastError = err
		if provider.cached.VersionCode > 0 && now.Sub(provider.fetchedAt) < provider.staleWindow {
			return provider.cached, nil
		}
		return AndroidUpdateMetadata{}, err
	}
	provider.cached = metadata
	provider.fetchedAt = now
	provider.lastError = nil
	return metadata, nil
}

// OpenAndroidPackage 通过受信的 GitHub Release 地址读取安装包，不把上游地址暴露给安卓端。
func (provider *GitHubReleaseUpdateProvider) OpenAndroidPackage(ctx context.Context, metadata AndroidUpdateMetadata, method, byteRange, ifRange string) (AndroidPackage, error) {
	if err := provider.validateSourceURL(metadata.DownloadURL); err != nil {
		return AndroidPackage{}, errors.New("安装包下载地址无效")
	}
	if method != http.MethodGet && method != http.MethodHead {
		return AndroidPackage{}, errors.New("安装包请求方法不受支持")
	}
	if metadata.SizeBytes <= 0 || metadata.SizeBytes > androidAPKMaxBytes {
		return AndroidPackage{}, errors.New("安装包大小超过允许范围")
	}
	request, err := http.NewRequestWithContext(ctx, method, metadata.DownloadURL, nil)
	if err != nil {
		return AndroidPackage{}, errors.New("创建安装包请求失败")
	}
	request.Header.Set("Accept", "application/vnd.android.package-archive, application/octet-stream")
	request.Header.Set("User-Agent", "poolwatch-update-checker")
	if byteRange != "" {
		request.Header.Set("Range", byteRange)
	}
	if ifRange != "" {
		request.Header.Set("If-Range", ifRange)
	}
	response, err := provider.downloadClient.Do(request)
	if err != nil {
		return AndroidPackage{}, err
	}
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		if err := validateUnsatisfiedContentRange(response.Header.Get("Content-Range"), metadata.SizeBytes); err != nil {
			response.Body.Close()
			return AndroidPackage{}, err
		}
		response.Body.Close()
		return AndroidPackage{
			Body: http.NoBody, ContentLength: 0, ContentType: "application/vnd.android.package-archive",
			ContentRange: response.Header.Get("Content-Range"), AcceptRanges: response.Header.Get("Accept-Ranges"),
			ETag: response.Header.Get("ETag"), LastModified: response.Header.Get("Last-Modified"),
			StatusCode: response.StatusCode, FileName: "poolwatch-update.apk", MaximumBytes: metadata.SizeBytes,
		}, nil
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		response.Body.Close()
		return AndroidPackage{}, fmt.Errorf("安装包上游返回状态 %d", response.StatusCode)
	}
	if byteRange == "" && response.StatusCode != http.StatusOK {
		response.Body.Close()
		return AndroidPackage{}, errors.New("安装包上游不支持完整下载")
	}
	if response.StatusCode == http.StatusPartialContent {
		if err := validatePartialContentRange(
			byteRange,
			response.Header.Get("Content-Range"),
			response.ContentLength,
			metadata.SizeBytes,
		); err != nil {
			response.Body.Close()
			return AndroidPackage{}, err
		}
	}
	if metadata.SizeBytes > 0 && response.StatusCode == http.StatusOK && response.ContentLength > 0 && response.ContentLength != metadata.SizeBytes {
		response.Body.Close()
		return AndroidPackage{}, errors.New("安装包大小与更新清单不一致")
	}
	if response.ContentLength > androidAPKMaxBytes {
		response.Body.Close()
		return AndroidPackage{}, errors.New("安装包响应超过允许大小")
	}
	fileName := path.Base(strings.TrimSpace(request.URL.Path))
	if fileName == "." || fileName == "/" || !strings.HasSuffix(strings.ToLower(fileName), ".apk") {
		fileName = "poolwatch-update.apk"
	}
	return AndroidPackage{
		Body: response.Body, ContentLength: response.ContentLength, ContentType: "application/vnd.android.package-archive",
		ContentRange: response.Header.Get("Content-Range"), AcceptRanges: response.Header.Get("Accept-Ranges"),
		ETag: response.Header.Get("ETag"), LastModified: response.Header.Get("Last-Modified"),
		StatusCode: response.StatusCode, FileName: fileName, MaximumBytes: metadata.SizeBytes,
	}, nil
}

func (provider *GitHubReleaseUpdateProvider) fetch(ctx context.Context) (AndroidUpdateMetadata, error) {
	var releases []githubRelease
	status, err := provider.readJSON(ctx, provider.releaseURL, githubReleaseMaxBytes, &releases)
	if err != nil {
		return AndroidUpdateMetadata{}, fmt.Errorf("读取 GitHub 最新版本失败: %w", err)
	}
	if status == http.StatusNotFound {
		return AndroidUpdateMetadata{}, ErrAndroidUpdateUnavailable
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return AndroidUpdateMetadata{}, fmt.Errorf("GitHub 最新版本接口返回状态 %d", status)
	}
	var release githubRelease
	found := false
	for _, candidate := range releases {
		if candidate.Draft || candidate.Prerelease || !strings.HasPrefix(candidate.TagName, "android-v") {
			continue
		}
		if _, exists := findReleaseAsset(candidate.Assets, androidUpdateAssetName); !exists {
			continue
		}
		release = candidate
		found = true
		break
	}
	if !found {
		return AndroidUpdateMetadata{}, ErrAndroidUpdateUnavailable
	}

	manifestAsset, exists := findReleaseAsset(release.Assets, androidUpdateAssetName)
	if !exists {
		return AndroidUpdateMetadata{}, ErrAndroidUpdateUnavailable
	}
	if err := provider.validateSourceURL(manifestAsset.BrowserDownloadURL); err != nil {
		return AndroidUpdateMetadata{}, fmt.Errorf("更新清单下载地址无效: %w", err)
	}

	var metadata AndroidUpdateMetadata
	status, err = provider.readJSON(ctx, manifestAsset.BrowserDownloadURL, androidManifestMaxBytes, &metadata)
	if err != nil {
		return AndroidUpdateMetadata{}, fmt.Errorf("读取安卓更新清单失败: %w", err)
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return AndroidUpdateMetadata{}, fmt.Errorf("安卓更新清单返回状态 %d", status)
	}
	return provider.validateMetadata(metadata, release)
}

func (provider *GitHubReleaseUpdateProvider) readJSON(ctx context.Context, endpoint string, maximumBytes int64, destination any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "poolwatch-update-checker")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := provider.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return response.StatusCode, nil
	}

	content, err := io.ReadAll(io.LimitReader(response.Body, maximumBytes+1))
	if err != nil {
		return response.StatusCode, err
	}
	if int64(len(content)) > maximumBytes {
		return response.StatusCode, errors.New("响应内容超过允许大小")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(destination); err != nil {
		return response.StatusCode, errors.New("响应不是有效的 JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return response.StatusCode, errors.New("响应包含多余的 JSON 内容")
	}
	return response.StatusCode, nil
}

func (provider *GitHubReleaseUpdateProvider) validateMetadata(metadata AndroidUpdateMetadata, release githubRelease) (AndroidUpdateMetadata, error) {
	metadata.VersionName = strings.TrimSpace(metadata.VersionName)
	metadata.Tag = strings.TrimSpace(metadata.Tag)
	metadata.DownloadURL = strings.TrimSpace(metadata.DownloadURL)
	metadata.SHA256 = strings.ToLower(strings.TrimSpace(metadata.SHA256))
	metadata.ReleaseURL = strings.TrimSpace(metadata.ReleaseURL)
	metadata.ReleaseNotes = strings.TrimSpace(metadata.ReleaseNotes)
	metadata.PublishedAt = strings.TrimSpace(metadata.PublishedAt)

	if metadata.VersionCode <= 0 {
		return AndroidUpdateMetadata{}, errors.New("安卓更新清单缺少有效版本号")
	}
	if metadata.VersionName == "" || len(metadata.VersionName) > 64 {
		return AndroidUpdateMetadata{}, errors.New("安卓更新清单的版本名称无效")
	}
	if metadata.Tag == "" {
		metadata.Tag = release.TagName
	}
	if metadata.Tag != release.TagName || len(metadata.Tag) > 100 {
		return AndroidUpdateMetadata{}, errors.New("安卓更新清单与发布标签不一致")
	}
	if !sha256Pattern.MatchString(metadata.SHA256) {
		return AndroidUpdateMetadata{}, errors.New("安卓更新清单的 SHA-256 无效")
	}
	if err := provider.validateSourceURL(metadata.DownloadURL); err != nil {
		return AndroidUpdateMetadata{}, fmt.Errorf("安卓安装包下载地址无效: %w", err)
	}

	apkAsset, exists := findReleaseAssetByURL(release.Assets, metadata.DownloadURL)
	if !exists || !strings.HasSuffix(strings.ToLower(apkAsset.Name), ".apk") {
		return AndroidUpdateMetadata{}, errors.New("安卓更新清单引用的安装包不属于当前发布")
	}
	if metadata.SizeBytes < 0 || (metadata.SizeBytes > 0 && apkAsset.Size > 0 && metadata.SizeBytes != apkAsset.Size) {
		return AndroidUpdateMetadata{}, errors.New("安卓更新清单的安装包大小与发布资产不一致")
	}
	if metadata.SizeBytes == 0 {
		metadata.SizeBytes = apkAsset.Size
	}
	if metadata.SizeBytes <= 0 || metadata.SizeBytes > androidAPKMaxBytes {
		return AndroidUpdateMetadata{}, errors.New("安卓安装包大小超过允许范围")
	}
	if digest := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(apkAsset.Digest)), "sha256:"); digest != "" && digest != metadata.SHA256 {
		return AndroidUpdateMetadata{}, errors.New("安卓更新清单的 SHA-256 与发布资产不一致")
	}

	if metadata.ReleaseURL == "" {
		metadata.ReleaseURL = strings.TrimSpace(release.HTMLURL)
	}
	if metadata.ReleaseURL != "" {
		if err := provider.validateSourceURL(metadata.ReleaseURL); err != nil {
			return AndroidUpdateMetadata{}, fmt.Errorf("安卓版本说明地址无效: %w", err)
		}
	}
	if len(metadata.ReleaseNotes) > 32<<10 {
		return AndroidUpdateMetadata{}, errors.New("安卓版本说明过长")
	}
	if metadata.PublishedAt == "" {
		metadata.PublishedAt = strings.TrimSpace(release.PublishedAt)
	}
	if metadata.PublishedAt != "" {
		publishedAt, err := time.Parse(time.RFC3339, metadata.PublishedAt)
		if err != nil {
			return AndroidUpdateMetadata{}, errors.New("安卓发布时间格式无效")
		}
		metadata.PublishedAt = publishedAt.UTC().Format(time.RFC3339)
	}
	return metadata, nil
}

func (provider *GitHubReleaseUpdateProvider) validateSourceURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("地址格式不正确")
	}
	if provider.allowInsecureURLs {
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return errors.New("地址协议不受支持")
		}
		return nil
	}
	if !isTrustedGitHubURL(parsed) {
		return errors.New("地址不是受信的 GitHub HTTPS 地址")
	}
	return nil
}

func isTrustedGitHubURL(parsed *url.URL) bool {
	if parsed == nil || parsed.Scheme != "https" || parsed.User != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "github.com", "api.github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com":
		return true
	default:
		return false
	}
}

func findReleaseAsset(assets []githubReleaseAsset, name string) (githubReleaseAsset, bool) {
	for _, asset := range assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return githubReleaseAsset{}, false
}

func findReleaseAssetByURL(assets []githubReleaseAsset, downloadURL string) (githubReleaseAsset, bool) {
	for _, asset := range assets {
		if asset.BrowserDownloadURL == downloadURL {
			return asset, true
		}
	}
	return githubReleaseAsset{}, false
}

func (s *Server) handleAndroidUpdate(response http.ResponseWriter, request *http.Request) {
	if s.dependencies.AndroidUpdates == nil {
		writeAPIError(response, http.StatusNotFound, "暂无安卓更新")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	metadata, err := s.dependencies.AndroidUpdates.LatestAndroid(ctx)
	if err != nil {
		if errors.Is(err, ErrAndroidUpdateUnavailable) {
			writeAPIError(response, http.StatusNotFound, "暂无安卓更新")
			return
		}
		s.dependencies.Logger.Warn("获取安卓更新信息失败", "error", err.Error())
		writeAPIError(response, http.StatusServiceUnavailable, "更新信息暂时不可用")
		return
	}
	// 安卓端始终从当前服务下载，避免不同网络环境下 GitHub 资源地址不可达。
	metadata.DownloadURL = androidAPKProxyURL(metadata.VersionCode)
	writeJSON(response, http.StatusOK, metadata)
}

func (s *Server) handleAndroidAPK(response http.ResponseWriter, request *http.Request) {
	select {
	case s.androidSlots <- struct{}{}:
		defer func() { <-s.androidSlots }()
	default:
		response.Header().Set("Retry-After", "10")
		writeAPIError(response, http.StatusTooManyRequests, "安装包下载人数较多，请稍后重试")
		return
	}
	provider, ok := s.dependencies.AndroidUpdates.(AndroidPackageProvider)
	if !ok {
		writeAPIError(response, http.StatusNotFound, "暂无安卓安装包")
		return
	}
	metadata, err := s.dependencies.AndroidUpdates.LatestAndroid(request.Context())
	if err != nil {
		if errors.Is(err, ErrAndroidUpdateUnavailable) {
			writeAPIError(response, http.StatusNotFound, "暂无安卓安装包")
			return
		}
		s.dependencies.Logger.Warn("读取安卓安装包清单失败", "error", err.Error())
		writeAPIError(response, http.StatusServiceUnavailable, "安装包暂时不可用")
		return
	}
	requestedVersion, err := strconv.ParseInt(strings.TrimSpace(request.URL.Query().Get("versionCode")), 10, 64)
	if err != nil || requestedVersion <= 0 || requestedVersion != metadata.VersionCode {
		writeAPIError(response, http.StatusConflict, "更新版本已经变化，请重新检查更新")
		return
	}
	byteRange, err := singleByteRange(request.Header.Get("Range"))
	if err != nil {
		response.Header().Set("Accept-Ranges", "bytes")
		response.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(metadata.SizeBytes, 10))
		writeAPIError(response, http.StatusRequestedRangeNotSatisfiable, "下载范围不受支持")
		return
	}
	ifRange, err := safeIfRange(request.Header.Get("If-Range"))
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, "续传校验信息格式不正确")
		return
	}
	packageFile, err := provider.OpenAndroidPackage(request.Context(), metadata, request.Method, byteRange, ifRange)
	if err != nil {
		s.dependencies.Logger.Warn("读取安卓安装包失败", "error", err.Error())
		writeAPIError(response, http.StatusBadGateway, "安装包暂时不可用")
		return
	}
	defer packageFile.Body.Close()
	status := packageFile.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	response.Header().Set("Content-Type", packageFile.ContentType)
	response.Header().Set("Content-Disposition", `attachment; filename="`+safeAPKFileName(packageFile.FileName)+`"`)
	// 安装包由版本号和校验值共同固定，允许设备缓存以减少重复代理流量。
	response.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	acceptRanges := packageFile.AcceptRanges
	if acceptRanges == "" {
		acceptRanges = "bytes"
	}
	response.Header().Set("Accept-Ranges", acceptRanges)
	response.Header().Set("X-Content-SHA256", metadata.SHA256)
	if packageFile.ContentRange != "" {
		response.Header().Set("Content-Range", packageFile.ContentRange)
	}
	if packageFile.ContentLength >= 0 {
		response.Header().Set("Content-Length", fmt.Sprintf("%d", packageFile.ContentLength))
	}
	if packageFile.ETag != "" {
		response.Header().Set("ETag", packageFile.ETag)
	}
	if packageFile.LastModified != "" {
		response.Header().Set("Last-Modified", packageFile.LastModified)
	}
	response.WriteHeader(status)
	if request.Method == http.MethodHead || status == http.StatusRequestedRangeNotSatisfiable {
		return
	}
	maximumBytes := packageFile.MaximumBytes
	if maximumBytes <= 0 || maximumBytes > androidAPKMaxBytes {
		maximumBytes = androidAPKMaxBytes
	}
	if packageFile.ContentLength > 0 && packageFile.ContentLength < maximumBytes {
		maximumBytes = packageFile.ContentLength
	}
	if _, err := io.Copy(response, io.LimitReader(packageFile.Body, maximumBytes)); err != nil {
		s.dependencies.Logger.Warn("发送安卓安装包时连接中断", "error", err.Error())
	}
}

func androidAPKProxyURL(versionCode int64) string {
	return androidAPKProxyPath + "?versionCode=" + strconv.FormatInt(versionCode, 10)
}

func safeAPKFileName(fileName string) string {
	fileName = path.Base(strings.TrimSpace(fileName))
	if fileName == "." || fileName == "/" || !strings.HasSuffix(strings.ToLower(fileName), ".apk") {
		return "poolwatch-update.apk"
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r) {
			return r
		}
		return '-'
	}, fileName)
}

var singleRangePattern = regexp.MustCompile(`^bytes=(?:[0-9]+-[0-9]*|-[0-9]+)$`)
var contentRangePattern = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)
var unsatisfiedContentRangePattern = regexp.MustCompile(`^bytes \*/([0-9]+)$`)

func singleByteRange(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !singleRangePattern.MatchString(raw) {
		return "", errors.New("仅支持单段字节范围")
	}
	return raw, nil
}

// validatePartialContentRange 校验上游 206 响应，防止错误的范围响应污染断点续传文件。
func validatePartialContentRange(requested, contentRange string, contentLength, total int64) error {
	if requested == "" {
		return errors.New("上游返回了未请求的范围响应")
	}
	match := contentRangePattern.FindStringSubmatch(strings.TrimSpace(contentRange))
	if len(match) != 4 {
		return errors.New("上游范围响应格式无效")
	}
	start, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return errors.New("上游范围起点无效")
	}
	end, err := strconv.ParseInt(match[2], 10, 64)
	if err != nil {
		return errors.New("上游范围终点无效")
	}
	declaredTotal, err := strconv.ParseInt(match[3], 10, 64)
	if err != nil || declaredTotal != total || total <= 0 || start < 0 || start > end || end >= total {
		return errors.New("上游范围总长度无效")
	}
	if contentLength < 0 || contentLength != end-start+1 {
		return errors.New("上游范围长度与响应不一致")
	}
	expectedStart, expectedEnd, err := requestedByteRange(requested, total)
	if err != nil || start != expectedStart || end != expectedEnd {
		return errors.New("上游返回的范围与请求不一致")
	}
	return nil
}

func validateUnsatisfiedContentRange(contentRange string, total int64) error {
	match := unsatisfiedContentRangePattern.FindStringSubmatch(strings.TrimSpace(contentRange))
	if len(match) != 2 {
		return errors.New("上游 416 响应缺少有效范围总长度")
	}
	declaredTotal, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || declaredTotal != total {
		return errors.New("上游 416 响应总长度不一致")
	}
	return nil
}

func requestedByteRange(raw string, total int64) (int64, int64, error) {
	if total <= 0 {
		return 0, 0, errors.New("文件总长度无效")
	}
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "bytes=")
	parts := strings.SplitN(raw, "-", 2)
	if len(parts) != 2 {
		return 0, 0, errors.New("范围格式无效")
	}
	if parts[0] == "" {
		suffix, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, errors.New("后缀范围无效")
		}
		if suffix > total {
			suffix = total
		}
		return total - suffix, total - 1, nil
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= total {
		return 0, 0, errors.New("范围起点无效")
	}
	if parts[1] == "" {
		return start, total - 1, nil
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < start {
		return 0, 0, errors.New("范围终点无效")
	}
	if end >= total {
		end = total - 1
	}
	return start, end, nil
}

func safeIfRange(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > 200 || strings.ContainsAny(raw, "\r\n") {
		return "", errors.New("If-Range 内容过长")
	}
	if strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`) {
		return raw, nil
	}
	if _, err := http.ParseTime(raw); err == nil {
		return raw, nil
	}
	return "", errors.New("If-Range 必须是实体标签或 HTTP 日期")
}
