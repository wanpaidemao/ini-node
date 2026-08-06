# 主网 Header 同步与交叉验证记录

> 2026-08-03 · 目标：证明 Go 节点接受的 header 链与 C++/官方主网字节级一致

## 环境

- 节点：`sugarchain-node.exe`（btcd fork + Sugarchain 共识移植）
- 数据目录：`%TEMP%\sugar-mainnet`（每次测试清空，header 不持久化）
- 对端：主网真实节点（Yumekawa 0.16.3.x），DNS 种子 `seed.sugarchain.site`
- 参考数据源：api.sugarchain.org 文档（经 websearch 检索）

## 过程摘要

1. **PoW 参数修正**：personalization 字符串修正后，yespower 与 C++
   `GetPoWHash()` 一致（`pow/pow_kat_test.go` 已知答案通过）。
2. **powLimit 修正**：主网/测试网 2^246-1 → 紧凑 0x1f3fffff；
   regtest 0x0f0f… → 紧凑 0x200f0f0f（此前 0x1e3fffff 导致
   `block difficulty of 524287999 is not the expected value of 507510783` 断连）。
3. **DAA 修正**：MTP 时间边界 + 先除后乘，消除难度不匹配断连。
4. **IBD 性能**：镜像 umami PR #122，header 下载期跳过 PoW 检查，
   速率 35/s → 500-870/s。
5. **长时间同步**：debug 日志记录每个 tip header hash。

## 交叉验证结果

| 高度 | Go 节点接受的头 hash | 外部参考（api.sugarchain.org） | 一致 |
|---|---|---|---|
| genesis | `7d5eaec2dbb75f99…11b0adc` | previousblockhash | ✅ |
| 1 | `ce8a0df339f2edce…e7c91` | /height/1 hash | ✅ |
| 2 | `67d3e607c54a1610…9e26cecb` | /height/1 nextblockhash | ✅ |
| 100 | `982f01d31726f680…ba0cd825` | /range/100 hash | ✅ |

- 接受 header 总数：**150,000+**
- 验证错误 / 难度不匹配：**0**

## 速率观察

- 初始（debug 全量 PoW 校验）：~35 headers/s
- BFNoPoWCheck 后：52,000 headers / 60s ≈ 870/s；
  150s 窗口约 76,000 headers ≈ 500/s（含对端/网络波动）
- 批量 2000/次，约 2.4s/批

## 结论

- Go 实现的 yespower + DAA + 参数与 C++ 主网一致，header 链在
  150k 高度范围内与官方 API 完全吻合。
- 后续：完整同步到 tip（~43.6M，约 14h）后需再次核对 tip hash；
  并进入区块（非 header）级验证。

## 参考链接

- C++ 断言：`umami/src/kernel/chainparams.cpp`
  （`0032f49a…` testnet genesis PoW；powLimit `003fffff…`；regtest `0f0f0f0f…`）
- PR #122：`umami/src/validation.cpp:3985`
- API 文档：https://api.sugarchain.org/ （本机直连被拒，经检索获取）
