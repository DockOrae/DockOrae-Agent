package files

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// ---------- 路径安全 ----------

func TestCleanPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 路径语义测试仅 Unix(生产目标为 Linux 宿主)")
	}
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"/etc", "/etc", true},
		{"/etc/../root", "/root", true}, // 规范化后即为实际目标(确定性,无歧义)
		{"/etc/nginx/", "/etc/nginx", true},
		{"/proc", "/proc", true},
		{"", "", false},
		{"etc", "", false},          // 必须绝对路径
		{"../etc", "", false},       // 非绝对
		{"/etc\x00x", "", false},    // NUL
		{"/../../etc", "/etc", true}, // clean 后仍为绝对
	}
	for _, c := range cases {
		got, err := CleanPath(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("CleanPath(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("CleanPath(%q) 应报错,got %q", c.in, got)
		}
	}
}

func TestJoinEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 路径语义测试仅 Unix")
	}
	if _, err := JoinEntry("/etc", ".."); err == nil {
		t.Error("JoinEntry 应拒绝 ..")
	}
	if _, err := JoinEntry("/etc", "."); err == nil {
		t.Error("JoinEntry 应拒绝 .")
	}
	if _, err := JoinEntry("/etc", "a/b"); err == nil {
		t.Error("JoinEntry 应拒绝含 / 的名称")
	}
	if _, err := JoinEntry("/etc", "a\\b"); err == nil {
		t.Error("JoinEntry 应拒绝含 \\ 的名称")
	}
	if _, err := JoinEntry("/etc", ""); err == nil {
		t.Error("JoinEntry 应拒绝空名称")
	}
	p, err := JoinEntry("/etc", "nginx.conf")
	if err != nil || p != filepath.Join("/etc", "nginx.conf") {
		t.Errorf("JoinEntry 正常路径失败: %q %v", p, err)
	}
}

func TestCheckDangerousRemove(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("危险目录列表为 Linux 语义")
	}
	for _, d := range DangerousDirs {
		if err := CheckDangerousRemove(d); err == nil {
			t.Errorf("危险目录 %q 应被拒绝删除", d)
		}
	}
	// 目录内文件不受限
	for _, ok := range []string{"/etc/nginx.conf", "/var/log/x.log", "/root/.bashrc", "/usr/local/bin/x"} {
		if err := CheckDangerousRemove(ok); err != nil {
			t.Errorf("非危险路径 %q 不应被拦截: %v", ok, err)
		}
	}
}

func TestSafeJoinInDest(t *testing.T) {
	base := t.TempDir()
	if _, err := SafeJoinInDest(base, "../evil"); err == nil {
		t.Error("越界条目应被拒绝")
	}
	if _, err := SafeJoinInDest(base, "/etc/passwd"); err == nil {
		t.Error("绝对路径条目应被拒绝")
	}
	if _, err := SafeJoinInDest(base, "a/../../evil"); err == nil {
		t.Error("多层越界应被拒绝")
	}
	p, err := SafeJoinInDest(base, "sub/file.txt")
	if err != nil || !strings.HasPrefix(p, base) {
		t.Errorf("正常条目失败: %q %v", p, err)
	}
}

// ---------- 文件操作(进程内 fsop,Direct 语义) ----------

func runOp(t *testing.T, op string, args string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	trash := NewTrash(filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "data"))
	err := fsopRun(op, []byte(args), strings.NewReader(""), &buf, trash)
	return buf.String(), err
}

// jsPath Windows 临时目录为反斜杠,JSON 参数需转成 /(避免 JSON 转义错误)
func jsPath(p string) string { return strings.ReplaceAll(p, "\\", "/") }

func TestFilesCRUD(t *testing.T) {
	base := jsPath(t.TempDir())

	// mkdir + touch
	if out, err := runOp(t, "mkdir", `{"path":"`+base+`/sub"}`); err != nil {
		t.Fatalf("mkdir: %v %s", err, out)
	}
	if out, err := runOp(t, "touch", `{"path":"`+base+`/a.txt"}`); err != nil {
		t.Fatalf("touch: %v %s", err, out)
	}
	// touch 已存在 → FILE_EXISTS
	if _, err := runOp(t, "touch", `{"path":"`+base+`/a.txt"}`); err == nil {
		t.Error("touch 已存在文件应报 FILE_EXISTS")
	} else if ae := errs.FromError(err); ae.Code != errs.FILE_EXISTS {
		t.Errorf("touch 重复应 FILE_EXISTS,got %s", ae.Code)
	}

	// write(编辑器保存)
	if err := fsopRun("write", []byte(`{"path":"`+base+`/a.txt"}`), strings.NewReader("hello world"), &bytes.Buffer{}, NewTrash(t.TempDir(), t.TempDir())); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(base, "a.txt"))
	if err != nil || string(b) != "hello world" {
		t.Errorf("write 内容不符: %q %v", b, err)
	}

	// list
	out, err := runOp(t, "list", `{"path":"`+base+`"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "sub") {
		t.Errorf("list 缺少条目: %s", out)
	}

	// stat
	out, err = runOp(t, "stat", `{"path":"`+base+`/a.txt"}`)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !strings.Contains(out, `"type":"file"`) {
		t.Errorf("stat 类型不符: %s", out)
	}

	// rename
	if _, err := runOp(t, "rename", `{"old_path":"`+base+`/a.txt","new_path":"`+base+`/b.txt"}`); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "b.txt")); err != nil {
		t.Error("rename 后 b.txt 不存在")
	}

	// copy
	if _, err := runOp(t, "copy", `{"src":"`+base+`/b.txt","dst":"`+base+`/c.txt"}`); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "c.txt")); err != nil {
		t.Error("copy 后 c.txt 不存在")
	}

	// move(同设备 rename)
	if _, err := runOp(t, "move", `{"src":"`+base+`/c.txt","dst":"`+base+`/sub/d.txt"}`); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "sub", "d.txt")); err != nil {
		t.Error("move 后 sub/d.txt 不存在")
	}

	// chmod(Unix 权限语义;Windows 只模拟只读位,跳过)
	if runtime.GOOS != "windows" {
		if _, err := runOp(t, "chmod", `{"path":"`+base+`/b.txt","mode":420}`); err != nil { // 0644
			t.Fatalf("chmod: %v", err)
		}
		fi, _ := os.Stat(filepath.Join(base, "b.txt"))
		if fi.Mode().Perm() != 0o644 {
			t.Errorf("chmod 未生效: %v", fi.Mode().Perm())
		}
	}

	// 删除:目录不递归 → 报错
	if _, err := runOp(t, "remove", `{"paths":["`+base+`/sub"],"recursive":false,"force":true}`); err == nil {
		t.Error("非空目录删除应要求 recursive")
	}
	// 删除:文件 + 递归目录(force 永久删除,不依赖回收站)
	if _, err := runOp(t, "remove", `{"paths":["`+base+`/b.txt","`+base+`/sub"],"recursive":true,"force":true}`); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "sub")); !os.IsNotExist(err) {
		t.Error("sub 应已删除")
	}
}

func TestRemoveDangerousRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("危险路径语义为 Linux 宿主,Windows 跳过")
	}
	if _, err := runOp(t, "remove", `{"paths":["/"],"recursive":true}`); err == nil {
		t.Fatal("删除 / 必须被拒绝")
	}
	if _, err := runOp(t, "remove", `{"paths":["/etc"],"recursive":true}`); err == nil {
		t.Fatal("删除 /etc 必须被拒绝")
	}
}

func TestFilesCompressExtract(t *testing.T) {
	base := jsPath(t.TempDir())
	dir := jsPath(filepath.Join(t.TempDir(), "data"))
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f1.txt"), []byte("content1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "f2.txt"), []byte("content2"), 0o644); err != nil {
		t.Fatal(err)
	}

	// tar.gz 压缩
	if _, err := runOp(t, "compress", `{"dir":"`+dir+`","archive":"backup.tar.gz","format":"tar.gz","names":["f1.txt","sub"]}`); err != nil {
		t.Fatalf("compress tar.gz: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "backup.tar.gz")); err != nil {
		t.Fatal("压缩产物不存在")
	}

	// 解压到新目录
	outDir := jsPath(filepath.Join(base, "out"))
	if _, err := runOp(t, "extract", `{"archive":"`+dir+`/backup.tar.gz","dest":"`+outDir + `"}`); err != nil {
		t.Fatalf("extract: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(outDir, "f1.txt"))
	if err != nil || string(b) != "content1" {
		t.Errorf("解压 f1 不符: %q %v", b, err)
	}
	b, err = os.ReadFile(filepath.Join(outDir, "sub", "f2.txt"))
	if err != nil || string(b) != "content2" {
		t.Errorf("解压 f2 不符: %q %v", b, err)
	}

	// zip 压缩 + 解压
	if _, err := runOp(t, "compress", `{"dir":"`+dir+`","archive":"backup.zip","format":"zip","names":["f1.txt"]}`); err != nil {
		t.Fatalf("compress zip: %v", err)
	}
	zipOut := jsPath(filepath.Join(base, "zipout"))
	if _, err := runOp(t, "extract", `{"archive":"`+dir+`/backup.zip","dest":"`+zipOut + `"}`); err != nil {
		t.Fatalf("extract zip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(zipOut, "f1.txt")); err != nil {
		t.Error("zip 解压产物缺失")
	}

	// 不支持格式
	if _, err := runOp(t, "extract", `{"archive":"`+dir+`/x.rar","dest":"`+outDir + `"}`); err == nil {
		t.Error("rar 应报 UNSUPPORTED_ARCH")
	}
}

func TestExtractZipSlip(t *testing.T) {
	// 构造恶意 zip(条目 ../evil.txt)
	base := jsPath(t.TempDir())
	evilZip := jsPath(filepath.Join(t.TempDir(), "evil.zip"))
	zw, err := os.Create(evilZip)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(zw)
	w, err := archive.Create("../evil.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("pwned")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	zw.Close()

	outDir := jsPath(filepath.Join(base, "out"))
	if _, err := runOp(t, "extract", `{"archive":"`+evilZip+`","dest":"`+outDir + `"}`); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "evil.txt")); !os.IsNotExist(err) {
		t.Error("zip-slip 越界文件不应被写出")
	}
}

func TestSearch(t *testing.T) {
	base := t.TempDir()
	_ = os.MkdirAll(filepath.Join(base, "logs"), 0o755)
	_ = os.WriteFile(filepath.Join(base, "app.log"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(base, "logs", "nginx-error.log"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(base, "readme.md"), []byte("x"), 0o644)

	results, err := Search(base, "log", 100, 6)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	// app.log + logs 目录 + logs/nginx-error.log = 3 条
	if len(results) != 3 {
		t.Errorf("应命中 3 条,got %d: %+v", len(results), results)
	}
}

// ---------- 符号链接(仅 Unix) ----------

func TestSymlinkListAndNoFollow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("符号链接测试仅 Unix")
	}
	base := t.TempDir()
	_ = os.WriteFile(filepath.Join(base, "target.txt"), []byte("t"), 0o644)
	if err := os.Symlink("target.txt", filepath.Join(base, "link.txt")); err != nil {
		t.Skipf("无符号链接权限: %v", err)
	}
	out, err := runOp(t, "list", `{"path":"`+base+`"}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, `"type":"symlink"`) || !strings.Contains(out, `"target":"target.txt"`) {
		t.Errorf("符号链接类型/目标缺失: %s", out)
	}
	// 删除链接只删链接本身,不删目标
	if _, err := runOp(t, "remove", `{"paths":["`+base+`/link.txt"],"recursive":false,"force":true}`); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "target.txt")); err != nil {
		t.Error("删除链接不应影响目标文件")
	}
}
