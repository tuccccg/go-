# 基准测试记录

> 本页只记录**实际跑过**的数字。空格表示未测，不要估算，不要跨环境复用。
> 复现命令见 [bench/README.md](../bench/README.md)。

## 测试环境

| 项 | 值 |
| --- | --- |
| CPU | Intel Core i7-11700 @ 2.50GHz（8 核 16 线程） |
| 内存 | 15.8 GB |
| OS | Windows 10 专业版 |
| Go | go1.27.0 windows/amd64 |
| 模型侧 | `bench/mockupstream`（Go），固定响应体，非流式 0ms；SSE 首块固定延迟 20ms 模拟首 token |
| 压测客户端 | `bench/loadgen`（Go），本机 loopback |
| 缓存 | 关闭（`REDIS_ADDR` 未设置） |
| 限流 | 关闭（`RATE_LIMIT_RPS=1000000`） |
| 时长 | 非流式每档 30s，流式每档 20s |

**测量边界：客户端、网关、mock 三进程同机，共享 16 个逻辑核；数字用于版本间横向比较，不等于线上容量。**

## 非流式 `POST /v1/chat/completions`

| 并发 | req/s | 平均 | P50 | P95 | P99 | 错误率 |
| --- | --- | --- | --- | --- | --- | --- |
| 10 | 15287 | 0.64 ms | 0.53 ms | 1.16 ms | 1.67 ms | 0 |
| 50 | 19611 | 2.54 ms | 1.89 ms | 6.56 ms | 9.43 ms | 0 |
| 200 | 23085 | 8.65 ms | 7.85 ms | 14.90 ms | 19.02 ms | 0 |

三档并发全部零错误，吞吐随并发仍单调上升，说明在 200 并发内未触及网关自身饱和点。

## 流式 `POST /v1/chat/completions`（`stream=true`，并发 50）

mock 每流首块固定延迟 20ms。同一条链路分别经网关和直连 mock 各跑一次，差值即网关的转发开销。

| 路径 | TTFT P50 | TTFT P95 | 总时长 P95 | 错误 |
| --- | --- | --- | --- | --- |
| 经网关 | 20.93 ms | 22.27 ms | 22.58 ms | 0 |
| 直连 mock | 20.57 ms | 21.62 ms | 21.89 ms | 0 |
| **网关开销** | — | **0.65 ms** | 0.69 ms | — |

结论：网关对 SSE 首字节的额外延迟约 0.65ms（P95），在真实模型首 token 数百毫秒的量级下可忽略。

## 压测中修复的两个问题

### 问题 1：上游连接复用不足 → 线程耗尽

首轮在并发 50 直接崩溃：

```
runtime: program exceeds 10000-thread limit
fatal error: thread exhaustion
```

`http.DefaultTransport` 每个 host 只保留 2 条空闲连接。网关是并发代理，请求一多就退化成
"每条请求新建一条 TCP 连接"，而 Windows 上每次 dial / close 都要占一个线程，几千并发后触顶。

### 问题 2：连接数无上限 → 90 秒后仍会崩

只放开空闲池（`MaxIdleConnsPerHost=128`）后，短时压测正常，但三档连续跑 90 秒左右再次崩溃，
并发 200 那档出现 200 个请求超时。说明没有总量约束时，连接仍会被不断新建。

### 修复

在 `gateway.New()` 中克隆默认 Transport 并显式设限：

```go
transport := http.DefaultTransport.(*http.Transport).Clone()
transport.MaxIdleConns = 128
transport.MaxIdleConnsPerHost = 64
transport.MaxConnsPerHost = 64
transport.IdleConnTimeout = 90 * time.Second
```

`MaxConnsPerHost` 是关键：没有它，空闲池再大也挡不住新建连接。

| 阶段 | 并发 50 | 并发 200（连续 90s） |
| --- | --- | --- |
| 修复前 | fatal: thread exhaustion | 崩溃 |
| 仅放开空闲池 | 19697 req/s，P95 6.47 ms，0 错误 | 90s 后崩溃，200 请求超时 |
| 空闲池 + 连接上限 | 19611 req/s，P95 6.56 ms，0 错误 | 23085 req/s，P95 14.90 ms，0 错误 |

## 未测项

- 缓存命中率：待补（需开启 Redis 并积累流量）。
- 长连接保持下的稳定性（小时级）。
- 多实例部署下的表现：限流计数、工具调用计数、幂等记录仍是单实例内存实现。
