// Package config 全局配置模型与持久化。
//
// 对应 Python 版 app/config.py：字段名保持完全一致，所以可以直接读取
// 老版本的 config.json（%APPDATA%\HHXSPxeRebuild\config.json），首次启动自动迁移。
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	AppName    = "DP-PXE (Go Rebuild)"
	AppVersion = "2.1"
	// ClientVersion 被控端版本号，握手时上报给服务端。
	ClientVersion = "2.1"
)

// VersionParts 把 AppVersion 拆成握手里用的三段版本号（"2.1" → 2,1,0）。
//
// 服务端握手回包必须带真实版本，否则被控端界面上的「服务端版本」永远是 1.0。
func VersionParts() (major, minor, patch uint32) {
	parts := strings.Split(AppVersion, ".")
	get := func(i int) uint32 {
		if i >= len(parts) {
			return 0
		}
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil || n < 0 {
			return 0
		}
		return uint32(n)
	}
	return get(0), get(1), get(2)
}

// Config 全量配置。JSON 字段名与 Python 版逐字对应，便于迁移。
type Config struct {
	// ---- 基础 / 引导 ----
	ServerIP       string `json:"server_ip"`
	BootBios       string `json:"boot_bios"`
	BootEfi        string `json:"boot_efi"`
	NativeBiosFile string `json:"native_bios_file"`
	NativeEfiFile  string `json:"native_efi_file"`
	MenuFile       string `json:"menu_file"`
	RootDir        string `json:"root_dir"`
	OnlySelected   bool   `json:"only_selected"`

	// ---- 服务开关 ----
	DhcpEnabled   bool `json:"dhcp_enabled"`
	TftpEnabled   bool `json:"tftp_enabled"`
	HttpEnabled   bool `json:"http_enabled"`
	ProxyDhcp     bool `json:"proxydhcp_enabled"`
	FileSvcEnable bool `json:"filesvc_enabled"`

	// ---- DHCP 设置 ----
	DhcpStart    string `json:"dhcp_start"`
	DhcpEnd      string `json:"dhcp_end"`
	SubnetMask   string `json:"subnet_mask"`
	Gateway      string `json:"gateway"`
	DNS1         string `json:"dns1"`
	DNS2         string `json:"dns2"`
	DhcpSname    string `json:"dhcp_sname"`
	LeaseSeconds int    `json:"lease_seconds"`
	FastResponse bool   `json:"fast_response"`
	SecureBoot   bool   `json:"secure_boot"`
	AutoNet      bool   `json:"auto_net"`

	// ---- 端口 ----
	TftpPort    int `json:"tftp_port"`
	HttpPort    int `json:"http_port"`
	ControlPort int `json:"control_port"`

	// ---- 其它设置 ----
	NamePrefix  string `json:"name_prefix"`
	NextBoot    string `json:"next_boot"`
	VncHidden   bool   `json:"vnc_hidden"`
	RecvPath    string `json:"recv_path"`
	VncViewer   string `json:"vnc_viewer"`
	VncPassword string `json:"vnc_password"`
	LastTab     int    `json:"last_tab"`
	CfgVersion  int    `json:"cfg_version"`
}

// Default 返回与 Python 版一致的默认值。
func Default() *Config {
	return &Config{
		BootBios:       "ipxe.bios",
		BootEfi:        "ipxe.efi",
		NativeBiosFile: "pxeboot.n12",
		NativeEfiFile:  "BootX64.EFI",
		MenuFile:       "menu.txt",
		RootDir:        "NetBoot",

		DhcpEnabled:   true,
		TftpEnabled:   true,
		HttpEnabled:   true,
		ProxyDhcp:     true,
		FileSvcEnable: true,

		DhcpStart:    "192.168.1.1",
		DhcpEnd:      "192.168.1.254",
		SubnetMask:   "255.255.255.0",
		DhcpSname:    "DPPXESERVER",
		LeaseSeconds: 86400,
		FastResponse: true,
		SecureBoot:   true,
		AutoNet:      true,

		TftpPort:    69,
		HttpPort:    80,
		ControlPort: 6003,

		NamePrefix: "Client-",
		NextBoot:   "local",
		RecvPath:   `D:\Receive\`,
		CfgVersion: 2,
	}
}

// ---------- 路径 ----------

// ConfigDir 配置目录：%APPDATA%\DPPXEGo。
func ConfigDir() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = home
		} else {
			base = "."
		}
	}
	d := filepath.Join(base, "DPPXEGo")
	_ = os.MkdirAll(d, 0o755)
	return d
}

// ConfigPath 配置文件路径。
func ConfigPath() string { return filepath.Join(ConfigDir(), "config.json") }

// legacyPath Python 版的配置位置，用于一次性迁移。
func legacyPath() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = home
		} else {
			return ""
		}
	}
	return filepath.Join(base, "HHXSPxeRebuild", "config.json")
}

// ---------- 网络工具 ----------

// ListLocalIPs 枚举本机 IPv4 地址（不含回环）。
func ListLocalIPs() []string {
	seen := map[string]bool{}
	var out []string
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, ifc := range ifaces {
			if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := ifc.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				ipnet, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				ip4 := ipnet.IP.To4()
				if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
					continue
				}
				s := ip4.String()
				if !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}
	}
	// 兜底：用不会真正发包的 UDP connect 拿默认出口地址
	if len(out) == 0 {
		if conn, err := net.Dial("udp4", "8.8.8.8:80"); err == nil {
			if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok {
				out = append(out, ua.IP.String())
			}
			_ = conn.Close()
		}
	}
	if len(out) == 0 {
		out = append(out, "127.0.0.1")
	}
	sort.Strings(out)
	return out
}

// LocalIPsWithLoopback DHCP 绑定用：包含 127.0.0.1（方便本机调试）。
func LocalIPsWithLoopback() []string {
	ips := ListLocalIPs()
	for _, ip := range ips {
		if ip == "127.0.0.1" {
			return ips
		}
	}
	return append(ips, "127.0.0.1")
}

// DeriveNetwork 按 /24 推导地址池，保证与"服务端IP"同网段。
func DeriveNetwork(ip string) (start, end, mask, gw string) {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return "192.168.1.1", "192.168.1.254", "255.255.255.0", "192.168.1.1"
	}
	base := strings.Join(parts[:3], ".")
	return base + ".1", base + ".254", "255.255.255.0", base + ".1"
}

// SameSubnet 判断两个地址是否同一个 /24。
func SameSubnet(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	return len(pa) == 4 && len(pb) == 4 && pa[0] == pb[0] && pa[1] == pb[1] && pa[2] == pb[2]
}

// SubnetBroadcast 计算网段广播地址。
func SubnetBroadcast(ip, mask string) string {
	i := net.ParseIP(ip).To4()
	m := net.ParseIP(mask).To4()
	if i == nil || m == nil {
		return "255.255.255.255"
	}
	out := make(net.IP, 4)
	for k := 0; k < 4; k++ {
		out[k] = i[k] | ^m[k]
	}
	return out.String()
}

// ---------- 文件查找 ----------

// ResolveIn 在 root 下按大小写不敏感的方式找文件。
func ResolveIn(root, name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	p := filepath.Join(root, filepath.FromSlash(name))
	if FileExists(p) {
		return p
	}
	cur := root
	for _, part := range strings.Split(name, "/") {
		if part == "" {
			continue
		}
		st, err := os.Stat(cur)
		if err != nil || !st.IsDir() {
			return ""
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return ""
		}
		hit := ""
		for _, e := range entries {
			if strings.EqualFold(e.Name(), part) {
				hit = e.Name()
				break
			}
		}
		if hit == "" {
			return ""
		}
		cur = filepath.Join(cur, hit)
	}
	if FileExists(cur) {
		return cur
	}
	return ""
}

// FileExists 判断文件（或目录）是否存在。
func FileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// FindNetbootDir 在常见位置找 netboot 目录（含 menu.txt / ipxe.efi 才算数）。
func FindNetbootDir() string {
	var roots []string
	if exe, err := os.Executable(); err == nil {
		abs, _ := filepath.Abs(filepath.Dir(exe))
		roots = append(roots, abs, filepath.Dir(abs), filepath.Dir(filepath.Dir(abs)))
	}
	if wd, err := os.Getwd(); err == nil {
		roots = append(roots, wd, filepath.Dir(wd), filepath.Dir(filepath.Dir(wd)))
	}
	var cands []string
	for _, r := range roots {
		cands = append(cands,
			filepath.Join(r, "netboot"),
			filepath.Join(r, "NetBoot"))
	}
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c] {
			continue
		}
		seen[c] = true
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			if FileExists(filepath.Join(c, "menu.txt")) || FileExists(filepath.Join(c, "ipxe.efi")) {
				if abs, err := filepath.Abs(c); err == nil {
					return abs
				}
				return c
			}
		}
	}
	return ""
}

// RenderMenu 把 menu.txt 里的 ${next-server} 换成实际 IP 再下发。
func RenderMenu(path, serverIP string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return []byte(strings.ReplaceAll(string(data), "${next-server}", serverIP))
}

// ---------- 持久化 ----------

// Load 读取配置；没有就返回默认值，并尝试从 Python 版迁移。
func Load() *Config {
	if p := ConfigPath(); FileExists(p) {
		if c := parse(p); c != nil {
			c.normalize()
			return c
		}
	}
	c := Default()
	if legacy := legacyPath(); legacy != "" && FileExists(legacy) {
		if old := parse(legacy); old != nil {
			c = old
			c.normalize()
			_ = c.Save()
			return c
		}
	}
	c.normalize()
	return c
}

func parse(path string) *Config {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	c := Default()
	if err := json.Unmarshal(raw, c); err != nil {
		return nil
	}
	return c
}

// normalize 补齐空值（对齐 Python __post_init__ 的行为）。
func (c *Config) normalize() {
	if c.ServerIP == "" {
		c.ServerIP = ListLocalIPs()[0]
	}
	// 根目录为空、是默认值、或指向一个已经不存在的路径时，自动重新找一次
	if c.RootDir == "" || c.RootDir == "NetBoot" || c.RootDir == "netboot" ||
		!FileExists(c.RootDir) {
		if found := FindNetbootDir(); found != "" {
			c.RootDir = found
		}
	}
	if c.BootBios == "" {
		c.BootBios = "ipxe.bios"
	}
	if c.BootEfi == "" {
		c.BootEfi = "ipxe.efi"
	}
	if c.MenuFile == "" {
		c.MenuFile = "menu.txt"
	}
	if c.NativeBiosFile == "" {
		c.NativeBiosFile = "pxeboot.n12"
	}
	if c.NativeEfiFile == "" {
		c.NativeEfiFile = "BootX64.EFI"
	}
	if c.DhcpSname == "" {
		c.DhcpSname = "DPPXESERVER"
	}
	if c.TftpPort == 0 {
		c.TftpPort = 69
	}
	if c.HttpPort == 0 {
		c.HttpPort = 80
	}
	if c.ControlPort == 0 {
		c.ControlPort = 6003
	}
	if c.LeaseSeconds == 0 {
		c.LeaseSeconds = 86400
	}
	if c.CfgVersion < 2 {
		c.SecureBoot = true
		c.CfgVersion = 2
	}
	if c.AutoNet && c.Gateway == "" {
		c.ApplyNetworkDefaults("")
	}
}

// Save 落盘。
func (c *Config) Save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), data, 0o644)
}

// ApplyNetworkDefaults 地址池 / 掩码 / 网关跟随服务端 IP 所在网段。
func (c *Config) ApplyNetworkDefaults(ip string) {
	if ip == "" {
		ip = c.ServerIP
	}
	c.DhcpStart, c.DhcpEnd, c.SubnetMask, c.Gateway = DeriveNetwork(ip)
}

// BootFileFor 返回某平台（bios/efi）对应的 iPXE 引导文件。
func (c *Config) BootFileFor(platform string) string {
	if platform == "bios" {
		return c.BootBios
	}
	return c.BootEfi
}

// ---------- 自检 ----------

// Issue 自检结果：Level = error（必然失败） / warn（能起但可能引导不了）。
type Issue struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// Preflight 启动前自检。
func (c *Config) Preflight() []Issue {
	var out []Issue
	st, err := os.Stat(c.RootDir)
	if err != nil || !st.IsDir() {
		abs := c.RootDir
		if a, err := filepath.Abs(c.RootDir); err == nil {
			abs = a
		}
		return append(out, Issue{"error", "根目录不存在：" + abs})
	}

	need := func(name, label, level string) {
		if ResolveIn(c.RootDir, name) == "" {
			out = append(out, Issue{level, fmt.Sprintf("%s 缺失：%s", label, name)})
		}
	}
	if c.SecureBoot {
		need(c.NativeEfiFile, "原生 EFI 引导器", "error")
		need(c.NativeBiosFile, "原生 BIOS 引导器", "error")
	} else {
		need(c.MenuFile, "IPXE 菜单文件", "error")
		need(c.BootEfi, "EFI 引导文件", "error")
		need(c.BootBios, "BIOS 引导文件", "error")
	}
	if !c.SecureBoot {
		for _, it := range [][2]string{
			{"IPXE/wimboot", "wimboot(EFI)"}, {"IPXE/wimboot2", "wimboot(BIOS)"},
			{"IPXE/BCD", "IPXE BCD"}, {"IPXE/bio", "IPXE BIO"},
		} {
			need(it[0], it[1], "warn")
		}
	}
	for _, it := range [][2]string{
		{"boot/BCD", "BCD(EFI)"}, {"boot/BIO", "BIO(BIOS)"},
		{"boot/boot.sdi", "boot.sdi"}, {"bootmgr.exe", "bootmgr.exe"},
	} {
		need(it[0], it[1], "warn")
	}
	if !hasWim(filepath.Join(c.RootDir, "boot")) {
		out = append(out, Issue{"warn", "boot 目录下没有 .WIM 镜像，进 PE 会失败"})
	}
	if !SameSubnet(c.ServerIP, c.DhcpStart) {
		out = append(out, Issue{"error", fmt.Sprintf(
			"服务端 IP %s 与地址池 %s 不在同一网段，客户机拿不到本机下发的引导文件",
			c.ServerIP, c.DhcpStart)})
	}
	if !c.SecureBoot {
		out = append(out, Issue{"warn", "已关闭安全引导：下发未签名的 iPXE 引导器，" +
			"开了 Secure Boot 的机器会直接拒绝执行（表现就是黑屏/跳过网启）"})
	}
	if c.ProxyDhcp && c.DhcpEnabled {
		out = append(out, Issue{"warn", "已开启代理 DHCP：本服务不分配 IP，地址池设置不生效" +
			"（客户机 IP 由现有 DHCP 提供）"})
	}
	if c.SecureBoot {
		out = append(out, Issue{"warn", fmt.Sprintf(
			"已开启安全引导：EFI 下发 %s、BIOS 下发 %s，iPXE 菜单不生效",
			c.NativeEfiFile, c.NativeBiosFile)})
	}
	return out
}

func hasWim(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".wim") {
			return true
		}
	}
	return false
}

// ---------- 杂项 ----------

// PortFree 检查端口能否绑定（用于启动前提示）。
func PortFree(port int, proto string) bool {
	if proto == "udp" {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
	ln, err := net.Listen("tcp4", ":"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
