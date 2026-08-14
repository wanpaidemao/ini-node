# 启动 sugard 节点(btcd)
# 用法: .\05-run.ps1 [-ExePath <path>] [-IniFile <path>]
# 默认从项目根构建二进制并设参启动; 已运行则仅提示。

param(
    [string]$ExePath = "",
    [string]$IniFile = "$PSScriptRoot\..\btcd-runtime.ini"
)

$ErrorActionPreference = 'Stop'

# 1) 定位二进制
if (-not $ExePath) {
    $built = Join-Path $PSScriptRoot "..\btcd.exe"
    if (Test-Path $built) { $ExePath = $built }
    else {
        $tmp = "C:\Users\adest\AppData\Local\Temp\opencode\btcd-new.exe"
        if (Test-Path $tmp) { $ExePath = $tmp }
        else { throw "找不到 btcd 二进制, 请用 -ExePath 指定" }
    }
}
if (-not (Test-Path $ExePath)) { throw "二进制不存在: $ExePath" }

# 2) 检查 ini 配置文件(RPC 账号密码等参数统一放这里, 不再走命令行)
if (-not (Test-Path $IniFile)) { throw "config file not found: $IniFile" }

# 3) 重复运行则跳过
$procName = (Split-Path $ExePath -Leaf).Replace('.exe', '')
if (Get-Process -Name $procName -ErrorAction SilentlyContinue) {
    Write-Host "检测到节点已在运行, 跳过启动。" -ForegroundColor Yellow
    exit 0
}

# 4) 日志文件
$logDir = Join-Path $PSScriptRoot "..\logs"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
$outFile = Join-Path $logDir "node.stdout.log"
$errFile = Join-Path $logDir "node.stderr.log"

# 5) 启动
# 注意: sugarindex=1 显式传入, 开启地址索引(供 getaddressbalance/
# getaddressutxos 使用), 首次会全量重建(约 7.2 万块/分钟, 全程约 8h)。
$args = @(
    "--configfile=$IniFile",
    "--sugarindex=1"
)
Start-Process -FilePath $ExePath -ArgumentList $args `
    -RedirectStandardOutput $outFile -RedirectStandardError $errFile `
    -WindowStyle Hidden
Write-Host "已启动节点: $ExePath" -ForegroundColor Green
Write-Host "日志: $outFile / $errFile"