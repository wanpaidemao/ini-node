# 同步逻辑深度分析（区块头/区块同步/落盘/窗口/并行下载）

> 基于 `netsync/manager.go` + `blockchain/accept.go`/`blockindex.go` 代码分析（2026-08-18）
> 背景：全量同步反复停滞（blocks 124 万/4390 万——peer 端外部条件——SugarChain 全网高峰）

## 关键参数
| 参数 | 值 | 位置 | 作用 |
|---|---|---|---|
| `headerFlushBatchSize` | **20000** | accept.go:22 | header 索引落盘批量（累积 20000 才落盘） |
| `maxBlockRequestWindow` | **8192** | netsync/manager.go:55 | 块下载窗口（同时请求的块范围） |
| `maxHeaderSyncPeers` | **8** | netsync/manager.go:71 | header 下载 peer 上限 |
| `blockSliceStallTimeout` | **30s** | netsync/manager.go:97 | 块切片停滞超时（peer 30s 未返回块 → Freeing） |
| `headerRangeStallTimeout` | **6s** | netsync/manager.go | header range 停滞超时（peer 6s 未响应 → 重发） |
| `headerwindow` | **50000** | config | 内存 header 窗口（窗口外 evict） |

## 1. 区块头与区块同步逻辑
- **startSync**（750）：IBD 启动——有更高 header 的 peer → `fetchHeaders()`（**header 优先**）→ return（header 追平前不专注块下载）——header 追平后才专注块下载
- **fetchHeaders**（541）：header 下载（分配 range 给 peer——getheaders——2000/批）
- **maybeStartBlockSync**（2294）：header 领先 margin（`blockSyncStartLead`——config 默认 20000）时**启动并行块下载**
- **块下载**：`assignBlockSlice`（1661）——窗口内分配 slice 给 peer——块下载（getdata）

## 2. 落盘逻辑
- **flushToDB**（blockindex.go:871）：header 索引批量落盘（`headerFlushBatchSize=20000`——累积 20000 才落盘——**批量**）
- **触发点**：accept.go:148（块连接后）+ header 应用路径——**ffldb 单写者（DB 写锁串行）**
- **已知问题（曾修复 a42b864f——已回退）**：若 flushToDB 被频繁调用（dirty 每次 1 个）——**每次 1 个节点落盘占死 DB 写锁 → 饿死块处理 → 窗口停**——修复：批量阈值（dirty<20000 且非强制不落盘）

## 3. 窗口逻辑
- **块下载窗口**：`maxBlockRequestWindow=8192`（同时请求窗口）——`nextAssign` 推进（**依赖 bestHeight**——块连接后 nextAssign 才前进）
- **header 分配**：`assignHeaderRange`（648）——从 `nextAssign`（分配前沿）分配——**可超前应用**（并行下载设计——分配不管应用进度）
- **内存窗口**：`headerwindow=50000`（内存 index 只留最近 5 万 header——窗口外 evict——`LookupNode` 查不到窗口外）

## 4. 并行下载逻辑
- **headerSync/blockSync 并行**（maybeStartBlockSync——header 领先 margin 时启动块下载——header 下载同时块下载）
- **分配超前应用**：header 下载快（分配推进超前）——应用（`nextHeight`——front range received 才应用）滞后——**front range 未 received（peer 未响应）→ 应用停 → 下载超前应用（差距拉大）**

## 5. 停滞机制（完整因果）
```
① previous not known：分配超前应用 + front range 未 received（peer 未响应 header range——外部）
   → 应用停 → peer 发送 previous 未应用 header → LookupNode nil → 断开（曾修复 headerAssignLead——已回退）
② peer stalled（块下载）：块下载 peer 30s 未返回块数据（blockSliceStallTimeout）
   → Freeing in-flight → inFlight=0 → 窗口不派发 → blocks 停
③ 根因：peer 端不响应（header range/块数据——SugarChain 全网高峰——外部网络条件——非代码问题）
```

## 6. 相关修复记录（a42b864f——已回退）
1. `flushToDB` 批量阈值（落盘每次 1 个占死 DB 锁 → 饿死块处理——修复）
2. `headerAssignLead` 分配协调（分配超前应用 → previous not known 断开——修复）
3. `SetGCPercent 100`（GC 优化）
4. `Block download dispatch` 派发诊断日志
5. previous not known **重新分配**（不断开——死循环风险需重试上限）

> 以上修复已 git reset 回退（用户要求回退本地所有代码——恢复远端 dc6cd5f0）——文档存档供后续参考
