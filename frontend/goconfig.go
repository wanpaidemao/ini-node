package main

// goconfig.go — desktop config bindings exposed to the frontend via Wails.
// / 桌面端配置 binding,经 Wails 暴露给前端。

import (
	"os"
	"os/exec"
	"runtime"
)

// OpenDataDir opens the given directory in the OS file manager (Explorer on
// Windows). Creates it first when missing so the button works on a fresh
// install. / 在系统文件管理器中打开给定目录(Windows 为资源管理器)。
// 目录不存在时先创建,保证全新安装也能点开。
func (s *GreetService) OpenDataDir(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}
