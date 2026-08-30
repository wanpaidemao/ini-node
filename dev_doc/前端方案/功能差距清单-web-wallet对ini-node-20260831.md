# 功能差距分析:web-wallet vs ini-node 前端(对照清单)

- 日期:2026-08-31
- 依据:逐方法比对 `web-wallet/sugar-wallet/app.go`(前端可调用的全部 Go 绑定)+ `routes/*.svelte` 交互,与 ini-node `frontend/gowallet.go` + `pages/*.svelte` + `services.ts` 现状
- 用途:作为功能补齐的任务来源(比"设计差异文档"更细一层,逐功能点打勾)

## 图例

- ✅ ini-node 已有等价实现
- 🔶 部分实现/占位
- ❌ 缺失
- (栏位)web=web-wallet 的实现方式;ini=ini-node 现状

## 1. 钱包生命周期

| 功能 | web-wallet | ini-node | 状态 |
|---|---|---|---|
| 创建随机钱包 | `CreateWalletType(addrType)` 生成密钥对,返回 WIF/公钥/三型地址 | `Create(passphrase)` BIP39 助记词+加密落盘 | ✅ 路线不同,ini 更强 |
| 邮箱密码打开 | `OpenWalletRegular` | `Login` binding | ✅ |
| WIF 私钥导入 | `OpenWalletKey(wif)` | — | ❌ |
| Saved 加密文件解锁 | `OpenSavedWallet(pwd)`(wallet.enc,AES-GCM+argon2id) | —(BIP39 wallet.db 替代) | ❌ 方案分歧,可不补 |
| 锁定 | `LockWallet` | `Lock` binding + 自动锁定 | ✅ |
| 刷新钱包信息 | `RefreshWallet` | `Status` binding | ✅ |
| **地址类型切换** | `GetAddress(addrType)`,bech32/segwit/legacy 三型地址同密钥派生,**设置里切换后立即生效** | —(HD 派生固定 bech32) | ❌ 高优 |
| WIF 展示/导出 | `buildWalletInfo` 带 WIF 字段 | — | ❌ |
| 保存钱包到文件 | `SaveWallet(password)` | —(BIP39 已落盘) | 不需 |
| 调试 KDF(测试用) | `DebugOpenRegular` | — | 不需 |

## 2. 链上查询

| 功能 | web-wallet | ini-node | 状态 |
|---|---|---|---|
| 余额(confirmed+unconfirmed) | `GetBalance`(REST/RPC 双后端) | getwalletinfo RPC + 外部 REST 降级 | ✅ |
| 任意地址余额 | `GetBalanceOf(addr)` | — | ❌(做浏览器页时需要) |
| 历史 | `GetHistory` 分页,txid/height/satoshis,绿收红支 | listtransactions(不同数据形态) | 🔶 |
| UTXO 列表 | `GetUTXOs(amount)`(amount>0 拉覆盖指定额) | listunspent RPC | 🔶(Send 页 Coin Control 未接) |
| 手续费估算 | `GetFee` → `/fee` | — | ❌(Send 页需) |
| 交易详情 | `GetTransaction(hash)` | — | ❌(浏览器页需) |
| 链信息/区块 | `GetBlockchainInfo/GetBlockByHeight/GetBlockByHash` | getblockchaininfo RPC(节点侧有) | 🔶(数据有,无 UI) |

## 3. 发送交易

| 功能 | web-wallet | ini-node | 状态 |
|---|---|---|---|
| 单输出发送 | `SendTransaction(to, amount, fee)`(UTXO 选币→建交易→签名→广播,找零回自己) | —(Step 5 计划) | ❌ |
| **多输出发送** | `SendTransactionMulti(outputs[], fee)` outputs 上限 10 行 | — | ❌ |
| 发送确认 modal | 余额/地址/费用校验→确认→结果(txid/找零/输入数) | Send 页表单+校验,无确认 modal | 🔶 |
| 广播失败重试 | 广播失败仍返回 rawHex,可走 `BroadcastRawTx` 重试 | — | ❌ |
| 裸交易广播页 | Broadcast 页 | —(控制台可发 RPC) | 🔶 |
| **近期收款地址** | `GetRecentRecipients/RemoveRecentRecipient`(发送页选历史收款人) | — | ❌ |
| 找零地址 | 找零回自身 bech32 地址 | —(随 Step 5) | 随 Step 5 |

## 4. 代币层(Step 6 计划内)

| 功能 | web-wallet | ini-node | 状态 |
|---|---|---|---|
| 代币余额列表 | `GetTokenBalances` | — | ❌(Step 6) |
| 代币元数据 | `GetTokenInfo(ticker)` | — | ❌(Step 6) |
| 代币层参数 | `GetTokenLayerParams` | — | ❌(Step 6) |
| 代币转账/创建/增发/销毁 | `TokenTransfer/TokenCreate/TokenIssue/TokenBurn`(OP_RETURN 负载+marker) | — | ❌(Step 6) |

## 5. 浏览器(Explorer)

| 功能 | web-wallet | ini-node | 状态 |
|---|---|---|---|
| 最新区块列表/链统计 | Explorer 首页 | — | ❌ |
| 区块详情 | `GetBlockByHash/Height` | —(节点 RPC 有数据) | ❌ |
| 交易详情 | `GetTransaction` | — | ❌ |

## 6. 设置

| 功能 | web-wallet | ini-node | 状态 |
|--|--|--|--|
| **后端模式切换(REST/RPC)** | Settings `backend` 字段,`applyConfig` 重建客户端,失败回退 REST | ❌ 只有外部 REST 余额降级(单向) | ❌ |
| 主链 API/代币 API URL | Settings 可改,存 config 文件 | 🔶 钱包设置里有外部 REST URL(单值) | 🔶 |
| **地址类型设置(并立即生效)** | Settings addrType→`RefreshWallet` 立即切显示地址 | — | ❌(依赖三型地址能力) |
| 语言 | Settings 多语 | ✅ | ✅ |
| 代理 | Settings proxyURL→REST 客户端 | — | ❌(低优先) |
| RPC 地址/账密 | Settings(RPC 模式) | ✅ 全局设置页(连接 tab) | ✅ |
| 节点参数(maxpeers 等) | — | ✅ ini-node 特有 | — |

## 7. 窗口/桌面

| 功能 | web-wallet | ini-node | 状态 |
|---|---|---|---|
| 最小化/最大化/关闭(自定义标题栏) | `MinimizeWindow/MaximizeWindow/CloseWindow` | —(系统标题栏) | 低优 |
| 近期邮箱列表(磁盘) | `GetRecentEmails/Remove` | localStorage 钱包卡片(等价) | ✅ |

## 8. 优先级建议(基于使用频率)

1. **P0 - 发送链路**(Step 5 本来就要做):单输出+多输出+确认 modal+费用估算+找零+近期收款人。web-wallet 的 `SendTransactionMulti` 完整逻辑可直接移植(选币→建→签→播)。
2. **P1 - 地址三型切换**:HD 钱包当前只派生 bech32;web 版用户可能有 segwit/legacy 地址余额,切类型查余额是刚需。设置里加"地址类型"项,复用 web-wallet `internal/address` 派生逻辑(同公钥三型地址)。
3. **P1 - 代币四操作**(Step 6 计划内,web-wallet 代码可移植)。
4. **P2 - Explorer 三页**(链统计/区块/交易),数据全在本节点,web 版没有的优势是历史 txid 可内链。
5. **P2 - 任意地址余额/交易详情查询**(浏览器页子功能)。
6. **P3 - WIF 导入、窗口控制、代理、Broadcast 独立页**。

## 9. 验证记录

- 2026-08-31:app.go 40 方法全列(routes 调用对应关系已交叉核对);ini-node 侧对照 gowallet.go(9 binding)/services.ts/pages 实测现状
