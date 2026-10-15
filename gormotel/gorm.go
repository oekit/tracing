package gormotel

import (
	"context"
	"database/sql"
	"database/sql/driver"
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

// Option 是插件的配置函数类型。
type Option func(p *otelPlugin)

// WithTracerProvider 设置自定义的 OpenTelemetry TracerProvider。
func WithTracerProvider(provider trace.TracerProvider) Option {
	return func(p *otelPlugin) {
		p.provider = provider
	}
}

// WithAttributes 添加额外的 Span 属性。
func WithAttributes(attrs ...attribute.KeyValue) Option {
	return func(p *otelPlugin) {
		p.attrs = append(p.attrs, attrs...)
	}
}

// WithDBName 设置数据库名称属性（db.namespace）。
func WithDBName(name string) Option {
	return func(p *otelPlugin) {
		p.attrs = append(p.attrs, semconv.DBNamespace(name))
	}
}

// WithServerAddress 设置 server.address Span 属性。
// Dialector 接口取不到连接地址，无法自动探测，由此显式配置
// （原 gormx 插件仅支持 MySQL 自动提取，本选项为通用替代）。
func WithServerAddress(addr string) Option {
	return func(p *otelPlugin) {
		p.serverAddress = addr
	}
}

// WithoutQueryVariables 在 Span 中排除 SQL 查询变量，仅记录 SQL 模板。
func WithoutQueryVariables() Option {
	return func(p *otelPlugin) {
		p.excludeQueryVars = true
	}
}

// WithQueryFormatter 设置自定义的查询语句格式化函数。
func WithQueryFormatter(fn func(query string) string) Option {
	return func(p *otelPlugin) {
		p.queryFormatter = fn
	}
}

// WithRecordStackTrace 在 Span 结束时记录堆栈跟踪信息。
func WithRecordStackTrace() Option {
	return func(p *otelPlugin) {
		p.recordStackTraceInSpan = true
	}
}

// NewPlugin 创建 GORM 的 OpenTelemetry 追踪插件实例。
// 经 db.Use(plugin) 显式接入（原 gormx.NewOtelPlugin，v0.2.0 迁入本包）。
func NewPlugin(opts ...Option) gorm.Plugin {
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

		{cb.Row().Before("gorm:row"), p.before("gorm.Row"), "before:row"},
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
		parentCtx := tx.Statement.Context
		ctx, span := p.tracer.Start(tx.Statement.Context, spanName, trace.WithSpanKind(trace.SpanKindClient))
		tx.Statement.Context = contextWrapper{ctx, parentCtx}

		if p.serverAddress != "" {
			span.SetAttributes(semconv.ServerAddressKey.String(p.serverAddress))
		}
	}
}

func (p *otelPlugin) after() gormHookFunc {
	return func(tx *gorm.DB) {
		defer func() {
			if c, ok := tx.Statement.Context.(contextWrapper); ok {
				tx.Statement.Context = c.parent
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
		switch tx.Error {
		case nil,
			gorm.ErrRecordNotFound,
			driver.ErrSkip,
			io.EOF,
			sql.ErrNoRows:
		default:
			span.RecordError(tx.Error)
			span.SetStatus(codes.Error, tx.Error.Error())
		}
	}
}

func dbSystem(tx *gorm.DB) attribute.KeyValue {
	switch tx.Dialector.Name() { //nolint:staticcheck // QF1008: Name 是 Dialector 接口方法，无法继续化简
	case "mysql":
		return semconv.DBSystemMySQL
	case "sqlite":
		return semconv.DBSystemSqlite
	default:
		return attribute.KeyValue{}
	}
}

func dbOperation(query string) string {
	s := cCommentRegex.ReplaceAllString(query, "")
	s = lineCommentRegex.ReplaceAllString(s, "")
	s = sqlPrefixRegex.ReplaceAllString(s, "")
	return strings.ToLower(firstWordRegex.FindString(s))
}
