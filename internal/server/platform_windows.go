//go:build windows

package server

import "os/exec"

// openPath 用资源管理器打开一个目录。
func openPath(dir string) error {
	cmd := exec.Command("explorer", dir)
	hideWindow(cmd)
	return cmd.Start()
}

// launchSupported 是否支持从控制端拉起本机被控端（Windows 有）。
const launchSupported = true

// vncViewerNames 外部 VNC 查看器候选名（内置查看器是首选，这里只作兜底）。
var vncViewerNames = []string{"tvnviewer.exe", "vncviewer.exe", "vncviewer64.exe"}
