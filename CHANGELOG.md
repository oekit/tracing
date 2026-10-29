## [v0.3.2] — 2026-10-29

- feat: Config 新增 DisableMetrics（**不导出 OTLP Metrics**，含 Go runtime 指标）——collector 侧**没有 metrics 管线**时的止噪开关

OTel collector 只为**配了管线的 signal** 注册 gRPC service；collector 只有 traces 管线时，它对 MetricsService 回 `Unimplemented: unknown service opentelemetry.proto.collector.metrics.v1.MetricsService`，而本包的周期读取器**每 15s 推一次** ⇒ 表现为「服务一切正常，但日志每 15s 一条导出失败」。

此时这些指标**本来就无处可去**（collector 会丢弃）⇒ 置 `DisableMetrics: true` 即止噪：不建 OTLP metrics 导出器、不启动 Go runtime 指标采集、全局 MeterProvider 保持调用方原样（业务侧 `otel.Meter` 打点照常可用，只是不导出）。待 collector 配好 metrics 管线后把该字段改回 `false` 即恢复。

**零值 false = 保持既有行为**（新增字段不改任何既有调用方的行为面，无 opt-in 陷阱）；grpc 与 http 两条分支同一语义。

## [v0.3.1] — 2026-10-28

- chore(deps): 测试依赖的 sqlite 驱动换成纯 Go 实现，清掉 CGO 直接依赖

主模块 go.mod 把 `gorm.io/driver/sqlite` v1.6.0（CGO 驱动，包装 `mattn/go-sqlite3`）列为**直接依赖**，而本仓**生产代码一行都没用它**——它只出现在 `gormotel/gorm_test.go` 里（真实 sqlite 集成测试）⇒ CGO 依赖链随本仓流向所有消费方的模块图与 go.sum；且本仓测试在 `CGO_ENABLED=0` 下不可用。

换成**纯 Go 驱动** `github.com/glebarez/sqlite`（modernc 系）⇒ ① 本仓测试可在 `CGO_ENABLED=0` 下运行；② 主模块不再直接依赖任何 CGO 驱动。

配套：`modernc.org/sqlite` → v1.61.0（当前最新）、`github.com/glebarez/go-sqlite` → v1.23.0（均为 go 1.26 内可用的最高版），`go` 指令保持 1.26.0 不变（#37）

## [v0.3.0] — 2026-10-20

- Config 新增 Protocol（grpc 缺省 / http）与 Headers（两种协议都生效）——OTLP/HTTP 成为包内一条分支：装配 otlptracehttp/otlpmetrichttp，与 gRPC 分支同样的全局语义（TracerProvider + MeterProvider 含 runtime 指标 + 传播器 + 关闭被替换的旧 provider），换协议退回换一条传输；端点接受完整 signal URL / 基址 / 无 scheme 的 host:port 三种写法；未知 Protocol 报错不静默回落；端点不嵌凭据（凭据走 Headers，不进日志与错误信息）(#23)

## [v0.2.3] — 2026-10-19

- build: 矩阵内依赖地板对齐（align_floors 不动点）：logx v0.6.0 → v0.7.0

## [v0.2.2] — 2026-10-17

- fix(deps): logx 地板 v0.5.0 → v0.6.0（零代码改动）

# Changelog — oekit/tracing

由 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来。所有 notable 变更记录如下。

## [v0.2.1] — 2026-10-17

- fix(deps): 内部依赖地板 logx v0.1.9 → v0.5.0（LevelEnabler 线）；go 维持 1.26.0——OTel v1.4x 全家（otel/contrib/go-redis extra/grpc）最新版均声明 go≥1.25，暂不做生态级回退

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