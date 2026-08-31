// 宿主监控 / 容器 IO / 镜像加速端点(面板 /system/host /system/monitor 数据来源)。
package apiserver

import (
	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// handleHostMonitor 宿主监控快照(cpu/mem/load/swap/disk)
func (s *Server) handleHostMonitor(c *Ctx) error {
	c.OK(s.Host.Monitor())
	return nil
}

// handleDockerIO 容器网络/磁盘 IO 速率(8s 缓存差分)
func (s *Server) handleDockerIO(c *Ctx) error {
	c.OK(s.Docker.ContainerIO(c.R.Context()))
	return nil
}

// handleRegistryMirrorsGet 读取镜像加速
func (s *Server) handleRegistryMirrorsGet(c *Ctx) error {
	res, err := s.Docker.RegistryMirrors()
	if err != nil {
		return err
	}
	c.OK(res)
	return nil
}

// handleRegistryMirrorsSave 写入镜像加速(危险操作需确认;面板提示重启 docker 生效)
func (s *Server) handleRegistryMirrorsSave(c *Ctx) error {
	var req struct {
		Mirrors []string `json:"mirrors"`
		Confirm *bool    `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "docker.registry_mirrors"); err != nil {
		return err
	}
	start := now()
	err := s.Docker.SaveRegistryMirrors(req.Mirrors)
	c.Audit("docker.registry_mirrors", "daemon.json", start, err, "", map[string]any{"mirrors": req.Mirrors})
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true, "needRestart": true})
	return nil
}

var _ = errs.INTERNAL
