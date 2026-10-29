package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"unsafe"

	"golang.org/x/sys/windows"

	"dppxe/internal/config"
	"dppxe/internal/server"
	"dppxe/internal/webui"
)

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procAllocConsole       = kernel32.NewProc("AllocConsole")
	procSetConsoleCP       = kernel32.NewProc("SetConsoleCP")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procSetConsoleTitleW   = kernel32.NewProc("SetConsoleTitleW")
)

// serveHTTP 浏览器模式：静态资源 + /api。
func serveHTTP(srv *server.Server, addr string) error {
	hs := &http.Server{Addr: addr, Handler: webui.BrowserHandler(srv)}
	return hs.ListenAndServe()
}

// signalNotify 订阅 Ctrl+C。
func signalNotify(ch chan os.Signal) {
	signal.Notify(ch, os.Interrupt)
}

// ensureConsole 给 windowsgui 子系统的进程补一个控制台窗口。
//
// DP-PXE.exe 编译时带了 -H windowsgui（双击不弹黑框），但同一个 exe 也可能
// 当被控端用（--client / --sim / --selftest）。这种时候没有 stdout，日志全丢，
// 用户看到的就是"启动没反应"，所以这里主动 AllocConsole 并把标准流接回去。
// 已有控制台时 AllocConsole 返回 0，忽略即可。
func ensureConsole() {
	if r, _, _ := procAllocConsole.Call(); r == 0 {
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
		os.Stdout = out
		os.Stderr = out
	}
	if in, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		os.Stdin = in
	}
	if title, err := windows.UTF16PtrFromString("DP-PXE 被控端 " + config.AppVersion); err == nil {
		_, _, _ = procSetConsoleTitleW.Call(uintptr(unsafe.Pointer(title)))
	}
}

// fixConsoleCodepage 把控制台切到 UTF-8。
//
// Go 往控制台写的是 UTF-8 字节，而中文 PE 默认代码页是 936(GBK)，
// 不切的话日志里的中文全是乱码（Python 版也做了同样的事）。
func fixConsoleCodepage() {
	_, _, _ = procSetConsoleOutputCP.Call(65001)
	_, _, _ = procSetConsoleCP.Call(65001)
}

// pauseIfConsole 出错时停一下，避免双击运行的窗口一闪而过。
func pauseIfConsole() {
	if os.Stdin == nil {
		return
	}
	fmt.Print("\n按回车键退出…")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
