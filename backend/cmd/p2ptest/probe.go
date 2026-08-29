// probe 子命令骨架：读取对端 version 消息。
// probe subcommand scaffold: read the peer's version message.
//
// 未来实现 / Future implementation: 替代 devtest/nover —— 连接目标节点 P2P 端口，
// 只读对端第一个 version 消息并打印 agent/subver。
// Will replace devtest/nover: connect to the target node's P2P port, read only the
// first version message and print its agent/subver.
package main

import (
	"fmt"

	"github.com/btcsuite/btcd/wire/v2"
)

// runProbe 执行 probe 子命令 / run the probe subcommand.
func runProbe(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("用法 / usage: p2ptest probe <host:port>")
	}
	// 骨架占位：仅验证 wire 依赖已正确接入，后续在此实现真实探测。
	// Scaffold placeholder: only confirms the wire dependency is wired up;
	// the real probe logic will be implemented here later.
	fmt.Printf("[probe] 骨架占位 / scaffold stub (not implemented yet)\n")
	fmt.Printf("[probe] 目标 / target: %s\n", args[0])
	fmt.Printf("[probe] wire 协议版本 / wire protocol version: %d\n", wire.ProtocolVersion)
	fmt.Printf("[probe] 预期输出 / expected output: agent= /ini:x.y.z/ pver=... lastBlock=...\n")
	return nil
}

func init() {
	register(&command{
		name:    "probe",
		usage:   "<host:port>",
		summary: "读取对端 version（未来替代 devtest/nover） / Read peer version (will replace devtest/nover)",
		run:     runProbe,
	})
}
