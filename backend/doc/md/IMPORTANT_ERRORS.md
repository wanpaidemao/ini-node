# 重要错误记录 / Important Errors & Learnings

> 中英双语 · 根目录唯一错误记录文档,以后新错误/教训持续追加到本文件
> Bilingual · Single root-level log; append new errors/learnings here from now on.

---

## 2026-08-06 — 真实节点实跑调试 / Real-node sync debugging

### E1. 启动节点时用了错误的 datadir,导致头部全部重下 / Wrong datadir on relaunch → headers re-downloaded from genesis

**现象 / Symptom:**
新二进制启动后日志显示 `Chain state (height 0, hash 7d5eaec...)`,并开始 `Downloading headers for blocks 1 to 43717965`。
真实数据(9.3GB)其实在自定义目录,默认目录是空的(0 MB)。

**根因 / Root cause:**
真实链数据在 `C:\Users\adest\AppData\Local\Btcd\sugarmainnet\blocks_ffldb`(旧进程用 `--datadir=C:\Users\adest\AppData\Local\Btcd` 启动,网络目录直接落在 `Btcd\sugarmainnet\`),
而 btcd 默认布局是 `Btcd\data\sugarmainnet\`。不带 `--datadir` 启动 = 指向全新空库 → 全部从头下。

**验证(用 `cmd/dbprobe`,直接读 ffldb)/ Verified via dbprobe:**
- `blockheaderidx` rows = **43,710,000**(头部都在)
- `heightidx` entries = **43,705,629**(高度→哈希索引几乎完整)
- `headerchainstate` height ≈ **43,709,998**(最佳头部状态已持久化)
- `chainstate` = genesis,height **0**(旧节点从未成功连接任何区块——原 bug 真实存在)

**修复 / Fix:** 启动必须带 `--datadir=C:\Users\adest\AppData\Local\Btcd`。
**教训 / Lesson:** 重启节点前先核对真实 datadir,别假设默认路径。

---

### E2. 不带 `--headerwindow=N` → 全量载入 43.7M header,内存暴涨 ~5GB+ / Missing `--headerwindow=N` → full index in memory, ~5GB+ spike

**现象 / Symptom:** 进程卡在 `Loading block index...`,内存 5GB 且继续涨,CPU 高(数分钟 368s)。

**根因 / Root cause:** `--headerwindow` 默认 **0 = 禁用窗口 = 物化全部 43.7M 个 blockNode**(设计文档 §5:全量 ~19GB)。旧进程应是带窗口值启动的。

**修复 / Fix:** 带 `--headerwindow=50000` 启动(内存 ~百 MB)。
**教训 / Lesson:** 启动 flag 必须与旧进程一致;`--headerwindow=0` 在全量主网上会 OOM。

---

### E3. btcd 没有 `--loglevel`,是 `--debuglevel` / Flag is `--debuglevel`, not `--loglevel`

**现象 / Symptom:** `--loglevel=debug` 启动即退出:`unknown flag 'loglevel'`。

**教训 / Lesson:** 想看 DBG 级日志(如 `PEER: Received headers`)用 `--debuglevel=debug`。`debuglevel=info` 时 DBG 行不可见,别拿"没有 DBG 行"当"没收到头部"的证据。

---

### E4. 探测 DB 时桶名用错 → 误判高度索引缺失 / Wrong bucket name in probe → false "height index missing"

**现象 / Symptom:** 探测 `blockheightidx` 报 MISSING;实际桶名是 **`heightidx`**(hash→height 是 `hashidx`,header 行是 `blockheaderidx`,链状态 key 是 `chainstate`,头部状态 key 是 `headerchainstate`,块下载游标 key 是 `blockdownloadstate`)。

**教训 / Lesson:** 先用 `chainio.go` 里的常量核对桶名;另外抽样点(10000/100000/1000000/20000000/43000000)恰好全缺失,险些误判索引稀疏——要统计条目总数,别只看抽样。

---

### E5. 已修复并实跑验证 / Fixed and verified in a real run

- 现象:同步连完一个块就停,日志 `previous block ... is unknown`(父 header 节点被窗口驱逐)。
- 修复:`98ddb81`(resume 游标 + 前沿保留)已合入。
- 真库现状:最佳链 = genesis(高度 0),**0 个区块连接成功** —— 该 bug 一直存在,区块从未真正上链。
- **验证(2026-08-06)**:用 `--datadir=C:\Users\adest\AppData\Local\Btcd --headerwindow=50000` 启动后,从持久化的 43.71M 头部**正确续传(未重下)**,区块开始持续连接推进(高度 44→3194+),**无** `previous block unknown` / 共识错误。✅ 修复生效。

### E6. 区块连接速率 ~5.5 blocks/s,全量需 ~90 天(性能问题,待优化)/ Block connect rate ~5.5/s → ~90 days for full chain

- 现象:稳态 ~5.5 blocks/s(30s 窗口 164 块),43.7M 区块 → 84–90 天,不可行。
- 观察:2 peer 并行供块(89.117.38.140 为主,65.108.72.71 少量),getdata 一批 2048;内存 ~660MB 稳定;CPU 约 1.75 核。
- 1-tx 区块(纯 coinbase)连接本应数百/s;待排查是**连接路径**(每块 flush/evictWindow/ffldb 写事务)还是**下载管线**(peer 交付延迟)为瓶颈。

---

### 关键命令 / Key commands

```
# 启动(正确 datadir + 窗口 + 调试日志)
& "sugarchain-node\btcd-new.exe" --datadir=C:\Users\adest\AppData\Local\Btcd --debuglevel=debug --headerwindow=50000

# 探测真实 DB(工具在 sugarchain-node\cmd\dbprobe)
go run ./cmd/dbprobe
```
