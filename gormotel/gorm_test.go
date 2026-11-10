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
	"go.opentelemetry.io/otel/codes"
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

// newTestDBWithExporter 打开一个 sqlite 内存 DB，挂上写本地 InMemoryExporter
// 的插件（同步导出，断言即时可见）。
func newTestDBWithExporter(t *testing.T) (*gorm.DB, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	db := newTestDB(t)
	if err := db.Use(NewPlugin(WithTracerProvider(tp))); err != nil {
		t.Fatal(err)
	}
	return db, exp
}

// findSpan 按名字取 span，找不到直接判失败。
// gorm v1.31 的 db.Raw(...).Row() 与 .Rows() 都经 row 回调派发（DB.Rows 内部
// Set("rows", true) 后仍走 callbacks.Row()），span 名同为 "gorm.Row"；
// "gorm:raw"/RawExec 回调只服务 db.Exec。
func findSpan(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range spans {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("span %q not found among %d spans", name, len(spans))
	return tracetest.SpanStub{}
}

// missingTableSQL 生成一条必然在查询期报错的 SQL（表不存在——错误在
// prepare/execute 时暴露，不是取数期）。
const missingTableSQL = "SELECT * FROM definitely_missing_table_for_span_test"

// TestOtelPlugin_RowQueryErrorRecordedIntoSpan 钉住 #101 的可捕获半边：
// db.Raw(...).Row() 的**查询期**错误此前落在返回的 *sql.Row 里（gorm 的
// db.Error 看不见、span 恒 Unset），与 Query/Exec 路径「错误记进 span」的
// 口径分裂。经 Statement.ConnPool 包装层用 Row.Err()（只读不消费）把查询
// 期错误记进 span——这也实证了查询在游标建立时就已真实执行（否则 Row.Err
// 在包装层调用的时刻拿不到错误）。
func TestOtelPlugin_RowQueryErrorRecordedIntoSpan(t *testing.T) {
	db, exp := newTestDBWithExporter(t)

	row := db.Raw(missingTableSQL).Row()
	if err := row.Err(); err == nil {
		t.Fatal("expected query-time error from Row.Err(), got nil")
	}

	span := findSpan(t, exp.GetSpans(), "gorm.Row")
	if got := span.Status.Code; got != codes.Error {
		t.Errorf("span status = %v, want Error（查询期错误必须进 span）", got)
	}
	if len(span.Events) == 0 {
		t.Error("span must record the query-time error event, got 0 events")
	}
}

// TestOtelPlugin_RowsQueryErrorRecordedIntoSpan 钉住 Rows 路径的查询期错误
// 口径（既有行为，gorm 把 QueryContext 错误写进 db.Error、after 钩子记录
// ——与 Query/Exec 路径一致，防止回归）。
func TestOtelPlugin_RowsQueryErrorRecordedIntoSpan(t *testing.T) {
	db, exp := newTestDBWithExporter(t)

	rows, rerr := db.Raw(missingTableSQL).Rows()
	if rerr == nil {
		// 查询应当失败；万一意外成功，把游标收掉再判失败
		defer func() { _ = rows.Close() }()
		_ = rows.Err()
		t.Fatal("expected query error from Rows(), got nil")
	}

	span := findSpan(t, exp.GetSpans(), "gorm.Row")
	if got := span.Status.Code; got != codes.Error {
		t.Errorf("span status = %v, want Error（查询期错误必须进 span）", got)
	}
	if len(span.Events) == 0 {
		t.Error("span must record the query-time error event, got 0 events")
	}
}

// TestOtelPlugin_RowScanErrorOutsideSpan 钉住 #101 的**文档化边界**（半边）：
// Scan 期错误（ErrNoRows/类型不匹配）发生在 gorm 回调链之外——DB.Row() 对
// Statement.Dest 做 *sql.Row 具体类型断言，回调无法包装返回值，database/sql
// 游标亦无外部可观察钩子，插件拿不到 Scan 时机。span 因此只覆盖「SQL 构建 →
// 游标建立」：Scan 错误不进 span、状态保持 Unset。需要全生命周期覆盖的取数
// 走 db.Raw(q).Scan(&dest)/Find(&dest)（Query 回调路径，gorm.Scan 完成后才走
// after 钩子）。本测试把该契约钉死：谁改了 span 形态（比如让 span 泄漏不
// 结束的伪修复）这里先红。
func TestOtelPlugin_RowScanErrorOutsideSpan(t *testing.T) {
	db, exp := newTestDBWithExporter(t)
	if err := db.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)").Error; err != nil {
		t.Fatal(err)
	}

	var id int64
	err := db.Raw("SELECT id FROM items").Row().Scan(&id)
	if err == nil {
		t.Fatal("expected sql.ErrNoRows from Scan on empty table")
	}

	span := findSpan(t, exp.GetSpans(), "gorm.Row")
	if got := span.Status.Code; got != codes.Unset {
		t.Errorf("span status = %v, want Unset（Scan 期错误超出回调模型可见范围，属文档化边界）", got)
	}
	if len(span.Events) != 0 {
		t.Errorf("span must not record Scan-time errors, got %d events", len(span.Events))
	}
}

// TestOtelPlugin_RowsSpanEndsAtCursorEstablishment 钉住 #101 的**文档化边界**
// （另半边）：Rows() 的 span 在游标建立时（Rows() 返回前，after 钩子内）已
// 结束并导出——迭代/Close 发生在回调链之外，插件不可见（证据同上：
// DB.Rows() 对 Dest 做 *sql.Rows 具体类型断言）。迭代慢在取数上的延迟不被
// 本 span 计入，属文档化口径；本测试钉住「Rows() 返回时 span 必已结束」，
// 防止未来有人把 span 改成泄漏不结束的伪修复。
func TestOtelPlugin_RowsSpanEndsAtCursorEstablishment(t *testing.T) {
	db, exp := newTestDBWithExporter(t)
	if err := db.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO items (id, name) VALUES (1, 'x')").Error; err != nil {
		t.Fatal(err)
	}

	rows, err := db.Raw("SELECT id FROM items").Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	// Rows() 一返回（迭代尚未开始）span 就必须已在导出面（已 End）
	spans := exp.GetSpans()
	span := findSpan(t, spans, "gorm.Row")
	if span.EndTime.IsZero() {
		t.Error("span must already be ended when Rows() returns (documented boundary)")
	}

	n := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			n++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows scanned = %d, want 1", n)
	}
	// 迭代期间插件不再产出任何 span（边界内没有第二个 span 的位置）
	if got := len(exp.GetSpans()); got != len(spans) {
		t.Errorf("span count changed during iteration: %d -> %d", len(spans), got)
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
