package apiserver

// handleDiskUsage 磁盘用量(§34)
func (s *Server) handleDiskUsage(c *Ctx) error {
	usage, err := s.Disk.Usage()
	if err != nil {
		return err
	}
	c.OK(map[string]any{"usage": usage})
	return nil
}

// handleDiskDevices 块设备(§34)
func (s *Server) handleDiskDevices(c *Ctx) error {
	devices, err := s.Disk.Devices()
	if err != nil {
		return err
	}
	c.OK(map[string]any{"devices": devices})
	return nil
}

// handleDiskMounts 挂载列表(§34)
func (s *Server) handleDiskMounts(c *Ctx) error {
	mounts, err := s.Disk.Mounts()
	if err != nil {
		return err
	}
	c.OK(map[string]any{"mounts": mounts})
	return nil
}

// handleSysctlGet 读取内核参数(§36)
func (s *Server) handleSysctlGet(c *Ctx) error {
	key := c.Query("key")
	resp, err := s.Sysctl.Get(key)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleSysctlSet 设置内核参数(§36:白名单)
func (s *Server) handleSysctlSet(c *Ctx) error {
	var req struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	resp, err := s.Sysctl.Set(req.Key, req.Value)
	c.Audit("sysctl.set", req.Key+"="+req.Value, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleSysctlWhitelist 白名单列表(供前端展示)
func (s *Server) handleSysctlWhitelist(c *Ctx) error {
	c.OK(map[string]any{"keys": sysctlKeys()})
	return nil
}
