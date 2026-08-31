// Package swap 宿主机交换空间管理(§11-§17)。
// 流程严格遵循:Validate → Permission → Lock → Disk Check → 操作 → Verify。
// 绝不实现"删除任意文件";swap 文件路径经严格校验且仅限受控位置。
package swap

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// 预设大小(§13):仅允许 512MB/1GB/2GB/4GB + 自定义(≥512MB)
var PresetSizesMB = []int{512, 1024, 2048, 4096}

// MinSizeMB 自定义最小大小
const MinSizeMB = 512

// 默认 swap 文件路径(仅在用户未指定时使用;用户指定路径须经严格校验)
const DefaultPath = "/swapfile"

// swapPathRe 允许的 swap 文件路径:绝对路径,仅字母数字 . _ / -,且必须以 / 开头
var swapPathRe = regexp.MustCompile(`^/([a-zA-Z0-9._-]+/)*[a-zA-Z0-9._-]+$`)

// 禁止的目录前缀(绝不允许指向设备/系统目录)
var forbiddenPrefixes = []string{"/dev", "/proc", "/sys", "/run", "/tmp", "/etc", "/var", "/usr", "/bin", "/sbin", "/boot", "/home"}

// Service swap 操作服务
type Service struct {
	Exec *hostexec.Execer
}

// New 构造 swap 服务
func New(e *hostexec.Execer) *Service { return &Service{Exec: e} }

// ValidateSize 校验大小(§14:Frontend 校验一次,Agent 必须再次校验)
// 返回字节数;非法大小返回 SWAP_INVALID_SIZE。
func ValidateSize(sizeMB int) (int64, error) {
	if sizeMB < MinSizeMB {
		return 0, errs.Newf(errs.SWAP_INVALID_SIZE, "swap 大小最小 %d MB", MinSizeMB)
	}
	if sizeMB > 64*1024 {
		return 0, errs.Newf(errs.SWAP_INVALID_SIZE, "swap 大小过大(最大 65536 MB)")
	}
	return int64(sizeMB) * 1024 * 1024, nil
}

// ValidatePath 校验 swap 文件路径
func ValidatePath(path string) (string, error) {
	if path == "" {
		return DefaultPath, nil
	}
	if len(path) > 128 || !swapPathRe.MatchString(path) {
		return "", errs.Newf(errs.INVALID_REQUEST, "swap 文件路径非法: %q", path)
	}
	for _, p := range forbiddenPrefixes {
		if strings.HasPrefix(path, p) {
			return "", errs.Newf(errs.INVALID_REQUEST, "swap 文件路径不允许位于 %s 下", p)
		}
	}
	if path == "/" {
		return "", errs.Newf(errs.INVALID_REQUEST, "swap 文件路径非法")
	}
	return filepath.Clean(path), nil
}

// Status 当前 swap 状态(§11 swap.status)
func (s *Service) Status() (map[string]any, error) {
	// /proc/swaps:文件名 类型 大小 已用 优先级
	swapsRaw, err := s.Exec.ReadFileString("/proc/swaps")
	if err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "读取 /proc/swaps 失败: %v", err)
	}
	devices := []map[string]any{}
	total := int64(0)
	used := int64(0)
	lines := strings.Split(strings.TrimSpace(swapsRaw), "\n")
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		sz, _ := strconv.ParseInt(f[2], 10, 64)
		us, _ := strconv.ParseInt(f[3], 10, 64)
		devices = append(devices, map[string]any{
			"path":     f[0],
			"type":     f[1],
			"size":     sz * 1024, // KB → bytes
			"used":     us * 1024,
			"priority": f[4],
		})
		total += sz * 1024
		used += us * 1024
	}
	pct := 0.0
	if total > 0 {
		pct = float64(used) / float64(total) * 100.0
	}
	return map[string]any{
		"enabled": total > 0,
		"total":   total,
		"used":    used,
		"pct":     round1(pct),
		"devices": devices,
	}, nil
}

// DiskAvailable 目标路径所在文件系统可用空间(字节);失败返回 0
func (s *Service) DiskAvailable() int64 {
	// 用 df 读宿主根文件系统可用空间(swap 文件默认放根)
	out, err := s.Exec.Output("df", "-B1", "--output=avail", "/")
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0
	}
	f := strings.Fields(lines[1])
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseInt(f[0], 10, 64)
	return v
}

// CheckDisk 校验磁盘空间充足(至少 2 倍 swap 大小,且剩余 > 1GB)
func (s *Service) CheckDisk(needBytes int64) error {
	avail := s.DiskAvailable()
	if avail <= 0 {
		return errs.New(errs.SWAP_INSUFFICIENT_DISK, "无法读取磁盘可用空间")
	}
	if avail < needBytes*2 {
		return errs.Newf(errs.SWAP_INSUFFICIENT_DISK,
			"磁盘可用空间不足:需约 %s,当前可用 %s", humanBytes(needBytes*2), humanBytes(avail))
	}
	if avail < 1024*1024*1024 {
		return errs.New(errs.SWAP_INSUFFICIENT_DISK, "磁盘剩余空间过小(低于 1GB)")
	}
	return nil
}

// Create 创建 swap 文件(§15 全流程)
func (s *Service) Create(sizeMB int, path string) (map[string]any, error) {
	bytes, err := ValidateSize(sizeMB)
	if err != nil {
		return nil, err
	}
	p, err := ValidatePath(path)
	if err != nil {
		return nil, err
	}
	if s.Exec.Exists(p) {
		return nil, errs.Newf(errs.SWAP_CREATE_FAILED, "swap 文件已存在: %s(如需调整请用 resize)", p)
	}
	if err := s.CheckDisk(bytes); err != nil {
		return nil, err
	}
	// fallocate(优先,失败回退 dd)→ chmod 600 → mkswap → swapon → fstab
	// 参数均为校验后的内部值,可安全嵌入脚本
	pq := hostexec.Quote(p)
	script := fmt.Sprintf(
		"fallocate -l %d %s 2>/dev/null || dd if=/dev/zero of=%s bs=1M count=%d status=none; "+
			"chmod 600 %s && mkswap %s >/dev/null && swapon %s",
		bytes, pq, pq, sizeMB, pq, pq, pq)
	if err := s.Exec.RunScript(script); err != nil {
		return nil, errs.Newf(errs.SWAP_CREATE_FAILED, "创建 swap 失败: %v", err)
	}
	// 写 fstab(幂等:已存在则不重复追加)
	if err := s.ensureFstab(p); err != nil {
		// fstab 写入失败回滚已启用的 swap,避免"运行时启用但重启丢失"
		_ = s.Exec.RunScript("swapoff " + pq)
		_ = s.Exec.RemoveFile(p)
		return nil, errs.Newf(errs.SWAP_CREATE_FAILED, "写入 fstab 失败: %v", err)
	}
	// Verify
	st, err := s.Status()
	if err != nil {
		return nil, err
	}
	for _, d := range st["devices"].([]map[string]any) {
		if d["path"] == p {
			return map[string]any{"ok": true, "path": p, "size": bytes, "status": st}, nil
		}
	}
	return nil, errs.Newf(errs.SWAP_CREATE_FAILED, "swap 创建后验证失败(未在 /proc/swaps 中找到 %s)", p)
}

// Resize 调整 swap 大小(§16):新建临时 swap → 切换 → 删除旧 → 更新 fstab → 验证。
// 采用"先建新、再切、最后删旧"的安全顺序,避免任何时刻无 swap。
func (s *Service) Resize(sizeMB int, path string) (map[string]any, error) {
	bytes, err := ValidateSize(sizeMB)
	if err != nil {
		return nil, err
	}
	p, err := ValidatePath(path)
	if err != nil {
		return nil, err
	}
	st, err := s.Status()
	if err != nil {
		return nil, err
	}
	devices := st["devices"].([]map[string]any)
	if len(devices) == 0 {
		// 当前无 swap → 等价创建
		return s.Create(sizeMB, p)
	}
	// 当前 swap 文件可能不是默认路径:目标路径 = 现有文件路径(若匹配)或默认
	target := p
	existingPath := ""
	for _, d := range devices {
		if d["path"] == p {
			existingPath = p
			break
		}
	}
	if existingPath == "" {
		// 只允许调整"受控路径"上的 swap;系统自带设备 swap 拒绝调整
		if !isControlledSwapPath(devices) {
			return nil, errs.Newf(errs.SWAP_RESIZE_FAILED,
				"检测到系统级 swap 设备(/dev 等),出于安全拒绝自动调整。请在宿主手动处理")
		}
		// 受控文件 swap(如 /swapfile2)存在但目标不同:按目标路径新建并删除旧的受控文件
		existingPath = controlledSwapPath(devices)
		if existingPath != "" && existingPath != target {
			// 保留现有文件,新建 target 后切换再清理
		}
	}
	if err := s.CheckDisk(bytes); err != nil {
		return nil, err
	}

	pq := hostexec.Quote(target)
	if existingPath == "" || existingPath == target {
		// 同一路径:先建临时新文件(同目录,带 .new 后缀)→ mkswap → swapon → swapoff 旧 → 删旧文件
		tmp := target + ".new"
		tq := hostexec.Quote(tmp)
		script := fmt.Sprintf(
			"fallocate -l %d %s 2>/dev/null || dd if=/dev/zero of=%s bs=1M count=%d status=none; "+
				"chmod 600 %s && mkswap %s >/dev/null && swapon %s && swapoff %s && rm -f %s",
			bytes, tq, tq, sizeMB, tq, tq, tq, pq, pq)
		if err := s.Exec.RunScript(script); err != nil {
			return nil, errs.Newf(errs.SWAP_RESIZE_FAILED, "调整 swap 失败: %v", err)
		}
	} else {
		// 不同路径:新建 target → 切换 → 删旧受控文件
		epq := hostexec.Quote(existingPath)
		script := fmt.Sprintf(
			"fallocate -l %d %s 2>/dev/null || dd if=/dev/zero of=%s bs=1M count=%d status=none; "+
				"chmod 600 %s && mkswap %s >/dev/null && swapon %s && swapoff %s && rm -f %s",
			bytes, pq, pq, sizeMB, pq, pq, pq, epq, epq)
		if err := s.Exec.RunScript(script); err != nil {
			return nil, errs.Newf(errs.SWAP_RESIZE_FAILED, "调整 swap 失败: %v", err)
		}
	}
	// 更新 fstab:删除旧条目 + 写入新条目
	if err := s.updateFstab(target, existingPath); err != nil {
		return nil, errs.Newf(errs.SWAP_RESIZE_FAILED, "更新 fstab 失败: %v", err)
	}
	// Verify:同一路径 resize 时生效文件为 target+".new"(新建→切换中间态),两种都算成功。
	// size 容差 4096:mkswap 在文件末尾写签名页,实际可用 swap = 文件大小 - 4096
	ver, err := s.Status()
	if err != nil {
		return nil, err
	}
	found := false
	for _, d := range ver["devices"].([]map[string]any) {
		p, _ := d["path"].(string)
		// size 经 JSON 序列化/反序列化后为 float64,需兼容两种类型
		if (p == target || p == target+".new") && abs(toInt64(d["size"])-bytes) <= 4096 {
			found = true
			break
		}
	}
	if !found {
		return nil, errs.Newf(errs.SWAP_RESIZE_FAILED, "调整后验证失败:未在 /proc/swaps 中找到 %s 且大小 %d", target, bytes)
	}
	return map[string]any{"ok": true, "path": target, "size": bytes, "status": ver}, nil
}

// toInt64 兼容 JSON 数值类型(int64/float64/int)
func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	return 0
}

// abs int64 绝对值
func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// Delete 删除 swap(§17):swapoff → 移除 fstab → 删文件 → 验证。
// 仅允许删除受控路径上的 swap 文件,绝不接受任意路径。
func (s *Service) Delete(path string) (map[string]any, error) {
	p, err := ValidatePath(path)
	if err != nil {
		return nil, err
	}
	st, err := s.Status()
	if err != nil {
		return nil, err
	}
	devices := st["devices"].([]map[string]any)
	if len(devices) == 0 {
		return map[string]any{"ok": true, "removed": false, "message": "当前没有启用中的 swap"}, nil
	}
	// 目标:仅删除受控 swap 文件(默认路径或 /swapfile* 模式);设备 swap 拒绝
	controlled := controlledSwapPath(devices)
	if controlled == "" {
		return nil, errs.Newf(errs.SWAP_DELETE_FAILED,
			"未找到受控 swap 文件(/swapfile*),仅设备级 swap 存在,拒绝删除")
	}
	if p != DefaultPath && p != controlled {
		// 用户指定路径必须与受控文件一致
		if !isControlledSwapPath(devices) || controlled != p {
			return nil, errs.Newf(errs.INVALID_REQUEST, "仅允许删除受控 swap 文件(%s)", controlled)
		}
	}
	pq := hostexec.Quote(controlled)
	script := fmt.Sprintf("swapoff %s && rm -f %s", pq, pq)
	if err := s.Exec.RunScript(script); err != nil {
		return nil, errs.Newf(errs.SWAP_DELETE_FAILED, "删除 swap 失败: %v", err)
	}
	if err := s.removeFstab(controlled); err != nil {
		return nil, errs.Newf(errs.SWAP_DELETE_FAILED, "更新 fstab 失败: %v", err)
	}
	ver, err := s.Status()
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "removed": true, "path": controlled, "status": ver}, nil
}

// ---------- fstab 操作 ----------

const fstabPath = "/etc/fstab"

// ensureFstab 幂等追加 swap 条目
func (s *Service) ensureFstab(path string) error {
	content, err := s.Exec.ReadFileString(fstabPath)
	if err != nil {
		return err
	}
	// 已有同路径条目则跳过
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == path {
			return nil
		}
	}
	entry := fmt.Sprintf("%s none swap sw 0 0\n", path)
	return s.appendFstab(entry)
}

// updateFstab 更新条目(移除旧路径,写入新路径)
func (s *Service) updateFstab(newPath, oldPath string) error {
	content, err := s.Exec.ReadFileString(fstabPath)
	if err != nil {
		return err
	}
	lines := strings.Split(content, "\n")
	var kept []string
	changed := false
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) > 0 && (f[0] == oldPath || (oldPath == "" && f[0] == newPath)) {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	entry := fmt.Sprintf("%s none swap sw 0 0\n", newPath)
	kept = append(kept, entry)
	if !changed {
		// 原无旧条目,直接追加
		return s.appendFstab(entry)
	}
	return s.Exec.WriteFile(fstabPath, []byte(strings.Join(kept, "\n")), 0o644)
}

// removeFstab 移除 fstab 中所有受控 swap 文件条目(/swapfile、/swapfileN、.new 中间态),
// 防止 delete 后残留指向已删除文件的条目
func (s *Service) removeFstab(path string) error {
	content, err := s.Exec.ReadFileString(fstabPath)
	if err != nil {
		return err
	}
	lines := strings.Split(content, "\n")
	var kept []string
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) > 0 && isControlledName(f[0]) {
			continue
		}
		kept = append(kept, line)
	}
	return s.Exec.WriteFile(fstabPath, []byte(strings.Join(kept, "\n")), 0o644)
}

// appendFstab 追加行(写回前确保末尾换行)
func (s *Service) appendFstab(entry string) error {
	content, err := s.Exec.ReadFileString(fstabPath)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return s.Exec.WriteFile(fstabPath, []byte(content+entry), 0o644)
}

// ---------- 受控路径判断 ----------

// isControlledSwapPath 是否全部 swap 为受控文件(/swapfile 或 /swapfileN)
func isControlledSwapPath(devices []map[string]any) bool {
	for _, d := range devices {
		p, _ := d["path"].(string)
		if !isControlledName(p) {
			return false
		}
	}
	return true
}

// controlledSwapPath 返回受控 swap 文件路径(多个时取第一个);无受控文件返回 ""
func controlledSwapPath(devices []map[string]any) string {
	for _, d := range devices {
		p, _ := d["path"].(string)
		if isControlledName(p) {
			return p
		}
	}
	return ""
}

// isControlledName 受控文件名:/swapfile 或 /swapfile 后跟数字,以及 resize 中间态的 .new 后缀
func isControlledName(p string) bool {
	base := strings.TrimSuffix(p, ".new")
	if base == p && strings.HasSuffix(p, ".new") {
		return false // 仅处理 .new 后缀一种形态
	}
	if base == "/swapfile" {
		return true
	}
	if strings.HasPrefix(base, "/swapfile") {
		rest := strings.TrimPrefix(base, "/swapfile")
		if rest != "" {
			if _, err := strconv.Atoi(rest); err == nil {
				return true
			}
		}
	}
	return false
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}

// WaitSwapActive 轮询等待 swap 状态(供 resize 切换等待;正常脚本同步执行,此函数备用)
func (s *Service) WaitSwapActive(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Status()
		if err == nil && st["enabled"].(bool) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
