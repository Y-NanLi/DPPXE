// Package client 被控端：连接服务端 6003，接收并执行指令，回传日志。
//
// 用法（同一个可执行文件）：
//
//	DP-PXE.exe --client --server 10.0.0.133        # 前台运行（PE / 系统）
//	DP-PXE.exe --client --server 10.0.0.133 --dry-run
//	DP-PXE.exe --client --install --server 10.0.0.133
//	DP-PXE.exe --client --uninstall
//	DP-PXE.exe --sim --server 10.0.0.133           # 被控端模拟器
package client

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dppxe/internal/config"
	"dppxe/internal/protocol"
	"dppxe/internal/services"
)

// HeartbeatSec 心跳间隔（秒）。
const HeartbeatSec = 20

// VersionWord 握手用的版本字（主版本<<16 | 次版本）。
const VersionWord = 1 << 16

// Callbacks 界面回调（控制台版本可全为 nil）。
type Callbacks struct {
	OnLog      func(text string)
	OnState    func(no, clientIP, serverIP, serverVer string)
	OnProgress func(pct int)
}

// Client 被控端主体。
type Client struct {
	Server  string
	Port    int
	DryRun  bool
	Quiet   bool
	Name    string
	Version string

	conn      *protocol.Conn
	cid       uint32
	localIP   string
	serverVer string
	engine    *Engine
	cb        Callbacks

	stop    chan struct{}
	sendMu  sync.Mutex
	chunkMu sync.Mutex
	waiters map[[2]uint32]chan []byte

	infoMu  sync.RWMutex
	info    Info
	logRing *LogRing

	hbStop chan struct{}
	vncRun bool
}

// New 创建被控端。
func New(server string, port int, dryRun bool, name string, quiet bool, cb Callbacks) *Client {
	if name == "" {
		name, _ = os.Hostname()
	}
	mode := "系统"
	if IsPE() {
		mode = "PE"
	}
	c := &Client{
		Server: server, Port: port, DryRun: dryRun, Name: name, Quiet: quiet,
		cb: cb, stop: make(chan struct{}), hbStop: make(chan struct{}),
		waiters: map[[2]uint32]chan []byte{},
		logRing: NewLogRing(500),
		info: Info{
			No: "未知", ClientIP: "-", ServerIP: server,
			ServerVer: "未知", ClientVer: config.ClientVersion,
			Name: name, Mode: mode,
		},
	}
	c.engine = NewEngine(c, dryRun)
	return c
}

// ExeDir 程序所在目录。
func (c *Client) ExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// Log 打印并回传日志（同时进界面用的环形缓冲）。
func (c *Client) Log(text string) {
	c.logRing.Add(text)
	if !c.Quiet {
		fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), text)
	}
	if c.cb.OnLog != nil {
		c.cb.OnLog(text)
	}
	c.sendLog(text)
}

// SendLog 只回传，不打印。
func (c *Client) SendLog(text string) { c.sendLog(text) }

func (c *Client) sendLog(text string) {
	if len([]rune(text)) > 200 {
		text = string([]rune(text)[:200])
	}
	m := protocol.New(protocol.OpLog)
	m.Text = text
	c.send(m)
}

func (c *Client) send(m *protocol.Message) bool {
	conn := c.conn
	if conn == nil {
		return false
	}
	m.ClientID = c.cid
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return conn.Send(m) == nil
}

func (c *Client) state(no, clientIP, serverIP, serverVer string) {
	c.setInfo(func(i *Info) {
		i.No = no
		if clientIP != "" {
			i.ClientIP = clientIP
		}
		if serverIP != "" {
			i.ServerIP = serverIP
		}
		if serverVer != "" {
			i.ServerVer = serverVer
		}
	})
	if c.cb.OnState != nil {
		c.cb.OnState(no, clientIP, serverIP, serverVer)
	}
}

func (c *Client) progress(p int) {
	c.setInfo(func(i *Info) { i.Progress = p })
	if c.cb.OnProgress != nil {
		c.cb.OnProgress(p)
	}
}

// MACInt 本机 MAC（取第一块可用网卡）。
func MACInt() uint64 {
	// Windows: 用 GetAdaptersAddresses 太重，这里取物理网卡里最小的那个 MAC
	ifaces, err := netInterfaces()
	if err != nil {
		return 0
	}
	var best uint64
	for _, m := range ifaces {
		if m == 0 {
			continue
		}
		if best == 0 || m < best {
			best = m
		}
	}
	return best
}

// Stop 停止重连循环。
func (c *Client) Stop() {
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// RunForever 断线自动重连（退避 3→6→12→24→30 秒）。
func (c *Client) RunForever() {
	if !c.Quiet {
		target := c.Server
		if target == "" {
			target = "自动检测"
		} else {
			target = fmt.Sprintf("%s:%d", c.Server, c.Port)
		}
		mode := "系统"
		if IsPE() {
			mode = "PE"
		}
		fmt.Printf("DP-PXE 被控端 %s 启动（%s，%s）→ 服务端 %s\n",
			config.AppVersion, c.Name, mode, target)
		fmt.Println("Ctrl+C 退出；连不上会一直自动重连")
	}

	backoff := 3
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		err := c.session()
		if err != nil {
			c.onDisconnected(err.Error())
		}
		select {
		case <-c.stop:
			return
		default:
		}
		for i := 0; i < backoff; i++ {
			select {
			case <-c.stop:
				return
			case <-time.After(time.Second):
			}
		}
		backoff *= 2
		if backoff > 30 {
			backoff = 30
		}
	}
}

func (c *Client) onDisconnected(reason string) {
	c.conn = nil
	select {
	case <-c.hbStop:
	default:
		close(c.hbStop)
	}
	c.cid = 0
	c.setInfo(func(i *Info) { i.Connected = false })
	c.state("未知", c.localIP, c.Server, c.serverVer)
	c.progress(0)
	c.Log(fmt.Sprintf("与服务端断开：%s，稍后自动重连", reason))
}

func (c *Client) discover() bool {
	c.Log("正在自动检测控制端…")
	info := services.ProbeServer(2 * time.Second)
	if info == nil {
		c.Log("未找到控制端（检查是否在同一网段/服务是否已启动）")
		return false
	}
	c.Server = info.IP
	if info.Port > 0 {
		c.Port = info.Port
	}
	c.serverVer = info.Version
	c.Log(fmt.Sprintf("已找到控制端 %s:%d（版本 %s）", c.Server, c.Port, c.serverVer))
	c.state("未知", c.localIP, c.Server, c.serverVer)
	return true
}

func (c *Client) session() error {
	if c.Server == "" && !c.discover() {
		return fmt.Errorf("未发现控制端")
	}
	raw, err := dialTCP(c.Server, c.Port, 10*time.Second)
	if err != nil {
		return err
	}
	c.conn = protocol.NewConn(raw)
	c.localIP = localAddr(raw)
	c.engine.vars["CLIENTIP"] = c.localIP
	c.engine.vars["SERVERIP"] = c.Server
	c.state("未知", c.localIP, c.Server, c.serverVer)

	mac := MACInt()
	mode := uint32(1) // 已装系统
	if IsPE() {
		mode = 0
	}
	hello := protocol.New(protocol.OpHello)
	hello.WithArgs(1, mode, VersionWord, uint32(mac&0xFFFFFFFF), uint32((mac>>32)&0xFFFF))
	hello.Text = c.Name
	c.send(hello)
	if !c.Quiet {
		fmt.Printf("已连接 %s:%d（%s，%s）\n", c.Server, c.Port, c.Name,
			map[bool]string{true: "PE", false: "系统"}[IsPE()])
	}

	c.hbStop = make(chan struct{})
	go c.heartbeat(c.hbStop)

	for {
		select {
		case <-c.stop:
			return nil
		default:
		}
		msg, err := c.conn.Recv()
		if err != nil {
			return err
		}
		if err := c.dispatch(msg); err != nil {
			return err
		}
	}
}

func (c *Client) heartbeat(stop chan struct{}) {
	for {
		select {
		case <-c.stop:
			return
		case <-stop:
			return
		case <-time.After(HeartbeatSec * time.Second):
		}
		c.send(protocol.New(protocol.OpHeartbeat))
	}
}

func (c *Client) dispatch(msg *protocol.Message) error {
	switch msg.Op {
	case protocol.OpHelloAck:
		c.cid = msg.Args[0]
		c.engine.vars["CLIENTNUM"] = fmt.Sprintf("%03d", c.cid)
		if msg.Args[1] != 0 {
			c.serverVer = formatVersion(msg.Args[1], msg.Args[2], msg.Args[3])
		}
		serverIP := msg.Text
		if serverIP == "" {
			serverIP = c.Server
		}
		c.setInfo(func(i *Info) { i.Connected = true })
		c.state(fmt.Sprintf("%03d", c.cid), c.localIP, serverIP, c.serverVer)
		c.progress(0)
		c.Log(fmt.Sprintf("握手成功，机器号 %03d，服务端 %s（版本 %s）",
			c.cid, serverIP, c.serverVer))
		go c.reportDisks()
		return nil
	case protocol.OpHeartbeat:
		c.send(protocol.New(protocol.OpAck))
		return nil
	case protocol.OpAck:
		return nil
	case protocol.OpBye:
		c.Log("服务端关闭了连接，稍后自动重连")
		if c.conn != nil {
			_ = c.conn.Close()
		}
		return fmt.Errorf("server closed")
	case protocol.OpFileSeed:
		go c.pullFile(msg)
		return nil
	case protocol.OpFileChunk:
		c.onChunk(msg)
		return nil
	case protocol.OpQueryDisks:
		go c.reportDisks()
		return nil
	}

	go c.runCommand(msg)
	return nil
}

// reportDisks 上报物理磁盘（型号/容量/分区表），服务端高级分区页直接用。
func (c *Client) reportDisks() {
	disks, err := ListDisks()
	if err != nil {
		c.Log("枚举磁盘失败：" + err.Error())
		return
	}
	payload, _ := json.Marshal(disks)
	m := protocol.New(protocol.OpDiskInfo)
	m.Payload = payload
	c.send(m)
	if len(disks) > 0 {
		var brief []string
		for _, d := range disks {
			model := d.Model
			if model == "" {
				model = "磁盘"
			}
			brief = append(brief, fmt.Sprintf("HD%d %s %dGB %s", d.Index, model, d.SizeGB, d.Style))
		}
		c.Log("上报磁盘：" + strings.Join(brief, "，"))
	} else {
		c.Log("未读到物理磁盘（可能权限不足）")
	}
}

// startVNC 拉起同目录的 TightVNC 服务端（PE 镜像里原版就带着）。
func (c *Client) startVNC() {
	if c.vncRun {
		return
	}
	for _, rel := range []string{
		`Tool\TightVNC\Amd64\tvnserver.EXE`,
		`Tool\TightVNC\X86\tvnserver.EXE`,
	} {
		exe := filepath.Join(c.ExeDir(), rel)
		if !config.FileExists(exe) {
			continue
		}
		cmd := exec.Command(exe, "-run")
		cmd.Dir = filepath.Dir(exe)
		hideWindow(cmd)
		if err := cmd.Start(); err != nil {
			c.Log("启动 VNC 服务端失败：" + err.Error())
			return
		}
		c.vncRun = true
		c.Log("已启动 VNC 服务端（tvnserver，端口 5900，未设密码）")
		return
	}
}

func (c *Client) runCommand(msg *protocol.Message) {
	defer func() {
		if r := recover(); r != nil {
			c.Log(fmt.Sprintf("执行 %s 异常：%v", msg.OpName(), r))
		}
	}()
	eng := c.engine
	switch msg.Op {
	case protocol.OpShell:
		eng.cmdSHELL(msg.Text)
	case protocol.OpShellWait:
		eng.cmdSHELLWAIT(msg.Text)
	case protocol.OpScript:
		body := ""
		if len(msg.Payload) > 0 {
			if raw, err := protocol.Decompress(msg.Payload); err == nil {
				body = string(raw)
			}
		}
		c.Log(fmt.Sprintf("收到自动化脚本 %d 行", len(strings.Split(body, "\n"))))
		eng.RunScript(body)
		c.Log("自动化任务完成")
	case protocol.OpPartition:
		c.Log("收到分区指令")
		eng.diskpart(string(msg.Payload))
	case protocol.OpInstall, protocol.OpBackup, protocol.OpStream, protocol.OpInjectDrv:
		// payload 是引擎指令（如 UNCOMPRESS "x|1" C: FALSE）
		for _, line := range strings.Split(string(msg.Payload), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				eng.ExecRaw(line)
			}
		}
	case protocol.OpFixBoot, protocol.OpRebuildMBR:
		// payload 是原始命令行（format / bcdboot）
		for _, line := range strings.Split(string(msg.Payload), "\r\n") {
			if strings.TrimSpace(line) != "" {
				eng.Run(strings.TrimSpace(line), true, 3600)
			}
		}
	case protocol.OpInjectClient:
		eng.cmdINJECTCLIENT(orDefault(msg.Text, "C:"))
	case protocol.OpInjectVNC:
		hide := "FALSE"
		if msg.Args[0] != 0 {
			hide = "TRUE"
		}
		eng.cmdINJECTVNC(orDefault(msg.Text, "C:") + " " + hide)
	case protocol.OpInjectPEDrv:
		eng.cmdINJECTPEDRV(orDefault(msg.Text, "C:"))
	case protocol.OpSetBoot:
		arg := "0"
		if msg.Args[0] != 0 {
			arg = "1"
		}
		eng.cmdSETBOOT(arg)
	case protocol.OpRename:
		eng.cmdSETCOMPUTERNAME(strings.ReplaceAll(msg.Text,
			"%ClientNum%", fmt.Sprintf("%03d", c.cid)))
	case protocol.OpPower:
		switch msg.Args[0] {
		case 1:
			eng.cmdPOWEROFF("")
		case 2:
			eng.cmdREBOOT("")
		}
	case protocol.OpRestartClient:
		c.Log("收到重启客户端进程指令")
		c.restartSelf()
	default:
		c.Log("未实现的操作码 " + msg.OpName())
	}
}

func (c *Client) restartSelf() {
	if c.DryRun {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	args := os.Args[1:]
	cmd := exec.Command(exe, args...)
	hideWindow(cmd)
	_ = cmd.Start()
	os.Exit(0)
}

// ---------------------------------------------------------------- 收文件（拉模式）

func (c *Client) pullFile(msg *protocol.Message) {
	job, size, chunk, total := msg.Args[0], int64(msg.Args[1]), int64(msg.Args[2]), int(msg.Args[3])
	target := msg.Text
	c.Log(fmt.Sprintf("收到文件种子 %s（%d B / %d 片）", target, size, total))
	if c.DryRun {
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		c.Log("创建目录失败：" + err.Error())
		return
	}
	fh, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		c.Log("写文件失败：" + err.Error())
		return
	}
	defer fh.Close()
	if err := fh.Truncate(size); err != nil {
		c.Log("预分配失败：" + err.Error())
	}
	for idx := 0; idx < total; idx++ {
		req := protocol.New(protocol.OpFileReq)
		req.WithArgs(job, uint32(idx))
		c.send(req)
		data := c.waitChunk(job, uint32(idx), 15*time.Second)
		if data == nil {
			c.Log(fmt.Sprintf("分片 %d 接收超时", idx))
			return
		}
		if _, err := fh.WriteAt(data, int64(idx)*chunk); err != nil {
			c.Log("写分片失败：" + err.Error())
			return
		}
		c.progress((idx + 1) * 100 / max(total, 1))
	}
	c.progress(100)
	c.Log(fmt.Sprintf("文件接收完成 %s（%d B）", target, size))
	done := protocol.New(protocol.OpFileDone)
	done.Text = target
	c.send(done)
}

// waitChunk 等某一片到达（按 job+片号分别登记，支持多文件并发传输）。
func (c *Client) waitChunk(job, idx uint32, timeout time.Duration) []byte {
	key := [2]uint32{job, idx}
	ch := make(chan []byte, 1)

	c.chunkMu.Lock()
	c.waiters[key] = ch
	c.chunkMu.Unlock()
	defer func() {
		c.chunkMu.Lock()
		if c.waiters[key] == ch {
			delete(c.waiters, key)
		}
		c.chunkMu.Unlock()
	}()

	select {
	case data := <-ch:
		return data
	case <-time.After(timeout):
		return nil
	}
}

func (c *Client) onChunk(msg *protocol.Message) {
	key := [2]uint32{msg.Args[0], msg.Args[1]}
	c.chunkMu.Lock()
	ch := c.waiters[key]
	c.chunkMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg.Payload:
	default:
	}
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// parsePort 解析端口字符串。
func parsePort(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return def
}
