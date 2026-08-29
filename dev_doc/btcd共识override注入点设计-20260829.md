# btcd 共识 override 化（注入点）设计方案 / Consensus Override via Injection Points

> 设计日期：2026-08-29
> 决策背景：已选择"方案 3（注入点/真 override）"，目标是让 btcd 本体保持上游原版，
> Sugarchain 共识差异通过注入点接入，升级 btcd = 换版本号 + 重放钩子层，彻底告别 9000 行 merge 痛苦。
> 前置依赖：dev_doc/btcd上游同步迁移方案-20260829.md（分叉面收敛清单）

---

## 一、背景与目标 / Context & Goal

**现状**：ini-node 是 btcd 完整 fork，54 个文件被改、62 个新增。核心痛点：7 处 Sugarchain
共识差异**硬编码在 btcd 的 blockchain 主模块内**（不是独立模块，无法单独 replace），
导致每次上游更新都要手工 merge，且 `netsync/chainio` 等大改加剧冲突。

**目标**（本方案的终态）：

```
┌─────────────────────────────────────────────┐
│  应用层（本项目）：入口/配置/RPC/sugarindex    │   ← 自己的代码
│  sugarconsensus 模块：共识钩子的实现           │   ← 只实现"零件"
├─────────────────────────────────────────────┤
│  btcd 各包（上游原版，依赖/ replace）          │   ← 一行不改
│  blockchain · netsync · wire · mempool …     │
└─────────────────────────────────────────────┘
   升级 = go.mod 换版本号 + 重放钩子层补丁
```

**核心认知**：override 只有三种粒度（详见方案 v2 §4），本方案选"**注入点接口**"这一档——
即：共识规则从"写死在 btcd 里"变成"btcd 调用你提供的实现"。

---

## 二、共识差异盘点与注入点清单 / Hook Inventory（核心）

> 已逐一核实 fork 源码（backend/），确定每个差异的：位置、现状、可选 override 机制、难度。

| # | 共识差异 | 位置（fork 内） | 现状 | override 机制 | 难度 |
|---|---------|----------------|------|--------------|------|
| 1 | 链参数（网络/创世/5s 出块/奖励） | `chaincfg/sugar_params.go` 等 | 已 replace | **参数化**（btcd 原生支持自定义网络） | ✅ 已完成 |
| 2 | PoW 哈希（yespower） | `blockchain/validate.go:416` `pow.BlockPoWHash` | 已调本地 pow 包 | **接口注入**（btcd 调 pow 包） | ✅ 已完成 |
| 3 | 未来时间窗 60s | `blockchain/validate.go:39` `MaxTimeOffsetSeconds`(const)，用点 `:545` | 硬编码常量 | **参数化进 chaincfg** | 🟢 低 |
| 4 | 难度算法（SugarShield-N510） | `blockchain/difficulty.go:179` `calcNextRequiredDifficulty`，调用点 `validate.go:789` | 硬编码函数体 | **注入点 hook**（最核心） | 🔴 高 |
| 5 | 无 coinbase witness commitment | `blockchain/merkle.go:227` `ValidateWitnessCommitment`，调用点 `validate.go:985` | 硬编码放宽 | 参数化 / 注入点 | 🟡 中 |
| 6 | P2WPKH 空 witness 兼容 | `txscript/engine.go` | 已 replace | **模块 override** | ✅ 已完成 |
| 7 | 矿工块上链判定 | `blockchain/process.go` `ProcessBlock` | 硬编码 | 注入点（较难） | 🟡 中 |

**结论**：7 处里已有 3 处（1/2/6）天然可 override 且已完成；真正要"造注入点"的是 **3/4/5/7 四处**，
其中**难度（#4）是硬骨头**，其余三处相对可控。

---

## 三、注入机制设计 / Injection Mechanisms

按差异性质选三种机制，可混合使用：

### 3.1 参数化（进 chaincfg）—— 用于 #3，可选 #5
btcd 的 `chaincfg.Params` 已支持自定义网络（网络 id、创世、TargetTimePerBlock、奖励等）。
把"未来时间窗 60s"这类**单一数值差异**改成参数：
```go
// chaincfg.Params 新增字段（上游 PR 或本地 params 扩展）
MaxTimeOffsetSeconds int64  // 主网 60；Bitcoin 7200
```
validate.go 用 `params.MaxTimeOffsetSeconds` 替代硬编码常量 → **参数化后连 fork 都不需要**
（chaincfg 本就是 replace 的本地模块）。
- 上游 PR 方向：为 btcd `chaincfg.Params` 增加 `MaxFutureTimeSeconds` 字段，一次造福所有分叉。

### 3.2 模块 override（replace）—— 用于 #6（已完成），可扩展
已经是 `replace txscript => ./txscript`。凡是能拆成独立模块的差异都走这条（改动隔离在模块内）。

### 3.3 注入点 hook —— 用于 #4、#7，兜底 #5
对**无法参数化、且住在 blockchain 内**的逻辑，在 fork 的对应文件里加"可替换回调"：
```go
// 在 fork 的 blockchain/difficulty.go 中（仅这一个文件是 fork，其余 blockchain 保持上游）
type DifficultyCalc func(lastNode HeaderCtx, newBlockTime time.Time, b *BlockChain) (uint32, error)

var calcDifficultyOverride DifficultyCalc  // 默认 nil → 走 btcd 原逻辑

// Sugarchain 启动时注入 N510 实现
func SetDifficultyCalc(f DifficultyCalc) { calcDifficultyOverride = f }
```
- `calcNextRequiredDifficulty` 开头检查 override 非空则调用它。
- **关键设计**：注入的钩子只负责"纯算法"（N510 平均窗口、目标换算），
  调用链、异常、窗口修复（`repairAncestorChain`）等**留在 btcd 侧**，避免钩子与 blockchain 内部强耦合。
- 钩子粒度按函数拆分，保证"能参数化的不 hook，能模块化的不 fork 文件"。

---

## 四、目标架构与模块布局 / Target Layout

### 4.1 目录结构（终态示意）

```
ini-node/
├── backend/
│   ├── cmd/btcd/            # 应用入口（本项目）
│   ├── sugarconsensus/      # 共识钩子实现模块（N510 难度、时间窗、witness 规则）
│   │   ├── difficulty.go    #   实现 DifficultyCalc（SugarShield-N510）
│   │   ├── witness.go       #   实现 commitment / 空 witness 规则
│   │   └── hooks.go         #   定义钩子接口 + SetXxx 注入入口
│   ├── chaincfg/            # replace 本地（已）：Sugar 链参数
│   ├── txscript/            # replace 本地（已）：空 witness 兼容
│   ├── pow/                 # 新增（已）：yespower 哈希
│   ├── sugarindex/          # 新增（已）：索引扩展
│   ├── blockchain/          # ⚠️ 见 4.2：尽可能回归上游 + 少数钩子点
│   └── …（其余 btcd 包 → 上游原版）
```

### 4.2 blockchain 的处理（唯一绕不开的点）

blockchain 是 btcd **主模块**内包，无法 replace。两种落地：
- **B1 薄 fork（推荐起点）**：保持 `module github.com/btcsuite/btcd` 主模块 fork，但内部只保留
  钩子点文件（difficulty.go、validate.go、merkle.go、process.go 的 hook 改动），
  其余文件与上游逐字节对齐 → fork 面从 54 文件收敛到 ~4 文件。
- **B2 主模块去 fork（终态）**：主模块回归上游 btcd，本项目变成独立应用模块
  （`cmd/btcd` + 各扩展），blockchain 差异改走 `-overlay` 或"极薄主模块 fork"。
  这一步涉及构建方式变化，放最后阶段。

### 4.3 依赖方向（无环）

```
sugarconsensus ──调──▶ pow、chaincfg
blockchain(钩子点) ──调──▶ sugarconsensus（仅启动注入，接口反向无环）
cmd/btcd ──装配──▶ sugarconsensus（注入钩子）+ blockchain + sugarindex
```

---

## 五、实施步骤 / Implementation Phases

> 每阶段独立可编译、可回归、可回退；阶段产出即提交。

### Phase 0：冻结与基线（前置，来自方案 v2）
- 先按收敛清单把 `netsync/chainio/chain` 等**性能自研压回上游**，把 fork 面先缩小到共识部分。
- 基线：全量同步到最新高度，记录区块高度/Hash 作为回归锚点。

### Phase 1：参数化（#3 时间窗，先软后硬）
- 把 `MaxTimeOffsetSeconds` 从常量改为从 `chaincfg.Params` 读取（本地 chaincfg 已 replace，无风险）。
- 向 btcd 上游提 PR：`chaincfg.Params` 增加未来时间窗字段。
- 验收：主网同步正常，60s 窗口行为不变；上游 PR 合入后可删本地分支。

### Phase 2：难度钩子（#4 核心）
- 在 fork 的 `blockchain/difficulty.go` 加入 `DifficultyCalc` 回调 + `SetDifficultyCalc`。
- 新建 `sugarconsensus/difficulty.go`：把现有 N510 纯算法搬过去。
- `cmd/btcd` 启动装配时注入。
- 验收：同步后每个区块的难度与未改造前**逐块一致**（对拍）。

### Phase 3：witness 规则 + 矿工块判定（#5、#7）
- #5：先参数化（链参数开关），不行再走 hook。
- #7：`ProcessBlock` 的矿工块判定抽成回调（默认 btcd 行为，Sugar 注入网络 header 链判定）。
- 验收：`submitblock` 场景回归（待办 #2 挖矿块上链判定）。

### Phase 4：主模块去 fork 化（终态，B2）
- 主模块回归上游 btcd，本项目转为独立应用模块；blockchain 差异经 `-overlay` 或极薄主模块 fork 提供。
- 构建脚本封装 `-overlay`（build.ps1 / build.sh）。
- 验收：干净环境从 0 构建通过；全量同步回归。

### Phase 5：回归与长期流程
- 回归矩阵：编译、单测（blockchain/netsync/wire/sugarindex）、主网全量同步、
  与 umami 高度对齐、sugarindex 索引一致性、挖矿/出块。
- 长期：升级 btcd = 换版本号 → `git apply` 钩子层补丁 → 跑回归。

---

## 六、回归与验收 / Verification

| 项 | 方法 | 通过标准 |
|----|------|---------|
| 编译 | `go build ./...`（backend） | 无错误 |
| 单元测试 | `go test ./...`（重点 blockchain/sugarindex） | 全绿 |
| 共识对拍 | 改造前后同一高度区块的难度/Hash | 逐块一致 |
| 全量同步 | 主网从头同步 | 高度与网络一致，无分叉 |
| 挖矿 | 本地出块 + submitblock | 块进入主链（待办 #2 关闭） |
| 索引 | sugarindex 余额/UTXO 查询 | 与 rpcclient 直连一致（待办 #1） |

---

## 七、风险与对策 / Risks

| 风险 | 影响 | 对策 |
|------|------|------|
| 上游不合并 PR（参数化 #3/#5） | 参数化字段无法进上游 | 本地 chaincfg（已 replace）持有字段即可，不影响 override 效果 |
| 难度钩子与 blockchain 内部耦合 | 钩子脆弱 | 钩子只含"纯算法"，调用链/异常留在 btcd 侧；接口按最小面设计 |
| 磁盘格式绑定（chainio 等） | 无法随上游 | 存储与共识**解耦处理**：存储按收敛清单决定（保留 or 重同步），不阻塞共识 override |
| `-overlay` 工具链摩擦（B2） | IDE/部分工具不一致 | Phase 4 才引入；在此之前用"薄 fork"（B1）同样达成目标 |
| 中途想回退 | 进度损失 | 每阶段独立提交、独立可回退 |

---

## 八、与既有方案 v2 的关系 / Relation to v2

- v2 的**分叉面收敛清单**是本方案的前置：先压回性能自研（netsync/chainio/chain 等），
  让 54 个文件先收敛到 7 处共识差异，再对本方案的 4 处做 override 化。
- 本方案完成后的 fork 面预期：**约 4 个钩子点文件 + sugarconsensus 模块 + 少量新增**，
  升级 btcd 不再需要 merge 9000 行。

---

## 附：已核实的钩子点源码位置 / Verified Hook Points

| 差异 | fork 内源码位置 |
|------|----------------|
| 难度 N510 | `blockchain/difficulty.go:179` `calcNextRequiredDifficulty`（调用点 `validate.go:789`） |
| PoW 哈希 | `blockchain/validate.go:416` `pow.BlockPoWHash(header)` |
| 时间窗 60s | `blockchain/validate.go:39` `MaxTimeOffsetSeconds`（用点 `:545`） |
| witness commitment | `blockchain/merkle.go:227` `ValidateWitnessCommitment`（调用点 `validate.go:985`） |
| 空 witness | `txscript/engine.go`（已 replace 模块） |
| 矿工块判定 | `blockchain/process.go` `ProcessBlock` |
