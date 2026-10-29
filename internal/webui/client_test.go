package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dppxe/internal/client"
)

// TestClientStateAPI 校验被控端界面拿到的 JSON 结构（界面每 400ms 轮询它）。
func TestClientStateAPI(t *testing.T) {
	cli := client.New("127.0.0.1", 6003, true, "TEST-PC", true, client.Callbacks{})
	cli.Log("正在自动检测控制端…")
	cli.Log("握手成功，机器号 003")

	srv := httptest.NewServer(ClientAPI(cli, func(string) {}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/client/state?since=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var st ClientState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("解析状态失败: %v", err)
	}
	if st.Info.No != "未知" {
		t.Errorf("未连接时机器号应为「未知」，实际 %q", st.Info.No)
	}
	if st.Info.Connected {
		t.Error("未握手时 connected 应为 false")
	}
	if st.Info.ClientVer == "" || st.Info.Name != "TEST-PC" {
		t.Errorf("版本/主机名缺失: %+v", st.Info)
	}
	if len(st.Logs) != 2 || st.LastSeq != 2 {
		t.Fatalf("应拿到 2 条日志，实际 %d 条（last_seq=%d）", len(st.Logs), st.LastSeq)
	}
	if !strings.Contains(st.Logs[0].Text, "自动检测") {
		t.Errorf("日志内容不对: %q", st.Logs[0].Text)
	}
	if len(st.Logs[0].TS) != 11 { // HH:MM:SS.cc
		t.Errorf("时间戳格式不对: %q", st.Logs[0].TS)
	}

	// 增量：不重复拿旧日志
	resp2, _ := http.Get(srv.URL + "/api/client/state?since=2")
	var st2 ClientState
	_ = json.NewDecoder(resp2.Body).Decode(&st2)
	resp2.Body.Close()
	if len(st2.Logs) != 0 {
		t.Errorf("since 之后不应有新日志，实际 %d 条", len(st2.Logs))
	}

	cli.Log("上报磁盘：HD0 ST1000VX013 932GB GPT")
	resp3, _ := http.Get(srv.URL + "/api/client/state?since=2")
	var st3 ClientState
	_ = json.NewDecoder(resp3.Body).Decode(&st3)
	resp3.Body.Close()
	if len(st3.Logs) != 1 || st3.LastSeq != 3 {
		t.Errorf("增量日志不对: %d 条 / last_seq=%d", len(st3.Logs), st3.LastSeq)
	}
}

// TestClientPageServed 校验被控端页面已内嵌，且 "/" 就是它。
func TestClientPageServed(t *testing.T) {
	srv := httptest.NewServer(ClientAssets())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	for _, want := range []string{"机器号", "客户端IP", "服务端IP", "client.js", "client.css"} {
		if !strings.Contains(html, want) {
			t.Errorf("被控端页面缺少 %q", want)
		}
	}

	// 静态资源也要能取到
	for _, path := range []string{"/client.js", "/client.css"} {
		r, err := http.Get(srv.URL + path)
		if err != nil || r.StatusCode != 200 {
			t.Errorf("%s 取不到（%v）", path, err)
		}
		if r != nil {
			r.Body.Close()
		}
	}
}
