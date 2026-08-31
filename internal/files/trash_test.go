package files

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 回收站全流程:删除→进回收站→列表→恢复→再次删除→清空
func TestTrashFlow(t *testing.T) {
	home := t.TempDir()
	dataDir := t.TempDir()
	base := t.TempDir()
	trash := NewTrash(home, dataDir)

	// 准备文件 + 目录
	if err := os.MkdirAll(filepath.Join(base, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 默认回收站开启
	if !trash.Enabled() {
		t.Fatal("回收站默认应开启")
	}

	// 删除 → 应移入回收站
	var buf strings.Builder
	if err := fsopRun("remove", []byte(`{"paths":["`+filepath.ToSlash(base)+`/a.txt","`+filepath.ToSlash(base)+`/sub"],"recursive":true}`), strings.NewReader(""), &buf, trash); err != nil {
		t.Fatalf("remove(回收站): %v", err)
	}
	if !strings.Contains(buf.String(), `"trashed":true`) {
		t.Errorf("应返回 trashed=true: %s", buf.String())
	}
	if _, err := os.Stat(filepath.Join(base, "a.txt")); !os.IsNotExist(err) {
		t.Error("源文件应已消失")
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "Trash", "files", "a.txt")); err != nil {
		t.Error("a.txt 应位于回收站")
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "Trash", "files", "sub")); err != nil {
		t.Error("sub 目录应位于回收站")
	}

	// 列表
	items, err := trash.List()
	if err != nil {
		t.Fatalf("trash list: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("回收站应有 2 条,got %d", len(items))
	}
	found := false
	for _, it := range items {
		if it.Name == "a.txt" && it.SourcePath == filepath.Join(base, "a.txt") {
			found = true
		}
	}
	if !found {
		t.Errorf("a.txt 条目应带原路径: %+v", items)
	}

	// 恢复 a.txt
	if err := fsopRun("trash_restore", []byte(`{"names":["a.txt"]}`), strings.NewReader(""), &buf, trash); err != nil {
		t.Fatalf("trash_restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "a.txt")); err != nil {
		t.Error("a.txt 应恢复回原路径")
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "Trash", "files", "a.txt")); !os.IsNotExist(err) {
		t.Error("回收站里的 a.txt 应已移除")
	}

	// 彻底删除 sub
	if err := fsopRun("trash_delete", []byte(`{"names":["sub"]}`), strings.NewReader(""), &buf, trash); err != nil {
		t.Fatalf("trash_delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "Trash", "files", "sub")); !os.IsNotExist(err) {
		t.Error("sub 应已彻底删除")
	}

	// 清空
	if err := fsopRun("trash_empty", []byte(`{}`), strings.NewReader(""), &buf, trash); err != nil {
		t.Fatalf("trash_empty: %v", err)
	}
	items, _ = trash.List()
	if len(items) != 0 {
		t.Errorf("清空后回收站应为空,got %d", len(items))
	}

	// 关闭回收站后删除 = 永久删除
	if err := fsopRun("trash_enable", []byte(`{"enabled":false}`), strings.NewReader(""), &buf, trash); err != nil {
		t.Fatalf("trash_enable: %v", err)
	}
	if trash.Enabled() {
		t.Error("回收站应已关闭")
	}
	if err := os.WriteFile(filepath.Join(base, "c.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsopRun("remove", []byte(`{"paths":["`+filepath.ToSlash(base)+`/c.txt"],"recursive":false}`), strings.NewReader(""), &buf, trash); err != nil {
		t.Fatalf("remove(关闭回收站): %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "Trash", "files", "c.txt")); !os.IsNotExist(err) {
		t.Error("关闭回收站时删除不应进回收站")
	}
}

// 危险目录禁止移入回收站
func TestTrashDangerousDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("危险路径语义为 Linux 宿主")
	}
	trash := NewTrash(t.TempDir(), t.TempDir())
	var buf strings.Builder
	err := fsopRun("remove", []byte(`{"paths":["/etc"],"recursive":true}`), strings.NewReader(""), &buf, trash)
	if err == nil {
		t.Fatal("删除 /etc 必须被拒绝(即使走回收站)")
	}
}
