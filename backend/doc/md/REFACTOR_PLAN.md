# umami Go 重构计划

## 第一性原理

umami = Bitcoin Core + 17 项共识修改

Go 重构 = btcd (Go Bitcoin) + 移植这 17 项修改

---

## 一、代码逻辑分析

### 1.1 区块验证流程

```
P2P 收到 BLOCK 消息
  └─ ProcessNewBlock()
       ├─ CheckBlock()
       │    ├─ CheckProofOfWork(Yespower)  ← 不是 SHA256
       │    ├─ BlockMerkleRoot()
       │    ├─ CheckTransaction() × N
       │    └─ 大小/签名检查
       │
       ├─ AcceptBlockHeader()
       │    ├─ ContextualCheckBlockHeader()
       │    │    ├─ 验证 nBits == GetNextWorkRequired()  ← SugarShield
       │    │    ├─ 检查时间戳
       │    │    └─ BIP 版本检查
       │    └─ AddToBlockIndex()
       │
       └─ ActivateBestChain()
            └─ ConnectTip()
                 └─ ConnectBlock()  ← 核心
                      ├─ CheckTxInputs() × N
                      ├─ CheckInputScripts() × N
                      ├─ UpdateCoins() × N
                      ├─ WriteAddressIndex()
                      ├─ UpdateAddressUnspentIndex()
                      ├─ UpdateSpentIndex()
                      └─ WriteTimestampIndex()
```

### 1.2 交易验证流程

```
P2P 收到 TX 消息
  └─ AcceptToMemoryPool()
       ├─ CheckTransaction()  ← 基础检查
       ├─ CheckTxInputs()  ← 输入验证
       ├─ CheckInputScripts()  ← 脚本验证
       ├─ addAddressIndex()  ← 内存池地址索引
       ├─ addSpentIndex()  ← 内存池花费索引
       └─ addUnchecked()  ← 加入内存池
```

### 1.3 难度调整算法 (SugarShield-N510)

```
GetNextWorkRequired(pindexLast)
  │
  ├─ STEP 1: 收集 510 个区块的 nBits
  │    bnTot = sum(nBits[0..509])
  │
  ├─ STEP 2: 计算平均目标
  │    bnAvg = bnTot / 510
  │
  ├─ STEP 3: 计算实际时间跨度
  │    nActualTimespan = MTP(last) - MTP(first)
  │
  ├─ STEP 4: 阻尼调整（只应用 25% 偏差）
  │    dampened = 2550 + (actual - 2550) / 4
  │
  ├─ STEP 5: 钳制到 [1734, 3366]
  │    // 最大下降 32%，最大上升 16%
  │
  └─ STEP 6: 计算新目标
       new_target = avg / 2550 * clamped
```

### 1.4 Yespower PoW

```
GetPoWHash(block_header)
  │
  ├─ 序列化区块头
  │    data = header.Serialize()
  │
  ├─ Yespower 1.0 参数
  │    version = YESPOWER_1_0
  │    N = 2048  (内存开销)
  │    r = 32    (块大小)
  │    pers = "Satoshi Nakamoto 31/Oct/2008..."
  │    perslen = 74
  │
  └─ 计算哈希
       hash = yespower_tls(data, len, params)
       return hash  ← 256 位
```

---

## 二、重构方案

### 2.1 技术栈

| 组件 | 选择 | 理由 |
|------|------|------|
| 基础 | btcd (fork) | Go Bitcoin 参考实现 |
| UI | Wails v2 | Go + WebView2 |
| 存储 | LevelDB (Go) | 与 umami 兼容 |
| Yespower | CGo 封装 | 复用 C 实现 |
| DAA | 纳米 Go | SugarShield-N510 |

### 2.2 项目结构

```
sugarchain/
├── chaincfg/
│   ├── params.go              ← 修改网络参数
│   ├── sugar_params.go        ← Sugarchain 主网参数
│   └── sugar_testnet.go       ← 测试网参数
│
├── yespower/
│   ├── yespower.go            ← Go 接口
│   ├── yespower.c             ← CGo 封装 umami 的 C 代码
│   └── yespower.h
│
├── sugarshield/
│   └── difficulty.go          ← SugarShield-N510 算法
│
├── blockchain/
│   ├── chain.go               ← 修改验证逻辑
│   ├── difficulty.go          ← 替换难度算法
│   └── index.go               ← 地址/花费/时间索引
│
├── mining/
│   ├── miner.go               ← 修改挖矿逻辑
│   └── cpuminer.go            ← CPU 挖矿
│
├── rpc/
│   ├──Mining.go               ← 修改 RPC
│   └── sugarindex.go          ← 自定义 RPC
│
├── wire/
│   └── message.go             ← 修改网络魔术字
│
├── cmd/
│   └── sugarchaind/
│       └── main.go            ← 节点入口
│
├── wallet/
│   ├── manager.go             ← 钱包管理
│   ├── key.go                 ← 密钥管理
│   └── tx.go                  ← 交易构建
│
└── frontend/
    └── wails/                 ← 桌面 UI
```

### 2.3 共识参数修改清单

| # | 参数 | btcd 值 | Sugarchain 值 | 修改文件 |
|---|------|---------|--------------|---------|
| 1 | PoW Hash | SHA256d | Yespower 1.0 | chaincfg/params.go |
| 2 | DAA | 2016-block retarget | SugarShield-N510 | blockchain/difficulty.go |
| 3 | MAX_MONEY | 21,000,000 BTC | 1,073,741,824 SUGAR | chaincfg/params.go |
| 4 | 初始奖励 | 50 BTC | 42.94967296 SUGAR | chaincfg/params.go |
| 5 | 减半间隔 | 210,000 | 12,500,000 | chaincfg/params.go |
| 6 | 出块时间 | 10 分钟 | 5 秒 | chaincfg/params.go |
| 7 | powLimit | 0x00000000FFFF... | 0x003FFFFF... | chaincfg/params.go |
| 8 | BIP34 高度 | 227,931 | 17 | chaincfg/params.go |
| 9 | BIP65/66 | 高度激活 | 基因块激活 | chaincfg/params.go |
| 10 | SegWit | 高度激活 | 基因块激活 | chaincfg/params.go |
| 11 | 创世块 | 2009-01-03 | 2019-08-14 | chaincfg/params.go |
| 12 | 网络魔术 | f9beb4d9 | 9feb4b9d | chaincfg/params.go |
| 13 | 端口 | 8333 | 34230 | chaincfg/params.go |
| 14 | 地址前缀 | "1"/"3"/bc1 | "S"/"s"/sugar1q | chaincfg/params.go |
| 15 | 规则变更阈值 | 95%/2016 | 75%/12240 | chaincfg/params.go |
| 16 | 最低难度(测试网) | 允许 | 禁用 | chaincfg/params.go |
| 17 | BIP30 异常 | 有 | 无 | blockchain/validate.go |

---

## 三、实施阶段

### Phase 1: 基础节点 (2 周)

**目标：** 能编译、能运行、能同步区块

```
Week 1:
├── Fork btcd
├── 修改 chaincfg/params.go (16 项常量)
├── 修改区块链验证逻辑
├── 基本 P2P 连接
└── 编译测试

Week 2:
├── 实现 SugarShield-N510 DAA
├── 修改挖矿奖励公式
├── 基本 RPC 服务器
└── 同步测试
```

**验证标准：**
- `go build ./cmd/sugarchaind` 编译成功
- 能连接到 Sugarchain 网络
- 能接收区块
- 能验证区块（正确的难度检查）

### Phase 2: Yespower 集成 (1-2 周)

**目标：** 实现正确的 PoW 验证

```
Week 3:
├── 封装 umami 的 Yespower C 代码
├── CGo 接口实现
├── PoW 验证集成
└── 性能测试

Week 4:
├── CPU 挖矿实现
├── 难度验证测试
└── 与 umami 节点对比验证
```

**验证标准：**
- 能验证 umami 产生的区块
- 能自己挖出区块
- PoW 性能可接受（< 1s per hash）

### Phase 3: 索引 + RPC (1 周)

**目标：** 实现地址索引和自定义 RPC

```
Week 5:
├── 地址索引实现
├── 花费索引实现
├── 时间索引实现
├── getaddressutxos RPC
├── getaddresstxids RPC
└── getaddressbalance RPC
```

**验证标准：**
- `getaddressutxos` 返回正确结果
- `getaddresstxids` 返回正确结果
- 索引在重启后保持

### Phase 4: Wails 桌面 (2 周)

**目标：** 完整的桌面钱包

```
Week 6:
├── Wails 项目初始化
├── 节点管理 UI
├── 同步进度显示
└── 钱包基本功能

Week 7:
├── 创建/导入钱包
├── 发送/接收交易
├── 交易历史
├── 设置页面
└── 测试打包
```

**验证标准：**
- `wails build` 编译成功
- 能创建钱包
- 能显示余额
- 能发送交易
- 能显示同步进度

---

## 四、关键技术决策

### 4.1 Yespower 集成方案

**推荐：CGo 封装**

```go
package yespower

// #cgo CFLAGS: -I./c
// #cgo LDFLAGS: -L./c -lyespower
// #include "yespower.h"
import "C"

func Hash(input []byte) [32]byte {
    var result [32]byte
    C.yespower_tls(
        (*C.uint8_t)(unsafe.Pointer(&input[0])),
        C.size_t(len(input)),
        &params,
        (*C.uint8_t)(unsafe.Pointer(&result[0])),
    )
    return result
}
```

**理由：**
- 直接复用 umami 的 C 实现
- 性能最优
- 避免重新实现复杂密码学

### 4.2 存储兼容性

**目标：** 与 umami 数据目录兼容

```
~/.sugarchain/
├── blocks/           ← 区块数据（扁平文件）
├── chainstate/       ← UTXO 集合（LevelDB）
├── blocks/index/     ← 区块索引（LevelDB）
├── sugar-index/      ← 地址/花费/时间索引（LevelDB）
└── wallet.dat        ← 钱包数据
```

### 4.3 P2P 兼容性

**目标：** 能与 umami 节点互通

- 相同的网络魔术字
- 相同的消息协议
- 相同的区块格式
- 相同的交易格式

---

## 五、风险评估

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| Yespower CGo 编译问题 | 中 | 高 | 提前测试跨平台编译 |
| P2P 协议不兼容 | 低 | 高 | 严格对照 btcd 和 umami |
| 难度算法实现错误 | 中 | 高 | 用测试向量验证 |
| 性能不足 | 低 | 中 | Yespower 有缓存优化 |
| 存储格式不兼容 | 中 | 中 | 保持 LevelDB 格式 |

---

## 六、验证清单

### 6.1 共识验证

- [ ] 能连接 Sugarchain 网络
- [ ] 能接收并验证区块
- [ ] 能验证 Yespower PoW
- [ ] 能正确计算难度（SugarShield-N510）
- [ ] 能正确计算区块奖励
- [ ] 能正确处理减半

### 6.2 交易验证

- [ ] 能验证 P2PKH 交易
- [ ] 能验证 P2WPKH 交易
- [ ] 能验证 P2SH-P2WPKH 交易
- [ ] 能验证 P2TR 交易
- [ ] 能正确计算手续费

### 6.3 索引验证

- [ ] getaddressutxos 返回正确结果
- [ ] getaddresstxids 返回正确结果
- [ ] 索引在重启后保持

### 6.4 挖矿验证

- [ ] 能生成区块模板
- [ ] 能进行 CPU 挖矿
- [ ] 能广播新区块

### 6.5 钱包验证

- [ ] 能创建钱包
- [ ] 能导入/导出密钥
- [ ] 能生成地址
- [ ] 能构建交易
- [ ] 能签名交易
- [ ] 能广播交易

---

## 七、与现有项目的关系

| 项目 | 作用 | 是否保留 |
|------|------|---------|
| umami | C++ 参考实现 | 保留 |
| btcd | Go Bitcoin 基础 | Fork |
| web-wallet | 桌面钱包 UI | 集成 |
| wallet-api-server | REST API | 参考 |

---

## 八、时间线

```
Week 1-2:  基础节点（编译、P2P、验证）
Week 3-4:  Yespower 集成（PoW、挖矿）
Week 5:    索引 + RPC
Week 6-7:  Wails 桌面 UI
Week 8:    测试、优化、打包
```

**总估算：8 周**
