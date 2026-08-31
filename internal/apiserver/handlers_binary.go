package apiserver

import (
	"context"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/oplock"
)

// handleBinaryStatus 更新状态(§27 状态机)
func (s *Server) handleBinaryStatus(c *Ctx) error {
	st := s.BinState.Info()
	// 补充健康检查结果(重启后持久化)
	if st.Phase == "success" || st.Phase == "failed" {
		if hr := s.BinState.HealthCheckResult(); hr != "" && st.Error == "" && st.Rollback == "" {
			// 健康检查文件存在但状态未反映:标记 rollback 信息
		}
	}
	c.OK(st)
	return nil
}

// handleBinaryCheckUpdate 检查更新(§24)
func (s *Server) handleBinaryCheckUpdate(c *Ctx) error {
	if err := s.Locks.Acquire(oplock.LockUpdate, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockUpdate)
	start := now()
	info, err := s.BinState.CheckUpdate(context.Background())
	c.Audit("binary.check_update", s.BinState.CurrentVersion(), start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(info)
	return nil
}

// handleBinaryDownload 下载(§24;下载到临时文件,不安装)
func (s *Server) handleBinaryDownload(c *Ctx) error {
	var req struct {
		Version string `json:"version"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := s.Locks.Acquire(oplock.LockUpdate, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockUpdate)
	start := now()
	pkg, sum, err := s.BinState.Download(context.Background(), req.Version)
	c.Audit("binary.download", req.Version, start, err, "", nil)
	if err != nil {
		return err
	}
	// 校验立即执行(下载与校验一体)
	if err := s.BinState.Verify(pkg, sum); err != nil {
		c.Audit("binary.verify", req.Version, start, err, "", nil)
		return err
	}
	c.OK(map[string]any{"ok": true, "version": req.Version, "downloaded": pkg, "verified": true})
	return nil
}

// handleBinaryInstall 安装(§25:原子替换 + 重启 + 健康检查 + 自动回滚)
func (s *Server) handleBinaryInstall(c *Ctx) error {
	var req struct {
		Version string `json:"version"`
		Confirm *bool  `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "binary.install"); err != nil {
		return err
	}
	// §57:binary update 与 compose update / system update 互斥
	if err := s.Locks.Acquire(oplock.LockUpdate, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockUpdate)
	start := now()
	// 若未预下载(直接安装),先下载+校验
	pkg, sum := s.BinState.Pending()
	tag := req.Version
	if pkg != "" {
		// 预下载场景:目标版本以下载时记录为准(客户端可能只发 confirm)
		if v := s.BinState.Info().Version; v != "" {
			tag = v
		}
	}
	if pkg == "" {
		var derr error
		pkg, sum, derr = s.BinState.Download(context.Background(), req.Version)
		if derr != nil {
			c.Audit("binary.install", req.Version, start, derr, "", nil)
			return derr
		}
	}
	if err := s.BinState.Verify(pkg, sum); err != nil {
		c.Audit("binary.install", req.Version, start, err, "", nil)
		return err
	}
	err := s.BinState.Install(context.Background(), pkg, sum, tag)
	// 安装失败时回滚(§26)
	rollback := ""
	if err != nil {
		if rbErr := s.BinState.Rollback(); rbErr == nil {
			rollback = "success"
		} else {
			rollback = "failed: " + rbErr.Error()
		}
	}
	c.Audit("binary.install", req.Version, start, err, rollback, nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true, "version": req.Version, "rollback": rollback})
	return nil
}

// handleBinaryRollback 手动回滚(§26)
func (s *Server) handleBinaryRollback(c *Ctx) error {
	if err := s.Locks.Acquire(oplock.LockUpdate, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockUpdate)
	start := now()
	err := s.BinState.Rollback()
	rb := ""
	if err == nil {
		rb = "success"
	}
	c.Audit("binary.rollback", "manual", start, err, rb, nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true, "rollback": rb})
	return nil
}

var _ = time.Second
