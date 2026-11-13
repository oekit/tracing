// 独立测试模块（#122）：hertz 是「纯 Kitex 服务引用本包不会引入 Hertz」
// 契约的唯一例外面——只为 traceid 的 hertz assert / RequestContext 集成
// 测试存在，隔离在本模块的 go.mod 里，不随根模块流向任何消费方。
// CI 嵌套模块发现（build + test -race + tidy 门禁）自动覆盖本模块。
module github.com/oekit/tracing/tests

go 1.26.0

require (
	github.com/cloudwego/hertz v0.10.6
	github.com/oekit/tracing v0.4.4
	go.opentelemetry.io/otel/trace v1.47.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/oekit/logx v0.10.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
)

replace github.com/oekit/tracing => ../
