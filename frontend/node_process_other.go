//go:build !windows

package main

import "os/exec"

// nodeProcessRunning reports whether an ini.exe (the backend node) process is
// already running.  On non-Windows platforms it uses pgrep; a missing pgrep or
// any error conservatively reports "not running" so the caller falls through
// to the normal start path.
// nodeProcessRunning 报告本机是否已有 ini.exe(后端节点)进程在运行。
// 非 Windows 平台用 pgrep;pgrep 缺失或任何错误都保守地报告"未运行",
// 让调用方走正常的启动路径。
func nodeProcessRunning() bool {
	out, err := exec.Command("pgrep", "-x", "ini.exe").Output()
	if err != nil {
		return false
	}
	return len(out) > 0
}
