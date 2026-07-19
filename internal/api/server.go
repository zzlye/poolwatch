package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"poolwatch/internal/auth"
	"poolwatch/internal/events"
	"poolwatch/internal/push"
	"poolwatch/internal/scheduler"
	"poolwatch/internal/secure"
	"poolwatch/internal/store"
)

const sessionCookieName = "poolwatch_session"

type contextKey string

const (
	adminContextKey   contextKey = "admin"
	sessionContextKey contextKey = "session"
)

// Dependencies 汇总 HTTP 服务依赖，便于入口和测试显式装配。
type Dependencies struct {
	Store               *store.Store
	Vault               *secure.Vault
	Auth                *auth.Service
	Scheduler           *scheduler.Service
	Push                *push.Service
	Events              *events.Hub
	Static              http.Handler
	PublicBaseURL       string
	AllowPrivateTargets bool
	Logger              *slog.Logger
}

// Server 提供同源 PWA、管理接口、健康检查和实时事件流。
type Server struct {
	dependencies Dependencies
	handler      http.Handler
	secureCookie bool
	publicOrigin string
	limiter      *attemptLimiter
}

// NewServer 创建并注册全部路由。
func NewServer(dependencies Dependencies) *Server {
	if dependencies.Logger == nil {
		dependencies.Logger = slog.Default()
	}
	server := &Server{dependencies: dependencies, limiter: newAttemptLimiter(10, 10*time.Minute)}
	if parsed, err := url.Parse(dependencies.PublicBaseURL); err == nil && parsed.Host != "" {
		server.secureCookie = parsed.Scheme == "https"
		server.publicOrigin = parsed.Scheme + "://" + parsed.Host
	}
	server.handler = server.routes()
	return server
}

// Handler 返回可直接交给 http.Server 的完整处理器。
func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/bootstrap", s.handleBootstrap)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/session", s.handleLogin)
	mux.Handle("DELETE /api/session", s.protected(http.HandlerFunc(s.handleLogout)))

	mux.Handle("GET /api/dashboard", s.protected(http.HandlerFunc(s.handleDashboard)))
	mux.Handle("GET /api/targets", s.protected(http.HandlerFunc(s.handleTargets)))
	mux.Handle("POST /api/targets", s.protected(http.HandlerFunc(s.handleCreateTarget)))
	mux.Handle("POST /api/targets/detect", s.protected(http.HandlerFunc(s.handleDetectTarget)))
	mux.Handle("POST /api/targets/test", s.protected(http.HandlerFunc(s.handleTestTarget)))
	mux.Handle("GET /api/targets/{id}", s.protected(http.HandlerFunc(s.handleTarget)))
	mux.Handle("PUT /api/targets/{id}", s.protected(http.HandlerFunc(s.handleUpdateTarget)))
	mux.Handle("DELETE /api/targets/{id}", s.protected(http.HandlerFunc(s.handleDeleteTarget)))
	mux.Handle("POST /api/targets/{id}/check", s.protected(http.HandlerFunc(s.handleCheckTarget)))
	mux.Handle("GET /api/targets/{id}/history", s.protected(http.HandlerFunc(s.handleHistory)))
	mux.Handle("POST /api/checks", s.protected(http.HandlerFunc(s.handleCheckAll)))

	mux.Handle("GET /api/alerts", s.protected(http.HandlerFunc(s.handleAlerts)))
	mux.Handle("PATCH /api/alerts/{id}", s.protected(http.HandlerFunc(s.handleAcknowledgeAlert)))
	mux.Handle("GET /api/push", s.protected(http.HandlerFunc(s.handlePushInfo)))
	mux.Handle("POST /api/push/subscriptions", s.protected(http.HandlerFunc(s.handlePushSubscribe)))
	mux.Handle("DELETE /api/push/subscriptions/{id}", s.protected(http.HandlerFunc(s.handlePushDelete)))
	mux.Handle("POST /api/push/test", s.protected(http.HandlerFunc(s.handlePushTest)))

	mux.Handle("GET /api/settings", s.protected(http.HandlerFunc(s.handleSettings)))
	mux.Handle("PUT /api/settings", s.protected(http.HandlerFunc(s.handleUpdateSettings)))
	mux.Handle("POST /api/security/totp/start", s.protected(http.HandlerFunc(s.handleTOTPStart)))
	mux.Handle("POST /api/security/totp/confirm", s.protected(http.HandlerFunc(s.handleTOTPConfirm)))
	mux.Handle("DELETE /api/security/totp", s.protected(http.HandlerFunc(s.handleTOTPDisable)))
	mux.Handle("PUT /api/security/password", s.protected(http.HandlerFunc(s.handlePasswordChange)))
	mux.Handle("GET /api/events", s.protected(s.dependencies.Events))
	mux.HandleFunc("/api/", func(response http.ResponseWriter, _ *http.Request) {
		writeAPIError(response, http.StatusNotFound, "接口不存在")
	})
	if s.dependencies.Static != nil {
		mux.Handle("/", s.dependencies.Static)
	} else {
		mux.HandleFunc("/", func(response http.ResponseWriter, _ *http.Request) {
			http.Error(response, "页面资源尚未构建", http.StatusServiceUnavailable)
		})
	}
	return s.securityHeaders(s.recoverPanics(mux))
}

func (s *Server) protected(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie(sessionCookieName)
		if err != nil {
			writeAPIError(response, http.StatusUnauthorized, "请先登录")
			return
		}
		admin, session, err := s.dependencies.Auth.Authenticate(request.Context(), cookie.Value)
		if err != nil {
			s.clearSessionCookie(response)
			writeAPIError(response, http.StatusUnauthorized, "登录状态已经失效")
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead && !s.sameOrigin(request) {
			writeAPIError(response, http.StatusForbidden, "请求来源校验失败")
			return
		}
		ctx, cancel := context.WithDeadline(request.Context(), session.ExpiresAt)
		defer cancel()
		ctx = context.WithValue(ctx, adminContextKey, admin)
		ctx = context.WithValue(ctx, sessionContextKey, session)
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Referrer-Policy", "same-origin")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; manifest-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(request.URL.Path, "/api/") {
			response.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(response, request)
	})
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.dependencies.Logger.Error("处理请求时发生异常", "path", request.URL.Path)
				writeAPIError(response, http.StatusInternalServerError, "服务器处理请求失败")
			}
		}()
		next.ServeHTTP(response, request)
	})
}

func (s *Server) sameOrigin(request *http.Request) bool {
	if strings.EqualFold(request.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := strings.TrimRight(strings.TrimSpace(request.Header.Get("Origin")), "/")
	if origin == "" {
		return true
	}
	if s.publicOrigin != "" {
		return strings.EqualFold(origin, s.publicOrigin)
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(origin, scheme+"://"+request.Host)
}

func (s *Server) setSessionCookie(response http.ResponseWriter, result auth.SessionResult) {
	http.SetCookie(response, &http.Cookie{
		Name: sessionCookieName, Value: result.Token, Path: "/", Expires: result.ExpiresAt,
		MaxAge: int(time.Until(result.ExpiresAt).Seconds()), HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(response http.ResponseWriter) {
	http.SetCookie(response, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
		HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) handleHealth(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := s.dependencies.Store.DB().PingContext(ctx); err != nil {
		writeAPIError(response, http.StatusServiceUnavailable, "数据库健康检查失败")
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func adminFromContext(ctx context.Context) store.Admin {
	admin, _ := ctx.Value(adminContextKey).(store.Admin)
	return admin
}

func decodeJSON(response http.ResponseWriter, request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(response, request.Body, 1<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("请求内容格式不正确")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeAPIError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]string{"message": message})
}

func clientAddress(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}

type attemptLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	attempts map[string][]time.Time
}

func newAttemptLimiter(limit int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{limit: limit, window: window, attempts: make(map[string][]time.Time)}
}

func (limiter *attemptLimiter) Allow(key string, now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	cutoff := now.Add(-limiter.window)
	recent := limiter.attempts[key][:0]
	for _, attempt := range limiter.attempts[key] {
		if attempt.After(cutoff) {
			recent = append(recent, attempt)
		}
	}
	if len(recent) >= limiter.limit {
		limiter.attempts[key] = recent
		return false
	}
	limiter.attempts[key] = append(recent, now)
	return true
}

func (limiter *attemptLimiter) Reset(key string) {
	limiter.mu.Lock()
	delete(limiter.attempts, key)
	limiter.mu.Unlock()
}
