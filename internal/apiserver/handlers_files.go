// 宿主文件管理端点(§55):列表/属性/新建/重命名/复制/移动/删除/权限/写入/上传/下载/压缩/解压/搜索。
// 全部经 files.Service 执行;删除为危险操作,必须 confirm=true + 目录必须 recursive=true。
package apiserver

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/DockOrae/DockOrae-Agent/internal/files"
)

// handleFilesList 目录列表(show_hidden 控制隐藏文件)
func (s *Server) handleFilesList(c *Ctx) error {
	res, err := s.Files.List(c.Query("path"), c.Query("show_hidden") == "true" || c.Query("show_hidden") == "1")
	if err != nil {
		return err
	}
	c.OK(map[string]any{"path": res.Path, "entries": res.Entries})
	return nil
}

// handleFilesDirSize 目录大小
func (s *Server) handleFilesDirSize(c *Ctx) error {
	size, err := s.Files.DirSize(c.Query("path"))
	if err != nil {
		return err
	}
	c.OK(map[string]any{"size": size})
	return nil
}

// handleFilesChown 修改所有者/用户组
func (s *Server) handleFilesChown(c *Ctx) error {
	var req struct {
		Path  string `json:"path"`
		Owner string `json:"owner"`
		Group string `json:"group"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Files.Chown(req.Path, req.Owner, req.Group)
	c.Audit("files.chown", req.Path, start, err, "", map[string]any{"owner": req.Owner, "group": req.Group})
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleFilesStat 单条目属性
func (s *Server) handleFilesStat(c *Ctx) error {
	e, err := s.Files.Stat(c.Query("path"))
	if err != nil {
		return err
	}
	c.OK(e)
	return nil
}

// handleFilesTouch 新建空文件
func (s *Server) handleFilesTouch(c *Ctx) error {
	var req struct {
		Path string `json:"path"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Files.Touch(req.Path)
	c.Audit("files.touch", req.Path, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleFilesMkdir 新建目录
func (s *Server) handleFilesMkdir(c *Ctx) error {
	var req struct {
		Path string `json:"path"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Files.Mkdir(req.Path)
	c.Audit("files.mkdir", req.Path, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleFilesRename 重命名
func (s *Server) handleFilesRename(c *Ctx) error {
	var req struct {
		OldPath string `json:"old_path"`
		NewPath string `json:"new_path"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Files.Rename(req.OldPath, req.NewPath)
	c.Audit("files.rename", req.OldPath+" → "+req.NewPath, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleFilesCopy 复制
func (s *Server) handleFilesCopy(c *Ctx) error {
	var req struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Files.Copy(req.Src, req.Dst)
	c.Audit("files.copy", req.Src+" → "+req.Dst, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleFilesMove 移动
func (s *Server) handleFilesMove(c *Ctx) error {
	var req struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Files.Move(req.Src, req.Dst)
	c.Audit("files.move", req.Src+" → "+req.Dst, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleFilesRemove 删除(危险:confirm=true + 目录 recursive=true;危险系统目录精确匹配禁止;
// 回收站开启且非 force → 移入回收站)
func (s *Server) handleFilesRemove(c *Ctx) error {
	var req struct {
		Paths     []string `json:"paths"`
		Recursive bool     `json:"recursive"`
		Force     bool     `json:"force"`
		Confirm   *bool    `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "删除文件"); err != nil {
		return err
	}
	start := now()
	trashed, err := s.Files.Remove(req.Paths, req.Recursive, req.Force)
	c.Audit("files.remove", strings.Join(req.Paths, ", "), start, err, "", map[string]any{"recursive": req.Recursive, "force": req.Force})
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true, "trashed": trashed})
	return nil
}

// handleTrashStatus 回收站状态(开关 + 目录)
func (s *Server) handleTrashStatus(c *Ctx) error {
	st, err := s.Files.TrashStatus()
	if err != nil {
		return err
	}
	c.OK(st)
	return nil
}
// handleTrashSetEnabled 回收站开关
func (s *Server) handleTrashSetEnabled(c *Ctx) error {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Files.TrashSetEnabled(req.Enabled)
	c.Audit("files.trash_enable", strconv.FormatBool(req.Enabled), start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleTrashList 回收站列表
func (s *Server) handleTrashList(c *Ctx) error {
	items, err := s.Files.TrashList()
	if err != nil {
		return err
	}
	c.OK(map[string]any{"items": items})
	return nil
}

// handleTrashRestore 恢复回收站条目
func (s *Server) handleTrashRestore(c *Ctx) error {
	var req struct {
		Names []string `json:"names"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Files.TrashRestore(req.Names)
	c.Audit("files.trash_restore", strings.Join(req.Names, ", "), start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleTrashDelete 彻底删除回收站条目
func (s *Server) handleTrashDelete(c *Ctx) error {
	var req struct {
		Names   []string `json:"names"`
		Confirm *bool    `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "彻底删除回收站条目"); err != nil {
		return err
	}
	start := now()
	err := s.Files.TrashDelete(req.Names)
	c.Audit("files.trash_delete", strings.Join(req.Names, ", "), start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleTrashEmpty 清空回收站
func (s *Server) handleTrashEmpty(c *Ctx) error {
	var req struct {
		Confirm *bool `json:"confirm"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Confirm(req.Confirm, "清空回收站"); err != nil {
		return err
	}
	start := now()
	err := s.Files.TrashEmpty()
	c.Audit("files.trash_empty", "all", start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleFilesChmod 修改权限
func (s *Server) handleFilesChmod(c *Ctx) error {
	var req struct {
		Path string `json:"path"`
		Mode uint32 `json:"mode"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Files.Chmod(req.Path, req.Mode)
	c.Audit("files.chmod", req.Path+" "+strconv.FormatUint(uint64(req.Mode), 8), start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleFilesWrite 覆盖写入(编辑器保存)
func (s *Server) handleFilesWrite(c *Ctx) error {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	err := s.Files.Write(req.Path, []byte(req.Content))
	c.Audit("files.write", req.Path, start, err, "", map[string]any{"bytes": len(req.Content)})
	if err != nil {
		return err
	}
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleFilesUpload 上传(原始 body 流式写入;dir + name 校验防穿越)
func (s *Server) handleFilesUpload(c *Ctx) error {
	dir := c.Query("dir")
	name := c.Query("name")
	path, err := files.JoinEntry(dir, name)
	if err != nil {
		return err
	}
	start := now()
	if err := s.Files.WriteStream(path, c.R.Body); err != nil {
		c.Audit("files.upload", path, start, err, "", nil)
		return err
	}
	c.Audit("files.upload", path, start, nil, "", nil)
	c.OK(map[string]any{"ok": true})
	return nil
}

// handleFilesDownload 下载(原始字节流)
func (s *Server) handleFilesDownload(c *Ctx) error {
	rc, err := s.Files.ReadStream(c.Query("path"))
	if err != nil {
		return err
	}
	defer rc.Close()
	c.W.Header().Set("Content-Type", "application/octet-stream")
	c.W.Header().Set("Content-Disposition", "attachment")
	c.W.WriteHeader(http.StatusOK)
	_, _ = io.Copy(c.W, rc)
	return nil
}

// handleFilesCompress 压缩(tar.gz/zip)
func (s *Server) handleFilesCompress(c *Ctx) error {
	var req struct {
		Dir     string   `json:"dir"`
		Archive string   `json:"archive"`
		Format  string   `json:"format"`
		Names   []string `json:"names"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	res, err := s.Files.Compress(req.Dir, req.Archive, req.Format, req.Names)
	c.Audit("files.compress", req.Dir+"/"+req.Archive, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(res)
	return nil
}

// handleFilesExtract 解压(防 zip-slip)
func (s *Server) handleFilesExtract(c *Ctx) error {
	var req struct {
		Archive string `json:"archive"`
		Dest    string `json:"dest"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	start := now()
	res, err := s.Files.Extract(req.Archive, req.Dest)
	c.Audit("files.extract", req.Archive+" → "+req.Dest, start, err, "", nil)
	if err != nil {
		return err
	}
	c.OK(res)
	return nil
}

// handleFilesSearch 递归搜索(特殊文件系统跳过,深度/结果数限制)
func (s *Server) handleFilesSearch(c *Ctx) error {
	limit, _ := strconv.Atoi(c.Query("limit"))
	depth, _ := strconv.Atoi(c.Query("depth"))
	results, truncated, err := s.Files.Search(c.Query("path"), c.Query("q"), limit, depth)
	if err != nil {
		return err
	}
	c.OK(map[string]any{"results": results, "truncated": truncated})
	return nil
}
