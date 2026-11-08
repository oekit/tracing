package httpotel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestTransport_Defaults(t *testing.T) {
	tr := Transport()
	if tr == nil {
		t.Fatal("Transport() returned nil")
	}
	if tr.MaxIdleConns != 100 {
		t.Errorf("MaxIdleConns = %d, want 100", tr.MaxIdleConns)
	}
	if tr.IdleConnTimeout.Seconds() != 90 {
		t.Errorf("IdleConnTimeout = %v, want 90s", tr.IdleConnTimeout)
	}
}

func TestWrapTransport_WrapsRoundTripper(t *testing.T) {
	base := http.DefaultTransport
	wrapped := WrapTransport(base)
	if wrapped == nil {
		t.Fatal("WrapTransport returned nil")
	}
	// 包装后应不再是原 RoundTripper
	if wrapped == base {
		t.Error("WrapTransport returned the same RoundTripper (not wrapped)")
	}
}

// TestTransport_InstrumentationContract 是 #79 的契约钉：Transport() 返回
// 裸 Transport（不含插桩，出站请求不注入 traceparent）；WrapTransport(
// Transport()) 才具备 OpenTelemetry 追踪（注入 traceparent）。函数级
// godoc 与包文档按此契约描述——若未来给 Transport() 偷偷加包装或
// WrapTransport 断链，本测试即红。
func TestTransport_InstrumentationContract(t *testing.T) {
	var sawTraceparent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawTraceparent = r.Header["Traceparent"]
	}))
	defer srv.Close()

	// otelhttp 默认走 global tracer provider / global propagator（otel 默认
	// 两者均为 noop）：换成真实采样 provider + W3C TraceContext 传播器，
	// 使被包装 transport 产生的 span 有效并注入 traceparent。
	prevTP := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample())))
	t.Cleanup(func() { otel.SetTracerProvider(prevTP) })
	prevProp := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(prevProp) })

	// 裸 Transport：零 span、零 traceparent 注入（文档明示的行为）。
	sawTraceparent = false
	bare := &http.Client{Transport: Transport()}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := bare.Do(req)
	if err != nil {
		t.Fatalf("bare Transport Do: %v", err)
	}
	resp.Body.Close()
	if sawTraceparent {
		t.Error("bare Transport injected traceparent — Transport() must stay un-instrumented per #79 contract (godoc says bare)")
	}

	// WrapTransport(Transport())：注入 traceparent（文档承诺的组合用法）。
	sawTraceparent = false
	traced := &http.Client{Transport: WrapTransport(Transport())}
	req2, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp2, err := traced.Do(req2)
	if err != nil {
		t.Fatalf("wrapped Transport Do: %v", err)
	}
	resp2.Body.Close()
	if !sawTraceparent {
		t.Error("WrapTransport(Transport()) did not inject traceparent — documented tracing path is broken")
	}
}
