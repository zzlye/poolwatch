package monitor

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

const newAPITokenPriceUnit = "USD/百万令牌"

type newAPIPriceCondition struct {
	variable string
	operator string
	value    int64
}

type newAPITierPrice struct {
	label     string
	condition string
	minTokens string
	maxTokens string
	prices    []GroupPriceItem
}

// ReadGroupPrices 读取 New API 当前用户可见模型，并计算指定分组的最终价格。
func (adapter *newAPIAdapter) ReadGroupPrices(ctx context.Context, target TargetConfig, groupKey string) (GroupPriceResult, error) {
	target = ensureTargetKind(target, adapter.Kind())
	groupKey = strings.TrimSpace(groupKey)
	if groupKey == "" || len(groupKey) > 200 {
		return GroupPriceResult{}, checkError(ErrorClassConfig, "读取 New API 分组价格", "分组标识无效", 0, nil)
	}
	session, headers, cached := adapter.cachedSession(target)
	if session == nil {
		session = adapter.http.newSession(target.AllowPrivateNetwork)
	}
	statusURL, err := joinTargetURL(target.BaseURL, "/api/status")
	if err != nil {
		return GroupPriceResult{}, err
	}
	var statusPayload any
	if err := session.doJSON(ctx, http.MethodGet, statusURL, nil, nil, &statusPayload); err != nil {
		return GroupPriceResult{}, err
	}
	statusData, err := newAPIEnvelopeObject(statusPayload, false)
	if err != nil {
		return GroupPriceResult{}, err
	}
	priceDisplay := parseNewAPIQuotaDisplay(statusData)
	if !cached {
		headers, err = adapter.authenticate(ctx, session, target, statusData)
		if err != nil {
			return GroupPriceResult{}, err
		}
		adapter.storeSession(target, session, headers)
	}
	read := func() (GroupPriceCatalog, error) {
		groups, readErr := adapter.readGroupMultipliers(ctx, session, target, headers)
		if readErr != nil {
			return GroupPriceCatalog{}, readErr
		}
		group, findErr := findGroupMultiplier(groups, groupKey)
		if findErr != nil {
			return GroupPriceCatalog{}, checkError(ErrorClassConfig, "读取 New API 分组价格", "当前账号已无法访问所选分组", 0, findErr)
		}
		return adapter.readNewAPIPriceCatalog(ctx, session, target, headers, group)
	}
	catalog, err := read()
	if err != nil && cached && IsAuthFailure(err) {
		adapter.deleteSession(target)
		session = adapter.http.newSession(target.AllowPrivateNetwork)
		headers, err = adapter.authenticate(ctx, session, target, statusData)
		if err == nil {
			adapter.storeSession(target, session, headers)
			catalog, err = read()
		}
	}
	if err != nil {
		return GroupPriceResult{}, err
	}
	if err := applyNewAPIPriceDisplay(&catalog, priceDisplay); err != nil {
		return GroupPriceResult{}, groupPriceFailure("转换 New API 模型价格", "New API 模型价格货币换算失败", err)
	}
	return GroupPriceResult{Catalog: catalog}, nil
}

// applyNewAPIPriceDisplay 把内部统一计算的 USD 价格转换为站点公开的展示单位。
// 价格页与余额页使用同一份公开配置，但这里的输入已经是 USD，不能再次除以 quota_per_unit。
func applyNewAPIPriceDisplay(catalog *GroupPriceCatalog, display newAPIQuotaDisplay) error {
	factor := decimal.NewFromInt(1)
	unit := "USD"
	switch display.displayType {
	case "CNY", "CUSTOM":
		factor = display.exchangeRate
		unit = normalizeGroupPriceText(display.unit, 32)
	case "TOKENS":
		factor = display.quotaPerUnit
		unit = "tokens"
	}
	if unit == "" {
		unit = "USD"
	}
	for modelIndex := range catalog.Models {
		if err := applyNewAPIPriceItems(catalog.Models[modelIndex].Prices, factor, unit); err != nil {
			return err
		}
		for intervalIndex := range catalog.Models[modelIndex].Intervals {
			if err := applyNewAPIPriceItems(catalog.Models[modelIndex].Intervals[intervalIndex].Prices, factor, unit); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyNewAPIPriceItems(items []GroupPriceItem, factor decimal.Decimal, displayUnit string) error {
	for index := range items {
		value, err := multiplyPrice(items[index].Value, factor)
		if err != nil {
			return err
		}
		items[index].Value = value
		// 适配器生成的价格单位均以 USD 开头，只替换货币部分并保留计量单位。
		if strings.HasPrefix(items[index].Unit, "USD") {
			items[index].Unit = displayUnit + strings.TrimPrefix(items[index].Unit, "USD")
		}
	}
	return nil
}

func (adapter *newAPIAdapter) readNewAPIPriceCatalog(ctx context.Context, session *requestSession, target TargetConfig, headers http.Header, group GroupMultiplier) (GroupPriceCatalog, error) {
	endpoint, err := joinTargetURL(target.BaseURL, "/api/pricing")
	if err != nil {
		return GroupPriceCatalog{}, err
	}
	var payload any
	if err := session.doJSON(ctx, http.MethodGet, endpoint, headers, nil, &payload); err != nil {
		if statusCodeOf(err) == http.StatusForbidden || statusCodeOf(err) == http.StatusNotFound {
			return GroupPriceCatalog{}, checkError(ErrorClassRemote, "读取 New API 模型价格", "当前站点未公开可读取的模型价格", statusCodeOf(err), nil)
		}
		return GroupPriceCatalog{}, err
	}
	root, ok := payload.(map[string]any)
	if !ok {
		return GroupPriceCatalog{}, groupPriceFailure("解析 New API 模型价格", "New API 模型价格响应格式无效", nil)
	}
	if success, exists := root["success"].(bool); exists && !success {
		return GroupPriceCatalog{}, checkError(ErrorClassRemote, "读取 New API 模型价格", "当前站点未公开可读取的模型价格", 0, nil)
	}
	rows, ok := root["data"].([]any)
	if !ok {
		return GroupPriceCatalog{}, groupPriceFailure("解析 New API 模型价格", "New API 响应缺少模型价格列表", nil)
	}
	models, notice, err := parseNewAPIModelPrices(rows, group)
	if err != nil {
		return GroupPriceCatalog{}, err
	}
	if !hasGroupPriceDetails(models) {
		return GroupPriceCatalog{}, checkError(ErrorClassRemote, "读取 New API 模型价格", "当前站点未公开所选分组的模型价格", 0, nil)
	}
	return GroupPriceCatalog{
		GroupKey: group.Key, GroupName: group.Name, Multiplier: group.Multiplier,
		Models: models, Notice: notice,
	}, nil
}

func parseNewAPIModelPrices(rows []any, group GroupMultiplier) ([]GroupModelPrice, string, error) {
	models := make([]GroupModelPrice, 0, min(len(rows), maxGroupPriceModels))
	seen := make(map[string]struct{})
	notice := ""
	priceItems := 0
	intervals := 0
	invalidModels := 0
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			invalidModels++
			continue
		}
		enabled, err := newAPIModelEnabledForGroup(row["enable_groups"], group.Key)
		if err != nil {
			invalidModels++
			continue
		}
		if !enabled {
			continue
		}
		name := normalizeGroupPriceText(stringField(row, "model_name"), maxGroupPriceModelNameLen)
		if name == "" {
			invalidModels++
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		if len(models) >= maxGroupPriceModels {
			notice = appendGroupPriceNotice(notice, "模型较多，本次只展示前 500 个")
			break
		}
		model, err := parseNewAPIModelPrice(row, group.Multiplier)
		if err != nil {
			invalidModels++
			continue
		}
		model.Name = name
		priceItems += len(model.Prices)
		for _, interval := range model.Intervals {
			priceItems += len(interval.Prices)
		}
		intervals += len(model.Intervals)
		if priceItems > maxGroupPriceItems || intervals > maxGroupPriceIntervals {
			notice = appendGroupPriceNotice(notice, "价格明细较多，后续模型已省略")
			break
		}
		seen[name] = struct{}{}
		models = append(models, model)
	}
	if invalidModels > 0 {
		notice = appendGroupPriceNotice(notice, "有 "+strconv.Itoa(invalidModels)+" 个模型价格无法识别，已跳过")
	}
	return models, notice, nil
}

func newAPIModelEnabledForGroup(raw any, groupKey string) (bool, error) {
	items, ok := raw.([]any)
	if !ok {
		if stringsList, stringsOK := raw.([]string); stringsOK {
			for _, item := range stringsList {
				if item == "all" || item == groupKey {
					return true, nil
				}
			}
			return false, nil
		}
		return false, errors.New("启用分组不是数组")
	}
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return false, errors.New("启用分组包含非文本值")
		}
		if strings.TrimSpace(text) == "all" || strings.TrimSpace(text) == groupKey {
			return true, nil
		}
	}
	return false, nil
}

func parseNewAPIModelPrice(row map[string]any, multiplier decimal.Decimal) (GroupModelPrice, error) {
	mode := strings.TrimSpace(stringField(row, "billing_mode"))
	if mode == "tiered_expr" {
		tiers, hasRequestRules, err := parseNewAPITierPrices(stringField(row, "billing_expr"), multiplier)
		model := GroupModelPrice{BillingMode: "tiered", Prices: []GroupPriceItem{}, Intervals: []GroupPriceInterval{}}
		if err != nil || len(tiers) == 0 {
			model.Note = "该模型采用动态计费，站点没有公开可安全解析的固定阶梯明细"
			return model, nil
		}
		for _, tier := range tiers {
			model.Intervals = append(model.Intervals, GroupPriceInterval{
				Label: tier.label, Condition: tier.condition, MinTokens: tier.minTokens, MaxTokens: tier.maxTokens, Prices: tier.prices,
			})
		}
		if hasRequestRules {
			model.Note = "显示的是基础阶梯价格，实际价格还可能受请求参数或时间规则调整"
		}
		return model, nil
	}
	quotaType, err := parseInt64(row["quota_type"])
	if err != nil || (quotaType != 0 && quotaType != 1) {
		return GroupModelPrice{}, groupPriceFailure("解析 New API 模型价格", "New API 模型计费类型无效", err)
	}
	if quotaType == 1 {
		price, err := parseRequiredNewAPIPrice(row, "model_price")
		if err != nil {
			return GroupModelPrice{}, err
		}
		price, err = multiplyPrice(price, multiplier)
		if err != nil {
			return GroupModelPrice{}, groupPriceFailure("计算 New API 模型价格", "New API 按次价格超出安全范围", err)
		}
		item, _ := newGroupPriceItem("per_request", "按次", "USD/次", price)
		return GroupModelPrice{BillingMode: "per_request", Prices: []GroupPriceItem{item}}, nil
	}
	modelRatio, err := parseRequiredNewAPIPrice(row, "model_ratio")
	if err != nil {
		return GroupModelPrice{}, err
	}
	base, err := multiplyPrice(modelRatio, decimal.NewFromInt(2), multiplier)
	if err != nil {
		return GroupModelPrice{}, groupPriceFailure("计算 New API 模型价格", "New API 模型基础价格超出安全范围", err)
	}
	completion, err := parseRequiredNewAPIPrice(row, "completion_ratio")
	if err != nil {
		return GroupModelPrice{}, err
	}
	prices := make([]GroupPriceItem, 0, 8)
	prices, err = appendCalculatedPrice(prices, "input", "输入", newAPITokenPriceUnit, base)
	if err == nil {
		prices, err = appendPriceWithRatios(prices, "output", "输出", newAPITokenPriceUnit, base, completion)
	}
	if err == nil {
		prices, err = appendOptionalNewAPIPrice(prices, row, "cache_ratio", "cache_read", "缓存读取", newAPITokenPriceUnit, base)
	}
	if err == nil {
		prices, err = appendOptionalNewAPIPrice(prices, row, "create_cache_ratio", "cache_write", "缓存写入", newAPITokenPriceUnit, base)
	}
	if err == nil {
		prices, err = appendOptionalNewAPIPrice(prices, row, "image_ratio", "image_input", "图片输入", "USD/百万图片 Token", base)
	}
	if err == nil {
		prices, err = appendOptionalNewAPIPrice(prices, row, "audio_ratio", "audio_input", "音频输入", "USD/百万音频 Token", base)
	}
	if err == nil {
		prices, err = appendNewAPIAudioOutputPrice(prices, row, base)
	}
	if err != nil {
		return GroupModelPrice{}, groupPriceFailure("计算 New API 模型价格", "New API 模型价格超出安全范围", err)
	}
	return GroupModelPrice{BillingMode: "token", Prices: prices}, nil
}

func parseRequiredNewAPIPrice(row map[string]any, field string) (decimal.Decimal, error) {
	raw, exists := row[field]
	if !exists || raw == nil {
		return decimal.Zero, groupPriceFailure("解析 New API 模型价格", "New API 响应缺少必要价格字段", nil)
	}
	value, err := parsePriceDecimal(raw)
	if err != nil {
		return decimal.Zero, groupPriceFailure("解析 New API 模型价格", "New API 响应包含无效价格", err)
	}
	return value, nil
}

func appendCalculatedPrice(items []GroupPriceItem, key, label, unit string, value decimal.Decimal) ([]GroupPriceItem, error) {
	item, err := newGroupPriceItem(key, label, unit, value)
	if err != nil {
		return items, err
	}
	return append(items, item), nil
}

func appendPriceWithRatios(items []GroupPriceItem, key, label, unit string, parts ...decimal.Decimal) ([]GroupPriceItem, error) {
	value, err := multiplyPrice(parts...)
	if err != nil {
		return items, err
	}
	return appendCalculatedPrice(items, key, label, unit, value)
}

func appendOptionalNewAPIPrice(items []GroupPriceItem, row map[string]any, field, key, label, unit string, parts ...decimal.Decimal) ([]GroupPriceItem, error) {
	raw, exists := row[field]
	if !exists || raw == nil {
		return items, nil
	}
	ratio, err := parsePriceDecimal(raw)
	if err != nil {
		return items, err
	}
	parts = append(parts, ratio)
	return appendPriceWithRatios(items, key, label, unit, parts...)
}

func appendNewAPIAudioOutputPrice(items []GroupPriceItem, row map[string]any, base decimal.Decimal) ([]GroupPriceItem, error) {
	audioRaw, audioExists := row["audio_ratio"]
	completionRaw, completionExists := row["audio_completion_ratio"]
	if !audioExists || audioRaw == nil || !completionExists || completionRaw == nil {
		return items, nil
	}
	audio, err := parsePriceDecimal(audioRaw)
	if err != nil {
		return items, err
	}
	completion, err := parsePriceDecimal(completionRaw)
	if err != nil {
		return items, err
	}
	return appendPriceWithRatios(items, "audio_output", "音频输出", "USD/百万音频 Token", base, audio, completion)
}

// parseNewAPITierPrices 只接受可完整验证的线性阶梯格式，不执行上游表达式。
func parseNewAPITierPrices(expression string, multiplier decimal.Decimal) ([]newAPITierPrice, bool, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" || len(expression) > 32_000 {
		return nil, false, errors.New("动态计费表达式为空或过长")
	}
	hasRequestRules := false
	if split := strings.Index(expression, "|||"); split >= 0 {
		hasRequestRules = true
		expression = strings.TrimSpace(expression[:split])
	}
	if strings.HasPrefix(expression, "v") {
		if colon := strings.IndexByte(expression, ':'); colon > 1 {
			version := expression[1:colon]
			if _, err := strconv.ParseUint(version, 10, 16); err == nil {
				expression = strings.TrimSpace(expression[colon+1:])
			}
		}
	}
	tiers, err := parseNewAPITierNode(expression, nil, multiplier)
	if err != nil {
		return nil, hasRequestRules, err
	}
	if len(tiers) > 20 {
		return nil, hasRequestRules, errors.New("动态计费阶梯过多")
	}
	return tiers, hasRequestRules, nil
}

func parseNewAPITierNode(expression string, inherited []newAPIPriceCondition, multiplier decimal.Decimal) ([]newAPITierPrice, error) {
	expression = strings.TrimSpace(expression)
	question := findTopLevelByte(expression, '?')
	if question < 0 {
		tier, err := parseNewAPITierLeaf(expression, inherited, multiplier)
		if err != nil {
			return nil, err
		}
		return []newAPITierPrice{tier}, nil
	}
	colon := findTopLevelByte(expression[question+1:], ':')
	if colon < 0 {
		return nil, errors.New("动态计费阶梯缺少分支")
	}
	colon += question + 1
	conditions, err := parseNewAPIPriceConditions(expression[:question])
	if err != nil {
		return nil, err
	}
	trueConstraints := append(append([]newAPIPriceCondition{}, inherited...), conditions...)
	left, err := parseNewAPITierNode(expression[question+1:colon], trueConstraints, multiplier)
	if err != nil {
		return nil, err
	}
	falseConstraints := append([]newAPIPriceCondition{}, inherited...)
	if len(conditions) == 1 {
		falseConstraints = append(falseConstraints, invertNewAPIPriceCondition(conditions[0]))
	}
	right, err := parseNewAPITierNode(expression[colon+1:], falseConstraints, multiplier)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

func findTopLevelByte(value string, target byte) int {
	depth := 0
	quoted := false
	escaped := false
	for index := 0; index < len(value); index++ {
		current := value[index]
		if quoted {
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				quoted = false
			}
			continue
		}
		switch current {
		case '"':
			quoted = true
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return -1
			}
		default:
			if current == target && depth == 0 {
				return index
			}
		}
	}
	return -1
}

func parseNewAPIPriceConditions(raw string) ([]newAPIPriceCondition, error) {
	parts := strings.Split(strings.TrimSpace(raw), "&&")
	if len(parts) == 0 || len(parts) > 4 {
		return nil, errors.New("动态计费条件数量无效")
	}
	conditions := make([]newAPIPriceCondition, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		operator := ""
		position := -1
		for _, candidate := range []string{"<=", ">=", "<", ">"} {
			if found := strings.Index(part, candidate); found >= 0 {
				operator, position = candidate, found
				break
			}
		}
		if position <= 0 {
			return nil, errors.New("动态计费条件格式无效")
		}
		variable := strings.TrimSpace(part[:position])
		if variable != "len" && variable != "p" && variable != "c" {
			return nil, errors.New("动态计费条件变量无效")
		}
		value, err := parseInt64(strings.TrimSpace(part[position+len(operator):]))
		if err != nil || value < 0 {
			return nil, errors.New("动态计费条件边界无效")
		}
		conditions = append(conditions, newAPIPriceCondition{variable: variable, operator: operator, value: value})
	}
	return conditions, nil
}

func invertNewAPIPriceCondition(condition newAPIPriceCondition) newAPIPriceCondition {
	switch condition.operator {
	case "<=":
		condition.operator = ">"
	case "<":
		condition.operator = ">="
	case ">=":
		condition.operator = "<"
	case ">":
		condition.operator = "<="
	}
	return condition
}

func parseNewAPITierLeaf(expression string, conditions []newAPIPriceCondition, multiplier decimal.Decimal) (newAPITierPrice, error) {
	expression = strings.TrimSpace(expression)
	if !strings.HasPrefix(expression, "tier(") || !strings.HasSuffix(expression, ")") {
		return newAPITierPrice{}, errors.New("动态计费分支不是可展示阶梯")
	}
	body := strings.TrimSpace(expression[len("tier(") : len(expression)-1])
	if !strings.HasPrefix(body, "\"") {
		return newAPITierPrice{}, errors.New("动态计费阶梯名称无效")
	}
	labelEnd := -1
	escaped := false
	for index := 1; index < len(body); index++ {
		if escaped {
			escaped = false
			continue
		}
		if body[index] == '\\' {
			escaped = true
			continue
		}
		if body[index] == '"' {
			labelEnd = index
			break
		}
	}
	if labelEnd < 1 || labelEnd+1 >= len(body) || body[labelEnd+1] != ',' {
		return newAPITierPrice{}, errors.New("动态计费阶梯格式无效")
	}
	label, err := strconv.Unquote(body[:labelEnd+1])
	if err != nil {
		return newAPITierPrice{}, errors.New("动态计费阶梯名称无效")
	}
	label = normalizeGroupPriceText(label, 100)
	if label == "" {
		label = "阶梯"
	}
	coefficients, err := parseNewAPILinearPrices(body[labelEnd+2:])
	if err != nil {
		return newAPITierPrice{}, err
	}
	prices := make([]GroupPriceItem, 0, len(coefficients))
	for _, definition := range newAPITierPriceDefinitions() {
		coefficient, exists := coefficients[definition.variable]
		if !exists {
			continue
		}
		value, err := multiplyPrice(coefficient, multiplier)
		if err != nil {
			return newAPITierPrice{}, err
		}
		item, err := newGroupPriceItem(definition.key, definition.label, definition.unit, value)
		if err != nil {
			return newAPITierPrice{}, err
		}
		prices = append(prices, item)
	}
	if len(prices) == 0 {
		return newAPITierPrice{}, errors.New("动态计费阶梯没有价格")
	}
	minimum, maximum, err := newAPITierTokenBounds(conditions)
	if err != nil {
		return newAPITierPrice{}, err
	}
	return newAPITierPrice{
		label: label, condition: newAPITierConditionText(conditions),
		minTokens: minimum, maxTokens: maximum, prices: prices,
	}, nil
}

// newAPITierConditionText 只把已经通过白名单解析的条件转换成说明文字，不返回或执行原始表达式。
func newAPITierConditionText(conditions []newAPIPriceCondition) string {
	labels := map[string]string{"len": "上下文 Token", "p": "输入 Token", "c": "输出 Token"}
	parts := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		label, ok := labels[condition.variable]
		if !ok {
			continue
		}
		parts = append(parts, label+" "+condition.operator+" "+strconv.FormatInt(condition.value, 10))
	}
	return strings.Join(parts, " 且 ")
}

func parseNewAPILinearPrices(expression string) (map[string]decimal.Decimal, error) {
	parts := strings.Split(expression, "+")
	if len(parts) == 0 || len(parts) > 9 {
		return nil, errors.New("动态计费价格项数量无效")
	}
	result := make(map[string]decimal.Decimal, len(parts))
	allowed := map[string]struct{}{"p": {}, "c": {}, "cr": {}, "cc": {}, "cc1h": {}, "img": {}, "img_o": {}, "ai": {}, "ao": {}}
	for _, part := range parts {
		factors := strings.Split(strings.TrimSpace(part), "*")
		if len(factors) != 2 {
			return nil, errors.New("动态计费价格项格式无效")
		}
		variable := strings.TrimSpace(factors[0])
		if _, ok := allowed[variable]; !ok {
			return nil, errors.New("动态计费价格变量无效")
		}
		if _, exists := result[variable]; exists {
			return nil, errors.New("动态计费价格变量重复")
		}
		value, err := parsePriceDecimal(strings.TrimSpace(factors[1]))
		if err != nil {
			return nil, err
		}
		result[variable] = value
	}
	return result, nil
}

type newAPIPriceDefinition struct {
	variable string
	key      string
	label    string
	unit     string
}

func newAPITierPriceDefinitions() []newAPIPriceDefinition {
	return []newAPIPriceDefinition{
		{variable: "p", key: "input", label: "输入", unit: newAPITokenPriceUnit},
		{variable: "c", key: "output", label: "输出", unit: newAPITokenPriceUnit},
		{variable: "cr", key: "cache_read", label: "缓存读取", unit: newAPITokenPriceUnit},
		{variable: "cc", key: "cache_write", label: "缓存写入", unit: newAPITokenPriceUnit},
		{variable: "cc1h", key: "cache_write_1h", label: "1 小时缓存写入", unit: newAPITokenPriceUnit},
		{variable: "img", key: "image_input", label: "图片输入", unit: "USD/百万图片 Token"},
		{variable: "img_o", key: "image_output", label: "图片输出", unit: "USD/百万图片 Token"},
		{variable: "ai", key: "audio_input", label: "音频输入", unit: "USD/百万音频 Token"},
		{variable: "ao", key: "audio_output", label: "音频输出", unit: "USD/百万音频 Token"},
	}
}

func newAPITierTokenBounds(conditions []newAPIPriceCondition) (string, string, error) {
	minimum := int64(0)
	maximum := int64(math.MaxInt64)
	bounded := false
	for _, condition := range conditions {
		if condition.variable != "len" {
			continue
		}
		bounded = true
		switch condition.operator {
		case "<=":
			maximum = min(maximum, condition.value)
		case "<":
			if condition.value == 0 {
				return "", "", errors.New("动态计费阶梯范围为空")
			}
			maximum = min(maximum, condition.value-1)
		case ">=":
			minimum = max(minimum, condition.value)
		case ">":
			if condition.value == math.MaxInt64 {
				return "", "", errors.New("动态计费阶梯范围为空")
			}
			minimum = max(minimum, condition.value+1)
		}
	}
	if bounded && minimum > maximum {
		return "", "", errors.New("动态计费阶梯范围冲突")
	}
	minimumText := ""
	maximumText := ""
	if bounded {
		minimumText = strconv.FormatInt(minimum, 10)
		if maximum != math.MaxInt64 {
			maximumText = strconv.FormatInt(maximum, 10)
		}
	}
	return minimumText, maximumText, nil
}

var _ GroupPriceReader = (*newAPIAdapter)(nil)
