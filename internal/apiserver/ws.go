// WebSocket 支持:容器日志/统计/终端、compose 日志、docker 事件。
// 全部 WS 端点同样经 Bearer token 认证(router 中先 authorize 再升级)。
package apiserver

import (
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// wsRoute WS 路由(pattern 支持 {param} 段)
type wsRoute struct {
	method  string
	pattern string
	handler func(c *Ctx, conn *websocket.Conn) error
}

// match 匹配 method + 路径,返回路径参数(与普通路由同一 {param} 语法)
func (r *wsRoute) match(method, path string) (map[string]string, bool) {
	if r.method != method {
		return nil, false
	}
	pat := strings.Split(strings.Trim(r.pattern, "/"), "/")
	seg := strings.Split(strings.Trim(path, "/"), "/")
	if len(pat) != len(seg) {
		return nil, false
	}
	params := map[string]string{}
	for i := range pat {
		if strings.HasPrefix(pat[i], "{") && strings.HasSuffix(pat[i], "}") {
			params[pat[i][1:len(pat[i])-1]] = seg[i]
			continue
		}
		if pat[i] != seg[i] {
			return nil, false
		}
	}
	return params, true
}

// wsPump 读取 WS 消息直到连接关闭或 onMsg 返回 false。
// 读循环在独立 goroutine;ctx 取消时主动关闭连接解除 ReadMessage 阻塞,保证及时退出。
// 不用 SetReadDeadline+超时轮询:gorilla 会把读超时记入 readErr,后续 ReadMessage
// 不再阻塞直接返回同一错误,外层 continue 形成热循环,1000 次后 panic
// ("repeated read on failed websocket connection")。
func wsPump(c *Ctx, conn *websocket.Conn, onMsg func(mt int, data []byte) bool) {
	ctx := c.R.Context()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if !onMsg(mt, data) {
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		_ = conn.Close()
		<-done
	case <-done:
	}
}
