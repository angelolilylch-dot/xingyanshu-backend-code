package evidence

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"xingyanshu-backend/internal/platform/httpx"
)

// RegisterCallbackRoute 注册存证服务专用回调入口；该入口不使用用户 JWT，只接受已验签的服务商通知。
func RegisterCallbackRoute(group *gin.RouterGroup, service *Service) {
	group.POST("/integrations/evidence/callbacks", func(c *gin.Context) {
		body, err := c.GetRawData()
		if err != nil || !service.VerifyCallback(body, c.GetHeader("X-Evidence-Timestamp"), c.GetHeader("X-Evidence-Signature")) {
			httpx.AbortWithError(c, httpx.NewError(http.StatusUnauthorized, "EXTERNAL_EVIDENCE_CALLBACK_INVALID", "存证回调验签失败"))
			return
		}
		var callback Callback
		if err := json.Unmarshal(body, &callback); err != nil {
			httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "存证回调格式错误"))
			return
		}
		code, err := service.ApplyCallback(c.Request.Context(), callback)
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "处理存证回调失败"))
			return
		}
		if code != "" {
			httpx.AbortWithError(c, httpx.NewValidationError(code, "存证回调字段错误"))
			return
		}
		httpx.Success(c, http.StatusOK, gin.H{})
	})
}
