# 待测试项 / Pending Test Items

> 状态图例：`[ ]` 待测 · `[~]` 进行中 · `[x]` 已通过/已完成
> 更新时间：2026-08-04

## 已完成并验证

- [x] **Genesis PoW 已知答案测试** — `pow/pow_kat_test.go` `TestTestnetGenesisPoW`
  - testnet genesis: 时间 1565913601, nonce 490, bits 0x1f3fffff
  - 断言 `BlockPoWHash == 0032f49a73e00fc182e08d5ede75c1418c7833092d663e43a5463c1dbd096f28` ✅ PASS (0.03s)
  - 证明 yespower 实现与 C++ umami 字节级一致
- [x] **主网 header 链与外部数据源交叉验证** — api.sugarchain.org
  - height 1 `ce8a0df3…c7c91` ✅、height 2 `67d3e607…cecb` ✅（= API nextblockhash）、height 100 `982f01d3…d825` ✅
  - genesis `7d5eaec2…` = API previousblockhash ✅
  - 已接受 150,000 个 header，**零** difficulty/验证错误
- [x] **DAA / 难度对齐 C++** — `blockchain/difficulty.go`
  - SugarShield-N510：每块重算，510 块窗口，MTP 时间边，先除后乘（与 C++ CalculateNextWorkRequired 同序）
  - 无 `difficulty of ... is not the expected value` 断连错误
- [x] **IBD 期间跳过 PoW 检查** — 镜像 umami PR #122
  - `netsync/manager.go` handleHeadersMsg 在 ibdMode 下传 `BFNoPoWCheck`
  - header 同步速率 ~500-870 headers/s（此前 PoW 全量校验约 35/s）
- [x] **重启后 header 持久化（步骤 2/3）** — `flushToDB` 写入 header-only 节点(`blockIndexBucket`+`hashIndex`,批量 `headerFlushBatchSize=10000`,`FlushBlockIndex` 关停兜底);`bestHeaderState` 存储/恢复 header tip,重启后 `initChainState` 恢复到上次 header tip(双 tip 分离)。单元用例 `TestBestHeaderState`/`TestFlushToDB`(已改"全部节点落盘")✅
- [x] **步骤 4 header 内存窗口化（代码落地 + 单测）** — `--headerwindow=N` 时仅物化每 tip 尾部窗口节点,其余落盘驱逐:
  - `blockIndex.evictWindow` 驱逐 header 边界以下节点(保留 bestChain 尾部窗口,兼容 header 远快于 block 的场景);`chainView.pruneBelow` 割断边界锚点 parent/ancestor
  - 窗口边界锚点 `workSum` 由 `initChainState` 逐行累加(不物化,无启动尖峰);`parentHash` 保留使 header 可重建
  - 深 reorg(< 窗口边界)+ `LookupNode` miss 拒绝,`ErrForkTooOld`;`FindFork` nil 安全(accept/chain 侧)
  - 新增 `blockindex_test.go` `TestHeaderWindowEviction`(驱逐、重启窗口物化、边界 workSum 一致、窗口推进重疏割)✅
- [x] **步骤 5 DB 冷读兜底（代码落地 + 单测）** — 窗口外节点经两跳 `hashIndex→heightIndex→blockIndexBucket` 懒物化:
  - 新增 `coldread.go`:`coldNodeCache`(FIFO 64)+`materializeColdNode`/`coldNodeAtHeight`/`nodeAtHeight`/`isMainChainHash`,冷读开关 `db!=nil && headerWindow!=0`(无 db 的 fake-chain 与窗口关闭节点行为不变)
  - 已接线(API+P2P 范围):`BlockByHash`/`BlockByHeight`/`BlockHeightByHash`/`BlockHashByHeight`/`HeaderHashByHeight`/`HeaderHeightByHash`/`IsValidHeader`/`HeightRange`/`locateInventory`/`locateBlocks`/`locateHeaders`
  - `TestLocateInventory` 恢复通过(护栏修复 fake-chain nil db);`HeightToHashRange`/`IntervalBlockHashes` 仍留窗口外错误
  - 新增 `coldread_test.go` `TestColdReadFallback`(冷热混合 HeightRange、冷高度 P2P locate、cache 指针同一性、severed parent、零 workSum)✅
  - 批量读:`materializeColdNodeWithTx` + `nodesInHeightRange`,`locateBlocks`/`locateHeaders`/`HeightRange` 连续冷段单事务物化(locator 首节点保留身份,侧链 stop hash 不被高度重解析);`TestLocateInventory` 回归保护 ✅
- [x] **步骤 6 指针同一性 + reorg 复查（代码侧完成）**:
  - 护栏:`materializeColdNodeWithTx` 读 DB 前先查内存索引,**内存节点原样返回内存指针**(先于 coldCache)→ 同 hash 永不复建第二个指针
  - 复查:reorg 仅窗口内(`verifyForkInWindow`/`ErrForkTooOld`);`getReorganizeNodes` severed-parent / 无 fork 点均 nil 安全;冷节点永不进入 reorg 决策
  - 断言已加入 `TestColdReadFallback`(边界节点冷查 = 同一内存指针);实测(手工 reorg)并入步骤 8/9
- [x] **步骤 7 `InactiveTips` 复核** — 步骤 4 的 severed-parent 护栏已足够(窗口内近期侧链可见,远处侧链接受"未知"),无额外接线
- [x] **并行 header 拉取（多 peer 分片）+ leveldb .ldb 16MB**（2026-08-04）:
  - `netsync/manager.go` 重写 `fetchHeaders` 为并行下载:`headerSyncState`(`ranges`/`peerRange`/`nextHeight`/`nextAssign`/`target`/`sliceLen`)+ 打乱 higherPeers、封顶 `maxHeaderSyncPeers=8`;切片 `assignHeaderRange`/`launchHeaderRange`(non-overlap,`headerLocator` 用 `HeaderHashByHeight` 定位);乱序到达按序落盘 `handleParallelHeadersMsg`→`processReadyHeaderRanges`(每 front 完成即应用,front peer 接续下一片 + 空闲 peer 顶格)
  - 慢 peer 绕行:`reissueStaleHeaderRanges`/`reissueFrontRange`(`headerRangeStallTimeout` 重发,front 空缺补洞)/`dropHeaderPeer`/`abortHeaderSync`/`finishHeaderSync`;新 peer 折叠 `headerSyncAddPeer`
  - **响应校验守卫**:`handleParallelHeadersMsg` 用 `HeaderHashByHeight(rng.start-1)` 校验 `headers[0].PrevBlock` → 过期/错配响应直接忽略,激进重发安全
  - leveldb:`database/ffldb/db.go` `defaultCompactionTableSize=16MB`/`defaultWriteBuffer=8MB`(原 2MB → .ldb 数量与写放大骤降;实测 .ldb ~17MB)
  - **新增 4 单测全绿**:`TestParallelHeaderSync`/`TestParallelHeaderSyncDropFrontPeer`/`TestParallelHeaderSyncShortResponse`/`TestParallelHeaderSyncAddPeer`
- [x] **并行 header 冒烟(主网临时 datadir,步骤 8 变体)**:8 peer 并行 `getchaintips` 确认 header 高度推进;**验证全干净**:`failed header verification`=0、`stale response`=0、空响应丢 peer=0、崩溃重启=0、stderr=0 字节、RSS 有界(~600MB/650k 高度)
  - 迭代:初版 60s 超时 + 慢 peer 挡 front → ~8k/min;`60s→12s→6s` + 校验守卫 → **~54k/min(≈895 h/s),追平/略超单 peer 基线(40–60k)**;根因 = in-order 落盘下 front 落在慢 peer 上整条下载等满超时,已用短超时 + 守卫解决

## 待测试项

### 同步 / 主网（步骤 8/9，需实测运行）

> 代码侧步骤 1–7 已全部落地;并行 header 拉取冒烟已在临时 datadir 完成(见"已完成"区)。以下为下一步实测手册(本机 btcd 已停机,原共享 DB 已被删,需在正式 datadir 重新全量同步)。

- [x] **步骤 8 并行 header 冒烟(临时 datadir)**:8 peer 并行、`headerRangeStallTimeout=6s` + 响应校验守卫 → **~54k/min(≈895 h/s)**;验证全干净(verifFail=0/stale=0/无崩溃/RSS 有界),详见"已完成并验证"
- [ ] **步骤 9 正式 datadir 全量同步 + 回归**:
  - 正式数据目录(原 `C:\Users\adest\AppData\Local\Btcd\sugarmainnet\blocks_ffldb` 已被删,需重建)用新二进制 `btcd_new.exe --headerwindow=2048` 启动
  - 关注:① 窗口化 `initChainState` 逐行 cursor 扫描耗时(43M 行 deserialize 为已知启动成本);② RSS 是否有界(目标 ≤ ~2-3GB);③ 无 OOM/无 panic
  - header 阶段(并行,~43.6M)追到 tip 后与 explorer2.sugarchain.net 对 tip hash
  - block 下载 + 全量校验;`--sugarindex` 地址索引正常增长
  - 增量重启几次,确认窗口化启动、驱逐、冷读稳定;RSS 长期有界
  - 手工 reorg 实测:`invalidateblock` + `reconsiderblock`(窗口内),`getchaintips` 正确、不 panic
  - 冷读边界:`HeightToHashRange`/`IntervalBlockHashes` 对祖先链冷段返回错误属已知限制,确认线上无 panic/无死锁
- [ ] **主网 100k header 冒烟（备选快速项）**:短跑几分钟看内存曲线即应远低于 19GB

### 单元测试（btcd 原厂 fixture 与 Sugar 参数不兼容导致）

- [ ] **chaincfg TestRegister** — `TestNet4Params` 已移除（Sugar 化），断言仍期待重复注册错误
- [ ] **blockchain** `TestMaybeAcceptBlockReusesHeaderNode / TestBip0030CheckNeededAfterBIP34 /
      TestHaveBlock / TestInvalidateBlock / TestReconsiderBlock`
  - 原因为比特币 regtest 测试数据 hash 超过 Sugar powLimit（0x0f0f…）
  - 另有同源 baseline 失败(与本次改动无关,已逐一对 stash 基线确认):
    `TestInitChainStateToleratesTrailingBestBlockBytes / TestNotifications /
     TestProcessBlockHeader / TestRegtestBuriedDeploymentsAlwaysActive /
     TestFlushOnPrune / TestInitConsistentState / TestCheckConnectBlockTemplate /
     TestCheckBlockSanity / TestFullBlocks`
- [ ] **netsync** `TestCheckHeadersList / TestBuildBlockRequestSkipsInflightBlocks /
      TestStartSyncChainCurrent / TestStartSyncBlockFallback / TestSyncStateMachine`
  - 同上，fixture 用比特币 genesis/powLimit
- [ ] **database** `Example_blockStorageAndRetrieval` — 期望输出 285 → 实际 304 字节（Sugar genesis 更大）
- [ ] **integration** `TestGetChainTips / TestInvalidateAndReconsiderBlock`

### regtest / 挖矿

- [ ] **regtest 单机挖矿**：powLimit=0x0f0f…（2^255 以下），`PoWNoRetargeting=true`，
      cpuminer 用 `pow.BlockPoWHash` 求解是否可用
- [ ] **regtest DAA 重算路径**（非 regtest 下每块重算的正确性边界）

### 钱包 / 地址

- [ ] `cmd/genaddr` 生成地址与主网/regtest 前缀核对（P2PKH 0x3f、bech32 `sugar`）
- [ ] 交易签名/验证（txscript）在 Sugar 上跑通

## 已知问题

- 本机直接访问 api.sugarchain.org / esplora 被拒（transport error）；需通过 websearch/webfetch 或 VPN 获取外部数据
- C++ 参考节点（sugarchain-cli port 34229）仍在 header 预同步（~136k），RPC `getblockhash` 返回 height out of range
