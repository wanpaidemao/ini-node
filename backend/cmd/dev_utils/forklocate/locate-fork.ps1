<#
.SYNOPSIS
  定位本地与全网主链的分叉点(只读,不修改任何数据)。

.DESCRIPTION
  用 forklocate.exe 以 P2P 出站身份连接参考节点,对 [分叉下界, 本地 tip]
  二分探测,输出:
    FORK_POINT       最后与全网一致的高度 X
    INVALIDATE_HASH  本地 X+1 的块 hash(恢复脚本将 invalidate 它)

  路径/参数自动发现,不写死:
    - DbPath : 缺省从节点 runtime.ini 的 datadir 推导
               (<datadir>/sugarmainnet/blocks_ffldb)
    - Peer   : 缺省用节点 RPC getpeerinfo 中 lastblock 最高的对等点
    - Lo     : 缺省从 runtime.ini 的 addcheckpoint=高度:哈希 解析高度
  显式参数优先级最高。

.PARAMETER DbPath
  blocks_ffldb 目录。缺省自动推导。

.PARAMETER Peer
  参考节点 host:port。缺省从运行节点 RPC 自动选取。

.PARAMETER Lo
  分叉下界。缺省取 addcheckpoint 高度。

.PARAMETER Config
  节点 runtime.ini 路径(缺省自动发现)。

.PARAMETER RpcHost / RpcPort / RpcUser / RpcPass
  用于自动选取 Peer;显式给出时优先。

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File locate-fork.ps1
  powershell -ExecutionPolicy Bypass -File locate-fork.ps1 -DbPath D:\data\blocks_ffldb -Peer 89.117.38.140:34230
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

# ---------- RPC 配置(仅用于取 peer) ----------
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
# 1) 块库路径(仅 -DbPath 显式指定且不存在时失败;RPC 模式不需要块库)
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

# 2) 参考节点:显式 > RPC getpeerinfo 中 lastblock 最高者 > 固定缺省
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

# 3) 分叉下界:显式 > addcheckpoint 高度 > 固定缺省
$resolvedLo = $Lo
if ($resolvedLo -eq 0) {
    $cp = $ini['addcheckpoint']
    if ($cp -match '^(\d+):') { $resolvedLo = [int]$matches[1] }
}
if ($resolvedLo -eq 0) { $resolvedLo = 43760164 }

Write-Host ("dbpath   = {0}" -f $resolvedDb)
Write-Host ("peer     = {0}" -f $resolvedPeer)
if ($iniPath -ne "") { Write-Host ("config   = {0}" -f $iniPath) }
Write-Host ("lo       = {0}" -f $resolvedLo)

$exe = Join-Path $PSScriptRoot 'forklocate.exe'
if (-not (Test-Path $exe)) {
    Write-Error "未找到 $exe,请先编译:`n  go build -o cmd/dev_utils/forklocate/forklocate.exe ./cmd/dev_utils/forklocate"
    exit 2
}

Write-Host ""
Write-Host "== forklocate 定位分叉点 =="
# RPC 模式:节点可保持运行,无需停止、无数据库锁冲突。
& $exe -rpc ("{0}:{1}" -f $RpcHost, $RpcPort) -rpcuser $RpcUser -rpcpass $RpcPass `
    -peer $resolvedPeer -lo $resolvedLo
exit $LASTEXITCODE