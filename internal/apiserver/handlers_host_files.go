// 宿主文件管理 HTTP 端点(宿主文件端点,2026-09-02 移植重构)。
// 端点/参数/响应对齐既定契约,底层经 filemgr 重执行(容器 nsenter 架构)。
// 端点:
//
//	GET  /v1/files                → 目录列表(path/limit/offset/search)
//	GET  /v1/files/entry          → 单条目属性(path)
//	POST /v1/files/entries        → 批量属性(paths)
//	GET  /v1/files/trash          → 回收站列表
//	GET  /v1/files/content        → 读文件/下载(path/disposition/mode/version)
//	PUT  /v1/files/content        → 写文件(path, JSON {content,expectedResourceVersion})
//	GET  /v1/files/archive        → 压缩下载(selection JSON + name)
//	GET  /v1/files/text           → 文本读取(≤64KiB)
//	GET  /v1/files/tail           → 文本尾部(≤64KiB)
//	POST /v1/files/upload         → 上传(path/name/overwrite, octet-stream)
//	POST /v1/files/actions        → 批量操作(mkdir/rename/copy/move/trash/chmod/compress/extract/trash_*)
//
// 下载/上传/压缩为流式:经 nsenter cat/tar 子进程,面板侧透传。
package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/contract"
	"github.com/DockOrae/DockOrae-Agent/internal/filemanager"
)

const (
	fileTransferMaxDuration = 2 * time.Hour
	fileTextMaxBytes        = 64 << 10
	fileToolTextMaxBytes    = 64 << 10
	fileArchiveQueryMax     = 256 << 10
)

type fileTextResult struct {
	Path            string `json:"path"`
	Content         string `json:"content"`
	SizeBytes       int64  `json:"sizeBytes"`
	ResourceVersion string `json:"resourceVersion"`
}

type fileTailResult struct {
	Path            string `json:"path"`
	Content         string `json:"content"`
	SizeBytes       int64  `json:"sizeBytes"`
	ResourceVersion string `json:"resourceVersion"`
	Truncated       bool   `json:"truncated"`
}

// handleFileList GET /v1/files
func (s *Server) handleFileList(c *Ctx) error {
	if c.R.URL.RawPath != "" {
		return c.err(http.StatusBadRequest, "invalid_path", "文件路径无效")
	}
	values := c.R.URL.Query()
	limit := filemanager.MaxDirectoryEntries
	if raw := values.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > filemanager.MaxDirectoryEntries {
			return c.err(http.StatusBadRequest, "invalid_limit", "目录项目上限无效")
		}
		limit = parsed
	}
	offset := 0
	if raw := values.Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed >= filemanager.MaxDirectoryScan {
			return c.err(http.StatusBadRequest, "invalid_offset", "目录偏移量无效")
		}
		offset = parsed
	}
	search := values.Get("search")
	if len(search) > filemanager.MaxSearchBytes {
		return c.err(http.StatusBadRequest, "invalid_search", "目录搜索内容过长")
	}
	result, err := s.FilemgrSvc.ListPage(c.R.Context(), values.Get("path"), filemanager.ListOptions{
		Limit: limit, Offset: offset, Search: search,
	})
	if err != nil {
		return s.writeFileProblem(c, err)
	}
	return c.rawJSON(http.StatusOK, result)
}

// handleFileEntry GET /v1/files/entry
func (s *Server) handleFileEntry(c *Ctx) error {
	if c.R.URL.RawPath != "" || c.R.URL.Query().Get("path") == "" {
		return c.err(http.StatusBadRequest, "invalid_query", "文件查询参数无效")
	}
	entry, err := s.FilemgrSvc.Stat(c.R.URL.Query().Get("path"))
	if err != nil {
		return s.writeFileProblem(c, err)
	}
	return c.rawJSON(http.StatusOK, entry)
}

// handleFileEntries POST /v1/files/entries
func (s *Server) handleFileEntries(c *Ctx) error {
	if c.R.URL.RawPath != "" || c.R.URL.RawQuery != "" {
		return c.err(http.StatusBadRequest, "invalid_query", "文件查询参数无效")
	}
	var input contract.FileEntryBatchRequest
	if err := c.Bind(&input); err != nil {
		return err
	}
	if len(input.Paths) == 0 || len(input.Paths) > contract.MaxFileEntryBatch {
		return c.err(http.StatusBadRequest, "file_request_invalid", "文件请求无效")
	}
	seen := make(map[string]struct{}, len(input.Paths))
	result := contract.FileEntryBatchResult{
		Entries:     make([]contract.FileEntry, 0, len(input.Paths)),
		Unavailable: make([]string, 0),
	}
	for _, filePath := range input.Paths {
		if filePath == "" {
			return c.err(http.StatusBadRequest, "file_request_invalid", "文件请求无效")
		}
		if _, exists := seen[filePath]; exists {
			return c.err(http.StatusBadRequest, "file_request_invalid", "文件请求无效")
		}
		seen[filePath] = struct{}{}
		entry, err := s.FilemgrSvc.Stat(filePath)
		if err != nil {
			result.Unavailable = append(result.Unavailable, filePath)
			continue
		}
		result.Entries = append(result.Entries, entry)
	}
	return c.rawJSON(http.StatusOK, result)
}

// handleFileTrashList GET /v1/files/trash
func (s *Server) handleFileTrashList(c *Ctx) error {
	if c.R.URL.RawPath != "" || c.R.URL.RawQuery != "" {
		return c.err(http.StatusBadRequest, "invalid_query", "回收站查询参数无效")
	}
	result, err := s.FilemgrSvc.ListTrash(c.R.Context())
	if err != nil {
		return s.writeFileProblem(c, err)
	}
	return c.rawJSON(http.StatusOK, result)
}

// handleFileContent GET/PUT /v1/files/content
func (s *Server) handleFileContent(c *Ctx) error {
	switch c.R.Method {
	case http.MethodGet, http.MethodHead:
		return s.handleFileRead(c, false)
	case http.MethodPut:
		return s.handleFileWrite(c)
	default:
		c.W.Header().Set("Allow", http.MethodGet+", "+http.MethodHead+", "+http.MethodPut)
		return c.err(http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不允许")
	}
}

// handleFileText GET /v1/files/text — 结构化文本读取(≤64KiB)
func (s *Server) handleFileText(c *Ctx) error {
	if c.R.URL.RawPath != "" || c.R.URL.Query().Get("path") == "" {
		return c.err(http.StatusBadRequest, "invalid_query", "File query is invalid")
	}
	entry, err := s.FilemgrSvc.Stat(c.R.URL.Query().Get("path"))
	if err != nil {
		return s.writeFileProblem(c, err)
	}
	if !entry.Editable || entry.SizeBytes > fileToolTextMaxBytes {
		return c.err(http.StatusUnprocessableEntity, "text_preview_unavailable", "File is not an editable UTF-8 text file up to 64 KiB")
	}
	content, err := s.FilemgrSvc.ReadText(c.R.Context(), entry.Path, fileToolTextMaxBytes)
	if err != nil {
		return s.writeFileProblem(c, err)
	}
	return c.rawJSON(http.StatusOK, fileTextResult{
		Path: entry.Path, Content: string(content), SizeBytes: entry.SizeBytes,
		ResourceVersion: entry.ResourceVersion,
	})
}

// handleFileTail GET /v1/files/tail — 文本尾部(≤64KiB)
func (s *Server) handleFileTail(c *Ctx) error {
	if c.R.URL.RawPath != "" {
		return c.err(http.StatusBadRequest, "invalid_query", "File tail query is invalid")
	}
	maxBytes := int64(32 << 10)
	if raw := c.R.URL.Query().Get("maxBytes"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1024 || parsed > fileToolTextMaxBytes {
			return c.err(http.StatusBadRequest, "invalid_limit", "File tail limit must be between 1024 and 65536 bytes")
		}
		maxBytes = parsed
	}
	entry, err := s.FilemgrSvc.Stat(c.R.URL.Query().Get("path"))
	if err != nil {
		return s.writeFileProblem(c, err)
	}
	if !entry.Editable || entry.SizeBytes == 0 {
		return c.err(http.StatusUnprocessableEntity, "text_preview_unavailable", "File is not an editable UTF-8 text file")
	}
	start := entry.SizeBytes - maxBytes
	if start < 0 {
		start = 0
	}
	content, err := s.FilemgrSvc.ReadRange(c.R.Context(), entry.Path, start, maxBytes)
	if err != nil {
		return s.writeFileProblem(c, err)
	}
	if int64(len(content)) > maxBytes {
		content = content[:maxBytes]
	}
	if start > 0 {
		if newline := bytes.IndexByte(content, '\n'); newline >= 0 {
			content = content[newline+1:]
		}
	}
	for removed := 0; start > 0 && len(content) > 0 && !utf8Valid(content) && removed < 3; removed++ {
		content = content[1:]
	}
	if !utf8Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return c.err(http.StatusUnprocessableEntity, "text_preview_unavailable", "File tail is not valid UTF-8 text")
	}
	return c.rawJSON(http.StatusOK, fileTailResult{
		Path: entry.Path, Content: string(content), SizeBytes: entry.SizeBytes,
		ResourceVersion: entry.ResourceVersion, Truncated: start > 0,
	})
}

func utf8Valid(b []byte) bool {
	return bytes.IndexFunc(b, func(r rune) bool { return r == 0xFFFD }) < 0
}

// handleFileWrite PUT /v1/files/content — JSON {content, expectedResourceVersion}
func (s *Server) handleFileWrite(c *Ctx) error {
	if c.R.URL.RawPath != "" || c.R.URL.Query().Get("path") == "" {
		return c.err(http.StatusBadRequest, "invalid_query", "文件查询参数无效")
	}
	if ct := strings.TrimSpace(strings.Split(c.R.Header.Get("Content-Type"), ";")[0]); ct != "application/json" {
		return c.err(http.StatusUnsupportedMediaType, "json_required", "必须提交 JSON")
	}
	c.R.Body = http.MaxBytesReader(c.W, c.R.Body, filemanager.MaxTextBytes+(64<<10))
	var input contract.FileWriteRequest
	if err := c.Bind(&input); err != nil {
		return err
	}
	entry, err := s.FilemgrSvc.WriteText(c.R.Context(), c.R.URL.Query().Get("path"), input)
	if err != nil {
		return s.writeFileProblem(c, err)
	}
	return c.rawJSON(http.StatusOK, contract.FileWriteResult{Entry: entry})
}

// handleFileAction POST /v1/files/actions
func (s *Server) handleFileAction(c *Ctx) error {
	if c.R.URL.RawPath != "" || c.R.URL.RawQuery != "" {
		return c.err(http.StatusBadRequest, "invalid_query", "文件操作参数无效")
	}
	var input contract.FileActionRequest
	if err := c.Bind(&input); err != nil {
		return err
	}
	result, err := s.FilemgrSvc.Action(c.R.Context(), input)
	if err != nil {
		return s.writeFileProblem(c, err)
	}
	status := http.StatusOK
	if len(result.Failed) > 0 {
		status = http.StatusMultiStatus
	}
	return c.rawJSON(status, result)
}

// handleFileUpload POST /v1/files/upload — octet-stream,path/name/overwrite query
func (s *Server) handleFileUpload(c *Ctx) error {
	if c.R.URL.RawPath != "" {
		return c.err(http.StatusBadRequest, "invalid_query", "上传参数无效")
	}
	if ct := strings.TrimSpace(strings.Split(c.R.Header.Get("Content-Type"), ";")[0]); ct != "application/octet-stream" {
		return c.err(http.StatusUnsupportedMediaType, "binary_required", "上传必须使用二进制内容")
	}
	overwrite := false
	if raw := c.R.URL.Query().Get("overwrite"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return c.err(http.StatusBadRequest, "invalid_overwrite", "覆盖选项无效")
		}
		overwrite = parsed
	}
	if c.R.ContentLength > filemanager.MaxUploadBytes {
		return s.writeFileProblem(c, filemanager.ErrTooLarge)
	}
	c.R.Body = http.MaxBytesReader(c.W, c.R.Body, filemanager.MaxUploadBytes+1)
	entry, err := s.FilemgrSvc.Upload(
		c.R.Context(),
		c.R.URL.Query().Get("path"),
		c.R.URL.Query().Get("name"),
		c.R.Body,
		c.R.ContentLength,
		overwrite,
	)
	if err != nil {
		return s.writeFileProblem(c, err)
	}
	return c.rawJSON(http.StatusCreated, entry)
}

// handleFileRead GET /v1/files/content — 下载/内联预览(流式)
func (s *Server) handleFileRead(c *Ctx, shareContent bool) error {
	values := c.R.URL.Query()
	allowedQuery := []string{"path", "disposition", "mode", "version"}
	if shareContent {
		allowedQuery = []string{"path", "disposition"}
	}
	if c.R.URL.RawPath != "" || !strictQuery(values, allowedQuery...) {
		return c.err(http.StatusBadRequest, "invalid_query", "文件查询参数无效")
	}
	disposition := values.Get("disposition")
	if disposition == "" {
		disposition = "inline"
	}
	if disposition != "inline" && disposition != "attachment" {
		return c.err(http.StatusBadRequest, "invalid_disposition", "文件响应方式无效")
	}
	readMode := values.Get("mode")
	if readMode != "" && readMode != "text" {
		return c.err(http.StatusBadRequest, "invalid_file_mode", "文件读取模式无效")
	}
	pathValue := values.Get("path")
	entry, err := s.FilemgrSvc.Stat(pathValue)
	if err != nil {
		return s.writeFileProblem(c, err)
	}
	if readMode == "text" {
		if disposition != "inline" || !entry.Editable || entry.SizeBytes > filemanager.MaxTextBytes {
			return c.err(http.StatusUnprocessableEntity, "text_preview_unavailable", "该文件不能作为文本编辑")
		}
		content, readErr := s.FilemgrSvc.ReadText(c.R.Context(), pathValue, filemanager.MaxTextBytes)
		if readErr != nil {
			return s.writeFileProblem(c, readErr)
		}
		c.W.Header().Set("Content-Disposition", "inline")
		c.W.Header().Set("Content-Type", "text/plain; charset=utf-8")
		c.W.Header().Set("ETag", `"`+entry.ResourceVersion+`"`)
		c.W.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		c.W.WriteHeader(http.StatusOK)
		_, _ = c.W.Write(content)
		return nil
	}
	contentType := entry.MIME
	if disposition == "inline" && activeContent(entry.Name, contentType) {
		contentType = "text/plain; charset=utf-8"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if formatted := mimeFormatMediaType(disposition, entry.Name); formatted != "" {
		c.W.Header().Set("Content-Disposition", formatted)
	}
	c.W.Header().Set("Content-Type", contentType)
	c.W.Header().Set("ETag", `"`+entry.ResourceVersion+`"`)
	c.W.Header().Set("Content-Security-Policy", fileContentSecurityPolicy(contentType))
	c.W.Header().Set("Cache-Control", "private, no-store")
	c.W.WriteHeader(http.StatusOK)
	if c.R.Method == http.MethodHead {
		return nil
	}
	return s.FilemgrSvc.StreamRead(c.R.Context(), pathValue, c.W)
}

// handleFileArchive GET /v1/files/archive — 压缩下载(selection JSON + name)
func (s *Server) handleFileArchive(c *Ctx) error {
	if c.R.Method != http.MethodGet {
		c.W.Header().Set("Allow", http.MethodGet)
		return c.err(http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不允许")
	}
	if c.R.URL.RawPath != "" || !strictQuery(c.R.URL.Query(), "selection", "name") {
		return c.err(http.StatusBadRequest, "invalid_query", "压缩下载参数无效")
	}
	selection := c.R.URL.Query().Get("selection")
	name := c.R.URL.Query().Get("name")
	if len(selection) == 0 || len(selection) > fileArchiveQueryMax ||
		!validArchiveDownloadName(name) {
		return c.err(http.StatusBadRequest, "invalid_archive_download", "压缩下载参数无效")
	}
	var input contract.FileArchiveDownloadRequest
	if json.Unmarshal([]byte(selection), &input) != nil {
		return c.err(http.StatusBadRequest, "invalid_archive_download", "压缩下载参数无效")
	}
	if !validArchiveDownloadSelection(input) {
		return c.err(http.StatusBadRequest, "invalid_archive_download", "压缩下载参数无效")
	}
	ctx, cancel := context.WithTimeout(c.R.Context(), fileTransferMaxDuration)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	go func() {
		err := s.FilemgrSvc.ExportZIP(ctx, input.Sources, input.ExpectedResourceVersions, writer)
		_ = writer.CloseWithError(err)
	}()
	buffer := make([]byte, 64<<10)
	read, err := reader.Read(buffer)
	if err != nil && read == 0 {
		return s.writeFileProblem(c, err)
	}
	c.W.Header().Set("Content-Type", "application/zip")
	if formatted := mimeFormatMediaType("attachment", name); formatted != "" {
		c.W.Header().Set("Content-Disposition", formatted)
	}
	c.W.Header().Set("Cache-Control", "private, no-store")
	c.W.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	c.W.WriteHeader(http.StatusOK)
	if read > 0 {
		if _, writeErr := c.W.Write(buffer[:read]); writeErr != nil {
			_ = reader.CloseWithError(writeErr)
			return nil
		}
	}
	if err == nil {
		_, _ = io.CopyBuffer(c.W, reader, buffer)
	}
	return nil
}

func validArchiveDownloadName(name string) bool {
	return name != "" && len(name) <= 1024 && path.Base(name) == name &&
		!strings.ContainsAny(name, "\\\x00\r\n") && strings.HasSuffix(strings.ToLower(name), ".zip")
}

func validArchiveDownloadSelection(input contract.FileArchiveDownloadRequest) bool {
	if len(input.Sources) == 0 || len(input.Sources) > filemanager.MaxBatchItems ||
		len(input.ExpectedResourceVersions) != len(input.Sources) {
		return false
	}
	seen := make(map[string]struct{}, len(input.Sources))
	for _, source := range input.Sources {
		version, ok := input.ExpectedResourceVersions[source]
		if !ok || version == "" || len(version) > 256 || source == "" || len(source) > 4096 ||
			!strings.HasPrefix(source, "/") || strings.ContainsAny(source, "\\\x00") || path.Clean(source) != source {
			return false
		}
		if _, exists := seen[source]; exists {
			return false
		}
		seen[source] = struct{}{}
	}
	return true
}

func strictQuery(values map[string][]string, allowed ...string) bool {
	keys := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		keys[value] = struct{}{}
	}
	for key, vals := range values {
		if _, ok := keys[key]; !ok || len(vals) != 1 {
			return false
		}
	}
	return true
}

func activeContent(name, contentType string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".html") ||
		strings.HasSuffix(lower, ".htm") ||
		strings.HasSuffix(lower, ".svg") ||
		contentType == "text/html" ||
		contentType == "image/svg+xml" ||
		contentType == "application/xhtml+xml"
}

func fileContentSecurityPolicy(contentType string) string {
	mediaType := strings.TrimSpace(strings.Split(contentType, ";")[0])
	if strings.HasPrefix(mediaType, "audio/") || strings.HasPrefix(mediaType, "video/") {
		return "default-src 'none'"
	}
	return "default-src 'none'; sandbox"
}

func mimeFormatMediaType(disposition, name string) string {
	return `attachment; filename="` + strings.ReplaceAll(name, `"`, `\"`) + `"`
}

// writeFileProblem 文件操作错误 → Problem 响应(错误码与契约一致)
func (s *Server) writeFileProblem(c *Ctx, err error) error {
	status, code, title := http.StatusUnprocessableEntity, "file_operation_failed", "文件操作失败"
	switch {
	case errors.Is(err, os.ErrNotExist):
		status, code, title = http.StatusNotFound, "file_not_found", "文件不存在"
	case errors.Is(err, os.ErrPermission):
		status, code, title = http.StatusForbidden, "file_permission_denied", "文件权限不足"
	case errors.Is(err, filemanager.ErrInvalidPath),
		errors.Is(err, filemanager.ErrRootOperation),
		errors.Is(err, filemanager.ErrAction),
		errors.Is(err, filemanager.ErrBatchTooLarge),
		errors.Is(err, filemanager.ErrInvalidEncoding):
		status, code, title = http.StatusBadRequest, "file_request_invalid", "文件请求无效"
	case errors.Is(err, filemanager.ErrProtected):
		status, code, title = http.StatusForbidden, "file_path_protected", "保护目录不可访问"
	case errors.Is(err, filemanager.ErrReadOnly):
		status, code, title = http.StatusForbidden, "file_path_read_only", "系统虚拟目录仅支持查看"
	case errors.Is(err, filemanager.ErrSymlink):
		status, code, title = http.StatusUnprocessableEntity, "file_symlink_rejected", "符号链接不能在面板中打开"
	case errors.Is(err, filemanager.ErrConflict),
		errors.Is(err, filemanager.ErrAlreadyExists):
		status, code, title = http.StatusConflict, "file_conflict", "文件状态冲突"
	case errors.Is(err, filemanager.ErrTooLarge):
		status, code, title = http.StatusRequestEntityTooLarge, "file_too_large", "文件超过允许的大小"
	case errors.Is(err, filemanager.ErrBusy):
		status, code, title = http.StatusTooManyRequests, "file_transfer_busy", "文件传输任务繁忙"
	case errors.Is(err, filemanager.ErrTrashFull):
		status, code, title = http.StatusInsufficientStorage, "file_trash_full", "回收站已满"
	case errors.Is(err, filemanager.ErrTrashMetadata):
		status, code, title = http.StatusUnprocessableEntity, "file_trash_not_restorable", "回收站项目无法恢复"
	case errors.Is(err, filemanager.ErrInvalidArchive):
		status, code, title = http.StatusUnprocessableEntity, "file_archive_invalid", "压缩包格式或内容无效"
	case errors.Is(err, filemanager.ErrNotDirectory),
		errors.Is(err, filemanager.ErrNotRegular):
		status, code, title = http.StatusUnprocessableEntity, "file_type_invalid", "文件类型不支持此操作"
	}
	return c.err(status, code, title)
}
