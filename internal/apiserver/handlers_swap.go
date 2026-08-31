package apiserver

import (
	"github.com/DockOrae/DockOrae-Agent/internal/oplock"
)

// handleSwapStatus swap 状态(§11)
func (s *Server) handleSwapStatus(c *Ctx) error {
	st, err := s.Swap.Status()
	if err != nil {
		return err
	}
	c.OK(st)
	return nil
}

// handleSwapCreate 创建 swap(§15)
func (s *Server) handleSwapCreate(c *Ctx) error {
	var req struct {
		SizeMB  int    `json:"size_mb"`
		Path    string `json:"path"`
		Confirm *bool  `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "swap.create"); err != nil {
		return err
	}
	if err := s.Locks.Acquire(oplock.LockSwap, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockSwap)
	start := now()
	resp, err := s.Swap.Create(req.SizeMB, req.Path)
	c.Audit("swap.create", swapTarget(req.SizeMB, req.Path), start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleSwapResize 调整 swap(§16)
func (s *Server) handleSwapResize(c *Ctx) error {
	var req struct {
		SizeMB  int    `json:"size_mb"`
		Path    string `json:"path"`
		Confirm *bool  `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "swap.resize"); err != nil {
		return err
	}
	if err := s.Locks.Acquire(oplock.LockSwap, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockSwap)
	start := now()
	resp, err := s.Swap.Resize(req.SizeMB, req.Path)
	c.Audit("swap.resize", swapTarget(req.SizeMB, req.Path), start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleSwapDelete 删除 swap(§17)
func (s *Server) handleSwapDelete(c *Ctx) error {
	var req struct {
		Path    string `json:"path"`
		Confirm *bool  `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "swap.delete"); err != nil {
		return err
	}
	if err := s.Locks.Acquire(oplock.LockSwap, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockSwap)
	start := now()
	resp, err := s.Swap.Delete(req.Path)
	c.Audit("swap.delete", req.Path, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

func swapTarget(sizeMB int, path string) string {
	if path == "" {
		path = "/swapfile"
	}
	return itoa(sizeMB) + "MB @" + path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
