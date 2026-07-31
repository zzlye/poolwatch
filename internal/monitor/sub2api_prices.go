package monitor

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

const sub2APITokenPriceUnit = "USD/百万令牌"

// ReadGroupPrices 读取 Sub2API 当前用户分组的公开模型价格，并保留续期后的 JWT。
func (adapter *sub2APIAdapter) ReadGroupPrices(ctx context.Context, target TargetConfig, groupKey string) (GroupPriceResult, error) {
	target = ensureTargetKind(target, adapter.Kind())
	groupKey = strings.TrimSpace(groupKey)
	if _, err := parsePositiveInt64String(groupKey); err != nil {
		return GroupPriceResult{}, checkError(ErrorClassConfig, "读取 Sub2API 分组价格", "分组标识无效", 0, err)
	}
	session := adapter.http.newSession(target.AllowPrivateNetwork)
	token, err := adapter.resolveToken(ctx, session, target)
	if err != nil {
		return GroupPriceResult{}, err
	}
	result := GroupPriceResult{CredentialUpdate: sub2APIUpdatedCredential(target.Credential, token)}
	read := func() (GroupPriceCatalog, error) {
		groups, readErr := adapter.readGroupMultipliers(ctx, session, target, token.accessToken)
		if readErr != nil {
			return GroupPriceCatalog{}, readErr
		}
		group, findErr := findGroupMultiplier(groups, groupKey)
		if findErr != nil {
			return GroupPriceCatalog{}, checkError(ErrorClassConfig, "读取 Sub2API 分组价格", "当前账号已无法访问所选分组", 0, findErr)
		}
		return adapter.readSub2APIPriceCatalog(ctx, session, target, token.accessToken, group)
	}
	result.Catalog, err = read()
	if err != nil && IsAuthFailure(err) && (token.refreshToken != "" || sub2APIUsesPassword(target.Credential)) {
		token, err = adapter.renewToken(ctx, session, target, token.refreshToken)
		if err == nil {
			result.CredentialUpdate = sub2APIUpdatedCredential(target.Credential, token)
			result.Catalog, err = read()
		}
	}
	return result, err
}

func (adapter *sub2APIAdapter) readSub2APIPriceCatalog(ctx context.Context, session *requestSession, target TargetConfig, accessToken string, group GroupMultiplier) (GroupPriceCatalog, error) {
	headers := make(http.Header)
	setBearer(headers, accessToken)
	models, notice, unavailable, err := adapter.readSub2APIModelPlaza(ctx, session, target, headers, group)
	if err != nil {
		return GroupPriceCatalog{}, err
	}
	if unavailable || !hasGroupPriceDetails(models) {
		fallback, fallbackNotice, fallbackErr := adapter.readSub2APIAvailableChannelPrices(ctx, session, target, headers, group)
		if fallbackErr != nil {
			return GroupPriceCatalog{}, fallbackErr
		}
		models = fallback
		notice = appendGroupPriceNotice(notice, fallbackNotice)
	}
	if group.ImageRateIndependent {
		notice = appendGroupPriceNotice(notice, "当前图片使用独立倍率 "+sub2APIImageMultiplier(group).String())
	}
	if !sub2APIBaseMultiplier(group).Equal(group.Multiplier) {
		notice = appendGroupPriceNotice(notice, "当前高峰倍率应用于 Token 和普通按次；图片不叠加高峰倍率")
	}
	if !hasGroupPriceDetails(models) {
		return GroupPriceCatalog{}, checkError(ErrorClassRemote, "读取 Sub2API 模型价格", "当前站点未公开可读取的模型价格，请让站点开启模型广场或可用渠道展示", 0, nil)
	}
	return GroupPriceCatalog{
		GroupKey: group.Key, GroupName: group.Name, Multiplier: group.Multiplier,
		Models: models, Notice: notice,
	}, nil
}

func (adapter *sub2APIAdapter) readSub2APIModelPlaza(ctx context.Context, session *requestSession, target TargetConfig, headers http.Header, group GroupMultiplier) ([]GroupModelPrice, string, bool, error) {
	endpoint, err := joinTargetURL(target.BaseURL, "/api/v1/model-plaza")
	if err != nil {
		return nil, "", false, err
	}
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err != nil {
		if statusCodeOf(err) == http.StatusNotFound || statusCodeOf(err) == http.StatusForbidden {
			return nil, "", true, nil
		}
		return nil, "", false, err
	}
	data, err := sub2APIEnvelopeValue(payload, true)
	if err != nil {
		return nil, "", false, err
	}
	root, ok := data.(map[string]any)
	if !ok {
		return nil, "", false, groupPriceFailure("解析 Sub2API 模型广场", "Sub2API 模型广场响应格式无效", nil)
	}
	groups, ok := root["groups"].([]any)
	if !ok {
		return nil, "", false, groupPriceFailure("解析 Sub2API 模型广场", "Sub2API 模型广场缺少分组列表", nil)
	}
	for _, raw := range groups {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, "", false, groupPriceFailure("解析 Sub2API 模型广场", "Sub2API 模型广场分组格式无效", nil)
		}
		key, keyErr := sub2APIGroupKey(item)
		if keyErr != nil {
			return nil, "", false, groupPriceFailure("解析 Sub2API 模型广场", "Sub2API 模型广场分组标识无效", keyErr)
		}
		if key != group.Key {
			continue
		}
		rows, ok := item["models"].([]any)
		if !ok {
			return nil, "", false, groupPriceFailure("解析 Sub2API 模型广场", "Sub2API 模型价格列表格式无效", nil)
		}
		models, notice, err := parseSub2APIModelRows(rows, group.Multiplier, sub2APIImageMultiplier(group), group.ImagePriceOverrides, "")
		return models, notice, false, err
	}
	// 模型广场可能隐藏了订阅有效性相关分组，继续尝试登录用户的可用渠道接口。
	return nil, "", true, nil
}

func (adapter *sub2APIAdapter) readSub2APIAvailableChannelPrices(ctx context.Context, session *requestSession, target TargetConfig, headers http.Header, group GroupMultiplier) ([]GroupModelPrice, string, error) {
	endpoint, err := joinTargetURL(target.BaseURL, "/api/v1/channels/available")
	if err != nil {
		return nil, "", err
	}
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err != nil {
		if statusCodeOf(err) == http.StatusNotFound || statusCodeOf(err) == http.StatusForbidden {
			return nil, "", checkError(ErrorClassRemote, "读取 Sub2API 模型价格", "当前站点未公开可读取的模型价格，请让站点开启模型广场或可用渠道展示", statusCodeOf(err), nil)
		}
		return nil, "", err
	}
	data, err := sub2APIEnvelopeValue(payload, true)
	if err != nil {
		return nil, "", err
	}
	channels, ok := data.([]any)
	if !ok {
		return nil, "", groupPriceFailure("解析 Sub2API 可用渠道", "Sub2API 可用渠道响应格式无效", nil)
	}
	models := make([]GroupModelPrice, 0)
	notice := ""
	priceItems := 0
	intervalCount := 0
	for _, rawChannel := range channels {
		channel, ok := rawChannel.(map[string]any)
		if !ok {
			return nil, "", groupPriceFailure("解析 Sub2API 可用渠道", "Sub2API 可用渠道条目格式无效", nil)
		}
		channelName := normalizeGroupPriceText(stringField(channel, "name"), 100)
		platforms, ok := channel["platforms"].([]any)
		if !ok {
			return nil, "", groupPriceFailure("解析 Sub2API 可用渠道", "Sub2API 可用渠道平台格式无效", nil)
		}
		for _, rawPlatform := range platforms {
			platform, ok := rawPlatform.(map[string]any)
			if !ok {
				return nil, "", groupPriceFailure("解析 Sub2API 可用渠道", "Sub2API 可用渠道平台条目无效", nil)
			}
			matched, err := sub2APIPlatformContainsGroup(platform, group.Key)
			if err != nil {
				return nil, "", err
			}
			if !matched {
				continue
			}
			rows, ok := platform["supported_models"].([]any)
			if !ok {
				return nil, "", groupPriceFailure("解析 Sub2API 可用渠道", "Sub2API 可用渠道模型列表无效", nil)
			}
			source := channelName
			if platformName := normalizeGroupPriceText(stringField(platform, "platform"), 80); platformName != "" {
				if source != "" {
					source += " / "
				}
				source += platformName
			}
			parsed, parsedNotice, err := parseSub2APIModelRows(rows, group.Multiplier, sub2APIImageMultiplier(group), group.ImagePriceOverrides, source)
			if err != nil {
				return nil, "", err
			}
			for _, model := range parsed {
				if len(models) >= maxGroupPriceModels {
					notice = appendGroupPriceNotice(notice, "模型较多，本次只展示前 500 个")
					break
				}
				priceItems += len(model.Prices)
				intervalCount += len(model.Intervals)
				for _, interval := range model.Intervals {
					priceItems += len(interval.Prices)
				}
				if priceItems > maxGroupPriceItems || intervalCount > maxGroupPriceIntervals {
					notice = appendGroupPriceNotice(notice, "价格明细较多，后续模型已省略")
					break
				}
				models = append(models, model)
			}
			notice = appendGroupPriceNotice(notice, parsedNotice)
		}
	}
	if len(models) > 0 {
		notice = appendGroupPriceNotice(notice, "模型广场不可用，当前价格来自站点公开的可用渠道列表")
	}
	sort.SliceStable(models, func(left, right int) bool {
		if models[left].Name == models[right].Name {
			return models[left].Note < models[right].Note
		}
		return models[left].Name < models[right].Name
	})
	return models, notice, nil
}

func sub2APIPlatformContainsGroup(platform map[string]any, groupKey string) (bool, error) {
	groups, ok := platform["groups"].([]any)
	if !ok {
		return false, groupPriceFailure("解析 Sub2API 可用渠道", "Sub2API 可用渠道分组格式无效", nil)
	}
	for _, raw := range groups {
		item, ok := raw.(map[string]any)
		if !ok {
			return false, groupPriceFailure("解析 Sub2API 可用渠道", "Sub2API 可用渠道分组条目无效", nil)
		}
		key, err := sub2APIGroupKey(item)
		if err != nil {
			return false, groupPriceFailure("解析 Sub2API 可用渠道", "Sub2API 可用渠道分组标识无效", err)
		}
		if key == groupKey {
			return true, nil
		}
	}
	return false, nil
}

func parseSub2APIModelRows(rows []any, tokenMultiplier, imageMultiplier decimal.Decimal, imagePriceOverrides map[string]decimal.Decimal, source string) ([]GroupModelPrice, string, error) {
	models := make([]GroupModelPrice, 0, min(len(rows), maxGroupPriceModels))
	notice := ""
	priceItems := 0
	intervalCount := 0
	invalidModels := 0
	for _, raw := range rows {
		if len(models) >= maxGroupPriceModels {
			notice = appendGroupPriceNotice(notice, "模型较多，本次只展示前 500 个")
			break
		}
		row, ok := raw.(map[string]any)
		if !ok {
			invalidModels++
			continue
		}
		name := normalizeGroupPriceText(stringField(row, "name"), maxGroupPriceModelNameLen)
		if name == "" {
			invalidModels++
			continue
		}
		model, err := parseSub2APIModelPrice(row, tokenMultiplier, imageMultiplier, imagePriceOverrides)
		if err != nil {
			invalidModels++
			continue
		}
		model.Name = name
		if source != "" {
			model.Note = appendGroupPriceNotice(model.Note, "来源："+normalizeGroupPriceText(source, 180))
		}
		priceItems += len(model.Prices)
		intervalCount += len(model.Intervals)
		for _, interval := range model.Intervals {
			priceItems += len(interval.Prices)
		}
		if priceItems > maxGroupPriceItems || intervalCount > maxGroupPriceIntervals {
			notice = appendGroupPriceNotice(notice, "价格明细较多，后续模型已省略")
			break
		}
		models = append(models, model)
	}
	if invalidModels > 0 {
		notice = appendGroupPriceNotice(notice, "有 "+strconv.Itoa(invalidModels)+" 个模型价格无法识别，已跳过")
	}
	sort.SliceStable(models, func(left, right int) bool { return models[left].Name < models[right].Name })
	return models, notice, nil
}

func parseSub2APIModelPrice(row map[string]any, tokenMultiplier, imageMultiplier decimal.Decimal, imagePriceOverrides map[string]decimal.Decimal) (GroupModelPrice, error) {
	pricing, _ := row["pricing"].(map[string]any)
	mode := "token"
	if pricing != nil {
		if configured := strings.ToLower(strings.TrimSpace(stringField(pricing, "billing_mode"))); configured != "" {
			mode = configured
		}
	}
	model := GroupModelPrice{BillingMode: mode, Prices: []GroupPriceItem{}}
	switch mode {
	case "token":
		prices, err := sub2APITokenPrices(pricing, tokenMultiplier)
		if err != nil {
			return GroupModelPrice{}, err
		}
		model.Prices = prices
	case "per_request", "image":
		value, exists, err := sub2APIPriceField(pricing, "per_request_price")
		if err != nil {
			return GroupModelPrice{}, err
		}
		if exists {
			multiplier := tokenMultiplier
			if mode == "image" {
				multiplier = imageMultiplier
			}
			value, err = multiplyPrice(value, multiplier)
			if err != nil {
				return GroupModelPrice{}, groupPriceFailure("计算 Sub2API 模型价格", "Sub2API 按次价格超出安全范围", err)
			}
			label, unit := "按次", "USD/次"
			if mode == "image" {
				label, unit = "按图片", "USD/张"
			}
			item, _ := newGroupPriceItem("per_request", label, unit, value)
			model.Prices = append(model.Prices, item)
		}
	default:
		model.Note = "该模型采用站点自定义计费方式，当前没有可安全展示的固定价格"
		return model, nil
	}
	if pricing != nil {
		intervalRows, exists := pricing["intervals"]
		if exists && intervalRows != nil {
			rows, ok := intervalRows.([]any)
			if !ok {
				return GroupModelPrice{}, groupPriceFailure("解析 Sub2API 阶梯价格", "Sub2API 阶梯价格格式无效", nil)
			}
			intervals, err := parseSub2APIPriceIntervals(rows, mode, tokenMultiplier, imageMultiplier)
			if err != nil {
				return GroupModelPrice{}, err
			}
			model.Intervals = intervals
			if len(intervals) > 0 && mode == "token" {
				model.BillingMode = "tiered"
			}
		}
	}
	if mode == "image" {
		if err := applySub2APIImagePriceOverrides(&model, imagePriceOverrides, imageMultiplier); err != nil {
			return GroupModelPrice{}, err
		}
	}
	if len(model.Prices) == 0 && len(model.Intervals) == 0 && model.Note == "" {
		model.Note = "站点没有公开这个模型的实际价格"
	}
	return model, nil
}

// applySub2APIImagePriceOverrides 让分组按尺寸配置的图片价覆盖同尺寸渠道价，并补齐渠道未列出的尺寸。
func applySub2APIImagePriceOverrides(model *GroupModelPrice, overrides map[string]decimal.Decimal, imageMultiplier decimal.Decimal) error {
	if model == nil || len(overrides) == 0 {
		return nil
	}
	for _, size := range []string{"1K", "2K", "4K"} {
		basePrice, exists := overrides[size]
		if !exists {
			continue
		}
		value, err := multiplyPrice(basePrice, imageMultiplier)
		if err != nil {
			return groupPriceFailure("计算 Sub2API 图片价格", "Sub2API 分组图片价格超出安全范围", err)
		}
		item, err := newGroupPriceItem("per_request", "按图片", "USD/张", value)
		if err != nil {
			return groupPriceFailure("计算 Sub2API 图片价格", "Sub2API 分组图片价格无效", err)
		}
		matched := false
		for index := range model.Intervals {
			if normalizeSub2APIImageSize(model.Intervals[index].Label) != size {
				continue
			}
			model.Intervals[index].Prices = []GroupPriceItem{item}
			matched = true
		}
		if !matched {
			model.Intervals = append(model.Intervals, GroupPriceInterval{
				Label: size, Prices: []GroupPriceItem{item},
			})
		}
	}
	return nil
}

func normalizeSub2APIImageSize(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	switch value {
	case "1K", "2K", "4K":
		return value
	default:
		return ""
	}
}

func sub2APITokenPrices(pricing map[string]any, multiplier decimal.Decimal) ([]GroupPriceItem, error) {
	type fieldDefinition struct {
		field string
		key   string
		label string
		unit  string
	}
	definitions := []fieldDefinition{
		{field: "input_price", key: "input", label: "输入", unit: sub2APITokenPriceUnit},
		{field: "output_price", key: "output", label: "输出", unit: sub2APITokenPriceUnit},
		{field: "cache_write_price", key: "cache_write", label: "缓存写入", unit: sub2APITokenPriceUnit},
		{field: "cache_read_price", key: "cache_read", label: "缓存读取", unit: sub2APITokenPriceUnit},
		{field: "image_input_price", key: "image_input", label: "图片输入", unit: "USD/百万图片 Token"},
		{field: "image_output_price", key: "image_output", label: "图片输出", unit: "USD/百万图片 Token"},
	}
	items := make([]GroupPriceItem, 0, len(definitions)+1)
	for _, definition := range definitions {
		value, exists, err := sub2APIPriceField(pricing, definition.field)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		value, err = multiplyPrice(value, groupPriceMillion, multiplier)
		if err != nil {
			return nil, groupPriceFailure("计算 Sub2API 模型价格", "Sub2API 模型价格超出安全范围", err)
		}
		item, _ := newGroupPriceItem(definition.key, definition.label, definition.unit, value)
		items = append(items, item)
	}
	// 站点自定义缓存写入价会同时用于普通和 1 小时缓存写入。
	cacheWrite, customCacheWrite, err := sub2APIPriceField(pricing, "cache_write_price")
	if err != nil {
		return nil, err
	}
	if customCacheWrite {
		cacheWrite, err = multiplyPrice(cacheWrite, groupPriceMillion, multiplier)
		if err != nil {
			return nil, groupPriceFailure("计算 Sub2API 模型价格", "Sub2API 缓存价格超出安全范围", err)
		}
		item, _ := newGroupPriceItem("cache_write_1h", "1 小时缓存写入", sub2APITokenPriceUnit, cacheWrite)
		items = append(items, item)
	}
	return items, nil
}

func sub2APIPriceField(object map[string]any, field string) (decimal.Decimal, bool, error) {
	if object == nil {
		return decimal.Zero, false, nil
	}
	raw, exists := object[field]
	if !exists || raw == nil {
		return decimal.Zero, false, nil
	}
	value, err := parsePriceDecimal(raw)
	if err != nil {
		return decimal.Zero, false, groupPriceFailure("解析 Sub2API 模型价格", "Sub2API 响应包含无效价格", err)
	}
	return value, true, nil
}

func parseSub2APIPriceIntervals(rows []any, mode string, tokenMultiplier, imageMultiplier decimal.Decimal) ([]GroupPriceInterval, error) {
	if len(rows) > 20 {
		return nil, groupPriceFailure("解析 Sub2API 阶梯价格", "单个模型的价格阶梯超过 20 档限制", nil)
	}
	intervals := make([]GroupPriceInterval, 0, len(rows))
	for index, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			return nil, groupPriceFailure("解析 Sub2API 阶梯价格", "Sub2API 阶梯价格条目无效", nil)
		}
		minimumText := ""
		maximumText := ""
		if mode == "token" {
			minimum, err := parseInt64(row["min_tokens"])
			if err != nil || minimum < 0 {
				return nil, groupPriceFailure("解析 Sub2API 阶梯价格", "Sub2API 阶梯最小 Token 无效", err)
			}
			if minimum == math.MaxInt64 {
				return nil, groupPriceFailure("解析 Sub2API 阶梯价格", "Sub2API 阶梯最小 Token 超出可展示范围", nil)
			}
			// Sub2API 官方区间是 (min,max]，API 输出转换为便于阅读的闭区间 [min+1,max]。
			minimum++
			minimumText = strconv.FormatInt(minimum, 10)
			if rawMaximum, exists := row["max_tokens"]; exists && rawMaximum != nil {
				maximum, err := parseInt64(rawMaximum)
				if err != nil || maximum < minimum {
					return nil, groupPriceFailure("解析 Sub2API 阶梯价格", "Sub2API 阶梯最大 Token 无效", err)
				}
				maximumText = strconv.FormatInt(maximum, 10)
			}
		}
		label := normalizeGroupPriceText(stringField(row, "tier_label"), 100)
		if label == "" {
			label = fmt.Sprintf("阶梯 %d", index+1)
		}
		prices := make([]GroupPriceItem, 0, 5)
		if mode == "per_request" || mode == "image" {
			value, exists, err := sub2APIPriceField(row, "per_request_price")
			if err != nil {
				return nil, err
			}
			if exists {
				multiplier := tokenMultiplier
				if mode == "image" {
					multiplier = imageMultiplier
				}
				value, err = multiplyPrice(value, multiplier)
				if err != nil {
					return nil, groupPriceFailure("计算 Sub2API 阶梯价格", "Sub2API 阶梯按次价格超出安全范围", err)
				}
				label, unit := "按次", "USD/次"
				if mode == "image" {
					label, unit = "按图片", "USD/张"
				}
				item, _ := newGroupPriceItem("per_request", label, unit, value)
				prices = append(prices, item)
			}
		} else {
			definitions := []struct{ field, key, label string }{
				{field: "input_price", key: "input", label: "输入"},
				{field: "output_price", key: "output", label: "输出"},
				{field: "cache_write_price", key: "cache_write", label: "缓存写入"},
				{field: "cache_read_price", key: "cache_read", label: "缓存读取"},
			}
			for _, definition := range definitions {
				value, exists, err := sub2APIPriceField(row, definition.field)
				if err != nil {
					return nil, err
				}
				if !exists {
					continue
				}
				value, err = multiplyPrice(value, groupPriceMillion, tokenMultiplier)
				if err != nil {
					return nil, groupPriceFailure("计算 Sub2API 阶梯价格", "Sub2API 阶梯价格超出安全范围", err)
				}
				item, _ := newGroupPriceItem(definition.key, definition.label, sub2APITokenPriceUnit, value)
				prices = append(prices, item)
			}
		}
		if len(prices) == 0 {
			continue
		}
		intervals = append(intervals, GroupPriceInterval{
			Label: label, MinTokens: minimumText, MaxTokens: maximumText, Prices: prices,
		})
	}
	return intervals, nil
}

func sub2APIImageMultiplier(group GroupMultiplier) decimal.Decimal {
	if group.ImageMultiplier.IsZero() && !group.ImageRateIndependent && !group.Multiplier.IsZero() {
		// 兼容测试替身和旧调用方构造的分组；真实 Sub2API 读取始终填充图片倍率。
		return sub2APIBaseMultiplier(group)
	}
	return group.ImageMultiplier
}

func sub2APIBaseMultiplier(group GroupMultiplier) decimal.Decimal {
	if group.BaseMultiplier.IsZero() && !group.Multiplier.IsZero() {
		return group.Multiplier
	}
	return group.BaseMultiplier
}

var _ GroupPriceReader = (*sub2APIAdapter)(nil)
