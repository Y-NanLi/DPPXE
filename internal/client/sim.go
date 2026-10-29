package client

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dppxe/internal/protocol"
)

// RunSim 被控端模拟器（控制台版）。
//
// 用途：在没有真实网启客户机的情况下，演示控制通道的完整交互。
// 它只"接收并记录"指令，**不会真的执行** diskpart / bcdboot / 关机等危险动作；
// 文件推送会真实落到 sim/received/ 目录。
func RunSim(server string, port int) error {
	if server == "" {
		server = "127.0.0.1"
	}
	recvDir := filepath.Join("sim", "received")
	_ = os.MkdirAll(recvDir, 0o755)

	say := func(format string, args ...any) {
		fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	}

	var conn *protocol.Conn
	var connect func() error
	connect = func() error {
		raw, err := dialTCP(server, port, 5*time.Second)
		if err != nil {
			return err
		}
		conn = protocol.NewConn(raw)
		mac := uint64(rand.Int63n(1<<40)) | (1 << 40)
		hello := protocol.New(protocol.OpHello)
		hello.WithArgs(1, 0, VersionWord, uint32(mac&0xFFFFFFFF), uint32((mac>>32)&0xFFFF))
		hello.Text = fmt.Sprintf("SIM-%03d", rand.Intn(999)+1)
		if err := conn.Send(hello); err != nil {
			return err
		}
		say("已连接 %s:%d（%s）", server, port, hello.Text)
		return nil
	}

	if err := connect(); err != nil {
		return fmt.Errorf("连接失败: %w", err)
	}
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()

	var cid uint32
	files := map[uint32]struct {
		path       string
		chunk, tot int
		got        int
	}{}

	for {
		msg, err := conn.Recv()
		if err != nil {
			say("连接中断: %v，3 秒后重连", err)
			time.Sleep(3 * time.Second)
			if err := connect(); err != nil {
				continue
			}
			continue
		}
		switch msg.Op {
		case protocol.OpHelloAck:
			cid = msg.Args[0]
			say("握手成功，客户端编号 #%d，服务端 %s", cid, msg.Text)
			logMsg := protocol.New(protocol.OpLog)
			logMsg.ClientID = cid
			logMsg.Text = fmt.Sprintf("客户端 #%d 上线（模拟器）", cid)
			_ = conn.Send(logMsg)
		case protocol.OpHeartbeat:
			ack := protocol.New(protocol.OpAck)
			ack.ClientID = cid
			ack.WithArgs(msg.Args[:]...)
			_ = conn.Send(ack)
		case protocol.OpFileSeed:
			job, size, chunk, total := msg.Args[0], msg.Args[1], int(msg.Args[2]), int(msg.Args[3])
			files[job] = struct {
				path       string
				chunk, tot int
				got        int
			}{path: msg.Text, chunk: chunk, tot: total}
			say("收到文件种子 %s（%d B / %d 片）", msg.Text, size, total)
			go func(job uint32, total int) {
				for idx := 0; idx < total; idx++ {
					req := protocol.New(protocol.OpFileReq)
					req.ClientID = cid
					req.WithArgs(job, uint32(idx))
					if err := conn.Send(req); err != nil {
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
			}(job, total)
		case protocol.OpFileChunk:
			job, idx, total := msg.Args[0], int(msg.Args[1]), int(msg.Args[2])
			info, ok := files[job]
			if !ok {
				continue
			}
			name := filepath.Base(info.path)
			target := filepath.Join(recvDir, name)
			fh, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o644)
			if err == nil {
				_, _ = fh.WriteAt(msg.Payload, int64(idx)*int64(info.chunk))
				_ = fh.Close()
			}
			info.got++
			files[job] = info
			if info.got >= total {
				say("文件接收完成 %s", name)
				done := protocol.New(protocol.OpFileDone)
				done.ClientID = cid
				done.Text = target
				_ = conn.Send(done)
			}
		case protocol.OpBye:
			say("服务端要求断开")
			return nil
		case protocol.OpAck:
			// 忽略
		default:
			detail := msg.Text
			if len(msg.Payload) > 0 {
				detail = string(msg.Payload)
			}
			say("收到 %s", msg.OpName())
			for i, line := range strings.Split(detail, "\n") {
				if i >= 12 {
					break
				}
				if strings.TrimSpace(line) != "" {
					fmt.Printf("    %s\n", strings.TrimRight(line, "\r"))
				}
			}
			report := protocol.New(protocol.OpLog)
			report.ClientID = cid
			report.Text = "已模拟执行 " + msg.OpName()
			_ = conn.Send(report)
		}
	}
}
