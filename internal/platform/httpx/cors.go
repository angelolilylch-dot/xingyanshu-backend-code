package httpx

import (
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/config"
)

// CORSMiddleware 按环境配置处理浏览器预检和跨域响应；测试环境可使用通配符，生产环境仅允许精确来源。
func CORSMiddleware(cfg config.CORSConfig) gin.HandlerFunc {
	if len(cfg.AllowedOrigins) == 0 {
		return func(c *gin.Context) { c.Next() }
	}
	allowAllOrigins := len(cfg.AllowedOrigins) == 1 && cfg.AllowedOrigins[0] == "*"
	options := cors.Config{
		AllowMethods:     []string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type", "X-Request-Id", "Idempotency-Key", "If-Match-Version"},
		ExposeHeaders:    []string{"X-Request-Id"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}
	if allowAllOrigins {
		options.AllowAllOrigins = true
	} else {
		options.AllowOrigins = cfg.AllowedOrigins
	}
	return cors.New(options)
}
