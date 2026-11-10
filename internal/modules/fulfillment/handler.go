package fulfillment

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idempotency"
)

func RegisterAdminRoutes(g *gin.RouterGroup, s *Service, auth gin.HandlerFunc) {
	r := g.Group("/fulfillment", auth)
	r.GET("/shipments", func(c *gin.Context) {
		d, code, e := s.List(c, current(c), c.Query("status"))
		respond(c, http.StatusOK, d, code, e)
	})
	r.POST("/product-orders/:orderId/shipments", func(c *gin.Context) {
		id, ok := idOf(c, "orderId")
		if !ok {
			return
		}
		var in ShipInput
		raw, ok := bind(c, &in)
		if ok {
			write(c, s, raw, http.StatusCreated, func() (any, string, error) { return s.Ship(c, current(c), id, in, c.ClientIP()) })
		}
	})
	r.GET("/delivery-orders/:deliveryId", func(c *gin.Context) {
		id, ok := idOf(c, "deliveryId")
		if ok {
			d, code, e := s.Get(c, current(c), id)
			respond(c, http.StatusOK, d, code, e)
		}
	})
}
func RegisterConsumerRoutes(g *gin.RouterGroup, s *Service, auth gin.HandlerFunc) {
	r := g.Group("/consumer", auth)
	r.GET("/product-orders/:productOrderId/logistics", func(c *gin.Context) {
		id, ok := idOf(c, "productOrderId")
		if ok {
			d, code, e := s.ConsumerLogistics(c, consumer(c), id)
			respond(c, http.StatusOK, d, code, e)
		}
	})
	r.POST("/product-orders/:productOrderId/confirm-receipt", func(c *gin.Context) {
		id, ok := idOf(c, "productOrderId")
		if ok {
			writeConsumer(c, s, []byte("{}"), http.StatusOK, func() (any, string, error) { return s.ConfirmReceipt(c, consumer(c), id, c.ClientIP()) })
		}
	})
}
func current(c *gin.Context) identity.CurrentAdmin {
	x, _ := identity.CurrentAdminFromContext(c)
	return x
}
func consumer(c *gin.Context) identity.CurrentConsumer {
	x, _ := identity.CurrentConsumerFromContext(c)
	return x
}
func bind(c *gin.Context, v any) ([]byte, bool) {
	raw, e := io.ReadAll(c.Request.Body)
	if e != nil {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "请求体读取失败"))
		return nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, httpx.BindJSON(c, v)
}
func idOf(c *gin.Context, key string) (uint64, bool) {
	id, e := strconv.ParseUint(c.Param(key), 10, 64)
	if e != nil || id == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "ID格式错误"))
		return 0, false
	}
	return id, true
}
func write(c *gin.Context, s *Service, raw []byte, status int, fn func() (any, string, error)) {
	u := current(c)
	result, replayed, e := idempotency.Execute(c, s.db, idempotency.Scope{SubjectType: "ADMIN", SubjectID: u.UserID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(_ *gorm.DB) (idempotency.Result, error) {
		d, code, e := fn()
		return idempotency.Result{HTTPStatus: httpStatus(status, code), Code: code, Data: d}, e
	})
	if e != nil {
		httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(e))
		return
	}
	if replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	respond(c, result.HTTPStatus, result.Data, result.Code, nil)
}
func writeConsumer(c *gin.Context, s *Service, raw []byte, status int, fn func() (any, string, error)) {
	u := consumer(c)
	result, replayed, e := idempotency.Execute(c, s.db, idempotency.Scope{SubjectType: "CONSUMER", SubjectID: u.ConsumerID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(_ *gorm.DB) (idempotency.Result, error) {
		d, code, e := fn()
		return idempotency.Result{HTTPStatus: httpStatus(status, code), Code: code, Data: d}, e
	})
	if e != nil {
		httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(e))
		return
	}
	if replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	respond(c, result.HTTPStatus, result.Data, result.Code, nil)
}
func httpStatus(ok int, code string) int {
	switch code {
	case "":
		return ok
	case "PRODUCT_ORDER_NOT_FOUND", "DELIVERY_ORDER_NOT_FOUND":
		return http.StatusNotFound
	case "AUTH_FORBIDDEN":
		return http.StatusForbidden
	case "STATE_TRANSITION_NOT_ALLOWED", "ORDER_NOT_CONFIRMABLE":
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadRequest
	}
}
func respond(c *gin.Context, status int, data any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(500, "INTERNAL_ERROR", "履约处理失败"))
		return
	}
	if code != "" {
		httpx.AbortWithError(c, httpx.NewError(status, code, message(code)))
		return
	}
	httpx.Success(c, status, data)
}
func message(code string) string {
	switch code {
	case "PRODUCT_ORDER_NOT_FOUND":
		return "商品订单不存在"
	case "DELIVERY_ORDER_NOT_FOUND":
		return "发货单不存在"
	case "STATE_TRANSITION_NOT_ALLOWED":
		return "当前订单状态不允许发货"
	case "ORDER_NOT_CONFIRMABLE":
		return "当前订单不可确认收货"
	case "AUTH_FORBIDDEN":
		return "无权执行履约操作"
	default:
		return "请求参数不正确"
	}
}
