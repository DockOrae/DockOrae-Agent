package files

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// ---------- 回收站(XDG 标准 ~/.local/share/Trash,§55 回收站) ----------
//
// 布局:
//   ~/.local/share/Trash/files/  删除的文件/目录(原名,冲突加时间戳后缀)
//   ~/.local/share/Trash/info/   <name>.trashinfo(Path=原绝对路径,DeletionDate)
//
// 开关状态存 Agent 数据目录 recycle.json(宿主侧持久化,面板不持有)。

const recycleStateFile = "recycle.json"

// TrashService 回收站操作(纯 Go,Direct/nsenter 共用;nsenter 模式下 HOME=/root)
type TrashService struct {
	home    string // 宿主用户主目录(决定回收站位置)
	dataDir string // Agent 数据目录(存开关状态)
}

// NewTrash 构造回收站服务
func NewTrash(home, dataDir string) *TrashService {
	if home == "" {
		home = "/root"
	}
	return &TrashService{home: home, dataDir: dataDir}
}

// TrashRoot 回收站根目录
func (t *TrashService) TrashRoot() string {
	return filepath.Join(t.home, ".local", "share", "Trash")
}

// filesDir / infoDir 回收站子目录
func (t *TrashService) filesDir() string { return filepath.Join(t.TrashRoot(), "files") }
func (t *TrashService) infoDir() string  { return filepath.Join(t.TrashRoot(), "info") }

// Enabled 回收站是否开启(默认开启)
func (t *TrashService) Enabled() bool {
	raw, err := os.ReadFile(filepath.Join(t.dataDir, recycleStateFile))
	if err != nil {
		return true
	}
	var st struct {
		Enabled *bool `json:"enabled"`
	}
	if json.Unmarshal(raw, &st) == nil && st.Enabled != nil {
		return *st.Enabled
	}
	return true
}

// SetEnabled 设置开关
func (t *TrashService) SetEnabled(enabled bool) error {
	if err := os.MkdirAll(t.dataDir, 0o755); err != nil {
		return errs.Newf(errs.EXEC_FAILED, "创建数据目录失败: %v", err)
	}
	b, _ := json.Marshal(map[string]bool{"enabled": enabled})
	return atomicWrite(filepath.Join(t.dataDir, recycleStateFile), b, 0o600)
}

// Status 回收站状态
func (t *TrashService) Status() map[string]any {
	return map[string]any{
		"enabled":  t.Enabled(),
		"trashDir": t.TrashRoot(),
	}
}

// TrashItem 回收站条目
type TrashItem struct {
	Name       string `json:"name"`
	SourcePath string `json:"source_path"`
	Size       int64  `json:"size"`
	DeleteTime string `json:"delete_time"`
	IsDir      bool   `json:"is_dir"`
}

// List 回收站列表(按删除时间倒序)
func (t *TrashService) List() ([]TrashItem, error) {
	filesDir := t.filesDir()
	entries, err := os.ReadDir(filesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []TrashItem{}, nil
		}
		return nil, opErr("读取回收站", filesDir, err)
	}
	items := make([]TrashItem, 0, len(entries))
	for _, de := range entries {
		name := de.Name()
		item := TrashItem{Name: name}
		if de.IsDir() {
			item.IsDir = true
		} else {
			if fi, err := de.Info(); err == nil {
				item.Size = fi.Size()
			}
		}
		// 读 trashinfo 取原路径 + 删除时间
		if info, err := t.readInfo(name); err == nil {
			item.SourcePath = info.Path
			item.DeleteTime = info.DeletionDate
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DeleteTime > items[j].DeleteTime })
	return items, nil
}

// trashInfo XDG trashinfo 文件内容
type trashInfo struct {
	Path         string
	DeletionDate string
}

// readInfo 解析 <name>.trashinfo
func (t *TrashService) readInfo(name string) (*trashInfo, error) {
	raw, err := os.ReadFile(filepath.Join(t.infoDir(), name+".trashinfo"))
	if err != nil {
		return nil, err
	}
	info := &trashInfo{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Path=") {
			if v, err := url.QueryUnescape(strings.TrimPrefix(line, "Path=")); err == nil {
				info.Path = v
			}
		} else if strings.HasPrefix(line, "DeletionDate=") {
			info.DeletionDate = strings.TrimPrefix(line, "DeletionDate=")
		}
	}
	return info, nil
}

// writeInfo 写 trashinfo
func (t *TrashService) writeInfo(name, sourcePath string) error {
	if err := os.MkdirAll(t.infoDir(), 0o755); err != nil {
		return opErr("写入回收站信息", t.infoDir(), err)
	}
	esc := url.PathEscape(sourcePath)
	content := "[Trash Info]\nPath=" + esc + "\nDeletionDate=" + time.Now().UTC().Format(time.RFC3339) + "\n"
	return atomicWrite(filepath.Join(t.infoDir(), name+".trashinfo"), []byte(content), 0o600)
}

// MoveToTrash 把多个路径移入回收站(危险目录保护先行;跨设备退化为复制+删除)
func (t *TrashService) MoveToTrash(paths []string) error {
	if len(paths) == 0 {
		return errs.New(errs.INVALID_REQUEST, "未指定删除目标")
	}
	filesDir := t.filesDir()
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return opErr("创建回收站", filesDir, err)
	}
	for _, p := range paths {
		clean, err := CleanPath(p)
		if err != nil {
			return err
		}
		if err := CheckDangerousRemove(clean); err != nil {
			return err
		}
		if _, err := os.Lstat(clean); err != nil {
			return opErr("移入回收站", clean, err)
		}
		// 回收站内唯一名(冲突加时间戳后缀)
		base := filepath.Base(clean)
		target := filepath.Join(filesDir, base)
		if _, err := os.Lstat(target); err == nil {
			base = base + "." + time.Now().UTC().Format("20060102T150405.000000000")
			target = filepath.Join(filesDir, base)
		}
		// 跨设备退化:复制+删除(与 Move 一致)
		if err := os.Rename(clean, target); err != nil {
			var linkErr *os.LinkError
			if !errors.As(err, &linkErr) || !isCrossDevice(linkErr) {
				return opErr("移入回收站", clean, err)
			}
			if err := copyTree(clean, target); err != nil {
				return err
			}
			if err := os.RemoveAll(clean); err != nil {
				return opErr("移入回收站(清理源)", clean, err)
			}
		}
		if err := t.writeInfo(base, clean); err != nil {
			return err
		}
	}
	return nil
}

// Restore 恢复条目到原路径(原路径冲突时加 " (1)" 后缀;危险目录/越界路径拒绝)
func (t *TrashService) Restore(names []string) error {
	if len(names) == 0 {
		return errs.New(errs.INVALID_REQUEST, "未指定恢复目标")
	}
	filesDir := t.filesDir()
	for _, name := range names {
		src := filepath.Join(filesDir, name)
		if _, err := os.Lstat(src); err != nil {
			return opErr("恢复", src, err)
		}
		info, err := t.readInfo(name)
		if err != nil {
			// 无 trashinfo(旧数据):恢复到用户主目录
			info = &trashInfo{Path: filepath.Join(t.home, name)}
		}
		dest, err := CleanPath(info.Path)
		if err != nil {
			return err
		}
		if err := CheckDangerousRemove(dest); err != nil {
			return errs.Newf(errs.DANGEROUS_PATH, "回收站条目原路径为系统目录,禁止恢复: %s", dest)
		}
		// 目标冲突:加后缀
		if _, err := os.Lstat(dest); err == nil {
			ext := filepath.Ext(dest)
			stem := strings.TrimSuffix(dest, ext)
			for i := 1; ; i++ {
				cand := stem + " (" + itoa(i) + ")" + ext
				if _, err := os.Lstat(cand); err != nil {
					dest = cand
					break
				}
			}
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return opErr("恢复", filepath.Dir(dest), err)
		}
		if err := os.Rename(src, dest); err != nil {
			var linkErr *os.LinkError
			if !errors.As(err, &linkErr) || !isCrossDevice(linkErr) {
				return opErr("恢复", src, err)
			}
			if err := copyTree(src, dest); err != nil {
				return err
			}
			if err := os.RemoveAll(src); err != nil {
				return opErr("恢复(清理源)", src, err)
			}
		}
		_ = os.Remove(filepath.Join(t.infoDir(), name+".trashinfo"))
	}
	return nil
}

// Delete 彻底删除回收站条目(不可恢复)
func (t *TrashService) Delete(names []string) error {
	if len(names) == 0 {
		return errs.New(errs.INVALID_REQUEST, "未指定删除目标")
	}
	filesDir := t.filesDir()
	for _, name := range names {
		target := filepath.Join(filesDir, name)
		fi, err := os.Lstat(target)
		if err != nil {
			return opErr("彻底删除", target, err)
		}
		if fi.IsDir() {
			if err := os.RemoveAll(target); err != nil {
				return opErr("彻底删除", target, err)
			}
		} else if err := os.Remove(target); err != nil {
			return opErr("彻底删除", target, err)
		}
		_ = os.Remove(filepath.Join(t.infoDir(), name+".trashinfo"))
	}
	return nil
}

// Empty 清空回收站
func (t *TrashService) Empty() error {
	if err := os.RemoveAll(t.filesDir()); err != nil {
		return opErr("清空回收站", t.filesDir(), err)
	}
	if err := os.RemoveAll(t.infoDir()); err != nil {
		return opErr("清空回收站", t.infoDir(), err)
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
