// Package httpx 提供 HTTP 接口共用的响应、错误和分页处理。
package httpx

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const requestIDKey = "request_id"

// Response 是所有 JSON 接口的统一外层响应结构。
type Response struct {
	Code        string       `json:"code"`
	Message     string       `json:"message"`
	Data        any          `json:"data,omitempty"`
	RequestID   string       `json:"requestId"`
	Timestamp   string       `json:"timestamp"`
	FieldErrors []FieldError `json:"fieldErrors,omitempty"`
}

// FieldError 表示可以由前端定位到具体输入控件的校验错误。
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// AppError 是服务层向 HTTP 层传递的受控业务错误。
type AppError struct {
	Status      int
	Code        string
	Message     string
	Data        any
	FieldErrors []FieldError
}

func (e *AppError) Error() string {
	return e.Code + ": " + e.Message
}

// NewError 创建一个不包含字段级错误的标准业务错误。
func NewError(status int, code, message string) *AppError {
	return &AppError{Status: status, Code: code, Message: message}
}

// NewValidationError 创建参数校验错误，供绑定器和业务字段校验共用。
func NewValidationError(code, message string, fieldErrors ...FieldError) *AppError {
	return &AppError{Status: http.StatusBadRequest, Code: code, Message: message, FieldErrors: fieldErrors}
}

// Success 写入成功响应。所有成功接口都通过这里保持响应字段一致。
func Success(c *gin.Context, status int, data any) {
	c.JSON(status, Response{Code: "OK", Message: "成功", Data: data, RequestID: RequestID(c), Timestamp: now()})
}

// Error 写入失败响应。错误码由业务代码决定，HTTP 状态只表达协议层分类。
func Error(c *gin.Context, appErr *AppError) {
	status := appErr.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	c.JSON(status, Response{Code: appErr.Code, Message: appErr.Message, Data: appErr.Data, RequestID: RequestID(c), Timestamp: now(), FieldErrors: appErr.FieldErrors})
}

// AbortWithError 终止当前处理链并输出统一错误响应。
func AbortWithError(c *gin.Context, appErr *AppError) {
	c.Abort()
	Error(c, appErr)
}

// RequestID 取得请求链路编号；中间件已写入时直接复用。
func RequestID(c *gin.Context) string {
	if value, exists := c.Get(requestIDKey); exists {
		if requestID, ok := value.(string); ok && requestID != "" {
			return requestID
		}
	}
	return c.GetHeader("X-Request-Id")
}

func now() string {
	return time.Now().In(time.FixedZone("+08:00", 8*60*60)).Format(time.RFC3339Nano)
}
