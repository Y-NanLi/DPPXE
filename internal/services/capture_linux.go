//go:build !windows

package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// captureRawExt 原始抓包文件的扩展名。
//
// Linux 用 tcpdump 直接写 pcap，没有"先存原始格式再转换"这一步，
// 所以这里留空 —— CaptureFileNames 拼出来的 raw 路径与 pcap 路径相同，
// 调用方（api.go）不需要写平台分支。
const captureRawExt = ""

// captureFilter BPF 过滤表达式：只抓 PXE 链路上真正关心的四类流量。
const captureFilter = "udp port 67 or udp port 68 or udp port 69 or tcp port 80"

// 正在运行的 tcpdump（StopCapture 要用它收尾）。
var (
	captureMu   sync.Mutex
	captureCmd  *exec.Cmd
	captureDone chan error
)

// CaptureAvailable 系统是否有 tcpdump。
func CaptureAvailable() bool {
	_, err := exec.LookPath("tcpdump")
	return err == nil
}

// StartCapture 启动 tcpdump 后台抓包，直接写 pcap。
//
// 用 -i any 而不是绑具体网卡：控制端可能有多网卡（PXE 常用单独一块），
// 事先猜哪块容易抓空。代价是链路层类型变成 SLL/SLL2，
// 解析侧已在 stripEthernet 里兼容。
func StartCapture(pcap string) (bool, string) {
	if !CaptureAvailable() {
		return false, "系统没有 tcpdump（请先安装：apt install tcpdump / dnf install tcpdump）"
	}
	// 上一次没停干净的话先清掉，否则两个 tcpdump 抢同一个文件。
	stopCaptureProcess()

	_ = os.MkdirAll(filepath.Dir(pcap), 0o755)
	_ = os.Remove(pcap)

	cmd := exec.Command("tcpdump", "-i", "any", "-n", "-s", "0", "-U",
		"-w", pcap, captureFilter)
	hideWindow(cmd)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return false, "启动 tcpdump 失败：" + err.Error()
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	captureMu.Lock()
	captureCmd, captureDone = cmd, done
	captureMu.Unlock()

	// tcpdump 因为没权限 / 网卡不存在会立刻退出。等 300ms 再判断，
	// 否则界面显示"已开始"但实际一个包都没抓到。
	select {
	case err := <-done:
		captureMu.Lock()
		captureCmd, captureDone = nil, nil
		captureMu.Unlock()
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if msg == "" {
			msg = "tcpdump 意外退出"
		}
		return false, msg
	case <-time.After(300 * time.Millisecond):
		return true, "抓包已开始"
	}
}

// StopCapture 停掉 tcpdump 并检查产物。
func StopCapture(_ string, pcap string) (bool, string) {
	stopCaptureProcess()
	// tcpdump 收到信号后才把缓冲区刷进文件，stopCaptureProcess 已经等它退出，
	// 这里 stat 就是最终大小。
	if st, err := os.Stat(pcap); err == nil && st.Size() > 24 {
		return true, fmt.Sprintf("已生成 %s（%d 字节）", filepath.Base(pcap), st.Size())
	}
	return false, "pcap 未生成（tcpdump 可能没有抓包权限，需 root 或 CAP_NET_RAW）"
}

// stopCaptureProcess 给 tcpdump 发 SIGINT 并等它收尾。
func stopCaptureProcess() {
	captureMu.Lock()
	cmd, done := captureCmd, captureDone
	captureCmd, captureDone = nil, nil
	captureMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt) // tcpdump 收到 SIGINT 会正常刷盘退出
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		// 3 秒还不退就强杀，避免"停止抓包"按钮点了没反应。
		_ = cmd.Process.Kill()
		<-done
	}
}
