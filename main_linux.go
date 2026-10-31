//go:build !windows

package main

import (
	"fmt"

	"dppxe/internal/server"
)

// Linux 版只提供控制端。
//
// 被控端（DP-PXEClient）要做的事——diskpart 分区、wimlib 还原、bcdboot 修引导、
// 写注册表、注入 VNC——全是 Windows 专有的，Linux 上没有任何对应实现。
// 与其给个"启动失败"，不如在入口就把话说清楚。

// runInstall Linux 上开机自启走 systemd，不在这里处理。
func runInstall(uninstall bool, serverAddr string, port int) int {
	fmt.Println("Linux 版没有内置自启安装：请用 systemd 单元托管本程序，")
	fmt.Println("例如 ExecStart=/usr/local/bin/dp-pxe --nogui")
	return 1
}

// runSimRole 模拟器依赖被控端的指令引擎，Linux 上不存在。
func runSimRole(serverAddr string, port int) int {
	fmt.Println("Linux 版不含被控端，模拟器同样不可用。")
	fmt.Println("请在被控机（Windows / PE）上运行 DP-PXEClient。")
	return 1
}

// runClientRole Linux 上没有被控端实现。
func runClientRole(o clientOpts) int {
	fmt.Println("Linux 版只提供控制端（DHCP / TFTP / HTTP / 控制通道 / VNC 查看器）。")
	fmt.Println("被控端请在 Windows 或 PE 里运行 DP-PXEClient.exe。")
	return 1
}

// runServerGUI Linux 的默认启动路径：直接走浏览器模式。
//
// 不用 Wails 窗口的原因：它要 CGO + libgtk-3 + libwebkit2gtk，
// 而控制端绝大多数时候跑在没装桌面的服务器上，装不起也跑不起来。
// 需要原生窗口时可以自行 `go build -tags linuxgui`（见 README）。
func runServerGUI(srv *server.Server, webPort int) int {
	return runBrowser(srv, webPort)
}
