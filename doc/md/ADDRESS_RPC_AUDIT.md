# 地址 RPC 契约审计 / Address RPC Contract Audit

> 状态:已确认 · 更新时间:2026-08-03
> 结论:wallet-api-server 对齐 **umami** 节点的地址 RPC 契约。

## 背景 / Background

wallet-api-server(Go 重写的原 Python api-server)通过 `rpc.Client` 调用节点地址索引 RPC。
节点候选有两个 C++ 实现,契约部分不一致:

- **umami**(`../../../backend/umami/src/rpc/index.cpp`)——新参考实现,btcd 重写节点(sugarchain-node)以其为镜像目标。
- **sugarchain**(`sugarchain/src/rpc/misc.cpp`)——旧 C++ 节点,`getspentinfo` 于 2020-06-04 被重写为数组式(commit `37a137ef0`)。

决策:对齐 **umami**。理由:btcd 重写全套逻辑(难度、PoW、IBD)都以 umami 为参照,
wallet-api-server 最终要跑在 btcd 之上;对齐 umami = Go 服务与底层节点单一事实来源,避免分叉。

## 逐 RPC 对比 / Per-RPC Comparison

| RPC | umami 契约 | Go 服务调用 | 状态 |
|---|---|---|---|
| `getaddressbalance` | `[addr]` → `{balance, balance_immature, balance_spendable, received}` | `[addr]` 原样透传 | ✓ 需改(透传与原 Python 一致,额外字段随节点) |
| `getaddressutxos` | `[addr, amount?, chainInfo?]` → 数组 `{address,txid,outputIndex,script,satoshis,height}` | `[addr, Amount(sat)]` | ✓ |
| `getaddresstxids` | `[addr, start?, end?]` → `[]string`(高度升序) | `[addr]` | ✓ |
| `getaddressmempool` | `[addr]` → 数组 `{address,txid,index,satoshis,timestamp,prevtxid?,prevout?}` | `[addr]` | ✓ |
| `getspentinfo` | `[{txid,index}]` → 单对象 `{txid,index,height}`,未花费抛 `RPC_INVALID_ADDRESS_OR_KEY` | 已修复 | ✅ 已改+已测 |

### 关键结论 / Key Points

1. umami 的 `getAddressesFromParams`(`index.cpp:99`)同时接受纯字符串与 `{addresses:[...]}` 对象,Go 的 `[addr]` 全部兼容。
2. `getaddressbalance` 透传行为与原 Python `address.py` 一致;umami 多返回 `balance_immature`/`balance_spendable` 只是节点字段,esplora 的 `received-balance` 计算仍成立。
3. **唯一需要修的是 `getspentinfo`**:旧 Go 代码用 `[txid, n]` 两个位置参数,与 umami(`[{txid,index}]` 单对象)和 sugarchain(`[txid]` 数组)都**不匹配**,导致 `/esplora/tx/:hash/outspends` 全部返回 `spent:false`。

## 修复 / Fix

文件:`../../../backend/wallet-api-server/internal/service/esplora.go` → `Outspends`

- 请求:`getspentinfo [{"txid": thash, "index": n}]`(按每个 vout 一次)
- 响应:解析单对象 `{txid, index, height}`,未花费 RPC 抛错 → 置 `spent:false`
- 字段映射:umami `index`(花费输入序号)→ Esplora `vin`;补全 `status` 的 `block_height/hash/time`(经 `getblockhash` + `getblock`)
- 注释为中英双语 / comments are bilingual

## 测试 / Test

- `TestOutspends`(`internal/service/service_test.go`)覆盖:两个 vout——vout 0 已花费(含完整区块信息)、vout 1 未花费。
- `go test ./internal/service/ -run TestOutspends` → PASS;`go test ./...` → 全绿。