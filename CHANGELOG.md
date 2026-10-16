# Changelog — oekit/tracing

由 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来。所有 notable 变更记录如下。

## [v0.2.0] — 2026-10-15

可观测性统一轮：装饰器显式接入口径。

### Added
- `gormotel` 包：GORM OTel 追踪插件（原 ekit-gorm `gormx.NewOtelPlugin` 迁入更名 `NewPlugin`；
  `OtelOption` → `Option`；新增 `WithServerAddress(addr)` 显式配置 server.address）

### Breaking
- 删除逐字节重复包 `instrumentation/redis`（≡ `redisotel`）与 `instrumentation/httpotel`（≡ `httpotel`）
- `httpotel` 包名 `httpxotel` → `httpotel`（目录名=包名对齐）
- `gormotel` 删除 `WithoutServerAddress` 选项（server.address 改为 `WithServerAddress` 显式配置）

### 迁移
- `instrumentation/redis` → `redisotel`；`instrumentation/httpotel` → `httpotel`
- gormx 用户：`gormx.WithoutTracing()` 调用直接删除（核心默认已不注入）；
  需要 span 改 `db.Use(gormotel.NewPlugin())`

## [v0.1.0] — 2026-10-15

自 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来的初始公开版本，完成拆分稳定化：模块路径统一为 `github.com/oekit/tracing`，多轮拆分/合并收敛至当前模块边界，组织基建（共享 CI workflow、golangci v2、Dependabot）就位。