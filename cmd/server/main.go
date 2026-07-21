package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"poolwatch/internal/alerts"
	"poolwatch/internal/api"
	"poolwatch/internal/auth"
	"poolwatch/internal/config"
	"poolwatch/internal/events"
	"poolwatch/internal/monitor"
	"poolwatch/internal/push"
	"poolwatch/internal/scheduler"
	"poolwatch/internal/secure"
	"poolwatch/internal/store"
	"poolwatch/internal/webui"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("服务退出", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configuration, err := config.Load()
	if err != nil {
		return err
	}
	database, err := store.Open(configuration.DataDir)
	if err != nil {
		return err
	}
	defer database.Close()
	initialized, err := database.HasAdmin(context.Background())
	if err != nil {
		return err
	}
	if !initialized && configuration.SetupToken == "" {
		return errors.New("首次启动必须设置 SETUP_TOKEN")
	}
	vault, err := secure.NewVault(configuration.EncryptionKey)
	if err != nil {
		return err
	}
	authService := auth.NewService(database, vault, configuration.SetupToken, configuration.SessionLifetime)
	eventHub := events.NewHub()
	pushService := push.NewService(database, vault, configuration.PublicBaseURL)
	if err := pushService.EnsureKeys(context.Background()); err != nil {
		return err
	}
	notifier := alerts.NotifierFunc(func(_ context.Context, notification alerts.Notification) error {
		eventHub.Publish("alert", notification)
		pushContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return pushService.Send(pushContext, push.Notification{
			Title: notification.Title, Body: notification.Message,
			URL: "/alerts?focus=" + notification.AlertID, Tag: notification.AlertID, Severity: notification.Severity,
		})
	})
	alertEngine := alerts.NewEngine(database, notifier)
	registry := monitor.NewRegistry(monitor.HTTPOptions{Timeout: 20 * time.Second, MaxResponseBytes: 1 << 20})
	schedulerService := scheduler.NewService(database, vault, registry, alertEngine, configuration.AllowPrivateTargets)
	schedulerService.SetErrorHandler(func(err error) {
		logger.Warn("后台检测完成但存在错误", "error", err.Error())
	})
	schedulerService.SetSnapshotHandler(func(targetID string) {
		eventHub.Publish("snapshot", map[string]string{"targetId": targetID})
	})
	staticHandler, err := webui.NewHandler()
	if err != nil {
		return err
	}
	apiServer := api.NewServer(api.Dependencies{
		Store: database, Vault: vault, Auth: authService, Scheduler: schedulerService,
		Push: pushService, Events: eventHub, AndroidUpdates: api.NewGitHubReleaseUpdateProvider(), Static: staticHandler,
		PublicBaseURL: configuration.PublicBaseURL, AllowPrivateTargets: configuration.AllowPrivateTargets, Logger: logger,
	})
	httpServer := &http.Server{
		Addr: configuration.Address, Handler: apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second,
	}
	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	schedulerService.Start(rootContext)

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("号池监控已经启动", "address", configuration.Address)
		serverErrors <- httpServer.ListenAndServe()
	}()
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-rootContext.Done():
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		return err
	}
	schedulerService.Wait()
	return nil
}
