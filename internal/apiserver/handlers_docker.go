package apiserver

import (
	"github.com/DockOrae/DockOrae-Agent/internal/oplock"
)

// handleDockerStatus docker 状态(§18)
func (s *Server) handleDockerStatus(c *Ctx) error {
	st, err := s.Docker.Status(c.R.Context())
	if err != nil {
		return err
	}
	c.OK(st)
	return nil
}

// handleDockerInfo docker info(§18)
func (s *Server) handleDockerInfo(c *Ctx) error {
	info, err := s.Docker.Info(c.R.Context())
	if err != nil {
		return err
	}
	c.OK(info)
	return nil
}

// handleDockerVersion docker 版本(§18)
func (s *Server) handleDockerVersion(c *Ctx) error {
	v, err := s.Docker.Version(c.R.Context())
	if err != nil {
		return err
	}
	c.OK(v)
	return nil
}

// handleDockerService docker 引擎服务操作(§18 start/stop/restart)
func (s *Server) handleDockerService(c *Ctx) error {
	var req struct {
		Action  string `json:"action"`
		Confirm *bool  `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "docker.service."+req.Action); err != nil {
		return err
	}
	if err := s.Locks.Acquire(oplock.LockDocker, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockDocker)
	start := now()
	resp, err := s.Docker.ServiceAction(req.Action)
	c.Audit("docker.service", req.Action, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleDockerCleanupPreview 清理预览(§19:预计释放空间 + 待删数量)
func (s *Server) handleDockerCleanupPreview(c *Ctx) error {
	preview, err := s.Docker.CleanupPreview(c.R.Context())
	if err != nil {
		return err
	}
	c.OK(preview)
	return nil
}

// handleDockerCleanup 执行清理(§19:危险操作需确认)
func (s *Server) handleDockerCleanup(c *Ctx) error {
	var req struct {
		Targets []string `json:"targets"`
		Confirm *bool    `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "docker.cleanup"); err != nil {
		return err
	}
	if err := s.Locks.Acquire(oplock.LockDocker, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockDocker)
	start := now()
	resp, err := s.Docker.Cleanup(c.R.Context(), req.Targets)
	c.Audit("docker.cleanup", joinTargets(req.Targets), start, err, "", resp)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

func joinTargets(t []string) string {
	out := ""
	for i, s := range t {
		if i > 0 {
			out += ","
		}
		out += s
	}
	if out == "" {
		return "(empty)"
	}
	return out
}
