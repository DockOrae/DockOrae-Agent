// Package disk 磁盘管理(§34):usage/devices/mounts 只读能力。
package disk

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// Service disk 服务
type Service struct {
	Exec *hostexec.Execer
}

// New 构造
func New(e *hostexec.Execer) *Service { return &Service{Exec: e} }

// Usage 磁盘用量(§34:Filesystem/Mount/Total/Used/Avail/Usage%)
func (s *Service) Usage() ([]map[string]any, error) {
	out, err := s.Exec.Output("df", "-B1", "--output=source,fstype,size,used,avail,pcent,target")
	if err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "df 失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return []map[string]any{}, nil
	}
	result := []map[string]any{}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		total, _ := strconv.ParseInt(f[2], 10, 64)
		used, _ := strconv.ParseInt(f[3], 10, 64)
		avail, _ := strconv.ParseInt(f[4], 10, 64)
		result = append(result, map[string]any{
			"filesystem": f[0],
			"fstype":     f[1],
			"total":      total,
			"used":       used,
			"available":  avail,
			"usage_pct":  strings.TrimSuffix(f[5], "%"),
			"mount":      strings.Join(f[6:], " "),
		})
	}
	return result, nil
}

// Devices 块设备与分区(§34:lsblk JSON)
func (s *Service) Devices() ([]map[string]any, error) {
	out, err := s.Exec.Output("lsblk", "-J", "-o", "NAME,SIZE,TYPE,FSTYPE,MOUNTPOINT,MODEL,SERIAL,TRAN")
	if err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "lsblk 失败: %v", err)
	}
	var parsed struct {
		Blockdevices []map[string]any `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "解析 lsblk 失败: %v", err)
	}
	if parsed.Blockdevices == nil {
		parsed.Blockdevices = []map[string]any{}
	}
	return parsed.Blockdevices, nil
}

// Mounts 挂载列表(/proc/mounts,过滤伪文件系统)
func (s *Service) Mounts() ([]map[string]any, error) {
	content, err := s.Exec.ReadFileString("/proc/mounts")
	if err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "读取 /proc/mounts 失败: %v", err)
	}
	pseudo := map[string]bool{
		"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true,
		"cgroup": true, "cgroup2": true, "tmpfs": true, "overlay": true,
		"mqueue": true, "securityfs": true, "pstore": true, "debugfs": true,
		"tracefs": true, "fusectl": true, "configfs": true, "bpf": true,
		"autofs": true, "binfmt_misc": true, "ramfs": true, "hugetlbfs": true,
	}
	result := []map[string]any{}
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		if pseudo[f[2]] {
			continue
		}
		result = append(result, map[string]any{
			"device": f[0], "mount": f[1], "fstype": f[2], "options": f[3],
		})
	}
	return result, nil
}
