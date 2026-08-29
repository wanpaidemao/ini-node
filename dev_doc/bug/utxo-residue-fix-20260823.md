# 区块同步死循环修复：ErrOverwriteTx 残留 UTXO（2026-08-23）

> 现象：witness 修复后节点同步至高度 43,998,744，块 43,998,745 反复报
> `tried to overwrite transaction ... not fully spent`（BIP30 `ErrOverwriteTx`），
> 日志无限"删块重下"循环，链永远卡在该高度。
> 结论：**不是块数据坏**，是之前伪造链回滚未撤销 UTXO 留下的孤立 coinbase 残留。

## 现象（node.stdout.log 关键行，近 40KB 内 109 次且持续增长）

```
[WRN] SYNC: Stored block ee172d68ed2e70109a86c3dbea8156828f4a72335f3438d58e3f1e6fddb3337a
(height 43998745) failed to connect with a rule error:
tried to overwrite transaction 6303b71074d23f1f494f17192d0c606de46d26dd8073982ab6182dfaca7c48cf
at block height 43998745 that is not fully spent
-- deleting incomplete block data and re-downloading
```

- 失败块 hash：`ee172d68ed2e70109a86c3dbea8156828f4a72335f3438d58e3f1e6fddb3337a`
- 高度：43998745（本地链 tip 43998744，恰差这一块）
- 冲突交易：`6303b71074d23f1f494f17192d0c606de46d26dd8073982ab6182dfaca7c48cf`，`gettxout` 实测为 **coinbase**、值 5.36870912、P2WPKH，`confirmations: 0`

## 根因

1. **UTXO 只在块成功连接时写入**（`connectBestChain` → `utxoCache.connectTransactions` → flush 落盘）
2. 之前的会话曾成功连接过 43998745（或其分叉块），coinbase `6303b710:0` 写入了 UTXO 集并持久化
3. 之后链被回滚（witness 修复前的伪造链误判，见 witness-sync-fix-20260822.md），而回滚路径
   `rollbackFabricatedHeaderChain` → `InvalidateHeaderChain` **只做三件事**：
   - 给高于回滚高度的 header 打 `statusInvalidAncestor` 标记
   - 重建 bestHeader 视图
   - 清除 DB 里高于回滚高度的 height→hash 索引行
   
   **从不 disconnect 块、从不撤销 UTXO**。正常的 `reorganizeChain → disconnectBlock` 才会撤销 UTXO，
   伪造链回滚路径绕过了这条 → coinbase `6303b710:0` 成为"高度 43998745 > tip 43998744"的孤立残留
4. witness 修复后块能正常重下了，但重连 43998745 时 `checkBIP0030`（backend/blockchain/validate.go:1001）
   发现该块 coinbase 在本地 UTXO 集**已存在且未花费** → `ErrOverwriteTx`
5. `reconnectStoredBlocks` 对 RuleError 的策略是"删块重下"——该策略为"缺 witness 数据"设计（重下能拿到
   带 witness 的块）；但这次**头哈希固定**（ee172d68），重下 100 次都是同一个块、同一个 coinbase，
   撞上同一个残留 → **无限循环**

一句话：**回滚时没撤销 UTXO。缺 witness 的块被误判为伪造链回滚 101 次，头链撤了但已连接块的
coinbase 留在 UTXO 集里成了孤儿；之后重连同一块时 BIP30 检查永远撞上这枚残留。**

## 修复方案（已实施，go build / go vet / 单元测试全部通过）

| 文件 | 修改 |
|---|---|
| `backend/blockchain/chainio.go` | 新增 **`deserializeOutpointKey`**：把 utxo bucket 的 key（`<32字节hash><VLQ index>`）解回 outpoint，供 DB 遍历去重 |
| `backend/blockchain/utxocache.go` | 新增 **`PurgeUtxosAboveHeight(height)`**：删除所有块高度 > height 的 UTXO（内存 cache + 磁盘 utxosetv2 bucket 双清理，`seen` 去重计数），返回清理条数 |
| `backend/netsync/manager.go` `reconnectStoredBlocks` | RuleError 分支按错误码分流：**`ErrOverwriteTx`** → 先 `PurgeUtxosAboveHeight(tip)`，`purged > 0` 则**重试连接同一存储块**（不再删块重下）；`purged == 0`（无残留、真双花）才落到原删块重下路径 |
| `backend/blockchain/utxocache_test.go` | 新增 `TestPurgeUtxosAboveHeight`（DB+cache 双路径：purge 后高条目删、低条目存、二次 purge 为 0、全清）+ `TestPurgeUtxosAboveHeightCacheOnly`（未 flush 的 cache-only 条目同样被清、DB 不受影响） |

## 修复后自愈路径

`ErrOverwriteTx` → 识别为"头链回滚未撤销 UTXO"的状态残留 → `PurgeUtxosAboveHeight(tip)` 清掉
高度 > 43998744 的孤立 coinbase → 重试连接同一存储块 → BIP30 通过 → 正常推进。

日志预期：
```
[WRN] SYNC: Purged 1 stale UTXO entries above height 43998744; retrying block
      ee172d68... (height 43998745)
```

## 验证

| 检查 | 结果 |
|---|---|
| `go build ./...` | ✅ exit 0 |
| `go vet ./blockchain/ ./netsync/` | ✅ exit 0 |
| `go test -run TestPurgeUtxosAboveHeight*` | ✅ 2/2 PASS |

## 待办

- [ ] 重新编译 `frontend/bin/btcd.exe`（当前运行二进制不含此修复）
- [ ] 重启节点验证（预期首次启动日志出现 `Purged ... retrying block`，链恢复推进）
- [ ] 提交合并推送（远程 `ini-node`）
