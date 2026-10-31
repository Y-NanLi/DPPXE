//go:build !windows

package services

import "golang.org/x/sys/unix"

// setBroadcastReuse 打开 SO_BROADCAST / SO_REUSEADDR。
//
// Go 默认不开 SO_BROADCAST，往 255.255.255.255 发包会直接失败；
// SO_REUSEADDR 让服务重启时不被上一次的 TIME_WAIT 占住端口。
//
// Linux 侧额外设了 SO_REUSEPORT：DHCP 监听 67 端口时，
// 如果同时有 dnsmasq / systemd-networkd 之类在跑，没有它会直接 bind 失败。
func setBroadcastReuse(fd uintptr) {
	_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1)
	_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
}
