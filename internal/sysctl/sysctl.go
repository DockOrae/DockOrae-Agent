// Package sysctl 内核参数读写(§36)。
// 必须使用 Key Whitelist:仅允许受控参数,不允许用户任意修改所有 sysctl。
package sysctl

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// keyRe sysctl key 格式校验(允许连字符,如 fs.file-max)
var keyRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z0-9_-]+)+$`)

// Whitelist 允许修改的参数及取值校验
var Whitelist = map[string]func(string) bool{
	"vm.swappiness":               intRange(0, 100),
	"vm.overcommit_memory":        intRange(0, 2),
	"vm.overcommit_ratio":         intRange(0, 100),
	"fs.file-max":                 intMin(1),
	"fs.inotify.max_user_watches": intMin(1),
	"net.core.somaxconn":          intMin(1),
	"net.core.netdev_max_backlog": intMin(1),
	"net.ipv4.ip_forward":         intIn(0, 1),
	"net.ipv4.tcp_tw_reuse":       intIn(0, 1),
	"net.ipv4.tcp_fastopen":       intIn(0, 3),
}

// Service sysctl 服务
type Service struct {
	Exec *hostexec.Execer
}

// New 构造
func New(e *hostexec.Execer) *Service { return &Service{Exec: e} }

// ValidateKey 校验 key 是否在白名单
func ValidateKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 || !keyRe.MatchString(key) {
		return "", errs.New(errs.INVALID_REQUEST, "sysctl key 格式非法")
	}
	if _, ok := Whitelist[key]; !ok {
		return "", errs.Newf(errs.INVALID_REQUEST, "sysctl key %q 不在允许列表中", key)
	}
	return key, nil
}

// ValidateValue 校验值
func ValidateValue(key, value string) (string, error) {
	value = strings.TrimSpace(value)
	fn, ok := Whitelist[key]
	if !ok {
		return "", errs.Newf(errs.INVALID_REQUEST, "sysctl key %q 不在允许列表中", key)
	}
	if !fn(value) {
		return "", errs.Newf(errs.INVALID_REQUEST, "sysctl %s 取值非法: %q", key, value)
	}
	return value, nil
}

// Get 读取参数值
func (s *Service) Get(key string) (map[string]any, error) {
	k, err := ValidateKey(key)
	if err != nil {
		return nil, err
	}
	// /proc/sys/vm/swappiness
	path := "/proc/sys/" + strings.ReplaceAll(k, ".", "/")
	val, err := s.Exec.OutputString("cat", path)
	if err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "读取 %s 失败: %v", k, err)
	}
	return map[string]any{"key": k, "value": strings.TrimSpace(val)}, nil
}

// Set 设置参数值(仅白名单;经 /proc/sys 写入,重启失效;持久化由宿主 sysctl.conf 管理)
func (s *Service) Set(key, value string) (map[string]any, error) {
	k, err := ValidateKey(key)
	if err != nil {
		return nil, err
	}
	v, err := ValidateValue(k, value)
	if err != nil {
		return nil, err
	}
	path := "/proc/sys/" + strings.ReplaceAll(k, ".", "/")
	// 写文件(容器模式经 nsenter 到宿主 /proc/sys)
	pq := hostexec.Quote(path)
	script := fmt.Sprintf("echo %s > %s", hostexec.Quote(v), pq)
	if err := s.Exec.RunScript(script); err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "设置 %s 失败: %v", k, err)
	}
	// Verify
	cur, err := s.Get(k)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "key": k, "value": v, "current": cur["value"]}, nil
}

// Keys 白名单列表(供前端展示)
func Keys() []string {
	out := make([]string, 0, len(Whitelist))
	for k := range Whitelist {
		out = append(out, k)
	}
	return out
}

// ---------- 取值校验器 ----------

func intRange(min, max int64) func(string) bool {
	return func(s string) bool {
		v, err := strconv.ParseInt(s, 10, 64)
		return err == nil && v >= min && v <= max
	}
}

func intMin(min int64) func(string) bool {
	return func(s string) bool {
		v, err := strconv.ParseInt(s, 10, 64)
		return err == nil && v >= min
	}
}

func intIn(vals ...int64) func(string) bool {
	return func(s string) bool {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return false
		}
		for _, x := range vals {
			if v == x {
				return true
			}
		}
		return false
	}
}
