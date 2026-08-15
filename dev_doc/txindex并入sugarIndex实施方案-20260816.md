# 正式实施方案 A：txindex 并入 sugarIndex（getrawtransaction 支持）

> 制定日期：2026-08-16
> 背景：`txindex=1` 当前**未生效**——`sugarindex=1` 时 server.go:3158 用 sugarIndex 覆盖含 txindex 的 indexers.NewManager（有意设计，避免 btcd 原生索引浪费）。`/transaction` 端点因缺 getrawtransaction 不可用。
> 目标：在 sugarindex 内实现 txid→region 索引，使 `getrawtransaction`（按哈希查任意历史交易）可用，**单一索引架构**、复用已调优的并行重建。

---

## 一、设计总览

```
sugarindex LevelDB 内新增 txid 表（与现有 address/spent 表同级）：
  key:   txid (32B)
  value: blockfile (4B LE) + offset (4B LE) + txLen (4B LE)   ← 指向主库块文件 region

查询 getrawtransaction(txid):
  txid → 查 txid 表 → 得 (blockfile, offset, len)
  → 读主库 ffldb 块文件 region → 反序列化 wire.MsgTx → 返回
```

- **单一索引**：不引入 btcd 原生 indexers，架构与"umami 字节兼容、sugar 主导"原则一致
- **复用并行重建**：txid 表随 sugarindex 重建（connectBlock 批量写）顺带填充，无二次全量扫描
- **磁盘增量**：每笔交易 12B（file+offset+len）→ 预计 +1-2 GB（Sugarchain 交易量中等）

---

## 二、数据结构

### 2.1 txid 表（sugarindex LevelDB 新增 bucket/前缀）

```go
// 键空间（在现有索引键前缀之外新加一个前缀，避免冲突）：
// 现有: addressIndexKey/addressUnspentKey/addressSpentKey/...（见 sugarindex/db.go）
const txIndexKeyPrefix = 0x07   // 新前缀，与现有混淆键区分

// key:  [1B prefix][32B txid]
// value: [4B blockfile][4B offset][4B txLen]   ← 主库块文件定位
type txIndexValue struct {
    blockfile uint32
    offset    uint32
    txLen     uint32
}
```

### 2.2 内存字段（Manager 新增）

```go
// Manager 新增
txIndexEnabled bool          // 是否写 txid 表（默认 true，随 sugarindex 一起）
```

---

## 三、重建接入（connectBlock 批量写）

### 3.1 在 `connectBlockBatch` 中填充 txid 表

```go
// connectBlockBatch 现有逻辑（写 address/spent）之后追加：
func (m *Manager) connectBlockBatch(block *btcutil.Block, stxos []blockchain.SpentTxOut, batch *leveldb.Batch) error {
    // ...现有 address/spent 写入...

    // txid → region 映射（每笔交易一条）
    blockFile, blockOffset, err := m.dbBlockRegion(block)   // 从主库读块文件定位（或从 block 元数据）
    if err != nil {
        return err
    }
    for _, tx := range block.Transactions() {
        key := append([]byte{txIndexKeyPrefix}, tx.Hash()[:]...)
        value := make([]byte, 12)
        byteOrder.PutUint32(value[0:], blockFile)
        byteOrder.PutUint32(value[4:], blockOffset+uint32(tx.MsgTx().SerializeSize()))  // 需精确 offset
        byteOrder.PutUint32(value[8:], uint32(tx.MsgTx().SerializeSize()))
        batch.Put(key, value)
    }
    return nil
}
```

> **关键**：块内交易 offset 需精确（txid → 交易在块文件中的字节偏移）。实现时从主库读块原始字节，逐交易序列化累加偏移，或复用 ffldb 的 block region 机制（`FetchBlockRegion`）。**不重建时（正常同步）ConnectBlock 同样写**，保持 tip 后索引完整。

### 3.2 与并行重建的衔接

- 并行 worker（sugarindex 已有 4 worker 读 + 串行批量写）**无需改动**——txid 写入在 `connectBlockBatch` 内，随现有批次一起落盘
- wipeIndex 需同时清空 txid 表（现有 wipe 按前缀删除，补 `txIndexKeyPrefix`）

---

## 四、getrawtransaction 查询实现

### 4.1 RPC 处理（sugarindex/rpc.go 新增）

```go
func (m *Manager) handleGetRawTransaction(...) {
    txid := ...  // 解析参数
    // ① 查 txid 表
    raw, err := m.db.Get(append([]byte{txIndexKeyPrefix}, txid[:]...))
    if err != nil {
        return -5  // 交易不存在
    }
    // ② 读主库块文件 region（需要区块链层提供 region 读取）
    region := database.BlockRegion{Hash: blockHash, Offset: offset, Len: txLen}
    // ③ 反序列化 MsgTx → 返回
}
```

### 4.2 主库 region 读取（需 blockchain 暴露）

sugarindex 是独立 LevelDB，**读主库块文件需要 blockchain 提供接口**：

```go
// blockchain 新增导出方法（chain.go / chainio.go）：
func (b *BlockChain) FetchBlockRegion(region *database.BlockRegion) ([]byte, error)
// 内部: b.db.FetchBlockRegion(region)（ffldb 已有此能力）
```

> 或者：`getrawtransaction` 需要完整交易（含确认高度/输入输出），更简单路径是**用 txid 表拿 region 后，走 dbTx.FetchBlockRegion**。需在 Manager 里持有 blockchain 引用（当前 sugarindex.Manager 未持有，需在 NewManager 时传入或注册回调）。

---

## 五、实施步骤

| 步骤 | 内容 | 文件 |
|------|------|------|
| P1 | 定义 txIndexKeyPrefix + txIndexValue 序列化/反序列化 | `sugarindex/db.go` |
| P2 | `connectBlockBatch` 写 txid 表（块内交易精确 offset） | `sugarindex/indexer.go` |
| P3 | wipeIndex 清空 txid 表（补前缀） | `sugarindex/indexer.go` |
| P4 | blockchain 暴露 `FetchBlockRegion` | `blockchain/chain.go` |
| P5 | sugarindex 持有 blockchain 引用 + `handleGetRawTransaction` RPC | `sugarindex/rpc.go` + `NewManager` 签名 |
| P6 | server.go 注册 getrawtransaction（或复用 btcd 的 handleGetRawTransaction 走 indexManager） | `server.go` |
| P7 | 编译 + 重启（触发一次全量重建填充 txid 表，并行 1.5-3h） | — |
| P8 | 验证：`getrawtransaction <txid>` 返回正确交易 + `/transaction` 端点可用 | — |

---

## 六、验证标准

1. `getrawtransaction <已知txid>` 返回完整交易（与链上一致）
2. `/transaction/{hash}` 端点不再报 `-5: transaction index must be enabled`
3. 随机抽查 N 个 txid（不同高度/区块）均可查到
4. 重建期间进度显示正常（复用 progress.json）
5. 孤儿判定修复继续生效（重启不白重建）

---

## 七、风险与边界

| 风险 | 等级 | 缓解 |
|------|------|------|
| 块内交易 offset 算错 → 读错数据 | 中 | 逐交易序列化累加 + 反序列化回验（读出后 hash 对比） |
| 主库 region 读取接口新增影响面 | 低 | `FetchBlockRegion` 纯转发 ffldb 已有能力 |
| 重建期间 getrawtransaction 返回过期 | 低 | 表未填充前返回"索引未就绪"错误码 |
| txid 表与块文件 region 漂移（重组） | 中 | 重组时 DisconnectBlock 删除该块 txid 条目（需实现） |
| 磁盘 +1-2 GB | 低 | 可接受，单表 12B/交易 |

---

## 八、与备选方案对比（结论回顾）

| | 方案 A（本方案） | 方案 B（双 Manager） |
|---|---|---|
| 架构 | ✅ sugar 单一索引 | ❌ 两套索引 |
| 重建 | 一套（并行 1.5-3h） | 双重建 |
| 磁盘 | +1-2 GB | +2-4 GB |
| 实现 | 中（新表 + 查询 + region） | 中-高（改 IndexManager 接口 + 驱动问题） |
| **结论** | **采纳** | 否 |
