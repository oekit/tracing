// Package redisotel 为 go-redis 提供 OpenTelemetry 追踪。
//
// 使用方式：
//
//	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
//	if err := redisotel.InstrumentTracing(rdb); err != nil {
//	    log.Fatal(err)
//	}
package redisotel

import (
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

// InstrumentTracing 为 go-redis 客户端注入 OpenTelemetry 追踪。
// 自动为每个 Redis 命令创建 Span，记录命令名、耗时、错误。
func InstrumentTracing(rdb redis.UniversalClient, opts ...redisotel.TracingOption) error {
	return redisotel.InstrumentTracing(rdb, opts...)
}
