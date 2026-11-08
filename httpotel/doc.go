// Package httpotel 为标准库 net/http 提供 OpenTelemetry 追踪的 Transport 封装。
//
// 职责划分（#79）：
//   - [Transport] 返回预配置的**裸** http.Transport（仅默认参数，不含插桩）；
//   - [WrapTransport] 将任意 http.RoundTripper 包装为支持 OpenTelemetry 的
//     Transport，自动为每个出站 HTTP 请求创建 Trace Span，记录请求方法、
//     URL、状态码和耗时，实现 HTTP 客户端层的全链路追踪。
//
// 需要追踪的组合用法：WrapTransport(Transport())。
package httpotel
