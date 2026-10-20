package activitybookings

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

func RegisterRoutes(g *gin.RouterGroup, s *Service, auth gin.HandlerFunc, environment string) {
	r := g.Group("/consumer", auth)
	r.POST("/activity-bookings/preview", func(c *gin.Context) {
		var in Input
		if _, ok := bind(c, &in); ok {
			d, code, e := s.Preview(c, curr(c), in)
			respond(c, http.StatusOK, d, code, e)
		}
	})
	r.POST("/activity-bookings", func(c *gin.Context) {
		var in Input
		if raw, ok := bind(c, &in); ok {
			write(c, s, raw, http.StatusCreated, func() (any, string, error) { return s.Create(c, curr(c), in, c.ClientIP()) })
		}
	})
	r.GET("/activity-bookings", func(c *gin.Context) { d, e := s.List(c, curr(c)); respond(c, http.StatusOK, d, "", e) })
	r.GET("/activity-bookings/:bookingId", func(c *gin.Context) {
		id, ok := id(c)
		if ok {
			d, code, e := s.Get(c, curr(c), id)
			respond(c, http.StatusOK, d, code, e)
		}
	})
	r.GET("/activity-bookings/:bookingId/verification-code", func(c *gin.Context) {
		id, ok := id(c)
		if ok {
			d, code, e := s.VerificationCode(c, curr(c), id)
			respond(c, http.StatusOK, d, code, e)
		}
	})
	r.POST("/activity-bookings/:bookingId/cancel-pending-payment", func(c *gin.Context) {
		id, ok := id(c)
		if ok {
			write(c, s, []byte("{}"), http.StatusOK, func() (any, string, error) { return s.CancelPendingPayment(c, curr(c), id, c.ClientIP()) })
		}
	})
	if environment == "test" {
		r.POST("/activity-bookings/:bookingId/mock-pay", func(c *gin.Context) {
			id, ok := id(c)
			if !ok {
				return
			}
			var in MockPayInput
			if raw, b := bind(c, &in); b {
				write(c, s, raw, http.StatusOK, func() (any, string, error) { return s.MockPay(c, curr(c), id, in, c.ClientIP()) })
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
func id(c *gin.Context) (uint64, bool) {
	v, e := strconv.ParseUint(c.Param("bookingId"), 10, 64)
	if e != nil || v == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "预约ID格式错误"))
		return 0, false
	}
	return v, true
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
	switch code {
	case "ACTIVITY_BOOKING_NOT_FOUND":
		return 404
	case "ACTIVITY_QUOTA_INSUFFICIENT", "ACTIVITY_BOOKING_NOT_PAYABLE", "STATE_TRANSITION_NOT_ALLOWED", "VERIFICATION_CODE_NOT_AVAILABLE":
		return 422
	}
	if code != "" {
		return 400
	}
	return ok
}
func respond(c *gin.Context, status int, data any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(500, "INTERNAL_ERROR", "活动预约处理失败"))
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
	case "ACTIVITY_TICKET_NOT_BOOKABLE":
		return "活动、场次或票种不可预约"
	case "ACTIVITY_QUOTA_INSUFFICIENT":
		return "活动名额不足"
	case "ACTIVITY_BOOKING_NOT_FOUND":
		return "活动预约不存在"
	case "ACTIVITY_BOOKING_NOT_PAYABLE":
		return "预约已超时或不可支付"
	case "VERIFICATION_CODE_NOT_AVAILABLE":
		return "当前预约暂无可用核销码"
	case "STATE_TRANSITION_NOT_ALLOWED":
		return "当前预约状态不允许该操作"
	default:
		return "请求参数不正确"
	}
}
