// Package files 宿主文件系统操作层(§55)。
//
// 两种执行路径,行为完全一致:
//   - Direct(二进制部署):操作在 Agent 进程内以纯 Go os 调用执行。
//   - Nsenter(容器部署):Agent 经 `nsenter -t 1 -m -u -i -n -- /proc/self/exe fsop <op> ...`
//     重新执行自身二进制进入宿主挂载命名空间,再以同一套纯 Go 代码操作宿主文件。
//     这样文件逻辑只有一份实现,且宿主无需预装 find/tar/unzip 等任何工具。
//
// 安全(§20/§21/§22):
//   - 所有路径经 filepath.Clean 规范化,entry 名称禁止含 "/"、"."、".."(防穿越);
//   - 危险系统目录(§19)禁止删除(精确匹配顶层目录本身,目录内文件不受限);
//   - 列表/压缩/搜索均不递归进入符号链接(Go WalkDir 语义),避免 A->B B->A 循环;
//   - 解压防 zip-slip:条目清理后必须仍位于目标目录内,符号链接目标越界则跳过。
package files

import (
	"path/filepath"
	"strings"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// DangerousDirs 禁止删除的宿主系统目录(§19 危险目录;仅禁止删除目录本身,
// 目录内文件/子目录的删除不受影响 —— 管理员可正常清理 /var/log 等)。
var DangerousDirs = []string{
	"/", "/boot", "/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/root", "/var",
}

// SpecialFS 特殊文件系统(浏览正常,递归搜索时跳过,避免无限遍历)
var SpecialFS = []string{"proc", "sys", "dev", "run"}

// CleanPath 规范化并校验路径:拒绝空/NUL,filepath.Clean,要求绝对路径。
// 返回值即实际操作的目标路径(确定性,不存在歧义解析)。
func CleanPath(p string) (string, error) {
	if p == "" {
		return "", errs.New(errs.PATH_INVALID, "路径不能为空")
	}
	if strings.ContainsRune(p, 0) {
		return "", errs.New(errs.PATH_INVALID, "路径包含非法字符")
	}
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) {
		return "", errs.Newf(errs.PATH_INVALID, "路径必须为绝对路径: %s", p)
	}
	return clean, nil
}

// JoinEntry 在目录下拼接 entry 名并校验(防路径穿越)。
// name 禁止为空、"."、"..",禁止包含路径分隔符。
func JoinEntry(dir, name string) (string, error) {
	if name == "" || name == "." || name == ".." {
		return "", errs.New(errs.PATH_INVALID, "文件名非法")
	}
	if strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, 0) {
		return "", errs.Newf(errs.PATH_INVALID, "文件名不能包含路径分隔符: %s", name)
	}
	base, err := CleanPath(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name), nil
}

// CheckDangerousRemove 危险目录删除保护:精确匹配危险列表则拒绝。
// Windows(Local Direct 测试)额外保护盘符根(如 C:\)与 "/" 的等价形式。
func CheckDangerousRemove(cleanPath string) error {
	for _, d := range DangerousDirs {
		if cleanPath == d {
			return errs.Newf(errs.DANGEROUS_PATH, "系统目录 %s 禁止删除(防止误操作导致系统损坏)", d)
		}
	}
	// Windows 盘符根(仅本地测试场景;Linux 无此形态)
	if len(cleanPath) == 3 && cleanPath[1] == ':' && (cleanPath[2] == '\\' || cleanPath[2] == '/') {
		return errs.Newf(errs.DANGEROUS_PATH, "磁盘根目录 %s 禁止删除", cleanPath)
	}
	if cleanPath == "\\" || cleanPath == "/" {
		return errs.Newf(errs.DANGEROUS_PATH, "根目录禁止删除")
	}
	return nil
}

// SafeJoinInDest 解压防穿越:把(可能恶意的)entry 名安全拼到目标目录下。
// entry 名来自压缩包(相对路径,可含子目录);越出目标目录(../ 或绝对路径)返回错误。
func SafeJoinInDest(dest, name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", errs.New(errs.PATH_INVALID, "压缩包条目名非法")
	}
	if strings.HasPrefix(name, "/") {
		return "", errs.Newf(errs.PATH_INVALID, "压缩包内不允许绝对路径条目: %s", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	sep := string(filepath.Separator)
	if clean == ".." || strings.HasPrefix(clean, ".."+sep) {
		return "", errs.Newf(errs.PATH_INVALID, "压缩包条目越出目标目录: %s", name)
	}
	base, err := CleanPath(dest)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(base, clean)
	if joined != base && !strings.HasPrefix(joined, base+sep) {
		return "", errs.Newf(errs.PATH_INVALID, "压缩包条目越出目标目录: %s", name)
	}
	return joined, nil
}
