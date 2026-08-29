// p2ptest 是 ini-node 的独立 P2P 测试工具模块（骨架）。
// p2ptest is a standalone P2P test tool module for ini-node (scaffold).
//
// 说明 / Note:
//   - 独立 module：go build ./... (backend) 不会编译本模块，不影响主项目编译。
//     Standalone module: `go build ./...` inside backend will NOT compile it,
//     so it never affects the main project build.
//   - 通过 replace 复用 backend/wire 包（仅读取其导出 API，不修改）。
//     Reuses the backend/wire package via a replace directive (read-only use).
module ini-node/tools/p2ptest

go 1.25.0

require github.com/btcsuite/btcd/wire/v2 v2.0.0

require (
	github.com/btcsuite/btcd/chainhash/v2 v2.0.0 // indirect
	golang.org/x/crypto v0.40.0 // indirect
	golang.org/x/sys v0.35.0 // indirect
)

replace github.com/btcsuite/btcd/wire/v2 => ../../wire
