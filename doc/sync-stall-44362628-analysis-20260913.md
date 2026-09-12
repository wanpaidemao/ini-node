# 全网同步停滞事件深度分析报告（44362628 卡块）

- 日期：2026-09-13
- 范围：Sugarchain 主网多节点同步停滞 + sugar index 损坏（EOF）
- 结论等级：已定位根因（全网分叉/污染 header 波 + header-first 同步脱钩），转账块本身无毒
- 关联修复：本仓库 A/B/C 三项修复（见 §7），均已落地待实测

---

## 1. 现象与时间线（2026-09-12 晚 ~ 09-13 凌晨）

| 时间（本地） | 事件 |
|---|---|
| 23:25:27 | 本节点正常出块 `height=44362607`（mined，gen=1218），模板持续刷新 |
| 23:27:06 | **44362628 (be12969b…)** 出块：全网罕见地包含一笔真实转账（tx 89e7a486…，1 进 3 出），本节点在链上正常接收该块 |
| 23:27 之后 | 本地块高停在 44362628 不再推进；headers 继续上涨（44363110 → 44363193） |
| 23:28:16 起 | sugarmaker 反复 `work fetch failed: context deadline exceeded`；GBT 返回 -10 "downloading blocks"（IBD 状态锁死） |
| 23:36:23 | 矿工提交基于本地旧 tip 的块被正确拒绝："header chain has f5920677… at height 44362629" |
| 00:19:56（他节点） | `[WRN] CHAN: Best header state 3fc0f25f… not found in block index, falling back to best chain tip` + `Rebuilding height-to-hash index (height 44363123)` |
| 00:32:48（他节点） | `[WRN] SRVR: Chain init failed with corruption (EOF); backing up corrupted sugar index and retrying with a fresh one`，块索引从 snapshot **44362628** 重新装载 |
| 09-13 复查 | 本节点已恢复：blocks=44363669，headers=44363669，差值 0（追平） |

**全网特征**：同类钱包/节点几乎同时停滞、卡在同一高度区间（44362628±）——同步策略相同，死法相同。

## 2. 证据清单

### 2.1 链上数据（sugar.wtf 浏览器 + 本地 RPC 双重核对）

- **44362628 be12969b…**：2 笔交易，2344 字节（stripped 909），bits `1f00ee4d`
  - coinbase 42e3b3b3…：产出 5.36890673 SUGAR → sugar1q7z0pl4…
  - 转账 89e7a486…：输入 sugar1q7z0pl4… 69.72342546 → 三路输出 47.70532673 + 21.90682159 + 0.11107954
  - 金额核算：输入 − 输出 = **0.00019760 SUGAR 手续费**，三输出合计 69.72322586，账目自洽，无异常
  - 下一块指针 = f5920677…（与 23:36:23 拒块日志中的"header chain has f5920677 at height 44362629"完全一致）
- **本地链核查**：44362607、44362628 单查均正常返回（批量脚本报"缺失 22"为脚本引号 bug，非真实缺失）；44362629..44362648 逐块检查，**全部为空块（tx 数=1）**——即本地链在"该转账块之后"继续正常连接了大量块

### 2.2 本节点 RPC 实测（停滞期间）

| 采样 | 值 |
|---|---|
| blocks | 44362628，25 秒双采样 0 推进 |
| headers | 44363110 → 44363193（10 分钟 +83，仍在涨） |
| peers | 5 个，`currentheight` 全部 = 44363624（当时 44363024 附近），**低于本地 header tip** |
| 网络流量 | 25 秒仅收 2.2 KB（下载管道实际死亡） |
| GBT | 1.2 秒快速失败，code -10 "Bitcoin is downloading blocks..." |
| getchaintips | **12 个分支，其中 2 个 invalid（44362797/44362977），1 个 valid-fork 挂在 44363193（正是本地 header tip 高度）** |

### 2.3 他节点日志（用户提供，字符串在代码中的出处）

| 日志 | 代码出处 | 含义 |
|---|---|---|
| `Best header state 3fc0f25f… not found in block index, falling back to best chain tip` | blockchain/chainio.go（best header state 恢复路径） | header 索引推进到了 3fc0f25f（高高度），但块索引里根本没有这个 header 的块数据 → **header-first 与块数据脱钩的持久化体现** |
| `Rebuilding height-to-hash index for the best header chain (height 44363123)` | blockchain/chainio.go:2129 | 启动时按 header 链重建高度→哈希索引，重建目标 44363123 |
| `Chain init failed with corruption (EOF); backing up corrupted sugar index and retrying` | server.go 启动路径 | **sugar index（LevelDB）文件 EOF 损坏**，自动备份为 index.corrupt 并重建 |
| `Loading block index from snapshot (height 44362628)` | chainio.go | 块索引快照停在 44362628 —— 与全网停滞高度吻合 |

**本地旁证**：`data/sugarmainnet/index.corrupt` 同样存在（本节点 sugar index 也发生过后台自愈重建）。

## 3. 根因分析（第一性原理）

### 3.1 公理与推导

**公理 0**：共识需要完整块数据；header 不是进度。header-first 同步的正确形态是"header 探路，块跟进"，且两者**脱钩后必须有回收机制**。

**推导链**：

```
① 触发源：全网分叉/污染 header 波（外因）
   getchaintips 显示 12 分支、2 invalid；多节点同时吞下同一批"长得合法"
   的 header。44362628 前后全网算力分散，同一高度出现多个竞争块
   （f5920677 等），header 链在每个节点上各自"领先"。

② header 无界推进（内因 1）
   ~5 秒/块的出块速度让 header 持续增长；块供给受 peer 实际高度限制。
   本地 header tip (44363110+) 超过所有 peer 的 currentheight ——
   超出部分的请求窗口没有任何 peer 能服务。

③ 请求窗口越过"可服务边界" → 管道死锁（内因 2）
   front 窗口卡在 >peer 最高高度 → 无 peer 能填 front → 无块到达 →
   补窗逻辑（块到达触发）永不运行 → 零流量死锁。

④ 三重自救机制集体失效（内因 3）
   - stall 轮换 syncPeer：新 peer 高度同样不够 → 白换
   - 90s 回滚护栏（D 修复）：fetchHigherPeers()=0 → 跳过回滚
     （护栏本意防误回滚，但把唯一"重切窗口"的自救也挡掉了）
   - 块到达补窗：0 块到达 → 永不触发

⑤ IBD 状态锁死 → 下游症状
   blocks 追不上 header tip → ibdMode=true → GBT -10 →
   sugarmaker "work fetch failed"；矿工基于旧 tip 挖出的块注定孤儿，
   提交被守卫按设计拒绝（"not on the network main chain"）。

⑥ 持久化损伤（他节点死法）
   长时间停滞中 header 索引/块索引/sugar index 的写入序列被打断
   （反复回滚、重连、进程重启），sugar index LevelDB 出现 EOF 损坏；
   启动自愈触发：备份 index.corrupt → 重建 → 从块索引快照 44362628
   重新装载。块索引快照停在 44362628 = 全网块供给的"公共水面"。
```

### 3.2 结论一句话

**根因不是 44362628 那笔转账，而是：全网算力在该高度附近分叉（chaintips 12 分支/2 invalid），header 链冲到所有 peer 块供给之上，请求窗口越过"可服务边界"后块管道死锁；本地三重自救机制全部以"peer 比我高"为前提，在此场景集体失效。** 转账块只是分叉波的"交汇点/地标"，不是毒性来源。

## 4. "一个块 ≥2 笔交易就卡住"假设的代码验证（用户点名核查）

对 sugarindex 写入路径逐行核查（backend/sugarindex/indexer.go:399-473）：

- `ConnectBlock → connectBlock → connectBlockBatch`：对 `block.Transactions()` **线性遍历**，txid 索引、spend journal（stxos 数组与 txIdx/inIdx 严格顺序对齐）、输出索引全部在同一 `leveldb.Batch` 内组装后**一次原子写入**
- coinbase（txIdx 0）正确跳过 spend journal；多交易时只是循环次数变多、batch 变大，**没有任何分支、锁、异步等待会造成"第 2 笔交易卡死"**
- A3 解耦刷写路径（chain.go:983 `writeQueue.enqueue`）：块体同步落盘、索引元数据异步入队，与交易笔数无关
- **本地实证**：历史上有交易块正常上链（如 60 块样本中含交易块正常连接）；44362628 本身（2 笔交易）在本节点**正常连接并成为主链一部分**

**判定：该假设不成立。** 多交易块不触发卡死；真正的卡死机制见 §3。但该假设指向的块确实是分叉波的地标——用户观察到"卡在这一块"是**相关**（同一时间）而非**因果**。

## 5. 为什么其他节点死得不一样（EOF vs 停滞）

| 节点类型 | 死法 | 机理 |
|---|---|---|
| 本节点（btcd 系） | 管道死锁、GBT -10 | header-first 窗口越过可服务边界（§3 ③④），内存态卡死，未损盘 |
| 他节点（INODE 侧） | sugar index EOF → 自愈重建 | 同一 header 波 + 停滞期间反复回滚/重启，索引写序列被打断 → LevelDB EOF；启动自愈备份重建，从 44362628 快照恢复 |

两者是**同一根因的两种死相**：一个卡在内存态（同步管理器），一个卡在持久态（索引文件）。

## 6. 风险残留

1. **分叉波源头未查明**：chaintips 里 2 个 invalid 分支（44362797/44362977）说明有人广播了无效块/污染 header。若再次发生，未修复节点会再次集体卡死
2. **sugar index EOF 的具体损坏点未复现**：推断为停滞期写序列中断，但未拿到损坏文件做字节级分析（index.corrupt 在本地与远端均有备份，可留档）
3. A/B/C 修复尚未在停滞场景实测（需下一次全网扰动或受控复现）

## 7. 已实施修复（本仓库 backend/netsync/manager.go）

| 修复 | 内容 | 针对环节 |
|---|---|---|
| **A 可服务边界钳制** | `buildBlockRequest` 请求上限钳制到 `maxPeerServeHeight()`（新增辅助函数：所有候选 peer 的最大 LastBlock）；peer 高度上涨后边界自动延伸 | §3 ②③ 根治 |
| **B 窗口重置** | 块下载停滞超时分支：若 `maxPeerServeHeight() <= best`（无人能供更高块）→ `resetDownloadState()` 重启下载窗口（不回滚链）；仅当有 peer 能供块却持续不来才走原 `rollbackFabricatedHeaderChain` | §3 ④ 根治 |
| **C header 失控刹车** | 新增 `headerRunawayThreshold=100`：header tip 领先块 tip 超 100 时 `fetchHeaders` 拒绝新一轮拉取；缺口收窄后自解除 | §3 ② 预防 |

## 8. 后续建议

1. **短期**：重启各停滞节点即可恢复（本节点已自行恢复至 44363669）；编译新版 btcd.exe 部署 A/B/C
2. **中期**：对他节点（IINI-NODE）移植同样的边界钳制/窗口重置逻辑；保留 index.corrupt 备份做字节级归档
3. **长期**：排查 invalid 分支广播源（44362797/44362977 的构造者）；评估 header-first 同步增加"块进度门控"作为协议级约定

## 9. 本地代码改动清单（两笔，互不相关）

本次本地工作区同时存在两批独立改动，**分属两条线**：

### 第一笔：前端 RPC / 节点连接改造（已提交，4 个 commit）

| Commit | 内容 |
|---|---|
| `c06e9fd3` | 入站动态让渡（connmgr 硬上限 + handleAddPeerMsg 动态分配/出站优先驱逐/白名单保护）+ 新建 PeerStatus.svelte 节点连接页 + 控制台命令 18→37 + 9 语言 i18n |
| `531a1424` | 控制台节点连接卡片整体移除（由 PeerStatus 页承接），冗余脚本清理 |
| `321d1893` | getPeers 延迟微秒→毫秒换算修复（services.ts ÷1000）+ PeerStatus 页样式补齐 |
| `a517e140` | 控制台输入区终端式单行化（合并命令/参数双框，Enter 执行 + ↑↓ 历史） |

- 涉及文件：`frontend/frontend/src/**`、`backend/server.go`（入站动态让渡）
- 状态：**已本地提交**，待用户实测后推送远程

### 第二笔：本次同步停滞修复 A/B/C（未提交，工作区改动）

| 修复 | 位置 | 状态 |
|---|---|---|
| A 可服务边界钳制 + `maxPeerServeHeight()` | `backend/netsync/manager.go` buildBlockRequest | **工作区未提交** |
| B 窗口重置（停滞分流：不可服务→重置，可服务→回滚） | `backend/netsync/manager.go` blockUnavailableTimeout 分支 | **工作区未提交** |
| C header 失控刹车（`headerRunawayThreshold=100`） | `backend/netsync/manager.go` fetchHeaders + 常量区 | **工作区未提交** |

- 状态：代码已落地但**尚未编译验证、尚未提交**（`git status`: `M backend/netsync/manager.go`）
- 两笔改动无文件交叠冲突：第一笔动 `server.go` 的入站连接逻辑（已提交），第二笔只动 `netsync/manager.go`（未提交）
