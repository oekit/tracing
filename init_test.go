package tracing

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oekit/logx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// TestInit_BasicSetup 验证 Init 在提供有效配置时不 panic 且返回非 nil provider。
// 使用本地无效 endpoint，验证初始化逻辑（不实际连接 collector）。
// 注意：Shutdown flush 会触发对 endpoint 的导出重试（约 10s 超时），-short 下跳过。
func TestInit_BasicSetup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exporter test in -short mode: shutdown flush may attempt network")
	}
	cfg := Config{
		ServiceName: "test-svc",
		Endpoint:    "localhost:4317",
		Insecure:    true,
	}
	p, err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	if p == nil {
		t.Fatal("Init() returned nil provider")
	}
	// 清理：Shutdown 不应 panic（即使未真正连接）
	_ = p.Shutdown(context.Background())
}

// TestInit_FullSampling 验证 SampleRate=1.0（全量）时不会因 < 1.0 守卫被跳过。
// 此前 SampleRate==1.0 会被 > 0 && < 1.0 条件排除，验证它能正常初始化。
func TestInit_FullSampling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exporter test in -short mode: shutdown flush may attempt network")
	}
	cfg := Config{
		ServiceName: "test-svc",
		Endpoint:    "localhost:4317",
		Insecure:    true,
		SampleRate:  1.0,
	}
	p, err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() with SampleRate=1.0 error: %v", err)
	}
	if p == nil {
		t.Fatal("Init() returned nil provider")
	}
	_ = p.Shutdown(context.Background())
}

// TestInit_PartialSampling 验证 0 < SampleRate < 1.0 时启用 TraceIDRatioBased 采样器。
func TestInit_PartialSampling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exporter test in -short mode: shutdown flush may attempt network")
	}
	cfg := Config{
		ServiceName: "test-svc",
		Endpoint:    "localhost:4317",
		Insecure:    true,
		SampleRate:  0.5,
	}
	p, err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() with SampleRate=0.5 error: %v", err)
	}
	if p == nil {
		t.Fatal("Init() returned nil provider")
	}
	_ = p.Shutdown(context.Background())
}

// TestInit_EmptyEndpoint_NoExporter 验证 Endpoint 为空时 Init 返回可用 provider
// 且不创建任何 OTLP 导出器（不发起网络连接，因此无需 -short 守卫）。
func TestInit_EmptyEndpoint_NoExporter(t *testing.T) {
	p, err := Init(Config{ServiceName: "test-svc"})
	if err != nil {
		t.Fatalf("Init() with empty endpoint error: %v", err)
	}
	if p == nil {
		t.Fatal("Init() returned nil provider")
	}
	impl, ok := p.(*providerImpl)
	if !ok {
		t.Fatalf("Init() returned unexpected provider type %T", p)
	}
	if impl.tracerProvider == nil {
		t.Error("expected non-nil tracer provider with empty endpoint")
	}
	if impl.meterProvider != nil {
		t.Error("empty endpoint must not create a meter provider (no exporter, no network)")
	}
	_ = p.Shutdown(context.Background())
}

// TestInit_SetsGlobalPropagator 验证 Init 安装了可解析 W3C traceparent 的全局传播器
// （组合中同时包含 B3/OT/Baggage，此处以 TraceContext 作代表断言）。
func TestInit_SetsGlobalPropagator(t *testing.T) {
	p, err := Init(Config{ServiceName: "test-svc"})
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()

	carrier := propagation.MapCarrier{
		"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		t.Fatal("expected global propagator to extract a valid span context from traceparent")
	}
	if got := sc.TraceID().String(); got != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("extracted trace id = %q, want %q", got, "0af7651916cd43dd8448eb211c80319c")
	}
}

// TestInit_ReInitShutsDownPrevious 验证重复 Init 不会泄漏：第二次 Init 会
// 自动关闭第一个 provider；原持有方对已被替换 provider 的重复 Shutdown
// 不应 panic（返回 already-shutdown 类错误，可安全忽略）。
func TestInit_ReInitShutsDownPrevious(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exporter test in -short mode: shutdown flush may attempt network")
	}
	p1, err := Init(Config{ServiceName: "svc-a", Endpoint: "localhost:4317", Insecure: true})
	if err != nil {
		t.Fatalf("first Init() error: %v", err)
	}
	p2, err := Init(Config{ServiceName: "svc-b", Endpoint: "localhost:4317", Insecure: true})
	if err != nil {
		t.Fatalf("second Init() error: %v", err)
	}
	// p1 已被第二次 Init 自动关闭；重复 Shutdown 允许返回错误，但必须快速返回且不 panic
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = p1.Shutdown(ctx)
	_ = p2.Shutdown(ctx)
}

// TestBuildSampler 验证采样率到采样器的映射：
// 0 < rate < 1.0 → ParentBased(TraceIDRatioBased)；rate <= 0 或 >= 1.0 → 全量。
func TestBuildSampler(t *testing.T) {
	cases := []struct {
		name string
		rate float64
		want sdktrace.Sampler
	}{
		{"zero means full sampling", 0, sdktrace.AlwaysSample()},
		{"negative means full sampling", -0.5, sdktrace.AlwaysSample()},
		{"one means full sampling", 1.0, sdktrace.AlwaysSample()},
		{"partial is parent based ratio", 0.5, sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.5))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildSampler(tc.rate)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("buildSampler(%v) = %#v, want %#v", tc.rate, got, tc.want)
			}
		})
	}
}

// TestNewResource_ServiceName 验证 resource 携带 service.name 属性。
func TestNewResource_ServiceName(t *testing.T) {
	res := newResource(Config{ServiceName: "svc-x"})
	v, ok := res.Set().Value(attribute.Key("service.name"))
	if !ok {
		t.Fatal("resource missing service.name attribute")
	}
	if v.AsString() != "svc-x" {
		t.Errorf("service.name = %q, want %q", v.AsString(), "svc-x")
	}
	if _, ok := res.Set().Value(semconv.ServiceNameKey); !ok {
		t.Error("semconv ServiceNameKey lookup failed")
	}
}

// ⭐ DisableMetrics：collector 侧**没有 metrics 管线**时的止噪开关。
//
// 判据面 = **全局 MeterProvider 有没有被替换**：关掉时不建 OTLP metrics 导出器、
// 不启动 Go runtime 指标采集 ⇒ 全局保持调用方原样（noop 或既有 provider）。
func TestInit_DisableMetrics_KeepsGlobalMeterProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exporter test in -short mode: shutdown flush may attempt network")
	}
	before := otel.GetMeterProvider()

	p, err := Init(Config{
		ServiceName:    "test-svc",
		Endpoint:       "localhost:4317",
		Insecure:       true,
		DisableMetrics: true,
	})
	if err != nil {
		t.Fatalf("Init() with DisableMetrics error: %v", err)
	}
	if p == nil {
		t.Fatal("Init() returned nil provider")
	}
	if got := otel.GetMeterProvider(); got != before {
		t.Fatalf("DisableMetrics=true 仍替换了全局 MeterProvider：%T != %T", got, before)
	}
	_ = p.Shutdown(context.Background())
}

// ⭐ 反向判据（防「开关写反」）：默认（不设 DisableMetrics）**必须**导出 metrics
// ⇒ 全局 MeterProvider 被替换成导出的 provider。
// ⭐ 这条同时守住「新增字段不改既有调用方行为面」这条纪律。
func TestInit_DefaultExportsMetrics(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exporter test in -short mode: shutdown flush may attempt network")
	}
	before := otel.GetMeterProvider()

	p, err := Init(Config{
		ServiceName: "test-svc",
		Endpoint:    "localhost:4317",
		Insecure:    true,
	})
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	if p == nil {
		t.Fatal("Init() returned nil provider")
	}
	if got := otel.GetMeterProvider(); got == before {
		t.Fatal("默认行为未导出 metrics（全局 MeterProvider 未被替换）——既有调用方行为面被改了")
	}
	_ = p.Shutdown(context.Background())
}

// TestInit_ReInitEmptyEndpoint_GlobalMeterStaysAlive 钉住 #58：第一次
// Init(Endpoint!="") 安装全局 MeterProvider 后，第二次 Init(Endpoint=="")
// 不得把该实例 Shutdown 成僵尸——全局 otel.GetMeterProvider() 必须永远指向
// 活实例（业务打点不能静默丢弃且无法恢复）。
// v0.3.4（#50/#52）已把旧 provider 的关闭改为与全局替换对称；本测试是
// 该语义在 #58 场景（Endpoint 非空→空）下的回归钉。
func TestInit_ReInitEmptyEndpoint_GlobalMeterStaysAlive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exporter test in -short mode: shutdown flush may attempt network")
	}
	before := otel.GetMeterProvider()

	p1, err := Init(Config{ServiceName: "svc-a", Endpoint: "localhost:4317", Insecure: true})
	if err != nil {
		t.Fatalf("first Init() error: %v", err)
	}
	mp1 := otel.GetMeterProvider()
	if mp1 == before {
		t.Fatal("first Init did not install a global MeterProvider")
	}

	// #58 场景：Endpoint 从非空改为空（测试/本地模式切换）
	p2, err := Init(Config{ServiceName: "svc-b"})
	if err != nil {
		t.Fatalf("second Init() error: %v", err)
	}

	// 断言 1：全局 MeterProvider 仍指向第一次 Init 安装的实例（未被替换）
	if got := otel.GetMeterProvider(); got != mp1 {
		t.Fatalf("re-Init with empty Endpoint replaced global MeterProvider: %T != %T", got, mp1)
	}

	// 断言 2：该实例仍是活的（没有被第二次 Init 关成僵尸）。
	// PeriodicReader 被 Shutdown 后再次 Shutdown 会立即返回 ErrReaderShutdown；
	// 活实例的 Shutdown 走真实 flush（对不可达端点表现为连接/超时错误，
	// 而非 ErrReaderShutdown）。
	impl1, ok := p1.(*providerImpl)
	if !ok || impl1.meterProvider == nil {
		t.Fatalf("first provider has no meter provider: %T", p1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := impl1.meterProvider.Shutdown(ctx); err != nil && errors.Is(err, metric.ErrReaderShutdown) {
		t.Fatalf("global MeterProvider was shut down by re-Init (zombie state): %v", err)
	}

	// 清理：第二个 provider 是本地 provider，关闭无网络副作用
	_ = p2.Shutdown(context.Background())
}

// TestInit_NegativeSampleRateRejected 钉住 #59：负采样率是配置错误，
// 直接报错而非静默全量——静默会把「采样率写错」表现为「线上成本暴涨」。
// 校验先于任何全局副作用，无网络动作，不需要 -short 守卫。
func TestInit_NegativeSampleRateRejected(t *testing.T) {
	_, err := Init(Config{ServiceName: "svc", SampleRate: -0.1})
	if err == nil {
		t.Fatal("Init with negative SampleRate must return an error")
	}
	if !strings.Contains(err.Error(), "SampleRate") {
		t.Errorf("error should mention SampleRate, got: %v", err)
	}
}

// TestInit_NaNSampleRateRejected NaN 同负值：配置错误报错（YAML 类型写错的
// 高频产物），不静默全量。
func TestInit_NaNSampleRateRejected(t *testing.T) {
	if _, err := Init(Config{ServiceName: "svc", SampleRate: math.NaN()}); err == nil {
		t.Fatal("Init with NaN SampleRate must return an error")
	}
}

// warnCapture 捕获 Warn 事件的记录型 Logger（其余方法经嵌入接口兜底，
// 测试保证不会触达）。
type warnCapture struct {
	logx.Logger
	mu    sync.Mutex
	warns []string
}

func (w *warnCapture) Warn(msg string, _ ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.warns = append(w.warns, msg)
}

func (w *warnCapture) WithAttrs(_ ...logx.Attr) logx.Logger { return w }

// TestInit_ZeroSampleRate_WarnsAndSucceeds 钉住 #59 的 0 值语义：
// SampleRate=0（含零值缺省）不报错、按全量采样，但必须打 WARN 点明
// 「=全量采样」——运维把采样率写成 0 常是想要「不采样」或「用默认」，
// 静默全量在高 QPS 服务上成本陡增且无线索。
func TestInit_ZeroSampleRate_WarnsAndSucceeds(t *testing.T) {
	cap := &warnCapture{}
	orig := logx.Default()
	logx.SetDefault(cap)
	t.Cleanup(func() { logx.SetDefault(orig) })

	p, err := Init(Config{ServiceName: "svc-zero", SampleRate: 0})
	if err != nil {
		t.Fatalf("SampleRate=0 must not error, got: %v", err)
	}
	if p == nil {
		t.Fatal("Init() returned nil provider")
	}

	cap.mu.Lock()
	warns := len(cap.warns)
	cap.mu.Unlock()
	if warns == 0 {
		t.Fatal("SampleRate=0 must emit a WARN explaining it means full sampling")
	}
	_ = p.Shutdown(context.Background())
}

// TestValidateSampleRate 单元钉住校验函数的边界。
func TestValidateSampleRate(t *testing.T) {
	for _, rate := range []float64{0, 0.5, 1, 1.5} {
		if err := validateSampleRate(rate); err != nil {
			t.Errorf("validateSampleRate(%v) = %v, want nil", rate, err)
		}
	}
	for _, rate := range []float64{-0.001, -1, math.NaN(), math.Inf(-1)} {
		if err := validateSampleRate(rate); err == nil {
			t.Errorf("validateSampleRate(%v) = nil, want error", rate)
		}
	}
}

// TestInit_ReInitOnOffOn_ClosesOrphanedMeterProvider 钉住 #68：re-Init
// 「metrics on→off→on」序列不得把第一个 on 的 MeterProvider 永久孤儿化。
// off 的 Init 产出不了 meterProvider，按 #52 语义第一个 on 的 meterProvider
// 继续持有全局位导出（必须活着）；但第二个 on 替换全局 meter 面之后，它在
// installed 单链上彻底无人引用——周期读取器（15s）与 runtime collector 每
// 15s 向旧 endpoint 导出直到进程结束（ghost 指标流 + 泄漏 goroutine）。
// 修复：meter 面单独追踪「上一个产出 meterProvider 的 impl」
// （lastMeterProvider 链），本次装了 meterProvider 时关掉它的 meter 面。
//
// 判据两半：
//  1. 白盒归因——off 那步之后 lastMeterProvider 仍指向第一个 impl（#52：
//     没人能关它），第二个 on 之后指向第三个 impl（关闭动作发生在这一步）；
//  2. 黑盒活体——第三个 Init 之后，第一个 impl 的 meterProvider 已是
//     已 Shutdown 态：重复 Shutdown 立即返回 ErrReaderShutdown（活实例的
//     Shutdown 走真实 flush，对不可达端点是连接错误而非 ErrReaderShutdown，
//     与 TestInit_ReInitEmptyEndpoint_GlobalMeterStaysAlive 同款判据）。
func TestInit_ReInitOnOffOn_ClosesOrphanedMeterProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exporter test in -short mode: shutdown flush may attempt network")
	}
	pa, err := Init(Config{ServiceName: "svc-a", Endpoint: "localhost:4317", Insecure: true}) // ON
	if err != nil {
		t.Fatalf("first Init() error: %v", err)
	}
	implA, ok := pa.(*providerImpl)
	if !ok || implA.meterProvider == nil {
		t.Fatalf("first provider has no meter provider: %T", pa)
	}
	ma := otel.GetMeterProvider()

	// OFF：DisableMetrics ⇒ 产出不了 meterProvider，#52 语义下全局位与活体都不变
	pb, err := Init(Config{ServiceName: "svc-b", Endpoint: "localhost:4317", Insecure: true, DisableMetrics: true})
	if err != nil {
		t.Fatalf("second Init() error: %v", err)
	}
	if got := otel.GetMeterProvider(); got != ma {
		t.Fatalf("DisableMetrics 的 Init 替换了全局 MeterProvider（违背 #52）: %T != %T", got, ma)
	}
	if lastMeterProvider.Load() != implA {
		t.Fatal("OFF 那步不得移动 lastMeterProvider 追踪位——此时全局 meter 面仍是 implA 的")
	}

	// ON again：替换全局 meter 面 ⇒ 必须把 implA 的 meter 面一并收掉（#68）
	pc, err := Init(Config{ServiceName: "svc-c", Endpoint: "localhost:4317", Insecure: true})
	if err != nil {
		t.Fatalf("third Init() error: %v", err)
	}
	defer func() { _ = pc.Shutdown(context.Background()) }()
	_ = pb.Shutdown(context.Background())
	if lastMeterProvider.Load() != pc.(*providerImpl) {
		t.Fatal("第二个 on 之后 lastMeterProvider 应指向最新产出 meterProvider 的 impl")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := implA.meterProvider.Shutdown(ctx); !errors.Is(err, metric.ErrReaderShutdown) {
		t.Fatalf("第一个 on 的 MeterProvider 在 on→off→on 后未被关闭（#68 孤儿导出）: err=%v, want ErrReaderShutdown", err)
	}
}

// TestInit_GRPCRejectsURLEndpoint 钉住 #98：gRPC 分支必须校验 Endpoint 形态。
// URL 形态端点（照抄 OTEL_EXPORTER_OTLP_ENDPOINT 的 URL 格式、grpc/http 两协议
// 共用一份 Endpoint 配置）此前被原样交给导出器：Init 成功返回 provider，但
// grpc 拨号目标解析不出可用地址、一次都不连——span/metric 在后台导出重试里
// 无声耗尽，表现是「服务正常但 APM 全黑」。与 http 分支同一纪律：不合规输入
// 报错，不静默断供。
func TestInit_GRPCRejectsURLEndpoint(t *testing.T) {
	cases := []struct{ name, endpoint string }{
		{"url with http scheme", "http://127.0.0.1:4317"},
		{"url with https scheme", "https://otel-collector:4317"},
		{"url with path", "http://127.0.0.1:4317/v1/traces"},
		{"host port with path", "otel-collector:4317/v1/traces"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Init(Config{ServiceName: "svc", Endpoint: tc.endpoint, SampleRate: 1})
			if err == nil {
				_ = p.Shutdown(context.Background())
				t.Fatalf("Init() with gRPC endpoint %q error = nil, want error（URL 形态端点 Init 成功却一次都不拨号）", tc.endpoint)
			}
			if !strings.Contains(err.Error(), "host:port") {
				t.Errorf("error should name the expected host:port form, got: %v", err)
			}
			if !strings.Contains(err.Error(), ProtocolHTTP) {
				t.Errorf("error should point URL-form endpoints at Protocol %q, got: %v", ProtocolHTTP, err)
			}
		})
	}
}

// TestInit_ConcurrentInit_NoZombieGlobalProvider 钉住 #100：并发 Init 的
// 「装全局（Set*）→ Swap/关前任」两步序列在无互斥时可交错成
// A.Set → B.Set → B.Swap → A.Swap——A 的 Swap 拿到 pB 并将其 Shutdown，
// 而全局位最后由 B.Set 写为 pB ⇒ 全局 TracerProvider 指向已 Shutdown 僵尸，
// 之后所有业务 span 不再记录（IsRecording=false）且零报错。与 #50/#52/#58/#68
// 的串行关闭不对称不同，本条根因是 Init 无互斥，修法是包级互斥把整个
// 「创建→Set*→Swap/关前任」序列圈进临界区。
//
// 判据复用 issue #100 探针：N 协程并发 Init，每轮后断言全局 tracer 的新
// span 仍 IsRecording。修复前为竞态复现（概率性命中，红输出见 PR 正文）；
// 修复后互斥串行化 ⇒ 判据恒成立（确定性绿）。
func TestInit_ConcurrentInit_NoZombieGlobalProvider(t *testing.T) {
	const goroutines = 8
	rounds := 4000
	if testing.Short() {
		rounds = 100
	}
	for round := range rounds {
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range goroutines {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				p, ierr := Init(Config{ServiceName: "concurrent-svc", SampleRate: 1})
				if ierr != nil {
					t.Errorf("concurrent Init() error: %v", ierr)
					return
				}
				// 不在此处 Shutdown：落选 provider 由 Swap 链自动关闭，
				// 当前全局 provider 必须保持活体供下方判据。
				_ = p
			}()
		}
		close(start)
		wg.Wait()

		_, span := otel.Tracer("probe").Start(context.Background(), "probe")
		recording := span.IsRecording()
		span.End()
		if !recording {
			t.Fatalf("round %d: 并发 Init 后全局 TracerProvider 是已 Shutdown 僵尸（业务 span 静默全丢）", round)
		}
	}
}

// TestValidateGRPCEndpoint 单元钉住 gRPC 端点形态校验（#98）：合规 host:port
// 放行；URL 形态（含 "://" 或 "/"）必须报错——URL 端点走 Protocol:"http"。
func TestValidateGRPCEndpoint(t *testing.T) {
	for _, ok := range []string{"localhost:4317", "otel-collector:4317", "127.0.0.1:4317", "[::1]:4317"} {
		if err := validateGRPCEndpoint(ok); err != nil {
			t.Errorf("validateGRPCEndpoint(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"http://127.0.0.1:4317", "https://otel-collector:4317", "otel-collector:4317/v1/traces", "otel-collector:4317/"} {
		if err := validateGRPCEndpoint(bad); err == nil {
			t.Errorf("validateGRPCEndpoint(%q) = nil, want error", bad)
		}
	}
}
