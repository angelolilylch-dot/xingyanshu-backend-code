package files

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
)

// RegisterProtectedRoutes 注册后台上传、元数据读取和临时下载地址接口。
func RegisterProtectedRoutes(group *gin.RouterGroup, service *Service, middleware gin.HandlerFunc) {
	protected := group.Group("", middleware)
	protected.POST("/files", func(c *gin.Context) {
		// 额外预留 1MB multipart 边界，防止超大文件在解析前长期占用服务端资源。
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.MaxUploadBytes()+1024*1024)
		header, err := c.FormFile("file")
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				httpx.AbortWithError(c, httpx.NewError(http.StatusRequestEntityTooLarge, "FILE_SIZE_EXCEEDED", "文件大小超出当前限制"))
				return
			}
			httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_REQUIRED", "请选择需要上传的文件", httpx.FieldError{Field: "file", Message: "不能为空"}))
			return
		}
		source, err := header.Open()
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, "FILE_CONTENT_INVALID", "文件无法读取"))
			return
		}
		defer source.Close()
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.Upload(c.Request.Context(), uploadOperator{ID: current.UserID, RoleCode: firstRole(current), ClientIP: c.ClientIP()}, header, source)
		writeResponse(c, http.StatusCreated, data, code, err)
	})
	protected.GET("/files/:fileId", func(c *gin.Context) {
		fileID, ok := parseFileID(c)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.Get(c.Request.Context(), current, fileID)
		writeResponse(c, http.StatusOK, data, code, err)
	})
	protected.POST("/files/:fileId/download-url", func(c *gin.Context) {
		fileID, ok := parseFileID(c)
		if !ok {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.CreateDownloadURL(c.Request.Context(), current, c.ClientIP(), fileID)
		writeResponse(c, http.StatusOK, data, code, err)
	})
}

func parseFileID(c *gin.Context) (uint64, bool) {
	value, err := strconv.ParseUint(c.Param("fileId"), 10, 64)
	if err != nil || value == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "文件 ID 格式不正确", httpx.FieldError{Field: "fileId", Message: "必须为正整数"}))
		return 0, false
	}
	return value, true
}

func writeResponse(c *gin.Context, status int, data map[string]any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "文件服务暂不可用"))
		return
	}
	switch code {
	case "":
		httpx.Success(c, status, data)
	case "RESOURCE_NOT_FOUND":
		httpx.AbortWithError(c, httpx.NewErrorWithData(http.StatusNotFound, "FILE_NOT_FOUND", "文件不存在或不可读取", map[string]any{"resourceType": "FILE", "field": "fileId"}))
	case "AUTH_FORBIDDEN":
		httpx.AbortWithError(c, httpx.NewError(http.StatusForbidden, code, "无权访问该文件"))
	case "FILE_SIZE_EXCEEDED":
		httpx.AbortWithError(c, httpx.NewError(http.StatusRequestEntityTooLarge, code, "文件大小超出当前限制"))
	case "FILE_TYPE_NOT_ALLOWED", "FILE_CONTENT_INVALID", "VALIDATION_FORMAT_INVALID", "VALIDATION_REQUIRED":
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, code, "文件格式或内容不符合要求"))
	case "RESOURCE_CONFLICT":
		httpx.AbortWithError(c, httpx.NewError(http.StatusConflict, code, "文件记录创建冲突"))
	default:
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadGateway, code, "文件存储服务暂不可用"))
	}
}
