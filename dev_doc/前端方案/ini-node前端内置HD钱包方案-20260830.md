# ini-node 前端内置 HD 钱包方案 / Built-in HD Wallet for ini-node Frontend

> 制定日期：2026-08-30　状态：**定稿（HD 版）**，开始分步实施
> 目标：让 ini-node 前端（Wails GUI）的 Wallet / Create / Send 页面从"mock 空数据"变为**真实 HD 钱包**。
> 演进：单钥方案 → **HD（BIP32/44 + BIP39 助记词 + 邮箱密码 Legacy 登录）**——单进程内嵌，比 btcwallet 更适配 Sugarchain fork。
> 关联：`dev_doc/未完成/钱包对接方案-20260814.md`（web-wallet↔节点对接）、待办 #1（联调）、web-wallet `internal/api/token.go`（代币 REST 参考）。

---

## 一、结论速览 / TL;DR

**在 ini-node backend 内置 HD 钱包**：
- **HD（BIP32/44）**：`btcutil/hdkeychain`（已在依赖）从种子派生主钥，BIP44 路径 `m/44'/<coinType>'/0'/0/N` 派生地址
- **BIP39 助记词**：12/24 词，支持备份/恢复（go-bip39 轻量依赖）
- **邮箱密码登录（Legacy KDF）**：移植 web-wallet `LegacyRegularSeed`，`(邮箱,密码)→种子→同一 HD 派生`；确定性登录即恢复（兼容原 web-wallet 钱包）
- 私钥/种子**加密落盘**（passphrase → AES-GCM），未解锁不出内存
- 余额/历史/UTXO 复用 **sugarindex**（getaddressbalance/getaddressutxos/getaddressdeltas）
- 发送复用 `txscript`/`psbt` 本地签名 + `sendrawtransaction`（对齐 umami 选币→建交易→签名→广播）
- 前端 mock services 接真实钱包 RPC（走现有 rpcproxy）
- **代币层走 REST 接入**（非节点实现）：后端代理 `tokenstest.sugar.wtf`（OP_RETURN + 链外记账，web-wallet 已验证）

**不做**：给节点实现代币协议（不必要，代币账本在链外服务）、硬件钱包、多签。**选型**：不采用 btcwallet（双进程 + fork 适配成本高）。

---

## 二、选型对比 / Why HD over btcwallet / single-key

| 维度 | btcwallet | 内置单钥 | **内置 HD（本方案）** |
|---|---|---|---|
| 成熟度 | ★★★ | ★ | ★★ |
| 单进程 | ❌ 双进程 | ✅ | ✅ |
| Sugarchain 适配成本 | **高**（fork 大项目） | 低 | 低（hdkeychain 已在） |
| 助记词恢复/多地址 | 全 | 无 | 全（BIP39+BIP44） |
| 维护负担 | 高 | 低 | 低-中 |

---

## 三、现状盘点 / Current State

### 3.1 前端（mock）
`getWallet()` 空 / `getHistory()` 空 / `unlock()` 恒 true / `buildPsbt()` 抛错；UI（Wallet/Create/Send）已就绪只缺数据源。

### 3.2 可复用资产
- web-wallet `internal/wallet`（key/kdf）、`internal/tx`（BuildAndSign）、`internal/address`（DeriveAll）——参考实现
- ini-node backend 已有：`btcec`、`address`、`txscript`、`psbt`、**`btcutil/hdkeychain`**、sugarindex RPC、`sendrawtransaction`、RPC 认证（rpcproxy 注入）

---

## 四、目标架构 / Architecture

```
ini-node.exe（Wails 前端） Wallet/Create/Send ← 真实数据
   │ rpcproxy（/rpc → 127.0.0.1:8334）
   ▼
backend (btcd fork)
  ├─ wallet RPC（新增）：createwallet/getwalletinfo/getnewaddress/listtransactions/
  │                     listunspent/walletpassphrase/buildpsbt/signrawtransaction/sendtoaddress…
  ├─ backend/wallet/ 包（新增）：HD 密钥 + BIP39 + 加密存储
  ├─ 查询 ← sugarindex　广播 ← sendrawtransaction
  └─ wallet.db（datadir，AES-GCM 加密）
```

---

## 五、详细设计 / Design

### 5.1 HD 密钥（backend/wallet/）
- 主钥：`hdkeychain.NewMaster(seed)`（seed 由 BIP39 助记词 + passphrase 派生）
- 派生：BIP44 `m/44'/<coinType>'/0'/0/N`（Sugarchain 无注册 coinType，取**可配置常量**，默认 0，可后改）
- 地址：`address` 包派生 bech32（默认）/legacy
- 支持导入：随机助记词 / 已有助记词恢复 / WIF 导入（兼容 web-wallet）

### 5.2 BIP39 助记词
- 生成 12/24 词 + 校验和；`go-bip39`（轻量、标准）
- 助记词即备份；恢复 = 助记词 + passphrase → seed → 主钥

### 5.3 密钥存储（backend/wallet/db.go）
- `<datadir>/wallet.db`：master 密钥 **AES-GCM 加密**（passphrase → scrypt 派生 32B）
- 生命周期：启动锁定；`walletpassphrase` 解锁；`walletlock` 丢弃
- 多地址：BIP44 按 index 顺序派生，index 存元数据

### 5.4 钱包 RPC（并入 rpcserver）
| RPC | 作用 | 来源 |
|---|---|---|
| `createwallet` | 建 HD 钱包（BIP39 助记词） | 本地（已实现） |
| `walletlogin` | 邮箱+密码登录（Legacy KDF 派生，确定性恢复） | 本地（新增） |
| `getwalletinfo` | WalletState（余额聚合） | getaddressbalance（已实现） |
| `getnewaddress` | 下一个派生地址 | 本地（已实现） |
| `listtransactions` | Tx[] | getaddressdeltas 聚合（已实现） |
| `listunspent` | UTXO | getaddressutxos（已实现） |
| `walletpassphrase`/`walletlock` | 解锁/锁定 | 本地（已实现） |
| `sendtoaddress` | 选币+建交易+签名+广播 | listunspent + txscript + sendrawtransaction（Step 4 新增） |
| `signrawtransaction` | 签名（P2WPKH） | txscript（Step 4 新增） |
| `tokenbalance`/`tokeninfo`/`tokenparams`/`tokenbuild` | 代币查询/构造（代理 tokenstest REST） | tokenapi 包（Step 6 新增） |
| `dumpprivkey`/`importprivkey`/`dumpwallet`/`importwallet` | 导入导出 | 本地（后续） |

### 5.5 前端接线（services.ts）
`getWallet→getwalletinfo`、`unlock→walletpassphrase`、`login→walletlogin`、`getHistory→listtransactions`、`buildPsbt→buildpsbt+signrawtransaction`（或 `sendtoaddress`）；WalletState 字段映射见上。

### 5.6 安全
- 种子/私钥仅密文落盘；解锁口令不入前端、私钥不进前端
- RPC 沿用 btcd-runtime.ini 认证；钱包 RPC 可加"仅本机回环"约束

### 5.7 邮箱密码登录（Legacy KDF，新增 · 优化版）✅ 已实施（2026-08-30）
- **移植**：web-wallet `internal/wallet/kdf.go` 的 `LegacyRegularSeed` → `backend/wallet/kdf.go`
  （含 JS UTF-16 长度对齐，中文/emoji 不串；52 轮 SHA-256，确定性；**纯 stdlib 无新依赖**）
- **接入**：`NewFromLegacy(email, password, net)` = `LegacyRegularSeed(email,password)` → 32B 种子 → **混合模式**
  - index 0 = 种子直接作为私钥（`P2WPKH`），**与 web-wallet 地址完全一致**（交叉验证通过，老资产登录即恢复）
  - index 1+ = 复用 `NewFromSeed` 的 BIP44 派生（多地址/找零能力保留）
  - 说明：方案原正文"复用 NewFromSeed（同一 BIP44 派生）"与验收"派生相同地址"冲突，经确认采用**混合模式**兼顾两者
- **RPC**：`walletlogin "email" "password"` → 派生 → **纯内存解锁（不落盘）**，与 BIP39 的 `wallet.db` **完全隔离、互不覆盖**
  - 理由（优化）：legacy 确定性派生、登录即恢复，无需加密落盘；**去掉"与 BIP39 二选一共用 wallet.db"的耦合**，两种登录方式天然隔离
  - nextIndex 防地址复用：legacy 用**独立旁车 `wallet.db.legacy.meta`**（不共享 BIP39 的 `.meta`，避免跨模式索引冲突、确保 index 0 始终先分配）
- **特点**：确定性登录即恢复，**无需助记词备份**；兼容原 web-wallet / 原 HTML 钱包（同算法）
- **前端**：登录页（邮箱+密码）与创建页（BIP39 助记词）两个独立入口

### 5.8 代币层接入（REST 代理，新增）
- **背景**：代币 = OP_RETURN + 链外记账，账本在 `https://tokenstest.sugar.wtf`（web-wallet TokenClient 已验证，见其 `internal/api/token.go`）；umami/ini-node 节点本身都无代币逻辑
- **后端**：新增 `backend/tokenapi/` 包（移植 web-wallet TokenClient）+ `tokenrpcserver.go` 代理 RPC，前端走 rpcproxy 避免 CORS/代理问题：
  - `tokenbalance <addr>` → `GET /layer/address/{addr}`
  - `tokeninfo <ticker>` → `GET /layer/token/{ticker}`
  - `tokenparams` → `GET /layer/params`
  - `tokenbuild <type> <args>` → `POST /message/{create|issue|transfer|burn}` 构造 OP_RETURN 负载（配合 sendtoaddress 发送）
- **代码形态（对齐同步方案 v3）**：`tokenapi/` 包 + `tokenrpcserver.go` 均为**新增文件**，不碰共识/blockchain，**上游同步零冲突**（对应 v3 处置清单"新增（应用层）"类）
- **配置**：baseURL 可配（测试网 tokenstest；主网待确认）；外网可达性依赖网络代理
- **边界**：若外网服务不可达或需自建，才是真正的大工程（复刻 OP_RETURN 解析记账），本方案默认不涉及

### 5.9 umami 转账参考（Step 5 对齐）
umami（C++）`sendtoaddress` 流程（`src/wallet/spend.cpp`）：**选币 → 建交易（输出+找零）→ 签名（P2WPKH）→ 广播**。ini-node Step 5 以 `listunspent`（选币）+ 移植 web-wallet `tx.BuildAndSign`（建交易+签名）+ `sendrawtransaction`（广播）对齐实现。

### 5.10 对齐开发规范（优化说明）
- **UI 先行**：Step 7 前端接线前**先设计界面**（开发流程第 2 条）：登录页 / 创建页（助记词）/ Wallet（余额·历史·代币）/ Send
- **新增为主 / 零冲突**：钱包与代币代码全部为**新增文件**（`wallet/`、`walletrpcserver.go`、`tokenapi/`、`tokenrpcserver.go`），不改 btcd 共识/blockchain；rpcserver/server 仅做"新增为主"的薄 fork（加 map/config 字段），**上游同步零冲突**（对齐 v3 处置清单）
- **文档同步**：新增钱包/代币 RPC 后**同步更新 `backend/doc/md/` RPC 文档并标注核对日期**（Checklist 第 6 条）
- **依赖**：仅 `go-bip39`（已在 D 盘缓存）；Legacy KDF 纯 stdlib；代币 REST 用 `net/http`

### 5.11 发送链路（P0 · 新增 2026-08-31，对齐 web-wallet `SendTransactionMulti`）

**架构决策**：发送走 **Wails binding（进程内）**，不走节点 RPC——与 Step 7 已确立的钱包架构一致；UTXO 查询走"本节点 listunspent → 外部 REST `/unspent/{addr}` 降级"两级链（复用 chainSource 设置）。

**后端（frontend/gosend.go 新增 `SendService`）**：
- 移植 web-wallet `internal/tx` 的 `BuildAndSign`（P2WPKH 签名）+ `Input/Output` 结构（来源：web-wallet/sugar-wallet/internal/tx/）
- Bindings：`Send(outputs, fee)`（单/多输出统一，对齐 `SendTransactionMulti`：UTXO 覆盖 total+fee、找零回自身地址、广播失败仍返回 rawHex）、`BroadcastRaw(hex)`（重试路径）、`EstimateFee()`（REST `/fee` 降级链）、`UTXOs()`（Coin Control）
- web 邮箱钱包（单密钥）与 HD（index 0）统一用当前地址签名/找零

**前端**：
- Send 页：多输出行动态增删（≤10）、费用估算按钮、确认 modal（输入数/总额/找零）、结果卡（txid+rawHex+重试按钮）、近期收款人下拉（localStorage）
- Wallet 页 history txid 内链 → 交易详情（依赖 Step 10 Explorer，先做外链）

### 5.12 地址三型切换（P1 · 新增 2026-08-31，对齐 web-wallet `GetAddress`/`RefreshWallet`）

- 移植 web-wallet `internal/address`（`DeriveAll(pubKey, params)`：同公钥派生 bech32/segwit/legacy 三型）
- `WalletService.AddressFor(type)` binding；钱包设置新增"地址类型"（立即生效，钱包页地址/QR/余额/历史全刷新，对齐 web 版 `RefreshWallet` 行为）
- 动机：web 版老用户 segwit/legacy 地址可能有历史余额；切换即可查（本节点 sugarindex 支持任意地址查询）

### 5.13 Explorer 三级页（P2 · 新增 2026-08-31，对齐 web-wallet Explorer）

- 路由：`#/explorer`（链统计+最新区块列表）、`#/explorer/block/<hash>`、`#/explorer/tx/<txid>`
- 数据全走本节点 RPC（getblockchaininfo/getblockhash/getblock/getrawtransaction）——**桌面前端独有优势**（web 版要靠外部 REST）
- 依赖：交易详情需节点 `txindex=1`（未开启时区块内交易只显示 txid 列表）

---

## 六、分步实施 / Steps（按开发规范流程）

| 步骤 | 内容 | 验收 |
|---|---|---|
| **Step 1** ✅ | `backend/wallet/` 包：HD 主钥/派生/BIP39 助记词/地址 + 单测 | ✅ `d9ba99f4`，`go test` 6 例全绿 |
| **Step 2** ✅ | 加密存储 db + walletpassphrase/walletlock | ✅ `450228df`：SaveWallet/UnlockWallet（scrypt+AES-GCM），Lock 清空密钥；建→锁→解锁→地址一致（11 单测全绿） |
| **Step 3** ✅ | 查询 RPC（getwalletinfo/getnewaddress/listtransactions/listunspent + 生命周期） | ✅ `542f7a71`：7 命令实现；查询依赖 sugarindex（未启用明确报错）；待节点验证 |
| **Step 4** ✅ | 邮箱密码登录（Legacy KDF）：移植 kdf.go + NewFromLegacy + walletlogin RPC（纯内存解锁，不落盘）+ 单测 | ✅ `68623664`：混合模式交叉验证通过（index 0 = web-wallet 地址，外部测试向量）；8 单测全绿 |
| **Step 5** | 发送 RPC：sendtoaddress（选币+建交易+签名+广播）+ signrawtransaction | 真实转账上链（对齐 umami 流程） |
| **Step 6** | 代币接入：tokenapi 包（新增）+ token 代理 RPC（新增） | tokenbalance/info/params/build 调通 tokenstest |
| **Step 6b** | **代币四操作对齐**（§ 功能差距清单 P1）：移植 web-wallet `internal/api/token.go` + `TokenTransfer/Create/Issue/Burn` bindings → Tokens tab 真实 UI | 钱包页 Tokens tab 可查余额/转账/创建/增发/销毁（tokenstest 联调） |
| **Step 7** 🔶 | **UI 先行**：设计登录/创建/Wallet/Send/Tokens 界面 → services.ts 去 mock 接线 → 同步 backend/doc/md RPC 文档 | 🔶（2026-08-30）UI 先行部分完成：登录/创建/Wallet/Send 三页改造 + services.ts 钱包查询去 mock + i18n×9；余额/历史已通真实 RPC；🔶（2026-08-30）新增钱包设置二级页面（`wallet-settings` 路由：自动锁定/隐藏余额/历史条数，localStorage 持久化 + 自动锁定接 walletlock RPC）；发送待 Step 8、代币待 Step 6 |
| **Step 8** | **发送链路（P0）**：§5.11——gosend.go 移植 BuildAndSign + Send/BroadcastRaw/EstimateFee/UTXOs bindings → Send 页多输出+确认 modal+近期收款人 | regtest/主网真实转账上链；广播失败可 rawHex 重试 |
| **Step 9** | **地址三型切换（P1）**：§5.12——移植 internal/address 三型派生 + AddressFor binding + 设置项即时生效 | 同一密钥三型地址可查余额/历史（sugarindex 验证） |
| **Step 10** | **Explorer（P2）**：§5.13——三级路由 + 本节点 RPC 数据 + 历史 txid 内链 | 链统计/区块列表/区块详情/交易详情四视图可用 |
| **Step 11** | P3 收尾：WIF 导入（OpenWalletKey 对应 binding）+ Broadcast 并入控制台 | 旧钱包可导入 |

> 每步遵循 `dev_doc/开发流程与提交规范.md`：读现状→方案→分步→双语注释→gofmt/vet/test→同步文档→提交。

---

## 七、边界与后续 / Boundaries
- 多地址已由 BIP44 覆盖；HD 路径/coinType 可配置
- 查任意历史需 `txindex=1`（待办 #2）；getaddressdeltas 不依赖
- **BIP39（wallet.db 落盘）与邮箱密码（纯内存派生）天然隔离**，可同时支持，互不覆盖；邮箱密码确定性恢复、无备份负担
- **代币走 tokenstest REST 代理**，节点不实现代币协议；主网 token 服务地址待确认；外网不可达则代币功能暂不可用
- web-wallet 保留 rpcclient 直连（待办 #3）
- 未来：钱包核心抽共享 go module，两端同源
