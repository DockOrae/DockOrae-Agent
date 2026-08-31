package files

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

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
	"github.com/DockOrae/DockOrae-Agent/internal/hostexec"
)

// Service 宿主文件操作服务(面板唯一调用方)。
// Direct 模式:fsop 逻辑进程内执行;Nsenter 模式:经 `nsenter ... -- <bin> fsop`
// 重新执行自身二进制进入宿主挂载命名空间 —— 两种模式共用同一套纯 Go 文件实现。
type Service struct {
	exec  *hostexec.Execer
	trash *TrashService
}

// ensureSelfBin 把自身二进制复制到宿主共享目录(与 agent socket 同目录,宿主可见),
// 供 nsenter 重执行;二进制已一致时跳过。
func (s *Service) ensureSelfBin() (string, error) {
	if !s.exec.InContainer() {
		return "/proc/self/exe", nil
	}
	self, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		return "", err
	}
	socket := os.Getenv("AGENT_SOCKET")
	shared := filepath.Dir(socket)
	if shared == "." || shared == "/" || socket == "" {
		return "", errors.New("AGENT_SOCKET 无效,无法定位宿主共享目录")
	}
	target := filepath.Join(shared, "agent-bin")
	if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, self) {
		return target, nil
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

// New 构造文件服务(dataDir 用于回收站开关状态持久化)
func New(exec *hostexec.Execer, dataDir string) *Service {
	home, _ := os.UserHomeDir()
	return &Service{exec: exec, trash: NewTrash(home, dataDir)}
}

// run 执行 fsop 操作,返回 data 字段原始 JSON。
func (s *Service) run(op string, args any, stdin io.Reader) ([]byte, error) {
	argJSON, _ := json.Marshal(args)
	if !s.exec.InContainer() {
		var buf bytes.Buffer
		if stdin == nil {
			stdin = bytes.NewReader(nil)
		}
		if err := fsopRun(op, argJSON, stdin, &buf, s.trash); err != nil {
			return nil, err
		}
		return envelopeData(buf.Bytes())
	}
	// nsenter 模式:进入宿主命名空间后重新执行自身二进制。
	// 坑:容器 overlay 内的二进制路径在宿主挂载命名空间不可见(/proc/self/exe 解析 ENOENT),
	// 须先把自身复制到宿主共享目录(/run/dockorae/agent-bin,与 socket 同目录)再执行。
	binPath, binErr := s.ensureSelfBin()
	if binErr != nil {
		return nil, errs.Newf(errs.INTERNAL, "准备宿主执行二进制失败: %v", binErr)
	}
	cmd := s.exec.Command(binPath, "fsop", op, string(argJSON))
	if stdin != nil {
		cmd.Stdin = stdin
	}
	out, err := runWithTimeout(cmd, 600*time.Second)
	if err != nil {
		// fsop 失败时信封已在 stdout;解析出业务错误码
		if _, parseErr := envelopeData(out); parseErr == nil {
			return nil, dataErr(out)
		}
		return nil, errs.Newf(errs.INTERNAL, "宿主文件操作失败: %v %s", err, bytes.TrimSpace(out))
	}
	return envelopeData(out)
}

// envelopeData 解析 {ok,data} 信封,返回 data 字节
func envelopeData(raw []byte) ([]byte, error) {
	var env struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, errs.New(errs.INTERNAL, "宿主文件操作响应无效")
	}
	if !env.OK {
		return nil, dataErr(raw)
	}
	return env.Data, nil
}

// dataErr 从错误信封构造 errs.Error
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
	return errs.New(errs.INTERNAL, "宿主文件操作失败")
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

// List 目录列表(showHidden 控制隐藏文件)
func (s *Service) List(dir string, showHidden bool) (*ListResult, error) {
	var res ListResult
	if err := s.call("list", map[string]any{"path": dir, "show_hidden": showHidden}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// DirSize 计算目录总大小
func (s *Service) DirSize(path string) (int64, error) {
	var res struct {
		Size int64 `json:"size"`
	}
	if err := s.call("dirsize", map[string]any{"path": path}, &res); err != nil {
		return 0, err
	}
	return res.Size, nil
}

// Chown 修改所有者/用户组
func (s *Service) Chown(path, owner, group string) error {
	return s.call("chown", map[string]any{"path": path, "owner": owner, "group": group}, nil)
}

// Stat 单条目
func (s *Service) Stat(path string) (*Entry, error) {
	var e Entry
	if err := s.call("stat", map[string]any{"path": path}, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// Touch 新建空文件
func (s *Service) Touch(path string) error {
	return s.call("touch", map[string]any{"path": path}, nil)
}

// Mkdir 新建目录
func (s *Service) Mkdir(path string) error {
	return s.call("mkdir", map[string]any{"path": path}, nil)
}

// Rename 重命名
func (s *Service) Rename(oldPath, newPath string) error {
	return s.call("rename", map[string]any{"old_path": oldPath, "new_path": newPath}, nil)
}

// Move 移动
func (s *Service) Move(src, dst string) error {
	return s.call("move", map[string]any{"src": src, "dst": dst}, nil)
}

// Remove 删除(回收站开启且非 force → 移入回收站;危险目录保护 + 目录递归确认在 fsop 层)
func (s *Service) Remove(paths []string, recursive, force bool) (bool, error) {
	var res struct {
		Trashed bool `json:"trashed"`
	}
	if err := s.call("remove", map[string]any{"paths": paths, "recursive": recursive, "force": force}, &res); err != nil {
		return false, err
	}
	return res.Trashed, nil
}

// Copy 复制
func (s *Service) Copy(src, dst string) error {
	return s.call("copy", map[string]any{"src": src, "dst": dst}, nil)
}

// Chmod 修改权限
func (s *Service) Chmod(path string, mode uint32) error {
	return s.call("chmod", map[string]any{"path": path, "mode": mode}, nil)
}

// Write 覆盖写入(编辑器保存,≤8MB)
func (s *Service) Write(path string, content []byte) error {
	_, err := s.run("write", map[string]any{"path": path}, bytes.NewReader(content))
	return err
}

// WriteStream 流式写入(上传;Direct 直接原子写,Nsenter 经 stdin 管道)
func (s *Service) WriteStream(path string, r io.Reader) error {
	if !s.exec.InContainer() {
		return WriteStream(path, r)
	}
	clean, err := CleanPath(path)
	if err != nil {
		return err
	}
	p := hostexec.Quote(clean)
	script := "cat > " + p + ".agent-tmp && mv -f " + p + ".agent-tmp " + p
	cmd := s.exec.Command("sh", "-c", script)
	cmd.Stdin = r
	if _, err := runWithTimeout(cmd, 600*time.Second); err != nil {
		return errs.Newf(errs.EXEC_FAILED, "写入宿主文件失败: %v", err)
	}
	return nil
}

// ReadStream 流式读取(下载)
func (s *Service) ReadStream(path string) (io.ReadCloser, error) {
	clean, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	if !s.exec.InContainer() {
		return ReadStream(clean)
	}
	cmd := s.exec.Command("cat", clean)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "读取宿主文件失败: %v", err)
	}
	return &cmdReadCloser{cmd: cmd, stdout: stdout}, nil
}

// cmdReadCloser 包装 nsenter cat 子进程为 ReadCloser(关闭时终止子进程)
type cmdReadCloser struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
}

func (c *cmdReadCloser) Read(p []byte) (int, error) { return c.stdout.Read(p) }
func (c *cmdReadCloser) Close() error {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.stdout.Close()
}

// Compress 压缩(格式 tar.gz/zip)
func (s *Service) Compress(dir, archive, format string, names []string) (*CompressResult, error) {
	var res CompressResult
	if err := s.call("compress", map[string]any{"dir": dir, "archive": archive, "format": format, "names": names}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Extract 解压
func (s *Service) Extract(archive, dest string) (*ExtractResult, error) {
	var res ExtractResult
	if err := s.call("extract", map[string]any{"archive": archive, "dest": dest}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Search 递归搜索
func (s *Service) Search(path, query string, limit, depth int) ([]SearchResult, bool, error) {
	var res struct {
		Results   []SearchResult `json:"results"`
		Truncated bool           `json:"truncated"`
	}
	if err := s.call("search", map[string]any{"path": path, "query": query, "limit": limit, "depth": depth}, &res); err != nil {
		return nil, false, err
	}
	return res.Results, res.Truncated, nil
}

// TrashStatus 回收站状态(开关 + 目录)
func (s *Service) TrashStatus() (map[string]any, error) {
	return s.callMap("trash_status")
}

// TrashSetEnabled 回收站开关
func (s *Service) TrashSetEnabled(enabled bool) error {
	return s.call("trash_enable", map[string]any{"enabled": enabled}, nil)
}

// TrashList 回收站列表
func (s *Service) TrashList() ([]TrashItem, error) {
	var res struct {
		Items []TrashItem `json:"items"`
	}
	if err := s.call("trash_list", map[string]any{}, &res); err != nil {
		return nil, err
	}
	return res.Items, nil
}

// TrashRestore 恢复
func (s *Service) TrashRestore(names []string) error {
	return s.call("trash_restore", map[string]any{"names": names}, nil)
}

// TrashDelete 彻底删除回收站条目
func (s *Service) TrashDelete(names []string) error {
	return s.call("trash_delete", map[string]any{"names": names}, nil)
}

// TrashEmpty 清空回收站
func (s *Service) TrashEmpty() error {
	return s.call("trash_empty", map[string]any{}, nil)
}

// callMap 执行操作返回 data 的 map 形式
func (s *Service) callMap(op string) (map[string]any, error) {
	raw, err := s.run(op, map[string]any{}, nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// runWithTimeout 执行命令并捕获输出,超时杀进程(与 hostexec 内部实现一致,此处独立供文件服务使用)
func runWithTimeout(cmd *exec.Cmd, timeout time.Duration) ([]byte, error) {
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
