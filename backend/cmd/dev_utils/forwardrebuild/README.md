# forwardrebuild —— 一次性前向重建工具

> 开发/运维工具（dev_utils）。仅用于修复 A3 异步写崩溃遗留的 **UTXO 一致点超前于 chainstate** 形态。正常情况下（方案 1 修复后的代码）不会产生该形态，此工具属一次性恢复工具。

## 背景：这个工具解决什么问题

A3（异步写队列）把主链元数据（块索引行 / best state / 高度索引 / spend journal）改为批量异步落盘，水印（`writewatermark`）记录最后落盘高度。崩溃瞬间可能出现：

```
UTXO 一致点 (utxostateconsistency) = 44339408
chainstate (chainstate)            = 44339388   ← 落后 UTXO 20 块
```

启动逻辑检测到一致点高于链尖时拒绝启动（避免用错误的 UTXO 状态继续连接）：

```
[ERR] Unable to start server on [:34230]: utxo consistency point ... (44339408)
      is above the chain tip (44339388): the on-disk UTXO set includes blocks
      missing from the chain state -- a forward-rebuild of the main-chain
      metadata is required
```

**为什么不能靠同步自愈**：UTXO 集合已是 44339408 的终态——44339389~44339408 的消费结果已从集合中删除。重新下载并连接这些块时，输入查询全部落空（双花），共识层硬拒绝，永远连不上。回退 UTXO 也死路：回退需要撤销这 20 块的消费，靠 spend journal——而 spend journal 恰好也在丢失的元数据段里。

**唯一自洽路径是前向对齐**：UTXO 终态可信且一致，把 chainstate / 高度索引 / best-tip 快照 / A3 水印**补写**到 UTXO 一致点，让 chainstate 追上 UTXO。

## 原理

- 块体（`dbStoreBlock`）在 A3 中始终**同步**落盘，因此 44339389~44339408 的块体完整存在。
- 从 UTXO 一致点 hash 沿块体 `PrevBlock` 链回退，直到 chainstate hash——得到这段缺失元数据的完整块序列。
- 逐块重建：块索引行（`blockheaderidx`，header+status）、hash→height / height→hash 索引（`hashidx`/`heightidx`）、workSum 累加、totalTxns 累加。
- 单事务写入新 chainstate（`serializeBestChainState` 格式）、best-tip 快照、A3 水印，与 UTXO 一致点对齐。

## 用法

```bat
rem 构建（在 backend/ 目录下）
go build ./cmd/dev_utils/forwardrebuild/

rem 阶段 1：只读备份受影响 key（chainstate / 一致点 / 水印）到
rem   <blocks_ffldb 上级目录>/forwardrebuild-backup-<时间戳>/keys.json
forwardrebuild.exe -dbpath <blocks_ffldb目录> -backup

rem 阶段 2：只读干跑校验——沿块体回退、校验块体齐全、prev 链连续、
rem   打印每块信息与补写后的新 chainstate（不写任何东西）
forwardrebuild.exe -dbpath <blocks_ffldb目录> -check

rem 阶段 3：应用——单事务补写块索引行 + 高度索引 + chainstate + 快照 + 水印
forwardrebuild.exe -dbpath <blocks_ffldb目录> -apply
```

示例（生产节点数据）：

```bat
forwardrebuild.exe -dbpath C:\path\to\data\sugarmainnet\blocks_ffldb -check
```

## 三阶段说明

| 阶段 | 是否写库 | 作用 |
|---|---|---|
| `-backup` | 否 | 导出 chainstate / 一致点 / best-header 状态 / 水印当前值，用于出错回滚 |
| `-check` | 否 | 干跑：从一致点沿块体回退到 chainstate，校验段内块体完整、链连续，打印补写目标（应等于 UTXO 一致点 hash） |
| `-apply` | 是 | 单事务补写：块索引行、hash/height 索引、新 chainstate、best-tip 快照、A3 水印 |

**务必先 `-backup` 再 `-check`，`-check` 输出确认与一致点吻合后再 `-apply`。**

## 回滚

出错时用备份文件恢复：

- `keys.json` 中的 `chainstate` / `utxostateconsistency` / `bestheaderstate` / `writewatermark/height` 为应用前的原始值。
- 可直接写回对应 key（或用任何 ffldb 编辑器）；块索引行与高度索引为新增（原为空），无需删除。

## 验证记录（2026-09-11）

- 生产节点：chainstate 44339388 → 44339408（`-apply` 后启动正常，同步恢复，无双花冲突）。
- `-check` 输出：20 块（44339389~44339408）块体齐全、prev 链连续，新 chainstate hash 与 UTXO 一致点 `1b40fd...` 精确吻合。
