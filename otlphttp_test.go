package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TestParseOTLPHTTPEndpoint 端点解析（issue #23：下游配置面的三种写法）。
func TestParseOTLPHTTPEndpoint(t *testing.T) {
	cases := []struct {
		name       string
		endpoint   string
		insecure   bool
		wantHost   string
		wantBase   string
		wantSecure bool // true = https
		wantErr    bool
	}{
		{name: "base url", endpoint: "https://host", wantHost: "host", wantBase: "", wantSecure: true},
		{name: "base url with port", endpoint: "https://host:4318", wantHost: "host:4318", wantBase: "", wantSecure: true},
		{name: "base url with path", endpoint: "https://host/api/otel", wantHost: "host", wantBase: "/api/otel", wantSecure: true},
		{name: "http scheme is insecure", endpoint: "http://host:4318", wantHost: "host:4318", wantBase: "", wantSecure: false},
		{name: "full signal url", endpoint: "https://host/api/public/otel/v1/traces", wantHost: "host", wantBase: "/api/public/otel/v1/traces", wantSecure: true},
		{name: "trailing slash trimmed", endpoint: "https://host/api/otel/", wantHost: "host", wantBase: "/api/otel", wantSecure: true},
		{name: "host port without scheme", endpoint: "otel-collector:4318", wantHost: "otel-collector:4318", wantBase: "", wantSecure: true},
		{name: "host port without scheme insecure", endpoint: "otel-collector:4318", insecure: true, wantHost: "otel-collector:4318", wantBase: "", wantSecure: false},
		{name: "empty", endpoint: "", wantErr: true},
		{name: "unsupported scheme", endpoint: "ftp://host/x", wantErr: true},
		{name: "missing host", endpoint: "https:///path", wantErr: true},
		{name: "embedded credentials", endpoint: "https://user:pass@host/x", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseOTLPHTTPEndpoint(c.endpoint, c.insecure)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseOTLPHTTPEndpoint(%q) error = nil, want error", c.endpoint)
				}
				// 端点里的凭据不得出现在错误信息里
				if strings.Contains(err.Error(), "pass") || strings.Contains(err.Error(), "user:") {
					t.Errorf("error leaks endpoint credentials: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOTLPHTTPEndpoint(%q) error = %v, want nil", c.endpoint, err)
			}
			if got.hostPort != c.wantHost {
				t.Errorf("hostPort = %q, want %q", got.hostPort, c.wantHost)
			}
			if got.basePath != c.wantBase {
				t.Errorf("basePath = %q, want %q", got.basePath, c.wantBase)
			}
			if got.insecure == c.wantSecure {
				t.Errorf("insecure = %v, want %v", got.insecure, !c.wantSecure)
			}
		})
	}
}

// TestOTLPHTTPEndpoint_SignalPaths signal 路径推导：端点指到哪个 signal，
// 另一个按同前缀换名；否则基址后拼标准路径。
func TestOTLPHTTPEndpoint_SignalPaths(t *testing.T) {
	cases := []struct {
		name        string
		basePath    string
		wantTraces  string
		wantMetrics string
	}{
		{"bare base", "", "/v1/traces", "/v1/metrics"},
		{"base with path", "/api/otel", "/api/otel/v1/traces", "/api/otel/v1/metrics"},
		{"points at traces", "/api/public/otel/v1/traces", "/api/public/otel/v1/traces", "/api/public/otel/v1/metrics"},
		{"points at metrics", "/api/public/otel/v1/metrics", "/api/public/otel/v1/traces", "/api/public/otel/v1/metrics"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ep := otlpHTTPEndpoint{basePath: c.basePath}
			traces, metrics := ep.signalPaths()
			if traces != c.wantTraces {
				t.Errorf("traces path = %q, want %q", traces, c.wantTraces)
			}
			if metrics != c.wantMetrics {
				t.Errorf("metrics path = %q, want %q", metrics, c.wantMetrics)
			}
		})
	}
}

// TestResolveProtocol 协议归一化：缺省 grpc；未知取值报错（不静默回落）。
func TestResolveProtocol(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ProtocolGRPC},
		{in: "grpc", want: ProtocolGRPC},
		{in: "GRPC", want: ProtocolGRPC},
		{in: " http ", want: ProtocolHTTP},
		{in: "http", want: ProtocolHTTP},
		{in: "thrift", wantErr: true},
	}
	for _, c := range cases {
		got, err := resolveProtocol(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("resolveProtocol(%q) error = nil, want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveProtocol(%q) error = %v, want nil", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("resolveProtocol(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestInit_HTTPProtocol_ExportsWithHeaders 端到端：OTLP/HTTP 分支真的把 span
// 与 metric 发到 /v1/traces 与 /v1/metrics，且带上 Config.Headers。
//
// 这是 issue #23 的核心断言——此前 OTLP/HTTP 只能由下游自写装配，headers
// 根本无处可填。
func TestInit_HTTPProtocol_ExportsWithHeaders(t *testing.T) {
	var (
		mu       sync.Mutex
		paths    []string
		authSeen []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		authSeen = append(authSeen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{
		ServiceName: "http-svc",
		Endpoint:    srv.URL,
		Protocol:    ProtocolHTTP,
		Headers:     map[string]string{"Authorization": "Bearer test-token"},
	}
	p, err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	impl, ok := p.(*providerImpl)
	if !ok {
		t.Fatalf("Init() returned unexpected provider type %T", p)
	}
	if impl.meterProvider == nil {
		t.Fatal("http protocol must install a meter provider (metrics must not silently disappear)")
	}

	// 打一个 span + 一个 counter，然后强制 flush
	_, span := otel.Tracer("test").Start(context.Background(), "op")
	span.End()
	counter, cerr := otel.Meter("test").Int64Counter("test.counter")
	if cerr != nil {
		t.Fatalf("Int64Counter error: %v", cerr)
	}
	counter.Add(context.Background(), 1)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := impl.tracerProvider.ForceFlush(ctx); err != nil {
		t.Fatalf("tracer ForceFlush error: %v", err)
	}
	if err := impl.meterProvider.ForceFlush(ctx); err != nil {
		t.Fatalf("meter ForceFlush error: %v", err)
	}
	_ = p.Shutdown(ctx)

	mu.Lock()
	defer mu.Unlock()
	gotPaths := make(map[string]bool, len(paths))
	for _, pth := range paths {
		gotPaths[pth] = true
	}
	if !gotPaths["/v1/traces"] {
		t.Errorf("no request to /v1/traces, got paths %v", paths)
	}
	if !gotPaths["/v1/metrics"] {
		t.Errorf("no request to /v1/metrics, got paths %v", paths)
	}
	for i, a := range authSeen {
		if a != "Bearer test-token" {
			t.Errorf("request %d Authorization header = %q, want %q", i, a, "Bearer test-token")
		}
	}
}

// TestInit_HTTPProtocol_GlobalSemantics http 分支的全局副作用与 grpc 分支
// 完全一致：传播器装上、meter provider 装上、旧 provider 被关闭。
//
// issue #23 的失效形态是「下游自写装配漏掉这些」——全是静默缺口，所以这里
// 逐条钉住。
func TestInit_HTTPProtocol_GlobalSemantics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := Config{
		ServiceName: "http-svc",
		Endpoint:    srv.URL,
		Protocol:    ProtocolHTTP,
	}
	p1, err := Init(cfg)
	if err != nil {
		t.Fatalf("first Init() error: %v", err)
	}
	impl1, ok := p1.(*providerImpl)
	if !ok {
		t.Fatalf("Init() returned unexpected provider type %T", p1)
	}
	if impl1.meterProvider == nil {
		t.Error("http protocol must install a meter provider")
	}

	// 传播器：出站请求要带 traceparent，否则跨进程链路断（静默）
	carrier := propagation.MapCarrier{
		"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}
	extracted := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
	if sc := trace.SpanContextFromContext(extracted); !sc.IsValid() {
		t.Error("expected global propagator to extract a valid span context from traceparent")
	}

	// 第二次 Init：旧 provider 必须被自动关闭
	p2, err := Init(cfg)
	if err != nil {
		t.Fatalf("second Init() error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := p1.Shutdown(ctx); err == nil {
		t.Error("expected error: previous provider must already be shut down by the second Init")
	}
	_ = p2.Shutdown(ctx)
}

// TestInit_UnknownProtocolRejected 协议名写错要报错，不能静默回落 grpc。
func TestInit_UnknownProtocolRejected(t *testing.T) {
	_, err := Init(Config{
		ServiceName: "svc",
		Endpoint:    "localhost:4317",
		Protocol:    "thrift",
	})
	if err == nil {
		t.Fatal("expected error for unknown protocol, got nil")
	}
	if !strings.Contains(err.Error(), "thrift") {
		t.Errorf("error should name the bad protocol, got: %v", err)
	}
}

// TestInit_GRPCDefaultUnchanged 既有 gRPC 调用方零改动仍可用：Protocol 留空
// 走 grpc 分支，装配语义不变。
func TestInit_GRPCDefaultUnchanged(t *testing.T) {
	p, err := Init(Config{
		ServiceName: "grpc-svc",
		Endpoint:    "localhost:4317",
		Insecure:    true,
	})
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	impl, ok := p.(*providerImpl)
	if !ok {
		t.Fatalf("Init() returned unexpected provider type %T", p)
	}
	if impl.meterProvider == nil {
		t.Error("grpc default must still install a meter provider")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.Shutdown(ctx)
}
