# 项目改动记录 / Changelog

> 状态:持续维护 · 创建:2026-08-09
> **维护规则(每次改动前必看)**
> 1. 开始任何代码/配置改动前,先读本文件,确认该改动前人是否已做过。
> 2. 每次改动落地(提交/配置生效)后,**必须**在此文件顶部追加一条摘要:日期、内容、提交 hash、影响。
> 3. 与既有的专项文档(见 §4)联动:改动若触及某专项,同步更新对应文档。

---

## 1. 最近改动 / Recent Changes(倒序,最新在上)

| 日期 | 提交 hash | 内容 |
|---|---|---|
| 2026-08-09 | `453924c4` | yespower: **`sync.Pool` 缓冲池复用~8MB scratch**(V/X/S/B 跨调用复用),单次哈希分配由 ~8.3MB 降至 ~104KB/op,消除 block/header 校验的 GC churn;`Hash()` 签名不变,KAT(`TestTestnetGenesisPoW`)通过;live profile 重验待重启机房节点 |
| 2026-08-08 | `e5d1d9ff` | netsync:落后于链时每 60s 打一条 INFO 同步进度(追上即静默) |
| 2026-08-08 | `2fa55034` | netsync: **IBD 期间周期 flush UTXO 缓存**(5min/次),防重建缺口 |
| 2026-08-08 | `43e347e5` | chain: PoW 跳过锚定到共享 header tip;netsync:切片固定宽,晚到 peer 不丢窗口 |
| 2026-08-08 | `744e00c7` | netsync: 加宽请求窗/orphan 池;切片按送达而非按时间重发 |
| 2026-08-07 | `53bed43a` | netsync: 每 peer 在途块数节流 + 保护 front 切片 |
| 2026-08-07 | `c7ac43bb` | netsync: 释放卡死下载的 peer,避免 worker 永久空闲 |
| 2026-08-07 | `1f827c06` | netsync: 修复并行 block 切片窗口边界 + 过期切片重发 |
| 2026-08-07 | `0a23c0e7` | blockchain: block-index 写入并入 connectBlock 事务(批量 DB 提交) |
| 2026-08-07 | `3ba348ec` | blockchain: IBD 期间每块 O(index) 成本封顶 + 修复窗口化 UTXO 重建 |
| 2026-08-06 | `58328892`/`1c9cb591` | netsync: **并行多 peer 区块下载(按高度分片)** |
| 2026-08-06 | `5e3043e8` | blockchain: 修复 flushToDB height-index 缺口 + dbprobe 流式修复工具 |
| 2026-08-06 | `98ddb81e` | sugarchain: block 下载通过 stored-block cursor 断点续传 |
| 2026-08-05 | `aba8af96` | sugarchain: 并行多 peer 初始 block 下载 + 主机去重 |
| 2026-08-04 | `b263f480` | sugarchain: **并行多 peer header 下载 + leveldb 16MB 表**(104 + 8MB write buffer) |
| 2026-08-04 | `fa37391c` | sugarchain: 内存 header 滑动窗口 + DB 冷读兜底 + bestHeader 续传 |
| 2026-08-03 | `6814a04d` | sugarchain: 移植 yespower PoW、SugarShield DAA、主网参数(DNSSeed 元初) |

## 2. 运行/配置类改动(未入 git 的现场变更)

| 日期 | 项 | 说明 |
|---|---|---|
| 2026-08-08 | 节点运行参数 | `--upnp --debuglevel=warn --profile=6060`;多次重启(含强制关停一次 → 触发 UTXO 重建 38 万→199 万) |
| 2026-08-08 | RPC 密码 | `btcd.conf` 凭据修正(曾有打字符错误导致 401);校验要用 `\n` 结尾 |
| 2026-08-08 | 防火墙 | 新增入站规则「Sugarchain P2P 34230」(TCP),允许他人连入 |
| 2026-08-08 | UPnP | `--upnp` 启动,路由器 `192.168.31.1` SSDP 无响应(发现失败),改靠防火墙+入站 |
| 2026-08-08 | 监控器 | `script/04-sync-rate.go`(+`.exe`):60s 轮询 RPC,输出 height/速率/ETA 到日志(替代旧 .ps1) |
| — | 并行 peer 数 | 现状:同步硬编码 8(`maxHeaderSyncPeers`,`netsync/manager.go:72`);出站上限 8(`defaultTargetOutbound`)。**增大方案见 `RUNTIME_SYNC_PEERS.md`(UI 阶段再做)** |

## 3. 已排查确认的关键结论(勿重复调查)

- **磁盘 I/O 不是瓶颈**:实测写 ~40KB/s;读峰值来自 leveldb 压实(初始化期拉平时时的峰值,属暂态)。
- **内存稳定** ~1.05GB(IBD 中),无泄露;22GB 机器跑全链可行。
- **metadata 10.6GB 组成**:43.75M header 全量索引(`blockheaderidx` 117B/条 + hashidx + heightidx)≈8.3GB 行成本 + leveldb 摊销 → 已是常量,**block 阶段几乎不涨**。
- **块体 ~0.41KB/块**:全链预估 18–45GB。
- **单一区块磁盘**:不必重建——blockheaderidx 必须全量持久(否则重启重拉 43.75M 头,数小时)。
- **同步 0-delta 卡顿**:block 下载期正常(批请求间隙),非卡死;到 tip 后每分钟 ~12 块稳定。
- **单线程 connect**:UTXO 状态机天然串行;可优化的是两侧独立工作(见 §9 待办)。
- **PoW 深度跳过已失效于 block 阶段**:`process.go:189` 依赖 parent 在内存 index 中,但 header 滑动窗口(默认 50000)早已把深高度 parent 冷读落盘 → `prevNode==nil` → 永不 `BFNoPoWCheck`。goroutine 实证当前 block 阶段仍每块调 `checkProofOfWork`。已在 yespower 层做缓冲池复用消除 GC;若要再省可改为"冷读 header 高度哈希比对后跳过"(见 §4)。
- **TestHeaderWindowEviction 独立跑即失败**(期望 12 节点,实际 21,无任何 eviction),纯 index 窗口逻辑、与 yespower 无关 → 属既有基线失败。

## 4. 待办 / 候选优化(已会议论,未做)

优先序(取决于 60s CPU profile 结果,`--profile=6060` 可抓):

1. [x] **CPU profile 归因**(已抓 2 次):主进程在 CPU——GC ~50-65% + yespower ~28%(GC 主因是 yespower 每块 8MB 分配)。
2. [x] **yespower 缓冲池复用**(2026-08-09,未提交):每哈希分配 8.3MB→~104KB,GC churn 弹窗已除;live profile 显示其分配已近乎消失。
3. [x] **新增主网 Checkpoint**(2026-08-09):`sugar_params.go` 指向已验证深块(高度盘查过),`runScripts` 全程关闭 → ECDSA/脚本项从 profile 中消失。
4. [x] **冷读 header 哈希比对跳过 PoW**(2026-08-09,未提交):`process.go` 当 parent 低于内存 header 窗口(`prevNode==nil`)时,经冷 `hashidx`(parent→高度)+`heightidx`(高度→hash)比对本块 hash,相同即 `BFNoPoWCheck`(深块 0.6~0.7 核 yespower 消除);构建+vet+`TestColdReadFallback/TestProcessBlockHeader/TestMaybeAcceptBlockReusesHeaderNode` 通过。
   - **深度收紧为 10000(2026-08-10)**:`consensusPowSkipDepth` 2048→**10000**,且**冷读分支同样套用**该深度——`height <= bestHeader-10000` 才允许免 PoW;最近 1w 块(可 reorg 范围)仍完整校验 PoW。原实现冷读对任意深块 hash 对上即跳过,实际覆盖到 3000 万+ 纵深;现收紧为锚定链深历史(硬编码,界面可调交后续 blocksyncpeers 一类 RPC 讨论)。

5. [x] **RPC creds/startup params -> ini** (2026-08-09, uncommitted): new `Plan\btcd-runtime.ini` ([Application Options]) holds rpcuser/rpcpass/datadir/headerwindow/rpclisten/profile/upnp/addcheckpoint; 05-run.ps1 launches with `--configfile`; 01/02/03 read creds+port via Get-RpcIni; 04-sync-rate.go added `-ini` (exe rebuilt). No more creds on the command line.
6. [x] **cold-read PoW skip live measure** (2026-08-09): yespower gone from profile, 1.3->3.2 cores (cgocall/ffdb ~35%, GC ~20%); sync 30-50 -> steady 160-265 bl/s (peak 265), ETA ~37h; next bottleneck: network + ffldb writes.
7. [ ] 网络侧:出站 `defaultTargetOutbound` 8→16 + `maxHeaderSyncPeers` 8→16;日志显示单一 peer 反复 stall → 每次 freeze(200-600 块)。
8. [ ] 预校验 worker 池(与 UTXO 无关校验离串行段;改动最大,最后做)。

## 5. 存储/架构速查(详见 ARCHITECTURE.md)

- 块体:`.fdb` flat 文件,512MiB/文件,记录 `<net,4B><len,4B><block,变长><CRC32 casts,4B>`;定位 `ffldb-blockidx`(hash32 → loc12B)。
- header:80B;磁盘两份(块文件头部 80B + `blockheaderidx` 桶 `heightBE4+hash32` → `header80+status1`);
  `hashidx`(hash→height LE4)、`heightidx`(height→hash)两跳。
- 读 header 冷路径 <access leveldb 不碰 .fdb;读 block 走 loc→ReadAt+CRC 校验。
- 写链:块直落 `.fdb` → loc 进缓存(100MB/5min flush) → 先 sync 后 metadata,崩溃 reconcile 截尾。

## 6. 同步进度速查(2026-08-09)

- 高度 3,346,323(7.65%、ETA ~264h,54 bl/s 波动)。
- 总同步目标: header 43.75M 已完成;block 阶段进行中。

## 7. 测试基线(改代码后需回归)

- `netsync` 并行套件:TestParallelHeaderSync / …DropFrontPeer / …ShortResponse / …AddPeer
- `blockchain`:TestHeaderWindowEviction / TestColdReadFallback / TestLocateInventory / TestBestHeaderState / TestFlushToDB
- `blockchain` 已知失败(基线,非本次引入):PowLimit fixture(TestMaybeAcceptBlockReusesHeaderNode/TestBip0030/TestHaveBlock/TestInvalidateBlock/TestReconsiderBlock)等,详见 PENDING_TESTS.md。

## 8. 构建/回归命令

```powershell
Set-Location sugarchain-node
go build ./... && go vet ./...
go test ./netsync/... ./blockchain/... -run '<TestParallelHeaderSync|TestHeaderWindowEviction|TestColdReadFallback|TestLocateInventory|TestBestHeaderState|TestFlushToDB>'
```

## 9. 相关文档索引

| 文档 | 内容 |
|---|---|
| `doc/md/ARCHITECTURE.md` | 存储架构(块/header/索引/读写流程/容量) |
| `doc/md/RUNTIME_SYNC_PEERS.md` | 运行时可调并行 peer 方案(UI 阶段) |
| `doc/md/HEADER_INDEX_MEMORY.md` | header 窗口化方案(已实现) |
| `doc/md/PENDING_TESTS.md` | 已知失败测试清单 |
| `doc/md/` | 生成文档 / DB 与 PoW 探针工具说明 |
