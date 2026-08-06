# RPC 使用文档(完整版)/ RPC Usage Guide (Complete)

> 中英双语 · 本节点(btcd 改造版)全部已实现 RPC 服务器命令:作用、参数、用法
> Bilingual · Every implemented RPC command of this modified btcd node: purpose, parameters, usage.

---

## 1. 连接信息 / Connection info

| 项 / Item | 值 / Value |
|---|---|
| RPC 地址 / Endpoint | `https://127.0.0.1:8334/` |
| 协议 / Protocol | JSON-RPC 1.0 over HTTPS(自签证书 / self-signed TLS) |
| 用户名 / User | `qYjMSzVGbXdgPJiuwxfMAp5EM3M=` |
| 密码 / Password | `dSQjlIXRW7ETycroIhjlTqduT0A=` |

> 密码用 HTTP Basic Auth。WebSocket 方式可改用 `authenticate` 命令(见 §4)。
> 端口 8334 = Sugarchain 主网 RPC 端口。

---

## 2. 调用工具 / Call tool

PowerShell 原生 `curl`/`Invoke-WebRequest` 走 schannel,对自签证书 TLS 握手失败
(`SEC_E_INTERNAL_ERROR`)。用 .NET `HttpWebRequest`(强制 TLS1.2 + 信任自签证书)。

现成脚本:`C:\Users\adest\AppData\Local\Temp\opencode\rpc.ps1`

```
用法 / Usage:
  & "<脚本路径>\rpc.ps1" -Method <方法名> [-ParamsJson '<JSON 数组>']

示例 / Examples:
  & rpc.ps1 -Method getblockcount
  & rpc.ps1 -Method getblockhash -ParamsJson '[10000]'
  & rpc.ps1 -Method getblock -ParamsJson '["<hash>", 1]'
  & rpc.ps1 -Method debuglevel -ParamsJson '["warn"]'
  & rpc.ps1 -Method stop
```

- `$Method` 必填;`$ParamsJson` 是可选 JSON 数组字符串,缺省 `[]`。
- 输出 `HTTP <状态码>` + `BODY: <响应体>`;200 = 成功,非 200 看 `error.message`。
- body 必须直接拼接原始 JSON(PowerShell 5.1 `ConvertTo-Json` 会把单元素数组
  折叠成对象导致 params 非法)。详见本文件下方"坑"。

调用格式等价于:

```
POST https://127.0.0.1:8334/
Authorization: Basic <base64(user:pass)>
Content-Type: application/json

{"jsonrpc":"1.0","id":"x","method":"<方法>","params":[<参数...>]}
```

---

## 3. 全部命令清单 / Complete command list

> 下表 = 本节点**已实现**的全部命令(rpcHandlers 分发表)。未列出的 btcjson 命令
> (如 `getwork`、`getmempoolentry`、`getnetworkinfo`、`getblockfilter`)虽可解析,
> 但本节点未实现,会返回 `Command unimplemented`。
> The table below lists every **implemented** command. Registered-but-unimplemented
> commands return `Command unimplemented`.

### 3.1 链与区块 / Chain & blocks

| 命令 / Command | 作用 / Purpose |
|---|---|
| `getbestblock` | 主链最佳区块的高度与哈希 / Best block height+hash |
| `getbestblockhash` | 最佳区块哈希 / Best block hash |
| `getblockcount` | 主链区块数(已连接区块高度)/ Connected block height |
| `getblockhash <height>` | 指定高度的主链区块哈希 / Hash at height |
| `getblock <hash> [verbosity]` | 区块数据;verbosity 0=hex,1=含txid,2=含交易详情 / Block data |
| `getblockheader <hash> [verbose]` | 区块头;verbose=true 返回 JSON,false 返回 hex / Header |
| `getblocktemplate` | 挖矿所需区块模板(或提交验证)/ Mining template |
| `getchaintips` | 区块树所有 tip(含主链与分叉)/ All chain tips |
| `getcfilter <hash> [type]` | 区块已提交过滤器(compact filter)/ Committed filter |
| `getcfilterheader <hash> [type]` | 区块过滤器头 / Filter header |
| `getdifficulty` | 相对最小难度的 PoW 难度 / Difficulty multiplier |
| `invalidateblock <hash>` | 标记区块无效(用 reconsiderblock 恢复)/ Invalidate |
| `reconsiderblock <hash>` | 恢复被 invalidateblock 的区块 / Reconsider |
| `verifychain [checklevel] [numblocks]` | 验证区块链数据库 / Verify chain DB |
| `getheaders <locators> [hashstop]` | 从已知块哈希开始的区块头序列 / Batch headers |

### 3.2 交易与内存池 / Transactions & mempool

| 命令 / Command | 作用 / Purpose |
|---|---|
| `createrawtransaction <inputs> <amounts> [locktime]` | 构造未签名原始交易 / Build raw tx |
| `decoderawtransaction <hex>` | 解码序列化交易为 JSON / Decode tx |
| `decodescript <hex>` | 解码脚本 / Decode script |
| `estimatefee` | 估算费率(聪/千字节)/ Fee estimate |
| `getrawmempool [verbose]` | 内存池交易哈希(或详情)列表 / Mempool txs |
| `getrawtransaction <txid> [verbose]` | 交易数据;verbose=0 hex,=1 JSON / Tx data |
| `gettxout <txid> <index> [mempool]` | 未花费输出(UTXO)信息 / Unspent output |
| `sendrawtransaction <hex> [allowhighfees]` | 提交并广播交易 / Broadcast tx |
| `testmempoolaccept <txs> [maxfeerate]` | 测试交易能否进内存池 / Mempool accept test |
| `gettxspendingprevout <outputs> [include_mempool]` | 扫内存池找花费某输出的交易 / Tx spending an outpoint |
| `searchrawtransactions <address> [verbose] [skip] [count] [vinextra] [reverse]` | 按地址查原始交易 / Tx by address |

### 3.3 网络与节点 / Network & node

| 命令 / Command | 作用 / Purpose |
|---|---|
| `addnode <peer> <add\|remove\|onetry>` | 增删持久化节点 / Persistent peers |
| `getaddednodeinfo <dns> [node]` | 手动添加节点信息 / Added node info |
| `getconnectioncount` | 活跃连接数 / Active connections |
| `getcurrentnet` | 当前网络类型 / Network ID |
| `getheaders <locators> [hashstop]` | 头部批量下载 / Header batch (P2P 用) |
| `getnettotals` | 网络流量统计 / Traffic stats |
| `getnodeaddresses [count]` | 已知可连节点地址 / Known addresses |
| `getpeerinfo` | 每个连接节点的数据 / Peer info |
| `node <connect\|remove\|disconnect> <target> [perm\|temp]` | 增删节点(通用)/ Add/remove peer |
| `ping` | 向所有节点发 ping / Send pings |
| `uptime` | 服务运行时长(秒)/ Server uptime |
| `version` | JSON-RPC API 版本 / API version |

### 3.4 挖矿 / Mining

| 命令 / Command | 作用 / Purpose |
|---|---|
| `getgenerate` | 是否开启挖矿 / Mining on? |
| `gethashespersec` | 挖矿哈希率(近期)/ Mining hashrate |
| `getmininginfo` | 挖矿相关信息 / Mining info |
| `getnetworkhashps [blocks] [height]` | 全网哈希率估算 / Network hashrate |
| `setgenerate <generate> [genproclimit]` | 开/关挖矿(simnet/regtest)/ Toggle mining |
| `generate <numblocks>` | 生成区块(simnet/regtest 专用)/ Generate blocks |
| `submitblock <hex> [params]` | 提交区块到网络 / Submit block |

### 3.5 通用与调试 / General & debug

| 命令 / Command | 作用 / Purpose |
|---|---|
| `getinfo` | 综合状态信息 / General state |
| `getmempoolinfo` | 内存池信息 / Mempool info |
| `help [command]` | 全部命令列表或指定命令帮助 / Help |
| `debuglevel <level>` | 动态改日志级别(见 §5)/ Change log level |
| `stop` | 优雅停机 / Shutdown |
| `signmessagewithprivkey <key> <message>` | 用私钥签名消息 / Sign message |
| `verifymessage <address> <signature> <message>` | 验证消息签名 / Verify message |
| `validateaddress <address>` | 验证地址合法性(无钱包)/ Validate address |
| `getblockchaininfo` | 链状态总览(软分叉状态)/ Chain state info |

### 3.6 Sugarchain 地址索引扩展 / Address-index extensions

| 命令 / Command | 作用 / Purpose |
|---|---|
| `getaddressbalance <address>` | 地址余额(含未成熟/可花费)/ Address balance |
| `getaddressesbalance {"addresses":[<地址>]}` | 多地址余额合计 / Multi-address balance |
| `getaddressutxos {"addresses":[...]} [amount] [chainInfo]` | 地址 UTXO 列表(chainInfo=true 附链头)/ Address UTXOs |
| `getaddressdeltas {"addresses":[...],"start":h,"end":h,["chainInfo"]}` | 高度区间内地址余额变动 / Address deltas |
| `getaddresstxids {"addresses":[...],"start":h,"end":h}` | 高度区间内地址相关交易 ID / Address txids |
| `getaddressmempool {"addresses":[...]}` | 地址在内存池的未确认输入/输出 / Mempool entries |
| `getblockhashes <high> <low> [{"noOrphans":b,"logicalTimes":b}]` | 高度区间区块哈希 / Block hashes in range |
| `getspentinfo {"inputs":{"txid":"<txid>","index":n}}` | 查询某个输出在哪个高度被花费 / Where an outpoint was spent |

---

## 4. WebSocket 专用命令 / WebSocket-only commands

仅经 `/ws` WebSocket 连接可用(HTTP POST 不支持)。可用于订阅通知与重扫。
WebSocket-only;used for notifications and rescan.

| 命令 / Command | 作用 / Purpose |
|---|---|
| `authenticate <user> <pass>` | WebSocket 认证(仅在不带 HTTP Basic Auth 头时需要)/ Authenticate |
| `notifyblocks` | 区块连接/断开时推送 `blockconnected`/`blockdisconnected` / Block notifications |
| `stopnotifyblocks` | 取消上面的订阅 / Stop block notifications |
| `notifyreceived <addresses>` | 收到发往指定地址的交易时推送(旧,建议用 loadtxfilter)/ Receive notifications |
| `stopnotifyreceived <addresses>` | 取消订阅 / Stop receive notifications |
| `notifyspent <outpoints>` | 某输出被花费时推送(旧)/ Spend notifications |
| `stopnotifyspent <outpoints>` | 取消订阅 / Stop spend notifications |
| `notifynewtransactions [verbose]` | 新交易进池时推送 `txaccepted(verbose)` / New tx notifications |
| `stopnotifynewtransactions` | 取消订阅 / Stop new-tx notifications |
| `session` | 当前 WebSocket 连接详情 / Session details |
| `loadtxfilter <reload> <addresses> <outpoints>` | 加载/增补交易过滤器 / Load tx filter |
| `rescanblocks <blockhashes>` | 按已加载过滤器重扫指定区块 / Rescan blocks |
| `rescan <beginblock> <addresses> <outpoints> [endblock]` | 按地址/输出重扫全链(旧)/ Rescan chain |

推送通知(主动消息)对应命令:按订阅时说明,可能收到
`blockconnected`、`blockdisconnected`、`filteredblockconnected`、
`filteredblockdisconnected`、`txaccepted`、`txacceptedverbose`、
`relevanttxaccepted`、`recvtx`、`redeemingtx`、`rescanprogress`、`rescanfinished`。

---

## 5. debuglevel — 动态改日志级别 / Change log level at runtime

```
& rpc.ps1 -Method debuglevel -ParamsJson '["warn"]'        # 全部子系统设 warn
& rpc.ps1 -Method debuglevel -ParamsJson '["CHAN=debug,SYNC=info"]'  # 按子系统
& rpc.ps1 -Method debuglevel -ParamsJson '["show"]'         # 列出可用子系统
```

- 级别:`trace` < `debug` < `info` < `warn` < `error` < `critical`。
- 子系统:`AMGR ADXR BCDB BMGR BTCD CHAN DISC INDX MINR PEER RPCS SCRP SRVR SYNC TXMP`。
- 已知怪癖:返回 `Done.` 后,日志可能打一条
  `[ERR] RPCS: Failed to marshal reply: rpcversion '' is invalid`
  (btcjson 空 rpcversion 的序列化问题),**级别实际已生效**,可忽略。
- 若 BODY 为空且没返回 `Done.`,说明 params 没发对(见 §2 的"坑")。

---

## 6. 常用排障流程 / Common troubleshooting flow

**判断是否卡在高度索引空洞(此前 bug):**
```
& rpc.ps1 -Method getblockcount                          # 停在 9999 = 怀疑卡死
& rpc.ps1 -Method getblockhash -ParamsJson '[10000]'     # 找不到 = 索引空洞
& rpc.ps1 -Method getblockhash -ParamsJson '[9999]'      # 正常 → 确认空洞断点
```

**恢复:** 先 `stop` 节点,用 `cmd/dbprobe.exe -repair` 补 `heightidx` 空洞,
再用修复后的 `btcd-new.exe` 启动续传。启动后 `getblockcount` 应持续增长、越过断点。

---

## 7. 坑 / Pitfalls

1. **PowerShell JSON 序列化**:`ConvertFrom-Json` + `ConvertTo-Json` 会把单元素
   数组折叠成 `{"value":["warn"],"Count":1}`,params 非法、RPC 静默失败(空 BODY,
   debuglevel 不生效)。必须直接拼 JSON 字符串。
2. **自签 TLS**:schannel 握手失败是正常的,须用 .NET HttpWebRequest + 信任回调。
3. **只读命令的权限**:多数查询命令(表内标 Y)对受限用户可用;管理命令(标 N)
   需要管理员 RPC 用户。
4. **`estimatefee`/`getnetworkhashps` 返回 `-1` 表示不可估算**(早期高度/数据不足)。

---

## 8. 相关文档 / Related docs

- 完整英文 API 参考(含每个命令返回值结构):
  `docs/json_rpc_api.md`(btcd 生成)
- 数据库探测与修复:见 `doc/md/IMPORTANT_ERRORS.md` 与 `cmd/dbprobe`
