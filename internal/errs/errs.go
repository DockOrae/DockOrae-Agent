// Package errs 统一错误体系(与 Skill §45 对齐)。
// 每个错误包含稳定错误码 + 人类可读消息 + 建议 HTTP 状态。
package errs

import (
	"errors"
	"fmt"
	"net/http"
)

// 通用错误码
const (
	INVALID_REQUEST       = "INVALID_REQUEST"
	UNAUTHORIZED          = "UNAUTHORIZED"
	PERMISSION_DENIED     = "PERMISSION_DENIED"
	OPERATION_IN_PROGRESS = "OPERATION_IN_PROGRESS"
	NOT_FOUND             = "NOT_FOUND"
	CONFLICT              = "CONFLICT"
	INTERNAL              = "INTERNAL"
	UNSUPPORTED           = "UNSUPPORTED"
	TIMEOUT               = "TIMEOUT"
	EXEC_FAILED           = "EXEC_FAILED"
	AGENT_UNAVAILABLE     = "AGENT_UNAVAILABLE"
	INVALID_CONFIRM       = "INVALID_CONFIRM" // 危险操作缺少/错误确认
)

// Swap 错误码
const (
	SWAP_INVALID_SIZE      = "SWAP_INVALID_SIZE"
	SWAP_INSUFFICIENT_DISK = "SWAP_INSUFFICIENT_DISK"
	SWAP_CREATE_FAILED     = "SWAP_CREATE_FAILED"
	SWAP_RESIZE_FAILED     = "SWAP_RESIZE_FAILED"
	SWAP_DELETE_FAILED     = "SWAP_DELETE_FAILED"
)

// Update 错误码
const (
	UPDATE_DOWNLOAD_FAILED    = "UPDATE_DOWNLOAD_FAILED"
	UPDATE_CHECKSUM_FAILED    = "UPDATE_CHECKSUM_FAILED"
	UPDATE_INSTALL_FAILED     = "UPDATE_INSTALL_FAILED"
	UPDATE_HEALTHCHECK_FAILED = "UPDATE_HEALTHCHECK_FAILED"
	UPDATE_ROLLBACK_FAILED    = "UPDATE_ROLLBACK_FAILED"
)

// Compose 错误码
const (
	COMPOSE_PROJECT_NOT_FOUND  = "COMPOSE_PROJECT_NOT_FOUND"
	COMPOSE_PULL_FAILED        = "COMPOSE_PULL_FAILED"
	COMPOSE_UPDATE_FAILED      = "COMPOSE_UPDATE_FAILED"
	COMPOSE_HEALTHCHECK_FAILED = "COMPOSE_HEALTHCHECK_FAILED"
	COMPOSE_ROLLBACK_FAILED    = "COMPOSE_ROLLBACK_FAILED"
)

// 其余错误码
const (
	DOCKER_UNAVAILABLE       = "DOCKER_UNAVAILABLE"
	DOCKER_ERROR             = "DOCKER_ERROR"
	CONTAINER_ERROR          = "CONTAINER_ERROR"
	IMAGE_ERROR              = "IMAGE_ERROR"
	NETWORK_ERROR            = "NETWORK_ERROR"
	VOLUME_ERROR             = "VOLUME_ERROR"
	COMPOSE_ERROR            = "COMPOSE_ERROR"
	HOST_ERROR               = "HOST_ERROR"
	SYSTEM_ERROR             = "SYSTEM_ERROR"
	DISK_ERROR               = "DISK_ERROR"
	SWAP_ERROR               = "SWAP_ERROR"
	VALIDATION_ERROR         = "VALIDATION_ERROR"
	SYSTEM_UPDATE_FAILED     = "SYSTEM_UPDATE_FAILED"
	NETWORK_OPERATION_FAILED = "NETWORK_OPERATION_FAILED"
)

// 宿主文件 / 终端错误码(§55 宿主文件管理器)
const (
	PATH_INVALID      = "PATH_INVALID"      // 路径非法/穿越
	DANGEROUS_PATH    = "DANGEROUS_PATH"    // 危险系统目录,禁止该操作
	FILE_EXISTS       = "FILE_EXISTS"       // 目标已存在
	FILE_NOT_FOUND    = "FILE_NOT_FOUND"    // 文件/目录不存在
	FILE_TOO_LARGE    = "FILE_TOO_LARGE"    // 超出大小限制
	UNSUPPORTED_ARCH  = "UNSUPPORTED_ARCH"  // 不支持的压缩格式
	PTY_UNAVAILABLE   = "PTY_UNAVAILABLE"   // PTY 不可用(仅 Linux 宿主支持)
	TERMINAL_SESSION  = "TERMINAL_SESSION"  // 终端会话错误
	ARCHIVE_LIMIT     = "ARCHIVE_LIMIT"     // 压缩/解压资源超限(§20)
	COPY_LIMIT        = "COPY_LIMIT"        // 复制资源超限(§20)
	TERMINAL_LIMIT    = "TERMINAL_LIMIT"    // 终端会话数/速率超限(§20)
	UPLOAD_LIMIT      = "UPLOAD_LIMIT"      // 上传大小超限(§20)
)

// Error Agent 统一错误
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *Error) Error() string { return fmt.Sprintf("[%s] %s", e.Code, e.Message) }

// New 构造错误(自动推导 HTTP 状态)
func New(code, msg string) *Error {
	return &Error{Code: code, Message: msg, Status: StatusFor(code)}
}

// Newf 带格式化
func Newf(code, format string, args ...any) *Error {
	return New(code, fmt.Sprintf(format, args...))
}

// WithStatus 覆盖 HTTP 状态
func (e *Error) WithStatus(s int) *Error { e.Status = s; return e }

// StatusFor 错误码 → 建议 HTTP 状态
func StatusFor(code string) int {
	switch code {
	case INVALID_REQUEST, SWAP_INVALID_SIZE, INVALID_CONFIRM, PATH_INVALID, FILE_TOO_LARGE, UNSUPPORTED_ARCH, ARCHIVE_LIMIT, COPY_LIMIT, UPLOAD_LIMIT:
		return http.StatusBadRequest
	case UNAUTHORIZED:
		return http.StatusUnauthorized
	case PERMISSION_DENIED, DANGEROUS_PATH:
		return http.StatusForbidden
	case NOT_FOUND, COMPOSE_PROJECT_NOT_FOUND, FILE_NOT_FOUND:
		return http.StatusNotFound
	case CONFLICT, OPERATION_IN_PROGRESS, FILE_EXISTS:
		return http.StatusConflict
	case AGENT_UNAVAILABLE, DOCKER_UNAVAILABLE, PTY_UNAVAILABLE, TERMINAL_SESSION:
		return http.StatusBadGateway
	case TERMINAL_LIMIT:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// FromError 包装普通错误(未知错误码统一 INTERNAL)
func FromError(err error) *Error {
	if err == nil {
		return nil
	}
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	return New(INTERNAL, err.Error())
}
