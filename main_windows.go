//go:build windows

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"dppxe/internal/client"
	"dppxe/internal/config"
	"dppxe/internal/server"
	"dppxe/internal/webui"
)

// appCtx 界面窗口的上下文（无边框窗口的最小化/关闭要用它）。
var appCtx context.Context

// runInstall 安装 / 卸载开机自启。
func runInstall(uninstall bool, serverAddr string, port int) int {
	if uninstall {
		return client.Uninstall()
	}
	return client.Install(serverAddr, port)
}

// runSimRole 被控端模拟器（只记录不执行）。
func runSimRole(serverAddr string, port int) int {
	if err := client.RunSim(serverAddr, port); err != nil {
		fmt.Println("模拟器退出:", err)
		pauseIfConsole()
		return 1
	}
	return 0
}

// runClientRole 以被控端身份运行。
func runClientRole(o clientOpts) int {
	cli := client.New(o.ServerAddr, o.Port, o.DryRun, o.Name, o.Quiet, client.Callbacks{})

	// GUI 规则：显式 --gui 一定开；客户端二进制只要没写 --nogui 就开界面。
	//
	// 但 **PE 里不能用 Wails**：PE 没有 WebView2，Wails 会弹
	// "The WebView2 runtime is required" 的模态框卡住整个被控端。
	// PE 改用原生 Win32 界面（client.RunNativeGUI，纯 user32/gdi32）。
	clientBinary := strings.HasPrefix(strings.ToLower(filepath.Base(os.Args[0])), "dp-pxeclient")
	inPE := client.IsPE()
	wantGUI := (o.GUI || (clientBinary && !o.NoGUI)) && !inPE
	if wantGUI {
		if err := runClientGUI(cli); err == nil {
			return 0
		}
		fmt.Println("被控端界面启动失败，改用控制台模式…")
	}
	// PE：默认开原生界面；--nogui 或开窗失败则退回控制台。
	if inPE && !o.NoGUI {
		if err := client.RunNativeGUI(cli); err == nil {
			return 0
		} else {
			// 先把控制台补出来再报错，否则这行会写到无效句柄上丢掉。
			ensureConsole()
			fixConsoleCodepage()
			fmt.Println("PE 图形界面启动失败，改用控制台模式：", err)
		}
	}
	// PE 里双击（不带任何参数）时 main 没有补控制台，但控制台模式必须有
	// 输出，否则用户看到的就是"启动了但什么都没有"。
	if inPE && !hasNoGUIFlag() {
		ensureConsole()
		fixConsoleCodepage()
	}
	cli.RunForever()
	return 0
}

// hasNoGUIFlag 判断命令行里有没有 --nogui（main 里算过一次，这里要独立判断）。
func hasNoGUIFlag() bool {
	for _, a := range os.Args[1:] {
		if a == "--nogui" || a == "-nogui" {
			return true
		}
	}
	return false
}

// runServerGUI 控制端的默认启动路径。
//
// PE 里不能用 Wails：没有 WebView2，wails.Run 会弹
// "The WebView2 runtime is required" 的模态框，不点掉就卡死。
// 改用 walk 原生界面（控制端精简版）。
func runServerGUI(srv *server.Server, webPort int) int {
	if client.IsPE() {
		if err := server.RunNativeGUI(srv); err == nil {
			return 0
		} else {
			ensureConsole()
			fixConsoleCodepage()
			fmt.Println("PE 图形界面启动失败，改用控制台模式：", err)
			return runHeadless(srv)
		}
	}
	if err := runGUI(srv); err != nil {
		fmt.Println("图形界面启动失败：", err)
		fmt.Println("改用浏览器模式（--web）……")
		return runBrowser(srv, webPort)
	}
	return 0
}

// runGUI 用 Wails(WebView2) 打开界面。
func runGUI(srv *server.Server) error {
	return wails.Run(&options.App{
		Title:     "DP-PXE " + config.AppVersion,
		Width:     1080,
		Height:    820,
		MinWidth:  960,
		MinHeight: 660,
		AssetServer: &assetserver.Options{
			Assets:  webui.Dist(),
			Handler: webui.APIHandler(srv),
		},
		BackgroundColour: &options.RGBA{R: 240, G: 240, B: 240, A: 1},
		OnShutdown: func(ctx context.Context) {
			srv.AutoCaptureStop()
			if srv.Running() {
				srv.Stop()
			}
			_ = srv.Cfg().Save()
		},
	})
}

// runClientGUI 被控端图形界面（对齐原版 DP-PXEClient 布局）。
//
// 窗口是无边框的：标题栏在页面里画（蓝色渐变 + 最小化/关闭），
// 拖动靠 CSS `--wails-draggable: drag`，按钮走 /api/client/cmd。
func runClientGUI(cli *client.Client) error {
	go cli.RunForever()

	return wails.Run(&options.App{
		Title:            "DP-PXEClient " + config.ClientVersion,
		Width:            700,
		Height:           400,
		MinWidth:         560,
		MinHeight:        300,
		Frameless:        true,
		BackgroundColour: &options.RGBA{R: 247, G: 250, B: 253, A: 1},
		AssetServer: &assetserver.Options{
			Assets:  webui.Dist(),
			Handler: webui.ClientAPI(cli, clientWindowAction),
			// 被控端界面就是 client.html，把它当作首页
			Middleware: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/" || r.URL.Path == "/index.html" {
						r.URL.Path = "/client.html"
					}
					next.ServeHTTP(w, r)
				})
			},
		},
		OnStartup: func(ctx context.Context) { appCtx = ctx },
		OnShutdown: func(ctx context.Context) {
			cli.Stop()
		},
	})
}

// clientWindowAction 处理页面上的窗口按钮。
func clientWindowAction(action string) {
	if appCtx == nil {
		return
	}
	switch action {
	case "minimize":
		runtime.WindowMinimise(appCtx)
	case "maximize":
		runtime.WindowToggleMaximise(appCtx)
	case "close":
		runtime.Quit(appCtx)
	}
}
