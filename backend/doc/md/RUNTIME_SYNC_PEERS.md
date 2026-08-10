# 计划：运行时动态调整并行同步 peer 数（界面可调）

> 状态：仅整理，未改代码。为将来做界面预留。

## 动机
- 实测：单 peer 同步 ~123 bl/s，8 peer 并行反而 ~49 bl/s（CPU 满但锁竞争/孤儿抖动拖慢）。
- 最优 peer 数与机器（8 核 16 线程）、网络、块大小都有关，运行时调优比写死更好。
- 界面需要能**不停机动态**调整，重启不行。

## 现状（全部写死）
`netsync/manager.go` 常量 `maxHeaderSyncPeers = 8`（L69-71），被以下位置直接引用：
- `L463-464`：header 并行候选 peer 数截断（`shuffled[:maxHeaderSyncPeers]`）
- `L833`：`headerSyncAddPeer` 上限
- `L867`：`blockSyncAddPeer` 上限
- `L1979-1980`：`finishHeaderSync` 里 `sm.blockSync` 截断到 8
- `L2004`：阻塞 slice 宽度 `sliceLen = int32(maxBlockRequestWindow) / maxHeaderSyncPeers`（即 8192/8=1024，此前已固定为该值）

## 目标设计
1. **启动参数** `--blocksyncpeers=<n>`（默认 8），写入 `netsync.Config` 新字段（`interface.go` 的 `Config{...}`）。
2. **运行时旋钮**：`SyncManager` 增加可原子读写字段（如 `atomic.Int32`）:
   - `SetBlockSyncPeers(n int)`（外部/UI 可安全并发调用）
   - 并行相关逻辑（上面 5 处）改读动态值。
3. **RPC 方法**：`blocksyncpeers [n]`
   - 带参 = 设置（返回新值）；无参 = 查询当前值。
   - 接线：
     - `rpcserver.go` 的 handler map 注册 `handleBlockSyncPeers`
     - `rpcserverSyncManager` 接口（rpcserver.go L4792 附近）加方法
     - `server.go` 用 `s.syncManager` 实现接口（L283）
   - `params` 解析在 `btcjson`（chainsvrcmds.go）注册命令；`rpcserverhelp.go` 补帮助条目。

## 语义约定
- 只生效于"下一次切 slice / 新 peer 加入"；已在途 slice 不动（避免重复请求）。
- **可取范围 = 4..16（含）**：与 UI 同步（`Settings ▸ 并行 block peer 数` / Dashboard 快捷），非法值 / <4 钳位到 4，>16 钳位到 16。0 表示"回到启动默认 8"。
- 若做"立即重切"，需先 `releaseBlockSlice` 清全局请求池再去重分，注意与 in-flight 去重/孤儿池配合（复杂度较高，v1 不建议）。

## 顺带可复用的观测设施
- 节点内置每 60s INFO 进度日志 `Sync progress: height=… 率… synced=% ETA=h`（netsync/manager.go `logSyncProgress`，已实现，`sm.current()` 时静默）。
- 外部 `script/04-sync-rate.[go|exe]` 持续监控（RPC 轮询 + 日志）。

## UI 建议呈现
- 输入框 + 应用按钮：`blocksyncpeers <n>`;
- 实时展示当前值 + 最近 1/5 分钟平均 bl/s，便于 A/B。