package works

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
)

func RegisterRoutes(group *gin.RouterGroup, service *Service, creatorAuth, adminAuth gin.HandlerFunc) {
	c := group.Group("", creatorAuth)
	c.POST("/creator/works", func(ctx *gin.Context) {
		var in SubmitRequest
		if !httpx.BindJSON(ctx, &in) {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(ctx)
		data, code, e := service.Submit(ctx.Request.Context(), current, in)
		respond(ctx, http.StatusCreated, data, code, e)
	})
	c.GET("/creator/works", func(ctx *gin.Context) {
		page, ok := httpx.MustParsePageQuery(ctx)
		if !ok {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(ctx)
		data, code, e := service.List(ctx.Request.Context(), current.CreatorID, page.PageNo, page.PageSize)
		respond(ctx, http.StatusOK, data, code, e)
	})
	c.GET("/creator/works/:workId", func(ctx *gin.Context) {
		id, ok := uintID(ctx, "workId")
		if !ok {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(ctx)
		data, code, e := service.Get(ctx.Request.Context(), current.CreatorID, id)
		respond(ctx, http.StatusOK, data, code, e)
	})
	c.POST("/creator/works/:workId/resubmit", func(ctx *gin.Context) {
		id, ok := uintID(ctx, "workId")
		if !ok {
			return
		}
		var in SubmitRequest
		if !httpx.BindJSON(ctx, &in) {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(ctx)
		data, code, e := service.Resubmit(ctx.Request.Context(), current, id, in)
		respond(ctx, http.StatusCreated, data, code, e)
	})
	a := group.Group("", adminAuth, identity.RequireAnyPermission(reviewPermission))
	a.POST("/copyright/work-versions/:workVersionId/reviews", func(ctx *gin.Context) {
		id, ok := uintID(ctx, "workVersionId")
		if !ok {
			return
		}
		var in ReviewRequest
		if !httpx.BindJSON(ctx, &in) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(ctx)
		data, code, e := service.Review(ctx.Request.Context(), current, id, in)
		respond(ctx, http.StatusOK, data, code, e)
	})
	a.GET("/copyright/works/pending", func(ctx *gin.Context) {
		page, ok := httpx.MustParsePageQuery(ctx)
		if !ok {
			return
		}
		data, code, e := service.Pending(ctx.Request.Context(), page.PageNo, page.PageSize, ctx.Query("reviewNode"), ctx.Query("status"))
		respond(ctx, http.StatusOK, data, code, e)
	})
	a.POST("/copyright/works/:workId/rights-confirmations", func(ctx *gin.Context) {
		id, ok := uintID(ctx, "workId")
		if !ok {
			return
		}
		var in ConfirmRequest
		if !httpx.BindJSON(ctx, &in) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(ctx)
		data, code, e := service.Confirm(ctx.Request.Context(), current, id, in)
		respond(ctx, http.StatusOK, data, code, e)
	})
}
func uintID(c *gin.Context, key string) (uint64, bool) {
	id, e := strconv.ParseUint(c.Param(key), 10, 64)
	if e != nil || id == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "参数格式错误", httpx.FieldError{Field: key, Message: "必须是正整数"}))
		return 0, false
	}
	return id, true
}
func respond(c *gin.Context, status int, data map[string]any, code string, e error) {
	if e != nil {
		httpx.Error(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "系统处理失败"))
		return
	}
	if code != "" {
		httpx.Error(c, httpx.NewError(http.StatusBadRequest, code, "请求无法处理"))
		return
	}
	httpx.Success(c, status, data)
}
