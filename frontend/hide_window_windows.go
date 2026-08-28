//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideWindow marks cmd so Windows does not pop a console window for the
// child process (CREATE_NO_WINDOW).  Used when the GUI app spawns console
// binaries such as ini.exe / walletapi.exe / powershell.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
