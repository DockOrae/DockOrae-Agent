// 网络 REST 端点(§9 Docker Network 全部由 Agent 执行)。
package apiserver

import (
	"encoding/json"

	"github.com/DockOrae/DockOrae-Agent/internal/docker"
	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// handleNetworksList 网络列表
func (s *Server) handleNetworksList(c *Ctx) error {
	items, err := s.Docker.NetworkList(c.R.Context())
	if err != nil {
		return err
	}
	c.OK(map[string]any{"items": items})
	return nil
}

// handleNetworkInspect 网络详情(原始 JSON)
func (s *Server) handleNetworkInspect(c *Ctx) error {
	raw, err := s.Docker.NetworkInspectRaw(c.R.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	c.W.Header().Set("Content-Type", "application/json")
	_, _ = c.W.Write(append([]byte(`{"ok":true,"data":`), append(raw, []byte(`}`)...)...))
	return nil
}

// handleNetworkCreate 创建网络
func (s *Server) handleNetworkCreate(c *Ctx) error {
	var req struct {
		Name     string  `json:"name"`
		Driver   string  `json:"driver"`
		Internal bool    `json:"internal"`
		Subnet   *string `json:"subnet"`
		Gateway  *string `json:"gateway"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Name == "" {
		return errs.New(errs.INVALID_REQUEST, "name 不能为空")
	}
	var ipam json.RawMessage
	if req.Subnet != nil || req.Gateway != nil {
		cfg := map[string]any{}
		if req.Subnet != nil {
			cfg["Subnet"] = *req.Subnet
		}
		if req.Gateway != nil {
			cfg["Gateway"] = *req.Gateway
		}
		raw, _ := json.Marshal(map[string]any{
			"Driver": "default",
			"Config": []any{cfg},
		})
		ipam = raw
	}
	start := now()
	id, err := s.Docker.NetworkCreate(c.R.Context(), docker.NetworkCreateReq{
		Name:     req.Name,
		Driver:   req.Driver,
		Internal: req.Internal,
		IPAM:     ipam,
	})
	c.Audit("network.create", req.Name, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"id": id})
	return nil
}

// handleNetworkRemove 删除网络
func (s *Server) handleNetworkRemove(c *Ctx) error {
	id := c.Param("id")
	start := now()
	err := s.Docker.NetworkRemove(c.R.Context(), id)
	c.Audit("network.remove", id, start, err, "", nil)
	return c.okOrErr(err)
}

// handleNetworksPrune 清理未使用网络(危险操作需确认)
func (s *Server) handleNetworksPrune(c *Ctx) error {
	var req struct {
		Confirm *bool `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "networks.prune"); err != nil {
		return err
	}
	report, err := s.Docker.NetworkPrune(c.R.Context())
	if err != nil {
		return err
	}
	c.OK(map[string]any{"networks_deleted": report.NetworksDeleted})
	return nil
}

// handleNetworkConnect 容器接入网络
func (s *Server) handleNetworkConnect(c *Ctx) error {
	var req struct {
		Container string `json:"container"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Docker.NetworkConnect(c.R.Context(), c.Param("id"), req.Container)
	c.Audit("network.connect", req.Container+" → "+c.Param("id"), start, err, "", nil)
	return c.okOrErr(err)
}

// handleNetworkDisconnect 容器断开网络
func (s *Server) handleNetworkDisconnect(c *Ctx) error {
	var req struct {
		Container string `json:"container"`
		Force     bool   `json:"force"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Docker.NetworkDisconnect(c.R.Context(), c.Param("id"), req.Container, req.Force)
	c.Audit("network.disconnect", req.Container+" ← "+c.Param("id"), start, err, "", nil)
	return c.okOrErr(err)
}
