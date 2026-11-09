#Requires -Version 5.1
<#
.SYNOPSIS
    一键跑网关基准测试：起 mock 上游 -> 起网关 -> 签 JWT -> 流式 TTFT -> 多档并发 -> 收尾。

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File bench/run.ps1
    powershell -ExecutionPolicy Bypass -File bench/run.ps1 -Duration 60s -Concurrency "10,50,200"

.NOTES
    压的是网关本身：上游是 mock（固定响应），缓存关闭，限流拉满。
    mock 与压测客户端都在 bench/ 下用 Go 写，只依赖标准库。
    流式阶段先跑：它最快暴露线程/连接问题，也避免被后面的长时间压测污染。
#>
param(
    [string]$Concurrency = "10,50,200",
    [string]$Duration = "30s",
    [string]$StreamDuration = "15s",
    [int]$Port = 8080,
    [int]$MockPort = 9999
)

$ErrorActionPreference = "Stop"

$repo = Split-Path -Parent $PSScriptRoot
Set-Location $repo
Write-Host "repo: $repo" -ForegroundColor DarkGray

$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) { throw "找不到 go，请确认 Go 已安装并在 PATH 中" }
if (-not $env:GOPROXY) { $env:GOPROXY = "https://goproxy.cn,direct" }   # proxy.golang.org 国内不可达

$levels = @()
foreach ($item in $Concurrency.Split(',')) {
    $trimmed = $item.Trim()
    if ($trimmed -ne "") { $levels += [int]$trimmed }
}
if ($levels.Count -eq 0) { throw "-Concurrency 至少给一个值，例如 10,50,200" }

# ---------- 编译 ----------
$bin = Join-Path $repo "bin"
$logs = Join-Path $bin "bench-logs"
New-Item -ItemType Directory -Force -Path $bin, $logs | Out-Null

Write-Host "编译 server / token / mock / loadgen ..." -ForegroundColor Yellow
& $go build -o (Join-Path $bin "gateway-bench.exe") ./cmd/server;      if ($LASTEXITCODE -ne 0) { throw "编译 cmd/server 失败" }
& $go build -o (Join-Path $bin "token.exe")         ./cmd/token;       if ($LASTEXITCODE -ne 0) { throw "编译 cmd/token 失败" }
& $go build -o (Join-Path $bin "mock-upstream.exe") ./bench/mockupstream; if ($LASTEXITCODE -ne 0) { throw "编译 bench/mockupstream 失败" }
& $go build -o (Join-Path $bin "loadgen.exe")       ./bench/loadgen;   if ($LASTEXITCODE -ne 0) { throw "编译 bench/loadgen 失败" }

# ---------- 环境变量 ----------
$env:JWT_SECRET       = "bench-secret"
$env:LLM_PROVIDERS    = "mock=http://127.0.0.1:$MockPort/v1|sk-mock|mock-model"
$env:REDIS_ADDR       = ""          # 关缓存：测纯代理链路
$env:RATE_LIMIT_RPS   = "1000000"   # 关限流：否则测的是限流器
$env:RATE_LIMIT_BURST = "1000000"
$env:HTTP_ADDR        = ":$Port"

# ---------- 起进程 ----------
$mock = $null
$srv = $null
try {
    Write-Host "启动 mock 上游 :$MockPort ..." -ForegroundColor Yellow
    $mock = Start-Process -FilePath (Join-Path $bin "mock-upstream.exe") -ArgumentList @("-addr", "127.0.0.1:$MockPort", "-first-chunk-delay", "20ms") -PassThru `
        -RedirectStandardOutput (Join-Path $logs "mock.out.log") `
        -RedirectStandardError  (Join-Path $logs "mock.err.log")

    $mockReady = $false
    for ($i = 0; $i -lt 25; $i++) {
        try {
            $probe = Invoke-WebRequest -Uri "http://127.0.0.1:$MockPort/v1/chat/completions" -Method POST `
                -ContentType "application/json" -Body '{"model":"mock-model","messages":[]}' `
                -UseBasicParsing -TimeoutSec 2
            if ($probe.StatusCode -eq 200) { $mockReady = $true; break }
        } catch {
            Start-Sleep -Milliseconds 200
        }
    }
    if (-not $mockReady) { throw "mock 上游未就绪，看一下 $logs\mock.err.log" }

    Write-Host "启动网关 :$Port ..." -ForegroundColor Yellow
    $srv = Start-Process -FilePath (Join-Path $bin "gateway-bench.exe") -PassThru `
        -RedirectStandardOutput (Join-Path $logs "server.out.log") `
        -RedirectStandardError  (Join-Path $logs "server.err.log")

    $ready = $false
    for ($i = 0; $i -lt 40; $i++) {
        try {
            $null = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/healthz" -UseBasicParsing -TimeoutSec 2
            $ready = $true
            break
        } catch {
            Start-Sleep -Milliseconds 400
        }
    }
    if (-not $ready) { throw "网关未在 16 秒内就绪，看一下 $logs\server.err.log" }
    Write-Host "网关就绪" -ForegroundColor Green

    # ---------- 签 token ----------
    $token = (& (Join-Path $bin "token.exe") -sub bench -tenant bench -secret "bench-secret").Trim()
    if (-not $token) { throw "token 生成失败" }

    # ---------- 1. 流式 TTFT：经网关 vs 直连 mock ----------
    # mock 首块固定延迟 20ms 模拟模型首个 token，两条链路相减即为网关的转发开销。
    Write-Host "`n=== 流式 TTFT（并发 50 / $StreamDuration / mock 首块延迟 20ms）===" -ForegroundColor Cyan
    $streamRows = @()
    $probes = @(
        @{ Label = "经网关";    Url = "http://127.0.0.1:$Port/v1/chat/completions" },
        @{ Label = "直连 mock"; Url = "http://127.0.0.1:$MockPort/v1/chat/completions" }
    )
    foreach ($p in $probes) {
        $raw = & (Join-Path $bin "loadgen.exe") -c 50 -z $StreamDuration -token $token -stream -url $p.Url 2>&1 | Out-String
        Write-Output $raw
        $line = ($raw -split "`n" | Where-Object { $_ -match '^RESULT ' } | Select-Object -First 1)
        if ($line) {
            $parsed = ($line -replace '^RESULT\s+', '') | ConvertFrom-Json
            $streamRows += [pscustomobject]@{
                Path     = $p.Label
                TtftP50  = $parsed.ttft_p50_ms
                TtftP95  = $parsed.ttft_p95_ms
                TotalP95 = $parsed.total_p95_ms
                Errors   = $parsed.errors
            }
        }
    }
    if ($streamRows.Count -eq 2) {
        $overhead = [math]::Round($streamRows[0].TtftP95 - $streamRows[1].TtftP95, 2)
        $streamRows += [pscustomobject]@{
            Path = "网关开销"; TtftP50 = ""; TtftP95 = $overhead; TotalP95 = ""; Errors = ""
        }
    }
    $streamRows | Format-Table -AutoSize
    $streamRows | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $logs "stream.json") -Encoding UTF8

    # ---------- 2. 逐档压测（非流式） ----------
    $summary = @()
    foreach ($c in $levels) {
        Write-Host "`n=== 并发 $c / 时长 $Duration ===" -ForegroundColor Cyan
        $logPath = Join-Path $logs ("loadgen-c{0}.txt" -f $c)

        $raw = & (Join-Path $bin "loadgen.exe") -c $c -z $Duration -token $token `
            -url "http://127.0.0.1:$Port/v1/chat/completions" 2>&1 | Out-String

        $raw | Set-Content -LiteralPath $logPath -Encoding UTF8
        Write-Output $raw

        $line = ($raw -split "`n" | Where-Object { $_ -match '^RESULT ' } | Select-Object -First 1)
        if (-not $line) {
            Write-Host "本档未拿到结果，见 $logPath" -ForegroundColor Red
            continue
        }
        $summary += ($line -replace '^RESULT\s+', '') | ConvertFrom-Json
    }

    Write-Host "`n===== 汇总（抄进 docs/benchmark.md）=====" -ForegroundColor Green
    $summary | Format-Table -AutoSize
    $summary | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $logs "summary.json") -Encoding UTF8
    Write-Host "原始输出: $logs" -ForegroundColor DarkGray
} finally {
    foreach ($p in @($srv, $mock)) {
        if ($p -and -not $p.HasExited) {
            Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
        }
    }
    Write-Host "已停止 mock 与网关进程" -ForegroundColor DarkGray
}
