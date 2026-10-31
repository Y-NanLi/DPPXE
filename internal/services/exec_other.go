//go:build !windows

package services

import "os/exec"

// hideWindow 在非 Windows 平台没有"黑窗口"概念，留空即可。
//
// 保留同名函数是为了让调用点（firewall / capture / tftp 等）不必写平台分支。
func hideWindow(cmd *exec.Cmd) {}
