package monitor

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"
)

func TestResolveJSONPointer支持转义和数组(t *testing.T) {
	root := map[string]any{
		"a/b": map[string]any{
			"~key": []any{json.Number("1"), "12.50"},
		},
	}
	value, err := ResolveJSONPointer(root, "/a~1b/~0key/1")
	if err != nil {
		t.Fatalf("读取 JSON Pointer 失败：%v", err)
	}
	parsed, err := parseDecimal(value)
	if err != nil {
		t.Fatalf("解析数字字符串失败：%v", err)
	}
	if !parsed.Equal(decimal.RequireFromString("12.5")) {
		t.Fatalf("数值不符合预期：%s", parsed)
	}
}

func TestResolveJSONPointer拒绝无效转义(t *testing.T) {
	_, err := ResolveJSONPointer(map[string]any{"key": 1}, "/~2")
	if err == nil {
		t.Fatal("无效转义应当返回错误")
	}
}

func TestDecimalJSON始终输出字符串(t *testing.T) {
	data, err := json.Marshal(Metric{Key: MetricWalletBalance, Value: decimal.RequireFromString("12.3400")})
	if err != nil {
		t.Fatalf("序列化指标失败：%v", err)
	}
	if string(data) != `{"key":"wallet_balance","label":"","value":"12.34","unit":""}` {
		t.Fatalf("十进制值没有按字符串输出：%s", data)
	}
}
