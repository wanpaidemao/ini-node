# 区块同步（Block Sync）逻辑详解

> ⚠️ 核对状态（2026-08-30）：本文**行号引用已过期**——代码经窗口化/冷读/并行同步大改后行号偏移 30~330 行，请**以代码为准**；关键数值已修正（`maxBlockRequestWindow=8000`、`headerFlushBatchSize=20000`）。
> ⚠️ Audit (2026-08-30): line refs are stale after heavy code evolution; verify against code. Key values fixed.

> Sugarchain Go 节点（btcd fork）的完整区块同步机制：下载依据、数据结构、
> 线格式、存储布局、并行下载。所有代码引用基于当前 `master`。
> 关键文件：`netsync/manager.go`、`blockchain/chain.go`、`blockchain/chainio.go`、
> `blockchain/blockindex.go`、`blockchain/utxocache.go`、`wire/`.

---

## 目录

- [1. 总体流程](#1-总体流程)
- [2. 下载依据：高度还是哈希？](#2-下载依据高度还是哈希)
- [3. 区块数据结构](#3-区块数据结构)
- [4. 网络消息格式](#4-网络消息格式)
- [5. 区块下载流程（单 peer 视角）](#5-区块下载流程单-peer-视角)
- [6. 并行下载逻辑](#6-并行下载逻辑)
- [7. 区块与索引存储](#7-区块与索引存储)
- [8. Header 窗口与冷读](#8-header-窗口与冷读)
- [9. UTXO 一致性重建](#9-utxo-一致性重建)
- [10. 关键参数速查表](#10-关键参数速查表)

---

## 1. 总体流程

节点从对等节点（peer）获取整条链，分两个阶段：

```
阶段一：header 同步
  请求方 getheaders(locator, stop)  ──►  对端 headers(2000 个/批)
  节点校验 PoW / 难度，把 header 逐个加入内存索引
  目的：快速定位分叉点，拿到"要下载哪些区块"的完整高度范围

阶段二：block 同步（IBD）
  请求方 getdata([block hash]...)      ──►  对端 block(区块字节)
  节点校验并连接区块，更新 UTXO / 主链状态 / 区块索引
  目的：把每个高度对应的区块本体下载并验证、落地
```

两条链独立推进，都记录独立 tip：

| 概念 | 含义 | 存储 |
|---|---|---|
| best chain（主链） | 已**验证并连接**的区块 | `chainstate` key |
| best header | 已**接受 header**（未验证区块本体）的最顶端 | `headerchainstate` key |

节点落后于网络时进入 **IBD（Initial Block Download）**；追上后退出 IBD，
转为靠 peer 的 `inv` 公告增量同步新区块。

**IBD 判定**（`manager.go:350-358`）：`chain.IsCurrent()`（主链高度 ≥ 全网络高度）
且没有更高 peer 时退出 IBD。

---

## 2. 下载依据：高度还是哈希？

**结论：P2P 层全部按「哈希」请求；「高度」只存在于本地，从 headers 条数/区块头
推导，从不通过线缆传输。**

| 消息 | 携带内容 | 是否含高度 |
|---|---|---|
| `getheaders` / `getblocks` | `BlockLocatorHashes[]` + `HashStop` | ❌ 只有哈希 |
| `headers` | 完整 80 字节 `BlockHeader` 列表 | ❌ 区块头里无高度字段 |
| `getdata` | `InvVect{Type, Hash}` 列表 | ❌ 只有哈希 |
| `inv` | `InvVect{Type, Hash}` 列表 | ❌ 只有哈希 |
| `block` | 完整区块字节（header + txs） | ❌ 区块本身无高度 |

具体原因：

1. **区块头只有 6 个字段**（Version/PrevBlock/MerkleRoot/Timestamp/Bits/Nonce），
   **没有高度字段**。高度是通过"从创世块逐块往后数"得到的推论。
2. **定位分叉点用哈希定位符**（block locator）：请求方把最近若干个已知区块哈希
   发给对端，对端在这些哈希里找共同祖先，从共同祖先之后开始回区块。
3. **请求区块本体用 `getdata(block_hash)`**：节点从 header 链知道每个高度对应
   哪个哈希，构造 `getdata` 把"待下载高度的哈希列表"发给对端。
4. 唯一的例外是本地数据库：本地按 `height → hash → block` 两级索引存储，
   **高度索引是本地加速结构，不属于线协议**。

一句话：**线缆上只有哈希，高度是本地用 headers 数量 + 本地主链推算出来的。**

### 高度是如何"长出来"的

- header 阶段：收到 `headers` 消息（最多 2000 个），逐个校验后，
  `headerNode.height = prev.height + 1`，即**由条数累计**。
- block 阶段：`block.Height()` 通过 `blockindex` 里节点父子关系得到（`SetHeight`），
  或用 coinbase 脚本里编码的高度（Sugarchain coinbase 高度标签）兜底
  （`handleBlockMsg` 里 `ExtractCoinbaseHeight`）。

---

## 3. 区块数据结构

### 3.1 区块头 `BlockHeader`（`wire/blockheader.go:21-40`，80 字节，全小端）

| 字段 | Go 类型 | 字节 | 含义 |
|---|---|---|---|
| `Version` | `int32` | 4 | 区块版本（≠ 协议版本） |
| `PrevBlock` | `chainhash.Hash` | 32 | 前一个区块头哈希（父指针） |
| `MerkleRoot` | `chainhash.Hash` | 32 | 本块全部交易的 Merkle 根 |
| `Timestamp` | `time.Time` | 4 | 出块时间（线上 uint32） |
| `Bits` | `uint32` | 4 | 难度目标（紧凑表示） |
| `Nonce` | `uint32` | 4 | 挖矿随机数 |

序列化顺序：`Version → PrevBlock → MerkleRoot → Timestamp → Bits → Nonce`。

### 3.2 区块 `MsgBlock`（`wire/msgblock.go:43-46`）

```go
type MsgBlock struct {
    Header       BlockHeader   // 80 字节
    Transactions []*MsgTx      // 交易列表
}
```

序列化顺序：`[BlockHeader 80B] [varint 交易数] [Tx1][Tx2]...[TxN]`。

- `BlockHash()` = **SHA256d(80B header)**，区块的唯一标识（链的连接用这个哈希）。
- 工作量证明哈希 = **yespower 1.0(80B header)**，与 BlockHash 无关（`pow/pow.go`）。

### 3.3 交易 `MsgTx`（`wire/msgtx.go:364-369`）

```go
type MsgTx struct {
    Version  int32
    TxIn     []*TxIn    // 输入
    TxOut    []*TxOut   // 输出
    LockTime uint32
}
```

- `TxIn`：`PreviousOutPoint{Hash,Index}`(36B) + `SignatureScript` + `Sequence`(4B)
- `TxOut`：`Value`(8B) + `PkScript`
- 序列化：`Version → [0x00 0x01 若带 witness] → varint(输入数) → 各输入 →
  varint(输出数) → 各输出 → [witness] → LockTime`
- Sugarchain 不支持 SegWit，区块/交易无 witness 数据。

---

## 4. 网络消息格式

所有 P2P 消息共享 24 字节消息头：

```
magic(4) | command(12) | length(4) | checksum(4) | payload
```

关键消息负载：

| 消息 | 结构 | 上限 |
|---|---|---|
| `getheaders` | `ProtocolVersion(4)` + `varint(n)` + `n×hash(32)` + `HashStop(32)` | 500 个定位符 |
| `headers` | `varint(n)` + `n×(80B header + varint(0))` | 2,000 个 |
| `getdata` / `inv` | `varint(n)` + `n×InvVect(4+32=36B)` | 50,000 个 |
| `block` | 完整区块字节 | 4,000,000 B（~4MB） |

`InvVect`（`wire/invvect.go:64-67`）：

```go
type InvVect struct {
    Type InvType        // uint32
    Hash chainhash.Hash // 32B
}
```

`InvTypeBlock = 2`、`InvTypeTx = 1`；Sugarchain 网络只发 `InvTypeBlock`。

尺寸上限常量（`wire/`）：

| 常量 | 值 |
|---|---|
| `MaxMessagePayload` | 32 MiB（磁盘/RPC/网络通用序列化上限） |
| `MaxProtocolMessageLength` | 4,000,000 B（P2P 单消息上限） |
| `MaxBlockPayload` | 4,000,000 B |
| `MaxInvPerMsg` | 50,000 |
| `MaxBlockHeadersPerMsg` | 2,000 |
| `MaxBlockLocatorsPerMsg` | 500 |

**VarInt 编码**：`<253` → 1B；`≤0xffff` → `0xfd`+2B；`≤0xffffffff` → `0xfe`+4B；
否则 `0xff`+8B。

---

## 5. 区块下载流程（单 peer 视角）

> 并行场景见下一节；先讲单 peer 的完整数据流。

### 5.1 启动 IBD（`startSync`，`manager.go:585-658`）

1. `fetchHigherPeers(bestHeaderHeight)` 找出比我们 header 高的 peer。
2. 有更高 header 的 peer → 阶段一 `fetchHeaders()`（header 下载）。
3. header 追平后 → 阶段二：选 `bestPeer` 为 `syncPeer`，设 `ibdMode=true`，
   构造 `blockSync` 参与 peer 集合，调 `reconnectStoredBlocks()` 先把上次
   会话落盘未连接的块连上。

### 5.2 header 下载（`getheaders → headers`）

- `fetchHeaders`（`manager.go:376-444`）：打乱 peer、按 host 去重、封顶
  `maxHeaderSyncPeers=8`；`target = 最高 peer 高度`；每个 peer 分配一个
  `headerRange` 并 `PushGetHeadersMsg(locator, zeroHash)`。
- `headerLocator`（`manager.go:450-459`）：用 `HeaderHashByHeight(start-1)` 构造
  单哈希定位符 → 对端从该高度之后开始返回。
- `handleHeadersMsg`（`manager.go:1555-1628`）：`ProcessBlockHeader` 逐个接受
  header（IBD 期 `BFNoPoWCheck` 跳过 PoW 校验提速，`manager.go:1583-1586`）。

### 5.3 block 下载（`getdata → block`）

- `finishHeaderSync`（`manager.go:1753-1813`）把 header 阶段移交到 block 阶段：
  - `sliceLen = maxBlockRequestWindow / len(blockSync)`（每 peer 的高度跨度）
  - 初始化 `blockSyncState{nextAssign, target, peerSlice, sliceLen}`
  - 对每个参与 peer 调 `fetchHeaderBlocks(p)`。
- `fetchHeaderBlocks`（`manager.go:1319-1329`）→ `buildBlockRequest(peer)`：
  构造 `MsgGetData`（全是 `InvTypeBlock`），`peer.QueueMessage(gdmsg)`。

### 5.4 收到区块（`handleBlockMsg`，`manager.go:1127-1315`）

1. 校验 peer 状态存在；若该块不在 `state.requestedBlocks` 中且非 regtest，
   视为"未请求块"断开 peer（lines 1135-1149）。
2. 从请求表删除该 hash（`requestedBlocks`，全局 + 该 peer）。
3. **`chain.ProcessBlock(block, flags)`**（line 1164）：
   - `blockExists` 查重
   - `checkBlockSanity`（结构/大小/共识规则）
   - prev 不在主链 → `addOrphanBlock` 进孤儿池，返回 `isOrphan=true`
   - 否则 `maybeAcceptBlock` → `connectBestChain` → `connectBlock`
4. 孤儿分支（lines 1202-1230）：`PushGetBlocksMsg(locator, orphanRoot)`
   回头请求父块。
5. 未到 best header 高度 → **`blkDownload()` 补量**（lines 1301-1307）。
   达到 → `ibdMode=false`，退出 IBD，转入 `inv` 公告增量模式。

### 5.5 下载窗口（防孤儿风暴）

三个核心常量（`manager.go:24-78`）：

| 常量 | 值 | 作用 |
|---|---|---|
| `maxBlockRequestWindow` | **8000** | 请求地平线不超过主链 tip 前方 8000 高度 |
| `minInFlightBlocks` | **10** | peer 在途请求数 < 10 时才补量 |
| `maxRequestedBlocks` | 50,000 | 全局在途请求表上限（超限随机驱逐） |

**为什么要有窗口**：如果一次把剩余整条 header 链（数千万高度）全部请求，
并行下载会把区块分散到数万高度之外，孤儿池被灌满、低高度块被驱逐、连接卡死。
窗口把请求限制在主链 tip 前方 8000 高度内，**连接推进时窗口跟着滑**，
孤儿数量保持可控。

---

## 6. 并行下载逻辑

> Sugarchain 特色的多 peer 并行 IBD：把 header 链按高度切成不相交的 slice，
> 每个 peer 拿一段，请求完自动续领下一段。

### 6.1 状态结构（`manager.go:225-230`）

```go
type blockSyncState struct {
    nextAssign int32                          // 下一个可分配的高度（推进前沿）
    target     int32                          // 要到达的最高 header 高度
    peerSlice  map[*peerpkg.Peer]*blockSlice  // 每个 peer 当前持有的切片
    sliceLen   int32                          // 每次分给一个 peer 的最大高度跨度
}
type blockSlice struct { start, end int32 }
```

只允许 `blockHandler` goroutine 访问。`sm.blockSync []*peerpkg.Peer` 是参与集合。

### 6.2 切片分配/释放（`assignBlockSlice` / `releaseBlockSlice`，`manager.go:1336-1392`）

**分配**：
1. `windowEnd = bestHeight + maxBlockRequestWindow`，不超过 `bestHeaderHeight`。
2. `nextAssign <= bestHeight` 时重置为 `bestHeight + 1`（绝不分配已连接的块）。
3. `start = nextAssign`，`end = min(start + sliceLen, windowEnd)`。
4. 成功则 `peerSlice[peer] = {start,end}`，`nextAssign = end`。

**释放**：
1. `buildBlockRequest` 在 peer 把 slice 内所有高度都请求完后调用
   `releaseBlockSlice`（`manager.go:1514-1516`）。
2. `releaseBlockSlice` 删除该 peer 的切片，并**回卷前沿**：
   `if sl.start < nextAssign { nextAssign = sl.start }` —— 使被释放高度可重新分配，
   而不是永久跳过。
3. peer 断开时（`handleDonePeerMsg`，line 945）也释放其切片，高度回到池中。

### 6.3 启动并行（`finishHeaderSync`，`manager.go:1753-1813`）

```
sliceLen = maxBlockRequestWindow(8000) / maxHeaderSyncPeers(8) = 1000（当前实现除以 maxHeaderSyncPeers，非活跃 peer 数）
blockSyncState{ nextAssign: bestHeight+1, target: bestHeaderHeight }
sm.syncPeer = blockSync[0]                    // 用于 stall/进度跟踪
for each peer: fetchHeaderBlocks(peer)        // 每个 peer 领第一个 slice
```

`blockSyncAddPeer`（`manager.go:808-831`）：新 peer 折叠进集合（封顶 8、按 host
去重），立刻 `fetchHeaderBlocks` 分配首个 slice。

### 6.4 持续补量：`blkDownload`（并行真正生效的关键）

```go
// manager.go:838-854
func (sm *SyncManager) blkDownload() {
    sm.reconnectStoredBlocks()                     // 先连落盘未连的块
    for _, p := range sm.blockSync {
        state := sm.peerStates[p]
        if state == nil || len(state.requestedBlocks) >= minInFlightBlocks {
            continue                              // 在途 ≥10 的 peer 不补
        }
        sm.fetchHeaderBlocks(p)                    // 给 drained peer 补下一段
    }
}
```

**触发点**：`handleBlockMsg` 只要 `block.Height() < lastHeight`（IBD 未结束）就调
`blkDownload()`（`manager.go:1301-1307`）。注释（1294-1300）明确说明：**不限制在
送达块的 peer 上**。因为若只看送达 peer 自己的在途数，sync peer 一直满窗，
其它 peer 只拿一个 slice 就饿死——并行退化成单 peer。

这样任意 peer 每收到一个块，都会给所有在途数 < 10 的 peer 补量。

### 6.5 header 并行（`headerSyncState`，`manager.go:201-210`）

- `sliceLen = wire.MaxBlockHeadersPerMsg = 2000`。
- 每个 peer 同时持有 1 个 `headerRange`；`assignHeaderRange`（`manager.go:483-527`）
  优先补 front 洞（`nextHeight`），否则推进 `nextAssign`。
- `handleParallelHeadersMsg`（`manager.go:1635-1685`）按 peer 找 range，
  校验 `headers[0].PrevBlock == HeaderHashByHeight(start-1)`（过期/错配丢弃）。
- `processReadyHeaderRanges`（`manager.go:1691-1748`）：**只有 front 就绪才按序
  应用**，应用后推进 `nextHeight`，该 peer 立即续领下一片；`nextHeight > target`
  时 `finishHeaderSync()` 转入 block 阶段。
- 超时重发：`reissueStaleHeaderRanges`（6s 超时）、`reissueFrontRange`
  （front 空洞时抢持有最大 range 的 peer 的 slice）。

### 6.6 stall / 换 peer（`handleStallSample`，`manager.go:860-891`）

- 每 30s 采样（`stallSampleInterval`）。
- header 未完成先 `reissueStaleHeaderRanges()`。
- `time.Since(lastProgressTime) > maxStallDuration(3min)` 才判定 stall。
- `clearRequestedState(state)` 清空全局请求表 → 换 sync peer（`updateSyncPeer`）。
- `shouldDCStalledSyncPeer`（`manager.go:897-913`）：peer 高度 > 我们高度才断开。

### 6.7 实测现象与已修复的问题

- 早期 bug：`blkDownload` 只在"送达块的 peer 在途 < 10"时触发，导致 sync peer
  一直满窗、其余 peer 饿死 → **并行退化成单 peer**（getpeerinfo 显示仅 1 个
  peer `sent` 在增长，其余 `sent≈0`）。
- 修复（提交 `0a23c0e7`）：去掉该门槛，任意块到达都 `blkDownload()` 补所有
  drained peer。实测 3 个 IP 并行传块（89.117.38.140 / 23.137.105.31 /
  108.62.161.20），速率 ~10-25 blocks/s。
- 当前已知局限：高延迟 peer 因"在途长期 ≥10"可能再次饿死 → 出现"某 10-20 秒
  窗口只有一个 IP 在传"的瞬时现象（资源监视器可见），但 1 分钟窗口内多个
  peer 都在传。属于负载不均，非完全退化。

---

## 7. 区块与索引存储

### 7.1 ffldb（leveldb）元数据 bucket（`chainio.go:37-89`）

| bucket / key 常量 | 值 | 内容 |
|---|---|---|
| `blockIndexBucketName` | `blockheaderidx` | 区块索引行（见下） |
| `hashIndexBucketName` | `hashidx` | hash → height（4B LE） |
| `heightIndexBucketName` | `heightidx` | height → hash |
| `chainStateKeyName` | `chainstate` | 主链 best state |
| `bestHeaderStateKeyName` | `headerchainstate` | header 链 best state |
| `blockDownloadStateKeyName` | `blockdownloadstate` | 已写盘最远块游标 |
| `utxoStateConsistencyKeyName` | `utxostateconsistency` | UTXO flush 一致性标记 |
| `spendJournalBucketName` | `spendjournal` | 花费日志（回滚用） |
| `utxoSetBucketName` | `utxosetv2` | UTXO 集合 |

### 7.2 区块索引行

```
key   = height(4B 大端) + hash(32B)          // blockIndexKey()
value = 序列化 80B header + 1B status         // dbStoreBlockNode()
```

`status` 位标志标记：块数据是否已落盘（`HaveData`）、是否验证过等。
判断"是否已有某块"：查 `index` map 且 `node.status.HaveData()`；或查 `dbTx.HasBlock`。

### 7.3 区块本体：flat 文件（`.fdb`）

区块数据存在 ffldb 的**扁平文件**里（`ffldb/blockio.go`）：

| 项 | 值 |
|---|---|
| 文件扩展名 | `.fdb` |
| 单文件上限 | 512 MiB |
| 文件名模板 | `%09d.fdb`（如 `000000001.fdb`） |
| 元数据 | `blockLocation{blockFileNum, fileOffset, blockLen}` |

写入：`dbTx.StoreBlock` 追加写文件，位置记录在元数据。
读取：`dbFetchBlockByNode` → `dbTx.FetchBlock(hash)`（用 location 定位）+ 反序列化。

### 7.4 两级索引读取路径

```
BlockByHeight(h):
    node = nodeAtHeight(h)             // 内存索引（窗口内）
         └─ 被驱逐 → materializeColdNode()   // 冷读 fallback（见 §8）
    block = dbFetchBlockByNode(node)   // .fdb 按 hash 读

BlockByHash(hash):
    node = LookupNode(hash)
         └─ 缺失 → materializeColdNode()
    block = dbFetchBlockByNode(node)

fetchBlockByHeight(h)   // UTXO 重建专用，不依赖内存节点
    hash = dbFetchHashByHeight(h)      // heightidx
    block = dbFetchBlockByHeightHash(hash)  // blockheaderidx + .fdb
```

### 7.5 批量写入（最近改动，`blockindex.go`）

三个方法配合，把"每块两次 DB 提交"合并成一次：

| 方法 | 职责 | 要求 |
|---|---|---|
| `flushToDB()`（763） | 独立事务写全部 dirty + 清 dirty | 持有 `bi.Lock` |
| `flushDirtyLocked(dbTx)`（790） | 把 dirty 节点写入**调用方提供的事务**（不提交） | 持有 `bi.Lock` |
| `finishFlushLocked()`（835） | 清 dirty + 节流 `evictWindow` | 持有 `bi.Lock` |

**`connectBlock` 合并提交**（`chain.go:664-753`）：持 `b.index.Lock`，在**同一个
`db.Update`** 里依次：`flushDirtyLocked`（本块索引行）→ `PruneBlocks`（可选）→
`dbPutBestState` → `dbPutBlockIndex` → `dbPutSpendJournalEntry` →
`indexManager.ConnectBlock`；提交成功后 `finishFlushLocked`。
→ 每块只有**一次 DB 提交**，且 chain state 绝不会提交在它指向的索引行之前
（崩溃一致性）。

`finishFlushLocked` 的节流：`evictCount++`，每 `blockFlushBatchSize=1000` 次才跑
一次 O(indexSize) 的 `evictWindow` 全量扫描，避免每块都扫。

### 7.6 落盘时机

- 块数据：`maybeAcceptBlock`（`accept.go:70`）里 `dbStoreBlock` 先写 `.fdb`，
  再推进 `blockDownload` 游标（`accept.go:77-83`）。
- 索引行：header 批量 flush（每 `headerFlushBatchSize=20000` 个）
  或 `connectBlock` 合并提交（每块）。
- best state：`dbPutBestState` 在 `connectBlock`/`disconnectBlock` 时写 `chainstate`。

---

## 8. Header 窗口与冷读

### 8.1 为什么需要窗口

主网约 4,370 万 header，全量驻留内存约 5GB+（每 header 一个 `blockNode`）。
`--headerwindow` 参数（`config.go:190`）限制内存中保留的 header 数：

- `0` = 全量驻留（历史 btcd 行为，主网会 OOM）。
- `>0` = 窗口化：只物化 `[chainBoundary, bestHeight+windowSize]`（连接链窗口）
  ∪ `[headerBoundary, ∞)`（header 窗口）。

### 8.2 evict 流程（`evictWindow`，`blockindex.go:567-671`）

- `windowBoundary(tipHeight) = tipHeight - windowSize`。
- 驱逐：删除低于 header 边界、不在 bestChain 视图、且不在连接前沿窗口
  `[chainTip-windowSize, chainTip+windowSize]` 内的节点；切断 in-window 节点
  指向被驱逐节点的 parent/ancestor 指针；`pruneBelow` 两个视图。
- 节流：由 `finishFlushLocked` 每 1000 次 flush 触发一次。

### 8.3 冷读 fallback（`coldread.go`）

- `materializeColdNode(hash)`：被驱逐的节点从 DB 重建（读 `blockheaderidx`）。
- `fetchBlockByHeight` / `dbFetchHashByHeight`：纯 DB 两跳读，不碰内存节点。
- 冷缓存 `coldCacheSize=64`，缓存最近物化的节点。

### 8.4 窗口化初始化（`initChainState`，`chainio.go:1545+`）

只物化窗口内节点；边界以下逐行累加 `runningWorkSum` 喂给边界锚点，保证
边界节点的累积工作量正确（DAA/难度判断需要）。

---

## 9. UTXO 一致性重建

### 9.1 一致性标记（`InitConsistentState`，`utxocache.go:604-730`）

`utxostateconsistency` key 记录"UTXO 快照对应的链 tip"。

- 无记录（旧库）→ 写 `tip.hash`，返回。
- 等于 `tip.hash` → 一致，返回。
- 不一致（上次异常关闭）→ 触发重建。

### 9.2 按高度重放（冷启动恢复）

```
statusHash = 记录里的一致点
node = LookupNode(statusHash)
     └─ 被驱逐 → materializeColdNode(statusHash)
校验 dbFetchHashByHeight(consistentHeight) == statusHash   // 确认在 best 链
for h = consistentHeight+1 .. tip.height:
    block = fetchBlockByHeight(h)                    // 纯 DB 读，不依赖内存节点
    utxoCache.connectTransactions(block, nil)        // 重放花费
    必要时 s.flush(FlushIfNeeded) 边放边写一致性     // 支持中断
```

关键：按高度读块（`fetchBlockByHeight`）不依赖已被窗口驱逐的 parent 指针，
窗口化下也能正确重放。

---

## 10. 关键参数速查表

### 网络/消息（`wire/`）

| 常量 | 值 |
|---|---|
| 区块头大小 | 80 B |
| `MessageHeaderSize` | 24 B |
| `MaxMessagePayload` | 32 MiB |
| `MaxProtocolMessageLength` | 4 MB |
| `MaxBlockPayload` | 4 MB |
| `MaxInvPerMsg` | 50,000 |
| `MaxBlockHeadersPerMsg` | 2,000 |
| `MaxBlockLocatorsPerMsg` | 500 |

### 同步（`netsync/manager.go`）

| 常量 | 值 | 作用 |
|---|---|---|
| `minInFlightBlocks` | 10 | peer 在途请求补量阈值 |
| `maxBlockRequestWindow` | 2,048 | 请求地平线（高度） |
| `maxRequestedBlocks` | 50,000 | 全局在途请求上限 |
| `maxStallDuration` | 3 min | stall 判定 |
| `stallSampleInterval` | 30 s | stall 采样周期 |
| `maxHeaderSyncPeers` | 8 | 并行下载 peer 上限 |
| `headerRangeStallTimeout` | 6 s | header range 重发超时 |
| `sliceLen`(header) | 2,000 | header 阶段每 peer 跨度 |
| `sliceLen`(block) | 2,048 / peer 数 | block 阶段每 peer 跨度 |

### 落盘批量（`blockchain/accept.go`）

| 常量 | 值 |
|---|---|
| `headerFlushBatchSize` | 10,000 |
| `blockFlushBatchSize` | 1,000 |

### 存储

| 项 | 值 |
|---|---|
| 块数据文件 | `*.fdb`，512 MiB/文件 |
| block index key | height(4B BE) + hash(32B) |
| 数据目录 | `--datadir`（默认 `C:\Users\adest\AppData\Local\Btcd`） |
| header 窗口 | `--headerwindow`（主网建议 50000） |

---

## 附录 A：一次区块接收的完整调用链

```
对端 ──block──► peer/peer.go OnBlock ──► netsync 队列
    └► blockHandler goroutine (manager.go:2265)
         └► handleBlockMsg (manager.go:1127)
              ├─ checkHeadersList          // IBD 加速标志
              ├─ 删 requestedBlocks 表项
              ├─ chain.ProcessBlock        // process.go:143
              │    ├─ blockExists          // 查重
              │    ├─ checkBlockSanity     // 结构校验
              │    ├─ addOrphanBlock 或 maybeAcceptBlock
              │    │    ├─ dbStoreBlock    // 写 .fdb
              │    │    ├─ flushToDB       // 索引落盘
              │    │    └─ connectBestChain (chain.go:1221)
              │         └─ connectBlock    // chain.go:618
              │              ├─ b.index.Lock()
              │              ├─ db.Update{                     // 单事务
              │              │    flushDirtyLocked            //  索引行
              │              │    dbPutBestState              //  主链状态
              │              │    dbPutBlockIndex             //  hash/height
              │              │    dbPutSpendJournalEntry      //  花费日志
              │              │    indexManager.ConnectBlock   //  可选索引
              │              ├─ } 提交成功 → finishFlushLocked + Unlock
              │              └─ utxoCache.flush(FlushIfNeeded) // 独立事务
              ├─ 孤儿分支 → getblocks 请求父块
              └─ blkDownload()             // 给 drained peer 补量（并行关键）
```

---

## 附录 B：术语对照

| 术语 | 含义 |
|---|---|
| IBD | Initial Block Download，初始区块下载 |
| best chain | 已验证并连接的主链 |
| best header | 已接受 header 的最高点 |
| sync peer | 负责主下载的 peer（`*SYNC*`） |
| blockSync | 参与并行下载的 peer 集合 |
| slice | 分给某个 peer 的一段不相交高度区间 |
| in-flight | 已发出 getdata 请求、尚未收到区块的在途区块 |
| orphan | 父块尚未连上、暂时无法连接的区块 |
| locator | 区块哈希定位符（找共同祖先） |

---

*文档生成时间：2026-08-07。适用于提交 `0a23c0e7` 及之后。*
