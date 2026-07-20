package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCLIProxyAPI额度解析支持三类账号(t *testing.T) {
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	codex := parseCLIProxyAPICodexQuotaWindows(map[string]any{
		"rate_limit": map[string]any{
			"primary_window":   map[string]any{"used_percent": "25", "reset_after_seconds": 60},
			"secondary_window": map[string]any{"used_percent": 110, "reset_at": "2026-07-27T08:00:00Z"},
		},
	}, now)
	assertCLIProxyAPIQuotaWindow(t, codex, "code-5h", "75", "2026-07-20T08:01:00Z")
	assertCLIProxyAPIQuotaWindow(t, codex, "code-7d", "0", "2026-07-27T08:00:00Z")

	modernCodex := parseCLIProxyAPICodexQuotaWindows(map[string]any{
		"rate_limit": map[string]any{
			"primary_window": map[string]any{
				"used_percent": 10, "limit_window_seconds": 18_000,
			},
			"secondary_window": map[string]any{
				"used_percent": 20, "limit_window_seconds": 2_592_000,
			},
		},
		"code_review_rate_limit": map[string]any{
			"primary_window": map[string]any{
				"used_percent": 30, "limit_window_seconds": 604_800,
			},
		},
	}, now)
	assertCLIProxyAPIQuotaWindowLabel(t, modernCodex, "code-5h", "5 小时", "90")
	assertCLIProxyAPIQuotaWindowLabel(t, modernCodex, "code-30d", "30 天", "80")
	assertCLIProxyAPIQuotaWindowLabel(t, modernCodex, "review-7d", "代码审查 7 天", "70")

	flatReview := parseCLIProxyAPICodexQuotaWindows(map[string]any{
		"code_review_rate_limit": map[string]any{
			"used_percent": 12, "limit_window_seconds": 604_800,
		},
	}, now)
	assertCLIProxyAPIQuotaWindowLabel(t, flatReview, "review-7d", "代码审查 7 天", "88")

	gemini := parseCLIProxyAPIGeminiQuotaWindows(map[string]any{
		"buckets": []any{
			map[string]any{"modelId": "gemini-2.5-pro", "remainingFraction": "0.425", "resetTime": "2026-07-20T09:00:00Z"},
		},
	})
	assertCLIProxyAPIQuotaWindow(t, gemini, "gemini-2.5-pro", "42.5", "2026-07-20T09:00:00Z")

	antigravity := parseCLIProxyAPIAntigravityQuotaWindows(map[string]any{
		"models": map[string]any{
			"gemini-2.5-flash": map[string]any{
				"displayName": "Gemini 2.5 Flash",
				"quotaInfo":   map[string]any{"remainingFraction": 0.8, "resetTime": int64(1784538000)},
			},
		},
	})
	assertCLIProxyAPIQuotaWindow(t, antigravity, "gemini-2.5-flash", "80", "2026-07-20T09:00:00Z")

	antigravitySummary := parseCLIProxyAPIAntigravityQuotaSummaryWindows(map[string]any{
		"groups": []any{map[string]any{
			"displayName": "Gemini 模型",
			"buckets": []any{map[string]any{
				"bucketId": "weekly", "displayName": "每周额度",
				"remainingFraction": "0.65", "resetTime": "2026-07-27T08:00:00Z",
			}},
		}},
	})
	assertCLIProxyAPIQuotaWindowLabel(t, antigravitySummary, "summary-1-weekly", "Gemini 模型 · 每周额度", "65")
}

func TestCLIProxyAPI额度缓存失败隔离与不支持状态(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/management/auth-files", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"files": []any{
			map[string]any{
				"auth_index": "codex-good", "provider": "codex", "status": "active",
				"id_token": map[string]any{"chatgpt_account_id": "account-good"},
			},
			map[string]any{
				"auth_index": "codex-failed", "provider": "codex", "status": "active",
				"id_token": map[string]any{"chatgpt_account_id": "account-failed"},
			},
			map[string]any{"auth_index": "claude-no-quota", "provider": "claude", "status": "active"},
		}})
	})
	mux.HandleFunc("/v0/management/api-call", func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("Authorization") != "Bearer management-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		var call map[string]any
		if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if call["auth_index"] == "codex-failed" {
			writeTestJSON(writer, map[string]any{"status_code": 503, "body": `{"message":"temporary"}`})
			return
		}
		writeTestJSON(writer, map[string]any{
			"status_code": 200,
			"body":        `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":25,"reset_at":"2026-07-20T09:00:00Z"}}}`,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	clock := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	adapter := newCLIProxyAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	adapter.quotaCache.now = func() time.Time { return clock }
	target := TargetConfig{
		ID: "cli-cache", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AdminKey: "management-secret"},
	}
	first, err := adapter.Check(context.Background(), target)
	if err != nil {
		t.Fatalf("首次检测 CLIProxyAPI 失败：%v", err)
	}
	good := findCLIProxyAPIAccount(t, first.Accounts, "codex-good")
	failed := findCLIProxyAPIAccount(t, first.Accounts, "codex-failed")
	unsupported := findCLIProxyAPIAccount(t, first.Accounts, "claude-no-quota")
	if first.Status != TargetStatusHealthy || good.QuotaState != AccountQuotaStateAvailable || good.Type != "plus" {
		t.Fatalf("成功额度不应影响账号健康状态，账号=%#v 渠道状态=%s", good, first.Status)
	}
	assertCLIProxyAPIQuotaWindow(t, good.QuotaWindows, "code-5h", "75", "2026-07-20T09:00:00Z")
	if failed.Status != string(TargetStatusHealthy) || failed.QuotaState != AccountQuotaStateUnavailable {
		t.Fatalf("额度读取失败不应改变账号健康状态：%#v", failed)
	}
	if unsupported.QuotaState != AccountQuotaStateUnsupported {
		t.Fatalf("无额度接口的账号状态不正确：%#v", unsupported)
	}
	if calls.Load() != 2 {
		t.Fatalf("只应查询支持额度接口的两个账号，实际调用 %d 次", calls.Load())
	}

	if _, err := adapter.Check(context.Background(), target); err != nil {
		t.Fatalf("缓存期内再次检测失败：%v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("成功额度应命中缓存，失败额度应允许重试，实际调用 %d 次", calls.Load())
	}

	clock = clock.Add(16 * time.Minute)
	if _, err := adapter.Check(context.Background(), target); err != nil {
		t.Fatalf("缓存过期后再次检测失败：%v", err)
	}
	if calls.Load() != 5 {
		t.Fatalf("十五分钟缓存过期后应重新读取额度，实际调用 %d 次", calls.Load())
	}
}

func TestCLIProxyAPIGoogle额度请求体符合上游协议(t *testing.T) {
	var geminiData string
	var antigravityData string
	var antigravityAssistCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/management/api-call", func(writer http.ResponseWriter, request *http.Request) {
		var call map[string]any
		if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		authIndex, _ := call["auth_index"].(string)
		switch call["url"] {
		case cliProxyAPIGeminiQuotaURL:
			geminiData, _ = call["data"].(string)
			writeTestJSON(writer, map[string]any{
				"status_code": 200,
				"body":        `{"buckets":[{"modelId":"gemini-2.5-pro","remainingFraction":0.5}]}`,
			})
		case cliProxyAPIGeminiLoadAssistURL:
			if authIndex == "antigravity-auth" {
				antigravityAssistCalls.Add(1)
				writeTestJSON(writer, map[string]any{
					"status_code": 200,
					"body":        `{"currentTier":{"id":"free-tier"},"paidTier":{"id":"g1-pro-tier"}}`,
				})
				return
			}
			// 套餐补充失败不应阻断已经明确的额度端点。
			writeTestJSON(writer, map[string]any{"status_code": 503, "body": `{"message":"temporary"}`})
		case cliProxyAPIAntigravityDailySummaryURL:
			antigravityData, _ = call["data"].(string)
			writeTestJSON(writer, map[string]any{
				"status_code": 200,
				"body": `{"groups":[{"displayName":"Gemini models","buckets":[` +
					`{"bucketId":"weekly","displayName":"Weekly limit","remainingFraction":0.75}` +
					`]}]}`,
			})
		default:
			writeTestJSON(writer, map[string]any{"status_code": 404, "body": `{}`})
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newCLIProxyAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	session := adapter.http.newSession(true)
	target := TargetConfig{BaseURL: server.URL, AllowPrivateNetwork: true}
	gemini, ok := adapter.queryCLIProxyAPIGeminiQuota(context.Background(), session, target, "management-secret", map[string]any{
		"auth_index": "gemini-auth", "project_id": "projects/gemini-workspace",
	})
	if !ok || len(gemini.Windows) != 1 {
		t.Fatalf("Gemini 额度请求失败：%#v", gemini)
	}
	var geminiBody map[string]any
	if err := json.Unmarshal([]byte(geminiData), &geminiBody); err != nil || len(geminiBody) != 1 ||
		geminiBody["project"] != "projects/gemini-workspace" {
		t.Fatalf("Gemini retrieveUserQuota 请求体不正确：%q, %v", geminiData, err)
	}

	antigravity, ok := adapter.queryCLIProxyAPIAntigravityQuota(context.Background(), session, target, "management-secret", map[string]any{
		"auth_index": "antigravity-auth", "project_id": "projects/antigravity-workspace",
	})
	if !ok || len(antigravity.Windows) != 1 || antigravity.PlanType != "pro" {
		t.Fatalf("Antigravity 额度请求失败：%#v", antigravity)
	}
	var antigravityBody map[string]any
	if err := json.Unmarshal([]byte(antigravityData), &antigravityBody); err != nil || len(antigravityBody) != 1 ||
		antigravityBody["project"] != "projects/antigravity-workspace" {
		t.Fatalf("Antigravity 额度摘要请求体不正确：%q, %v", antigravityData, err)
	}
	if antigravityAssistCalls.Load() != 1 {
		t.Fatalf("带项目标识的 Antigravity 账号仍应补充套餐，实际调用 %d 次", antigravityAssistCalls.Load())
	}
}

func TestCLIProxyAPIAntigravity旧额度回退携带项目(t *testing.T) {
	var fallbackData string
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/management/api-call", func(writer http.ResponseWriter, request *http.Request) {
		var call map[string]any
		if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		switch call["url"] {
		case cliProxyAPIAntigravityDailySummaryURL, cliProxyAPIAntigravitySandboxSummaryURL, cliProxyAPIAntigravitySummaryURL:
			writeTestJSON(writer, map[string]any{"status_code": 404, "body": `{}`})
		case cliProxyAPIAntigravityModelsURL:
			fallbackData, _ = call["data"].(string)
			writeTestJSON(writer, map[string]any{
				"status_code": 200,
				"body":        `{"models":{"gemini-2.5-pro":{"quotaInfo":{"remainingFraction":0.75}}}}`,
			})
		case cliProxyAPIGeminiLoadAssistURL:
			writeTestJSON(writer, map[string]any{"status_code": 503, "body": `{}`})
		default:
			writeTestJSON(writer, map[string]any{"status_code": 404, "body": `{}`})
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newCLIProxyAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	result, ok := adapter.queryCLIProxyAPIAntigravityQuota(
		context.Background(), adapter.http.newSession(true),
		TargetConfig{BaseURL: server.URL, AllowPrivateNetwork: true}, "management-secret",
		map[string]any{"auth_index": "antigravity-fallback", "project_id": "projects/fallback"},
	)
	if !ok || len(result.Windows) != 1 {
		t.Fatalf("Antigravity 旧额度接口回退失败：%#v", result)
	}
	var fallbackBody map[string]any
	if err := json.Unmarshal([]byte(fallbackData), &fallbackBody); err != nil || fallbackBody["project"] != "projects/fallback" {
		t.Fatalf("Antigravity 旧额度接口未携带项目标识：%q, %v", fallbackData, err)
	}
}

func TestCLIProxyAPIAntigravity套餐标识规范化(t *testing.T) {
	cases := map[string]string{
		"free-tier":          "free",
		"g1-pro-tier":        "pro",
		"g1-ultra-tier":      "ultra",
		"g1-ultra-lite-tier": "ultra-lite",
		"future-tier":        "future-tier",
	}
	for input, expected := range cases {
		if actual := normalizeCLIProxyAPIGooglePlanType(input); actual != expected {
			t.Fatalf("套餐标识 %q 规范化结果不正确：%q", input, actual)
		}
	}
}

func TestCLIProxyAPI额度查询最多四并发(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	var calls atomic.Int32
	accounts := make([]any, 0, 9)
	for index := 0; index < 9; index++ {
		accounts = append(accounts, map[string]any{
			"auth_index": "codex-" + string(rune('a'+index)), "provider": "codex", "status": "active",
			"id_token": map[string]any{"chatgpt_account_id": "account-" + string(rune('a'+index))},
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/management/auth-files", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"files": accounts})
	})
	mux.HandleFunc("/v0/management/api-call", func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		defer active.Add(-1)
		time.Sleep(40 * time.Millisecond)
		writeTestJSON(writer, map[string]any{
			"status_code": 200,
			"body":        `{"rate_limit":{"primary_window":{"used_percent":10}}}`,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newCLIProxyAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	_, err := adapter.Check(context.Background(), TargetConfig{
		ID: "cli-concurrency", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AdminKey: "management-secret"},
	})
	if err != nil {
		t.Fatalf("并发额度检测失败：%v", err)
	}
	if calls.Load() != 9 {
		t.Fatalf("额度接口调用次数不正确：%d", calls.Load())
	}
	if maximum.Load() > cliProxyAPIQuotaWorkerCount || maximum.Load() < 2 {
		t.Fatalf("额度查询并发数应在 2 至 %d 之间，实际为 %d", cliProxyAPIQuotaWorkerCount, maximum.Load())
	}
}

func TestCLIProxyAPI慢账号不会长期占用全部额度查询工位(t *testing.T) {
	var calls atomic.Int32
	accounts := make([]any, 0, 8)
	for index := 0; index < 8; index++ {
		accounts = append(accounts, map[string]any{
			"auth_index": "codex-" + string(rune('a'+index)), "provider": "codex", "status": "active",
			"id_token": map[string]any{"chatgpt_account_id": "account-" + string(rune('a'+index))},
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/management/auth-files", func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, map[string]any{"files": accounts})
	})
	mux.HandleFunc("/v0/management/api-call", func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		var call map[string]any
		if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		authIndex, _ := call["auth_index"].(string)
		if authIndex >= "codex-a" && authIndex <= "codex-d" {
			select {
			case <-request.Context().Done():
				return
			case <-time.After(time.Second):
			}
		}
		writeTestJSON(writer, map[string]any{
			"status_code": 200,
			"body":        `{"rate_limit":{"primary_window":{"used_percent":10}}}`,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newCLIProxyAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	adapter.quotaRequestTimeout = 80 * time.Millisecond
	snapshot, err := adapter.Check(context.Background(), TargetConfig{
		ID: "cli-slow", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AdminKey: "management-secret"},
	})
	if err != nil {
		t.Fatalf("慢账号隔离检测失败：%v", err)
	}
	if calls.Load() != 8 {
		t.Fatalf("慢账号不应阻止后续账号开始查询，实际调用 %d 次", calls.Load())
	}
	for _, externalID := range []string{"codex-e", "codex-f", "codex-g", "codex-h"} {
		account := findCLIProxyAPIAccount(t, snapshot.Accounts, externalID)
		if account.QuotaState != AccountQuotaStateAvailable {
			t.Fatalf("后续账号应正常取得额度：%#v", account)
		}
	}
}

func assertCLIProxyAPIQuotaWindow(t *testing.T, windows []AccountQuotaWindow, key, remaining, resetAt string) {
	t.Helper()
	for _, window := range windows {
		if window.Key != key {
			continue
		}
		if window.RemainingPercent == nil || window.RemainingPercent.String() != remaining || window.ResetAt != resetAt {
			t.Fatalf("额度窗口 %s 不符合预期：%#v", key, window)
		}
		return
	}
	t.Fatalf("缺少额度窗口：%s，全部窗口=%#v", key, windows)
}

func assertCLIProxyAPIQuotaWindowLabel(t *testing.T, windows []AccountQuotaWindow, key, label, remaining string) {
	t.Helper()
	for _, window := range windows {
		if window.Key != key {
			continue
		}
		if window.Label != label || window.RemainingPercent == nil || window.RemainingPercent.String() != remaining {
			t.Fatalf("额度窗口 %s 的标签或比例不符合预期：%#v", key, window)
		}
		return
	}
	t.Fatalf("缺少额度窗口：%s，全部窗口=%#v", key, windows)
}

func findCLIProxyAPIAccount(t *testing.T, accounts []AccountStatus, externalID string) AccountStatus {
	t.Helper()
	for _, account := range accounts {
		if account.ExternalID == externalID {
			return account
		}
	}
	t.Fatalf("缺少 CLIProxyAPI 账号：%s", externalID)
	return AccountStatus{}
}
