# 同步停滞分析（"开始并行后面不能并行"——复现确认与根因）

> 2026-08-18——基于详细诊断日志（Flushing / Block download dispatch / refused）的复现确认与分析
> 状态：**已复现确认**——根因深挖中（不急于下结论——复现确认后再讨论方案）

## 一、问题现象
- 同步**开始并行**（header 下载领先 blocks——并行下载）——**待下载区块超过约 32 万时——同步停滞**（"开始并行后面不能并行"）
- 现象：`Sync progress 0.00 bl/s stalled`——blocks 停止推进——块下载 inFlight 下降

## 二、复现确认（日志证据——backend/logs/node.stdout.log）
| 时间 | 日志证据 |
|---|---|
| 23:21-23:24 | `Sync progress: height=560000 +0 0.00 bl/s stalled`（连续——blocks 560000 停止） |
| 23:20-23:23 | `Block download dispatch: frontier=567098 applied=560000（停）headerTip=910000（领先 blocks 35 万）inFlight=2→1` |
| 23:20 | `Flushing 1 dirty block index nodes`（每次 1 个节点） |
| 23:22-23:24 | `Flushing 16000 → 20000 dirty`（**批量落盘恢复**——但同步仍停滞） |
| refused | 0（无派发拒绝） |

**待下载**：headerTip 910000 - blocks 560000 = **35 万**（**超过 32 万临界**——用户观察一致："待下载区块超过 32w 会发生"）

## 三、根因分析（关键结论）
### ⚠️ Flushing 1 落盘异常【不是本次停滞的主因】
- 落盘已恢复**批量 20000**（23:23 后——`Flushing 20000 dirty`）——**但同步仍停滞**（Sync stalled 持续）
- 之前（低高度）Flushing 1 持续（每次 1 个——DB 锁占死——块处理饿死）——但**本次停滞时落盘已批量**——**主因不是落盘**

### 停滞链（本次）
```
applied 停（560000——块连接停）
→ inFlight 下降（2→1——块下载停——块不回来）
→ 块处理没块 → 停滞（Sync progress 0.00 bl/s）
```

### 深挖方向（块下载为什么停）
1. **块下载 peer 响应**（inFlight 1——块下载 peer 响应慢/停——块数据不回来）
2. **待下载临界机制**（header 领先 blocks 35 万（>32 万）——块下载/处理停的触发——**用户观察的">32 万"临界确认真实**）
3. **refused 0**（不是派发拒绝——是**块下载无推进**（块不回来——peer 不响应块数据——或块下载窗口/分配临界））

## 四、诊断日志（已加——netsync/manager.go + blockindex.go）
- **blockindex.go**：`Flushing %d dirty block index nodes to DB (header persist, force=%v)`（落盘批量/节奏）+ `Header index flush to DB failed: %v`（失败 Warn）
- **netsync/manager.go**：`Block download dispatch: frontier=%d applied=%d headerTip=%d inFlight=%d`（30s 节流——派发状态）+ `Block download dispatch refused: peer %s in-flight slice held`（派发拒绝）+ `lastBlockAssignLog` 字段

## 五、环境
- **数据目录**：项目内 `sugarchain-node\data`（从 AppData 迁移——datadir 配置 runtime.ini）
- **节点**：bin 节点（frontend/bin/btcd.exe——runtime.ini——listen=0.0.0.0:34230 + datadir 项目内 data）
- **日志**：backend/logs/node.stdout.log（stdout 重定向——详细诊断日志捕获）

## 六、后续（按用户指示——不急于下结论）
1. **深挖块下载停的根因**（peer 响应（inFlight 1——块不回来）/待下载临界机制（>32 万）——块下载窗口/分配——深入日志）
2. **复现确认后再讨论方案**（不改代码/不重启——先复现确认根因——按用户批评）
3. 候选方向（记录——待确认后）：A flushToDB 批量阈值（已回退——Flushing 1 不是本次主因）/ F 移除 148 / C 定时落盘——**均待根因确认后评估**
