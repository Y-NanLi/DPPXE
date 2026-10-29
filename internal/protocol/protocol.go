// Package protocol 控制通道协议（TCP 6003）。
//
// 与 Python 版逐字节兼容：固定 296 字节头 + len 字节负载。
//
//	0      2      4      6    10     14      18      22            54         278   296
//	+------+------+------+----+------+-------+-------+--------------+----------+----+
//	| 'HX' | op   |flags |len | seq  |client | 保留  |  args[8] u32 | text 224B|pad |
//	+------+------+------+----+------+-------+-------+--------------+----------+----+
//
// 收包先读满 296 字节，再按 len 读负载 —— 天然解决粘包。
package protocol

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"unicode/utf16"
)

const (
	Magic      = "HX"
	HeaderSize = 296
	TextBytes  = 224
	TextChars  = TextBytes / 2
	// MaxPayload 单条报文负载上限（防止畸形长度把内存打爆）。
	MaxPayload = 64 * 1024 * 1024

	offMagic  = 0
	offOp     = 2
	offFlags  = 4
	offLen    = 6
	offSeq    = 10
	offClient = 14
	offArgs   = 22
	offText   = 54
)

// ---------------------------------------------------------------- 操作码

const (
	OpHello     uint16 = 0x0001
	OpHelloAck  uint16 = 0x0002
	OpHeartbeat uint16 = 0x0003
	OpAck       uint16 = 0x0004
	OpLog       uint16 = 0x0005
	OpBye       uint16 = 0x0006

	OpShell     uint16 = 0x0101 // 不等待执行
	OpShellWait uint16 = 0x0102 // 等待执行完成

	OpFileSeed  uint16 = 0x0201
	OpFileReq   uint16 = 0x0202
	OpFileChunk uint16 = 0x0203
	OpFileDone  uint16 = 0x0204

	OpPartition  uint16 = 0x0301
	OpInstall    uint16 = 0x0302
	OpBackup     uint16 = 0x0303
	OpStream     uint16 = 0x0304
	OpFixBoot    uint16 = 0x0305
	OpRebuildMBR uint16 = 0x0306
	OpInjectDrv  uint16 = 0x0307

	OpScript      uint16 = 0x0401
	OpScriptAbort uint16 = 0x0402

	OpInjectClient  uint16 = 0x0501
	OpInjectVNC     uint16 = 0x0502
	OpInjectPEDrv   uint16 = 0x0503
	OpSetBoot       uint16 = 0x0504
	OpRename        uint16 = 0x0505
	OpPower         uint16 = 0x0506
	OpRestartClient uint16 = 0x0507

	OpDiskInfo   uint16 = 0x0601
	OpQueryDisks uint16 = 0x0602
)

// ---------------------------------------------------------------- 标志位

const (
	FlagReply      uint16 = 0x0001
	FlagUrgent     uint16 = 0x0002
	FlagCompressed uint16 = 0x0004 // payload 为 deflate(zlib) 压缩
)

// OpNames 操作码 → 名字，用于日志。
var OpNames = map[uint16]string{
	OpHello: "HELLO", OpHelloAck: "HELLO_ACK", OpHeartbeat: "HEARTBEAT",
	OpAck: "ACK", OpLog: "LOG", OpBye: "BYE",
	OpShell: "SHELL", OpShellWait: "SHELLWAIT",
	OpFileSeed: "FILE_SEED", OpFileReq: "FILE_REQ", OpFileChunk: "FILE_CHUNK",
	OpFileDone:  "FILE_DONE",
	OpPartition: "PARTITION", OpInstall: "INSTALL", OpBackup: "BACKUP",
	OpStream: "STREAM", OpFixBoot: "FIXBOOT", OpRebuildMBR: "REBUILD_MBR",
	OpInjectDrv: "INJECTDRV",
	OpScript:    "SCRIPT", OpScriptAbort: "SCRIPT_ABORT",
	OpInjectClient: "INJECT_CLIENT", OpInjectVNC: "INJECT_VNC",
	OpInjectPEDrv: "INJECT_PEDRV", OpSetBoot: "SETBOOT", OpRename: "RENAME",
	OpPower: "POWER", OpRestartClient: "RESTART_CLIENT",
	OpDiskInfo: "DISK_INFO", OpQueryDisks: "QUERY_DISKS",
}

// OpName 返回操作码名字（未知则 0xNNNN）。
func OpName(op uint16) string {
	if n, ok := OpNames[op]; ok {
		return n
	}
	return fmt.Sprintf("0x%04X", op)
}

// ---------------------------------------------------------------- Message

// Message 一条协议报文。
type Message struct {
	Op       uint16
	Flags    uint16
	Seq      uint32
	ClientID uint32
	Args     [8]uint32
	Text     string
	Payload  []byte
}

// New 构造一条只带操作码的报文。
func New(op uint16) *Message { return &Message{Op: op} }

// WithArgs 设置数值参数（从第 0 位开始，多余的忽略）。
func (m *Message) WithArgs(args ...uint32) *Message {
	for i, a := range args {
		if i >= len(m.Args) {
			break
		}
		m.Args[i] = a
	}
	return m
}

// OpName 报文操作码名字。
func (m *Message) OpName() string { return OpName(m.Op) }

// Pack 序列化为字节流（头部 + 负载）。
func (m *Message) Pack() []byte {
	buf := make([]byte, HeaderSize+len(m.Payload))
	copy(buf[offMagic:], Magic)
	binary.LittleEndian.PutUint16(buf[offOp:], m.Op)
	binary.LittleEndian.PutUint16(buf[offFlags:], m.Flags)
	binary.LittleEndian.PutUint32(buf[offLen:], uint32(len(m.Payload)))
	binary.LittleEndian.PutUint32(buf[offSeq:], m.Seq)
	binary.LittleEndian.PutUint32(buf[offClient:], m.ClientID)
	for i, a := range m.Args {
		binary.LittleEndian.PutUint32(buf[offArgs+i*4:], a)
	}
	copy(buf[offText:], encodeText(m.Text))
	copy(buf[HeaderSize:], m.Payload)
	return buf
}

// encodeText 把文本编码成 224 字节的 UTF-16LE 定长字段（截断到 222 字节，留结束符）。
func encodeText(s string) []byte {
	out := make([]byte, TextBytes)
	units := utf16.Encode([]rune(s))
	n := 0
	for _, u := range units {
		if n+2 > TextBytes-2 {
			break
		}
		binary.LittleEndian.PutUint16(out[n:], u)
		n += 2
	}
	return out
}

// decodeText 从定长字段里取出文本（遇到 0 截断）。
func decodeText(raw []byte) string {
	units := make([]uint16, 0, TextChars)
	for i := 0; i+1 < len(raw); i += 2 {
		u := binary.LittleEndian.Uint16(raw[i:])
		if u == 0 {
			break
		}
		units = append(units, u)
	}
	return string(utf16.Decode(units))
}

// Unpack 从头部 + 负载还原报文。
func Unpack(header, payload []byte) (*Message, error) {
	if len(header) < HeaderSize {
		return nil, errors.New("header too short")
	}
	if string(header[offMagic:offMagic+2]) != Magic {
		return nil, errors.New("bad magic")
	}
	m := &Message{
		Op:       binary.LittleEndian.Uint16(header[offOp:]),
		Flags:    binary.LittleEndian.Uint16(header[offFlags:]),
		Seq:      binary.LittleEndian.Uint32(header[offSeq:]),
		ClientID: binary.LittleEndian.Uint32(header[offClient:]),
		Text:     decodeText(header[offText : offText+TextBytes]),
		Payload:  payload,
	}
	for i := 0; i < 8; i++ {
		m.Args[i] = binary.LittleEndian.Uint32(header[offArgs+i*4:])
	}
	return m, nil
}

// ---------------------------------------------------------------- Conn

// Conn 一条 TCP 连接上的收发封装（写操作加锁，读只允许一个 goroutine）。
type Conn struct {
	conn net.Conn
	br   *bufio.Reader

	writeMu sync.Mutex
	closeMu sync.Mutex
	closed  bool
}

// NewConn 包装一条已建立的连接。
func NewConn(c net.Conn) *Conn {
	return &Conn{conn: c, br: bufio.NewReaderSize(c, 64*1024)}
}

// PeerIP 对端 IP。
func (c *Conn) PeerIP() string {
	if c.conn == nil || c.conn.RemoteAddr() == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(c.conn.RemoteAddr().String())
	if err != nil {
		return c.conn.RemoteAddr().String()
	}
	return host
}

// Recv 读取一条报文（先读满头部，再按 len 读负载）。
func (c *Conn) Recv() (*Message, error) {
	head := make([]byte, HeaderSize)
	if _, err := io.ReadFull(c.br, head); err != nil {
		return nil, err
	}
	if string(head[offMagic:offMagic+2]) != Magic {
		return nil, errors.New("bad magic")
	}
	length := binary.LittleEndian.Uint32(head[offLen:])
	if length > MaxPayload {
		return nil, fmt.Errorf("payload too large: %d", length)
	}
	var payload []byte
	if length > 0 {
		payload = make([]byte, length)
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return nil, err
		}
	}
	return Unpack(head, payload)
}

// Send 发送一条报文。
func (c *Conn) Send(m *Message) error {
	data := m.Pack()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return errors.New("connection closed")
	}
	_, err := c.conn.Write(data)
	return err
}

// Close 关闭连接（可重复调用）。
func (c *Conn) Close() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

// Closed 是否已关闭。
func (c *Conn) Closed() bool {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	return c.closed
}

// ---------------------------------------------------------------- 工具

// Compress zlib 压缩（与 Python zlib.compress 兼容）。
func Compress(data []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Bytes()
}

// Decompress zlib 解压。
func Decompress(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// Describe 给日志用的单行摘要。
func Describe(m *Message) string {
	bits := []string{m.OpName()}
	if m.Text != "" {
		t := strings.NewReplacer("\r", " ", "\n", " ").Replace(m.Text)
		if len([]rune(t)) > 60 {
			t = string([]rune(t)[:57]) + "..."
		}
		bits = append(bits, t)
	}
	if len(m.Payload) > 0 {
		bits = append(bits, fmt.Sprintf("%dB", len(m.Payload)))
	}
	return strings.Join(bits, " ")
}
