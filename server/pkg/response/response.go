// Package response 提供 autoShip Server 统一的 Gin JSON 响应信封封装，并关联请求级诊断编号与审计错误摘要。
package response

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	// ContextKeyRequestID 是请求追踪中间件注入到 Gin Context 的唯一请求编号键名。
	ContextKeyRequestID = "autoship.request_id"
	// ContextKeyAuditError 是失败响应向审计中间件传递客户端可见错误文案的键名。
	ContextKeyAuditError = "autoship.audit_error"
)

// Envelope 是所有管理端与 Agent JSON 接口统一返回的顶层信封结构（Code=0 表示成功，非 0 表示业务或鉴权错误）。
type Envelope struct {
	RequestID string `json:"requestId,omitempty"` // 关联诊断请求 ID
	Code      int    `json:"code"`                // 业务状态码（0 为成功）
	Message   string `json:"message"`             // 客户端可见提示信息
	Data      any    `json:"data,omitempty"`      // 响应业务数据载荷
}

// PageResult 封装标准分页列表数据结构。
type PageResult struct {
	Total    int64 `json:"total"`    // 满足筛选条件的总记录数
	Page     int   `json:"page"`     // 当前页码（从 1 开始）
	PageSize int   `json:"pageSize"` // 每页条数
	List     any   `json:"list"`     // 当前页数据列表
}

// RequestID 从 Gin 上下文读取当前请求的追踪编号。
// 参数 c 为 Gin 请求上下文；返回请求 ID 字符串，不存在时返回空串。
func RequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(ContextKeyRequestID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// AuditError 从 Gin 上下文提取本次请求最近一次返回的公开错误文案，供审计模块写入日志。
// 参数 c 为 Gin 请求上下文；返回错误摘要字符串，成功请求返回空串。
func AuditError(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(ContextKeyAuditError); ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// OK 输出 HTTP 200 及 Code=0 的成功 JSON 响应。
// 参数 c 为 Gin 上下文，data 为业务返回对象（可为 nil）；直接写入响应流。
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{
		RequestID: RequestID(c),
		Code:      0,
		Message:   "success",
		Data:      data,
	})
}

// OKPage 输出带分页元信息的成功 JSON 响应。
// 参数 c 为 Gin 上下文，total 为总行数，page/pageSize 为分页参数，list 为列表切片；直接写入响应流。
func OKPage(c *gin.Context, total int64, page, pageSize int, list any) {
	OK(c, PageResult{
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		List:     list,
	})
}

// Fail 输出指定 HTTP 状态码、业务码与错误提示的失败 JSON 响应，并将可见文案记录到审计上下文。
// 参数 c 为 Gin 上下文，httpStatus 为 HTTP 状态码，code 为业务错误码，msg 为安全提示文案；直接写入响应流。
func Fail(c *gin.Context, httpStatus, code int, msg string) {
	if c != nil {
		c.Set(ContextKeyAuditError, strings.TrimSpace(msg))
	}
	c.JSON(httpStatus, Envelope{
		RequestID: RequestID(c),
		Code:      code,
		Message:   msg,
	})
}

// FailWith 输出附带结构化诊断或引导数据（如 otpRequired）的失败 JSON 响应。
// 参数 c 为 Gin 上下文，httpStatus 为 HTTP 状态码，code 为业务错误码，msg 为提示文案，data 为附加载荷；直接写入响应流。
func FailWith(c *gin.Context, httpStatus, code int, msg string, data any) {
	if c != nil {
		c.Set(ContextKeyAuditError, strings.TrimSpace(msg))
	}
	c.JSON(httpStatus, Envelope{
		RequestID: RequestID(c),
		Code:      code,
		Message:   msg,
		Data:      data,
	})
}

// BadRequest 输出 HTTP 400 参数校验失败响应。
// 参数 c 为 Gin 上下文，msg 为错误说明；直接写入响应流。
func BadRequest(c *gin.Context, msg string) {
	Fail(c, http.StatusBadRequest, 400, msg)
}

// Unauthorized 输出 HTTP 401 未登录或签名失效响应。
// 参数 c 为 Gin 上下文，msg 为错误说明；直接写入响应流。
func Unauthorized(c *gin.Context, msg string) {
	Fail(c, http.StatusUnauthorized, 401, msg)
}

// Forbidden 输出 HTTP 403 权限不足、IP 不在白名单或 CSRF 校验失败响应。
// 参数 c 为 Gin 上下文，msg 为错误说明；直接写入响应流。
func Forbidden(c *gin.Context, msg string) {
	Fail(c, http.StatusForbidden, 403, msg)
}

// NotFound 输出 HTTP 404 资源不存在响应。
// 参数 c 为 Gin 上下文，msg 为错误说明；直接写入响应流。
func NotFound(c *gin.Context, msg string) {
	Fail(c, http.StatusNotFound, 404, msg)
}

// TooManyRequests 输出 HTTP 429 触发限流响应。
// 参数 c 为 Gin 上下文，msg 为错误说明；直接写入响应流。
func TooManyRequests(c *gin.Context, msg string) {
	Fail(c, http.StatusTooManyRequests, 429, msg)
}

// ServiceUnavailable 输出 HTTP 503 服务或依赖暂未就绪响应。
// 参数 c 为 Gin 上下文，msg 为错误提示；直接写入响应流。
func ServiceUnavailable(c *gin.Context, msg string) {
	Fail(c, http.StatusServiceUnavailable, 503, msg)
}

// ServerError 输出 HTTP 500 内部服务异常响应（调用方只传脱敏提示，不传底层堆栈或 SQL 详情）。
// 参数 c 为 Gin 上下文，msg 为客户端可见错误提示；直接写入响应流。
func ServerError(c *gin.Context, msg string) {
	Fail(c, http.StatusInternalServerError, 500, msg)
}
