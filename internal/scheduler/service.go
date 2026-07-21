package scheduler

import (
	"context"
	"database/sql"
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
	onSnapshot          func(string)
	wait                sync.WaitGroup
}

// NewService 创建检测调度器。
func NewService(database *store.Store, vault *secure.Vault, runner monitor.Runner, alertEngine *alerts.Engine, allowPrivateTargets bool) *Service {
	return &Service{
		store: database, vault: vault, runner: runner, alerts: alertEngine,
		allowPrivateTargets: allowPrivateTargets, checkTimeout: 20 * time.Second, tickInterval: 15 * time.Second,
		semaphore: make(chan struct{}, 4), running: make(map[string]struct{}),
		now: func() time.Time { return time.Now().UTC() }, onError: func(error) {},
		onSnapshot: func(string) {},
	}
}

// SetErrorHandler 设置只接收脱敏错误的后台日志回调。
func (s *Service) SetErrorHandler(handler func(error)) {
	if handler != nil {
		s.onError = handler
	}
}

// SetSnapshotHandler 设置检测结果保存后的实时刷新回调。
func (s *Service) SetSnapshotHandler(handler func(string)) {
	if handler != nil {
		s.onSnapshot = handler
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
		notificationTicker := time.NewTicker(5 * time.Minute)
		defer notificationTicker.Stop()
		if err := s.alerts.RetryPending(ctx, 100); err != nil && ctx.Err() == nil {
			s.onError(err)
		}
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
			case <-notificationTicker.C:
				if err := s.alerts.RetryPending(ctx, 100); err != nil && ctx.Err() == nil {
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

// RefreshAccountQuotas 刷新 CLIProxyAPI 当前页账号额度，并与常规检测共用渠道锁。
func (s *Service) RefreshAccountQuotas(ctx context.Context, targetID string, accountIDs []string) ([]store.ChatAccount, error) {
	if len(accountIDs) == 0 || len(accountIDs) > monitor.MaxAccountQuotaRefreshAccounts {
		return nil, errors.New("每次需要选择 1 至 100 个账号")
	}
	if !s.acquireTarget(targetID) {
		return nil, ErrAlreadyRunning
	}
	defer s.releaseTarget(targetID)
	select {
	case s.semaphore <- struct{}{}:
		defer func() { <-s.semaphore }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	target, err := s.store.TargetByID(ctx, targetID)
	if err != nil {
		return nil, err
	}
	if target.Kind != string(monitor.TargetKindCLIProxyAPI) {
		return nil, errors.New("只有 CLIProxyAPI 渠道支持账号额度刷新")
	}
	runtimeConfig, err := s.runtimeConfig(target)
	if err != nil {
		return nil, err
	}
	refresher, ok := s.runner.(monitor.AccountQuotaRefresher)
	if !ok {
		return nil, errors.New("当前检测器不支持账号额度刷新")
	}
	// 每四个账号为一批预留七秒，并为账号列表读取留出三十秒。
	batches := (len(accountIDs) + 3) / 4
	quotaTimeout := 30*time.Second + time.Duration(batches)*7*time.Second
	timeoutContext, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	accounts, err := refresher.RefreshAccountQuotas(timeoutContext, runtimeConfig, accountIDs)
	if err != nil {
		return nil, err
	}
	currentTarget, err := s.store.TargetByID(ctx, target.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	if targetMonitoringConfigChanged(target, currentTarget) {
		return nil, errors.New("渠道配置已经变化，请刷新页面后重试")
	}
	if err := s.alerts.SaveAccountQuotas(ctx, currentTarget, accounts); err != nil {
		return nil, err
	}
	storedAccounts, err := s.store.ListChatAccounts(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]store.ChatAccount, len(storedAccounts))
	for _, account := range storedAccounts {
		byID[account.ExternalID] = account
	}
	result := make([]store.ChatAccount, 0, len(accounts))
	for _, account := range accounts {
		publicID := monitor.PublicAccountID(monitor.TargetKindCLIProxyAPI, account.ExternalID)
		stored, exists := byID[publicID]
		if !exists {
			return nil, errors.New("账号列表已经变化，请刷新页面后重试")
		}
		result = append(result, stored)
	}
	return result, nil
}

// LockTarget 等待并独占一个渠道，供配置更新与检测共享同一把渠道锁。
func (s *Service) LockTarget(ctx context.Context, targetID string) (func(), error) {
	if s.acquireTarget(targetID) {
		return func() { s.releaseTarget(targetID) }, nil
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if s.acquireTarget(targetID) {
				return func() { s.releaseTarget(targetID) }, nil
			}
		}
	}
}

// TestConfig 运行尚未保存的配置，不写入历史或触发告警，并临时返回自定义响应样本。
func (s *Service) TestConfig(ctx context.Context, target monitor.TargetConfig) (monitor.Snapshot, any, error) {
	target.AllowPrivateNetwork = s.allowPrivateTargets
	timeoutContext, cancel := context.WithTimeout(ctx, s.checkTimeout)
	defer cancel()
	if prober, ok := s.runner.(monitor.Prober); ok {
		return prober.Probe(timeoutContext, target)
	}
	result, err := s.runner.Run(timeoutContext, target)
	return result, nil, err
}

// VerifyBrowserCredential 在统一超时和私网策略下校验浏览器授权捕获的凭据。
func (s *Service) VerifyBrowserCredential(ctx context.Context, target monitor.TargetConfig) (monitor.Credential, error) {
	verifier, ok := s.runner.(monitor.BrowserCredentialVerifier)
	if !ok {
		return monitor.Credential{}, errors.New("当前检测器不支持网页登录凭据")
	}
	target.AllowPrivateNetwork = s.allowPrivateTargets
	timeoutContext, cancel := context.WithTimeout(ctx, s.checkTimeout)
	defer cancel()
	return verifier.VerifyBrowserCredential(timeoutContext, target)
}

// DetectTarget 使用只读端点识别渠道类型。
func (s *Service) DetectTarget(ctx context.Context, baseURL string) (monitor.TargetKind, error) {
	detector, ok := s.runner.(monitor.Detector)
	if !ok {
		return "", errors.New("当前检测器不支持自动识别")
	}
	timeoutContext, cancel := context.WithTimeout(ctx, s.checkTimeout)
	defer cancel()
	return detector.Detect(timeoutContext, baseURL, s.allowPrivateTargets)
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
	currentTarget, reloadErr := s.store.TargetByID(ctx, target.ID)
	if errors.Is(reloadErr, sql.ErrNoRows) {
		// 检测期间渠道已删除，旧结果不再具有保存意义。
		return nil
	}
	if reloadErr != nil {
		return reloadErr
	}
	if targetMonitoringConfigChanged(target, currentTarget) {
		// 检测期间渠道配置已更新，丢弃旧配置产生的结果，避免重新写入已取消的指标和告警。
		return nil
	}
	target = currentTarget
	if checkErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.alerts.HandleFailure(ctx, target, checkErr); err != nil {
			return errors.Join(checkErr, err)
		}
		s.onSnapshot(target.ID)
		return checkErr
	}
	if snapshot.CredentialUpdate != nil {
		updatedCredential := *snapshot.CredentialUpdate
		updatedCredential.TOTPCode = ""
		encoded, err := json.Marshal(updatedCredential)
		if err != nil {
			return errors.New("编码续期凭据失败")
		}
		encrypted, err := s.vault.Encrypt(encoded)
		if err != nil {
			return err
		}
		if err := s.store.UpdateTargetCredentials(ctx, target.ID, encrypted, s.now()); err != nil {
			return err
		}
		snapshot.CredentialUpdate = nil
	}
	if err := s.alerts.HandleSuccess(ctx, target, snapshot); err != nil {
		return err
	}
	s.onSnapshot(target.ID)
	return nil
}

func targetMonitoringConfigChanged(previous, current store.Target) bool {
	return previous.Kind != current.Kind ||
		previous.BaseURL != current.BaseURL ||
		previous.ConfigJSON != current.ConfigJSON ||
		previous.CredentialsEnc != current.CredentialsEnc ||
		previous.Enabled != current.Enabled
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
