package services

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"dppxe/internal/config"
	"dppxe/internal/protocol"
)

// ChunkSize 文件分片大小。
const ChunkSize = 256 * 1024

// DiskInfo 被控端上报的物理磁盘。
type DiskInfo struct {
	Index     int    `json:"index"`
	Model     string `json:"model"`
	SizeBytes int64  `json:"size_bytes"`
	SizeGB    int    `json:"size_gb"`
	Style     string `json:"style"`
}

// ClientSession 一条客户端会话。
type ClientSession struct {
	CID         uint32
	Conn        *protocol.Conn
	IP          string
	MAC         string
	Hostname    string
	Arch        string
	Mode        string // PE / SYS
	Version     string
	LastSeen    time.Time
	ConnectedAt time.Time
	Selected    bool

	Received int
	Total    int
	Disks    []DiskInfo

	mu sync.Mutex
}

// State 会话状态文本。
func (s *ClientSession) State() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Total > 0 && s.Received >= s.Total {
		return "完成"
	}
	if s.Total > 0 {
		return fmt.Sprintf("传输 %d%%", s.Received*100/maxInt(s.Total, 1))
	}
	return "在线"
}

// Progress 传输进度（0-100）。
func (s *ClientSession) Progress() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Total <= 0 {
		return 0
	}
	return s.Received * 100 / s.Total
}

// Send 向该会话发一条报文。
func (s *ClientSession) Send(m *protocol.Message) bool {
	m.ClientID = s.CID
	return s.Conn.Send(m) == nil
}

// DisksCopy 返回上报磁盘的副本。
func (s *ClientSession) DisksCopy() []DiskInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DiskInfo{}, s.Disks...)
}

// SetSelected 设置"选中"状态（受"仅仅选择中客户端接收指令"影响）。
func (s *ClientSession) SetSelected(v bool) { s.Selected = v }

// FileJob 一次文件推送任务。
type FileJob struct {
	JobID     uint32
	Path      string
	TargetDir string
	Chunk     int
	Size      int64
	Total     int
}

// ReadChunk 读第 index 片。
func (j *FileJob) ReadChunk(index int) ([]byte, error) {
	fh, err := os.Open(j.Path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	buf := make([]byte, j.Chunk)
	n, err := fh.ReadAt(buf, int64(index)*int64(j.Chunk))
	if err != nil && n == 0 {
		return nil, err
	}
	return buf[:n], nil
}

// ControlServer 控制/文件服务（TCP 6003）—— 服务端侧。
//
// 会话流程：
//
//	客户端 connect -> HELLO(arch, mode, version, mac, hostname)
//	服务端 -> HELLO_ACK(client_id, server_ip)
//	之后双向：指令下发 / 日志回传 / 心跳 / 文件分片请求
//
// 文件推送用"拉模式"：服务端只发 FILE_SEED（文件名、大小、分片大小、总片数），
// 客户端按需 FILE_REQ 第 N 片，服务端回 FILE_CHUNK。
type ControlServer struct {
	cfg     *config.Config
	log     LogFunc
	onDirty func()
	onLog   func(cid uint32, text string)

	ln   net.Listener
	stop chan struct{}
	wg   sync.WaitGroup

	mu       sync.RWMutex
	sessions map[uint32]*ClientSession
	jobs     map[uint32]*FileJob
	macIDs   map[string]uint32

	nextID  atomic.Uint32
	nextJob atomic.Uint32
	seq     atomic.Uint32

	// 本批文件推送的分片进度（已发 / 总数），供界面进度条用。
	pushTotal atomic.Int64
	pushSent  atomic.Int64

	// IdleTimeout 超过这个时间没收到任何报文就判离线。
	IdleTimeout time.Duration
}

// NewControlServer 创建控制服务。
func NewControlServer(cfg *config.Config, log LogFunc, onDirty func(), onLog func(cid uint32, text string)) *ControlServer {
	return &ControlServer{
		cfg: cfg, log: log, onDirty: onDirty, onLog: onLog,
		sessions:    map[uint32]*ClientSession{},
		jobs:        map[uint32]*FileJob{},
		macIDs:      map[string]uint32{},
		IdleTimeout: 45 * time.Second,
	}
}

// Start 监听端口。
func (c *ControlServer) Start() error {
	ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", c.cfg.ControlPort))
	if err != nil {
		return fmt.Errorf("绑定 TCP %d 失败: %w", c.cfg.ControlPort, err)
	}
	c.ln = ln
	c.stop = make(chan struct{})
	c.wg.Add(2)
	go c.acceptLoop()
	go c.sweepLoop()
	c.log(fmt.Sprintf("文件服务已启动（TCP %d）", c.cfg.ControlPort), "ok")
	return nil
}

// Stop 停止服务并断开所有客户端。
func (c *ControlServer) Stop() {
	if c.stop != nil {
		select {
		case <-c.stop:
		default:
			close(c.stop)
		}
	}
	if c.ln != nil {
		_ = c.ln.Close()
		c.ln = nil
	}
	c.mu.Lock()
	sessions := make([]*ClientSession, 0, len(c.sessions))
	for _, s := range c.sessions {
		sessions = append(sessions, s)
	}
	c.sessions = map[uint32]*ClientSession{}
	c.mu.Unlock()
	for _, s := range sessions {
		bye := protocol.New(protocol.OpBye)
		bye.Text = "server shutdown"
		_ = s.Conn.Send(bye)
		_ = s.Conn.Close()
	}
	c.wg.Wait()
	c.log("文件服务已停止", "warn")
}

func (c *ControlServer) acceptLoop() {
	defer c.wg.Done()
	for {
		conn, err := c.ln.Accept()
		if err != nil {
			select {
			case <-c.stop:
				return
			default:
			}
			return
		}
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.sessionLoop(conn)
		}()
	}
}

// sweepLoop 心跳超时的连接要主动关掉，否则拔网线/断电时 socket 会一直挂着。
func (c *ControlServer) sweepLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
		}
		now := time.Now()
		c.mu.RLock()
		var dead []*ClientSession
		for _, s := range c.sessions {
			if now.Sub(s.LastSeen) > c.IdleTimeout {
				dead = append(dead, s)
			}
		}
		c.mu.RUnlock()
		for _, s := range dead {
			c.log(fmt.Sprintf("客户端 #%d %s 心跳超时（%s），断开",
				s.CID, s.Hostname, c.IdleTimeout), "warn")
			_ = s.Conn.Close()
		}
	}
}

func (c *ControlServer) sessionLoop(raw net.Conn) {
	conn := protocol.NewConn(raw)
	ses := &ClientSession{Conn: conn, IP: conn.PeerIP(), LastSeen: time.Now(), ConnectedAt: time.Now()}

	_ = raw.SetReadDeadline(time.Now().Add(30 * time.Second))
	hello, err := conn.Recv()
	if err != nil || hello.Op != protocol.OpHello {
		_ = conn.Close()
		return
	}
	_ = raw.SetReadDeadline(time.Time{})

	ses.Arch = "x64"
	if hello.Args[0] == 0 {
		ses.Arch = "x86"
	}
	ses.Mode = "PE"
	if hello.Args[1] == 1 {
		ses.Mode = "SYS"
	}
	ses.Version = fmt.Sprintf("%d.%d", hello.Args[2]>>16, hello.Args[2]&0xFFFF)
	mac48 := uint64(hello.Args[3]&0xFFFFFFFF) | (uint64(hello.Args[4]&0xFFFF) << 32)
	ses.MAC = formatMAC48(mac48)

	// 同一台机器重连时复用原编号，机器号不会一直往上涨
	c.mu.Lock()
	cid, ok := c.macIDs[ses.MAC]
	if !ok {
		cid = c.nextID.Add(1)
		c.macIDs[ses.MAC] = cid
	}
	ses.CID = cid
	if hello.Text != "" {
		ses.Hostname = hello.Text
	} else {
		ses.Hostname = fmt.Sprintf("CLIENT-%03d", cid)
	}
	c.sessions[cid] = ses
	c.mu.Unlock()

	// args: [编号, 主版本, 次版本, 修订, 端口, 0,0,0]；text = 服务端 IP
	ack := protocol.New(protocol.OpHelloAck)
	ack.ClientID = cid
	ack.Seq = c.nextSeq()
	ack.WithArgs(cid, 1, 0, 0, uint32(c.cfg.ControlPort), 0, 0, 0)
	ack.Text = c.cfg.ServerIP
	_ = conn.Send(ack)

	c.log(fmt.Sprintf("客户端接入 #%d %s %s [%s/%s]", cid, ses.Hostname, ses.IP,
		ses.Arch, ses.Mode), "ok")
	c.notify()

	for {
		select {
		case <-c.stop:
			_ = conn.Close()
			return
		default:
		}
		msg, err := conn.Recv()
		if err != nil {
			break
		}
		ses.LastSeen = time.Now()
		c.dispatch(ses, msg)
	}

	c.mu.Lock()
	if cur, ok := c.sessions[cid]; ok && cur == ses {
		delete(c.sessions, cid)
	}
	c.mu.Unlock()
	_ = conn.Close()
	c.log(fmt.Sprintf("客户端断开 #%d %s (%s)", cid, ses.Hostname, ses.IP), "warn")
	c.notify()
}

func (c *ControlServer) dispatch(ses *ClientSession, msg *protocol.Message) {
	switch msg.Op {
	case protocol.OpHeartbeat:
		ack := protocol.New(protocol.OpAck)
		ack.ClientID = ses.CID
		ack.Seq = msg.Seq
		_ = ses.Conn.Send(ack)
	case protocol.OpLog:
		if c.onLog != nil {
			c.onLog(ses.CID, msg.Text)
		}
	case protocol.OpFileReq:
		jobID := msg.Args[0]
		idx := int(msg.Args[1])
		c.mu.RLock()
		job := c.jobs[jobID]
		c.mu.RUnlock()
		if job == nil {
			return
		}
		data, err := job.ReadChunk(idx)
		if err != nil {
			c.log("读取分片失败: "+err.Error(), "err")
			return
		}
		chunk := protocol.New(protocol.OpFileChunk)
		chunk.ClientID = ses.CID
		chunk.Seq = uint32(idx)
		chunk.WithArgs(job.JobID, uint32(idx), uint32(job.Total))
		chunk.Payload = data
		_ = ses.Conn.Send(chunk)
		ses.mu.Lock()
		ses.Received = idx + 1
		ses.Total = job.Total
		ses.mu.Unlock()
		c.pushSent.Add(1)
		if idx+1 >= job.Total {
			c.notify()
		}
	case protocol.OpFileDone:
		c.log(fmt.Sprintf("#%d 文件接收完成: %s", ses.CID, msg.Text), "ok")
		c.notify()
	case protocol.OpDiskInfo:
		var disks []DiskInfo
		if err := json.Unmarshal(msg.Payload, &disks); err != nil {
			disks = nil
		}
		ses.mu.Lock()
		ses.Disks = disks
		ses.mu.Unlock()
		var brief []string
		for _, d := range disks {
			model := d.Model
			if model == "" {
				model = "磁盘"
			}
			brief = append(brief, fmt.Sprintf("HD%d %s %dGB", d.Index, model, d.SizeGB))
		}
		if len(brief) == 0 {
			brief = []string{"无"}
		}
		c.log(fmt.Sprintf("#%d 磁盘：%s", ses.CID, joinCN(brief)), "info")
		c.notify()
	}
}

// ---------------------------------------------------------------- 下发

// Targets 本次广播的目标会话（受"仅仅选择中客户端接收指令"影响）。
func (c *ControlServer) Targets() []*ClientSession {
	c.mu.RLock()
	sessions := make([]*ClientSession, 0, len(c.sessions))
	for _, s := range c.sessions {
		sessions = append(sessions, s)
	}
	c.mu.RUnlock()
	if c.cfg.OnlySelected {
		var picked []*ClientSession
		for _, s := range sessions {
			if s.Selected {
				picked = append(picked, s)
			}
		}
		return picked
	}
	return sessions
}

// Broadcast 向目标会话广播一条指令，返回成功台数。
func (c *ControlServer) Broadcast(msg *protocol.Message) int {
	n := 0
	for _, ses := range c.Targets() {
		m := *msg
		m.ClientID = ses.CID
		m.Seq = c.nextSeq()
		if ses.Conn.Send(&m) == nil {
			n++
		}
	}
	return n
}

// SendTo 发给指定编号的客户端。
func (c *ControlServer) SendTo(cid uint32, msg *protocol.Message) bool {
	c.mu.RLock()
	ses := c.sessions[cid]
	c.mu.RUnlock()
	if ses == nil {
		return false
	}
	m := *msg
	m.ClientID = cid
	m.Seq = c.nextSeq()
	return ses.Conn.Send(&m) == nil
}

// QueryDisks 要求客户端重新上报磁盘。
func (c *ControlServer) QueryDisks(cid uint32) bool {
	return c.SendTo(cid, protocol.New(protocol.OpQueryDisks))
}

// Disconnect 断开某个客户端。
func (c *ControlServer) Disconnect(cid uint32) bool {
	c.mu.Lock()
	ses := c.sessions[cid]
	delete(c.sessions, cid)
	c.mu.Unlock()
	if ses == nil {
		return false
	}
	_ = ses.Conn.Close()
	c.log(fmt.Sprintf("已断开客户端 #%d %s", cid, ses.Hostname), "warn")
	c.notify()
	return true
}

// Renumber 把某个客户端改成指定编号（用于人工对齐机器号）。
func (c *ControlServer) Renumber(oldCID, newCID uint32) bool {
	if newCID == 0 || newCID == oldCID {
		return false
	}
	c.mu.Lock()
	ses := c.sessions[oldCID]
	if ses == nil {
		c.mu.Unlock()
		return false
	}
	if _, exists := c.sessions[newCID]; exists {
		c.mu.Unlock()
		return false
	}
	delete(c.sessions, oldCID)
	ses.CID = newCID
	c.sessions[newCID] = ses
	c.macIDs[ses.MAC] = newCID
	c.mu.Unlock()
	c.log(fmt.Sprintf("客户端 #%d 编号改为 #%d", oldCID, newCID), "info")
	c.notify()
	return true
}

// PushFile 向所有目标客户端推送一个文件（拉模式：只发种子）。
func (c *ControlServer) PushFile(path, targetDir string) error {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return fmt.Errorf("文件不存在: %s", path)
	}
	total := int((st.Size() + ChunkSize - 1) / ChunkSize)
	if total < 1 {
		total = 1
	}
	job := &FileJob{
		JobID: c.nextJob.Add(1), Path: path, TargetDir: targetDir,
		Chunk: ChunkSize, Size: st.Size(), Total: total,
	}
	c.mu.Lock()
	c.jobs[job.JobID] = job
	c.mu.Unlock()

	name := filepath.Base(path)
	c.log(fmt.Sprintf("推送文件 %s (%d B, %d 片) -> %s", name, job.Size, job.Total, targetDir), "info")
	for _, ses := range c.Targets() {
		ses.mu.Lock()
		ses.Received = 0
		ses.Total = job.Total
		ses.mu.Unlock()
		seed := protocol.New(protocol.OpFileSeed)
		seed.WithArgs(job.JobID, uint32(job.Size), uint32(job.Chunk), uint32(job.Total))
		seed.Text = joinWin(targetDir, name)
		m := *seed
		m.ClientID = ses.CID
		m.Seq = c.nextSeq()
		_ = ses.Conn.Send(&m)
	}
	c.notify()
	return nil
}

// BeginPush 开始一批文件推送，重置进度计数（totalChunks = 本批所有文件的总分片数）。
func (c *ControlServer) BeginPush(totalChunks int64) {
	c.pushTotal.Store(totalChunks)
	c.pushSent.Store(0)
}

// PushProgress 返回（已发分片, 总片数）。
func (c *ControlServer) PushProgress() (int64, int64) {
	return c.pushSent.Load(), c.pushTotal.Load()
}

// Snapshot 线程安全快照，供 UI 遍历。
func (c *ControlServer) Snapshot() []*ClientSession {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*ClientSession, 0, len(c.sessions))
	for _, s := range c.sessions {
		out = append(out, s)
	}
	return out
}

// Count 在线会话数。
func (c *ControlServer) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.sessions)
}

// Get 取某个会话。
func (c *ControlServer) Get(cid uint32) *ClientSession {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessions[cid]
}

// ClearJobs 清空传输任务缓存。
func (c *ControlServer) ClearJobs() {
	c.mu.Lock()
	c.jobs = map[uint32]*FileJob{}
	c.mu.Unlock()
}

func (c *ControlServer) notify() {
	if c.onDirty != nil {
		c.onDirty()
	}
}

func (c *ControlServer) nextSeq() uint32 { return c.seq.Add(1) }

// ---------------------------------------------------------------- 工具

func formatMAC48(v uint64) string {
	b := make([]byte, 6)
	for i := 0; i < 6; i++ {
		b[5-i] = byte((v >> (8 * uint(i))) & 0xFF)
	}
	return formatMAC(b)
}

func joinCN(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "，"
		}
		out += p
	}
	return out
}

func joinWin(dir, name string) string {
	d := dir
	for len(d) > 0 && (d[len(d)-1] == '\\' || d[len(d)-1] == '/') {
		d = d[:len(d)-1]
	}
	return d + "\\" + name
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
