# 挖矿上链闭环修复：GBT rules + 本地块不污染主链 + submitblock 补 witness nonce

日期：2026-09-15
范围：ini-node（Go 重构 umami，btcd fork）挖矿长期不上主链、分叉、卡住
参照：umami（Bitcoin Core，backend/umami）——ini-node 的重构蓝本，其挖矿一直正常
合并自：gbt-rules-segwit-fix-20260914.md / fork-evidence-fastcut-20260915.md
       / submitblock-witness-nonce-20260915.md / mining-mainchain-rootcause-20260915.md

## 现象与实测证据

| 现象 | 证据 |
|---|---|
| 缺 GBT rules 全网停摆 | 9/14 20:05-21:35，全网 ini 矿工块 `merkle root is invalid`，tip 停 44394998 |
| 本地在分叉上挖，blocks==headers 自洽 | 9/15 本地 44398592，peer 主链 44398410（高 182 块） |
| 挖的块上不了主链 | 9/15 00:39:31 `not on the network main chain`；浏览器长期不见本地块 |
| 带交易块被拒 | 9/15 10:09-10:10 多次 `block merkle root is invalid`（补 nonce 前） |
| 误 ban 健康同类节点 | 9/15 00:33:49 ban 112.244.169.18（本地分叉请求分叉块 → not found → 误判） |
| 修复后 | 9/15 10:30 起挖到块成功上链，blocks==headers 正常推进 |

## 根因（三层，逐层修复）

### ① GBT 缺 `rules:["segwit"]` → 矿工 merkle 用 wtxid → 全网停摆

btcd 的 `btcjson.GetBlockTemplateResult` 无 Rules 字段；sugarmaker 依据该字段选择
merkle 叶子算法，缺失时判定 segwit=false → 交易叶子误用 wtxid（完整序列化哈希，
`sha256d(tx)`）；正确应剥离 witness 用 txid。[mempool 空时仅自建 legacy coinbase
恰好无恙；带 witness 交易必错 → `merkle root is invalid` → 全网拒绝。

修复：`GetBlockTemplateResult` 加 `Rules []string`；GBT reply 填 `["segwit"]`
（Sugarchain 创世即激活 SegWit，恒定值）。

### ② 本地挖的块推进 bestHeader → 分叉自洽 → 永不 reorg

本地挖的块被 [accept.go C1](file:///c:/Users/adest/Desktop/git/Mimo/apiserver/new/sugarchain-node/backend/blockchain/accept.go#L293-L321)
无条件按 workSum 推进 header 视图 → header 链跟随本地分叉 → blocks==headers 自洽 →
本地以为在主链上 → 不再拉主链 header → 分叉永不暴露 → 永久在分叉上挖。

对比 umami：无独立 header 视图，active tip 恒为最大 work 链（ActivateBestChain
主动循环）；本地块上 chain 受 "prev 必须真实存在" 约束，无特殊待遇。

修复：
- **C1 修正**：本地挖的块（`flags&BFMinerSubmit`）**不**推进 bestHeader
  视图——header 链只代表网络确认的主链，本地块要等网络 relay 确认（对齐 umami）。
- **GBT 同步门控（miningSyncGuard=1）**：`bestChain.Height` 落后
  `bestHeader.Height` 超 1 块即暂停出模板（-10 "Block download catching up"），
  block 追平再挖——消灭"挖必被拒块"的算力浪费。
- **心跳机制已移除**（曾 current 时 30s getheaders 探测分叉）：实测误伤
  （09-15 09:47: 本地 header 领先 peer 1 块的正常传播延迟被误判为分叉回滚）。
  umami 无主动探测，靠 peer 推送 + workSum reorg；C1 + BFMinerSubmit 守卫
  （本地块须衔接 header 链确认的父块）已覆盖原需求。

### ③ submitblock 缺"补 witness nonce"→ 本地块理想形态与链上不符

矿工（C/Go 版）coinbase 构造一致：带 commitment 输出、无 witness 32 字节 nonce
（legacy 序列化）。umami 在 submitblock RPC 提交时自动调
`UpdateUncommittedBlockStructures` 补 32 字节零 reserved value
（[mining.cpp L984-986](../../../backend/umami/src/rpc/mining.cpp)），
ini-node 初版缺此步。

修复：`handleSubmitBlock` 提交前，若 coinbase 有 commitment 输出且输入无
witness → 补零 nonce，并**重新序列化整块再反序列化**（btcutil.Tx 从 rawBytes
哈希：仅改内存 MsgTx 会让 rawBytes 陈旧，HasWitness() 判定与剥离逻辑错位 →
coinbase txid 算错 → merkle mismatch；重建后 rawBytes 与 MsgTx 一致）。

## 修复清单（按提交）

| 组件 | 变更 |
|---|---|
| btcjson/chainsvrresults.go | `GetBlockTemplateResult.Rules []string` |
| rpcserver.go | GBT reply `Rules:["segwit"]`；`miningSyncGuard=1` 门控；submitblock 补 nonce+重建块 |
| blockchain/accept.go | C1：本地块（BFMinerSubmit）不推进 bestHeader 视图 |
| netsync/manager.go | 心跳机制移除（字段/常量/探测/判定全删） |
| 保留 | fastCutOnDoNotExtend / maybeFastCutFront（header 下载期 do-not-extend 快速切链与 2 票兜底）、headerLocator 全链 locator |

## 验证

- 9/14 晚：rules 修复后挖矿恢复（merkle 错误消失）
- 9/15 10:30：补 nonce+重建后挖到块成功上链（区块浏览器可见），
  blocks==headers==44405192 正常推进
- go build ./... / go vet 通过

## 遗留观察

- 挖矿期间 `RPC authentication failure` 刷屏（前端 ini.exe 用错凭证调 RPC）——
  非本次修复范围，建议前端配置与 ini-node rpcuser/rpcpass 对齐。