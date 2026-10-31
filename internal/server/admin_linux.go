//go:build !windows

package server

import "os"

// IsAdmin 当前进程是否以 root 运行。
//
// Linux 上 DHCP(67) / TFTP(69) / HTTP(80) 都是特权端口，
// 抓包也需要 CAP_NET_RAW —— 统一按 root 判断即可。
func IsAdmin() bool { return os.Geteuid() == 0 }

// launchSupported 是否支持从控制端拉起本机被控端。
//
// 被控端（DP-PXEClient）只在 Windows / PE 里有意义，
// Linux 版没有对应实现，界面上点了要给出明确提示而不是"启动失败"。
const launchSupported = false

// vncViewerNames 外部 VNC 查看器候选名（内置查看器是首选，这里只作兜底）。
var vncViewerNames = []string{"xtigervncviewer", "tigervnc", "vncviewer", "remmina"}
