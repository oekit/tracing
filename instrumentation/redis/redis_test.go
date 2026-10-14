package redisotel

import (
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestInstrumentTracing_RegistersWithoutError(t *testing.T) {
	// InstrumentTracing 只注册 tracing hook，不建立连接，故对未连接的 client 也应成功。
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:0"})
	defer rdb.Close()

	if err := InstrumentTracing(rdb); err != nil {
		t.Errorf("InstrumentTracing error: %v", err)
	}
}
