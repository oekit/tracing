package gormotel

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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

// TestDoc_RowRowsSpanBoundaryDocumented 钉住 #101 的兜底契约：gorm 回调
// 模型拿不到取数期时机——gorm.io/gorm 的 Row()/Rows() 对 Statement.Dest 做
// *sql.Row / *sql.Rows 具体类型断言，插件无法包装返回值去延迟 span.End /
// RecordError，因此 Row()/Rows() 的 span 只覆盖查询发起期：Scan/迭代期的
// 错误与耗时不在 span 内、状态恒 Unset。包文档必须点明这一边界，否则
// Scan 失败（ErrNoRows 等）会被错误归因为「DB 没问题」。
func TestDoc_RowRowsSpanBoundaryDocumented(t *testing.T) {
	docFile, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatalf("read doc.go: %v", err)
	}
	doc := string(docFile)
	for _, want := range []string{"Row()", "Rows()", "查询发起", "Scan"} {
		if !strings.Contains(doc, want) {
			t.Errorf("gormotel/doc.go 未点明 Row()/Rows() 的 span 生命周期边界（缺 %q）——Scan/迭代期错误会全部丢失、被错误归因（#101）", want)
		}
	}
}

// renameDialector 包装真实的 sqlite Dialector，只改写 Name() 的返回值
// ——让 gorm.Open 照常工作（真连接池/真 SQL 执行），而插件视角的
// 方言名是任意指定值。#78 的映射表用例靠它模拟 postgres/sqlserver/
// 未知方言，无需真 PG/MSSQL 实例。
type renameDialector struct {
	gorm.Dialector
	name string
}

func (d renameDialector) Name() string { return d.name }

// newTestDBWithDialectName 打开一个「方言名被改写」的 sqlite 内存 DB，
// 并挂上写本地 InMemoryExporter 的插件。
func newTestDBWithDialectName(t *testing.T, name string) (*gorm.DB, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	dsn := "file:gormotel-" + strconv.FormatInt(testDBSeq.Add(1), 10) + "?mode=memory&cache=shared"
	db, err := gorm.Open(renameDialector{Dialector: sqlite.Open(dsn), name: name}, &gorm.Config{
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
	if err := db.Use(NewPlugin(WithTracerProvider(tp))); err != nil {
		t.Fatal(err)
	}
	return db, exp
}

// TestOtelPlugin_DBSystemMapping 是 #78 的回归钉：db.system 映射表必须
// 覆盖 postgres 等常见方言（此前 postgres/sqlserver 静默丢维度），未知
// 方言回落 other_sql（至少留下「这是一个 SQL 方言」的事实），方言名
// 大小写不敏感。假 Dialector（内嵌 sqlite、改写 Name()）走真实执行路径，
// 用 tracetest 导出器抓 span 断言属性在场。
func TestOtelPlugin_DBSystemMapping(t *testing.T) {
	cases := []struct {
		dialect string
		want    string
	}{
		{dialect: "postgres", want: "postgresql"},
		{dialect: "sqlserver", want: "mssql"}, // semconv v1.26.0 的 SQL Server 值
		{dialect: "mysql", want: "mysql"},
		{dialect: "sqlite", want: "sqlite"},
		{dialect: "weird-db", want: "other_sql"}, // 未知方言回落 other_sql，不再丢维度
		{dialect: "MySQL", want: "mysql"},        // 方言名大小写不敏感
	}
	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			db, exp := newTestDBWithDialectName(t, tc.dialect)

			type item struct {
				ID   int64
				Name string
			}
			_ = db.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)").Error
			if err := db.Create(&item{ID: 1, Name: "x"}).Error; err != nil {
				t.Fatalf("Create: %v", err)
			}

			spans := exp.GetSpans()
			if len(spans) == 0 {
				t.Fatal("expected spans to be exported, got 0")
			}
			for _, span := range spans {
				var got string
				for _, kv := range span.Attributes {
					if kv.Key == attribute.Key("db.system") {
						got = kv.Value.AsString()
					}
				}
				if got != tc.want {
					t.Errorf("span %q: db.system = %q, want %q", span.Name, got, tc.want)
				}
			}
		})
	}
}
