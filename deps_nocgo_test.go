package tracing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cgoSQLiteDeps 是**禁止出现在本仓 go.mod require 段**里的 CGO sqlite 依赖。
//
//	gorm.io/driver/sqlite      —— 包装 github.com/mattn/go-sqlite3（CGO）
//	github.com/mattn/go-sqlite3 —— CGO 实现本体
//
// 缘由（与 oekit/gormx#36 同根因）：本仓的 sqlite 只是**测试**用途
// （gormotel 包的真实 sqlite 集成测试），却让 CGO 驱动进了本仓的直接依赖段——
// 于是 CGO 依赖链随本仓流向所有消费方的模块图与 go.sum。
//
// **重要边界**：这两个条目**不可能从模块图里彻底消失**——gorm.io/gorm v1.31.2 自身的
// go.mod 就写着 `github.com/mattn/go-sqlite3` 与 `gorm.io/driver/sqlite`（均 indirect）。
// 但它们**不进任何二进制**（gorm 的测试依赖不参与编译），所以静态构建不受影响。
//
// 因此本门禁断言的是**能改的那一半**：本仓的 go.mod 不**直接**依赖 CGO 驱动。
var cgoSQLiteDeps = []string{
	"gorm.io/driver/sqlite",
	"github.com/mattn/go-sqlite3",
}

// TestNoDirectCGOSQLiteDependency 是本仓「sqlite 免 CGO」原则的门禁。
//
// 依赖升级 / dependabot 周更都可能把 CGO 驱动悄悄带回 require 段
// （它是 gorm 的官方 sqlite 驱动，示例代码里最常见）⇒ 这条断言把
// 「不直接依赖 CGO 驱动」变成**可执行**的契约，而不是口头约定。
func TestNoDirectCGOSQLiteDependency(t *testing.T) {
	root := moduleRoot(t)

	files := map[string]string{}
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "go.mod" {
			if b, rerr := os.ReadFile(path); rerr == nil {
				rel, _ := filepath.Rel(root, path)
				files[rel] = string(b)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("扫描 go.mod 失败: %v", walkErr)
	}
	if len(files) == 0 {
		t.Fatal("未找到任何 go.mod，扫描范围可能不对")
	}

	for rel, content := range files {
		for _, dep := range cgoSQLiteDeps {
			for _, line := range requireLines(content) {
				if strings.Contains(line, dep) {
					t.Errorf("%s 的 require 段直接依赖了 CGO sqlite 驱动 %q"+
						"——本仓口径是 sqlite 走纯 Go 驱动（github.com/glebarez/sqlite）", rel, dep)
				}
			}
		}
	}
}

// requireLines 只取 go.mod 的 require 段（单行 require 与块状 require 都取），
// 排除注释与其它段——否则 module / go / toolchain 行会被误判。
func requireLines(content string) []string {
	var out []string
	inBlock := false
	for _, raw := range strings.Split(content, "\n") {
		line := raw
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "require ("):
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case inBlock:
			out = append(out, line)
		case strings.HasPrefix(line, "require "):
			out = append(out, line)
		}
	}
	return out
}

// moduleRoot 从当前目录向上找 module 行等于本仓 module 的 go.mod 所在目录。
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "module ") &&
					strings.TrimSpace(strings.TrimPrefix(line, "module ")) == "github.com/oekit/tracing" {
					return dir
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("未找到本模块的 go.mod")
		}
		dir = parent
	}
}
