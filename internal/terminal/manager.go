package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
	"github.com/DockOrae/DockOrae-Agent/internal/hostpty"
)

const (
	DefaultBufferBytes      = 1 << 20
	DefaultMaxSessions      = 16
	DefaultMaxOwnerSessions = 4
	DefaultIdleTimeout      = 30 * time.Minute
	DefaultLifetime         = 8 * time.Hour
	MaxInputBytes           = 16 << 10
	MaxOutputBytes          = 64 << 10
)

var (
	ErrNotFound = errors.New("terminal session not found")
	ErrLimit    = errors.New("terminal session limit reached")
	ErrClosed   = errors.New("terminal session is closed")
	ErrOffset   = errors.New("terminal output offset is invalid")
)

type Process interface {
	io.ReadWriteCloser
	Wait() error
	Kill() error
	Resize(rows, columns uint16) error
}

type Starter func(rows, columns uint16) (Process, error)

type Config struct {
	Starter          Starter
	ParentUnit       string
	BufferBytes      int
	MaxSessions      int
	MaxOwnerSessions int
	IdleTimeout      time.Duration
	Lifetime         time.Duration
	Now              func() time.Time
	// DefaultCWD 默认工作目录(空 = /root;DockOrae 无此字段,DockOrae 扩展:
	// 文件管理器「在终端打开」需指定目录)
	DefaultCWD string
}

type Manager struct {
	mu       sync.RWMutex
	config   Config
	execer   *hostexec.Execer // DockOrae 扩展:nsenter 启动器(供 OpenWithCWD 闭包复用)
	sessions map[string]*session
	stop     chan struct{}
	stopOnce sync.Once
	closed   bool
}

type session struct {
	mu        sync.Mutex
	id        string
	owner     string
	process   Process
	buffer    []byte
	base      int64
	next      int64
	notify    chan struct{}
	createdAt time.Time
	updatedAt time.Time
	exitedAt  *time.Time
	exitError string
	closed    bool
}

type Snapshot struct {
	ID        string     `json:"id"`
	Offset    int64      `json:"offset"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	ExitedAt  *time.Time `json:"exitedAt,omitempty"`
	ExitError string     `json:"exitError,omitempty"`
	Closed    bool       `json:"closed"`
}

type Output struct {
	Data       []byte     `json:"data"`
	Offset     int64      `json:"offset"`
	NextOffset int64      `json:"nextOffset"`
	Truncated  bool       `json:"truncated"`
	ExitedAt   *time.Time `json:"exitedAt,omitempty"`
	ExitError  string     `json:"exitError,omitempty"`
	Closed     bool       `json:"closed"`
}

func New(config Config) *Manager {
	if config.Starter == nil {
		config.Starter = defaultStarter(config.ParentUnit, config.DefaultCWD, nil)
	}
	if config.BufferBytes <= 0 {
		config.BufferBytes = DefaultBufferBytes
	}
	if config.MaxSessions <= 0 {
		config.MaxSessions = DefaultMaxSessions
	}
	if config.MaxOwnerSessions <= 0 {
		config.MaxOwnerSessions = DefaultMaxOwnerSessions
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = DefaultIdleTimeout
	}
	if config.Lifetime <= 0 {
		config.Lifetime = DefaultLifetime
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	manager := &Manager{config: config, sessions: make(map[string]*session), stop: make(chan struct{})}
	go manager.reapLoop()
	return manager
}

// NewWithExec 构造管理器,Starter 经 execer(nsenter 前缀)在宿主命名空间启动 shell
func NewWithExec(execer *hostexec.Execer, config Config) *Manager {
	// 必须显式用带 execer 的 starter:若交给 New() 的 nil fallback,
	// shell 会在容器命名空间启动(hostname=容器ID,ls 看到容器 /root)
	config.Starter = defaultStarter(config.ParentUnit, config.DefaultCWD, execer)
	manager := New(config)
	manager.execer = execer
	return manager
}

func defaultStarter(parentUnit, defaultCWD string, execer *hostexec.Execer) Starter {
	return func(rows, columns uint16) (Process, error) {
		return starterWithExec(rows, columns, defaultCWD, execer)
	}
}

func starterWithExec(rows, columns uint16, cwd string, execer *hostexec.Execer) (Process, error) {
	shell := "/bin/bash"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}
	directory := "/root"
	if strings.TrimSpace(cwd) != "" {
		directory = strings.TrimSpace(cwd)
	} else if root, err := os.Open("/root"); err == nil {
		if info, statErr := root.Stat(); statErr == nil && info.IsDir() {
			directory = "/root"
		}
		_ = root.Close()
	}
	environment := terminalEnvironment(shell)
	var command *exec.Cmd
	if execer != nil {
		command = execer.Command(shell, "-l")
	} else {
		command = exec.Command(shell, "-l")
	}
	command.Dir = directory
	command.Env = environment
	return hostpty.Start(command, rows, columns)
}

func terminalEnvironment(shell string) []string {
	return []string{
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"HOME=/root",
		"USER=root",
		"LOGNAME=root",
		"SHELL=" + shell,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8",
	}
}

// Open 打开会话(DockOrae 同款签名)
func (m *Manager) Open(owner string, rows, columns uint16) (Snapshot, error) {
	return m.open(owner, rows, columns, m.config.DefaultCWD)
}

// OpenWithCWD 打开会话并指定工作目录(DockOrae 扩展:文件管理器「在终端打开」)
func (m *Manager) OpenWithCWD(owner string, rows, columns uint16, cwd string) (Snapshot, error) {
	return m.open(owner, rows, columns, cwd)
}

func (m *Manager) open(owner string, rows, columns uint16, cwd string) (Snapshot, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" || rows == 0 || columns == 0 || rows > 500 || columns > 1000 {
		return Snapshot{}, errors.New("invalid terminal session request")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Snapshot{}, ErrClosed
	}
	ownerCount := 0
	activeCount := 0
	for _, item := range m.sessions {
		if item.isActive() {
			activeCount++
		}
		if item.owner == owner && item.isActive() {
			ownerCount++
		}
	}
	if activeCount >= m.config.MaxSessions || ownerCount >= m.config.MaxOwnerSessions {
		m.mu.Unlock()
		return Snapshot{}, ErrLimit
	}
	// Starter 闭包注入 cwd(DockOrae 扩展:文件管理器「在终端打开」指定目录)
	starter := m.config.Starter
	if strings.TrimSpace(cwd) != "" {
		execer := m.execer
		starter = func(rows, columns uint16) (Process, error) {
			return starterWithExec(rows, columns, cwd, execer)
		}
	}
	process, err := starter(rows, columns)
	if err != nil {
		m.mu.Unlock()
		return Snapshot{}, err
	}
	id, err := randomID()
	if err != nil {
		_ = process.Close()
		_ = process.Kill()
		m.mu.Unlock()
		return Snapshot{}, err
	}
	now := m.config.Now().UTC()
	item := &session{id: id, owner: owner, process: process, notify: make(chan struct{}), createdAt: now, updatedAt: now}
	m.sessions[id] = item
	m.mu.Unlock()
	go m.capture(item)
	return item.snapshot(), nil
}

func (m *Manager) capture(item *session) {
	buffer := make([]byte, 32<<10)
	for {
		read, err := item.process.Read(buffer)
		if read > 0 {
			item.append(buffer[:read], m.config.BufferBytes, m.config.Now().UTC())
		}
		if err != nil {
			if !hostpty.IsEnd(err) && !errors.Is(err, os.ErrClosed) {
				item.setExit(err, m.config.Now().UTC())
			}
			break
		}
	}
	err := item.process.Wait()
	item.setExit(err, m.config.Now().UTC())
}

func (m *Manager) Output(ctx context.Context, owner, id string, offset int64, wait time.Duration) (Output, error) {
	item, err := m.lookup(owner, id)
	if err != nil {
		return Output{}, err
	}
	if wait < 0 || wait > 1500*time.Millisecond {
		return Output{}, errors.New("invalid terminal wait duration")
	}
	for {
		output, notify, ready, err := item.output(offset, MaxOutputBytes, m.config.Now().UTC())
		if err != nil || ready || wait == 0 {
			return output, err
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return Output{}, ctx.Err()
		case <-notify:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			return item.currentOutput(offset, MaxOutputBytes, m.config.Now().UTC())
		}
	}
}

func (m *Manager) Input(owner, id string, data []byte) error {
	if len(data) == 0 || len(data) > MaxInputBytes {
		return errors.New("invalid terminal input size")
	}
	item, err := m.lookup(owner, id)
	if err != nil {
		return err
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.closed || item.exitedAt != nil {
		return ErrClosed
	}
	_, err = item.process.Write(data)
	if err == nil {
		item.updatedAt = m.config.Now().UTC()
	}
	return err
}

func (m *Manager) Resize(owner, id string, rows, columns uint16) error {
	if rows == 0 || columns == 0 || rows > 500 || columns > 1000 {
		return errors.New("invalid terminal dimensions")
	}
	item, err := m.lookup(owner, id)
	if err != nil {
		return err
	}
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.closed || item.exitedAt != nil {
		return ErrClosed
	}
	if err := item.process.Resize(rows, columns); err != nil {
		return err
	}
	item.updatedAt = m.config.Now().UTC()
	return nil
}

func (m *Manager) Close(owner, id string) error {
	item, err := m.lookup(owner, id)
	if err != nil {
		return err
	}
	item.mu.Lock()
	if item.closed {
		item.mu.Unlock()
		return nil
	}
	item.closed = true
	item.updatedAt = m.config.Now().UTC()
	close(item.notify)
	item.notify = make(chan struct{})
	item.mu.Unlock()
	_ = item.process.Close()
	if err := item.process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	m.closed = true
	items := make([]*session, 0, len(m.sessions))
	for _, item := range m.sessions {
		items = append(items, item)
	}
	m.mu.Unlock()
	m.stopOnce.Do(func() { close(m.stop) })
	for _, item := range items {
		_ = m.Close(item.owner, item.id)
	}
}

func (m *Manager) reapLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.reap(m.config.Now().UTC())
		case <-m.stop:
			return
		}
	}
}

func (m *Manager) reap(now time.Time) {
	type staleSession struct{ owner, id string }
	stale := make([]staleSession, 0)
	m.mu.Lock()
	for id, item := range m.sessions {
		item.mu.Lock()
		inactive := now.Sub(item.updatedAt) >= m.config.IdleTimeout || now.Sub(item.createdAt) >= m.config.Lifetime
		finished := item.closed || item.exitedAt != nil
		if finished && now.Sub(item.updatedAt) >= 5*time.Minute {
			delete(m.sessions, id)
		} else if inactive && !finished {
			stale = append(stale, staleSession{owner: item.owner, id: id})
		}
		item.mu.Unlock()
	}
	m.mu.Unlock()
	for _, item := range stale {
		_ = m.Close(item.owner, item.id)
	}
}

func (m *Manager) lookup(owner, id string) (*session, error) {
	m.mu.RLock()
	item := m.sessions[id]
	m.mu.RUnlock()
	if item == nil || item.owner != strings.TrimSpace(owner) {
		return nil, ErrNotFound
	}
	return item, nil
}

func (item *session) isActive() bool {
	item.mu.Lock()
	defer item.mu.Unlock()
	return !item.closed && item.exitedAt == nil
}

func (item *session) snapshot() Snapshot {
	item.mu.Lock()
	defer item.mu.Unlock()
	return Snapshot{ID: item.id, Offset: item.next, CreatedAt: item.createdAt, UpdatedAt: item.updatedAt, ExitedAt: item.exitedAt, ExitError: item.exitError, Closed: item.closed}
}

func (item *session) append(data []byte, limit int, now time.Time) {
	item.mu.Lock()
	defer item.mu.Unlock()
	item.buffer = append(item.buffer, data...)
	item.next += int64(len(data))
	if len(item.buffer) > limit {
		drop := len(item.buffer) - limit
		copy(item.buffer, item.buffer[drop:])
		item.buffer = item.buffer[:limit]
		item.base += int64(drop)
	}
	item.updatedAt = now
	close(item.notify)
	item.notify = make(chan struct{})
}

func (item *session) setExit(err error, now time.Time) {
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.exitedAt != nil {
		return
	}
	item.exitedAt = &now
	item.updatedAt = now
	if err != nil {
		item.exitError = err.Error()
	}
	close(item.notify)
	item.notify = make(chan struct{})
}

func (item *session) output(offset int64, limit int, now time.Time) (Output, <-chan struct{}, bool, error) {
	item.mu.Lock()
	defer item.mu.Unlock()
	if offset < 0 || offset > item.next {
		return Output{}, nil, false, ErrOffset
	}
	truncated := offset < item.base
	if truncated {
		offset = item.base
	}
	start := int(offset - item.base)
	available := len(item.buffer) - start
	if available > limit {
		available = limit
	}
	data := append([]byte(nil), item.buffer[start:start+available]...)
	item.updatedAt = now
	result := Output{Data: data, Offset: offset, NextOffset: offset + int64(len(data)), Truncated: truncated, ExitedAt: item.exitedAt, ExitError: item.exitError, Closed: item.closed}
	return result, item.notify, len(data) > 0 || item.closed || item.exitedAt != nil, nil
}

func (item *session) currentOutput(offset int64, limit int, now time.Time) (Output, error) {
	output, _, _, err := item.output(offset, limit, now)
	return output, err
}

func randomID() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
