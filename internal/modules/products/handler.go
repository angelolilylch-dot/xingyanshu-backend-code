package products

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strconv"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idempotency"
)

func RegisterRoutes(g *gin.RouterGroup, s *Service, creatorAuth, adminAuth gin.HandlerFunc) {
	c := g.Group("", creatorAuth)
	c.POST("/creator/products", func(x *gin.Context) {
		var in Input
		raw, ok := bind(x, &in)
		if !ok {
			return
		}
		u, _ := creators.CurrentCreatorFromContext(x)
		execute(x, s, u, raw, http.StatusCreated, func() (map[string]any, string, error) { return s.Create(x.Request.Context(), u, in) })
	})
	c.GET("/creator/products", func(x *gin.Context) {
		u, _ := creators.CurrentCreatorFromContext(x)
		query, ok := listInput(x)
		if !ok {
			return
		}
		out, code, e := s.List(x.Request.Context(), u.CreatorID, query)
		reply(x, http.StatusOK, out, code, e)
	})
	c.GET("/creator/products/:productId", func(x *gin.Context) {
		id, ok := id(x, "productId")
		if !ok {
			return
		}
		u, _ := creators.CurrentCreatorFromContext(x)
		out, code, e := s.Get(x.Request.Context(), u.CreatorID, id)
		reply(x, http.StatusOK, out, code, e)
	})
	c.PATCH("/creator/products/:productId", func(x *gin.Context) {
		id, ok := id(x, "productId")
		if !ok {
			return
		}
		var in Input
		raw, ok := bind(x, &in)
		if !ok {
			return
		}
		u, _ := creators.CurrentCreatorFromContext(x)
		execute(x, s, u, raw, http.StatusOK, func() (map[string]any, string, error) { return s.Update(x.Request.Context(), u, id, in) })
	})
	c.POST("/creator/products/:productId/delist", func(x *gin.Context) {
		id, ok := id(x, "productId")
		if !ok {
			return
		}
		u, _ := creators.CurrentCreatorFromContext(x)
		execute(x, s, u, []byte(`{}`), http.StatusOK, func() (map[string]any, string, error) { return s.Delist(x.Request.Context(), u.CreatorID, id) })
	})
	a := g.Group("", adminAuth, identity.RequireAnyPermission(reviewPermission))
	a.GET("/copyright/products/pending", func(x *gin.Context) {
		out, code, e := s.Pending(x.Request.Context(), x.Query("reviewNode"))
		reply(x, http.StatusOK, out, code, e)
	})
	a.POST("/copyright/product-versions/:productVersionId/reviews", func(x *gin.Context) {
		id, ok := id(x, "productVersionId")
		if !ok {
			return
		}
		var in ReviewInput
		raw, ok := bind(x, &in)
		if !ok {
			return
		}
		u, _ := identity.CurrentAdminFromContext(x)
		executeAdmin(x, s, u, raw, func() (map[string]any, string, error) { return s.Review(x.Request.Context(), u, id, in) })
	})
}
func listInput(c *gin.Context) (ListInput, bool) {
	parse := func(name string) (int, bool) {
		value := c.Query(name)
		if value == "" {
			return 0, true
		}
		n, err := strconv.Atoi(value)
		return n, err == nil && n > 0
	}
	pageNo, ok := parse("pageNo")
	if !ok {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "分页参数格式错误"))
		return ListInput{}, false
	}
	pageSize, ok := parse("pageSize")
	if !ok {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "分页参数格式错误"))
		return ListInput{}, false
	}
	var ipID uint64
	if raw := c.Query("ipId"); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || value == 0 {
			httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "ipId 格式错误"))
			return ListInput{}, false
		}
		ipID = value
	}
	return ListInput{Keyword: c.Query("keyword"), IPID: ipID, Status: c.Query("status"), FulfillmentMode: c.Query("fulfillmentMode"), PageNo: pageNo, PageSize: pageSize}, true
}
func bind(c *gin.Context, target any) ([]byte, bool) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "请求体读取失败"))
		return nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, httpx.BindJSON(c, target)
}
func execute(c *gin.Context, s *Service, u creators.CurrentCreator, raw []byte, status int, action func() (map[string]any, string, error)) {
	r, replayed, err := idempotency.Execute(c.Request.Context(), s.db, idempotency.Scope{SubjectType: "CREATOR", SubjectID: u.CreatorID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(_ *gorm.DB) (idempotency.Result, error) {
		data, code, e := action()
		if e != nil {
			return idempotency.Result{}, e
		}
		if code != "" {
			return idempotency.Result{HTTPStatus: http.StatusBadRequest, Code: code, Message: "商品请求无法处理", Data: data}, nil
		}
		return idempotency.Result{HTTPStatus: status, Code: "OK", Message: "成功", Data: data}, nil
	})
	if err != nil {
		httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(err))
		return
	}
	if replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	data, _ := r.Data.(map[string]any)
	code := r.Code
	if code == "OK" {
		code = ""
	}
	reply(c, r.HTTPStatus, data, code, nil)
}
func executeAdmin(c *gin.Context, s *Service, u identity.CurrentAdmin, raw []byte, action func() (map[string]any, string, error)) {
	r, replayed, err := idempotency.Execute(c.Request.Context(), s.db, idempotency.Scope{SubjectType: "ADMIN", SubjectID: u.UserID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(_ *gorm.DB) (idempotency.Result, error) {
		data, code, e := action()
		if e != nil {
			return idempotency.Result{}, e
		}
		if code != "" {
			return idempotency.Result{HTTPStatus: http.StatusBadRequest, Code: code, Message: "商品审核无法处理", Data: data}, nil
		}
		return idempotency.Result{HTTPStatus: http.StatusOK, Code: "OK", Message: "成功", Data: data}, nil
	})
	if err != nil {
		httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(err))
		return
	}
	if replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	data, _ := r.Data.(map[string]any)
	code := r.Code
	if code == "OK" {
		code = ""
	}
	reply(c, r.HTTPStatus, data, code, nil)
}
func id(c *gin.Context, k string) (uint64, bool) {
	v, e := strconv.ParseUint(c.Param(k), 10, 64)
	if e != nil || v == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "参数格式错误"))
		return 0, false
	}
	return v, true
}
func reply(c *gin.Context, status int, data map[string]any, code string, e error) {
	if e != nil {
		httpx.Error(c, httpx.NewError(500, "INTERNAL_ERROR", "商品处理失败"))
		return
	}
	if code != "" {
		httpx.Error(c, httpx.NewError(400, code, "商品请求无法处理"))
		return
	}
	httpx.Success(c, status, data)
}
