package vnc

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// mockConn 只收集写入字节，用来校验 PointerEvent / KeyEvent 的编码。
type mockConn struct {
	net.Conn
	buf bytes.Buffer
}

func (m *mockConn) Write(b []byte) (int, error) { return m.buf.Write(b) }
func (m *mockConn) Close() error                { return nil }
func (m *mockConn) SetDeadline(time.Time) error { return nil }

func TestPointerAndKeyEncoding(t *testing.T) {
	mc := &mockConn{}
	c := &Client{conn: mc}

	if err := c.PointerEvent(ButtonLeft|ButtonRight, 300, 200); err != nil {
		t.Fatal(err)
	}
	// 类型 5 | 掩码 | x(2,BE) | y(2,BE)
	want := []byte{5, ButtonLeft | ButtonRight, 0x01, 0x2C, 0x00, 0xC8}
	if got := mc.buf.Bytes(); !bytes.Equal(got, want) {
		t.Errorf("PointerEvent 编码不对\n got % x\nwant % x", got, want)
	}

	mc.buf.Reset()
	if err := c.KeyEvent(true, 0xFF0D); err != nil { // Enter
		t.Fatal(err)
	}
	wantKey := []byte{4, 1, 0, 0, 0x00, 0x00, 0xFF, 0x0D}
	if got := mc.buf.Bytes(); !bytes.Equal(got, wantKey) {
		t.Errorf("KeyEvent 编码不对\n got % x\nwant % x", got, wantKey)
	}

	mc.buf.Reset()
	_ = c.KeyEvent(false, 0x61) // 'a'
	if got := mc.buf.Bytes(); !bytes.Equal(got, []byte{4, 0, 0, 0, 0, 0, 0, 0x61}) {
		t.Errorf("KeyEvent(keyup) 编码不对: % x", got)
	}
}

func TestRequestUpdateFirstIsFull(t *testing.T) {
	mc := &mockConn{}
	c := &Client{conn: mc, width: 100, height: 50, fb: make([]byte, 100*50*4)}

	if err := c.RequestUpdate(); err != nil {
		t.Fatal(err)
	}
	got := mc.buf.Bytes()
	if len(got) != 10 || got[0] != 3 {
		t.Fatalf("请求格式不对: % x", got)
	}
	if got[1] != 0 {
		t.Error("第一次请求必须是整屏（incremental=0）")
	}
	// 已发出未回包时，不应重复发请求
	mc.buf.Reset()
	_ = c.RequestUpdate()
	if mc.buf.Len() != 0 {
		t.Error("pending 期间不应重复请求")
	}
}
