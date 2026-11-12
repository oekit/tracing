// 本文件属独立测试模块 github.com/oekit/tracing/tests（#122）：hertz 只允许
// 出现在这个模块的 go.mod 里——根模块 doc.go 承诺「纯 Kitex 服务引用本包
// 不会引入 Hertz」，而全仓唯一引用点就是这里的 assert 包；测试挪出根模块后
// 依赖链不再流向消费方。CI 的嵌套模块发现（build + test -race + tidy 门禁）
// 会自动覆盖本模块。
package traceid_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/test/assert"
	traceid "github.com/oekit/tracing/traceid"
	"go.opentelemetry.io/otel/trace"
)

// TestKeyIsNotStandardContextKey 钉住 #115 的行为边界：Key 是 hertz
// RequestContext 的字符串键，不是标准 context 的取回键——按 Key
// context.WithValue 可编译，但 FromContext 永远取不到（标准 context 用
// 未导出的类型化键 ctxKey）。行为面钉死，防止未来有人误把 Key 接进
// context 语义「修兼容」。
func TestKeyIsNotStandardContextKey(t *testing.T) {
	//nolint:staticcheck // SA1029：本行**故意**用内置 string 当 context 键——钉住「按 Key 存取必然断链」的错误用法（#115 文档契约）
	ctx := context.WithValue(context.Background(), traceid.Key, "via-key")
	assert.DeepEqual(t, "", traceid.FromContext(ctx))

	ctx = traceid.ContextWith(ctx, "via-ctx")
	assert.DeepEqual(t, "via-ctx", traceid.FromContext(ctx))
}

// TestDoc_KeyNotContextValueKey 钉住 #115 的文档契约：此前 Key 的 godoc 与
// 包文档都声称 Key 是「标准 context 中存储 trace_id 的键名」，与实现
// （ctxKey{} 类型化键）相反——按文档写 context.WithValue(ctx, traceid.Key,
// id) 可编译但全链路静默断链、零报错，唯一防线是文档显式警告。本测试钉住
// ①两处文档都带警告、②旧的错误声称不再出现。
func TestDoc_KeyNotContextValueKey(t *testing.T) {
	coreRaw, err := os.ReadFile("../traceid/core.go")
	if err != nil {
		t.Fatalf("read core.go: %v", err)
	}
	docRaw, err := os.ReadFile("../traceid/doc.go")
	if err != nil {
		t.Fatalf("read doc.go: %v", err)
	}
	coreDoc, pkgDoc := string(coreRaw), string(docRaw)

	for _, want := range []string{"RequestContext", "context.WithValue", "ctxKey"} {
		if !strings.Contains(coreDoc, want) {
			t.Errorf("traceid/core.go 的 Key godoc 缺 %q——必须写明 Key 仅用于 hertz RequestContext、标准 context 走 ContextWith/FromContext 的类型化键（#115）", want)
		}
	}
	for _, want := range []string{"RequestContext", "context.WithValue"} {
		if !strings.Contains(pkgDoc, want) {
			t.Errorf("traceid/doc.go 的包文档缺 %q——Key 的用途口径必须与 core.go 一致（#115）", want)
		}
	}
	for _, stale := range []string{
		"在 Hertz RequestContext 与标准 context 中存储使用的键名", // core.go 旧声称
		"标准库 context 与 RequestContext 中存储",            // doc.go 旧声称
	} {
		if strings.Contains(coreDoc, stale) || strings.Contains(pkgDoc, stale) {
			t.Errorf("traceid 文档仍声称 Key 同时用于标准 context（%q）——与 ctxKey{} 实现相反，按文档写全链路静默断链（#115）", stale)
		}
	}
}

func TestContextWith_FromContext(t *testing.T) {
	ctx := context.Background()
	assert.DeepEqual(t, "", traceid.FromContext(ctx))

	ctx = traceid.ContextWith(ctx, "trace-abc")
	assert.DeepEqual(t, "trace-abc", traceid.FromContext(ctx))
}

// TestInitRegisteredExtractor 验证 init() 向 logx 注册了提取器，
// 使得通过 ContextWith 注入的 trace_id 能被 logx 提取（集成验证）。
// 这里验证 FromContext（注册到 logx 的函数）行为正确。
func TestInitRegisteredExtractor(t *testing.T) {
	ctx := traceid.ContextWith(context.Background(), "integrated-trace")
	// FromContext 即注册到 logx 的提取器，验证其正确性
	assert.DeepEqual(t, "integrated-trace", traceid.FromContext(ctx))
	// 空上下文返回空字符串
	assert.DeepEqual(t, "", traceid.FromContext(context.Background()))
}

func TestConstants(t *testing.T) {
	assert.DeepEqual(t, "trace_id", traceid.Key)
	assert.DeepEqual(t, "X-Trace-Id", traceid.HeaderKey)
	assert.DeepEqual(t, 12, traceid.IDLength)
}

// TestNewID 钉住 #102 的生成原语契约：长度恒为 IDLength、字符集为小写
// 十六进制（与既有 X-Trace-Id 的「UUID 前缀」形态一致）、连续生成不重复。
func TestNewID(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := traceid.NewID()
		if len(id) != traceid.IDLength {
			t.Fatalf("NewID() length = %d, want %d", len(id), traceid.IDLength)
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
	if got := traceid.FromSpanContext(zero); got != "" {
		t.Errorf("FromSpanContext(zero) = %q, want empty", got)
	}

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x0a, 0xf7, 0x65, 0x19, 0x16, 0xcd, 0x43, 0xdd, 0x84, 0x48, 0xeb, 0x21, 0x1c, 0x80, 0x31, 0x9c},
		SpanID:     trace.SpanID{0xb7, 0xad, 0x6b, 0x71, 0x69, 0x20, 0x33, 0x31},
		TraceFlags: trace.FlagsSampled,
	})
	want := sc.TraceID().String()
	if got := traceid.FromSpanContext(sc); got != want {
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
	ctx := traceid.ContextWith(context.Background(), traceid.FromSpanContext(sc))
	assert.DeepEqual(t, sc.TraceID().String(), traceid.FromContext(ctx))
}
