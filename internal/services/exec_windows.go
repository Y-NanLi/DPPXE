package services

import (
	"os/exec"
	"syscall"
)

// hideWindow 启动子进程时不弹黑窗口。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
