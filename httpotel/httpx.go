package httpxotel

import (
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Transport 创建一个带有默认参数并支持 OpenTelemetry 追踪的 *http.Transport。
func Transport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// WrapTransport 为指定的 RoundTripper 包装 OpenTelemetry 链路追踪能力。
func WrapTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base)
}
