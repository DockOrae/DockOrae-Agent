package files

import (
	"encoding/json"
	"io"
	"os"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// FsopMain fsop 子命令入口(经 nsenter 重新执行自身时调用;Direct 模式由 Service 进程内调用)。
// 用法: dockorae-agent fsop <op> <jsonArgs>
// 输出: 正常 {"ok":true,"data":...};失败 {"ok":false,"error":{"code","message"}} + 退出码 1。
// write 操作的数据经 stdin 传入(避免 JSON 转义大文件)。
func FsopMain(args []string) int {
	if len(args) < 1 {
		writeErr(os.Stdout, errs.New(errs.INVALID_REQUEST, "fsop: 缺少操作"))
		return 1
	}
	op := args[0]
	var argJSON []byte
	if len(args) >= 2 {
		argJSON = []byte(args[1])
	}
	// 回收站状态经 env 继承(AGENT_DATA_DIR);home 在宿主命名空间内解析
	home, _ := os.UserHomeDir()
	trash := NewTrash(home, os.Getenv("AGENT_DATA_DIR"))
	if err := fsopRun(op, argJSON, os.Stdin, os.Stdout, trash); err != nil {
		writeErr(os.Stdout, err)
		return 1
	}
	return 0
}

// fsopRun 执行单个文件操作;stdout 收到 {ok,data} 信封(write 之外)。
func fsopRun(op string, argJSON []byte, stdin io.Reader, stdout io.Writer, trash *TrashService) error {
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
	// write 特殊:数据走 stdin(原始字节)
	if op == "write" {
		var a struct {
			Path string `json:"path"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop write: 参数无效")
		}
		data, err := io.ReadAll(io.LimitReader(stdin, 8<<20+1))
		if err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop write: 读取数据失败")
		}
		if len(data) > 8<<20 {
			return errs.Newf(errs.FILE_TOO_LARGE, "文件超过 8MB 上限,请用上传")
		}
		if err := WriteJSON(a.Path, data); err != nil {
			return err
		}
		return ok(map[string]any{})
	}

	switch op {
	case "list":
		var a struct {
			Path       string `json:"path"`
			ShowHidden bool   `json:"show_hidden"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop list: 参数无效")
		}
		res, err := List(a.Path, a.ShowHidden)
		if err != nil {
			return err
		}
		return ok(res)
	case "stat":
		var a struct{ Path string `json:"path"` }
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop stat: 参数无效")
		}
		e, err := Stat(a.Path)
		if err != nil {
			return err
		}
		return ok(e)
	case "dirsize":
		var a struct{ Path string `json:"path"` }
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop dirsize: 参数无效")
		}
		size, err := DirSize(a.Path)
		if err != nil {
			return err
		}
		return ok(map[string]any{"size": size})
	case "chown":
		var a struct {
			Path  string `json:"path"`
			Owner string `json:"owner"`
			Group string `json:"group"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop chown: 参数无效")
		}
		if err := Chown(a.Path, a.Owner, a.Group); err != nil {
			return err
		}
		return ok(map[string]any{})
	case "touch":
		var a struct{ Path string `json:"path"` }
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop touch: 参数无效")
		}
		if err := Touch(a.Path); err != nil {
			return err
		}
		return ok(map[string]any{})
	case "mkdir":
		var a struct{ Path string `json:"path"` }
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop mkdir: 参数无效")
		}
		if err := Mkdir(a.Path); err != nil {
			return err
		}
		return ok(map[string]any{})
	case "rename":
		var a struct {
			OldPath string `json:"old_path"`
			NewPath string `json:"new_path"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop rename: 参数无效")
		}
		if err := Rename(a.OldPath, a.NewPath); err != nil {
			return err
		}
		return ok(map[string]any{})
	case "move":
		var a struct {
			Src string `json:"src"`
			Dst string `json:"dst"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop move: 参数无效")
		}
		if err := Move(a.Src, a.Dst); err != nil {
			return err
		}
		return ok(map[string]any{})
	case "remove":
		var a struct {
			Paths     []string `json:"paths"`
			Recursive bool     `json:"recursive"`
			Force     bool     `json:"force"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop remove: 参数无效")
		}
		// 回收站开启且非强制删除 → 移入回收站(危险目录保护对两者均生效)
		if !a.Force && trash.Enabled() {
			if err := trash.MoveToTrash(a.Paths); err != nil {
				return err
			}
			return ok(map[string]any{"trashed": true})
		}
		if err := Remove(a.Paths, a.Recursive); err != nil {
			return err
		}
		return ok(map[string]any{"trashed": false})
	case "copy":
		var a struct {
			Src string `json:"src"`
			Dst string `json:"dst"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop copy: 参数无效")
		}
		if err := Copy(a.Src, a.Dst); err != nil {
			return err
		}
		return ok(map[string]any{})
	case "chmod":
		var a struct {
			Path string `json:"path"`
			Mode uint32 `json:"mode"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop chmod: 参数无效")
		}
		if err := Chmod(a.Path, a.Mode); err != nil {
			return err
		}
		return ok(map[string]any{})
	case "compress":
		var a struct {
			Dir     string   `json:"dir"`
			Archive string   `json:"archive"`
			Format  string   `json:"format"`
			Names   []string `json:"names"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop compress: 参数无效")
		}
		res, err := Compress(a.Dir, a.Archive, a.Format, a.Names)
		if err != nil {
			return err
		}
		return ok(res)
	case "extract":
		var a struct {
			Archive string `json:"archive"`
			Dest    string `json:"dest"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop extract: 参数无效")
		}
		res, err := Extract(a.Archive, a.Dest)
		if err != nil {
			return err
		}
		return ok(res)
	case "search":
		var a struct {
			Path  string `json:"path"`
			Query string `json:"query"`
			Limit int    `json:"limit"`
			Depth int    `json:"depth"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop search: 参数无效")
		}
		results, err := Search(a.Path, a.Query, a.Limit, a.Depth)
		if err != nil {
			return err
		}
		return ok(map[string]any{
			"results":   results,
			"truncated": len(results) >= searchLimitOf(a.Limit),
		})
	case "trash_status":
		return ok(trash.Status())
	case "trash_enable":
		var a struct {
			Enabled bool `json:"enabled"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop trash_enable: 参数无效")
		}
		if err := trash.SetEnabled(a.Enabled); err != nil {
			return err
		}
		return ok(map[string]any{})
	case "trash_list":
		items, err := trash.List()
		if err != nil {
			return err
		}
		return ok(map[string]any{"items": items})
	case "trash_restore":
		var a struct {
			Names []string `json:"names"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop trash_restore: 参数无效")
		}
		if err := trash.Restore(a.Names); err != nil {
			return err
		}
		return ok(map[string]any{})
	case "trash_delete":
		var a struct {
			Names []string `json:"names"`
		}
		if err := dec(&a); err != nil {
			return errs.New(errs.INVALID_REQUEST, "fsop trash_delete: 参数无效")
		}
		if err := trash.Delete(a.Names); err != nil {
			return err
		}
		return ok(map[string]any{})
	case "trash_empty":
		if err := trash.Empty(); err != nil {
			return err
		}
		return ok(map[string]any{})
	default:
		return errs.Newf(errs.INVALID_REQUEST, "fsop: 未知操作 %s", op)
	}
}

func searchLimitOf(limit int) int {
	if limit <= 0 || limit > 1000 {
		return 200
	}
	return limit
}

// writeErr 输出错误信封(供子进程与进程内共用)
func writeErr(w io.Writer, err error) {
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
