# 基准测试（bench）

压的是**网关本身**，不是模型。上游换成 mock（固定响应、0ms 处理），压测客户端与 mock 都在
`bench/` 下用 Go 写、只依赖标准库，所以不需要装任何第三方压测工具，也不会因为工具版本不同测出不同结果。

> 结论只写进 `docs/benchmark.md`，且必须能由本页命令复现。

## 一键跑

```powershell
powershell -ExecutionPolicy Bypass -File bench/run.ps1
# 或指定档位与时长
powershell -ExecutionPolicy Bypass -File bench/run.ps1 -Duration 60s -Concurrency "10,50,200"
```

脚本会依次：编译 `cmd/server`、`cmd/token`、`bench/mockupstream`、`bench/loadgen` →
起 mock（:9999）→ 起网关（:8080）→ 等 `/healthz` 就绪 → 签 JWT → 逐档压测 → 停掉两个进程。

原始输出落在 `bin/bench-logs/`，汇总同时打印成表格。

## 三个必须注意的环境变量

| 变量 | 值 | 为什么 |
| --- | --- | --- |
| `REDIS_ADDR` | 留空 | 关缓存，测纯代理链路（要测缓存命中另跑一轮） |
| `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` | 1000000 | 默认 20 / 40，不关掉测的是限流器 |
| `LLM_PROVIDERS` | `mock=http://127.0.0.1:9999/v1\|sk-mock\|mock-model` | 指向 mock，不指向真模型 |

`run.ps1` 已经替你设好这三项。手动跑的话必须自己设。

## 手动跑（分三个终端）

```powershell
# 终端 1
go run ./bench/mockupstream -addr 127.0.0.1:9999

# 终端 2
$env:JWT_SECRET="bench-secret"
$env:LLM_PROVIDERS="mock=http://127.0.0.1:9999/v1|sk-mock|mock-model"
$env:REDIS_ADDR=""
$env:RATE_LIMIT_RPS="1000000"
$env:RATE_LIMIT_BURST="1000000"
go run ./cmd/server

# 终端 3
$token = go run ./cmd/token -sub bench -tenant bench -secret bench-secret
go run ./bench/loadgen -c 50 -z 30s -token $token `
  -url "http://127.0.0.1:8080/v1/chat/completions"
```

`loadgen` 输出一行人读的汇总，外加一行 `RESULT {...}` JSON，方便脚本解析。

## 已知的测量边界

- 本机 loopback 压测，**客户端、网关、mock 在同一台机器上抢 CPU**，并发越高越明显；
  数字用于横向比较不同版本，不能当作线上容量。
- 只测非流式。流式要用首字延迟（TTFT）单独衡量，`loadgen` 目前计的是完整响应时间。
- 缓存关闭。想测命中率，另起一轮并设置 `REDIS_ADDR`，从 `redis-cli info stats` 取
  `keyspace_hits` / `keyspace_misses`。
