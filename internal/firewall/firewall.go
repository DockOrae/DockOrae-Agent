// Package firewall 防火墙管理(§37-§38)。
// 先检测后端(ufw/firewalld/nftables)再操作;nftables 本轮只读(规则集复杂,防误操作)。
// 安全(§38):绝不提供"清空全部规则"操作;add/delete 均需确认;端口/协议严格校验。
package firewall

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// Backend 防火墙后端
type Backend string

const (
	BackendUFW         Backend = "ufw"
	BackendFirewalld   Backend = "firewalld"
	BackendNFTables    Backend = "nftables"
	BackendNone        Backend = "none"
	BackendUnsupported Backend = "unsupported"
)

// portRe 端口范围校验:1-65535 或 "port1:port2"
var portRe = regexp.MustCompile(`^[0-9]{1,5}(:[0-9]{1,5})?$`)

// Service firewall 服务
type Service struct {
	Exec *hostexec.Execer
}

// New 构造
func New(e *hostexec.Execer) *Service { return &Service{Exec: e} }

// Detect 检测宿主防火墙后端
func (s *Service) Detect() Backend {
	if _, err := s.Exec.Output("ufw", "status"); err == nil {
		return BackendUFW
	}
	if out, err := s.Exec.OutputString("firewall-cmd", "--state"); err == nil && strings.EqualFold(out, "running") {
		return BackendFirewalld
	}
	if _, err := s.Exec.Output("nft", "list", "ruleset"); err == nil {
		return BackendNFTables
	}
	return BackendNone
}

// ValidatePort 校验端口(1-65535 或范围)
func ValidatePort(p string) (string, error) {
	p = strings.TrimSpace(p)
	if !portRe.MatchString(p) || len(p) > 11 {
		return "", errs.New(errs.INVALID_REQUEST, "端口非法(1-65535 或 范围如 8000:8100)")
	}
	parts := strings.Split(p, ":")
	nums := make([]int, 0, 2)
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > 65535 {
			return "", errs.New(errs.INVALID_REQUEST, "端口非法(1-65535)")
		}
		nums = append(nums, n)
	}
	if len(nums) == 2 && nums[0] > nums[1] {
		return "", errs.New(errs.INVALID_REQUEST, "端口范围起始大于结束")
	}
	return p, nil
}

// ValidateProto 校验协议
func ValidateProto(p string) (string, error) {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return "tcp", nil
	}
	if p != "tcp" && p != "udp" {
		return "", errs.New(errs.INVALID_REQUEST, "协议仅支持 tcp/udp")
	}
	return p, nil
}

// Status 防火墙状态(§37)
func (s *Service) Status() (map[string]any, error) {
	backend := s.Detect()
	out := map[string]any{"backend": string(backend)}
	switch backend {
	case BackendUFW:
		st, _ := s.Exec.OutputString("ufw", "status")
		out["active"] = strings.Contains(st, "Status: active")
		out["detail"] = st
	case BackendFirewalld:
		st, _ := s.Exec.OutputString("firewall-cmd", "--state")
		out["active"] = strings.EqualFold(st, "running")
		out["detail"] = st
	case BackendNFTables:
		out["active"] = true
		out["detail"] = "nftables ruleset active"
	case BackendNone:
		out["active"] = false
		out["detail"] = "未检测到 ufw/firewalld/nftables"
	default:
		out["active"] = false
	}
	return out, nil
}

// Rules 规则列表(§37)
func (s *Service) Rules() (map[string]any, error) {
	backend := s.Detect()
	out := map[string]any{"backend": string(backend)}
	switch backend {
	case BackendUFW:
		st, err := s.Exec.OutputString("ufw", "status", "numbered")
		if err != nil {
			return nil, errs.Newf(errs.FIREWALL_OPERATION_FAILED, "读取 ufw 规则失败: %v", err)
		}
		out["rules"] = strings.Split(st, "\n")
	case BackendFirewalld:
		st, err := s.Exec.OutputString("firewall-cmd", "--list-all")
		if err != nil {
			return nil, errs.Newf(errs.FIREWALL_OPERATION_FAILED, "读取 firewalld 规则失败: %v", err)
		}
		out["rules"] = strings.Split(st, "\n")
	case BackendNFTables:
		st, err := s.Exec.OutputString("nft", "list", "ruleset")
		if err != nil {
			return nil, errs.Newf(errs.FIREWALL_OPERATION_FAILED, "读取 nftables 规则失败: %v", err)
		}
		out["rules"] = strings.Split(st, "\n")
	default:
		out["rules"] = []string{}
	}
	return out, nil
}

// Add 添加放行规则(§38:Validate → Preview → Apply → Verify)
func (s *Service) Add(port, proto string) (map[string]any, error) {
	p, err := ValidatePort(port)
	if err != nil {
		return nil, err
	}
	pr, err := ValidateProto(proto)
	if err != nil {
		return nil, err
	}
	backend := s.Detect()
	switch backend {
	case BackendUFW:
		// ufw allow 8000/tcp;先 dry-run 校验
		if _, err := s.Exec.Output("ufw", "--dry-run", "allow", p+"/"+pr); err != nil {
			return nil, errs.Newf(errs.FIREWALL_OPERATION_FAILED, "ufw 规则预览失败: %v", err)
		}
		if _, err := s.Exec.Output("ufw", "allow", p+"/"+pr); err != nil {
			return nil, errs.Newf(errs.FIREWALL_OPERATION_FAILED, "ufw 添加规则失败: %v", err)
		}
	case BackendFirewalld:
		if _, err := s.Exec.Output("firewall-cmd", "--permanent", "--add-port="+p+"/"+pr); err != nil {
			return nil, errs.Newf(errs.FIREWALL_OPERATION_FAILED, "firewalld 添加规则失败: %v", err)
		}
		if _, err := s.Exec.Output("firewall-cmd", "--reload"); err != nil {
			return nil, errs.Newf(errs.FIREWALL_OPERATION_FAILED, "firewalld 重载失败: %v", err)
		}
	case BackendNFTables:
		return nil, errs.New(errs.UNSUPPORTED, "nftables 添加规则需指定链,暂不支持自动添加(仅只读)")
	default:
		return nil, errs.New(errs.UNSUPPORTED, "未检测到受支持的防火墙(ufw/firewalld)")
	}
	// Verify
	rules, _ := s.Rules()
	return map[string]any{"ok": true, "port": p, "proto": pr, "rules": rules}, nil
}

// Delete 删除放行规则(§38)
func (s *Service) Delete(port, proto string) (map[string]any, error) {
	p, err := ValidatePort(port)
	if err != nil {
		return nil, err
	}
	pr, err := ValidateProto(proto)
	if err != nil {
		return nil, err
	}
	backend := s.Detect()
	switch backend {
	case BackendUFW:
		if _, err := s.Exec.Output("ufw", "delete", "allow", p+"/"+pr); err != nil {
			return nil, errs.Newf(errs.FIREWALL_OPERATION_FAILED, "ufw 删除规则失败: %v", err)
		}
	case BackendFirewalld:
		if _, err := s.Exec.Output("firewall-cmd", "--permanent", "--remove-port="+p+"/"+pr); err != nil {
			return nil, errs.Newf(errs.FIREWALL_OPERATION_FAILED, "firewalld 删除规则失败: %v", err)
		}
		if _, err := s.Exec.Output("firewall-cmd", "--reload"); err != nil {
			return nil, errs.Newf(errs.FIREWALL_OPERATION_FAILED, "firewalld 重载失败: %v", err)
		}
	case BackendNFTables:
		return nil, errs.New(errs.UNSUPPORTED, "nftables 删除规则需指定链,暂不支持自动删除(仅只读)")
	default:
		return nil, errs.New(errs.UNSUPPORTED, "未检测到受支持的防火墙(ufw/firewalld)")
	}
	rules, _ := s.Rules()
	return map[string]any{"ok": true, "port": p, "proto": pr, "rules": rules}, nil
}

// 供审计展示
func (s *Service) Describe(port, proto string) string {
	return fmt.Sprintf("%s/%s", port, proto)
}
