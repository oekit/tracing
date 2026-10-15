package traceid

import (
	"context"

	"github.com/oekit/logx"
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

// init 向 logx 注册 trace 提取器。
// 这样 logx.Ctx* 方法能从 context 自动提取 trace_id，
// 而无需 logx 反向依赖本包或 middleware 包，打破潜在的循环依赖。
func init() {
	logx.SetTraceExtractor(FromContext)
}
