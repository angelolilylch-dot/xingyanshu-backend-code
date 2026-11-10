package evidence

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"xingyanshu-backend/internal/modules/creators"
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

// RegisterCreatorRoutes 提供创作者端的受控证书查询；仅允许查询其授权合同或二创作品的存证。
func RegisterCreatorRoutes(group *gin.RouterGroup, service *Service, auth gin.HandlerFunc) {
	r := group.Group("/creator/evidences", auth)
	r.GET("/:evidenceId/certificate", func(c *gin.Context) {
		evidenceID, err := strconv.ParseUint(c.Param("evidenceId"), 10, 64)
		if err != nil || evidenceID == 0 {
			httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "存证编号格式错误", httpx.FieldError{Field: "evidenceId", Message: "必须为正整数"}))
			return
		}
		current, ok := creators.CurrentCreatorFromContext(c)
		if !ok {
			httpx.AbortWithError(c, httpx.NewError(http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED", "登录状态已失效"))
			return
		}
		data, code, serviceErr := service.CertificateForCreator(c.Request.Context(), current.CreatorID, evidenceID)
		if serviceErr != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "查询存证证书失败"))
			return
		}
		if code == "VALIDATION_FORMAT_INVALID" {
			httpx.AbortWithError(c, httpx.NewValidationError(code, "存证编号格式错误"))
			return
		}
		if code == "RESOURCE_NOT_FOUND" {
			httpx.AbortWithError(c, httpx.NewError(http.StatusNotFound, code, "存证记录不存在或无权访问"))
			return
		}
		if code != "" {
			httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "查询存证证书失败"))
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
}
