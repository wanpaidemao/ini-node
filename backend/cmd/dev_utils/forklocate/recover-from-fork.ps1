<#
.SYNOPSIS
  从本地分叉恢复:把本地假链在分叉点处剪掉,让节点回退并按真实主链重新下载。

.DESCRIPTION
  适用场景:本地节点吞下污染/分叉 header 段,把假链走到 tip(特征:
  blocks==headers、单 active tip、peer 高度远高于本地、peer 无法从本地
  tip 延伸)。此时把本地 "分叉点+1" 的块 invalidateblock 掉,节点即
  reorg 回退到分叉点,并从 peer 重新下载真实链自动追平。

  不适用:UTXO 一致点超前元数据(用 forwardrebuild)、sugar index 损坏
  (启动自动备份重建)。重置/全量重同步均不需要。

  RPC 端口/账号/密码自动从节点 runtime.ini 读取(按序:显式参数 >
  -Config 指定 > 当前目录 runtime.ini > 脚本旁 runtime.ini),读取失败才
  回退默认 ini:ini@127.0.0.1:34229。

.PARAMETER ForkHeight
  必填。分叉点高度 X:本地与全网主链最后一致的高度。本地 X+1 即为
  第一个分叉块(将对该块的 hash 执行 invalidateblock)。
  X 的来源:explorer 对比本地 getblockhash 得到,或由 locate-fork.ps1 定位。

.PARAMETER Config
  节点 runtime.ini 路径(缺省自动发现)。

.PARAMETER RpcHost / RpcPort / RpcUser / RpcPass
  RPC 连接参数,显式给出时优先级最高。

.PARAMETER WatchSeconds / SampleInterval
  剪块后观察恢复的时长与采样间隔。

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File recover-from-fork.ps1 -ForkHeight 44373615
#>
param(
    [Parameter(Mandatory=$true)][int]$ForkHeight,
    [string]$Config = "",
    [string]$RpcHost = "",
    [int]$RpcPort = 0,
    [string]$RpcUser = "",
    [string]$RpcPass = "",
    [int]$WatchSeconds = 300,
    [int]$SampleInterval = 15
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

# ---------- RPC 配置:显式参数 > -Config > 当前目录 runtime.ini > 脚本旁 runtime.ini ----------
$iniPath = ""
if ($Config -ne "") {
    $iniPath = $Config
} else {
    foreach ($cand in @(
            (Join-Path (Get-Location) 'runtime.ini'),
            (Join-Path $PSScriptRoot 'runtime.ini'),
            (Join-Path $PSScriptRoot '..\..\..\..\frontend\bin\runtime.ini')
        )) {
        if (Test-Path $cand) { $iniPath = $cand; break }
    }
}

if ($iniPath -ne "") {
    $ini = Read-Ini $iniPath
    if ($RpcUser -eq "") { $RpcUser = if ($ini['rpcuser']) { $ini['rpcuser'] } else { 'ini' } }
    if ($RpcPass -eq "") { $RpcPass = if ($ini['rpcpass']) { $ini['rpcpass'] } else { 'ini' } }
    if ($RpcHost -eq "" -or $RpcPort -eq 0) {
        $listen = $ini['rpclisten']
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
    if ($RpcHost -ne "" -and $RpcPort -ne 0) {
        Write-Host ("RPC 配置取自: {0}" -f $iniPath)
    }
}
if ($RpcHost -eq "") { $RpcHost = '127.0.0.1' }
if ($RpcPort -eq 0)  { $RpcPort = 34229 }
if ($RpcUser -eq "") { $RpcUser = 'ini' }
if ($RpcPass -eq "") { $RpcPass = 'ini' }

function Rpc([string]$Method, $Params) {
    $body  = @{ jsonrpc = '1.0'; id = 1; method = $Method; params = $Params } |
        ConvertTo-Json -Compress -Depth 6
    $auth  = 'Basic ' + [Convert]::ToBase64String(
        [Text.Encoding]::ASCII.GetBytes("${RpcUser}:${RpcPass}"))
    $r = Invoke-RestMethod `
        -Uri ("http://{0}:{1}" -f $RpcHost, $RpcPort) `
        -Method Post -Body $body -ContentType 'application/json' `
        -Headers @{ Authorization = $auth } -TimeoutSec 30
    if ($null -ne $r.error -and $null -ne $r.error) {
        throw ("RPC error: " + ($r.error | ConvertTo-Json -Compress))
    }
    return $r.result
}

Write-Host ("RPC: {0}:{1} (user={2})" -f $RpcHost, $RpcPort, $RpcUser)
Write-Host "== 0) 检查节点状态 =="
$bci = Rpc 'getblockchaininfo' @()
Write-Host ("blocks=$($bci.blocks) headers=$($bci.headers) tip=$($bci.bestblockhash)")

if ($bci.blocks -le $ForkHeight) {
    Write-Host ("本地已在分叉点($ForkHeight)或之下,跳过剪块。")
} else {
    Write-Host ("== 1) 剪掉本地分叉块(height {0}+1) ==" -f $ForkHeight)
    $invalidHash = Rpc 'getblockhash' @($ForkHeight + 1)
    Write-Host "invalidating $invalidHash  (height $($ForkHeight + 1))"
    Rpc 'invalidateblock' @($invalidHash) | Out-Null
    Write-Host "已发送 invalidateblock,等待 reorg 回退与重连(20s)..."
    Start-Sleep -Seconds 20
    $bci = Rpc 'getblockchaininfo' @()
    Write-Host ("回退后: blocks=$($bci.blocks) headers=$($bci.headers)")
    if ($bci.blocks -gt $ForkHeight + 1) {
        Write-Warning "blocks=$($bci.blocks) 仍高于分叉点+1,reorg 未按预期回退,请人工检查。"
    }
}

Write-Host ("== 2) 观察恢复(最长 {0}s,每 {1}s 采样) ==" -f $WatchSeconds, $SampleInterval)
$deadline = [DateTimeOffset]::UtcNow.AddSeconds($WatchSeconds)
$lastBlocks = -1
$stallCount = 0
do {
    Start-Sleep -Seconds $SampleInterval
    $bci = Rpc 'getblockchaininfo' @()
    $diff = $bci.headers - $bci.blocks
    Write-Host ("blocks={0} headers={1} head-gap={2} tip={3}" -f `
        $bci.blocks, $bci.headers, $diff, $bci.bestblockhash.Substring(0, 12))

    if ($bci.blocks -gt ($ForkHeight + 30)) {
        Write-Host ""
        Write-Host "=== 恢复成功:块高已越过分叉点 30+ 块,节点正沿真实主链追赶。 ==="
        exit 0
    }

    if ($bci.blocks -eq $lastBlocks) {
        $stallCount++
        if ($stallCount -ge 4) {
            Write-Host ""
            Write-Warning "块高连续 $stallCount 次采样无推进(约 $($stallCount*$SampleInterval)s)。"
            Write-Warning "可能原因:分叉点比 $ForkHeight 更深,或 peer 供给不稳定。建议:"
            Write-Warning "  1) 若分叉点更深:用更小的 -ForkHeight 重跑(先用 explorer/forklocate 确认);"
            Write-Warning "  2) 检查 getpeerinfo 的 lastblock(peer 高度应 > 本地)。"
        }
    } else {
        $stallCount = 0
    }
    $lastBlocks = $bci.blocks
} while ([DateTimeOffset]::UtcNow -lt $deadline)

Write-Host ""
Write-Warning "观察超时($WatchSeconds s),仍未恢复到分叉点+30 块。请按上面提示排查后重试。"
exit 1