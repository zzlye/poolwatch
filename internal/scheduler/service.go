package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"poolwatch/internal/alerts"
	"poolwatch/internal/monitor"
	"poolwatch/internal/secure"
	"poolwatch/internal/store"
)

var ErrAlreadyRunning = errors.New("该渠道正在检测中")

// Service 定时选择到期渠道，并保证单个渠道不会同时执行两次。
type Service struct {
	store               *store.Store
	vault               *secure.Vault
	runner              monitor.Runner
	alerts              *alerts.Engine
	allowPrivateTargets bool
	checkTimeout        time.Duration
	tickInterval        time.Duration
	semaphore           chan struct{}
	mu                  sync.Mutex
	running             map[string]struct{}
	now                 func() time.Time
	onError             func(error)
	wait                sync.WaitGroup
}

// NewService 创建检测调度器。
func NewService(database *store.Store, vault *secure.Vault, runner monitor.Runner, alertEngine *alerts.Engine, allowPrivateTargets bool) *Service {
	return &Service{
		store: database, vault: vault, runner: runner, alerts: alertEngine,
		allowPrivateTargets: allowPrivateTargets, checkTimeout: 20 * time.Second, tickInterval: 15 * time.Second,
		semaphore: make(chan struct{}, 4), running: make(map[string]struct{}),
		now: func() time.Time { return time.Now().UTC() }, onError: func(error) {},
	}
}

// SetErrorHandler 设置只接收脱敏错误的后台日志回调。
func (s *Service) SetErrorHandler(handler func(error)) {
	if handler != nil {
		s.onError = handler
	}
}

// Start 启动到期检测、会话清理和每日历史清理循环。
func (s *Service) Start(ctx context.Context) {
	s.wait.Add(1)
	go func() {
		defer s.wait.Done()
		ticker := time.NewTicker(s.tickInterval)
		defer ticker.Stop()
		cleanupTicker := time.NewTicker(24 * time.Hour)
		defer cleanupTicker.Stop()
		if err := s.CheckDue(ctx); err != nil && ctx.Err() == nil {
			s.onError(err)
		}
		if err := s.Cleanup(ctx); err != nil && ctx.Err() == nil {
			s.onError(err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.CheckDue(ctx); err != nil && ctx.Err() == nil {
					s.onError(err)
				}
			case <-cleanupTicker.C:
				if err := s.Cleanup(ctx); err != nil && ctx.Err() == nil {
					s.onError(err)
				}
			}
		}
	}()
}

// Wait 等待后台循环和已经开始的检测退出。
func (s *Service) Wait() {
	s.wait.Wait()
}

// CheckDue 并发检测所有到达计划时间的渠道。
func (s *Service) CheckDue(ctx context.Context) error {
	targets, err := s.store.DueTargets(ctx, s.now())
	if err != nil {
		return err
	}
	return s.checkTargets(ctx, targets)
}

// CheckAll 立即检测全部启用渠道。
func (s *Service) CheckAll(ctx context.Context) error {
	targets, err := s.store.ListTargets(ctx)
	if err != nil {
		return err
	}
	enabled := make([]store.Target, 0, len(targets))
	for _, target := range targets {
		if target.Enabled {
			enabled = append(enabled, target)
		}
	}
	return s.checkTargets(ctx, enabled)
}

// CheckTarget 立即检测指定渠道，包括当前被停用的渠道。
func (s *Service) CheckTarget(ctx context.Context, targetID string) error {
	if !s.acquireTarget(targetID) {
		return ErrAlreadyRunning
	}
	defer s.releaseTarget(targetID)
	return s.runTarget(ctx, targetID)
}

// TestConfig 运行尚未保存的配置，不写入历史或触发告警。
func (s *Service) TestConfig(ctx context.Context, target monitor.TargetConfig) (monitor.Snapshot, error) {
	target.AllowPrivateNetwork = s.allowPrivateTargets
	timeoutContext, cancel := context.WithTimeout(ctx, s.checkTimeout)
	defer cancel()
	return s.runner.Run(timeoutContext, target)
}

// Cleanup 根据设置清理历史和过期会话。
func (s *Service) Cleanup(ctx context.Context) error {
	retentionText, err := s.store.GetSetting(ctx, "history_retention_days")
	if err != nil {
		return err
	}
	retention, err := strconv.Atoi(retentionText)
	if err != nil || retention < 1 || retention > 365 {
		retention = 7
	}
	now := s.now()
	if err := s.store.CleanupHistory(ctx, now.AddDate(0, 0, -retention)); err != nil {
		return err
	}
	return s.store.CleanupSessions(ctx, now)
}

func (s *Service) checkTargets(ctx context.Context, targets []store.Target) error {
	var wait sync.WaitGroup
	var errorMu sync.Mutex
	found := make([]error, 0)
	for _, target := range targets {
		targetID := target.ID
		if !s.acquireTarget(targetID) {
			continue
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			defer s.releaseTarget(targetID)
			if err := s.runTarget(ctx, targetID); err != nil {
				errorMu.Lock()
				found = append(found, err)
				errorMu.Unlock()
			}
		}()
	}
	wait.Wait()
	return errors.Join(found...)
}

func (s *Service) runTarget(ctx context.Context, targetID string) error {
	select {
	case s.semaphore <- struct{}{}:
		defer func() { <-s.semaphore }()
	case <-ctx.Done():
		return ctx.Err()
	}
	target, err := s.store.TargetByID(ctx, targetID)
	if err != nil {
		return err
	}
	runtimeConfig, err := s.runtimeConfig(target)
	if err != nil {
		classified := &monitor.CheckError{Kind: monitor.ErrorClassConfig, Message: "渠道配置或加密凭据无效"}
		if alertErr := s.alerts.HandleFailure(ctx, target, classified); alertErr != nil {
			return errors.Join(err, alertErr)
		}
		return err
	}
	timeoutContext, cancel := context.WithTimeout(ctx, s.checkTimeout)
	defer cancel()
	snapshot, checkErr := s.runner.Run(timeoutContext, runtimeConfig)
	if checkErr != nil {
		if err := s.alerts.HandleFailure(ctx, target, checkErr); err != nil {
			return errors.Join(checkErr, err)
		}
		return checkErr
	}
	if err := s.alerts.HandleSuccess(ctx, target, snapshot); err != nil {
		return err
	}
	return nil
}

func (s *Service) runtimeConfig(target store.Target) (monitor.TargetConfig, error) {
	var config monitor.TargetConfig
	if target.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(target.ConfigJSON), &config); err != nil {
			return monitor.TargetConfig{}, fmt.Errorf("解析渠道配置失败: %w", err)
		}
	}
	if target.CredentialsEnc != "" {
		credentials, err := s.vault.Decrypt(target.CredentialsEnc)
		if err != nil {
			return monitor.TargetConfig{}, err
		}
		if err := json.Unmarshal(credentials, &config.Credential); err != nil {
			return monitor.TargetConfig{}, errors.New("渠道凭据格式无效")
		}
	}
	config.ID = target.ID
	config.Name = target.Name
	config.Kind = monitor.TargetKind(target.Kind)
	config.BaseURL = target.BaseURL
	config.AllowPrivateNetwork = s.allowPrivateTargets
	return config, nil
}

func (s *Service) acquireTarget(targetID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.running[targetID]; exists {
		return false
	}
	s.running[targetID] = struct{}{}
	return true
}

func (s *Service) releaseTarget(targetID string) {
	s.mu.Lock()
	delete(s.running, targetID)
	s.mu.Unlock()
}
