# forklocate —— 本地分叉定位与恢复工具

本地节点因吞下污染/分叉 header 段而把一条**假链**走到 tip 时（典型特征：`blocks == headers`、`getchaintips` 只有单个 `active` tip、`getpeerinfo` 的 `lastblock` 远高于本地、peer 无法从本地 tip 延伸 headers），用本工具找出本地与全网主链的**分叉点**，`invalidateblock` 剪掉假链段，让节点按真实主链重新下载。**分叉点通常在贴近 tip 的几十到几百块内；若定位结果等于 `-lo`（下界），是失败信号，不要按它剪**。

> 背景：44362628、44373628 两次"污染 header 波"事件的完整分析见
> `doc/sync-stall-44362628-analysis-20260913.md`（§10 为本地层残留修复）。

## 组成

| 文件 | 说明 |
|---|---|
| `forklocate.exe` | 已编译可执行文件（12.7MB，已入库；git 例外 `!forklocate.exe` 放行） |
| `main.go` | 源码。双数据源（`-rpc` 推荐 / `-dbpath`），peer 侧以 P2P 拉段对比定位 |
| `locate-fork.ps1` | 定位执行脚本（只读，不修改任何数据） |
| `recover-from-fork.ps1` | 恢复脚本：`invalidateblock` 剪块 → reorg → 采样观察恢复 |
| `fix-fork.ps1` | 一键：定位 → 剪块 → 观察 |
| `README.md` | 本文档 |
| `forklocate-prod.zip` | 生产机传递包（exe + 3 个 ps1，git 忽略不提交；内含本工具全部使用必需文件） |

## 双数据源模式

| 模式 | 用法 | 要求 |
|---|---|---|
| **`-rpc`（推荐）** | `forklocate -rpc <host:port> -rpcpass <pw> -peer <addr>` | 节点可保持运行，经 `getblockchaininfo/getblockhash` 读本地高度→哈希，**无数据库锁冲突** |
| **`-dbpath`** | `forklocate -dbpath <blocks_ffldb> -peer <addr>` | 直读 `blocks_ffldb`，**必须先停止节点**（数据库独占） |

peer 侧统一以 P2P 出站连接参考节点（默认 Umami 参考实现，端口 34230），用 `getheaders` 多点 locator 拉连续 header 段，与本地逐高度对比定位分叉点。

## 快速使用

生产机上（脚本自动读 `runtime.ini` 的 RPC 与 datadir，节点可保持运行）：

```powershell
# 一键：定位 → 剪块 → 观察恢复
powershell -ExecutionPolicy Bypass -File fix-fork.ps1

# 或分开执行
powershell -ExecutionPolicy Bypass -File locate-fork.ps1                    # 只读定位
powershell -ExecutionPolicy Bypass -File recover-from-fork.ps1 -ForkHeight X  # 剪块恢复（X 取自定位输出）
```

本机（开发/调试）：

```powershell
go build -o cmd/dev_utils/forklocate/forklocate.exe ./cmd/dev_utils/forklocate
cmd\dev_utils\forklocate\forklocate.exe `
  -rpc 127.0.0.1:34229 -rpcuser ini -rpcpass ini -peer 24.160.187.156:34230
```

## 输出解读

```
local tip height=44373596  low bound=43760164
connected to 24.160.187.156:34230 (subver="/Umami:25.0.0/")
segment matched up to ..., continuing...        ← 段耗尽续拍（正常，通常 1-2 轮）
FORK_POINT=443736xx                             ← 本地最后与全网一致的高度 X
INVALIDATE_HASH=<hash>                          ← 本地 X+1 的第一个分叉块（供剪块）
NO_INVALIDATE                                   ← 本地 tip 已在主链，无需处理
```

- **正常**：`FORK_POINT` 在 tip 附近（本案例 44373xxx）。
- **失败信号**：`FORK_POINT` 等于 `-lo`（43760164）或有 `segment first header prev ... not in the local locator` / `peer returned no headers` 提示——说明 peer 不配合或 locator 未命中，**严禁按该结果剪块**，换参考 peer 或检查节点 RPC。

## 参数

**locate-fork.ps1 / fix-fork.ps1（自动发现，显式优先）**

| 参数 | 缺省来源 | 说明 |
|---|---|---|
| `-Config` | 自动找 | `runtime.ini` 路径（当前目录 → 脚本旁 → `frontend/bin`） |
| `-RpcHost/-RpcPort/-RpcUser/-RpcPass` | `runtime.ini` | 节点 RPC；用于定位（RPC 模式）与剪块 |
| `-Peer` | `getpeerinfo` 中 `lastblock` 最高的对等点 | 参考节点，用于 P2P 对比；`-Peer` 可写死地址强制指定 |
| `-Lo` | `runtime.ini` 的 `addcheckpoint=高度:哈希` 解析 | 分叉下界（应为全网共识高度） |
| `-DbPath` | `datadir/sugarmainnet/blocks_ffldb` | 仅 `-dbpath` 模式使用（需先停节点） |

**recover-from-fork.ps1**

| 参数 | 缺省 | 说明 |
|---|---|---|
| `-ForkHeight` | 必填 | 分叉点 X（只剪 X+1） |
| `-WatchSeconds/-SampleInterval` | 300 / 15 | 剪块后观察恢复的时长与采样间隔 |

## 恢复原理

1. 定位 X（本地与全网最后一致的高度）；
2. `invalidateblock <本地 X+1 的 hash>` → 节点将该块及其上假链标记无效并 reorg 回退到 X；
3. 节点按真实主链从 X+1 重新下载（浅分叉只损失几十~几百块），块高越过 `X+30` 判定恢复成功。

注意：`invalidateblock` 需要节点 RPC 支持该方法。若节点返回 `-32600 Invalid request: malformed`，是该节点二进制版本问题（非本工具问题），需更新节点二进制后再剪。

## 适用范围 / 不适用

- 适用：`blocks == headers` 的"假链走到 tip"本地分叉（污染/分叉 header 段，分叉点在 tip 附近）。
- **不适用**：`UTXO 一致点超前元数据`（用 `forwardrebuild`）；`sugar index(LevelDB) 损坏`（节点启动自动备份 `index.corrupt` 重建）；需全量重同步的严重损坏。
- 定位为只读，不会改动任何数据；剪块动作由你显式执行（`fix-fork.ps1` 或 `recover-from-fork.ps1`）。

## 参考

- 事件分析：`doc/sync-stall-44362628-analysis-20260913.md`
- 同类只读诊断：`cmd/dev_utils/journalcheck`（停滞高度段 chainstate/status/journal/HasBlock 核查）