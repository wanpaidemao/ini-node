# ini-node / Sugarchain Go 全节点

Sugarchain 的 Go 实现全节点（btcd fork + Sugarchain 定制：yespower PoW、sugarindex、header 窗口化/冷读、并行同步），
配套 Wails 前端 web-wallet。

> 本仓库为 **Go 重构版**；C++ 原版（umami / Sugarchain Core）与 Go 重构的 web-wallet 均为独立仓库。

---

## 目录结构 / Layout

```
ini-node/
├── backend/        # Go 后端（btcd fork + Sugarchain 定制，主模块 github.com/btcsuite/btcd）
│   ├── cmd/        #   入口与工具（btcd / btcctl / walletapi / p2ptest 等）
│   ├── blockchain/ #   共识与链处理（含 header 窗口/冷读等定制）
│   ├── sugarindex/ #   Sugarchain 专属索引（余额/UTXO 查询）
│   ├── pow/ yespower/ # yespower PoW
│   ├── chaincfg/   #   链参数（Sugarchain，replace 本地）
│   └── doc/md/     #   架构/同步/RPC 文档
├── frontend/       # Wails3 前端（Svelte5 + TS + Vite）
├── dev_doc/        # 开发文档（方案/待办/规范）
├── build.bat       # 构建脚本
└── README.md       # 本文件
```

---

## 快速开始 / Quick Start

```powershell
# 后端构建（依赖缓存放 D 盘，不落 C 盘）
cd backend
$env:GOMODCACHE='D:\codeX\gomodcache'
go build -o btcd.exe .
```

详见 `backend/README.md`（运行参数、RPC、同步说明）与 `dev_doc/`。

---

## 开发约定 / Conventions

- **开发流程**：读文档 → 设计界面 → 设计技术方案 → 分步实现 → 记录进度
  → 详见 [dev_doc/开发流程与提交规范.md](dev_doc/开发流程与提交规范.md)
- **代码规范**：UTF-8、中英双语注释、依赖放 `D:\codeX\gomodcache`
- **提交规范**：`[项目版本号-拆分步骤编号] <type>: 英文 / 中文`
  （版本号取 `backend/version.go`，当前 0.0.1）
- **上游同步**：保留全部本地硬代码，patch-queue 补丁化管理，btcd 每 1-2 年同步一次
  → 详见 [dev_doc/btcd上游同步迁移方案-20260829.md](dev_doc/btcd上游同步迁移方案-20260829.md)

---

## 文档索引 / Documentation

- **开发文档总索引**：见 [dev_doc/README.md](dev_doc/README.md)（按类别：任务规范 / 上游迁移 / 同步存储 / 共识网络 / 前后端 / 启动加载）
- **任务跟踪**：[dev_doc/待办/待办.md](dev_doc/待办/待办.md)
- **开发流程与提交规范**：[dev_doc/开发流程与提交规范.md](dev_doc/开发流程与提交规范.md)
- **btcd 上游同步方案**：[dev_doc/btcd上游同步迁移方案-20260829.md](dev_doc/btcd上游同步迁移方案-20260829.md)
- **后端技术文档**：`backend/doc/md/`（架构 / 同步 / RPC）
