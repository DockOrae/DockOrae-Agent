// Docker 事件 WebSocket 流(面板事件中心/通知订阅;断线由面板侧重连)。
package apiserver

import (
	"context"
	"encoding/json"

	"github.com/gorilla/websocket"
	"github.com/moby/moby/client"
)

// handleDockerEventsWS 实时推送 docker 事件(每帧一条 JSON TEXT 消息)
func (s *Server) handleDockerEventsWS(c *Ctx, conn *websocket.Conn) error {
	cli, err := s.Docker.Client()
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"error":"docker client failed"}`))
		_ = conn.WriteMessage(websocket.CloseMessage, nil)
		return nil
	}
	ctx, cancel := context.WithCancel(c.R.Context())
	defer cancel()
	res := cli.Events(ctx, client.EventsListOptions{})
	for {
		select {
		case m, ok := <-res.Messages:
			if !ok {
				_ = conn.WriteMessage(websocket.CloseMessage, nil)
				return nil
			}
			raw, _ := json.Marshal(m)
			if conn.WriteMessage(websocket.TextMessage, raw) != nil {
				cancel()
				return nil
			}
		case <-ctx.Done():
			return nil
		}
	}
}
