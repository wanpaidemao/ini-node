# patch-queue 补丁存档 / Patch Queue Archive

> 用途：把 fork（backend/）相对上游 btcd 的**全部本地改动**导出为可重放的补丁，永久保留。
> Purpose: archive every local change (fork vs upstream btcd) as re-appliable patches.
> 依据：`dev_doc/btcd上游同步迁移方案-20260829.md`（v3 简化主方案：保留全部硬代码 + patch-queue）。

## 补丁清单 / Patches

| 补丁 | 内容 | 类别 |
|------|------|------|
| `01-consensus.patch` | 共识差异（difficulty/validate/merkle/process/accept/error/mining/txscript/chaincfg） | 共识 |
| `02-perf-sync.patch` | 性能自研-同步（netsync/chain/blockindex/chainview） | 性能 |
| `03-perf-storage.patch` | 性能自研-存储（utxocache/ffldb/thresholdstate/checkpoints） | 性能 |
| `04-storage-format.patch` | 存储/磁盘格式（chainio header 索引） | 存储 |
| `05-assembly-rpc.patch` | 装配/RPC（server/config/btcd/rpcserver/log/signal/version…） | 装配 |
| `06-dormant-wire.patch` | 休眠子模块修改（wire/blockheader、wire/msgversion） | 子模块 |
| `07-misc.patch` | 项目杂项（.gitignore/README/Dockerfile/sample-conf/go.mod） | 杂项 |

## 重新生成 / Regenerate

```powershell
powershell -File script/gen-patches.ps1
```

生成脚本会把 fork 相对上游（`d:\dev\AI\btcd-ref`）每个被修改文件的差异，规范化为相对路径补丁。

## 重放（上游同步后）/ Re-apply after upstream sync

```bash
# 在合并了上游 btcd 的工作树里，逐补丁重放
git apply patch/01-consensus.patch
git apply patch/02-perf-sync.patch
# ... 冲突会定位到具体补丁，逐个手工解决
```

## 注意 / Note

- 补丁是**备份存档**，不是代码本体；源码里的改动照常查看/编译。
- Patches are backup archives, not the source of truth; source files keep the changes.
