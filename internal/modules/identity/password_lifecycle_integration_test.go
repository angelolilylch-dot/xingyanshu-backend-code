package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/cache"
	"xingyanshu-backend/internal/platform/idgen"
)

// TestPasswordLifecycleWithMySQLAndRedis 验证无 loginType 登录、首次强制改密、管理员重置和短信找回。
// 显式设置 XINGYANSHU_RUN_DB_TESTS=1 后运行，使用 config/test.yaml 的本地 MySQL 和 Redis。
func TestPasswordLifecycleWithMySQLAndRedis(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1 to run password lifecycle integration test")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(root, "..", "..", "..", "config", "test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	rdb, err := cache.OpenRedis(context.Background(), cfg.Redis)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rdb.Close() })

	ctx := context.Background()
	userID, orgID := idgen.Next(), idgen.Next()
	username, mobile := "pwd_it_"+jsonNumber(userID), fmt.Sprintf("138%08d", userID%100000000)
	initial, next := "Aa@123456", "Bb#654321"
	hash, err := bcrypt.GenerateFromPassword([]byte(initial), 12)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO sys_org (id,status,version,org_code,org_name,org_type) VALUES (?,?,?,?,?,?)", orgID, "ACTIVE", 1, "IT_"+jsonNumber(orgID), "密码流程测试组织", "PLATFORM").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO sys_user (id,status,version,username,password_hash,must_change_password,display_name,mobile,org_id) VALUES (?,?,?,?,?,?,?,?,?)", userID, "ACTIVE", 1, username, string(hash), true, "密码流程测试账号", mobile, orgID).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DELETE FROM auth_session WHERE subject_id=? AND subject_type='ADMIN'", userID)
		db.Exec("DELETE FROM sys_user WHERE id=?", userID)
		db.Exec("DELETE FROM sys_org WHERE id=?", orgID)
	})

	captcha := identity.NewCaptchaService(rdb)
	login := identity.NewLoginService(db, captcha, rdb, cfg.Auth, cfg.App.Environment)
	router := gin.New()
	api := router.Group("/api/v1")
	identity.RegisterPublicRoutes(api, captcha, login)
	creatorService := creators.NewService(db, rdb, cfg.Auth, "test")
	creators.RegisterRoutes(api, creatorService, login, nil, func(c *gin.Context) {}, func(c *gin.Context) {})
	protected := identity.RequireAdminAuthentication(db, cfg.Auth)
	api.GET("/test-protected", protected, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	identity.RegisterProtectedRoutes(api, login, identity.NewAccountService(db), identity.NewOrganizationService(db), protected)

	loginResponse := postJSON(t, router, "/api/v1/auth/login", map[string]any{"account": username, "password": initial, "captchaId": testCaptcha(t, ctx, captcha, rdb), "captchaCode": "123456"}, "")
	if loginResponse.Code != http.StatusOK || loginResponse.MustChange != true || loginResponse.Token == "" {
		t.Fatalf("first login: %+v", loginResponse)
	}
	blocked := request(t, router, http.MethodGet, "/api/v1/test-protected", nil, loginResponse.Token)
	if blocked.Code != http.StatusPreconditionRequired {
		t.Fatalf("expected forced password change, got %d", blocked.Code)
	}
	changed := postJSON(t, router, "/api/v1/auth/password/change", map[string]any{"oldPassword": initial, "newPassword": next}, loginResponse.Token)
	if changed.Code != http.StatusOK {
		t.Fatalf("change password: %+v", changed)
	}

	normal := postJSON(t, router, "/api/v1/auth/login", map[string]any{"account": username, "password": next, "captchaId": testCaptcha(t, ctx, captcha, rdb), "captchaCode": "123456"}, "")
	if normal.Code != http.StatusOK || normal.MustChange {
		t.Fatalf("normal login: %+v", normal)
	}
	if code, err := login.ResetPassword(ctx, userID, initial); err != nil || code != "" {
		t.Fatalf("admin reset: code=%s err=%v", code, err)
	}
	old := request(t, router, http.MethodGet, "/api/v1/test-protected", nil, normal.Token)
	if old.Code != http.StatusUnauthorized {
		t.Fatalf("old session after reset: got %d", old.Code)
	}

	sms, code, err := login.SendPasswordResetSMS(ctx, mobile)
	if err != nil || code != "" || sms["mockCode"] != "123456" {
		t.Fatalf("send reset sms: data=%v code=%s err=%v", sms, code, err)
	}
	if code, err := login.ResetPasswordBySMS(ctx, mobile, "123456", next); err != nil || code != "" {
		t.Fatalf("sms reset: code=%s err=%v", code, err)
	}
}

type testResponse struct {
	Code       int
	Token      string
	MustChange bool
}

func postJSON(t *testing.T, router http.Handler, path string, body map[string]any, token string) testResponse {
	return request(t, router, http.MethodPost, path, body, token)
}

func request(t *testing.T, router http.Handler, method, path string, body map[string]any, token string) testResponse {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	var envelope struct {
		Data struct {
			AccessToken        string `json:"accessToken"`
			MustChangePassword bool   `json:"mustChangePassword"`
		} `json:"data"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &envelope)
	return testResponse{Code: res.Code, Token: envelope.Data.AccessToken, MustChange: envelope.Data.MustChangePassword}
}

func testCaptcha(t *testing.T, ctx context.Context, captcha *identity.CaptchaService, rdb *redis.Client) string {
	item, err := captcha.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set(ctx, "captcha:"+item.CaptchaID, "123456", 5*time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	return item.CaptchaID
}

func jsonNumber(value uint64) string { return fmt.Sprintf("%d", value) }
