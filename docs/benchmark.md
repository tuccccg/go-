# 基准测试记录

> 本页只记录**实际跑过**的数字。空格表示未测，不要估算，不要跨环境复用。
> 复现命令见 [bench/README.md](../bench/README.md)。

## 测试环境

| 项 | 值 |
| --- | --- |
| CPU / 内存 | 待填（如 i7-13700H / 32GB） |
| OS | 待填（如 Windows 11 23H2） |
| Go 版本 | 待填（`go version`） |
| 模型侧 | `bench/mock_upstream.py`，固定响应体，0ms 处理延迟 |
| 缓存 | 关闭（`REDIS_ADDR` 未设置） |
| 限流 | 关闭（`RATE_LIMIT_RPS=1000000`） |
| 并发工具 | `ghz -z 60s` |

## 非流式 `POST /v1/chat/completions`

| 并发 | req/s | 平均延迟 | P95 | P99 | 错误率 |
| --- | --- | --- | --- | --- | --- |
| 10 | | | | | |
| 50 | | | | | |
| 200 | | | | | |

## 流式 `POST /v1/chat/completions`（`stream=true`）

| 指标 | 值 |
| --- | --- |
| 首字延迟（TTFT）P95 | 待填 |
| 完整响应 P95 | 待填 |

## 缓存命中（开启 Redis，`REDIS_ADDR` 指向本地实例）

| 指标 | 值 |
| --- | --- |
| 命令 | `redis-cli info stats \| Select-String keyspace_hits` / `keyspace_misses` |
| 命中率 | 待填 |
| 采样窗口 | 待填（请求数 / 时长） |

## 结论

- 网关在 并发 N 下达到 X req/s，P95 Y ms，错误率 0。
- 瓶颈观察：待填（如 GC、JSON 序列化、连接复用）。
- 已知限制：限流计数、工具调用计数、幂等记录为单实例内存实现，多实例部署需迁移到 Redis。
