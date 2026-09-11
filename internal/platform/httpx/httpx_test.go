package httpx

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSuccessIncludesRequestIDAndTimestamp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware())
	router.GET("/test", func(c *gin.Context) { Success(c, http.StatusOK, gin.H{"value": "ok"}) })

	request := httptest.NewRequest(http.MethodGet, "/test", nil)
	request.Header.Set("X-Request-Id", "req_test_001")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("X-Request-Id") != "req_test_001" {
		t.Fatalf("unexpected response: status=%d requestID=%q", response.Code, response.Header().Get("X-Request-Id"))
	}
	if got := response.Body.String(); !containsAll(got, `"code":"OK"`, `"requestId":"req_test_001"`, `"timestamp"`) {
		t.Fatalf("unexpected body: %s", got)
	}
}

func TestBindJSONReturnsFieldError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	type requestBody struct {
		Name string `json:"name" binding:"required"`
	}
	router := gin.New()
	router.Use(RequestIDMiddleware())
	router.POST("/test", func(c *gin.Context) {
		var request requestBody
		if !BindJSON(c, &request) {
			return
		}
		Success(c, http.StatusOK, nil)
	})

	request := httptest.NewRequest(http.MethodPost, "/test", bytes.NewBufferString("{}"))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !containsAll(response.Body.String(), `"code":"VALIDATION_REQUIRED"`, `"field":"name"`) {
		t.Fatalf("unexpected response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestParsePageQueryAndPageResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/test?pageNo=2&pageSize=30", nil)
	page, appErr := ParsePageQuery(context)
	if appErr != nil || page.PageNo != 2 || page.PageSize != 30 {
		t.Fatalf("page = %#v, error = %#v", page, appErr)
	}
	result := NewPageResult([]string{"a"}, page, 31)
	if result.TotalPages != 2 || result.Items[0] != "a" {
		t.Fatalf("page result = %#v", result)
	}
}

func TestRecoveryReturnsUnifiedError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestIDMiddleware(), RecoveryMiddleware())
	router.GET("/panic", func(*gin.Context) { panic("test") })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if response.Code != http.StatusInternalServerError || !containsAll(response.Body.String(), `"code":"INTERNAL_ERROR"`, `"requestId"`) {
		t.Fatalf("unexpected response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func containsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
