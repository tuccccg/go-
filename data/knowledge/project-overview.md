# Go 智能体服务系统

- Go/Gin 网关负责 JWT 鉴权、模型调用、限流、重试、故障切换和 SSE 流式响应。
- Python LangGraph Supervisor 负责 Agent 编排，并通过 MCP 动态发现和调用工具。
- Redis 用于缓存 Embedding 与可复用的问答结果。
- 工具调用经过参数校验、幂等控制、调用上限和失败处理。
- SMC/PPO 训练与评估脚本位于 training 目录，需要准备交互轨迹数据。
- DeepSeek 是当前配置的模型服务；真实效果指标应通过部署和测量获得。
