// 容器 REST 端点(§7 Container 全部由 Agent 执行;列表过滤/业务逻辑在面板)。
package apiserver

import (
	"encoding/json"
	"strconv"

	"github.com/DockOrae/DockOrae-Agent/internal/docker"
	"github.com/DockOrae/DockOrae-Agent/internal/oplock"
)

// handleContainersList 容器列表(?all=1 含已停止)
func (s *Server) handleContainersList(c *Ctx) error {
	all := c.Query("all") == "1" || c.Query("all") == "true"
	items, err := s.Docker.ContainerList(c.R.Context(), all)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"items": items})
	return nil
}

// handleContainerInspect 容器详情(原始 JSON)
func (s *Server) handleContainerInspect(c *Ctx) error {
	raw, err := s.Docker.ContainerInspectRaw(c.R.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	c.W.Header().Set("Content-Type", "application/json")
	_, _ = c.W.Write(append([]byte(`{"ok":true,"data":`), append(raw, []byte(`}`)...)...))
	return nil
}

// handleContainerCreate 创建容器(config/host_config 由面板业务层构造)
func (s *Server) handleContainerCreate(c *Ctx) error {
	var req docker.ContainerCreateReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	id, err := s.Docker.ContainerCreate(c.R.Context(), req)
	c.Audit("container.create", req.Name, start, err, "", map[string]any{"image": imageNameFromConfig(req.Config)})
	if err != nil {
		return err
	}
	c.OK(map[string]any{"id": id})
	return nil
}

func imageNameFromConfig(raw []byte) string {
	var v struct {
		Image string `json:"Image"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Image
}

// 生命周期操作:start/stop/restart/kill/pause/unpause
func (s *Server) handleContainerStart(c *Ctx) error {
	return c.okOrErr(s.Docker.ContainerStart(c.R.Context(), c.Param("id")))
}
func (s *Server) handleContainerStop(c *Ctx) error {
	var timeout *int
	if t := c.Query("timeout"); t != "" {
		if n, err := strconv.Atoi(t); err == nil {
			timeout = &n
		}
	}
	return c.okOrErr(s.Docker.ContainerStop(c.R.Context(), c.Param("id"), timeout))
}
func (s *Server) handleContainerRestart(c *Ctx) error {
	var timeout *int
	if t := c.Query("timeout"); t != "" {
		if n, err := strconv.Atoi(t); err == nil {
			timeout = &n
		}
	}
	return c.okOrErr(s.Docker.ContainerRestart(c.R.Context(), c.Param("id"), timeout))
}
func (s *Server) handleContainerKill(c *Ctx) error {
	return c.okOrErr(s.Docker.ContainerKill(c.R.Context(), c.Param("id")))
}
func (s *Server) handleContainerPause(c *Ctx) error {
	return c.okOrErr(s.Docker.ContainerPause(c.R.Context(), c.Param("id")))
}
func (s *Server) handleContainerUnpause(c *Ctx) error {
	return c.okOrErr(s.Docker.ContainerUnpause(c.R.Context(), c.Param("id")))
}

// handleContainerRename 重命名
func (s *Server) handleContainerRename(c *Ctx) error {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Docker.ContainerRename(c.R.Context(), c.Param("id"), req.Name)
	c.Audit("container.rename", c.Param("id")+" → "+req.Name, start, err, "", nil)
	return c.okOrErr(err)
}

// handleContainerRemove 删除(?force=1&v=1)
func (s *Server) handleContainerRemove(c *Ctx) error {
	id := c.Param("id")
	force := c.Query("force") == "1" || c.Query("force") == "true"
	vols := c.Query("v") == "1" || c.Query("v") == "true"
	if err := s.Locks.Acquire(oplock.LockDocker, c.RequestID); err != nil {
		return err
	}
	defer s.Locks.Release(oplock.LockDocker)
	start := now()
	err := s.Docker.ContainerRemove(c.R.Context(), id, force, vols)
	c.Audit("container.remove", id, start, err, "", map[string]any{"force": force, "volumes": vols})
	return c.okOrErr(err)
}

// handleContainersPrune 清理已停止容器(危险操作需确认)
func (s *Server) handleContainersPrune(c *Ctx) error {
	var req struct {
		Confirm *bool `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "containers.prune"); err != nil {
		return err
	}
	report, err := s.Docker.ContainerPrune(c.R.Context())
	if err != nil {
		return err
	}
	c.OK(map[string]any{"containers_deleted": report.ContainersDeleted, "space_reclaimed": report.SpaceReclaimed})
	return nil
}

// handleContainerWait 等待容器退出(面板在线更新 helper 用),返回退出码
func (s *Server) handleContainerWait(c *Ctx) error {
	code, err := s.Docker.ContainerWait(c.R.Context(), c.Param("id"))
	if err != nil {
		return err
	}
	c.OK(map[string]any{"status_code": code})
	return nil
}

// handleContainerLogsTail 容器日志尾部文本(更新失败诊断;?lines=N 默认 80)
func (s *Server) handleContainerLogsTail(c *Ctx) error {
	lines := 80
	if l := c.Query("lines"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			lines = n
		}
	}
	out, err := s.Docker.ContainerLogsTail(c.R.Context(), c.Param("id"), lines)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"logs": out})
	return nil
}

// 小工具
func (c *Ctx) okOrErr(err error) error {
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}
