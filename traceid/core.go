package traceid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/oekit/logx"
	"go.opentelemetry.io/otel/trace"
)

const (
	// Key 是 trace_id 在 Hertz RequestContext 与标准 context 中存储使用的键名。
	Key = "trace_id"

	// HeaderKey 是 trace_id 透传使用的请求/响应头名称。
	HeaderKey = "X-Trace-Id"

	// IDLength 是 trace_id 的标准长度（取自 UUID 前缀，碰撞概率极低）。
	IDLength = 12
)

// ctxKey 是标准 context.Context 中存储 trace_id 的键类型。
// 使用独立类型避免与其他包的 context value 冲突。
type ctxKey struct{}

// FromContext 从标准 context.Context 中提取 trace_id。
// 返回空字符串表示上下文中没有 trace_id（中间件未注册或尚未注入）。
func FromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// ContextWith 将 trace_id 注入到标准 context.Context 中。
// 适用于 service 层、NATS publisher 等不持有 RequestContext 的场景需要向下传递 trace_id 时。
func ContextWith(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, traceID)
}

// NewID 生成一条新的 trace_id（#102）：IDLength 位十六进制随机串
// （crypto/rand 驱动，与既有 X-Trace-Id 的「UUID 前缀」形态一致）。
// 本包是 trace_id 的唯一真相源，生成也归此包——hmw 标准栈与无框架入口
// （定时任务/MQ 消费等）都应经本函数取新 ID，避免第二生成点。OTel span
// 已在场时优先用 FromSpanContext 桥接 APM trace：日志 trace_id 与 APM
// trace 同源，双 ID 不断轨。
func NewID() string {
	raw := make([]byte, (IDLength+1)/2) // IDLength 为奇数时向上取整，截尾保长度恒定
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand.Read 自 Go 1.24 起保证填满且不返回错误（熵源不可用直接
		// panic 在其内部）；此分支仅为显式契约，理论不可达——trace_id 生成
		// 失败静默回退（返回空串/零串）会造成链路 ID 撞车，必须显式上抛
		panic(fmt.Sprintf("traceid: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(raw)[:IDLength]
}

// FromSpanContext 把 OTel SpanContext 桥接为本包语义的 trace_id（#102）：
// 取 APM（OTel）侧的 32 位十六进制 TraceID。调用方经 ContextWith 注入后，
// logx 输出的 trace_id 与 APM trace 同源——「日志里的 trace_id 和 APM trace
// 对不上」的双 ID 断轨就此闭合。SpanContext 无效（无 trace）时返回空串，
// 与 FromContext 的空值口径一致。
//
// 反方向（X-Trace-Id → SpanContext）不提供：IDLength 位短 ID 不足 16 字节，
// 反推不出合法 OTel TraceID；跨进程恢复链路由 HTTP traceparent 头与 OTel
// 传播器承担，本包不做猜测式重建。
func FromSpanContext(sc trace.SpanContext) string {
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

// init 向 logx 注册 trace 提取器。
// 这样 logx.Ctx* 方法能从 context 自动提取 trace_id，
// 而无需 logx 反向依赖本包或 middleware 包，打破潜在的循环依赖。
func init() {
	logx.SetTraceExtractor(FromContext)
}
