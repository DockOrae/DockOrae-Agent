package apiserver

// handleFirewallStatus 防火墙状态(§37)
func (s *Server) handleFirewallStatus(c *Ctx) error {
	st, err := s.Firewall.Status()
	if err != nil {
		return err
	}
	c.OK(st)
	return nil
}

// handleFirewallRules 规则列表(§37)
func (s *Server) handleFirewallRules(c *Ctx) error {
	rules, err := s.Firewall.Rules()
	if err != nil {
		return err
	}
	c.OK(rules)
	return nil
}

// handleFirewallAdd 添加放行规则(§38:预览→确认→应用→验证)
func (s *Server) handleFirewallAdd(c *Ctx) error {
	var req struct {
		Port    string `json:"port"`
		Proto   string `json:"proto"`
		Confirm *bool  `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "firewall.add"); err != nil {
		return err
	}
	start := now()
	resp, err := s.Firewall.Add(req.Port, req.Proto)
	c.Audit("firewall.add", s.Firewall.Describe(req.Port, req.Proto), start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleFirewallDelete 删除规则(§38)
func (s *Server) handleFirewallDelete(c *Ctx) error {
	var req struct {
		Port    string `json:"port"`
		Proto   string `json:"proto"`
		Confirm *bool  `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "firewall.delete"); err != nil {
		return err
	}
	start := now()
	resp, err := s.Firewall.Delete(req.Port, req.Proto)
	c.Audit("firewall.delete", s.Firewall.Describe(req.Port, req.Proto), start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(resp)
	return nil
}

// handleNetworkInterfaces 网络接口(§39)
func (s *Server) handleNetworkInterfaces(c *Ctx) error {
	info, err := s.Network.Interfaces()
	if err != nil {
		return err
	}
	c.OK(info)
	return nil
}

// handleNetworkRoutes 路由表(§39)
func (s *Server) handleNetworkRoutes(c *Ctx) error {
	info, err := s.Network.Routes()
	if err != nil {
		return err
	}
	c.OK(info)
	return nil
}

// handleNetworkDNS DNS 配置(§39)
func (s *Server) handleNetworkDNS(c *Ctx) error {
	info, err := s.Network.DNS()
	if err != nil {
		return err
	}
	c.OK(info)
	return nil
}
