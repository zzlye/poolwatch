package monitor

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

func TestNewAPI展示所选分组全部可靠价格并跳过损坏模型(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{}})
	})
	mux.HandleFunc("/api/user/self/groups", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "manage-token" || request.Header.Get("New-Api-User") != "42" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{
			"vip": map[string]any{"ratio": "0.5", "desc": "会员分组"},
		}})
	})
	mux.HandleFunc("/api/pricing", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": []any{
			map[string]any{
				"model_name": "gpt-token", "quota_type": 0, "enable_groups": []string{"vip"},
				"model_ratio": "2.5", "completion_ratio": "4", "cache_ratio": "0.1", "create_cache_ratio": "0.25",
				"image_ratio": "2", "audio_ratio": "3", "audio_completion_ratio": "4",
			},
			map[string]any{
				"model_name": "image-request", "quota_type": 1, "enable_groups": []string{"all"}, "model_price": "0.04",
			},
			map[string]any{
				"model_name": "video-second", "quota_type": 2, "price_unit": "second", "enable_groups": []string{"vip"},
				"model_price": "0.3", "video_per_second_pricing": map[string]any{
					"prices": map[string]any{"720p": "0.3", "1080p": "0.55"},
				},
			},
			map[string]any{
				"model_name": "broken", "quota_type": 0, "enable_groups": []string{"vip"},
				"model_ratio": "1e999999", "completion_ratio": "1",
			},
			map[string]any{
				"model_name": "other-group", "quota_type": 1, "enable_groups": []string{"default"}, "model_price": "10",
			},
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	result, err := adapter.ReadGroupPrices(context.Background(), TargetConfig{
		ID: "new-price", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "manage-token", UserID: "42"},
	}, "vip")
	if err != nil {
		t.Fatalf("读取 New API 价格失败：%v", err)
	}
	if result.Catalog.Multiplier.String() != "0.5" || len(result.Catalog.Models) != 3 {
		t.Fatalf("价格目录基础信息不正确：%#v", result.Catalog)
	}
	if !strings.Contains(result.Catalog.Notice, "1 个模型价格无法识别") {
		t.Fatalf("损坏模型提示缺失：%q", result.Catalog.Notice)
	}
	byName := make(map[string]GroupModelPrice)
	for _, model := range result.Catalog.Models {
		byName[model.Name] = model
	}
	assertPriceValues(t, byName["gpt-token"].Prices, map[string]string{
		"input": "2.5", "output": "10", "cache_read": "0.25", "cache_write": "0.625",
		"image_input": "5", "audio_input": "7.5", "audio_output": "30",
	})
	assertPriceValues(t, byName["image-request"].Prices, map[string]string{"per_request": "0.02"})
	video := byName["video-second"]
	if video.BillingMode != "per_second" || len(video.Prices) != 1 || video.Prices[0].Value.String() != "0.15" || video.Prices[0].Unit != "USD/秒" || len(video.Intervals) != 2 {
		t.Fatalf("New API 按秒视频价格未进入分组目录：%#v", video)
	}
}

func TestNewAPI分组价格跟随站点货币单位(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{
			"quota_display_type": "CNY", "quota_per_unit": 500000, "usd_exchange_rate": "7",
		}})
	})
	mux.HandleFunc("/api/user/self/groups", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{
			"default": map[string]any{"ratio": "1", "desc": "默认分组"},
		}})
	})
	mux.HandleFunc("/api/pricing", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": []any{
			map[string]any{
				"model_name": "token-model", "quota_type": 0, "enable_groups": []string{"default"},
				"model_ratio": "1", "completion_ratio": "2", "cache_ratio": "0.1",
			},
			map[string]any{
				"model_name": "request-model", "quota_type": 1, "enable_groups": []string{"default"},
				"model_price": "0.04",
			},
		}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	result, err := adapter.ReadGroupPrices(context.Background(), TargetConfig{
		ID: "new-cny-price", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "manage-token", UserID: "42"},
	}, "default")
	if err != nil {
		t.Fatalf("读取 CNY 价格失败：%v", err)
	}
	byName := make(map[string]GroupModelPrice)
	for _, model := range result.Catalog.Models {
		byName[model.Name] = model
	}
	assertPriceValues(t, byName["token-model"].Prices, map[string]string{
		"input": "14", "output": "28", "cache_read": "1.4",
	})
	for _, item := range byName["token-model"].Prices {
		if item.Unit != "CNY/百万令牌" {
			t.Fatalf("Token 价格单位没有跟随站点货币：%#v", byName["token-model"].Prices)
		}
	}
	requestPrice := byName["request-model"].Prices
	if len(requestPrice) != 1 || requestPrice[0].Value.String() != "0.28" || requestPrice[0].Unit != "CNY/次" {
		t.Fatalf("按次价格没有跟随站点货币：%#v", requestPrice)
	}
}

func TestNewAPI价格单位转换覆盖站点显示模式(t *testing.T) {
	tests := []struct {
		name         string
		display      newAPIQuotaDisplay
		expected     string
		expectedUnit string
	}{
		{name: "美元", display: newAPIQuotaDisplay{displayType: "USD", exchangeRate: decimalOne, quotaPerUnit: decimal.NewFromInt(500000), unit: "USD"}, expected: "2", expectedUnit: "USD/百万令牌"},
		{name: "自定义货币", display: newAPIQuotaDisplay{displayType: "CUSTOM", exchangeRate: mustPriceDecimal(t, "0.034"), quotaPerUnit: decimal.NewFromInt(500000), unit: "HUHN"}, expected: "0.068", expectedUnit: "HUHN/百万令牌"},
		{name: "额度令牌", display: newAPIQuotaDisplay{displayType: "TOKENS", exchangeRate: decimalOne, quotaPerUnit: decimal.NewFromInt(500000), unit: "tokens"}, expected: "1000000", expectedUnit: "tokens/百万令牌"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := GroupPriceCatalog{Models: []GroupModelPrice{{
				Name: "test-model", Prices: []GroupPriceItem{{Key: "input", Label: "输入", Value: decimal.NewFromInt(2), Unit: "USD/百万令牌"}},
			}}}
			if err := applyNewAPIPriceDisplay(&catalog, test.display); err != nil {
				t.Fatalf("转换站点价格失败：%v", err)
			}
			item := catalog.Models[0].Prices[0]
			if item.Value.String() != test.expected || item.Unit != test.expectedUnit {
				t.Fatalf("站点价格转换不正确：value=%s unit=%s", item.Value.String(), item.Unit)
			}
		})
	}
}

func TestNewAPI严格解析阶梯价格但不执行请求规则(t *testing.T) {
	row := map[string]any{
		"quota_type": 0, "billing_mode": "tiered_expr",
		"billing_expr": `v1:len <= 200000 ? tier("标准", p * 3 + c * 15 + cr * 0.3 + cc * 3.75 + cc1h * 6 + img * 2 + img_o * 4 + ai * 1 + ao * 5) : tier("长上下文", p * 6 + c * 22.5)|||when(header("x-fast") has "1") * 2`,
	}
	model, err := parseNewAPIModelPrice(row, mustPriceDecimal(t, "0.5"))
	if err != nil {
		t.Fatalf("解析 New API 阶梯价格失败：%v", err)
	}
	if model.BillingMode != "tiered" || len(model.Intervals) != 2 || !strings.Contains(model.Note, "请求参数或时间") {
		t.Fatalf("阶梯价格结构不正确：%#v", model)
	}
	if model.Intervals[0].MinTokens != "0" || model.Intervals[0].MaxTokens != "200000" ||
		model.Intervals[1].MinTokens != "200001" || model.Intervals[1].MaxTokens != "" {
		t.Fatalf("阶梯 Token 边界不正确：%#v", model.Intervals)
	}
	assertPriceValues(t, model.Intervals[0].Prices, map[string]string{
		"input": "1.5", "output": "7.5", "cache_read": "0.15", "cache_write": "1.875",
		"cache_write_1h": "3", "image_input": "1", "image_output": "2", "audio_input": "0.5", "audio_output": "2.5",
	})
}

func TestNewAPI按秒视频价格支持分辨率明细(t *testing.T) {
	model, err := parseNewAPIModelPrice(map[string]any{
		"quota_type":  2,
		"model_price": 0.3,
		"price_unit":  "second",
		"video_per_second_pricing": map[string]any{
			"default_resolution": "720p",
			"prices":             map[string]any{"480p": 0.25, "720p": 0.3, "1080p": 0.55},
		},
	}, mustPriceDecimal(t, "0.5"))
	if err != nil {
		t.Fatalf("解析按秒视频价格失败：%v", err)
	}
	if model.BillingMode != "per_second" || len(model.Prices) != 1 || model.Prices[0].Key != "per_second" || model.Prices[0].Value.String() != "0.15" || model.Prices[0].Unit != "USD/秒" {
		t.Fatalf("按秒基础价格结构不正确：%#v", model)
	}
	if len(model.Intervals) != 3 {
		t.Fatalf("按秒分辨率价格数量不正确：%#v", model.Intervals)
	}
	for _, interval := range model.Intervals {
		if len(interval.Prices) != 1 || interval.Prices[0].Key != "per_second" || interval.Prices[0].Unit != "USD/秒" {
			t.Fatalf("按秒分辨率价格结构不正确：%#v", model.Intervals)
		}
	}
}

func TestNewAPI非上下文阶梯只返回条件说明(t *testing.T) {
	conditional, err := parseNewAPIModelPrice(map[string]any{
		"quota_type": 0, "billing_mode": "tiered_expr",
		"billing_expr": `v1:p > 1000 ? tier("大输入", p * 2) : tier("普通", p * 1)`,
	}, decimalOne)
	if err != nil || len(conditional.Intervals) != 2 {
		t.Fatalf("解析 New API 输入条件阶梯失败：model=%#v err=%v", conditional, err)
	}
	for _, interval := range conditional.Intervals {
		if interval.MinTokens != "" || interval.MaxTokens != "" || !strings.Contains(interval.Condition, "输入 Token") {
			t.Fatalf("输入条件不应伪装成上下文 Token 边界：%#v", interval)
		}
	}
	unconditional, err := parseNewAPIModelPrice(map[string]any{
		"quota_type": 0, "billing_mode": "tiered_expr", "billing_expr": `tier("统一", p * 1 + c * 2)`,
	}, decimalOne)
	if err != nil || len(unconditional.Intervals) != 1 {
		t.Fatalf("解析 New API 无条件阶梯失败：model=%#v err=%v", unconditional, err)
	}
	interval := unconditional.Intervals[0]
	if interval.MinTokens != "" || interval.MaxTokens != "" || interval.Condition != "" {
		t.Fatalf("无条件阶梯不应返回虚构边界或条件：%#v", interval)
	}
}

func TestNewAPI价格页关闭返回未公开而不是登录失效(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{}})
	})
	mux.HandleFunc("/api/user/self/groups", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"success": true, "data": map[string]any{"vip": map[string]any{"ratio": 1}}})
	})
	mux.HandleFunc("/api/pricing", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newNewAPIAdapter(newSecureHTTPClient(HTTPOptions{}))
	_, err := adapter.ReadGroupPrices(context.Background(), TargetConfig{
		ID: "new-disabled", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "token", UserID: "1"},
	}, "vip")
	if err == nil || ErrorClassOf(err) != ErrorClassRemote || !strings.Contains(err.Error(), "未公开") {
		t.Fatalf("关闭价格页应返回安全的未公开错误：%v", err)
	}
}

func TestSub2API展示站点实际价格阶梯和图片价格(t *testing.T) {
	mux := sub2APIPriceBaseMux()
	mux.HandleFunc("/api/v1/model-plaza", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"groups": []any{
			map[string]any{"id": 10, "name": "基础组", "models": []any{
				map[string]any{"name": "token-model", "pricing": map[string]any{
					"billing_mode": "token", "input_price": "0.000002", "output_price": "0.000004",
					"cache_write_price": "0.000001", "cache_read_price": "0.0000005",
					"image_input_price": "0.000003", "image_output_price": "0.000006",
					"intervals": []any{map[string]any{
						"min_tokens": 0, "max_tokens": 200000, "tier_label": "标准",
						"input_price": "0.000001", "output_price": "0.000003",
					}},
				}, "official_pricing": map[string]any{"input_price": "999"}},
				map[string]any{"name": "image-model", "pricing": map[string]any{
					"billing_mode": "image", "per_request_price": "0.12", "intervals": []any{
						map[string]any{"tier_label": "1K", "per_request_price": "0.1"},
						map[string]any{"tier_label": "2K", "per_request_price": "0.2"},
					},
				}},
				map[string]any{"name": "broken", "pricing": map[string]any{
					"billing_mode": "token", "input_price": "1e999999",
				}},
			}},
		}}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	result, err := adapter.ReadGroupPrices(context.Background(), TargetConfig{
		ID: "sub-price", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "access", RefreshToken: "refresh"},
	}, "10")
	if err != nil {
		t.Fatalf("读取 Sub2API 价格失败：%v", err)
	}
	if result.Catalog.Multiplier.String() != "0.5" || result.CredentialUpdate == nil || len(result.Catalog.Models) != 2 {
		t.Fatalf("Sub2API 价格目录不正确：%#v", result)
	}
	if !strings.Contains(result.Catalog.Notice, "1 个模型价格无法识别") {
		t.Fatalf("Sub2API 损坏模型提示缺失：%q", result.Catalog.Notice)
	}
	byName := make(map[string]GroupModelPrice)
	for _, model := range result.Catalog.Models {
		byName[model.Name] = model
	}
	token := byName["token-model"]
	assertPriceValues(t, token.Prices, map[string]string{
		"input": "1", "output": "2", "cache_write": "0.5", "cache_read": "0.25",
		"cache_write_1h": "0.5", "image_input": "1.5", "image_output": "3",
	})
	for _, item := range token.Prices {
		if !strings.HasPrefix(item.Unit, "USD/") {
			t.Fatalf("Sub2API 官方价格应保持 USD：%#v", token.Prices)
		}
	}
	if len(token.Intervals) != 1 || token.Intervals[0].MinTokens != "1" || token.Intervals[0].MaxTokens != "200000" {
		t.Fatalf("Sub2API 阶梯边界不正确：%#v", token.Intervals)
	}
	assertPriceValues(t, token.Intervals[0].Prices, map[string]string{"input": "0.5", "output": "1.5"})
	image := byName["image-model"]
	if len(image.Prices) != 1 || image.Prices[0].Label != "按图片" || image.Prices[0].Unit != "USD/张" || image.Prices[0].Value.String() != "0.06" {
		t.Fatalf("Sub2API 图片价格不正确：%#v", image.Prices)
	}
	if len(image.Intervals) != 3 {
		t.Fatalf("Sub2API 分组图片覆盖价没有补齐尺寸：%#v", image.Intervals)
	}
	bySize := make(map[string]string)
	for _, interval := range image.Intervals {
		bySize[interval.Label] = interval.Prices[0].Value.String()
	}
	if bySize["1K"] != "0.1" || bySize["2K"] != "0.1" || bySize["4K"] != "0.4" {
		t.Fatalf("分组图片覆盖价优先级或渠道回退价格不正确：%#v", bySize)
	}
}

func TestSub2API模型广场关闭时回退可用渠道(t *testing.T) {
	mux := sub2APIPriceBaseMux()
	mux.HandleFunc("/api/v1/model-plaza", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/api/v1/channels/available", func(writer http.ResponseWriter, _ *http.Request) {
		model := map[string]any{"name": "fallback-model", "pricing": map[string]any{
			"billing_mode": "token", "input_price": "0.000002", "output_price": "0.000004", "intervals": []any{},
		}}
		platform := map[string]any{
			"platform": "openai", "groups": []any{map[string]any{"id": 10}}, "supported_models": []any{model},
		}
		channel := map[string]any{"name": "公开渠道", "platforms": []any{platform}}
		writeTestJSON(writer, map[string]any{"code": 0, "data": []any{channel}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	result, err := adapter.ReadGroupPrices(context.Background(), TargetConfig{
		ID: "sub-fallback", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "access"},
	}, "10")
	if err != nil || len(result.Catalog.Models) != 1 || !strings.Contains(result.Catalog.Notice, "可用渠道列表") {
		t.Fatalf("Sub2API 可用渠道价格回退失败，result=%#v err=%v", result, err)
	}
}

func TestSub2API价格读取续期令牌并乘入当前高峰倍率(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/groups/available", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer renewed-access" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, map[string]any{"code": 0, "data": []any{map[string]any{
			"id": 10, "name": "高峰组", "subscription_type": "subscription", "rate_multiplier": "0.8",
			"peak_rate_enabled": true, "peak_start": "08:00", "peak_end": "18:00", "peak_rate_multiplier": "3",
			"image_rate_independent": true, "image_rate_multiplier": "0.5",
		}}})
	})
	mux.HandleFunc("/api/v1/groups/rates", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"10": "0.8"}})
	})
	mux.HandleFunc("/api/v1/settings/public", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"server_timezone": "UTC", "server_utc_offset": "+00:00",
		}})
	})
	mux.HandleFunc("/api/v1/auth/refresh", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{
			"access_token": "renewed-access", "refresh_token": "renewed-refresh", "expires_in": 3600,
		}})
	})
	mux.HandleFunc("/api/v1/model-plaza", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer renewed-access" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		model := map[string]any{"name": "peak-model", "pricing": map[string]any{
			"billing_mode": "token", "input_price": "0.000002", "intervals": []any{},
		}}
		imageModel := map[string]any{"name": "image-model", "pricing": map[string]any{
			"billing_mode": "image", "per_request_price": "0.12", "intervals": []any{map[string]any{
				"tier_label": "高清", "min_tokens": "不是 Token", "per_request_price": "0.2",
			}},
		}}
		requestModel := map[string]any{"name": "request-model", "pricing": map[string]any{
			"billing_mode": "per_request", "per_request_price": "0.12", "intervals": []any{map[string]any{
				"tier_label": "批量", "per_request_price": "0.1",
			}},
		}}
		group := map[string]any{"id": 10, "models": []any{model, imageModel, requestModel}}
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"groups": []any{group}}})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	adapter := newSub2APIAdapter(newSecureHTTPClient(HTTPOptions{}))
	adapter.now = func() time.Time { return time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC) }
	result, err := adapter.ReadGroupPrices(context.Background(), TargetConfig{
		ID: "sub-renew-price", BaseURL: server.URL, AllowPrivateNetwork: true,
		Credential: Credential{AccessToken: "expired-access", RefreshToken: "old-refresh"},
	}, "10")
	if err != nil {
		t.Fatalf("Sub2API 续期后读取价格失败：%v", err)
	}
	if result.CredentialUpdate == nil || result.CredentialUpdate.AccessToken != "renewed-access" || result.CredentialUpdate.RefreshToken != "renewed-refresh" {
		t.Fatalf("Sub2API 续期凭据不正确：%#v", result.CredentialUpdate)
	}
	// 基础或用户倍率 0.8 × 当前高峰倍率 3 = Token 和普通按次的实际倍率 2.4。
	if result.Catalog.Multiplier.String() != "2.4" || !strings.Contains(result.Catalog.Notice, "图片不叠加高峰倍率") ||
		!strings.Contains(result.Catalog.Notice, "独立倍率 0.5") {
		t.Fatalf("Sub2API 当前高峰价格不正确：%#v", result.Catalog)
	}
	byName := make(map[string]GroupModelPrice)
	for _, model := range result.Catalog.Models {
		byName[model.Name] = model
	}
	if byName["peak-model"].Prices[0].Value.String() != "4.8" ||
		byName["request-model"].Prices[0].Value.String() != "0.288" ||
		byName["image-model"].Prices[0].Value.String() != "0.06" {
		t.Fatalf("Token、普通按次和独立图片倍率计算不正确：%#v", result.Catalog.Models)
	}
	requestModel := byName["request-model"]
	if requestModel.BillingMode != "per_request" || len(requestModel.Intervals) != 1 ||
		requestModel.Intervals[0].MinTokens != "" || requestModel.Intervals[0].MaxTokens != "" ||
		requestModel.Intervals[0].Prices[0].Value.String() != "0.24" {
		t.Fatalf("普通按次档位不应标为 Token 阶梯：%#v", requestModel)
	}
	imageModel := byName["image-model"]
	if imageModel.BillingMode != "image" || len(imageModel.Intervals) != 1 ||
		imageModel.Intervals[0].MinTokens != "" || imageModel.Intervals[0].MaxTokens != "" ||
		imageModel.Intervals[0].Prices[0].Value.String() != "0.1" {
		t.Fatalf("图片档位不应标为 Token 阶梯：%#v", imageModel)
	}
}

func TestSub2API未独立图片倍率使用高峰前基础倍率(t *testing.T) {
	model, err := parseSub2APIModelPrice(map[string]any{"pricing": map[string]any{
		"billing_mode": "image", "per_request_price": "0.12",
	}}, mustPriceDecimal(t, "2.4"), mustPriceDecimal(t, "0.8"), nil)
	if err != nil || len(model.Prices) != 1 || model.Prices[0].Value.String() != "0.096" {
		t.Fatalf("未独立图片价格必须使用高峰前基础倍率：model=%#v err=%v", model, err)
	}
}

func TestSub2APIToken开区间转换闭区间并拒绝溢出(t *testing.T) {
	intervals, err := parseSub2APIPriceIntervals([]any{map[string]any{
		"min_tokens": 10, "max_tokens": 20, "tier_label": "测试",
		"input_price": "0.000001",
	}}, "token", decimalOne, decimalOne)
	if err != nil || len(intervals) != 1 || intervals[0].MinTokens != "11" || intervals[0].MaxTokens != "20" {
		t.Fatalf("Sub2API (min,max] 没有转换为闭区间：intervals=%#v err=%v", intervals, err)
	}
	for _, row := range []map[string]any{
		{"min_tokens": int64(math.MaxInt64), "input_price": "0.000001"},
		{"min_tokens": 10, "max_tokens": 10, "input_price": "0.000001"},
	} {
		if _, err := parseSub2APIPriceIntervals([]any{row}, "token", decimalOne, decimalOne); err == nil {
			t.Fatalf("空区间或溢出区间必须拒绝：%#v", row)
		}
	}
}

func Test价格文本按字符截断保持有效UTF8(t *testing.T) {
	value := strings.Repeat("中文🙂", 100)
	normalized := normalizeGroupPriceText(value, 200)
	if !utf8.ValidString(normalized) || len([]rune(normalized)) != 200 {
		t.Fatalf("中文价格文本截断无效：runes=%d valid=%v", len([]rune(normalized)), utf8.ValidString(normalized))
	}
}

func Test价格目录限制模型数量和响应规模(t *testing.T) {
	rows := make([]any, 0, maxGroupPriceModels+1)
	for index := 0; index <= maxGroupPriceModels; index++ {
		rows = append(rows, map[string]any{
			"model_name": "model-" + strconv.Itoa(index), "quota_type": 1,
			"enable_groups": []string{"default"}, "model_price": "0.01",
		})
	}
	models, notice, err := parseNewAPIModelPrices(rows, GroupMultiplier{
		Key: "default", Name: "默认", Multiplier: decimalOne,
	})
	if err != nil || len(models) != maxGroupPriceModels || !strings.Contains(notice, "前 500 个") {
		t.Fatalf("模型数量限制无效，models=%d notice=%q err=%v", len(models), notice, err)
	}
}

func sub2APIPriceBaseMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/groups/available", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": []any{
			map[string]any{
				"id": 10, "name": "基础组", "rate_multiplier": "1",
				"image_price_1k": "0.2", "image_price_4k": "0.8",
			},
		}})
	})
	mux.HandleFunc("/api/v1/groups/rates", func(writer http.ResponseWriter, _ *http.Request) {
		writeTestJSON(writer, map[string]any{"code": 0, "data": map[string]any{"10": "0.5"}})
	})
	return mux
}

func assertPriceValues(t *testing.T, items []GroupPriceItem, expected map[string]string) {
	t.Helper()
	actual := make(map[string]string, len(items))
	for _, item := range items {
		actual[item.Key] = item.Value.String()
	}
	for key, value := range expected {
		if actual[key] != value {
			t.Fatalf("价格 %s 不正确：want=%s got=%s，全部=%#v", key, value, actual[key], items)
		}
	}
	if len(actual) != len(expected) {
		t.Fatalf("返回了未预期的价格项：want=%#v got=%#v", expected, actual)
	}
}

func mustPriceDecimal(t *testing.T, value string) decimal.Decimal {
	t.Helper()
	parsed, err := parsePriceDecimal(value)
	if err != nil {
		t.Fatalf("构造测试价格失败：%v", err)
	}
	return parsed
}

type groupPriceRetryAdapter struct {
	kind       TargetKind
	failClass  ErrorClass
	failures   int32
	calls      atomic.Int32
	cancel     context.CancelFunc
	credential *Credential
}

func (adapter *groupPriceRetryAdapter) Kind() TargetKind { return adapter.kind }

func (adapter *groupPriceRetryAdapter) Check(context.Context, TargetConfig) (Snapshot, error) {
	return Snapshot{}, nil
}

func (adapter *groupPriceRetryAdapter) ReadGroupPrices(_ context.Context, _ TargetConfig, groupKey string) (GroupPriceResult, error) {
	call := adapter.calls.Add(1)
	result := GroupPriceResult{CredentialUpdate: adapter.credential}
	if adapter.cancel != nil {
		adapter.cancel()
	}
	if call <= adapter.failures {
		return result, checkError(adapter.failClass, "测试价格读取", "测试失败", http.StatusBadGateway, nil)
	}
	result.Catalog = GroupPriceCatalog{
		GroupKey: groupKey, GroupName: "默认", Multiplier: decimalOne,
	}
	return result, nil
}

func TestRegistry价格读取只重试网络和服务错误(t *testing.T) {
	registry := NewRegistry(HTTPOptions{})
	networkAdapter := &groupPriceRetryAdapter{kind: TargetKindNewAPI, failClass: ErrorClassNetwork, failures: 2}
	registry.Register(networkAdapter)
	if _, err := registry.ReadGroupPrices(context.Background(), TargetConfig{Kind: TargetKindNewAPI}, "default"); err != nil {
		t.Fatalf("价格网络错误重试后应成功：%v", err)
	}
	if networkAdapter.calls.Load() != 3 {
		t.Fatalf("价格网络错误应尝试三次：%d", networkAdapter.calls.Load())
	}
	authAdapter := &groupPriceRetryAdapter{kind: TargetKindSub2API, failClass: ErrorClassAuth, failures: 1}
	registry.Register(authAdapter)
	if _, err := registry.ReadGroupPrices(context.Background(), TargetConfig{Kind: TargetKindSub2API}, "10"); !IsAuthFailure(err) {
		t.Fatalf("价格认证错误应直接返回：%v", err)
	}
	if authAdapter.calls.Load() != 1 {
		t.Fatalf("价格认证错误不应重试：%d", authAdapter.calls.Load())
	}
}

func TestRegistry价格读取取消时保留轮换凭据(t *testing.T) {
	registry := NewRegistry(HTTPOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	rotated := &Credential{AccessToken: "renewed", RefreshToken: "rotated"}
	adapter := &groupPriceRetryAdapter{
		kind: TargetKindSub2API, failClass: ErrorClassNetwork, failures: 1,
		cancel: cancel, credential: rotated,
	}
	registry.Register(adapter)
	result, err := registry.ReadGroupPrices(ctx, TargetConfig{Kind: TargetKindSub2API}, "10")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的价格读取必须返回取消错误：%v", err)
	}
	if result.CredentialUpdate == nil || result.CredentialUpdate.RefreshToken != "rotated" {
		t.Fatalf("取消读取前轮换的凭据不能丢失：%#v", result.CredentialUpdate)
	}
}
