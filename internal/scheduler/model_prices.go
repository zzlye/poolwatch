package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"poolwatch/internal/monitor"
	"poolwatch/internal/store"
)

// withPriceTarget 与钱包、倍率及渠道编辑共用锁，但价格选择和基准完全独立。
func (s *Service) withPriceTarget(ctx context.Context, id string, action func(*store.Target) error) error {
	if !s.acquireTarget(id) {
		return ErrAlreadyRunning
	}
	defer s.releaseTarget(id)
	select {
	case s.semaphore <- struct{}{}:
		defer func() { <-s.semaphore }()
	case <-ctx.Done():
		return ctx.Err()
	}
	target, err := s.store.TargetByID(ctx, id)
	if err != nil {
		return err
	}
	if !supportsGroupMultipliers(target.Kind) {
		return fmt.Errorf("%w：只有 New API 和 Sub2API 支持模型价格监控", store.ErrPriceSelection)
	}
	return action(&target)
}

// DiscoverPriceGroups 只发现价格可选分组，不保存或检测倍率监控。
func (s *Service) DiscoverPriceGroups(ctx context.Context, id string) (groups []monitor.GroupMultiplier, err error) {
	err = s.withPriceTarget(ctx, id, func(target *store.Target) error {
		var e error
		groups, e = s.readGroupMultipliersUnlocked(ctx, target)
		return e
	})
	return
}

// ReadModelPriceCatalog 不要求已启用倍率监控，直接读取当前账号可访问分组的价格。
func (s *Service) ReadModelPriceCatalog(ctx context.Context, id, key string) (catalog monitor.GroupPriceCatalog, err error) {
	err = s.withPriceTarget(ctx, id, func(target *store.Target) error {
		var e error
		catalog, e = s.readModelPriceCatalogUnlocked(ctx, target, key)
		return e
	})
	return
}

func (s *Service) readModelPriceCatalogUnlocked(ctx context.Context, target *store.Target, key string) (monitor.GroupPriceCatalog, error) {
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		return monitor.GroupPriceCatalog{}, fmt.Errorf("%w：分组标识无效", store.ErrPriceSelection)
	}
	config, err := s.runtimeConfig(*target)
	if err != nil {
		return monitor.GroupPriceCatalog{}, err
	}
	reader, ok := s.runner.(monitor.GroupPriceReader)
	if !ok {
		return monitor.GroupPriceCatalog{}, errors.New("当前检测器不支持分组价格")
	}
	timeout, cancel := context.WithTimeout(ctx, s.checkTimeout)
	defer cancel()
	result, err := reader.ReadGroupPrices(timeout, config, key)
	if result.CredentialUpdate != nil {
		if persistErr := s.persistCredentialUpdate(ctx, target, result.CredentialUpdate); persistErr != nil {
			return monitor.GroupPriceCatalog{}, errors.Join(err, persistErr)
		}
	}
	if err != nil {
		return monitor.GroupPriceCatalog{}, err
	}
	if result.Catalog.GroupKey != key {
		return monitor.GroupPriceCatalog{}, errors.New("渠道返回的价格分组与请求不一致")
	}
	return result.Catalog, nil
}

// ModelPriceObservations 将不同计费档位归并到一个模型；比较时计费方式、档位和单位都参与判断。
func ModelPriceObservations(catalog monitor.GroupPriceCatalog) []store.ModelPriceObservation {
	models := make(map[string][]store.PricePoint)
	for _, model := range catalog.Models {
		if _, ok := models[model.Name]; !ok {
			models[model.Name] = []store.PricePoint{}
		}
		appendItems := func(rangeID, rangeLabel string, items []monitor.GroupPriceItem) {
			for _, item := range items {
				key, _ := json.Marshal([]string{model.BillingMode, rangeID, item.Key})
				models[model.Name] = append(models[model.Name], store.PricePoint{Key: string(key), Label: item.Label, Range: rangeLabel, Value: item.Value.String(), Unit: item.Unit})
			}
		}
		label := "通用价格"
		switch model.BillingMode {
		case "request", "per_request":
			label = "按次"
		case "second", "per_second":
			label = "按秒"
		case "image", "per_image":
			label = "按图片"
		}
		appendItems("base", label, model.Prices)
		for _, interval := range model.Intervals {
			id, _ := json.Marshal([]string{interval.MinTokens, interval.MaxTokens, interval.Condition, interval.Label})
			label := interval.Label
			if interval.Condition != "" {
				label += "（" + interval.Condition + "）"
			}
			appendItems(string(id), label, interval.Prices)
		}
	}
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]store.ModelPriceObservation, 0, len(names))
	for _, name := range names {
		result = append(result, store.ModelPriceObservation{Name: name, Prices: models[name]})
	}
	return result
}

// SaveModelPriceSelection 重新读取真实价格后保存选择，禁止使用浏览器提交的价格作为基准。
func (s *Service) SaveModelPriceSelection(ctx context.Context, id, key string, names []string) error {
	return s.withPriceTarget(ctx, id, func(target *store.Target) error {
		if len(names) > 500 {
			return fmt.Errorf("%w：每个分组最多选择 500 个模型", store.ErrPriceSelection)
		}
		catalog := monitor.GroupPriceCatalog{GroupKey: key}
		if len(names) > 0 {
			var err error
			catalog, err = s.readModelPriceCatalogUnlocked(ctx, target, key)
			if err != nil {
				return err
			}
		}
		if err := s.store.SyncModelPriceSelection(ctx, id, key, catalog.GroupName, names, ModelPriceObservations(catalog), s.now()); err != nil {
			return err
		}
		s.onPrice(id)
		return nil
	})
}

// CheckModelPrices 手动刷新当前渠道的价格，不检测钱包或修改倍率监控。
func (s *Service) CheckModelPrices(ctx context.Context, id string) error {
	return s.withPriceTarget(ctx, id, func(target *store.Target) error { return s.checkModelPricesUnlocked(ctx, target) })
}

func (s *Service) checkModelPricesUnlocked(ctx context.Context, target *store.Target) error {
	items, err := s.store.ListModelPriceMonitors(ctx, target.ID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	keys := []string{}
	seen := map[string]bool{}
	for _, item := range items {
		if !seen[item.GroupKey] {
			keys = append(keys, item.GroupKey)
			seen[item.GroupKey] = true
		}
	}
	// 一轮独立价格检测有总时限，避免大量分组长期占用渠道锁。
	budget, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	failures := []error{}
	for _, key := range keys {
		catalog, err := s.readModelPriceCatalogUnlocked(budget, target, key)
		if err == nil {
			var alert *store.Alert
			alert, err = s.store.ApplyModelPriceCheck(ctx, target.ID, key, catalog.GroupName, ModelPriceObservations(catalog), s.now())
			if err == nil {
				s.alerts.NotifyPriceChange(ctx, *target, alert)
			}
		}
		if err != nil {
			message := "价格检测失败，保留上次有效价格。"
			if monitor.IsAuthFailure(err) {
				message = "渠道登录已失效，请更新登录信息。"
			}
			markErr := s.store.MarkModelPriceFailure(ctx, target.ID, key, message, s.now())
			failures = append(failures, errors.Join(err, markErr))
		}
	}
	s.onPrice(target.ID)
	return errors.Join(failures...)
}

// SetPriceHandler 设置独立价格状态刷新事件，避免使倍率缓存产生无关失效。
func (s *Service) SetPriceHandler(handler func(string)) {
	if handler != nil {
		s.onPrice = handler
	}
}
