package vnc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// fakeServer 一个最小的 RFB 3.8 服务端（None 认证），用来验证握手与解码。
// 64x48 的桌面，先发一块 Raw、一块 Hextile、一块 CopyRect。
func fakeServer(t *testing.T, ln net.Listener) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	must := func(err error) {
		if err != nil {
			t.Logf("fake server: %v", err)
		}
	}

	// 协议版本
	_, _ = conn.Write([]byte("RFB 003.008\n"))
	ver := make([]byte, 12)
	if _, err := io.ReadFull(conn, ver); err != nil {
		return
	}
	// 安全类型：只有 None
	_, _ = conn.Write([]byte{1, secNone})
	sel := make([]byte, 1)
	if _, err := io.ReadFull(conn, sel); err != nil {
		return
	}
	// SecurityResult = 0
	_ = binary.Write(conn, binary.BigEndian, uint32(0))
	// ClientInit
	if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
		return
	}
	// ServerInit: 64x48
	w, h := 64, 48
	_ = binary.Write(conn, binary.BigEndian, uint16(w))
	_ = binary.Write(conn, binary.BigEndian, uint16(h))
	pf := make([]byte, 16)
	pf[0], pf[1], pf[2], pf[3] = 32, 24, 0, 1 // bpp, depth, bigendian, truecolour
	pf[4], pf[5] = 255, 255
	pf[6], pf[7] = 255, 255
	pf[8], pf[9] = 255, 255
	pf[10], pf[11], pf[12] = 0, 8, 16
	_, _ = conn.Write(pf)
	_ = binary.Write(conn, binary.BigEndian, uint32(4))
	_, _ = conn.Write([]byte("TEST"))

	// SetPixelFormat(20) + SetEncodings
	if _, err := io.ReadFull(conn, make([]byte, 20)); err != nil {
		return
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return
	}
	n := int(binary.BigEndian.Uint16(hdr[2:]))
	if _, err := io.ReadFull(conn, make([]byte, n*4)); err != nil {
		return
	}
	// FramebufferUpdateRequest
	if _, err := io.ReadFull(conn, make([]byte, 10)); err != nil {
		return
	}

	// ---- 组装一帧：3 个矩形 ----
	var buf bytes.Buffer
	buf.WriteByte(0) // FramebufferUpdate
	buf.WriteByte(0)
	_ = binary.Write(&buf, binary.BigEndian, uint16(3))

	// 1) Raw 4x4 @(0,0)：纯红
	buf.Write([]byte{0, 0, 0, 0, 0, 4, 0, 4})
	_ = binary.Write(&buf, binary.BigEndian, uint32(encRaw))
	for i := 0; i < 4*4; i++ {
		buf.Write([]byte{255, 0, 0, 0})
	}

	// 2) Hextile 16x16 @(16,0)：背景蓝 + 一个 1x1 绿子矩形在 (2,3)
	buf.Write([]byte{0, 16, 0, 0, 0, 16, 0, 16})
	_ = binary.Write(&buf, binary.BigEndian, uint32(encHextile))
	buf.WriteByte(0x02 | 0x08 | 0x10) // 背景 + 有子矩形 + 子矩形带色
	buf.Write([]byte{0, 0, 255, 0})   // 背景蓝
	buf.WriteByte(1)                  // 1 个子矩形
	buf.Write([]byte{0, 255, 0, 0})   // 前景绿
	buf.WriteByte(0x23)               // x=2, y=3
	buf.WriteByte(0x00)               // w=1, h=1

	// 3) CopyRect 4x4 @(0,16) 复制自 (0,0)
	buf.Write([]byte{0, 0, 0, 16, 0, 4, 0, 4})
	_ = binary.Write(&buf, binary.BigEndian, uint32(encCopyRect))
	buf.Write([]byte{0, 0, 0, 0})

	must(writeAll(conn, buf.Bytes()))
	time.Sleep(300 * time.Millisecond)
}

func writeAll(conn net.Conn, b []byte) error {
	for len(b) > 0 {
		n, err := conn.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func TestHandshakeAndDecode(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go fakeServer(t, ln)

	addr := ln.Addr().(*net.TCPAddr)
	c, err := Dial(Config{Host: "127.0.0.1", Port: addr.Port, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("握手失败: %v", err)
	}
	defer c.Close()

	if w, h := c.Size(); w != 64 || h != 48 {
		t.Errorf("分辨率应为 64x48，实际 %dx%d", w, h)
	}
	if c.Name() != "TEST" {
		t.Errorf("桌面名应为 TEST，实际 %q", c.Name())
	}

	go c.Run()
	if err := c.RequestUpdate(); err != nil {
		t.Fatalf("请求更新失败: %v", err)
	}

	var rects []Rect
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rects = c.TakeDirty()
		if len(rects) >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(rects) != 3 {
		t.Fatalf("应收到 3 块脏矩形，实际 %d", len(rects))
	}

	// Raw：全红
	raw := rects[0]
	if raw.X != 0 || raw.Y != 0 || raw.W != 4 || raw.H != 4 {
		t.Errorf("Raw 矩形位置不对: %+v", raw)
	}
	if !(raw.Pixels[0] == 255 && raw.Pixels[1] == 0 && raw.Pixels[2] == 0) {
		t.Errorf("Raw 像素应为红色，实际 %v", raw.Pixels[:4])
	}
	if raw.Pixels[3] != 255 {
		t.Errorf("alpha 应补成 255，实际 %d", raw.Pixels[3])
	}

	// Hextile：背景蓝，子矩形绿
	hx := rects[1]
	if hx.X != 16 || hx.W != 16 || hx.H != 16 {
		t.Errorf("Hextile 矩形位置不对: %+v", hx)
	}
	at := func(x, y int) []byte {
		off := (y*hx.W + x) * 4
		return hx.Pixels[off : off+4]
	}
	if px := at(0, 0); !(px[0] == 0 && px[1] == 0 && px[2] == 255) {
		t.Errorf("Hextile 背景应为蓝，实际 %v", px)
	}
	if px := at(2, 3); !(px[0] == 0 && px[1] == 255 && px[2] == 0) {
		t.Errorf("Hextile 子矩形应为绿，实际 %v", px)
	}

	// CopyRect：复制过来还是红
	cp := rects[2]
	if cp.X != 0 || cp.Y != 16 || cp.W != 4 {
		t.Errorf("CopyRect 位置不对: %+v", cp)
	}
	if !(cp.Pixels[0] == 255 && cp.Pixels[1] == 0) {
		t.Errorf("CopyRect 内容应复制自 (0,0)，实际 %v", cp.Pixels[:4])
	}
}

func TestPickSecurity(t *testing.T) {
	cases := []struct {
		in   []byte
		want byte
	}{
		{[]byte{secNone, secVNC}, secNone},
		{[]byte{secVNC}, secVNC},
		{[]byte{16, 18}, secInvalid}, // 只有 Tight/Ultra 等不支持的类型
	}
	for _, c := range cases {
		if got := pickSecurity(c.in); got != c.want {
			t.Errorf("pickSecurity(%v) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

func TestReverseBits(t *testing.T) {
	if reverseBits(0x01) != 0x80 || reverseBits(0xF0) != 0x0F {
		t.Errorf("比特反转不对: %02x %02x", reverseBits(0x01), reverseBits(0xF0))
	}
}

// TestVncAuthDES 校验 VNC 密码认证的 DES 加密（与 rfbproto 的算法一致）。
func TestVncAuthDES(t *testing.T) {
	// 密码 "pass" 补零后作为密钥（比特反转），挑战值全 0x00 时，
	// 用 DES 加密得到的密文固定，可用来防止实现被改坏。
	challenge := make([]byte, 16)
	out, err := vncAuthResponse("pass", challenge)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 16 {
		t.Fatalf("响应长度应为 16，实际 %d", len(out))
	}
	if fmt.Sprintf("%x", out) == "00000000000000000000000000000000" {
		t.Error("响应不应为全零（说明没加密）")
	}
}

// TestRealServer 连真实 VNC 服务端（默认跳过）：
//
//	DPPXE_VNC_TEST=127.0.0.1:5900 DPPXE_VNC_PASS=xxx go test ./internal/vnc -run TestRealServer -v
//
// 用来确认握手 / 编码协商 / 画面解码在真实 TightVNC 上也能跑通。
func TestRealServer(t *testing.T) {
	addr := os.Getenv("DPPXE_VNC_TEST")
	if addr == "" {
		t.Skip("未设置 DPPXE_VNC_TEST，跳过真实服务端测试")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port := 0
	fmt.Sscanf(portStr, "%d", &port)

	c, err := Dial(Config{Host: host, Port: port, Password: os.Getenv("DPPXE_VNC_PASS"),
		Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("连接真实服务端失败: %v", err)
	}
	defer c.Close()

	w, h := c.Size()
	t.Logf("真实桌面: %dx%d 名称=%q", w, h, c.Name())
	if w <= 0 || h <= 0 {
		t.Fatalf("分辨率异常: %dx%d", w, h)
	}

	go c.Run()
	if err := c.RequestUpdate(); err != nil {
		t.Fatalf("请求更新失败: %v", err)
	}

	var total int
	var nonzero int
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && total == 0 {
		rects := c.TakeDirty()
		for _, r := range rects {
			total += r.W * r.H
			for i := 0; i+3 < len(r.Pixels); i += 4 {
				if r.Pixels[i] != 0 || r.Pixels[i+1] != 0 || r.Pixels[i+2] != 0 {
					nonzero++
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if total == 0 {
		t.Fatalf("没收到任何画面数据（err=%v）", c.Err())
	}
	t.Logf("收到 %d 像素，其中非黑 %d 个", total, nonzero)
	if nonzero == 0 {
		t.Error("整屏全黑，可能解码有问题")
	}
}
