# gen-patches.ps1 — 生成 patch-queue 补丁存档 / Generate the patch-queue archive
# 说明 / About:
#   把 fork（backend）相对上游 btcd（btcd-ref）的每个"被修改文件"差异，导出为
#   规范化（相对路径、可 git apply 重放）的补丁，按类别分组存入 patch/ 目录。
#   Exports each modified file's diff (fork vs upstream btcd) into normalized,
#   re-appliable patches grouped by category under patch/.
#
# 用法 / Usage:
#   powershell -File script/gen-patches.ps1
#
# 依赖 / Prerequisites:
#   - 上游对照树：d:\dev\AI\btcd-ref（btcd master 快照）
#   - 主仓库：本仓库 backend/
#
# 补丁分类 / Categories（对齐 dev_doc/btcd上游同步迁移方案-20260829.md §三）：
#   01-consensus       共识差异（difficulty/validate/merkle/process/error/mining/txscript/chaincfg）
#   02-perf-sync       性能自研-同步（netsync/chain/blockindex/chainview）
#   03-perf-storage    性能自研-存储（utxocache/ffldb/thresholdstate/checkpoints）
#   04-storage-format  存储/磁盘格式（chainio）
#   05-assembly-rpc    装配/RPC（server/config/btcd/rpcserver/log/signal/version…）
#   06-dormant-wire    休眠子模块修改（wire/blockheader、wire/msgversion）
#   07-misc            项目杂项（.gitignore/README/Dockerfile/sample-conf/go.mod）

$ErrorActionPreference = 'Stop'

# 上游对照树与 fork 根 / upstream reference tree and fork root
$up   = 'd:\dev\AI\btcd-ref'
$fork = 'd:\dev\AI\ini-node\backend'
$patchDir = Join-Path $fork '..\patch'   # 仓库根/patch（backend 上一级）

if (-not (Test-Path $up)) { Write-Error "缺少上游对照树: $up"; exit 1 }
if (-not (Test-Path (Join-Path $fork 'go.mod'))) { Write-Error "缺少 fork: $fork"; exit 1 }

New-Item -ItemType Directory -Force -Path $patchDir | Out-Null

# 类别 → 被修改文件列表（相对 backend/ 的路径）/ category -> modified file list
$categories = [ordered]@{
  '01-consensus'       = @('blockchain/difficulty.go','blockchain/validate.go','blockchain/merkle.go','blockchain/process.go','blockchain/accept.go','blockchain/error.go','blockchain/internal/workmath/difficulty.go','txscript/engine.go','chaincfg/params.go','mining/mining.go','mining/cpuminer/cpuminer.go','blockchain/validate_test.go','chaincfg/genesis_test.go','chaincfg/register_test.go')
  '02-perf-sync'       = @('netsync/manager.go','netsync/interface.go','netsync/manager_test.go','blockchain/chain.go','blockchain/blockindex.go','blockchain/blockindex_test.go','blockchain/chainview.go','blockchain/chainview_test.go','blockchain/common_test.go','blockchain/internal/testhelper/common.go')
  '03-perf-storage'    = @('blockchain/utxocache.go','blockchain/utxocache_test.go','blockchain/thresholdstate.go','blockchain/checkpoints.go','database/ffldb/db.go','database/ffldb/whitebox_test.go','database/interface.go')
  '04-storage-format'  = @('blockchain/chainio.go')
  '05-assembly-rpc'    = @('server.go','config.go','config_test.go','btcd.go','rpcserver.go','rpcserver_test.go','rpcserverhelp.go','rpcadapters.go','log.go','signal.go','service_windows.go','upgrade.go','version.go','cmd/btcctl/version.go','doc.go')
  '06-dormant-wire'    = @('wire/blockheader.go','wire/msgversion.go')
  '07-misc'            = @('.gitignore','README.md','Dockerfile','sample-btcd.conf','go.mod')
}

# 把 git diff 输出里的绝对路径前缀替换为相对路径（可重放）/ strip absolute path prefixes
# git 在 Windows 上输出的头部路径是"反斜杠被双写"的带引号形式，这里用字面替换还原。
# git quotes doubled-backslash paths on Windows; strip them with literal replace.
$upEsc   = ($up   + '\').Replace('\','\\')   # 例/ e.g. d:\\dev\\AI\\btcd-ref\\
$forkEsc = ($fork + '\').Replace('\','\\')   # 例/ e.g. d:\\dev\\AI\\ini-node\\backend\\

function Normalize-PatchLines {
  param([string[]]$Lines)
  return $Lines | ForEach-Object {
    $line = $_
    # 只规范化"路径头部行"（diff --git / --- / +++），正文里的反斜杠不能动
    # only normalize path header lines; never touch backslashes in content
    if ($line.StartsWith('diff --git ') -or $line.StartsWith('--- ') -or $line.StartsWith('+++ ')) {
      # 先剥绝对前缀，再把（git 转义过的）反斜杠路径统一为正斜杠，去掉引号
      # strip absolute prefix, then normalize escaped backslashes to '/', drop quotes
      $line = $line.Replace($upEsc, '').Replace($forkEsc, '')
      $line = $line.Replace('\\', '/').Replace('\', '/').Replace('"', '')
    }
    $line
  }
}

foreach ($cat in $categories.Keys) {
  $collected = @()
  $fileCount = 0
  foreach ($rel in $categories[$cat]) {
    $a = Join-Path $up   ($rel -replace '/','\')
    $b = Join-Path $fork ($rel -replace '/','\')
    if (-not (Test-Path $a)) { Write-Warning "上游缺失(跳过): $rel"; continue }
    if (-not (Test-Path $b)) { Write-Warning "fork 缺失(跳过): $rel"; continue }
    $diff = git diff --no-index --src-prefix=a/ --dst-prefix=b/ $a $b 2>$null
    if ($LASTEXITCODE -ne 0 -and $LASTEXITCODE -ne 1) { continue }  # 1 = 有差异，0/1 都正常
    $collected += Normalize-PatchLines $diff
    $fileCount++
  }
  $outPath = Join-Path $patchDir "$cat.patch"
  [System.IO.File]::WriteAllLines($outPath, $collected, (New-Object System.Text.UTF8Encoding($false)))
  Write-Output ("{0,-24} files={1,-3} lines={2}" -f ($cat + '.patch'), $fileCount, $collected.Count)
}
Write-Output ("补丁目录 / patch dir: " + $patchDir)
