package tracing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oekit/logx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
)

// zombieCaptureLogger 记录 Warn 留痕（#121 的跳过路径必须可观测）。
// With* 构造方法返回自身——tlog 是 logx.Delegating，每次调用解析
// logx.Default() 并包 component attr，返回自身才能保住捕获面。
type zombieCaptureLogger struct {
	captureLoggerStub
	warns []string
}

func (c *zombieCaptureLogger) Warn(msg string, _ ...any) {
	c.warns = append(c.warns, msg)
}

func (c *zombieCaptureLogger) WithCtx(context.Context) logx.Logger { return c }
func (c *zombieCaptureLogger) WithService(string) logx.Logger      { return c }
func (c *zombieCaptureLogger) WithAttrs(...logx.Attr) logx.Logger  { return c }
func (c *zombieCaptureLogger) WithGroup(string) logx.Logger        { return c }

// captureLoggerStub 提供 logx.Logger 其余方法的空实现。
type captureLoggerStub struct{}

func (captureLoggerStub) Debug(string, ...any)                      {}
func (captureLoggerStub) Info(string, ...any)                       {}
func (captureLoggerStub) Warn(string, ...any)                       {}
func (captureLoggerStub) Error(string, ...any)                      {}
func (captureLoggerStub) Debugf(string, ...any)                     {}
func (captureLoggerStub) Infof(string, ...any)                      {}
func (captureLoggerStub) Warnf(string, ...any)                      {}
func (captureLoggerStub) Errorf(string, ...any)                     {}
func (captureLoggerStub) CtxDebug(context.Context, string, ...any)  {}
func (captureLoggerStub) CtxInfo(context.Context, string, ...any)   {}
func (captureLoggerStub) CtxWarn(context.Context, string, ...any)   {}
func (captureLoggerStub) CtxError(context.Context, string, ...any)  {}
func (captureLoggerStub) CtxDebugf(context.Context, string, ...any) {}
func (captureLoggerStub) CtxInfof(context.Context, string, ...any)  {}
func (captureLoggerStub) CtxWarnf(context.Context, string, ...any)  {}
func (captureLoggerStub) CtxErrorf(context.Context, string, ...any) {}
func (captureLoggerStub) WithCtx(context.Context) logx.Logger       { return captureLoggerStub{} }
func (captureLoggerStub) WithService(string) logx.Logger            { return captureLoggerStub{} }
func (captureLoggerStub) WithAttrs(...logx.Attr) logx.Logger        { return captureLoggerStub{} }
func (captureLoggerStub) WithGroup(string) logx.Logger              { return captureLoggerStub{} }
func (captureLoggerStub) SetLevel(logx.Level)                       {}

// TestShutdown_DisplacedHolderKeepsGlobalMeterAlive 钉住 #121：Init#1(metrics ON)
// → Init#2(DisableMetrics)（#52 语义下 mp1 继续持有全局位导出）→ 原持有方执行
// README 推荐的 `defer provider.Shutdown(ctx)`——此前 providerImpl.Shutdown
// 无条件关掉 mp1：全局 otel.GetMeterProvider() 指向已 Shutdown 僵尸实例，
// 全局指标静默全灭、零报错、不可恢复（meter 面是首次关闭，连 already-shutdown
// 错误都没有）。裁决后的语义：被顶掉且 meterProvider 仍持有全局位时跳过
// meter 面的关闭（全局位由 Init/全局注册策略管理），打 Warn 留痕。
func TestShutdown_DisplacedHolderKeepsGlobalMeterAlive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exporter test in -short mode: shutdown flush may attempt network")
	}

	p1, err := Init(Config{ServiceName: "svc-a", Endpoint: "localhost:4317", Insecure: true})
	if err != nil {
		t.Fatalf("first Init() error: %v", err)
	}
	mp1 := otel.GetMeterProvider()
	impl1, ok := p1.(*providerImpl)
	if !ok || impl1.meterProvider == nil {
		t.Fatalf("first provider has no meter provider: %T", p1)
	}

	// Init#2：DisableMetrics ⇒ 不产出 meterProvider，#52 语义下全局位不变
	p2, err := Init(Config{ServiceName: "svc-b", Endpoint: "localhost:4317", Insecure: true, DisableMetrics: true})
	if err != nil {
		t.Fatalf("second Init() error: %v", err)
	}
	if got := otel.GetMeterProvider(); got != mp1 {
		t.Fatalf("DisableMetrics Init replaced global MeterProvider (violates #52): %T != %T", got, mp1)
	}

	// 跳过路径必须留痕（可观测，不再静默）
	zc := &zombieCaptureLogger{}
	prev := logx.Default()
	logx.SetDefault(zc)
	t.Cleanup(func() { logx.SetDefault(prev) })

	// 原持有方退出（README 推荐写法）
	if err := p1.Shutdown(context.Background()); errors.Is(err, metric.ErrReaderShutdown) {
		t.Fatalf("displaced holder Shutdown hit meter reader shutdown — meter face was killed (#121): %v", err)
	}
	if len(zc.warns) == 0 {
		t.Error("meter skip path should leave a Warn record (#121)")
	}

	// 断言 1：全局 MeterProvider 身份不变（仍指向 mp1）
	if got := otel.GetMeterProvider(); got != mp1 {
		t.Fatalf("holder Shutdown replaced global MeterProvider: %T != %T", got, mp1)
	}
	// 断言 2：全局 meter 仍是活实例——ForceFlush 无 ErrReaderShutdown
	// （被 Shutdown 后的 PeriodicReader 一律立即返回 ErrReaderShutdown）
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := impl1.meterProvider.ForceFlush(ctx); errors.Is(err, metric.ErrReaderShutdown) {
		t.Fatalf("global MeterProvider was shut down by displaced holder (zombie state, #121): %v", err)
	}

	_ = p2.Shutdown(context.Background())
}

// TestShutdown_CurrentHolderFlushesMeter 钉住 #121 的另一半：当前持有方
// （installed.Load() == p）照常 Shutdown——进程退出前 flush 的契约不变，
// 不得为护全局位而过度跳过（跳过条件仅「被顶掉且仍持有全局位」）。
func TestShutdown_CurrentHolderFlushesMeter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exporter test in -short mode: shutdown flush may attempt network")
	}

	p, err := Init(Config{ServiceName: "svc-current", Endpoint: "localhost:4317", Insecure: true})
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	impl, ok := p.(*providerImpl)
	if !ok || impl.meterProvider == nil {
		t.Fatalf("provider has no meter provider: %T", p)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil && errors.Is(err, metric.ErrReaderShutdown) {
		t.Fatalf("current holder Shutdown should flush (not skip): %v", err)
	}
	// 铁证：meter 面确实被关闭（再次 Shutdown 收 ErrReaderShutdown），
	// 而非被跳过（活实例二次 Shutdown 才会走到 reader 已关的错误面）。
	if err := impl.meterProvider.Shutdown(ctx); !errors.Is(err, metric.ErrReaderShutdown) {
		t.Fatalf("meter face should have been really shut down by current holder, second Shutdown err = %v (want ErrReaderShutdown)", err)
	}
}
