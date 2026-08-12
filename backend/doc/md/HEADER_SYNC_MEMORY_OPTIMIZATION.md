# Header 同步内存/GC 优化方案(FreeOSMemory + 对象池 + 偏移视图)/ Header Sync Memory & GC Optimization Plan

> 状态:方案 · 未改代码 · 更新时间:2026-08-12
> 关联:`HEADER_INDEX_MEMORY.md`(窗口化,步骤 1-6 已提交)、`HEADER_SYNC_SPEED_MEMORY.md`(速度优先)
> 范围:本次只出方案,不落代码;按 §5 顺序分步实施、分步验证。

## 1. 现状(实测量化)

pprof `inuse_space`(tip≈28.5M 时):
| 分配点 | live | 占比 | 说明 |
|---|---|---|---|
| `chainView.setTip` | 217MB | 45.9% | 两视图按高度索引的 backing array,随 tip 线性涨,`pruneBelow` 只置 nil 不缩容 |
| `newUtxoCache` | 100MB | 21.2% | UTXO 验证缓存,连接区块后还会涨 |
| leveldb memdb | 32MB | 6.8% | 写缓冲 |
| `blockIndex.addNode` | 26MB | 5.5% | **窗口已钳住**(~50k 节点)✓ |
| 其余(sigcache/treap/big.Int 等) | ~100MB | ~20% | |

- **live 堆 ≈ 473MB**,但 **RSS ≈ 1.25GB**。差值 ~780MB = Go heap arena 高水位(本轮已分配/回收 ~5.4GB blockNode,arena 未归还 OS)+ DB 文件映射缓存。
- `GOGC=10` **已启用**(`btcd.go:444`,未设环境变量时 `debug.SetGCPercent(10)`)。所以问题不是 GC 频率,而是:① chainView 数组线性增长;② 每 header 的 blockNode(~200B)+ workSum big.Int(~70B) 分配/回收尖峰;③ arena 归还滞后。

## 2. 目标

1. RSS 贴住 live 堆(1.25GB → 期望 <600MB,且不再随 tip 涨)。
2. 消除每 header 的对象分配尖峰 → GC 频率与停顿大降,arena 高水位消失。
3. chainView 内存从"随 tip 线性"变"窗口恒定",与 tip 高度解耦。

## 3. 改动清单(含用户指定调整)

### A. `headerFlushBatchSize` 10000 → 20000(用户指定,`accept.go:20`)
- 批量落盘事务频率减半;`forceEvict` 从每 10k 头一次变为**每 20k 头一次**;`FreeOSMemory` 触发点(见 B)随之按 20k 头节奏。
- 影响:index 上界 = 窗口(50000)+ 一个批量(20000)≈ 70k 节点 ≈ 18MB,无碍;重启重下上限从 ≤10k 翻倍到 ≤20k 头(可接受,header 同步中重启本就罕见)。

### B. IBD 期周期 `debug.FreeOSMemory()`(`accept.go:302-305` 批量落盘后)
- 触发:每次批量落盘成功(即每 20k 头)后,**异步** `go debug.FreeOSMemory()`;用 `time.Since(lastFree) >= 15s` 合并,防爆刷期(高 h/s 时 20k 头可能 <15s)触发过频。
- 位置:`maybeAcceptBlockHeader` 的 flush 分支(`accept.go:305` 成功后),import `runtime/debug`。
- 代价:每次全量 GC STW 约 10-50ms(473MB live,GOGC=10);20k 头 @600h/s = 33s 一次 → 占比 <0.1%;CPU 余量(0.9/4 核)足够。
- 效果:arena 每次 flush 后归还 OS → RSS 贴 live 堆,不再残留 780MB 高水位。

### C. `sync.Pool`:复用 `workSum *big.Int`(安全)+ `blockNode`(带冷缓存复位钩子)

**C1. workSum big.Int 池(完全安全,必做)**
- 依据:`initBlockNode`(`blockindex.go:142`)`node.workSum.Add(parent.workSum, node.workSum)` 只写本节点自己的 big.Int,**不与 parent 别名**;冷节点各自 `workSum: new(big.Int)`(`coldread.go:143`),不引用窗口节点的 workSum。→ 池化零副作用。
- 实现:
  - `blockindex.go` 加 `var blockWorkPool sync.Pool`。
  - `initBlockNode`:`ws := blockWorkPool.Get(); if ws==nil { ws = new(big.Int) }`;算好后替换 `node.workSum`;**若复用节点(见 C2)被覆盖前有旧 workSum → 先归还**。
  - 归还点:见 C2 的 evict 流程。

**C2. blockNode 池(收益最大,需冷缓存复位钩子)**
- 唯一安全归还点:**`evictWindow`(`blockindex.go:567`)把节点从 `bi.index` 删除之后**。此时不变量全部成立:
  1. 已从 `bi.index` 删除(删除循环);
  2. 两视图槽位已 `pruneBelow` 置 nil(视图 prune 之后);
  3. 在窗口内节点的 parent/ancestor 指向该节点的指针已被第二遍 severed 清掉;
  4. `dirty` 已在 `finishFlushLocked` 清空。
- ⚠️ **必须额外处理:冷读缓存别名**。`materializeColdNodeWithTx`(`coldread.go:160-162`)会给 DB 物化的冷节点挂上"仍在内存的 parent 指针"。若该 parent 之后被 evict 并入池复用,冷节点(FIFO 64 条仍存活)会经 `parent` 误读复用后的新节点 → 损坏。内存节点本身不会进 coldCache(`coldread.go:122` 先查内存直接返回),**只有 parent 指针这一个方向**有别名。
  - 缓解:**evict 时整清 coldCache**。`blockIndex` 加可注入回调 `onEvicted func([]*blockNode)`(仿 §18#14 的 `bestHeaderNode` 注入方式,`chain.go New` 注入 `b.coldCache.reset()`)。64 条重物化成本可忽略。
- 实现:
  - `blockindex.go`:`var blockNodePool sync.Pool`;`newBlockNode`(150):`node := blockNodePool.Get(); if node==nil { node=&blockNode{} }`;若 `node.workSum!=nil` 先 `blockWorkPool.Put(node.workSum)`;`initBlockNode` 整结构覆盖(`*node = blockNode{...}` 自动清零全字段)。
  - `evictWindow` 删除循环内收集 `evicted []*blockNode`;视图 prune 后调 `bi.onEvicted(evicted)`(清 coldCache)→ 逐个 `blockWorkPool.Put(n.workSum)`、`blockNodePool.Put(n)`。
- 收益:每 header 的对象分配从 ~270B 降到 ~0;~5.4GB/轮的分配回收消失;GC 几乎不跑;arena 高水位消失。
- 风险与护栏:见 §6。

### D. chainView 偏移视图(offset + 回收,`chainview.go`)

**问题**:`nodes []*blockNode` 按**绝对高度**索引(`setTip` 分配 `make([]*blockNode, needed, needed+approxNodesPerWeek)`,`needed=tip+1`),backing array 随 tip 涨到 43.8M×8B≈350MB/视图;`pruneBelow`(184-186)只把下沿置 nil,**不缩容**。

**设计**:struct 增加 `base int32`,数组只覆盖 `[base, tip]`,索引统一 `nodes[h-base]`:
| 访问点 | 现状 | 改后 |
|---|---|---|
| `setTip` 写槽位(144-145) | `c.nodes[node.height]` | `c.nodes[node.height-c.base]` |
| `nodeByHeight`(242) | `c.nodes[height]` | `c.nodes[height-c.base]`(越界判 `height-base`) |
| `blockLocator`(448) | `c.nodes[height]` | `c.nodes[height-c.base]` |
| `pruneBelow`(192) | `c.nodes[i]=nil`(i 从 0) | 推进 `base`;**紧凑**:当 `base` 前进超过阈值(如 ≥ window,即下沿全空)时,分配 `len-tail+slack` 新数组拷贝尾部,`base` 随之下移;否则仅置 nil(摊还 O(window)/窗口) |
| `height()`(217) | `len(nodes)-1` | `c.base + len(c.nodes) - 1` |
| `setTip` 扩容(132-135) | `needed=tip+1` | 只需保证 `tip-base ≤ cap`,分配量 = 窗口+slack |

- 语义不变:导出方法 `Height()/NodeByHeight()/Contains()/Equals()/FindFork()/BlockLocator()` 全部按绝对高度对外,内部做 `-base` 转换;`equals` 比较也要含 `base` 或改为 tip+高度表长度一致。
- 收益:217MB live → 两视图各 ~窗口×8B(50k → ~400KB),**内存与 tip 解耦**;同时把 GC 目标堆从 ~520MB 压到 ~300MB,连带 B/C 效果叠加。
- 回归面:chainView 全部调用点(见上表)+ `evictWindow`/`initChainState`/`findFork` 等外部对 view 的操作;需重跑窗口/冷读/reorg 测试。

## 4. 顺序与联动

| 步 | 内容 | 验证 |
|---|---|---|
| 1 | A(常量 20k) | `go build ./...` + 相关单测 |
| 2 | B(FreeOSMemory) | 冒烟:proc-mem.csv 确认 RSS 每次 flush 后回落 |
| 3 | C1(workSum 池)→ C2(blockNode 池 + onEvicted 钩子) | 重点:`TestHeaderWindowEviction`、`TestColdReadFallback`(指针身份/severed parent)、`TestFlushToDB`、`TestBestHeaderState` |
| 4 | D(偏移视图) | blockchain 全量 + `TestParallelHeaderSync` + 主网冒烟 |

A/B/C/D 代码面独立(accept.go / blockindex.go / chainview.go + chain.go 注入),可顺序合入、每步可回退。

## 5. 验证计划

- 单元:`go test -tags=rpctest ./blockchain/...`(窗口驱逐边界、冷读指针同一性、severed parent、flush 原子性、并行 header sync)。
- 回归:全库 failure 集应与当前基线一致(PowLimit fixture 除外,`PENDING_TESTS.md` 已记录)。
- 主网冒烟(`--headerwindow=50000`,现网 datadir):
  - RSS 曲线:期望从 ~1.2GB 平台 → <600MB 且随 flush 回落;
  - 速率:不低于现状(~600 h/s 均线)且无逐 flush 掉帧;
  - `proc-mem.csv` / `go tool pprof -inuse_space` 复核三处分配点消失/收敛。

## 6. 风险

| 风险 | 等级 | 缓解 |
|---|---|---|
| 池化指针身份错乱(视图复用/冷读 parent 别名) | 最高 | 归还点唯一 = evict 后;onEvicted 清 coldCache;debug 构建加断言(归还前 `LookupNode(hash)==nil` 且两视图 contains==false) |
| D 回归面大(chainView 全调用点) | 中 | 分步提交;window=1 极窄窗口跑 simnet 全链 + reorg;`Height()`/`NodeByHeight()` 语义单测 |
| FreeOSMemory 频率过高致 STW | 低 | 15s 合并间隔 + 异步触发;`GOGC`/间隔做成常量可调 |
| flush 20k 重启重下上限翻倍 | 低 | header 已落盘,重下 ≤20k 头,代价可忽略 |
| 池对象被 in-flight 引用 | 低 | in-flight 持 `wire.BlockHeader`(80B 值),非 blockNode;dirty 在 evict 前已清 |

## 7. 不做/留待

- UTXO 缓存(100MB+)为块连接期固有,不动。
- `GOGC=10` 维持;若要更省 CPU,可评估 `GOGC=100 + GOMEMLIMIT=4GiB` 组合(本方案落地后 live 堆已很小,收益有限)。
- 块下载期的 `blockSlice`/`requestedBlocks` 内存优化留待 block 阶段观察。
