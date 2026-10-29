package tracing

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// OTLP/HTTP 的标准 signal 路径。
const (
	otlpHTTPTracesPath  = "/v1/traces"
	otlpHTTPMetricsPath = "/v1/metrics"
)

// otlpHTTPEndpoint 是解析后的 OTLP/HTTP 端点。
type otlpHTTPEndpoint struct {
	// hostPort "host:port"，传给 WithEndpoint。
	hostPort string
	// basePath 基址路径前缀（不含 signal 段），如 "/api/public/otel"。
	basePath string
	// insecure 是否走 http:// 而非 https://。
	insecure bool
}

// parseOTLPHTTPEndpoint 解析 OTLP/HTTP 端点。
//
// 接受三种写法（issue #23 的下游配置面就是这几种）：
//
//  1. 完整 signal URL —— "https://host/api/public/otel/v1/traces"：
//     signal 段原样保留，另一个 signal 按同前缀换名推导；
//  2. 基址 —— "https://host" / "https://host/api/public/otel"：
//     后接标准 signal 路径 /v1/traces 与 /v1/metrics；
//  3. 无 scheme 的 host:port —— "otel-collector:4318"：
//     scheme 由 Config.Insecure 决定。
//
// 不接受 URL 内嵌凭据（http://user:pass@host）：凭据走 Config.Headers——
// URL 里的凭据会进访问日志、代理日志与错误栈。
func parseOTLPHTTPEndpoint(endpoint string, insecure bool) (otlpHTTPEndpoint, error) {
	ep := otlpHTTPEndpoint{insecure: insecure}
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return ep, errors.New("tracing: empty OTLP/HTTP endpoint")
	}
	if !strings.Contains(raw, "://") {
		// 无 scheme：host:port 形态，scheme 由 Insecure 决定。
		ep.hostPort = raw
		return ep, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			// 只回显内层原因，不回显原始 URL（可能带凭据/令牌）。
			return ep, fmt.Errorf("tracing: invalid OTLP/HTTP endpoint: %w", ue.Err)
		}
		return ep, errors.New("tracing: invalid OTLP/HTTP endpoint")
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		ep.insecure = true
	case "https":
		ep.insecure = false
	default:
		return ep, fmt.Errorf("tracing: unsupported OTLP/HTTP endpoint scheme %q (want http or https)", u.Scheme)
	}
	if u.User != nil {
		return ep, errors.New("tracing: OTLP/HTTP endpoint must not embed credentials; use Config.Headers instead")
	}
	if u.Host == "" {
		return ep, errors.New("tracing: OTLP/HTTP endpoint missing host")
	}
	ep.hostPort = u.Host
	ep.basePath = strings.TrimSuffix(u.Path, "/")
	return ep, nil
}

// signalPaths 推导 traces / metrics 的 URL 路径。
//
// 端点已指到某个 signal（以 /v1/traces 或 /v1/metrics 结尾）→ 该 signal 原样用，
// 另一个按同前缀换名；否则基址后拼标准 signal 路径。
func (e otlpHTTPEndpoint) signalPaths() (traces, metrics string) {
	base := e.basePath
	switch {
	case strings.HasSuffix(base, otlpHTTPTracesPath):
		return base, strings.TrimSuffix(base, otlpHTTPTracesPath) + otlpHTTPMetricsPath
	case strings.HasSuffix(base, otlpHTTPMetricsPath):
		return strings.TrimSuffix(base, otlpHTTPMetricsPath) + otlpHTTPTracesPath, base
	default:
		return base + otlpHTTPTracesPath, base + otlpHTTPMetricsPath
	}
}

// newHTTPExportingProviders 创建带 OTLP/HTTP 导出器的 Tracer/Meter Provider。
//
// 与 grpc 分支**同样的全局语义**：MeterProvider（含 Go runtime 指标）一并装上，
// 传播器由 Init 统一设置，被替换的旧 provider 由 Init 统一关闭——
// 「换协议」退回「换一条传输」，而不是「换一整套可观测性语义」。
//
// 此前 OTLP/HTTP 只能由下游绕开 tracing.Init 自写整套装配，漏掉传播器/
// 指标/旧 provider 关闭时全是**静默**缺口（链路断但不报错）。
func newHTTPExportingProviders(cfg Config, sampler sdktrace.Sampler, res *resource.Resource) (*sdktrace.TracerProvider, *metric.MeterProvider, error) {
	ep, err := parseOTLPHTTPEndpoint(cfg.Endpoint, cfg.Insecure)
	if err != nil {
		return nil, nil, err
	}
	tracePath, metricPath := ep.signalPaths()

	// ---- tracing exporter ----
	var traceOpts []otlptracehttp.Option
	traceOpts = append(traceOpts,
		otlptracehttp.WithEndpoint(ep.hostPort),
		otlptracehttp.WithURLPath(tracePath),
	)
	if ep.insecure {
		traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		traceOpts = append(traceOpts, otlptracehttp.WithHeaders(cfg.Headers))
	}
	traceExp, err := otlptrace.New(context.Background(), otlptracehttp.NewClient(traceOpts...))
	if err != nil {
		return nil, nil, fmt.Errorf("tracing: create otlp http trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sampler),
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(traceExp)),
	)

	// ---- metrics exporter（与 grpc 分支等价）----
	// ⭐ DisableMetrics=true ⇒ 不建 metrics 导出器（与 grpc 分支同一纪律）：
	// collector 侧无 metrics 管线时推送只会每 15s 换一条 Unimplemented 报错，
	// 且这些指标本就无处可去 ⇒ 关掉即止噪，待配好 metrics 管线后改回 false 即可恢复。
	if cfg.DisableMetrics {
		return tp, nil, nil
	}
	var metricOpts []otlpmetrichttp.Option
	metricOpts = append(metricOpts,
		otlpmetrichttp.WithEndpoint(ep.hostPort),
		otlpmetrichttp.WithURLPath(metricPath),
	)
	if ep.insecure {
		metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		metricOpts = append(metricOpts, otlpmetrichttp.WithHeaders(cfg.Headers))
	}
	metricExp, err := otlpmetrichttp.New(context.Background(), metricOpts...)
	if err != nil {
		_ = tp.Shutdown(context.Background()) // 回滚已创建的 trace provider
		return nil, nil, fmt.Errorf("tracing: create otlp http metric exporter: %w", err)
	}

	reader := metric.WithReader(metric.NewPeriodicReader(metricExp, metric.WithInterval(metricExportInterval)))
	mp := metric.NewMeterProvider(reader, metric.WithResource(res))

	// 与 grpc 分支同一纪律：必须显式指定 provider（无参 runtime.Start() 在
	// 调用时刻捕获的全局还是 noop，runtime 指标会静默丢失）。
	if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
		_ = mp.Shutdown(context.Background())
		_ = tp.Shutdown(context.Background())
		return nil, nil, fmt.Errorf("tracing: start runtime metrics collector: %w", err)
	}
	return tp, mp, nil
}
