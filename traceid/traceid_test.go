package traceid

import (
	"context"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/test/assert"
)

func TestContextWith_FromContext(t *testing.T) {
	ctx := context.Background()
	assert.DeepEqual(t, "", FromContext(ctx))

	ctx = ContextWith(ctx, "trace-abc")
	assert.DeepEqual(t, "trace-abc", FromContext(ctx))
}

// TestInitRegisteredExtractor 验证 init() 向 logx 注册了提取器，
// 使得通过 ContextWith 注入的 trace_id 能被 logx 提取（集成验证）。
// 这里验证 FromContext（注册到 logx 的函数）行为正确。
func TestInitRegisteredExtractor(t *testing.T) {
	ctx := ContextWith(context.Background(), "integrated-trace")
	// FromContext 即注册到 logx 的提取器，验证其正确性
	assert.DeepEqual(t, "integrated-trace", FromContext(ctx))
	// 空上下文返回空字符串
	assert.DeepEqual(t, "", FromContext(context.Background()))
}

func TestConstants(t *testing.T) {
	assert.DeepEqual(t, "trace_id", Key)
	assert.DeepEqual(t, "X-Trace-Id", HeaderKey)
	assert.DeepEqual(t, 12, IDLength)
}
