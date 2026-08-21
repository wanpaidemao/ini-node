# 同步监控记录（一晚上运行——2026-08-18 夜 → 08-19 晨）

> 记录时间：2026-08-19 00:12（明天检查基线）

## 当前状态（一晚上同步）
| 项 | 状态 |
|---|---|
| **节点** | 运行中（PID 34740——内存 759 MB——一晚上同步推进） |
| **blocks** | **677750**（从 64 万推进——一晚上 ~3 万块） |
| **速度波动** | 31 → 116 → 4.9 bl/s（**波动大**——peer 端——但推进） |
| **孤儿** | 59.3 万累计（header 领先受限后 prev 应用——孤儿趋势待观察） |
| **Freeing** | 85（stalled 快速替换——blockSliceStallTimeout 15s 生效） |
| **GC** | NumGC 433——GCCPUFraction **1.07%**——Alloc 859 MB——内存 759 MB（**健康——GC 影响小**） |

## 已生效改动（今晚跑）
- **headerAssignLead = 200000（20 万）**（用户指令——允许区块头领先 20 万——header 下载快）
- **blockSliceStallTimeout = 15s**（stalled peer 快速替换——减少停滞）
- 诊断日志（Block download dispatch/refused/Got block/Flushing——详细捕获）

## 明天早上检查（基线——对照）
1. **同步推进**（blocks 是否追平/速度——**header 领先 20 万后块下载是否追得上**）
2. **孤儿**（Adding orphan——header 领先 20 万 + prev 应用——**孤儿是否减少**）
3. **停滞**（Sync progress 0.00 bl/s——**"开始并行后面不能并行"是否复现**——headerAssignLead 20 万 + ① 15s 的长期效果）
4. **GC**（NumGC/暂停/内存——GC 对同步的影响）
5. **Freeing**（① 快速替换——stalled peer 处理——同步稳定性）
