# umami vs ini-node 挖矿块处理全流程对比

- 日期：2026-08-27
- 背景：ini-node（Go btcd 系）本地挖矿挖出的块 `a23e7e62`（高度 44060189）上不了网络主链，block 同步卡死；umami（C++ Sugarchain 官方节点）"挖到就上链到账"。本文逐环节对比两个节点对"收到挖矿块后"的处理差异。
- 代码位置：
  - umami：`C:/Users/adest/Desktop/git/Mimo/apiserver/new/backend/umami`（Bitcoin Core 结构，C++）
  - ini-node：`C:/Users/adest/Desktop/git/Mimo/apiserver/new/sugarchain-node/backend`（Go，btcd 派生）

---

## 0. 总体架构差异（先看根子）

| 维度 | umami（Bitcoin Core） | ini-node（btcd） |
|---|---|---|
| 区块组织 | **header 索引（CBlockIndex）全局共享**，块数据独立落盘 | 内存 blockNode 索引 + 主链/侧链视图分离 |
| 主链选择 | `setBlockIndexCandidates`（按 chainwork 排序的候选集）→ `FindMostWorkChain()` 选**全局 work 最大**的链 | `bestChain`（主链视图）由连接/回滚显式切换 |
| 未上链块去向 | 落盘保存 + 留在候选集 / `m_blocks_unlinked`（缺父块）| 进**孤儿池**（内存，addOrphanBlock）或直接拒绝 |
| 挖矿块提交入口 | `submitblock` RPC → `ProcessNewBlock` | `submitblock` RPC → `SyncMgr.SubmitBlock` → `ProcessBlock` |

**一句话**：umami 是"任何有合法 header + work 的块都先存下来，再按 work 挑主链"；ini-node 是"只有能连上现有主链的块才处理，连不上的进孤儿池等父块"。

---

## 1. 挖矿块接收入口

### umami：`src/rpc/mining.cpp:937` `submitblock`

```cpp
// rpc/mining.cpp:993
bool accepted = chainman.ProcessNewBlock(blockptr,
    /*force_processing=*/true,   // 强制处理，不管是否请求过
    /*min_pow_checked=*/true,    // 挖矿提交的块 PoW 已被矿机验证过
    /*new_block=*/&new_block);
```

- 配套 `submitblock_StateCatcher`（mining.cpp:919）实现 `CValidationInterface::BlockChecked`，捕获异步验证结果，RPC 按 BIP22 返回状态码（`duplicate`/`inconclusive`/`rejected`/`null`）。
- **关键**：`force_processing=true` 意味着"即使没请求过也处理"——矿机提交的块无条件走完整链路。

### ini-node：`backend/rpcserver.go:3731` `handleSubmitBlock`

```go
// rpcserver.go:3758
isOrphan, err := s.cfg.SyncMgr.SubmitBlock(block, blockchain.BFNone)
if err != nil {
    return fmt.Sprintf("rejected: %s", err.Error()), nil
}
```

- 委托链：`rpcadapters.go:259 SubmitBlock` → `netsync/manager.go:4160 SyncManager.ProcessBlock`（投递 `processBlockMsg` 到 blockHandler goroutine）→ `blockchain/process.go:156 ProcessBlock`。
- **关键**：`BFNone` 无特殊标志，走与 P2P 收到块相同的处理路径；RPC 层只拿到 `(isOrphan, err)`，没有 BIP22 状态码语义。

---

## 2. 验证流程

### umami：`src/validation.cpp:4243` `ProcessNewBlock`

```
CheckBlock(block, state, consensus)              // 基础校验（结构/merkle/PoW）
  └─ 通过 → AcceptBlock                          // validation.cpp:4158
       ├─ AcceptBlockHeader                      // header 进全局索引（含 min_pow_checked 跳过 PoW）
       ├─ CheckBlock + ContextualCheckBlock      // 上下文校验（版本/时间/难度/父链）
       ├─ 失败 → pindex->nStatus |= BLOCK_FAILED_VALID  // 明确标记无效并持久化
       ├─ SaveBlockToDisk                        // 落盘（blockstorage.cpp）
       ├─ ReceivedBlockTransactions              // 更新索引状态/交易数
       └─ FlushStateToDisk                       // 刷盘
ActivateBestChain(state, block)                  // 重新选主链并连接（validation.cpp:4275）
```

- **验证失败 ≠ 丢弃**：失败块被标记 `BLOCK_FAILED_VALID` 并记入 `m_failed_blocks`，从候选集移除（validation.cpp:1744-1747），之后同类块不再重复校验（防 DoS）。
- 上下文校验以 `pindex->pprev`（header 索引里的父节点）为锚，**不要求父块是当前 tip**。

### ini-node：`backend/blockchain/process.go:156` `ProcessBlock`

```
ProcessBlockHeader 校验（时间/难度/checkpoint）      // process.go 前端
  ├─ prev 不存在 → addOrphanBlock（孤儿池）          // process.go:283-287
  └─ prev 存在 → maybeAcceptBlock                    // process.go:292
       ├─ CheckConnectBlockTemplate / ConnectBlock   // 完整连接验证
       ├─ 失败 → ruleError（拒绝，不标记持久化无效，除非已判定）
       └─ 成功 → connectBestChain 更新主链          // process.go:300
processOrphans(blockHash)                            // 尝试连接依赖它的孤儿
```

- **关键差异**：`37a5330d` 放宽了 `CheckConnectBlockTemplate`——原来要求 **prev 必须是当前链 tip**（`ErrPrevBlockNotBest`），放宽为 **prev 只要在主链上**（`b.bestChain.Contains(prevNode)`）。注释说明这是为 Sugarchain 5 秒出块节奏避免 GBT 模板竞态误拒。
- 验证失败直接返回错误给 RPC（`rejected: ...`），**不会像 umami 那样持久化标记无效**（btcd 的 `maybeAcceptBlock` 对失败块只在内存标记，是否持久化取决于分支）。

---

## 3. 上链打包（Connect）

### umami：`ActivateBestChain` → `ConnectBlock` → `UpdateTip`

- `FindMostWorkChain()`（validation.cpp:3114）从 `setBlockIndexCandidates` 取 **chainwork 最大的候选**，逐块回退/前进（`DisconnectTip`/`ConnectTip`），切换到全局最优链。
- 更新 tip 后 `UpdateTip`（validation.cpp:2839）刷新 `CoinsTip()`（UTXO 缓存），触发 `BlockConnected` 信号（钱包入账）。
- **上链标准 = 全局 chainwork 最大**，不要求"块是矿机基于我 tip 挖的"。

### ini-node：`maybeAcceptBlock` → `connectBestChain`

- `maybeAcceptBlock` 先 `CheckConnectBlockTemplate`（放宽版）通过 → `connectBestChain` 判断是否应该切换主链（按 work 比较）。
- 切换主链时 `reorganizeChain`（分离/连接），更新 `bestChain` 视图与 stateSnapshot。
- **差异**：主链选择基于"本地 bestChain + 侧链 work 比较"，而 umami 是"全局候选集 work 最大"——当本地 bestChain 本身被污染（如 tip 是挖错的分叉块）时，ini-node 缺乏"网络候选"参照，容易在错误 tip 上继续连接；umami 的候选集始终包含网络主链（header 索引全局共享），work 最大原则会自动纠正。

---

## 4. 落库（存数据库）

### umami：`SaveBlockToDisk` + `ReceivedBlockTransactions`

- **任何通过 header/上下文校验的块都会落盘**（validation.cpp:4226 `SaveBlockToDisk`），无论是否上主链：
  - 主链块 → `blocks/` 文件 + 索引 `BLOCK_HAVE_DATA`
  - 侧链/未上链块 → **同样落盘**，留在 `setBlockIndexCandidates` 或 `m_blocks_unlinked`（validation.cpp:3155），将来若其链 work 反超可重组上链
- `ReceivedBlockTransactions` 更新 `nTx`/`nChainTx`，`FlushStateToDisk` 保证持久化。

### ini-node：`dbPutBlock`（blockchain/chainio.go）

- **只有 maybeAcceptBlock 成功（连接）的块才落盘**；孤儿块只进内存孤儿池（有大小上限，`maxOrphanBlocks`），**不落盘**。
- 未上链的侧链块：btcd 传统上在 `maybeAcceptBlock` 里对 work 不足的侧链块 `return false, nil`（不连接也不落盘，或标记后丢弃）；相比 umami "先存后选"，ini-node 是"先选后存"。
- **差异影响**：网络主链块若暂时连不上本地污染 tip（prev 不匹配），ini-node 全部进孤儿池且不落盘——重启即丢失；umami 则落盘保存，等父块到位即可重组恢复。

---

## 5. 未上链（失败/分叉/孤儿）处理

| 场景 | umami | ini-node |
|---|---|---|
| 验证失败 | `BLOCK_FAILED_VALID` + `m_failed_blocks` + 从候选集移除（持久化标记，防重验） | 返回 `ruleError`，RPC 显示 `rejected:`；失败标记仅内存/条件持久化 |
| 缺父块（真孤儿） | `m_blocks_unlinked`（落盘保存，父块到位后重新入候选集） | `addOrphanBlock` 进内存孤儿池（`Adding orphan block ...`），有容量上限，溢出驱逐 |
| 分叉/低 work 块 | 落盘 + 留在候选集（`BLOCK_HAVE_DATA`），work 反超时重组上链 | 不连接、不落盘（或视 work 比较返回不处理） |
| 挖错块（本地 tip 污染） | 网络主链 work 更大 → `FindMostWorkChain` 自动切回网络主链，错块成侧链 | 本地 bestChain 被污染时缺乏网络参照，容易持续在错 tip 上连接（本次事故） |
| 重试/恢复 | 父块/网络块到达 → `m_blocks_unlinked` 重新入候选集 → ActivateBestChain 自动恢复 | `processOrphans` 在父块连接后尝试；孤儿池溢出会丢块 |

---

## 6. 关键差异总结（结合本次事故）

本次事故：本地挖出 `a23e7e62`（44060189），网络主链该高度是 `b345517e`，本地 bestChain tip 被 `a23e7e62` 占据 → 网络主链 44060190+ 全部成孤儿 → block 卡死。

| # | 差异点 | umami 行为 | ini-node 行为 | 对事故的影响 |
|---|---|---|---|---|
| 1 | **主链选择依据** | 全局候选集 chainwork 最大（网络块自动胜出） | 本地 bestChain + 侧链 work 比较（无网络参照时被污染 tip 困住） | 根因之一 |
| 2 | **未上链块是否落盘** | 落盘保存，可重组恢复 | 只进内存孤儿池，不落盘 | 网络块在 ini-node 全部丢失/等待 |
| 3 | **CheckConnectBlockTemplate 放宽**（37a5330d） | 无此机制（天然按 pprev 上下文校验） | prev 只需在主链上 → 基于错 tip 的挖矿块也会被本地接受 | 助长"挖错块也被接受" |
| 4 | **失败块持久化标记** | `BLOCK_FAILED_VALID` 持久化 | 失败仅内存/拒绝 | umami 重启后仍记得错块，ini-node 重启后重新踩坑 |
| 5 | **挖矿提交强制处理** | `force_processing=true` | `BFNone`（与 P2P 同路径） | 挖矿块在 ini-node 被当作普通网络块，受孤儿/连接逻辑约束 |

---

## 7. 结论与修复方向（供后续决策）

1. **核心修复点**（已在进行）：ini-node 需要一个"网络参照"来纠正被污染的 bestChain tip——当前已实现：① `handleStallSample` 中 tip hash 与 DB 高度索引比对；② 新增 peer 通告高度比对（`ae4f631e`），本地 tip 落后 peer 且下载停滞时立即回滚。
2. **对照 umami 的启示**：真正的根治是让 ini-node 也具备"网络主链自动胜出"能力——header 同步已完成（44,077,197），应在 block 侧也以 header 链（网络）为参照：本地 bestChain tip 与 header 链同高度 hash 不一致时，直接以 header 链为准切换主链，而不是等回滚。
3. **挖矿侧**：`CheckConnectBlockTemplate` 放宽（37a5330d）对正常竞态是必要的，但应补充"prev 必须是网络主链（header 链）上的块"约束，避免基于本地污染 tip 的挖矿块被接受。
4. **落库**：可借鉴 umami"未上链块也落盘"——网络主链块连不上时先落盘保存（而非仅孤儿池），父块到位后可恢复，降低重启丢失风险。

---

## 8. 共识层深度对比（新增）

本节基于两边源码逐行核对（umami: `kernel/chainparams.cpp` + `pow.cpp` + `consensus/params.h` + `validation.cpp`；ini-node: `chaincfg/params.go` + `blockchain/difficulty.go` + `validate.go`）。

### 8.1 完全一致的部分（共识核心）

| 共识项 | umami（C++） | ini-node（Go） | 一致性 |
|---|---|---|---|
| **PoW 哈希算法** | `GetPoWHash()` → **YespowerSugar**（primitives/block.cpp:25，注释 `/* YespowerSugar */`） | `checkProofOfWork` 用 `pow.BlockPoWHash(header)`（validate.go:393） | ✅ 一致 |
| **难度算法** | **SugarShield-N510**（pow.cpp:19 `GetNextWorkRequired`，基于 Zcash Digishield 修改） | **SugarShield-N510**（difficulty.go:178 `calcNextRequiredDifficulty`） | ✅ 一致 |
| 难度窗口 | `nPowAveragingWindow = 510`（2550s/5s） | `SugarPowAveragingWindow = 510` | ✅ |
| 目标出块时间 | `nPowTargetSpacing = 5`（秒） | `SugarPowTargetSpacing = 5` / `TargetTimePerBlock = 5s` | ✅ |
| 调整钳制 | `nPowMaxAdjustDown = 32`（下调 32%）、`nPowMaxAdjustUp = 16`（上调 16%）；`MinActualTimespan`/`MaxActualTimespan` = 2142s/3366s | `SugarPowMaxAdjustDown = 32`、`SugarPowMaxAdjustUp = 16`；`SugarMinActualTimespan`/`SugarMaxActualTimespan` = 2142s/3366s | ✅ |
| 阻尼因子 | `nActualTimespan = 2550 + (actual-2550)/4`（pow.cpp:58） | 相同公式（difficulty.go:271） | ✅ |
| 时间扭曲防护 | 用 `GetMedianTimePast()` 中位时间（pow.cpp:47） | 用 `CalcPastMedianTime`（difficulty.go:263） | ✅ |
| 减半间隔 | `nSubsidyHalvingInterval = 12500000` | `SubsidyReductionInterval = 12500000` | ✅ |
| BIP34 高度 | `BIP34Height = 17` | `BIP0034Height = 17` | ✅ |
| BIP65/66 | `BIP65Height = 0`、`BIP66Height = 0`（Always on） | `BIP0065Height = 0`、`BIP0066Height = 0` | ✅ |
| CSV/SegWit | `CSVHeight = 0`、`SegwitHeight = 0`（Always on，创世激活） | 注释 "CSV/SegWit active at genesis" | ✅ |
| 区块时间规则 | `block.GetBlockTime() <= pindexPrev->GetMedianTimePast()` 拒绝（validation.cpp:3858） | `header.Timestamp.After(medianTime)` 必须严格大于中位时间（validate.go:788） | ✅ |
| 未来时间上限 | BTC 标准 2 小时 | `MaxTimeOffsetSeconds = 2 * 60 * 60`（validate.go:27） | ✅ |
| Coinbase 成熟期 | `COINBASE_MATURITY = 100` | `CoinbaseMaturity = 100` | ✅ |

**结论**：**共识核心（PoW 算法、难度算法、出块节奏、BIP 激活、时间规则）两边完全一致**——所以任何"网络接受的块"在两边都应验证通过；`a23e7e62` 上不了链**不是共识规则差异导致的**，而是 ini-node 的**链状态机（bestChain 选择/持久化）**问题。

### 8.2 差异点（共识策略而非规则）

| # | 差异项 | umami | ini-node | 影响 |
|---|---|---|---|---|
| C1 | **检查点（Checkpoints）** | 主网**有** `checkpointData`（kernel/chainparams.cpp:172，多个历史 checkpoint 高度+hash） | **`Checkpoints: nil`（空）**（chaincfg/params.go:335） | **最显著差异**：umami 早期链被检查点锁定（高度 < checkpoint 的块必须 hash 匹配，防重放/防分叉污染）；ini-node 无检查点，任何高度只要满足 PoW 规则都可能被接受/重组，对"分叉块占用 tip"的容忍度更高，恢复也更依赖自身状态机 |
| C2 | **难度计算的父链修复** | 全局 `CBlockIndex` 内存常驻，祖先链永远完整（pow.cpp 直接 `pindexFirst->pprev` 回溯） | 内存窗口（`headerwindow=50000`）会切断父指针，需 `repairDifficultyChain`/`repairAncestorChain` 深度修复后才能回溯 510 窗口（difficulty.go:201-241）；修复失败回退 `PowLimitBits`（TEMP-DBG 路径） | 不是共识差异，但 ini-node 在**内存窗口边界**（重启后、深回滚后）可能因父链断裂把难度算成 PowLimit——若恰好此时收到低难度块，会被误接受或误拒（曾是 8-21 事故的怀疑根因之一） |
| C3 | **区块大小/签名限制** | `MAX_BLOCK_WEIGHT = 4000000`、`MAX_BLOCK_SERIALIZED_SIZE = 4000000`、`MAX_BLOCK_SIGOPS_COST = 80000`（consensus/consensus.h） | btcd 标准 `MaxBlockWeight = 4000000` 体系（weight.go） | ✅ 等价，非差异 |
| C4 | **BIP9 版本位部署** | 保留 `vDeployments[MAX_VERSION_BITS_DEPLOYMENTS]` 框架（当前无活动部署） | 保留 `ConsensusDeployment` 框架（params.go:102-133，当前无活动部署） | ✅ 等价，两边都不在投票部署 |
| C5 | **最低链 work / 假设有效锚点** | 主网**有** `nMinimumChainWork = 0x...3f23ef34da28`（高度 6513497 的 chainwork）与 `defaultAssumeValid = 855f0c66...`（高度 6513497 的 hash）（kernel/chainparams.cpp:124-125） | **无**（chaincfg/params.go 无 `MinChainWork`/`AssumeValid` 字段） | 与 C1 同类：umami 有**两个外部锚点**（最低 work 门槛拒绝低于该 work 的链，防低 work 攻击；assumevalid 加速 IBD 校验）；ini-node 无任何外部锚点，完全依赖自身状态机，对污染/分叉链的防御更弱 |

### 8.3 共识对比对本事故的结论

1. **共识规则两边一致**——不能通过"改共识规则"来修复上链问题。
2. **检查点差异（C1）是真正值得注意的策略差异**：umami 有检查点兜底（即使本地状态机出问题，低于 checkpoint 的链不可被替换），ini-node 完全依赖自身的 bestChain 选择逻辑——一旦被污染 tip 持久化（`a23e7e62` 场景），没有任何外部锚点强制纠偏，只能靠回滚机制（现已在 handleStallSample 中补 peer 高度比对检测）。
3. **难度父链修复（C2）提示**：ini-node 在窗口边界/深回滚后难度计算有脆弱路径，若再叠加分叉 tip 场景，可能产生"低难度块被接受"的二次污染——建议后续验证 `repairAncestorChain` 的覆盖完整性。

---

## 9. 细节缺口补充（新增）

### 9.1 P2P 收块路径（原文档只覆盖 submitblock 入口）

| 环节 | umami | ini-node |
|---|---|---|
| 网络收块入口 | `net_processing.cpp` 收到 `inv`/`block` 消息 → `ProcessNewBlock`（同样走 CheckBlock→AcceptBlock→ActivateBestChain） | `handleBlockMsg`（netsync）→ `SyncManager.ProcessBlock` → 投递 `processBlockMsg` 到 blockHandler goroutine → `blockchain.ProcessBlock` |
| 矿机提交 | `submitblock` RPC → `ProcessNewBlock(force_processing=true, min_pow_checked=true)` | `handleSubmitBlock` → `SyncMgr.SubmitBlock`（`BFNone`，与 P2P 同路径） |
| 关键差异 | 矿机提交块**强制处理**且 PoW 已验（`min_pow_checked`） | 矿机提交块无特殊标志，PoW 在 `checkProofOfWork` 中重算——多花一次 YespowerSugar 哈希，但语义等价 |

### 9.2 重组（Reorg）细节

| 环节 | umami | ini-node |
|---|---|---|
| 触发 | `ActivateBestChain` 内 `FindMostWorkChain()` 找到 work 更大的候选链 → 自动重组 | `connectBestChain` 比较新旧链 work → `reorganizeChain`（分离旧链块/连接新链块） |
| 块状态保留 | 被分离的块**保留在磁盘+索引**（可再重组回来） | 被分离的块回退为"侧链"，**不落盘**（仅内存） |
| 回滚持久化 | `DisconnectTip` 写 UTXO/undo 数据 | `disconnectBlock` 回滚 UTXO 视图 + stateSnapshot 重建（chain.go `InvalidateHeaderChain` 路径） |

### 9.3 钱包入账信号

| 环节 | umami | ini-node |
|---|---|---|
| 上链通知 | `UpdateTip` 触发 `BlockConnected`（CValidationInterface）→ 钱包扫描入账 | `blockConnected` 通知（blockchain/notifications.go）→ 钱包/API 处理 |
| 重组通知 | `BlockDisconnected` + `BlockConnected` 成对触发 | 相同语义（断开旧链块、连接新链块分别通知） |
| 差异 | 无本质差异，两边都能正确入账/回滚入账 | 同左 |

### 9.4 Mempool 交互

| 环节 | umami | ini-node |
|---|---|---|
| 块上链后 | `RemoveForBlock`（validation.cpp）把已确认交易从 mempool 移除 | `RemoveConfirmedTransactions` 等价逻辑（mempool 处理） |
| 回滚后 | `BlockDisconnected` 后交易重新进 mempool（`AddToMempool`） | 相同语义 |
| 差异 | 无本质差异 | 同左 |

### 9.5 补充结论

- **P2P/钱包/mempool 三块两边行为等价**，不属于事故根因。
- **检查点（C1）与未上链块落库（原文档第 4 节）是两个节点最本质的策略差异**：umami 用"落盘+work 最大候选"+ 检查点锚定，天然抗污染；ini-node 用"先连后存 + 孤儿池"+ 无检查点，恢复完全依赖自身回滚机制——这正是 `a23e7e62` 卡死事故的结构性原因。

---

## 10. 字节序（大小端）差异（新增）

### 10.1 两边存储布局的根本不同

| 维度 | umami（Bitcoin Core） | ini-node（btcd） |
|---|---|---|
| block index 主键 | **hash 索引**：`m_block_index` 是 `BlockMap`（blockstorage.cpp:73 `m_block_index.find(hash)`），磁盘/内存都以 **区块 hash 为 key**，不按高度组织 | **高度前缀索引**：`blockIndexKey`（chainio.go:2255）= **BigEndian 高度(4B) + hash(32B)**，按高度排序存储（cursor 按高度遍历） |
| 高度字段序列化 | `CDiskBlockIndex::SERIALIZE_METHODS`（chain.h:426）内 `nHeight` 为 **int32 小端** | block index key 高度用 **BigEndian**；`heightIndexBucket`（高度→hash）用 `byteOrder = binary.LittleEndian`（chainio.go:92） |
| 哈希字节序 | C++ `uint256` 内部 4×uint64 小端存储，序列化写内部字节（即小端序 32 字节） | Go `chainhash.Hash` 字节数组，序列化/网络格式与 C++ 一致（同为小端/内部字节序） |
| 难度 bits | `uint32` Compact，数学值比较，无字节序问题 | 同（`CompactToBig`/`BigToCompact`，workmath） |

**结论**：**哈希与难度的字节序两边一致**（网络/磁盘格式兼容——这也是两边能对同一链达成共识的前提）；差异只在**存储布局**：umami 按 hash 索引，ini-node 按"BigEndian 高度前缀 + hash"排序索引。

### 10.2 ini-node 内部字节序混用的风险点（历史损坏的疑似来源）

- ini-node 的 block index **key 高度用 BigEndian**（chainio.go:2257 `binary.BigEndian.PutUint32(indexKey[0:4], ...)`），读侧对应 BigEndian 解析（chainio.go:1127 `binary.BigEndian.Uint32(cursor.Key()[0:4])`）——**当前代码自洽**。
- 但 `heightIndexBucket` / best-state / utxo 序列化用 **LittleEndian**（chainio.go:92 `byteOrder = binary.LittleEndian`，2130/2146 行）——**同一文件两套字节序**，任一读写路径若混用 bucket，会把高度解析成巨大/负数（`0xfffff94d`~`0xffffff24` 这类损坏行正是小端字节被 BigEndian 解析的典型症状——8-21 观察到的 block index 尾部 53 万行损坏极可能与此类混用有关）。
- **风险建议**：后续维护时对 blockIndex key 的读写必须严格配对 BigEndian；新增对高度索引的读写须确认使用 `byteOrder`（LittleEndian）而非误用 BigEndian，并建议加单元测试覆盖"写入→读出"回环，防止字节序回归。

### 10.3 与本事故的关系

- **大小端不是 `a23e7e62` 上不了链的直接原因**（哈希/难度字节序两边一致，网络块验证不受影响）。
- 但它是**历史 block index 损坏**（51GB 重建、53 万行缺失、BAD rows）的疑似来源之一；损坏 → 全量重建 → sugar index 索引污染，是本次事故链（同步卡死 → 挖矿上不了链）的上游放大器。
