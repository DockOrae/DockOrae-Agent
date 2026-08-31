// Package network 网络信息(§39)。
// 本轮只读能力(interfaces/routes/dns);修改能力(§40)记录为后续任务。
package network

import (
	"encoding/json"
	"strings"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// Service network 服务
type Service struct {
	Exec *hostexec.Execer
}

// New 构造
func New(e *hostexec.Execer) *Service { return &Service{Exec: e} }

// Interfaces 网络接口列表(§39:Interface/MAC/IPv4/IPv6/State/Speed)
func (s *Service) Interfaces() (map[string]any, error) {
	// ip -j addr
	out, err := s.Exec.Output("ip", "-j", "addr")
	if err != nil {
		return nil, errs.Newf(errs.NETWORK_OPERATION_FAILED, "读取接口失败: %v", err)
	}
	var addrs []struct {
		IfName   string   `json:"ifname"`
		Address  string   `json:"address"`
		Flags    []string `json:"flags"`
		LinkInfo struct {
			InfoKind string `json:"info_kind"`
		} `json:"link_info"`
		AddrInfo []struct {
			Family    string `json:"family"`
			Local     string `json:"local"`
			PrefixLen int    `json:"prefixlen"`
		} `json:"addr_info"`
		Speed     string `json:"speed"`
		Mtu       int    `json:"mtu"`
		Operstate string `json:"operstate"`
	}
	if err := json.Unmarshal(out, &addrs); err != nil {
		return nil, errs.Newf(errs.NETWORK_OPERATION_FAILED, "解析接口失败: %v", err)
	}
	result := []map[string]any{}
	for _, a := range addrs {
		up := false
		for _, f := range a.Flags {
			if f == "UP" {
				up = true
				break
			}
		}
		ipv4 := []string{}
		ipv6 := []string{}
		for _, ai := range a.AddrInfo {
			if ai.Family == "inet" {
				ipv4 = append(ipv4, ai.Local)
			} else if ai.Family == "inet6" && !strings.HasPrefix(ai.Local, "fe80") {
				ipv6 = append(ipv6, ai.Local)
			}
		}
		result = append(result, map[string]any{
			"interface": a.IfName,
			"mac":       a.Address,
			"ipv4":      ipv4,
			"ipv6":      ipv6,
			"state":     a.Operstate,
			"up":        up,
			"mtu":       a.Mtu,
			"speed":     a.Speed,
			"type":      a.LinkInfo.InfoKind,
		})
	}
	return map[string]any{"interfaces": result}, nil
}

// Routes 路由表(§39)
func (s *Service) Routes() (map[string]any, error) {
	out, err := s.Exec.Output("ip", "-j", "route")
	if err != nil {
		return nil, errs.Newf(errs.NETWORK_OPERATION_FAILED, "读取路由失败: %v", err)
	}
	var routes []map[string]any
	if err := json.Unmarshal(out, &routes); err != nil {
		return nil, errs.Newf(errs.NETWORK_OPERATION_FAILED, "解析路由失败: %v", err)
	}
	if routes == nil {
		routes = []map[string]any{}
	}
	return map[string]any{"routes": routes}, nil
}

// DNS 配置(§39:/etc/resolv.conf + resolvectl)
func (s *Service) DNS() (map[string]any, error) {
	out := map[string]any{}
	content, err := s.Exec.ReadFileString("/etc/resolv.conf")
	if err != nil {
		return nil, errs.Newf(errs.NETWORK_OPERATION_FAILED, "读取 resolv.conf 失败: %v", err)
	}
	nameservers := []string{}
	search := []string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nameserver ") {
			nameservers = append(nameservers, strings.TrimSpace(strings.TrimPrefix(line, "nameserver ")))
		} else if strings.HasPrefix(line, "search ") {
			search = append(search, strings.Fields(strings.TrimPrefix(line, "search "))...)
		}
	}
	out["nameservers"] = nameservers
	out["search"] = search
	out["resolv_conf"] = content
	// resolvectl(systemd-resolved 存在时)
	if st, err := s.Exec.OutputString("resolvectl", "status"); err == nil {
		out["resolvectl"] = st
	}
	return out, nil
}
