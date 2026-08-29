# Sugarchain 主网节点存储架构 / Storage Architecture

> ⚠️ 核对状态（2026-08-30）：本文行号已按当前代码修正（chainio.go 等偏移 30~331 行），但**行号仍会随代码变动，请以代码为准**；leveldb 参数已修正为 64/64MB。
> ⚠️ Audit (2026-08-30): line refs updated to current code; treat as approximate.

> 状态:文档 · 更新时间:2026-08-08
> 依据代码:sugarchain-node 仓库(btcd fork, 含 header 窗口化/冷读/并行同步改造)
> 本文回答四件事:block 存成什么样、header 存成什么样、二者如何关联、读写流程怎么走。

---

## 1. 总览:一个数据目录、两种数据库

数据目录:`<datadir>/sugarmainnet/`(如 `C:\Users\...\Btcd\sugarmainnet`)

```
sugarmainnet/
├── btcd.conf                 # 节点配置(含 rpc 凭据)
├── blocks_ffldb/             # ffldb 存储根目录
│   ├── 000000000.fdb         # 块体 flat 文件 #0 (≤512 MiB)
│   ├── 000000001.fdb         # 块体 flat 文件 #1
│   ├── ...
│   └── metadata/             # leveldb(所有索引与元数据)
│       └── *.ldb / LOG / CURRENT / MANIFEST-*
└── peg/ wallets/ log/ ...    # 其它
```

- **块体(block body)** 存在 flat 二进制文件(`.fdb`),追加式、不可变。
- **所有索引/元数据**(header 索引、hash/height 映射、UTXO、spendjournal、best chain state)在 `metadata/` 的 **leveldb** 中。
- 两层由 ffldb driver(`database/ffldb`)统一封装,`btcd` 上层通过 `db.Update/View` 访问,意识不到两层存在。

---

## 2. 块体存储(flat block files)

### 2.1 文件切分

| 常量 | 值 | 位置 |
|---|---|---|
| `blockFilenameTemplate` | `"%09d" + ".fdb"` | `blockio.go:44` |
| `blockFileExtension` | `".fdb"` | `blockio.go:33` |
| `maxBlockFileSize` | `512 * 1024 * 1024`(512 MiB) | `blockio.go:57` |

写满一个文件就落到下一个(`writeBlock`,`blockio.go:432`):当前文件 `<network+len+block+crc>` 的末偏移超过 512MiB 时,关闭当前写文件、`curFileNum++`、`curOffset` 归零。

### 2.2 文件内记录格式(block record)

每一条 block 记录在文件内是:

```
<network 4B><length 4B><serialized block 变长><CRC-32 Castagnoli 4B>
```

| 字段 | 类型 | 大小 | 说明 |
|---|---|---|---|
| network | uint32 LE | 4 | 网络 id,校验防混入其它链的块文件 |
| length | uint32 LE | 4 | serialized block 字节数(不含本 12 字节) |
| serialized block | []byte | `length` | `wire.MsgBlock.Serialize()`,含 80B header + 交易 |
| checksum | uint32 LE | 4 | `crc32.Castagnoli(前 8 字节 + block)` |

record 总长 = `blockLen = length + 12`。写入时一个 hasher 连续消化 network+length+block 后 Sum 成 checksum。

- 位置上,记录可能跨两个文件吗?`writeBlock` 先把整条 `network+len+block+crc` 算成全量再判断是否放得下 → **不会跨文件**。
- 追加写:每次在 `writeCursor.curOffset` 处连续写入,后移 offset;写满 512MiB 则换文件。

### 2.3 blockLocation(定位一个块)

- 由 `serializeBlockLoc` 序列化 `blockLocation` = `<fileNum u32><fileOffset u32><blockLen u32>`,12 字节(`blockLocSize`,`blockio.go:67`)。
- 每一条落在 `ffldb-blockidx` bucket(见 §4)。
- 这里的 `fileOffset` 指向记录起点(network 字段所在偏移);`blockLen` 是含 12 字节外壳的全长。

### 2.4 数据完整性保障

- 读时**校验 network** 与 **CRC32**(`readBlock`,`blockio.go:538-584`)→ 检出文件损坏/换错链文件。
- 写路径在 commit 时对当前文件 `syncBlocks()` 后再更新 metadata(见 §6),崩溃时未落盘的尾巴由 §7 的 reconcile 截掉重来。

---

## 3. 区块头存储(两处磁盘 + 一处内存)

Sugarchain 区块头固定为 **80 字节**:

```
Version int32 LE(4) | PrevBlock hash(32) | MerkleRoot hash(32) | Timestamp uint32(4) | Bits uint32(4) | Nonce uint32(4)
```

`wire.MaxBlockHeaderPayload = 16 + (HashSize*2) = 80`(`wire/blockheader.go:17`)。

### 3.1 磁盘位置一:块文件头部 80 字节(唯一权威副本)

header 就是 block 的**前缀 80 字节**。哪个 hash 存哪条记录,由 `ffldb-blockidx` 的 location 决定。

### 3.2 磁盘位置二:`blockheaderidx` leveldb bucket(索引副本/窗口外解引用)

这是区块链层自己维护的持久化 header 索引,key/value 为:

| Bucket | Key | Value |
|---|---|---|
| `blockheaderidx` | `(height u32 BE)(4B) + hash(32B)` | `header(80B) + status(1B)` |

一行 36B key + 81B value = **117B/条**。header 阶段即全量写入(43.75M 条),block 阶段每块只更新行尾 status 一个字节。

> 注意:`blockheaderidx` 的 height 前缀用 **BigEndian**(`blockIndexKey`,`chainio.go:2266`,为让 leveldb key 高度有序),而 `heightidx`/`hashidx` 用 **LittleEndian**(`byteOrder = binary.LittleEndian`,`chainio.go:89`)。两处字节序不同,极易搞混。

### 3.3 内存:blockNode + 滑动窗口

- 每个已知块一个 `blockNode`(含 header 全字段 + 指针 + 状态),进程内驻留**窗口以内**的节点(`--headerwindow`,默认 0=全量;主网用 2048),窗口外节点被驱逐、需要时冷读重建(§6.3)。
- `bestChain`/`bestHeader` 两个 chainView 各指一个 tip;窗口化下二者不同一(header tip 远高于 chain tip,正是本次 IBD 场景)。

---

## 4. metadata leveldb 明细

### 4.1 ffldb 内部 bucket(驱动层)

因为 `bidx` 前缀机制,leveldb key 实际 = `bucketID(4B BE) + 实际 key`。

| bucket | id(4B BE 前缀) | key | value | 用途 |
|---|---|---|---|---|
| metadata(根桶,id=0) | `00000000` | 直接 key | 直接 value | 顶层状态项 + 虚桶索引 |
| `ffldb-blockidx`(id=1) | `00000001` | `hash(32B)` | `blockLocation(12B)` | hash → 块在 `.fdb` 的位置 |

顶层元数据项:

| key | value | 用途 |
|---|---|---|
| `ffldb-writeloc` | `{fileNum u32 LE}{offset u32 LE}{crc32(前8B)}`(12B) | 当前写指针,崩溃恢复锚点 |

### 4.2 区块链层 bucket(chain 层虚桶)

通过 metadata 的事务虚拟 bucket 读写:

| bucket | key | value | 写时机 | 用途 |
|---|---|---|---|---|
| `blockheaderidx` | height(BE4)+hash(32) | header(80B)+status(1B) | header/block accept 时 | 磁盘全量索引的 header+状态 |
| `hashidx` | hash(32) | height(LE4) | 全部 header(header sync 即写) | hash → 高度两跳查询第一步 |
| `heightidx` | height(LE4) | hash(32) | **仅 bestheader tip 主链** | 高度 → hash,冷读按高度定位 |
| `utxosetv2` | outpoint(变长) | utxo(变长) | block connect/disconnect | UTXO 集合 |
| `spendjournal` | blockhash(32) | 变长 spend 记录 | block connect/disconnect | reorg 回扫 |
| *(顶层 key)* `chainstate` | `chainstate` | bestChainState 序列化(见 §4.3) | tip 推进 | 主链 tip 持久化 |
| *(顶层 key)* `headerchainstate` | `headerchainstate` | bestHeaderState(见 §4.3) | bestHeader tip 推进 | **header tip 持久化(崩溃续传关键,本 fork 新增)** |

`chainstate` value 序列化(`chainio.go:1305`):

```
<hash32><height u32 LE><totalTxns u64 LE><workSumLen u32 LE><workSum bytes>
```

`bestHeaderState`(本 fork 新增,续传关键)：

```
<hash32><height u32 LE>
```

### 4.3 各 bucket 行成本核算(影响磁盘量主因)

| bucket | 每头行成本 | 43.75M 头全量 |
|---|---|---|
| `blockheaderidx` | 36B key + 81B val ≈ 117B | ≈ 5.1 GB |
| `hashidx` | 32 + 4 ≈ 36B | ≈ 1.6 GB |
| `heightidx` | 4 + 32 ≈ 36B | ≈ 1.6 GB |
| 合计索引行 | — | ≈ 8.3 GB |
| + leveldb SST/日志/过滤器(实测 ~1.28×) | — | **实测 ≈ 10.6 GB** |

---

## 5. 块体 + header + 索引:写入流程

一条 block 的落盘链路(`dbStoreBlock → … → ffldb`):

```text
Blockchain 接受 block
  ├─ dbStoreBlock / dbStoreBlockNode  (blockchain/chainio.go)
  │    ├─ blockheaderidx(key=(height,hash)) ← header80+status1
  │    └─ dbTx.StoreBlock(block)                              (ffldb)
  │         └─ 只记入 pendingBlocks 内存;commit 时触达:
  │              writeBlock → .fdb 追加 <net,len,block,crc>(blockio.go:432)
  │              并序列化 blockLocation(12B) → ffldb-blockidx
  │              metaBucket 写 ffldb-writeloc = 新 (fileNum,curOffset)
  └─ db.cache.commitTx(...)                                   (dbcache.go)
       ├─ 缓存未满 → 只进内存 treap
       └─ 缓存超 100MB 或距上次 flush>5min
            → flush(): sync 块文件 → commitTreaps → 落 leveldb
```

要点:

- 块体写不等 leveldb 缓存:块直接落 `.fdb`,只把 **location** 进缓存 → 缓存留给 metadata 的批量追加型索引。
- **metadata 缓存 flush 阈值**:`defaultCacheSize=100MB`、`defaultFlushSecs=300s`(`dbcache.go:24, 29`)。
- 先 sync 后 metadata(`flush/`):保证 `.fdb` 真落盘后才让 index 可见,崩溃时由 §7 的 reconcile 修复。
- leveldb 调优已改:`CompactionTableSize=64MB`、`WriteBuffer=64MB`、L0 触发 8、slowdown/pause 24/48(`database/ffldb/db.go:2186,2195,2201`,应用于 openDB ~2273),header 阶段写放大与文件数大降。

---

## 6. 读取流程

### 6.1 读区块(header + 交易)

用户 API `BlockByHeight(高度)` / `BlockByHash(哈希)`:

```text
1. b.nodeAtHeight(h) / index.LookupNode(hash)      # 内存窗口内?直接拿 blockNode
   └─ 窗口外 → materializeColdNode(冷读,§6.3)      # 只拿 header/status,不含 body
2. db.FetchBlock(hash)  (ffldb, db.go:1340)
      a. pendingBlocks? → 直接返回内存字节
      b. ffldb-blockIdx.Get(hash) → blockLocation(12B)
      c. 打开对应 .fdb → 定位 fileOffset → ReadAt 全长 →
         CRC32 + network 校验 → 返回 serialized block
3. wire 反序列化 → btcutil.Block(带高度)
```

### 6.2 读区块头

- **带 block:**`dbFetchHeaderByHash`(`chainio.go:2182`)`→ db.FetchBlockHeader(hash)`。
  ffldb 实现 `FetchBlockHeader = FetchBlockRegion{Hash, offset:0, len:80}` → 先查 `ffldb-blockidx` 拿 location,再 `readBlockRegion` 读记录 `fileOffset+8` 处 80 字节(只读 80B,不读整块)。
- **header-only(窗口外,内存中只有 header/status):**`dbFetchBlockRowByHeight/ByHash`(`chainio.go:1242/1268`):`heightidx → hash → blockheaderidx → (header, status, height)`,**全程 leveldb,不碰 .fdb 文件** —— 这是 header 窗口化之后冷读的解码路径。
- 内存热路径:`blockIndex.LookupNode` 直接返回驻留 `blockNode.Header()`。

### 6.3 冷读(窗口外兜底,`coldread.go`)

- 触发:窗口驱逐后的高度/hash 查询。
- 路径:`hashidx → height` → `blockheaderidx[(height,hash)] → header+status+parentHash`,重建临时 `blockNode`(parent 断开、workSum=0,仅服务 hash/height/header 查询;禁作 PoW/指针链比较)。
- FIFO 64 项 `coldNodeCache` 加速;指针身份护栏:先查 `index.LookupNode`,内存仍在的原样返回内存指针,保证 reorg 判定安全。只读,不写 block 文件。

---

## 7. 启动恢复(崩溃续传关键)

`openDB → reconcileDB`(`ffldb/reconcile.go:52`):

1. 打开底层 leveldb(`metadata/`)+ 建块仓库(扫描当前 .fdb 末端 = 磁盘真实写入指针)。
2. 读出 `ffldb-writeloc` 的 metadata 指针。
3. 比较:
   - **磁盘在后**(`wc.curOffset > metadata`)= 上次崩溃后未来得及写 metadata 的尾巴 → `handleRollback` 整体截掉重启校验。
   - **磁盘在前** = `.fdb` 缺失/被删 → 有损坏,报错终止。
4. 区块链层(`initChainState`)从 `chainstate` + `bestHeaderState` 双 tip 重建两视图,窗口回填;header 索引全在 leveldb,启动**无需重下 header**(本 fork 窗口化 + 落盘的核心收益)。

---

## 8. 磁盘实测(本机 2026-08,高度 ~2,663,695)

| 项 | 值 | 说明 |
|---|---|---|
| block 文件数 | 3 个 `.fdb` | 0(≈512MiB 满)、1(≈512MiB)、2(≈16MB) |
| block 文件合计 | **≈ 1,090 MB** | = 已连接 2.66M 块的原始数据 |
| 每块平均 | ≈ **0.41 KB/块** | 早期段多次单 tx 块、块头 80B 占比高 |
| metadata 目录 | **≈ 10,594 MB**,881 个 `.ldb`(~12MB/个) | 大头 = 43.75M header 索引(§4),**已基本不再增长**,如下 |
| 索引摊销 | ≈ 0.41 KB/块(块部分) | header 索引 43.75M × ~0.24KB ≈ 10.6GB,是常数(与 height 无关) |

推断:header 索引是**恒定**的(43.75M 全量已入盘);随 block 下载,只有块体 + `ffldb-blockidx`(每块 32B hash + 12B loc ≈ 44B)增长。全链(约 110M 块,还需再下 ~66M 块)按实测 0.41KB/块 计,块体最终 ≈ 45GB 上限;实际越往近期块体越大(多交易),故总体预估在 **18–45 GB** 区间。

## 9. 常见问题 FAQ

**Q: 为什么 metadata 10.6GB 在 block 下载期不涨了?**
A: header 已在窗口期全量落盘(43.75M 条),header 索引增长与高度无关(§4.3),即固定成本;block 阶段只追加 `ffldb-blockidx`(每块 +44B)和 UTXO。

**Q: block 文件为什么要减 12 字节?**
A: record 里 network+block length+checksum 各 4B 随行,location 指向的记录起点是 network,读取时 +8B 跳 length 字段得到 block 本文(blockio.go:534, `readBlock`)。

**Q: 窗口化之后仍能读历史 header / block 吗?**
A: 能。header 走 §6.2/6.3 的 leveldb 冷读;block 走 `ffldb-blockidx` → .fdb(有块即读);冷读只取 header/status(不含 UTXO 重建)。

**Q: 崩溃后要重下吗?**
A: header 已在 metadata,双 tip 恢复 → 不需要;block 由 reconcile 截尾回滚到已 commit 的索引位置。

---

## 10. 代码索引速查

| 功能 | 位置 |
|---|---|
| flat file 读写 | `database/ffldb/blockio.go` |
| ffld 事务/commit/flush | `database/ffldb/db.go`, `database/ffldb/dbcache.go` |
| 崩溃恢复 reconcile | `database/ffldb/reconcile.go` |
| header/block 序列化 | `wire/blockheader.go`, `wire/msgblock.go` |
| chain 层索引 bucket 读写 | `blockchain/chainio.go`(`dbStoreBlockNode:2230`, `blockIndexKey:2266`) |
| cold 冷读 | `blockchain/coldread.go` |
| 内存窗口 | `blockchain/blockindex.go`(setWindow/evictWindow), `chainview.go` |
| UTXO flush | `blockchain/utxocache.go`(Required/IfNeeded/Periodic) |
| 同步进度/并行 | `netsync/manager.go`(header 并行 + block 阶段) |