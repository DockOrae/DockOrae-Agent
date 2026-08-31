package apiserver

import (
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/oplock"
)

// handleHostInfo 宿主完整信息(§8)
func (s *Server) handleHostInfo(c *Ctx) error {
	info, err := s.Host.Info()
	if err != nil {
		return err
	}
	c.OK(info)
	return nil
}

// handleHostHostname 查询主机名(§9)
func (s *Server) handleHostHostname(c *Ctx) error {
	h, err := s.Host.Hostname()
	if err != nil {
		return err
	}
	c.OK(map[string]any{"hostname": h})
	return nil
}

// handleHostSetHostname 设置主机名(§9,校验 + 验证)
func (s *Server) handleHostSetHostname(c *Ctx) error {
	var req struct {
		Hostname string `json:"hostname"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	resp, err := s.Host.SetHostname(req.Hostname)
	c.Audit("host.set_hostname", req.Hostname, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleHostReboot 重启宿主(§10:权限检查 + 二次确认 + 审计)
func (s *Server) handleHostReboot(c *Ctx) error {
	var req struct {
		Confirm *bool `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "host.reboot"); err != nil {
		return err
	}
	// 全部操作锁互斥(重启期间不能有其他操作)
	lockNames := []string{oplock.LockSwap, oplock.LockUpdate, oplock.LockCompose, oplock.LockSystem, oplock.LockDocker, oplock.LockReboot}
	if err := s.Locks.AcquireMany(lockNames, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.ReleaseMany(lockNames)

	start := now()
	err := s.Host.Reboot()
	c.Audit("host.reboot", "reboot", start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true, "message": "重启命令已执行"})
	return nil
}

var _ = errs.INTERNAL
var _ = time.Now
