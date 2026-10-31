// DP-PXE —— Go 重构版（单文件多角色）
//
// 控制端（Windows / Linux 通用）：
//
//	DP-PXE                          图形界面（Windows: Wails + WebView2；Linux: 浏览器模式）
//	DP-PXE --nogui                  无界面模式，直接跑 DHCP/TFTP/HTTP/文件服务
//	DP-PXE --web                    不开窗口，用浏览器打开界面
//	DP-PXE --selftest               协议 / 控制通道 / 文件分片 / 脚本引擎自检
//
// 被控端（仅 Windows，PE / 已装系统）：
//
//	DP-PXE --client --server IP     被控端
//	DP-PXE --sim  --server IP       被控端模拟器（只记录不执行）
//
// 平台差异集中在三处：main_windows.go / main_linux.go、helpers_*.go，
// 以及各 internal 包里同名不同后缀的实现文件。
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"dppxe/internal/config"
	"dppxe/internal/server"
	"dppxe/internal/webui"
)

// clientOpts 被控端启动参数。
//
// 故意不引用 internal/client（那个包只在 Windows 上编译），
// 否则 Linux 构建会连带把整个被控端拖进来。
type clientOpts struct {
	ServerAddr string // 服务端 IP，空则自动发现
	Port       int    // 控制端口
	DryRun     bool   // 只记录不执行
	Name       string // 客户端名称，空则用主机名
	Quiet      bool   // 静默模式
	GUI        bool   // 显式要求图形界面（--gui）
	NoGUI      bool   // 强制控制台（--nogui）
}

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
	// （Linux 上 ensureConsole / fixConsoleCodepage 是空实现。）
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
		os.Exit(runInstall(*uninstall, *serverAddr, *port))
	}
	if *isSim {
		os.Exit(runSimRole(*serverAddr, *port))
	}
	// 角色判定：
	//   DP-PXEClient.exe 这个文件名本身就代表"被控端"——PE 里的老启动脚本是
	//   `DP-PXEClient.exe --nogui`（没有 --client），必须仍然按被控端跑，
	//   否则装进 PE 会起成服务端（DHCP/TFTP/HTTP）。
	clientBinary := strings.HasPrefix(strings.ToLower(filepath.Base(os.Args[0])), "dp-pxeclient")
	runAsClient := *isClient ||
		(clientBinary && !*isSim && !*selftest && !*install && !*uninstall && !*webMode)

	if runAsClient {
		os.Exit(runClientRole(clientOpts{
			ServerAddr: *serverAddr,
			Port:       *port,
			DryRun:     *dryRun,
			Name:       *clientName,
			Quiet:      *quiet,
			GUI:        *guiClient,
			NoGUI:      *nogui,
		}))
	}

	// ---------------- 控制端 ----------------
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
	// 默认路径：Windows 先试 Wails 窗口（PE 里改用 walk 原生界面），
	// Linux 直接走浏览器模式 —— 见 main_windows.go / main_linux.go。
	os.Exit(runServerGUI(srv, *webPort))
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
		openBrowser(url)
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

// serveHTTP 浏览器模式：静态资源 + /api（两个平台共用）。
func serveHTTP(srv *server.Server, addr string) error {
	hs := &http.Server{Addr: addr, Handler: webui.BrowserHandler(srv)}
	return hs.ListenAndServe()
}
