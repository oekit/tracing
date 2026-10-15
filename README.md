# tracing

> 分布式追踪门面——OTel 接入/trace_id 真相源/HTTP·Redis·GORM 插桩装饰器

> 🌐 项目主页：**https://oekit.github.io/** · 名称由来：**O**pen **E**asy **Kit**（开源 · 易用 · 工具集合）
> 🤖 AI Agent：本仓规范与硬规则见 [AGENTS.md](AGENTS.md)；机器可读索引见 [llms.txt](https://oekit.github.io/llms.txt)（接口签名以源码为准）。

## 安装

```bash
go get github.com/oekit/tracing@v0.2.0
```

## 这个仓库做什么

`tracing` 是**分布式追踪门面**：OTel 接入、`trace_id` 唯一真相源、HTTP/Redis/GORM 插桩装饰器。

**口径：核心组件零 tracing 强制注入，装饰器显式接入**——redisx/gormx 等核心构造无任何可观测性副作用，需要 span 时用本仓装饰器一行接入。

## 组成

| 包 | 做什么 |
|---|---|
| 根包 | `tracing.Init(Config)`：OTLP 导出器 + provider（Endpoint 为空 = 仅本地 provider，零网络） |
| `traceid` | trace_id 生成/提取/桥接：`Middleware()`、`FromContext`、`ContextWith`。logx 的 `Ctx*` 方法自动读取 |
| `httpotel` | net/http 插桩（`Transport()` / `WrapTransport`） |
| `redisotel` | go-redis 插桩（`InstrumentTracing`） |
| `gormotel` | GORM 插桩插件（`NewPlugin`，`db.Use` 显式挂载） |

## 快速使用

```go
import "github.com/oekit/tracing"

provider, _ := tracing.Init(tracing.Config{
    ServiceName: "my-service",
    Endpoint:    "otel-collector:4317",
    Insecure:    true,
})
defer provider.Shutdown(context.Background())
```

装饰器接入（均为显式、无 SDK 时 noop）：

```go
import (
    gormotel "github.com/oekit/tracing/gormotel"
    redisotel "github.com/oekit/tracing/redisotel"
)

// GORM：显式挂插件
_ = db.Use(gormotel.NewPlugin(gormotel.WithDBName("mydb")))

// Redis：装饰已创建的 client
_ = redisotel.InstrumentTracing(rdb.Client())
```

## 边界

- Hertz/Kitex 框架侧 instrumentation 分属 [hertz](https://github.com/oekit/hertz)（hertzotel/hertztracer）
  与 [kitex](https://github.com/oekit/kitex)（kitexotel）——**同一服务 OTel 与 OpenTracing 二选一**，叠加会双 span


## 矩阵总览

## 矩阵总览（23 活跃模块，自动生成）

| 模块 | 内容 | 矩阵内依赖 |
|---|---|---|
| [pkg](https://github.com/oekit/pkg) | 微工具集 16 合 1（errors/validator/stringx/…） | — |
| [concurrency](https://github.com/oekit/concurrency) | 并发与流控原语 | — |
| [logx](https://github.com/oekit/logx) | 日志门面 + shutdown | — |
| [core](https://github.com/oekit/core) | 应用基建件 | — |
| [istio](https://github.com/oekit/istio) | Istio XDS | — |
| [metrics](https://github.com/oekit/metrics) | Prometheus 门面 | [core](https://github.com/oekit/core) |
| [tracing](https://github.com/oekit/tracing) | OTel + traceid | [logx](https://github.com/oekit/logx) |
| [registry](https://github.com/oekit/registry) | 注册发现 + Nacos + 配置 | [core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx) |
| [data](https://github.com/oekit/data) | 缓存检查器 | [concurrency](https://github.com/oekit/concurrency)、[logx](https://github.com/oekit/logx) |
| [gorm](https://github.com/oekit/gorm) | SQL/CRUD | [logx](https://github.com/oekit/logx)、[pkg](https://github.com/oekit/pkg) |
| [redis](https://github.com/oekit/redis) | Redis 全家 | [concurrency](https://github.com/oekit/concurrency)、[data](https://github.com/oekit/data)、[logx](https://github.com/oekit/logx) |
| [httpx](https://github.com/oekit/httpx) | HTTP 客户端 | [concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[registry](https://github.com/oekit/registry)、[tracing](https://github.com/oekit/tracing) |
| [network](https://github.com/oekit/network) | 轻量网络件 | [concurrency](https://github.com/oekit/concurrency)、[logx](https://github.com/oekit/logx)、[tracing](https://github.com/oekit/tracing) |
| [timertask](https://github.com/oekit/timertask) | 定时任务 | [concurrency](https://github.com/oekit/concurrency)、[logx](https://github.com/oekit/logx) |
| [middleware](https://github.com/oekit/middleware) | Hertz 中间件栈 | [concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[pkg](https://github.com/oekit/pkg)、[registry](https://github.com/oekit/registry)、[tracing](https://github.com/oekit/tracing) |
| [nns](https://github.com/oekit/nns) | 服务治理门面 | [concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx)、[registry](https://github.com/oekit/registry) |
| [nns-endpoint](https://github.com/oekit/nns-endpoint) | 端点熔断重试 | [concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[httpx](https://github.com/oekit/httpx)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[nns](https://github.com/oekit/nns)、[registry](https://github.com/oekit/registry) |
| [bootstrap](https://github.com/oekit/bootstrap) | 应用引导 | [concurrency](https://github.com/oekit/concurrency)、[logx](https://github.com/oekit/logx) |
| [handler](https://github.com/oekit/handler) | CRUD handler | [concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[gorm](https://github.com/oekit/gorm)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[middleware](https://github.com/oekit/middleware)、[pkg](https://github.com/oekit/pkg)、[registry](https://github.com/oekit/registry)、[tracing](https://github.com/oekit/tracing) |
| [hertz](https://github.com/oekit/hertz) | Hertz 运行时 | [bootstrap](https://github.com/oekit/bootstrap)、[concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[nns](https://github.com/oekit/nns)、[registry](https://github.com/oekit/registry)、[tracing](https://github.com/oekit/tracing) |
| [kitex](https://github.com/oekit/kitex) | Kitex 生态 | [bootstrap](https://github.com/oekit/bootstrap)、[concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[nns](https://github.com/oekit/nns)、[pkg](https://github.com/oekit/pkg)、[registry](https://github.com/oekit/registry) |
| [workflow](https://github.com/oekit/workflow) | 流程编排 | [concurrency](https://github.com/oekit/concurrency)、[logx](https://github.com/oekit/logx) |
| [kitex-examples](https://github.com/oekit/kitex-examples) | Kitex 示例 | [bootstrap](https://github.com/oekit/bootstrap)、[concurrency](https://github.com/oekit/concurrency)、[core](https://github.com/oekit/core)、[kitex](https://github.com/oekit/kitex)、[logx](https://github.com/oekit/logx)、[metrics](https://github.com/oekit/metrics)、[nns](https://github.com/oekit/nns)、[pkg](https://github.com/oekit/pkg)、[registry](https://github.com/oekit/registry)、[tracing](https://github.com/oekit/tracing) |

> 依赖严格自底向上；微库已合并为 pkg，旧微库仓已删除。

> 本仓在矩阵中的位置与依赖见上表「矩阵内依赖」列。历史沿革：源自 ekit 单体库
> （git.enjoye.top/enjoydream/ekit v0.34.0，冻结维护），2026-10 拆分为独立 module。

## License

MIT — 见 [LICENSE](LICENSE)
