package activities

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idempotency"
)

func RegisterRoutes(g *gin.RouterGroup, s *Service, creatorAuth gin.HandlerFunc) {
	c := g.Group("", creatorAuth)
	c.GET("/creator/activities", func(x *gin.Context) {
		u, _ := creators.CurrentCreatorFromContext(x)
		in, ok := listInput(x)
		if !ok {
			return
		}
		out, code, e := s.List(x.Request.Context(), u.CreatorID, in)
		reply(x, http.StatusOK, out, code, e)
	})
	c.POST("/creator/activities", func(x *gin.Context) {
		var in Input
		raw, ok := bind(x, &in)
		if !ok {
			return
		}
		u, _ := creators.CurrentCreatorFromContext(x)
		execute(x, s, u, raw, http.StatusCreated, func() (map[string]any, string, error) { return s.Create(x.Request.Context(), u, in) })
	})
	c.GET("/creator/activities/:activityId", func(x *gin.Context) {
		id, ok := id(x)
		if !ok {
			return
		}
		u, _ := creators.CurrentCreatorFromContext(x)
		out, code, e := s.Get(x.Request.Context(), u.CreatorID, id)
		reply(x, http.StatusOK, out, code, e)
	})
	c.POST("/creator/activities/:activityId/pause-booking", func(x *gin.Context) {
		id, ok := id(x)
		if !ok {
			return
		}
		u, _ := creators.CurrentCreatorFromContext(x)
		execute(x, s, u, []byte(`{}`), http.StatusOK, func() (map[string]any, string, error) { return s.ChangeBooking(x.Request.Context(), u, id, false) })
	})
	c.POST("/creator/activities/:activityId/resume-booking", func(x *gin.Context) {
		id, ok := id(x)
		if !ok {
			return
		}
		u, _ := creators.CurrentCreatorFromContext(x)
		execute(x, s, u, []byte(`{}`), http.StatusOK, func() (map[string]any, string, error) { return s.ChangeBooking(x.Request.Context(), u, id, true) })
	})
	c.POST("/creator/activities/:activityId/delist", func(x *gin.Context) {
		id, ok := id(x)
		if !ok {
			return
		}
		u, _ := creators.CurrentCreatorFromContext(x)
		execute(x, s, u, []byte(`{}`), http.StatusOK, func() (map[string]any, string, error) { return s.Delist(x.Request.Context(), u, id) })
	})
}
func listInput(c *gin.Context) (ListInput, bool) {
	num := func(k string) (int, bool) {
		v := c.Query(k)
		if v == "" {
			return 0, true
		}
		n, e := strconv.Atoi(v)
		return n, e == nil && n > 0
	}
	p, ok := num("pageNo")
	if !ok {
		return ListInput{}, validation(c)
	}
	s, ok := num("pageSize")
	if !ok {
		return ListInput{}, validation(c)
	}
	var ip uint64
	if v := c.Query("ipId"); v != "" {
		n, e := strconv.ParseUint(v, 10, 64)
		if e != nil || n == 0 {
			return ListInput{}, validation(c)
		}
		ip = n
	}
	return ListInput{Keyword: c.Query("keyword"), IPID: ip, CityCode: c.Query("cityCode"), Status: c.Query("status"), PageNo: p, PageSize: s}, true
}
func validation(c *gin.Context) bool {
	httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "请求参数格式错误"))
	return false
}
func bind(c *gin.Context, target any) ([]byte, bool) {
	raw, e := io.ReadAll(c.Request.Body)
	if e != nil {
		return nil, validation(c)
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, httpx.BindJSON(c, target)
}
func execute(c *gin.Context, s *Service, u creators.CurrentCreator, raw []byte, status int, action func() (map[string]any, string, error)) {
	r, replayed, e := idempotency.Execute(c.Request.Context(), s.db, idempotency.Scope{SubjectType: "CREATOR", SubjectID: u.CreatorID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(_ *gorm.DB) (idempotency.Result, error) {
		data, code, e := action()
		if e != nil {
			return idempotency.Result{}, e
		}
		if code != "" {
			return idempotency.Result{HTTPStatus: http.StatusBadRequest, Code: code, Message: "活动请求无法处理", Data: data}, nil
		}
		return idempotency.Result{HTTPStatus: status, Code: "OK", Message: "成功", Data: data}, nil
	})
	if e != nil {
		httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(e))
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
func id(c *gin.Context) (uint64, bool) {
	v, e := strconv.ParseUint(c.Param("activityId"), 10, 64)
	if e != nil || v == 0 {
		return 0, validation(c)
	}
	return v, true
}
func reply(c *gin.Context, status int, data map[string]any, code string, e error) {
	if e != nil {
		httpx.Error(c, httpx.NewError(500, "INTERNAL_ERROR", "活动处理失败"))
		return
	}
	if code != "" {
		httpx.Error(c, httpx.NewError(400, code, "活动请求无法处理"))
		return
	}
	httpx.Success(c, status, data)
}
