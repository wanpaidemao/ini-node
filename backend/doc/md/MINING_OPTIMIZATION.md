# 挖矿链路 & GC 优化点 / Mining Pipeline & GC Optimization Notes

> 状态:优化点清单(未改代码)· 更新时间:2026-09-06
> 数据来源:`go run ./cmd/powbench`(实测)、代码走读(pow / yespower / mining / cpuminer)
> 关联:`HEADER_SYNC_MEMORY_OPTIMIZATION.md`(header 同步侧 GC 方案,已实施)、`PENDING_TESTS.md`(regtest 挖矿待测)

## 1. 现状(实测)

`cmd/powbench` 单线程实测(纯 Go yespower, N=2048, r=32, 8MB scratch):

| 指标 | 实测值 |
|---|---|
| 单次 `pow.BlockPoWHash` | ≈ 29.3 ms |
| 哈希率(单核) | ≈ 34 H/s |
| cpuminer 默认 worker 数 | `runtime.NumCPU()`(多核并行) |
| 每 5 秒出块(Sugarchain 节奏) | 挖矿循环每块只需解 ~1 次即可 |

**每哈希分配量(代码走读统计):**

| 分配点 | 次数/哈希 | 单次大小 | 小计 |
|---|---|---|---|
| `blockmixPwxform` 内 `make([]uint32, 16)` | ~3415(smix1 2048 + smix2 1367) | 64 B | ~218 KB |
| `salsaXOR` 内 `make([]uint32, 16)` | ~3415(随 blockmix 调用) | 64 B | ~218 KB |
| `pbkdf2.Key` 输出 `make([]byte, 4096)` | 1 | 4 KB | 4 KB |
| `pow.BlockPoWHash` 的 `bytes.Buffer` | 1 | ~80 B | ~80 B |
| `blockchain.HashToBig` 的 `big.Int`(solveBlock 每 nonce) | 1 | ~72 B | ~72 B |
| **合计** | | | **≈ 440 KB/哈希** |

按 34 H/s 单核 → **≈ 15 MB/s 分配率**;8 核 worker → **≈ 120 MB/s**。
GOGC=60(`btcd.go:521`)下,GC 频率与 STW 随分配率线性恶化 —— 这就是挖矿侧的主要 GC 压力源。

## 2. 挖矿链路优化点(按优先级)

### P0 — 每哈希热循环(收益最大)

**A. yespower 内层零分配(GC 压力主源)**
- `yespower.go`:`blockmixPwxform`(L277)、`salsaXOR`(L367)、`blockmixSalsa`(L260)每次调用 `make([]uint32, …)` 新建 16~44 字临时缓冲。
- 现状:`Scratch` 池(全局 `sync.Pool`)已复用 8MB 大缓冲,但内层小缓冲未复用。
- 改法:在 `Scratch` 增加一块小缓冲(`tmpX []uint32`,16 字足够覆盖 pwxWords=16),三处 `make` 改为复用该缓冲;注意 `salsaXOR` 被递归/嵌套调用时需确认无重入冲突(当前调用链 `blockmixPwxform → salsaXOR` 是尾调用,单层复用安全)。
- 效果:每哈希 ~437 KB 分配 → 0,分配率降 ~99%。

**B. `pow.BlockPoWHash` 序列化零分配**
- `pow/pow.go:12`:`bytes.Buffer` + `buf.Grow(80)` 每次堆分配。
- 改法:固定 `[80]byte` 栈数组 + `header.BtcEncode` 直接写入,避免堆分配。

**C. solveBlock 的 `HashToBig` 比较零分配**
- `cpuminer.go:284`:`blockchain.HashToBig(&hash).Cmp(targetDifficulty)` 每次 `new(big.Int)`。
- 改法一(简单):把 `targetDifficulty` 与比较用的 big.Int 做成 worker 级复用变量(`SetBytes` 后 `Cmp`)。
- 改法二(更优):target 是 256 位大数,直接按字节比较 —— hash(小端)反转后与 target 的 32 字节从高位到低位比较,前 1~2 字节不同即可短路,零分配且更快。
- 注意:验证路径 `validate.go:416` 也走 `HashToBig`,可一并受益(该处频率低,优先级低)。

**D. 挖矿核心算法本身(吞吐瓶颈,大工程,可选)**
- 29 ms/哈希中 ~99% 是 yespower 纯 Go 计算。内部分配优化只降 GC,不显著提 H/s。
- 真正提速方向:SIMD/手写汇编移植(参考 C++ umami 的 yespower 实现)、多 scratch 并行预取。收益 2~5×,代价高,列为远期。

### P1 — 区块模板生成(每 5 s 或 stale 时触发)

**E. `NewBlockTemplate` 耗时**
- `mining.go:533` 对 mempool 每个 tx 调 `FetchUtxoView`(DB 查询);`mining.go:864` 再跑 `CheckConnectBlockTemplate` 全量校验。
- 链上每 5 秒出块、挖矿 worker 每次 stale 都要重建模板,若模板生成 > 出块间隔会形成瓶颈。建议先 pprof 实测 `NewBlockTemplate` 耗时占比再决定是否优化(候选:UTXO 批量预取、模板缓存复用未变部分)。

**F. 多 worker 模板生成串行化**
- `cpuminer.go:331/346`:所有 worker 在 `submitBlockLock` 下各自生成模板,生成期间全局锁被占用。
- 改法:单 worker 生成模板后广播共享(经典池模式),或模板生成移出锁外(用 `BestSnapshot().Hash` 校验模板有效性替代锁)。
- 效果:消除多核下模板生成的串行化与锁竞争。

### P2 — 杂项

**G. 废弃的 `rand.Seed` 调用**
- `cpuminer.go:340`、`cpuminer.go:594`:Go 1.20+ 全局 rand 已自动 seed,显式 `rand.Seed` 无必要;且全局 `math/rand` 自带互斥锁。
- 改法:删除 `rand.Seed`,或改用 worker 本地 `rand.New(rand.NewSource(...))` 消除全局锁竞争(地址选择频率极低,优先级低)。

**H. `GenerateNBlocks` 与 `generateBlocks` 逻辑重复**
- 两处各有一份 ticker/模板/求解循环,可抽公共 worker 函数,减少维护面(行为不变,优先级低)。

## 3. GC 优化点汇总

| # | 位置 | 现状 | 改法 | 分配削减 |
|---|---|---|---|---|
| 1 | `yespower.go` blockmixPwxform / salsaXOR / blockmixSalsa | 每哈希 ~6830 次 64 B 小分配 | 复用 `Scratch.tmpX` | ~437 KB/哈希 → 0 |
| 2 | `pow/pow.go` BlockPoWHash | 每哈希 1 次 80 B 堆分配 | `[80]byte` 栈缓冲 | ~0 |
| 3 | `cpuminer.go` solveBlock `HashToBig` | 每 nonce 1 次 big.Int 分配 | 复用 big.Int / 字节比较 | ~72 B/哈希 → 0 |
| 4 | `mining.go` NewBlockTemplate | 每次模板大量 UTXO 视图分配 | 批量预取 + 视图复用 | 模板级(低频) |
| 5 | `btcd.go:521` GOGC=60 | 已启用(好) | 挖矿高负载期可配合 `debug.SetGCPercent` 动态调优;建议在 P0 落地后复测 | — |

预期效果:落地 #1~#3 后,挖矿侧分配率 120 MB/s → 数 MB/s 量级,GC 频率下降一个数量级,多核 worker 的 STW 停顿显著减少;#4 待实测。

## 4. 验证方式

- 基准:扩展现有 `pow/pow_bench_test.go`(仅覆盖 `yespower.Hash`,建议补 `BlockPoWHash` 与 solveBlock 内循环对比)。
- 回归:`pow/pow_kat_test.go` 已知答案必须保持不变(字节级一致)。
- 分配观测:`go test -bench . -benchmem`、`go run ./cmd/powbench` 前后对比;pprof `alloc_objects`/`inuse_space`。
- 挖矿功能:regtest 单机挖矿(PENDING_TESTS.md 待办)验证求解/提交链路无回归。

## 5. 难度竞争失败分叉:同步中断与 RPC 阻塞(稳定性)

> 关联实现:`[0.0.1-28]`(33df4c5d,快速回滚+孤儿错块删除,已提交)。
> 现象:竞争失败后 **① block 不再同步(0 bl/s,header 仍前进);② RPC 中断数秒~数分钟**。

### 5.1 根因链路(代码证据)

**A. 孤儿洪流 × 完整 PoW 校验 = 写锁长占(→ "RPC 中断")**
- 竞争失败后本地块占据 `bestChain` tip,网络主链块 `PrevBlock` 不匹配 → 每个真实块进 `addOrphanBlock`(`process.go:291-296`)。commit 前实测:2838 个孤儿。
- 关键:`ProcessBlock` 的孤儿分支位于 `checkBlockSanity`(L237)之后 —— 每个孤儿在入池**前**已经跑完 `CheckBlockHeaderSanity → checkProofOfWork → pow.BlockPoWHash`(≈29ms/次)。
- 全程持有 `chainLock.Lock()` 写锁(`process.go:165`):2838 孤儿 × 29ms ≈ **82 秒写锁**。期间所有 RPC 链查询(读锁)与矿工提交块全部排队 → "RPC 中断一会"。
- 孤儿池上限 `maxOrphanBlocks=16384`(`chain.go:34`),洪流不会撑爆内存,但 CPU/锁阻塞依旧。

**B. 回滚路径全程写锁(→ "RPC 中断" 第二来源)**
- `InvalidateHeaderChain`(`chain.go:2387`)持 `chainLock.Lock()` 全流程:
  - `reorganizeChain`:逐块 UTXO 回滚(verifyReorganizationValidity 校验);
  - `removeDisconnectedBlocks`:每块一次 `db.Update` 事务(DeleteBlock + dbRemoveBlockNode)+ 末尾 `flushToDB`;
  - 状态快照重建:逐块 `dbTx.FetchBlock` + 反序列化(TotalTxns/Size/Weight)。
- 全部在写锁内,RPC `RLock` 查询全部阻塞;回滚块数越多阻塞越长。

**C. 检测窗口 = "不再同步" 的体验时长**
- 同步卡死的时序:本地块占 tip → 真实块全部变孤儿、下载 0 进展 → 需等待分叉检测器触发回滚:
  - 快速检测器(handleStallSample):front-unreachable 两票 / DB 高度索引对比 / peer 高度对比(需停滞>1min)/ `HeaderChainDiverged`(hash 对比,最快);
  - 兜底定时器 `blockUnavailableTimeout=90s`(已从 10min 缩短)。
- 因此竞争失败后,同步最长停滞 ~1.5min 才恢复;若网络稀疏凑不齐两票、DB 索引未污染,窗口接近 90s。

**D. blockHandler 单 goroutine 串行**
- `handleStallSample` 在 blockHandler 的 `stallTicker` 分支**同步执行**回滚,期间 `msgChan` 全部消息(blockMsg/headersMsg/processBlockMsg)排队 —— 回滚耗时 = 消息处理暂停时长。

### 5.2 优化点(按收益排序)

**O1. 孤儿入池前跳过完整 PoW 校验(消除 82s 级写锁阻塞,首选)**
- 父块已知不存在(`blockExists(prevHash)=false`)时,PoW 校验可推迟到孤儿真正被接入(`processOrphans → maybeAcceptBlock`)时再执行;入池前只做轻量 bits 范围检查。
- 复刻 bitcoind 语义:孤儿池只缓存,不逐块全量验 PoW。
- 收益:孤儿洪流期间写锁占用从 ~29ms/块降到 ~0.1ms/块,数量级改善;同步期间 RPC 不再中断。
- 注意:需保证孤儿被接入时仍走完整校验(现状 `processOrphans` 的 `maybeAcceptBlock` 已全量校验,天然满足)。

**O2. 回滚路径降低写锁时长**
- `removeDisconnectedBlocks`:每块一个 `db.Update` → 合并为**单事务**批量删除(DeleteBlock + dbRemoveBlockNode 全部在一个 Update 内),`flushToDB` 保持一次。
- 状态快照重建中逐块 `FetchBlock` 反序列化仅需 tx 数/权重,可用 `DBBlockFromBytes` 前的轻量 header 解析替代完整反序列化(或复用 `GetBlockWeight` 免序列化路径)。
- `reorganizeChain` 在 detach-only(attachNodes 为空)时跳过 attach 侧校验。

**O3. 竞争失败即时回滚(缩短检测窗口,消除 90s 停滞)**
- 矿工 `submitBlock` 失败且错误为 `ErrMinedBlockNotOnMainChain`(accept.go 新守卫)时,**立即**触发 `rollbackToForkPoint`(可异步入队到 blockHandler),不等 stall 采样周期。
- 或将 `HeaderChainDiverged` 检测从"header 链已越过 tip"提前为"header 链前进而 block 下载 0 进展 N 秒" —— 秒级触发,无需 1min。

**O4. 长耗时操作移出 blockHandler 主循环**
- 回滚/resetDownloadState 改为异步执行:stallTicker 只做检测与入队,实际回滚在独立 goroutine(或消息队列)执行,期间 blockHandler 继续处理 blockMsg/headersMsg,矿工提交不被推迟。
- 需注意与 `msgChan` 顺序的一致性(回滚期间到达的块应排队到回滚后处理)。

**O5. 孤儿池针对性清理(洪流期间内存/CPU 卫生)**
- 现状:`addOrphanBlock` 惰性清理过期项 + `maxOrphanBlocks=16384` 上限,父块缺失的孤儿在洪流期存活到过期。
- 优化:对"父块被回滚删除"的孤儿立即清出(回滚时遍历 `prevOrphans` 删除以错块为父的孤儿),避免重启后孤儿链残留;顺带释放 `orphans` map 内存。

### 5.3 验证方式

- 复现:regtest 双节点竞争挖矿(或注入伪造 tip),观测回滚周期内 RPC 延迟(`getblockcount` 响应时间)与 bl/s 恢复时长。
- 回归:`[0.0.1-28]` 的既有场景(伪造链回滚、重启后错块不再物化)必须保持绿色;`process_test.go`/`chain_test.go` 全套通过。
- 指标:回滚期间最大 RPC 延迟、孤儿洪流下 `chainLock` 写锁累计占用、检测到回滚的耗时分布。
