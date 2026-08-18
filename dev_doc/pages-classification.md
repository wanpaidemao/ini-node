# 控制中心页面分类（Wails 专用 vs RPC 独立）

> 分类记录（页面不显示——仅文档）：区分 Wails 专用页面与 RPC 页面（含可独立作为前端的页面）
> 对应 App.svelte `sections` 的 `kind` 字段（PageKind = "rpc" | "wails"）

## 页面分类
| 分区 | 页面 | kind | 说明 |
|---|---|---|---|
| **node** | `dashboard`（仪表盘） | `rpc` | 经 RPC 代理访问——**可独立作为纯前端**（同步状态/节点概览） |
| **node** | `internals`（内部观测） | `rpc` | 经 RPC 代理访问——**可独立作为纯前端**（blockTasks/headerTasks/内部观测） |
| **node** | `control`（控制） | `wails` | **Wails 专用**（依赖桌面后端——节点启动/停止） |
| **wallet** | `wallet`（钱包） | `rpc` | 经 RPC 代理访问——**可独立作为纯前端**（余额/历史） |
| **wallet** | `create`（创建） | `rpc` | 经 RPC 代理访问——**可独立作为纯前端**（创建地址） |
| **wallet** | `send`（发送） | `rpc` | 经 RPC 代理访问——**可独立作为纯前端**（发送交易） |
| **system** | `settings`（设置） | `wails` | **Wails 专用**（依赖桌面后端——本地配置） |
| **system** | `console`（控制台） | `wails` | **Wails 专用**（依赖桌面后端——RPC 控制台） |

## 说明
- **rpc 页面**（🌐）：通过 RPC 代理（rpcproxy.go `/rpc`）访问 btcd——**可独立作为纯前端**（不依赖 Wails 桌面——可单独部署）
- **wails 页面**（🔒）：依赖 Wails 桌面后端（节点控制/本地设置/控制台——桌面 API）
- 分类仅用于文档记录（`kind` 字段保留在代码——不显示在页面上）
