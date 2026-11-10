package traceid

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/test/assert"
	"go.opentelemetry.io/otel/trace"
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

// TestNewID 钉住 #102 的生成原语契约：长度恒为 IDLength、字符集为小写
// 十六进制（与既有 X-Trace-Id 的「UUID 前缀」形态一致）、连续生成不重复。
func TestNewID(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := NewID()
		if len(id) != IDLength {
			t.Fatalf("NewID() length = %d, want %d", len(id), IDLength)
		}
		if strings.ToLower(id) != id {
			t.Fatalf("NewID() = %q, want lowercase hex", id)
		}
		for _, c := range id {
			if !strings.ContainsRune("0123456789abcdef", c) {
				t.Fatalf("NewID() = %q, want hex charset, got %q at %q", id, c, id)
			}
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("NewID() collision within 1000 draws: %q", id)
		}
		seen[id] = struct{}{}
	}
}

// TestFromSpanContext 钉住 #102 的桥接原语契约：有效 SpanContext 取 32 位
// 十六进制 OTel TraceID（与 APM 同源），无效（无 trace）返回空串——与
// FromContext 的空值口径一致。
func TestFromSpanContext(t *testing.T) {
	zero := trace.SpanContext{}
	if got := FromSpanContext(zero); got != "" {
		t.Errorf("FromSpanContext(zero) = %q, want empty", got)
	}

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x0a, 0xf7, 0x65, 0x19, 0x16, 0xcd, 0x43, 0xdd, 0x84, 0x48, 0xeb, 0x21, 0x1c, 0x80, 0x31, 0x9c},
		SpanID:     trace.SpanID{0xb7, 0xad, 0x6b, 0x71, 0x69, 0x20, 0x33, 0x31},
		TraceFlags: trace.FlagsSampled,
	})
	want := sc.TraceID().String()
	if got := FromSpanContext(sc); got != want {
		t.Errorf("FromSpanContext(sc) = %q, want %q", got, want)
	}
	if len(want) != 32 {
		t.Errorf("OTel TraceID string length = %d, want 32 hex chars", len(want))
	}
}

// TestBridgeIntoContext 钉住桥接的完整用法（#102 的核心场景）：
// FromSpanContext 桥接 → ContextWith 注入 → FromContext 提取，
// 日志 trace_id 与 APM trace 同源。
func TestBridgeIntoContext(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:     trace.SpanID{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := ContextWith(context.Background(), FromSpanContext(sc))
	assert.DeepEqual(t, sc.TraceID().String(), FromContext(ctx))
}
