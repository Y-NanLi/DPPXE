package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"dppxe/internal/config"
)

// DiscoveryProbe 被控端广播探测串。
var DiscoveryProbe = []byte("DPPXE_DISCOVER_V1")

// DiscoveryPort 服务发现端口。
const DiscoveryPort = 6004

// DiscoveryInfo 服务端发现应答。
type DiscoveryInfo struct {
	IP      string `json:"ip"`
	Port    int    `json:"port"`
	Version string `json:"version"`
	Name    string `json:"name"`
}

// DiscoveryResponder 服务端发现：UDP 广播应答，让被控端"自动检测控制端"。
//
// 被控端向 255.255.255.255:6004 广播 DPPXE_DISCOVER_V1，
// 本服务回一个 JSON：{ip, port, version, name}。这样客户端不用预先知道服务端 IP。
type DiscoveryResponder struct {
	cfg     *config.Config
	version string
	log     LogFunc
	conn    *net.UDPConn
	stop    chan struct{}
	wg      sync.WaitGroup
}

// NewDiscoveryResponder 创建发现服务。
func NewDiscoveryResponder(cfg *config.Config, version string, log LogFunc) *DiscoveryResponder {
	return &DiscoveryResponder{cfg: cfg, version: version, log: log}
}

// Start 绑定 6004 并开始应答。
func (d *DiscoveryResponder) Start() error {
	conn, err := listenUDPBroadcast("", DiscoveryPort)
	if err != nil {
		return fmt.Errorf("绑定 UDP %d 失败: %w", DiscoveryPort, err)
	}
	d.conn = conn
	d.stop = make(chan struct{})
	d.wg.Add(1)
	go d.loop()
	d.log(fmt.Sprintf("服务发现已启动（UDP %d，被控端可自动找到本机）", DiscoveryPort), "ok")
	return nil
}

// Stop 停止应答。
func (d *DiscoveryResponder) Stop() {
	if d.stop != nil {
		select {
		case <-d.stop:
		default:
			close(d.stop)
		}
	}
	if d.conn != nil {
		_ = d.conn.Close()
		d.conn = nil
	}
	d.wg.Wait()
	d.log("服务发现已停止", "warn")
}

func (d *DiscoveryResponder) loop() {
	defer d.wg.Done()
	buf := make([]byte, 1024)
	for {
		select {
		case <-d.stop:
			return
		default:
		}
		_ = d.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, addr, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return
		}
		if n < len(DiscoveryProbe) || string(buf[:len(DiscoveryProbe)]) != string(DiscoveryProbe) {
			continue
		}
		host, _ := os.Hostname()
		payload, _ := json.Marshal(DiscoveryInfo{
			IP: d.cfg.ServerIP, Port: d.cfg.ControlPort,
			Version: d.version, Name: host,
		})
		if _, err := d.conn.WriteToUDP(payload, addr); err != nil {
			continue
		}
		d.log(fmt.Sprintf("被控端发现请求 <- %s，已回复 %s", addr.IP, d.cfg.ServerIP), "info")
	}
}

// ProbeServer 被控端用：广播找服务端，返回应答或 nil。
func ProbeServer(timeout time.Duration) *DiscoveryInfo {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil
	}
	defer conn.Close()
	setBroadcast(conn)

	if _, err := conn.WriteToUDP(DiscoveryProbe,
		&net.UDPAddr{IP: net.IPv4bcast, Port: DiscoveryPort}); err != nil {
		return nil
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return nil
		}
		var info DiscoveryInfo
		if err := json.Unmarshal(buf[:n], &info); err != nil {
			continue
		}
		if info.IP != "" {
			return &info
		}
	}
}
