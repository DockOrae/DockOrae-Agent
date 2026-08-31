package apiserver

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/DockOrae/DockOrae-Agent/internal/errs"
)

// Ctx 单请求上下文
type Ctx struct {
	W         http.ResponseWriter
	R         *http.Request
	RequestID string
	User      string // 发起用户(面板 X-Agent-User 头透传)
	S         *Server
	Params    map[string]string // 路由路径参数(:id 等)
	started   time.Time
}

// Param 读取路由路径参数
func (c *Ctx) Param(key string) string {
	if c.Params == nil {
		return ""
	}
	return c.Params[key]
}

// OK 成功信封:{"ok":true,"data":...}
func (c *Ctx) OK(data any) {
	c.W.Header().Set("Content-Type", "application/json")
	c.W.WriteHeader(http.StatusOK)
	if data == nil {
		_, _ = c.W.Write([]byte(`{"ok":true}`))
		return
	}
	b, err := json.Marshal(map[string]any{"ok": true, "data": data})
	if err != nil {
		c.Fail(errs.New(errs.INTERNAL, "响应序列化失败"))
		return
	}
	_, _ = c.W.Write(b)
}

// Fail 错误信封:{"ok":false,"error":{"code","message"}}
func (c *Ctx) Fail(err error) {
	ae := errs.FromError(err)
	c.W.Header().Set("Content-Type", "application/json")
	c.W.WriteHeader(ae.Status)
	b, _ := json.Marshal(map[string]any{
		"ok": false,
		"error": map[string]string{
			"code":    ae.Code,
			"message": ae.Message,
		},
	})
	_, _ = c.W.Write(b)
}

// Bind 解析 JSON 请求体(拒绝未知超大 body)
func (c *Ctx) Bind(v any) error {
	r := io.LimitReader(c.R.Body, 1<<20)
	if err := json.NewDecoder(r).Decode(v); err != nil {
		return errs.New(errs.INVALID_REQUEST, "请求体无效: "+err.Error())
	}
	return nil
}

// Query 读取查询参数
func (c *Ctx) Query(key string) string { return c.R.URL.Query().Get(key) }

// Audit 记录本次操作的审计日志(危险操作 handler 调用)
func (c *Ctx) Audit(action, target string, started time.Time, err error, rollback string, detail any) {
	c.S.Audit.LogOp(c.RequestID, c.User, action, target, started, err, rollback, detail)
}

// Confirm 校验危险操作确认(§10/§19/§38:必须显式 confirm=true)
func (c *Ctx) Confirm(confirm *bool, action string) error {
	if confirm == nil || !*confirm {
		return errs.Newf(errs.INVALID_CONFIRM, "%s 为危险操作,必须显式确认(confirm=true)", action)
	}
	return nil
}

// requestID 生成请求 ID
func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// constantTimeEqual 常量时间字符串比较
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		// 长度不同也走一次比较,避免长度信息泄露
		subtle.ConstantTimeCompare([]byte(a), []byte(b))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// errNotFound 统一 404 错误
func errNotFound() error {
	return errs.New(errs.NOT_FOUND, "接口不存在")
}

// 审计便捷:开始时间
func now() time.Time { return time.Now() }
