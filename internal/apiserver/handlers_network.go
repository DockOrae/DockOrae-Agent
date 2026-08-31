// 基础宿主机网络信息端点(§17:仅接口/路由/DNS,不做 Firewall 管理)。
package apiserver

// handleNetworkInterfaces 网络接口
func (s *Server) handleNetworkInterfaces(c *Ctx) error {
	info, err := s.Network.Interfaces()
	if err != nil {
		return err
	}
	c.OK(info)
	return nil
}

// handleNetworkRoutes 路由表
func (s *Server) handleNetworkRoutes(c *Ctx) error {
	info, err := s.Network.Routes()
	if err != nil {
		return err
	}
	c.OK(info)
	return nil
}

// handleNetworkDNS DNS 配置
func (s *Server) handleNetworkDNS(c *Ctx) error {
	info, err := s.Network.DNS()
	if err != nil {
		return err
	}
	c.OK(info)
	return nil
}
