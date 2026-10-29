package client

import (
	"fmt"
	"sync"
	"time"

	"dppxe/internal/config"
)

// LogEntry 一条客户端日志（带序号，界面按序号增量拉取）。
type LogEntry struct {
	Seq  int64  `json:"seq"`
	TS   string `json:"ts"`
	Text string `json:"text"`
}

// LogRing 环形日志缓冲（被控端界面 / 服务端回传都用它）。
type LogRing struct {
	mu    sync.Mutex
	seq   int64
	items []LogEntry
	max   int
}

// NewLogRing 创建日志缓冲。
func NewLogRing(max int) *LogRing {
	if max <= 0 {
		max = 500
	}
	return &LogRing{max: max}
}

// Add 追加一条日志。
func (r *LogRing) Add(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	now := time.Now()
	r.items = append(r.items, LogEntry{
		Seq:  r.seq,
		TS:   fmt.Sprintf("%02d:%02d:%02d.%02d", now.Hour(), now.Minute(), now.Second(), now.Nanosecond()/1e7),
		Text: text,
	})
	if len(r.items) > r.max {
		r.items = r.items[len(r.items)-r.max:]
	}
}

// Since 取 seq 之后的日志。
func (r *LogRing) Since(seq int64) []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seq >= r.seq {
		return nil
	}
	idx := 0
	for idx < len(r.items) && r.items[idx].Seq <= seq {
		idx++
	}
	out := make([]LogEntry, len(r.items)-idx)
	copy(out, r.items[idx:])
	return out
}

// Last 最新序号。
func (r *LogRing) Last() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// Info 界面需要的状态快照。
type Info struct {
	No        string `json:"no"`         // 机器号（"未知" 表示还没握手）
	ClientIP  string `json:"client_ip"`  // 本机 IP
	ServerIP  string `json:"server_ip"`  // 服务端 IP
	ServerVer string `json:"server_ver"` // 服务端版本
	ClientVer string `json:"client_ver"` // 本机版本
	Progress  int    `json:"progress"`   // 文件传输百分比
	Connected bool   `json:"connected"`  // 是否已握手
	Name      string `json:"name"`       // 主机名
	Mode      string `json:"mode"`       // PE / 系统
}

// Info 返回当前状态快照（线程安全）。
func (c *Client) Info() Info {
	c.infoMu.RLock()
	defer c.infoMu.RUnlock()
	info := c.info
	info.ClientVer = config.ClientVersion
	if info.Mode == "" {
		if IsPE() {
			info.Mode = "PE"
		} else {
			info.Mode = "系统"
		}
	}
	return info
}

// Logs 返回日志缓冲（界面用）。
func (c *Client) Logs() *LogRing { return c.logRing }

// formatVersion 把握手里的 (主,次,修订) 拼成人类可读版本号：
// 1.0.0 → "1.0"，1.6.9 → "1.6.9"。
func formatVersion(major, minor, patch uint32) string {
	switch {
	case minor == 0 && patch == 0:
		return fmt.Sprintf("%d.0", major)
	case patch == 0:
		return fmt.Sprintf("%d.%d", major, minor)
	default:
		return fmt.Sprintf("%d.%d.%d", major, minor, patch)
	}
}

func (c *Client) setInfo(fn func(*Info)) {
	c.infoMu.Lock()
	fn(&c.info)
	c.infoMu.Unlock()
}
