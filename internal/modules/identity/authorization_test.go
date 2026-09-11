package identity

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRequireAnyPermission 验证路由权限中间件仅放行拥有指定权限的当前用户。
func TestRequireAnyPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		permissions map[string]struct{}
		wantStatus  int
	}{
		{name: "拥有权限", permissions: map[string]struct{}{"IP_CREATE_CITY": {}}, wantStatus: http.StatusOK},
		{name: "没有权限", permissions: map[string]struct{}{"DASHBOARD_VIEW_ALL": {}}, wantStatus: http.StatusForbidden},
	}

	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(currentAdminKey, CurrentAdmin{UserID: 1, PermissionCodes: item.permissions})
				c.Next()
			})
			router.GET("/protected", RequireAnyPermission("IP_CREATE_CITY"), func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/protected", nil))
			if response.Code != item.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, item.wantStatus)
			}
		})
	}
}

// TestValidScopeColumn 拒绝不受控的列名，避免业务层错误配置带来 SQL 注入风险。
func TestValidScopeColumn(t *testing.T) {
	if !validScopeColumn("ip.main_city_code") {
		t.Fatal("expected qualified column name to be accepted")
	}
	if validScopeColumn("main_city_code; DROP TABLE ip_asset") {
		t.Fatal("expected unsafe column name to be rejected")
	}
}
