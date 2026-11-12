package gormotel_test

import (
	"testing"

	// 双 dot-import 是本文件的存在意义（gormotel#132 的原始复现形态）：
	// 两个包的导出名都进文件块，gorm 本体导出 `type Option interface{...}`
	// （gorm.go），本包若再导出裸 `Option`，编译期直接 Option redeclared——
	// 消费方 dot-import 插件包与 gorm 本体的常见写法当场编不过。
	// 本文件常驻 ⇒ 裸 Option 一旦回流，测试编译即红（AGENTS.md:40 命名红线）。
	. "github.com/oekit/tracing/gormotel"
	. "gorm.io/gorm"
)

// assertPlugin / assertGormOption 以**参数类型**承载断言（而非 var 声明——
// staticcheck QF1011 会建议省略 var 的类型，省略后断言即失效）：
//   - 第一个实参要求 NewPlugin 的返回值可赋给 gorm.Plugin（gorm 的导出面）；
//   - 后两个实参要求选项构造器返回 GormOption（本包导出面，裸 Option 回流
//     时 NewPlugin 签名与构造器签名两侧都会撞）。
func assertPlugin(p Plugin)         {}
func assertGormOption(o GormOption) {}

// TestOptionNaming_DotImportCoexistsWithGorm 钉住 #132：
//   - gormotel 导出的选项类型必须是 GormOption（XxxOption 口径），不得导出裸 Option；
//   - dot-import 本包与 gorm 本体必须能在同一文件共存（编译期即断言）。
func TestOptionNaming_DotImportCoexistsWithGorm(t *testing.T) {
	// Plugin 来自 gorm 的 dot-import；NewPlugin / GormOption 来自本包的
	// dot-import——两者同文件被真实引用 ⇒ 共存性在编译期被检查。
	assertPlugin(NewPlugin())
	assertGormOption(WithoutQueryVariables())
	assertGormOption(WithDBName("db"))
}
