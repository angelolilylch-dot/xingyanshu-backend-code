package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/config"
)

// TestCORSMiddleware 验证白名单来源能够完成浏览器预检，未授权来源不会获得跨域许可。
func TestCORSMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORSMiddleware(config.CORSConfig{AllowedOrigins: []string{"http://localhost:5173"}}))
	router.GET("/api/v1/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	allowed := httptest.NewRequest(http.MethodOptions, "/api/v1/ping", nil)
	allowed.Header.Set("Origin", "http://localhost:5173")
	allowed.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	allowed.Header.Set("Access-Control-Request-Headers", "Authorization,If-Match-Version")
	allowedResponse := httptest.NewRecorder()
	router.ServeHTTP(allowedResponse, allowed)
	if allowedResponse.Code != http.StatusNoContent {
		t.Fatalf("allowed preflight status = %d, want %d", allowedResponse.Code, http.StatusNoContent)
	}
	if got := allowedResponse.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("allowed origin = %q", got)
	}

	denied := httptest.NewRequest(http.MethodOptions, "/api/v1/ping", nil)
	denied.Header.Set("Origin", "https://untrusted.example")
	denied.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	deniedResponse := httptest.NewRecorder()
	router.ServeHTTP(deniedResponse, denied)
	if got := deniedResponse.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("untrusted origin must not be allowed, got %q", got)
	}
}

// TestCORSMiddlewareAllowsAllForTestConfig 验证测试环境的通配符会返回浏览器需要的跨域许可。
func TestCORSMiddlewareAllowsAllForTestConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORSMiddleware(config.CORSConfig{AllowedOrigins: []string{"*"}}))
	router.GET("/api/v1/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	request := httptest.NewRequest(http.MethodOptions, "/api/v1/ping", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("wildcard preflight status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("wildcard allow origin = %q", got)
	}
}
