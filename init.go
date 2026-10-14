package tracing

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

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

// metricExportInterval 是 OTLP 指标周期读取器的读取间隔（与旧
// obs-opentelemetry/provider 的 15s 默认值一致）。
const metricExportInterval = 15 * time.Second

// abandonShutdownTimeout 是被替换旧 provider 的兜底关闭超时。
const abandonShutdownTimeout = 5 * time.Second

// installed 保存最近一次 Init 成功安装的 provider。
// 再次 Init 会替换全局 TracerProvider/MeterProvider，被替换的旧 provider
// 若不主动关闭，其批量 span 处理器与 OTLP 导出器会随进程泄漏，
// 因此在新 Init 成功后自动关闭上一个 provider。
var installed atomic.Pointer[providerImpl]

// Provider 是 tracing provider 的最小接口，仅暴露 Shutdown 能力。
// 用于解耦调用方对具体 OTel SDK 实现的直接依赖。
type Provider interface {
	Shutdown(ctx context.Context) error
}

// Config 是 OpenTelemetry Provider 的配置。
type Config struct {
	// ServiceName 服务名称，如 "wechat"、"fuxi_engine"。
	ServiceName string

	// Endpoint OTLP gRPC 地址，如 "otel-collector:4317"。
	// 为空时不创建 OTLP 导出器、不发起任何网络连接，仅安装本地 provider。
	Endpoint string

	// Insecure 是否跳过 TLS（本地开发用 true，生产用 false）。
	Insecure bool

	// SampleRate 采样率，0.0~1.0。
	// 0 < SampleRate < 1.0 时启用 ParentBased(TraceIDRatioBased) 采样；
	// SampleRate >= 1.0 或 <= 0 均为全量采样（默认 1.0）——<=0 沿用旧版
	// "静默全量" 语义，不视为错误。
	SampleRate float64
}

// Init 初始化 OpenTelemetry 全局 TracerProvider、Propagator（Endpoint 非空时
// 还包含 MeterProvider 与 Go runtime 指标采集），返回 Provider 接口用于
// 关闭时 flush。进程退出前调用 provider.Shutdown(ctx) 确保数据 flush。
//
// 行为说明：
//   - Propagator: B3 + OpenTracing + W3C TraceContext + Baggage（与旧
//     hertz-contrib/obs-opentelemetry/provider 的默认组合一致）
//   - 采样: 0 < SampleRate < 1.0 → ParentBased(TraceIDRatioBased(rate))；
//     SampleRate >= 1.0 或 <= 0 → 全量采样
//   - Endpoint 非空时同时初始化 OTLP gRPC Metrics（15s 周期读取 + runtime 指标）；
//     Endpoint 为空时跳过导出器初始化，不产生网络连接
//   - 重复调用 Init 会自动关闭上一个 provider 后再安装新 provider；
//     被替换 provider 的原持有方再调用 Shutdown 会得到 already-shutdown
//     类错误，可安全忽略。常规服务应只 Init 一次
func Init(cfg Config) (Provider, error) {
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
	if prev := installed.Swap(p); prev != nil {
		abandonCtx, cancel := context.WithTimeout(context.Background(), abandonShutdownTimeout)
		defer cancel()
		_ = prev.Shutdown(abandonCtx) // 旧 provider 已被替换，这里尽力关闭即可
	}
	return p, nil
}

// newExportingProviders 创建带 OTLP gRPC 导出器的 Tracer/Meter Provider。
// tracing 与 metrics 共用 Config.Endpoint 与 Insecure 配置。
func newExportingProviders(cfg Config, sampler sdktrace.Sampler, res *resource.Resource) (*sdktrace.TracerProvider, *metric.MeterProvider, error) {
	// ---- tracing exporter ----
	var traceOpts []otlptracegrpc.Option
	traceOpts = append(traceOpts, otlptracegrpc.WithEndpoint(cfg.Endpoint))
	if cfg.Insecure {
		traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
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
	var metricOpts []otlpmetricgrpc.Option
	metricOpts = append(metricOpts, otlpmetricgrpc.WithEndpoint(cfg.Endpoint))
	if cfg.Insecure {
		metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
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
// rate >= 1.0 或 <= 0 → AlwaysSample（全量采样）。
func buildSampler(rate float64) sdktrace.Sampler {
	if rate > 0 && rate < 1.0 {
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(rate))
	}
	return sdktrace.AlwaysSample()
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
