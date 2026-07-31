package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

const (
	maxGroupPriceModels       = 500
	maxGroupPriceItems        = 4000
	maxGroupPriceIntervals    = 500
	maxGroupPriceModelNameLen = 200
)

var groupPriceMillion = decimal.NewFromInt(1_000_000)

// parsePriceDecimal 在创建大整数前限制输入长度，再把远端数值转换为精确十进制。
func parsePriceDecimal(value any) (decimal.Decimal, error) {
	raw, err := priceDecimalText(value)
	if err != nil {
		return decimal.Zero, err
	}
	if len(raw) == 0 || len(raw) > 128 {
		return decimal.Zero, errors.New("价格数值长度无效")
	}
	parsed, err := decimal.NewFromString(raw)
	if err != nil || parsed.IsNegative() {
		return decimal.Zero, errors.New("价格数值无效")
	}
	return validatePriceDecimal(parsed)
}

func priceDecimalText(value any) (string, error) {
	switch typed := value.(type) {
	case json.Number:
		return strings.TrimSpace(typed.String()), nil
	case string:
		return strings.TrimSpace(typed), nil
	case decimal.Decimal:
		return typed.String(), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case int32:
		return strconv.FormatInt(int64(typed), 10), nil
	case uint:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint64:
		return strconv.FormatUint(typed, 10), nil
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32), nil
	default:
		return "", errors.New("价格字段不是数值")
	}
}

func validatePriceDecimal(value decimal.Decimal) (decimal.Decimal, error) {
	// 价格只用于展示，但仍需限制指数和有效位，避免 String 展开或乘法造成资源消耗。
	if value.IsNegative() || value.Exponent() < -50 || value.Exponent() > 50 || value.Coefficient().BitLen() > 256 {
		return decimal.Zero, errors.New("价格数值超出安全范围")
	}
	return value, nil
}

func multiplyPrice(parts ...decimal.Decimal) (decimal.Decimal, error) {
	value := decimal.NewFromInt(1)
	for _, part := range parts {
		value = value.Mul(part)
		if _, err := validatePriceDecimal(value); err != nil {
			return decimal.Zero, err
		}
	}
	return value, nil
}

func newGroupPriceItem(key, label, unit string, value decimal.Decimal) (GroupPriceItem, error) {
	value, err := validatePriceDecimal(value)
	if err != nil {
		return GroupPriceItem{}, err
	}
	return GroupPriceItem{Key: key, Label: label, Value: value, Unit: unit}, nil
}

func appendGroupPriceNotice(current, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return current
	}
	if current == "" {
		return next
	}
	if strings.Contains(current, next) {
		return current
	}
	return current + "；" + next
}

func normalizeGroupPriceText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit > 0 {
		runes := []rune(value)
		if len(runes) > limit {
			value = string(runes[:limit])
		}
	}
	return value
}

func groupPriceFailure(operation, message string, cause error) error {
	return checkError(ErrorClassResponse, operation, message, 0, cause)
}

func findGroupMultiplier(groups []GroupMultiplier, key string) (GroupMultiplier, error) {
	key = strings.TrimSpace(key)
	for _, group := range groups {
		if group.Key == key {
			return group, nil
		}
	}
	return GroupMultiplier{}, fmt.Errorf("当前账号已无法访问所选分组")
}

func hasGroupPriceDetails(models []GroupModelPrice) bool {
	for _, model := range models {
		if len(model.Prices) > 0 || len(model.Intervals) > 0 {
			return true
		}
	}
	return false
}
