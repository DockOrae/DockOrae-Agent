// 卷 REST 端点(§10 Volume 全部由 Agent 执行)。
package apiserver

import (
	"github.com/moby/moby/client"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// handleVolumesList 卷列表
func (s *Server) handleVolumesList(c *Ctx) error {
	items, err := s.Docker.VolumeList(c.R.Context())
	if err != nil {
		return err
	}
	c.OK(map[string]any{"items": items})
	return nil
}

// handleVolumeInspect 卷详情(原始 JSON)
func (s *Server) handleVolumeInspect(c *Ctx) error {
	raw, err := s.Docker.VolumeInspectRaw(c.R.Context(), c.Param("name"))
	if err != nil {
		return err
	}
	c.W.Header().Set("Content-Type", "application/json")
	_, _ = c.W.Write(append([]byte(`{"ok":true,"data":`), append(raw, []byte(`}`)...)...))
	return nil
}

// handleVolumeCreate 创建卷
func (s *Server) handleVolumeCreate(c *Ctx) error {
	var req struct {
		Name       string            `json:"name"`
		Driver     string            `json:"driver"`
		DriverOpts map[string]string `json:"driver_opts"`
		Labels     map[string]string `json:"labels"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Name == "" {
		return errs.New(errs.INVALID_REQUEST, "name 不能为空")
	}
	driver := req.Driver
	if driver == "" {
		driver = "local"
	}
	start := now()
	vol, err := s.Docker.VolumeCreate(c.R.Context(), client.VolumeCreateOptions{
		Name:       req.Name,
		Driver:     driver,
		DriverOpts: req.DriverOpts,
		Labels:     req.Labels,
	})
	c.Audit("volume.create", req.Name, start, err, "", map[string]any{"driver": driver})
	if err != nil {
		return err
	}
	c.OK(vol)
	return nil
}

// handleVolumeRemove 删除卷(?force=1)
func (s *Server) handleVolumeRemove(c *Ctx) error {
	name := c.Param("name")
	force := c.Query("force") == "1" || c.Query("force") == "true"
	start := now()
	err := s.Docker.VolumeRemove(c.R.Context(), name, force)
	c.Audit("volume.remove", name, start, err, "", nil)
	return c.okOrErr(err)
}

// handleVolumesPrune 清理未使用卷(危险操作需确认)
func (s *Server) handleVolumesPrune(c *Ctx) error {
	var req struct {
		Confirm *bool `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "volumes.prune"); err != nil {
		return err
	}
	report, err := s.Docker.VolumePrune(c.R.Context())
	if err != nil {
		return err
	}
	c.OK(map[string]any{"volumes_deleted": report.VolumesDeleted, "space_reclaimed": report.SpaceReclaimed})
	return nil
}
