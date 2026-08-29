# 区块同步死循环修复：缺 witness 数据（2026-08-22）

> 现象：重启后区块停在 43,760,265 不动，日志出现 **101 次回滚循环**。
> 结论：**不是伪造链**，是主网真实块（极可能含步骤三转账），本地块数据缺 witness 导致验证失败。

## 现象（btcd.log 关键行）

```
Stored block 1f370294306dc7f4a216b5df797483952eae2cf622b1990a59cce5aae1398817
(height 43760266) failed to connect:
failed to validate input ...: witness program must have clean stack
(input witness [], input script bytes , prev output script bytes 0014f09e...)
-- local data inconsistent with header chain, rolling back
```

- 失败块 hash：`1f370294306dc7f4a216b5df797483952eae2cf622b1990a59cce5aae1398817`
- 高度：43760266（本地链 tip 43760265，恰差这一块）
- `prev output script bytes 0014...` = **P2WPKH（SegWit v0 输出）**
- 回滚循环：Stored block 失败 → 回滚 → header 被标 invalid → 重下 header 又被拒（`known to be invalid`）→ 再回滚 → 101 次
- 伴随大量 `Got unrequested block ... -- disconnecting`（peer 被误断）

## 根因

1. `buildBlockRequest`（backend/netsync/manager.go）一直用 **`InvTypeBlock`**（无 witness）请求块
2. Sugarchain **从创世激活 SegWit**（chaincfg `DeploymentSegwit` 高度 0，对齐 umami `SegwitHeight=0`，Bech32 前缀 `sugar`），链上存在 P2WPKH 交易
3. 缺 witness 数据的块无法通过脚本验证（P2WPKH 输入必须带 witness 签名）
4. 43760266 验证失败 → 被误判为"伪造链"回滚 → header 标 invalid → 死循环

## 修复方案（已实施，go build 通过）

| 文件 | 修改 |
|---|---|
| `backend/netsync/manager.go` `buildBlockRequest` | peer 支持 witness 时请求 **`InvTypeWitnessBlock`**（与 handleInvMsg 一致） |
| `backend/database/interface.go` + `backend/database/ffldb/db.go` | 新增 **`DeleteBlock`** 接口/实现 |
| `backend/blockchain/chain.go` | 新增 **`RemoveBlockData`**：删坏块数据 + 清除 `statusDataStored` 标志 |
| `backend/netsync/manager.go` `reconnectStoredBlocks` | RuleError（缺 witness 是**数据问题**）→ **删除坏块数据重新下载**，不再回滚 header 链 |
| `backend/blockchain/accept.go` `maybeAcceptBlock` | 连接成功（connectBestChain 通过）后 **`UnsetStatusFlags(statusInvalidAncestor)`** 并 flush——清除上次回滚持久化的误标 invalid 状态 |
| `backend/blockchain/accept.go` `maybeAcceptBlock` prev 检查 | **best chain 上的 prev 节点忽略 invalid 标志**（链上节点不可能真 invalid，误标不阻断子块连接） |

## 第二轮验证发现（重启后仍卡 43760266→43760267）

首次编译重启后：`height=43760266` 已推进（witness 修复生效，43760266 连接成功），但日志：
```
Rejected block 1ca7f41c...: previous block 1f370294... is known to be invalid
```
原因：之前 101 次回滚的 `InvalidateHeaderChain` 把 43760266+ 的 header 标记 `statusInvalidAncestor` 并 **flushToDB 持久化**；重启加载后该标志仍在，且 `maybeAcceptBlock` 连接成功只 `SetStatusFlags(statusValid)` 不清除它，`KnownInvalid()`（`status&(statusValidateFailed|statusInvalidAncestor)`）仍为真 → 43760267 的 prev 检查（accept.go）拒绝 → 永远连不上。

**补充修复**（accept.go 两处）：连接成功后清除该标志 + prev 检查对链上节点放行。

## 第三轮验证发现（重启后仍卡：header 路径拒绝）

第二次编译重启后：`height=43760266` 已连接（getblockcount 确认），但回滚循环变成 **header 应用路径**：
```
Header range at height 43760267 failed to apply:
previous block 1f370294... is known to be invalid -- rolling back header chain
```
原因：之前回滚的 `InvalidateHeaderChain` 把 43760266+ 的 header 标记 `statusInvalidAncestor` 并持久化；块路径（maybeAcceptBlock）修复只放行了**块连接**，但 **header 应用**（maybeAcceptBlockHeader）的 prev 检查（accept.go 原 239 行）和**自身节点检查**（原 284/287 行）仍直接拒绝 → `processReadyHeaderRanges` 报错 → 又触发 rollbackFabricatedHeaderChain → 循环（6 次回滚点 43760266）。

**补充修复**（accept.go maybeAcceptBlockHeader 两处）：
1. **prev 检查放行**：`!b.bestChain.Contains(prevNode) && KnownInvalid()` 才拒绝——链上节点不可能真 invalid，误标不阻断子 header
2. **自身节点检查**：节点带 `statusValidateFailed`/`statusInvalidAncestor` 但 prevNode 在 best chain 上时，**清除该标志继续验证**（误标），否则才拒绝

## 修复后自愈路径

坏块 43760266 被删 → `haveInventory` 判定无此块 → 用 witness 请求重新下载 → 验证通过 → 正常推进。

## 待办

- [ ] 重新编译 `frontend/bin/btcd.exe`（当前运行 20:43 二进制**不含**这些修复）
- [ ] 重启节点验证（预期首次启动自动删坏块重下，同步恢复）
- [ ] 确认提交合并推送（远程 `ini-node`）
