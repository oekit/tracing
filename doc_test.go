package tracing

import (
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/oekit/tracing/traceid"
)

// TestDoc_TraceIDCapabilityClaimsBackedByImplementation 钉住 #102：包文档
// 宣称的能力必须与实现一致。v0.4.0 起 traceid 落地最小诚实能力——NewID
// （生成，唯一生成点）与 FromSpanContext（OTel → trace_id 桥接，日志与
// APM 双 ID 关联点）；此前文档三处宣称、零处实现（#102），一度改为文档
// 对齐实现，本版本按 issue 的「坐实唯一真相源」倾向补齐实现，文档契约
// 随之升级：宣称生成/桥接的文档必须点名实现符号本体，不得只挂归属。
// 反方向（X-Trace-Id → SpanContext）不可实现（IDLength 位短 ID 反推不出
// 合法 OTel TraceID），文档不得宣称。
func TestDoc_TraceIDCapabilityClaimsBackedByImplementation(t *testing.T) {
	// 实现面：宣称的两个原语必须真实存在且签名一致（编译期断言）
	var _ = struct {
		newID           func() string
		fromSpanContext func(trace.SpanContext) string
	}{
		newID:           traceid.NewID,
		fromSpanContext: traceid.FromSpanContext,
	}

	for _, path := range []string{"doc.go", "traceid/doc.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(src), "NewID") || !strings.Contains(string(src), "FromSpanContext") {
			t.Errorf("%s 宣称 traceid 生成/桥接能力但未点名实现符号 NewID/FromSpanContext——下游按文档找能力找不到（#102）", path)
		}
	}
}
