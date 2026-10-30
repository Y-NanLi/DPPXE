// Package server 服务端聚合层：持有配置、四个网络服务、设备台账与日志，
// 并对外暴露给图形界面调用的一组方法（GUI 通过本地 HTTP API 访问）。
package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dppxe/internal/config"
	"dppxe/internal/protocol"
	"dppxe/internal/services"
)

// MaxLogs 日志环形缓冲长度。
const MaxLogs = 2000

// LogEntry 一条日志。
type LogEntry struct {
	Seq  int64  `json:"seq"`
	TS   string `json:"ts"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Device 设备台账里的一台机器（按 MAC 归档，掉线不消失）。
type Device struct {
	Key      string  `json:"key"`
	MAC      string  `json:"mac"`
	IP       string  `json:"ip"`
	Name     string  `json:"name"`
	CID      string  `json:"cid"`
	Online   bool    `json:"online"`
	LastSeen float64 `json:"last_seen"`
	PXEAt    float64 `json:"pxe_at"`
	Boot     string  `json:"boot"`
	Speed    string  `json:"speed"`
	Root     string  `json:"root"`
}

// Row 客户端表格里的一行。
type Row struct {
	CID      string  `json:"cid"`
	IP       string  `json:"ip"`
	MAC      string  `json:"mac"`
	State    string  `json:"state"`
	Speed    string  `json:"speed"`
	Root     string  `json:"root"`
	Boot     string  `json:"boot"`
	LastSeen float64 `json:"last_seen"`
	Name     string  `json:"name"`
	Progress int     `json:"progress"`
	Online   bool    `json:"online"`
}

// Server 服务端主对象。
type Server struct {
	mu  sync.Mutex
	cfg *config.Config

	dhcp      *services.DhcpServer
	tftp      *services.TftpServer
	http      *services.HttpFileServer
	control   *services.ControlServer
	discovery *services.DiscoveryResponder

	devices map[string]*Device
	logs    []LogEntry
	logSeq  int64

	running   bool
	capturing bool
	svcCount  int

	// 内置 VNC 会话（控制端窗口里直接看被控端画面）
	vncMu sync.Mutex
	vnc   *VncSession

	// LogDir 抓包 / 日志输出目录。
	LogDir string
	// OnLog 每写一条日志回调（无界面模式用来打印到控制台）。
	OnLog func(LogEntry)
}

// New 创建服务端对象。
func New(cfg *config.Config) *Server {
	s := &Server{cfg: cfg, devices: map[string]*Device{}}
	s.LogDir = DefaultLogDir()
	_ = os.MkdirAll(s.LogDir, 0o755)
	return s
}

// DefaultLogDir 日志目录：程序目录下的 logs。
func DefaultLogDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "logs"
	}
	return filepath.Join(filepath.Dir(exe), "logs")
}

// Cfg 返回配置（只读使用；修改请通过 Mutate）。
func (s *Server) Cfg() *config.Config { return s.cfg }

// ---------------------------------------------------------------- 日志

// Log 写一条日志（线程安全，环形缓冲）。
func (s *Server) Log(msg, kind string) {
	if kind == "" {
		kind = "info"
	}
	s.mu.Lock()
	s.logSeq++
	e := LogEntry{Seq: s.logSeq, TS: time.Now().Format("15:04:05.000"), Kind: kind, Text: msg}
	s.logs = append(s.logs, e)
	if len(s.logs) > MaxLogs {
		s.logs = s.logs[len(s.logs)-MaxLogs:]
	}
	hook := s.OnLog
	s.mu.Unlock()
	if hook != nil {
		hook(e)
	}
}

// LogsSince 取 seq 之后的日志。
func (s *Server) LogsSince(seq int64) []LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq >= s.logSeq {
		return nil
	}
	idx := 0
	for idx < len(s.logs) && s.logs[idx].Seq <= seq {
		idx++
	}
	out := make([]LogEntry, len(s.logs)-idx)
	copy(out, s.logs[idx:])
	return out
}

// LogText 全部日志文本（导出用）。
func (s *Server) LogText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, e := range s.logs {
		fmt.Fprintf(&b, "%s %s\n", e.TS, e.Text)
	}
	return b.String()
}

// LastLogSeq 最新日志序号。
func (s *Server) LastLogSeq() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logSeq
}

// ---------------------------------------------------------------- 生命周期

// Running 服务是否在跑。
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Capturing 是否正在抓包。
func (s *Server) Capturing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capturing
}

// SetCapturing 记录抓包状态。
func (s *Server) SetCapturing(v bool) {
	s.mu.Lock()
	s.capturing = v
	s.mu.Unlock()
}

// ServiceCount 成功启动的服务数。
func (s *Server) ServiceCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.svcCount
}

// Start 启动勾选的服务，返回成功项数。
func (s *Server) Start() int {
	if s.Running() {
		return s.ServiceCount()
	}
	s.Log("—— 开始启动服务 ——", "info")
	ok := 0

	if s.cfg.DhcpEnabled {
		s.dhcp = services.NewDhcpServer(s.cfg, s.Log, s.onDhcpClient)
		if err := s.dhcp.Start(); err != nil {
			s.Log("DHCP 启动失败: "+err.Error()+"（需要管理员权限且 67 端口未被占用）", "err")
			s.dhcp = nil
		} else {
			ok++
		}
	}
	if s.cfg.TftpEnabled {
		s.tftp = services.NewTftpServer(s.cfg, s.Log)
		if err := s.tftp.Start(); err != nil {
			s.Log("TFTP 启动失败: "+err.Error()+"（69 端口）", "err")
			s.tftp = nil
		} else {
			ok++
		}
	}
	if s.cfg.HttpEnabled {
		s.http = services.NewHttpFileServer(s.cfg, s.Log, nil)
		if err := s.http.Start(); err != nil {
			s.Log("HTTP 启动失败: "+err.Error()+"（80 端口可能被 IIS/其它程序占用）", "err")
			s.http = nil
		} else {
			ok++
		}
	}
	if s.cfg.FileSvcEnable {
		s.control = services.NewControlServer(s.cfg, s.Log, nil, s.onClientLog)
		if err := s.control.Start(); err != nil {
			s.Log("文件服务启动失败: "+err.Error()+fmt.Sprintf("（%d 端口）", s.cfg.ControlPort), "err")
			s.control = nil
		} else {
			s.discovery = services.NewDiscoveryResponder(s.cfg, config.AppVersion, s.Log)
			if err := s.discovery.Start(); err != nil {
				s.Log("服务发现启动失败: "+err.Error(), "warn")
				s.discovery = nil
			}
			ok++
		}
	}

	s.mu.Lock()
	s.running = ok > 0
	s.svcCount = ok
	s.mu.Unlock()

	if ok > 0 {
		s.Log(fmt.Sprintf("服务启动完成，成功 %d 项", ok), "ok")
	} else {
		s.Log("启动失败：没有任何服务起来（检查权限与端口占用）", "err")
	}
	return ok
}

// Stop 停止所有服务。
func (s *Server) Stop() {
	if s.discovery != nil {
		s.discovery.Stop()
		s.discovery = nil
	}
	if s.dhcp != nil {
		s.dhcp.Stop()
		s.dhcp = nil
	}
	if s.tftp != nil {
		s.tftp.Stop()
		s.tftp = nil
	}
	if s.http != nil {
		s.http.Stop()
		s.http = nil
	}
	if s.control != nil {
		s.control.Stop()
		s.control = nil
	}
	s.mu.Lock()
	s.running = false
	s.svcCount = 0
	s.mu.Unlock()
	s.Log("所有服务已停止", "warn")
}

// Services 返回各服务是否在跑（给底部状态灯用）。
func (s *Server) Services() map[string]bool {
	return map[string]bool{
		"dhcp": s.dhcp != nil,
		"tftp": s.tftp != nil,
		"http": s.http != nil,
		"file": s.control != nil,
	}
}

// Control 返回控制服务（可能为 nil）。
func (s *Server) Control() *services.ControlServer { return s.control }

// ---------------------------------------------------------------- 设备台账

func (s *Server) device(key string) *Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devices[key]
	if d == nil {
		mac := "-"
		if strings.Contains(key, ":") {
			mac = key
		}
		d = &Device{Key: key, MAC: mac, IP: "-", Boot: "-", Speed: "-", Root: "-"}
		s.devices[key] = d
	}
	return d
}

// onDhcpClient PXE 阶段的客户端：只更新台账，不代表"已上线"。
func (s *Server) onDhcpClient(mac, ip string, isIpxe bool, platform string) {
	d := s.device(mac)
	s.mu.Lock()
	d.PXEAt = float64(time.Now().UnixNano()) / 1e9
	if ip != "" && ip != "-" {
		d.IP = ip
	}
	if isIpxe {
		d.Boot = s.cfg.MenuFile
	} else {
		d.Boot = s.cfg.BootFileFor(platform)
	}
	s.mu.Unlock()
}

// onClientLog 客户端回传的日志。
func (s *Server) onClientLog(cid uint32, text string) {
	s.Log(fmt.Sprintf("[#%03d] %s", cid, text), "info")
}

// RefreshDevices 把 6003 会话合并进台账：在线置在线并刷新时间，掉线置离线但保留记录。
func (s *Server) RefreshDevices() {
	now := float64(time.Now().UnixNano()) / 1e9
	alive := map[string]bool{}
	if s.control != nil {
		for _, ses := range s.control.Snapshot() {
			key := ses.MAC
			if key == "" || key == "-" {
				key = "ip:" + ses.IP
			}
			d := s.device(key)
			boot := "本地"
			if ses.Mode == "PE" {
				boot = s.cfg.MenuFile
			}
			s.mu.Lock()
			d.MAC = ses.MAC
			if d.MAC == "" {
				d.MAC = "-"
			}
			d.IP = ses.IP
			d.CID = fmt.Sprintf("%03d", ses.CID)
			d.Name = ses.Hostname
			d.Online = true
			d.LastSeen = now
			d.Speed = "1G"
			d.Root = "√"
			d.Boot = boot
			s.mu.Unlock()
			alive[key] = true
		}
	}
	s.mu.Lock()
	var offline []string
	for key, d := range s.devices {
		if !alive[key] && d.Online {
			d.Online = false
			offline = append(offline, fmt.Sprintf("客户端 %s（%s）已离线", d.IP, d.MAC))
		}
	}
	s.mu.Unlock()
	for _, msg := range offline {
		s.Log(msg, "warn")
	}
}

// Rows 生成客户端表格数据。
func (s *Server) Rows() []Row {
	s.RefreshDevices()
	now := float64(time.Now().UnixNano()) / 1e9
	progress := map[string]int{}
	if s.control != nil {
		for _, ses := range s.control.Snapshot() {
			progress[fmt.Sprintf("%03d", ses.CID)] = ses.Progress()
		}
	}
	s.mu.Lock()
	rows := make([]Row, 0, len(s.devices))
	for _, d := range s.devices {
		state, speed, root := "离线", "-", "-"
		switch {
		case d.Online:
			state, speed, root = "在线", "1G", "√"
		case now-d.PXEAt < 90:
			state = "PXE"
		}
		cid := d.CID
		if cid == "" {
			cid = "-"
		}
		rows = append(rows, Row{
			CID: cid, IP: d.IP, MAC: d.MAC, State: state, Speed: speed,
			Root: root, Boot: d.Boot, LastSeen: d.LastSeen, Name: d.Name,
			Progress: progress[cid], Online: d.Online,
		})
	}
	s.mu.Unlock()
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.State == "在线") != (b.State == "在线") {
			return a.State == "在线"
		}
		if (a.CID == "-") != (b.CID == "-") {
			return a.CID != "-"
		}
		return a.CID < b.CID
	})
	return rows
}

// Stats 在线 / 离线 / 引导中 统计。
func (s *Server) Stats() (online, offline, pxe int) {
	for _, r := range s.Rows() {
		switch r.State {
		case "在线":
			online++
		case "离线":
			offline++
		case "PXE":
			pxe++
		}
	}
	return
}

// DisksOf 取"选中的 / 唯一的"在线客户端的磁盘列表。
func (s *Server) DisksOf() []services.DiskInfo {
	if s.control == nil {
		return nil
	}
	sessions := s.control.Snapshot()
	var picked []*services.ClientSession
	for _, x := range sessions {
		if x.Selected {
			picked = append(picked, x)
		}
	}
	if len(picked) == 0 && len(sessions) == 1 {
		picked = sessions
	}
	if len(picked) == 0 {
		return nil
	}
	return picked[0].DisksCopy()
}

// SelectClients 标记选中的客户端编号。
func (s *Server) SelectClients(cids []uint32) {
	if s.control == nil {
		return
	}
	want := map[uint32]bool{}
	for _, c := range cids {
		want[c] = true
	}
	for _, ses := range s.control.Snapshot() {
		ses.SetSelected(want[ses.CID])
	}
}

// ---------------------------------------------------------------- 下发

// Send 向所有目标客户端下发一条指令，返回台数。
func (s *Server) Send(op uint16, args []uint32, text string, payload []byte) int {
	ctl := s.control
	if ctl == nil || ctl.Count() == 0 {
		s.Log(fmt.Sprintf("[未下发] %s：没有在线客户端", protocol.OpName(op)), "warn")
		return 0
	}
	msg := protocol.New(op)
	for i, a := range args {
		if i >= 8 {
			break
		}
		msg.Args[i] = a
	}
	msg.Text = text
	msg.Payload = payload
	n := ctl.Broadcast(msg)
	s.Log(fmt.Sprintf("-> %s  (%d 台)", protocol.Describe(msg), n), "ok")
	return n
}

// SendTo 发给指定编号。
func (s *Server) SendTo(cid uint32, op uint16, args []uint32, text string, payload []byte) bool {
	ctl := s.control
	if ctl == nil {
		return false
	}
	msg := protocol.New(op)
	for i, a := range args {
		if i >= 8 {
			break
		}
		msg.Args[i] = a
	}
	msg.Text = text
	msg.Payload = payload
	return ctl.SendTo(cid, msg)
}

// PushFiles 推送一组文件（拉模式）。
func (s *Server) PushFiles(paths []string, targetDir string) int {
	ctl := s.control
	if ctl == nil || ctl.Count() == 0 {
		s.Log("没有在线客户端", "warn")
		return 0
	}
	var totalChunks int64
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			totalChunks += (st.Size() + int64(services.ChunkSize) - 1) / int64(services.ChunkSize)
		}
	}
	ctl.BeginPush(totalChunks)
	n := 0
	for _, p := range paths {
		if err := ctl.PushFile(p, targetDir); err != nil {
			s.Log("推送失败: "+err.Error(), "err")
			continue
		}
		n++
	}
	if n > 0 {
		// 种子是异步的：真正传输靠客户端来拉分片。如果 10 秒内一片都没发出去，
		// 说明客户端根本没在拉（目录不可写 / 连接异常 / 旧版客户端），
		// 否则界面上就是进度条一直 0%，看不出到底是"慢"还是"死了"。
		go func() {
			time.Sleep(10 * time.Second)
			if sent, total := ctl.PushProgress(); sent == 0 && total > 0 {
				s.Log("文件种子已发出，但 10 秒内没有任何分片请求：请检查被控端日志"+
					"（目标目录是否可写、连接是否正常）", "warn")
			}
		}()
	}
	return n
}

// ParseCID "#003" / "003" → 3。
func ParseCID(s string) (uint32, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if s == "" || s == "-" {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return 0, false
	}
	return uint32(v), true
}
