package gormotel

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

var (
	firstWordRegex   = regexp.MustCompile(`^\w+`)
	cCommentRegex    = regexp.MustCompile(`(?is)/\*.*?\*/`)
	lineCommentRegex = regexp.MustCompile(`(?im)(?:--|#).*?$`)
	sqlPrefixRegex   = regexp.MustCompile(`^[\s;]*`)

	dbRowsAffected = attribute.Key("db.rows_affected")
)

type otelPlugin struct {
	provider               trace.TracerProvider
	tracer                 trace.Tracer
	attrs                  []attribute.KeyValue
	serverAddress          string
	excludeQueryVars       bool
	recordStackTraceInSpan bool
	queryFormatter         func(query string) string
}

// GormOption 是插件的配置函数类型。
//
// 命名遵循本仓 AGENTS.md「函数选项模式 XxxOption（禁止裸 Option，多包 co-import
// 冲突）」：gorm 本体同样导出 gorm.Option（接口形态），插件包再导出裸 Option 时，
// 消费方 dot-import 插件包与 gorm 本体会在编译期撞名（gormotel#132 实测
// Option redeclared）。原 ekit-gorm 的 OtelOption 迁入时曾误改成裸名，本类型
// 即其合规形态。
type GormOption func(p *otelPlugin)

// WithTracerProvider 设置自定义的 OpenTelemetry TracerProvider。
func WithTracerProvider(provider trace.TracerProvider) GormOption {
	return func(p *otelPlugin) {
		p.provider = provider
	}
}

// WithAttributes 添加额外的 Span 属性。
func WithAttributes(attrs ...attribute.KeyValue) GormOption {
	return func(p *otelPlugin) {
		p.attrs = append(p.attrs, attrs...)
	}
}

// WithDBName 设置数据库名称属性（db.namespace）。
func WithDBName(name string) GormOption {
	return func(p *otelPlugin) {
		p.attrs = append(p.attrs, semconv.DBNamespace(name))
	}
}

// WithServerAddress 设置 server.address Span 属性。
// Dialector 接口取不到连接地址，无法自动探测，由此显式配置
// （原 gormx 插件仅支持 MySQL 自动提取，本选项为通用替代）。
func WithServerAddress(addr string) GormOption {
	return func(p *otelPlugin) {
		p.serverAddress = addr
	}
}

// WithoutQueryVariables 在 Span 中排除 SQL 查询变量，仅记录 SQL 模板。
func WithoutQueryVariables() GormOption {
	return func(p *otelPlugin) {
		p.excludeQueryVars = true
	}
}

// WithQueryFormatter 设置自定义的查询语句格式化函数。
func WithQueryFormatter(fn func(query string) string) GormOption {
	return func(p *otelPlugin) {
		p.queryFormatter = fn
	}
}

// WithRecordStackTrace 在 Span 结束时记录堆栈跟踪信息。
func WithRecordStackTrace() GormOption {
	return func(p *otelPlugin) {
		p.recordStackTraceInSpan = true
	}
}

// NewPlugin 创建 GORM 的 OpenTelemetry 追踪插件实例。
// 经 db.Use(plugin) 显式接入（原 gormx.NewOtelPlugin，v0.2.0 迁入本包）。
func NewPlugin(opts ...GormOption) gorm.Plugin {
	p := &otelPlugin{}
	for _, opt := range opts {
		opt(p)
	}
	if p.provider == nil {
		p.provider = otel.GetTracerProvider()
	}
	p.tracer = p.provider.Tracer("github.com/oekit/tracing/gormotel")
	return p
}

// Name 返回插件名称。
func (p otelPlugin) Name() string {
	return "otelgorm"
}

type gormRegister interface {
	Register(name string, fn func(*gorm.DB)) error
}

type gormHookFunc func(tx *gorm.DB)

// Initialize 在 GORM 的各个回调阶段注入 OTel 追踪钩子。
func (p otelPlugin) Initialize(db *gorm.DB) (err error) {
	cb := db.Callback()
	hooks := []struct {
		callback gormRegister
		hook     gormHookFunc
		name     string
	}{
		{cb.Create().Before("gorm:create"), p.before("gorm.Create"), "before:create"},
		{cb.Create().After("gorm:create"), p.after(), "after:create"},

		{cb.Query().Before("gorm:query"), p.before("gorm.Query"), "before:select"},
		{cb.Query().After("gorm:query"), p.after(), "after:select"},

		{cb.Delete().Before("gorm:delete"), p.before("gorm.Delete"), "before:delete"},
		{cb.Delete().After("gorm:delete"), p.after(), "after:delete"},

		{cb.Update().Before("gorm:update"), p.before("gorm.Update"), "before:update"},
		{cb.Update().After("gorm:update"), p.after(), "after:update"},

		// row 回调派发面 = db.Raw(...).Row() 与 .Rows()（gorm v1.31 的 DB.Rows
		// 内部 Set("rows", true) 后仍走 callbacks.Row()）；before 钩子用
		// beforeRow——在 span 之上再包一层 ConnPool，把 Row 路径落在
		// *sql.Row 里的查询期错误捞进 span（#101）。Scan/迭代/Close 时机
		// 仍在 gorm 回调模型内不可观察（见包文档「span 生命周期边界」，#101）。
		{cb.Row().Before("gorm:row"), p.beforeRow("gorm.Row"), "before:row"},
		{cb.Row().After("gorm:row"), p.after(), "after:row"},

		{cb.Raw().Before("gorm:raw"), p.before("gorm.Raw"), "before:raw"},
		{cb.Raw().After("gorm:raw"), p.after(), "after:raw"},
	}

	var firstErr error
	for _, h := range hooks {
		if err := h.callback.Register("otel:"+h.name, h.hook); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("callback register %s failed: %w", h.name, err)
		}
	}
	return firstErr
}

type contextWrapper struct {
	context.Context
	parent context.Context
}

func (p *otelPlugin) before(spanName string) gormHookFunc {
	return func(tx *gorm.DB) {
		p.startSpan(tx, spanName)
	}
}

// startSpan 开启当前回调链的 span 并把 span ctx 挂到 tx.Statement.Context
// （contextWrapper 保留 parent 供 after 钩子还原），返回 span 本身——
// beforeRow 需要持有它把查询期错误记进 span（#101）。
func (p *otelPlugin) startSpan(tx *gorm.DB, spanName string) trace.Span {
	parentCtx := tx.Statement.Context
	ctx, span := p.tracer.Start(parentCtx, spanName, trace.WithSpanKind(trace.SpanKindClient))
	tx.Statement.Context = contextWrapper{ctx, parentCtx}

	if p.serverAddress != "" {
		span.SetAttributes(semconv.ServerAddressKey.String(p.serverAddress))
	}
	return span
}

// beforeRow 与 before 同构，但额外包一层 Statement.ConnPool（#101）：
// gorm 的 RowQuery 走 ConnPool.QueryRowContext——查询在游标建立时已真实
// 执行，查询期错误（语法错/连接断）落在返回的 *sql.Row 里，gorm 自身的
// db.Error 看不见 ⇒ after 钩子拿不到；包装层用 Row.Err()（database/sql
// 为包装方提供的只读访问器，不消费游标）把该错误记进 span。包装只在本条
// 语句的回调窗口内存活，after 钩子还原。
func (p *otelPlugin) beforeRow(spanName string) gormHookFunc {
	return func(tx *gorm.DB) {
		span := p.startSpan(tx, spanName)
		tx.Statement.ConnPool = rowConnPool{ConnPool: tx.Statement.ConnPool, span: span}
	}
}

// rowConnPool 是 row 回调窗口内的 ConnPool 包装（#101）：QueryRowContext 的
// 查询期错误经 Row.Err() 只读捞取并记进 span；其余方法经嵌入接口原样透传
// （Rows() 路径的 QueryContext 错误由 gorm 写进 db.Error，after 钩子既有
// 口径已覆盖，此处不重复记录）。
type rowConnPool struct {
	gorm.ConnPool
	span trace.Span
}

func (w rowConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	row := w.ConnPool.QueryRowContext(ctx, query, args...)
	recordSpanError(w.span, row.Err())
	return row
}

func (p *otelPlugin) after() gormHookFunc {
	return func(tx *gorm.DB) {
		defer func() {
			if c, ok := tx.Statement.Context.(contextWrapper); ok {
				tx.Statement.Context = c.parent
			}
			// 还原 beforeRow 换上的 ConnPool 包装（#101）：包装只服务本条
			// 语句的回调窗口，不留在 Statement 上影响后续链式调用
			if rp, ok := tx.Statement.ConnPool.(rowConnPool); ok {
				tx.Statement.ConnPool = rp.ConnPool
			}
		}()

		span := trace.SpanFromContext(tx.Statement.Context)
		if !span.IsRecording() {
			return
		}
		defer span.End(trace.WithStackTrace(p.recordStackTraceInSpan))

		attrs := make([]attribute.KeyValue, 0, len(p.attrs)+4)
		attrs = append(attrs, p.attrs...)

		if sys := dbSystem(tx); sys.Valid() {
			attrs = append(attrs, sys)
		}

		vars := tx.Statement.Vars
		var query string
		if p.excludeQueryVars {
			query = tx.Statement.SQL.String()
		} else {
			query = tx.Dialector.Explain(tx.Statement.SQL.String(), vars...) //nolint:staticcheck // QF1008: Explain 是 Dialector 接口方法，无法继续化简
		}

		formatQuery := query
		if p.queryFormatter != nil {
			formatQuery = p.queryFormatter(query)
		}
		attrs = append(attrs, semconv.DBQueryText(formatQuery))
		operation := dbOperation(formatQuery)
		attrs = append(attrs, semconv.DBOperationName(operation))
		if tx.Statement.Table != "" {
			attrs = append(attrs, semconv.DBCollectionName(tx.Statement.Table))
			span.SetName(operation + " " + tx.Statement.Table)
		}
		if tx.Statement.RowsAffected != -1 {
			attrs = append(attrs, dbRowsAffected.Int64(tx.Statement.RowsAffected))
		}

		span.SetAttributes(attrs...)
		recordSpanError(span, tx.Error)
	}
}

// recordSpanError 把错误记进 span（#101 统一口径）：业务语义性错误（未命中/
// 驱动跳过/流结束）不算失败，保持 Unset——与 Query/Exec 路径既有口径一致；
// Row 路径的查询期错误（rowConnPool 捞取）与回调链尾的 tx.Error 共用此判定，
// 避免两处口径漂移。
//
// 哨兵判定必须走 errors.Is（#111）：switch == 是相等比较，经
// fmt.Errorf("%w") / 各类 Wrap 包装后的 gorm.ErrRecordNotFound（业务「未命中」
// 是最高频的被包装哨兵）此前不匹配，span 被 RecordError + SetStatus(Error)，
// APM 上未命中查询全量报红，与本函数「业务语义性错误不算失败」的口径相反。
func recordSpanError(span trace.Span, err error) {
	if err == nil ||
		errors.Is(err, gorm.ErrRecordNotFound) ||
		errors.Is(err, driver.ErrSkip) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, sql.ErrNoRows) {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// dbSystem 把 GORM 方言名映射为 OTel 约定的 db.system 值（#78）。
//
// 此前只认 mysql/sqlite，postgres 等方言一律返回零值、被 after 钩子
// 的 Valid() 静默跳过——span 照常产出，唯独缺 db.system 维度，且无
// 任何日志/报错可循（「面板少一块」与「没人访问」无法区分）。
//
// 映射按 semconv v1.26.0 的 DBSystem 枚举对照 GORM 方言名（gorm.io/driver
// 各 Dialector 的 Name() 返回值）；未识别的方言回落 DBSystemOtherSQL
// ——至少留下「这是一个 SQL 方言」的事实，避免维度整片落空；
// 仅空方言名（极少见）保持缺省。
func dbSystem(tx *gorm.DB) attribute.KeyValue {
	switch strings.ToLower(tx.Dialector.Name()) { //nolint:staticcheck // QF1008: Name 是 Dialector 接口方法，无法继续化简
	case "mysql":
		return semconv.DBSystemMySQL
	case "mariadb":
		return semconv.DBSystemMariaDB
	case "postgres":
		return semconv.DBSystemPostgreSQL
	case "sqlite":
		return semconv.DBSystemSqlite
	case "sqlserver":
		return semconv.DBSystemMSSQL
	case "oracle":
		return semconv.DBSystemOracle
	case "clickhouse":
		return semconv.DBSystemClickhouse
	case "":
		return attribute.KeyValue{}
	default:
		return semconv.DBSystemOtherSQL
	}
}

func dbOperation(query string) string {
	s := cCommentRegex.ReplaceAllString(query, "")
	s = lineCommentRegex.ReplaceAllString(s, "")
	s = sqlPrefixRegex.ReplaceAllString(s, "")
	return strings.ToLower(firstWordRegex.FindString(s))
}
