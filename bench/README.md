# 基准测试（bench）

压的是**网关本身**，不是模型。所以上游换成 mock：固定 0ms 处理延迟、固定响应体，
这样测出来的 req/s 和 P95 才是网关的转发、鉴权、限流、缓存逻辑本身的成本。

> 结论只写进 `docs/benchmark.md`，并且必须能由本页命令复现。

## 0. 前提

- Go 1.23+（`go version`）
- Python 3.8+（只用标准库）
- `ghz`：`go install github.com/bojand/ghz/cmd/ghz@latest`

## 1. 起 mock 上游

```powershell
python bench/mock_upstream.py
# mock upstream listening on http://127.0.0.1:9999/v1
```

## 2. 起网关，指向 mock

**关键三点**，漏一个测出来的就不是网关性能：

| 变量 | 值 | 为什么 |
|---|---|---|
| `REDIS_ADDR` | 留空 | 关缓存，测纯代理链路（要测缓存另跑一轮） |
| `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` | 1000000 | 默认 20/40，不关掉测的是限流器 |
| `LLM_PROVIDERS` | `mock=http://127.0.0.1:9999/v1\|sk-mock\|mock-model` | 指向 mock |

```powershell
$env:JWT_SECRET            = "bench-secret"
$env:LLM_PROVIDERS         = "mock=http://127.0.0.1:9999/v1|sk-mock|mock-model"
$env:REDIS_ADDR            = ""
$env:RATE_LIMIT_RPS        = "1000000"
$env:RATE_LIMIT_BURST      = "1000000"
go run ./cmd/server
```

## 3. 生成 token

```powershell
$env:JWT_SECRET = "bench-secret"
$token = go run ./cmd/token -sub bench -tenant bench -secret bench-secret
```

## 4. 压非流式

```powershell
ghz -z 60s -c 50 -m POST `
  -H "Content-Type: application/json" `
  -H "Authorization: Bearer $token" `
  -d '{"model":"mock-model","messages":[{"role":"user","content":"hello"}]}' `
  http://127.0.0.1:8080/v1/chat/completions
```

并发至少跑三档（10 / 50 / 200），每档记录：

- `Requests/s`
- `Average` / `P95` / `P99` 延迟
- 错误率（`Error distribution`）

## 5. 压流式（可选）

把 body 里的 `stream` 改成 `true`，ghz 会读到首字节就结束计时不明显，
这种场景建议改用首字延迟（TTFT）单独测，或用 `wrk` 记录 `time_starttransfer`。

## 6. 记录

把机型、Go 版本、三档并发的数字填进 `docs/benchmark.md`。
**没有跑过的格子留空**，不要估算。
