// Package gormotel 为 GORM 提供 OpenTelemetry 追踪插件（装饰器）。
//
// 本包是可观测性装饰器：核心 gormx.Open 不做任何 tracing 注入，
// 需要 SQL Span 的服务在此显式接入：
//
//	db, err := gormx.Open(mysql.Open(dsn))
//	if err := db.Use(gormotel.NewPlugin()); err != nil {
//	    return err
//	}
//
// 无 SDK（未调用 tracing.Init）时 Span 为 noop，无实害。
// 本包只依赖 gorm.io/gorm 与 OTel API，不依赖 oekit/gorm 模块。
//
// # Row()/Rows() 的 span 生命周期边界（#101）
//
// db.Raw(...).Row() / .Rows() 产出的 span 只覆盖查询发起期（SQL 构建与
// 游标建立——查询本身在游标建立时就已真实执行）；Scan/迭代期的错误与
// 耗时不在 span 内：
//
//   - Row()：查询期错误（语法错/连接断）经 ConnPool 包装层用 Row.Err()
//     （只读不消费）记进 span，与 Query/Exec 路径口径一致；Scan 阶段的
//     错误（sql.ErrNoRows、类型不匹配等）发生在回调链之外，无法记入
//     span，span 状态对 Scan 失败保持 Unset；
//   - Rows()：查询期错误由 gorm 写进 db.Error、after 钩子照常记录；span
//     覆盖到游标建立，之后的迭代与 Close 的耗时/错误不在 span 内。
//
// Scan/迭代期不可观察的根因是 gorm 回调模型拿不到取数期时机：
// gorm.io/gorm 的 Row()/Rows() 对 Statement.Dest 做 *sql.Row / *sql.Rows
// 具体类型断言，插件无法包装返回值去延迟 span.End 或在 Scan/Close 处
// RecordError（完整覆盖需要 gorm 上游提供 Scan/Close 时机回调）。需要
// 全生命周期覆盖请走 Scan()/Find()/First()（Query 回调全程覆盖，错误照常
// RecordError/SetStatus），或在业务侧对取数期错误自行记录——避免把 Scan
// 失败错误归因为「DB 没问题」。
//
// 迁移说明：原 gormx.NewOtelPlugin / gormx.OtelOption（ekit-gorm v0.1.x）已迁入本包，
// 更名 NewPlugin / GormOption（选项类型沿用 XxxOption 口径——裸 Option 与 gorm 本体的
// gorm.Option 在 dot-import 场景撞名，且违反本仓 AGENTS.md 命名红线）；server.address 由 MySQL Dialector 自动探测
// 改为 WithServerAddress(addr) 显式配置（Dialector 接口取不到连接地址）。
package gormotel
