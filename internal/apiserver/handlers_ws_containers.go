// 容器 WebSocket 端点:日志 / 统计 / 终端(§7;与面板侧协议完全一致)。
// 协议约定:
//   - logs:    TEXT 消息(每行一条,stdcopy 解复用后文本)
//   - stats:   TEXT 消息(原始 stats JSON 帧,字段计算由面板业务层完成)
//   - terminal:TEXT 控制("resize:W,H" / "stop" / 原始输入)+ BINARY 输出(原始字节)
package apiserver

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// handleContainerLogsWS 容器日志流(WebSocket)
func (s *Server) handleContainerLogsWS(c *Ctx, conn *websocket.Conn) error {
	id := c.Param("id")
	tail := int64(500)
	if t := c.Query("tail"); t != "" {
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			tail = n
		}
	}
	cli, err := s.Docker.Client()
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("[docker client failed: "+err.Error()+"]"))
		_ = conn.WriteMessage(websocket.CloseMessage, nil)
		return nil
	}
	ctx, cancel := context.WithCancel(c.R.Context())
	defer cancel()
	logs, err := cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       strconv.FormatInt(tail, 10),
	})
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("[logs failed: "+err.Error()+"]"))
		_ = conn.WriteMessage(websocket.CloseMessage, nil)
		return nil
	}
	defer logs.Close()

	w := wsTextWriter{conn: conn}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = stdcopy.StdCopy(w, w, logs)
		cancel()
	}()
	wsPump(c, conn, func(mt int, data []byte) bool {
		return mt != websocket.CloseMessage
	})
	cancel()
	logs.Close()
	wg.Wait()
	_ = conn.WriteMessage(websocket.CloseMessage, nil)
	return nil
}

// wsTextWriter 每段写入一条 TEXT 消息
type wsTextWriter struct {
	conn *websocket.Conn
}

func (w wsTextWriter) Write(p []byte) (int, error) {
	if err := w.conn.WriteMessage(websocket.TextMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// handleContainerStatsWS 容器 stats 流(原始帧透传,计算在面板)
func (s *Server) handleContainerStatsWS(c *Ctx, conn *websocket.Conn) error {
	id := c.Param("id")
	cli, err := s.Docker.Client()
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"docker client failed"}`))
		_ = conn.WriteMessage(websocket.CloseMessage, nil)
		return nil
	}
	ctx, cancel := context.WithCancel(c.R.Context())
	defer cancel()
	stats, err := cli.ContainerStats(ctx, id, client.ContainerStatsOptions{Stream: true})
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"`+err.Error()+`"}`))
		_ = conn.WriteMessage(websocket.CloseMessage, nil)
		return nil
	}
	defer stats.Body.Close()

	dec := json.NewDecoder(stats.Body)
	for {
		var frame json.RawMessage
		if err := dec.Decode(&frame); err != nil {
			break
		}
		if conn.WriteMessage(websocket.TextMessage, frame) != nil {
			cancel()
			break
		}
	}
	cancel()
	_ = conn.WriteMessage(websocket.CloseMessage, nil)
	return nil
}

// handleContainerTerminalWS 容器终端(exec TTY;协议与面板旧实现一致)
func (s *Server) handleContainerTerminalWS(c *Ctx, conn *websocket.Conn) error {
	id := c.Param("id")
	shell := "/bin/sh"
	if q := c.Query("shell"); q != "" {
		shell = q
	}
	cli, err := s.Docker.Client()
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("[exec failed: "+err.Error()+"]\r\n"))
		return nil
	}
	ctx, cancel := context.WithCancel(c.R.Context())
	defer cancel()

	execRes, err := cli.ExecCreate(ctx, id, client.ExecCreateOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		TTY:          true,
		Cmd:          []string{shell},
	})
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("[exec failed: "+err.Error()+"]\r\n"))
		return nil
	}
	attach, err := cli.ExecAttach(ctx, execRes.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("[exec failed: "+err.Error()+"]\r\n"))
		return nil
	}
	defer attach.Close()

	// exec 输出 → ws 二进制
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := attach.Reader.Read(buf)
			if n > 0 {
				if conn.WriteMessage(websocket.BinaryMessage, buf[:n]) != nil {
					cancel()
					return
				}
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()

	// ws → exec 输入 + resize/stop 控制协议(单读方,避免并发读)
	wsPump(c, conn, func(mt int, data []byte) bool {
		switch mt {
		case websocket.BinaryMessage:
			if _, err := attach.Conn.Write(data); err != nil {
				return false
			}
		case websocket.TextMessage:
			text := string(data)
			if strings.HasPrefix(text, "resize:") {
				parts := strings.SplitN(strings.TrimPrefix(text, "resize:"), ",", 2)
				if len(parts) == 2 {
					w, err1 := strconv.Atoi(parts[0])
					h, err2 := strconv.Atoi(parts[1])
					if err1 == nil && err2 == nil {
						_, _ = cli.ExecResize(ctx, execRes.ID, client.ExecResizeOptions{Width: uint(w), Height: uint(h)})
					}
				}
			} else if text == "stop" {
				return false
			} else {
				_, _ = attach.Conn.Write(data)
			}
		case websocket.CloseMessage:
			return false
		}
		return true
	})
	cancel()
	attach.Close()
	wg.Wait()
	_ = conn.WriteMessage(websocket.CloseMessage, nil)
	return nil
}

var _ = io.EOF
