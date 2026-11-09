# Go 智能体服务系统

Go/Gin 多模型网关 + Python LangGraph Supervisor。覆盖 JWT 鉴权与租户隔离、令牌桶限流、多上游重试与故障切换、
SSE 流式代理、Redis 缓存、MCP 工具动态发现与调用治理，以及一套独立的 SMC/PPO 离线实验代码。

**当前状态**：功能链路完整，有单元测试与 CI，有可复现的压测数据（见 [docs/benchmark.md](docs/benchmark.md)）；
限流计数、工具调用计数、幂等记录仍是单实例内存实现，多实例部署需迁移到 Redis。

## 能力清单

| 能力 | 实现位置 | 说明 |
| --- | --- | --- |
| 多模型网关与故障切换 | `internal/gateway` | OpenAI 兼容入口；429/5xx 指数退避重试，跨上游自动切换 |
| SSE 流式代理 | `internal/gateway` | 响应头发送前可切上游，发出后中继并支持客户端取消，15s 心跳保活 |
| 鉴权与租户隔离 | `internal/middleware` | JWT（`tenant_id` 可选 claim），按用户主体的内存令牌桶限流 |
| MCP 工具发现与治理 | `internal/tools` | 启动拉取工具目录、每 30s 刷新，同步 input schema 做入参校验，支持 `Idempotency-Key` |
| Agent 编排 | `supervisor/` | LangGraph assistant/tool loop，按本次发现的工具构建 |
| 缓存 | `internal/cache` | 仅缓存 `temperature=0` 的问答与 Embedding，key 含 tenant/user/provider/模型参数 |
| 离线策略实验 | `training/` | 粒子滤波（SMC）估计偏好向量 + PPO 动作选择器，未接入线上路由 |
| 测试与压测 | `internal/**/**_test.go`、`bench/` | 单元测试 + 自写负载工具与假上游，可复现 TTFT 与吞吐 |

## 架构

```text
Client -> Gin API -> JWT -> 内存令牌桶 -> model providers
                       |          |-> Redis cache (tenant/user/model/request key)
                       |          |-> retry + provider failover
                       |          +-> SSE stream + heartbeat + cancellation
                       +-> MCP Tool Registry -> Python Supervisor -> MCP server(s)
                       +-> /v1/agent/plan -----> LangGraph Supervisor -> model + MCP tools

离线轨迹 JSONL -> SMC 偏好后验 -> PPO 路由 -> 留出集规则基线对比
```

细节见 [docs/architecture.md](docs/architecture.md)。

## 快速开始

```powershell
Copy-Item .env.example .env
# 编辑 .env：至少设置 JWT_SECRET、SUPERVISOR_TOKEN、LLM_PROVIDERS、LLM_API_KEY
docker compose up --build
```

默认启动网关 `:8080`、Supervisor `:8001` 和带 AOF 的 Redis。通过 `MCP_SERVERS` 配置远程
Streamable HTTP MCP 服务：

```dotenv
MCP_SERVERS={"math":{"url":"http://host.docker.internal:8002/mcp"}}
```

MCP 工具目录由 Supervisor 实时发现，Go 网关启动时加载并每 30 秒同步一次。手动运行：在 `supervisor/`
安装依赖后执行 `uvicorn main:app --port 8001`，另起终端运行 Go 服务并设置 `.env.example` 中的变量。

**环境要求**：Go 1.23+、Docker Desktop（或本机 Redis + Python 3.12）、一个 OpenAI 兼容且支持 tool calling 的模型 API。

## 生成开发 JWT

```powershell
$env:JWT_SECRET = "replace-with-a-long-random-secret"
go run ./cmd/token -sub demo-user -tenant demo -secret $env:JWT_SECRET
```

请求头使用 `Authorization: Bearer <JWT>`。

## API

- `GET /healthz`：健康检查
- `POST /v1/chat/completions`：OpenAI Chat Completions JSON/SSE 代理
- `POST /v1/embeddings`：Embedding 代理，Redis 缓存
- `POST /v1/agent/plan`：转发至 LangGraph Supervisor 执行 Agent 流程
- `GET /v1/tools`：列出本地工具与已发现的 MCP 工具
- `POST /v1/tools/:name`：执行工具，支持 `Idempotency-Key`

## 开发、测试与压测

```powershell
go vet ./... && go test ./... -race     # CI 跑的就是这三步
powershell -ExecutionPolicy Bypass -File bench/run.ps1
```

CI（[.github/workflows/ci.yml](.github/workflows/ci.yml)）在每次 push / PR 上执行 gofmt、go vet、go build、
go test -race。

压测用 `bench/` 下自写的负载工具与假上游，只依赖标准库，不依赖第三方压测工具：

| 场景 | 并发 | 结果 |
| --- | --- | --- |
| 非流式 | 200 | 23085 req/s，P95 14.90ms，错误率 0 |
| 流式 TTFT | 50 | 网关额外开销 P95 0.65ms（对比直连 mock） |

测量环境、完整三档数据、以及压测过程中修复的两轮上游连接缺陷（线程耗尽 → 连接总数上限），
都记录在 [docs/benchmark.md](docs/benchmark.md) 和 [bench/README.md](bench/README.md)。

## 已知限制

- 限流计数、工具调用计数、幂等记录在单实例内存中，多实例部署需要迁移到 Redis 并加 TTL。
- 缓存命中率未测量；流式只测了首字节开销，未测长连接稳定性。
- `MaxConnsPerHost` 目前硬编码 64，上游延迟变成秒级时需要按「目标并发 × 延迟」重算。
- 无生产级遥测（OpenTelemetry）、无熔断器、无严格 JSON Schema 校验。

## PPO 离线实验

参照 [training/README.md](training/README.md) 准备轨迹 JSONL 数据，然后在 `training/` 安装依赖并运行
SMC 偏好估计与 PPO/规则基线留出集对比。该策略是离线实验，不会自动改变在线路由。

## 配置

配置样例见 [.env.example](.env.example)。上游格式为 `name=base_url|api_key|default_model`，多个上游用逗号分隔。
Redis 可选：未设置 `REDIS_ADDR` 时缓存关闭，Redis 不可用时网关继续请求上游。
