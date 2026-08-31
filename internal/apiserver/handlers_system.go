package apiserver

import (
	"context"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/oplock"
)

// handleSystemInfo 系统信息(§30)
func (s *Server) handleSystemInfo(c *Ctx) error {
	info, err := s.System.Info()
	if err != nil {
		return err
	}
	c.OK(info)
	return nil
}

// handleSystemTimezone 读取时区(§31)
func (s *Server) handleSystemTimezone(c *Ctx) error {
	c.OK(map[string]any{"timezone": s.System.Timezone()})
	return nil
}

// handleSystemSetTimezone 设置时区(§31,白名单校验)
func (s *Server) handleSystemSetTimezone(c *Ctx) error {
	var req struct {
		Timezone string `json:"timezone"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	resp, err := s.System.SetTimezone(req.Timezone)
	c.Audit("system.set_timezone", req.Timezone, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleSystemTime 时间同步状态(§32)
func (s *Server) handleSystemTime(c *Ctx) error {
	st, err := s.System.TimeStatus()
	if err != nil {
		return err
	}
	c.OK(st)
	return nil
}

// handleSystemTimeSync 手动同步时间(§32)
func (s *Server) handleSystemTimeSync(c *Ctx) error {
	if err := s.Locks.Acquire(oplock.LockSystem, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockSystem)
	start := now()
	resp, err := s.System.SyncTime()
	c.Audit("system.time_sync", "sync", start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleSystemService 服务操作(§28:start/stop/restart/enable/disable)
func (s *Server) handleSystemService(c *Ctx) error {
	var req struct {
		Name   string `json:"name"`
		Action string `json:"action"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	// status 为只读,不需系统锁;写操作需锁
	needLock := req.Action != "status"
	if needLock {
		if err := s.Locks.Acquire(oplock.LockSystem, c.RequestID); err != nil {
			return err
		}
		defer s.Locks.Release(oplock.LockSystem)
	}
	start := now()
	resp, err := s.System.ServiceAction(req.Name, req.Action)
	if err != nil {
		c.Audit("system.service", req.Action+" "+req.Name, start, err, "", nil)
		return err
	}
	if needLock {
		c.Audit("system.service", req.Action+" "+req.Name, start, err, "", nil)
	}
	c.OK(resp)
	return nil
}

// handleSystemUpdateCheck 系统更新检查(§33)
func (s *Server) handleSystemUpdateCheck(c *Ctx) error {
	if err := s.Locks.Acquire(oplock.LockUpdate, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockUpdate)
	info, err := s.System.UpdateCheck()
	if err != nil {
		return err
	}
	c.OK(info)
	return nil
}

// handleSystemUpdate 执行系统更新(§33;危险操作需确认 + 审计)
func (s *Server) handleSystemUpdate(c *Ctx) error {
	var req struct {
		Confirm *bool `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "system.update"); err != nil {
		return err
	}
	if err := s.Locks.Acquire(oplock.LockUpdate, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockUpdate)
	start := now()
	resp, err := s.System.Update()
	c.Audit("system.update", "distro-upgrade", start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

var _ = context.Background
var _ = time.Second
var _ = errs.INTERNAL
