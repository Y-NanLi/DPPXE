//go:build windows

package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// captureRawExt 原始抓包文件的扩展名（Windows 用 pktmon 的 .etl，需要转 pcap）。
const captureRawExt = ".etl"

// CaptureAvailable 系统是否有 pktmon（Win10 1809+）。
func CaptureAvailable() bool {
	_, err := exec.LookPath("pktmon")
	return err == nil
}

// StartCapture 开始抓包（pktmon 写 .etl）。
func StartCapture(etl string) (bool, string) {
	if !CaptureAvailable() {
		return false, "系统没有 pktmon（需要 Win10 1809+）"
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	_, _ = run("pktmon", "stop")
	_, _ = run("pktmon", "filter", "remove")
	for _, f := range [][3]string{
		{"D67", "67", "udp"}, {"D68", "68", "udp"},
		{"T69", "69", "udp"}, {"H80", "80", "tcp"},
	} {
		_, _ = run("pktmon", "filter", "add", f[0], "-p", f[1], "-t", f[2])
	}
	out, err := run("pktmon", "start", "--capture", "--pkt-size", "0", "--file-name", etl)
	if err != nil {
		msg := strings.TrimSpace(out)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return false, msg
	}
	return true, "抓包已开始"
}

// StopCapture 停止抓包并转成 pcap。
func StopCapture(etl, pcap string) (bool, string) {
	run := func(args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	_, _ = run("pktmon", "stop")
	out, _ := run("pktmon", "etl2pcap", etl, "-o", pcap)
	if st, err := os.Stat(pcap); err == nil && st.Size() > 24 {
		return true, fmt.Sprintf("已生成 %s（%d 字节）", filepath.Base(pcap), st.Size())
	}
	msg := strings.TrimSpace(out)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if msg == "" {
		msg = "pcap 未生成"
	}
	return false, msg
}
