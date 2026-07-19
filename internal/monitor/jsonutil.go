package monitor

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

func unwrapEnvelope(value any) (any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return value, nil
	}
	if code, exists := object["code"]; exists {
		parsed, err := parseDecimal(code)
		if err == nil && !parsed.IsZero() {
			return nil, checkError(ErrorClassRemote, "解析渠道响应", "渠道返回了失败状态", 0, nil)
		}
		if data, exists := object["data"]; exists {
			return data, nil
		}
	}
	if success, exists := object["success"]; exists {
		if healthy, ok := success.(bool); ok && !healthy {
			return nil, checkError(ErrorClassRemote, "解析渠道响应", "渠道返回了失败状态", 0, nil)
		}
		if data, exists := object["data"]; exists {
			return data, nil
		}
	}
	return value, nil
}

func parseDecimal(value any) (decimal.Decimal, error) {
	var raw string
	switch typed := value.(type) {
	case decimal.Decimal:
		return typed, nil
	case json.Number:
		raw = typed.String()
	case string:
		raw = strings.TrimSpace(typed)
	case float64:
		raw = strconv.FormatFloat(typed, 'g', -1, 64)
	case float32:
		raw = strconv.FormatFloat(float64(typed), 'g', -1, 32)
	case int:
		raw = strconv.Itoa(typed)
	case int64:
		raw = strconv.FormatInt(typed, 10)
	case int32:
		raw = strconv.FormatInt(int64(typed), 10)
	case uint:
		raw = strconv.FormatUint(uint64(typed), 10)
	case uint64:
		raw = strconv.FormatUint(typed, 10)
	case nil:
		return decimal.Zero, fmt.Errorf("数值为空")
	default:
		return decimal.Zero, fmt.Errorf("字段不是数值")
	}
	if raw == "" {
		return decimal.Zero, fmt.Errorf("数值为空")
	}
	parsed, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, fmt.Errorf("无法解析数值")
	}
	return parsed, nil
}

func decimalField(object map[string]any, names ...string) (decimal.Decimal, error) {
	for _, name := range names {
		if value, ok := object[name]; ok {
			return parseDecimal(value)
		}
	}
	return decimal.Zero, fmt.Errorf("缺少数值字段")
}

func int64Field(object map[string]any, names ...string) int64 {
	value, err := decimalField(object, names...)
	if err != nil {
		return 0
	}
	return value.IntPart()
}

func stringField(object map[string]any, names ...string) string {
	for _, name := range names {
		value, ok := object[name]
		if !ok || value == nil {
			continue
		}
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func boolField(object map[string]any, names ...string) (bool, bool) {
	for _, name := range names {
		value, ok := object[name]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed, true
		case string:
			parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
			if err == nil {
				return parsed, true
			}
		}
	}
	return false, false
}

// ResolveJSONPointer 按 RFC 6901 从已解析 JSON 中读取字段。
func ResolveJSONPointer(root any, pointer string) (any, error) {
	if pointer == "" {
		return root, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("JSON Pointer 必须以斜杠开头")
	}
	current := root
	for _, encoded := range strings.Split(pointer[1:], "/") {
		segment, err := decodePointerSegment(encoded)
		if err != nil {
			return nil, err
		}
		switch typed := current.(type) {
		case map[string]any:
			value, exists := typed[segment]
			if !exists {
				return nil, fmt.Errorf("JSON Pointer 指向的字段不存在")
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, fmt.Errorf("JSON Pointer 数组下标无效")
			}
			current = typed[index]
		default:
			return nil, fmt.Errorf("JSON Pointer 穿过了非容器字段")
		}
	}
	return current, nil
}

func decodePointerSegment(value string) (string, error) {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '~' {
			builder.WriteByte(value[index])
			continue
		}
		if index+1 >= len(value) {
			return "", fmt.Errorf("JSON Pointer 转义无效")
		}
		index++
		switch value[index] {
		case '0':
			builder.WriteByte('~')
		case '1':
			builder.WriteByte('/')
		default:
			return "", fmt.Errorf("JSON Pointer 转义无效")
		}
	}
	return builder.String(), nil
}
