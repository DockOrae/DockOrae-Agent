// Package host 宿主机基本信息/主机名/重启(§8-§10)。
package host

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// Service host 服务
type Service struct {
	Exec *hostexec.Execer
}

// New 构造
func New(e *hostexec.Execer) *Service { return &Service{Exec: e} }

// hostnameRe 主机名校验(RFC 1123):字母数字与连字符,首尾不能是连字符,≤63
var hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// ValidateHostname 校验主机名
func ValidateHostname(h string) (string, error) {
	h = strings.TrimSpace(h)
	if len(h) < 1 || len(h) > 63 || !hostnameRe.MatchString(h) {
		return "", errs.New(errs.INVALID_REQUEST, "主机名非法(仅限字母数字与连字符,1-63 字符,首尾不能为连字符)")
	}
	return h, nil
}

// Info 宿主机完整信息(§8:数据必须来自真实宿主机)
func (s *Service) Info() (map[string]any, error) {
	hostname, _ := s.Exec.Hostname()
	osName, distro, distroVer := s.distroInfo()
	kernel, _ := s.Exec.OutputString("uname", "-r")
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	} else if arch == "arm64" {
		arch = "aarch64"
	}
	cpuModel, cores := s.cpuInfo()
	memTotal, memAvail := s.memInfo()
	uptime := s.uptime()
	load := s.loadAvg()
	diskTotal, diskUsed := s.diskRoot()
	swapTotal, swapUsed := s.swapInfo()

	return map[string]any{
		"hostname":       hostname,
		"os":             osName,
		"distribution":   distro,
		"distro_version": distroVer,
		"kernel":         kernel,
		"arch":           arch,
		"cpu_model":      cpuModel,
		"cpu_cores":      cores,
		"mem_total":      memTotal,
		"mem_used":       memTotal - memAvail,
		"uptime":         uptime,
		"load_avg":       load,
		"disk": map[string]any{
			"total": diskTotal, "used": diskUsed,
			"pct": pct(diskUsed, diskTotal),
		},
		"swap": map[string]any{
			"total": swapTotal, "used": swapUsed, "pct": pct(swapUsed, swapTotal),
		},
		"server_time": time.Now().Unix(),
	}, nil
}

// 监控采样缓存(CPU 使用率需要两次采样做差值,与面板侧原实现同款)
var (
	monitorMu    sync.Mutex
	monitorCPU   *[2]uint64 // (idle, total)
	monitorCPUAt time.Time
)

// Monitor 监控快照(cpu_pct/mem/load/swap/disk;面板每 3 秒轮询)
func (s *Service) Monitor() map[string]any {
	// CPU 使用率(两次采样差值)
	idle, total := s.cpuStat()
	cpuPct := 0.0
	monitorMu.Lock()
	if monitorCPU != nil {
		dTotal := total - monitorCPU[1]
		dIdle := idle - monitorCPU[0]
		if dTotal > 0 {
			cpuPct = (1.0 - float64(dIdle)/float64(dTotal)) * 100.0
		}
	}
	monitorCPU = &[2]uint64{idle, total}
	monitorCPUAt = time.Now()
	monitorMu.Unlock()

	memTotal, memAvail := s.memInfo()
	memUsed := uint64(0)
	memPct := 0.0
	if memTotal > 0 {
		memUsed = memTotal - memAvail
		memPct = pct(memUsed, memTotal)
	}
	diskTotal, diskUsed := s.diskRoot()
	swapTotal, swapUsed := s.swapInfo()

	return map[string]any{
		"cpu_pct": round2(cpuPct),
		"mem": map[string]any{
			"total": memTotal, "used": memUsed, "pct": round2(memPct),
		},
		"load": s.loadAvg(),
		"swap": map[string]any{
			"total": swapTotal, "used": swapUsed, "pct": round2(pct(swapUsed, swapTotal)),
		},
		"disk": map[string]any{
			"total": diskTotal, "used": diskUsed, "pct": round2(pct(diskUsed, diskTotal)),
		},
		"server_time": time.Now().Unix(),
	}
}

// cpuStat 读取 (idle, total) CPU 时钟
func (s *Service) cpuStat() (uint64, uint64) {
	raw, err := s.Exec.Output("cat", "/proc/stat")
	if err != nil {
		return 0, 0
	}
	line := strings.SplitN(string(raw), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return 0, 0
	}
	var parts []uint64
	for _, f := range fields[1:] {
		if v, err := strconv.ParseUint(f, 10, 64); err == nil {
			parts = append(parts, v)
		}
	}
	var total uint64
	for _, v := range parts {
		total += v
	}
	idle := parts[3]
	if len(parts) > 4 {
		idle += parts[4]
	}
	return idle, total
}

// round2 保留两位小数
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100.0
}

// SetHostname 设置主机名并验证(§9)
func (s *Service) SetHostname(h string) (map[string]any, error) {
	name, err := ValidateHostname(h)
	if err != nil {
		return nil, err
	}
	// hostnamectl 优先,回退 hostname 命令
	if _, err := s.Exec.Output("hostnamectl", "set-hostname", name); err != nil {
		if err2 := s.Exec.RunScript("hostname " + hostexec.Quote(name)); err2 != nil {
			return nil, errs.Newf(errs.EXEC_FAILED, "设置主机名失败: %v", err)
		}
	}
	// Verify:读回确认
	cur, err := s.Exec.Hostname()
	if err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "设置后验证失败: %v", err)
	}
	if !strings.EqualFold(cur, name) {
		return nil, errs.Newf(errs.EXEC_FAILED, "设置后验证失败:当前 %q,期望 %q", cur, name)
	}
	return map[string]any{"ok": true, "hostname": cur}, nil
}

// Hostname 读取主机名
func (s *Service) Hostname() (string, error) {
	return s.Exec.Hostname()
}

// Reboot 重启宿主机(§10:权限检查 + 二次确认 + 审计由 API 层负责)
func (s *Service) Reboot() error {
	// 先尝试 systemctl,回退 reboot 命令
	if _, err := s.Exec.Output("systemctl", "reboot"); err != nil {
		if err2 := s.Exec.RunScript("reboot"); err2 != nil {
			return errs.Newf(errs.EXEC_FAILED, "重启失败: %v", err)
		}
	}
	return nil
}

// ---------- 数据读取 ----------

// distroInfo 读取发行版信息(/etc/os-release)
func (s *Service) distroInfo() (name, id, version string) {
	content, err := s.Exec.ReadFileString("/etc/os-release")
	if err != nil {
		// 回退 /etc/redhat-release
		if c2, err2 := s.Exec.ReadFileString("/etc/redhat-release"); err2 == nil {
			return strings.TrimSpace(c2), "rhel", ""
		}
		return "linux", "linux", ""
	}
	vals := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		kv := strings.SplitN(line, "=", 2)
		if len(kv) == 2 {
			vals[kv[0]] = strings.Trim(strings.TrimSpace(kv[1]), `"'`)
		}
	}
	name = vals["NAME"]
	if name == "" {
		name = vals["ID"]
	}
	return name, vals["ID"], vals["VERSION_ID"]
}

// cpuInfo /proc/cpuinfo:型号 + 核数
func (s *Service) cpuInfo() (string, int) {
	content, err := s.Exec.ReadFileString("/proc/cpuinfo")
	if err != nil {
		return "", 0
	}
	model := ""
	cores := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "model name") && model == "" {
			model = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		}
		if strings.HasPrefix(line, "processor") {
			cores++
		}
	}
	// 有物理核限制的容器环境显示逻辑核;尽力而为
	return model, cores
}

// memInfo /proc/meminfo (kB → bytes)
func (s *Service) memInfo() (total, avail uint64) {
	content, err := s.Exec.ReadFileString("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(content, "\n") {
		var v uint64
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			fmt.Sscanf(line, "MemTotal: %d", &v)
			total = v * 1024
		case strings.HasPrefix(line, "MemAvailable:"):
			fmt.Sscanf(line, "MemAvailable: %d", &v)
			avail = v * 1024
		}
	}
	return total, avail
}

// uptime 秒
func (s *Service) uptime() uint64 {
	content, err := s.Exec.ReadFileString("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(content)
	if len(f) == 0 {
		return 0
	}
	secs, _ := strconv.ParseFloat(f[0], 64)
	return uint64(secs)
}

// loadAvg 1/5/15 分钟负载
func (s *Service) loadAvg() []float64 {
	content, err := s.Exec.ReadFileString("/proc/loadavg")
	if err != nil {
		return nil
	}
	f := strings.Fields(content)
	if len(f) < 3 {
		return nil
	}
	out := make([]float64, 3)
	for i := 0; i < 3; i++ {
		out[i], _ = strconv.ParseFloat(f[i], 64)
	}
	return out
}

// diskRoot 根文件系统总量/已用(df)
func (s *Service) diskRoot() (total, used uint64) {
	out, err := s.Exec.Output("df", "-B1", "--output=size,used", "/")
	if err != nil {
		return 0, 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, 0
	}
	f := strings.Fields(lines[1])
	if len(f) < 2 {
		return 0, 0
	}
	total, _ = strconv.ParseUint(f[0], 10, 64)
	used, _ = strconv.ParseUint(f[1], 10, 64)
	return total, used
}

// swapInfo /proc/meminfo
func (s *Service) swapInfo() (total, used uint64) {
	content, err := s.Exec.ReadFileString("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var totalKb, freeKb uint64
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "SwapTotal:"):
			fmt.Sscanf(line, "SwapTotal: %d", &totalKb)
		case strings.HasPrefix(line, "SwapFree:"):
			fmt.Sscanf(line, "SwapFree: %d", &freeKb)
		}
	}
	return totalKb * 1024, (totalKb - freeKb) * 1024
}

func pct(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

// CurrentUser 当前用户(审计用)
func CurrentUser() string {
	return os.Getenv("USER")
}
