package server

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dppxe/internal/config"
	"dppxe/internal/protocol"
	"dppxe/internal/services"
	"dppxe/internal/tasks"
)

// State 前端轮询用的完整状态快照。
type State struct {
	Version      string              `json:"version"`
	Running      bool                `json:"running"`
	Admin        bool                `json:"admin"`
	Capturing    bool                `json:"capturing"`
	ServiceCount int                 `json:"service_count"`
	Services     map[string]bool     `json:"services"`
	Cfg          config.Config       `json:"cfg"`
	IPs          []string            `json:"ips"`
	Issues       []config.Issue      `json:"issues"`
	Clients      []Row               `json:"clients"`
	Online       int                 `json:"online"`
	Offline      int                 `json:"offline"`
	PXE          int                 `json:"pxe"`
	Selected     int                 `json:"selected"`
	Logs         []LogEntry          `json:"logs"`
	LastSeq      int64               `json:"last_seq"`
	Disks        []services.DiskInfo `json:"disks"`
	VncViewer    string              `json:"vnc_viewer"`
	ConfigDir    string              `json:"config_dir"`
	LogDir       string              `json:"log_dir"`
}

// State 生成状态快照；since 为上次拿到的日志序号。
func (s *Server) State(since int64) State {
	rows := s.Rows()
	online, offline, pxe := 0, 0, 0
	for _, r := range rows {
		switch r.State {
		case "在线":
			online++
		case "离线":
			offline++
		case "PXE":
			pxe++
		}
	}
	selected := 0
	if s.control != nil {
		for _, ses := range s.control.Snapshot() {
			if ses.Selected {
				selected++
			}
		}
	}
	ips := config.ListLocalIPs()
	found := false
	for _, ip := range ips {
		if ip == s.cfg.ServerIP {
			found = true
			break
		}
	}
	if !found {
		ips = append([]string{s.cfg.ServerIP}, ips...)
	}

	return State{
		Version:      config.AppVersion,
		Running:      s.Running(),
		Admin:        IsAdmin(),
		Capturing:    s.Capturing(),
		ServiceCount: s.ServiceCount(),
		Services:     s.Services(),
		Cfg:          *s.cfg,
		IPs:          ips,
		Issues:       s.cfg.Preflight(),
		Clients:      rows,
		Online:       online,
		Offline:      offline,
		PXE:          pxe,
		Selected:     selected,
		Logs:         s.LogsSince(since),
		LastSeq:      s.LastLogSeq(),
		Disks:        s.DisksOf(),
		VncViewer:    s.cfg.VncViewer,
		ConfigDir:    config.ConfigDir(),
		LogDir:       s.LogDir,
	}
}

// Args 前端传上来的参数包。
type Args map[string]any

func (a Args) str(k string) string {
	if v, ok := a[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		if v != nil {
			return fmt.Sprint(v)
		}
	}
	return ""
}

func (a Args) num(k string) int {
	switch v := a[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	return 0
}

func (a Args) boolean(k string) bool {
	switch v := a[k].(type) {
	case bool:
		return v
	case string:
		return v == "1" || strings.EqualFold(v, "true")
	case float64:
		return v != 0
	}
	return false
}

func (a Args) strs(k string) []string {
	raw, ok := a[k].([]any)
	if !ok {
		if s := a.str(k); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (a Args) nums(k string) []uint32 {
	raw, ok := a[k].([]any)
	if !ok {
		return nil
	}
	out := make([]uint32, 0, len(raw))
	for _, v := range raw {
		switch n := v.(type) {
		case float64:
			out = append(out, uint32(n))
		case string:
			if x, err := strconv.Atoi(n); err == nil {
				out = append(out, uint32(x))
			}
		}
	}
	return out
}

// Result 命令返回。
type Result struct {
	OK   bool        `json:"ok"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data,omitempty"`
}

func okResult(msg string, data interface{}) Result { return Result{true, msg, data} }
func errResult(msg string) Result                  { return Result{false, msg, nil} }

// Command 执行一条前端命令。
func (s *Server) Command(cmd string, a Args) Result {
	switch cmd {
	// ---------------- 服务 ----------------
	case "state":
		return okResult("", s.State(int64(a.num("since"))))
	case "saveConfig":
		return s.cmdSaveConfig(a)
	case "start":
		return s.cmdStart(a)
	case "stop":
		s.Stop()
		return okResult("所有服务已停止", nil)
	case "setIP":
		return s.cmdSetIP(a)
	case "preflight":
		return s.cmdPreflight()
	case "diagnose":
		return s.cmdDiagnose()
	case "firewall":
		n, msg := services.AddFirewallRules()
		s.Log(fmt.Sprintf("防火墙规则：成功 %d/4 条  %s", n, msg), "ok")
		return okResult(fmt.Sprintf("成功 %d/4 条 %s", n, msg), nil)
	case "toggleCapture":
		return s.cmdToggleCapture()
	case "exportLog":
		return s.cmdExportLog()
	case "openCfgDir":
		_ = exec.Command("explorer", config.ConfigDir()).Start()
		return okResult("已打开配置目录", nil)
	case "openLogDir":
		_ = exec.Command("explorer", s.LogDir).Start()
		return okResult("已打开日志目录", nil)
	case "launchClient":
		return s.cmdLaunch("client")
	case "launchSim":
		return s.cmdLaunch("sim")

	// ---------------- 对话框 ----------------
	case "pickRoot":
		p, err := PickFolder("选择 netboot 目录", s.cfg.RootDir)
		if err != nil || p == "" {
			return errResult("已取消")
		}
		s.cfg.RootDir = p
		_ = s.cfg.Save()
		s.Log("根目录已设为 "+p, "info")
		return okResult(p, p)
	case "pickFiles":
		files, err := PickFiles("选择要发送的文件")
		if err != nil {
			return errResult(err.Error())
		}
		return okResult("", files)
	case "pickFolder":
		p, err := PickFolder("选择文件夹", "")
		if err != nil || p == "" {
			return errResult("已取消")
		}
		// 递归收集文件（对齐 Python 版"添加文件夹"）
		var files []string
		_ = filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				files = append(files, path)
			}
			return nil
		})
		return okResult("", files)
	case "saveClientList":
		return s.cmdSaveClientList()
	case "fileSizes":
		files := a.strs("files")
		sizes := make([]int64, len(files))
		for i, f := range files {
			if st, err := os.Stat(f); err == nil {
				sizes[i] = st.Size()
			}
		}
		return okResult("", sizes)

	// ---------------- DHCP 页 ----------------
	case "saveDhcp":
		return s.cmdSaveDhcp(a)
	case "derivePool":
		s.cfg.ApplyNetworkDefaults(s.cfg.ServerIP)
		_ = s.cfg.Save()
		s.Log(fmt.Sprintf("已按服务端 IP %s 推导地址池：%s ~ %s / %s",
			s.cfg.ServerIP, s.cfg.DhcpStart, s.cfg.DhcpEnd, s.cfg.SubnetMask), "ok")
		return okResult("", s.cfg)

	// ---------------- 远程 CMD ----------------
	case "shell":
		return s.cmdShell(a)

	// ---------------- 发送文件 ----------------
	case "sendFiles":
		target := a.str("target")
		if target == "" {
			target = s.cfg.RecvPath
		}
		files := a.strs("files")
		if len(files) == 0 {
			return errResult("文件列表为空")
		}
		s.Log(fmt.Sprintf("开始发送 %d 个文件（%s）-> %s",
			len(files), orDefault(a.str("mode"), "单播"), target), "info")
		n := s.PushFiles(files, target)
		return okResult(fmt.Sprintf("已推送 %d 个文件", n), n)

	// ---------------- 高级分区 ----------------
	case "partitionDefaults":
		sizes := tasks.DefaultSizes(a.num("total_gb"), a.num("count"),
			orDefault(a.str("table"), "gpt"), a.num("esp_mb"), a.num("msr_mb"),
			a.boolean("create_esp"), a.boolean("create_msr"))
		return okResult("", sizes)
	case "partitionScript":
		spec := parseSpec(a)
		return okResult("", tasks.BuildDiskpart(spec))
	case "partition":
		spec := parseSpec(a)
		script := tasks.BuildDiskpart(spec)
		table := 2
		if spec.Table == "mbr" {
			table = 1
		}
		msr := 0
		if spec.CreateMSR {
			msr = 1
		}
		sizes := make([]string, len(spec.SizesGB))
		for i, v := range spec.SizesGB {
			sizes[i] = strconv.Itoa(v)
		}
		n := s.Send(protocol.OpPartition,
			[]uint32{uint32(spec.Disk), uint32(table), uint32(spec.Count),
				uint32(spec.EspMB), uint32(msr), uint32(spec.TotalGB),
				uint32(spec.Align), 0},
			strings.Join(sizes, "|"), []byte(script))
		return okResult(fmt.Sprintf("下发分区指令（%s / %d 分区）-> %d 台客户端",
			strings.ToUpper(spec.Table), spec.Count, n), n)

	// ---------------- 系统安装 ----------------
	case "autopart":
		disk := a.num("disk")
		n := s.Send(protocol.OpPartition, []uint32{uint32(disk), 0, 1, 100, 2, 512, 0, 500}, "", nil)
		return okResult(fmt.Sprintf("智能自动分区 -> %d 台", n), n)
	case "rebuildMbr":
		disk := a.num("disk")
		payload := strings.Join(tasks.BuildRebuildMBR(disk), "\r\n")
		n := s.Send(protocol.OpRebuildMBR, []uint32{uint32(disk)}, "", []byte(payload))
		return okResult(fmt.Sprintf("重建主引导 -> %d 台（GPT 盘会被客户端跳过）", n), n)
	case "fixboot":
		win := orDefault(a.str("win"), `C:\`)
		boot := orDefault(a.str("boot"), "S:")
		fw := orDefault(a.str("firmware"), "auto")
		winDir := strings.TrimRight(win, "\\") + `\Windows`
		cmds := tasks.BuildFixboot(winDir, boot, fw)
		n := s.Send(protocol.OpFixBoot, nil, cmds[1], []byte(strings.Join(cmds, "\r\n")))
		return okResult(fmt.Sprintf("引导修复(%s) -> %d 台", fw, n), n)
	case "unattend":
		target := orDefault(a.str("target"), `C:\`)
		n := s.Send(protocol.OpInstall, []uint32{9}, target, nil)
		return okResult(fmt.Sprintf("注入自动应答 -> %d 台", n), n)
	case "install":
		img := a.str("image")
		if img == "" {
			return errResult("请填写镜像路径")
		}
		index := a.num("index")
		if index <= 0 {
			index = 1
		}
		target := strings.TrimRight(orDefault(a.str("target"), `C:\`), "\\")
		compact := a.boolean("compact")
		line := tasks.BuildApply(img, index, target, compact)
		c := uint32(0)
		if compact {
			c = 1
		}
		n := s.Send(protocol.OpInstall, []uint32{1, uint32(index), c}, img, []byte(line))
		return okResult(fmt.Sprintf("下发还原: %s -> %d 台", line, n), n)
	case "backup":
		img := a.str("image")
		if img == "" {
			return errResult("请填写备份输出路径")
		}
		source := strings.TrimRight(orDefault(a.str("source"), `C:\`), "\\")
		incr := a.boolean("incremental")
		line := tasks.BuildBackup(source, img, incr)
		f := uint32(0)
		if incr {
			f = 1
		}
		n := s.Send(protocol.OpBackup, []uint32{f}, img, []byte(line))
		return okResult(fmt.Sprintf("下发备份: %s -> %d 台", line, n), n)
	case "injectDrv":
		dir := a.str("dir")
		if dir == "" {
			return errResult("请填写驱动文件夹")
		}
		target := strings.TrimRight(orDefault(a.str("target"), `C:\`), "\\")
		line := tasks.BuildInjectDrv(target, dir)
		n := s.Send(protocol.OpInjectDrv, nil, dir, []byte(line))
		return okResult(fmt.Sprintf("注入驱动 -> %d 台（dism /Add-Driver /forceunsigned）", n), n)

	// ---------------- 流式安装 ----------------
	case "stream":
		url := a.str("url")
		if !strings.HasPrefix(strings.ToLower(url), "http://") &&
			!strings.HasPrefix(strings.ToLower(url), "https://") {
			return errResult("镜像流地址必须是 http/https")
		}
		part := a.num("part")
		if part <= 0 {
			part = 1
		}
		disk := a.num("disk")
		params := tasks.BuildStreamParams(tasks.StreamOptions{
			URL: url, PartIndex: part, Disk: disk, Compact: true,
			AutoPart: a.boolean("autopart"), Format: a.boolean("format"),
			FixBoot: a.boolean("fixboot"), Unattend: a.boolean("unattend"),
			PEDrv: a.boolean("pedrv"), Client: a.boolean("client"),
			VNC: a.boolean("vnc"), Reboot: a.boolean("reboot"),
		})
		n := s.Send(protocol.OpStream, []uint32{uint32(part), uint32(disk)}, url, []byte(params))
		return okResult(fmt.Sprintf("流式安装 -> %d 台\nSTREAMINSTALL %s", n, params), n)

	// ---------------- 自动化脚本 ----------------
	case "scriptTemplate":
		return okResult("", tasks.ScriptTemplate)
	case "scriptCheck":
		body := tasks.WrapScript(a.str("body"))
		return okResult(tasks.ScriptSummary(body), nil)
	case "script":
		body := tasks.WrapScript(a.str("body"))
		if body == "" {
			return errResult("脚本为空")
		}
		auto := a.boolean("auto")
		mode := "manual"
		flag := uint32(0)
		if auto {
			mode = "auto"
			flag = 1
		}
		n := s.Send(protocol.OpScript, []uint32{flag}, mode,
			protocol.Compress([]byte(body)))
		return okResult(fmt.Sprintf("下发自动化脚本 %d 行 -> %d 台（已 deflate 压缩）",
			len(strings.Split(body, "\n")), n), n)

	// ---------------- 其它设置 ----------------
	case "restartClient":
		n := s.Send(protocol.OpRestartClient, nil, "", nil)
		return okResult(fmt.Sprintf("重启客户端进程 -> %d 台", n), n)
	case "injectClient":
		n := s.Send(protocol.OpInjectClient, nil, `C:\`, nil)
		return okResult(fmt.Sprintf("注入客户端 -> %d 台（服务 LocalSystem + Run 键）", n), n)
	case "injectVNC":
		hide := uint32(0)
		if a.boolean("hide") {
			hide = 1
			s.cfg.VncHidden = true
		} else {
			s.cfg.VncHidden = false
		}
		n := s.Send(protocol.OpInjectVNC, []uint32{hide}, `C:\`, nil)
		return okResult(fmt.Sprintf("注入 VNC（%s托盘）-> %d 台",
			map[bool]string{true: "隐藏", false: "显示"}[hide == 1], n), n)
	case "injectPEDrv":
		n := s.Send(protocol.OpInjectPEDrv, nil, `C:\`, nil)
		return okResult(fmt.Sprintf("注入 PE 驱动（WofAdk / USBMMIDD）-> %d 台", n), n)
	case "setboot":
		mode := orDefault(a.str("mode"), "local")
		s.cfg.NextBoot = mode
		_ = s.cfg.Save()
		arg := uint32(1)
		if mode == "pe" {
			arg = 0
		}
		n := s.Send(protocol.OpSetBoot, []uint32{arg}, "", nil)
		name := "本地系统"
		if mode == "pe" {
			name = "PE"
		}
		return okResult(fmt.Sprintf("设置下次启动为 %s -> %d 台", name, n), n)
	case "rename":
		prefix := orDefault(a.str("prefix"), "Client-")
		s.cfg.NamePrefix = prefix
		_ = s.cfg.Save()
		n := s.Send(protocol.OpRename, nil, prefix+"%ClientNum%", nil)
		return okResult(fmt.Sprintf("自动改名 -> %d 台（前缀 %s）", n, prefix), n)
	case "power":
		action := a.num("action")
		name := map[int]string{1: "关机", 2: "重启"}[action]
		n := s.Send(protocol.OpPower, []uint32{uint32(action)}, "", nil)
		return okResult(fmt.Sprintf("下发%s -> %d 台", name, n), n)
	case "wolAll":
		return s.cmdWolAll()
	case "clearCache":
		if s.control != nil {
			s.control.ClearJobs()
		}
		s.Log("服务端缓存已清空（分片缓存 / 传输任务）", "ok")
		return okResult("已清空", nil)

	// ---------------- 客户端右键菜单 ----------------
	case "clientPower":
		return s.cmdClientPower(a)
	case "clientRestart":
		cid, ok := ParseCID(a.str("cid"))
		if !ok || s.control == nil {
			return errResult("客户端未在线")
		}
		ok2 := s.control.SendTo(cid, protocol.New(protocol.OpRestartClient))
		if ok2 {
			s.Log(fmt.Sprintf("下发重启客户端进程 -> #%d", cid), "ok")
		}
		return okResult("已下发", ok2)
	case "clientRename":
		return s.cmdClientRename(a)
	case "clientRemove":
		cid, ok := ParseCID(a.str("cid"))
		if ok && s.control != nil {
			s.control.Disconnect(cid)
		}
		mac := a.str("mac")
		s.mu.Lock()
		delete(s.devices, mac)
		s.mu.Unlock()
		s.Log(fmt.Sprintf("已从列表移除 %s（%s）", a.str("ip"), mac), "warn")
		return okResult("已移除", nil)
	case "clientWol":
		mac := a.str("mac")
		if mac == "" || mac == "-" {
			return errResult("没有 MAC")
		}
		ok := services.SendMagicPacket(mac)
		s.Log(fmt.Sprintf("已发送 WOL 魔术包 -> %s", mac), map[bool]string{true: "ok", false: "err"}[ok])
		return okResult("已发送魔术包", ok)
	case "clientInfo":
		return okResult("", s.clientInfo(a.str("cid")))
	case "vncExternal":
		return s.cmdVNC(a)

	// ---------------- 内置 VNC 查看器 ----------------
	case "vncConnect":
		port := a.num("port")
		if port == 0 {
			port = 5900
		}
		pwd := a.str("password")
		if err := s.VncConnect(a.str("ip"), port, pwd); err != nil {
			return errResult(err.Error())
		}
		return okResult("已连接", s.VncStatus())
	case "vncStatus":
		return okResult("", s.VncStatus())
	case "vncInput":
		switch a.str("kind") {
		case "pointer":
			if err := s.VncPointer(a.num("mask"), a.num("x"), a.num("y")); err != nil {
				return errResult(err.Error())
			}
		case "key":
			ks := uint32(a.num("keysym"))
			if ks != 0 {
				if err := s.VncKey(a.boolean("down"), ks); err != nil {
					return errResult(err.Error())
				}
			}
		}
		return okResult("", nil)
	case "vncClose":
		s.VncClose()
		return okResult("已断开", nil)
	case "select":
		s.SelectClients(a.nums("cids"))
		return okResult("", nil)
	case "queryDisks":
		cid, ok := ParseCID(a.str("cid"))
		if !ok || s.control == nil {
			return errResult("客户端未在线")
		}
		return okResult("已请求刷新磁盘", s.control.QueryDisks(cid))
	}
	return errResult("未知命令: " + cmd)
}

// ---------------------------------------------------------------- 各命令实现

func (s *Server) cmdSaveConfig(a Args) Result {
	if ip := a.str("server_ip"); ip != "" {
		s.cfg.ServerIP = ip
	}
	if v := a.str("boot_bios"); v != "" {
		s.cfg.BootBios = v
	}
	if v := a.str("boot_efi"); v != "" {
		s.cfg.BootEfi = v
	}
	if v := a.str("menu_file"); v != "" {
		s.cfg.MenuFile = v
	}
	if v := a.str("root_dir"); v != "" {
		s.cfg.RootDir = v
	}
	if _, ok := a["only_selected"]; ok {
		s.cfg.OnlySelected = a.boolean("only_selected")
	}
	for _, k := range []string{"dhcp_enabled", "tftp_enabled", "http_enabled",
		"proxydhcp_enabled", "filesvc_enabled"} {
		if _, ok := a[k]; ok {
			switch k {
			case "dhcp_enabled":
				s.cfg.DhcpEnabled = a.boolean(k)
			case "tftp_enabled":
				s.cfg.TftpEnabled = a.boolean(k)
			case "http_enabled":
				s.cfg.HttpEnabled = a.boolean(k)
			case "proxydhcp_enabled":
				s.cfg.ProxyDhcp = a.boolean(k)
			case "filesvc_enabled":
				s.cfg.FileSvcEnable = a.boolean(k)
			}
		}
	}
	if err := s.cfg.Save(); err != nil {
		return errResult("保存失败: " + err.Error())
	}
	return okResult("配置已保存", nil)
}

func (s *Server) cmdStart(a Args) Result {
	// 启动前把 UI 上的设置落盘
	_ = s.cfg.Save()

	for _, it := range []struct {
		port  int
		name  string
		proto string
	}{
		{s.cfg.HttpPort, "HTTP", "tcp"},
		{67, "DHCP", "udp"},
		{69, "TFTP", "udp"},
		{s.cfg.ControlPort, "文件服务", "tcp"},
	} {
		if !config.PortFree(it.port, it.proto) {
			s.Log(fmt.Sprintf("端口 %d(%s) 已被占用或权限不足，%s 可能起不来",
				it.port, strings.ToUpper(it.proto), it.name), "warn")
		}
	}

	var errors []string
	for _, is := range s.cfg.Preflight() {
		if is.Level == "error" {
			s.Log("自检失败: "+is.Msg, "err")
			errors = append(errors, is.Msg)
		} else {
			s.Log("自检提示: "+is.Msg, "warn")
		}
	}
	if len(errors) > 0 && !a.boolean("force") {
		// 前端会二次确认后再带 force=1 调用
		return Result{OK: false, Msg: "启动前自检发现问题", Data: map[string]any{
			"need_confirm": true, "errors": errors,
		}}
	}

	n := s.Start()
	if n > 0 {
		s.autoCaptureStart()
	}
	return okResult(fmt.Sprintf("服务启动完成，成功 %d 项", n), n)
}

func (s *Server) cmdSetIP(a Args) Result {
	ip := a.str("ip")
	if strings.Count(ip, ".") != 3 {
		return errResult("IP 格式不正确")
	}
	s.cfg.ServerIP = ip
	if s.cfg.AutoNet {
		s.cfg.ApplyNetworkDefaults(ip)
		s.Log(fmt.Sprintf("服务端 IP 已切换为 %s，地址池同步为 %s ~ %s",
			ip, s.cfg.DhcpStart, s.cfg.DhcpEnd), "info")
	}
	_ = s.cfg.Save()
	return okResult("", s.cfg)
}

func (s *Server) cmdPreflight() Result {
	issues := s.cfg.Preflight()
	for _, is := range issues {
		kind := "warn"
		prefix := "! "
		if is.Level == "error" {
			kind = "err"
			prefix = "✗ "
		}
		s.Log(prefix+is.Msg, kind)
	}
	if len(issues) == 0 {
		s.Log("✓ 未发现问题", "ok")
	}
	return okResult("", issues)
}

func (s *Server) cmdDiagnose() Result {
	issues := s.cfg.Preflight()
	s.Log("—— 网络启动自检 ——", "info")
	for _, is := range issues {
		kind, prefix := "warn", "! "
		if is.Level == "error" {
			kind, prefix = "err", "✗ "
		}
		s.Log(prefix+is.Msg, kind)
	}
	if len(issues) == 0 {
		s.Log("✓ 未发现问题", "ok")
	}
	return okResult("", map[string]any{
		"root": s.cfg.RootDir, "ip": s.cfg.ServerIP,
		"pool": s.cfg.DhcpStart + " ~ " + s.cfg.DhcpEnd, "issues": issues,
	})
}

func (s *Server) cmdSaveDhcp(a Args) Result {
	set := func(dst *string, key string) {
		if v := a.str(key); v != "" || a[key] != nil {
			*dst = v
		}
	}
	set(&s.cfg.DhcpStart, "start")
	set(&s.cfg.DhcpEnd, "end")
	set(&s.cfg.SubnetMask, "mask")
	set(&s.cfg.Gateway, "gateway")
	set(&s.cfg.DNS1, "dns1")
	set(&s.cfg.DNS2, "dns2")
	if v := a.num("lease"); v > 0 {
		s.cfg.LeaseSeconds = v
	}
	s.cfg.FastResponse = a.boolean("fast")
	s.cfg.SecureBoot = a.boolean("secure")
	s.cfg.ProxyDhcp = a.boolean("proxy")
	if err := s.cfg.Save(); err != nil {
		return errResult("保存失败: " + err.Error())
	}
	s.Log("DHCP 设置已保存（重启服务后生效）", "ok")
	return okResult("已保存", s.cfg)
}

func (s *Server) cmdShell(a Args) Result {
	text := strings.TrimSpace(a.str("text"))
	if text == "" {
		return errResult("命令为空")
	}
	wait := a.boolean("wait")
	op := protocol.OpShell
	name := "SHELL"
	if wait {
		op = protocol.OpShellWait
		name = "SHELLWAIT"
	}
	flags := uint32(0)
	if a.boolean("asUser") {
		flags = 0x01
	}
	w := uint32(0)
	if wait {
		w = 1
	}
	n := s.Send(op, []uint32{w, flags}, text, nil)
	return okResult(fmt.Sprintf("下发 %s -> %d 台客户端", name, n), n)
}

func (s *Server) cmdWolAll() Result {
	s.mu.Lock()
	var macs []string
	for _, d := range s.devices {
		if d.MAC != "" && d.MAC != "-" {
			macs = append(macs, d.MAC)
		}
	}
	s.mu.Unlock()
	if len(macs) == 0 {
		return errResult("没有已知 MAC，无法发 WOL 魔术包")
	}
	ok := 0
	for _, mac := range macs {
		if services.SendMagicPacket(mac) {
			ok++
		}
	}
	s.Log(fmt.Sprintf("已发送 WOL 魔术包 %d/%d", ok, len(macs)), "ok")
	return okResult(fmt.Sprintf("已发送 %d/%d", ok, len(macs)), ok)
}

func (s *Server) cmdClientPower(a Args) Result {
	cid, ok := ParseCID(a.str("cid"))
	if !ok || s.control == nil {
		return errResult("客户端未在线")
	}
	action := uint32(a.num("action"))
	name := map[uint32]string{1: "关机", 2: "重启"}[action]
	ok2 := s.control.SendTo(cid, protocol.New(protocol.OpPower).WithArgs(action))
	if ok2 {
		s.Log(fmt.Sprintf("下发%s -> #%d", name, cid), "ok")
	}
	return okResult("已下发"+name, ok2)
}

func (s *Server) cmdClientRename(a Args) Result {
	oldCID, ok := ParseCID(a.str("cid"))
	if !ok {
		return errResult("编号无效")
	}
	newCID := uint32(a.num("new_cid"))
	ip := a.str("ip")
	if s.control != nil {
		s.control.Renumber(oldCID, newCID)
	}
	mac := a.str("mac")
	if mac != "" {
		s.mu.Lock()
		if d := s.devices[mac]; d != nil {
			d.CID = fmt.Sprintf("%03d", newCID)
			if ip != "" {
				d.IP = ip
			}
		}
		s.mu.Unlock()
	}
	s.Log(fmt.Sprintf("已修改 %s：编号 %03d -> %03d，IP %s", mac, oldCID, newCID, ip), "info")
	return okResult("已修改", nil)
}

func (s *Server) clientInfo(cidStr string) map[string]any {
	cid, ok := ParseCID(cidStr)
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.CID == fmt.Sprintf("%03d", cid) {
			last := "-"
			if d.LastSeen > 0 {
				last = time.Unix(int64(d.LastSeen), 0).Format("2006-01-02 15:04:05")
			}
			state := "离线"
			if d.Online {
				state = "在线"
			}
			return map[string]any{
				"cid": d.CID, "ip": d.IP, "mac": d.MAC, "name": d.Name,
				"state": state, "boot": d.Boot, "last_seen": last,
			}
		}
	}
	return nil
}

func (s *Server) cmdVNC(a Args) Result {
	ip := a.str("ip")
	if ip == "" || ip == "-" {
		return errResult("没有 IP")
	}
	viewer := s.FindVncViewer()
	if viewer == "" {
		p, err := PickFile("选择 VNC Viewer 程序", "", "可执行文件 (*.exe)|*.exe")
		if err != nil || p == "" {
			return errResult("没找到 VNC Viewer（tvnviewer.exe / vncviewer.exe）")
		}
		viewer = p
		s.cfg.VncViewer = p
		_ = s.cfg.Save()
	}
	exe := strings.ToLower(filepath.Base(viewer))
	var args []string
	if strings.Contains(exe, "tvnviewer") {
		args = []string{"-host=" + ip, "-port=5900"}
	} else {
		args = []string{ip}
	}
	cmd := exec.Command(viewer, args...)
	if err := cmd.Start(); err != nil {
		s.Log("启动 VNC 失败："+err.Error(), "err")
		return errResult("启动 VNC 失败：" + err.Error())
	}
	s.Log(fmt.Sprintf("已启动 VNC 连接 %s（%s）", ip, filepath.Base(viewer)), "ok")
	return okResult("已启动 VNC 连接", nil)
}

// FindVncViewer 按优先级找 VNC Viewer：
// 手动指定 → netboot\vnc → 程序自带 → PATH → 常见安装目录。
func (s *Server) FindVncViewer() string {
	if s.cfg.VncViewer != "" && config.FileExists(s.cfg.VncViewer) {
		return s.cfg.VncViewer
	}
	appDir := DefaultLogDir()
	appDir = filepath.Dir(appDir)
	names := []string{"tvnviewer.exe", "vncviewer.exe", "vncviewer64.exe"}
	var cands []string
	for _, base := range []string{
		filepath.Join(s.cfg.RootDir, "vnc"),
		filepath.Join(appDir, "tools", "vnc"),
		filepath.Join(appDir, "vnc"),
	} {
		for _, n := range names {
			cands = append(cands, filepath.Join(base, n))
		}
	}
	for _, c := range cands {
		if config.FileExists(c) {
			return c
		}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	roots := []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")}
	rel := []string{
		`TightVNC\tvnviewer.exe`,
		`RealVNC\VNC Viewer\vncviewer.exe`,
		`TigerVNC\vncviewer.exe`,
		`UltraVNC\vncviewer.exe`,
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		for _, r := range rel {
			c := filepath.Join(root, r)
			if config.FileExists(c) {
				return c
			}
		}
	}
	return ""
}

func (s *Server) cmdSaveClientList() Result {
	path, err := PickSave("保存客户端列表", "客户端列表.csv",
		"CSV 文件 (*.csv)|*.csv|文本文件 (*.txt)|*.txt")
	if err != nil || path == "" {
		return errResult("已取消")
	}
	rows := s.Rows()
	var b strings.Builder
	if strings.HasSuffix(strings.ToLower(path), ".txt") {
		b.WriteString("编号\tIP\tMAC\t状态\t引导\r\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\r\n", r.CID, r.IP, r.MAC, r.State, r.Boot)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			return errResult("保存失败：" + err.Error())
		}
	} else {
		b.WriteString("\ufeff编号,IP地址,MAC地址,状态,引导\r\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "%s,%s,%s,%s,%s\r\n", r.CID, r.IP, r.MAC, r.State, r.Boot)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			return errResult("保存失败：" + err.Error())
		}
	}
	s.Log(fmt.Sprintf("客户端列表已保存：%s（%d 条）", path, len(rows)), "ok")
	return okResult("已保存", path)
}

func (s *Server) cmdToggleCapture() Result {
	etl, pcap := services.CaptureFileNames(s.LogDir)
	if !s.Capturing() {
		if !IsAdmin() {
			return errResult("抓包需要管理员权限：请以管理员身份运行本程序")
		}
		ok, msg := services.StartCapture(etl)
		s.Log(map[bool]string{true: "抓包已开始：", false: "抓包启动失败："}[ok]+msg,
			map[bool]string{true: "ok", false: "err"}[ok])
		if ok {
			s.SetCapturing(true)
			s.Log("现在去重启虚拟机，等它跑完再点「停止抓包并分析」", "info")
		}
		return okResult(msg, ok)
	}
	ok, msg := services.StopCapture(etl, pcap)
	s.SetCapturing(false)
	s.Log(map[bool]string{true: "抓包已停止：", false: "抓包停止失败："}[ok]+msg,
		map[bool]string{true: "ok", false: "err"}[ok])
	if ok {
		services.AnalyzeCapture(pcap, s.Log)
	}
	return okResult(msg, ok)
}

// autoCaptureStart 启动服务时自动开抓包，省得用户手动跑 pktmon。
func (s *Server) autoCaptureStart() {
	if s.Capturing() || !IsAdmin() || !services.CaptureAvailable() {
		return
	}
	etl, pcap := services.CaptureFileNames(s.LogDir)
	ok, msg := services.StartCapture(etl)
	if ok {
		s.SetCapturing(true)
		s.Log(fmt.Sprintf("已自动开启抓包（%s），停止服务后自动分析", pcap), "ok")
	} else {
		s.Log("自动抓包未开启："+msg, "warn")
	}
}

// AutoCaptureStop 停止抓包并分析（停止服务 / 退出时调用）。
func (s *Server) AutoCaptureStop() {
	if !s.Capturing() {
		return
	}
	etl, pcap := services.CaptureFileNames(s.LogDir)
	ok, msg := services.StopCapture(etl, pcap)
	s.SetCapturing(false)
	s.Log(map[bool]string{true: "抓包已保存：", false: "抓包保存失败："}[ok]+msg,
		map[bool]string{true: "ok", false: "err"}[ok])
	if ok {
		services.AnalyzeCapture(pcap, s.Log)
	}
}

func (s *Server) cmdExportLog() Result {
	_ = os.MkdirAll(s.LogDir, 0o755)
	name := fmt.Sprintf("app_%s.log", time.Now().Format("20060102_150405"))
	p := filepath.Join(s.LogDir, name)
	if err := os.WriteFile(p, []byte(s.LogText()), 0o644); err != nil {
		return errResult("导出日志失败: " + err.Error())
	}
	s.Log("运行日志已导出: "+p, "ok")
	return okResult("已保存到 "+p, p)
}

func (s *Server) cmdLaunch(kind string) Result {
	exe, err := os.Executable()
	if err != nil {
		return errResult("找不到程序自身路径")
	}
	dir := filepath.Dir(exe)
	guiExe := filepath.Join(dir, "DP-PXE.exe")
	consoleExe := filepath.Join(dir, "DP-PXEClient.exe")

	var args []string
	label := ""
	switch kind {
	case "client":
		// 图形界面被控端：优先用 GUI 版 exe（不会多一个黑窗口）
		if config.FileExists(guiExe) {
			exe = guiExe
		}
		args = []string{"--client", "--gui", "--server", "127.0.0.1",
			"--port", strconv.Itoa(s.cfg.ControlPort)}
		label = "被控端（图形界面）"
	case "clientConsole":
		// 控制台被控端：PE / 需要看滚动日志时用
		if config.FileExists(consoleExe) {
			exe = consoleExe
		}
		args = []string{"--client", "--server", "127.0.0.1",
			"--port", strconv.Itoa(s.cfg.ControlPort)}
		label = "被控端（控制台）"
	case "sim":
		args = []string{"--sim", "--server", s.cfg.ServerIP,
			"--port", strconv.Itoa(s.cfg.ControlPort)}
		label = "客户端模拟器"
	default:
		return errResult("未知的启动类型: " + kind)
	}

	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		s.Log("启动失败: "+err.Error(), "err")
		return errResult("启动失败: " + err.Error())
	}
	s.Log(fmt.Sprintf("已启动%s（%s，PID %d）", label, filepath.Base(exe), cmd.Process.Pid), "info")
	return okResult("已启动", nil)
}

// parseSpec 从前端参数还原分区参数。
func parseSpec(a Args) *tasks.PartitionSpec {
	spec := tasks.NewSpec()
	spec.Disk = a.num("disk")
	if t := a.str("table"); t != "" {
		spec.Table = strings.ToLower(t)
	}
	if v := a.num("total_gb"); v > 0 {
		spec.TotalGB = v
	}
	if v := a.num("count"); v > 0 {
		spec.Count = v
	}
	if v := a.nums("sizes_gb"); len(v) > 0 {
		spec.SizesGB = nil
		for _, x := range v {
			spec.SizesGB = append(spec.SizesGB, int(x))
		}
	}
	if raw, ok := a["labels"].([]any); ok {
		spec.Labels = nil
		for _, x := range raw {
			spec.Labels = append(spec.Labels, fmt.Sprint(x))
		}
	}
	if raw, ok := a["filesystems"].([]any); ok {
		spec.Filesystems = nil
		for _, x := range raw {
			spec.Filesystems = append(spec.Filesystems, fmt.Sprint(x))
		}
	}
	spec.RebuildMBR = a.boolean("rebuild_mbr")
	spec.KeepESP = a.boolean("keep_esp")
	spec.CreateESP = a.boolean("create_esp")
	spec.CreateMSR = a.boolean("create_msr")
	if v := a.num("esp_mb"); v > 0 {
		spec.EspMB = v
	}
	if v := a.num("msr_mb"); v > 0 {
		spec.MsrMB = v
	}
	spec.Align = a.num("align")
	return spec
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// JSONArgs 把原始 JSON 解成 Args。
func JSONArgs(raw []byte) (Args, error) {
	if len(raw) == 0 {
		return Args{}, nil
	}
	var a Args
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if a == nil {
		a = Args{}
	}
	return a, nil
}
