package payments

import (
	"bytes"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idempotency"
)

func RegisterMockRoute(g *gin.RouterGroup, s *Service, auth gin.HandlerFunc, environment string) {
	if environment != "test" {
		return
	}
	g.POST("/payments/mock/pay", auth, func(c *gin.Context) {
		var input MockPayInput
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "请求体读取失败"))
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(raw))
		if !httpx.BindJSON(c, &input) {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(c)
		result, replayed, err := idempotency.Execute(c.Request.Context(), s.db, idempotency.Scope{SubjectType: "CREATOR", SubjectID: current.CreatorID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(tx *gorm.DB) (idempotency.Result, error) {
			data, code, actionErr := s.MockPay(c.Request.Context(), tx, current, input, c.ClientIP())
			if actionErr != nil {
				return idempotency.Result{}, actionErr
			}
			if code != "" {
				return idempotency.Result{HTTPStatus: errorStatus(code), Code: code, Message: message(code), Data: data}, nil
			}
			return idempotency.Result{HTTPStatus: http.StatusOK, Data: data}, nil
		})
		if err != nil {
			httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(err))
			return
		}
		if replayed {
			c.Header("Idempotency-Replayed", "true")
		}
		data, _ := result.Data.(map[string]any)
		if result.Code != "" && result.Code != "OK" {
			if result.Code == "AUTH_ORDER_NOT_FOUND" || result.Code == "RESOURCE_NOT_FOUND" {
				httpx.AbortWithError(c, httpx.NewErrorWithData(http.StatusNotFound, "AUTH_ORDER_NOT_FOUND", message(result.Code), map[string]any{"resourceType": "AUTH_ORDER", "field": "bizOrderId"}))
			} else if result.Code == "AUTH_ORDER_PAYMENT_EXPIRED" || result.Code == "STATE_TRANSITION_NOT_ALLOWED" {
				httpx.AbortWithError(c, httpx.NewErrorWithData(result.HTTPStatus, result.Code, message(result.Code), data))
			} else {
				httpx.AbortWithError(c, httpx.NewError(result.HTTPStatus, result.Code, message(result.Code)))
			}
			return
		}
		httpx.Success(c, result.HTTPStatus, data)
	})
}

func errorStatus(code string) int {
	switch code {
	case "RESOURCE_NOT_FOUND", "AUTH_ORDER_NOT_FOUND":
		return http.StatusNotFound
	case "AUTH_ORDER_PAYMENT_EXPIRED", "STATE_TRANSITION_NOT_ALLOWED":
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadRequest
	}
}
func message(code string) string {
	switch code {
	case "RESOURCE_NOT_FOUND", "AUTH_ORDER_NOT_FOUND":
		return "授权订单不存在"
	case "AUTH_ORDER_PAYMENT_EXPIRED":
		return "订单已超时或不可支付"
	case "STATE_TRANSITION_NOT_ALLOWED":
		return "订单状态不允许支付"
	default:
		return "请求参数不正确"
	}
}
