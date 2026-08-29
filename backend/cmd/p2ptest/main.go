// p2ptest 入口：子命令路由 + 帮助信息（骨架）。
// p2ptest entry: subcommand router + help text (scaffold).
//
// 设计目标 / Design goals:
//   - 把 devtest/ 下零散的 P2P 探测小程序汇聚成一个独立 CLI 工具。
//     Consolidate the scattered P2P probe mini-tools under devtest/ into one CLI.
//   - 独立 Go module，脱离主项目运行，不影响 backend 编译。
//     Standalone Go module: runs detached from the main project, never affects the backend build.
//   - 后续新增子命令：只需在本目录新增一个 package main 文件并调用 register()。
//     To add a subcommand later: just add one new file in this dir (package main)
//     and call register().
package main

import (
	"fmt"
	"os"
	"sort"
)

// command 描述一个子命令 / A subcommand descriptor.
type command struct {
	name    string // 子命令名 / subcommand name
	usage   string // 参数用法 / argument usage
	summary string // 一句话说明 / one-line summary
	run     func(args []string) error
}

// commands 存放全部已注册子命令 / registry of all registered subcommands.
var commands []*command

// register 注册一个子命令 / register a subcommand.
func register(c *command) {
	commands = append(commands, c)
}

// usage 打印帮助信息（CLI 界面） / print the help text (the CLI "UI").
func usage(w *os.File) {
	fmt.Fprintln(w, "p2ptest — ini-node 独立 P2P 测试工具 / Standalone P2P test tool")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "用法 / Usage:")
	fmt.Fprintln(w, "  p2ptest <子命令 / subcommand> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "子命令 / Subcommands:")
	// 按名称排序输出，保证帮助稳定 / sort by name for a stable help output.
	sorted := make([]*command, len(commands))
	copy(sorted, commands)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	for _, c := range sorted {
		fmt.Fprintf(w, "  %-8s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w, "  help     显示本帮助 / Show this help")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "示例 / Example:")
	fmt.Fprintln(w, "  p2ptest probe 127.0.0.1:34230")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "开发说明 / Dev note:")
	fmt.Fprintln(w, "  独立 Go module，不影响 backend 编译 / Standalone module, no impact on backend build.")
	fmt.Fprintln(w, "  子命令当前为骨架占位，逐个实现中 / Subcommands are scaffold stubs, being implemented one by one.")
}

func main() {
	// 无参数或 help 时输出帮助 / print help when no args or help is requested.
	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
		usage(os.Stdout)
		return
	}

	name := os.Args[1]
	rest := os.Args[2:]
	for _, c := range commands {
		if c.name == name {
			if err := c.run(rest); err != nil {
				fmt.Fprintf(os.Stderr, "p2ptest %s: error: %v\n", name, err)
				os.Exit(1)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "p2ptest: 未知子命令 %q\np2ptest: unknown subcommand %q\n\n", name, name)
	usage(os.Stderr)
	os.Exit(2)
}
