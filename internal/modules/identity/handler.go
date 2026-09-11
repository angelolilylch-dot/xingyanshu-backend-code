package identity

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"xingyanshu-backend/internal/platform/httpx"
)

// RegisterPublicRoutes 注册无需登录即可访问的认证基础接口。
func RegisterPublicRoutes(group *gin.RouterGroup, captcha *CaptchaService, login *LoginService) {
	group.GET("/auth/captcha", func(c *gin.Context) {
		response, err := captcha.Create(c.Request.Context())
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "CAPTCHA_UNAVAILABLE", "验证码服务暂不可用"))
			return
		}
		httpx.Success(c, http.StatusOK, response)
	})
	group.POST("/auth/refresh", func(c *gin.Context) {
		var request RefreshRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		data, code, err := login.Refresh(c.Request.Context(), request)
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
}

// RegisterProtectedRoutes 注册已经完成后台登录校验的认证接口。
func RegisterProtectedRoutes(group *gin.RouterGroup, login *LoginService, accounts *AccountService, organizations *OrganizationService, middleware gin.HandlerFunc) {
	protected := group.Group("", middleware)
	RegisterAccountRoutes(protected, accounts)
	RegisterOrganizationRoutes(protected, organizations)
}

// RegisterAccountRoutes 注册仅平台管理员可调用的后台账号管理接口。
func RegisterAccountRoutes(group *gin.RouterGroup, accounts *AccountService) {
	admin := group.Group("", RequireAnyPermission(adminAccountPermission))
	admin.GET("/admin/accounts", func(c *gin.Context) {
		orgID, _ := strconv.ParseUint(c.Query("orgId"), 10, 64)
		pageNo, pageSize := parseAccountPagination(c)
		data, err := accounts.List(c.Request.Context(), c.Query("keyword"), orgID, c.Query("roleCode"), c.Query("status"), pageNo, pageSize)
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "查询后台账号失败"))
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.POST("/admin/accounts", func(c *gin.Context) {
		var request AdminAccountCreateRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := CurrentAdminFromContext(c)
		data, code, err := accounts.Create(c.Request.Context(), current, request, c.ClientIP())
		writeAccountServiceResponse(c, http.StatusCreated, data, code, err)
	})
	admin.GET("/admin/accounts/:userId", func(c *gin.Context) {
		userID, ok := parseAccountUserID(c)
		if !ok {
			return
		}
		data, code, err := accounts.Get(c.Request.Context(), userID)
		writeAccountServiceResponse(c, http.StatusOK, data, code, err)
	})
	admin.PATCH("/admin/accounts/:userId", func(c *gin.Context) {
		userID, ok := parseAccountUserID(c)
		if !ok {
			return
		}
		version, ok := parseAccountVersion(c)
		if !ok {
			return
		}
		var request AdminAccountUpdateRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := CurrentAdminFromContext(c)
		data, code, err := accounts.Update(c.Request.Context(), current, userID, version, request, c.ClientIP())
		writeAccountServiceResponse(c, http.StatusOK, data, code, err)
	})
	admin.PATCH("/admin/accounts/:userId/status", func(c *gin.Context) {
		userID, ok := parseAccountUserID(c)
		if !ok {
			return
		}
		version, ok := parseAccountVersion(c)
		if !ok {
			return
		}
		var request AdminAccountStatusRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := CurrentAdminFromContext(c)
		data, code, err := accounts.ChangeStatus(c.Request.Context(), current, userID, version, request, c.ClientIP())
		writeAccountServiceResponse(c, http.StatusOK, data, code, err)
	})
}

// RegisterOrganizationRoutes 注册平台管理员维护组织主数据及账号下拉选项的接口。
func RegisterOrganizationRoutes(group *gin.RouterGroup, organizations *OrganizationService) {
	admin := group.Group("", RequireAnyPermission(adminOrgPermission))
	admin.GET("/admin/orgs/options", func(c *gin.Context) {
		data, err := organizations.Options(c.Request.Context())
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "查询组织下拉选项失败"))
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.GET("/admin/orgs", func(c *gin.Context) {
		pageNo, pageSize := parseAccountPagination(c)
		data, err := organizations.List(c.Request.Context(), c.Query("keyword"), c.Query("orgType"), c.Query("status"), pageNo, pageSize)
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "查询组织列表失败"))
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.POST("/admin/orgs", func(c *gin.Context) {
		var request OrgCreateRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := CurrentAdminFromContext(c)
		data, code, err := organizations.Create(c.Request.Context(), current, request, c.ClientIP())
		writeOrganizationServiceResponse(c, http.StatusCreated, data, code, err)
	})
	admin.GET("/admin/orgs/:orgId", func(c *gin.Context) {
		orgID, ok := parseOrganizationID(c)
		if !ok {
			return
		}
		data, code, err := organizations.Get(c.Request.Context(), orgID)
		writeOrganizationServiceResponse(c, http.StatusOK, data, code, err)
	})
	admin.PATCH("/admin/orgs/:orgId", func(c *gin.Context) {
		orgID, ok := parseOrganizationID(c)
		if !ok {
			return
		}
		version, ok := parseAccountVersion(c)
		if !ok {
			return
		}
		var request OrgUpdateRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := CurrentAdminFromContext(c)
		data, code, err := organizations.Update(c.Request.Context(), current, orgID, version, request, c.ClientIP())
		writeOrganizationServiceResponse(c, http.StatusOK, data, code, err)
	})
	admin.PATCH("/admin/orgs/:orgId/status", func(c *gin.Context) {
		orgID, ok := parseOrganizationID(c)
		if !ok {
			return
		}
		version, ok := parseAccountVersion(c)
		if !ok {
			return
		}
		var request OrgStatusRequest
		if !httpx.BindJSON(c, &request) {
			return
		}
		current, _ := CurrentAdminFromContext(c)
		data, code, err := organizations.ChangeStatus(c.Request.Context(), current, orgID, version, request, c.ClientIP())
		writeOrganizationServiceResponse(c, http.StatusOK, data, code, err)
	})
}

func parseAccountUserID(c *gin.Context) (uint64, bool) {
	userID, err := strconv.ParseUint(c.Param("userId"), 10, 64)
	if err != nil || userID == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "账号 ID 格式不正确", httpx.FieldError{Field: "userId", Message: "必须为正整数"}))
		return 0, false
	}
	return userID, true
}

func parseOrganizationID(c *gin.Context) (uint64, bool) {
	orgID, err := strconv.ParseUint(c.Param("orgId"), 10, 64)
	if err != nil || orgID == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "组织 ID 格式不正确", httpx.FieldError{Field: "orgId", Message: "必须为正整数"}))
		return 0, false
	}
	return orgID, true
}

func parseAccountVersion(c *gin.Context) (uint32, bool) {
	return httpx.MustParseIfMatchVersion(c)
}

func parseAccountPagination(c *gin.Context) (int, int) {
	pageNo, _ := strconv.Atoi(c.DefaultQuery("pageNo", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if pageNo < 1 {
		pageNo = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return pageNo, pageSize
}

func writeAccountServiceResponse(c *gin.Context, successStatus int, data map[string]any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "后台账号服务暂不可用"))
		return
	}
	switch code {
	case "":
		httpx.Success(c, successStatus, data)
	case "RESOURCE_NOT_FOUND":
		httpx.AbortWithError(c, httpx.NewError(http.StatusNotFound, code, "后台账号不存在"))
	case "VERSION_CONFLICT":
		httpx.AbortWithError(c, httpx.NewError(http.StatusConflict, code, "账号已被其他操作修改，请刷新后重试"))
	case "RESOURCE_CONFLICT":
		httpx.AbortWithError(c, httpx.NewError(http.StatusConflict, code, "账号或手机号已存在"))
	case "VALIDATION_FORMAT_INVALID":
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, code, "账号或数据范围格式不正确"))
	default:
		httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, code, "后台账号服务暂不可用"))
	}
}

func writeOrganizationServiceResponse(c *gin.Context, successStatus int, data map[string]any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "组织服务暂不可用"))
		return
	}
	switch code {
	case "":
		httpx.Success(c, successStatus, data)
	case "RESOURCE_NOT_FOUND":
		httpx.AbortWithError(c, httpx.NewError(http.StatusNotFound, code, "组织不存在"))
	case "VERSION_CONFLICT":
		httpx.AbortWithError(c, httpx.NewError(http.StatusConflict, code, "组织已被其他操作修改，请刷新后重试"))
	case "RESOURCE_CONFLICT":
		httpx.AbortWithError(c, httpx.NewError(http.StatusConflict, code, "组织编码重复，或组织仍有关联的启用账号、下级组织"))
	case "VALIDATION_FORMAT_INVALID":
		httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, code, "组织资料、层级或城市编码不正确"))
	default:
		httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, code, "组织服务暂不可用"))
	}
}
