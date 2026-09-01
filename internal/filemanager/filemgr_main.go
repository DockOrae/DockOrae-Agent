// filemgr 子命令入口(经 nsenter 重新执行自身时调用;Direct 模式由 Service 进程内调用)。
// 用法: dockorae-agent filemgr <op> <jsonArgs>
// 输出: 正常 {"ok":true,"data":...};失败 {"ok":false,"error":{"code","message"}} + 退出码 1。
// 自 DockOrae internal/agent/files.go 移植(2026-09-02):文件操作逻辑与 DockOrae 完全一致,
// 仅把"进程内常驻 Manager"改为"每次操作重执行构造 Manager"以适配容器 nsenter 架构。
package filemanager

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path"
	"strings"

	"github.com/DockOrae/DockOrae-Agent/internal/contract"
	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// DefaultConfig 默认管理器配置(DockOrae 同款保护目录,替换为 DockOrae 自身目录)
func DefaultConfig() Config {
	stateDirectory := os.Getenv("AGENT_DATA_DIR")
	trashDirectory := "/var/lib/dockorae-agent/file-trash"
	protectedDirectories := []string{
		"/var/lib/dockorae-agent",
		"/run/dockorae",
		"/opt/docker-manager",
		"/etc/dockorae",
		"/root/.local/share/Trash",
	}
	if stateDirectory = strings.TrimSpace(stateDirectory); stateDirectory != "" && path.IsAbs(stateDirectory) {
		stateDirectory = path.Clean(stateDirectory)
		trashDirectory = path.Join(stateDirectory, "file-trash")
		protectedDirectories = append(protectedDirectories, stateDirectory)
	}
	return Config{
		Root: "/", TrashVirtual: trashDirectory,
		ProtectedVirtual: protectedDirectories,
		ReadOnlyVirtual:  []string{"/proc", "/sys", "/dev"},
	}
}

func noCtx() context.Context { return context.Background() }

// FilemgrMain filemgr 子命令入口。op 与 DockOrae 端点一一对应:
//
//	list / entry / entries / trash / action / write_text / read(流式 stdout)
func FilemgrMain(args []string) int {
	if len(args) < 1 {
		writeFilemgrErr(os.Stdout, errs.New(errs.INVALID_REQUEST, "filemgr: 缺少操作"))
		return 1
	}
	op := args[0]
	var argJSON []byte
	if len(args) >= 2 {
		argJSON = []byte(args[1])
	}
	manager, err := New(DefaultConfig())
	if err != nil {
		writeFilemgrErr(os.Stdout, err)
		return 1
	}
	defer manager.Close()
	if err := filemgrRun(op, argJSON, os.Stdin, os.Stdout, manager); err != nil {
		writeFilemgrErr(os.Stdout, err)
		return 1
	}
	return 0
}

// filemgrRun 执行单个文件操作;stdout 收到 {ok,data} 信封(read 之外)。
func filemgrRun(op string, argJSON []byte, stdin io.Reader, stdout io.Writer, manager *Manager) error {
	dec := func(v any) error {
		if len(argJSON) == 0 {
			return nil
		}
		return json.Unmarshal(argJSON, v)
	}
	ok := func(data any) error {
		b, err := json.Marshal(map[string]any{"ok": true, "data": data})
		if err != nil {
			return err
		}
		_, err = stdout.Write(b)
		return err
	}
	switch op {
	case "list":
		var a struct {
			Path   string `json:"path"`
			Limit  int    `json:"limit"`
			Offset int    `json:"offset"`
			Search string `json:"search"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "filemgr list: 参数无效")
		}
		res, err := manager.ListPage(noCtx(), a.Path, ListOptions{Limit: a.Limit, Offset: a.Offset, Search: a.Search})
		if err != nil {
			return err
		}
		return ok(res)
	case "entry":
		var a struct {
			Path string `json:"path"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "filemgr entry: 参数无效")
		}
		entry, err := manager.Stat(a.Path)
		if err != nil {
			return err
		}
		return ok(entry)
	case "entries":
		var a struct {
			Paths []string `json:"paths"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "filemgr entries: 参数无效")
		}
		result := struct {
			Entries     []contract.FileEntry `json:"entries"`
			Unavailable []string             `json:"unavailable"`
		}{Entries: make([]contract.FileEntry, 0, len(a.Paths)), Unavailable: make([]string, 0)}
		for _, p := range a.Paths {
			entry, err := manager.Stat(p)
			if err != nil {
				result.Unavailable = append(result.Unavailable, p)
				continue
			}
			result.Entries = append(result.Entries, entry)
		}
		return ok(result)
	case "trash":
		res, err := manager.ListTrash(noCtx())
		if err != nil {
			return err
		}
		return ok(res)
	case "action":
		var a contract.FileActionRequest
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "filemgr action: 参数无效")
		}
		result, err := manager.Action(noCtx(), a)
		if err != nil {
			return err
		}
		return ok(result)
	case "write_text":
		var a struct {
			Path                    string `json:"path"`
			Content                 string `json:"content"`
			ExpectedResourceVersion string `json:"expectedResourceVersion"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "filemgr write_text: 参数无效")
		}
		if strings.TrimSpace(a.Path) == "" {
			return errs.New(errs.INVALID_REQUEST, "filemgr write_text: 缺少 path")
		}
		entry, err := manager.WriteText(noCtx(), a.Path, contract.FileWriteRequest{
			Content: a.Content, ExpectedResourceVersion: a.ExpectedResourceVersion,
		})
		if err != nil {
			return err
		}
		return ok(entry)
	default:
		return errs.Newf(errs.INVALID_REQUEST, "filemgr: 未知操作 %s", op)
	}
}

// filemgrStream 流式操作:read 输出原始字节;upload 从 stdin 读入。返回信封数据(write 之外)。
func filemgrStream(op string, argJSON []byte, stdout io.Writer, manager *Manager) error {
	dec := func(v any) error {
		if len(argJSON) == 0 {
			return nil
		}
		return json.Unmarshal(argJSON, v)
	}
	switch op {
	case "read":
		var a struct {
			Path  string `json:"path"`
			Limit int64  `json:"limit"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "filemgr read: 参数无效")
		}
		file, _, err := manager.Open(noCtx(), a.Path)
		if err != nil {
			return err
		}
		defer file.Close()
		if a.Limit > 0 {
			_, err = io.Copy(stdout, io.LimitReader(file, a.Limit))
			return err
		}
		_, err = io.Copy(stdout, file)
		return err
	case "read_range":
		var a struct {
			Path   string `json:"path"`
			Start  int64  `json:"start"`
			Length int64  `json:"length"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "filemgr read_range: 参数无效")
		}
		file, _, err := manager.Open(noCtx(), a.Path)
		if err != nil {
			return err
		}
		defer file.Close()
		if a.Start > 0 {
			if _, err := file.Seek(a.Start, io.SeekStart); err != nil {
				return err
			}
		}
		_, err = io.Copy(stdout, io.LimitReader(file, a.Length))
		return err
	case "upload":
		var a struct {
			Path      string `json:"path"`
			Name      string `json:"name"`
			Overwrite bool   `json:"overwrite"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "filemgr upload: 参数无效")
		}
		entry, err := manager.Upload(noCtx(), a.Path, a.Name, os.Stdin, -1, a.Overwrite)
		if err != nil {
			return err
		}
		b, err := json.Marshal(map[string]any{"ok": true, "data": entry})
		if err != nil {
			return err
		}
		_, err = stdout.Write(b)
		return err
	case "export_zip":
		var a struct {
			Sources                  []string          `json:"sources"`
			ExpectedResourceVersions map[string]string `json:"expectedResourceVersions"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "filemgr export_zip: 参数无效")
		}
		return manager.ExportZIP(noCtx(), a.Sources, a.ExpectedResourceVersions, stdout)
	default:
		return errs.Newf(errs.INVALID_REQUEST, "filemgr stream: 未知操作 %s", op)
	}
}

// writeFilemgrErr 输出错误信封
func writeFilemgrErr(w io.Writer, err error) {
	ae := errs.FromError(err)
	b, _ := json.Marshal(map[string]any{
		"ok": false,
		"error": map[string]string{
			"code":    ae.Code,
			"message": ae.Message,
		},
	})
	_, _ = w.Write(b)
}
