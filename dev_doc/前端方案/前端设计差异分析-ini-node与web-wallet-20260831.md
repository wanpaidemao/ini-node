# 前端设计差异分析:ini-node 桌面前端 vs web-wallet

- 日期:2026-08-31
- 对比对象:
  - `d:\dev\AI\web-wallet\sugar-wallet\frontend`(Svelte 3 + Go 后端,原版 HTML/JS web 钱包的 Go 重构版,**用户心中的"web 端标准"**)
  - `d:\dev\AI\ini-node\frontend\frontend`(Svelte 5 + Wails 桌面前端,本项目)
- 目的:解释"为什么 web 端差别这么大",量化两边差异,作为后续界面/交互对齐的依据
- 参考代码:web-wallet 的 `App.svelte / routes/*.svelte / style.css`;ini-node 的 `App.svelte / pages/*.svelte / public/global.css`

---

## 1. 一句话结论

**两套前端只是"同 token 名、不同皮"的远亲**:ini-node 早期借用了 web-wallet 旧版糖果色 token 的**变量名**(violet/straw/honey/mint/ink),但把**色值全部换掉了**(暖纸+焦糖琥珀 → 冷灰+蓝色),页面结构也从 web-wallet 的"5 页纯钱包应用"变成了"节点监控台 + 钱包"的双中心结构。视觉气质与信息架构都不同,所以观感差别大。

## 2. 产品定位差异(差异的根源)

| | web-wallet | ini-node |
|---|---|---|
| 产品形态 | 纯钱包(Web/桌面,单窗口应用) | 节点监控台 + 内置钱包(桌面套件) |
| 后端依赖 | REST API(外部或内置)/ 可选 RPC | 本节点 RPC + 进程内 wallet.Manager + 外部 REST 降级 |
| 信息中心 | 钱包(链上数据为辅) | 节点(同步/peers/共识)在前,钱包在后 |
| 页面数量 | 6 页(home/create/wallet/broadcast/explorer/settings) | 12+ 页(dashboard/console/wallet/create/send/wallet-settings/settings/internals/…) |

ini-node 首页是**节点仪表盘**;web-wallet 首页是**打开钱包表单**。这是最直观的"第一屏差异"。

## 3. 视觉设计 token 对比(核心证据)

两边 token **名字同源**(web-wallet style.css 里保留了旧糖果 token 的 legacy 别名,如 `--strawberry→--accent`),但 ini-node 的 `global.css` 把同名 token 换成了完全不同的色值:

| token 名 | web-wallet(极简暖纸风) | ini-node(冷灰蓝风) | 观感 |
|---|---|---|---|
| 背景 `--bg`/`--ink` | `#faf8f3` 暖纸 | `#ffffff` 纯白 + `#f6f6f6` 灰卡 | 暖 vs 冷 |
| 主强调 `--straw`/`--accent` | `#b45309` 焦糖琥珀 | `#2563eb` 蓝 | **品牌色完全不同** |
| 成功/警告 | 哑光绿 `#3f7d51` / 琥珀 `#a16207` | `#059669` / `#b45309` | 接近但用法不同 |
| 字体 | Nunito + JetBrains Mono(本地打包 woff2) | Nunito(display)+ system-ui(body) | web-wallet 等宽字体更统一 |
| 阴影 | 极淡(`0 1px 3px rgba(...)`) | 各页自定 | web-wallet 更克制 |
| 设计哲学(style.css 原注释) | "无渐变、无重阴影——结构来自留白与细线" | 无此约束 | 极简 vs 常规后台 |

> 注意:web-wallet 自己也在 `style.css` L60-75 留了"旧糖果 token → 极简值"的别名层,说明它**经历过一次去糖果化的视觉重构**;ini-node 分叉时拿到的是旧命名体系,又各自换了一遍值。

## 4. 信息架构与导航对比

| | web-wallet | ini-node |
|---|---|---|
| 路由 | hash 路由(`lib/router.js`),6 个一级页 | store 内 route 字符串,12+ 页,二级页(`wallet-settings`) |
| 导航 | **侧栏固定 5 项**:首页/钱包/浏览器/广播/设置;钱包未打开时呈锁定态 | 侧栏/顶栏**可切换**(`app.navMode`),按"节点/钱包/系统"分组,标注 rpc/wails 后端依赖 |
| 钱包入口 | 首页即打开表单,登录后"钱包"高亮 | 独立 Create 页("打开钱包"),登录后跳 Wallet 页 |

## 5. 钱包功能流程对比

### 5.1 打开钱包入口

| | web-wallet Home.svelte | ini-node Create.svelte |
|---|---|---|
| 打开方式 | **3 个 tab**:Regular(邮箱+密码)/ Key(WIF 私钥)/ Saved(本地加密文件口令解锁) | 2 个 tab:邮箱密码 / BIP39 口令(无 WIF、无 Saved 文件解锁) |
| 邮箱辅助 | **近期邮箱下拉**(后端 `GetRecentEmails` 磁盘持久,✕ 可删,替代浏览器自动填充) | 已保存钱包卡片列表(2026-08-31 新加,localStorage,纯元数据) |
| 已存钱包 | 后端加密文件 `~/.sugar-wallet/wallet.enc`(AES-GCM+argon2id)+ recents 列表 | localStorage 档案(仅名字/邮箱/地址,**密码永不存**)+ BIP39 wallet.db |
| 创建钱包 | Create 页:随机密钥对,展示地址/WIF/公钥 + 备份 checkbox | 折叠面板:BIP39 助记词(12 词)+ 口令 + 备份确认 |

### 5.2 钱包主页

| | web-wallet Wallet.svelte | ini-node Wallet.svelte |
|---|---|---|
| 页面结构 | 单页 5 tab:**wallet(余额+收款二维码)/ send / history / keys / tokens** | 单页 4 tab:history / tokens / keys / consolidation(收款二维码在顶部余额卡) |
| 余额展示 | 地址栏 + 余额主视觉 + 刷新按钮,tab 内收款 | 余额卡(总/确认/待确认/未成熟 + 外部数据源徽章)+ 发送/接收按钮 + 齿轮进设置 |
| 发送 | **集成在 wallet 页 send tab** | 独立 Send 页 + Send 快捷跳转 |
| 发送能力 | **多输出**(outputs 数组,增删行)+ 确认 modal + `SendTransactionMulti` | 单输出(Step 5 未接线,表单+校验已就绪) |
| 空态提示 | — | Token/Consolidation 占位文案 |

### 5.3 设置

| | web-wallet Settings.svelte | ini-node |
|---|---|---|
| 设置内容 | 地址类型、**后端模式(REST/RPC)**、主链 API、**代币 API**、语言、代理、保存/重置 | 全局 Settings 页(连接/节点/数据/外观,对接节点 ini)+ **钱包设置二级页**(自动锁定/隐藏余额/历史条数/外部数据源) |
| 双方互补 | web-wallet 的"后端/代币 API/代理"更贴钱包场景 | ini-node 的"节点 maxpeers/debuglevel/datadir"是桌面节点特有,web-wallet 没有 |

## 6. web-wallet 有、ini-node 没有的页面/能力

| 能力 | web-wallet 实现 | 对 ini-node 的价值 |
|---|---|---|
| **浏览器(Explorer)** | `#/explorer`、`#/explorer/block/<hash>`、`#/explorer/tx/<txid>`,最新区块列表+详情 | 高:桌面前端有节点本地数据,做浏览器比 web 版更有优势;History tab 的 txid 可直接内链 |
| **广播页(Broadcast)** | 粘贴裸交易广播 | 中:调试工具,可并入控制台 |
| **WIF 私钥导入** | Home `Key` tab | 中:导入旧钱包的刚需,backend 侧 `FromWIF` 已有(web-wallet internal/wallet/key.go) |
| **Saved 加密文件钱包** | `wallet.enc`(AES-GCM+argon2id) | 低(方案分歧):ini-node 已用 BIP39+localStorage 档案组合,不必照搬 |
| **多输出发送** | outputs 行数组 + 确认 modal | 高:Send 页 Step 5 接线时直接对齐此交互 |
| **近期邮箱下拉(磁盘持久)** | `GetRecentEmails` | 低:ini-node 的钱包卡片列表已覆盖同需求 |
| **代币 tab 真实数据** | tokens tab 接 Token API | 已在计划内(Step 6) |

## 7. ini-node 有、web-wallet 没有的

| 能力 | 说明 |
|---|---|
| 节点仪表盘 Dashboard | 同步进度/高度曲线/peers/内存(桌面节点特有) |
| 控制台 Console | RPC 直发/连接管理/一键套件 |
| 节点设置(真实 ini 读写) | maxpeers/debuglevel(datadir 打开目录,2026-08-31 整改完成) |
| 钱包设置二级页 | 自动锁定/隐私余额/外部 REST 降级 |
| 导航布局切换(顶栏/侧栏) | `app.navMode` |
| 双语 i18n | web-wallet 也多语,但 ini-node 键位更细 |

## 8. 差异根因总结

1. **分叉时点**:ini-node 前端起步时参照的是 web-wallet **旧糖果色 token 体系**(仅取其命名),后续各自演化,web-wallet 还做了一次"极简暖纸化"视觉重构,ini-node 没跟上。
2. **产品定位**:ini-node 以节点监控为主中心(首页 Dashboard),钱包是内嵌模块;web-wallet 是纯钱包,首页即开钱包表单。
3. **后端形态**:web-wallet 是 Go 后端 REST;ini-node 是 RPC+Wails bindings 混合,页面按"rpc/wails 依赖"组织,导航结构自然不同。
4. **功能时序**:钱包功能按 Step 4~7 逐步接线(发送/代币/浏览器尚未完成),当前完成度低于 web-wallet 属预期,但**已完成部分的交互习惯也应尽量对齐 web-wallet**,降低用户切换成本。

## 9. 对齐建议(待用户定夺,本文档只列方案)

| 方案 | 内容 | 成本 |
|---|---|---|
| A. 视觉对齐(推荐先做) | 把 ini-node `global.css` 的 token 值切换到 web-wallet 暖纸/焦糖琥珀体系(变量名不变,组件零改动),字体补 JetBrains Mono 本地包 | 小(1 个文件+字体) |
| B. 钱包流交互对齐 | Home 风格打开页(近期列表已有)+ Wallet 页 tab 顺序调成 wallet/send/history/keys/tokens + 发送多输出 | 中 |
| C. 补 Explorer 页 | 依 web-wallet Explorer 三级路由实现(数据来自本节点 RPC) | 中,价值高 |
| D. 结构不动 | 保持双中心形态,仅钱包模块内部对齐 | — |

> 建议顺序:A(1 文件见效)→ B(随 Step 5 一起做)→ C(随 Step 6/7)。A/B/C 都不需要改后端。

## 10. 验证记录

- 2026-08-31:依据两边源码逐文件比对成文(style.css / global.css / App.svelte / Home|Create / Wallet / Settings / Explorer / store / services)
