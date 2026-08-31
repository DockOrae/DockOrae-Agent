package files

import (
	"os"
	"path/filepath"
	"time"
)

// Entry 文件/目录条目(与前端 HostFile 类型一一对应)
type Entry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Type        string `json:"type"` // file|directory|symlink|socket|device|fifo|unknown
	Size        int64  `json:"size"`
	ModifiedAt  string `json:"modified_at"` // RFC3339
	Mode        uint32 `json:"mode"`        // 完整 st_mode(含文件类型位)
	Permissions string `json:"permissions"` // "drwxr-xr-x"
	Owner       string `json:"owner"`
	Group       string `json:"group"`
	Target      string `json:"target,omitempty"` // 符号链接目标
}

// FileType 类型集合(与前端 FileType 对齐)
const (
	TypeFile      = "file"
	TypeDirectory = "directory"
	TypeSymlink   = "symlink"
	TypeSocket    = "socket"
	TypeDevice    = "device"
	TypeFIFO      = "fifo"
	TypeUnknown   = "unknown"
)

// typeOf lstat 模式 → 类型字符串
func typeOf(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return TypeSymlink
	case mode.IsDir():
		return TypeDirectory
	case mode&os.ModeSocket != 0:
		return TypeSocket
	case mode&os.ModeDevice != 0:
		return TypeDevice
	case mode&os.ModeNamedPipe != 0:
		return TypeFIFO
	case mode.IsRegular():
		return TypeFile
	default:
		return TypeUnknown
	}
}

// permString 经典权限串(如 drwxr-xr-x)
func permString(mode os.FileMode) string {
	var b [10]byte
	b[0] = '-'
	switch {
	case mode&os.ModeSymlink != 0:
		b[0] = 'l'
	case mode.IsDir():
		b[0] = 'd'
	case mode&os.ModeSocket != 0:
		b[0] = 's'
	case mode&os.ModeDevice != 0:
		if mode&os.ModeCharDevice != 0 {
			b[0] = 'c'
		} else {
			b[0] = 'b'
		}
	case mode&os.ModeNamedPipe != 0:
		b[0] = 'p'
	}
	rwx := func(bit os.FileMode, c byte) byte {
		if mode&bit != 0 {
			return c
		}
		return '-'
	}
	b[1] = rwx(mode>>6&7&4, 'r') // owner read
	b[2] = rwx(mode>>6&7&2, 'w')
	b[3] = rwx(mode>>6&7&1, 'x')
	b[4] = rwx(mode>>3&7&4, 'r')
	b[5] = rwx(mode>>3&7&2, 'w')
	b[6] = rwx(mode>>3&7&1, 'x')
	b[7] = rwx(mode&7&4, 'r')
	b[8] = rwx(mode&7&2, 'w')
	b[9] = rwx(mode&7&1, 'x')
	// setuid/setgid/sticky 位
	if mode&os.ModeSetuid != 0 {
		b[3] = map[byte]byte{'x': 's', '-': 'S'}[b[3]]
	}
	if mode&os.ModeSetgid != 0 {
		b[6] = map[byte]byte{'x': 's', '-': 'S'}[b[6]]
	}
	if mode&os.ModeSticky != 0 {
		b[9] = map[byte]byte{'x': 't', '-': 'T'}[b[9]]
	}
	return string(b[:])
}

// statEntry lstat 单个路径 → Entry(不跟随符号链接)
func statEntry(path string) (Entry, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	e := entryFromInfo(path, fi)
	if e.Type == TypeSymlink {
		if t, err := os.Readlink(path); err == nil {
			e.Target = t
		}
	}
	return e, nil
}

// entryFromInfo 构建 Entry(owner/group 由平台实现填充)
func entryFromInfo(path string, fi os.FileInfo) Entry {
	mode := fi.Mode()
	e := Entry{
		Name:        filepath.Base(path),
		Path:        path,
		Type:        typeOf(mode),
		Size:        fi.Size(),
		ModifiedAt:  fi.ModTime().UTC().Format(time.RFC3339),
		Mode:        uint32(mode.Perm()),
		Permissions: permString(mode),
	}
	e.Owner, e.Group = ownerGroup(path, fi)
	return e
}
