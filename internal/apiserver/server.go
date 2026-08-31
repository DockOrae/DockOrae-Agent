// Package apiserver Agent 的 Unix Socket API 服务。
// 仅监听 Unix Socket(默认 /run/dockorae/agent.sock),绝不监听 TCP(§4)。
package apiserver

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/audit"
	"github.com/DockOrae/DockOrae-Agent/internal/binary"
	"github.com/DockOrae/DockOrae-Agent/internal/compose"
	"github.com/DockOrae/DockOrae-Agent/internal/config"
	"github.com/DockOrae/DockOrae-Agent/internal/disk"
	"github.com/DockOrae/DockOrae-Agent/internal/docker"
	"github.com/DockOrae/DockOrae-Agent/internal/firewall"
	"github.com/DockOrae/DockOrae-Agent/internal/host"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
	"github.com/DockOrae/DockOrae-Agent/internal/network"
	"github.com/DockOrae/DockOrae-Agent/internal/oplock"
	"github.com/DockOrae/DockOrae-Agent/internal/swap"
	"github.com/DockOrae/DockOrae-Agent/internal/sysctl"
	"github.com/DockOrae/DockOrae-Agent/internal/system"
)

// Handler 处理函数签名:返回 error 即按统一错误信封输出
type Handler func(c *Ctx) error

// Server Agent API 服务(依赖容器)
type Server struct {
	Cfg      *config.Config
	Exec     *hostexec.Execer
	Locks    *oplock.Manager
	Audit    *audit.Logger
	BinState *binary.State

	Host     *host.Service
	System   *system.Service
	Swap     *swap.Service
	Docker   *docker.Service
	Compose  *compose.Service
	Disk     *disk.Service
	Sysctl   *sysctl.Service
	Firewall *firewall.Service
	Network  *network.Service

	httpSrv   *http.Server
	routes    map[string]Handler // key: "METHOD path"
	Version   string
	Commit    string
	BuildTime string
}

// New 构造服务
func New(cfg *config.Config) (*Server, error) {
	aud, err := audit.New(cfg.LogDir)
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	exec := hostexec.New(cfg.InContainer, cfg.ComposeBin)
	s := &Server{
		Cfg:      cfg,
		Exec:     exec,
		Locks:    oplock.New(),
		Audit:    aud,
		BinState: binary.NewState(cfg),
		Host:     host.New(exec),
		System:   system.New(exec),
		Swap:     swap.New(exec),
		Docker:   docker.New(exec),
		Compose:  compose.New(exec, cfg),
		Disk:     disk.New(exec),
		Sysctl:   sysctl.New(exec),
		Firewall: firewall.New(exec),
		Network:  network.New(exec),
		routes:   make(map[string]Handler),
	}
	s.registerRoutes()
	return s, nil
}

// Run 启动 Unix Socket 服务(阻塞)
func (s *Server) Run() error {
	// 清理残留 socket 文件(上次异常退出可能遗留)
	if st, err := os.Lstat(s.Cfg.SocketPath); err == nil {
		if st.Mode()&os.ModeSocket != 0 {
			_ = os.Remove(s.Cfg.SocketPath)
		}
	}
	ln, err := net.Listen("unix", s.Cfg.SocketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.Cfg.SocketPath, 0o660); err != nil {
		_ = ln.Close()
		return err
	}
	s.httpSrv = &http.Server{
		Handler:           s.router(),
		ReadHeaderTimeout: 15 * time.Second,
	}
	return s.httpSrv.Serve(ln)
}

// Shutdown 优雅退出
func (s *Server) Shutdown(ctx context.Context) {
	if s.httpSrv != nil {
		_ = s.httpSrv.Shutdown(ctx)
	}
	s.Audit.Close()
}

// register 注册路由(METHOD + 路径)
func (s *Server) register(method, path string, h Handler) {
	s.routes[method+" "+path] = h
}

// router 构造 HTTP handler(认证 + 请求 ID + 路由分发 + 兜底错误)
func (s *Server) router() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. 认证(§41:所有 API 必须经过认证)
		if !s.authorize(w, r) {
			return
		}
		// 2. 请求 ID(§43)
		reqID := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if reqID == "" || len(reqID) > 64 {
			reqID = randomID()
		}
		user := strings.TrimSpace(r.Header.Get("X-Agent-User"))
		if len(user) > 64 {
			user = user[:64]
		}
		c := &Ctx{W: w, R: r, RequestID: reqID, User: user, S: s}
		c.W.Header().Set("X-Request-Id", reqID)

		// 3. 路由
		key := r.Method + " " + r.URL.Path
		h, ok := s.routes[key]
		if !ok {
			c.Fail(errNotFound())
			return
		}
		if err := h(c); err != nil {
			c.Fail(err)
		}
	})
}

// authorize Bearer token 认证(常量时间比较,防时序侧信道)
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		writeRawError(w, http.StatusUnauthorized, "UNAUTHORIZED", "缺少认证 token")
		return false
	}
	token := strings.TrimSpace(auth[len(prefix):])
	if !constantTimeEqual(token, s.Cfg.Token) {
		writeRawError(w, http.StatusUnauthorized, "UNAUTHORIZED", "认证 token 无效")
		return false
	}
	return true
}

func writeRawError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"ok":false,"error":{"code":%q,"message":%q}}`, code, msg)
}

// 供 handler 引用,避免循环导入
func (s *Server) logf(format string, args ...any) {
	log.Printf(format, args...)
}
