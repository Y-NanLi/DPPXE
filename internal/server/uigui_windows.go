//go:build windows && servergui

package server

// PE 专用的控制端图形界面：用 walk（纯 user32/gdi32）重绘，不依赖 WebView2。
//
// 为什么不用 Wails：PE 里没有 WebView2，wails.Run 会弹
// "The WebView2 runtime is required" 模态框，不点掉进程就卡死。
// 这里只保留日常够用的功能：服务启停、客户端列表、远程命令、发送文件、
// 分区、安装/备份、引导文件设置、日志。

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/lxn/walk"
	d "github.com/lxn/walk/declarative"

	"dppxe/internal/config"
)

var (
	guiMW     *walk.MainWindow
	guiSrv    *Server
	guiInited bool
	guiLast   int64

	edServerIP                         *walk.LineEdit
	chkDhcp, chkTftp, chkHttp, chkFile *walk.CheckBox
	btnStart, btnStop                  *walk.PushButton
	lblRun, lblConn                    *walk.Label
	logBox                             *walk.TextEdit
	tabsWidget                         *walk.TabWidget

	clientTable *walk.TableView
	clientModel *clientTableModel

	cmdBrowser *walk.TextEdit
	chkWait    *walk.CheckBox
	filePath   *walk.LineEdit
	fileTarget *walk.LineEdit
	imgPath    *walk.LineEdit
	imgIndex   *walk.NumberEdit
	imgTarget  *walk.LineEdit
	bkPath     *walk.LineEdit
	partDisk   *walk.ComboBox
	partCount  *walk.NumberEdit
	partFirst  *walk.NumberEdit

	bootBios, bootEfi, menuFile, rootDir *walk.LineEdit
)

// clientTableModel 把 server.Row 喂给 walk 的 TableView。
type clientTableModel struct {
	walk.TableModelBase
	rows []Row
}

func (m *clientTableModel) RowCount() int { return len(m.rows) }

func (m *clientTableModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	r := m.rows[row]
	switch col {
	case 0:
		return r.CID
	case 1:
		return r.IP
	case 2:
		return r.MAC
	case 3:
		return r.State
	case 4:
		return r.Speed
	case 5:
		return r.Root
	}
	return ""
}

// RunNativeGUI 打开控制端的原生界面（PE 用）。
func RunNativeGUI(s *Server) (err error) {
	// walk 内部有若干 typed-nil / 资源加载的坑，panic 会把整个控制端带走。
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("界面异常: %v", r)
		}
	}()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	guiSrv = s
	clientModel = &clientTableModel{}

	err = d.MainWindow{
		AssignTo: &guiMW,
		Title:    "DP-PXE " + config.AppVersion,
		// 必须传非 nil 的 Icon：declarative 的 Icon 是 interface{}，
		// 塞进 (*walk.Icon)(nil) 会变成 typed-nil，walk 判不住就 panic。
		Icon:    walk.IconApplication(),
		Size:    d.Size{Width: 1020, Height: 740},
		MinSize: d.Size{Width: 880, Height: 620},
		// 统一用微软雅黑，默认的宋体在 Win32 控件里很难看
		Font:   d.Font{Family: "Microsoft YaHei UI", PointSize: 9},
		Layout: d.VBox{Margins: d.Margins{Left: 10, Top: 8, Right: 10, Bottom: 8}},
		Children: []d.Widget{
			topBar(),
			d.TabWidget{
				AssignTo: &tabsWidget,
				Pages: []d.TabPage{
					clientPage(), cmdPage(), filePage(),
					partPage(), installPage(), settingsPage(),
				},
				StretchFactor: 3,
			},
			d.GroupBox{
				Title:  "运行日志",
				Layout: d.VBox{Margins: d.Margins{Left: 6, Top: 4, Right: 6, Bottom: 6}},
				Children: []d.Widget{
					d.TextEdit{
						AssignTo: &logBox,
						ReadOnly: true,
						VScroll:  true,
						Font:     d.Font{Family: "Consolas", PointSize: 9},
						MinSize:  d.Size{Height: 84},
						MaxSize:  d.Size{Height: 140},
					},
				},
			},
			statusBar(),
		},
	}.Create()
	if err != nil {
		return err
	}

	tabsWidget.SetCurrentIndex(0)

	// NumberEdit 的初始值不能写在 declarative 里：Value 会先于 MinValue/MaxValue
	// 应用，落在默认的 0~0 范围里直接报 "value out of range"。这里创建后再设。
	partCount.SetValue(3)
	partFirst.SetValue(100)
	imgIndex.SetValue(1)

	refresh()
	go func() {
		t := time.NewTicker(600 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			if guiMW == nil {
				return
			}
			guiMW.Synchronize(refresh)
		}
	}()

	guiMW.Run()
	return nil
}

func topBar() d.Widget {
	return d.GroupBox{
		Title:  "服务控制",
		Layout: d.HBox{Margins: d.Margins{Left: 8, Top: 6, Right: 8, Bottom: 6}},
		Children: []d.Widget{
			d.Label{Text: "服务端IP", MinSize: d.Size{Width: 62}},
			d.LineEdit{AssignTo: &edServerIP, MinSize: d.Size{Width: 130, Height: 24}, MaxSize: d.Size{Width: 160}},
			d.PushButton{Text: "应用", MinSize: d.Size{Width: 58, Height: 24},
				OnClicked: func() { applyIP() }},
			d.HSpacer{},
			d.CheckBox{AssignTo: &chkDhcp, Text: "DHCP", OnClicked: func() { applySwitches() }},
			d.CheckBox{AssignTo: &chkTftp, Text: "TFTP", OnClicked: func() { applySwitches() }},
			d.CheckBox{AssignTo: &chkHttp, Text: "HTTP", OnClicked: func() { applySwitches() }},
			d.CheckBox{AssignTo: &chkFile, Text: "文件服务", OnClicked: func() { applySwitches() }},
			d.HSpacer{},
			d.PushButton{AssignTo: &btnStart, Text: "启动服务", MinSize: d.Size{Width: 100, Height: 28},
				OnClicked: func() { runCmd("start", nil) }},
			d.PushButton{AssignTo: &btnStop, Text: "停止服务", MinSize: d.Size{Width: 100, Height: 28}, Visible: false,
				OnClicked: func() { runCmd("stop", nil) }},
		},
	}
}

func clientPage() d.TabPage {
	return d.TabPage{Title: "客户端", Layout: d.VBox{}, Children: []d.Widget{
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.Label{AssignTo: &lblConn, Text: "在线 0 / 离线 0"},
			d.HSpacer{},
			d.PushButton{Text: "重启客户端", OnClicked: func() { runCmd("restartClient", nil) }},
			d.PushButton{Text: "刷新", OnClicked: func() { refresh() }},
		}},
		d.TableView{
			AssignTo: &clientTable,
			Model:    clientModel,
			Columns: []d.TableViewColumn{
				{Title: "编号", Width: 60},
				{Title: "IP地址", Width: 120},
				{Title: "MAC地址", Width: 140},
				{Title: "状态", Width: 70},
				{Title: "网卡", Width: 60},
				{Title: "根", Width: 50},
			},
			StretchFactor: 1,
		},
	}}
}

func cmdPage() d.TabPage {
	return d.TabPage{Title: "命令", Layout: d.VBox{}, Children: []d.Widget{
		d.Label{Text: "远程命令：广播给全部在线客户端（多行 = 多条命令）"},
		d.TextEdit{AssignTo: &cmdBrowser, VScroll: true, MinSize: d.Size{Height: 150}, StretchFactor: 1},
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.CheckBox{AssignTo: &chkWait, Text: "等待执行完成（阻塞）"},
			d.HSpacer{},
			d.PushButton{Text: "发送", MinSize: d.Size{Width: 88}, OnClicked: func() { sendShell() }},
		}},
	}}
}

func filePage() d.TabPage {
	return d.TabPage{Title: "发送文件", Layout: d.VBox{}, Children: []d.Widget{
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.Label{Text: "文件路径"},
			d.LineEdit{AssignTo: &filePath, StretchFactor: 1},
			d.PushButton{Text: "浏览…", OnClicked: func() { pickFile() }},
		}},
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.Label{Text: "目标目录"},
			d.LineEdit{AssignTo: &fileTarget, StretchFactor: 1, Text: `D:\Receive`},
			d.PushButton{Text: "发送", MinSize: d.Size{Width: 88}, OnClicked: func() { sendFile() }},
		}},
		d.Label{Text: "提示：目标目录是【客户端】上的路径；多个文件用分号分隔。"},
		d.VSpacer{},
	}}
}

func partPage() d.TabPage {
	return d.TabPage{Title: "分区", Layout: d.VBox{}, Children: []d.Widget{
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.Label{Text: "磁盘"},
			d.ComboBox{AssignTo: &partDisk, MinSize: d.Size{Width: 220}},
			d.Label{Text: "分区数"},
			d.NumberEdit{AssignTo: &partCount, Decimals: 0, MinValue: 1, MaxValue: 8, MinSize: d.Size{Width: 60}},
			d.Label{Text: "首分区(GB)"},
			d.NumberEdit{AssignTo: &partFirst, Decimals: 0, MinValue: 20, MaxValue: 4096, MinSize: d.Size{Width: 80}},
		}},
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.PushButton{Text: "刷新磁盘", OnClicked: func() { refreshDisks() }},
			d.PushButton{Text: "智能自动分区", OnClicked: func() { runCmd("autopart", nil) }},
			d.PushButton{Text: "重建主引导", OnClicked: func() { runCmd("rebuildMbr", nil) }},
			d.PushButton{Text: "引导修复", OnClicked: func() { runCmd("fixboot", nil) }},
			d.HSpacer{},
			d.PushButton{Text: "执行分区", OnClicked: func() { doPartition() }},
		}},
		d.Label{Text: "提示：分区会清空目标磁盘数据。默认 GPT；自动分区按客户端自身磁盘容量选择方案。"},
		d.VSpacer{},
	}}
}

func installPage() d.TabPage {
	return d.TabPage{Title: "安装 / 备份", Layout: d.VBox{}, Children: []d.Widget{
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.Label{Text: "镜像文件"},
			d.LineEdit{AssignTo: &imgPath, StretchFactor: 1},
			d.PushButton{Text: "浏览…", OnClicked: func() { pickImage() }},
		}},
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.Label{Text: "卷索引"},
			d.NumberEdit{AssignTo: &imgIndex, Decimals: 0, MinValue: 1, MaxValue: 99, MinSize: d.Size{Width: 60}},
			d.Label{Text: "目标盘"},
			d.LineEdit{AssignTo: &imgTarget, Text: `C:\`, MinSize: d.Size{Width: 80}},
			d.PushButton{Text: "开始安装", OnClicked: func() { doInstall() }},
		}},
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.Label{Text: "备份输出"},
			d.LineEdit{AssignTo: &bkPath, StretchFactor: 1, Text: `D:\backup.wim`},
			d.PushButton{Text: "开始备份", OnClicked: func() { doBackup() }},
		}},
		d.Label{Text: "提示：镜像路径是【服务端】上的文件；目标盘是【客户端】上的盘符。"},
		d.VSpacer{},
	}}
}

func settingsPage() d.TabPage {
	return d.TabPage{Title: "设置", Layout: d.VBox{}, Children: []d.Widget{
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.Label{Text: "传统网启引导", MinSize: d.Size{Width: 110}},
			d.LineEdit{AssignTo: &bootBios, StretchFactor: 1},
		}},
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.Label{Text: "EFI 网启引导", MinSize: d.Size{Width: 110}},
			d.LineEdit{AssignTo: &bootEfi, StretchFactor: 1},
		}},
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.Label{Text: "IPXE 菜单文件", MinSize: d.Size{Width: 110}},
			d.LineEdit{AssignTo: &menuFile, StretchFactor: 1},
		}},
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.Label{Text: "相对根目录", MinSize: d.Size{Width: 110}},
			d.LineEdit{AssignTo: &rootDir, StretchFactor: 1},
		}},
		d.Composite{Layout: d.HBox{}, Children: []d.Widget{
			d.HSpacer{},
			d.PushButton{Text: "保存设置", MinSize: d.Size{Width: 96}, OnClicked: func() { saveSettings() }},
		}},
		d.VSpacer{},
	}}
}

func statusBar() d.Widget {
	return d.Composite{Layout: d.HBox{}, Children: []d.Widget{
		d.Label{AssignTo: &lblRun, Text: "服务未启动"},
		d.HSpacer{},
		d.Label{Text: config.AppVersion},
	}}
}

// ---------------- 状态刷新 ----------------

func refresh() {
	if guiSrv == nil || guiMW == nil {
		return
	}
	st := guiSrv.State(guiLast)
	guiLast = st.LastSeq

	if !guiInited {
		guiInited = true
		edServerIP.SetText(st.Cfg.ServerIP)
		chkDhcp.SetChecked(st.Cfg.DhcpEnabled)
		chkTftp.SetChecked(st.Cfg.TftpEnabled)
		chkHttp.SetChecked(st.Cfg.HttpEnabled)
		chkFile.SetChecked(st.Cfg.FileSvcEnable)
		bootBios.SetText(st.Cfg.BootBios)
		bootEfi.SetText(st.Cfg.BootEfi)
		menuFile.SetText(st.Cfg.MenuFile)
		rootDir.SetText(st.Cfg.RootDir)
	}

	btnStart.SetVisible(!st.Running)
	btnStop.SetVisible(st.Running)
	if st.Running {
		lblRun.SetText(fmt.Sprintf("运行中（%d 个服务）", st.ServiceCount))
	} else {
		lblRun.SetText("服务未启动")
	}

	clientModel.rows = st.Clients
	clientModel.PublishRowsReset()
	lblConn.SetText(fmt.Sprintf("在线 %d / 离线 %d", st.Online, st.Offline))

	if len(st.Logs) > 0 {
		for _, e := range st.Logs {
			logBox.AppendText(fmt.Sprintf("[%s] %s\r\n", e.TS, e.Text))
		}
	}
}

func refreshDisks() {
	if guiSrv == nil {
		return
	}
	st := guiSrv.State(guiLast)
	items := make([]string, 0, len(st.Disks))
	for _, dk := range st.Disks {
		items = append(items, fmt.Sprintf("%d: %s (%d GB, %s)", dk.Index, dk.Model, dk.SizeGB, dk.Style))
	}
	if len(items) == 0 {
		items = []string{"（暂无客户端上报磁盘）"}
	}
	partDisk.SetModel(items)
	if len(items) > 0 {
		partDisk.SetCurrentIndex(0)
	}
}

// ---------------- 操作 ----------------

// runCmd 在后台执行命令，避免绑定端口等耗时操作卡住界面。
func runCmd(name string, args Args) {
	if guiSrv == nil {
		return
	}
	go func() {
		r := guiSrv.Command(name, args)
		msg := r.Msg
		if msg == "" {
			return
		}
		guiMW.Synchronize(func() {
			logBox.AppendText("> " + msg + "\r\n")
		})
	}()
}

func applyIP() {
	ip := strings.TrimSpace(edServerIP.Text())
	if ip == "" {
		return
	}
	runCmd("setIP", Args{"ip": ip})
	guiInited = false // 让下一轮刷新重新同步配置
}

func applySwitches() {
	runCmd("saveConfig", Args{
		"dhcp_enabled":    chkDhcp.Checked(),
		"tftp_enabled":    chkTftp.Checked(),
		"http_enabled":    chkHttp.Checked(),
		"filesvc_enabled": chkFile.Checked(),
	})
}

func sendShell() {
	text := strings.TrimSpace(cmdBrowser.Text())
	if text == "" {
		return
	}
	runCmd("shell", Args{"text": text, "wait": chkWait.Checked()})
}

func pickFile() {
	dlg := new(walk.FileDialog)
	dlg.Title = "选择要发送的文件"
	if ok, _ := dlg.ShowOpen(guiMW); ok {
		filePath.SetText(dlg.FilePath)
	}
}

func pickImage() {
	dlg := new(walk.FileDialog)
	dlg.Title = "选择镜像文件"
	dlg.Filter = "WIM/ESD (*.wim;*.esd)|*.wim;*.esd|所有文件 (*.*)|*.*"
	if ok, _ := dlg.ShowOpen(guiMW); ok {
		imgPath.SetText(dlg.FilePath)
	}
}

func sendFile() {
	paths := strings.TrimSpace(filePath.Text())
	if paths == "" {
		return
	}
	files := strings.Split(paths, ";")
	runCmd("sendFiles", Args{
		"files":  files,
		"target": strings.TrimSpace(fileTarget.Text()),
	})
}

func doPartition() {
	idx := partDisk.CurrentIndex()
	if idx < 0 {
		return
	}
	runCmd("partition", Args{
		"disk":     idx,
		"count":    int(partCount.Value()),
		"first_gb": int(partFirst.Value()),
		"table":    "gpt",
	})
}

func doInstall() {
	runCmd("install", Args{
		"image":  strings.TrimSpace(imgPath.Text()),
		"index":  int(imgIndex.Value()),
		"target": strings.TrimSpace(imgTarget.Text()),
	})
}

func doBackup() {
	runCmd("backup", Args{
		"image":  strings.TrimSpace(bkPath.Text()),
		"source": strings.TrimSpace(imgTarget.Text()),
	})
}

func saveSettings() {
	runCmd("saveConfig", Args{
		"boot_bios": bootBios.Text(),
		"boot_efi":  bootEfi.Text(),
		"menu_file": menuFile.Text(),
		"root_dir":  rootDir.Text(),
	})
}
