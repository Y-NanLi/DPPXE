// Package vnc 内置 VNC(RFB) 客户端。
//
// 目的：把"连接 VNC"从"拉起外部 tvnviewer.exe"变成控制端窗口里直接看画面，
// 不依赖任何外部 viewer。实现 RFB 3.3 / 3.7 / 3.8：
//
//	握手 → 安全类型（None / VNC 认证 DES）→ ServerInit
//	SetPixelFormat(32bpp RGBA) + SetEncodings(CopyRect, Hextile, Raw, DesktopSize)
//	FramebufferUpdateRequest → 收 FramebufferUpdate（Raw / Hextile / CopyRect）
//	PointerEvent / KeyEvent 回传键鼠
//
// 帧数据在本地维护一份 RGBA 画布，每次更新只把"脏矩形"交出去（TakeDirty），
// 前端按矩形贴到 canvas 上，比整屏重传省得多。
package vnc

import (
	"bufio"
	"bytes"
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// 编码类型
const (
	encRaw         = 0
	encCopyRect    = 1
	encHextile     = 5
	encDesktopSize = -223
)

// 安全类型
const (
	secInvalid = 0
	secNone    = 1
	secVNC     = 2
)

// 鼠标按键掩码
const (
	ButtonLeft      = 1
	ButtonMiddle    = 2
	ButtonRight     = 4
	ButtonWheelUp   = 8
	ButtonWheelDown = 16
)

// Rect 一块脏区域（RGBA，行优先）。
type Rect struct {
	X, Y, W, H int
	Pixels     []byte
}

// Config 连接参数。
type Config struct {
	Host     string
	Port     int
	Password string
	Timeout  time.Duration
}

// Client 一个 VNC 会话。
type Client struct {
	conn net.Conn
	br   *bufio.Reader

	writeMu sync.Mutex

	mu           sync.Mutex
	width        int
	height       int
	name         string
	fb           []byte // 完整画布 RGBA
	dirty        []Rect
	pending      bool      // 已发出更新请求，等回包
	pendingSince time.Time // 请求发出的时间（超时就重发，防止请求被服务端丢掉后卡死）
	gotFull      bool      // 已经拿到过整屏（之后用增量请求）
	lastErr      error
	closed       bool
	updatedAt    time.Time
}

// Dial 建立连接并完成握手（不启动读循环，由调用方 Run 驱动）。
func Dial(cfg Config) (*Client, error) {
	if cfg.Port == 0 {
		cfg.Port = 5900
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 8 * time.Second
	}
	conn, err := net.DialTimeout("tcp4",
		net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port)), cfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("连接 %s:%d 失败: %w", cfg.Host, cfg.Port, err)
	}
	c := &Client{conn: conn, br: bufio.NewReaderSize(conn, 64*1024)}
	_ = conn.SetDeadline(time.Now().Add(cfg.Timeout))
	if err := c.handshake(cfg.Password); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return c, nil
}

// ---------------------------------------------------------------- 握手

func (c *Client) handshake(password string) error {
	ver := make([]byte, 12)
	if _, err := io.ReadFull(c.br, ver); err != nil {
		return fmt.Errorf("读取协议版本失败: %w", err)
	}
	if !bytes.HasPrefix(ver, []byte("RFB ")) {
		return errors.New("对方不是 VNC 服务端")
	}
	minor := 3
	if len(ver) >= 11 {
		// "RFB 003.008\n" → 取最后 3 位
		fmt.Sscanf(string(ver[8:11]), "%d", &minor)
	}

	var secType byte
	switch {
	case minor >= 8:
		if _, err := c.conn.Write([]byte("RFB 003.008\n")); err != nil {
			return err
		}
		n, err := c.br.ReadByte()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("服务端拒绝连接: %s", c.readReason())
		}
		types := make([]byte, n)
		if _, err := io.ReadFull(c.br, types); err != nil {
			return err
		}
		secType = pickSecurity(types)
		if secType == secInvalid {
			return fmt.Errorf("不支持的安全类型 %v（需要 None 或 VNC 认证）", types)
		}
		if _, err := c.conn.Write([]byte{secType}); err != nil {
			return err
		}
	case minor >= 7:
		if _, err := c.conn.Write([]byte("RFB 003.007\n")); err != nil {
			return err
		}
		n, err := c.br.ReadByte()
		if err != nil {
			return err
		}
		types := make([]byte, n)
		if _, err := io.ReadFull(c.br, types); err != nil {
			return err
		}
		secType = pickSecurity(types)
		if secType == secInvalid {
			return fmt.Errorf("不支持的安全类型 %v", types)
		}
		if _, err := c.conn.Write([]byte{secType}); err != nil {
			return err
		}
	default: // 3.3
		if _, err := c.conn.Write([]byte("RFB 003.003\n")); err != nil {
			return err
		}
		var t uint32
		if err := binary.Read(c.br, binary.BigEndian, &t); err != nil {
			return err
		}
		switch t {
		case secNone:
			secType = secNone
		case secVNC:
			secType = secVNC
		default:
			return fmt.Errorf("服务端要求不支持的安全类型 %d", t)
		}
	}

	if secType == secVNC {
		if password == "" {
			return errors.New("该 VNC 服务端需要密码，请先填写密码")
		}
		challenge := make([]byte, 16)
		if _, err := io.ReadFull(c.br, challenge); err != nil {
			return err
		}
		resp, err := vncAuthResponse(password, challenge)
		if err != nil {
			return err
		}
		if _, err := c.conn.Write(resp); err != nil {
			return err
		}
	}

	// 3.8/3.7 在认证后都回 SecurityResult
	if minor >= 7 {
		var result uint32
		if err := binary.Read(c.br, binary.BigEndian, &result); err != nil {
			return err
		}
		if result != 0 {
			return fmt.Errorf("VNC 认证失败（密码错误？）%s", c.readReason())
		}
	}

	// ClientInit：shared = 1（不影响其它已连接的查看器）
	if _, err := c.conn.Write([]byte{1}); err != nil {
		return err
	}

	// ServerInit
	var w, h uint16
	if err := binary.Read(c.br, binary.BigEndian, &w); err != nil {
		return err
	}
	if err := binary.Read(c.br, binary.BigEndian, &h); err != nil {
		return err
	}
	pf := make([]byte, 16)
	if _, err := io.ReadFull(c.br, pf); err != nil {
		return err
	}
	var nameLen uint32
	if err := binary.Read(c.br, binary.BigEndian, &nameLen); err != nil {
		return err
	}
	if nameLen > 1<<16 {
		return errors.New("桌面名过长")
	}
	name := make([]byte, nameLen)
	if _, err := io.ReadFull(c.br, name); err != nil {
		return err
	}

	c.mu.Lock()
	c.width, c.height = int(w), int(h)
	c.name = string(name)
	c.fb = make([]byte, c.width*c.height*4)
	c.mu.Unlock()

	if err := c.setPixelFormat(); err != nil {
		return err
	}
	if err := c.setEncodings(); err != nil {
		return err
	}
	return nil
}

func pickSecurity(types []byte) byte {
	for _, t := range types {
		if t == secNone {
			return secNone
		}
	}
	for _, t := range types {
		if t == secVNC {
			return secVNC
		}
	}
	return secInvalid
}

func (c *Client) readReason() string {
	var n uint32
	if err := binary.Read(c.br, binary.BigEndian, &n); err != nil || n > 4096 {
		return ""
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.br, buf); err != nil {
		return ""
	}
	return "：" + string(buf)
}

// vncAuthResponse 用 DES 加密挑战值（VNC 密码认证）。
// 密码截断/补零到 8 字节，作为 DES 密钥（每字节比特序反转）。
func vncAuthResponse(password string, challenge []byte) ([]byte, error) {
	key := make([]byte, 8)
	copy(key, []byte(password))
	for i := range key {
		key[i] = reverseBits(key[i])
	}
	block, err := des.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16)
	block.Encrypt(out[:8], challenge[:8])
	block.Encrypt(out[8:], challenge[8:16])
	return out, nil
}

func reverseBits(b byte) byte {
	var out byte
	for i := 0; i < 8; i++ {
		out <<= 1
		out |= b & 1
		b >>= 1
	}
	return out
}

// setPixelFormat 固定 32bpp RGBA：红 0-7、绿 8-15、蓝 16-23、第 4 字节留空。
func (c *Client) setPixelFormat() error {
	buf := make([]byte, 20)
	buf[0] = 0                                // SetPixelFormat
	buf[4] = 32                               // bits-per-pixel
	buf[5] = 24                               // depth
	buf[6] = 0                                // big-endian = false → 内存里依次是 R,G,B,X
	buf[7] = 1                                // true-colour
	binary.BigEndian.PutUint16(buf[8:], 255)  // red-max
	binary.BigEndian.PutUint16(buf[10:], 255) // green-max
	binary.BigEndian.PutUint16(buf[12:], 255) // blue-max
	buf[14] = 0                               // red-shift
	buf[15] = 8                               // green-shift
	buf[16] = 16                              // blue-shift
	return c.write(buf)
}

func (c *Client) setEncodings() error {
	encs := []int32{encCopyRect, encHextile, encRaw, encDesktopSize}
	buf := make([]byte, 4+len(encs)*4)
	buf[0] = 2 // SetEncodings
	binary.BigEndian.PutUint16(buf[2:], uint16(len(encs)))
	for i, e := range encs {
		binary.BigEndian.PutUint32(buf[4+i*4:], uint32(e))
	}
	return c.write(buf)
}

func (c *Client) write(b []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.conn.Write(b)
	return err
}

// ---------------------------------------------------------------- 状态

// Size 远端分辨率。
func (c *Client) Size() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.width, c.height
}

// Name 远端桌面名。
func (c *Client) Name() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.name
}

// Err 最近一次读错误。
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// LastUpdate 最近一次收到画面的时间。
func (c *Client) LastUpdate() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.updatedAt
}

// Close 断开。
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.conn.Close()
}

// RequestUpdate 请求一帧。
//
// 已有未完成的请求时直接返回（自然限速）；但超过 2 秒还没回包就重发一次 ——
// TightVNC 在"分辨率通知(DesktopSize)"之后会丢掉紧接着的那次请求，
// 不重发的话画面就永远不来了。
func (c *Client) RequestUpdate() error {
	c.mu.Lock()
	if c.closed || c.width == 0 {
		c.mu.Unlock()
		return nil
	}
	if c.pending && time.Since(c.pendingSince) < 2*time.Second {
		c.mu.Unlock()
		return nil
	}
	c.pending = true
	c.pendingSince = time.Now()
	w, h := c.width, c.height
	c.mu.Unlock()

	c.mu.Lock()
	incremental := c.gotFull
	c.mu.Unlock()

	buf := make([]byte, 10)
	buf[0] = 3 // FramebufferUpdateRequest
	if incremental {
		buf[1] = 1 // 第一次必须整屏，之后才用增量
	}
	binary.BigEndian.PutUint16(buf[2:], 0)
	binary.BigEndian.PutUint16(buf[4:], 0)
	binary.BigEndian.PutUint16(buf[6:], uint16(w))
	binary.BigEndian.PutUint16(buf[8:], uint16(h))
	return c.write(buf)
}

// TakeDirty 取走当前所有脏矩形（清空）。
func (c *Client) TakeDirty() []Rect {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.dirty) == 0 {
		return nil
	}
	out := c.dirty
	c.dirty = nil
	return out
}

// ---------------------------------------------------------------- 读循环

// Run 读循环，直到连接断开。调用方一般放在 goroutine 里。
func (c *Client) Run() {
	for {
		if err := c.readMessage(); err != nil {
			c.mu.Lock()
			if !c.closed {
				c.lastErr = err
			}
			c.mu.Unlock()
			_ = c.Close()
			return
		}
	}
}

func (c *Client) readMessage() error {
	typ, err := c.br.ReadByte()
	if err != nil {
		return err
	}
	switch typ {
	case 0: // FramebufferUpdate
		return c.readFramebufferUpdate()
	case 1: // SetColourMapEntries
		var hdr [5]byte
		if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
			return err
		}
		n := int(binary.BigEndian.Uint16(hdr[3:]))
		skip := make([]byte, n*6)
		_, err := io.ReadFull(c.br, skip)
		return err
	case 2: // Bell
		return nil
	case 3: // ServerCutText
		var hdr [7]byte
		if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
			return err
		}
		n := binary.BigEndian.Uint32(hdr[3:])
		if n > 1<<20 {
			return errors.New("剪贴板数据异常")
		}
		buf := make([]byte, n)
		_, err := io.ReadFull(c.br, buf)
		return err
	default:
		return fmt.Errorf("未知的 VNC 消息类型 %d", typ)
	}
}

func (c *Client) readFramebufferUpdate() error {
	var hdr [3]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return err
	}
	nrects := int(binary.BigEndian.Uint16(hdr[1:]))
	sawPixels := false
	for i := 0; i < nrects; i++ {
		got, err := c.readRect()
		if err != nil {
			return err
		}
		if got {
			sawPixels = true
		}
	}
	c.mu.Lock()
	c.pending = false
	if sawPixels {
		c.gotFull = true
		c.updatedAt = time.Now()
	}
	needMore := !c.gotFull // 只收到 DesktopSize / 空更新时，立刻再要一帧整屏
	c.mu.Unlock()
	if needMore {
		return c.RequestUpdate()
	}
	return nil
}

// readRect 处理一个矩形，返回是否包含真实像素。
func (c *Client) readRect() (bool, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return false, err
	}
	x := int(binary.BigEndian.Uint16(hdr[0:]))
	y := int(binary.BigEndian.Uint16(hdr[2:]))
	w := int(binary.BigEndian.Uint16(hdr[4:]))
	h := int(binary.BigEndian.Uint16(hdr[6:]))
	enc := int32(binary.BigEndian.Uint32(hdr[8:]))

	switch enc {
	case encRaw:
		return true, c.decodeRaw(x, y, w, h)
	case encHextile:
		return true, c.decodeHextile(x, y, w, h)
	case encCopyRect:
		var src [4]byte
		if _, err := io.ReadFull(c.br, src[:]); err != nil {
			return false, err
		}
		sx := int(binary.BigEndian.Uint16(src[0:]))
		sy := int(binary.BigEndian.Uint16(src[2:]))
		return true, c.copyRect(x, y, w, h, sx, sy)
	case encDesktopSize:
		// 分辨率变化（TightVNC 会先单独发一条这个，再发真正的画面）：
		// 重建画布，并让下一帧重新整屏
		c.mu.Lock()
		c.width, c.height = w, h
		c.fb = make([]byte, w*h*4)
		c.dirty = nil
		c.pending = false
		c.gotFull = false
		c.mu.Unlock()
		return false, nil
	default:
		return false, fmt.Errorf("不支持的编码 %d（只实现了 Raw/Hextile/CopyRect）", enc)
	}
}

func (c *Client) decodeRaw(x, y, w, h int) error {
	buf := make([]byte, w*h*4)
	if _, err := io.ReadFull(c.br, buf); err != nil {
		return err
	}
	c.blit(x, y, w, h, buf)
	return nil
}

// decodeHextile Hextile 编码：16x16 瓦片，逐块解码。
func (c *Client) decodeHextile(x0, y0, w, h int) error {
	cw, ch := c.Size()
	tile := make([]byte, 16*16*4)
	var bg, fg [4]byte

	for ty := 0; ty < h; ty += 16 {
		for tx := 0; tx < w; tx += 16 {
			sub, err := c.br.ReadByte()
			if err != nil {
				return err
			}
			tw := min(16, w-tx)
			th := min(16, h-ty)

			if sub&0x01 != 0 { // Raw 瓦片
				buf := make([]byte, tw*th*4)
				if _, err := io.ReadFull(c.br, buf); err != nil {
					return err
				}
				c.blit(x0+tx, y0+ty, tw, th, buf)
				continue
			}

			if sub&0x02 != 0 { // 指定背景色
				if _, err := io.ReadFull(c.br, bg[:]); err != nil {
					return err
				}
			}
			if sub&0x04 != 0 { // 指定前景色
				if _, err := io.ReadFull(c.br, fg[:]); err != nil {
					return err
				}
			}

			// 先用背景色铺满瓦片
			for i := 0; i < tw*th; i++ {
				copy(tile[i*4:], bg[:])
			}

			if sub&0x08 != 0 { // 有子矩形
				nByte, err := c.br.ReadByte()
				if err != nil {
					return err
				}
				n := int(nByte)
				for k := 0; k < n; k++ {
					col := fg
					if sub&0x10 != 0 { // 每个子矩形自带颜色
						if _, err := io.ReadFull(c.br, col[:]); err != nil {
							return err
						}
					}
					xy, err := c.br.ReadByte()
					if err != nil {
						return err
					}
					wh, err := c.br.ReadByte()
					if err != nil {
						return err
					}
					sx := int(xy >> 4)
					sy := int(xy & 0x0F)
					sw := int(wh>>4) + 1
					sh := int(wh&0x0F) + 1
					for row := 0; row < sh && sy+row < th; row++ {
						for colIdx := 0; colIdx < sw && sx+colIdx < tw; colIdx++ {
							off := ((sy+row)*tw + sx + colIdx) * 4
							copy(tile[off:], col[:])
						}
					}
				}
			}

			// 只把瓦片实际覆盖的部分贴回去（边缘可能不满 16）
			full := make([]byte, tw*th*4)
			copy(full, tile[:tw*th*4])
			c.blit(x0+tx, y0+ty, tw, th, full)
		}
	}
	_ = cw
	_ = ch
	return nil
}

func (c *Client) copyRect(x, y, w, h, sx, sy int) error {
	c.mu.Lock()
	fw, fh := c.width, c.height
	if (x+w) > fw || (y+h) > fh || (sx+w) > fw || (sy+h) > fh {
		c.mu.Unlock()
		return errors.New("CopyRect 越界")
	}
	buf := make([]byte, w*h*4)
	for row := 0; row < h; row++ {
		srcOff := ((sy+row)*fw + sx) * 4
		dstOff := row * w * 4
		copy(buf[dstOff:dstOff+w*4], c.fb[srcOff:srcOff+w*4])
	}
	// 原地拷贝：先写回画布再登记脏区
	for row := 0; row < h; row++ {
		dst := ((y+row)*fw + x) * 4
		copy(c.fb[dst:dst+w*4], buf[row*w*4:(row+1)*w*4])
	}
	c.mu.Unlock()
	c.blit(x, y, w, h, buf)
	return nil
}

// blit 把一块 RGBA 写进画布并登记脏矩形。
func (c *Client) blit(x, y, w, h int, pixels []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fw := c.width
	if fw == 0 || w <= 0 || h <= 0 {
		return
	}
	if x+w > fw {
		w = fw - x
	}
	if y+h > c.height {
		h = c.height - y
	}
	if w <= 0 || h <= 0 {
		return
	}
	for row := 0; row < h; row++ {
		dst := ((y+row)*fw + x) * 4
		copy(c.fb[dst:dst+w*4], pixels[row*w*4:(row+1)*w*4])
	}
	// 顺手把 alpha 补成 255（RFB 的第 4 字节是填充）
	for i := 0; i < len(pixels); i += 4 {
		if i+3 < len(pixels) {
			pixels[i+3] = 255
		}
	}
	cp := make([]byte, len(pixels))
	copy(cp, pixels)
	c.dirty = append(c.dirty, Rect{X: x, Y: y, W: w, H: h, Pixels: cp})
}

// ---------------------------------------------------------------- 输入

// PointerEvent 鼠标事件。
func (c *Client) PointerEvent(mask, x, y int) error {
	buf := make([]byte, 6)
	buf[0] = 5
	buf[1] = byte(mask)
	binary.BigEndian.PutUint16(buf[2:], uint16(clamp(x, 0, 65535)))
	binary.BigEndian.PutUint16(buf[4:], uint16(clamp(y, 0, 65535)))
	return c.write(buf)
}

// KeyEvent 键盘事件（keysym 用 X11 编码）。
func (c *Client) KeyEvent(down bool, keysym uint32) error {
	buf := make([]byte, 8)
	buf[0] = 4
	if down {
		buf[1] = 1
	}
	binary.BigEndian.PutUint32(buf[4:], keysym)
	return c.write(buf)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
