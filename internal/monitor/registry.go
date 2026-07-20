package monitor

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Registry 保存所有可用适配器，并实现主线 Runner 接口。
type Registry struct {
	mu       sync.RWMutex
	adapters map[TargetKind]Adapter
	http     *secureHTTPClient
}

// NewRegistry 创建带默认四种适配器的注册器。
func NewRegistry(options HTTPOptions) *Registry {
	httpClient := newSecureHTTPClient(options)
	registry := &Registry{adapters: make(map[TargetKind]Adapter), http: httpClient}
	registry.Register(newNewAPIAdapter(httpClient))
	registry.Register(newSub2APIAdapter(httpClient))
	registry.Register(newChatGPT2APIAdapter(httpClient))
	registry.Register(newCustomHTTPAdapter(httpClient))
	return registry
}

// DefaultRegistry 使用生产默认值创建注册器。
func DefaultRegistry() *Registry {
	return NewRegistry(HTTPOptions{})
}

// Register 注册或替换同类型适配器。
func (registry *Registry) Register(adapter Adapter) {
	if registry == nil || adapter == nil {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.adapters[adapter.Kind()] = adapter
}

// Adapter 返回指定类型的适配器。
func (registry *Registry) Adapter(kind TargetKind) (Adapter, error) {
	if registry == nil {
		return nil, checkError(ErrorClassConfig, "查找渠道适配器", "监控适配器注册器未初始化", 0, nil)
	}
	registry.mu.RLock()
	adapter := registry.adapters[kind]
	registry.mu.RUnlock()
	if adapter == nil {
		return nil, checkError(ErrorClassConfig, "查找渠道适配器", fmt.Sprintf("不支持的渠道类型：%s", kind), 0, nil)
	}
	return adapter, nil
}

// Run 根据 TargetKind 分派一次只读检测。
func (registry *Registry) Run(ctx context.Context, target TargetInput) (Result, error) {
	adapter, err := registry.Adapter(target.Kind)
	if err != nil {
		return Result{}, err
	}
	result, _, err := runWithRetry(ctx, func() (Result, any, error) {
		result, checkErr := adapter.Check(ctx, target)
		return result, nil, checkErr
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// Probe 执行连接测试；自定义渠道会额外返回临时 JSON sample。
func (registry *Registry) Probe(ctx context.Context, target TargetInput) (Result, any, error) {
	adapter, err := registry.Adapter(target.Kind)
	if err != nil {
		return Result{}, nil, err
	}
	if prober, ok := adapter.(Prober); ok {
		return runWithRetry(ctx, func() (Result, any, error) {
			return prober.Probe(ctx, target)
		})
	}
	return runWithRetry(ctx, func() (Result, any, error) {
		result, checkErr := adapter.Check(ctx, target)
		return result, nil, checkErr
	})
}

// VerifyBrowserCredential 将浏览器捕获的凭据交给对应适配器进行只读校验。
func (registry *Registry) VerifyBrowserCredential(ctx context.Context, target TargetInput) (Credential, error) {
	adapter, err := registry.Adapter(target.Kind)
	if err != nil {
		return Credential{}, err
	}
	verifier, ok := adapter.(BrowserCredentialVerifier)
	if !ok {
		return Credential{}, checkError(ErrorClassConfig, "校验网页登录凭据", "该渠道不支持网页登录凭据", 0, nil)
	}
	return verifier.VerifyBrowserCredential(ctx, target)
}

func runWithRetry(ctx context.Context, run func() (Result, any, error)) (Result, any, error) {
	var result Result
	var sample any
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		result, sample, err = run()
		if err == nil {
			return result, sample, nil
		}
		kind := ErrorClassOf(err)
		if kind != ErrorClassNetwork && kind != ErrorClassServer {
			return result, sample, err
		}
		if attempt == 2 {
			break
		}
		backoff := time.Duration(attempt+1) * 100 * time.Millisecond
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return Result{}, sample, checkError(ErrorClassNetwork, "重试渠道检测", "渠道检测已取消或超时", 0, ctx.Err())
		case <-timer.C:
		}
	}
	return result, sample, err
}

// NewAdapter 创建单个适配器，适合独立测试或嵌入已有调度器。
func NewAdapter(kind TargetKind, options HTTPOptions) (Adapter, error) {
	return NewRegistry(options).Adapter(kind)
}

var _ Runner = (*Registry)(nil)
var _ Prober = (*Registry)(nil)
var _ Detector = (*Registry)(nil)
var _ BrowserCredentialVerifier = (*Registry)(nil)
