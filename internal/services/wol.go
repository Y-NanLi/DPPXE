package services

import (
	"net"
	"strings"

	"golang.org/x/sys/windows"
)

// setBroadcast 打开 SO_BROADCAST（Go 默认不开，发 255.255.255.255 会失败）。
func setBroadcast(conn *net.UDPConn) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_BROADCAST, 1)
		_ = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
	})
}

// SendMagicPacket WOL 网络唤醒：由服务端发魔术包（客户端此时是关机状态）。
func SendMagicPacket(mac string) bool {
	clean := strings.NewReplacer("-", ":", ".", ":").Replace(strings.TrimSpace(mac))
	parts := strings.Split(clean, ":")
	if len(parts) != 6 {
		return false
	}
	raw := make([]byte, 6)
	for i, p := range parts {
		v, ok := parseHexByte(p)
		if !ok {
			return false
		}
		raw[i] = v
	}
	packet := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		packet = append(packet, 0xFF)
	}
	for i := 0; i < 16; i++ {
		packet = append(packet, raw...)
	}

	ok := false
	for _, port := range []int{9, 7} {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
		if err != nil {
			continue
		}
		setBroadcast(conn)
		_, err = conn.WriteToUDP(packet, &net.UDPAddr{IP: net.IPv4bcast, Port: port})
		_ = conn.Close()
		if err == nil {
			ok = true
		}
	}
	return ok
}

func parseHexByte(s string) (byte, bool) {
	if len(s) == 0 || len(s) > 2 {
		return 0, false
	}
	var v int
	for _, ch := range s {
		var d int
		switch {
		case ch >= '0' && ch <= '9':
			d = int(ch - '0')
		case ch >= 'a' && ch <= 'f':
			d = int(ch-'a') + 10
		case ch >= 'A' && ch <= 'F':
			d = int(ch-'A') + 10
		default:
			return 0, false
		}
		v = v*16 + d
	}
	return byte(v), true
}
