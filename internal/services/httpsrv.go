package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dppxe/internal/config"
)

// HttpFileServer HTTP 服务（默认 80），根目录 = netboot。
//
// 用途与真实软件一致：
//   - iPXE 从这里取 wimboot / BCD / boot.sdi / *.wim（menu.txt 里的 http://${next-server}/...）
//   - 客户端从这里下载镜像、客户端更新包、Tool\TightVNC
//
// 支持 Range 请求（wimboot 与 WIM 下载都要用），并统计传输量给 UI 显示。
type HttpFileServer struct {
	cfg  *config.Config
	log  LogFunc
	onKB func(total int64, delta int64)

	srv      *http.Server
	ln       net.Listener
	root     string
	menuPath string

	totalBytes atomic.Int64
	mu         sync.Mutex
	running    bool
}

// NewHttpFileServer 创建 HTTP 服务。
func NewHttpFileServer(cfg *config.Config, log LogFunc, onBytes func(total, delta int64)) *HttpFileServer {
	return &HttpFileServer{cfg: cfg, log: log, onKB: onBytes}
}

// Start 监听端口。
func (h *HttpFileServer) Start() error {
	root := h.cfg.RootDir
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	h.root = root
	h.menuPath = config.ResolveIn(root, h.cfg.MenuFile)

	ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", h.cfg.HttpPort))
	if err != nil {
		return fmt.Errorf("绑定 TCP %d 失败: %w", h.cfg.HttpPort, err)
	}
	h.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.serve)
	h.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 15 * time.Second}
	h.mu.Lock()
	h.running = true
	h.mu.Unlock()
	go func() {
		if err := h.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.log("HTTP 服务退出: "+err.Error(), "err")
		}
	}()
	h.log(fmt.Sprintf("HTTP 服务已启动 http://%s:%d/ -> %s",
		h.cfg.ServerIP, h.cfg.HttpPort, root), "ok")
	return nil
}

// Stop 关闭服务。
func (h *HttpFileServer) Stop() {
	h.mu.Lock()
	running := h.running
	h.running = false
	h.mu.Unlock()
	if running && h.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = h.srv.Shutdown(ctx)
		cancel()
	}
	if h.ln != nil {
		_ = h.ln.Close()
		h.ln = nil
	}
	h.log("HTTP 服务已停止", "warn")
}

// TotalBytes 累计发送字节数。
func (h *HttpFileServer) TotalBytes() int64 { return h.totalBytes.Load() }

func (h *HttpFileServer) serve(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimLeft(strings.SplitN(r.URL.Path, "?", 2)[0], "/")
	local := filepath.Join(h.root, filepath.FromSlash(rel))
	abs, err := filepath.Abs(local)
	if err != nil || !strings.HasPrefix(strings.ToLower(abs), strings.ToLower(h.root)) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	st, err := os.Stat(abs)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if st.IsDir() {
		idx := filepath.Join(abs, "index.html")
		if config.FileExists(idx) {
			abs = idx
			if s2, err := os.Stat(abs); err == nil {
				st = s2
			}
		} else {
			http.Error(w, "Directory listing disabled", http.StatusForbidden)
			return
		}
	}

	var content []byte
	isMenu := h.menuPath != "" && samePath(abs, h.menuPath)
	if isMenu {
		content = config.RenderMenu(abs, h.cfg.ServerIP)
	}

	rec := &countingWriter{ResponseWriter: w, onWrite: func(n int) {
		h.totalBytes.Add(int64(n))
		if h.onKB != nil {
			h.onKB(h.totalBytes.Load(), int64(n))
		}
	}}
	w = rec
	w.Header().Set("Content-Type", guessType(abs))
	w.Header().Set("Accept-Ranges", "bytes")

	if isMenu {
		http.ServeContent(w, r, path.Base(abs), time.Time{}, bytes.NewReader(content))
	} else {
		fh, err := os.Open(abs)
		if err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		defer fh.Close()
		http.ServeContent(w, r, path.Base(abs), st.ModTime(), fh)
	}

	code := rec.status
	if code == 0 {
		code = http.StatusOK
	}
	h.log(fmt.Sprintf("HTTP %d %s <- %s", code, r.URL.Path, clientIP(r)), "info")
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type countingWriter struct {
	http.ResponseWriter
	status  int
	onWrite func(int)
}

func (c *countingWriter) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	if n > 0 && c.onWrite != nil {
		c.onWrite(n)
	}
	return n, err
}

func guessType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".wim":
		return "application/x-ms-wim"
	case ".txt", ".md":
		return "text/plain; charset=utf-8"
	case ".ini", ".cfg", ".log":
		return "text/plain"
	case ".xml":
		return "application/xml"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

// ParseRangeHeader 解析 Range 头（给自检用）。
func ParseRangeHeader(spec string, size int64) (int64, int64, bool) {
	if !strings.HasPrefix(spec, "bytes=") {
		return 0, size - 1, false
	}
	part := strings.TrimSpace(strings.SplitN(spec[6:], ",", 2)[0])
	a, b, found := strings.Cut(part, "-")
	if !found {
		return 0, size - 1, false
	}
	start, end := int64(0), size-1
	if a != "" {
		v, err := strconv.ParseInt(a, 10, 64)
		if err != nil {
			return 0, size - 1, false
		}
		start = v
		if b != "" {
			if e, err := strconv.ParseInt(b, 10, 64); err == nil {
				end = e
			}
		}
	} else if b != "" {
		if n, err := strconv.ParseInt(b, 10, 64); err == nil {
			start = size - n
			if start < 0 {
				start = 0
			}
		}
	}
	if start > size-1 {
		start = size - 1
	}
	if end > size-1 {
		end = size - 1
	}
	if end < start {
		end = start
	}
	return start, end, true
}
