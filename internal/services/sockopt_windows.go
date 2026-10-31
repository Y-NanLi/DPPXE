//go:build windows

package services

import "golang.org/x/sys/windows"

// setBroadcastReuse 打开 SO_BROADCAST / SO_REUSEADDR。
//
// Go 默认不开 SO_BROADCAST，往 255.255.255.255 发包会直接失败；
// SO_REUSEADDR 让服务重启时不被上一次的 TIME_WAIT 占住端口。
func setBroadcastReuse(fd uintptr) {
	_ = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_BROADCAST, 1)
	_ = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
}
