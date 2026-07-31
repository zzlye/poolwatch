package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"poolwatch/internal/monitor"
	"poolwatch/internal/scheduler"
	"poolwatch/internal/store"
)

func (s *Server) handleGroupMultiplierState(response http.ResponseWriter, request *http.Request) {
	target, err := s.groupMultiplierTarget(request.Context(), request.PathValue("id"))
	if err != nil {
		s.writeGroupMultiplierError(response, err)
		return
	}
	state, err := s.mapGroupMultiplierState(request.Context(), target, nil)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取倍率监控失败")
		return
	}
	writeJSON(response, http.StatusOK, state)
}

func (s *Server) handleDetectGroupMultipliers(response http.ResponseWriter, request *http.Request) {
	s.handleGroupMultiplierCheck(response, request)
}

func (s *Server) handleCheckGroupMultipliers(response http.ResponseWriter, request *http.Request) {
	s.handleGroupMultiplierCheck(response, request)
}

func (s *Server) handleGroupMultiplierCheck(response http.ResponseWriter, request *http.Request) {
	target, err := s.groupMultiplierTarget(request.Context(), request.PathValue("id"))
	if err != nil {
		s.writeGroupMultiplierError(response, err)
		return
	}
	groups, err := s.dependencies.Scheduler.DetectGroupMultipliers(request.Context(), target.ID)
	if err != nil {
		s.writeGroupMultiplierError(response, err)
		return
	}
	state, err := s.mapGroupMultiplierState(request.Context(), target, groups)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取倍率检测结果失败")
		return
	}
	writeJSON(response, http.StatusOK, state)
}

func (s *Server) handleSaveGroupMultipliers(response http.ResponseWriter, request *http.Request) {
	var input groupMultiplierSelectionRequest
	if err := decodeJSON(response, request, &input); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	target, err := s.groupMultiplierTarget(request.Context(), request.PathValue("id"))
	if err != nil {
		s.writeGroupMultiplierError(response, err)
		return
	}
	groups, err := s.dependencies.Scheduler.SaveGroupMultiplierSelection(request.Context(), target.ID, input.GroupKeys)
	if err != nil {
		s.writeGroupMultiplierError(response, err)
		return
	}
	_ = s.dependencies.Store.AddAuditEvent(request.Context(), "multiplier.selection.updated", target.ID, "更新倍率监控分组", time.Now().UTC())
	state, err := s.mapGroupMultiplierState(request.Context(), target, groups)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取倍率监控结果失败")
		return
	}
	writeJSON(response, http.StatusOK, state)
}

func (s *Server) groupMultiplierTarget(ctx context.Context, targetID string) (store.Target, error) {
	target, err := s.dependencies.Store.TargetByID(ctx, targetID)
	if err != nil {
		return store.Target{}, err
	}
	if target.Kind != string(monitor.TargetKindNewAPI) && target.Kind != string(monitor.TargetKindSub2API) {
		return store.Target{}, errors.New("该渠道不支持倍率监控")
	}
	return target, nil
}

func (s *Server) mapGroupMultiplierState(ctx context.Context, target store.Target, detected []monitor.GroupMultiplier) (targetMultiplierStateResponse, error) {
	monitors, err := s.dependencies.Store.ListGroupMultiplierMonitors(ctx, target.ID)
	if err != nil {
		return targetMultiplierStateResponse{}, err
	}
	byKey := make(map[string]store.GroupMultiplierMonitor, len(monitors))
	for _, item := range monitors {
		byKey[item.GroupKey] = item
	}
	groups := make([]groupMultiplierResponse, 0, len(monitors)+len(detected))
	seen := make(map[string]struct{}, len(detected))
	for _, live := range detected {
		seen[live.Key] = struct{}{}
		if item, exists := byKey[live.Key]; exists {
			groups = append(groups, mapStoredGroupMultiplier(item))
			continue
		}
		groups = append(groups, groupMultiplierResponse{
			Key: live.Key, Name: live.Name, Description: live.Description,
			Multiplier: live.Multiplier.String(), Monitored: false, Status: "unknown",
		})
	}
	for _, item := range monitors {
		if _, exists := seen[item.GroupKey]; exists {
			continue
		}
		groups = append(groups, mapStoredGroupMultiplier(item))
	}
	sort.Slice(groups, func(left, right int) bool {
		if groups[left].Name == groups[right].Name {
			return groups[left].Key < groups[right].Key
		}
		return groups[left].Name < groups[right].Name
	})
	state := targetMultiplierStateResponse{
		TargetID: target.ID, TargetName: target.Name, TargetKind: target.Kind, Enabled: target.Enabled, Groups: groups,
	}
	for _, item := range monitors {
		if item.LastCheckedAt.After(timeValue(state.LastCheckedAt)) {
			state.LastCheckedAt = apiTimePointer(item.LastCheckedAt)
		}
		if state.LastError == "" && item.LastError != "" {
			state.LastError = item.LastError
		}
	}
	return state, nil
}

func mapStoredGroupMultiplier(item store.GroupMultiplierMonitor) groupMultiplierResponse {
	status := "stable"
	if item.CurrentMultiplier == "" {
		status = "unknown"
	} else if item.Missing {
		status = "missing"
	} else if item.LastError != "" {
		status = "unknown"
	} else if !item.LastChangedAt.IsZero() && item.LastChangedAt.Equal(item.LastCheckedAt) {
		status = "changed"
	}
	return groupMultiplierResponse{
		Key: item.GroupKey, Name: item.GroupName, Description: item.Description,
		Multiplier: item.CurrentMultiplier, PreviousMultiplier: item.PreviousMultiplier,
		Monitored: true, Status: status, LastCheckedAt: apiTimePointer(item.LastCheckedAt),
		ChangedAt: apiTimePointer(item.LastChangedAt), LastError: item.LastError,
	}
}

func (s *Server) writeGroupMultiplierError(response http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(response, http.StatusNotFound, "渠道不存在")
		return
	}
	if errors.Is(err, scheduler.ErrAlreadyRunning) {
		writeAPIError(response, http.StatusConflict, "该渠道正在检测，请稍后重试")
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeAPIError(response, http.StatusGatewayTimeout, "倍率检测超时，请稍后重试")
		return
	}
	var classified *monitor.CheckError
	if errors.As(err, &classified) {
		switch classified.Kind {
		case monitor.ErrorClassAuth:
			writeAPIError(response, http.StatusUnprocessableEntity, "渠道登录已失效，请先更新登录信息")
		case monitor.ErrorClassNetwork, monitor.ErrorClassServer:
			writeAPIError(response, http.StatusBadGateway, "暂时无法连接渠道倍率接口")
		case monitor.ErrorClassResponse, monitor.ErrorClassRemote:
			writeAPIError(response, http.StatusBadGateway, "渠道返回的分组倍率无法识别")
		case monitor.ErrorClassConfig:
			writeAPIError(response, http.StatusBadRequest, "渠道登录或倍率监控配置不完整")
		}
		return
	}
	message := err.Error()
	if strings.Contains(message, "已不在本次检测结果") {
		writeAPIError(response, http.StatusConflict, "分组列表已经变化，请重新检测后再保存")
	} else if strings.Contains(message, "最多") || strings.Contains(message, "重复") || strings.Contains(message, "标识") || strings.Contains(message, "不支持倍率监控") {
		writeAPIError(response, http.StatusBadRequest, message)
	} else {
		writeAPIError(response, http.StatusInternalServerError, "倍率监控操作失败")
	}
}

func apiTimePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copyValue := value
	return &copyValue
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
