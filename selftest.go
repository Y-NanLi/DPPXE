package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dppxe/internal/client"
	"dppxe/internal/config"
	"dppxe/internal/protocol"
	"dppxe/internal/services"
	"dppxe/internal/tasks"
)

type tester struct {
	pass, fail int
}

func (t *tester) check(name string, ok bool, detail string) {
	if ok {
		t.pass++
		fmt.Printf("  [OK]   %s\n", name)
		return
	}
	t.fail++
	fmt.Printf("  [FAIL] %s  %s\n", name, detail)
}

// runSelftest 无界面自检：协议 / 控制通道 / 文件分片 / DHCP 报文 / 脚本引擎 / HTTP / TFTP。
func runSelftest() int {
	fmt.Println("—— DP-PXE (Go) 自检 ——")
	t := &tester{}
	testProtocol(t)
	testTasks(t)
	testDhcpParse(t)
	testControl(t)
	testHTTP(t)
	testTFTP(t)
	testEngine(t)

	fmt.Printf("\n结果：%d 通过 / %d 失败\n", t.pass, t.fail)
	if t.fail > 0 {
		return 1
	}
	return 0
}

func testProtocol(t *tester) {
	fmt.Println("[协议]")
	m := protocol.New(protocol.OpShell)
	m.Seq = 42
	m.ClientID = 7
	m.WithArgs(1, 2, 3, 4, 5, 6, 7, 8)
	m.Text = "diskpart /s C:\\临时\\dp.txt"
	m.Payload = []byte{0xDE, 0xAD, 0xBE, 0xEF}
	raw := m.Pack()
	t.check("头部固定 296 字节", len(raw) == protocol.HeaderSize+len(m.Payload),
		fmt.Sprintf("实际 %d", len(raw)))

	got, err := protocol.Unpack(raw[:protocol.HeaderSize], raw[protocol.HeaderSize:])
	ok := err == nil && got.Op == m.Op && got.Seq == 42 && got.ClientID == 7 &&
		got.Args == m.Args && got.Text == m.Text && bytes.Equal(got.Payload, m.Payload)
	t.check("打包/解包往返一致（含中文文本）", ok, fmt.Sprintf("err=%v text=%q", err, got.Text))

	long := strings.Repeat("宽", 200)
	m2 := protocol.New(protocol.OpLog)
	m2.Text = long
	raw2 := m2.Pack()
	got2, _ := protocol.Unpack(raw2[:protocol.HeaderSize], nil)
	t.check("超长文本按 224 字节截断", len([]rune(got2.Text)) <= 111, fmt.Sprintf("%d", len([]rune(got2.Text))))

	data := []byte(strings.Repeat("DPPXE", 500))
	c := protocol.Compress(data)
	d, err := protocol.Decompress(c)
	t.check("zlib 压缩/解压（与 Python zlib 兼容）",
		err == nil && bytes.Equal(d, data) && len(c) < len(data), fmt.Sprintf("err=%v", err))
}

func testTasks(t *tester) {
	fmt.Println("[指令构造]")
	spec := tasks.NewSpec()
	spec.Disk, spec.Table, spec.TotalGB, spec.Count = 0, "gpt", 60, 3
	spec.SizesGB = tasks.DefaultSizes(60, 3, "gpt", 300, 16, true, true)
	t.check("默认分区大小（60GB/3 分区）", len(spec.SizesGB) == 3 && spec.SizesGB[0] == 19,
		fmt.Sprintf("%v", spec.SizesGB))

	script := tasks.BuildDiskpart(spec)
	wants := []string{
		"rem DP-PXE partition script  disk=0 table=gpt count=3 align=2048",
		"Select disk 0", "Clean", "convert gpt",
		"create partition efi size=300", `format quick fs=Fat32 label="ESP"`,
		"create partition msr size=16",
		"Create partition primary size=19456 align=2048",
		"select partition 3", "assign letter=C", "format quick fs=NTFS",
		`label 系统`, "exit",
	}
	missing := ""
	for _, w := range wants {
		if !strings.Contains(script, w) {
			missing = w
			break
		}
	}
	t.check("DiskPart 脚本与 Python 版逐行一致", missing == "", "缺少: "+missing)

	mbr := tasks.NewSpec()
	mbr.Table = "mbr"
	mbr.Count = 3
	mbr.SizesGB = []int{20, 20, 10}
	mbr.Labels = []string{"系统", "软件", "文档"}
	mbrScript := tasks.BuildDiskpart(mbr)
	t.check("MBR 走扩展分区 + active",
		strings.Contains(mbrScript, "Create partition extended") &&
			strings.Contains(mbrScript, "active"), mbrScript)

	wrapped := tasks.WrapScript("ADDLOG hi\n\nPE Sleep 1000\n' comment")
	t.check("脚本规整（补前缀 / 去空行）",
		wrapped == "ALL ADDLOG hi\nPE Sleep 1000\n' comment", wrapped)
	t.check("脚本前缀统计", strings.Contains(tasks.ScriptSummary(wrapped), "ALL 1 / PE 1"),
		tasks.ScriptSummary(wrapped))

	apply := tasks.BuildApply(`D:\Win11.wim`, 1, "C:", true)
	t.check("还原指令格式", apply == `UNCOMPRESS "D:\Win11.wim|1" C: TRUE`, apply)

	stream := tasks.BuildStreamParams(tasks.StreamOptions{
		URL: "http://a/b.wim", PartIndex: 3, AutoPart: true, Format: true,
		FixBoot: true, Unattend: true, PEDrv: true, Client: true,
		VNC: false, Reboot: true, Disk: 0, Compact: true,
	})
	t.check("流式安装 11 个参数位",
		stream == `"http://a/b.wim" 3 1 1 1 1 1 1 0 1 0 1`, stream)
	t.check("盘符 → 分区序号", tasks.PartIndexFromLetter(`D:\`) == 4,
		fmt.Sprint(tasks.PartIndexFromLetter(`D:\`)))
}

// buildDiscover 造一条 PXE DISCOVER 报文。
func buildDiscover(vendor string, arch uint16) []byte {
	pkt := make([]byte, 236)
	pkt[0] = 1 // BOOTREQUEST
	pkt[1] = 1
	pkt[2] = 6
	binary.BigEndian.PutUint32(pkt[4:], 0x12345678)
	binary.BigEndian.PutUint16(pkt[10:], 0x8000)
	mac := []byte{0x00, 0x0C, 0x29, 0xAA, 0xBB, 0xCC}
	copy(pkt[28:34], mac)
	pkt = append(pkt, []byte{0x63, 0x82, 0x53, 0x63}...)
	pkt = append(pkt, 53, 1, 1) // DISCOVER
	pkt = append(pkt, 60, byte(len(vendor)))
	pkt = append(pkt, []byte(vendor)...)
	pkt = append(pkt, 93, 2, byte(arch>>8), byte(arch))
	pkt = append(pkt, 97, 17)
	pkt = append(pkt, bytes.Repeat([]byte{0x11}, 17)...)
	pkt = append(pkt, 255)
	return pkt
}

func testDhcpParse(t *tester) {
	fmt.Println("[DHCP 报文]")
	info, ok := services.ParseDiscover(buildDiscover("PXEClient:Arch:00007:UNDI:003016", 7))
	t.check("解析 EFI 客户端 DISCOVER",
		ok && info.MAC == "00:0C:29:AA:BB:CC" && info.MsgType == 1 &&
			info.Platform == "efi" && info.IsPXE && !info.IsIPXE,
		fmt.Sprintf("%+v", info))

	info2, _ := services.ParseDiscover(buildDiscover("iPXE", 0))
	t.check("识别 iPXE 第二轮请求", info2.IsIPXE && info2.Platform == "bios",
		fmt.Sprintf("%+v", info2))

	bad, okBad := services.ParseDiscover([]byte{1, 2, 3})
	t.check("畸形报文被丢弃", !okBad && bad.MAC == "", "")
}

func tempDir() string {
	d, err := os.MkdirTemp("", "dppxe-selftest-")
	if err != nil {
		return "."
	}
	return d
}

func freePort(network string) int {
	if network == "udp" {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{})
		if err != nil {
			return 0
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func testControl(t *tester) {
	fmt.Println("[控制通道 6003]")
	cfg := config.Default()
	cfg.ServerIP = "127.0.0.1"
	cfg.ControlPort = freePort("tcp")
	logs := &bytes.Buffer{}
	logf := func(msg, kind string) { fmt.Fprintf(logs, "%s: %s\n", kind, msg) }

	cs := services.NewControlServer(cfg, logf, nil, nil)
	if err := cs.Start(); err != nil {
		t.check("控制服务启动", false, err.Error())
		return
	}
	defer cs.Stop()
	t.check("控制服务启动", true, "")

	raw, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", cfg.ControlPort), 3*time.Second)
	if err != nil {
		t.check("客户端连接", false, err.Error())
		return
	}
	conn := protocol.NewConn(raw)
	defer conn.Close()

	mac := uint64(0x000C29AABBCC)
	hello := protocol.New(protocol.OpHello)
	hello.WithArgs(1, 0, 1<<16, uint32(mac&0xFFFFFFFF), uint32((mac>>32)&0xFFFF))
	hello.Text = "SELFTEST-PC"
	if err := conn.Send(hello); err != nil {
		t.check("握手 HELLO", false, err.Error())
		return
	}
	_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
	ack, err := conn.Recv()
	t.check("握手 HELLO_ACK（编号/服务端 IP）",
		err == nil && ack.Op == protocol.OpHelloAck && ack.Args[0] == 1 && ack.Text == "127.0.0.1",
		fmt.Sprintf("err=%v ack=%+v", err, ack))

	// 心跳
	hb := protocol.New(protocol.OpHeartbeat)
	hb.Seq = 99
	_ = conn.Send(hb)
	resp, _ := conn.Recv()
	t.check("心跳回 ACK", resp != nil && resp.Op == protocol.OpAck && resp.Seq == 99, "")

	// 文件推送（拉模式）
	dir := tempDir()
	src := filepath.Join(dir, "payload.bin")
	payload := bytes.Repeat([]byte("DPPXE-文件分片-"), 60000) // ~ 900KB，多分片
	_ = os.WriteFile(src, payload, 0o644)
	_ = cs.PushFile(src, `C:\Receive`)

	seed, err := conn.Recv()
	t.check("收到 FILE_SEED",
		err == nil && seed.Op == protocol.OpFileSeed && seed.Args[0] > 0 &&
			seed.Text == `C:\Receive\payload.bin`,
		fmt.Sprintf("err=%v seed=%+v", err, seed))

	job := seed.Args[0]
	total := int(seed.Args[3])
	var got []byte
	okAll := true
	for idx := 0; idx < total; idx++ {
		req := protocol.New(protocol.OpFileReq)
		req.WithArgs(job, uint32(idx))
		_ = conn.Send(req)
		chunk, err := conn.Recv()
		if err != nil || chunk.Op != protocol.OpFileChunk || chunk.Args[1] != uint32(idx) {
			okAll = false
			break
		}
		got = append(got, chunk.Payload...)
	}
	t.check(fmt.Sprintf("文件分片拉取（%d 片 / %d B）", total, len(payload)),
		okAll && bytes.Equal(got, payload), fmt.Sprintf("收到 %d B", len(got)))

	// 磁盘信息上报
	di := protocol.New(protocol.OpDiskInfo)
	di.Payload = []byte(`[{"index":0,"model":"ST1000","size_gb":932,"style":"GPT"}]`)
	_ = conn.Send(di)
	time.Sleep(200 * time.Millisecond)
	snaps := cs.Snapshot()
	disks := snaps[0].DisksCopy()
	t.check("磁盘信息上报解析", len(disks) == 1 && disks[0].Style == "GPT", fmt.Sprintf("%+v", disks))

	// 指令下发
	shellMsg := protocol.New(protocol.OpShell)
	shellMsg.Text = "whoami"
	n := cs.Broadcast(shellMsg)
	t.check("指令广播到在线客户端", n == 1, fmt.Sprintf("n=%d", n))
	cmdMsg, err := conn.Recv()
	t.check("客户端收到 SHELL 指令",
		err == nil && cmdMsg.Op == protocol.OpShell && cmdMsg.Text == "whoami", "")

	// 重连复用编号
	conn2raw, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", cfg.ControlPort), 3*time.Second)
	if err == nil {
		conn2 := protocol.NewConn(conn2raw)
		_ = conn2.Send(hello)
		_ = conn2raw.SetReadDeadline(time.Now().Add(3 * time.Second))
		ack2, _ := conn2.Recv()
		t.check("重连复用机器号", ack2 != nil && ack2.Args[0] == ack.Args[0], "")
		_ = conn2.Close()
	}
}

func testHTTP(t *tester) {
	fmt.Println("[HTTP 服务]")
	dir := tempDir()
	content := bytes.Repeat([]byte("0123456789"), 100) // 1000 B
	_ = os.WriteFile(filepath.Join(dir, "test.wim"), content, 0o644)

	cfg := config.Default()
	cfg.RootDir = dir
	cfg.HttpPort = freePort("tcp")
	cfg.ServerIP = "127.0.0.1"
	cfg.MenuFile = "menu.txt"
	hs := services.NewHttpFileServer(cfg, func(m, k string) {}, nil)
	if err := hs.Start(); err != nil {
		t.check("HTTP 启动", false, err.Error())
		return
	}
	defer hs.Stop()
	base := fmt.Sprintf("http://127.0.0.1:%d/test.wim", cfg.HttpPort)

	resp, err := http.Get(base)
	ok := err == nil && resp.StatusCode == 200
	if resp != nil {
		body := make([]byte, 1000)
		n, _ := resp.Body.Read(body)
		ok = ok && n == 1000
		_ = resp.Body.Close()
	}
	t.check("整文件下载 200", ok, fmt.Sprint(err))

	req, _ := http.NewRequest("GET", base, nil)
	req.Header.Set("Range", "bytes=100-199")
	resp2, err2 := http.DefaultClient.Do(req)
	ok2 := err2 == nil && resp2.StatusCode == 206
	if resp2 != nil {
		buf := make([]byte, 200)
		n, _ := resp2.Body.Read(buf)
		ok2 = ok2 && n == 100 && string(buf[:n]) == string(content[100:200])
		_ = resp2.Body.Close()
	}
	t.check("Range 分段请求 206", ok2, fmt.Sprint(err2))

	resp3, _ := http.Get(fmt.Sprintf("http://127.0.0.1:%d/nope.bin", cfg.HttpPort))
	t.check("不存在返回 404", resp3 != nil && resp3.StatusCode == 404, "")
	if resp3 != nil {
		_ = resp3.Body.Close()
	}
}

func testTFTP(t *tester) {
	fmt.Println("[TFTP 服务]")
	dir := tempDir()
	content := []byte("iPXE boot image " + strings.Repeat("x", 1200))
	_ = os.WriteFile(filepath.Join(dir, "ipxe.efi"), content, 0o644)

	cfg := config.Default()
	cfg.RootDir = dir
	cfg.TftpPort = freePort("udp")
	cfg.MenuFile = "menu.txt"
	ts := services.NewTftpServer(cfg, func(m, k string) {})
	if err := ts.Start(); err != nil {
		t.check("TFTP 启动", false, err.Error())
		return
	}
	defer ts.Stop()

	sock, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		t.check("TFTP 客户端套接字", false, err.Error())
		return
	}
	defer sock.Close()

	rrq := []byte{0, 1}
	rrq = append(rrq, []byte("ipxe.efi\x00octet\x00")...)
	if _, err := sock.WriteToUDP(rrq, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: cfg.TftpPort}); err != nil {
		t.check("发送 RRQ", false, err.Error())
		return
	}
	_ = sock.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	var data []byte
	serverAddr := &net.UDPAddr{}
	for {
		n, addr, err := sock.ReadFromUDP(buf)
		if err != nil {
			break
		}
		serverAddr = addr
		if n < 4 || binary.BigEndian.Uint16(buf[:2]) != 3 {
			break
		}
		block := binary.BigEndian.Uint16(buf[2:4])
		data = append(data, buf[4:n]...)
		ack := []byte{0, 4, byte(block >> 8), byte(block)}
		_, _ = sock.WriteToUDP(ack, addr)
		if n-4 < 512 {
			break
		}
	}
	t.check(fmt.Sprintf("RRQ 传完文件（%d B）", len(content)),
		bytes.Equal(data, content), fmt.Sprintf("收到 %d B from %v", len(data), serverAddr))
}

func testEngine(t *tester) {
	fmt.Println("[被控端脚本引擎]")
	var lines []string
	cb := client.Callbacks{OnLog: func(s string) { lines = append(lines, s) }}
	c := client.New("127.0.0.1", 6003, true, "SELFTEST", true, cb)
	eng := client.NewEngine(c, true)

	script := `ALL ADDLOG 开始 %COMPUTERNAME%
ALL SETVAR X=5
ALL IF %X% = 5 THEN ADDLOG 条件成立
ALL FOR I=1 TO 3
ALL ADDLOG 循环 %I%
ALL NEXT
:END
ALL ADDLOG 结束`
	eng.RunScript(script)
	joined := strings.Join(lines, "|")
	t.check("变量替换 + 单行 IF", strings.Contains(joined, "条件成立"), joined)
	t.check("FOR/NEXT 循环 3 次",
		strings.Contains(joined, "循环 1") && strings.Contains(joined, "循环 3") &&
			!strings.Contains(joined, "循环 4"), joined)
	t.check("ADDLOG 执行", strings.Contains(joined, "结束"), joined)

	lines = nil
	eng.ExecRaw(`UNCOMPRESS "D:\a.wim|2" C: TRUE`)
	t.check("结构化指令 UNCOMPRESS 解析",
		strings.Contains(strings.Join(lines, "|"), "卷2"), strings.Join(lines, "|"))

	lines = nil
	eng.ExecRaw("SETVAR P=42")
	eng.ExecRaw("ADDLOG 值是 %P%")
	t.check("SETVAR 变量生效", strings.Contains(strings.Join(lines, "|"), "值是 42"),
		strings.Join(lines, "|"))
}
