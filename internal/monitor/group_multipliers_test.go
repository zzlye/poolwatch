package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestNewAPI读取当前用户固定分组倍率(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"turnstile_check": false}})
	})
	mux.HandleFunc("/api/user/self/groups", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "manage-token" || request.Header.Get("New-Api-User") != "42" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{
			"default": map[string]any{"ratio": "1.0", "desc": "默认分组"},
			"vip":     map[string]any{"ratio": 0.5, "desc": "会员分组"},
			"auto":    map[string]any{"ratio": "自动", "desc": "自动选择"},
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	result, err := adapter.ReadGroupMultipliers(context.Background(), TargetConfig{
		ID: "new-groups", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "manage-token", UserID: "42"},
	})
	if err != nil {
		t.Fatalf("读取 New API 分组倍率失败：%v", err)
	}
	if len(result.Groups) != 2 {
		t.Fatalf("固定分组数量不正确：%#v", result.Groups)
	}
	if result.Groups[0].Key != "default" || result.Groups[0].Multiplier.String() != "1" || result.Groups[0].Description != "默认分组" {
		t.Fatalf("默认分组解析错误：%#v", result.Groups[0])
	}
	if result.Groups[1].Key != "vip" || result.Groups[1].Multiplier.String() != "0.5" {
		t.Fatalf("会员分组解析错误：%#v", result.Groups[1])
	}
}

func TestNewAPI旧版价格页倍率回退(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{}})
	})
	mux.HandleFunc("/api/user/self/groups", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/api/pricing", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Cookie") != "session=ready" || request.Header.Get("New-Api-User") != "7" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{
			"success": true, "group_ratio": map[string]any{"vip": "0.333333"},
			"usable_group": map[string]any{"vip": "旧版会员组"},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	result, err := adapter.ReadGroupMultipliers(context.Background(), TargetConfig{
		ID: "new-old", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{Cookie: "session=ready", UserID: "7"},
	})
	if err != nil || len(result.Groups) != 1 || result.Groups[0].Multiplier.String() != "0.333333" {
		t.Fatalf("旧版价格页倍率回退失败，result=%#v err=%v", result, err)
	}
}

func TestNewAPI旧版价格页拒绝访问令牌匿名回退(t *testing.T) {
	var pricingCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{}})
	})
	mux.HandleFunc("/api/user/self/groups", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/api/pricing", func(writer http.ResponseWriter, _ *http.Request) {
		pricingCalls.Add(1)
		writeTestJSON(writer, map[string]any{"success": true, "group_ratio": map[string]any{"default": 1}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	_, err := adapter.ReadGroupMultipliers(context.Background(), TargetConfig{
		ID: "new-old-token", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "token", UserID: "7"},
	})
	if err == nil || ErrorClassOf(err) != ErrorClassResponse || pricingCalls.Load() != 0 {
		t.Fatalf("访问令牌不应回退到匿名价格页，calls=%d err=%v", pricingCalls.Load(), err)
	}
}

func TestNewAPI仅跳过明确自动组并拒绝损坏倍率(t *testing.T) {
	for _, invalid := range []any{"损坏", -1, nil} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			_, err := parseNewAPIGroupMultipliers(map[string]any{
				"default": map[string]any{"ratio": 1},
				"broken":  map[string]any{"ratio": invalid},
				"auto":    map[string]any{"ratio": "自动"},
			})
			if err == nil || ErrorClassOf(err) != ErrorClassResponse {
				t.Fatalf("损坏倍率不应静默变成分组缺失：%v", err)
			}
		})
	}
}

func Test分组倍率拒绝超大科学计数法(t *testing.T) {
	for _, invalid := range []any{"1e100000000", "1e-100000000"} {
		if _, err := parseNewAPIGroupMultipliers(map[string]any{"default": map[string]any{"ratio": invalid}}); err == nil {
			t.Fatalf("超大科学计数倍率必须在展开前拒绝：%v", invalid)
		}
	}
}

func TestNewAPI用户标识拒绝超大科学计数法(t *testing.T) {
	if _, err := parsePositiveInt64String("1e100000000"); err == nil {
		t.Fatal("超大用户 ID 必须在展开前拒绝")
	}
	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	_, err := adapter.VerifyBrowserCredential(context.Background(), TargetConfig{
		BaseURL: "https://example.com", Credential: Credential{Cookie: "session=ready", UserID: "1e100000000"},
	})
	if err == nil || ErrorClassOf(err) != ErrorClassConfig {
		t.Fatalf("用户提交的超大 ID 应作为配置错误拒绝：%v", err)
	}
}

func TestSub2API合并用户专属分组倍率(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/groups/available", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": []any{
			map[string]any{"id": 10, "name": "基础组", "description": "公开", "rate_multiplier": "1.25"},
			map[string]any{"id": 11, "name": "订阅组", "rate_multiplier": "0.8"},
		}})
	})
	mux.HandleFunc("/api/v1/groups/rates", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"10": "0.75"}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	result, err := adapter.ReadGroupMultipliers(context.Background(), TargetConfig{
		ID: "sub-groups", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "access", RefreshToken: "refresh"},
	})
	if err != nil {
		t.Fatalf("读取 Sub2API 分组倍率失败：%v", err)
	}
	if len(result.Groups) != 2 || result.Groups[0].Key != "10" || result.Groups[0].Multiplier.String() != "0.75" {
		t.Fatalf("用户专属倍率未优先使用：%#v", result.Groups)
	}
	if result.Groups[1].Key != "11" || result.Groups[1].Multiplier.String() != "0.8" {
		t.Fatalf("默认倍率解析错误：%#v", result.Groups[1])
	}
	if result.CredentialUpdate == nil || result.CredentialUpdate.RefreshToken != "refresh" {
		t.Fatalf("当前令牌未返回调度层：%#v", result.CredentialUpdate)
	}
}

func TestSub2API高峰时段使用实际计费倍率(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/groups/available", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": []any{
			map[string]any{
				"id": 10, "name": "高峰订阅组", "subscription_type": "subscription",
				"rate_multiplier": "1.5", "peak_rate_enabled": true,
				"peak_start": "9:00", "peak_end": "11:00", "peak_rate_multiplier": "2",
			},
		}})
	})
	mux.HandleFunc("/api/v1/groups/rates", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"10": "0.75"}})
	})
	mux.HandleFunc("/api/v1/settings/public", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"server_timezone": "Asia/Shanghai", "server_utc_offset": "+08:00",
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	adapter.now = func() time.Time { return time.Date(2026, 7, 31, 2, 30, 0, 0, time.UTC) }
	result, err := adapter.ReadGroupMultipliers(context.Background(), TargetConfig{
		ID: "sub-peak", BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{AccessToken: "access"},
	})
	if err != nil || len(result.Groups) != 1 || result.Groups[0].Multiplier.String() != "1.5" {
		t.Fatalf("高峰实际倍率应为用户倍率乘高峰因子，result=%#v err=%v", result, err)
	}
}

func TestSub2API分组标识和倍率在展开前限制规模(t *testing.T) {
	for name, item := range map[string]map[string]any{
		"标识": {"id": "1e100000000", "name": "异常组", "rate_multiplier": 1},
		"倍率": {"id": 1, "name": "异常组", "rate_multiplier": "1e100000000"},
	} {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/groups/available", func(writer http.ResponseWriter, _ *http.Request) {
				writeTestJSON(writer, map[string]any{"code": 0, "data": []any{item}})
			})
			mux.HandleFunc("/api/v1/groups/rates", func(writer http.ResponseWriter, _ *http.Request) {
				writeTestJSON(writer, map[string]any{"code": 0, "data": nil})
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
			_, err := adapter.ReadGroupMultipliers(context.Background(), TargetConfig{
				ID: "sub-large", BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{AccessToken: "access"},
			})
			if err == nil || ErrorClassOf(err) != ErrorClassResponse {
				t.Fatalf("超大%s必须在展开前拒绝：%v", name, err)
			}
		})
	}
}

func TestSub2API旧版缺少专属倍率端点仍使用默认倍率(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/groups/available", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": []any{
			map[string]any{"id": 3, "name": "默认组", "rate_multiplier": "1.1250"},
		}})
	})
	mux.HandleFunc("/api/v1/groups/rates", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	result, err := adapter.ReadGroupMultipliers(context.Background(), TargetConfig{
		ID: "sub-old", BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{AccessToken: "access"},
	})
	if err != nil || len(result.Groups) != 1 || result.Groups[0].Multiplier.String() != "1.125" {
		t.Fatalf("旧版默认倍率兼容失败，result=%#v err=%v", result, err)
	}
}

func TestSub2API拒绝损坏或非正数的用户专属倍率(t *testing.T) {
	for _, invalid := range []any{"bad-rate", 0, -0.5} {
		t.Run(fmt.Sprint(invalid), func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/groups/available", func(writer http.ResponseWriter, _ *http.Request) {
				writeTestJSON(writer, map[string]any{"code": 0, "data": []any{
					map[string]any{"id": 8, "name": "测试组", "rate_multiplier": 1},
				}})
			})
			mux.HandleFunc("/api/v1/groups/rates", func(writer http.ResponseWriter, _ *http.Request) {
				writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"8": invalid}})
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
			_, err := adapter.ReadGroupMultipliers(context.Background(), TargetConfig{
				ID: "sub-invalid", BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{AccessToken: "access"},
			})
			if err == nil || ErrorClassOf(err) != ErrorClassResponse {
				t.Fatalf("无效专属倍率应拒绝：%v", err)
			}
		})
	}
}

func TestSub2API拒绝损坏分组条目而不是静默缺失(t *testing.T) {
	for _, broken := range []any{nil, "bad-item", map[string]any{"id": "bad", "name": "损坏组", "rate_multiplier": 1}} {
		t.Run(fmt.Sprint(broken), func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/v1/groups/available", func(writer http.ResponseWriter, _ *http.Request) {
				writeTestJSON(writer, map[string]any{"code": 0, "data": []any{
					map[string]any{"id": 1, "name": "正常组", "rate_multiplier": 1}, broken,
				}})
			})
			mux.HandleFunc("/api/v1/groups/rates", func(writer http.ResponseWriter, _ *http.Request) {
				writeTestJSON(writer, map[string]any{"code": 0, "data": nil})
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
			_, err := adapter.ReadGroupMultipliers(context.Background(), TargetConfig{
				ID: "sub-broken-item", BaseURL: server.URL, AllowPrivateNetwork: true, Credential: Credential{AccessToken: "access"},
			})
			if err == nil || ErrorClassOf(err) != ErrorClassResponse {
				t.Fatalf("损坏分组条目不应静默变成缺失：%v", err)
			}
		})
	}
}

func TestSub2API倍率读取遇到失效令牌会续期一次(t *testing.T) {
	var availableCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/groups/available", func(writer http.ResponseWriter, request *http.Request) {
		availableCalls.Add(1)
		if request.Header.Get("Authorization") != "Bearer renewed" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": []any{
			map[string]any{"id": 5, "name": "续期组", "rate_multiplier": "0.9"},
		}})
	})
	mux.HandleFunc("/api/v1/groups/rates", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer renewed" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": nil})
	})
	mux.HandleFunc("/api/v1/auth/refresh", func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["refresh_token"] != "refresh" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"access_token": "renewed", "refresh_token": "rotated", "expires_in": 3600,
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	result, err := adapter.ReadGroupMultipliers(context.Background(), TargetConfig{
		ID: "sub-renew", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "expired", RefreshToken: "refresh"},
	})
	if err != nil || availableCalls.Load() != 2 || result.CredentialUpdate == nil || result.CredentialUpdate.RefreshToken != "rotated" {
		t.Fatalf("倍率读取令牌续期失败，calls=%d result=%#v err=%v", availableCalls.Load(), result, err)
	}
}

type groupMultiplierTestAdapter struct {
	kind      TargetKind
	failClass ErrorClass
	failures  int32
	calls     atomic.Int32
}

func (adapter *groupMultiplierTestAdapter) Kind() TargetKind { return adapter.kind }

func (adapter *groupMultiplierTestAdapter) Check(context.Context, TargetConfig) (Snapshot, error) {
	return Snapshot{}, nil
}

func (adapter *groupMultiplierTestAdapter) ReadGroupMultipliers(context.Context, TargetConfig) (GroupMultiplierResult, error) {
	call := adapter.calls.Add(1)
	if call <= adapter.failures {
		return GroupMultiplierResult{}, checkError(adapter.failClass, "测试倍率读取", "测试失败", http.StatusBadGateway, nil)
	}
	return GroupMultiplierResult{Groups: []GroupMultiplier{{Key: "default", Name: "默认", Multiplier: decimalOne}}}, nil
}

var decimalOne = mustTestDecimal("1")

func mustTestDecimal(value string) decimal.Decimal {
	result, err := decimal.NewFromString(value)
	if err != nil {
		panic(err)
	}
	return result
}

func TestRegistry倍率读取只重试网络和服务错误(t *testing.T) {
	networkAdapter := &groupMultiplierTestAdapter{kind: TargetKindNewAPI, failClass: ErrorClassNetwork, failures: 2}
	registry := NewRegistry(HTTPOptions{})
	registry.Register(networkAdapter)
	if _, err := registry.ReadGroupMultipliers(context.Background(), TargetConfig{Kind: TargetKindNewAPI}); err != nil {
		t.Fatalf("网络错误重试后应成功：%v", err)
	}
	if networkAdapter.calls.Load() != 3 {
		t.Fatalf("网络错误应总共尝试三次，实际 %d", networkAdapter.calls.Load())
	}

	authAdapter := &groupMultiplierTestAdapter{kind: TargetKindSub2API, failClass: ErrorClassAuth, failures: 1}
	registry.Register(authAdapter)
	if _, err := registry.ReadGroupMultipliers(context.Background(), TargetConfig{Kind: TargetKindSub2API}); !IsAuthFailure(err) {
		t.Fatalf("认证错误应直接返回：%v", err)
	}
	if authAdapter.calls.Load() != 1 {
		t.Fatalf("认证错误不应重试，实际 %d", authAdapter.calls.Load())
	}
}
