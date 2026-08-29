# UPnP端口映射截断Bug修复 - 20260828

## 问题描述

节点启用UPnP后，路由器上显示的映射端口为 `32767`，而非配置的P2P默认端口 `34230`，导致外部节点无法通过UPnP自动发现并连接本节点。

## 根因分析

**文件:** `backend/server.go:2959`

```go
lport, _ := strconv.ParseInt(activeNetParams.DefaultPort, 10, 16)
```

`strconv.ParseInt` 的第三个参数 `16` 表示结果限制为 **int16** 类型。

| 类型 | 最大值 | 34230是否溢出 |
|------|--------|--------------|
| int16 | 32,767 | **溢出** → 被截断为32767 |
| int64 | 9,223,372,036,854,775,807 | 不溢出 |

`34230 > 32767`，Go的 `strconv.ParseInt` 在溢出时不报错（`_` 忽略了error），直接返回类型最大值 `32767`。

## 影响范围

- sugarmainnet P2P端口 `34230` 被截断为 `32767`
- UPnP映射了错误的端口，外部节点无法通过UPnP自动连接
- 用户需要手动配置端口转发才能被外部连接

## 修复方案

将 `16` 改为 `0`，让Go自动推断为int64，避免溢出：

```go
// 修复前
lport, _ := strconv.ParseInt(activeNetParams.DefaultPort, 10, 16)

// 修复后
lport, _ := strconv.ParseInt(activeNetParams.DefaultPort, 10, 0)
```

## 验证

修复后UPnP应正确映射端口 `34230`，路由器UPnP列表中出现：

```
TCP  34230  192.168.x.x  34230  ini listen port
```

## 关联代码排查

代码中其他端口解析使用的是 `ParseUint`（无符号），uint16最大值65535，`34230` 未溢出，无需修改：

- `server.go:3591` - `ParseUint(..., 16)` ✓
- `server.go:3605` - `ParseUint(..., 16)` ✓
- `server.go:3702` - `ParseUint(..., 16)` ✓
- `peer/peer.go:342` - `ParseUint(..., 16)` ✓
- `peer/peer.go:2614` - `ParseUint(..., 16)` ✓
- `addrmgr/addrmanager.go:559` - `ParseUint(..., 16)` ✓
