# Go 智能体服务系统

Go/Gin 多模型网关 + Python LangGraph Supervisor 的复现项目。当前仓库包含 JWT 鉴权、内存令牌桶限流、多上游重试/切换、SSE 流式代理、Redis Embedding/问答缓存、MCP 动态发现与调用、工具幂等和调用治理，以及独立的 SMC/PPO 离线实验代码。

## 环境

- Go 1.23 或更高版本。当前机器已安装 Go 1.27.0，默认目录 `C:\Program Files\Go`。
- Docker Desktop（推荐一键启动）或本机 Redis、Python 3.12。
- 一个 OpenAI 兼容模型 API。Supervisor 需要支持 tool calling 的模型。

## Docker Compose 启动

```powershell
Copy-Item .env.example .env
# 编辑 .env：至少设置 JWT_SECRET、SUPERVISOR_TOKEN、LLM_PROVIDERS、LLM_API_KEY
docker compose up --build
```

默认启动网关 `:8080`、Supervisor `:8001` 和带 AOF 的 Redis。通过 `MCP_SERVERS` 配置远程 Streamable HTTP MCP 服务，例如：

```dotenv
MCP_SERVERS={"math":{"url":"http://host.docker.internal:8002/mcp"}}
```

MCP 工具目录由 Supervisor 实时发现，Go 网关启动时加载目录并每 30 秒同步一次。手动运行时，在 `supervisor/` 安装依赖后执行 `uvicorn main:app --port 8001`，另起终端运行 Go 服务并设置 `.env.example` 中的环境变量。

## 生成开发 JWT

```powershell
$env:JWT_SECRET = "replace-with-a-long-random-secret"
go run ./cmd/token -sub demo-user -tenant demo -secret $env:JWT_SECRET
```

请求头使用 `Authorization: Bearer <JWT>`。`tenant_id` 是可选 claim，缓存按 tenant、用户主体、上游和请求模型/参数隔离。

## API

- `GET /healthz`：网关健康检查
- `POST /v1/chat/completions`：OpenAI Chat Completions JSON/SSE 代理
- `POST /v1/embeddings`：Embedding 代理，Redis 缓存
- `POST /v1/agent/plan`：转发至 LangGraph Supervisor 执行 Agent 流程
- `GET /v1/tools`：列出本地工具和已发现的 MCP 工具
- `POST /v1/tools/:name`：执行工具，支持 `Idempotency-Key`

## PPO 离线实验

参照 [training/README.md](D:\go\training\README.md) 准备轨迹 JSONL 数据，然后在 `training/` 安装依赖并运行 SMC 偏好估计与 PPO/规则基线留出集对比。该策略目前是离线实验，不会自动改变在线路由。

## 配置与当前边界

配置样例在 [.env.example](D:\go\.env.example)。上游格式为 `name=base_url|api_key|default_model`，多个上游用逗号分隔。Redis 缓存可选；未设置 `REDIS_ADDR` 时缓存关闭，Redis 暂时不可用时网关继续请求上游。限流、工具计数和幂等结果目前保存在单实例内存中。

99.9% SLA、500+ QPS、35% 缓存命中率、PPO 提升后的完成率/延迟/Token 成本等简历指标，需按目标部署、真实数据集和基准压测独立测量，本项目不预先声称已达到这些结果。
