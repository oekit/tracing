// Package tracing 提供 OpenTelemetry 分布式追踪初始化。
//
// 直接基于 go.opentelemetry.io/otel + OTLP gRPC 导出器实现（不依赖任何
// HTTP/RPC 框架，纯 Kitex 服务引用本包不会引入 Hertz），一行代码完成：
//   - OTLP gRPC Trace Exporter + BatchSpanProcessor
//   - OTLP gRPC Metrics Exporter（15s 周期读取）+ Go Runtime 指标采集
//   - B3 + OpenTracing + W3C TraceContext + Baggage 传播器
//   - service.name 资源属性
//
// # 快速接入
//
//	provider, err := tracing.Init(tracing.Config{
//	    ServiceName: "wechat",
//	    Endpoint:    "otel-collector:4317",
//	    Insecure:    true,
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer provider.Shutdown(context.Background())
//
//	// Hertz server tracing（通过 instrumentation/hertzotel 接入）
//	tracing := hertzotel.NewServerTracing()
//	h := server.New(server.WithHostPorts(":8080"), tracing.Option)
//	h.Use(tracing.Middleware)
//
//	// ehttp client tracing
//	client := ehttp.NewClient("moyu", ehttp.WithTracing())
//
// # 配置说明
//
//   - ServiceName: 服务名称，写入 resource 的 service.name 属性
//   - Endpoint: OTLP gRPC 地址，如 "otel-collector:4317"；为空时不创建
//     导出器、不发起网络连接（仅安装本地 provider）
//   - Insecure: 是否跳过 TLS（本地开发用 true，生产用 false）
//   - SampleRate: 采样率，0.0~1.0；0 < SampleRate < 1.0 时启用
//     ParentBased(TraceIDRatioBased)，其余（含 <= 0）为全量采样
//
// # 重复调用
//
// Init 会安装全局 provider；重复调用会自动关闭上一个被替换的 provider
// 后再安装新实例。常规服务应只 Init 一次（bootstrap 已保证）。
//
// # 与 bootstrap 集成
//
// bootstrap.Run 会自动初始化 tracing：
//
//	bootstrap.Run(bootstrap.Config{
//	    Tracing: &tracing.Config{
//	        Endpoint: "otel-collector:4317",
//	    },
//	}, func(app *bootstrap.App) error {
//	    // ...
//	})
//
// # 子包
//
//   - instrumentation/hertzotel: Hertz server/client 追踪（含 NewServerTracing 成对 API）
//   - instrumentation/kitexotel: Kitex server/client 追踪
//   - instrumentation/httpotel: HTTP 客户端追踪（标准库 http.Client）
//   - instrumentation/redis: Redis 追踪
package tracing
