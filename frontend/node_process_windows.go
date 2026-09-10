//go:build windows

package main

import (
	"os/exec"
	"strings"
)

// nodeProcessRunning reports whether an ini.exe (the backend node) process is
// already running on this machine.  It is the second line of defense against
// a double start: the rpclisten probe can miss a node that is still loading
// its database (RPC not bound yet), and that window used to let a second
// ini.exe start and collide on :6000 and the data files.
// nodeProcessRunning 报告本机是否已有 ini.exe(后端节点)进程在运行。
// 它是双开的第二道防线:rpclisten 探测可能漏掉仍在加载数据库(尚未监听
// RPC)的节点,这个窗口期曾导致第二个 ini.exe 被拉起并冲突。
func nodeProcessRunning() bool {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq ini.exe", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "ini.exe")
}
