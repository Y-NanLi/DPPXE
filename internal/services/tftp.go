package services

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dppxe/internal/config"
)

// TFTP opcode
const (
	tftpRRQ   = 1
	tftpWRQ   = 2
	tftpData  = 3
	tftpAck   = 4
	tftpError = 5
	tftpOACK  = 6
)

// TftpServer TFTP 服务（UDP 69，RFC 1350 + blksize 选项）。
//
// 只服务 netboot 目录下的引导文件；大镜像不走 TFTP —— 和原软件一样，WIM 交给 HTTP。
type TftpServer struct {
	cfg  *config.Config
	log  LogFunc
	conn *net.UDPConn
	stop chan struct{}
	wg   sync.WaitGroup

	root     string
	menuPath string
}

// NewTftpServer 创建 TFTP 服务。
func NewTftpServer(cfg *config.Config, log LogFunc) *TftpServer {
	return &TftpServer{cfg: cfg, log: log, root: cfg.RootDir}
}

// Start 绑定 69 端口并开始收包。
func (t *TftpServer) Start() error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: t.cfg.TftpPort})
	if err != nil {
		return fmt.Errorf("绑定 UDP %d 失败: %w", t.cfg.TftpPort, err)
	}
	t.conn = conn
	t.root = t.cfg.RootDir
	t.menuPath = config.ResolveIn(t.root, t.cfg.MenuFile)
	t.stop = make(chan struct{})
	t.wg.Add(1)
	go t.loop()
	t.log(fmt.Sprintf("TFTP 服务已启动（根目录 %s）", absPath(t.root)), "ok")
	return nil
}

// Stop 停止服务。
func (t *TftpServer) Stop() {
	if t.stop != nil {
		select {
		case <-t.stop:
		default:
			close(t.stop)
		}
	}
	if t.conn != nil {
		_ = t.conn.Close()
		t.conn = nil
	}
	t.wg.Wait()
	t.log("TFTP 服务已停止", "warn")
}

func (t *TftpServer) loop() {
	defer t.wg.Done()
	buf := make([]byte, 2048)
	for {
		select {
		case <-t.stop:
			return
		default:
		}
		_ = t.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, addr, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return
		}
		if n < 4 {
			t.log(fmt.Sprintf("TFTP 收到异常短包 <- %s (%d B)", addr.IP, n), "warn")
			continue
		}
		op := binary.BigEndian.Uint16(buf[:2])
		if op != tftpRRQ {
			t.log(fmt.Sprintf("TFTP 收到非 RRQ 报文 opcode=%d <- %s", op, addr.IP), "warn")
			continue
		}
		req := make([]byte, n)
		copy(req, buf[:n])
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			t.serve(req, addr)
		}()
	}
}

// resolve 把请求文件名解析成根目录内的真实路径（防目录穿越）。
func (t *TftpServer) resolve(name string) string {
	name = strings.TrimLeft(strings.ReplaceAll(name, "\\", "/"), "/")
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return ""
		}
	}
	p := config.ResolveIn(t.root, name)
	if p == "" {
		return ""
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return ""
	}
	return p
}

func (t *TftpServer) serve(req []byte, addr *net.UDPAddr) {
	parts := bytes.Split(req[2:], []byte{0})
	filename := string(parts[0])
	opts := map[string]string{}
	for i := 2; i+1 < len(parts); i += 2 {
		k := strings.ToLower(string(parts[i]))
		if k != "" {
			opts[k] = string(parts[i+1])
		}
	}

	path := t.resolve(filename)
	sock, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return
	}
	defer sock.Close()

	if path == "" {
		sendTftpError(sock, addr, 1, "File not found")
		// UEFI 启动时会"探测性"地依次尝试多个路径（先 \BCD 再 \Boot\BCD）、
		// 以及 Secure Boot 策略/语言资源/字体等可选文件。找不到属于正常现象，
		// 用 err 级别刷屏会让人以为网络启动失败了。
		if isOptionalTftpFile(filename) {
			t.log(fmt.Sprintf("TFTP 跳过 %s（可选文件，PE 里没有属正常）", filename), "info")
		} else {
			t.log(fmt.Sprintf("TFTP 404 %s <- %s  (根目录 %s 下找不到该文件)",
				filename, addr.IP, absPath(t.root)), "err")
		}
		return
	}

	// 先决定要发什么内容（菜单需要现场渲染），再回 OACK，保证 tsize 准确
	var inline []byte
	var size int64
	note := ""
	if t.menuPath != "" && samePath(path, t.menuPath) {
		inline = config.RenderMenu(path, t.cfg.ServerIP)
		size = int64(len(inline))
		note = fmt.Sprintf("，已替换 ${next-server}=%s", t.cfg.ServerIP)
	} else {
		st, err := os.Stat(path)
		if err != nil {
			sendTftpError(sock, addr, 1, "File not found")
			return
		}
		size = st.Size()
	}

	blksize := 512
	if v, ok := opts["blksize"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			// Windows 的 PXE 客户端常要 65464，别卡在 1468 上（否则 500MB 的 WIM 要传半小时）
			if n < 512 {
				n = 512
			}
			if n > 65464 {
				n = 65464
			}
			blksize = n
		}
	}
	_, wantTsize := opts["tsize"]

	if blksize != 512 || wantTsize {
		var body []byte
		if blksize != 512 {
			body = append(body, []byte("blksize\x00"+strconv.Itoa(blksize)+"\x00")...)
		}
		if wantTsize {
			body = append(body, []byte("tsize\x00"+strconv.FormatInt(size, 10)+"\x00")...)
		}
		oack := make([]byte, 2, 2+len(body))
		binary.BigEndian.PutUint16(oack, tftpOACK)
		oack = append(oack, body...)
		if !sendWaitAckT(sock, addr, oack, 0, 2, time.Second) {
			// 客户端常常在收到 OACK 之前就重发了 RRQ（或已经转到别的会话），
			// 这种"没人应答"很常见，且已有新的请求在跑，不是传输失败。
			t.log("TFTP OACK 无应答，放弃本次请求 "+filename+"（客户端通常会重发）", "info")
			return
		}
	}

	t.log(fmt.Sprintf("TFTP 发送 %s -> %s (%d B, blksize=%d%s)",
		filename, addr.IP, size, blksize, note), "info")

	var src io.Reader
	if inline != nil {
		src = bytes.NewReader(inline)
	} else {
		fh, err := os.Open(path)
		if err != nil {
			sendTftpError(sock, addr, 2, "Access violation")
			return
		}
		defer fh.Close()
		src = fh
	}

	block := 1
	buf := make([]byte, blksize)
	for {
		n, err := io.ReadFull(src, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return
		}
		pkt := make([]byte, 4, 4+n)
		binary.BigEndian.PutUint16(pkt[0:], tftpData)
		binary.BigEndian.PutUint16(pkt[2:], uint16(block&0xFFFF))
		pkt = append(pkt, buf[:n]...)
		if !sendWaitAck(sock, addr, pkt, uint16(block&0xFFFF)) {
			t.log(fmt.Sprintf("TFTP 超时中断 %s（第 %d 块）", filename, block), "warn")
			return
		}
		if n < blksize {
			return
		}
		block++
	}
}

// sendWaitAck 发一包并等 ACK，最多重试 4 次。
func sendWaitAck(sock *net.UDPConn, addr *net.UDPAddr, pkt []byte, block uint16) bool {
	return sendWaitAckT(sock, addr, pkt, block, 4, 2*time.Second)
}

// sendWaitAckT 发一个包并等对应块号的 ACK（可配置重试次数与单次超时）。
// 数据块用 4×2s；OACK 只需要 2×1s——OACK 没人应答时客户端多半已经重发 RRQ，
// 等太久只会让日志和资源白白拖着。
func sendWaitAckT(sock *net.UDPConn, addr *net.UDPAddr, pkt []byte, block uint16,
	attempts int, timeout time.Duration) bool {
	buf := make([]byte, 1024)
	for attempt := 0; attempt < attempts; attempt++ {
		if _, err := sock.WriteToUDP(pkt, addr); err != nil {
			return false
		}
		_ = sock.SetReadDeadline(time.Now().Add(timeout))
		for {
			n, peer, err := sock.ReadFromUDP(buf)
			if err != nil {
				break // 超时，重发
			}
			if !peer.IP.Equal(addr.IP) {
				continue
			}
			if n >= 4 && binary.BigEndian.Uint16(buf[:2]) == tftpAck {
				if binary.BigEndian.Uint16(buf[2:4]) == block {
					return true
				}
			}
		}
	}
	return false
}

// isOptionalTftpFile 判断是不是"启动过程探测性的可选文件"。
//
// UEFI/Windows 启动管理器会按固定顺序尝试多个路径和可选资源，
// 缺了会自动降级，不算错误：
//   - 根目录 \BCD（随后会试 \Boot\BCD）
//   - *.p7b / *.pol：Secure Boot 代码完整性策略、BitLocker 解锁策略
//   - *.mui：语言资源
//   - *.ttf：启动界面字体
func isOptionalTftpFile(name string) bool {
	n := strings.ToLower(strings.ReplaceAll(name, "/", `\`))
	if n == `\bcd` {
		return true
	}
	for _, suf := range []string{".p7b", ".pol", ".mui", ".ttf"} {
		if strings.HasSuffix(n, suf) {
			return true
		}
	}
	return false
}

func sendTftpError(sock *net.UDPConn, addr *net.UDPAddr, code uint16, msg string) {
	pkt := make([]byte, 4, 4+len(msg)+1)
	binary.BigEndian.PutUint16(pkt[0:], tftpError)
	binary.BigEndian.PutUint16(pkt[2:], code)
	pkt = append(pkt, []byte(msg)...)
	pkt = append(pkt, 0)
	_, _ = sock.WriteToUDP(pkt, addr)
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return strings.EqualFold(aa, bb)
}
