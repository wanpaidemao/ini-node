<#
.SYNOPSIS
  一键修复:自动定位分叉点 → 剪掉本地分叉块 → 观察真实链恢复。

.DESCRIPTION
  串联 locate-fork.ps1 与 recover-from-fork.ps1:
    1) forklocate 以 P2P 交叉验证定位 FORK_POINT(只读)
    2) 对本地 FORK_POINT+1 的块 invalidateblock(节点 reorg 回退)
    3) 周期采样 getblockchaininfo 直到块高越过分叉点 30+,判定恢复

  路径/参数自动发现,不写死(DbPath 从 runtime.ini datadir 推导、Peer 从
  getpeerinfo 自动选、Lo 从 addcheckpoint 解析、RPC 从 runtime.ini 读);
  显式参数优先级最高。

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File fix-fork.ps1
#>
param(
    [string]$DbPath = "",
    [string]$Peer = "",
    [int]$Lo = 0,
    [string]$Config = "",
    [string]$RpcHost = "",
    [int]$RpcPort = 0,
    [string]$RpcUser = "",
    [string]$RpcPass = ""
)

$ErrorActionPreference = 'Stop'
$dir = $PSScriptRoot

function Read-Ini([string]$Path) {
    $map = @{}
    if (-not (Test-Path $Path)) { return $map }
    foreach ($line in [System.IO.File]::ReadAllLines($Path)) {
        $t = $line.Trim()
        if ($t -eq '' -or $t.StartsWith(';') -or $t.StartsWith('#')) { continue }
        if ($t.StartsWith('[') -and $t.EndsWith(']')) { continue }
        $idx = $t.IndexOf('=')
        if ($idx -le 0) { continue }
        $map[$t.Substring(0, $idx).Trim()] = $t.Substring($idx + 1).Trim()
    }
    return $map
}

function Find-Ini([string]$Config) {
    if ($Config -ne "") { return $Config }
    foreach ($cand in @(
            (Join-Path (Get-Location) 'runtime.ini'),
            (Join-Path $PSScriptRoot 'runtime.ini'),
            (Join-Path $PSScriptRoot '..\..\..\..\frontend\bin\runtime.ini')
        )) {
        if (Test-Path $cand) { return $cand }
    }
    return ""
}

# ---------- RPC 配置 ----------
$iniPath = Find-Ini $Config
$ini = @{}
if ($iniPath -ne "") { $ini = Read-Ini $iniPath }

if ($RpcUser -eq "") { $RpcUser = if ($ini['rpcuser']) { $ini['rpcuser'] } else { 'ini' } }
if ($RpcPass -eq "") { $RpcPass = if ($ini['rpcpass']) { $ini['rpcpass'] } else { 'ini' } }
$listen = $ini['rpclisten']
if ($RpcHost -eq "" -or $RpcPort -eq 0) {
    if ($listen) {
        if ($listen -match '^\[([^\]]+)\]:(\d+)$') {
            if ($RpcHost -eq "") { $RpcHost = $matches[1] }
            if ($RpcPort -eq 0)   { $RpcPort = [int]$matches[2] }
        } elseif ($listen -match '^(.+):(\d+)$') {
            if ($RpcHost -eq "") { $RpcHost = $matches[1] }
            if ($RpcPort -eq 0)   { $RpcPort = [int]$matches[2] }
        } else {
            if ($RpcHost -eq "") { $RpcHost = '127.0.0.1' }
            if ($RpcPort -eq 0)   { $RpcPort = [int]$listen }
        }
    }
}
if ($RpcHost -eq "") { $RpcHost = '127.0.0.1' }
if ($RpcPort -eq 0)  { $RpcPort = 34229 }

function Rpc([string]$Method, $Params) {
    $body = @{ jsonrpc = '1.0'; id = 1; method = $Method; params = $Params } |
        ConvertTo-Json -Compress -Depth 6
    $auth = 'Basic ' + [Convert]::ToBase64String(
        [Text.Encoding]::ASCII.GetBytes("${RpcUser}:${RpcPass}"))
    $r = Invoke-RestMethod `
        -Uri ("http://{0}:{1}" -f $RpcHost, $RpcPort) `
        -Method Post -Body $body -ContentType 'application/json' `
        -Headers @{ Authorization = $auth } -TimeoutSec 20
    if ($null -ne $r.error -and $null -ne $r.error) {
        throw ("RPC error: " + ($r.error | ConvertTo-Json -Compress))
    }
    return $r.result
}

# ---------- 参数解析:显式 > 配置文件/运行时推导 ----------
$resolvedDb = $DbPath
if ($resolvedDb -eq "") {
    $dd = $ini['datadir']
    if ($dd) {
        $cand = Join-Path $dd 'sugarmainnet\blocks_ffldb'
        if (Test-Path $cand) { $resolvedDb = $cand }
    }
}
if ($resolvedDb -ne "" -and -not (Test-Path $resolvedDb)) {
    Write-Error ("块库不存在: '{0}'" -f $resolvedDb)
    exit 2
}

$resolvedPeer = $Peer
if ($resolvedPeer -eq "") {
    try {
        $peers = Rpc 'getpeerinfo' @()
        $best = $peers | Sort-Object -Property @{Expression = { $_.lastblock }; Descending = $true } | Select-Object -First 1
        if ($null -ne $best -and $best.addr) { $resolvedPeer = $best.addr }
    } catch {
        Write-Warning ("无法从节点 RPC 选取 peer:{0}" -f $_.Exception.Message)
    }
}
if ($resolvedPeer -eq "") {
    $resolvedPeer = '24.160.187.156:34230'
    Write-Warning "未能自动选取 peer,使用默认 $resolvedPeer(RPC 不可达或节点未运行?)"
}

$resolvedLo = $Lo
if ($resolvedLo -eq 0) {
    $cp = $ini['addcheckpoint']
    if ($cp -match '^(\d+):') { $resolvedLo = [int]$matches[1] }
}
if ($resolvedLo -eq 0) { $resolvedLo = 43760164 }

Write-Host ("RPC      = {0}:{1} (user={2})" -f $RpcHost, $RpcPort, $RpcUser)
Write-Host ("dbpath   = {0}" -f $resolvedDb)
Write-Host ("peer     = {0}" -f $resolvedPeer)
if ($iniPath -ne "") { Write-Host ("config   = {0}" -f $iniPath) }
Write-Host ("lo       = {0}" -f $resolvedLo)

$exe = Join-Path $dir 'forklocate.exe'
if (-not (Test-Path $exe)) {
    Write-Error "未找到 forklocate.exe;先在 backend 下编译:`n  go build -o cmd/dev_utils/forklocate/forklocate.exe ./cmd/dev_utils/forklocate"
    exit 2
}

Write-Host ""
Write-Host "=== 第 1 步:定位分叉点(只读) ==="
# RPC 模式:节点可保持运行,无需停止、无数据库锁冲突。
$raw = (& $exe -rpc ("{0}:{1}" -f $RpcHost, $RpcPort) -rpcuser $RpcUser -rpcpass $RpcPass `
        -peer $resolvedPeer -lo $resolvedLo 2>&1 | ForEach-Object { $_.ToString() }) -join "`n"
Write-Host $raw

$m = [regex]::Match($raw, 'FORK_POINT=(\d+)')
if (-not $m.Success) {
    Write-Error "未能解析 forklocate 输出中的 FORK_POINT"
    exit 3
}
$fork = [int]$m.Groups[1].Value
Write-Host ""
Write-Host "定位结果:FORK_POINT=$fork"

Write-Host ""
Write-Host "=== 第 2 步:剪块并观察恢复 ==="
$recoverArgs = @('-ForkHeight', $fork)
if ($Config -ne "")   { $recoverArgs += @('-Config', $Config) }
$recoverArgs += @('-RpcHost', $RpcHost, '-RpcPort', $RpcPort, '-RpcUser', $RpcUser, '-RpcPass', $RpcPass)
& (Join-Path $dir 'recover-from-fork.ps1') @recoverArgs