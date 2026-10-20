package foundation

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
)

// RegisterPublicRoutes 注册无需登录的只读主数据接口。固定白名单字典只包含前端展示所需枚举，
// 不包含后台控制项，因此创作者、消费者和未登录页面均可读取启用项。
func RegisterPublicRoutes(group *gin.RouterGroup, service *Service) {
	group.GET("/public/regions", func(c *gin.Context) {
		data, code, err := service.ListRegions(c.Request.Context(), c.Query("keyword"), c.Query("level"), c.Query("parentCode"))
		writeResponse(c, http.StatusOK, data, code, err)
	})
	group.GET("/dictionaries/:dictType", func(c *gin.Context) {
		enabledOnly := c.DefaultQuery("enabledOnly", "true") != "false"
		data, code, err := service.ListDictionary(c.Request.Context(), c.Param("dictType"), enabledOnly)
		writeResponse(c, http.StatusOK, data, code, err)
	})
}

// RegisterProtectedRoutes 注册字典查询和平台管理员维护接口。
func RegisterProtectedRoutes(group *gin.RouterGroup, service *Service, middleware gin.HandlerFunc) {
	protected := group.Group("", middleware)
	protected.GET("/tags", func(c *gin.Context) {
		data, code, err := service.ListTags(c.Request.Context(), c.Query("tagType"), c.Query("keyword"), false)
		writeResponse(c, http.StatusOK, data, code, err)
	})

	admin := protected.Group("", identity.RequireAnyPermission(adminConfigPermission))
	admin.GET("/admin/tags", func(c *gin.Context) {
		data, code, err := service.ListTags(c.Request.Context(), c.Query("tagType"), c.Query("keyword"), true)
		writeResponse(c, http.StatusOK, data, code, err)
	})
	admin.POST("/admin/tags", func(c *gin.Context) {
		var request TagCreateRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.CreateTag(c.Request.Context(), operator(current, c.ClientIP()), request)
		writeResponse(c, http.StatusCreated, data, code, err)
	})
	admin.PATCH("/admin/tags/:tagId/status", func(c *gin.Context) {
		id, version, ok := parseIDAndVersion(c, "tagId")
		if !ok {
			return
		}
		var request EnabledRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.ChangeTagStatus(c.Request.Context(), operator(current, c.ClientIP()), id, version, request)
		writeResponse(c, http.StatusOK, data, code, err)
	})
	admin.GET("/admin/regions", func(c *gin.Context) {
		data, err := service.ListRegionsForAdmin(c.Request.Context(), c.Query("keyword"))
		if err != nil {
			writeResponse(c, http.StatusOK, nil, "INTERNAL_ERROR", err)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.POST("/admin/regions", func(c *gin.Context) {
		var request RegionCreateRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.CreateRegion(c.Request.Context(), operator(current, c.ClientIP()), request)
		writeResponse(c, http.StatusCreated, data, code, err)
	})
	admin.PATCH("/admin/regions/:regionId/status", func(c *gin.Context) {
		id, version, ok := parseIDAndVersion(c, "regionId")
		if !ok {
			return
		}
		var request EnabledRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.ChangeRegionStatus(c.Request.Context(), operator(current, c.ClientIP()), id, version, request)
		writeResponse(c, http.StatusOK, data, code, err)
	})
	admin.GET("/admin/configs", func(c *gin.Context) {
		data, err := service.ListConfigs(c.Request.Context())
		if err != nil {
			writeResponse(c, http.StatusOK, nil, "INTERNAL_ERROR", err)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.PATCH("/admin/configs/:configKey", func(c *gin.Context) {
		version, ok := httpx.MustParseIfMatchVersion(c)
		if !ok {
			return
		}
		var request ConfigUpdateRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.UpdateConfig(c.Request.Context(), operator(current, c.ClientIP()), c.Param("configKey"), version, request)
		writeResponse(c, http.StatusOK, data, code, err)
	})
	admin.GET("/admin/after-sale-templates", func(c *gin.Context) {
		data, code, err := service.ListTemplates(c.Request.Context(), c.Query("bizType"), c.Query("enabledOnly") == "true")
		writeResponse(c, http.StatusOK, data, code, err)
	})
	admin.POST("/admin/after-sale-templates", func(c *gin.Context) {
		var request TemplateCreateRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.CreateTemplate(c.Request.Context(), operator(current, c.ClientIP()), request)
		writeResponse(c, http.StatusCreated, data, code, err)
	})
	admin.POST("/admin/after-sale-templates/:templateId/versions", func(c *gin.Context) {
		id, version, ok := parseIDAndVersion(c, "templateId")
		if !ok {
			return
		}
		var request TemplateVersionRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.CreateTemplateVersion(c.Request.Context(), operator(current, c.ClientIP()), id, version, request)
		writeResponse(c, http.StatusCreated, data, code, err)
	})
	admin.PATCH("/admin/after-sale-templates/:templateId/status", func(c *gin.Context) {
		id, version, ok := parseIDAndVersion(c, "templateId")
		if !ok {
			return
		}
		var request EnabledRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, code, err := service.ChangeTemplateStatus(c.Request.Context(), operator(current, c.ClientIP()), id, version, request)
		writeResponse(c, http.StatusOK, data, code, err)
	})
}

func operator(current identity.CurrentAdmin, clientIP string) Operator {
	role := ""
	if len(current.RoleCodes) > 0 {
		role = current.RoleCodes[0]
	}
	return Operator{ID: current.UserID, RoleCode: role, ClientIP: clientIP}
}
func parseIDAndVersion(c *gin.Context, field string) (uint64, uint32, bool) {
	id, err := strconv.ParseUint(c.Param(field), 10, 64)
	if err != nil || id == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "资源 ID 格式不正确", httpx.FieldError{Field: field, Message: "必须为正整数"}))
		return 0, 0, false
	}
	version, ok := httpx.MustParseIfMatchVersion(c)
	return id, version, ok
}
func writeResponse(c *gin.Context, successStatus int, data map[string]any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "基础数据服务暂不可用"))
		return
	}
	switch code {
	case "":
		httpx.Success(c, successStatus, data)
	case "RESOURCE_NOT_FOUND":
		path := c.FullPath()
		resource, field, message := "FOUNDATION_RESOURCE", "id", "基础资源不存在"
		if strings.Contains(path, "regions") {
			resource, field, message = "REGION", "regionId", "地区不存在"
		} else if strings.Contains(path, "tags") {
			resource, field, message = "TAG", "tagId", "标签不存在"
		} else if strings.Contains(path, "configs") {
			resource, field, message = "SYSTEM_CONFIG", "configKey", "平台配置不存在"
		} else if strings.Contains(path, "after-sale-templates") {
			resource, field, message = "AFTER_SALE_TEMPLATE", "templateId", "售后模板不存在"
		}
		httpx.AbortWithError(c, httpx.NewErrorWithData(http.StatusNotFound, resource+"_NOT_FOUND", message, map[string]any{"resourceType": resource, "field": field}))
	case "RESOURCE_CONFLICT", "VERSION_CONFLICT":
		httpx.AbortWithError(c, httpx.NewError(http.StatusConflict, code, "数据已变更或重复，请刷新后重试"))
	case "VALIDATION_FORMAT_INVALID":
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, code, "请求数据格式不正确"))
	default:
		httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, code, "基础数据服务暂不可用"))
	}
}
