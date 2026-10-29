package services

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"dppxe/internal/config"
)

// DHCP 报文类型
const (
	DHCPDiscover = 1
	DHCPOffer    = 2
	DHCPRequest  = 3
	DHCPDecline  = 4
	DHCPAck      = 5
	DHCPNak      = 6
	DHCPRelease  = 7
	DHCPInform   = 8
)

var dhcpMsgNames = map[byte]string{
	DHCPDiscover: "DISCOVER", DHCPOffer: "OFFER", DHCPRequest: "REQUEST",
	DHCPDecline: "DECLINE", DHCPAck: "ACK", DHCPNak: "NAK",
	DHCPRelease: "RELEASE", DHCPInform: "INFORM",
}

// DHCP option 号
const (
	optMask          = 1
	optRouter        = 3
	optDNS           = 6
	optHostname      = 12
	optRequestedIP   = 50
	optLease         = 51
	optMsgType       = 53
	optServerID      = 54
	optVendorClass   = 60
	optTFTP          = 66
	optBootFile      = 67
	optArch          = 93
	optUUID          = 97
	optVendor        = 43
	optPXE175        = 175
	magicCookie      = "\x63\x82\x53\x63"
	dhcpBroadcast    = "255.255.255.255"
	pxeBootServerPrc = 4011
)

var archNames = map[uint16]string{
	0: "BIOS x86", 6: "UEFI x86", 7: "UEFI x64", 9: "UEFI x64(BC)",
	10: "ARM32 UEFI", 11: "ARM64 UEFI",
}

// Lease 一条地址租约。
type Lease struct {
	MAC      string
	IP       string
	Expires  time.Time
	Hostname string
}

type dhcpSock struct {
	conn  *net.UDPConn
	port  int
	viaIP string
}

// DhcpServer DHCP / 代理 DHCP 服务（UDP 67 + 4011）。
//
// 关键点（与 Python 版一致）：
//  1. 按网卡分别绑定（每个本机 IP 各绑一个 socket），回包源 IP 才正确；
//  2. 同时监听 4011（PXE Boot Server 端口），否则部分 PXE ROM 拿到 IP 后放弃；
//  3. option 43 带完整引导服务器列表，并保留 DP-PXE 实测的 option 175；
//  4. 代理模式 DISCOVER 回 OFFER、REQUEST 回 ACK（yiaddr=0，只给引导信息）；
//  5. 回包目标按 RFC：广播标志 / yiaddr=0 / 有中继 → 广播，否则单播给 yiaddr。
type DhcpServer struct {
	cfg      *config.Config
	log      LogFunc
	onClient func(mac, ip string, isIpxe bool, platform string)

	socks []*dhcpSock
	stop  chan struct{}
	wg    sync.WaitGroup

	mu          sync.Mutex
	leases      map[string]*Lease
	offerCounts map[string]int
}

// NewDhcpServer 创建 DHCP 服务。
func NewDhcpServer(cfg *config.Config, log LogFunc, onClient func(mac, ip string, isIpxe bool, platform string)) *DhcpServer {
	return &DhcpServer{
		cfg: cfg, log: log, onClient: onClient,
		leases:      map[string]*Lease{},
		offerCounts: map[string]int{},
	}
}

// Start 绑定端口并开始收包。
func (d *DhcpServer) Start() error {
	d.stop = make(chan struct{})
	d.socks = nil

	type target struct{ bind, via string }
	var targets []target
	for _, ip := range config.LocalIPsWithLoopback() {
		targets = append(targets, target{ip, ip})
	}

	ports := []int{67}
	if d.cfg.ProxyDhcp {
		ports = append(ports, pxeBootServerPrc)
	}

	for _, port := range ports {
		for _, t := range targets {
			if conn, err := listenUDPBroadcast(t.bind, port); err == nil {
				d.socks = append(d.socks, &dhcpSock{conn: conn, port: port, viaIP: t.via})
			}
		}
	}
	if len(d.socks) == 0 { // 兜底：0.0.0.0
		for _, port := range ports {
			if conn, err := listenUDPBroadcast("", port); err == nil {
				d.socks = append(d.socks, &dhcpSock{conn: conn, port: port})
			}
		}
	}
	if len(d.socks) == 0 {
		return errors.New("无法绑定 DHCP 端口 67/4011（需要管理员权限，且端口未被占用）")
	}

	for _, s := range d.socks {
		d.wg.Add(1)
		go d.loop(s)
	}

	mode := "完整"
	if d.cfg.ProxyDhcp {
		mode = "代理"
	}
	ifaceSet := map[string]bool{}
	portSet := map[int]bool{}
	for _, s := range d.socks {
		if s.viaIP != "" {
			ifaceSet[s.viaIP] = true
		}
		portSet[s.port] = true
	}
	var ifaces []string
	for k := range ifaceSet {
		ifaces = append(ifaces, k)
	}
	sort.Strings(ifaces)
	var portsUsed []string
	for p := range portSet {
		portsUsed = append(portsUsed, strconv.Itoa(p))
	}
	sort.Strings(portsUsed)
	if len(ifaces) == 0 {
		ifaces = []string{"全部"}
	}
	d.log(fmt.Sprintf("DHCP 服务已启动（%s模式，端口 %s，网卡：%s）",
		mode, strings.Join(portsUsed, "/"), strings.Join(ifaces, "、")), "ok")
	return nil
}

// Stop 停止服务。
func (d *DhcpServer) Stop() {
	if d.stop != nil {
		select {
		case <-d.stop:
		default:
			close(d.stop)
		}
	}
	for _, s := range d.socks {
		_ = s.conn.Close()
	}
	d.socks = nil
	d.wg.Wait()
	d.log("DHCP 服务已停止", "warn")
}

// listenUDPBroadcast 绑定 UDP 并打开 SO_BROADCAST。
func listenUDPBroadcast(ip string, port int) (*net.UDPConn, error) {
	addr := &net.UDPAddr{Port: port}
	if ip != "" {
		addr.IP = net.ParseIP(ip)
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return nil, err
	}
	raw, err := conn.SyscallConn()
	if err == nil {
		_ = raw.Control(func(fd uintptr) {
			_ = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_BROADCAST, 1)
			_ = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
		})
	}
	return conn, nil
}

func (d *DhcpServer) loop(s *dhcpSock) {
	defer d.wg.Done()
	buf := make([]byte, 2048)
	for {
		select {
		case <-d.stop:
			return
		default:
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		func() {
			defer func() {
				if r := recover(); r != nil {
					d.log(fmt.Sprintf("DHCP 处理异常: %v", r), "err")
				}
			}()
			d.handle(s, data, addr)
		}()
	}
}

func (d *DhcpServer) handle(s *dhcpSock, data []byte, addr *net.UDPAddr) {
	if len(data) < 240 || string(data[236:240]) != magicCookie {
		return
	}
	if data[0] != 1 { // BOOTREQUEST
		return
	}
	htype, hlen := data[1], int(data[2])
	xid := binary.BigEndian.Uint32(data[4:8])
	flags := binary.BigEndian.Uint16(data[10:12])
	ciaddr := net.IP(data[12:16]).String()
	giaddr := net.IP(data[24:28]).String()
	chaddrLen := hlen
	if chaddrLen < 6 {
		chaddrLen = 6
	}
	if 28+chaddrLen > len(data) {
		return
	}
	chaddr := data[28 : 28+chaddrLen]
	mac := formatMAC(chaddr[:6])

	opts := parseDhcpOptions(data[240:])
	msgType, ok := opts[optMsgType]
	if !ok || len(msgType) == 0 {
		return
	}
	if _, known := dhcpMsgNames[msgType[0]]; !known {
		return
	}
	vendor := string(opts[optVendorClass])
	hostname := string(opts[optHostname])
	arch := uint16(0xFFFF)
	if a, ok := opts[optArch]; ok && len(a) == 2 {
		arch = binary.BigEndian.Uint16(a)
	}
	platform := "bios"
	switch arch {
	case 6, 7, 8, 9, 10, 11:
		platform = "efi"
	}

	vlow := strings.ToLower(strings.TrimSpace(vendor))
	isIpxe := strings.HasPrefix(vlow, "ipxe") || strings.Contains(vendor, "iPXE")
	// FENSYS 版 iPXE 的厂商类恰好等于 "PXEClient"（没有 :Arch:...:UNDI: 后缀）
	if vlow == "pxeclient" {
		isIpxe = true
	}
	isPxe := strings.HasPrefix(vlow, "pxeclient") || strings.HasPrefix(vlow, "ipxe")

	if d.cfg.ProxyDhcp && !isPxe {
		return // 代理模式只服务 PXE 客户端
	}
	upperVendor := strings.ToUpper(vendor)
	if !isPxe && (strings.Contains(upperVendor, "MESH") || strings.Contains(upperVendor, "DISCOVER")) {
		return // 过滤局域网发现广播
	}

	serverIP := s.viaIP
	if serverIP == "" {
		serverIP = d.cfg.ServerIP
	}
	boot := d.bootFile(isIpxe, platform)

	reqIP := ""
	if r, ok := opts[optRequestedIP]; ok && len(r) == 4 {
		reqIP = net.IP(r).String()
	}

	switch msgType[0] {
	case DHCPDiscover:
		yiaddr := "0.0.0.0"
		if !d.cfg.ProxyDhcp {
			yiaddr = d.alloc(mac)
		}
		d.reply(s, data, addr, msgType[0], DHCPOffer, serverIP, yiaddr,
			mac, chaddr, htype, hlen, xid, flags, giaddr, ciaddr,
			boot, hostname, arch, isIpxe, reqIP)
	case DHCPRequest:
		if reqIP == "" && ciaddr != "0.0.0.0" {
			reqIP = ciaddr
		}
		if d.cfg.ProxyDhcp {
			// 代理模式：只补一条"引导信息 ACK"（yiaddr=0），不抢 IP 分配
			d.reply(s, data, addr, msgType[0], DHCPAck, serverIP, "0.0.0.0",
				mac, chaddr, htype, hlen, xid, flags, giaddr, ciaddr,
				boot, hostname, arch, isIpxe, reqIP)
		} else {
			ip := reqIP
			if ip == "" || ip == "0.0.0.0" {
				ip = d.alloc(mac)
			}
			d.remember(mac, ip, hostname)
			d.reply(s, data, addr, msgType[0], DHCPAck, serverIP, ip,
				mac, chaddr, htype, hlen, xid, flags, giaddr, ciaddr,
				boot, hostname, arch, isIpxe, reqIP)
		}
	case DHCPRelease:
		d.mu.Lock()
		delete(d.leases, mac)
		d.mu.Unlock()
	}
}

func (d *DhcpServer) bootFile(isIpxe bool, platform string) string {
	if d.cfg.SecureBoot {
		if platform == "efi" {
			return d.cfg.NativeEfiFile
		}
		return d.cfg.NativeBiosFile
	}
	if isIpxe {
		return d.cfg.MenuFile
	}
	return d.cfg.BootFileFor(platform)
}

func (d *DhcpServer) reply(s *dhcpSock, req []byte, addr *net.UDPAddr,
	reqType, respType byte, serverIP, yiaddr, mac string, chaddr []byte,
	htype byte, hlen int, xid uint32, flags uint16, giaddr, ciaddr,
	boot, hostname string, arch uint16, isIpxe bool, reqIP string) {

	proxy := d.cfg.ProxyDhcp
	platform := "bios"
	switch arch {
	case 6, 7, 8, 9, 10, 11:
		platform = "efi"
	}
	var opts []byte
	opts = append(opts, optMsgType, 1, respType)
	opts = append(opts, optServerID, 4)
	opts = append(opts, net.ParseIP(serverIP).To4()...)
	opts = append(opts, optVendorClass, 9)
	opts = append(opts, []byte("PXEClient")...) // RFC4578 必须带
	if boot != "" {
		opts = append(opts, optTFTP, byte(len(serverIP)))
		opts = append(opts, []byte(serverIP)...)
		opts = append(opts, optBootFile, byte(len(boot)))
		opts = append(opts, []byte(boot)...)
	}
	if !proxy {
		lease := make([]byte, 4)
		binary.BigEndian.PutUint32(lease, uint32(d.cfg.LeaseSeconds))
		opts = append(opts, optLease, 4)
		opts = append(opts, lease...)
		opts = append(opts, optMask, 4)
		opts = append(opts, net.ParseIP(d.cfg.SubnetMask).To4()...)
		if d.cfg.Gateway != "" {
			if gw := net.ParseIP(d.cfg.Gateway).To4(); gw != nil {
				opts = append(opts, optRouter, 4)
				opts = append(opts, gw...)
			}
		}
		var dns []byte
		cnt := 0
		for _, d0 := range []string{d.cfg.DNS1, d.cfg.DNS2} {
			if d0 == "" {
				continue
			}
			if ip := net.ParseIP(d0).To4(); ip != nil {
				dns = append(dns, ip...)
				cnt++
			}
		}
		if cnt > 0 {
			opts = append(opts, optDNS, byte(4*cnt))
			opts = append(opts, dns...)
		}
	}
	// 回显客户端 UUID（部分 PXE ROM 用它校验）
	if len(req) > 240 {
		if u, ok := parseDhcpOptions(req[240:])[optUUID]; ok && len(u) > 0 {
			opts = append(opts, optUUID, byte(len(u)))
			opts = append(opts, u...)
		}
	}
	// option 43：发现控制=0 + 引导服务器列表 1 台（本机）+ 空菜单
	vendor43 := []byte{0x06, 0x01, 0x00}
	vendor43 = append(vendor43, 0x08, 0x06, 0x01, 0x01)
	vendor43 = append(vendor43, net.ParseIP(serverIP).To4()...)
	vendor43 = append(vendor43, 0x09, 0x00, 0x0a, 0x00)
	opts = append(opts, optVendor, byte(len(vendor43)))
	opts = append(opts, vendor43...)
	// option 175：DP-PXE 实测结构
	opts = append(opts, optPXE175, 6, 0x01, 0x01, 0x01, 0x08, 0x01, 0x01)
	opts = append(opts, 0xFF)

	header := make([]byte, 236)
	header[0] = 2 // BOOTREPLY
	header[1] = htype
	header[2] = byte(hlen)
	binary.BigEndian.PutUint32(header[4:], xid)
	binary.BigEndian.PutUint16(header[10:], flags)
	copy(header[12:16], net.ParseIP(ciaddr).To4())
	copy(header[16:20], net.ParseIP(yiaddr).To4())
	copy(header[20:24], net.ParseIP(serverIP).To4())
	copy(header[24:28], net.ParseIP(giaddr).To4())
	copy(header[28:44], padTo(chaddr, 16))
	sname := d.cfg.DhcpSname
	if sname == "" {
		sname = "DPPXESERVER"
	}
	copy(header[44:108], padTo([]byte(sname), 64))
	copy(header[108:236], padTo([]byte(boot), 128))

	packet := append(header, []byte(magicCookie)...)
	packet = append(packet, opts...)

	type dst struct {
		ip   string
		port int
	}
	var targets []dst
	switch {
	case s.port == pxeBootServerPrc:
		targets = []dst{{addr.IP.String(), addr.Port}}
	case flags&0x8000 != 0 || yiaddr == "0.0.0.0" || giaddr != "0.0.0.0":
		targets = []dst{{dhcpBroadcast, 68}}
	default:
		targets = []dst{{yiaddr, 68}}
	}

	for _, t := range targets {
		ua := &net.UDPAddr{IP: net.ParseIP(t.ip), Port: t.port}
		if _, err := s.conn.WriteToUDP(packet, ua); err != nil {
			d.log(fmt.Sprintf("DHCP 回包发送失败 -> %s:%d: %v", t.ip, t.port, err), "err")
		}
	}

	aname := archNames[arch]
	if aname == "" {
		aname = fmt.Sprintf("arch%d", arch)
	}
	d.log(fmt.Sprintf("DHCP %s -> %s %s [%s] file=%s server=%s yi=%s -> %s:%d",
		dhcpMsgNames[reqType], dhcpMsgNames[respType], mac, aname, boot, serverIP,
		yiaddr, targets[0].ip, targets[0].port), "info")

	if d.onClient != nil {
		ip := yiaddr
		if ip == "0.0.0.0" {
			if reqIP != "" {
				ip = reqIP
			} else {
				ip = "-"
			}
		}
		d.onClient(mac, ip, isIpxe, platform)
	}

	d.mu.Lock()
	d.offerCounts[mac]++
	cnt := d.offerCounts[mac]
	d.mu.Unlock()
	if cnt == 3 {
		d.log(fmt.Sprintf("已连续 3 次给 %s 下发引导信息但没来取 %s："+
			"可能是 TFTP 未通（防火墙/端口占用），或客户端 IP 冲突", mac, boot), "warn")
	}
}

func (d *DhcpServer) alloc(mac string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if l, ok := d.leases[mac]; ok && l.Expires.After(time.Now()) {
		return l.IP
	}
	used := map[string]bool{}
	for _, l := range d.leases {
		used[l.IP] = true
	}
	start := ipToUint32(d.cfg.DhcpStart)
	end := ipToUint32(d.cfg.DhcpEnd)
	if end < start {
		start, end = end, start
	}
	for n := start; n <= end; n++ {
		ip := uint32ToIP(n)
		if !used[ip] {
			d.leases[mac] = &Lease{MAC: mac, IP: ip,
				Expires: time.Now().Add(time.Duration(d.cfg.LeaseSeconds) * time.Second)}
			return ip
		}
	}
	return d.cfg.DhcpStart
}

func (d *DhcpServer) remember(mac, ip, hostname string) {
	d.mu.Lock()
	d.leases[mac] = &Lease{MAC: mac, IP: ip, Hostname: hostname,
		Expires: time.Now().Add(time.Duration(d.cfg.LeaseSeconds) * time.Second)}
	d.mu.Unlock()
}

// LeaseCount 当前租约数（给 UI 显示用）。
func (d *DhcpServer) LeaseCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.leases)
}

func parseDhcpOptions(raw []byte) map[byte][]byte {
	out := map[byte][]byte{}
	i := 0
	for i < len(raw) {
		code := raw[i]
		if code == 255 {
			break
		}
		if code == 0 {
			i++
			continue
		}
		if i+1 >= len(raw) {
			break
		}
		ln := int(raw[i+1])
		if i+2+ln > len(raw) {
			break
		}
		out[code] = raw[i+2 : i+2+ln]
		i += 2 + ln
	}
	return out
}

// DiscoverInfo 解析出来的 PXE 请求信息（自检 / 诊断用）。
type DiscoverInfo struct {
	MAC      string
	MsgType  byte
	Vendor   string
	Arch     uint16
	Platform string
	IsIPXE   bool
	IsPXE    bool
	Hostname string
}

// ParseDiscover 解析一条 BOOTP/DHCP 请求报文（不依赖 socket，便于自检）。
func ParseDiscover(data []byte) (DiscoverInfo, bool) {
	var info DiscoverInfo
	if len(data) < 240 || string(data[236:240]) != magicCookie || data[0] != 1 {
		return info, false
	}
	hlen := int(data[2])
	if hlen < 6 {
		hlen = 6
	}
	if 28+hlen > len(data) {
		return info, false
	}
	info.MAC = formatMAC(data[28 : 28+hlen])
	opts := parseDhcpOptions(data[240:])
	if v, ok := opts[optMsgType]; ok && len(v) > 0 {
		info.MsgType = v[0]
	}
	info.Vendor = string(opts[optVendorClass])
	info.Hostname = string(opts[optHostname])
	info.Arch = 0xFFFF
	if a, ok := opts[optArch]; ok && len(a) == 2 {
		info.Arch = binary.BigEndian.Uint16(a)
	}
	info.Platform = "bios"
	switch info.Arch {
	case 6, 7, 8, 9, 10, 11:
		info.Platform = "efi"
	}
	vlow := strings.ToLower(strings.TrimSpace(info.Vendor))
	info.IsIPXE = strings.HasPrefix(vlow, "ipxe") || strings.Contains(info.Vendor, "iPXE") ||
		vlow == "pxeclient"
	info.IsPXE = strings.HasPrefix(vlow, "pxeclient") || strings.HasPrefix(vlow, "ipxe")
	return info, true
}

func formatMAC(b []byte) string {
	if len(b) < 6 {
		return "-"
	}
	parts := make([]string, 6)
	for i := 0; i < 6; i++ {
		parts[i] = fmt.Sprintf("%02X", b[i])
	}
	return strings.Join(parts, ":")
}

func padTo(b []byte, n int) []byte {
	out := make([]byte, n)
	copy(out, b)
	return out
}

func ipToUint32(ip string) uint32 {
	p := net.ParseIP(ip).To4()
	if p == nil {
		return 0
	}
	return binary.BigEndian.Uint32(p)
}

func uint32ToIP(n uint32) string {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, n)
	return net.IP(b).String()
}
