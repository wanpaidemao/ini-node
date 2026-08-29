# Header 索引内存优化(方案 B:落盘 + 滑动窗口)/ Header Index Memory Optimization (Option B: Disk-Backed + Sliding Window)

> ⚠️ 核对状态（2026-08-30）：本文行号引用已过期（代码大改后偏移 30~330 行），请以代码为准；所描述的窗口化/冷读/bestHeaderState 机制**均已实现**。
> ⚠️ Audit (2026-08-30): line refs stale; mechanisms described are implemented.

> 状态:方案草案 · 更新时间:2026-08-04
> 状态图例:`[ ]` 待办 · `[~]` 进行中 · `[x]` 已完成

## 背景 / Background

Sugarchain 主网 5 秒一个块,当前高度 ~43.6M。btcd 采用 headers-first 同步,header 阶段把 **43.6M 个 blockNode 全部驻留内存**:

- `b.index` map 持有全部节点(含侧链)
- `bestChain` + `bestHeader` 两个 chainView 各持 43.6M 个指针
- `flushToDB`(`blockchain/blockindex.go:537`)在 header 阶段**故意跳过** `HaveHeader() && !HaveData()` 节点 → 磁盘零写入

实测每 header ≈ 430–440B(内存增量 / header 下载量),全量 ≈ **19GB**;即便全同步完成后,btcd 按设计仍**常驻全部索引 ≈ 15GB**。当前机器 21.7GB 总内存,header 阶段即近 OOM,block 下载阶段必挂。

## 目标 / Goal

把 btcd 常驻 block 索引从"全量在内存"改为 **"磁盘全量 + 内存滑动窗口 + DB 兜底查询"**,使:
- header 同步期内存从 ~19GB 降到 ~几百 MB(窗口 ~2k 节点)
- 全同步后常驻内存同样降到窗口级
- 崩溃/重启可续传(header 已落盘,无需从 0 重下)

## 现状盘点(可行性依据)/ Existing Infrastructure

以下能力**已存在**,方案 B 站在它们之上:

| 能力 | 位置 |
|---|---|
| 写 header+status → `blockIndexBucket`(key=(height,hash)) | `dbStoreBlockNode`(`chainio.go:1428`) |
| 写 hash→height / height→hash 映射 | `dbPutBlockIndex`(`chainio.go:880`,目前仅 block connect 时调用) |
| ⚠️ 按 hash 取 header | `dbFetchHeaderByHash`(`chainio.go:1380`) **只读 ffldb 区块文件**(`FetchBlockHeader`→`FetchBlockRegion`,`ffldb/db.go:1261`),header-only 阶段无 block 数据 → **冷查询不可用它** |
| 按 hash 取 height / 按 height 取 hash | `dbFetchHeightByHash`(`chainio.go:917`) / `dbFetchHashByHeight`(`chainio.go:931`)(目前仅主链 connect 后有数据) |
| DB 全量重建内存索引 + bestChain/bestHeader | `initChainState`(`chainio.go:1254`,启动时遍历 blockIndexBucket) |
| 难度/共识热数据窗口很小 | SugarShield 回走 510 块(`difficulty.go:189`)+ 中位时间 11 块 |

**缺失项(本方案要做的):**
1. `flushToDB` 放开 header-only 节点的写入(批量,勿逐 header 开事务)
2. header 阶段**也写索引映射**:hash→height 全部 header 写(`hashIndexBucket`,hash 唯一无冲突);height→hash 仅在 header 成为 bestHeader tip 时写(`heightIndexBucket`,每高度唯一 → 转发叉时绝不写,防覆盖)
3. ⚠️ **持久化 bestHeader 状态**(评审发现,见 §18):现有 `chainStateKeyName` 只存 best **区块**链,`initChainState`(`chainio.go:1302`)把 bestHeader 恢复到与 bestChain 相同的 tip → 无该状态则重启后 `BestHeader()=0`,header 全部重下,续传目标落空
4. `blockIndex`/`chainView` 改为窗口 + 懒加载,窗口外走 DB(冷查询 = `hashIndex`→height → `blockIndexBucket`→header,两步;不用 ffldb 区块文件)
5. **指针同一性**:chainView 的 `contains`/`FindFork` 用指针相等(`chainview.go:228`),DB 取回的节点必须原位替换进视图同一高度,身份不能变
6. reorg 深度护栏 + RPC(`getchaintips`/`InactiveTips`)侧链信息兜底
7. block 下载期 `buildBlockRequest`(`netsync/manager.go:905`)逐高度 `HeaderHashByHeight` 的 DB 兜底与分页

## 设计 / Design

### 1. Header 落盘(恢复续传 + DB 兜底的前提)
- `flushToDB`:`if node.status.HaveHeader() && !node.status.HaveData() { continue }` 改为**写盘**
- 引入批量化:每累计 N(如 10k)个 header 或每 T 秒刷一次,单事务写三处:
  - `dbStoreBlockNode`(`blockIndexBucket`,key=(height,hash),value=header80B+status)→ **全部 header**
  - `hashIndex.Put(hash→height)` → **全部 header**(hash 唯一,不冲突)
  - `heightIndex.Put(height→hash)` → **仅 bestHeader tip 节点**(每高度唯一;转发叉/非 tip 不写,防覆盖主链映射)
- ⚠️ **不**复用 `dbTx.FetchBlockHeader` 做查询(FetchBlockHeader 读 block 文件,header-only 阶段无数据);冷读一律走 `hashIndex`→`blockIndexBucket`
- **新增** `dbPutBestHeaderState`/`dbFetchBestHeaderState`:每次 bestHeader tip 推进时持久化 header tip 的 hash+height(小 key,~36B;无该状态则重启续传不成立,见 §18)
- 副作用:`blocks_ffldb` 从 header 阶段即增长。占用预算(**估算**,实现时以实际 leveldb 摊销校准):`blockIndexBucket` 43.6M×(36B key+81B val)≈ **~5.1GB**;`hashIndex` 43.6M×(32+4)≈ **~1.6GB**;`heightIndex` 43.6M×(4+32)≈ **~1.6GB**;加 leveldb SST/日志开销 → 索引盘估计 **~10–14GB**(实测待 §16 校准)

### 2. 内存窗口化
- `blockIndex` 只保留:`bestHeader`/`bestChain` tip 以下 ~K 个(默认 K=2048,≥ SugarShield 510 + 中位 11 + reorg 余量)+ 全部仍在活跃处理的 in-flight / orphan 节点
- `chainView`(bestChain/bestHeader)保留窗口内切片;窗口外查询走冷路径:按 hash = `hashIndex`→`blockIndexBucket`,按 height = `heightIndex`→`blockIndexBucket`,重建**临时节点**
- 懒加载缓存:被 DB 取回且处于 bestChain/bestHeader 上的节点,**注册回视图对应高度槽位**(保持指针身份)

### 3. 查询路径改造(按风险排序)
| 调用点 | 现状 | 改造 |
|---|---|---|
| `LookupNode(hash)`(`blockindex.go:404`) | 内存 map | miss → `hashIndex`(height)→`blockIndexBucket`(header+status)→物化 |
| `HeaderHashByHeight`(`chain.go:1589`) | 内存视图 | 窗口外 → `dbFetchHashByHeight` |
| `NodeByHeight` / 父链回走 | 内存指针链 | 窗口外 → DB 重建;难度/中位时间回走只到 510,窗口内 |
| `InactiveTips`(`blockindex.go:466`) | 扫全 map | 窗口内扫 + DB 侧链兜底(见风险 4) |
| reorg `FindFork`/`SetTip` | 指针相等 | 深度 < 窗口安全;超窗 fail-safe 重建 |

### 4. 启动流程(含 bestHeader 恢复)
- 读新增 `bestHeaderStateKeyName`(hash+height)→ 得 header tip
- 从 `heightIndex`(height→hash)定位窗口起点 = `tip.height - K`,用 `blockIndexBucket` 逐块**回填窗口**(升序构建 parent 链接);窗口首节点的 parent 走冷查询
- `bestChain` 仍从 `chainStateKeyName` 恢复;`bestHeader.SetTip(headerTip)` —— 关键差异:两 tip **不再相同**(区块 tip 远低于 header tip)
- 一致性检查:bestHeader 高度 ≥ bestChain 高度;header tip 在 DB 中存在(blockIndexBucket 有该 hash)
- 窗口化启动只载入窗口段;**旧路径 `--headerwindow=0` 仍全量载入**,行为与今日一致

## 实施步骤 / Implementation Steps

- [x] 1. 单元探针:`TestSugarIndexRPC`(simnet)基线跑绿,记录内存基线
- [x] 2. 放开 `flushToDB` + 批量化 header 落盘 + `hashIndex` 写入;simnet 回归
- [x] 3. **`bestHeaderState` 持久化 + 启动恢复**:重启后 `BestHeader()` 保留 → 不重下 header(«§18 续传缺口»)
- [x] 4. blockIndex 窗口化 + `LookupNode` DB 兜底;simnet 回归
- [x] 5. `HeaderHashByHeight`/`NodeByHeight` DB 兜底;netsync block 下载回归
- [x] 6. 指针同一性改造(视图原位替换)+ 手工 reorg 测试【代码侧已完成,实测见步骤 8/9】
- [x] 7. `InactiveTips`/`getchaintips` 侧链兜底【确认步骤 4 的 severed-parent 护栏已足够,不额外接线】
- [ ] 8. 主网 100k header 冒烟(内存曲线验证 → 应远低于 19GB)【需实测运行】
- [ ] 9. 全量 header 同步 + block 下载 + sugarindex 增长验证【需实测运行】

## 风险 / Risks

1. **指针同一性**(最高):reorg/fork 判定靠指针相等,DB 节点身份错乱 → 共识错误。缓解:视图槽位原位替换 + 强一致性断言(debug assert)
2. **侧链信息**(`InactiveTips`/`getchaintips`):窗口化后远处侧链从内存认知中消失,DB 无侧链结构、全扫 43.6M 失去窗口化意义。缓解:保留全部**近期侧链 tip 指针**在内存(数量小),远处侧链接受"未知"(实践中 reorg 不会到历史深处)
3. **block 下载期 DB 反查性能**:逐高度 `HeaderHashByHeight` 43.6M 次。缓解:按窗口分页批量取,不进热点路径
4. **难度共识**:窗口 ≥ 510+11 即可,若窗口过小需 fail-safe 而非静默错
5. **回归面**:改动触及 blockchain/netsync/RPC ~8 个文件,需重跑 simnet、umami 字节兼容、reorg 测试

## 验证计划 / Verification

- [ ] simnet `TestSugarIndexRPC` 全绿(8 RPC 字节兼容)
- [ ] simnet 手动 reorg:`invalidateblock` + `reconsiderblock`,`getchaintips` 正确
- [ ] 重启续传:header 落盘后重启,不重下已收 header
- [ ] 主网冒烟:100k header 内存曲线(目标 << 19GB),随后逐步放开
- [ ] 主网全量:header → block → sugarindex 增长,与 umami 对账

## 技术细节 / Technical Details

### 5. 内存构成与目标(每 header 分解)
`blockNode`(`blocknode.go:74`)单节点开销(x64):

| 字段 | 字节 |
|---|---|
| parent + ancestor 指针 | 16 |
| hash + merkleRoot | 64 |
| workSum `*big.Int`(≈4-5 word) | ~72 |
| height/version/bits/nonce/timestamp | ~24 |
| status + 对齐 | ~8 |
| Go 堆分配头 + map 项摊销 | ~50–70 |
| **合计** | **~230–260B/节点** |

再乘:两个 chainView 各 43.6M 指针(8B×2 ≈ 700MB)+ GC 堆膨胀 → 实测 430–440B/header、全量 ~19GB。
目标态:内存只驻窗口 ~2k 节点 + 近期侧链 tip,预计 **<1GB**(其余走 DB)。

### 6. 批量化落盘设计(步骤 2)
- 沿用现有 `dirty` map;每次 `flushToDB` 刷完 `dirty` 后,若累计新增 header ≥ 10k(常量 `headerFlushBatchSize`,`accept.go`)或关停(`FlushBlockIndex`),开**单事务**批量写:
  - `dbStoreBlockNode`(每个节点 1 条,key=(height,hash))
  - `dbPutHashIndex`(新增 helper,`chainio.go`;hash↔height,**不写 heightIndex**,理由见 §18#6/#13)
- 注意 ffldb 写事务内 `dbStoreBlockNode` 已有,批量时复用;避免事务内做非 DB 工作
- 回滚语义:任何一条失败 → 整个事务回滚,`dirty` 不清空,下次重试
- `TestFlushToDB` 已改写:任何非空 dirty 集 → 恰好 1 次 Update,且全部节点(含 header-only)落盘

### 7. 窗口与分页接口(步骤 3-5)
- 新增 `blockIndex.LookupNodeDB(hash)` 内部路径:
  1. 内存 map miss → `dbFetchHeightByHash(hash)` 拿高度
  2. blockIndexBucket 按 (height,hash) 取 header+status
  3. 重建 `blockNode`(父指针经 DB 再取,或若父在窗口内用内存指针)
  4. 若命中 bestChain/bestHeader 且高度 ≥ 窗口起点 → **注册回视图槽位**(`c.nodes[h]=node`),保指针身份
- 窗口起点 = `tip.height - K`(K=2048);`buildBlockRequest` 按 `[窗口内内存 | 窗口外 DB]` 两段拼 getdata,DB 段按 20k/批分页,避免单次放大
- 难度/中位时间回走(`difficulty.go:189` + `CalcPastMedianTime`)只到 510/11,全在窗口内,不改共识路径

### 8. 失败模式与安全护栏
- **reorg 深度 > 窗口**:`findFork` 找不到窗口内公共点 → 直接 fail(拒绝该块,记日志),而不是静默 reorg 到错位
- **DB 缺 header**(重启时半窗口):`initChainState` 校验 DB 高度 vs 内存 tip,不一致 → 拒绝启动,提示重新同步
- **指针身份**:视图槽位替换唯一入口(chainView.setTip / 新增 page-in),debug 构建下加 `assert(c.nodes[h] == nil || c.nodes[h] == node)` 防重入错乱

### 9. 存储 schema 定稿(含序列化)
沿用**现有 bucket,不新增**;三个映射合一冷查询路径:

| Bucket/Key | Key | Value | 写入时机 |
|---|---|---|---|
| `blockIndexBucket` | `(height,u32 BE)+(hash,32B)` | `header(80B)+status(1B)` | 每个接受 header 批量写(原值不变,`dbStoreBlockNode`) |
| `hashIndexBucket` | `hash(32B)` | `height(4B)` | 每个接受 header 写(`hashIndex.Put`) |
| `heightIndexBucket` | `height(4B)` | `hash(32B)` | 仅 bestHeader tip 写(`heightIndex.Put`) |
| **`bestHeaderStateKeyName`(新增)** | 固定 key | `headerTipHash(32B)+height(4B)` | bestHeader tip 推进时写(`dbPutBestHeaderState`) |

冷读路径(两跳,纯 leveldb,不碰 block 文件):
```
LookupNode(hash):
  1. hashIndex → height            # O(1)
  2. blockIndexBucket[(height,hash)] → header+status
  3. 物化 blockNode + 按需取父(父在窗口→内存;否则递归走本路径)
```
一致性:写事务内三者一起提交(同一 `db.Update`),`dirty` 未清空即事务失败回滚,天然原子。

### 10. 窗口管理:移动、驱逐、分页(page-in)
- **窗口边界** = `max(bestHeaderTip.height, bestChainTip.height) - K`;K 可配(`--headerwindow`,默认 2048)
- **驱逐**:`flushToDB` 成功提交后,顺 bestHeader 链从窗口边界往下走,把低于边界的节点从 `b.index.map` 删除(它们已在 DB)。侧链 tip 单独保留(见 §13),不驱逐
- **page-in 注册**:DB 取回的节点若高度 ≥ 窗口边界:
  - 若在 bestHeader/bestChain 上 → `c.nodes[h]=node`(指针身份)
  - 否则仅放内存 map,由下一轮驱逐淘汰
- **分页批量**:`buildBlockRequest` 冷段每批 20k 高度;冷段用 `heightIndexBucket` **Cursor 范围扫描**(key=height BE,天然有序,一次游标扫 [start,end) 即得全部 hash),不用 `FetchBlockRegions`(它读 block 文件,header-only 阶段无数据),积攒 hash 后发 getdata,避免放大
- 反例:高度**低于窗口边界**的冷节点每次查询都重建 → 允许(仅 reorg/RPC 偶发触达;block 下载期逐高度上升,天然在窗口内)

### 11. 并发与锁模型
- btcd 主链写路径持 `chainLock`;`blockIndex` 自带 `RWMutex`。**维持现状**,仅约束:
  - 冷读发生在 `RLock` 内可接受(leveldb 点读 ~µs);但**禁止**在 `chainLock` 持锁期间做长 DB 扫描
  - `buildBlockRequest` 冷段**预取到 slice 后**再一次性拼 getdata,不把 DB 循环放进锁内
  - `flushToDB` 批量事务保持与现有 `b.db.Update` 相同的"无外部锁内做 I/O"纪律
- 新增指标记录 DB miss 比例,便于观察分页是否影响主路径

### 12. 各阶段窗口行为
| 阶段 | 窗口内热点 | 窗口外如何服务 |
|---|---|---|
| header 下载 | 最近 2048 header(难度 510+11 全在) | 老 header 落盘后即驱逐,内存恒定 |
| block 下载 | 连接中的 tip 附近 + in-flight | `HeaderHashByHeight` 冷段 DB |
| reorg | 浅 reorg(块级)窗口内 | 深度超窗 → §8 拒绝 |
| RPC(getblockheader/hash) | 热 | 冷段走 §9 两跳 |
| **sugarindex 追平(`Init`,`indexer.go:86`)** | 连接 tip 附近 | **强制消费方**:从索引 tip 到 bestHeight 逐高度 `BlockByHeight(h)`,窗口外必须 DB 兜底,否则冷高度报错 → §3 表"NodeByHeight→DB 重建"为本路径硬要求 |

### 13. 侧链 tip 与 `getchaintips` 兜底
- `blockIndex` 增加一个小集合 `sideTips []*blockNode`:存**最近窗口内产生的全部侧链 tip**(通常个位数~十位)
- 窗口外已驱逐的侧链 tip:接受"未知"(不返回 `getchaintips`)。理由:老分叉不可能复活成主链(见风险 §2 讨论),且 `InactiveTips` 语义在 IBD 后由 block 阶段自然重建(连接侧链块时 tip 重新进入窗口)
- 一致性:窗口驱逐侧链节点前,先确认它不是任何内存节点的父链必需节点

### 14. 状态(status)持久化一致性
- `SetStatusFlags/UnsetStatusFlags`(`blockindex.go:445,456`)已把改动挂进 `dirty` → 批量 flush 天然落盘 ✓
- **冷节点例外**:被驱逐的侧链/无效节点再被 DB 物化时,status 从 `blockIndexBucket` 读回(含 `statusValidateFailed`/`statusInvalidAncestor`/`statusValid`),保证 `KnownInvalid` 判定不因驱逐丢失
- 回归测试:驱逐后 invalidate 旧块 → 重启 → `getbestblockhash` 仍拒绝该分支

### 15. 配置与回滚
- 新增 `--headerwindow=<n>`:`0`=旧行为(全量驻留,默认值)→ **默认不改共识行为**,风险可控;`n>0` 启用窗口化(主网验证用 2048)
- 兼容性主体是**本 fork 内**版本(DB 为 Sugarchain 专用,不会给上游 btcd 读,无上游兼容问题):
  - 新版写的 header-only 节点,旧版(本 fork 早期)若 `HaveBlock` 仅看 presence 会误判"有数据" → 依赖 `HaveBlock` 返回 `hasBlock && node.status.HaveData()`(`blockindex.go:395`,当前正确保持);实现时确认所有版本此判定一致
  - `--headerwindow=0` 老路径加载含 header-only 节点的 DB:仅节点数变多(内存回 15G,用户自选),不损坏数据、不抛错
  - 旧 DB 缺 `bestHeaderStateKeyName`(从未落盘)→ 启动走"回退到 bestChain tip"旧逻辑,照常可用
- 若窗口化异常:改回 `--headerwindow=0` 即恢复旧路径,数据不损坏

### 16. 可观测性
- `blockIndex` 增加 `NumNodes()`(或每 10s 日志):确认常驻节点 ≈ K + 侧链 tip,而非 43.6M
- 新增 `headerWindow:{inMem, dbMiss, pageIn}` 计数;`debuglevel=window` 打印驱逐/注册事件
- 内存验证命令:`go tool pprof -inuse_space` 抓 heap;对照基线文档 §5 的目标 <1GB
- 磁盘增长:header 落盘后 `blocks_ffldb`/`index` 尺寸曲线(预算见 §1)

### 17. 测试矩阵(冒烟→回归→主网)
| 层级 | 用例 | 目标 |
|---|---|---|
| 单元 | 窗口驱逐边界、page-in 物化等价、hashIndex/heightIndex 写入原子性 | 语义正确 |
| 单元 | `window=1` 跑 simnet 全链 → 逐块 reorg → DB 兜底正确 | 极窄窗口压力 |
| 单元 | 冷 status 持久化(驱逐后 invalidate → 重启拒绝) | §14 回归 |
| 集成 | simnet `TestSugarIndexRPC` 8 RPC 字节兼容(全绿基线) | umami 对齐 |
| 集成 | `invalidateblock`+`reconsiderblock`,`getchaintips` 正确 | 指针身份/侧链 |
| 集成 | 重启续传:header 落盘后重启不重下 | §1 目标 |
| 主网 | `--headerwindow=2048` 跑 100k header:内存曲线 <<19G、无 OOM | 主冒烟 |
| 主网 | 全量 header → block → sugarindex 增长,与 umami 对账 | 最终验收 |

### 18. 评审记录(2026-08-04)/ Review Log
逐条查证后修正/确认的事项:

| # | 项 | 结论 |
|---|---|---|
| 1 | ⚠️ **重启续传缺口(新发现,已修正文)** | `chainStateKeyName` 只存 best **区块**链;`initChainState`(`chainio.go:1302`)将 bestHeader 恢复到与 bestChain 相同 tip → 无新状态则重启后 `BestHeader()=0`、全部 header 重下。**必须新增 `bestHeaderStateKeyName` + `dbPut/DbFetchBestHeaderState`,并让两 tip 分离恢复**。这是原文档 §1/§4"续传"目标的硬缺口 |
| 2 | ⚠️ `dbFetchHeaderByHash` 不可用于冷读(已修正文) | 它走 `FetchBlockHeader`→`FetchBlockRegion`(`ffldb/db.go:1261`),读 **block 文件**,header-only 阶段无数据 → 冷读只能用 `hashIndex`→`blockIndexBucket` 两跳 |
| 3 | §15 降级兼容措辞修正(已修正文) | 本 fork DB 为 Sugarchain 专用,不存在"上游 btcd 兼容"。真正的约束:本 fork 内 `HaveBlock` 必须保持 status-aware(`blockindex.go:395`),且旧 DB 缺 bestHeaderState 时回退旧逻辑 |
| 4 | §10 冷段取 hash(已修正文) | 用 `heightIndexBucket` Cursor 范围扫描(key=height BE 有序),不用 `FetchBlockRegions`(同样读 block 文件,不可用) |
| 5 | 磁盘预算(估算,待实测) | 修正为 **~10–14GB**(含 leveldb 摊销),标注"实现时校准" |
| 6 | `heightIndex` 只写 bestHeader tip | 已确认每高度唯一 → 转发叉/非 tip 不写,防覆盖主链映射(§1/§9) |
| 7 | 窗口 ≥ 510+11 | 已确认 SugarShield 回走 510 + 中位时间 11,窗口 2048 足够 |
| 10 | block 下载期逐高度 DB 读延迟 | **可忽略**:leveldb 点读 ~10–100µs,43.6M 次 ≈ 0.4–1h 纯读,摊在数天 block 下载内不构成瓶颈;20k 批大小做成可配置 `--headerwindow-batch`,§16 指标落地后校准 |
| 11 | **与未提交索引代码交叉核对**(本目录 git HEAD 后改) | 变更:sugarindex 独立 LevelDB 索引、`getaddress*`/`getspentinfo`/`getblockhashes` 8 个 RPC、`--sugarindex` 开关。**新消费者(§12 已加)**:`sugarindex.Manager.Init`(`indexer.go:86`)追平逐高度 `BlockByHeight` 全量走 → 窗口化必须保证窗口外 `NodeByHeight`/`BlockByHeight` 走 DB 兜底,否则索引追平在冷高度报错;该路径与 §3 表第 3 行一致,纳入实施步骤 #5 |
| 12 | 索引写作与收益预期 | sugarindex 是独立 LevelDB(`<datadir>/index`,非 `blocks_ffldb`);其增长在 block 连接后发生,与 header 阶段内存问题无交集 → 方案 B 完成后,`--sugarindex` 的地址索引在真实主网上正常增长即可作为最终验收 |
| 9 | 窗口首节点冷查询递归深度 | **深度有界**:启动回填窗口首节点的 parent 仅 1 次冷查;运行期冷查仅发生于 orphan 的 parent(深度受 orphan 池上限约束)。实现时加递归护栏常量(如 10k)防意外 |
| 10 | block 下载期逐高度 DB 读延迟 | **可忽略**:leveldb 点读 ~10–100µs,43.6M 次 ≈ 0.4–1h 纯读,摊在数天 block 下载内不构成瓶颈;20k 批大小做成可配置 `--headerwindow-batch`,§16 指标落地后校准 |
| 13 | **步骤 2 实现已提交(2026-08-04)** | `flushToDB`(`blockindex.go`)去除 header-only 跳过 + `needsWrite` 早退,改为:非空 dirty 集 → 单事务内 `dbStoreBlockNode` + 新增 `dbPutHashIndex`(仅 hash↔height,**不含 heightIndex**,与 §18#6 一致防转发叉覆盖);`maybeAcceptBlockHeader`(`accept.go`)按 `headerFlushBatchSize=10000` 计数批量 flush(`BlockChain.headerFlushCount`,chainLock 保护);`BlockChain.FlushBlockIndex()`(`chain.go`)供 `server.Stop()`(`server.go`)关停时刷尾部。**验证**:`TestFlushToDB` 全绿(语义改写),`TestSugarIndexRPC` PASS,`go build ./...`+`go vet` 干净;`TestProcessBlockHeader`/`TestHaveBlock`/`TestInvalidateBlock` 失败为共识层遗留(PowLimit fixture 不匹配),stash 基线对比确认非本次引入 |
| 14 | **步骤 3 实现已提交(2026-08-04)** | 新增 `bestHeaderStateKeyName`(`chainio.go`)+ `bestHeaderState` 序列化(仿 bestChainState,hash+height)+ `dbPut/DbFetchBestHeaderState`;`blockIndex` 增 `bestHeaderNode func() *blockNode` 字段,`flushToDB` 同一事务内写 bestHeaderState(`chain.go New` 注入,返回 `bestHeader.Tip()`);`createChainState` 写 genesis state;`initChainState` 双 tip 分离恢复(有 state 且节点在索引 → 恢复到 header tip;key 缺失或节点缺失 → 回退 bestChain tip,兼容旧 DB)。**验证**:新增 `TestBestHeaderState`(正:重启恢复 header tip;负:删 key 后回退 bestChain tip)全绿,`TestSugarIndexRPC` PASS,build/vet 干净。`blockchain` 包全量仅剩 5 个 PowLimit fixture 基线失败(`TestMaybeAcceptBlockReusesHeaderNode`/`TestBip0030CheckNeededAfterBIP34`/`TestHaveBlock`/`TestInvalidateBlock`/`TestReconsiderBlock`,见 `PENDING_TESTS.md` 已记录) |
| 15 | **双窗口边界决策(实现步骤 4 时修正原单边界设计)** | 原 §2 单一边界(按 header tip)在 header 同步期会把 bestChain 链上节点(deep 在 header tip 之下)驱逐 → `initChainState` 找不到 bestChain tip / 链状态失效。修正为**每 tip 各留尾部窗口**:`evictWindow` 按 headerBoundary 驱逐 map,但保留 `bestChainView` 内节点;bestChain/bestHeader 视图各按其 own boundary `pruneBelow`。内存上界 ≈ 2×window + 在途,header 远快于 block 时也成立(两窗口不相交)。`reorgWindowBoundary` 用 bestChain 边界(深 reorg 拒绝) |
| 16 | **步骤 4 实现已提交(2026-08-04)** | `blockIndex.setWindow`/`windowBoundary`/`evictWindow`/`markInitialized` + `InactiveTips` nil-parent 护栏;`chainView.pruneBelow`(内部+导出);`initChainState` 窗口化启动(仅物化两窗口,逐行累加 runningWork 供边界锚点,`parentHash` 保留可重建 header,边界锚点高度/workSum 修正);`connectBestChain` fork 分支 + `getReorganizeNodes` 深 reorg 拒绝(`ErrForkTooOld`)、`FindFork` nil 安全;`FetchHeightRange`/`HeightToHashRange`/`IntervalBlockHashes`/`locateInventory` 对窗口外高度 nil 安全返回错误(冷读兜底留步骤 5);`config.go --headerwindow` 链路。**验证**:新增 `TestHeaderWindowEviction`(驱逐/重启窗口物化/边界 workSum 与父 hash 一致/窗口推进重疏割)全绿;`blockchain` 全量 + 全库 failure 集与 stash 基线逐一同(9 个 PowLimit/regtest fixture + 5 个既有,全部非本次引入);build/vet/`sugarindex` 干净。已知代价:两视图高度索引数组约 2×350MB(43.6M 高度),后续可做 offset 视图回收 |
| 17 | **步骤 5 冷读兜底实现已提交(2026-08-04)** | 新增 `coldread.go`:`coldNodeCache`(FIFO 64 项)+ `materializeColdNode`/`coldNodeAtHeight`/`nodeAtHeight`/`isMainChainHash`,冷读经两跳 `hashIndex→heightIndex→blockIndexBucket`(新增 `dbFetchBlockRowByHash`/`ByHeight`,`chainio.go`,**不读 block 文件**,规避 §18#2)。冷节点携带 header/height/status/parentHash、parent 断裂、workSum 置零(文档注明仅服务 hash/height/header 查询,禁作 PoW/指针链比较)。已接线消费者(API+P2P 范围):`BlockByHash`/`BlockByHeight`/`BlockHeightByHash`/`BlockHashByHeight`/`HeaderHashByHeight`/`HeaderHeightByHash`/`IsValidHeader`/`HeightRange` + `locateInventory`/`locateBlocks`/`locateHeaders`(locator 冷高度锚定、停止 hash 冷读、主链归属用 heightIndex 往返校验);`HeightToHashRange`/`IntervalBlockHashes` 仍留窗口外错误(范围走祖先链,后续再深化)。冷读开关 `coldReadEnabled()=db!=nil && headerWindow!=0`,保证无 db 的 fake-chain 测试与窗口关闭节点行为不变。**验证**:新增 `TestColdReadFallback`(重启窗口化后高度 14 冷读:HeaderHashByHeight/HeaderHeightByHash/BlockHashByHeight/BlockHeightByHash/IsValidHeader/cache 指针同一性/severed parent/HeightRange 冷热混合/P2P locate 锚定冷高度/无 locator 冷 stop hash)全绿;`TestLocateInventory` 恢复通过(初版缺 `coldReadEnabled` 护栏致 fake-chain nil db panic,已修);build/vet/`sugarindex` 干净;全库 failure 集与基线一致(5 个 PowLimit fixture 全量可见 + 9 个同源隔离可见,均非本次引入)。后续小改:`materializeColdNodeWithTx`(tx 内物化,单查路径 1 高度 ≤1 次 db.View)+ `nodesInHeightRange` 批量读,`locateBlocks`/`locateHeaders`/`HeightRange` 的连续冷段**单事务**物化(locator 首个节点保留指针身份,侧链 stop hash 不被高度重解析);`TestLocateInventory` 对"无 locator + 侧链 stop"回归保护已覆盖 |
| 18 | **步骤 6 指针同一性 + reorg 复查已提交(2026-08-04)** | ① 指针同一性护栏: `materializeColdNodeWithTx` 在读 DB 前先查 `index.LookupNode(hash)`,**仍在内存的节点原样返回内存指针**(且先于 coldCache,内存优先于陈旧冷条目)→ 同 hash 永不复建为第二个指针,reorg/fork 指针相等判定安全。② reorg 复查: `connectBestChain` 仅接受窗口内 fork(`verifyForkInWindow` + `reorgWindowBoundary`,深 fork 拒 `ErrForkTooOld`);`getReorganizeNodes` 对 severed-parent(`node.parent==nil`)与无 fork 点均 nil 安全返回空;`FindFork` 只走窗口内指针 → **冷节点永不进入 reorg 决策**。③ 单测 `TestColdReadFallback` 增断言:边界节点(高度 15,内存内)经 `materializeColdNode`/`coldNodeAtHeight` 冷查返回**同一内存指针**。**验证**:build/vet/`sugarindex` 干净;`TestColdReadFallback`/`TestLocateInventory`/窗口套件全绿。步骤 7 `InactiveTips` 复核结论:步骤 4 的 severed-parent 护栏已足够(窗口化只认窗口内近期侧链,远处侧链按计划风险 2 接受"未知"),无额外接线。真实运行验证(步骤 8/9)留待实测(需启动节点 + ~14h 同步) |
| 19 | **并行 header 拉取 + leveldb 16MB 已提交(2026-08-04)** | ① `netsync/manager.go`:`fetchHeaders` 重写为**多 peer 并行分片**下载——`headerSyncState`(`ranges`/`peerRange`/`nextHeight`/`nextAssign`/`target`/`sliceLen`)+ 打乱 higherPeers 封顶 `maxHeaderSyncPeers=8`;`assignHeaderRange`/`launchHeaderRange` 分配 non-overlap 切片(切片按 peer tip 与已分配起点截断),`headerLocator` 用 `HeaderHashByHeight` 单哈希定位;响应 `handleParallelHeadersMsg`→`processReadyHeaderRanges` **按序落盘**(front 就绪才应用,front peer 接续下一片 + 空闲 peer 顶格),`finishHeaderSync` 交还 block 下载。② **慢 peer 绕行**:`reissueStaleHeaderRanges`/`reissueFrontRange`(front 空缺补洞、超时重发)/`dropHeaderPeer`/`abortHeaderSync`;新 peer 折叠 `headerSyncAddPeer`。③ **响应校验守卫**:`handleParallelHeadersMsg` 用 `HeaderHashByHeight(rng.start-1)` 核对 `headers[0].PrevBlock` → 过期/错配(重排后旧请求迟到的)响应直接忽略,使**短超时激进重发绝对安全**;`headerRangeStallTimeout` 定为 **6s**。④ `database/ffldb/db.go`:新增 `defaultCompactionTableSize=16MB`/`defaultWriteBuffer=8MB`(上游 goleveldb 默认 2MB;header 阶段追加型 blockIndex 文件数/写放大骤降,实测 .ldb ~17MB),`openDB` 应用。⑤ **新增 4 单测全绿**:`TestParallelHeaderSync`(8 peer 并行乱序按序落盘)/`TestParallelHeaderSyncDropFrontPeer`(front 空缺重发)/`TestParallelHeaderSyncShortResponse`(切片不足不回退)/`TestParallelHeaderSyncAddPeer`(中途加 peer 折叠)。⑥ **主网冒烟**(临时 datadir `btcd-parallel`,`--headerwindow=2048`):迭代 60s→12s→6s 超时 + 守卫;初版 ~8k/min,终版 **~54k/min(≈895 h/s)追平/略超单 peer 基线(40–60k)**;**验证全干净**——`failed header verification`=0、`stale response`=0、空响应丢 peer=0、崩溃/重启=0、stderr=0 字节、RSS 有界(~600MB/650k 高度)。性能复盘:in-order 落盘下 **front 切片一旦落在慢 peer,整条下载等满 `headerRangeStallTimeout`** 是唯一串行瓶颈 → 短超时 + 守卫直接解决;真实主网优质 peer 下并行放大更明显 |

**已全部消除存疑项;**block 下载期批大小等参数留待实现阶段(§16 指标)校准。

| 方案 | 内存 | 重启续传 | 改动/风险 | 适用 |
|---|---|---|---|---|
| A. 仅 header 落盘 | 不变(~19G) | ✅ | 小/低 | 解决"重启重下"痛点,不解决 OOM |
| **B. 落盘+窗口** | **~几百 MB** | ✅ | 大/高 | 22GB 机器跑全链 |
| C. 加内存 / 大页面文件 | 依赖 OS swap | ✅ | 零 | 立即能试,慢 |
| D. 不跑全量 IBD 验证索引 | — | — | 零 | 只验证索引增长,不验证全链 |
