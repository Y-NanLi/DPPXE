// DP-PXE —— Go 重构版（单文件多角色）
//
//	DP-PXE.exe                      启动图形界面（Wails + WebView2）
//	DP-PXE.exe --nogui              无界面模式，直接跑 DHCP/TFTP/HTTP/文件服务
//	DP-PXE.exe --web                不开窗口，用浏览器打开界面（WebView2 不可用时兜底）
//	DP-PXE.exe --client --server IP 被控端（PE / 已装系统）
//	DP-PXE.exe --sim  --server IP   被控端模拟器（只记录不执行）
//	DP-PXE.exe --selftest           协议 / 控制通道 / 文件分片 / 脚本引擎自检
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"dppxe/internal/client"
	"dppxe/internal/config"
	"dppxe/internal/server"
	"dppxe/internal/webui"
)

func main() {
	fs := flag.NewFlagSet("DP-PXE", flag.ContinueOnError)
	var (
		mode        = fs.String("mode", "", "")
		nogui       = fs.Bool("nogui", false, "无界面模式")
		webMode     = fs.Bool("web", false, "用浏览器打开界面（不创建窗口）")
		webPort     = fs.Int("web-port", 8788, "浏览器模式端口")
		isClient    = fs.Bool("client", false, "以被控端身份运行")
		isSim       = fs.Bool("sim", false, "以客户端模拟器身份运行")
		serverAddr  = fs.String("server", "", "服务端 IP（被控端/模拟器用；不填则自动发现）")
		port        = fs.Int("port", 6003, "服务端控制端口（被控端用）")
		clientName  = fs.String("name", "", "客户端名称（默认主机名）")
		dryRun      = fs.Bool("dry-run", false, "只记录不执行（调试用）")
		quiet       = fs.Bool("quiet", false, "静默模式")
		install     = fs.Bool("install", false, "安装为开机自启")
		uninstall   = fs.Bool("uninstall", false, "卸载开机自启")
		selftest    = fs.Bool("selftest", false, "运行内置自检")
		rootDir     = fs.String("root", "", "netboot 目录（无界面模式用）")
		ipFlag      = fs.String("ip", "", "服务端 IP（无界面模式用）")
		controlPort = fs.Int("control-port", 0, "控制端口（无界面模式用）")
		httpPort    = fs.Int("http-port", 0, "HTTP 端口（无界面模式用，默认 80）")
		tftpPort    = fs.Int("tftp-port", 0, "TFTP 端口（无界面模式用，默认 69）")
		noDhcp      = fs.Bool("no-dhcp", false, "")
		noTftp      = fs.Bool("no-tftp", false, "")
		noHttp      = fs.Bool("no-http", false, "")
		guiClient   = fs.Bool("gui", false, "被控端以图形界面运行（默认控制台，PE 里请勿开启）")
	)
	// 被控端/模拟器/自检都在控制台里跑：GUI 版程序被这样调用时先补一个控制台窗口，
	// 否则 windowsgui 子系统没有 stdout，用户看到的就是"双击没反应"。
	withGUI := false
	needConsole := false
	for _, a := range os.Args[1:] {
		switch a {
		case "--gui", "-gui":
			withGUI = true
		case "--client", "-client", "--sim", "-sim", "--selftest", "-selftest",
			"--nogui", "-nogui", "--install", "-install", "--uninstall", "-uninstall":
			needConsole = true
		}
	}
	if needConsole && !withGUI {
		ensureConsole()
		fixConsoleCodepage()
	}

	if err := fs.Parse(os.Args[1:]); err != nil {
		if err != flag.ErrHelp {
			fmt.Println("参数有误，可用参数：")
			fs.SetOutput(os.Stdout)
			fs.PrintDefaults()
			pauseIfConsole()
		}
		return
	}
	_ = mode

	defer func() {
		if r := recover(); r != nil {
			writeCrash(fmt.Sprintf("panic: %v\n%s", r, debug.Stack()))
			panic(r)
		}
	}()
	writeCrash("---- start " + time.Now().Format("2006-01-02 15:04:05") + " ----")

	if *selftest {
		os.Exit(runSelftest())
	}
	if *install || *uninstall {
		if *uninstall {
			os.Exit(client.Uninstall())
		}
		os.Exit(client.Install(*serverAddr, *port))
	}
	if *isSim {
		if err := client.RunSim(*serverAddr, *port); err != nil {
			fmt.Println("模拟器退出:", err)
			pauseIfConsole()
			os.Exit(1)
		}
		return
	}
	// 角色判定：
	//   DP-PXEClient.exe 这个文件名本身就代表"被控端"——PE 里的老启动脚本是
	//   `DP-PXEClient.exe --nogui`（没有 --client），必须仍然按被控端跑，
	//   否则装进 PE 会起成服务端（DHCP/TFTP/HTTP）。
	clientBinary := strings.HasPrefix(strings.ToLower(filepath.Base(os.Args[0])), "dp-pxeclient")
	runAsClient := *isClient ||
		(clientBinary && !*isSim && !*selftest && !*install && !*uninstall && !*webMode)

	if runAsClient {
		cli := client.New(*serverAddr, *port, *dryRun, *clientName, *quiet, client.Callbacks{})
		// GUI 规则：显式 --gui 一定开；客户端二进制只要没写 --nogui 就开界面
		//（PE 里没有 WebView2，--nogui 走控制台；双击则是界面）
		wantGUI := *guiClient || (clientBinary && !*nogui)
		if wantGUI {
			if err := runClientGUI(cli); err == nil {
				return
			}
			fmt.Println("被控端界面启动失败，改用控制台模式…")
		}
		cli.RunForever()
		return
	}

	cfg := config.Load()
	if *rootDir != "" {
		cfg.RootDir = *rootDir
	}
	if *ipFlag != "" {
		cfg.ServerIP = *ipFlag
		cfg.ApplyNetworkDefaults(*ipFlag)
	}
	if *controlPort > 0 {
		cfg.ControlPort = *controlPort
	}
	if *httpPort > 0 {
		cfg.HttpPort = *httpPort
	}
	if *tftpPort > 0 {
		cfg.TftpPort = *tftpPort
	}
	if *noDhcp {
		cfg.DhcpEnabled = false
	}
	if *noTftp {
		cfg.TftpEnabled = false
	}
	if *noHttp {
		cfg.HttpEnabled = false
	}

	srv := server.New(cfg)
	if *nogui {
		os.Exit(runHeadless(srv))
	}
	if *webMode {
		os.Exit(runBrowser(srv, *webPort))
	}
	if err := runGUI(srv); err != nil {
		fmt.Println("图形界面启动失败：", err)
		fmt.Println("改用浏览器模式（--web）……")
		os.Exit(runBrowser(srv, *webPort))
	}
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

// appCtx 界面窗口的上下文（无边框窗口的最小化/关闭要用它）。
var appCtx context.Context

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

// runHeadless 无界面模式：在 PE / 无桌面环境里直接跑全部服务。
func runHeadless(srv *server.Server) int {
	logf := func(msg, kind string) {
		tag := map[string]string{"info": "  ", "ok": "[OK] ", "warn": "[!] ", "err": "[X] "}[kind]
		fmt.Printf("%s %s%s\n", time.Now().Format("15:04:05"), tag, msg)
	}
	srv.OnLog = func(e server.LogEntry) { logf(e.Text, e.Kind) }

	cfg := srv.Cfg()
	logf(fmt.Sprintf("DP-PXE %s（无界面模式）", config.AppVersion), "info")
	logf("根目录 "+absOr(cfg.RootDir), "info")
	logf(fmt.Sprintf("服务端 IP %s  控制端口 %d", cfg.ServerIP, cfg.ControlPort), "info")
	for _, is := range cfg.Preflight() {
		logf(is.Msg, map[bool]string{true: "err", false: "warn"}[is.Level == "error"])
	}

	n := srv.Start()
	if n == 0 {
		logf("没有任何服务启动成功", "err")
		return 1
	}
	logf(fmt.Sprintf("服务已启动 %d 项，按 Ctrl+C 退出", n), "ok")

	waitSignal()
	srv.AutoCaptureStop()
	srv.Stop()
	logf("已停止", "warn")
	return 0
}

// runBrowser 浏览器模式：本地起 HTTP 服务，用默认浏览器打开界面。
func runBrowser(srv *server.Server, port int) int {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	url := "http://" + addr + "/"
	fmt.Println("界面地址：", url)
	fmt.Println("（关闭本窗口即停止服务）")

	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}()

	if err := serveHTTP(srv, addr); err != nil {
		fmt.Println("启动失败:", err)
		return 1
	}
	return 0
}

func absOr(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// waitSignal 等 Ctrl+C。
func waitSignal() {
	ch := make(chan os.Signal, 1)
	signalNotify(ch)
	<-ch
}

// writeCrash 追加一行到 logs/crash.log，避免"闪退"后什么都没留下。
func writeCrash(text string) {
	dir := server.DefaultLogDir()
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "crash.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, text)
}
