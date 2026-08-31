// 面板自身部署信息端点:面板 compose 文件读取(面板在线更新用)。
// 仅读取固定候选路径(AGENT_COMPOSE_DIR / 默认 /opt/docker-manager),不接受任意路径。
package apiserver

import (
	"path/filepath"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// handlePanelCompose 读取面板宿主 compose 文件(在线更新定位 compose 目录用)
func (s *Server) handlePanelCompose(c *Ctx) error {
	candidates := []string{s.Cfg.ComposeDir}
	if s.Cfg.ComposeDir != "/opt/docker-manager" {
		candidates = append(candidates, "/opt/docker-manager")
	}
	for _, dir := range candidates {
		p := filepath.Join(dir, "docker-compose.yml")
		if !s.Exec.Exists(p) {
			continue
		}
		raw, err := s.Exec.ReadFileString(p)
		if err != nil {
			return errs.Newf(errs.HOST_ERROR, "读取 compose 文件失败: %v", err)
		}
		c.OK(map[string]any{"dir": dir, "yaml": raw})
		return nil
	}
	return errs.Newf(errs.NOT_FOUND, "未找到面板 compose 文件(已探测: %s)", joinCandidates(candidates))
}

func joinCandidates(dirs []string) string {
	out := ""
	for i, d := range dirs {
		if i > 0 {
			out += "、"
		}
		out += filepath.Join(d, "docker-compose.yml")
	}
	return out
}
