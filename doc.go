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
//	// Hertz server tracing（hertzotel 住在 github.com/oekit/hertz 仓）
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
//     ParentBased(TraceIDRatioBased)，0 与 >=1.0 为全量采样（0 会打 WARN
//     提示显式写 1.0）；负值/NaN 直接报错，不静默全量
//
// # 重复调用
//
// Init 会安装全局 provider；重复调用会替换全局实例并自动关闭被替换的旧
// provider——但关闭与全局替换**对称**：本次未产出 meterProvider（Endpoint
// 为空 / DisableMetrics=true）时旧的 MeterProvider 不关闭、继续持有全局位
// 导出，全局 otel.GetMeterProvider() 永远指向活实例。并发调用 Init 由
// 包级互斥串行化（#100）——「装全局→关前任」两步序列不会交错，全局位
// 不会指向已 Shutdown 僵尸实例。常规服务应只 Init 一次（bootstrap 已
// 保证）。
//
// # 与 bootstrap 集成
//
// bootstrap.Run 会自动初始化 tracing：
//
//	b, _ := bootstrap.New(bootstrap.WithServiceName("my-service"),
//	    bootstrapobs.WithTracing(tracing.Config{Endpoint: "otel-collector:4317"})) // bootstrap/obs
//	b.Run(func(app *bootstrap.App) error {
//	    // ...
//	})
//
// # 子包（装饰器显式接入——核心组件不做 tracing 强制注入）
//
//   - httpotel: 标准库 net/http 客户端追踪（otelhttp Transport 封装）
//   - redisotel: go-redis 追踪（redisotel.InstrumentTracing(client.Client())）
//   - gormotel: GORM 追踪插件（db.Use(gormotel.NewPlugin())）
//   - traceid: trace_id 生成/提取/桥接原语（NewID/FromContext/FromSpanContext；
//     HTTP 中间件正身在 oekit/middleware 的 hmw 标准栈）
//
// Hertz/Kitex 框架侧 instrumentation 分属 github.com/oekit/hertz（hertzotel/
// hertztracer）与 github.com/oekit/kitex（kitexotel）。
package tracing
