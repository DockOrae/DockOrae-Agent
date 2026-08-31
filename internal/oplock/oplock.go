// Package oplock 命名操作锁,防止危险操作并发(两个 swap resize、两个更新、更新+重启同时跑)。
package oplock

import (
	"fmt"
	"sync"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// 锁名常量(与 Skill §42 对齐)
const (
	LockSwap    = "swap"
	LockUpdate  = "update" // binary update + compose update + system update 互斥(§57)
	LockCompose = "compose"
	LockSystem  = "system" // systemd/timezone/time sync
	LockDocker  = "docker" // docker service 操作 + cleanup
	LockReboot  = "reboot"
)

// Manager 命名锁管理器
type Manager struct {
	mu   sync.Mutex
	held map[string]*holder
}

type holder struct {
	reqID    string
	acquired time.Time
}

// New 创建锁管理器
func New() *Manager {
	return &Manager{held: make(map[string]*holder)}
}

// Acquire 获取单个锁;已被占用返回 OPERATION_IN_PROGRESS
func (m *Manager) Acquire(name, reqID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.held[name]; ok {
		return errs.Newf(errs.OPERATION_IN_PROGRESS,
			"操作 %q 正在进行中(请求 %s,已运行 %s)", name, h.reqID, time.Since(h.acquired).Round(time.Second))
	}
	m.held[name] = &holder{reqID: reqID, acquired: time.Now()}
	return nil
}

// AcquireMany 按序获取多个锁;任一失败则释放已获取的并返回错误
func (m *Manager) AcquireMany(names []string, reqID string) error {
	var got []string
	for _, n := range names {
		if err := m.Acquire(n, reqID); err != nil {
			for _, g := range got {
				m.Release(g)
			}
			return err
		}
		got = append(got, n)
	}
	return nil
}

// Release 释放锁
func (m *Manager) Release(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.held, name)
}

// ReleaseMany 释放多个锁
func (m *Manager) ReleaseMany(names []string) {
	for _, n := range names {
		m.Release(n)
	}
}

// Held 当前持有的锁列表(诊断用)
func (m *Manager) Held() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.held))
	for k := range m.held {
		out = append(out, fmt.Sprintf("%s(%s)", k, m.held[k].reqID))
	}
	return out
}
