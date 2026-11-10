package tracing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/oekit/logx"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	b3prop "go.opentelemetry.io/contrib/propagators/b3"
	otprop "go.opentelemetry.io/contrib/propagators/ot"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// tlog 是本包的结构化日志出口（Delegating 懒解析：消费方 SetDefault 后
// 自动跟随）。
var tlog = logx.Delegating("tracing")

// metricExportInterval 是 OTLP 指标周期读取器的读取间隔（与旧
// obs-opentelemetry/provider 的 15s 默认值一致）。
const metricExportInterval = 15 * time.Second

// abandonShutdownTimeout 是被替换旧 provider 的兜底关闭超时。
const abandonShutdownTimeout = 5 * time.Second

// installed 保存最近一次 Init 成功安装的 provider。
// 再次 Init 会替换全局 TracerProvider/MeterProvider，被替换的旧 provider
// 若不主动关闭，其批量 span 处理器与 OTLP 导出器会随进程泄漏，
// 因此在新 Init 成功后自动关闭上一个 provider。
// ⚠️ 只沿这一条单链追踪**不够**关 meter 面（#68）：未产出 meterProvider 的
// Init（Endpoint 空 / DisableMetrics）按 #52 语义保留旧 meterProvider 活着
// 继续导出 ⇒ 单链上没有它的位置，等下一个产出 meterProvider 的 Init 替换
// 全局后，它就彻底无人引用了——meter 面另见 lastMeterProvider 链。
var installed atomic.Pointer[providerImpl]

// lastMeterProvider 单独追踪「最近一次**产出 meterProvider** 的 Init」
// （#68）：meter 面的关闭判据不能挂在 installed 单链上——
// 「metrics on→off→on」序列里，off 的 impl 没有 meterProvider（#52 语义下
// 第一个 on 的 meterProvider 仍持有全局位、必须活着），若只沿 installed
// 追踪，第二个 on 替换全局 meter 面后第一个 on 的 meterProvider 永久孤儿：
// 周期读取器（15s）与 runtime collector 每 15s 向旧 endpoint 导出直到进程
// 结束（ghost 指标流 + 泄漏 goroutine）。本次 Init 装了 meterProvider
// （= 全局 meter 面被本次替换）时，沿本链把上一个持 meterProvider 的 impl
// 的 meter 面关掉（shutdownOnce 幂等，重复关闭无害）。
var lastMeterProvider atomic.Pointer[providerImpl]

// Provider 是 tracing provider 的最小接口，仅暴露 Shutdown 能力。
// 用于解耦调用方对具体 OTel SDK 实现的直接依赖。
type Provider interface {
	Shutdown(ctx context.Context) error
}

// 导出协议常量（Config.Protocol 取值）。
const (
	// ProtocolGRPC OTLP/gRPC 导出协议——Config.Protocol 的缺省值。
	ProtocolGRPC = "grpc"
	// ProtocolHTTP OTLP/HTTP 导出协议。Langfuse / SaaS 类观测后端基本只提供
	// HTTP 接收端，走这条分支。
	ProtocolHTTP = "http"
)

// Config 是 OpenTelemetry Provider 的配置。
type Config struct {
	// ServiceName 服务名称，如 "wechat"、"fuxi_engine"。
	ServiceName string

	// Endpoint OTLP 端点。
	//
	// grpc 协议：host:port 形态，如 "otel-collector:4317"；URL 形态
	//（含 "://" 或 "/"）直接报错（#98，URL 端点请设 Protocol:"http"）。
	// http 协议：接受完整 signal URL（"https://host/api/otel/v1/traces"）、
	// 基址（"https://host"、"https://host/api/otel"）或无 scheme 的
	// host:port（"otel-collector:4318"）；详见 otlphttp.go 的推导规则。
	//
	// 为空时不创建 OTLP 导出器、不发起任何网络连接，仅安装本地 provider。
	Endpoint string

	// Insecure 是否跳过 TLS（本地开发用 true，生产用 false）。
	// http 协议下 Endpoint 自带 scheme 时以 scheme 为准（http:// 即不加密）。
	Insecure bool

	// SampleRate 采样率，0.0~1.0。
	// 0 < SampleRate < 1.0 时启用 ParentBased(TraceIDRatioBased) 采样；
	// SampleRate >= 1.0 或 == 0 均为全量采样——0 会打 WARN 说明「=全量」
	// （运维把采样率写成 0 常是想要「不采样」或「用默认」，静默全量在
	// 高 QPS 服务上表现为成本暴涨，#59）；负值/NaN 直接报错，不再静默
	// 全量（与「未知 Protocol 直接报错不静默回落」同一设计哲学）。
	SampleRate float64

	// Protocol 导出协议：ProtocolGRPC（缺省）/ ProtocolHTTP。
	//
	// 两种协议下 Init 的全局副作用完全一致——TracerProvider + MeterProvider
	// （含 Go runtime 指标）+ 传播器 + 关闭被替换的旧 provider，差别只在
	// 传输层。未知取值直接报错，不静默回落 grpc（静默回落会让「协议名写错」
	// 表现为「连不上」而不是「协议名错了」）。
	Protocol string

	// Headers 随每次导出请求带上的附加头（认证等）。
	//
	// 两种协议都生效：http 形态是 HTTP 头，grpc 形态是 gRPC metadata。
	// 属配置面——本包不会把头写进日志或错误信息。
	Headers map[string]string

	// DisableMetrics 不导出 OTLP Metrics（含 Go runtime 指标）。
	//
	// 零值 false = **保持既有行为**（导出 metrics）——新增字段不改任何既有调用方的
	// 行为面（无 opt-in 陷阱）。
	//
	// 什么时候该置 true：**collector 侧没有 metrics 管线**时。OTel collector 只为
	// 配了管线的 signal 注册 gRPC service；没有 metrics 管线时它对 MetricsService 回
	// `Unimplemented: unknown service ...MetricsService`，而本包的周期读取器每 15s
	// 推一次 ⇒ 表现为「服务正常但日志每 15s 一条导出失败」。
	// 此时 metrics 本来也无处可去（collector 会丢弃）⇒ 关掉推送即**止噪**，
	// 待 collector 配好 metrics 管线后把该字段改回 false 即可恢复。
	//
	// 置 true 时：不创建 OTLP Metrics 导出器、不启动 Go runtime 指标采集、
	// 全局 MeterProvider 保持 noop（业务侧 otel.Meter 打点照常可用，只是不导出）。
	DisableMetrics bool
}

// Init 初始化 OpenTelemetry 全局 TracerProvider、Propagator（Endpoint 非空时
// 还包含 MeterProvider 与 Go runtime 指标采集），返回 Provider 接口用于
// 关闭时 flush。进程退出前调用 provider.Shutdown(ctx) 确保数据 flush。
//
// 行为说明：
//   - Propagator: B3 + OpenTracing + W3C TraceContext + Baggage（与旧
//     hertz-contrib/obs-opentelemetry/provider 的默认组合一致）
//   - 采样: 0 < SampleRate < 1.0 → ParentBased(TraceIDRatioBased(rate))；
//     SampleRate >= 1.0 或 == 0 → 全量采样（0 会打 WARN 提示显式写 1.0）；
//     负值/NaN 是配置错误，直接报错（不静默全量）
//   - Endpoint 非空时同时初始化 OTLP Metrics（15s 周期读取 + runtime 指标）；
//     Endpoint 为空时跳过导出器初始化，不产生网络连接
//   - 导出协议由 Config.Protocol 选：grpc（缺省）/ http。**两条分支的全局
//     副作用完全相同**——传播器、指标、旧 provider 关闭都不因换协议而丢失
//   - 重复调用 Init 会替换全局 provider 并自动关闭被替换的旧 provider，
//     但关闭与全局替换**对称**（#50/#52/#58/#68）：本次未产出 meterProvider
//     （Endpoint 为空 / DisableMetrics=true）时，旧的 MeterProvider 不关闭、
//     继续持有全局位导出——全局 otel.GetMeterProvider() 永远指向活实例，
//     不会出现指向已 Shutdown 僵尸实例的窗口。被替换 provider 的原持有方
//     再调用 Shutdown 会得到 already-shutdown 类错误，可安全忽略。
//     meter 面的关闭沿「最近产出 meterProvider 的 Init」链追踪（#68）：
//     「on→off→on」序列中第一个 on 的 MeterProvider 会在第二个 on 替换
//     全局后被关闭，不再永久孤儿导出。常规服务应只 Init 一次
func Init(cfg Config) (Provider, error) {
	if err := validateSampleRate(cfg.SampleRate); err != nil {
		return nil, err
	}
	if cfg.SampleRate == 0 {
		tlog.Warn("SampleRate=0 按全量采样处理（不是「不采样」也不是「用默认」）；"+
			"确认要全量请显式写 1.0，要按比例采样请写 0<r<1.0",
			"sample_rate", cfg.SampleRate, "service", cfg.ServiceName)
	}

	sampler := buildSampler(cfg.SampleRate)
	res := newResource(cfg)

	p := &providerImpl{}
	if cfg.Endpoint == "" {
		// 无导出目标：仅安装本地 provider，不创建导出器、不发起网络连接
		p.tracerProvider = sdktrace.NewTracerProvider(
			sdktrace.WithSampler(sampler),
			sdktrace.WithResource(res),
		)
	} else {
		tp, mp, err := newExportingProviders(cfg, sampler, res)
		if err != nil {
			return nil, err
		}
		p.tracerProvider = tp
		p.meterProvider = mp
	}

	otel.SetTracerProvider(p.tracerProvider)
	if p.meterProvider != nil {
		otel.SetMeterProvider(p.meterProvider)
	}
	otel.SetTextMapPropagator(defaultPropagator())

	// 关闭被本次 Init 替换的旧 provider（其已不再被全局引用，继续运行只会泄漏）
	// ⚠️ 关闭必须与全局替换**对称**（#50/#52），且 tracer / meter 两面**分链
	// 追踪**（#68）：
	//   - tracer 面始终被本次替换 ⇒ 上一任 impl 的 tracerProvider 照常关闭；
	//   - meter 面：本次未产出 meterProvider（Endpoint 为空 / DisableMetrics=true）
	//     时全局 MeterProvider 仍指向旧实例，谁都不能关（#52）；本次产出时，
	//     要关的是**上一个产出 meterProvider 的 impl**（lastMeterProvider 链）
	//     ——不是上一任 impl：on→off→on 序列里上一任（off）没有 meter 面，
	//     真正被替换出局的是更早那个 on 的 meterProvider（#68 孤儿）。
	if prev := installed.Swap(p); prev != nil {
		abandonCtx, cancel := context.WithTimeout(context.Background(), abandonShutdownTimeout)
		defer cancel()
		_ = prev.tracerProvider.Shutdown(abandonCtx) // tracer 面始终被本次替换
	}
	if p.meterProvider != nil {
		if prevMP := lastMeterProvider.Swap(p); prevMP != nil && prevMP.meterProvider != nil {
			abandonCtx, cancel := context.WithTimeout(context.Background(), abandonShutdownTimeout)
			defer cancel()
			_ = prevMP.meterProvider.Shutdown(abandonCtx) // 已出局，尽力关闭即可
		}
	}
	return p, nil
}

// newExportingProviders 按 Config.Protocol 选导出器分支，返回带 OTLP 导出器的
// Tracer/Meter Provider。两条分支产出的结构一致——下游拿到的都是「装了指标、
// 装了传播器、旧 provider 会被关掉」的完整装配，差别只在传输层。
func newExportingProviders(cfg Config, sampler sdktrace.Sampler, res *resource.Resource) (*sdktrace.TracerProvider, *metric.MeterProvider, error) {
	protocol, err := resolveProtocol(cfg.Protocol)
	if err != nil {
		return nil, nil, err
	}
	if protocol == ProtocolHTTP {
		return newHTTPExportingProviders(cfg, sampler, res)
	}
	return newGRPCExportingProviders(cfg, sampler, res)
}

// resolveProtocol 归一化 Protocol 取值：空 → grpc；大小写与空白不敏感。
// 未知取值报错而非静默回落 grpc——回落会让「协议名写错」表现为「连不上」。
func resolveProtocol(p string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", ProtocolGRPC:
		return ProtocolGRPC, nil
	case ProtocolHTTP:
		return ProtocolHTTP, nil
	default:
		return "", fmt.Errorf("tracing: unsupported protocol %q (want %q or %q)", p, ProtocolGRPC, ProtocolHTTP)
	}
}

// newGRPCExportingProviders 创建带 OTLP gRPC 导出器的 Tracer/Meter Provider。
// tracing 与 metrics 共用 Config.Endpoint 与 Insecure 配置。
func newGRPCExportingProviders(cfg Config, sampler sdktrace.Sampler, res *resource.Resource) (*sdktrace.TracerProvider, *metric.MeterProvider, error) {
	if err := validateGRPCEndpoint(cfg.Endpoint); err != nil {
		return nil, nil, err
	}

	// ---- tracing exporter ----
	var traceOpts []otlptracegrpc.Option
	traceOpts = append(traceOpts, otlptracegrpc.WithEndpoint(cfg.Endpoint))
	if cfg.Insecure {
		traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		traceOpts = append(traceOpts, otlptracegrpc.WithHeaders(cfg.Headers))
	}
	traceExp, err := otlptrace.New(context.Background(), otlptracegrpc.NewClient(traceOpts...))
	if err != nil {
		return nil, nil, fmt.Errorf("tracing: create otlp trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sampler),
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(traceExp)),
	)

	// ---- metrics exporter（与旧 provider 默认行为等价）----
	// ⭐ DisableMetrics=true ⇒ 不建 metrics 导出器（collector 侧无 metrics 管线时
	// 推送只会每 15s 换一条 Unimplemented 报错，且这些指标本就无处可去）。
	if cfg.DisableMetrics {
		return tp, nil, nil
	}
	var metricOpts []otlpmetricgrpc.Option
	metricOpts = append(metricOpts, otlpmetricgrpc.WithEndpoint(cfg.Endpoint))
	if cfg.Insecure {
		metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		metricOpts = append(metricOpts, otlpmetricgrpc.WithHeaders(cfg.Headers))
	}
	metricExp, err := otlpmetricgrpc.New(context.Background(), metricOpts...)
	if err != nil {
		_ = tp.Shutdown(context.Background()) // 回滚已创建的 trace provider
		return nil, nil, fmt.Errorf("tracing: create otlp metric exporter: %w", err)
	}

	reader := metric.WithReader(metric.NewPeriodicReader(metricExp, metric.WithInterval(metricExportInterval)))
	mp := metric.NewMeterProvider(reader, metric.WithResource(res))

	// 必须显式指定 provider：无参 runtime.Start() 在调用时刻捕获
	// otel.GetMeterProvider()——此刻全局还是 noop（SetMeterProvider 在
	// Init 返回后才执行），runtime 指标全部静默丢失；重复 Init 时则挂到
	// 上一轮已被 Shutdown 的 provider 上同样丢失
	if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
		_ = mp.Shutdown(context.Background())
		_ = tp.Shutdown(context.Background())
		return nil, nil, fmt.Errorf("tracing: start runtime metrics collector: %w", err)
	}
	return tp, mp, nil
}

// buildSampler 将 SampleRate 转换为采样器。
// 0 < rate < 1.0 → ParentBased(TraceIDRatioBased(rate))，尊重上游采样决策；
// rate >= 1.0 或 == 0 → AlwaysSample（全量采样）。
// 负值/NaN 由 Init 的 validateSampleRate 拒绝（#59），不会走到这里；
// 本函数保持全定义域可用的纯映射。
func buildSampler(rate float64) sdktrace.Sampler {
	if rate > 0 && rate < 1.0 {
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(rate))
	}
	return sdktrace.AlwaysSample()
}

// validateGRPCEndpoint 校验 gRPC 分支的 Endpoint 形态（#98）。
//
// gRPC 导出器把 Endpoint 当拨号目标（host:port）解析，URL 形态解析不出可用
// 地址：Init 成功返回 provider 但一次都不拨号，span/metric 在后台导出重试里
// 无声耗尽（「服务正常但 APM 全黑」）。与 http 分支的端点校验、resolveProtocol
// 对未知协议的处置同一纪律：不合规输入报错，不静默断供；URL 形态端点请改设
// Protocol:"http"（OTLP/HTTP 分支原生接受 URL）。错误不回显端点原值——URL
// 形态端点可能内嵌凭据/令牌，不进日志与错误信息。
func validateGRPCEndpoint(endpoint string) error {
	if strings.Contains(endpoint, "://") || strings.Contains(endpoint, "/") {
		return errors.New("tracing: gRPC Config.Endpoint must be \"host:port\" (e.g. \"otel-collector:4317\"); " +
			"URL-form endpoint must set Protocol: \"http\"")
	}
	return nil
}

// validateSampleRate 校验采样率配置（#59）。
// 负值与 NaN 是配置错误（YAML 类型写错/单位写错的高频产物），直接报错：
// 静默按全量采样会把配置笔误表现为「线上成本暴涨」而非「启动报错」，
// 与 resolveProtocol 对未知协议「报错不静默回落」的哲学一致。
// 0 不报错（零值缺省语义保持全量），但 Init 会打 WARN 点明「=全量采样」。
func validateSampleRate(rate float64) error {
	if math.IsNaN(rate) || rate < 0 {
		return fmt.Errorf("tracing: invalid SampleRate %v (want 0.0~1.0; 0 与 >=1.0 均为全量采样，负值/NaN 是配置错误)", rate)
	}
	return nil
}

// defaultPropagator 返回全局传播器组合。
// 与旧 obs-opentelemetry/provider 默认组合保持一致：
// B3 + OpenTracing + W3C Baggage + W3C TraceContext。
func defaultPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		b3prop.New(),
		otprop.OT{},
		propagation.Baggage{},
		propagation.TraceContext{},
	)
}

// newResource 构建带 service.name 的资源；主机/进程探测失败时退回
// resource.Default()，但仍尽力保留 service.name。
func newResource(cfg Config) *resource.Resource {
	res, err := resource.New(context.Background(),
		resource.WithHost(),
		resource.WithFromEnv(),
		resource.WithProcessPID(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(semconv.ServiceNameKey.String(cfg.ServiceName)),
	)
	if err == nil {
		return res
	}
	if merged, merr := resource.Merge(resource.Default(),
		resource.NewWithAttributes("", semconv.ServiceNameKey.String(cfg.ServiceName))); merr == nil {
		return merged
	}
	return resource.Default()
}

// providerImpl 是 Init 返回的 Provider 实现，持有全局态对应的两类 provider。
type providerImpl struct {
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *metric.MeterProvider
}

// Shutdown 关闭并 flush tracing 与 metrics provider。
// TracerProvider.Shutdown 会 flush 批量 span 处理器并关闭 OTLP 导出器，重复调用幂等；
// MeterProvider.Shutdown 会 flush 周期读取器，重复调用返回 "already shutdown" 类错误。
// 因此被重复 Init 自动关闭的 provider，其原持有方再调用本方法时会收到错误，可安全忽略。
func (p *providerImpl) Shutdown(ctx context.Context) error {
	var errs []error
	if p.tracerProvider != nil {
		if err := p.tracerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if p.meterProvider != nil {
		if err := p.meterProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
