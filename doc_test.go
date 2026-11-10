package tracing

import (
	"os"
	"strings"
	"testing"
)

// TestDoc_TraceIDCapabilityClaimsAttributed 钉住 #102：包文档宣称的能力
// 必须与实现一致。traceid 实现面只有 Key/HeaderKey/IDLength 常量与
// ContextWith/FromContext 存取（外加 init() 注册 logx 提取器）——「生成」
// 与「桥接」的正身在 oekit/middleware 的 hmw 标准栈。此前 traceid/doc.go
// 与 tracing/doc.go 两处宣称 traceid 提供生成/桥接，下游按文档找能力找不到，
// 且 traceid/doc.go 第 1 行与第 16 行自相矛盾（#51 修掉 Middleware() 空
// 宣称后，这是同一文档的另一对空宣称）。
//
// 契约：文档凡宣称「生成」「桥接」，必须同行标注其归属（middleware/hmw），
// 否则判为失实宣称。
func TestDoc_TraceIDCapabilityClaimsAttributed(t *testing.T) {
	for _, path := range []string{"doc.go", "traceid/doc.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, "生成") && !strings.Contains(line, "桥接") {
				continue
			}
			if !strings.Contains(line, "middleware") && !strings.Contains(line, "hmw") {
				t.Errorf("%s:%d 宣称生成/桥接能力但未标注归属（正身在 oekit/middleware 的 hmw 栈，本包没有该实现）：%s",
					path, i+1, strings.TrimSpace(line))
			}
		}
	}
}
