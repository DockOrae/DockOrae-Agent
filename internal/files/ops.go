package files

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// ListResult 目录列表结果(文件夹优先,再按名称排序)
type ListResult struct {
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
}

// List 列出目录(单层,不递归;特殊文件系统可正常浏览;showHidden 控制隐藏文件)
func List(dir string, showHidden bool) (*ListResult, error) {
	clean, err := CleanPath(dir)
	if err != nil && runtime.GOOS == "windows" && strings.TrimSpace(dir) == "/" {
		// Windows 本地 Direct 测试:"/" 归一化为当前盘根(生产 Linux 不受影响)
		clean, err = volumeRoot(), nil
	}
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(clean)
	if err != nil {
		return nil, opErr("读取目录", clean, err)
	}
	res := &ListResult{Path: clean, Entries: make([]Entry, 0, len(des))}
	for _, de := range des {
		if !showHidden && strings.HasPrefix(de.Name(), ".") {
			continue
		}
		full := filepath.Join(clean, de.Name())
		fi, err := os.Lstat(full)
		if err != nil {
			continue // 条目在读取期间消失/无权限:跳过
		}
		e := entryFromInfo(full, fi)
		if e.Type == TypeSymlink {
			if t, err := os.Readlink(full); err == nil {
				e.Target = t
			}
		}
		res.Entries = append(res.Entries, e)
	}
	// 文件夹优先,同类型按名称排序
	sort.SliceStable(res.Entries, func(i, j int) bool {
		di := res.Entries[i].Type == TypeDirectory
		dj := res.Entries[j].Type == TypeDirectory
		if di != dj {
			return di
		}
		return res.Entries[i].Name < res.Entries[j].Name
	})
	return res, nil
}

// DirSize 计算目录总大小(不跟随符号链接;/proc /sys /dev /run 特殊文件系统直接返回 0)
func DirSize(dir string) (int64, error) {
	clean, err := CleanPath(dir)
	if err != nil {
		return 0, err
	}
	for _, special := range []string{"/proc", "/sys", "/dev", "/run"} {
		if clean == special || strings.HasPrefix(clean, special+string(filepath.Separator)) {
			return 0, nil
		}
	}
	var total int64
	err = filepath.WalkDir(clean, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // 权限受限目录跳过,不中断
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // 不跟随符号链接(防循环)
		}
		if d.IsDir() {
			if p != clean && (d.Name() == "proc" || d.Name() == "sys" || d.Name() == "dev" || d.Name() == "run") {
				return filepath.SkipDir
			}
			total += 4096
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, opErr("计算目录大小", clean, err)
	}
	return total, nil
}

// Chown 修改所有者/用户组(Linux;owner/group 为空则不变)
func Chown(path, owner, group string) error {
	clean, err := CleanPath(path)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return errs.New(errs.UNSUPPORTED_ARCH, "chown 仅支持 Linux 宿主")
	}
	var uid, gid int
	if owner != "" {
		u, err := user.Lookup(owner)
		if err != nil {
			return errs.Newf(errs.INVALID_REQUEST, "用户不存在: %s", owner)
		}
		uid, err = strconv.Atoi(u.Uid)
		if err != nil {
			return errs.Newf(errs.INVALID_REQUEST, "用户 UID 无效: %s", u.Uid)
		}
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return errs.Newf(errs.INVALID_REQUEST, "用户组不存在: %s", group)
		}
		gid, err = strconv.Atoi(g.Gid)
		if err != nil {
			return errs.Newf(errs.INVALID_REQUEST, "用户组 GID 无效: %s", g.Gid)
		}
	}
	if owner == "" && group == "" {
		return errs.New(errs.INVALID_REQUEST, "请指定用户或用户组")
	}
	if err := os.Lchown(clean, uid, gid); err != nil {
		return opErr("修改所有者", clean, err)
	}
	return nil
}

// Stat 单个条目(lstat,不跟随符号链接)
func Stat(path string) (*Entry, error) {
	clean, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	e, err := statEntry(clean)
	if err != nil {
		return nil, opErr("读取", clean, err)
	}
	return &e, nil
}

// Touch 新建空文件(已存在则报错,防止误截断)
func Touch(path string) error {
	clean, err := CleanPath(path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(clean, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return opErr("创建文件", clean, err)
	}
	return f.Close()
}

// Mkdir 新建目录(单层;已存在则报错)
func Mkdir(path string) error {
	clean, err := CleanPath(path)
	if err != nil {
		return err
	}
	if err := os.Mkdir(clean, 0o755); err != nil {
		return opErr("创建目录", clean, err)
	}
	return nil
}

// Rename 重命名/移动(同文件系统内;目标已存在报错)
func Rename(oldPath, newPath string) error {
	oldC, err := CleanPath(oldPath)
	if err != nil {
		return err
	}
	newC, err := CleanPath(newPath)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(newC); err == nil {
		return errs.Newf(errs.FILE_EXISTS, "目标已存在: %s", newC)
	}
	if err := os.Rename(oldC, newC); err != nil {
		return opErr("重命名", oldC, err)
	}
	return nil
}

// Move 移动(跨设备时退化为 复制+删除)
func Move(srcPath, dstPath string) error {
	srcC, err := CleanPath(srcPath)
	if err != nil {
		return err
	}
	dstC, err := CleanPath(dstPath)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dstC); err == nil {
		return errs.Newf(errs.FILE_EXISTS, "目标已存在: %s", dstC)
	}
	err = os.Rename(srcC, dstC)
	if err == nil {
		return nil
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || !isCrossDevice(linkErr) {
		return opErr("移动", srcC, err)
	}
	// EXDEV:跨文件系统,复制后删除
	if err := copyTree(srcC, dstC); err != nil {
		return err
	}
	if err := os.RemoveAll(srcC); err != nil {
		return opErr("移动(清理源)", srcC, err)
	}
	return nil
}

// Remove 删除(多个)。目录必须 recursive=true;危险系统目录精确匹配禁止删除。
func Remove(paths []string, recursive bool) error {
	if len(paths) == 0 {
		return errs.New(errs.INVALID_REQUEST, "未指定删除目标")
	}
	for _, p := range paths {
		clean, err := CleanPath(p)
		if err != nil {
			return err
		}
		if err := CheckDangerousRemove(clean); err != nil {
			return err
		}
		fi, err := os.Lstat(clean)
		if err != nil {
			return opErr("删除", clean, err)
		}
		if fi.IsDir() && !recursive {
			return errs.Newf(errs.INVALID_REQUEST, "目录 %s 为递归删除,必须显式确认 recursive=true", clean)
		}
		if fi.IsDir() {
			if err := os.RemoveAll(clean); err != nil {
				return opErr("删除目录", clean, err)
			}
		} else if err := os.Remove(clean); err != nil {
			return opErr("删除", clean, err)
		}
	}
	return nil
}

// Copy 复制(递归;目标已存在报错;符号链接重建为链接)
func Copy(srcPath, dstPath string) error {
	srcC, err := CleanPath(srcPath)
	if err != nil {
		return err
	}
	dstC, err := CleanPath(dstPath)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dstC); err == nil {
		return errs.Newf(errs.FILE_EXISTS, "目标已存在: %s", dstC)
	}
	return copyTree(srcC, dstC)
}

// copyTree 复制文件/目录树(不跟随符号链接)
func copyTree(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return opErr("复制", src, err)
	}
	switch {
	case fi.IsDir():
		if err := os.MkdirAll(dst, fi.Mode().Perm()); err != nil {
			return opErr("复制", dst, err)
		}
		des, err := os.ReadDir(src)
		if err != nil {
			return opErr("复制", src, err)
		}
		for _, de := range des {
			if err := copyTree(filepath.Join(src, de.Name()), filepath.Join(dst, de.Name())); err != nil {
				return err
			}
		}
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return opErr("复制链接", src, err)
		}
		if err := os.Symlink(target, dst); err != nil {
			return opErr("复制链接", dst, err)
		}
	default:
		in, err := os.Open(src)
		if err != nil {
			return opErr("复制", src, err)
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, fi.Mode().Perm())
		if err != nil {
			return opErr("复制", dst, err)
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return opErr("复制", dst, err)
		}
		if err := out.Close(); err != nil {
			return opErr("复制", dst, err)
		}
	}
	return nil
}

// Chmod 修改权限(如 0755;与当前 umask 无关,直接设置)
func Chmod(path string, mode uint32) error {
	clean, err := CleanPath(path)
	if err != nil {
		return err
	}
	if err := os.Chmod(clean, os.FileMode(mode)); err != nil {
		return opErr("修改权限", clean, err)
	}
	return nil
}

// WriteJSON 覆盖写入(编辑器保存,JSON body 传入,上限 8MB,原子替换)
func WriteJSON(path string, data []byte) error {
	clean, err := CleanPath(path)
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return errs.Newf(errs.FILE_TOO_LARGE, "文件 %d 字节超过 8MB 上限,请用上传", len(data))
	}
	return atomicWrite(clean, data, 0o644)
}

// WriteStream 流式写入(上传;不限制大小,原子替换,保留目标已有权限)
func WriteStream(path string, r io.Reader) error {
	clean, err := CleanPath(path)
	if err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if fi, err := os.Lstat(clean); err == nil && !fi.IsDir() {
		perm = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(clean), ".agent-upload-*")
	if err != nil {
		return opErr("写入", clean, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return opErr("写入", clean, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), clean); err != nil {
		return opErr("写入", clean, err)
	}
	return nil
}

// ReadStream 流式读取(下载;直接模式返回 os.File)
func ReadStream(path string) (io.ReadCloser, error) {
	clean, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(clean)
	if err != nil {
		return nil, opErr("读取", clean, err)
	}
	return f, nil
}

// atomicWrite 临时文件 + rename 原子替换
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-write-*")
	if err != nil {
		return opErr("写入", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return opErr("写入", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return opErr("写入", path, err)
	}
	return nil
}

// CompressResult 压缩结果
type CompressResult struct {
	Archive     string `json:"archive"`
	Files       int    `json:"files"`
	Skipped     int    `json:"skipped"`
	SkippedWhy  string `json:"skipped_why,omitempty"`
}

// Compress 压缩目录内的若干条目为 tar.gz 或 zip(不跟随符号链接,链接原样保留)
func Compress(dir string, archiveName, format string, names []string) (*CompressResult, error) {
	if format != "tar.gz" && format != "zip" {
		return nil, errs.Newf(errs.UNSUPPORTED_ARCH, "不支持的压缩格式: %s(仅 tar.gz/zip)", format)
	}
	if len(names) == 0 {
		return nil, errs.New(errs.INVALID_REQUEST, "未指定压缩条目")
	}
	base, err := CleanPath(dir)
	if err != nil {
		return nil, err
	}
	archivePath, err := JoinEntry(base, archiveName)
	if err != nil {
		return nil, err
	}
	// 收集源条目(全部必须存在于 base 内)
	srcs := make([]string, 0, len(names))
	for _, n := range names {
		p, err := JoinEntry(base, n)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(p); err != nil {
			return nil, opErr("压缩", p, err)
		}
		srcs = append(srcs, p)
	}
	res := &CompressResult{Archive: archivePath}
	switch format {
	case "tar.gz":
		err = compressTarGz(archivePath, srcs, res)
	case "zip":
		err = compressZip(archivePath, srcs, res)
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

// compressTarGz 写 tar.gz(顶层保留原名)
func compressTarGz(archivePath string, srcs []string, res *CompressResult) error {
	f, err := os.Create(archivePath)
	if err != nil {
		return opErr("压缩", archivePath, err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for _, src := range srcs {
		if err := tarWalk(src, src, tw, res); err != nil {
			return err
		}
	}
	return nil
}

func tarWalk(root, src string, tw *tar.Writer, res *CompressResult) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return opErr("压缩", src, err)
	}
	name, _ := filepath.Rel(filepath.Dir(root), src)
	name = filepath.ToSlash(name)
	mode := int64(fi.Mode().Perm())
	switch {
	case fi.IsDir():
		hdr := &tar.Header{Name: name + "/", Mode: mode, Typeflag: tar.TypeDir, ModTime: fi.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			return opErr("压缩", src, err)
		}
		des, err := os.ReadDir(src)
		if err != nil {
			return opErr("压缩", src, err)
		}
		for _, de := range des {
			if err := tarWalk(root, filepath.Join(src, de.Name()), tw, res); err != nil {
				return err
			}
		}
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return opErr("压缩", src, err)
		}
		hdr := &tar.Header{Name: name, Mode: mode, Typeflag: tar.TypeSymlink, Linkname: target, ModTime: fi.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			return opErr("压缩", src, err)
		}
		res.Files++
	case fi.Mode().IsRegular():
		hdr := &tar.Header{Name: name, Mode: mode, Size: fi.Size(), ModTime: fi.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			return opErr("压缩", src, err)
		}
		in, err := os.Open(src)
		if err != nil {
			return opErr("压缩", src, err)
		}
		_, cpErr := io.Copy(tw, in)
		in.Close()
		if cpErr != nil {
			return opErr("压缩", src, cpErr)
		}
		res.Files++
	default:
		res.Skipped++
		res.SkippedWhy = "特殊文件(socket/设备/管道)未包含"
	}
	return nil
}

// compressZip 写 zip(符号链接以链接方式记录)
func compressZip(archivePath string, srcs []string, res *CompressResult) error {
	f, err := os.Create(archivePath)
	if err != nil {
		return opErr("压缩", archivePath, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	for _, src := range srcs {
		if err := zipWalk(src, src, zw, res); err != nil {
			return err
		}
	}
	return nil
}

func zipWalk(root, src string, zw *zip.Writer, res *CompressResult) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return opErr("压缩", src, err)
	}
	name, _ := filepath.Rel(filepath.Dir(root), src)
	name = filepath.ToSlash(name)
	mode := fi.Mode()
	switch {
	case fi.IsDir():
		hdr := &zip.FileHeader{Name: name + "/", Method: zip.Deflate, Modified: fi.ModTime()}
		hdr.SetMode(mode.Perm())
		if _, err := zw.CreateHeader(hdr); err != nil {
			return opErr("压缩", src, err)
		}
		des, err := os.ReadDir(src)
		if err != nil {
			return opErr("压缩", src, err)
		}
		for _, de := range des {
			if err := zipWalk(root, filepath.Join(src, de.Name()), zw, res); err != nil {
				return err
			}
		}
	case mode&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return opErr("压缩", src, err)
		}
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: fi.ModTime()}
		hdr.SetMode(mode.Perm() | os.ModeSymlink)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return opErr("压缩", src, err)
		}
		if _, err := w.Write([]byte(target)); err != nil {
			return opErr("压缩", src, err)
		}
		res.Files++
	case mode.IsRegular():
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: fi.ModTime()}
		hdr.SetMode(mode.Perm())
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return opErr("压缩", src, err)
		}
		in, err := os.Open(src)
		if err != nil {
			return opErr("压缩", src, err)
		}
		_, cpErr := io.Copy(w, in)
		in.Close()
		if cpErr != nil {
			return opErr("压缩", src, cpErr)
		}
		res.Files++
	default:
		res.Skipped++
		res.SkippedWhy = "特殊文件(socket/设备/管道)未包含"
	}
	return nil
}

// ExtractResult 解压结果
type ExtractResult struct {
	Dest     string `json:"dest"`
	Files    int    `json:"files"`
	Skipped  int    `json:"skipped"`
	Warning  string `json:"warning,omitempty"`
}

// Extract 解压 tar.gz/tar/zip 到目标目录(防 zip-slip;越界符号链接跳过)
func Extract(archivePath, dest string) (*ExtractResult, error) {
	archC, err := CleanPath(archivePath)
	if err != nil {
		return nil, err
	}
	destC, err := CleanPath(dest)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(destC, 0o755); err != nil {
		return nil, opErr("解压", destC, err)
	}
	lower := strings.ToLower(archC)
	res := &ExtractResult{Dest: destC}
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		f, err := os.Open(archC)
		if err != nil {
			return nil, opErr("解压", archC, err)
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, opErr("解压", archC, err)
		}
		defer gz.Close()
		if err := extractTar(tar.NewReader(gz), destC, res); err != nil {
			return nil, err
		}
	case strings.HasSuffix(lower, ".tar"):
		f, err := os.Open(archC)
		if err != nil {
			return nil, opErr("解压", archC, err)
		}
		defer f.Close()
		if err := extractTar(tar.NewReader(f), destC, res); err != nil {
			return nil, err
		}
	case strings.HasSuffix(lower, ".zip"):
		zr, err := zip.OpenReader(archC)
		if err != nil {
			return nil, opErr("解压", archC, err)
		}
		defer zr.Close()
		if err := extractZip(&zr.Reader, destC, res); err != nil {
			return nil, err
		}
	default:
		return nil, errs.Newf(errs.UNSUPPORTED_ARCH, "不支持的压缩格式: %s(仅支持 tar.gz/tar/zip)", filepath.Base(archC))
	}
	return res, nil
}

// extractTar 逐条目解压 tar
func extractTar(tr *tar.Reader, dest string, res *ExtractResult) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return opErr("解压", dest, err)
		}
		isSym := hdr.Typeflag == tar.TypeSymlink
		if err := extractEntry(dest, hdr.Name, isSym, hdr.Linkname, os.FileMode(hdr.Mode).Perm(), func(w io.Writer) error {
			_, err := io.Copy(w, tr)
			return err
		}, res); err != nil {
			return err
		}
	}
}

// extractZip 逐条目解压 zip
func extractZip(zr *zip.Reader, dest string, res *ExtractResult) error {
	for _, f := range zr.File {
		mode := f.Mode()
		isSym := mode&os.ModeSymlink != 0
		var linkTarget string
		if isSym {
			rc, err := f.Open()
			if err != nil {
				return opErr("解压", f.Name, err)
			}
			b, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return opErr("解压", f.Name, err)
			}
			linkTarget = string(b)
		}
		if err := extractEntry(dest, f.Name, isSym, linkTarget, mode.Perm(), func(w io.Writer) error {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			_, err = io.Copy(w, rc)
			return err
		}, res); err != nil {
			return err
		}
	}
	return nil
}

// extractEntry 单个条目:目录/文件/符号链接;越界或特殊文件跳过并计数
func extractEntry(dest, name string, isSym bool, linkTarget string, perm os.FileMode, write func(io.Writer) error, res *ExtractResult) error {
	target, err := SafeJoinInDest(dest, name)
	if err != nil {
		res.Skipped++
		return nil // 恶意条目直接跳过,不中断整个解压
	}
	if isSym {
		// 符号链接:目标解析后必须仍位于 dest 内,否则跳过
		linked := linkTarget
		if !filepath.IsAbs(linked) {
			linked = filepath.Join(filepath.Dir(target), linked)
		}
		linkedC := filepath.Clean(linked)
		if linkedC == dest || strings.HasPrefix(linkedC, dest+string(filepath.Separator)) {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
				if err := os.Symlink(linkTarget, target); err == nil {
					res.Files++
					return nil
				}
			}
		}
		res.Skipped++
		return nil
	}
	if strings.HasSuffix(name, "/") {
		if err := os.MkdirAll(target, 0o755); err != nil {
			return opErr("解压", target, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return opErr("解压", target, err)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return opErr("解压", target, err)
	}
	if err := write(out); err != nil {
		out.Close()
		return opErr("解压", target, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	res.Files++
	return nil
}

// SearchResult 搜索结果
type SearchResult struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// Search 递归搜索(大小写不敏感子串匹配)。
// limit 结果数上限;depth 目录深度上限;特殊文件系统(/proc /sys /dev /run)自动跳过;
// 不跟随符号链接目录;无权限目录跳过。
func Search(root, query string, limit, depth int) ([]SearchResult, error) {
	base, err := CleanPath(root)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return nil, errs.New(errs.INVALID_REQUEST, "搜索关键词不能为空")
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if depth <= 0 || depth > 20 {
		depth = 6
	}
	q := strings.ToLower(query)
	results := make([]SearchResult, 0, 64)
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir // 无权限目录跳过
			}
			return nil
		}
		if path != base {
			rel, _ := filepath.Rel(base, path)
			seg := strings.SplitN(rel, string(filepath.Separator), 2)[0]
			if path == filepath.Join(base, seg) && d.IsDir() {
				for _, sp := range SpecialFS {
					if seg == sp {
						return fs.SkipDir // 特殊文件系统不递归
					}
				}
			}
		}
		if len(results) >= limit {
			return fs.SkipAll
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(base, path)
			if rel != "." && strings.Count(rel, string(filepath.Separator)) >= depth {
				return fs.SkipDir
			}
		}
		if strings.Contains(strings.ToLower(d.Name()), q) {
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			results = append(results, SearchResult{
				Path: path,
				Name: d.Name(),
				Type: typeOf(fi.Mode()),
				Size: fi.Size(),
			})
		}
		return nil
	})
	return results, nil
}

// opErr 包装 os 错误为业务错误(权限/不存在映射到对应错误码)
func opErr(action, path string, err error) error {
	if os.IsNotExist(err) {
		return errs.Newf(errs.FILE_NOT_FOUND, "%s失败: 不存在 %s", action, path)
	}
	if os.IsPermission(err) {
		return errs.Newf(errs.PERMISSION_DENIED, "%s失败: 权限不足 %s", action, path)
	}
	if os.IsExist(err) {
		return errs.Newf(errs.FILE_EXISTS, "%s失败: 已存在 %s", action, path)
	}
	return errs.Newf(errs.INTERNAL, "%s失败: %s: %v", action, path, err)
}

// isCrossDevice 判断 rename 是否因跨文件系统失败
func isCrossDevice(err *os.LinkError) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "cross-device") || strings.Contains(msg, "EXDEV") || strings.Contains(msg, "invalid cross-device link")
}

// volumeRoot 当前盘根(仅 Windows 本地测试:目录树/首页从 "/" 进入)
func volumeRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return string(os.PathSeparator)
	}
	return filepath.VolumeName(wd) + string(os.PathSeparator)
}
