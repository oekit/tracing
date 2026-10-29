package tracing

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
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
