package httpotel

import (
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Transport 返回一个预配置了默认参数的裸 *http.Transport。
//
// 返回值**不含** OpenTelemetry 插桩——直接把它挂到 http.Client 上，
// 出站请求零 span、零 traceparent 注入，与上下游链路静默断裂（#79）。
// 需要追踪时用 [WrapTransport] 包装：
//
//	client := &http.Client{Transport: WrapTransport(Transport())}
func Transport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// WrapTransport 为指定的 RoundTripper 包装 OpenTelemetry 链路追踪能力：
// 为每个出站 HTTP 请求创建 Trace Span，并注入/提取 traceparent 等传播头。
// 包内追踪能力的唯一入口——[Transport] 返回的是裸 Transport（#79）。
func WrapTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base)
}
