module changeme

go 1.25.0

require (
	github.com/btcsuite/btcd v0.0.0
	github.com/btcsuite/btcd/chaincfg/v2 v2.0.0
	github.com/wailsapp/wails/v3 v3.0.0-alpha2.117
)

// Local wallet core lives in the backend module; only the wallet package
// (and its deps) are compiled into the frontend binary.
// 钱包核心位于 backend 模块；frontend 二进制只编译 wallet 包及其依赖。
replace github.com/btcsuite/btcd => ../backend

replace github.com/btcsuite/btcd/chaincfg/v2 => ../backend/chaincfg

replace github.com/btcsuite/btcd/txscript/v2 => ../backend/txscript

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/btcsuite/btcd/address/v2 v2.0.0 // indirect
	github.com/btcsuite/btcd/btcec/v2 v2.5.0 // indirect
	github.com/btcsuite/btcd/btcutil/v2 v2.0.1 // indirect
	github.com/btcsuite/btcd/chainhash/v2 v2.0.0 // indirect
	github.com/btcsuite/btcd/wire/v2 v2.0.1 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.0 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/kcalvinalvin/anet v0.0.0-20251112173137-d8ddc1f6dbee // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/tyler-smith/go-bip39 v1.1.0 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
)
