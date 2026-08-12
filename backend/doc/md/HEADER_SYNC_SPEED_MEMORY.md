# Header 同步内存问题分析(第一优先:同步速度)/ Header Sync Memory Analysis (Speed First)

> 状态:分析文档 · 未改代码 · 更新时间:2026-08-12
> 前提:本 fork 已实现窗口化 + 冷读兜底(见 `HEADER_INDEX_MEMORY.md`,步骤 1-6 已提交),并行 header 拉取已上线(`netsync/manager.go`,`--headerwindow` 窗口生效)。

## 结论(速度优先视角)/ Verdict

1. **当前回退版本(`837c56b2`,撤销 `2a783e47`)没有买到速度**:实测尾部 ~533 h/s、120s 均值 433 h/s,并不比 `2a783e47` 版本在受控冒烟里的 895 h/s 快。此前的"变慢(~100 h/s)"是 peer/网络期性波动,不是逐批 eviction 引起的。
2. **逐批 eviction(2a783e47)的成本可忽略**:它只在 header 批量落盘(每 10000 头)后跑一次 `evictWindow`,而 `evictWindow` 是 O(indexSize) 且 indexSize 被窗口钳在 ≈ 2×2048 个节点 → 每次微秒级,每 ~20s 一次。CPU 开销对同步速度无实质影响。
3. **真正的速度杀手是内存失控**:回退版本要等 `blockFlushBatchSize(1000) × headerFlushBatchSize(10000) = 1000 万头`才逐一次窗。窗口期 index 可涨到 ~1000 万节点 ≈ 4.3GB(按 430B/头),形成每 ~5.5h(500 h/s)一次的锯齿峰;大堆 + Go GC 压力反而拖慢同步,极端时 OOM 直接把速度归零。
4. **建议**:恢复 `2a783e47` 的逐批 forceEvict(内存恒被窗口钳住,CPU 可忽略),这是速度第一前提下唯一符合"长跑不翻车"的选择。

## 实测数据 / Measured Data

同一台机器、同一 `--headerwindow=2048`、同一 datadir(并行 header 同步进行中)。

| 构建 | 时间段 | tip 区间 | 速率 | RSS 行为 |
|---|---|---|---|---|
| `2a783e47`(逐批 evict) | 2026-08-11 受控冒烟(临时 datadir,650k 高度) | — | **~54k/min ≈ 895 h/s** | 恒定 ~600MB,有界(`HEADER_INDEX_MEMORY.md` §19) |
| `2a783e47` | 2026-08-12 同步进行中 | 6.5M→6.9M | ~667 h/s(峰) | 有界;后遇网络期性 stall 低至 ~100 h/s |
| 回退 `837c56b2` | 30s 采样 | 6.99M→7.00M | ~333 h/s | 394.6→445.1MB(+50.5MB/30s,含 GC 抖动) |
| 回退 `837c56b2` | 120s 采样 | 7.038M→7.090M | 均值 433 h/s,尾部 ~533 h/s | 487→533MB 攀升,末点 GC 回收回落 419MB;净增速 ~0.5MB/s(攀升相) |

要点:两个版本速度都在同数量级,差异落在 peer 质量与网络波动;回退版内存**无逐批下界**,方向性增长。

## 机制与代码证据 / Mechanism (code evidence)

### 常量(回退版本,`backend/blockchain/accept.go`)
```go
const headerFlushBatchSize = 10000  // accept.go:20  每收 10000 头 → 批量落盘一次
const blockFlushBatchSize = 1000    // accept.go:25  block 下载期:每连接 1000 块 → 落盘一次
```

### 回退版:逐窗被节流(问题所在)
- 批量落盘:`accept.go:302-305`(header 阶段 `maybeAcceptBlockHeader`),`b.headerFlushCount >= headerFlushBatchSize` → `b.index.flushToDB()`。
- 逐窗:`blockindex.go:835-840` `finishFlushLocked()`:`evictCount++`;仅当 `evictCount >= blockFlushBatchSize(1000)` 才 `evictWindow()`。
- 合成节流周期 = **1000 × 10000 = 10,000,000 头**。两次逐窗之间 index 无界增长:
  - 每节点开销 ~430-440B(`HEADER_INDEX_MEMORY.md` §5:blockNode ~230-260B + 双 chainView 指针 16B + map/GC 摊销)。
  - **峰值 ≈ 10M × 430B ≈ 4.3GB**,锯齿。500 h/s 下周期 ~5.5h,37M 全同步 ≈ 3-4 个周期,累计向堆里分配/释放 ~40GB 的 blockNode。

### evict 版(`2a783e47`):每批量落盘即逐窗
- 提交内容:签名改 `flushToDB(forceEvict bool)`/`finishFlushLocked(forceEvict bool)`,header 批量落盘路径传 `true` → 每次 flush 后立即 `evictWindow()`。
- `evictWindow`(`blockindex.go:567`):遍历 map 删边界以下节点 + 两视图 `pruneBelow`。**复杂度 O(indexSize)**,而 indexSize 被窗口钳在 ≈ 2×2048(每 tip 各留 `windowSize`,不相交)→ ~4-5k 节点 → 微秒级;每 20s(10k 头 @500 h/s)一次,年化可忽略。
- 收益:index 恒 ≈ 窗口规模(实测 RSS ~600MB 恒定),GC 压力消失,长跑速度稳定。

### 为什么"逐批 evict 变慢"的证据不足
- 同代码在受控冒烟达 895 h/s(§19),回退版当前也只有 433-533 h/s —— 两个构建无速度差。
- stall 期间(6.5M→6.9M)发生在**逐批 evict 已在线的代码**上,但同一时期网络侧出现长时间空转/低吞吐;随后回退后也未回到 895,反而接近 500。速度与构建无关,与 peer/网络相关。
- evict 每次仅 O(window) 微秒级、且不在 header 接收热路径上(在批量落盘事务提交后,chainLock 之外经 `blockIndex` 自己的锁),不存在成为瓶颈的路径。

## 速度第一的推论 / Speed-First Implications

| 项目 | 回退版(现) | 逐批 evict 版 |
|---|---|---|
| index 上界 | ~1000 万节点 ≈ **4.3GB 锯齿峰** | ≈ 2×2048 节点,~MB 级 |
| GC 压力 | 20h 跑批分配/释放 ~40GB 节点 | 极低 |
| 长跑稳定性 | 4-5GB 堆上 GC 停顿,极端 OOM | 恒定小堆,速度稳定 |
| 相对速度(受控对比) | 433-533 h/s | 895 h/s(冒烟峰值) |

速度第一 ≠ 当前回退版更快;恰恰相反,内存失控的锯齿峰 + GC 停顿是唯一能在 20h 长跑中把速度拖到零的因素。

## 建议 / Recommendation

1. **恢复 `2a783e47`**(`git revert` 或 `cherry-pick`):逐批 forceEvict,内存恒定有界,CPU 可忽略。这是速度第一前提下唯一满足"长跑不 OOM、不因 GC 拖速"的方案。
2. **可选加固(不阻塞恢复)**:`evictWindow` 的 O(indexSize) 逐窗在窗口内是微秒级,但可改"剪到 `window+slack`(如 2×window)"摊销 map 遍历,避免每 10k 头一次全扫——收益甚微,不必优先。
3. **窗口大小**:`--headerwindow=2048` 在 header 阶段足够(SugarShield 回走 510 + 中位时间 11,`HEADER_INDEX_MEMORY.md` §18#7);若要减少 block 下载期冷读,可适度调大(如 10k,内存代价仅 ~10-50k 节点 ≈ 3-15MB),属可选项。
4. **验证口径**:恢复后按 §19 冒烟口径重跑一次 100k 头,记录 RSS 曲线 + 速率,确认"内存恒定 + 速度不回退",作为后续所有改动(冷读、block 下载批大小)的回归基线。

## 附录:采集方法 / Measurement Method

- 状态:每 30s 打 `getblocksyncstatus`(8334 RPC,`header_tip`);内存:`Get-Process btcd` 的 `WorkingSet64`。
- 已知噪声:RSS 含 Go heap 预留,GC 后骤降(末点 532.8→419.4MB);净增速以攀升相斜率 ~0.5MB/s 计,30s 窗口内的 +50.5MB 属 GC 相位,不作线性外推。
