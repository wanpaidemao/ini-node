# umami 对齐 · P2/P3 实施文档（可行性 / 性价比 / 方案 / 实现步骤）

- 日期：2026-08-28
- 背景：`fix-plan-20260827.md` 定义了 ini-node 对齐 umami 的分层修复 P0/P1/P2/P3。P0（A1/A2 回滚检测）与 P1（header 链参照、模板 prev 网络约束、字节序加固）已随 `5b32b01f` 落地并验证；**P2（稳健性）、P3（外部锚点）尚未实施**。本文档补全 P2/P3 的可行性分析、性价比分析、方案设计与详细实现步骤，供决策与排期。
- 前置事实（已核对源码）：
  - umami 源码：`../backend/umami/src/validation.cpp`、`kernel/chainparams.cpp`
  - ini-node 源码：`backend/blockchain/{process,chain,chainio,blockindex}.go`、`backend/chaincfg/params.go`

---

## 0. 结论摘要（先看这里）

| 项 | 目标 | 改动量 | 风险 | 性价比 | 建议 |
|---|---|---|---|---|---|
| **P2-1** | 未上链网络块落盘，重启不丢、父块到位自动上链 | 中（~80 行 + 1 常量） | 中 | **高**：直接消除"网络块进孤儿池重启即丢"的结构缺陷；与 P0/P1 形成完整闭环 | **推荐实施**（下一轮） |
| **P2-2** | 失败块持久化标记，重启后不再重复校验 | 小（~40 行） | 中 | **中高**：防 DoS + 防重启后重复踩坑；但需先审计 status 持久化路径 | 推荐实施（与 P2-1 同轮） |
| **P3-1** | MinChainWork 门槛 + assumevalid | 中（~60 行） | 高 | **中**：MinChainWork 防御价值高，assumevalid 对本节点（同步已完成）收益低 | 仅 MinChainWork，视需要 |
| **P3-2** | 检查点（Checkpoints） | 大（~100 行 + 数据核对） | 高 | **低**：**umami 主网检查点实际只有创世块一个**（见 §4），fix-plan 的"多个历史 checkpoint"假设不成立；引入多个检查点反而比 umami 更激进 | **不建议**（除非有明确防重放需求） |

**推荐执行顺序**：P2-1 → P2-2（同轮编译部署）→ 观察稳定 → 视情况 P3-1 → P3-2 搁置。

---

## 1. P2-1 未上链网络块落盘保存

### 1.1 umami 语义（参照实现）

- `validation.cpp:4226`：`AcceptBlock` 里对**任何通过 header/上下文校验的块**（无论是否上主链）执行 `SaveBlockToDisk(block, pindex->nHeight, ...)`：
  - 主链块 → `blocks/` 文件 + 索引 `BLOCK_HAVE_DATA`
  - 侧链/未上链块 → **同样落盘**，留在 `setBlockIndexCandidates` 或 `m_blocks_unlinked`（validation.cpp:3150-3155），将来若其链 work 反超可重组上链
- 缺父块的块：进 `m_blocks_unlinked`（validation.cpp:3155），**父块到位后**（3672-3682）重新入候选集 → `ActivateBestChain` 自动恢复。
- 关键语义：**"先存后选"**——块数据先保住，主链选择交给 chainwork 比较，重启不丢失任何已收到的合法块。

### 1.2 ini-node 现状

- `process.go:283-287`：`ProcessBlock` 发现 prev 不存在 → `addOrphanBlock(block)` 进**内存孤儿池**，**不落盘**。
- `chain.go:24-34`：`maxOrphanBlocks = 16384`，溢出时驱逐最老孤儿（`removeOrphanBlock`），被驱逐块**直接丢弃**（数据永久丢失）。
- 主链块/连接成功的块才走 `maybeAcceptBlock` → `dbPutBlock` 落盘；未上链的侧链块（work 不足）`return false, nil`，不落盘。
- **结构缺陷**：网络主链块若暂时连不上本地污染 tip（P0 修复前的场景），全部进孤儿池且不落盘——重启即丢失；umami 则落盘保存，父块到位即可恢复。

### 1.3 可行性分析

| 维度 | 评估 |
|---|---|
| 技术可行性 | **高**。`dbPutBlock` 已存在（连接路径在用），孤儿分支只需在 `addOrphanBlock` 前先落盘；`processOrphans` 现有流程在父块连接后走 `maybeAcceptBlock`，落盘块会被正常索引读取 |
| 兼容性 | 中。需给孤儿块写入 block index（`statusDataStored`/`statusHeaderStored` 标志），与现有 `maybeAcceptBlock` 的索引写入路径复用；注意孤儿块**不应**写入 best-chain/高度索引，只写块数据 + header 索引 |
| 依赖 | 无新依赖；磁盘空间按需增长（孤儿块通常远小于主链块） |
| 主要障碍 | ① 孤儿块落盘后如何区分"待连接孤儿"与"主链块"——靠 header 索引状态 + prev 未在 bestChain 判定；② 容量上限需要从"内存条目上限"改为"磁盘占用上限"（按字节或按高度窗口）；③ 清理策略（孤儿块何时可删） |

### 1.4 性价比分析

- **收益**：
  - 消除"网络块重启即丢"——本事故（`a23e7e62` 卡死）中网络主链块全部进孤儿池，若当时已落盘，父块/重连后可直接恢复，P0 的回滚检测甚至不必触发；
  - 与 P0/P1 形成闭环：P0/P1 负责"纠正本地错误 tip"，P2-1 负责"网络块不因本地错误而丢失"，两者配合才完整覆盖 umami 的"先存后选"语义；
  - 重启恢复体验：节点重启后不再需要重新下载大量孤儿块。
- **成本**：~80 行改动 + 1 个容量常量；磁盘占用增加（受上限约束，可控）。
- **结论**：性价比**高**——它是除 P0/P1 外对本事故结构性缺陷最直接的对症修复，且改动面小。

### 1.5 方案设计

```
孤儿分支改造（process.go ProcessBlock 的 prev 不存在分支）：

1. 收到 prev 不存在的块：
   a. 先 dbPutBlock 落盘（块数据 + header 索引，标记 statusDataStored|statusHeaderStored）
   b. 再 addOrphanBlock 进内存池（条目保留，用于 processOrphans 依赖索引）
2. 清理策略 = 事件驱动主动清理（无定时巡检）：
   a. 容量上限：孤儿+候选磁盘占用超 maxOrphanBlockBytes（1 GiB，可调）→
      按 work 最小/最旧优先删（删块数据 + 清 header 索引数据标志，保留索引头或整体移除）
   b. 落后阈值：候选/孤儿链 tip 落后主链 > maxOrphanBlockHeight（2000，重组窗口上限）
      → 该链已无逆转可能 → 删数据清索引
   c. 触发点仅两个（事件驱动，无后台定时器）：
      · 块连接失败（竞争失败，按 P2-2 持久化标记 + 删磁盘数据，记录即完事）
      · 驱逐（removeOrphanBlock 现有路径扩展：应用容量上限 a 与落后阈值 b）
3. processOrphans 父块连接成功后：走正常 maybeAcceptBlock（数据已在磁盘，直接读盘验证），
   连接失败（如共识失败）→ 按 P2-2 标记失败，并清理其磁盘数据
```

关键点：
- **落盘≠上链**：孤儿块只写数据 + header 索引，不写高度索引、不进入 bestChain 视图；
- **去重**：`addOrphanBlock` 现有去重（hash 已存在则忽略）在落盘后仍适用——落盘前先查 header 索引是否已有数据，避免重复写盘；
- **清理 = 事件驱动主动清理**（根治"先存后选" + 容量/落后双上限，无定时巡检）：触发点仅两个——① 块连接失败（竞争失败，按 P2-2 持久化标记 + 删数据，记录即完事）；② 驱逐（`removeOrphanBlock` 现有路径应用容量上限 `maxOrphanBlockBytes` 与落后阈值 `maxOrphanBlockHeight`）。孤儿/候选块被连接（转正保留）不清盘、无后台巡检 goroutine。

### 1.6 详细实现步骤

| # | 文件 | 动作 |
|---|---|---|
| 1 | `backend/blockchain/chain.go` | 新增常量 `maxOrphanBlockBytes = 1 << 30`（1 GiB，可调）与 `maxOrphanBlockHeight = 2000`（落后阈值，重组窗口上限）；`BlockChain` 结构新增字段 `orphanDiskBytes int64` |
| 2 | `backend/blockchain/process.go` | `ProcessBlock` 的孤儿分支（283-287）：在 `addOrphanBlock` 前调用新增 `b.dbPutOrphanBlock(block)`；失败（写盘错误）时回退为纯内存孤儿（记录日志，不阻断） |
| 3 | `backend/blockchain/chainio.go` | 新增 `dbPutOrphanBlock`：复用 `dbPutBlock` 的块数据写入 + 新增 header 索引条目（标记 `statusDataStored\|statusHeaderStored`，**不**设置 `statusValid`）；新增 `dbRemoveOrphanBlockData(hash)`：删块数据 + 清 header 索引数据标志 |
| 4 | `backend/blockchain/chain.go` | `removeOrphanBlock` 扩展：驱逐时调用 `dbRemoveOrphanBlockData`，累计/扣除 `orphanDiskBytes` |
| 5 | `backend/blockchain/process.go` | `processOrphans` 连接失败分支：调用 `dbRemoveOrphanBlockData`（按 P2-2 标记失败的块不再保留数据） |
| 6 | `backend/blockchain/process.go` | `maybeAcceptBlock` 前置：若 header 索引已含数据标志且块未上链，跳过重复写盘（复用现有读取路径） |
| 7 | `backend/blockchain/process.go` | 事件驱动主动清理（无定时巡检）：仅两个触发点——① `processOrphans` 块连接失败分支调用 `b.pruneOrphanDisk`（按 P2-2 标记 + 删数据）；② `removeOrphanBlock` 驱逐路径扩展调用 `b.pruneOrphanDisk`（应用 `maxOrphanBlockBytes` 容量上限与 `maxOrphanBlockHeight` 落后阈值，work 最小/最旧优先删数据清索引）。无后台定时器、无 ProcessBlock 入口检查 |
| 8 | 测试 | `backend/blockchain/process_test.go`：① 孤儿块落盘 → 模拟重启（新 BlockChain 实例）→ 父块到达 → 自动连接；② 孤儿池驱逐 → 磁盘数据删除；③ 重复孤儿不重复写盘；④ 超容量/超落后阈值 → 磁盘数据清理（事件触发，无巡检） |

### 1.7 风险与验证

- **风险（中）**：
  - 磁盘占用：受 `maxOrphanBlockBytes` 上限 + `maxOrphanBlockHeight` 落后阈值双约束，且驱逐/失败/落后即清理；
  - 与现有孤儿池逻辑耦合：`removeOrphanBlock` 现有调用点（驱逐、父块处理完）需逐一核对，避免漏删磁盘数据；
  - 状态一致性：孤儿块 header 索引不设 `statusValid`，若意外被 `KnownValid` 逻辑误判需排查。
- **验证**：
  1. `go test ./blockchain/...` 通过；
  2. 构造缺父块场景 → 断言日志出现孤儿落盘；重启节点 → 父块到位后自动上链（无需重新下载）；
  3. 压测孤儿洪流（>16384 条目）→ 磁盘占用不超过上限，驱逐正常；
  4. 构造落后主链超阈值的候选/孤儿链 → 事件触发清理（无巡检），磁盘数据删除、索引数据标志清除。

---

## 2. P2-2 失败块持久化标记（BLOCK_FAILED_VALID 等价）

### 2.1 umami 语义

- `validation.cpp:1744`：`pindex->nStatus |= BLOCK_FAILED_VALID`——验证失败的块**持久化标记无效**，并加入 `m_failed_blocks`，从候选集移除（validation.cpp:1744-1747）；
- 之后同类块（同 hash 再收到）**不再重复校验**（防 DoS）；
- `BLOCK_FAILED_CHILD`（3150）标记失败块的后代，递归阻止整条坏链。

### 2.2 ini-node 现状

- `blockindex.go:41-42`：已有 `statusValidateFailed` 标志位，`KnownInvalid()`（79-80）可判定；`chain.go:1260/1364/1392` 多处 `SetStatusFlags(node, statusValidateFailed)`（重组/失效路径在用）；
- **status 落盘：已审计确认（2026-09-11）**——块索引行 value = header 序列化 + 1 字节 status（`chainio.go:2296 WriteByte(byte(node.status))`，读取 2230），`SetStatusFlags` 标记 dirty（blockindex.go:816-821）→ `flushDirtyLocked`/`flushToDB` 持久化。P2-2 的核心机制（失败标记 + 落盘 + 重启秒拒 + 后代标记）已随修复 B / 放宽语义 / 回滚自愈轮次**全部落地**，本节剩余缺口仅文档化确认，无需新代码。
- 模板级失败（`CheckConnectBlockTemplate` 返回）与共识级失败混用同一错误通道，需区分。

### 2.3 可行性分析

| 维度 | 评估 |
|---|---|
| 技术可行性 | **高**。标志位已存在，主要工作=①审计/打通 status 序列化落盘；②在 `maybeAcceptBlock` 入口对 `KnownInvalid` 块直接拒绝（不重复校验）；③区分模板级/共识级失败 |
| 兼容性 | 中。block index 序列化格式若已含 status 字段则向后兼容；若不含，需扩展序列化（注意与字节序加固 P1-3 一并处理） |
| 主要障碍 | ① status 字段落盘路径不明确（需先读 chainio.go 序列化代码）；② 失败判定与 37a5330d 放宽语义的边界（模板级失败不能持久化，否则误杀合法竞态块） |

### 2.4 性价比分析

- **收益**：
  - 防 DoS：恶意/损坏块只校验一次，之后秒拒；
  - 防"重启后重新踩坑"：本次事故的失败块若持久化，重启后直接跳过，避免重复触发回滚/重建路径；
  - 与 P2-1 配合：孤儿块连接失败后标记失败并清数据，避免下次再进孤儿池。
- **成本**：~40 行 + 序列化审计（若序列化缺失，+~20 行）。
- **结论**：性价比**中高**，与 P2-1 强耦合（同一轮实施收益最大）。

### 2.5 方案设计

```
1. 审计并打通 status 落盘：
   - chainio.go 的 block index 序列化（blockIndexKey 的 value 部分）是否含 status 字段；
   - 若含 → 确认 statusValidateFailed 随写入生效；
   - 若不含 → 在 value 序列化中扩展 status 字段（保持向后兼容读旧格式），或单独
     failed-blocks bucket（高度 → hash 列表，重启时重建内存判定）。
2. maybeAcceptBlock 入口（process.go:292 前）：
   - b.index.LookupNode(hash) 存在且 KnownInvalid() → 直接返回拒绝（不重新校验）。
3. 失败标记时机（accept.go 的 ruleError 路径 / maybeAcceptBlock 失败分支）：
   - 仅共识级失败（CheckBlockSanity/CheckConnectBlock 明确 ruleError）→ SetStatusFlags(statusValidateFailed) + 持久化；
   - 模板级失败（CheckConnectBlockTemplate 返回）→ 不标记（37a5330d 放宽语义保留）。
4. 后代标记（可选）：失败块的后代递归标记 statusInvalidAncestor（复用 KnownInvalid 判定）。
```

### 2.6 详细实现步骤

| # | 文件 | 动作 |
|---|---|---|
| 1 | `backend/blockchain/chainio.go` | 审计 block index 序列化：确认 status 字段是否落盘；缺失则扩展（兼容旧格式读取） |
| 2 | `backend/blockchain/process.go` | `maybeAcceptBlock` 入口：`KnownInvalid` 直接拒绝 + 日志 |
| 3 | `backend/blockchain/accept.go` | 失败分支（290-308 附近）：共识级失败时 `SetStatusFlags(statusValidateFailed)` + 触发索引落盘 |
| 4 | `backend/blockchain/chain.go` | 失败块的后代递归标记 `statusInvalidAncestor`（对齐 `BLOCK_FAILED_CHILD`） |
| 5 | 测试 | ① 失败块落盘 → 重启 → 同块再提交立即拒绝（不重复校验）；② 模板级失败不被持久化；③ 后代块被连带拒绝 |

### 2.7 风险与验证

- **风险（中）**：
  - 序列化格式变更需向后兼容（旧 DB 数据可读）；
  - 误标风险：若把模板级失败误持久化，合法竞态块会被永久拒绝——**必须在标记处严格区分**；
  - `InvalidateHeaderChain` 路径已用 `statusValidateFailed`（chain.go:1260 等），需确认两处语义不冲突（重组失效 vs 校验失败）。
- **验证**：
  1. `go test ./blockchain/...` 通过；
  2. 提交一个明知失败的块 → 重启 → 再提交同块，日志显示直接拒绝（无重复校验耗时）；
  3. 正常竞态（37a5330d 场景）下模板不被误标（回归测试）。

---

## 3. P3-1 MinChainWork 门槛 / assumevalid

### 3.1 umami 语义（参照实现）

- `kernel/chainparams.cpp:124-125`（主网）：
  - `consensus.nMinimumChainWork = 0x0000…3f23ef34da28`（高度 **6513497** 的 chainwork）
  - `consensus.defaultAssumeValid = 0x855f0c66…`（高度 6513497 的区块 hash）
- `nMinimumChainWork` 作用：候选链的 chainwork 必须 ≥ 该值才可能成为主链（validation.cpp 的 `SetBlockIndexCandidates`/`FindMostWorkChain` 应用），拒绝低 work 链（防低 work 分叉攻击 / 防污染 tip 长期存活）。
- `defaultAssumeValid` 作用：IBD 时跳过该高度之前区块的完整校验（加速），其后的块仍全量校验。
- 注：主网锚点取高度 6513497（早期链），testnet 取高度 4000000（chainparams.cpp:239-240）。

### 3.2 ini-node 现状

- `chaincfg/params.go`：**无** `MinChainWork`/`AssumeValid` 字段（对比文档 8.2 节 C5 已确认）；
- 主链选择（`connectBestChain` / `reorganizeChain`）：纯本地 bestChain + 侧链 work 比较，无外部 work 门槛；
- IBD：`ProcessBlock` 全量校验所有历史块（无 assumevalid 跳过路径）。

### 3.3 可行性分析

| 维度 | 评估 |
|---|---|
| 技术可行性 | **中高**（MinChainWork 部分）。chainwork 计算（`CalcWork`/`AddWork`）已有；只需在链选择/候选判定处加一个门槛比较；assumevalid 涉及 IBD 校验跳过，改动面大且本节点已同步完成、收益低 |
| 兼容性 | 中。门槛是纯增量约束：正常同步的链 work 远大于门槛，不会误拒；但**门槛值必须与 umami 主网逐位核对**，写错会拒绝整条真链（灾难性） |
| 依赖 | 需从 umami 或链上数据确认高度 6513497 的真实 chainwork/hash（可用 `getblockheader 6513497` 获取） |
| 主要障碍 | ① 门槛值核验（安全第一）；② 应用点选择：ini-node 无"候选集"概念，门槛要落在 bestChain 切换判定或 maybeAcceptBlock 处；③ assumevalid 需设计"跳过校验但保留孤儿判定"的路径，复杂 |

### 3.4 性价比分析

- **收益**：
  - MinChainWork：**防御价值最高**的外部锚点——即使本地 bestChain 被污染 tip 持久化，只要该 tip 的链 work < 门槛，重启后主链选择直接拒绝它，等效于 umami 的"网络主链自动胜出"下限保障；对低 work 分叉/攻击链是硬性拒绝；
  - assumevalid：仅加速首次同步，本节点已完成同步，**当前收益≈0**。
- **成本**：MinChainWork ~60 行；assumevalid ~100+ 行（校验跳过路径 + 测试）。
- **结论**：性价比**中**——MinChainWork 值得做（与 P0/P1 互补，是第三道防线）；assumevalid 不建议（收益低、风险高）。

### 3.5 方案设计

```
1. chaincfg/params.go：Params 结构新增 MinChainWork *big.Int 与 AssumeValid *chainhash.Hash
   （主网填入高度 6513497 的真实值；testnet/regtest 可为 nil 保持现状）。
2. 门槛应用点（二选一，推荐 A）：
   A. maybeAcceptBlock 的 connectBestChain 切换判定：候选链 tip 的 chainwork < MinChainWork
      → 不切换（本地链太弱不配当主链），并记录日志；
   B. ProcessBlock 入口：块所在链 chainwork < MinChainWork → 仍接受/落盘但标记"不够格"，
      不参与主链选择（更接近 umami 候选集语义，改动更大）。
3. assumevalid（可选，暂缓）：IBD 阶段（bestChain 高度 < assumevalid 高度）跳过历史块
   的 CheckBlockSanity/checkBlockContext，仅校验 PoW + 连接结构。
```

### 3.6 详细实现步骤

| # | 文件 | 动作 |
|---|---|---|
| 1 | `backend/chaincfg/params.go` | 新增 `MinChainWork`/`AssumeValid` 字段；主网填 6513497 真实值（先用 `getblockheader`/`getblockchaininfo` 核验） |
| 2 | `backend/blockchain/chain.go` | `connectBestChain` 切换前：`if b.chainParams.MinChainWork != nil && newTip.work.Sign() >= 0 && newTip.work.Cmp(MinChainWork) < 0 → return false`（不切换）+ 日志 |
| 3 | `backend/blockchain/chain.go` | `HeaderChainDiverged`/P0 回滚路径同样应用门槛（回滚目标不得低于 MinChainWork） |
| 4 | 测试 | 构造低 work 候选链 → 断言不切换主链；门槛值边界（== / < / >）测试 |

### 3.7 风险与验证

- **风险（高）**：
  - 门槛值错误 = 拒绝整条真链（最坏情况全链同步失效）——**必须先核验**；
  - 应用点选错可能影响正常重组（如重组的中间状态 chainwork 暂时偏低被误拒）——需保证门槛只对"候选 tip"比较，不对"重组过程"比较。
- **验证**：
  1. `go test ./blockchain/...` 通过；
  2. 主网实跑：同步/挖矿正常（真链 work 远大于门槛，无行为变化）；
  3. 构造低于门槛的假链 → 断言永不成为主链。

---

## 4. P3-2 检查点（Checkpoints）

### 4.1 umami 语义（关键更正）

- `kernel/chainparams.cpp:172-176`（主网 `checkpointData`）**实际只有创世块一个**：

```cpp
checkpointData = {
    {
        { 0, uint256S("0x7d5eaec2dbb75f99feadfa524c78b7cabc1d8c8204f79d4f3a83381b811b0adc")},
    }
};
```

- **fix-plan-20260827.md 中"主网有多个历史 checkpoint 高度+hash"的描述不准确**——umami 主网实际只锚定创世块（testnet 同样只有创世锚点，chainparams.cpp:278）。umami 的真正外部锚点是 `nMinimumChainWork`（§3），不是检查点。
- 检查点机制在 Bitcoin Core 中是"高度 < checkpoint 的块必须 hash 匹配，否则拒绝"，主要用于防重放/早期链锁定。

### 4.2 ini-node 现状

- `chaincfg/params.go:334-335`：主网 `Checkpoints: nil`（testnet/regtest 同样 nil，262 行字段定义存在但未启用）。
- 无任何检查点校验逻辑。

### 4.3 可行性分析

| 维度 | 评估 |
|---|---|
| 技术可行性 | 中。btcd 系有 Checkpoints 字段和 `checkpointBlockHash` 判定框架（params.go:261-262 定义仍在），实现成本不高 |
| 兼容性 | **低-中**：若检查点数据与真实链不符会拒绝整条链；且**必须从 umami 主网数据逐块核对**，任何错误都是灾难性的 |
| 主要障碍 | ① 数据来源：umami 主网只有创世检查点，若 ini-node 引入"每 500k 一个"的检查点，**没有任何现成数据可抄**，需自行从主链逐块核验生成（工作量大、易错）；② 引入比 umami 更严格的锚定 = 与 umami 行为**不一致**（umami 自己都不锁这些高度） |

### 4.4 性价比分析

- **收益**：理论上防早期链重放/深度重组；但 umami 本身不依赖它（只有创世锚点），本节点的 P0/P1/MinChainWork 已覆盖分叉防御；
- **成本**：~100 行 + 大量数据核验；风险高（数据错 = 全链拒绝）。
- **结论**：性价比**低**——**不建议实施**。若确需防重放，优先做 P3-1（MinChainWork，与 umami 语义一致），检查点留作最后手段。

### 4.5 方案设计（仅作备案，不建议实施）

```
若未来确需：
1. params.go 主网 Checkpoints 填入"从主链高度 0 到当前，每 N 高度"的真实 hash（N 建议 ≥ 500k）；
2. validate.go/process.go 增加 checkpoints 校验：高度 < 某 checkpoint 的块，hash 必须匹配；
3. 仅主网启用，testnet/regtest 保持 nil。
```

### 4.6 详细实现步骤（备案）

| # | 文件 | 动作 |
|---|---|---|
| 1 | `backend/chaincfg/params.go` | 主网 Checkpoints 填真实数据（数据核验先行） |
| 2 | `backend/blockchain/validate.go` | `checkBlockContext` 或 ProcessBlock 前置：检查点 hash 匹配校验 |
| 3 | 测试 | 构造 checkpoint 高度 hash 不匹配的块 → 拒绝 |

### 4.7 风险与验证

- **风险（高）**：检查点数据错误 → 整条主链被拒（恢复需删库重建）；与 umami 行为不一致（umami 不锁这些高度）。
- **验证**（若实施）：主网实跑 24h+ 无拒绝；checkpoint 前后高度块均正常。

---

## 5. 实施顺序与总工作量

| 轮次 | 项 | 改动量 | 风险 | 说明 |
|---|---|---|---|---|
| **下一轮** | P2-1（孤儿落盘） | ~80 行 | 中 | 与 P2-2 同轮，一次编译部署 |
| **下一轮** | P2-2（失败块持久化） | ~40 行 | 中 | 依赖 chainio status 序列化审计 |
| 视情况 | P3-1（MinChainWork） | ~60 行 | 高 | 需先核验 6513497 真实 chainwork |
| 暂缓 | P3-2（检查点） | ~100 行+数据 | 高 | 不建议；umami 主网仅创世锚点 |
| 不做 | assumevalid | ~100+ 行 | 高 | 本节点已同步完成，收益≈0 |

**推荐**：下一轮实施 P2-1 + P2-2（编译部署后观察同步/挖矿稳定性）→ 若仍有分叉污染反复，再加 P3-1 → P3-2 原则上不做。

---

## 6. 验证清单（P2/P3 每轮后）

1. 编译：`go build ./...` 通过；部署：`build.bat` 成功。
2. 重启后日志无 `does not exist`/`corruption`/`is not the main-chain` 告警循环。
3. 分叉演练：构造错 tip → P0/P1 检测触发；P2-1 场景下孤儿块重启不丢、父块到位自动上链；P2-2 场景下失败块重启后秒拒（无重复校验）。
4. block index 无新损坏行（`Skipping corrupt row` 计数不增长）。
5. 挖矿：矿机提交块本地接受且网络可广播；`getblockchaininfo` tip 与网络一致。
6. （若做 P3-1）低 work 假链永不成为主链；真链同步/挖矿无行为变化。

---

## 附录（2026-09-11 补充）：方案 C —— 分叉防护结构性修复记录

### 背景

2026-09-11 生产节点事故：本地矿工（booooo）通过 submitblock 连续提交本地块
（dbcb1294/ba5cc834/0af1401e/75c78936），节点全部接受；`0af1401e@44337489`
成为主链 tip 但**不在网络主链上**（peer 无法从它延续出 44337490 的 header），
导致 header/block 下载死锁、矿工提交被 `ErrOverwriteTx` 拒绝、分叉无法自愈。
根因 = BFMinerSubmit 守卫在 bestHeader 视图滞后（落后 62 块）时整体跳过校验。

### 方案 C 定义（结构性修复，umami 对齐）

让本地矿工块**永远无法直接坐上 best-chain tip 位置**，从机制上消除
"submitblock 污染 tip"这一类事故：

1. **本地矿工块只入候选、不做 tip**：submitblock 的矿工块经过校验后加入
   side-chain 候选（`setBlockIndexCandidates` 语义），仅当它的 chainwork
   **反超**当前 best chain 时才激活（`ActivateBestChain` / `FindMostWorkChain`
   语义，对应 umami validation.cpp:3114）。网络主链持续出块时本地块 work
   永远追不上 → 自然被比下去，不会污染 tip。
2. **先存后选（复用 P2-1）**：矿工块（以及所有通过校验的侧链/孤儿块）
   **落盘保存**（umami `SaveBlockToDisk` 语义），父块到位或 work 反超时自动
   上链；不落盘导致的重启即丢问题由 P2-1 一并解决。
3. **网络锚点永不滞后**：bestHeader 视图在节点同步后停止更新是本次事故的
   直接前提（守卫依赖视图高度）。结构性方案应让 header 视图持续跟随网络
   （`m_best_header` 语义：始终指向网络确认的最高 header 链 tip），或让守卫
   在视图滞后时不再依赖视图（见方案 B，已实施）。

### 与方案 A/B 的关系

| 方案 | 性质 | 状态 |
|---|---|---|
| A（视图滞后直接拒绝） | 快速修复 | ❌ 否决：会误拒正常矿工（0.8s 场景，accept.go 注释记载的真实观察） |
| **B（视图滞后触发 header 追赶再校验）** | 对症修复 | ✅ **已实施**（accept.go 守卫 + netsync 触发 fetchHeaders） |
| **C（候选制 + chainwork 定胜负 + 先存后选）** | 结构修复 | 📋 本附录记录，待排期 |

### 实施建议（进入 P2 轮次时）

- C-1 依赖 P2-1（侧链/孤儿块落盘）——先做 P2-1，再做候选制改造；
- C-2 主链选择改造点：`maybeAcceptBlock`（accept.go:63）的 best-chain tip
  切换处，改为"加入候选 + 比较 chainwork"；
- C-3 守卫（accept.go BFMinerSubmit 分支）在 C 落地后可简化：视图滞后时
  不再需要"拒绝 + 追赶"的临时逻辑，矿工块天然无法成为 tip；
- 验证：事故复现（本地挖错块 → 不污染 tip → 网络链继续推进 → 自动纠正）；
  正常挖矿无行为变化（本地块 work 反超时照常上链）。
