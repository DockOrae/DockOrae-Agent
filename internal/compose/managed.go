// 面板托管 compose 项目执行(§11 Compose 职责分离:面板管 YAML/项目/业务,Agent 执行)。
// 面板把 docker-compose.yml + 附加文件(conf/data 等相对路径依赖)随请求发送,
// Agent 写入宿主 <DataDir>/compose/<project>/ 后经宿主 docker compose 执行(nsenter)。
// 安全:项目名/文件路径经严格校验,禁止任意路径与命令。
package compose

import (
	"encoding/base64"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/DockOrae/DockOrae-Agent/internal/config"
	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// ManagedService 面板托管 compose 执行服务
type ManagedService struct {
	Exec *hostexec.Execer
	Cfg  *config.Config
}

// NewManaged 构造
func NewManaged(e *hostexec.Execer, cfg *config.Config) *ManagedService {
	return &ManagedService{Exec: e, Cfg: cfg}
}

// projectDir 项目目录(宿主路径)
func (s *ManagedService) projectDir(project string) string {
	return filepath.Join(s.Cfg.DataDir, "compose", project)
}

// filePath compose 文件路径(宿主路径)
func (s *ManagedService) filePath(project string) string {
	return filepath.Join(s.projectDir(project), "docker-compose.yml")
}

// validateFileRel 校验相对文件路径(仅字母数字 _ - . 与 /,禁 .. 与隐藏文件)
func validateFileRel(p string) error {
	if p == "" || len(p) > 256 || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return errs.Newf(errs.INVALID_REQUEST, "文件路径非法: %s", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return errs.Newf(errs.INVALID_REQUEST, "文件路径非法: %s", p)
		}
	}
	return nil
}

// WriteProject 把 yaml + 附加文件写入宿主项目目录(先写文件再执行,保证相对路径依赖可用)。
// files 为相对路径 → base64 内容;docker-compose.yml 单独以 yaml 字段传入。
// 返回 compose 文件宿主路径。
func (s *ManagedService) WriteProject(project, yaml string, files map[string]string) (string, error) {
	name, err := ValidateProject(project)
	if err != nil {
		return "", err
	}
	dir := s.projectDir(name)
	if err := s.Exec.MkdirAll(dir); err != nil {
		return "", err
	}
	if strings.TrimSpace(yaml) == "" {
		return "", errs.New(errs.INVALID_REQUEST, "yaml 不能为空")
	}
	if err := s.Exec.WriteFile(s.filePath(name), []byte(yaml), 0o644); err != nil {
		return "", errs.Newf(errs.COMPOSE_ERROR, "写入 compose 文件失败: %v", err)
	}
	for rel, b64 := range files {
		if err := validateFileRel(rel); err != nil {
			return "", err
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return "", errs.Newf(errs.INVALID_REQUEST, "文件 %s 内容编码无效", rel)
		}
		dest := filepath.Join(dir, rel)
		if err := s.Exec.MkdirAll(filepath.Dir(dest)); err != nil {
			return "", err
		}
		if err := s.Exec.WriteFile(dest, raw, 0o644); err != nil {
			return "", errs.Newf(errs.COMPOSE_ERROR, "写入文件 %s 失败: %v", rel, err)
		}
	}
	return s.filePath(name), nil
}

// ComposeCmd 构造 compose 命令(docker compose -p P -f FILE args...;未启动)
func (s *ManagedService) ComposeCmd(project string, args ...string) *exec.Cmd {
	f := s.filePath(project)
	all := append([]string{"-p", project, "-f", f}, args...)
	return s.Exec.ComposeCommand(all...)
}

// Run 写项目文件并同步执行 compose 命令,返回 {ok, output}
func (s *ManagedService) Run(project, yaml string, files map[string]string, args ...string) (map[string]any, error) {
	if _, err := s.WriteProject(project, yaml, files); err != nil {
		return nil, err
	}
	cmd := s.ComposeCmd(project, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return nil, errs.Newf(errs.COMPOSE_ERROR, "compose %s 失败: %s", strings.Join(args, " "), msg)
	}
	return map[string]any{"ok": true, "output": strings.TrimSpace(string(out))}, nil
}

// LogsCmd 构造 compose logs 命令(面板托管项目;tail 默认 300)
func (s *ManagedService) LogsCmd(project, tail string) (*exec.Cmd, error) {
	name, err := ValidateProject(project)
	if err != nil {
		return nil, err
	}
	if tail == "" {
		tail = "300"
	}
	return s.ComposeCmd(name, "logs", "-f", "--tail", tail), nil
}

// RemoveProject 删除宿主项目目录(compose down 后清理)
func (s *ManagedService) RemoveProject(project string) error {
	name, err := ValidateProject(project)
	if err != nil {
		return err
	}
	cmd := s.Exec.Command("rm", "-rf", s.projectDir(name))
	_ = cmd.Run()
	return nil
}
