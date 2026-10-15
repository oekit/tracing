// Package httpotel 为标准库 net/http 提供 OpenTelemetry 追踪的 Transport 封装。
//
// Transport 返回预配置的 http.Transport，WrapTransport 则将任意 http.RoundTripper 包装为
// 支持 OpenTelemetry 的 Transport，自动为每个出站 HTTP 请求创建 Trace Span，
// 记录请求方法、URL、状态码和耗时，实现 HTTP 客户端层的全链路追踪。
package httpotel
