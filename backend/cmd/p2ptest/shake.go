// shake 子命令骨架：完整 P2P 握手。
// shake subcommand scaffold: full P2P handshake.
//
// 未来实现 / Future implementation: 替代 devtest/main —— 向目标发送 version + verack，
// 读取对端 version/verack 回复并打印其 agent/subver/services。
// Will replace devtest/main: send version + verack to the target, read the peer's
// version/verack replies and print its agent/subver/services.
package main

import "fmt"

// runShake 执行 shake 子命令 / run the shake subcommand.
func runShake(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("用法 / usage: p2ptest shake <host:port>")
	}
	// 骨架占位 / scaffold stub (not implemented yet).
	fmt.Printf("[shake] 骨架占位 / scaffold stub (not implemented yet)\n")
	fmt.Printf("[shake] 目标 / target: %s\n", args[0])
	fmt.Printf("[shake] 预期输出 / expected output: REMOTE VERSION agent=... pver=... services=...\n")
	return nil
}

func init() {
	register(&command{
		name:    "shake",
		usage:   "<host:port>",
		summary: "完整 P2P 握手（未来替代 devtest/main） / Full handshake (will replace devtest/main)",
		run:     runShake,
	})
}
