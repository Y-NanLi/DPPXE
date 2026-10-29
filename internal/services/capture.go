package services

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 内置抓包诊断：pktmon 采集 + pcap/pcapng 解析。
//
// 菜单「☰ → 抓包诊断」一键开始/停止，结束后自动把 DHCP/TFTP/HTTP 时间线写进日志，
// 不用再去命令行折腾 pktmon。

var dhcpMsgByCode = map[byte]string{
	1: "DISCOVER", 2: "OFFER", 3: "REQUEST", 4: "DECLINE",
	5: "ACK", 6: "NAK", 7: "RELEASE", 8: "INFORM",
}

var tftpOpByCode = map[uint16]string{
	1: "RRQ", 2: "WRQ", 3: "DATA", 4: "ACK", 5: "ERROR", 6: "OACK",
}

var archByCode = map[uint16]string{0: "BIOS", 6: "EFI32", 7: "EFI64", 9: "EFI64", 10: "ARM64"}

// CaptureAvailable 系统是否有 pktmon（Win10 1809+）。
func CaptureAvailable() bool {
	_, err := exec.LookPath("pktmon")
	return err == nil
}

// StartCapture 开始抓包。
func StartCapture(etl string) (bool, string) {
	if !CaptureAvailable() {
		return false, "系统没有 pktmon（需要 Win10 1809+）"
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	_, _ = run("pktmon", "stop")
	_, _ = run("pktmon", "filter", "remove")
	for _, f := range [][3]string{
		{"D67", "67", "udp"}, {"D68", "68", "udp"},
		{"T69", "69", "udp"}, {"H80", "80", "tcp"},
	} {
		_, _ = run("pktmon", "filter", "add", f[0], "-p", f[1], "-t", f[2])
	}
	out, err := run("pktmon", "start", "--capture", "--pkt-size", "0", "--file-name", etl)
	if err != nil {
		msg := strings.TrimSpace(out)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return false, msg
	}
	return true, "抓包已开始"
}

// StopCapture 停止抓包并转成 pcap。
func StopCapture(etl, pcap string) (bool, string) {
	run := func(args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	_, _ = run("pktmon", "stop")
	out, _ := run("pktmon", "etl2pcap", etl, "-o", pcap)
	if st, err := os.Stat(pcap); err == nil && st.Size() > 24 {
		return true, fmt.Sprintf("已生成 %s（%d 字节）", filepath.Base(pcap), st.Size())
	}
	msg := strings.TrimSpace(out)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if msg == "" {
		msg = "pcap 未生成"
	}
	return false, msg
}

// ---------------------------------------------------------------- 解析

type pcapPacket struct {
	TS       float64
	LinkType uint32
	Data     []byte
}

func readPcap(path string) ([]pcapPacket, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 24 {
		return nil, errors.New("文件太小")
	}
	if string(data[:4]) == "\x0a\x0d\x0d\x0a" {
		return readPcapng(data), nil
	}
	magic := binary.LittleEndian.Uint32(data[0:])
	var order binary.ByteOrder = binary.LittleEndian
	if magic != 0xA1B2C3D4 && magic != 0xA1B23C4D {
		order = binary.BigEndian
	}
	linkType := order.Uint32(data[20:])
	var out []pcapPacket
	pos := 24
	for pos+16 <= len(data) {
		ts := order.Uint32(data[pos:])
		tu := order.Uint32(data[pos+4:])
		caplen := order.Uint32(data[pos+8:])
		pos += 16
		if int(caplen) > len(data)-pos {
			break
		}
		out = append(out, pcapPacket{
			TS: float64(ts) + float64(tu)/1e6, LinkType: linkType,
			Data: data[pos : pos+int(caplen)],
		})
		pos += int(caplen)
	}
	return out, nil
}

func readPcapng(data []byte) []pcapPacket {
	var out []pcapPacket
	pos := 0
	var order binary.ByteOrder = binary.LittleEndian
	var linkTypes []uint32
	for pos+12 <= len(data) {
		btype := order.Uint32(data[pos:])
		blen := order.Uint32(data[pos+4:])
		if blen < 12 || pos+int(blen) > len(data) {
			break
		}
		blk := data[pos : pos+int(blen)]
		switch btype {
		case 0x0A0D0D0A:
			if binary.LittleEndian.Uint32(blk[8:]) == 0x1A2B3C4D {
				order = binary.LittleEndian
			} else {
				order = binary.BigEndian
			}
			linkTypes = nil
		case 1:
			linkTypes = append(linkTypes, uint32(order.Uint16(blk[8:])))
		case 6:
			iface := order.Uint32(blk[8:])
			tsh := order.Uint32(blk[12:])
			tsl := order.Uint32(blk[16:])
			caplen := order.Uint32(blk[20:])
			ts := float64((uint64(tsh)<<32)|uint64(tsl)) / 1e6
			lt := uint32(1)
			if int(iface) < len(linkTypes) {
				lt = linkTypes[iface]
			}
			end := 28 + int(caplen)
			if end > len(blk) {
				end = len(blk)
			}
			out = append(out, pcapPacket{TS: ts, LinkType: lt, Data: blk[28:end]})
		case 3:
			orig := order.Uint32(blk[8:])
			lt := uint32(1)
			if len(linkTypes) > 0 {
				lt = linkTypes[0]
			}
			end := 12 + int(orig)
			if end > len(blk) {
				end = len(blk)
			}
			out = append(out, pcapPacket{TS: 0, LinkType: lt, Data: blk[12:end]})
		}
		pos += int(blen)
	}
	return out
}

// stripEthernet 去掉链路层头，只留 IPv4。
func stripEthernet(pkt []byte, linkType uint32) []byte {
	if linkType == 101 { // raw IP
		return pkt
	}
	if len(pkt) < 14 {
		return nil
	}
	etype := binary.BigEndian.Uint16(pkt[12:])
	off := 14
	for (etype == 0x8100 || etype == 0x88A8) && len(pkt) >= off+4 {
		etype = binary.BigEndian.Uint16(pkt[off+2:])
		off += 4
	}
	if etype != 0x0800 {
		return nil
	}
	return pkt[off:]
}

func describeDhcp(p []byte) string {
	if len(p) < 240 || string(p[236:240]) != magicCookie {
		return ""
	}
	hlen := int(p[2])
	if hlen < 6 {
		hlen = 6
	}
	if 28+hlen > len(p) {
		return ""
	}
	mac := formatMAC(p[28 : 28+hlen])
	opts := parseDhcpOptions(p[240:])
	var mt byte
	if v, ok := opts[optMsgType]; ok && len(v) > 0 {
		mt = v[0]
	}
	name := dhcpMsgByCode[mt]
	if name == "" {
		name = fmt.Sprintf("%d", mt)
	}
	parts := []string{fmt.Sprintf("DHCP %-8s %s", name, mac)}
	if v, ok := opts[optRequestedIP]; ok && len(v) == 4 {
		parts = append(parts, "请求IP="+net.IP(v).String())
	}
	if v, ok := opts[optServerID]; ok && len(v) == 4 {
		parts = append(parts, "server="+net.IP(v).String())
	}
	if v, ok := opts[optArch]; ok && len(v) == 2 {
		a := binary.BigEndian.Uint16(v)
		an := archByCode[a]
		if an == "" {
			an = "?"
		}
		parts = append(parts, fmt.Sprintf("arch=%d(%s)", a, an))
	}
	if f := strings.TrimRight(string(p[108:236]), "\x00"); f != "" {
		parts = append(parts, "file="+f)
	}
	if yi := net.IP(p[16:20]).String(); yi != "0.0.0.0" {
		parts = append(parts, "yi="+yi)
	}
	return strings.Join(parts, "  ")
}

func describeTftp(p []byte) string {
	if len(p) < 4 {
		return ""
	}
	op := binary.BigEndian.Uint16(p)
	switch op {
	case 1, 2:
		name := strings.SplitN(string(p[2:]), "\x00", 2)[0]
		return "TFTP " + tftpOpByCode[op] + " " + name
	case 5:
		msg := strings.SplitN(string(p[4:]), "\x00", 2)[0]
		return "TFTP ERROR " + msg
	}
	return ""
}

func describeHTTP(p []byte) string {
	head := string(p)
	if i := strings.Index(head, "\r\n"); i >= 0 {
		head = head[:i]
	}
	if strings.HasPrefix(head, "GET") || strings.HasPrefix(head, "HEAD") ||
		strings.HasPrefix(head, "HTTP/") {
		return head
	}
	return ""
}

// AnalyzeCapture 把抓包内容整理成时间线写进日志。
func AnalyzeCapture(pcap string, log LogFunc) {
	pkts, err := readPcap(pcap)
	if err != nil {
		log(fmt.Sprintf("读取 %s 失败: %v", filepath.Base(pcap), err), "err")
		return
	}
	log(fmt.Sprintf("—— 抓包分析（%d 个包）——", len(pkts)), "info")
	type key struct {
		src, dst string
		sp, dp   int
		ln       int
		h        uint64
	}
	seen := map[key]bool{}
	var t0 float64
	if len(pkts) > 0 {
		t0 = pkts[0].TS
	}
	counts := map[string]int{"dhcp": 0, "tftp": 0, "http": 0}
	for _, pk := range pkts {
		ip := stripEthernet(pk.Data, pk.LinkType)
		if len(ip) < 20 || ip[9] != 17 {
			continue
		}
		ihl := int(ip[0]&0x0F) * 4
		if len(ip) < ihl+8 {
			continue
		}
		src := net.IP(ip[12:16]).String()
		dst := net.IP(ip[16:20]).String()
		sp := int(binary.BigEndian.Uint16(ip[ihl:]))
		dp := int(binary.BigEndian.Uint16(ip[ihl+2:]))
		ul := int(binary.BigEndian.Uint16(ip[ihl+4:]))
		end := ihl + ul
		if ul < 8 || end > len(ip) {
			end = len(ip)
		}
		pl := ip[ihl+8 : end]
		k := key{src, dst, sp, dp, len(pl), hashBytes(pl)}
		if seen[k] {
			continue
		}
		seen[k] = true

		text := ""
		switch {
		case sp == 67 || dp == 67 || sp == 68 || dp == 68:
			text = describeDhcp(pl)
			if text != "" {
				counts["dhcp"]++
			}
		case sp == 69 || dp == 69:
			text = describeTftp(pl)
			if text != "" {
				counts["tftp"]++
			}
		case sp == 80 || dp == 80:
			text = describeHTTP(pl)
			if text != "" {
				counts["http"]++
			}
		}
		if text != "" {
			log(fmt.Sprintf("+%6.2fs %s:%d -> %s:%d  %s", pk.TS-t0, src, sp, dst, dp, text), "info")
		}
	}
	log(fmt.Sprintf("抓包统计：DHCP %d / TFTP %d / HTTP %d",
		counts["dhcp"], counts["tftp"], counts["http"]), "info")
	if counts["tftp"] == 0 {
		log("结论：客户端从未发起 TFTP —— 说明它没拿到/没用上我们下发的引导文件名。", "warn")
	} else if counts["http"] == 0 {
		log("结论：有 TFTP 但无 HTTP —— 已进入原生引导流程（WIM 也走 TFTP 属正常）。", "info")
	}
}

func hashBytes(b []byte) uint64 {
	var h uint64 = 1469598103934665603
	n := len(b)
	if n > 64 {
		n = 64
	}
	for i := 0; i < n; i++ {
		h ^= uint64(b[i])
		h *= 1099511628211
	}
	return h
}

// CaptureFileNames 返回 logs 目录下的固定产物路径。
func CaptureFileNames(logDir string) (etl, pcap string) {
	return filepath.Join(logDir, "run.etl"), filepath.Join(logDir, "run.pcap")
}

// CaptureNow 时间戳后缀（给文件名用）。
func CaptureNow() string { return time.Now().Format("20060102_1504") }
