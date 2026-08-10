# umami (Sugarchain) Go 重构 — 阶段总结

> 仓库：`sugarchain-node`（btcd v0.26.2 fork）
> 对照参考：`../../../backend/umami/`（Sugarchain 基于 Bitcoin Core 25 的 C++ 客户端）
> 更新时间：2026-08-03

## 项目目标

将 Sugarchain（umami, Bitcoin Core 25 派生）用 Go 重写。基础采用 btcd，
需要移植 Sugarchain 相对 Bitcoin 的全部共识修改，核心为：

1. **Yespower PoW**（替代 SHA256d）—— 挖矿与工作量证明
2. **SugarShield-N510 DAA**（每块重算难度，替代 2016 块周期重定向）
3. **Sugarchain 网络参数**（magic、端口、powLimit、BIP 生效高度、地址前缀、补贴曲线）

## 已完成的移植

### 1. Yespower 1.0 纯 Go 实现 — `yespower/yespower.go`

- N=2048, r=32, personalization =
  `"Satoshi Nakamoto 31/Oct/2008 Proof-of-work is essentially one-CPU-one-vote"`
  （74 字节，与 `../../../backend/umami/src/primitives/block.cpp` GetPoWHash 的 perslen 一致）
- 复用 `pbkdf2`/`crypto/hmac` 标准库，SIMD 版 salsa20/8 核心
- **正确性由已知答案测试证明**：`pow/pow_kat_test.go`
  `TestTestnetGenesisPoW` 用 C++ testnet genesis 头断言 PoW hash
  `0032f49a73e00fc182e08d5ede75c1418c7833092d663e43a5463c1dbd096f28` ✅ PASS
- 性能：约 22.5 ms/hash（16 核），与 C++ 量级一致（这是慢但必要的代价）

### 2. PoW 集成 — `pow/pow.go`

- `pow.BlockPoWHash(header)`：以 `BtcEncode` 序列化 80 字节头并计算 yespower
- 接入点：
  - `blockchain/validate.go` `checkProofOfWork`（区块/header 验证）
  - `mining/cpuminer/cpuminer.go` `solveBlock`（挖矿求解）
  - `wire/blockheader.go` `PoWHash()` 辅助方法（供外部使用，不参与索引）
- 注意：**区块身份/索引 hash 仍用 SHA256d**（`BlockHash()`），
  PoW hash 只用于工作证明与挖矿 —— 与 C++ `GetPoWHash()`/`GetHash()` 分工一致

### 3. SugarShield-N510 DAA — `blockchain/difficulty.go` `calcNextRequiredDifficulty`

- 常数：窗口 510 块、目标出块 5s、窗口跨度 2550s、上调上限 32%、下调上限 16%
  （min 2142s / max 3366s）
- 每块重算（无 2016 周期）；取窗口内各块 nBits 之和求平均目标
- 时间边界用 11 块中位时间（`CalcPastMedianTime`，对应 C++ `GetMedianTimePast()`）
- 阻尼：仅应用偏差的 1/4（`new = 2550 + (actual-2550)/4`）
- 运算顺序：先除后乘（`bnAvg/2550 * nActualTimespan`），与 C++ 一致
- 结果 clamp 到 powLimit

### 4. Sugarchain 网络参数 — `chaincfg/params.go` + `chaincfg/sugar_params.go`

| 项 | mainnet | testnet | regtest |
|---|---|---|---|
| 端口 | 34230 | 43230 | 18444 |
| magic | 0x9d4beb9f | 0x709011b0 | 0xad5bfbaf |
| powLimit | 2^246-1 (0x003f…) | 同主网 | 0x0f0f… (32B) |
| PowLimitBits | 0x1f3fffff | 0x1f3fffff | 0x200f0f0f |
| 目标出块 | 5s | 5s | 5s |
| 减半间隔 | 12,500,000 | 12,500,000 | 150 |
| bech32 HRP | `sugar` | `tugar` | `rugar` |

- 关键修复：主网/测试网 powLimit 应为 **2^246-1**（紧凑形 0x1f3fffff），
  regtest 为 **0x0f0f…**（紧凑形 0x200f0f0f）—— 均与 C++ chainparams.cpp 对齐
- 说明：`chaincfg` 已拆分为独立 go module（`go.mod` 中 `replace` 指向 `./chaincfg`）
- 存在两份参数：`params.go`（btcd 结构命名，被 `mainNetParams` 引用，激活）与
  `sugar_params.go`（Sugar 风格导出）。**两处需保持同步**

### 5. IBD 性能 — 镜像 umami PR #122

- C++ 在 IBD header 下载期跳过 `CheckBlockHeader` 的 PoW 检查
  （`../../../backend/umami/src/validation.cpp:3985`），仅在非 IBD 时全量校验
- Go 对应：`netsync/manager.go` `handleHeadersMsg` 在 `ibdMode` 下传
  `blockchain.BFNoPoWCheck`，否则 `BFNone`
- 效果：header 同步 ~35/s → **~500-870 headers/s**

## 验证结果（2026-08-03）

1. `go build ./...`、`go vet`、`sugarchain-node.exe` 构建通过
2. `TestTestnetGenesisPoW` PASS —— yespower 与 C++ 字节级一致
3. 主网真实节点 header 同步：
   - 150,000+ headers 接受，**零** difficulty/验证错误
   - 与 api.sugarchain.org 独立交叉验证一致：
     height 1 `ce8a0df3…`、height 2 `67d3e607…`、height 100 `982f01d3…`、genesis `7d5eaec2…`
4. 未通过的单测均为 **btcd 原厂测试数据与 Sugar 参数不兼容**导致
   （见 `doc/md/PENDING_TESTS.md`），非回归

## 尚未完成

- 完整 43.6M header 同步到 tip（~14h）与 tip hash 核对
- 区块（非 header）下载与全量校验（Merkle、交易、PoW 全量）
- header 持久化（重启免重下）
- 原厂单测适配 / regtest 挖矿 / 钱包与地址前缀验证

## 测试命令

```powershell
cd sugarchain-node
go build ./...
go test ./pow/ ./yespower/            # 已知答案 + 基准
go test ./...                          # 全量（部分原厂 fixture 失败，见 Plan）
go test ./blockchain/ -run TestXxx     # 定向
```

## 节点运行

```powershell
# 主网（临时数据目录，RPC 127.0.0.1:8335）
.\sugarchain-node.exe --debuglevel=debug --rpcuser=ini --rpcpass=ini `
  --rpclisten=127.0.0.1:8335 --notls --datadir=$env:TEMP\sugar-mainnet `
  --listen=0.0.0.0:34231
```
