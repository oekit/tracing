// Package traceid 提供 HTTP 请求链路追踪 ID（trace_id）的生成、存储键与
// context 提取/注入/桥接原语。
//
// trace_id 贯穿整个请求链路（日志、响应体、下游 RPC/MQ），是 ekit 可观测性的基石。
// 本包是 trace_id 的唯一真相源（single source of truth）：
//
//   - Key: 标准库 context 与 RequestContext 中存储 trace_id 的键名
//   - HeaderKey: HTTP 头名称（X-Trace-Id），用于跨服务透传
//   - IDLength: trace_id 的标准长度
//   - NewID: 生成一条符合本包规范的 trace_id（IDLength 位十六进制，#102）——
//     生成点唯一，hmw 标准栈与无框架入口都经此取新 ID，不留第二生成点
//   - ContextWith/FromContext: 标准 context 的注入/提取原语
//   - FromSpanContext: OTel SpanContext → trace_id 桥接——日志与 APM 双 ID
//     的关联点（#102）
//
// 本包通过 init() 向 logx 注册 trace 提取器，使 logx.Ctx* 方法能从 context
// 自动提取 trace_id，从而避免 logx 反向依赖 middleware（参见 logx 包文档）。
//
// HTTP 中间件正身（请求进入时透传 + RequestContext 注入）在 oekit/middleware
// 的 hmw 标准栈；本包只提供生成/key/提取/桥接原语，不做框架绑定。
//
// 用法:
//
//	// service 层：把 APM trace 桥接进日志链（日志 trace_id = APM trace id）
//	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
//	    ctx = traceid.ContextWith(ctx, traceid.FromSpanContext(sc))
//	}
//	traceID := traceid.FromContext(ctx)
//
//	// 无上游 trace 的入口（定时任务/MQ 消费等）：先生成再注入
//	ctx = traceid.ContextWith(ctx, traceid.NewID())
package traceid
