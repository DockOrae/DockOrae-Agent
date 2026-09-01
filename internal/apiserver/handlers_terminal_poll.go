// 宿主终端 HTTP 长轮询端点(2026-09-02 长轮询重构):
//
//	POST /v1/host/terminal                 → 打开会话(owner/rows/columns)→ Snapshot{id,offset,...}
//	GET  /v1/host/terminal/{id}/output     → 长轮询输出(owner/offset/wait≤1500ms)→ Output{data,nextOffset,...}
//	POST /v1/host/terminal/{id}/input      → 输入(base64 data)
//	POST /v1/host/terminal/{id}/resize     → 调整尺寸(rows/columns)
//	POST /v1/host/terminal/{id}/close      → 关闭会话
//
// 彻底替代旧 WS 终端(连接不稳定根因)。数据经环形缓冲 + offset 游标,
// 空闲时 poll 阻塞最长 1500ms,天然免疫 WS 超时/热循环问题。
package apiserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/terminal"
)

const (
	terminalMaxWaitMS = 1500
)

type terminalOpenInput struct {
	Owner   string `json:"owner"`
	Rows    uint16 `json:"rows"`
	Columns uint16 `json:"columns"`
	Cwd     string `json:"cwd,omitempty"`
}

type terminalInput struct {
	Owner string `json:"owner"`
	Data  string `json:"data"`
}

type terminalResizeInput struct {
	Owner   string `json:"owner"`
	Rows    uint16 `json:"rows"`
	Columns uint16 `json:"columns"`
}

// handleTerminalOperationRoute 生成带固定 action 的路由适配器(路由参数 id 从 Ctx.Params 取)
func (s *Server) handleTerminalOperationRoute(action string) Handler {
	return func(c *Ctx) error {
		return s.handleTerminalOperation(c, c.Param("id"), action)
	}
}

// handleTerminalOpen POST /v1/host/terminal
func (s *Server) handleTerminalOpen(c *Ctx) error {
	if s.Term == nil {
		return c.err(http.StatusServiceUnavailable, "terminal_unavailable", "Terminal service unavailable")
	}
	if c.R.URL.RawQuery != "" {
		return c.err(http.StatusBadRequest, "invalid_terminal_request", "Invalid terminal request")
	}
	var input terminalOpenInput
	if err := c.Bind(&input); err != nil {
		return err
	}
	var snapshot terminal.Snapshot
	var err error
	if strings.TrimSpace(input.Cwd) != "" {
		snapshot, err = s.Term.OpenWithCWD(input.Owner, input.Rows, input.Columns, input.Cwd)
	} else {
		snapshot, err = s.Term.Open(input.Owner, input.Rows, input.Columns)
	}
	if err != nil {
		return s.writeTerminalError(c, err)
	}
	return c.rawJSON(http.StatusCreated, snapshot)
}

// handleTerminalOperation 处理 /v1/host/terminal/{id}/{action}
func (s *Server) handleTerminalOperation(c *Ctx, id, action string) error {
	if s.Term == nil {
		return c.err(http.StatusServiceUnavailable, "terminal_unavailable", "Terminal service unavailable")
	}
	if action != "output" && c.R.URL.RawQuery != "" {
		return c.err(http.StatusBadRequest, "invalid_terminal_request", "Invalid terminal request")
	}
	switch action {
	case "output":
		if c.R.Method != http.MethodGet {
			return c.err(http.StatusMethodNotAllowed, "method_not_allowed", "Request method not allowed")
		}
		return s.terminalOutput(c, id)
	case "input":
		if c.R.Method != http.MethodPost {
			return c.err(http.StatusMethodNotAllowed, "method_not_allowed", "Request method not allowed")
		}
		var input terminalInput
		if err := c.Bind(&input); err != nil {
			return err
		}
		data, err := decodeTerminalInput(input.Data)
		if err != nil {
			return s.writeTerminalError(c, errors.New("invalid terminal input"))
		}
		if err := s.Term.Input(input.Owner, id, data); err != nil {
			return s.writeTerminalError(c, err)
		}
		return c.rawJSON(http.StatusOK, map[string]bool{"accepted": true})
	case "resize":
		if c.R.Method != http.MethodPost {
			return c.err(http.StatusMethodNotAllowed, "method_not_allowed", "Request method not allowed")
		}
		var input terminalResizeInput
		if err := c.Bind(&input); err != nil {
			return err
		}
		if err := s.Term.Resize(input.Owner, id, input.Rows, input.Columns); err != nil {
			return s.writeTerminalError(c, err)
		}
		return c.rawJSON(http.StatusOK, map[string]bool{"accepted": true})
	case "close":
		if c.R.Method != http.MethodPost {
			return c.err(http.StatusMethodNotAllowed, "method_not_allowed", "Request method not allowed")
		}
		var input struct {
			Owner string `json:"owner"`
		}
		if err := c.Bind(&input); err != nil {
			return err
		}
		if err := s.Term.Close(input.Owner, id); err != nil {
			return s.writeTerminalError(c, err)
		}
		return c.rawJSON(http.StatusOK, map[string]bool{"closed": true})
	default:
		return c.err(http.StatusNotFound, "not_found", "Terminal route not found")
	}
}

func (s *Server) terminalOutput(c *Ctx, id string) error {
	query := c.R.URL.Query()
	if len(query) > 3 || len(query["owner"]) != 1 || len(query["offset"]) != 1 || len(query["wait"]) != 1 {
		return c.err(http.StatusBadRequest, "invalid_terminal_query", "Invalid terminal query")
	}
	offset, err := strconv.ParseInt(query.Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		return s.writeTerminalError(c, terminal.ErrOffset)
	}
	waitMilliseconds, err := strconv.Atoi(query.Get("wait"))
	if err != nil || waitMilliseconds < 0 || waitMilliseconds > terminalMaxWaitMS {
		return s.writeTerminalError(c, errors.New("invalid terminal wait"))
	}
	ctx, cancel := context.WithTimeout(c.R.Context(), time.Duration(waitMilliseconds+250)*time.Millisecond)
	defer cancel()
	output, err := s.Term.Output(ctx, query.Get("owner"), id, offset, time.Duration(waitMilliseconds)*time.Millisecond)
	if err != nil {
		return s.writeTerminalError(c, err)
	}
	return c.rawJSON(http.StatusOK, output)
}

func (s *Server) writeTerminalError(c *Ctx, err error) error {
	switch {
	case errors.Is(err, terminal.ErrNotFound):
		return c.err(http.StatusNotFound, "terminal_not_found", "Terminal session not found")
	case errors.Is(err, terminal.ErrLimit):
		return c.err(http.StatusTooManyRequests, "terminal_limit", "Terminal session limit reached")
	case errors.Is(err, terminal.ErrClosed):
		return c.err(http.StatusConflict, "terminal_closed", "Terminal session is closed")
	case errors.Is(err, terminal.ErrOffset):
		return c.err(http.StatusBadRequest, "terminal_invalid", "Terminal output offset is invalid")
	default:
		return c.err(http.StatusBadRequest, "terminal_invalid", "Terminal request failed: "+err.Error())
	}
}

func decodeTerminalInput(value string) ([]byte, error) {
	data, err := base64.RawStdEncoding.DecodeString(value)
	if err == nil {
		return data, nil
	}
	return base64.StdEncoding.DecodeString(value)
}

// rawJSON 裸 JSON 响应(终端协议数据不走统一 ok 信封)
func (c *Ctx) rawJSON(status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return errs.New(errs.INTERNAL, "响应序列化失败")
	}
	c.W.Header().Set("Content-Type", "application/json")
	c.W.WriteHeader(status)
	_, _ = c.W.Write(b)
	return nil
}

// err 结构化错误响应(与 Fail 信封一致,可指定状态码)
func (c *Ctx) err(status int, code, message string) error {
	return errs.New(code, message).WithStatus(status)
}
