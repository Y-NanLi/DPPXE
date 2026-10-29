package server

import (
	"os/exec"
	"strings"
)

// IsAdmin 当前进程是否有管理员权限。
func IsAdmin() bool {
	cmd := exec.Command("net", "session")
	hideWindow(cmd)
	return cmd.Run() == nil
}

// psQuote 把字符串转成 PowerShell 单引号字面量。
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
