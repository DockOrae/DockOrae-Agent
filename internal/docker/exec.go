// 容器单次命令执行(Exec,2026-09-02 容器终端重构)。
// 与旧交互式 WS 终端不同:单条命令经 /bin/sh -c 执行,收集 stdout/stderr + 退出码,
// 带超时(默认 30s)/输出上限(1MiB)/同容器并发限制(2),浏览器不接触 Docker。
package docker

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// Exec 限制常量(面板侧契约一致)
const (
	// DefaultExecTimeout 默认执行超时
	DefaultExecTimeout = 30 * time.Second
	// MaxExecTimeout 最大执行超时
	MaxExecTimeout = 300 * time.Second
	// DefaultExecMaxBytes 输出上限(超过截断,防 yes/超大日志)
	DefaultExecMaxBytes = 1 << 20 // 1 MiB
	// MaxExecConcurrency 同一容器最大并发 Exec 数
	MaxExecConcurrency = 2
	// execInspectInterval 退出码轮询间隔
	execInspectInterval = 150 * time.Millisecond
)

// ExecResult 单次执行结果
type ExecResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Truncated  bool   `json:"truncated"`
}

// errOutputLimit 输出超上限信号(内部用,stdcopy 经 writer 错误中断)
var errOutputLimit = errs.New(errs.EXEC_OUTPUT_LIMIT, "命令输出超过上限")

// execSlotTracker 同一容器并发 Exec 计数(上限 MaxExecConcurrency)
type execSlotTracker struct {
	mu    sync.Mutex
	count map[string]int
}

func newExecSlotTracker() *execSlotTracker {
	return &execSlotTracker{count: map[string]int{}}
}

// acquire 尝试占用一个槽位;已满返回 false
func (t *execSlotTracker) acquire(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.count[id] >= MaxExecConcurrency {
		return false
	}
	t.count[id]++
	return true
}

func (t *execSlotTracker) release(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.count[id] <= 1 {
		delete(t.count, id)
		return
	}
	t.count[id]--
}

// capWriter 输出上限写入器:stdout/stderr 共享 total 上限,超出标记截断并报错中断拷贝。
// 数据写入 dst(与 total 计数同源,避免计数与内容不一致)。
type capWriter struct {
	limit     int64
	total     int64
	truncated bool
	dst       io.Writer
}

func (w *capWriter) Write(p []byte) (int, error) {
	room := w.limit - w.total
	if room <= 0 {
		w.truncated = true
		return 0, errOutputLimit
	}
	if int64(len(p)) > room {
		// 部分写入后立即报错:stdcopy 收到 writer 错误即中断,提前结束读流
		p = p[:room]
		w.total += int64(len(p))
		w.truncated = true
		_, _ = w.dst.Write(p)
		return len(p), errOutputLimit
	}
	w.total += int64(len(p))
	_, _ = w.dst.Write(p)
	return len(p), nil
}

// ContainerExec 在容器内执行单条命令(非 TTY)。
// command 经 /bin/sh -c 执行(POSIX 基线,alpine/busybox 镜像均可用);
// timeout<=0 用默认 30s;输出总上限 1MiB,超出 truncated=true 并提前结束读流;
// 命令退出码非 0 不视为错误(HTTP 层照常返回,exit_code 字段表达命令结果)。
func (s *Service) ContainerExec(ctx context.Context, id, command string, timeout time.Duration) (ExecResult, error) {
	var res ExecResult
	if strings.TrimSpace(command) == "" {
		return res, errs.New(errs.INVALID_REQUEST, "command 不能为空")
	}
	if !s.execSlots.acquire(id) {
		return res, errs.Newf(errs.EXEC_CONCURRENT_LIMIT,
			"该容器已有 %d 个命令在执行,请稍后再试", MaxExecConcurrency)
	}
	defer s.execSlots.release(id)

	cli, err := s.Client()
	if err != nil {
		return res, errs.Newf(errs.DOCKER_UNAVAILABLE, "构造 docker client 失败: %v", err)
	}
	if timeout <= 0 {
		timeout = DefaultExecTimeout
	}
	if timeout > MaxExecTimeout {
		timeout = MaxExecTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 容器必须存在且运行中(避免执行到一半才报错,前端据此禁用按钮)
	insp, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return res, dockerErr(err)
	}
	if !insp.Container.State.Running {
		return res, errs.New(errs.CONTAINER_NOT_RUNNING, "容器当前未运行,无法执行命令")
	}

	started := time.Now()
	execRes, err := cli.ExecCreate(ctx, id, client.ExecCreateOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"/bin/sh", "-c", command},
	})
	if err != nil {
		return res, dockerErr(err)
	}
	attach, err := cli.ExecAttach(ctx, execRes.ID, client.ExecAttachOptions{})
	if err != nil {
		return res, dockerErr(err)
	}

	// 分离 stdout/stderr;超上限截断并中断拷贝(goroutine + ctx 取消,防无输出命令挂死)
	var outBuf, errBuf bytes.Buffer
	outW := &capWriter{limit: DefaultExecMaxBytes, dst: &outBuf}
	errW := &capWriter{limit: DefaultExecMaxBytes, dst: &errBuf}
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		_, _ = stdcopy.StdCopy(outW, errW, attach.Reader)
	}()
	select {
	case <-copyDone:
	case <-ctx.Done():
		attach.Close() // 解除 Reader 阻塞,中断 exec
		<-copyDone
		return res, errs.New(errs.EXEC_TIMEOUT, "命令执行超时(超过 "+timeout.String()+")")
	}
	attach.Close()
	truncated := outW.truncated || errW.truncated
	if truncated {
		// 输出超限:连接已关(exec 端 EOF,SIGPIPE 类命令会自然退出),直接查退出码
		return s.execFinish(ctx, cli, execRes.ID, started, outBuf.String(), errBuf.String(), true)
	}
	if ctx.Err() != nil {
		return res, errs.New(errs.EXEC_TIMEOUT, "命令执行超时(超过 "+timeout.String()+")")
	}
	return s.execFinish(ctx, cli, execRes.ID, started, outBuf.String(), errBuf.String(), false)
}

// execFinish 轮询 exec 退出码并组装结果(ctx 超时兜底)
func (s *Service) execFinish(ctx context.Context, cli *client.Client, execID string, started time.Time, stdout, stderr string, truncated bool) (ExecResult, error) {
	exitCode := 0
	for {
		st, err := cli.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		if err == nil && !st.Running {
			exitCode = st.ExitCode
			break
		}
		select {
		case <-ctx.Done():
			return ExecResult{}, errs.New(errs.EXEC_TIMEOUT, "命令执行超时")
		case <-time.After(execInspectInterval):
		}
	}
	return ExecResult{
		Stdout:     stdout,
		Stderr:     stderr,
		ExitCode:   exitCode,
		DurationMS: time.Since(started).Milliseconds(),
		Truncated:  truncated,
	}, nil
}

var _ = io.EOF
