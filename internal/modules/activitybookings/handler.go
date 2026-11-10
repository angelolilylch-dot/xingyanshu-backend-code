package activitybookings

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"xingyanshu-backend/internal/modules/creators"
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
	r.GET("/activity-bookings", func(c *gin.Context) {
		d, e := s.List(c, curr(c), c.Query("status"))
		respond(c, http.StatusOK, d, "", e)
	})
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
	r.GET("/activity-check-ins/:entryCode/preview", func(c *gin.Context) {
		d, code, e := s.PreviewCheckin(c, curr(c), c.Param("entryCode"))
		respond(c, checkinStatus(code), d, code, e)
	})
	r.POST("/activity-check-ins/:entryCode/confirm", func(c *gin.Context) {
		var in CheckinConfirmInput
		if raw, ok := bind(c, &in); ok {
			write(c, s, raw, http.StatusOK, func() (any, string, error) {
				return s.ConfirmCheckin(c, curr(c), c.Param("entryCode"), in, c.ClientIP())
			})
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

// RegisterCreatorRoutes exposes a persistent, downloadable venue QR code.
// The event staff only needs to display it; they never need to log in on site.
func RegisterCreatorRoutes(g *gin.RouterGroup, s *Service, auth gin.HandlerFunc) {
	r := g.Group("/creator", auth)
	activityID := func(c *gin.Context) (uint64, bool) {
		v, e := strconv.ParseUint(c.Param("activityId"), 10, 64)
		if e != nil || v == 0 {
			httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "活动ID格式错误"))
			return 0, false
		}
		return v, true
	}
	current := func(c *gin.Context) creators.CurrentCreator { v, _ := creators.CurrentCreatorFromContext(c); return v }
	r.POST("/activities/:activityId/checkin-qrcode", func(c *gin.Context) {
		id, ok := activityID(c)
		if !ok {
			return
		}
		creatorWrite(c, s, []byte("{}"), http.StatusCreated, func() (any, string, error) { return s.CreateOrGetActivityQRCode(c, current(c), id) })
	})
	r.GET("/activities/:activityId/checkin-qrcode", func(c *gin.Context) {
		id, ok := activityID(c)
		if !ok {
			return
		}
		d, code, e := s.GetActivityQRCode(c, current(c), id)
		respond(c, checkinStatus(code), d, code, e)
	})
	r.GET("/activities/:activityId/checkin-qrcode/download", func(c *gin.Context) {
		id, ok := activityID(c)
		if !ok {
			return
		}
		png, name, code, e := s.DownloadActivityQRCode(c, current(c), id)
		if e != nil {
			httpx.AbortWithError(c, httpx.NewError(500, "INTERNAL_ERROR", "生成活动核销二维码失败"))
			return
		}
		if code != "" {
			httpx.AbortWithError(c, httpx.NewError(checkinStatus(code), code, message(code)))
			return
		}
		c.Header("Content-Type", "image/png")
		c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
		c.Data(http.StatusOK, "image/png", png)
	})
	r.POST("/activities/:activityId/checkin-qrcode/rotate", func(c *gin.Context) {
		id, ok := activityID(c)
		if !ok {
			return
		}
		creatorWrite(c, s, []byte("{}"), http.StatusOK, func() (any, string, error) { return s.RotateActivityQRCode(c, current(c), id) })
	})
}

// RegisterPublicRoutes lets an H5 validate the scanned code before login. It
// intentionally exposes no consumer or booking information.
func RegisterPublicRoutes(g *gin.RouterGroup, s *Service) {
	g.GET("/public/activity-checkin-qrcodes/:entryCode", func(c *gin.Context) {
		qr, code, e := s.loadCheckinQR(s.db.WithContext(c), c.Param("entryCode"), false)
		if e != nil {
			httpx.AbortWithError(c, httpx.NewError(500, "INTERNAL_ERROR", "查询活动核销码失败"))
			return
		}
		if code != "" {
			httpx.AbortWithError(c, httpx.NewError(checkinStatus(code), code, message(code)))
			return
		}
		var row struct{ Name, Venue string }
		if e := s.db.WithContext(c).Table("activity a").Select("v.activity_name name,v.venue_name venue").Joins("JOIN activity_version v ON v.id=a.current_version_id").Where("a.id=?", qr.ActivityID).Take(&row).Error; e != nil {
			httpx.AbortWithError(c, httpx.NewError(500, "INTERNAL_ERROR", "查询活动核销码失败"))
			return
		}
		httpx.Success(c, http.StatusOK, gin.H{"activityId": strconv.FormatUint(qr.ActivityID, 10), "activityName": row.Name, "venueName": row.Venue, "requiresLogin": true})
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
	case "ACTIVITY_QUOTA_INSUFFICIENT", "ACTIVITY_BOOKING_NOT_PAYABLE", "STATE_TRANSITION_NOT_ALLOWED", "VERIFICATION_CODE_NOT_AVAILABLE", "ACTIVITY_CHECKIN_NOT_OPEN", "ACTIVITY_CHECKIN_NO_ELIGIBLE_BOOKING", "ACTIVITY_CHECKIN_BOOKING_INVALID":
		return 422
	case "ACTIVITY_CHECKIN_QRCODE_UNAVAILABLE", "ACTIVITY_CHECKIN_ALREADY_CONFIRMED":
		return 409
	case "ACTIVITY_CHECKIN_QRCODE_NOT_FOUND", "ACTIVITY_CHECKIN_QRCODE_INVALID":
		return 404
	}
	if code != "" {
		return 400
	}
	return ok
}
func checkinStatus(code string) int { return choose(http.StatusOK, code) }

func creatorWrite(c *gin.Context, s *Service, raw []byte, status int, fn func() (any, string, error)) {
	current, _ := creators.CurrentCreatorFromContext(c)
	r, replayed, e := idempotency.Execute(c, s.db, idempotency.Scope{SubjectType: "CREATOR", SubjectID: current.CreatorID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(_ *gorm.DB) (idempotency.Result, error) {
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
	case "ACTIVITY_CHECKIN_QRCODE_NOT_FOUND":
		return "当前活动尚未生成现场核销二维码"
	case "ACTIVITY_CHECKIN_QRCODE_INVALID":
		return "活动现场核销二维码无效"
	case "ACTIVITY_CHECKIN_QRCODE_UNAVAILABLE":
		return "活动现场核销二维码当前不可用"
	case "ACTIVITY_CHECKIN_NOT_OPEN":
		return "当前不在该场次核销时间内"
	case "ACTIVITY_CHECKIN_NO_ELIGIBLE_BOOKING":
		return "未找到可核销的本活动预约记录"
	case "ACTIVITY_CHECKIN_BOOKING_INVALID":
		return "请选择本活动可核销的预约记录"
	default:
		return "请求参数不正确"
	}
}
