// Package traceid 负责 HTTP 请求链路追踪 ID（trace_id）的生成、透传与提取。
//
// trace_id 贯穿整个请求链路（日志、响应体、下游 RPC/MQ），是 ekit 可观测性的基石。
// 本包是 trace_id 的唯一真相源（single source of truth）：
//
//   - Key: 标准库 context 与 RequestContext 中存储 trace_id 的键名
//   - HeaderKey: HTTP 头名称（X-Trace-Id），用于跨服务透传
//   - IDLength: trace_id 的标准长度
//
// 本包通过 init() 向 logx 注册 trace 提取器，使 logx.Ctx* 方法能从 context
// 自动提取 trace_id，从而避免 logx 反向依赖 middleware（参见 logx 包文档）。
//
// 用法:
//
//	// 注册中间件链（生成 + 桥接）
//	h.Use(traceid.Middleware()...)
//
//	// 在 service 层提取（用于日志关联）
//	traceID := traceid.FromContext(ctx)
package traceid
