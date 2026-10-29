// Package webui 内嵌前端资源与本地 API。
package webui

import (
	"bytes"
	"embed"
	"encoding/binary"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"

	"dppxe/internal/server"
)

//go:embed all:dist
var assets embed.FS

// Dist 返回 dist 子目录（给 Wails 的 AssetServer 用）。
func Dist() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// APIHandler 处理 /api/state 与 /api/cmd。
//
// 前端只依赖这两个接口，所以同一套页面既能跑在 Wails(WebView2) 里，
// 也能跑在普通浏览器里（--web 模式）。
func APIHandler(s *server.Server) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		writeJSON(w, s.State(since))
	})

	// 内置 VNC 查看器：把脏矩形按二进制帧发给前端（JSON 装不下这么大的像素流）
	//   帧格式：u32 seq | u16 count | count * (u16 x,y,w,h + u32 len + RGBA 像素)
	mux.HandleFunc("/api/vnc/frame", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		seq, rects := s.VncFrame()
		if len(rects) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var buf bytes.Buffer
		_ = binary.Write(&buf, binary.LittleEndian, uint32(seq)) // 必须 4 字节，前端按 u32 读
		_ = binary.Write(&buf, binary.LittleEndian, uint16(len(rects)))
		for _, rc := range rects {
			for _, v := range []int{rc.X, rc.Y, rc.W, rc.H} {
				_ = binary.Write(&buf, binary.LittleEndian, uint16(v))
			}
			_ = binary.Write(&buf, binary.LittleEndian, uint32(len(rc.Pixels)))
			buf.Write(rc.Pixels)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(buf.Bytes())
	})

	mux.HandleFunc("/api/cmd", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Cmd  string         `json:"cmd"`
			Args map[string]any `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, server.Result{OK: false, Msg: "参数解析失败: " + err.Error()})
			return
		}
		res := s.Command(body.Cmd, server.Args(body.Args))
		writeJSON(w, res)
	})

	return mux
}

// BrowserHandler 浏览器模式：静态资源 + API。
func BrowserHandler(s *server.Server) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", APIHandler(s))
	mux.Handle("/", http.FileServer(http.FS(Dist())))
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
