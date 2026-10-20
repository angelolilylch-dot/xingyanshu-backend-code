package productorders

import (
	"bytes"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strconv"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idempotency"
)

func RegisterRoutes(g *gin.RouterGroup, s *Service, auth gin.HandlerFunc, environment string) {
	r := g.Group("/consumer", auth)
	r.GET("/addresses", func(c *gin.Context) { d, e := s.ListAddresses(c, curr(c)); respond(c, http.StatusOK, d, "", e) })
	r.POST("/addresses", func(c *gin.Context) {
		var in AddressInput
		raw, ok := bind(c, &in)
		if ok {
			write(c, s, raw, http.StatusCreated, func() (any, string, error) { return s.AddAddress(c, curr(c), in) })
		}
	})
	r.PATCH("/addresses/:addressId", func(c *gin.Context) {
		id, ok := id(c, "addressId")
		if !ok {
			return
		}
		var in AddressInput
		raw, b := bind(c, &in)
		if b {
			write(c, s, raw, http.StatusOK, func() (any, string, error) { return s.UpdateAddress(c, curr(c), id, in, version(c)) })
		}
	})
	r.DELETE("/addresses/:addressId", func(c *gin.Context) {
		id, ok := id(c, "addressId")
		if ok {
			write(c, s, []byte("{}"), http.StatusOK, func() (any, string, error) {
				code, e := s.DeleteAddress(c, curr(c), id, version(c))
				return map[string]any{"addressId": strconv.FormatUint(id, 10), "deleted": code == ""}, code, e
			})
		}
	})
	r.POST("/addresses/:addressId/default", func(c *gin.Context) {
		id, ok := id(c, "addressId")
		if ok {
			write(c, s, []byte("{}"), http.StatusOK, func() (any, string, error) { return s.SetDefaultAddress(c, curr(c), id, version(c)) })
		}
	})
	r.POST("/product-orders/preview", func(c *gin.Context) {
		var in OrderInput
		if _, ok := bind(c, &in); ok {
			d, code, e := s.Preview(c, curr(c), in)
			respond(c, http.StatusOK, d, code, e)
		}
	})
	r.POST("/product-orders", func(c *gin.Context) {
		var in OrderInput
		raw, ok := bind(c, &in)
		if ok {
			write(c, s, raw, http.StatusCreated, func() (any, string, error) { return s.Create(c, curr(c), in, c.ClientIP()) })
		}
	})
	r.GET("/product-orders", func(c *gin.Context) { d, e := s.List(c, curr(c)); respond(c, http.StatusOK, d, "", e) })
	r.GET("/product-orders/:productOrderId", func(c *gin.Context) {
		oid, ok := id(c, "productOrderId")
		if ok {
			d, code, e := s.Get(c, curr(c), oid)
			respond(c, http.StatusOK, d, code, e)
		}
	})
	r.POST("/product-orders/:productOrderId/cancel", func(c *gin.Context) {
		oid, ok := id(c, "productOrderId")
		if ok {
			write(c, s, []byte("{}"), http.StatusOK, func() (any, string, error) { return s.Cancel(c, curr(c), oid, c.ClientIP()) })
		}
	})
	if environment == "test" {
		r.POST("/product-orders/:productOrderId/mock-pay", func(c *gin.Context) {
			oid, ok := id(c, "productOrderId")
			if !ok {
				return
			}
			var in MockPayInput
			raw, b := bind(c, &in)
			if b {
				write(c, s, raw, http.StatusOK, func() (any, string, error) { return s.MockPay(c, curr(c), oid, in, c.ClientIP()) })
			}
		})
	}
}
func curr(c *gin.Context) identity.CurrentConsumer {
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
func id(c *gin.Context, key string) (uint64, bool) {
	v, e := strconv.ParseUint(c.Param(key), 10, 64)
	if e != nil || v == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "ID格式错误"))
		return 0, false
	}
	return v, true
}
func version(c *gin.Context) uint {
	v, _ := strconv.ParseUint(c.GetHeader("If-Match-Version"), 10, 32)
	return uint(v)
}
func write(c *gin.Context, s *Service, raw []byte, status int, fn func() (any, string, error)) {
	current := curr(c)
	r, replayed, e := idempotency.Execute(c, s.db, idempotency.Scope{SubjectType: "CONSUMER", SubjectID: current.ConsumerID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(_ *gorm.DB) (idempotency.Result, error) {
		d, code, err := fn()
		if err != nil {
			return idempotency.Result{}, err
		}
		return idempotency.Result{HTTPStatus: choose(status, code), Code: code, Data: d}, nil
	})
	if e != nil {
		httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(e))
		return
	}
	if replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	if r.Code == "OK" {
		r.Code = ""
	}
	respond(c, r.HTTPStatus, r.Data, r.Code, nil)
}
func choose(ok int, code string) int {
	if code == "" {
		return ok
	}
	if code == "PRODUCT_ORDER_NOT_FOUND" || code == "ADDRESS_NOT_FOUND" {
		return 404
	}
	if code == "VERSION_CONFLICT" || code == "STATE_TRANSITION_NOT_ALLOWED" || code == "PRODUCT_ORDER_NOT_PAYABLE" || code == "SKU_STOCK_INSUFFICIENT" {
		return 422
	}
	return 400
}
func respond(c *gin.Context, status int, data any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(500, "INTERNAL_ERROR", "商品订单处理失败"))
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
	case "ADDRESS_LIMIT_EXCEEDED":
		return "地址簿最多保存10条地址"
	case "ADDRESS_NOT_FOUND":
		return "收货地址不存在"
	case "PRODUCT_ORDER_NOT_FOUND":
		return "商品订单不存在"
	case "PRODUCT_OR_SKU_NOT_PURCHASABLE":
		return "商品或规格已不可购买"
	case "SKU_STOCK_INSUFFICIENT":
		return "商品库存不足"
	case "SKU_QUANTITY_EXCEEDED":
		return "单个规格数量不能超过99"
	case "PRODUCT_ORDER_NOT_PAYABLE":
		return "订单已超时或不可支付"
	case "STATE_TRANSITION_NOT_ALLOWED":
		return "当前订单状态不允许该操作"
	case "VERSION_CONFLICT":
		return "数据已变更，请刷新后重试"
	default:
		return "请求参数不正确"
	}
}
