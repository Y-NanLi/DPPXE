package webui

import (
	"encoding/json"
	"net/http"
	"strconv"

	"dppxe/internal/client"
)

// ClientState 被控端界面轮询用的状态。
type ClientState struct {
	Info    client.Info       `json:"info"`
	Logs    []client.LogEntry `json:"logs"`
	LastSeq int64             `json:"last_seq"`
}

// ClientAPI 被控端界面的本地接口：
//
//	GET  /api/client/state?since=<seq>
//	POST /api/client/cmd  {cmd: minimize|close|reconnect}
//
// windowAction 由 main 注入（无边框窗口的最小化 / 关闭走 Wails runtime）。
func ClientAPI(cli *client.Client, windowAction func(action string)) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/client/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		logs := cli.Logs()
		writeJSON(w, ClientState{
			Info:    cli.Info(),
			Logs:    logs.Since(since),
			LastSeq: logs.Last(),
		})
	})

	mux.HandleFunc("/api/client/cmd", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Cmd string `json:"cmd"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body.Cmd {
		case "minimize", "close":
			if windowAction != nil {
				windowAction(body.Cmd)
			}
		case "reconnect":
			// 断线时本来就会自动重连，这里只给用户一个"我手动催一下"的入口
			cli.Log("手动触发重连…")
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	return mux
}

// ClientAssets 把 index.html 指向被控端页面，其余静态资源照常提供。
func ClientAssets() http.Handler {
	files := http.FileServer(http.FS(Dist()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			r.URL.Path = "/client.html"
		}
		files.ServeHTTP(w, r)
	})
}
