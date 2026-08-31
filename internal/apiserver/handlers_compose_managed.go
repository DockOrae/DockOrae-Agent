// 面板托管 compose 执行端点(§11:面板管 YAML/业务,Agent 执行)。
// up/build/pull 为 NDJSON 流式输出;start/stop/restart/down 为同步 {ok, output};
// logs 为 WebSocket 流。
package apiserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strconv"

	"github.com/gorilla/websocket"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// managedComposeReq compose 操作请求(yaml + 附加文件 + 可选参数)
type managedComposeReq struct {
	Project string            `json:"project"`
	Yaml    string            `json:"yaml"`
	Files   map[string]string `json:"files"` // 相对路径 → base64
}

func (s *Server) managedCompose(c *Ctx, args ...string) error {
	var req managedComposeReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Project == "" {
		return errs.New(errs.INVALID_REQUEST, "project 不能为空")
	}
	if err := s.Locks.Acquire(oplockKey("compose-"+req.Project), c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplockKey("compose-" + req.Project))
	start := now()
	res, err := s.ManagedCompose.Run(req.Project, req.Yaml, req.Files, args...)
	c.Audit("compose."+args[0], req.Project, start, err, "", map[string]any{"args": args})
	if err != nil {
		return err
	}
	c.OK(res)
	return nil
}

func oplockKey(k string) string { return k }

// handleComposeManagedUp 流式执行 compose up/build/pull(NDJSON 每行一条)
func (s *Server) handleComposeManagedUp(c *Ctx) error {
	var req managedComposeReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	args := []string{"up", "-d", "--remove-orphans"}
	if a := c.Query("args"); a != "" {
		switch a {
		case "build":
			args = []string{"up", "-d", "--build"}
		case "recreate":
			args = []string{"up", "-d", "--force-recreate", "--pull", "always"}
		case "pull":
			args = []string{"up", "-d", "--pull", "always"}
		}
	}
	if err := s.Locks.Acquire(oplockKey("compose-"+req.Project), c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplockKey("compose-" + req.Project))
	start := now()
	err := s.streamCompose(c, req, args...)
	c.Audit("compose.up", req.Project, start, err, "", nil)
	return err
}

// handleComposeManagedPull 流式执行 compose pull
func (s *Server) handleComposeManagedPull(c *Ctx) error {
	var req managedComposeReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := s.Locks.Acquire(oplockKey("compose-"+req.Project), c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplockKey("compose-" + req.Project))
	start := now()
	err := s.streamCompose(c, req, "pull", "--quiet")
	c.Audit("compose.pull", req.Project, start, err, "", nil)
	return err
}

// streamCompose 写项目文件并流式执行,输出转 NDJSON
func (s *Server) streamCompose(c *Ctx, req managedComposeReq, args ...string) error {
	if req.Project == "" {
		return errs.New(errs.INVALID_REQUEST, "project 不能为空")
	}
	if _, err := s.ManagedCompose.WriteProject(req.Project, req.Yaml, req.Files); err != nil {
		return err
	}
	cmd := s.ManagedCompose.ComposeCmd(req.Project, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errs.Newf(errs.COMPOSE_ERROR, "compose 管道失败: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return errs.Newf(errs.COMPOSE_ERROR, "compose 管道失败: %v", err)
	}
	if err := cmd.Start(); err != nil {
		return errs.Newf(errs.COMPOSE_ERROR, "compose 启动失败: %v", err)
	}
	c.W.Header().Set("Content-Type", "application/x-ndjson")
	c.W.WriteHeader(200)
	flusher, _ := c.W.(interface{ Flush() })
	write := func(line string) {
		b, _ := json.Marshal(line)
		_, _ = c.W.Write([]byte(`{"type":"line","data":` + string(b) + "}\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	done := make(chan bool, 2)
	go scanComposeLines(stdout, write, done)
	go scanComposeLines(stderr, write, done)
	<-done
	<-done
	err = cmd.Wait()
	if err == nil {
		_, _ = c.W.Write([]byte(`{"type":"done","ok":true}` + "\n"))
	} else {
		_, _ = c.W.Write([]byte(`{"type":"done","ok":false,"error":"compose.failed"}` + "\n"))
	}
	return nil
}

func scanComposeLines(r io.Reader, write func(string), done chan<- bool) {
	defer func() { done <- true }()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		write(sc.Text())
	}
}

// handleComposeManagedStart/Stop/Restart/Down 同步执行
func (s *Server) handleComposeManagedStart(c *Ctx) error {
	return s.managedCompose(c, "start")
}
func (s *Server) handleComposeManagedStop(c *Ctx) error {
	return s.managedCompose(c, "stop")
}
func (s *Server) handleComposeManagedRestart(c *Ctx) error {
	return s.managedCompose(c, "restart")
}
func (s *Server) handleComposeManagedDown(c *Ctx) error {
	args := []string{"down"}
	if c.Query("volumes") == "1" || c.Query("volumes") == "true" {
		args = append(args, "-v")
	}
	return s.managedCompose(c, args...)
}
func (s *Server) handleComposeManagedBuild(c *Ctx) error {
	return s.managedCompose(c, "build")
}

// handleComposeManagedRun 同步执行 up/pull(应用商店安装/升级;动作与参数固定 allowlist)
func (s *Server) handleComposeManagedRun(c *Ctx) error {
	var req struct {
		Project string            `json:"project"`
		Yaml    string            `json:"yaml"`
		Files   map[string]string `json:"files"`
		Action  string            `json:"action"` // up | pull
		Mode    string            `json:"mode"`   // "" | recreate(up 专用:--force-recreate --pull always)
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Project == "" {
		return errs.New(errs.INVALID_REQUEST, "project 不能为空")
	}
	var args []string
	switch req.Action {
	case "up":
		args = []string{"up", "-d", "--remove-orphans"}
		if req.Mode == "recreate" {
			args = []string{"up", "-d", "--force-recreate", "--pull", "always"}
		}
	case "pull":
		args = []string{"pull", "--quiet"}
	default:
		return errs.Newf(errs.INVALID_REQUEST, "不支持的 compose 动作: %s", req.Action)
	}
	if err := s.Locks.Acquire(oplockKey("compose-"+req.Project), c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplockKey("compose-" + req.Project))
	start := now()
	res, err := s.ManagedCompose.Run(req.Project, req.Yaml, req.Files, args...)
	c.Audit("compose."+req.Action, req.Project, start, err, "", map[string]any{"mode": req.Mode})
	if err != nil {
		return err
	}
	c.OK(res)
	return nil
}

// handleComposeManagedLogsWS compose 日志流(WebSocket)
func (s *Server) handleComposeManagedLogsWS(c *Ctx, conn *websocket.Conn) error {
	tail := c.Query("tail")
	if tail == "" {
		tail = "300"
	}
	if _, err := strconv.Atoi(tail); err != nil {
		tail = "300"
	}
	cmd, err := s.ManagedCompose.LogsCmd(c.Query("project"), tail)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
		_ = conn.WriteMessage(websocket.CloseMessage, nil)
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("compose.logsFailed"))
		_ = conn.WriteMessage(websocket.CloseMessage, nil)
		return nil
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("compose.logsFailed"))
		_ = conn.WriteMessage(websocket.CloseMessage, nil)
		return nil
	}
	_, cancel := context.WithCancel(c.R.Context())
	defer cancel()
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			if conn.WriteMessage(websocket.TextMessage, []byte(sc.Text())) != nil {
				break
			}
		}
		cancel()
	}()
	wsPump(c, conn, func(mt int, data []byte) bool {
		return mt != websocket.CloseMessage
	})
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	_ = conn.WriteMessage(websocket.CloseMessage, nil)
	return nil
}
