## [v0.4.1] — 2026-11-12

- fix(init): parseOTLPHTTPEndpoint 无 scheme 分支显式拒绝带路径段，不再静默断供（#112）——"collector:4318/api/otel"（漏写 scheme 但带基址路径）此前被整串当 host:port 收下、basePath 恒空，最终导出打 https://collector:4318/v1/traces 而 /api/otel 段无声丢失：Init 成功、导出 404 循环，与 validateGRPCEndpoint「不合规输入报错，不静默断供」的既定纪律相悖；修复为无 scheme 且含 "/"（含尾斜杠）直接报错并提示补 scheme（"https://host:4318/api/otel"），错误不回显端点原值（无 scheme 形态也可能内嵌凭据，与内嵌凭据拒绝同一口径），补 parse 回归测试（基址路径/无端口带路径/尾斜杠三形态）
- fix(gormotel): recordSpanError 哨兵判定改 errors.Is，包装后的 ErrRecordNotFound 不再被误标 span 失败（#111）——switch == 是相等比较，经 fmt.Errorf("%w") 包装的 gorm.ErrRecordNotFound（业务「未命中」是最高频的被包装哨兵）此前不匹配，span 被 RecordError + SetStatus(codes.Error)，APM 上未命中查询全量报红，与「业务语义性错误不算失败」的口径相反；修复为 errors.Is 链逐层拆 %w（gorm.ErrRecordNotFound/driver.ErrSkip/io.EOF/sql.ErrNoRows 四哨兵，包装层级任意深命中），补 wrapped 哨兵回归测试（多级包装 × 4 哨兵）+ 真错误正向对照 + First 未命中全链路契约钉
- refactor(init): gRPC 与 HTTP 两条导出分支的 MeterProvider 装配尾段收敛到 assembleMeterProvider（#110）——reader+mp+runtime.Start+失败回滚 12 行在 init.go/otlphttp.go 两处逐字复制（连 DisableMetrics 早退回滚与「runtime.Start 必须显式指定 provider」关键注释都是双份维护），抽为单点后两分支各剩一行调用；另抽 applyCommonGrpcOpts 泛型帮助函数收编 grpc 分支 trace/metric 两组逐字复制的连接选项构建（Go 1.26 泛型，Option 构造器由调用方注入），行为无变化
- build: 依赖地板抬到 logx v0.10.0（地板跑步机：旧地板会焊进已发布产物且 tag 不可重推）

关联问题单：tracing#110、tracing#111、tracing#112。

## [v0.4.0] — 2026-11-11

- fix(init): gRPC 分支校验 Endpoint 形态，URL 形态端点不再静默断供（#98）——cfg.Endpoint 此前原样交给 otlptracegrpc/otlpmetricgrpc：URL 形态（http://host:port、https://host:port、带路径）Init 成功返回 provider，但 grpc 拨号目标解析不出可用地址、一次都不连，span/metric 在后台导出重试里无声耗尽（「服务正常但 APM 全黑」）；修复为镜像 http 分支的校验哲学——Endpoint 含 "://" 或 "/" 直接报错，错误点名 host:port 期望形态并指向 URL 端点改设 Protocol:"http"，不回显端点原值（URL 可能内嵌凭据）

关联问题单：oekit/tracing#98。

- fix(init): parseOTLPHTTPEndpoint 显式拒绝 URL 的 query/fragment，不再静默丢弃（#99）——query token 型鉴权（?api_key= / ?token=）此前解析后凭空消失：Init 成功、导出请求到达后端却不带 token，后端 401/403、OTLP 重试耗尽数据全丢，而配置里的 token「明明配了」；修复为与内嵌凭据同一纪律——报错并指向 Config.Headers（query 里的密钥同样会进访问日志/代理日志），fragment 本就不会发给服务端、出现即配置错误，错误不回显端点内容

关联问题单：oekit/tracing#99。

- fix(init): 并发 Init 加进程级互斥，全局 TracerProvider 不再可能指向已 Shutdown 僵尸（#100）——「Set* 装全局→Swap/关前任」是两步非原子序列且全程无锁，两个 goroutine 并发 Init 时 Set 顺序与 Swap 顺序相反（A.Set → B.Set → B.Swap → A.Swap）会让 A 的 Swap 拿到 pB 并将其 Shutdown，而全局位最后由 B.Set 写为 pB：之后所有业务 span 不再记录（IsRecording=false）且零报错；修复为包级 mutex 把「创建→Set*→Swap/关前任」整个临界区圈住，补并发 Init 回归测试（N 协程并发 Init 后断言全局 tracer 新 span 仍 IsRecording）

关联问题单：oekit/tracing#100。

- fix(gormotel): Row()/Rows() 的 span 口径补齐 + 生命周期边界文档化（#101）——Row()/Rows() 的 span 在游标建立后即结束，Scan/迭代期错误与耗时全部丢失（Scan 的 sql.ErrNoRows 不进 span，错误被归因为「DB 没问题」）；本轮两半处置：①可捕获半边已修——Row 路径的**查询期**错误（语法错/连接断）此前落在返回的 *sql.Row 里（gorm 的 db.Error 看不见、span 恒 Unset），现经 Statement.ConnPool 包装层用 Row.Err()（database/sql 为包装方提供的只读访问器，不消费游标）记进 span，与 Query/Exec 路径口径合一（recordSpanError 统一判定，业务语义错误仍保持 Unset），实证「查询在游标建立时已真实执行」；②Scan/迭代/Close 期在 gorm 回调模型内不可观察（Row()/Rows() 对 Statement.Dest 做 *sql.Row / *sql.Rows 具体类型断言，插件无法包装返回值），包文档「span 生命周期边界」一节点明并给出覆盖取数期的替代路径（Scan()/Find()/First() 全程覆盖），契约测试钉住文档不再失实 + 行为测试钉住边界

关联问题单：oekit/tracing#101。

- **feat(traceid)**: 「生成/桥接」能力落地为真实 API，文档三处宣称由「对齐实现」升级为「坐实能力」（#102）——`NewID()`（crypto/rand 驱动的 IDLength 位十六进制随机串，与既有 X-Trace-Id 的「UUID 前缀」形态一致；生成点唯一，hmw 标准栈与无框架入口都经此取新 ID，不留第二生成点）与 `FromSpanContext(sc)`（OTel SpanContext → trace_id 桥接：经 ContextWith 注入后日志 trace_id 与 APM trace 同源，双 ID 断轨闭合）；反方向（X-Trace-Id → SpanContext）不提供：12 位短 ID 不足 16 字节反推不出合法 OTel TraceID，跨进程恢复走 traceparent 由 OTel 传播器承担。新增公开 API ⇒ minor 升 v0.4.0

关联问题单：oekit/tracing#102。

现代语言特性贴合处：`for range int`（Go 1.22+，新增行为测试的确定性循环）。

## [v0.3.10] — 2026-11-10

- - build: 依赖地板对齐（floor sync，无代码变更）

## [v0.3.9] — 2026-11-09

- docs(httpotel): Transport 契约改述——追踪唯一入口 WrapTransport（#79）

关联问题单：tracing#79。

## [v0.3.8] — 2026-11-09

- fix(gormotel): db.system 映射补 postgres/sqlserver/mariadb/oracle/clickhouse，未知方言落 other_sql（#78）

关联问题单：tracing#78。

## [v0.3.7] — 2026-11-08

- fix(init): re-Init「metrics on→off→on」不再永久孤儿化第一个 on 的 MeterProvider（#68）——installed 只沿单链追踪最近一任 impl，off 的 Init 产出不了 meterProvider（#52 语义下旧 meter 面继续持有全局位导出），第二个 on 替换全局后第一个 on 的 meterProvider 彻底无人引用：周期读取器（15s）与 runtime collector 每 15s 向旧 endpoint 导出直到进程结束（ghost 指标流 + 泄漏 goroutine）；修复为 meter 面单独追踪「最近产出 meterProvider 的 Init」链（lastMeterProvider），本次装了 meterProvider 时关掉上一个持 meterProvider 的 impl 的 meter 面（shutdownOnce 幂等）；#52「未替换 meter 面时谁都不许关」语义不变

关联问题单：tracing#68。

## [v0.3.6] — 2026-11-08

- build: 依赖地板对齐——本版把 oekit 依赖地板抬到各依赖当前最新已发布版本（matrix-tools/scripts/align_floors.py 维护轮）

## [v0.3.5] — 2026-11-07

- docs(init): re-Init 对称关闭语义文档化 + #58 场景回归钉——空 Endpoint 时全局 Meter 不再指向已 Shutdown 实例（该缺陷已随 v0.3.4 修复，本轮钉住契约）（#58）
- fix(init): validateSampleRate——负值/NaN 报错、0 值 WARN「=全量采样」——配置写错不再静默变成 100% 成本暴涨（#59）
- docs: obs.WithTracing 空 Endpoint 什么都不装的缺口在 bootstrap/obs 侧修复（PR #106：本地 provider+传播器照装、WARN 点明未启用导出），本仓文档交叉声明（#60）

关联问题单：tracing#58、tracing#59、tracing#60。

## [v0.3.4] — 2026-11-06

- **BREAKING** fix(init): 重 Init 的旧 provider 关闭与全局替换对称化（[#50](https://github.com/oekit/tracing/issues/50)、[#52](https://github.com/oekit/tracing/issues/52)）——此前 `prev.Shutdown` 无条件执行，而 `otel.SetMeterProvider` 仅在本次产出 meterProvider 时才调用：重 Init 传 `DisableMetrics=true` 或 Endpoint="" 时全局 MeterProvider 仍指向旧实例却被关闭——打点零报错、导出静默全灭（与 v0.3.2 承诺「DisableMetrics=true ⇒ 全局 MeterProvider 保持原样」在重开场景相反）。修法：关闭对称化——本次也装了 meterProvider（全局已替换）才整体 Shutdown 旧 provider；否则只关 tracerProvider，旧 meterProvider 继续导出
- docs(traceid): README/doc.go 指向真实中间件位置（[#51](https://github.com/oekit/tracing/issues/51)）——两处宣称的 `traceid.Middleware()` 在本包不存在（按快速上手写 `h.Use(traceid.Middleware()...)` 直接编译失败）：正身在 oekit/middleware 的 hmw 标准栈（ekit 拆分时中间件留在 middleware 仓）。本包导出面 Key/HeaderKey/IDLength/ContextWith/FromContext 不变

关联问题单：tracing#50、tracing#51、tracing#52。

## [v0.3.3] — 2026-11-06

- build: 矩阵依赖地板对齐（维护轮 #233）

## [v0.3.2] — 2026-10-29

- feat: Config 新增 DisableMetrics（**不导出 OTLP Metrics**，含 Go runtime 指标）——collector 侧**没有 metrics 管线**时的止噪开关

OTel collector 只为**配了管线的 signal** 注册 gRPC service；collector 只有 traces 管线时，它对 MetricsService 回 `Unimplemented: unknown service opentelemetry.proto.collector.metrics.v1.MetricsService`，而本包的周期读取器**每 15s 推一次** ⇒ 表现为「服务一切正常，但日志每 15s 一条导出失败」。

此时这些指标**本来就无处可去**（collector 会丢弃）⇒ 置 `DisableMetrics: true` 即止噪：不建 OTLP metrics 导出器、不启动 Go runtime 指标采集、全局 MeterProvider 保持调用方原样（业务侧 `otel.Meter` 打点照常可用，只是不导出）。待 collector 配好 metrics 管线后把该字段改回 `false` 即恢复。

**零值 false = 保持既有行为**（新增字段不改任何既有调用方的行为面，无 opt-in 陷阱）；grpc 与 http 两条分支同一语义。

## [v0.3.1] — 2026-10-28

- chore(deps): 测试依赖的 sqlite 驱动换成纯 Go 实现，清掉 CGO 直接依赖（#37）

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