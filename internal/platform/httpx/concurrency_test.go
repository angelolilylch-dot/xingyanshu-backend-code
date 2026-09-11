package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/platform/idempotency"
	"xingyanshu-backend/internal/platform/state"
)

// TestConcurrencyErrors 验证幂等和状态机错误能稳定映射为接口错误码。
func TestConcurrencyErrors(t *testing.T) {
	if appErr := AppErrorFromIdempotency(idempotency.ErrConflict); appErr.Status != http.StatusConflict || appErr.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("unexpected idempotency error: %#v", appErr)
	}
	if appErr := AppErrorFromStateTransition(state.ErrTransitionNotAllowed); appErr.Status != http.StatusUnprocessableEntity || appErr.Code != "STATE_TRANSITION_NOT_ALLOWED" {
		t.Fatalf("unexpected transition error: %#v", appErr)
	}
}

// TestMustParseIfMatchVersion 验证更新接口统一要求正整数版本号。
func TestMustParseIfMatchVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PATCH("/resource", func(c *gin.Context) {
		version, ok := MustParseIfMatchVersion(c)
		if !ok {
			return
		}
		Success(c, http.StatusOK, gin.H{"version": version})
	})

	invalid := httptest.NewRequest(http.MethodPatch, "/resource", nil)
	invalidResponse := httptest.NewRecorder()
	router.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid version status = %d", invalidResponse.Code)
	}

	valid := httptest.NewRequest(http.MethodPatch, "/resource", nil)
	valid.Header.Set("If-Match-Version", "3")
	validResponse := httptest.NewRecorder()
	router.ServeHTTP(validResponse, valid)
	if validResponse.Code != http.StatusOK {
		t.Fatalf("valid version status = %d", validResponse.Code)
	}
}
