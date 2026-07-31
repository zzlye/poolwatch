package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"poolwatch/internal/monitor"
	"poolwatch/internal/scheduler"
)

func (s *Server) handleGroupPrices(response http.ResponseWriter, request *http.Request) {
	values, exists := request.URL.Query()["groupKey"]
	if !exists || len(values) != 1 {
		writeAPIError(response, http.StatusBadRequest, "请指定一个已监控分组")
		return
	}
	groupKey := strings.TrimSpace(values[0])
	if groupKey == "" || len(groupKey) > 200 {
		writeAPIError(response, http.StatusBadRequest, "分组标识无效")
		return
	}
	target, err := s.groupMultiplierTarget(request.Context(), request.PathValue("id"))
	if err != nil {
		s.writeGroupPriceError(response, err)
		return
	}
	monitors, err := s.dependencies.Store.ListGroupMultiplierMonitors(request.Context(), target.ID)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取倍率监控失败")
		return
	}
	monitored := false
	for _, item := range monitors {
		if item.GroupKey == groupKey {
			monitored = true
			break
		}
	}
	if !monitored {
		writeAPIError(response, http.StatusNotFound, "该分组尚未加入倍率监控")
		return
	}
	catalog, err := s.dependencies.Scheduler.ReadGroupPrices(request.Context(), target.ID, groupKey)
	if err != nil {
		s.writeGroupPriceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, mapTargetGroupPrice(target.ID, catalog))
}

func mapTargetGroupPrice(targetID string, catalog monitor.GroupPriceCatalog) targetGroupPriceResponse {
	result := targetGroupPriceResponse{
		TargetID: targetID, GroupKey: catalog.GroupKey, GroupName: catalog.GroupName,
		Multiplier: catalog.Multiplier.String(), Models: make([]groupModelPriceResponse, 0, len(catalog.Models)),
		Notice: catalog.Notice,
	}
	for _, model := range catalog.Models {
		mapped := groupModelPriceResponse{
			Name: model.Name, BillingMode: model.BillingMode, Prices: mapGroupPriceItems(model.Prices),
			Intervals: make([]groupPriceIntervalResponse, 0, len(model.Intervals)), Note: model.Note,
		}
		for _, interval := range model.Intervals {
			mapped.Intervals = append(mapped.Intervals, groupPriceIntervalResponse{
				Label: interval.Label, Condition: interval.Condition,
				MinTokens: interval.MinTokens, MaxTokens: interval.MaxTokens,
				Prices: mapGroupPriceItems(interval.Prices),
			})
		}
		result.Models = append(result.Models, mapped)
	}
	return result
}

func mapGroupPriceItems(items []monitor.GroupPriceItem) []groupPriceItemResponse {
	result := make([]groupPriceItemResponse, 0, len(items))
	for _, item := range items {
		result = append(result, groupPriceItemResponse{
			Key: item.Key, Label: item.Label, Value: item.Value.String(), Unit: item.Unit,
		})
	}
	return result
}

func (s *Server) writeGroupPriceError(response http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(response, http.StatusNotFound, "渠道不存在")
		return
	}
	if errors.Is(err, scheduler.ErrAlreadyRunning) {
		writeAPIError(response, http.StatusConflict, "该渠道正在检测，请稍后重试")
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeAPIError(response, http.StatusGatewayTimeout, "价格读取超时，请稍后重试")
		return
	}
	if strings.Contains(err.Error(), "尚未加入倍率监控") {
		writeAPIError(response, http.StatusNotFound, "该分组尚未加入倍率监控")
		return
	}
	if strings.Contains(err.Error(), "只有 New API") || strings.Contains(err.Error(), "不支持分组价格") {
		writeAPIError(response, http.StatusBadRequest, "该渠道不支持分组价格展示")
		return
	}
	var classified *monitor.CheckError
	if errors.As(err, &classified) {
		switch classified.Kind {
		case monitor.ErrorClassAuth:
			writeAPIError(response, http.StatusUnprocessableEntity, "渠道登录已失效，请先更新登录信息")
		case monitor.ErrorClassNetwork, monitor.ErrorClassServer:
			writeAPIError(response, http.StatusBadGateway, "暂时无法连接渠道价格接口")
		case monitor.ErrorClassRemote:
			if strings.Contains(classified.Message, "未公开") {
				writeAPIError(response, http.StatusUnprocessableEntity, "当前站点未公开可读取的模型价格")
			} else {
				writeAPIError(response, http.StatusBadGateway, "渠道暂时无法提供模型价格")
			}
		case monitor.ErrorClassResponse:
			writeAPIError(response, http.StatusBadGateway, "渠道返回的模型价格无法识别")
		case monitor.ErrorClassConfig:
			writeAPIError(response, http.StatusBadRequest, "渠道登录或分组配置不完整")
		default:
			writeAPIError(response, http.StatusBadGateway, "渠道暂时无法提供模型价格")
		}
		return
	}
	writeAPIError(response, http.StatusInternalServerError, "读取分组价格失败")
}
