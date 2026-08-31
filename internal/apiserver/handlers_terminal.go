// 宿主终端 WebSocket 端点(§56):一个连接 = 一个 PTY 会话。
// 协议与容器终端一致:
//   - 服务端 → 客户端:BINARY(终端输出字节);首个 TEXT 为会话信息 JSON {id,shell,cwd,cols,rows}
//   - 客户端 → 服务端:BINARY(键盘输入);TEXT "resize:W,H" 调整尺寸;TEXT "stop" 关闭
package apiserver

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

// handleHostTerminalWS 宿主终端(PTY;bash 优先,sh 回退;经 nsenter 进入宿主命名空间)
func (s *Server) handleHostTerminalWS(c *Ctx, conn *websocket.Conn) error {
	cols, _ := strconv.Atoi(c.Query("cols"))
	rows, _ := strconv.Atoi(c.Query("rows"))
	cwd := c.Query("cwd")
	shell := c.Query("shell")

	sess, err := s.Term.Create(shell, cwd, cols, rows)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("[terminal failed: "+err.Error()+"]\r\n"))
		return nil
	}
	defer s.Term.Close(sess.ID)

	// 会话信息首帧
	if b, err := json.Marshal(map[string]any{
		"id": sess.ID, "shell": sess.Shell, "cwd": sess.CWD, "cols": sess.Cols, "rows": sess.Rows,
	}); err == nil {
		_ = conn.WriteMessage(websocket.TextMessage, b)
	}

	// PTY 输出 → WS 二进制(专属读取方,不持锁)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 8192)
		for {
			n, err := sess.PTMX().Read(buf)
			if n > 0 {
				if conn.WriteMessage(websocket.BinaryMessage, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WS → PTY 输入 + resize/stop 控制协议(单读方,避免并发读)
	wsPump(c, conn, func(mt int, data []byte) bool {
		switch mt {
		case websocket.BinaryMessage:
			if _, err := sess.Write(data); err != nil {
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
						_ = sess.Resize(w, h)
					}
				}
			} else if text == "stop" {
				return false
			} else if !strings.HasPrefix(text, "{") {
				// 非控制文本(粘贴等)写入输入
				if _, err := sess.Write(data); err != nil {
					return false
				}
			}
		case websocket.CloseMessage:
			return false
		}
		return true
	})
	wg.Wait()
	return nil
}
