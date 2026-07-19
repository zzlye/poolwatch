package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"poolwatch/internal/auth"
)

func (s *Server) handleBootstrap(response http.ResponseWriter, request *http.Request) {
	initialized, err := s.dependencies.Auth.Initialized(request.Context())
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "读取初始化状态失败")
		return
	}
	productName, _ := s.dependencies.Store.GetSetting(request.Context(), "product_name")
	if productName == "" {
		productName = "号池监控"
	}
	result := bootstrapResponse{Initialized: initialized, ProductName: productName}
	if initialized {
		if cookie, err := request.Cookie(sessionCookieName); err == nil {
			admin, _, authErr := s.dependencies.Auth.Authenticate(request.Context(), cookie.Value)
			if authErr == nil {
				result.Authenticated = true
				result.TOTPEnabled = admin.TOTPEnabled
			}
		}
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Server) handleSetup(response http.ResponseWriter, request *http.Request) {
	key := "setup:" + clientAddress(request)
	if !s.limiter.Allow(key, time.Now()) {
		writeAPIError(response, http.StatusTooManyRequests, "尝试次数过多，请稍后再试")
		return
	}
	var body struct {
		InitializationToken string `json:"initializationToken"`
		Username            string `json:"username"`
		Password            string `json:"password"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.dependencies.Auth.Setup(request.Context(), body.InitializationToken, body.Username, body.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrAlreadyInitialized):
			writeAPIError(response, http.StatusConflict, err.Error())
		case errors.Is(err, auth.ErrSetupToken):
			writeAPIError(response, http.StatusUnauthorized, err.Error())
		default:
			writeAPIError(response, http.StatusBadRequest, err.Error())
		}
		return
	}
	s.limiter.Reset(key)
	s.setSessionCookie(response, result)
	writeJSON(response, http.StatusCreated, map[string]bool{"ok": true})
}

func (s *Server) handleLogin(response http.ResponseWriter, request *http.Request) {
	key := "login:" + clientAddress(request)
	if !s.limiter.Allow(key, time.Now()) {
		writeAPIError(response, http.StatusTooManyRequests, "尝试次数过多，请稍后再试")
		return
	}
	var body struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		SecondFactor string `json:"secondFactor"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.dependencies.Auth.Login(request.Context(), body.Username, body.Password, body.SecondFactor)
	if err != nil {
		writeAPIError(response, http.StatusUnauthorized, err.Error())
		return
	}
	s.limiter.Reset(key)
	s.setSessionCookie(response, result)
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(response http.ResponseWriter, request *http.Request) {
	if cookie, err := request.Cookie(sessionCookieName); err == nil {
		_ = s.dependencies.Auth.Logout(request.Context(), cookie.Value)
	}
	s.clearSessionCookie(response)
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTOTPStart(response http.ResponseWriter, request *http.Request) {
	setup, err := s.dependencies.Auth.StartTOTP(request.Context(), adminFromContext(request.Context()))
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, "生成动态验证码配置失败")
		return
	}
	setup.OTPAuthURL = auth.SanitizeOTPAuthURL(setup.OTPAuthURL)
	writeJSON(response, http.StatusOK, setup)
}

func (s *Server) handleTOTPConfirm(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	codes, err := s.dependencies.Auth.ConfirmTOTP(request.Context(), body.Code)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	s.dependencies.Events.Publish("settings.updated", map[string]bool{"totpEnabled": true})
	writeJSON(response, http.StatusOK, map[string][]string{"recoveryCodes": codes})
}

func (s *Server) handleTOTPDisable(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	admin := adminFromContext(request.Context())
	if err := s.dependencies.Auth.DisableTOTP(request.Context(), admin, body.Code); err != nil {
		writeAPIError(response, http.StatusUnauthorized, err.Error())
		return
	}
	s.dependencies.Events.Publish("settings.updated", map[string]bool{"totpEnabled": false})
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePasswordChange(response http.ResponseWriter, request *http.Request) {
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
		SecondFactor    string `json:"secondFactor"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.NewPassword) == "" {
		writeAPIError(response, http.StatusBadRequest, "新密码不能为空")
		return
	}
	if err := s.dependencies.Auth.ChangePassword(request.Context(), adminFromContext(request.Context()), body.CurrentPassword, body.NewPassword, body.SecondFactor); err != nil {
		writeAPIError(response, http.StatusUnauthorized, err.Error())
		return
	}
	s.clearSessionCookie(response)
	response.WriteHeader(http.StatusNoContent)
}
