package apiserver

// handleHealth 存活探针(供面板检测 Agent 连通性)
func (s *Server) handleHealth(c *Ctx) error {
	c.OK(map[string]any{
		"ok":      true,
		"name":    "dockorae-agent",
		"version": s.Version,
		"mode":    s.Cfg.ModeLabel(),
	})
	return nil
}

// handleVersion 版本信息
func (s *Server) handleVersion(c *Ctx) error {
	c.OK(map[string]any{
		"version":    s.Version,
		"commit":     s.Commit,
		"build_time": s.BuildTime,
		"mode":       s.Cfg.ModeLabel(),
		"socket":     s.Cfg.SocketPath,
	})
	return nil
}
