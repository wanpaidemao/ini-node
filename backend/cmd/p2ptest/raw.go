// raw 子命令骨架：静默连接并读取原始字节。
// raw subcommand scaffold: silent connect and read raw bytes.
//
// 未来实现 / Future implementation: 替代 devtest/rst —— 不发送任何消息直接读取，
// 观察对端在静默连接下的行为（是否先发消息 / 是否直接断开）。
// Will replace devtest/rst: read without sending anything, to observe the peer's
// behaviour on a silent connection (does it speak first / does it disconnect).
package main

import "fmt"

// runRaw 执行 raw 子命令 / run the raw subcommand.
func runRaw(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("用法 / usage: p2ptest raw <host:port>")
	}
	// 骨架占位 / scaffold stub (not implemented yet).
	fmt.Printf("[raw] 骨架占位 / scaffold stub (not implemented yet)\n")
	fmt.Printf("[raw] 目标 / target: %s\n", args[0])
	fmt.Printf("[raw] 预期输出 / expected output: got N bytes without sending anything: <hex>\n")
	return nil
}

func init() {
	register(&command{
		name:    "raw",
		usage:   "<host:port>",
		summary: "静默连接读原始字节（未来替代 devtest/rst） / Silent raw read (will replace devtest/rst)",
		run:     runRaw,
	})
}
