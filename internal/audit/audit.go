// Package audit 危险操作审计日志(JSONL 追加)。
// 记录:谁(面板转发用户名)、何时、什么操作、目标、结果、是否回滚(§44)。
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry 单条审计记录
type Entry struct {
	Time      string `json:"time"`       // RFC3339
	RequestID string `json:"request_id"` // 操作请求 ID(§43)
	User      string `json:"user"`       // 发起用户(由面板在 X-Agent-User 头透传)
	Action    string `json:"action"`     // 如 swap.resize
	Target    string `json:"target"`     // 如 2GB→4GB / service name / project
	Status    string `json:"status"`     // success / failed / in_progress
	Error     string `json:"error,omitempty"`
	Rollback  string `json:"rollback,omitempty"` // rollback=success / rollback=failed / 空
	Duration  string `json:"duration,omitempty"` // 耗时
	Detail    any    `json:"detail,omitempty"`   // 额外结构化信息
}

// Logger 审计日志器(线程安全,单次写 ≤ 4KB)
type Logger struct {
	mu   sync.Mutex
	file *os.File
}

// New 打开审计日志(目录不存在则创建)
func New(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "audit.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Logger{file: f}, nil
}

// Log 追加一条审计记录
func (l *Logger) Log(e Entry) {
	if l == nil || l.file == nil {
		return
	}
	if e.Time == "" {
		e.Time = time.Now().Format(time.RFC3339)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.file.Write(append(b, '\n'))
}

// LogOp 便捷方法:记录一次危险操作的开始/结束
func (l *Logger) LogOp(reqID, user, action, target string, started time.Time, err error, rollback string, detail any) {
	st := "success"
	errMsg := ""
	if err != nil {
		st = "failed"
		errMsg = err.Error()
	}
	l.Log(Entry{
		RequestID: reqID,
		User:      user,
		Action:    action,
		Target:    target,
		Status:    st,
		Error:     errMsg,
		Rollback:  rollback,
		Duration:  time.Since(started).Round(time.Millisecond).String(),
		Detail:    detail,
	})
}

// Close 关闭日志文件
func (l *Logger) Close() {
	if l != nil && l.file != nil {
		_ = l.file.Close()
	}
}

// 便于调试输出
func (e Entry) String() string {
	return fmt.Sprintf("%s %s %s %s", e.Time, e.Action, e.Target, e.Status)
}
