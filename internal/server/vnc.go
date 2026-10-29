package server

import (
	"fmt"
	"time"

	"dppxe/internal/vnc"
)

// VncSession 一条内置 VNC 会话（控制端窗口里直接看被控端画面）。
type VncSession struct {
	Client  *vnc.Client
	IP      string
	Port    int
	Started time.Time
	Seq     uint64
}

// VncStatus 会话状态（给界面轮询）。
type VncStatus struct {
	Connected bool   `json:"connected"`
	IP        string `json:"ip"`
	Port      int    `json:"port"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Name      string `json:"name"`
	Error     string `json:"error"`
}

// VncConnect 连接被控端的 VNC 服务（默认 5900）。
//
// 成功后会起一个读循环持续解码画面；界面通过 VncFrame 取脏矩形。
func (s *Server) VncConnect(ip string, port int, password string) error {
	if ip == "" || ip == "-" {
		return fmt.Errorf("没有可连接的 IP")
	}
	if port == 0 {
		port = 5900
	}

	s.vncMu.Lock()
	old := s.vnc
	s.vnc = nil
	s.vncMu.Unlock()
	if old != nil {
		_ = old.Client.Close()
	}

	c, err := vnc.Dial(vnc.Config{Host: ip, Port: port, Password: password,
		Timeout: 8 * time.Second})
	if err != nil {
		s.Log(fmt.Sprintf("连接 VNC %s:%d 失败：%v", ip, port, err), "err")
		return err
	}
	w, h := c.Size()
	sess := &VncSession{Client: c, IP: ip, Port: port, Started: time.Now()}
	s.vncMu.Lock()
	s.vnc = sess
	s.vncMu.Unlock()

	go func() {
		c.Run()
		if err := c.Err(); err != nil {
			s.Log(fmt.Sprintf("VNC %s 已断开：%v", ip, err), "warn")
		}
		s.vncMu.Lock()
		if s.vnc == sess {
			s.vnc = nil
		}
		s.vncMu.Unlock()
	}()

	if password != "" {
		s.cfg.VncPassword = password
		_ = s.cfg.Save()
	}
	s.Log(fmt.Sprintf("已连接 VNC %s:%d（%dx%d %s）", ip, port, w, h, c.Name()), "ok")
	return nil
}

// VncStatus 当前会话状态。
func (s *Server) VncStatus() VncStatus {
	s.vncMu.Lock()
	sess := s.vnc
	s.vncMu.Unlock()
	if sess == nil {
		return VncStatus{Connected: false}
	}
	w, h := sess.Client.Size()
	st := VncStatus{
		Connected: true, IP: sess.IP, Port: sess.Port,
		Width: w, Height: h, Name: sess.Client.Name(),
	}
	if err := sess.Client.Err(); err != nil {
		st.Connected = false
		st.Error = err.Error()
	}
	return st
}

// VncFrame 取当前脏矩形（没有新画面时返回 0）。
func (s *Server) VncFrame() (uint64, []vnc.Rect) {
	s.vncMu.Lock()
	sess := s.vnc
	s.vncMu.Unlock()
	if sess == nil {
		return 0, nil
	}
	rects := sess.Client.TakeDirty()
	// 无论有没有画面都催一帧；pending 标志保证不会堆积请求
	_ = sess.Client.RequestUpdate()
	if len(rects) == 0 {
		return 0, nil
	}
	s.vncMu.Lock()
	sess.Seq++
	seq := sess.Seq
	s.vncMu.Unlock()
	return seq, rects
}

// VncPointer 鼠标事件。
func (s *Server) VncPointer(mask, x, y int) error {
	s.vncMu.Lock()
	sess := s.vnc
	s.vncMu.Unlock()
	if sess == nil {
		return fmt.Errorf("VNC 未连接")
	}
	return sess.Client.PointerEvent(mask, x, y)
}

// VncKey 键盘事件（keysym 为 X11 编码）。
func (s *Server) VncKey(down bool, keysym uint32) error {
	s.vncMu.Lock()
	sess := s.vnc
	s.vncMu.Unlock()
	if sess == nil {
		return fmt.Errorf("VNC 未连接")
	}
	return sess.Client.KeyEvent(down, keysym)
}

// VncClose 断开。
func (s *Server) VncClose() {
	s.vncMu.Lock()
	sess := s.vnc
	s.vnc = nil
	s.vncMu.Unlock()
	if sess != nil {
		_ = sess.Client.Close()
		s.Log(fmt.Sprintf("已断开 VNC %s", sess.IP), "warn")
	}
}

// VncActive 是否有活动会话。
func (s *Server) VncActive() bool {
	s.vncMu.Lock()
	defer s.vncMu.Unlock()
	return s.vnc != nil
}
