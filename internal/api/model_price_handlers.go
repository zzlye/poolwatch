package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"poolwatch/internal/scheduler"
	"poolwatch/internal/store"
)

// handleModelPrices 提供独立价格监控入口，既不读取也不修改倍率选择。
func (s *Server) handleModelPrices(w http.ResponseWriter, r *http.Request) {
	target, err := s.dependencies.Store.TargetByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeGroupPriceError(w, err)
		return
	}
	if target.Kind != "new_api" && target.Kind != "sub2api" {
		writeAPIError(w, http.StatusBadRequest, "只有 New API 和 Sub2API 渠道支持模型价格监控")
		return
	}
	root := "/api/targets/" + target.ID + "/model-prices"
	switch {
	case strings.HasSuffix(r.URL.Path, "/groups"):
		groups, e := s.dependencies.Scheduler.DiscoverPriceGroups(r.Context(), target.ID)
		if e != nil {
			s.writeModelPriceError(w, e)
			return
		}
		items := make([]map[string]string, 0, len(groups))
		for _, g := range groups {
			items = append(items, map[string]string{"key": g.Key, "name": g.Name})
		}
		writeJSON(w, http.StatusOK, items)
		return
	case strings.HasSuffix(r.URL.Path, "/catalog"):
		key, ok := modelPriceGroupKey(w, r)
		if !ok {
			return
		}
		catalog, e := s.dependencies.Scheduler.ReadModelPriceCatalog(r.Context(), target.ID, key)
		if e != nil {
			s.writeModelPriceError(w, e)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"groupKey": catalog.GroupKey, "groupName": catalog.GroupName, "multiplier": catalog.Multiplier.String(), "models": scheduler.ModelPriceObservations(catalog), "notice": catalog.Notice})
		return
	case r.Method == http.MethodPut && r.URL.Path == root:
		var input struct {
			GroupKey string   `json:"groupKey"`
			Models   []string `json:"models"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			writeAPIError(w, http.StatusBadRequest, "价格监控配置格式无效")
			return
		}
		if input.Models == nil || strings.TrimSpace(input.GroupKey) == "" || len(input.GroupKey) > 200 {
			writeAPIError(w, http.StatusBadRequest, "请指定分组和模型列表；空列表用于取消该分组监控")
			return
		}
		if e := s.dependencies.Scheduler.SaveModelPriceSelection(r.Context(), target.ID, input.GroupKey, input.Models); e != nil {
			s.writeModelPriceError(w, e)
			return
		}
		_ = s.dependencies.Store.AddAuditEvent(r.Context(), "price.selection.updated", target.ID, "更新独立模型价格监控", time.Now().UTC())
	case strings.HasSuffix(r.URL.Path, "/check"):
		if e := s.dependencies.Scheduler.CheckModelPrices(r.Context(), target.ID); e != nil {
			s.writeModelPriceError(w, e)
			return
		}
	}
	items, err := s.dependencies.Store.ListModelPriceMonitors(r.Context(), target.ID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "读取价格监控失败")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func modelPriceGroupKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.URL.Query()["groupKey"]
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" || len(values[0]) > 200 {
		writeAPIError(w, http.StatusBadRequest, "请指定一个有效分组")
		return "", false
	}
	return values[0], true
}

func (s *Server) writeModelPriceError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrPriceSelection) {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeGroupPriceError(w, err)
}
