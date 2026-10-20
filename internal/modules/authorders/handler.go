package authorders

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

func RegisterRoutes(group *gin.RouterGroup, service *Service, auth gin.HandlerFunc) {
	r := group.Group("/creator/auth-orders", auth)
	r.POST("", func(c *gin.Context) {
		var input CreateInput
		raw, ok := bind(c, &input)
		if !ok {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(c)
		execute(c, service.db, current, raw, http.StatusCreated, func() (map[string]any, string, error) {
			return service.Create(c.Request.Context(), current, input, c.ClientIP())
		})
	})
	r.GET("/:authOrderId", func(c *gin.Context) {
		id, ok := orderID(c)
		if !ok {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(c)
		data, code, err := service.Get(c.Request.Context(), current, id)
		respond(c, http.StatusOK, data, code, err)
	})
	r.POST("/:authOrderId/cancel", func(c *gin.Context) {
		id, ok := orderID(c)
		if !ok {
			return
		}
		var input CancelInput
		raw, ok := bind(c, &input)
		if !ok {
			return
		}
		current, _ := creators.CurrentCreatorFromContext(c)
		execute(c, service.db, current, raw, http.StatusOK, func() (map[string]any, string, error) {
			return service.Cancel(c.Request.Context(), current, id, input.Reason, c.ClientIP())
		})
	})
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
func orderID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("authOrderId"), 10, 64)
	if err != nil || id == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "授权订单编号格式错误"))
		return 0, false
	}
	return id, true
}
func execute(c *gin.Context, db *gorm.DB, current creators.CurrentCreator, raw []byte, status int, action func() (map[string]any, string, error)) {
	result, replayed, err := idempotency.Execute(c.Request.Context(), db, idempotency.Scope{SubjectType: "CREATOR", SubjectID: current.CreatorID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(_ *gorm.DB) (idempotency.Result, error) {
		data, code, actionErr := action()
		if actionErr != nil {
			return idempotency.Result{}, actionErr
		}
		resultStatus := status
		if code != "" {
			resultStatus = ErrorStatus(code)
		}
		return idempotency.Result{HTTPStatus: resultStatus, Code: code, Message: "授权订单操作完成", Data: data}, nil
	})
	if err != nil {
		httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(err))
		return
	}
	if replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	data, _ := result.Data.(map[string]any)
	code := result.Code
	if code == "OK" {
		code = ""
	}
	respond(c, result.HTTPStatus, data, code, nil)
}
func respond(c *gin.Context, status int, data map[string]any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "授权订单处理失败"))
		return
	}
	if code != "" {
		if code == "AUTH_ORDER_NOT_FOUND" || code == "RESOURCE_NOT_FOUND" {
			httpx.AbortWithError(c, httpx.NewErrorWithData(http.StatusNotFound, "AUTH_ORDER_NOT_FOUND", message(code), map[string]any{"resourceType": "AUTH_ORDER", "field": "authOrderId"}))
			return
		}
		if code == "STATE_TRANSITION_NOT_ALLOWED" {
			httpx.AbortWithError(c, httpx.NewErrorWithData(ErrorStatus(code), code, message(code), data))
			return
		}
		httpx.AbortWithError(c, httpx.NewError(ErrorStatus(code), code, message(code)))
		return
	}
	httpx.Success(c, status, data)
}
func message(code string) string {
	switch code {
	case "CREATOR_IDENTITY_REQUIRED":
		return "请先完成实名认证"
	case "CONTRACT_AGREEMENT_REQUIRED":
		return "请阅读并同意虚拟合同及购买须知"
	case "IP_NOT_PURCHASABLE":
		return "IP未上架或不可购买"
	case "AUTH_ORDER_PENDING_EXISTS":
		return "已有一笔待支付授权订单"
	case "AUTHORIZATION_ALREADY_ACTIVE":
		return "已持有该IP的有效授权"
	case "AUTHORIZATION_QUOTA_SOLD_OUT":
		return "授权名额已售罄"
	case "RESOURCE_NOT_FOUND", "AUTH_ORDER_NOT_FOUND":
		return "授权订单不存在"
	case "STATE_TRANSITION_NOT_ALLOWED":
		return "当前订单状态不允许该操作"
	default:
		return "请求参数不正确"
	}
}
