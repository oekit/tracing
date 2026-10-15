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
// 迁移说明：原 gormx.NewOtelPlugin / gormx.OtelOption（ekit-gorm v0.1.x）已迁入本包，
// 更名 NewPlugin / Option；server.address 由 MySQL Dialector 自动探测
// 改为 WithServerAddress(addr) 显式配置（Dialector 接口取不到连接地址）。
package gormotel
