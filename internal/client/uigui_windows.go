//go:build windows

package client

// PE 里可用的原生图形界面（纯 Win32，不依赖 WebView2 / .NET / 浏览器）。
//
// 为什么不用 Wails：Wails 的窗口就是 WebView2（Chromium），PE 里没有这个运行时，
// 一开就弹 "The WebView2 runtime is required" 的模态框，被控端等于离线。
// 这里用 github.com/lxn/walk（纯 user32/gdi32 封装）画普通窗口和控件，
// 单 exe、零运行时依赖，WinPE 里必定可用。

import (
	"bytes"
	_ "embed"
	"fmt"
	"image/png"
	"runtime"
	"strings"
	"time"

	"github.com/lxn/walk"
	d "github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"dppxe/internal/config"
)

// appIconPNG 窗口图标（256x256，由 cmd/icongen 从 assets/icon-src.webp 生成）。
//
//go:embed appicon.png
var appIconPNG []byte

// windowIcon 生成窗口图标。
//
// 这里**必须保证返回值非 nil**：declarative 的 Icon 字段是 interface{}，
// 如果把 (*walk.Icon)(nil) 塞进去会变成"有类型的 nil"，walk 内部
// `if src == nil` 判不住，随后直接解引用 → panic（实测崩过一次，
// 现象就是 PE 里被控端完全起不来）。
func windowIcon() *walk.Icon {
	if img, err := png.Decode(bytes.NewReader(appIconPNG)); err == nil {
		if ic, err := walk.IconFrom(img, 96); err == nil && ic != nil {
			return ic
		}
	}
	// 兜底：系统默认图标（stock icon，永远不是 nil）
	return walk.IconApplication()
}

// 界面控件（包级变量，render 里直接更新）。
var (
	guiMW      *walk.MainWindow
	guiNo      *walk.Label
	guiPct     *walk.Label
	guiCliIP   *walk.Label
	guiCliVer  *walk.Label
	guiSrvIP   *walk.Label
	guiSrvVer  *walk.Label
	guiWimlib  *walk.Label
	guiLog     *walk.TextEdit
	guiLogRing []string
)

const guiLogMax = 400 // 界面最多保留的日志行数

// RunNativeGUI 打开 PE 用的原生界面并阻塞到窗口关闭。
//
// 返回 nil 表示界面正常跑完（用户关窗）；返回错误表示这个环境开不了窗口
// （比如纯命令行的 WinPE），调用方应退回控制台模式。
func RunNativeGUI(c *Client) (err error) {
	// 兜底：walk 内部有若干 typed-nil / 资源加载的坑，一旦 panic 就会把整个
	// 被控端带走（PE 里表现为"完全没启动"）。这里接住，退回控制台模式。
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("界面异常: %v", r)
		}
	}()

	// walk（Win32）要求窗口的创建和消息循环在同一个 OS 线程上。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	title := "DP-PXEClient: " + config.ClientVersion
	err = d.MainWindow{
		AssignTo: &guiMW,
		Title:    title,
		Icon:     windowIcon(),
		Size:     d.Size{Width: 700, Height: 430},
		MinSize:  d.Size{Width: 560, Height: 330},
		Layout: d.VBox{
			Margins: d.Margins{Left: 12, Top: 10, Right: 12, Bottom: 8},
			Spacing: 6,
		},
		Children: []d.Widget{
			// 机器号 + 进度百分比
			d.Composite{
				Layout: d.HBox{MarginsZero: true, Spacing: 8},
				Children: []d.Widget{
					d.Label{
						AssignTo:  &guiNo,
						Text:      "机器号:未知",
						TextColor: walk.RGB(224, 0, 0),
						Font:      d.Font{Family: "Microsoft YaHei UI", PointSize: 30, Bold: true},
					},
					d.HSpacer{},
					d.Label{
						AssignTo:  &guiPct,
						Text:      "0%",
						TextColor: walk.RGB(40, 40, 40),
						Font:      d.Font{Family: "Microsoft YaHei UI", PointSize: 18, Bold: true},
					},
				},
			},
			// 客户端 IP / 版本
			d.Composite{
				Layout: d.HBox{MarginsZero: true, Spacing: 4},
				Children: []d.Widget{
					d.Label{Text: "客户端IP:", TextColor: walk.RGB(20, 72, 156), Font: d.Font{Family: "Microsoft YaHei UI", PointSize: 10, Bold: true}},
					d.Label{AssignTo: &guiCliIP, Text: "0.0.0.0"},
					d.HSpacer{MinSize: d.Size{Width: 28}},
					d.Label{Text: "版本:", TextColor: walk.RGB(20, 72, 156), Font: d.Font{Family: "Microsoft YaHei UI", PointSize: 10, Bold: true}},
					d.Label{AssignTo: &guiCliVer, Text: config.ClientVersion},
				},
			},
			// 服务端 IP / 版本
			d.Composite{
				Layout: d.HBox{MarginsZero: true, Spacing: 4},
				Children: []d.Widget{
					d.Label{Text: "服务端IP:", TextColor: walk.RGB(20, 72, 156), Font: d.Font{Family: "Microsoft YaHei UI", PointSize: 10, Bold: true}},
					d.Label{AssignTo: &guiSrvIP, Text: "0.0.0.0"},
					d.HSpacer{MinSize: d.Size{Width: 28}},
					d.Label{Text: "版本:", TextColor: walk.RGB(20, 72, 156), Font: d.Font{Family: "Microsoft YaHei UI", PointSize: 10, Bold: true}},
					d.Label{AssignTo: &guiSrvVer, Text: "未知"},
				},
			},
			// 日志（只读、带垂直滚动条）
			d.TextEdit{
				AssignTo: &guiLog,
				ReadOnly: true,
				VScroll:  true,
				Font:     d.Font{Family: "Consolas", PointSize: 10},
			},
			// 右下角 wimlib 版本
			d.Composite{
				Layout: d.HBox{MarginsZero: true},
				Children: []d.Widget{
					d.HSpacer{},
					d.Label{
						AssignTo:  &guiWimlib,
						Text:      "Wimlib Ver:未知",
						TextColor: walk.RGB(26, 156, 58),
					},
				},
			},
		},
	}.Create()
	if err != nil {
		return fmt.Errorf("创建窗口失败: %w", err)
	}

	// 窗口起来之后再跑客户端（这样日志从第一行就能显示）。
	go c.RunForever()

	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(400 * time.Millisecond)
		defer t.Stop()
		var lastSeq int64
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			info := c.Info()
			logs := c.Logs().Since(lastSeq)
			if len(logs) > 0 {
				lastSeq = logs[len(logs)-1].Seq
			}
			// walk 的控件只能在 UI 线程改
			guiMW.Synchronize(func() { renderGUI(info, logs) })
		}
	}()

	// 双保险：如果本进程是被 SW_HIDE 拉起的（例如从隐藏的 cmd 里直接调用），
	// Windows 会用 STARTUPINFO.wShowWindow 覆盖进程"第一次" ShowWindow 的参数，
	// 结果窗口创建成功但不可见，看起来就像"被控端没启动"。
	// 这里先 Show 一次（可能被覆盖），再显式 ShowWindow 一次（一定生效）。
	guiMW.Show()
	win.ShowWindow(guiMW.Handle(), win.SW_SHOWNORMAL)

	guiMW.Run() // 阻塞到窗口关闭
	close(stop)
	c.Stop()
	return nil
}

// renderGUI 把状态刷到控件上（UI 线程调用）。
func renderGUI(info Info, logs []LogEntry) {
	if guiMW == nil {
		return
	}
	no := info.No
	if no == "" {
		no = "未知"
	}
	guiNo.SetText("机器号:" + no)
	if info.Connected && no != "未知" {
		guiNo.SetTextColor(walk.RGB(26, 127, 55)) // 已连接：绿色
	} else {
		guiNo.SetTextColor(walk.RGB(224, 0, 0)) // 未连接：红色
	}

	pct := info.Progress
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	guiPct.SetText(fmt.Sprintf("%d%%", pct))

	guiCliIP.SetText(orDefault(info.ClientIP, "0.0.0.0"))
	guiCliVer.SetText(orDefault(info.ClientVer, config.ClientVersion))
	guiSrvIP.SetText(orDefault(info.ServerIP, "0.0.0.0"))
	guiSrvVer.SetText(orDefault(info.ServerVer, "未知"))
	if info.WimlibVer != "" {
		guiWimlib.SetText("Wimlib Ver:" + info.WimlibVer)
	}

	if len(logs) == 0 {
		return
	}
	for _, e := range logs {
		guiLogRing = append(guiLogRing, fmt.Sprintf("[%s] %s", e.TS, e.Text))
	}
	if len(guiLogRing) > guiLogMax {
		guiLogRing = guiLogRing[len(guiLogRing)-guiLogMax:]
	}
	guiLog.SetText(strings.Join(guiLogRing, "\r\n"))
	// 滚到末尾，保证最新的命令始终可见
	win.SendMessage(guiLog.Handle(), win.EM_SETSEL, 0x7FFFFFFF, 0x7FFFFFFF)
	win.SendMessage(guiLog.Handle(), win.EM_SCROLLCARET, 0, 0)
}
