package httpx

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
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
		c.Header("X-Request-Id", requestID)
		c.Next()
	}
}

// RecoveryMiddleware 将未捕获 panic 统一转换为不泄露内部细节的 JSON 错误。
func RecoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recover() != nil {
				AbortWithError(c, NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "服务暂时不可用"))
			}
		}()
		c.Next()
	}
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
