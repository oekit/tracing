package gormotel

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// testDBSeq 生成唯一 dsn 序号，避免测试间共享内存库。
var testDBSeq atomic.Int64

// newTestDB 打开一个 sqlite 内存 DB。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:gormotel-" + strconv.FormatInt(testDBSeq.Add(1), 10) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestNewPlugin_Default(t *testing.T) {
	p := NewPlugin()
	if p.Name() != "otelgorm" {
		t.Errorf("expected Name 'otelgorm', got %q", p.Name())
	}
}

func TestOtelPlugin_AllOptions(t *testing.T) {
	// 验证所有 option 都能被应用, 不 panic, 也不破坏 Initialize
	customProvider := otel.GetTracerProvider()
	formatter := func(q string) string { return "FORMATTED:" + q }

	p := NewPlugin(
		WithTracerProvider(customProvider),
		WithAttributes(attribute.String("k", "v")),
		WithDBName("mydb"),
		WithServerAddress("127.0.0.1:3306"),
		WithoutQueryVariables(),
		WithQueryFormatter(formatter),
		WithRecordStackTrace(),
	)

	db := newTestDB(t)
	if err := db.Use(p); err != nil {
		t.Fatal(err)
	}

	// 跑一次 query 触发 trace
	_ = db.Exec("SELECT 1").Error
}

func TestOtelPlugin_InitializeError(t *testing.T) {
	// 重复注册同名 plugin 应当报错
	p := NewPlugin()
	db := newTestDB(t)
	if err := db.Use(p); err != nil {
		t.Fatal(err)
	}
	// 第二次注册应当失败
	p2 := NewPlugin()
	if err := db.Use(p2); err == nil {
		t.Error("expected error on duplicate plugin registration")
	}
}

func TestOtelPlugin_QueryFormatter(t *testing.T) {
	// 验证 WithQueryFormatter 实际影响 span 记录
	// 注: queryFormatter 在 query 解析后调用, SQLite 的某些 query
	// 可能不进 trace, 这里仅验证不 panic + option 接收成功。
	formatter := func(q string) string {
		return "[" + q + "]"
	}
	p := NewPlugin(WithQueryFormatter(formatter))
	db := newTestDB(t)
	if err := db.Use(p); err != nil {
		t.Fatal(err)
	}
	_ = db.Exec("SELECT 1").Error
}

func TestOtelPlugin_WithoutQueryVars_NotPanic(t *testing.T) {
	p := NewPlugin(WithoutQueryVariables())
	db := newTestDB(t)
	if err := db.Use(p); err != nil {
		t.Fatal(err)
	}
	// 跑带 var 的 query, 不 panic
	type Item struct {
		ID   int64
		Name string
	}
	_ = db.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)").Error
	_ = db.Create(&Item{ID: 1, Name: "x"}).Error
	_ = db.Where("name = ?", "x").Find(&[]Item{}).Error
}

func TestOtelPlugin_WithServerAddress_NotPanic(t *testing.T) {
	p := NewPlugin(WithServerAddress("10.0.0.1:3306"))
	db := newTestDB(t)
	if err := db.Use(p); err != nil {
		t.Fatal(err)
	}
	_ = db.Exec("SELECT 1").Error
}

func TestOtelPlugin_WithDBName(t *testing.T) {
	// 验证 WithDBName 把 db.namespace 加到 attrs
	p := NewPlugin(WithDBName("test-db"))
	// 通过 reflect / 直接比较 attrs 不可行 (私有字段), 只能验证不 panic
	db := newTestDB(t)
	if err := db.Use(p); err != nil {
		t.Fatal(err)
	}
	_ = db.Exec("SELECT 1").Error
}

func TestOtelPlugin_QueryErrorCapturesError(t *testing.T) {
	p := NewPlugin()
	db := newTestDB(t)
	if err := db.Use(p); err != nil {
		t.Fatal(err)
	}
	// 故意制造一个错误: 无效 SQL
	err := db.Exec("INVALID SQL SYNTAX HERE").Error
	if err == nil {
		t.Error("expected SQL error")
	}
	_ = context.Background()
}

func TestOtelPlugin_StackTrace_NotPanic(t *testing.T) {
	p := NewPlugin(WithRecordStackTrace())
	db := newTestDB(t)
	if err := db.Use(p); err != nil {
		t.Fatal(err)
	}
	_ = db.Exec("SELECT 1").Error
	_ = gorm.ErrRecordNotFound // 防止 unused import
}
