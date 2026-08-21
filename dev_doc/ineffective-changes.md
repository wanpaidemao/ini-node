# 无效改动记录（2026-08-19）

> 同步"开始并行后面不能并行"问题的无效改动——已全部回退（git reset 到 b49ac18c）

## 无效改动清单（已回退）
| 改动 | 内容 | 为什么无效 |
|---|---|---|
| **headerAssignLead = 200000（20 万）** | assignHeaderRange 分配贴近应用（领先不超 20 万） | **无效**——headerAssignLead 只限【分配】（新分配不超前应用 20 万）——但【header 应用严重滞后】（front range 未 received——应用停）——**已下载领先不受分配限制**——header 领先仍增长（3546 万）——问题复现 |
| **blockSliceStallTimeout 30s→15s** | stalled peer 快速替换 | **无效**——块下载停（inFlight=0）不是 stalled 快速替换能解决（块下载停是应用停/孤儿连锁）——问题复现 |
| **② headerStallLead = 300000** | 领先达 30 万阈值暂停 header 下载 | **猜测（未编译/未实施）**——即使领先达阈值暂停 header 下载——不解决【front range 未 received】（peer 不响应 header range）——应用停——非根治 |
| **诊断日志**（Block download dispatch/refused/Got block/Flushing） | 诊断观察 | 有效（诊断用）——随回退一并回退（如需可重加） |

## 根本原因（回退后确认——日志铁证）
- **header range stalled 18655 次**（front range 881290 未 received——peer 108.62 不响应 header range）
- **header 应用停**（front range received 才应用——front range 未 received——应用停）
- header 领先增长（3546 万）→ 孤儿 71.9 万（prev 未应用）→ 块停 → 停滞（"开始并行后面不能并行"）
- **根本原因 = header range stalled（peer 不响应 header range——front range 未 received——应用停）**——非分配限制问题（headerAssignLead 无效的根因）

## 结论
- 改动（headerAssignLead/blockSliceStallTimeout/②）**无效**——已全部回退（git reset 到 b49ac18c）
- **根因待解决**（header range stalled——peer 不响应 header range——front range 未 received——应用停）——方案（① header range 重发——front range received——应用恢复 / ② 等 peer 外部）
