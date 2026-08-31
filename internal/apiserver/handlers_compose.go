package apiserver

import (
	"github.com/DockOrae/DockOrae-Agent/internal/oplock"
)

// handleComposeProjects 列出 compose 项目(§20)
func (s *Server) handleComposeProjects(c *Ctx) error {
	projects, err := s.Compose.Projects()
	if err != nil {
		return err
	}
	c.OK(map[string]any{"projects": projects})
	return nil
}

// handleComposeStatus 项目状态(§20)
func (s *Server) handleComposeStatus(c *Ctx) error {
	project := c.Query("project")
	st, err := s.Compose.Status(project)
	if err != nil {
		return err
	}
	c.OK(st)
	return nil
}

// handleComposeCheckUpdate 检查更新(§20-§22:digest 对比)
func (s *Server) handleComposeCheckUpdate(c *Ctx) error {
	project := c.Query("project")
	if err := s.Locks.Acquire(oplock.LockCompose, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockCompose)
	start := now()
	info, err := s.Compose.CheckUpdate(project)
	c.Audit("compose.check_update", project, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(info)
	return nil
}

// handleComposePull 拉取镜像(§20)
func (s *Server) handleComposePull(c *Ctx) error {
	project := c.Query("project")
	if err := s.Locks.Acquire(oplock.LockCompose, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockCompose)
	start := now()
	resp, err := s.Compose.Pull(project)
	c.Audit("compose.pull", project, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleComposeUpdate 更新项目(§21;失败自动回滚 §23)
func (s *Server) handleComposeUpdate(c *Ctx) error {
	var req struct {
		Project string `json:"project"`
		Confirm *bool  `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "compose.update"); err != nil {
		return err
	}
	// §57:compose update 与 binary update / system update 互斥
	if err := s.Locks.AcquireMany([]string{oplock.LockUpdate, oplock.LockCompose}, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.ReleaseMany([]string{oplock.LockUpdate, oplock.LockCompose})
	start := now()
	resp, err := s.Compose.Update(req.Project)
	rollback := ""
	if err != nil {
		// 模块内部已自动回滚;查询记录确认回滚结果(§44 审计要求记录是否回滚)
		if hist, herr := s.Compose.History(req.Project); herr == nil && len(hist) > 0 {
			rollback = hist[0].Rollback
		}
	}
	c.Audit("compose.update", req.Project, start, err, rollback, resp)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleComposeRollback 手动回滚(§23)
func (s *Server) handleComposeRollback(c *Ctx) error {
	var req struct {
		Project string `json:"project"`
		Confirm *bool  `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "compose.rollback"); err != nil {
		return err
	}
	if err := s.Locks.Acquire(oplock.LockCompose, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockCompose)
	start := now()
	resp, err := s.Compose.Rollback(req.Project)
	rb := ""
	if err == nil {
		rb = "success"
	}
	c.Audit("compose.rollback", req.Project, start, err, rb, nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleComposeHistory 更新历史(供面板展示)
func (s *Server) handleComposeHistory(c *Ctx) error {
	project := c.Query("project")
	hist, err := s.Compose.History(project)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"history": hist})
	return nil
}
