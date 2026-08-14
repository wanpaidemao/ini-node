# sugarindex 重建优化方案（加速地址索引全量重建）

> 制定日期：2026-08-14
> 现状：开启 `sugarindex=1` 后节点启动时全量重建地址索引，实测约 **2.4 万块/分钟**，4385 万块全程约 **30 小时**，期间 RPC 不可用。
> 状态：**方案待定，未实施**（当前重建已在进行，不建议中途打断）。

---

## 一、现状与瓶颈回顾（已查代码）

| 位置 | 代码 | 瓶颈 |
|------|------|------|
| `sugarindex/indexer.go:86-105` | `Init`：`for height := tip+1; height <= best; height++` 逐块同步 | 单线程串行：读区块+读 spend journal+写索引，三者叠加 |
| `sugarindex/indexer.go:160-185` | `connectBlock`：每块构造 `leveldb.Batch` → `m.db.Write(batch, nil)` | 4385 万块 × 每块一次写事务，写频率极高 |
| `sugarindex/db.go:33-38` | `openIndexDB`：仅 `BloomFilter(10)`，**无 WriteBuffer/CompactionTableSize/L0 调参** | goleveldb 默认 WriteBuffer 仅 **4 MB** → 频繁刷 L0、海量小 SST、写放大严重 |
| `sugarindex/db.go:82-` | `putObfuscated`：每次写做 XOR 混淆 | 额外 CPU（相对小头） |
| 主库 ffldb | `database/ffldb/db.go:2123-2166` | 已调至 WriteBuffer 64MB / Table 64MB / Total 256MB / L0 触发 8 |

**核心差距**：sugarindex 独立 LevelDB 用 goleveldb 默认小参数（4MB WriteBuffer），而主库已调大 16 倍——这是重建慢最直接的单一原因；其次是无并行 + 每块一次写。

---

## 二、优化方案（按性价比排序）

### 方案 A：调大 sugarindex 独立 LevelDB 参数（首选，低风险，立竿见影）

**改动**：`sugarindex/db.go` `openIndexDB` 的 `opt.Options` 对齐 ffldb：

| 参数 | 现值（goleveldb 默认） | 建议值 |
|------|------------------------|--------|
| `WriteBuffer` | 4 MB | **64 MB** |
| `CompactionTableSize` | 2 MB | **64 MB** |
| `CompactionTotalSize` | 10 MB | **256 MB** |
| `CompactionL0Trigger` | 4 | **8** |

**预期**：memtable 大 16 倍 → 每次 flush 产生更少 L0 表；配合大 L1 预算，compaction 次数大幅减少，写放大从几十倍降到个位数——**重建速度有望提升 3-5 倍**（30h → 6-10h 级）。

**风险**：低。仅影响独立索引库的写入效率，不涉及共识、不改变索引字节格式（仍是 umami 兼容），不影响主库。内存增加约 60-70 MB。

### 方案 B：多块共用 LevelDB Batch（次选，中风险，需小重构）

**改动**：`sugarindex/indexer.go`：
- `connectBlock` 增加批量变体（如 `connectBlockBatch(bd *blockDeltas, batch *leveldb.Batch)`），把 `m.db.Write(batch, nil)` 从每块一次改为**每 N 块一次**（如每 100 块，约 24 万块/批 → 写事务次数降 100 倍）。
- 由于 `connectBlock` 同时被正常连接路径（`ConnectBlock`）调用，需保留原单块接口；重建路径（`Init`）走批量接口。
- Batch 大小需控制（每 100 块的条目数可能较大，`leveldb.Batch` 内存约数十 MB，可接受；或按条目数阈值 flush）。

**预期**：写事务频率降 100 倍，fsync/写放大进一步下降；与方案 A 叠加效果更好。

**风险**：中。改动 `connectBlock` 结构；需确保批量路径与单块路径结果一致（可先用方案 A 验证正确性再上 B）。崩溃中断时已提交批次保留、未提交批次重来（Init 可幂等重跑）。

### 方案 C：重建期间关闭 WAL 同步（辅助，低风险）

**改动**：`sugarindex/indexer.go` `Init` 中 `m.db.Write(batch, nil)` 改为使用 `opt.WriteOptions{Sync: false}`（goleveldb 默认即异步写 WAL，但显式声明更明确；或 `m.db.Write(batch, &opt.WriteOptions{})`）。

**预期**：省去每批 fsync，写入吞吐小幅提升。

**注意**：sugarindex 是**独立可重建索引**（非主链状态），崩溃后从上次 tip 重跑即可，关闭同步无共识风险。

### 方案 D：并行重建（远期，高风险，不建议首期）

**改动**：`Init` 按高度分片，多 goroutine 并行 `BlockByHeight` + `FetchSpendJournal` + 计算 deltas，再按高度顺序批量写库（写库单线程保证顺序）。

**预期**：利用多核，读/算并行；写仍串行（LevelDB 单写者）。

**风险**：高。spend journal 与区块读取并发、内存占用翻倍、顺序保证复杂；首期不建议。

---

## 三、推荐实施路径

| 步骤 | 内容 | 时机 |
|------|------|------|
| 1 | **方案 A**（调参 3 常量） | 下次重建/首次建库前实施，收益最大、风险最低 |
| 2 | 实测 A 后速度对比（日志 `indexed height` 速率） | A 达标（如 ≥8 万块/分钟）则停 |
| 3 | 若仍慢 → 方案 B（批量 Batch） | 需要改 `connectBlock` 结构 |
| 4 | 可选叠加方案 C（关 WAL sync） | 与 B 一起做 |

> 当前正在进行的重建（约 30h）**不打断**；优化代码先备好，供下次重建或清库重建使用。若愿意接受一次重启，可实施 A 后重启让重建从断点（index tip）继续，但 `Init` 当前是"tip 孤儿才重建"——正常重启会从断点继续而非重头，收益主要在后续日常增量与将来首次建库。

---

## 四、验证方法

1. 实施 A 后重启节点，日志观察 `Sugar index: indexed height` 速率（对比当前 2.4 万块/分钟）
2. `index/` 目录大小与 SST 文件数（`*.ldb` 数量应显著减少）
3. 重建完成后 `btcctl getaddressbalance/getaddressutxos` 输出正确
4. 若做 B：批量与单块结果一致性用同一区块样本比对

---

## 五、待办勾选

- [ ] 方案 A：`sugarindex/db.go` `openIndexDB` 加 4 参数（WriteBuffer 64M/Table 64M/Total 256M/L0 8）
- [ ] （可选）方案 B：`connectBlock` 批量变体 + `Init` 每 100 块一次 `Write`
- [ ] （可选）方案 C：重建路径显式 `WriteOptions{Sync:false}`
- [ ] 重启实测速率对比，更新本方案结论
