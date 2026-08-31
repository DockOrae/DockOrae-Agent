// Package hostexec 宿主机操作执行器。
//
// 两种模式(§46:Agent 必须操作真实宿主机,而非自身容器环境):
//   - Nsenter 模式(容器内):全部命令经 nsenter -t 1 -m -u -i -n 进入宿主命名空间执行,
//     文件读写也经宿主挂载命名空间完成(需要 privileged + pid:host)。
//   - Direct 模式(二进制部署):Agent 直接跑在宿主,命令原样执行。
//
// 安全约定:所有命令由固定 handler 构造(参数经严格校验),禁止拼接用户输入执行任意 shell。
package hostexec

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// Mode 执行模式
type Mode int

const (
	ModeDirect Mode = iota
	ModeNsenter
)

// Execer 宿主执行器
type Execer struct {
	Mode Mode
	// NsenterArgs 进入宿主命名空间的前缀(nsenter 模式专用)
	NsenterArgs []string
	// ComposeBin compose 命令拆分(如 ["docker","compose"] 或 ["docker-compose"])
	ComposeBin []string
}

// New 构造执行器
func New(inContainer bool, composeBin string) *Execer {
	e := &Execer{Mode: ModeDirect}
	if inContainer {
		e.Mode = ModeNsenter
		e.NsenterArgs = []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "--"}
	}
	if composeBin != "" {
		e.ComposeBin = strings.Fields(composeBin)
	}
	return e
}

// InContainer 是否容器模式
func (e *Execer) InContainer() bool { return e.Mode == ModeNsenter }

// Command 构造命令(容器模式自动加 nsenter 前缀)
func (e *Execer) Command(name string, args ...string) *exec.Cmd {
	full := []string{name}
	full = append(full, args...)
	if e.Mode == ModeNsenter {
		full = append(e.NsenterArgs, full...)
	}
	return exec.Command(full[0], full[1:]...)
}

// Output 执行并返回 stdout(截断到 1MB)
func (e *Execer) Output(name string, args ...string) ([]byte, error) {
	return e.OutputWithTimeout(120*time.Second, name, args...)
}

// OutputWithTimeout 带超时执行
func (e *Execer) OutputWithTimeout(timeout time.Duration, name string, args ...string) ([]byte, error) {
	cmd := e.Command(name, args...)
	out, err := runWithTimeout(cmd, timeout)
	if err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "执行 %s 失败: %v %s", name, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// OutputString 执行并返回 stdout 字符串(去首尾空白)
func (e *Execer) OutputString(name string, args ...string) (string, error) {
	out, err := e.Output(name, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RunScript 执行内部固定脚本(sh -c)。脚本内容必须是代码内常量,
// 用户输入仅能以单引号转义后的参数形式拼接(见 Quote)。
func (e *Execer) RunScript(script string) error {
	cmd := e.Command("sh", "-c", script)
	out, err := runWithTimeout(cmd, 300*time.Second)
	if err != nil {
		return errs.Newf(errs.EXEC_FAILED, "脚本执行失败: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Quote 单引号转义(嵌入 sh -c 脚本的安全参数形式)
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ReadFile 读取宿主文件
func (e *Execer) ReadFile(path string) ([]byte, error) {
	return e.Output("cat", path)
}

// ReadFileString 读取宿主文件为字符串
func (e *Execer) ReadFileString(path string) (string, error) {
	out, err := e.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// WriteFile 写入宿主文件(先写临时文件再原子替换,权限 0600/0644 等)
func (e *Execer) WriteFile(path string, data []byte, perm os.FileMode) error {
	if e.Mode == ModeDirect {
		tmp := path + ".agent-tmp"
		if err := os.WriteFile(tmp, data, perm); err != nil {
			return errs.Newf(errs.EXEC_FAILED, "写入临时文件失败: %v", err)
		}
		if err := os.Chmod(tmp, perm); err != nil {
			_ = os.Remove(tmp)
			return errs.Newf(errs.EXEC_FAILED, "设置权限失败: %v", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return errs.Newf(errs.EXEC_FAILED, "替换文件失败: %v", err)
		}
		return nil
	}
	// nsenter 模式:sh -c 'cat > path.tmp && chmod && mv' ,数据经 stdin 传入
	p := Quote(path)
	script := fmt.Sprintf("cat > %s.agent-tmp && chmod %o %s.agent-tmp && mv -f %s.agent-tmp %s", p, perm, p, p, p)
	cmd := e.Command("sh", "-c", script)
	cmd.Stdin = bytes.NewReader(data)
	out, err := runWithTimeout(cmd, 30*time.Second)
	if err != nil {
		return errs.Newf(errs.EXEC_FAILED, "写入宿主文件 %s 失败: %v %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveFile 删除宿主文件(路径须经调用方严格校验,仅用于受控文件)
func (e *Execer) RemoveFile(path string) error {
	cmd := e.Command("rm", "-f", path)
	if out, err := runWithTimeout(cmd, 30*time.Second); err != nil {
		return errs.Newf(errs.EXEC_FAILED, "删除文件 %s 失败: %v %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// MkdirAll 递归创建宿主目录(0755)
func (e *Execer) MkdirAll(path string) error {
	cmd := e.Command("mkdir", "-p", path)
	if out, err := runWithTimeout(cmd, 30*time.Second); err != nil {
		return errs.Newf(errs.EXEC_FAILED, "创建目录 %s 失败: %v %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CommandStream 执行命令并逐行回调 stdout/stderr(流式,如 compose up 进度)。
// 返回的 cancel 用于中断;errCh 在命令结束后返回最终错误。
func (e *Execer) CommandStream(name string, args []string, onLine func(line string)) (cancel func(), errCh <-chan error, err error) {
	cmd := e.Command(name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	ch := make(chan error, 1)
	go func() {
		defer close(ch)
		var wg sync.WaitGroup
		scan := func(r io.Reader) {
			defer wg.Done()
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				onLine(sc.Text())
			}
		}
		wg.Add(2)
		go scan(stdout)
		go scan(stderr)
		wg.Wait()
		ch <- cmd.Wait()
	}()
	return func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}, ch, nil
}

// Exists 宿主文件是否存在
func (e *Execer) Exists(path string) bool {
	_, err := e.Output("test", "-e", path)
	return err == nil
}

// Hostname 读取真实宿主 hostname(经宿主 UTS 命名空间)
func (e *Execer) Hostname() (string, error) {
	return e.OutputString("hostname")
}

// runWithTimeout 执行命令并捕获输出,超时杀进程
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
		return buf.Bytes(), fmt.Errorf("执行超时(%s)", timeout)
	}
}

// ComposeCommand 构造 compose 命令(经 ComposeBin 拆分)
func (e *Execer) ComposeCommand(args ...string) *exec.Cmd {
	bin := e.ComposeBin
	if len(bin) == 0 {
		bin = []string{"docker", "compose"}
	}
	return e.Command(bin[0], append(bin[1:], args...)...)
}

// ComposeOutput 执行 compose 命令并返回输出
func (e *Execer) ComposeOutput(timeout time.Duration, args ...string) ([]byte, error) {
	cmd := e.ComposeCommand(args...)
	out, err := runWithTimeout(cmd, timeout)
	if err != nil {
		return nil, errs.Newf(errs.EXEC_FAILED, "compose %s 失败: %v %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
