package aftersales

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

func RegisterRoutes(g *gin.RouterGroup, s *Service, auth gin.HandlerFunc) {
	r := g.Group("/consumer/after-sales", auth)
	r.POST("/product", func(c *gin.Context) {
		var in ProductApplyInput
		raw, ok := bind(c, &in)
		if ok {
			write(c, s, raw, http.StatusCreated, func() (any, string, error) { return s.ApplyProduct(c, curr(c), in, c.ClientIP()) })
		}
	})
	r.POST("/activity", func(c *gin.Context) {
		var in ActivityApplyInput
		raw, ok := bind(c, &in)
		if ok {
			write(c, s, raw, http.StatusCreated, func() (any, string, error) { return s.ApplyActivity(c, curr(c), in, c.ClientIP()) })
		}
	})
	r.GET("", func(c *gin.Context) {
		d, e := s.List(c, curr(c), c.Query("status"))
		respond(c, http.StatusOK, d, "", e)
	})
	r.GET("/:afterSaleId", func(c *gin.Context) {
		id, ok := idOf(c)
		if ok {
			d, code, e := s.Get(c, curr(c), id)
			respond(c, http.StatusOK, d, code, e)
		}
	})
	r.POST("/:afterSaleId/withdraw", func(c *gin.Context) {
		id, ok := idOf(c)
		if ok {
			write(c, s, []byte("{}"), http.StatusOK, func() (any, string, error) { return s.Withdraw(c, curr(c), id, version(c), c.ClientIP()) })
		}
	})
	r.POST("/:afterSaleId/return-logistics", func(c *gin.Context) {
		id, ok := idOf(c)
		if !ok {
			return
		}
		var in ReturnLogisticsInput
		raw, ok := bind(c, &in)
		if ok {
			write(c, s, raw, http.StatusOK, func() (any, string, error) {
				return s.SubmitReturnLogistics(c, curr(c), id, in, version(c), c.ClientIP())
			})
		}
	})
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
func idOf(c *gin.Context) (uint64, bool) {
	id, e := strconv.ParseUint(c.Param("afterSaleId"), 10, 64)
	if e != nil || id == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "售后单ID格式错误"))
		return 0, false
	}
	return id, true
}
func version(c *gin.Context) uint {
	v, _ := strconv.ParseUint(c.GetHeader("If-Match-Version"), 10, 32)
	return uint(v)
}
func write(c *gin.Context, s *Service, raw []byte, status int, fn func() (any, string, error)) {
	u := curr(c)
	r, replayed, e := idempotency.Execute(c, s.db, idempotency.Scope{SubjectType: "CONSUMER", SubjectID: u.ConsumerID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(_ *gorm.DB) (idempotency.Result, error) {
		d, code, e := fn()
		if e != nil {
			return idempotency.Result{}, e
		}
		return idempotency.Result{HTTPStatus: httpStatus(status, code), Code: code, Data: d}, nil
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
func httpStatus(ok int, code string) int {
	if code == "" {
		return ok
	}
	switch code {
	case "AFTER_SALE_NOT_FOUND", "PRODUCT_ORDER_NOT_FOUND", "PRODUCT_ORDER_ITEM_NOT_FOUND", "ACTIVITY_BOOKING_NOT_FOUND", "AFTER_SALE_EVIDENCE_FILE_NOT_FOUND":
		return http.StatusNotFound
	case "AFTER_SALE_NOT_ALLOWED", "AFTER_SALE_TYPE_NOT_ALLOWED", "AFTER_SALE_QUANTITY_EXCEEDED", "AFTER_SALE_CONFLICT", "ACTIVITY_AFTER_SALE_DEADLINE_EXPIRED", "AFTER_SALE_NOT_WITHDRAWABLE", "RETURN_LOGISTICS_NOT_ALLOWED", "RETURN_LOGISTICS_TRACKING_CONFLICT", "VERSION_CONFLICT", "AFTER_SALE_TEXT_REJECTED", "AFTER_SALE_TEXT_CHECK_ERROR":
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadRequest
	}
}
func respond(c *gin.Context, status int, data any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(500, "INTERNAL_ERROR", "售后处理失败"))
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
	case "AFTER_SALE_NOT_FOUND":
		return "售后单不存在"
	case "PRODUCT_ORDER_NOT_FOUND":
		return "商品订单不存在"
	case "PRODUCT_ORDER_ITEM_NOT_FOUND":
		return "商品订单明细不存在"
	case "ACTIVITY_BOOKING_NOT_FOUND":
		return "活动预约不存在"
	case "AFTER_SALE_NOT_ALLOWED":
		return "当前状态不允许申请售后"
	case "AFTER_SALE_TYPE_NOT_ALLOWED":
		return "售后模板或订单状态不支持该售后类型"
	case "AFTER_SALE_QUANTITY_EXCEEDED":
		return "申请数量不能超过订单明细数量"
	case "AFTER_SALE_CONFLICT":
		return "该对象已有处理中售后申请"
	case "AFTER_SALE_REASON_REQUIRED":
		return "选择其他原因时必须填写说明"
	case "AFTER_SALE_EVIDENCE_FILE_NOT_FOUND":
		return "售后凭证文件不存在或未上传完成"
	case "ACTIVITY_AFTER_SALE_DEADLINE_EXPIRED":
		return "活动开始后不能申请退款"
	case "AFTER_SALE_NOT_WITHDRAWABLE":
		return "当前售后单不能撤回"
	case "RETURN_LOGISTICS_NOT_ALLOWED":
		return "当前售后单不能提交退货物流"
	case "RETURN_LOGISTICS_TRACKING_CONFLICT":
		return "退货运单号已被使用"
	case "VERSION_CONFLICT":
		return "售后单已变更，请刷新后重试"
	case "AFTER_SALE_TEXT_REJECTED":
		return "申请说明未通过内容审核，请修改后重试"
	case "AFTER_SALE_TEXT_CHECK_ERROR":
		return "申请说明审核暂时不可用，请稍后重试"
	default:
		return "请求参数不正确"
	}
}
