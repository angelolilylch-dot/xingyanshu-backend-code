package httpx

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/platform/idempotency"
	"xingyanshu-backend/internal/platform/state"
)

// MustParseIfMatchVersion 解析乐观锁版本号；更新类接口共用该规则，避免并发覆盖。
func MustParseIfMatchVersion(c *gin.Context) (uint32, bool) {
	version, err := strconv.ParseUint(c.GetHeader("If-Match-Version"), 10, 32)
	if err != nil || version == 0 {
		AbortWithError(c, NewValidationError("VALIDATION_REQUIRED", "缺少有效版本号", FieldError{Field: "If-Match-Version", Message: "必须为正整数"}))
		return 0, false
	}
	return uint32(version), true
}

// AppErrorFromIdempotency 将通用幂等执行器的错误映射为接口约定的错误响应。
func AppErrorFromIdempotency(err error) *AppError {
	switch {
	case errors.Is(err, idempotency.ErrKeyRequired):
		return NewValidationError("VALIDATION_REQUIRED", "缺少幂等键", FieldError{Field: "Idempotency-Key", Message: "不能为空"})
	case errors.Is(err, idempotency.ErrKeyInvalid):
		return NewValidationError("VALIDATION_FORMAT_INVALID", "幂等键格式不正确", FieldError{Field: "Idempotency-Key", Message: "须为 16-128 位字母、数字、点、下划线或连字符"})
	case errors.Is(err, idempotency.ErrConflict):
		return NewError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", "幂等键已用于不同请求，请更换后重试")
	default:
		return NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "幂等处理失败")
	}
}

// AppErrorFromStateTransition 将状态机拒绝结果映射为业务接口的统一错误码。
func AppErrorFromStateTransition(err error) *AppError {
	if errors.Is(err, state.ErrTransitionNotAllowed) {
		return NewError(http.StatusUnprocessableEntity, "STATE_TRANSITION_NOT_ALLOWED", "当前状态不允许执行该操作")
	}
	return NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "状态流转处理失败")
}
