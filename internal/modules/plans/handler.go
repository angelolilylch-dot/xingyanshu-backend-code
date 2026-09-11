package plans

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
)

const (
	planManagePermission   = "AUTH_PLAN_MANAGE"
	planReviewPermission   = "AUTH_PLAN_REVIEW"
	cityIPCreatePermission = "IP_CREATE_CITY"
)

// RegisterRoutes 注册方案模板查询和平台管理员维护入口。
func RegisterRoutes(group *gin.RouterGroup, s *Service, m gin.HandlerFunc) {
	p := group.Group("", m)
	read := p.Group("", identity.RequireAnyPermission(planManagePermission, planReviewPermission, cityIPCreatePermission))
	read.GET("/distribution-templates", func(c *gin.Context) {
		d, e := s.ListTemplates(c.Request.Context(), c.DefaultQuery("enabledOnly", "true") != "false")
		respond(c, http.StatusOK, d, "", e)
	})
	read.GET("/distribution-templates/:templateId", func(c *gin.Context) {
		id, e := strconv.ParseUint(c.Param("templateId"), 10, 64)
		if e != nil || id == 0 {
			httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "模板ID格式错误"))
			return
		}
		d, code, e := s.GetTemplate(c.Request.Context(), id)
		respond(c, http.StatusOK, d, code, e)
	})
	read.GET("/auth-plans", func(c *gin.Context) {
		d, e := s.ListAuthPlans(c.Request.Context(), c.DefaultQuery("enabledOnly", "true") != "false")
		respond(c, http.StatusOK, d, "", e)
	})
	read.GET("/auth-plans/:planId", func(c *gin.Context) {
		id, e := strconv.ParseUint(c.Param("planId"), 10, 64)
		if e != nil || id == 0 {
			httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "方案ID格式错误"))
			return
		}
		d, code, e := s.GetAuthPlan(c.Request.Context(), id)
		respond(c, http.StatusOK, d, code, e)
	})
	admin := p.Group("", identity.RequireAnyPermission(planManagePermission))
	admin.POST("/distribution-templates", func(c *gin.Context) {
		var r DistributionCreate
		if !httpx.BindJSON(c, &r) {
			return
		}
		u, _ := identity.CurrentAdminFromContext(c)
		d, code, e := s.CreateTemplate(c.Request.Context(), u, r, c.ClientIP())
		respond(c, http.StatusCreated, d, code, e)
	})
	admin.PATCH("/distribution-templates/:templateId", func(c *gin.Context) {
		id, e := strconv.ParseUint(c.Param("templateId"), 10, 64)
		v, ok := httpx.MustParseIfMatchVersion(c)
		if e != nil || id == 0 || !ok {
			return
		}
		var r TemplateUpdate
		if !httpx.BindJSON(c, &r) {
			return
		}
		u, _ := identity.CurrentAdminFromContext(c)
		d, code, e := s.UpdateTemplate(c.Request.Context(), u, id, v, r, c.ClientIP())
		respond(c, http.StatusOK, d, code, e)
	})
	admin.PATCH("/distribution-templates/:templateId/status", func(c *gin.Context) {
		id, e := strconv.ParseUint(c.Param("templateId"), 10, 64)
		version, ok := httpx.MustParseIfMatchVersion(c)
		if e != nil || id == 0 || !ok {
			return
		}
		var r struct {
			Enabled bool   `json:"enabled"`
			Reason  string `json:"reason" binding:"max=500"`
		}
		if !httpx.BindJSON(c, &r) {
			return
		}
		u, _ := identity.CurrentAdminFromContext(c)
		d, code, err := s.ChangeTemplateStatus(c.Request.Context(), u, id, version, r.Enabled, r.Reason, c.ClientIP())
		respond(c, http.StatusOK, d, code, err)
	})
	admin.POST("/auth-plans", func(c *gin.Context) {
		var r AuthCreate
		if !httpx.BindJSON(c, &r) {
			return
		}
		u, _ := identity.CurrentAdminFromContext(c)
		d, code, e := s.CreateAuth(c.Request.Context(), u, r, c.ClientIP())
		respond(c, http.StatusCreated, d, code, e)
	})
	admin.PATCH("/auth-plans/:planId/draft", func(c *gin.Context) {
		id, version, ok := planIDVersion(c)
		if !ok {
			return
		}
		var r AuthDraftUpdate
		if !httpx.BindJSON(c, &r) {
			return
		}
		u, _ := identity.CurrentAdminFromContext(c)
		d, code, err := s.UpdateAuthDraft(c.Request.Context(), u, id, version, r, c.ClientIP())
		respond(c, http.StatusOK, d, code, err)
	})
	for _, action := range []string{"submit", "enable", "disable"} {
		a := action
		admin.POST("/auth-plans/:planId/"+a, func(c *gin.Context) {
			id, v, ok := planIDVersion(c)
			if !ok {
				return
			}
			var r Status
			if !httpx.BindJSON(c, &r) {
				return
			}
			u, _ := identity.CurrentAdminFromContext(c)
			d, code, e := s.ChangeAuthStatus(c.Request.Context(), u, id, v, a, "", r.Reason, c.ClientIP())
			respond(c, http.StatusOK, d, code, e)
		})
	}
	reviewer := p.Group("", identity.RequireAnyPermission(planReviewPermission))
	reviewer.POST("/auth-plans/:planId/reviews", func(c *gin.Context) {
		id, v, ok := planIDVersion(c)
		if !ok {
			return
		}
		var r Review
		if !httpx.BindJSON(c, &r) {
			return
		}
		u, _ := identity.CurrentAdminFromContext(c)
		d, code, e := s.ChangeAuthStatus(c.Request.Context(), u, id, v, "review", r.Result, r.Comment, c.ClientIP())
		respond(c, http.StatusOK, d, code, e)
	})
}
func planIDVersion(c *gin.Context) (uint64, uint32, bool) {
	id, e := strconv.ParseUint(c.Param("planId"), 10, 64)
	v, ok := httpx.MustParseIfMatchVersion(c)
	return id, v, e == nil && id > 0 && ok
}
func respond(c *gin.Context, status int, d map[string]any, code string, e error) {
	if e != nil {
		httpx.AbortWithError(c, httpx.NewError(500, "INTERNAL_ERROR", "方案服务暂不可用"))
		return
	}
	if code == "" {
		httpx.Success(c, status, d)
		return
	}
	if code == "RESOURCE_NOT_FOUND" {
		httpx.AbortWithError(c, httpx.NewError(http.StatusNotFound, code, "资源不存在或关联分账模板未启用"))
		return
	}
	if code == "VERSION_CONFLICT" {
		httpx.AbortWithError(c, httpx.NewError(http.StatusConflict, code, "数据已被其他操作更新，请刷新后重试"))
		return
	}
	if code == "AUTH_PLAN_BOUND_LOCKED" || code == "STATE_TRANSITION_NOT_ALLOWED" {
		httpx.AbortWithError(c, httpx.NewError(http.StatusUnprocessableEntity, code, "当前方案或模板状态不允许执行该操作"))
		return
	}
	httpx.AbortWithError(c, httpx.NewError(http.StatusBadRequest, code, "方案字段不符合要求"))
}
