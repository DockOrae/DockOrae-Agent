// Package system 系统管理(§30-§33):系统信息/时区/时间同步/systemd 服务/系统更新。
package system

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// Service system 服务
type Service struct {
	Exec *hostexec.Execer
}

// New 构造
func New(e *hostexec.Execer) *Service { return &Service{Exec: e} }

// serviceNameRe systemd 服务名校验(§28:严格校验,禁止注入)
var serviceNameRe = regexp.MustCompile(`^[a-zA-Z0-9_.@-]+$`)

// ValidateServiceName 校验 systemd 服务名
func ValidateServiceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || len(name) > 128 ||
		!serviceNameRe.MatchString(name) || strings.HasPrefix(name, "-") {
		return "", errs.New(errs.INVALID_REQUEST, "服务名非法")
	}
	return name, nil
}

// Info 系统信息(§30)
func (s *Service) Info() (map[string]any, error) {
	hostname, _ := s.Exec.Hostname()
	osContent, _ := s.Exec.ReadFileString("/etc/os-release")
	distro := map[string]string{}
	for _, line := range strings.Split(osContent, "\n") {
		kv := strings.SplitN(line, "=", 2)
		if len(kv) == 2 {
			distro[kv[0]] = strings.Trim(strings.TrimSpace(kv[1]), `"'`)
		}
	}
	kernel, _ := s.Exec.OutputString("uname", "-r")
	arch, _ := s.Exec.OutputString("uname", "-m")
	tz := s.Timezone()

	return map[string]any{
		"hostname":     hostname,
		"os":           distro["NAME"],
		"distribution": distro["ID"],
		"version":      distro["VERSION_ID"],
		"kernel":       kernel,
		"architecture": arch,
		"timezone":     tz,
		"uptime":       s.uptime(),
		"server_time":  time.Now().Unix(),
	}, nil
}

// Timezone 读取当前时区(/etc/timezone 优先,回退 localtime 解析)
func (s *Service) Timezone() string {
	if tz, err := s.Exec.OutputString("cat", "/etc/timezone"); err == nil && tz != "" {
		return tz
	}
	// 回退:timedatectl
	if out, err := s.Exec.OutputString("timedatectl", "show", "-p", "Timezone", "--value"); err == nil && out != "" {
		return out
	}
	return "UTC"
}

// timezoneRe 时区名校验:字母数字 _ / + -
var timezoneRe = regexp.MustCompile(`^[a-zA-Z0-9_+/.-]+$`)

// ValidateTimezone 校验时区名(白名单:必须存在于 /usr/share/zoneinfo)
func (s *Service) ValidateTimezone(tz string) (string, error) {
	tz = strings.TrimSpace(tz)
	if tz == "" || len(tz) > 128 || !timezoneRe.MatchString(tz) || strings.HasPrefix(tz, ".") || strings.Contains(tz, "..") {
		return "", errs.New(errs.INVALID_REQUEST, "时区名非法")
	}
	// 白名单:zoneinfo 文件必须存在
	ok, err := s.Exec.Output("test", "-f", "/usr/share/zoneinfo/"+tz)
	if err != nil {
		// 特殊:UTC / Etc/UTC
		if tz == "UTC" {
			return tz, nil
		}
		return "", errs.Newf(errs.INVALID_REQUEST, "时区 %q 不存在于系统 zoneinfo", tz)
	}
	_ = ok
	return tz, nil
}

// SetTimezone 设置时区(§31)
func (s *Service) SetTimezone(tz string) (map[string]any, error) {
	name, err := s.ValidateTimezone(tz)
	if err != nil {
		return nil, err
	}
	// timedatectl 优先
	if _, err := s.Exec.Output("timedatectl", "set-timezone", name); err == nil {
		// Verify
		if cur := s.Timezone(); cur != name {
			return nil, errs.Newf(errs.EXEC_FAILED, "设置后验证失败:当前 %q,期望 %q", cur, name)
		}
		return map[string]any{"ok": true, "timezone": name}, nil
	}
	// 回退:软链 localtime + 写 /etc/timezone
	nq := hostexec.Quote(name)
	script := fmt.Sprintf("ln -sf /usr/share/zoneinfo/%s /etc/localtime && echo %s > /etc/timezone", nq, nq)
	if err := s.Exec.RunScript(script); err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "设置时区失败: %v", err)
	}
	if cur := s.Timezone(); cur != name && cur != "" {
		return nil, errs.Newf(errs.EXEC_FAILED, "设置后验证失败:当前 %q,期望 %q", cur, name)
	}
	return map[string]any{"ok": true, "timezone": name}, nil
}

// TimeStatus 时间同步状态(§32:优先检测 systemd-timesyncd/chrony/ntpd)
func (s *Service) TimeStatus() (map[string]any, error) {
	out := map[string]any{}
	// timedatectl 汇总
	if tdc, err := s.Exec.OutputString("timedatectl", "status"); err == nil {
		out["timedatectl"] = tdc
	}
	// 检测服务
	services := []string{}
	for _, svc := range []string{"systemd-timesyncd", "chrony", "chronyd", "ntp", "ntpd"} {
		if st, err := s.Exec.OutputString("systemctl", "is-active", svc); err == nil && st == "active" {
			services = append(services, svc)
		}
	}
	out["active_services"] = services
	if len(services) == 0 {
		out["sync_enabled"] = false
	} else {
		out["sync_enabled"] = true
	}
	// NTP 同步状态(timedatectl show)
	if ntp, err := s.Exec.OutputString("timedatectl", "show", "-p", "NTP", "--value"); err == nil {
		out["ntp"] = ntp
	}
	if sync, err := s.Exec.OutputString("timedatectl", "show", "-p", "NTPSynchronized", "--value"); err == nil {
		out["ntp_synchronized"] = sync
	}
	return out, nil
}

// SyncTime 手动同步时间(§32)
func (s *Service) SyncTime() (map[string]any, error) {
	// 按检测到的服务重启/触发
	services := []string{}
	for _, svc := range []string{"systemd-timesyncd", "chrony", "chronyd", "ntp", "ntpd"} {
		if st, err := s.Exec.OutputString("systemctl", "is-active", svc); err == nil && st == "active" {
			services = append(services, svc)
		}
	}
	if len(services) == 0 {
		// 无服务:尝试 timedatectl set-ntp true 启用
		if _, err := s.Exec.Output("timedatectl", "set-ntp", "true"); err != nil {
			return nil, errs.New(errs.EXEC_FAILED, "未检测到时间同步服务,且启用 NTP 失败")
		}
		return map[string]any{"ok": true, "enabled": "timedatectl NTP"}, nil
	}
	var errsList []string
	for _, svc := range services {
		if _, err := s.Exec.Output("systemctl", "restart", svc); err != nil {
			errsList = append(errsList, svc+": "+err.Error())
		}
	}
	if len(errsList) > 0 {
		return nil, errs.Newf(errs.EXEC_FAILED, "重启时间同步服务失败: %s", strings.Join(errsList, "; "))
	}
	// 等待同步结果(最多 5 秒)
	time.Sleep(3 * time.Second)
	st, _ := s.TimeStatus()
	return map[string]any{"ok": true, "restarted": services, "status": st}, nil
}

// ServiceAction 服务操作(§28)
func (s *Service) ServiceAction(name, action string) (map[string]any, error) {
	svc, err := ValidateServiceName(name)
	if err != nil {
		return nil, err
	}
	switch action {
	case "start", "stop", "restart", "enable", "disable", "status":
	default:
		return nil, errs.Newf(errs.INVALID_REQUEST, "不支持的服务操作: %s", action)
	}
	if _, err := s.Exec.Output("systemctl", action, svc); err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "systemctl %s %s 失败: %v", action, svc, err)
	}
	// Verify:读取最终状态
	state, _ := s.Exec.OutputString("systemctl", "is-active", svc)
	return map[string]any{"ok": true, "service": svc, "action": action, "state": state}, nil
}

// DistroID 发行版 ID
func (s *Service) DistroID() string {
	content, _ := s.Exec.ReadFileString("/etc/os-release")
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "ID=") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "ID=")), `"'`)
		}
	}
	return ""
}

// UpdateCheck 检查系统更新(§33:按发行版固定实现,禁止前端传命令)
func (s *Service) UpdateCheck() (map[string]any, error) {
	distro := s.DistroID()
	switch distro {
	case "debian", "ubuntu":
		// 先刷新索引(静默)
		_ = s.Exec.RunScript("export DEBIAN_FRONTEND=noninteractive; apt-get update -qq 2>/dev/null || true")
		out, err := s.Exec.Output("apt-get", "upgrade", "--dry-run", "-qq")
		if err != nil {
			return nil, errs.Newf(errs.SYSTEM_UPDATE_FAILED, "检查更新失败: %v", err)
		}
		return s.parseAptUpgradable(string(out)), nil
	case "alpine":
		_ = s.Exec.RunScript("apk update 2>/dev/null || true")
		out, err := s.Exec.OutputString("apk", "list", "--upgradable", "--no-cache")
		if err != nil {
			return nil, errs.Newf(errs.SYSTEM_UPDATE_FAILED, "检查更新失败: %v", err)
		}
		lines := []string{}
		for _, l := range strings.Split(out, "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
		return map[string]any{"distro": distro, "updates_available": len(lines), "packages": lines}, nil
	default:
		return nil, errs.Newf(errs.UNSUPPORTED, "不支持的发行版: %q(支持 debian/ubuntu/alpine)", distro)
	}
}

// Update 执行系统更新(异步由 API 层控制;此处同步执行核心更新)
func (s *Service) Update() (map[string]any, error) {
	distro := s.DistroID()
	switch distro {
	case "debian", "ubuntu":
		if err := s.Exec.RunScript("export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get upgrade -y -qq"); err != nil {
			return nil, errs.Newf(errs.SYSTEM_UPDATE_FAILED, "系统更新失败: %v", err)
		}
		return map[string]any{"ok": true, "distro": distro, "method": "apt"}, nil
	case "alpine":
		if err := s.Exec.RunScript("apk update && apk upgrade"); err != nil {
			return nil, errs.Newf(errs.SYSTEM_UPDATE_FAILED, "系统更新失败: %v", err)
		}
		return map[string]any{"ok": true, "distro": distro, "method": "apk"}, nil
	default:
		return nil, errs.Newf(errs.UNSUPPORTED, "不支持的发行版: %q(支持 debian/ubuntu/alpine)", distro)
	}
}

// parseAptUpgradable 解析 apt dry-run 输出(统计可升级包)
func (s *Service) parseAptUpgradable(out string) map[string]any {
	lines := []string{}
	for _, l := range strings.Split(out, "\n") {
		// 形如 "Inst nginx [1.24] (1.26 Debian:...)";包含 Inst 开头即待升级
		if strings.HasPrefix(l, "Inst ") {
			f := strings.Fields(l)
			if len(f) > 0 {
				lines = append(lines, f[1])
			}
		}
	}
	return map[string]any{
		"distro": s.DistroID(), "updates_available": len(lines),
		"packages": dedupe(lines),
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (s *Service) uptime() uint64 {
	content, err := s.Exec.ReadFileString("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(content)
	if len(f) == 0 {
		return 0
	}
	var secs float64
	fmt.Sscanf(f[0], "%f", &secs)
	return uint64(secs)
}
