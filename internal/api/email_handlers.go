package api

import (
	"errors"
	"net/http"

	"poolwatch/internal/mailnotify"
)

// handleEmailSettings 返回脱敏后的邮箱提醒配置。
func (s *Server) handleEmailSettings(response http.ResponseWriter, request *http.Request) {
	if s.dependencies.Email == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "邮箱提醒服务尚未就绪")
		return
	}
	settings, err := s.dependencies.Email.Settings(request.Context())
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取邮箱配置失败")
		return
	}
	writeJSON(response, http.StatusOK, settings)
}

// handleUpdateEmailSettings 校验并保存完整邮箱提醒配置。
func (s *Server) handleUpdateEmailSettings(response http.ResponseWriter, request *http.Request) {
	if s.dependencies.Email == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "邮箱提醒服务尚未就绪")
		return
	}
	var body mailnotify.Config
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	settings, err := s.dependencies.Email.Save(request.Context(), body)
	if err != nil {
		if errors.Is(err, mailnotify.ErrInvalidConfig) {
			writeAPIError(response, http.StatusBadRequest, mailnotify.ConfigErrorMessage(err))
			return
		}
		writeAPIError(response, http.StatusInternalServerError, "保存邮箱配置失败")
		return
	}
	if s.dependencies.Events != nil {
		s.dependencies.Events.Publish("settings.updated", map[string]bool{"emailUpdated": true})
	}
	writeJSON(response, http.StatusOK, settings)
}

// handleDeleteEmailSettings 清除邮箱设置及其加密授权码。
func (s *Server) handleDeleteEmailSettings(response http.ResponseWriter, request *http.Request) {
	if s.dependencies.Email == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "邮箱提醒服务尚未就绪")
		return
	}
	settings, err := s.dependencies.Email.Clear(request.Context())
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "清除邮箱配置失败")
		return
	}
	if s.dependencies.Events != nil {
		s.dependencies.Events.Publish("settings.updated", map[string]bool{"emailUpdated": true})
	}
	writeJSON(response, http.StatusOK, settings)
}

// handleEmailTest 使用当前草稿发送测试邮件，但不保存草稿。
func (s *Server) handleEmailTest(response http.ResponseWriter, request *http.Request) {
	if s.dependencies.Email == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "邮箱提醒服务尚未就绪")
		return
	}
	var body mailnotify.Config
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.dependencies.Email.Test(request.Context(), body); err != nil {
		if errors.Is(err, mailnotify.ErrInvalidConfig) {
			writeAPIError(response, http.StatusBadRequest, mailnotify.ConfigErrorMessage(err))
			return
		}
		s.dependencies.Logger.Warn("测试邮件发送失败", "error", err.Error())
		writeAPIError(response, http.StatusBadGateway, "测试邮件发送失败，请检查服务器、端口、加密方式和授权码")
		return
	}
	response.WriteHeader(http.StatusNoContent)
}
