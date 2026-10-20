package identity

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/platform/httpx"
)

// RegisterConsumerRoutes 注册消费者 H5 专用认证接口；公开商品/活动浏览不依赖本路由。
func RegisterConsumerRoutes(group *gin.RouterGroup, login *LoginService, middleware gin.HandlerFunc) {
	consumer := group.Group("/consumer")
	consumer.POST("/auth/sms/send", func(c *gin.Context) {
		var request ConsumerSMSRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		data, code, err := login.SendConsumerSMS(c.Request.Context(), request.Mobile, c.ClientIP())
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusServiceUnavailable, code, "短信服务暂不可用"))
			return
		}
		if code != "" {
			httpx.AbortWithError(c, httpx.NewError(http.StatusUnprocessableEntity, code, "短信发送失败"))
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	consumer.POST("/auth/login", func(c *gin.Context) {
		var request ConsumerLoginRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		data, code, err := login.ConsumerLogin(c.Request.Context(), request)
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, code, "消费者登录失败"))
			return
		}
		if code != "" {
			httpx.AbortWithError(c, httpx.NewError(http.StatusUnprocessableEntity, code, "消费者登录信息不正确"))
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	consumer.POST("/auth/refresh", func(c *gin.Context) {
		var request RefreshRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		data, code, err := login.RefreshConsumer(c.Request.Context(), request)
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, code, "刷新登录状态失败"))
			return
		}
		if code != "" {
			httpx.AbortWithError(c, httpx.NewError(http.StatusUnauthorized, code, "登录状态已失效"))
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	protected := consumer.Group("", middleware)
	protected.POST("/auth/logout", func(c *gin.Context) {
		current, _ := CurrentConsumerFromContext(c)
		if err := login.ConsumerLogout(c.Request.Context(), current); err != nil {
			rejectConsumer(c)
			return
		}
		httpx.Success(c, http.StatusOK, gin.H{})
	})
	protected.GET("/me", func(c *gin.Context) {
		current, _ := CurrentConsumerFromContext(c)
		data, err := login.ConsumerMe(c.Request.Context(), current.ConsumerID)
		if err != nil {
			rejectConsumer(c)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
}
