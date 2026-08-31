// Package terminal 宿主 PTY 终端会话(§56)。
//
// 每个 WebSocket 连接 = 一个独立会话:
//   - Direct 模式:直接在宿主(或本机测试环境)spawn 交互式 shell;
//   - Nsenter 模式:经 `nsenter -t 1 -m -u -i -n -- <shell> -c 'cd <cwd> && exec <shell>'`
//     进入宿主 PID/挂载/UTS/IPC/网络命名空间 —— PTY 从属端 fd 经 exec 继承,
//     会话进程即宿主进程(ps 可见),vim/top/htop/ssh 等交互程序全部可用。
//
// Shell 探测:优先 /bin/bash,不存在回退 /bin/sh(宿主侧 test -x 探测)。
package terminal

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/creack/pty"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// DefaultShells 探测顺序
var DefaultShells = []string{"/bin/bash", "/bin/sh"}

// Session 单个终端会话
type Session struct {
	ID    string `json:"id"`
	Shell string `json:"shell"`
	CWD   string `json:"cwd"`
	Cols  uint16 `json:"cols"`
	Rows  uint16 `json:"rows"`

	cmd  *exec.Cmd
	ptmx *os.File
	mu   sync.Mutex
}

// Manager 会话管理器(并发安全)
type Manager struct {
	exec *hostexec.Execer
	mu   sync.Mutex
	live map[string]*Session
}

// NewManager 构造会话管理器
func NewManager(exec *hostexec.Execer) *Manager {
	return &Manager{exec: exec, live: make(map[string]*Session)}
}

// DetectShell 探测宿主可用 shell(bash 优先,sh 回退)
func (m *Manager) DetectShell() string {
	for _, sh := range DefaultShells {
		if _, err := m.exec.Output("test", "-x", sh); err == nil {
			return sh
		}
	}
	return "/bin/sh"
}

// Create 创建会话并启动 shell(交互式,经 nsenter 进入宿主命名空间)
func (m *Manager) Create(shell, cwd string, cols, rows int) (*Session, error) {
	if shell == "" {
		shell = m.DetectShell()
	}
	if cwd == "" {
		cwd = "/root" // root 用户默认目录(§28)
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	if !strings.HasPrefix(shell, "/") {
		return nil, errs.New(errs.INVALID_REQUEST, "shell 必须为绝对路径")
	}

	var cmd *exec.Cmd
	if strings.TrimSpace(cwd) != "" {
		// cd 由 shell 自身在宿主命名空间内完成(Agent 进程 cwd 属于自身挂载命名空间,不能直接 Dir)
		cdCmd := "cd -- " + hostexec.Quote(cwd) + " && exec " + shell
		cmd = m.exec.Command(shell, "-c", cdCmd)
	} else {
		cmd = m.exec.Command(shell)
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, errs.Newf(errs.PTY_UNAVAILABLE, "启动宿主终端失败: %v", err)
	}
	sess := &Session{
		ID:    randomID(),
		Shell: shell,
		CWD:   cwd,
		Cols:  uint16(cols),
		Rows:  uint16(rows),
		cmd:   cmd,
		ptmx:  ptmx,
	}
	m.mu.Lock()
	m.live[sess.ID] = sess
	m.mu.Unlock()
	return sess, nil
}

// Get 按 ID 取会话
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.live[id]
	return s, ok
}

// Resize 调整终端尺寸
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ptmx == nil {
		return errs.New(errs.TERMINAL_SESSION, "会话已关闭")
	}
	s.Cols, s.Rows = uint16(cols), uint16(rows)
	return pty.Setsize(s.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// Write 写入输入(键盘/粘贴);短操作持锁,不阻塞
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ptmx == nil {
		return 0, errs.New(errs.TERMINAL_SESSION, "会话已关闭")
	}
	return s.ptmx.Write(p)
}

// PTMX 原始主端。输出读取由专属 goroutine 直接使用(不持锁,避免阻塞输入),
// 会话关闭时 ptmx 被 Close,读取方随即返回错误退出。
func (s *Session) PTMX() *os.File { return s.ptmx }

// Close 关闭会话:杀进程 + 关闭 PTY + 从管理器移除
func (m *Manager) Close(id string) {
	m.mu.Lock()
	s, ok := m.live[id]
	if ok {
		delete(m.live, id)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
	s.mu.Lock()
	if s.ptmx != nil {
		_ = s.ptmx.Close()
		s.ptmx = nil
	}
	s.mu.Unlock()
}

// Count 存活会话数
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.live)
}

// CloseAll 关闭全部会话(服务退出)
func (m *Manager) CloseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.live))
	for id := range m.live {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Close(id)
	}
}

// randomID 会话 ID(crypto/rand,失败 panic:安全标识生成失败不可静默降级)
func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
