package httpx

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/platform/logx"
)

const maxRequestIDLength = 128

// RequestIDMiddleware 透传合法请求号或生成新请求号，并同时回写响应头。
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := sanitizeRequestID(c.GetHeader("X-Request-Id"))
		if requestID == "" {
			requestID = newRequestID()
		}
		c.Set(requestIDKey, requestID)
		c.Request = c.Request.WithContext(logx.WithRequestID(c.Request.Context(), requestID))
		c.Header("X-Request-Id", requestID)
		c.Next()
	}
}

// DiagnosticRequestMiddleware retains test-only API request bodies so errors
// can be diagnosed without copying uploaded binary files into text logs.
func DiagnosticRequestMiddleware(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if enabled && shouldCaptureBody(c.GetHeader("Content-Type")) && c.Request.Body != nil {
			body, err := io.ReadAll(c.Request.Body)
			if err != nil {
				logx.Errorf("request_id=%q stage=http.read_request_body error_type=%T error=%q", RequestID(c), err, err)
			} else {
				c.Set("diagnostic_request_body", logx.Redact(string(body)))
				c.Request.Body = io.NopCloser(bytes.NewReader(body))
			}
		}
		c.Next()
	}
}

// AccessLogMiddleware creates one concise access line per request. Query
// strings are intentionally omitted because they can contain tokens.
func AccessLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		logx.Accessf("request_id=%q method=%q path=%q status=%d latency_ms=%d", RequestID(c), c.Request.Method, c.Request.URL.Path, c.Writer.Status(), time.Since(started).Milliseconds())
	}
}

// RecoveryMiddleware 将未捕获 panic 统一转换为不泄露内部细节的 JSON 错误。
func RecoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logx.Errorf("request_id=%q stage=http.panic panic=%q stack=%q", RequestID(c), fmt.Sprint(recovered), string(debug.Stack()))
				AbortWithError(c, NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "服务暂时不可用"))
			}
		}()
		c.Next()
	}
}

func shouldCaptureBody(contentType string) bool {
	contentType = strings.ToLower(contentType)
	return strings.Contains(contentType, "application/json") || strings.Contains(contentType, "application/x-www-form-urlencoded") || strings.Contains(contentType, "text/")
}

func sanitizeRequestID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > maxRequestIDLength {
		return ""
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.') {
			return ""
		}
	}
	return value
}

func newRequestID() string {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		// 加密随机源不可用时仍需提供链路编号，时间戳保证同一进程内具有足够区分度。
		return "req_" + time.Now().Format("20060102150405.000000000")
	}
	return "req_" + time.Now().Format("20060102") + "_" + hex.EncodeToString(bytes)
}
