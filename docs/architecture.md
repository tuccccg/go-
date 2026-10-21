# Architecture

```text
Client -> Gin API -> JWT -> in-memory per-subject token bucket -> model providers
                          |                     |-> Redis cache (tenant/user/model/request key)
                          |                     |-> retry + provider failover
                          |                     +-> SSE stream + heartbeat + cancellation
                          +-> MCP Tool Registry -> Python Supervisor -> MCP server(s)
                          +-> /agent/plan -----> LangGraph Supervisor -> model + MCP tools

Offline trajectory JSONL -> SMC latent-preference posterior -> PPO router -> held-out rule baseline
```

## Runtime behavior

- Redis is optional. Chat responses are cached only when `temperature=0`; embeddings are cached by request. Cache keys include tenant, user fallback, provider identity, and the full model request, with a configurable TTL.
- The Go gateway discovers MCP definitions from the Supervisor on startup and refreshes the registry every 30 seconds. Tool input properties and required keys are copied into the gateway validator. Tool execution proxies back to the Supervisor.
- The Supervisor reads `MCP_SERVERS` as a JSON object of server names and Streamable HTTP URLs. `/v1/plan` builds a LangGraph assistant/tool loop from tools discovered for that run.
- Non-streaming model calls retry transient failures and fail over across configured providers. SSE can fail over before headers are sent; afterward it relays the upstream stream with client cancellation and 15-second keepalive comments.
- `training/` estimates a preference vector from interaction feedback using particle filtering and trains a PPO action selector against offline outcomes. It is not connected to production routing by default.

## Remaining production engineering

Rate-limit state, tool-call counts, and idempotency records are still in memory. Multi-instance deployments need shared Redis implementations plus TTL/retention policies. Add production telemetry, provider circuit breakers, strict JSON Schema validation, token-budgeted conversation compaction, and a representative anonymized trajectory set before making performance or quality claims.
