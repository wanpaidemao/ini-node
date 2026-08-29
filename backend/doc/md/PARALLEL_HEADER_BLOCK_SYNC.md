# 并行 Header/Block 同步（Pipelined Initial Sync）方案

> ⚠️ 核对状态（2026-08-30）：本文行号引用已过期（`server.go:3248` → 实际 3378），请以代码为准；并行同步机制已实现。
> ⚠️ Audit (2026-08-30): line refs stale; parallel sync implemented.

> 目标：header 并行下载期间，一旦已应用的 header 高度领先已连接 block 高度达到阈值，
> 立即用同一批 peer 同时启动并行 block 下载；header 下载不拆除，两边同时推进，
> header 全部就位后再由 block 单独追赶至同步。总 IBD 时长从 `T_header + T_block`
> 收敛到接近 `T_block`（block 是 header 的千倍数据量，收益最大）。

---

## 1. 现状（改造前的状态机）

- 阶段 1（纯 header）：`startSync → fetchHeaders` 建立 `headerSyncState`，最多
  `maxHeaderSyncPeers=8` 个 peer 并行分片下载；`processReadyHeaderRanges` 按序应用
  （IBD 下 `BFNoPoWCheck`）并批量落盘（`headerFlushBatchSize=20000` +
  `debug.FreeOSMemory`），逐批按 `--headerwindow` evict 内存索引。
- 交接：`finishHeaderSync()` 在 `hs.nextHeight > hs.target`（header 追平最高参与 peer）
  时拆掉 `headerSync`，用同一批 peer 建立 `blockSyncState`，启动并行 block 下载。
- 阶段 2（纯 block）：`assignBlockSlice` 给每个 peer 发放互不重叠的连续切片；
  `buildBlockRequest` 逐高度用 `HeaderHashByHeight` 取 hash 发 getdata；
  block 按序 `ProcessBlock` 连接。请求边界始终被钳制在
  `requestEnd = min(bestHeight + maxBlockRequestWindow(8192), bestHeaderHeight)`。

**两个关键既有事实，是本次方案安全性的地基：**

1. header 与 block 消息都进 `msgChan`，由 `blockHandler` 单 goroutine 串行处理——
   并发模式不引入任何新的并发访问。
2. block 请求边界天然被 `bestHeaderHeight` 钳制——**只要 header 领先，block 下载
   物理上不可能越过 header 前沿**，不存在"block 下到没有 header 的高度"。

---

## 2. 方案要点

### 2.1 新增配置 `--blocksyncstartlead`（默认 20000）

- 触发阈值：`bestHeaderHeight - bestBlockHeight >= blocksyncstartlead` 时，
  在 header 应用过程中顺带启动并行 block 下载。
- 保底领先守卫（防御性）：`assignBlockSlice` 额外要求
  `bestHeaderHeight - bestHeight >= maxBlockRequestWindow(8192)` 才发放新切片；
  领先不足时暂停发放新切片、只让已 in-flight 的 block 落盘，优先保护 header 前沿
  不被 block 处理（同一 goroutine 上的重活）挤垮。
- 建议约束：`--headerwindow > blocksyncstartlead + maxBlockRequestWindow`，
  保证 block 连接期需要的节点基本都在内存窗口内，极少触发冷读；
  若窗口更小，既有 `coldread.go` 兜底（正确但慢）。文档注明。

### 2.2 抽取共享启动函数 `startParallelBlockDownload(peers, target)`

把 `finishHeaderSync` 中"建立 `blockSyncState` + 设 `syncPeer` + `fetchHeaderBlocks`
+ `reconnectStoredBlocks`"提炼为公共函数，两个入口复用：

- `finishHeaderSync()`：header 就位。若 `blockSyncState == nil` 调用之；
  若并发模式早已启动（`blockSyncState != nil`）则**不再重建**，仅拆除 `headerSync`、
  保留现有 block 下载（in-flight 切片不能丢）。
- `maybeStartBlockSync()`（新）：header 应用中领先达标后首次调用，**不拆除
  `headerSync`**，`blockSync` 集合 = `headerSync.peers`（同一批 peer）。

每个参与 peer 同时背一个 header range + 一个 block slice，wire 层天然支持同一
连接上 getheaders/getdata 交错传输。

### 2.3 触发点

- `processReadyHeaderRanges`：每应用完一段 range 后，若
  `blockSyncState == nil && headerSync != nil && lead >= startLead` → `maybeStartBlockSync()`。
- 单 peer 路径 `handleHeadersMsg`：应用 header 后同样检查（未走并行分片的场景）。
- 启动后 header 继续并行分片 + 批量落盘（既有逻辑零改动）。

### 2.4 Peer 生命周期协调（双向，补齐对称性）

- `handleDonePeerMsg` 已分别处理两侧（`dropHeaderPeer` + `releaseBlockSlice`），
  并发模式下天然正确——同 peer 掉线时其 header range 与 block slice 各自被释放重发。
- `dropHeaderPeer`（主动剔除：空响应/stale）：补充一行 block 侧清理
  （`releaseBlockSlice` + 从 `sm.blockSync` 移除），保证被 header 剔除的 peer
  也不会继续占着 block 切片。
- `handleNewPeerMsg` 已分别 `headerSyncAddPeer` + `blockSyncAddPeer`，并发模式下
  新 peer 自动同时加入两侧并各拿一个 range + 一个 slice，无需改动。

### 2.5 其它接线

- `reconnectStoredBlocks`：`maybeStartBlockSync` 里也调用一次，重启后已落盘 block
  先接上（与 `finishHeaderSync` 语义一致）。
- `syncStatusSnapshot`：`SyncStatus` 字段已同时承载
  HeaderTarget/HeaderNextAssign/BlockTarget/BlockNextAssign/每 peer 的
  slice+range，无需结构改动；可选加一个 `Overlap` 布尔字段供前端标注
  "header 与 block 并行阶段"（低优先级）。
- IBD 标志与 checkpoint：`fetchHeaders` 已置 `ibdMode=true`；block 侧 `checkHeadersList`
  按 checkpoint 用 `BFNoPoWCheck`；`handleBlockMsg` 的 caught-up 判定
  `bmsg.block.Height() >= lastHeight` 只在 header 就位后触发。并发期不会误判。

---

## 3. 安全性论证

- **Block 永不超出 header**：`assignBlockSlice` / `buildBlockRequest` 的
  `requestEnd = min(bestHeight+8192, bestHeaderHeight)` 硬边界不变 ⇒ 被请求的每个
  block 的 header 必已应用，`HeaderHashByHeight(h)` 必成功。
- **顺序连接不变**：block 从连接 tip 连续发放，前切片不完成不发后切片；孤儿池被
  `maxBlockRequestWindow` + 切片边界约束，与现状同界。
- **无新增并发**：所有新状态仍在 `blockHandler` 线程触碰，不破坏既有
  "仅在 blockHandler 线程访问" 不变量。
- **信任模型不变**：header 阶段 IBD 跳过 PoW，block 阶段依赖 checkpoint + header
  hash 链，并发不改写任何校验路径。

---

## 4. 性能 / 资源分析

| 维度 | 说明 |
|------|------|
| 总时长 | 现状 `T_h + T_b`；并发 ≈ `T_b` + 追赶 `startLead` 的小段，IBD 收益最大 |
| CPU | header ~µs/个、block ~ms/个；同一 goroutine，领先阈值 + 保底守卫吸收 block 突发 |
| 网络 | 复用同一批 ≤8 peer，每 peer 同时传 header 分片 + block 切片，无新增连接 |
| 内存 | header 窗口 ≤ `--headerwindow`；block 请求 ≤ 8192 + 孤儿池，均有界 |
| 磁盘 | header 追加 index ffldb（批量 flush + FreeOSMemory）；block 写 block 文件 + UTXO 5min flush，区域不同、无新增锁 |

---

## 5. 实施步骤

1. `config.go`：加 `BlockSyncStartLead int32` + `--blocksyncstartlead`（默认 20000），
   经 `server.go:3248 netsync.New(&netsync.Config{...})` 传入。
2. `netsync/manager.go`：
   - 从 `finishHeaderSync` 提炼 `startParallelBlockDownload`；`finishHeaderSync`
     改为复用，`blockSyncState != nil` 时跳过重建。
   - 新增 `maybeStartBlockSync()`；在 `processReadyHeaderRanges`、`handleHeadersMsg`
     尾部调用。
   - `assignBlockSlice` 加保底领先守卫（lead < 8192 暂停发新切片）。
   - `dropHeaderPeer` 补 block 侧清理。
3. 单测（`netsync/manager_test.go`）：
   - `TestParallelHeaderBlockOverlap`：header 下载中领先达标后 blockSyncState 建立、
     slice 发放、headerSync 存活、两边推进；header 完成后 blockSync 继续、不重建。
   - `TestConcurrentSyncPeerDrop`：同 peer 持 range+slice，掉线后两侧释放并重分配。
   - `TestBlockSliceCappedByHeaderLead`：并发期 assignBlockSlice 永不越过
     bestHeaderHeight（含领先不足）。
   - 现有 `TestParallelHeaderSync*` 全量回归。
4. 冒烟：临时 datadir 主网 `--headerwindow=50000 --blocksyncstartlead=20000`，
   观察 header 与 block 同时推进、RSS 有界、verifFail/stale=0、崩溃=0、stderr=0。
