package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"poolwatch/internal/identity"
	"poolwatch/internal/monitor"
	"poolwatch/internal/secure"
)

const (
	targetAuthAttemptLifetime = 10 * time.Minute
	maxImportedCookieBytes    = 16 * 1024
	maxImportedTokenBytes     = 64 * 1024
)

var (
	errTargetAuthAttemptNotFound = errors.New("网页登录尝试不存在或已过期")
	errTargetAuthAttemptNotReady = errors.New("网页登录尚未完成")
	errTargetAuthAttemptReady    = errors.New("网页登录凭据已经捕获")
)

type targetAuthAttempt struct {
	ID                    string
	AdminID               int64
	Kind                  monitor.TargetKind
	BaseURL               string
	LoginURL              string
	Status                string
	CaptureTokenHash      string
	CaptureTokenEncrypted string
	CredentialEncrypted   string
	UserID                string
	Message               string
	ExpiresAt             time.Time
}

type targetAuthAttemptStore struct {
	mu       sync.Mutex
	vault    *secure.Vault
	attempts map[string]targetAuthAttempt
	now      func() time.Time
}

type targetAuthAttemptResponse struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	LoginURL  string    `json:"loginUrl"`
	ExpiresAt time.Time `json:"expiresAt"`
	UserID    string    `json:"userId,omitempty"`
	Message   string    `json:"message,omitempty"`
}

type nativeTargetAuthAttemptResponse struct {
	ID           string             `json:"id"`
	Status       string             `json:"status"`
	Kind         monitor.TargetKind `json:"kind"`
	BaseURL      string             `json:"baseUrl"`
	LoginURL     string             `json:"loginUrl"`
	CaptureToken string             `json:"captureToken"`
	ExpiresAt    time.Time          `json:"expiresAt"`
}

type targetAuthCaptureRequest struct {
	Cookie       string `json:"cookie"`
	UserID       string `json:"userId"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

func newTargetAuthAttemptStore(vault *secure.Vault) *targetAuthAttemptStore {
	return &targetAuthAttemptStore{
		vault:    vault,
		attempts: make(map[string]targetAuthAttempt),
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func (store *targetAuthAttemptStore) create(adminID int64, kind monitor.TargetKind, baseURL, loginURL string) (targetAuthAttempt, error) {
	if store == nil || store.vault == nil {
		return targetAuthAttempt{}, errors.New("网页登录服务未初始化")
	}
	id, err := identity.NewID("auth")
	if err != nil {
		return targetAuthAttempt{}, err
	}
	captureToken, err := identity.RandomToken(32)
	if err != nil {
		return targetAuthAttempt{}, err
	}
	encryptedToken, err := store.vault.Encrypt([]byte(captureToken))
	if err != nil {
		return targetAuthAttempt{}, err
	}
	attempt := targetAuthAttempt{
		ID: id, AdminID: adminID, Kind: kind, BaseURL: baseURL, LoginURL: loginURL,
		Status: "waiting", CaptureTokenHash: identity.HashToken(captureToken), CaptureTokenEncrypted: encryptedToken,
		ExpiresAt: store.now().Add(targetAuthAttemptLifetime),
	}
	store.mu.Lock()
	store.cleanupExpiredLocked()
	store.attempts[id] = attempt
	store.mu.Unlock()
	return attempt, nil
}

func (store *targetAuthAttemptStore) owned(id string, adminID int64) (targetAuthAttempt, error) {
	if store == nil {
		return targetAuthAttempt{}, errTargetAuthAttemptNotFound
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.cleanupExpiredLocked()
	attempt, exists := store.attempts[strings.TrimSpace(id)]
	if !exists || attempt.AdminID != adminID {
		return targetAuthAttempt{}, errTargetAuthAttemptNotFound
	}
	return attempt, nil
}

func (store *targetAuthAttemptStore) native(id string) (targetAuthAttempt, string, error) {
	if store == nil || store.vault == nil {
		return targetAuthAttempt{}, "", errTargetAuthAttemptNotFound
	}
	store.mu.Lock()
	store.cleanupExpiredLocked()
	attempt, exists := store.attempts[strings.TrimSpace(id)]
	store.mu.Unlock()
	if !exists {
		return targetAuthAttempt{}, "", errTargetAuthAttemptNotFound
	}
	if attempt.Status != "waiting" || attempt.CaptureTokenEncrypted == "" {
		return targetAuthAttempt{}, "", errTargetAuthAttemptNotFound
	}
	token, err := store.vault.Decrypt(attempt.CaptureTokenEncrypted)
	if err != nil {
		return targetAuthAttempt{}, "", errors.New("读取网页登录票据失败")
	}
	return attempt, string(token), nil
}

func (store *targetAuthAttemptStore) pendingForCapture(id, captureToken string) (targetAuthAttempt, error) {
	if store == nil {
		return targetAuthAttempt{}, errTargetAuthAttemptNotFound
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.cleanupExpiredLocked()
	attempt, exists := store.attempts[strings.TrimSpace(id)]
	if !exists || !constantTimeHashEqual(attempt.CaptureTokenHash, identity.HashToken(strings.TrimSpace(captureToken))) {
		return targetAuthAttempt{}, errTargetAuthAttemptNotFound
	}
	if attempt.Status == "ready" {
		return targetAuthAttempt{}, errTargetAuthAttemptReady
	}
	if attempt.Status != "waiting" {
		return targetAuthAttempt{}, errTargetAuthAttemptNotFound
	}
	return attempt, nil
}

func (store *targetAuthAttemptStore) markReady(id, captureToken string, credential monitor.Credential) (targetAuthAttempt, error) {
	encoded, err := json.Marshal(credential)
	if err != nil {
		return targetAuthAttempt{}, errors.New("编码网页登录凭据失败")
	}
	encrypted, err := store.vault.Encrypt(encoded)
	if err != nil {
		return targetAuthAttempt{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.cleanupExpiredLocked()
	attempt, exists := store.attempts[strings.TrimSpace(id)]
	if !exists || !constantTimeHashEqual(attempt.CaptureTokenHash, identity.HashToken(strings.TrimSpace(captureToken))) {
		return targetAuthAttempt{}, errTargetAuthAttemptNotFound
	}
	if attempt.Status == "ready" {
		return targetAuthAttempt{}, errTargetAuthAttemptReady
	}
	if attempt.Status != "waiting" {
		return targetAuthAttempt{}, errTargetAuthAttemptNotFound
	}
	attempt.Status = "ready"
	attempt.CredentialEncrypted = encrypted
	attempt.CaptureTokenEncrypted = ""
	attempt.CaptureTokenHash = ""
	attempt.UserID = credential.UserID
	attempt.Message = "网页登录成功，保存渠道后会接管本次凭据。"
	store.attempts[attempt.ID] = attempt
	return attempt, nil
}

func (store *targetAuthAttemptStore) credential(id string, adminID int64, kind monitor.TargetKind, baseURL string) (monitor.Credential, error) {
	attempt, err := store.owned(id, adminID)
	if err != nil {
		return monitor.Credential{}, err
	}
	if attempt.Kind != kind || attempt.BaseURL != baseURL {
		return monitor.Credential{}, errors.New("网页登录尝试与当前渠道不匹配")
	}
	if attempt.Status != "ready" || attempt.CredentialEncrypted == "" {
		return monitor.Credential{}, errTargetAuthAttemptNotReady
	}
	decoded, err := store.vault.Decrypt(attempt.CredentialEncrypted)
	if err != nil {
		return monitor.Credential{}, errors.New("读取网页登录凭据失败")
	}
	var credential monitor.Credential
	if err := json.Unmarshal(decoded, &credential); err != nil {
		return monitor.Credential{}, errors.New("网页登录凭据格式无效")
	}
	return credential, nil
}

func (store *targetAuthAttemptStore) consume(id string, adminID int64) {
	if store == nil || strings.TrimSpace(id) == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	attempt, exists := store.attempts[strings.TrimSpace(id)]
	if exists && attempt.AdminID == adminID {
		delete(store.attempts, attempt.ID)
	}
}

func (store *targetAuthAttemptStore) delete(id string, adminID int64) error {
	if store == nil {
		return errTargetAuthAttemptNotFound
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.cleanupExpiredLocked()
	attempt, exists := store.attempts[strings.TrimSpace(id)]
	if !exists || attempt.AdminID != adminID {
		return errTargetAuthAttemptNotFound
	}
	delete(store.attempts, attempt.ID)
	return nil
}

func (store *targetAuthAttemptStore) cleanupExpiredLocked() {
	now := store.now()
	for id, attempt := range store.attempts {
		if attempt.ExpiresAt.Add(targetAuthAttemptLifetime).Before(now) {
			delete(store.attempts, id)
			continue
		}
		if !attempt.ExpiresAt.After(now) && attempt.Status != "expired" {
			attempt.Status = "expired"
			attempt.CaptureTokenEncrypted = ""
			attempt.CaptureTokenHash = ""
			attempt.CredentialEncrypted = ""
			attempt.UserID = ""
			attempt.Message = "网页登录任务已经过期，请重新准备。"
			store.attempts[id] = attempt
		}
	}
}

func constantTimeHashEqual(first, second string) bool {
	if len(first) != len(second) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(first), []byte(second)) == 1
}

func (s *Server) handleCreateTargetAuthAttempt(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Kind    string `json:"kind"`
		BaseURL string `json:"baseUrl"`
	}
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	kind := monitor.TargetKind(strings.TrimSpace(body.Kind))
	if kind != monitor.TargetKindNewAPI && kind != monitor.TargetKindSub2API {
		writeAPIError(response, http.StatusBadRequest, "该渠道不支持网页登录")
		return
	}
	baseURL, err := normalizeTargetBaseURL(body.BaseURL)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, "渠道地址无效")
		return
	}
	validationContext, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	if err := monitor.ValidateTargetURL(validationContext, baseURL, s.dependencies.AllowPrivateTargets); err != nil {
		writeAPIError(response, http.StatusUnprocessableEntity, err.Error())
		return
	}
	attempt, err := s.targetAuth.create(adminFromContext(request.Context()).ID, kind, baseURL, baseURL)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "创建网页登录尝试失败")
		return
	}
	writeJSON(response, http.StatusCreated, mapTargetAuthAttempt(attempt))
}

func (s *Server) handleTargetAuthAttempt(response http.ResponseWriter, request *http.Request) {
	attempt, err := s.targetAuth.owned(request.PathValue("id"), adminFromContext(request.Context()).ID)
	if err != nil {
		writeAPIError(response, http.StatusNotFound, errTargetAuthAttemptNotFound.Error())
		return
	}
	writeJSON(response, http.StatusOK, mapTargetAuthAttempt(attempt))
}

func (s *Server) handleDeleteTargetAuthAttempt(response http.ResponseWriter, request *http.Request) {
	if err := s.targetAuth.delete(request.PathValue("id"), adminFromContext(request.Context()).ID); err != nil {
		writeAPIError(response, http.StatusNotFound, errTargetAuthAttemptNotFound.Error())
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNativeTargetAuthAttempt(response http.ResponseWriter, request *http.Request) {
	attempt, captureToken, err := s.targetAuth.native(request.PathValue("id"))
	if err != nil {
		writeAPIError(response, http.StatusNotFound, errTargetAuthAttemptNotFound.Error())
		return
	}
	writeJSON(response, http.StatusOK, nativeTargetAuthAttemptResponse{
		ID: attempt.ID, Status: attempt.Status, Kind: attempt.Kind, BaseURL: attempt.BaseURL,
		LoginURL: attempt.LoginURL, CaptureToken: captureToken, ExpiresAt: attempt.ExpiresAt,
	})
}

func (s *Server) handleCaptureTargetAuthAttempt(response http.ResponseWriter, request *http.Request) {
	captureToken := strings.TrimSpace(request.Header.Get("X-Target-Auth-Token"))
	if captureToken == "" {
		writeAPIError(response, http.StatusUnauthorized, "缺少网页登录捕获票据")
		return
	}
	attempt, err := s.targetAuth.pendingForCapture(request.PathValue("id"), captureToken)
	if errors.Is(err, errTargetAuthAttemptReady) {
		writeAPIError(response, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeAPIError(response, http.StatusUnauthorized, errTargetAuthAttemptNotFound.Error())
		return
	}
	var body targetAuthCaptureRequest
	if err := decodeJSON(response, request, &body); err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	credential, err := capturedCredential(attempt.Kind, body)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, err.Error())
		return
	}
	verified, err := s.dependencies.Scheduler.VerifyBrowserCredential(request.Context(), monitor.TargetConfig{
		ID: attempt.ID, Kind: attempt.Kind, BaseURL: attempt.BaseURL, Credential: credential,
	})
	if err != nil {
		writeAPIError(response, http.StatusUnprocessableEntity, err.Error())
		return
	}
	attempt, err = s.targetAuth.markReady(attempt.ID, captureToken, verified)
	if errors.Is(err, errTargetAuthAttemptReady) {
		writeAPIError(response, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeAPIError(response, http.StatusUnauthorized, errTargetAuthAttemptNotFound.Error())
		return
	}
	writeJSON(response, http.StatusOK, mapTargetAuthAttempt(attempt))
}

func capturedCredential(kind monitor.TargetKind, body targetAuthCaptureRequest) (monitor.Credential, error) {
	switch kind {
	case monitor.TargetKindNewAPI:
		cookie := strings.TrimSpace(body.Cookie)
		userID := strings.TrimSpace(body.UserID)
		if cookie == "" {
			return monitor.Credential{}, errors.New("New API 网页登录需要会话 Cookie")
		}
		if len(cookie) > maxImportedCookieBytes {
			return monitor.Credential{}, errors.New("网页登录会话超过 16 KB 限制")
		}
		if strings.ContainsAny(cookie, "\r\n") {
			return monitor.Credential{}, errors.New("网页登录会话格式无效")
		}
		return monitor.Credential{Cookie: cookie, UserID: userID}, nil
	case monitor.TargetKindSub2API:
		accessToken := strings.TrimSpace(body.AccessToken)
		refreshToken := strings.TrimSpace(body.RefreshToken)
		if accessToken == "" {
			return monitor.Credential{}, errors.New("Sub2API 网页登录缺少访问令牌")
		}
		if len(accessToken) > maxImportedTokenBytes || len(refreshToken) > maxImportedTokenBytes {
			return monitor.Credential{}, errors.New("网页登录令牌超过 64 KB 限制")
		}
		if strings.ContainsAny(accessToken+refreshToken, "\r\n") {
			return monitor.Credential{}, errors.New("网页登录令牌格式无效")
		}
		return monitor.Credential{AccessToken: accessToken, RefreshToken: refreshToken}, nil
	default:
		return monitor.Credential{}, errors.New("该渠道不支持网页登录")
	}
}

func mapTargetAuthAttempt(attempt targetAuthAttempt) targetAuthAttemptResponse {
	return targetAuthAttemptResponse{
		ID: attempt.ID, Status: attempt.Status, LoginURL: attempt.LoginURL, ExpiresAt: attempt.ExpiresAt,
		UserID: attempt.UserID, Message: attempt.Message,
	}
}

func normalizeTargetBaseURL(raw string) (string, error) {
	validated, err := validateHTTPURL(raw, true)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(validated)
	if err != nil {
		return "", err
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}
