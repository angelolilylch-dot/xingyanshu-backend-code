package authorizations

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/platform/httpx"
)

func RegisterRoutes(group *gin.RouterGroup, service *Service, auth gin.HandlerFunc) {
	r := group.Group("/creator/authorizations", auth)
	r.GET("", func(c *gin.Context) {
		pageNo, _ := strconv.Atoi(c.DefaultQuery("pageNo", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
		current, _ := creators.CurrentCreatorFromContext(c)
		data, code, err := service.List(c.Request.Context(), current, c.Query("status"), c.Query("ipId"), pageNo, pageSize)
		respond(c, data, code, err)
	})
	r.GET("/:authorizationId", func(c *gin.Context) {
		id, ok := authorizationID(c)
		if !ok {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(c)
		data, code, err := service.Get(c.Request.Context(), current, id)
		respond(c, data, code, err)
	})
	r.GET("/:authorizationId/contract", func(c *gin.Context) {
		id, ok := authorizationID(c)
		if !ok {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(c)
		data, code, err := service.ContractDownload(c.Request.Context(), current, id, c.ClientIP())
		respond(c, data, code, err)
	})
	r.GET("/:authorizationId/certificate", func(c *gin.Context) {
		id, ok := authorizationID(c)
		if !ok {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(c)
		data, code, err := service.CertificateDownload(c.Request.Context(), current, id, c.ClientIP())
		respond(c, data, code, err)
	})
	r.GET("/:authorizationId/material-packs", func(c *gin.Context) {
		id, ok := authorizationID(c)
		if !ok {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(c)
		data, code, err := service.MaterialPacks(c.Request.Context(), current, id)
		respond(c, data, code, err)
	})
	r.POST("/:authorizationId/material-downloads", func(c *gin.Context) {
		id, ok := authorizationID(c)
		if !ok {
			return
		}
		var input MaterialDownloadInput
		if !httpx.BindJSON(c, &input) {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(c)
		data, code, err := service.MaterialDownload(c.Request.Context(), current, id, input, c.ClientIP())
		respond(c, data, code, err)
	})
}

func authorizationID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("authorizationId"), 10, 64)
	if err != nil || id == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "授权编号格式错误", httpx.FieldError{Field: "authorizationId", Message: "必须为正整数"}))
		return 0, false
	}
	return id, true
}

func respond(c *gin.Context, data map[string]any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "授权记录处理失败"))
		return
	}
	switch code {
	case "":
		httpx.Success(c, http.StatusOK, data)
	case "AUTHORIZATION_NOT_FOUND":
		httpx.AbortWithError(c, httpx.NewErrorWithData(http.StatusNotFound, code, "授权记录不存在", map[string]any{"resourceType": "AUTHORIZATION", "field": "authorizationId"}))
	case "CONTRACT_NOT_READY":
		httpx.AbortWithError(c, httpx.NewErrorWithData(http.StatusConflict, code, "正式合同正在生成，请稍后刷新", data))
	case "CERTIFICATE_NOT_READY":
		httpx.AbortWithError(c, httpx.NewErrorWithData(http.StatusConflict, code, "授权凭证正在生成，请稍后刷新", data))
	case "AUTHORIZATION_EXPIRED":
		httpx.AbortWithError(c, httpx.NewErrorWithData(http.StatusForbidden, code, "授权已到期或已解除，不能下载素材", data))
	case "MATERIAL_ITEM_NOT_FOUND":
		httpx.AbortWithError(c, httpx.NewErrorWithData(http.StatusNotFound, code, "素材项不存在或不属于该授权IP", map[string]any{"resourceType": "MATERIAL_ITEM", "field": "materialItemId"}))
	case "MATERIAL_ITEM_NOT_DELIVERABLE":
		httpx.AbortWithError(c, httpx.NewError(http.StatusForbidden, code, "该素材不在授权交付范围内"))
	case "MATERIAL_PACK_NOT_FROZEN", "MATERIAL_PACK_EVIDENCE_PENDING":
		httpx.AbortWithError(c, httpx.NewError(http.StatusConflict, code, "素材包尚未完成确权冻结或存证，暂不能下载"))
	case "MATERIAL_PACK_SNAPSHOT_MISMATCH":
		httpx.AbortWithError(c, httpx.NewError(http.StatusConflict, code, "授权订单引用的素材包版本不可用，暂不能下载"))
	case "RESOURCE_NOT_FOUND":
		httpx.AbortWithError(c, httpx.NewErrorWithData(http.StatusNotFound, "FILE_NOT_FOUND", "合同或凭证文件不存在", map[string]any{"resourceType": "FILE"}))
	case "FILE_DOWNLOAD_URL_FAILED":
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadGateway, code, "合同或凭证下载链接生成失败"))
	case "VALIDATION_FORMAT_INVALID":
		httpx.AbortWithError(c, httpx.NewErrorWithData(http.StatusBadRequest, code, "请求参数格式不正确", data))
	default:
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, code, "授权记录请求失败"))
	}
}
