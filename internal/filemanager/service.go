// Service 文件管理器服务(自 KPanel filemanager.Manager 移植,2026-09-02)。
// Direct 模式:进程内构造 Manager 执行;Nsenter 模式:经 `nsenter ... -- <bin> filemgr`
// 重新执行自身二进制进入宿主挂载命名空间 —— 两种模式共用 KPanel 同一套文件实现。
package filemanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/contract"
	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// Service 宿主文件管理器服务(面板唯一调用方)。
type Service struct {
	exec *hostexec.Execer
}

// NewService 构造文件管理器服务
func NewService(exec *hostexec.Execer) *Service {
	return &Service{exec: exec}
}

// ensureSelfBin 把自身二进制复制到宿主可见且可执行目录(供 nsenter 重执行)
func (s *Service) ensureSelfBin() (string, error) {
	if !s.exec.InContainer() {
		return "/proc/self/exe", nil
	}
	self, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		return "", err
	}
	shared := os.Getenv("AGENT_BIN_DIR")
	if shared == "" {
		socket := os.Getenv("AGENT_SOCKET")
		shared = filepath.Dir(socket)
	}
	if shared == "." || shared == "/" || shared == "" {
		return "", errors.New("无法定位宿主二进制目录(AGENT_BIN_DIR/AGENT_SOCKET)")
	}
	target := filepath.Join(shared, "agent-bin")
	if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, self) {
		return target, nil
	}
	if err := os.MkdirAll(shared, 0o755); err != nil {
		return "", err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, self, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return target, nil
}

// run 执行 filemgr 操作,返回 data 字段原始 JSON。
func (s *Service) run(op string, args any, stdin io.Reader) ([]byte, error) {
	argJSON, _ := json.Marshal(args)
	if !s.exec.InContainer() {
		var buf bytes.Buffer
		if stdin == nil {
			stdin = bytes.NewReader(nil)
		}
		manager, err := New(DefaultConfig())
		if err != nil {
			return nil, errs.Newf(errs.INTERNAL, "初始化文件管理器失败: %v", err)
		}
		defer manager.Close()
		if err := filemgrRun(op, argJSON, stdin, &buf, manager); err != nil {
			return nil, err
		}
		return envelopeData(buf.Bytes())
	}
	binPath, binErr := s.ensureSelfBin()
	if binErr != nil {
		return nil, errs.Newf(errs.INTERNAL, "准备宿主执行二进制失败: %v", binErr)
	}
	cmd := s.exec.Command(binPath, "filemgr", op, string(argJSON))
	if stdin != nil {
		cmd.Stdin = stdin
	}
	out, err := runFilemgrTimeout(cmd, 600*time.Second)
	if err != nil {
		if envErr := dataErr(out); envErr != nil {
			return nil, envErr
		}
		return nil, errs.Newf(errs.INTERNAL, "宿主文件操作失败: %v %s", err, bytes.TrimSpace(out))
	}
	return envelopeData(out)
}

// call 执行操作并把 data 解码到 out
func (s *Service) call(op string, args any, out any) error {
	raw, err := s.run(op, args, nil)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// ListPage 目录列表
func (s *Service) ListPage(ctx context.Context, path string, options ListOptions) (contract.FileDirectory, error) {
	var res contract.FileDirectory
	if err := s.call("list", map[string]any{"path": path, "limit": options.Limit, "offset": options.Offset, "search": options.Search}, &res); err != nil {
		return res, err
	}
	return res, nil
}

// Stat 单条目
func (s *Service) Stat(path string) (contract.FileEntry, error) {
	var e contract.FileEntry
	err := s.call("entry", map[string]any{"path": path}, &e)
	return e, err
}

// ListTrash 回收站列表
func (s *Service) ListTrash(ctx context.Context) (contract.FileTrashDirectory, error) {
	var res contract.FileTrashDirectory
	err := s.call("trash", map[string]any{}, &res)
	return res, err
}

// Action 批量操作
func (s *Service) Action(ctx context.Context, input contract.FileActionRequest) (contract.FileActionResult, error) {
	var res contract.FileActionResult
	err := s.call("action", input, &res)
	return res, err
}

// WriteText 文本写入(编辑器保存)
func (s *Service) WriteText(ctx context.Context, path string, input contract.FileWriteRequest) (contract.FileEntry, error) {
	var e contract.FileEntry
	args := map[string]any{
		"path": path, "content": input.Content,
		"expectedResourceVersion": input.ExpectedResourceVersion,
	}
	err := s.call("write_text", args, &e)
	return e, err
}

// Upload 上传(流式)
func (s *Service) Upload(ctx context.Context, directory, name string, content io.Reader, contentLength int64, overwrite bool) (contract.FileEntry, error) {
	if !s.exec.InContainer() {
		manager, err := New(DefaultConfig())
		if err != nil {
			return contract.FileEntry{}, errs.Newf(errs.INTERNAL, "初始化文件管理器失败: %v", err)
		}
		defer manager.Close()
		return manager.Upload(ctx, directory, name, content, contentLength, overwrite)
	}
	var e contract.FileEntry
	args := map[string]any{"path": directory, "name": name, "overwrite": overwrite}
	// nsenter 模式:经 stdin 管道流式写入子进程(与 fsop 上传一致)
	argJSON, _ := json.Marshal(args)
	binPath, binErr := s.ensureSelfBin()
	if binErr != nil {
		return e, errs.Newf(errs.INTERNAL, "准备宿主执行二进制失败: %v", binErr)
	}
	cmd := s.exec.Command(binPath, "filemgr", "upload", string(argJSON))
	cmd.Stdin = content
	out, err := runFilemgrTimeout(cmd, 600*time.Second)
	if err != nil {
		if envErr := dataErr(out); envErr != nil {
			return e, envErr
		}
		return e, errs.Newf(errs.INTERNAL, "宿主上传失败: %v", err)
	}
	raw, err := envelopeData(out)
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, err
	}
	return e, nil
}

// ReadText 读取文本(≤limit 字节;内部经 filemgr read 流式)
func (s *Service) ReadText(ctx context.Context, path string, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	if err := s.stream("read", map[string]any{"path": path, "limit": limit}, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadRange 读取文件指定区间(尾部预览)
func (s *Service) ReadRange(ctx context.Context, path string, start, length int64) ([]byte, error) {
	var buf bytes.Buffer
	if err := s.stream("read_range", map[string]any{"path": path, "start": start, "length": length}, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// StreamRead 流式读取整个文件到 writer(下载)
func (s *Service) StreamRead(ctx context.Context, path string, w io.Writer) error {
	return s.stream("read", map[string]any{"path": path}, w)
}

// ExportZIP 压缩导出(流式)
func (s *Service) ExportZIP(ctx context.Context, sources []string, versions map[string]string, w io.Writer) error {
	return s.stream("export_zip", map[string]any{"sources": sources, "expectedResourceVersions": versions}, w)
}

// stream 流式操作:stdout 原始字节(非信封)。
func (s *Service) stream(op string, args any, w io.Writer) error {
	argJSON, _ := json.Marshal(args)
	if !s.exec.InContainer() {
		manager, err := New(DefaultConfig())
		if err != nil {
			return errs.Newf(errs.INTERNAL, "初始化文件管理器失败: %v", err)
		}
		defer manager.Close()
		return filemgrStream(op, argJSON, w, manager)
	}
	binPath, binErr := s.ensureSelfBin()
	if binErr != nil {
		return errs.Newf(errs.INTERNAL, "准备宿主执行二进制失败: %v", binErr)
	}
	cmd := s.exec.Command(binPath, "filemgr", op, string(argJSON))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return errs.Newf(errs.EXEC_FAILED, "宿主文件流操作失败: %v", err)
	}
	_, copyErr := io.Copy(w, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil {
		return copyErr
	}
	if waitErr != nil {
		return errs.Newf(errs.EXEC_FAILED, "宿主文件流操作失败: %v", waitErr)
	}
	return nil
}

// envelopeData 解析 {ok,data} 信封;ok:false 时返回信封内业务错误
func envelopeData(raw []byte) ([]byte, error) {
	var env struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
		Err  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, errs.New(errs.INTERNAL, "宿主文件操作响应无效")
	}
	if !env.OK {
		if env.Err != nil {
			return nil, errs.New(env.Err.Code, env.Err.Message)
		}
		return nil, errs.New(errs.INTERNAL, "宿主文件操作失败")
	}
	return env.Data, nil
}

// dataErr 从错误信封构造业务错误;非错误信封返回 nil
func dataErr(raw []byte) error {
	var env struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Error != nil {
		return errs.New(env.Error.Code, env.Error.Message)
	}
	return nil
}

// runFilemgrTimeout 执行命令并捕获输出,超时杀进程
func runFilemgrTimeout(cmd *exec.Cmd, timeout time.Duration) ([]byte, error) {
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return buf.Bytes(), err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return buf.Bytes(), err
		}
		return buf.Bytes(), nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return buf.Bytes(), context.DeadlineExceeded
	}
}

