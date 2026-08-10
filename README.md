# sugarchain-node

Sugarchain（umami）的 Go 全节点实现。基于 btcd v0.26.2 fork，移植了
Sugarchain 相对 Bitcoin 的全部共识修改。

**共识特性**

- **Yespower 1.0 PoW**（N=2048, r=32）—— 挖矿与工作量证明，替代 SHA256d
- **SugarShield-N510 DAA** —— 每块重算难度（510 块滑动窗口、中位时间、阻尼）
- **5 秒出块**，每 12,500,000 块减半

---

## 目录结构

```
sugarchain-node/
├── btcd.go            # 入口（btcdMain）
├── config.go          # 配置解析 / 命令行参数
├── params.go          # 各网络（mainnet/testnet/regtest/simnet/signet）参数
├── rpcserver.go       # 内置 RPC 服务器
├── blockchain/        # 区块/header 验证、难度（SugarShield DAA）
├── chaincfg/          # 网络参数（独立 go module，含 Sugar 参数）
├── pow/               # PoW 接口 + 已知答案测试（pow_kat_test.go）
├── yespower/          # Yespower 1.0 纯 Go 实现
├── wire/              # 协议消息（独立 go module）
├── mining/cpuminer/   # CPU 挖矿（用 pow.BlockPoWHash 求解）
└── cmd/genaddr/       # 生成地址的小工具
```

---

## 环境要求

- **Go 1.25+**
- 网络：可访问 Sugarchain 主网节点（默认经 DNS 种子 `seed.sugarchain.site`、`1seed.sugarchain.info`）

---

## 编译

主模块（含可执行文件、blockchain、pow、yespower 等）：

```powershell
cd sugarchain-node
go build ./...
go build -o sugarchain-node.exe .
```

生成二进制 `sugarchain-node.exe`。

子模块（独立 go.mod）单独构建：

```powershell
cd wire
go build ./...
cd ..\chaincfg
go build ./...
```

> 说明：`chaincfg` 与 `wire` 已拆分为独立 go module；主模块的 `go.mod` 通过
> `replace github.com/btcsuite/btcd/chaincfg/v2 => ./chaincfg` 引用本地版本。

### 运行测试

```powershell
go test ./pow/ ./yespower/        # 关键测试（已知答案 + 基准）
go test ./...                     # 全量（部分原厂 fixture 因 Sugar 参数不兼容会失败）
go test ./chaincfg/...            # chaincfg 模块测试（workdir 需在 chaincfg/ 下）
go test ./blockchain/ -run TestXxx   # 定向测试
```

---

## 运行

### 主网（默认）

```powershell
# 前台运行（数据存 %LOCALAPPDATA%\Btcd\data）
.\sugarchain-node.exe

# 推荐：独立数据目录 + 自定义 RPC + debug 日志
.\sugarchain-node.exe --debuglevel=info --rpcuser=ini --rpcpass=ini `
  --rpclisten=127.0.0.1:8335 --notls --datadir=$env:TEMP\sugar-mainnet `
  --listen=0.0.0.0:34231
```

### 测试网 / 回归网

```powershell
.\sugarchain-node.exe --testnet
.\sugarchain-node.exe --regtest
.\sugarchain-node.exe --simnet
.\sugarchain-node.exe --signet
```

### 查看参数与版本

```powershell
.\sugarchain-node.exe --help      # 或 -h
.\sugarchain-node.exe --version   # 或 -V
```

---

## 常用运行参数

> 所有 `--long` 形式参数均可写入配置文件 `btcd.conf`（每行 `key=value`）。
> 默认配置文件路径：Windows `%LOCALAPPDATA%\Btcd\btcd.conf`，可用 `-C/--configfile` 覆盖。

### 网络

| 参数 | 说明 |
|---|---|
| `--testnet` | 使用测试网（sugartestnet，端口 43230） |
| `--testnet4` | 使用 Bitcoin testnet4（仅保留兼容） |
| `--regtest` | 使用回归测试网（sugarregtest，端口 18444） |
| `--simnet` | 使用仿真测试网 |
| `--signet` | 使用 signet |
| `--connect=<addr>` | 只连接指定对等节点（可多次指定） |
| `--addpeer=<addr>` | 启动时额外连接一个对等节点 |
| `--nodnsseed` | 禁用 DNS 种子发现 |
| `--externalip=<ip>` | 向对等节点宣告的外部地址 |
| `--maxpeers=<n>` | 最大对等节点数（默认 125） |
| `--whitelist=<net>` | 不封禁的 IP/网段（如 192.168.1.0/24） |

### 数据 / 日志

| 参数 | 说明 |
|---|---|
| `-C/--configfile=<path>` | 配置文件路径 |
| `-b/--datadir=<dir>` | 数据目录（默认 `%LOCALAPPDATA%\Btcd\data`） |
| `--logdir=<dir>` | 日志目录（默认 `%LOCALAPPDATA%\Btcd\logs`） |
| `-d/--debuglevel=<level>` | 日志级别 `{trace,debug,info,warn,error,critical}`，可用 `<子系统>=<级别>` 细分，`debuglevel=show` 列出子系统 |
| `--listen=<addr>` | 监听地址（默认全部接口，端口见网络表） |
| `--nolisten` | 不监听入站连接 |

### 挖矿

| 参数 | 说明 |
|---|---|
| `--generate` | 使用 CPU 挖矿 |
| `--miningaddr=<addr>` | 出块奖励地址（`--generate` 时必须提供） |
| `--blockmaxsize` / `--blockminsize` | 出块大小限制（字节） |
| `--blockmaxweight` / `--blockminweight` | 出块权重限制 |

### RPC

| 参数 | 说明 |
|---|---|
| `-u/--rpcuser=<user>` | RPC 用户名（启用 RPC 所必需） |
| `-P/--rpcpass=<pass>` | RPC 密码（启用 RPC 所必需） |
| `--rpclisten=<addr>` | RPC 监听地址（默认端口：主网 8334） |
| `--rpccert` / `--rpckey` | RPC TLS 证书/密钥路径 |
| `--notls` | 禁用 RPC TLS（仅允许绑定 localhost） |
| `--norpc` | 禁用内置 RPC 服务器 |
| `--rpcmaxclients` | 标准连接最大 RPC 客户端数（默认 10） |
| `--rpcmaxwebsockets` | RPC websocket 最大数（默认 25） |
| `--rpclimituser` / `--rpclimitpass` | 受限 RPC 用户/密码 |

### 索引

| 参数 | 说明 |
|---|---|
| `--txindex` | 维护完整交易索引（`getrawtransaction` 需要） |
| `--addrindex` | 维护地址交易索引（`searchrawtransactions` 需要） |
| `--droptxindex` / `--dropaddrindex` / `--dropcfindex` | 启动时删除对应索引后退出 |

### 内存 / 性能

| 参数 | 说明 |
|---|---|
| `--utxocachemaxsize=<MiB>` | UTXO 缓存大小（默认 250 MiB） |
| `--sigcachemaxsize=<n>` | 签名验证缓存条目数（默认 100000） |
| `--prune=<MiB>` | 剪枝已确认区块（最小 1536 MiB） |
| `--profile=<port>` | 启用 HTTP pprof 分析（端口 1024-65535） |
| `--cpuprofile` / `--memprofile` / `--traceprofile` | 写出 CPU/内存/执行 trace 文件 |

### 其他

| 参数 | 说明 |
|---|---|
| `--blocksonly` | 不从远端接受交易 |
| `--relaynonstd` / `--rejectnonstd` | 中继/拒绝非标准交易 |
| `--nobanning` / `--banthreshold` / `--banduration` | 封禁策略 |
| `--proxy=<addr>` / `--onion=<addr>` | SOCKS5 代理 / Tor |
| `--v2transport` | 启用 P2P v2 加密传输（BIP324） |
| `--uacomment=<str>` | 在 user agent 后追加注释（BIP 14） |
| `--upnp` | 使用 UPnP 映射端口 |
| `--nocheckpoints` | 禁用内置检查点（慎用） |
| `--addcheckpoint=<h>:<hash>` | 添加自定义检查点 |

### Windows 服务

```powershell
.\sugarchain-node.exe --service=install
.\sugarchain-node.exe --service=start
.\sugarchain-node.exe --service=stop
.\sugarchain-node.exe --service=remove
```

---

## 网络 / 端口速查

| 网络 | 参数 | P2P 端口 | RPC 端口 | magic |
|---|---|---|---|---|
| 主网 | （默认） | 34230 | 8334 | `9d4beb9f` |
| 测试网 | `--testnet` | 43230 | 18334 | `709011b0` |
| 回归网 | `--regtest` | 18444 | 18334 | `ad5bfbaf` |
| 仿真网 | `--simnet` | 18555 | 18556 | — |
| signet | `--signet` | 38333 | 38332 | — |

> RPC 端口在 `params.go` 中由各网络 `rpcPort` 字段定义；
> 默认 8334 与 Bitcoin Core 不同（btcd 约定，钱包进程占 8332）。

---

## RPC 调用示例

```powershell
# 启用 RPC 运行（--notls 需配合 --rpcuser/--rpcpass）
.\sugarchain-node.exe --rpcuser=ini --rpcpass=ini --rpclisten=127.0.0.1:8335 --notls

# 用 curl 调用（HTTP JSON-RPC）
curl -u ini:ini -H "Content-Type: application/json" `
  -d '{"jsonrpc":"1.0","method":"getblockcount","params":[],"id":1}' `
  http://127.0.0.1:8335/

curl -u ini:ini -H "Content-Type: application/json" `
  -d '{"jsonrpc":"1.0","method":"getblockhash","params":[1],"id":1}' `
  http://127.0.0.1:8335/
```

### 常用 RPC 方法

| 方法 | 说明 |
|---|---|
| `getblockcount` | 当前链高度 |
| `getbestblockhash` | 最佳区块哈希 |
| `getblockhash <height>` | 指定高度区块哈希 |
| `getblock <hash> [verbose]` | 获取区块（hex 或 JSON） |
| `getblockheader <hash>` | 获取区块头 |
| `getrawtransaction <txid>` | 获取原始交易（需 `--txindex`） |
| `getnetworkinfo` | 网络/版本信息 |
| `getpeerinfo` | 已连接对等节点 |
| `getchaintips` | 所有链分叉尖端 |
| `stop` | 优雅停机 |

---

## 说明与限制

- **区块身份 hash 使用 SHA256d**（`BlockHash`）；**PoW hash 使用 Yespower**
  （`pow.BlockPoWHash`），两者分工与 C++ umami 的 `GetHash()`/`GetPoWHash()` 一致。
- **IBD 期间 header 下载会跳过 PoW 全量检查**（镜像 umami PR #122，提速 ~35/s → ~870/s），
  区块下载阶段会做全量验证。
- **header-only 节点目前不持久化**：每次重启需重新下载 header（详见 `doc/md/PENDING_TESTS.md`）。
- 主网全量同步约 4,360 万个区块，header 阶段约需 14 小时（网络/机器而定）。

## 相关文档

- `doc/md/PENDING_TESTS.md` — 待测试项
- `doc/md/CHANGELOG.md` — 开发变更日志
- `doc/md/RUNTIME_SYNC_PEERS.md` — 运行时可调并行 peer 方案
- `doc/md/ARCHITECTURE.md` — 存储架构
- `../dev/refactor-summary.md` — 重构总结
- `../dev/sync-verification.md` — 主网 header 同步与交叉验证记录
- 参考实现：`../backend/umami/`（C++，基于 Bitcoin Core 25）
