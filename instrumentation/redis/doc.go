// Package redisotel 为 go-redis 客户端提供 OpenTelemetry 追踪插桩。
//
// 通过 InstrumentTracing 函数为 go-redis 客户端注入追踪能力，自动为每个 Redis 命令创建 Span，
// 记录命令名、执行耗时和错误信息，实现 Redis 访问层的全链路可观测性。
//
// 使用方式：
//
//	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
//	if err := redisotel.InstrumentTracing(rdb); err != nil {
//	    log.Fatal(err)
//	}
package redisotel
