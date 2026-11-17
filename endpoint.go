package tracing

import "errors"

// OTLP 端点污染守卫的共享错误构造（#182）。
//
// gRPC 侧 validateGRPCEndpoint（init.go）与 OTLP/HTTP 侧
// parseOTLPHTTPEndpoint（otlphttp.go，scheme 与无 scheme 两个分支）各自维护
// 域内守卫——禁令域不同可不同：HTTP 多 fragment 一道（url.Parse 面的
// u.Fragment），gRPC 多 whitespace 一道（拨号目标面；HTTP 的 url.Parse 对
// 空白天然非法）。但「错误不回显端点原值 + 指向 Config.Headers」的错误构造
// 是同一族：此前九个调用点各自手拼同构文案，句式与措辞跨文件漂移无从察觉
// （#99/#123/#133 三轮各写一遍）。本文件把六条文案收敛为四个构造器——每条
// 文案在仓内恰好出现一次，共同契约由构造器单点保证：
//
//   - **不回显端点原值**：污染串大概率内嵌凭据/令牌，进日志与错误信息即泄漏
//     （#99 叙事）；构造器签名不接收端点值，回显在类型上不可能；
//   - 凭据/query/fragment 型统一指向 Config.Headers（两侧鉴权的唯一修复面：
//     gRPC OTLP metadata / HTTP 头）；whitespace 型例外——修复是 trim，
//     与鉴权无关；
//   - "tracing: " 前缀与「主体 + 禁止形态 + 修复指引」句式统一。
//
// 同步义务（两侧互指）：新增污染形态或改禁令域时，两侧守卫同步评估，勿单侧
// 加禁——gRPC 侧义务注释见 init.go validateGRPCEndpoint，HTTP 侧见
// otlphttp.go parseOTLPHTTPEndpoint 的无 scheme 分支注释。

// errEndpointCredentials 构造「端点内嵌凭据」拒绝错误（gRPC/HTTP 两侧共享）。
// protocol 取 ProtocolGRPC / ProtocolHTTP（与 resolveProtocol 的归一化值同域）。
func errEndpointCredentials(protocol string) error {
	if protocol == ProtocolGRPC {
		return errors.New("tracing: gRPC Config.Endpoint must not embed credentials; use Config.Headers instead (OTLP metadata)")
	}
	return errors.New("tracing: OTLP/HTTP endpoint must not embed credentials; use Config.Headers instead")
}

// errEndpointQuery 构造「端点携带 query」拒绝错误（gRPC/HTTP 两侧共享）：
// query token 型鉴权会被传输面静默丢弃（#99），两侧各附各自传输面的原因说明
// ——gRPC 侧说明 query 根本不消费、token 静默丢弃，HTTP 侧说明同指
// Config.Headers。
func errEndpointQuery(protocol string) error {
	if protocol == ProtocolGRPC {
		return errors.New("tracing: gRPC Config.Endpoint must not carry query parameters; the gRPC transport never consumes query, so the token would be silently dropped — use Config.Headers instead (OTLP metadata)")
	}
	return errors.New("tracing: OTLP/HTTP endpoint must not carry query parameters; use Config.Headers instead")
}

// errEndpointFragment 构造「端点携带 fragment」拒绝错误（仅 HTTP 侧守卫：
// fragment 本来就不会发给服务端，出现即配置错误）。
func errEndpointFragment() error {
	return errors.New("tracing: OTLP/HTTP endpoint must not carry a fragment; it is never sent to the server, use Config.Headers for auth")
}

// errEndpointWhitespace 构造「端点含空白」拒绝错误（仅 gRPC 侧守卫：拨号
// 目标被污染、Init 成功但导出永远拨不通；修复是 trim，与鉴权无关，故不指向
// Config.Headers）。
func errEndpointWhitespace() error {
	return errors.New("tracing: gRPC Config.Endpoint must be plain \"host:port\" without whitespace (a padded endpoint dials a polluted target and exports nothing); trim the value")
}
