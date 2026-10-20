package identity_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/cache"
	"xingyanshu-backend/internal/platform/idgen"
)

func TestConsumerSMSLoginLifecycleWithMySQLAndRedis(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1 to run consumer login integration test")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
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

	mobile := fmt.Sprintf("139%08d", idgen.Next()%100000000)
	rejectedMobile := fmt.Sprintf("138%08d", idgen.Next()%100000000)
	login := identity.NewLoginService(db, identity.NewCaptchaService(rdb), rdb, cfg.Auth, "test")
	router := gin.New()
	identity.RegisterConsumerRoutes(router.Group("/api/v1"), login, identity.RequireConsumerAuthentication(db, cfg.Auth))
	identity.RegisterUnifiedRoutes(router.Group("/api/v1"), db, cfg.Auth, login)
	t.Cleanup(func() {
		db.Exec("DELETE FROM auth_session WHERE subject_type='CONSUMER' AND subject_id IN (SELECT id FROM consumer_account WHERE mobile_hash=SHA2(?,256))", mobile)
		db.Exec("DELETE FROM consumer_account WHERE mobile_hash=SHA2(?,256)", mobile)
		db.Exec("DELETE FROM sms_code_log WHERE mobile_hash=SHA2(?,256)", mobile)
		db.Exec("DELETE FROM consumer_account WHERE mobile_hash=SHA2(?,256)", rejectedMobile)
		db.Exec("DELETE FROM sms_code_log WHERE mobile_hash=SHA2(?,256)", rejectedMobile)
	})

	if sendRejected := postJSON(t, router, "/api/v1/consumer/auth/sms/send", map[string]any{"mobile": rejectedMobile}, ""); sendRejected.Code != http.StatusOK {
		t.Fatalf("send rejected consumer SMS: %+v", sendRejected)
	}
	if noAgreement := postJSON(t, router, "/api/v1/consumer/auth/login", map[string]any{"mobile": rejectedMobile, "smsCode": "123456", "agreementAccepted": false, "agreementVersion": "v1.0"}, ""); noAgreement.Code != http.StatusUnprocessableEntity {
		t.Fatalf("consumer login without agreement must fail: %+v", noAgreement)
	}
	sms := postJSON(t, router, "/api/v1/consumer/auth/sms/send", map[string]any{"mobile": mobile}, "")
	if sms.Code != http.StatusOK {
		t.Fatalf("send consumer SMS: %+v", sms)
	}
	first := postJSON(t, router, "/api/v1/consumer/auth/login", map[string]any{"mobile": mobile, "smsCode": "123456", "agreementAccepted": true, "agreementVersion": "v1.0"}, "")
	if first.Code != http.StatusOK || first.Token == "" || first.RefreshToken == "" {
		t.Fatalf("consumer first login: %+v", first)
	}
	var consent struct {
		Version    string     `gorm:"column:agreement_version"`
		AcceptedAt *time.Time `gorm:"column:agreement_accepted_at"`
	}
	if err := db.Table("consumer_account").Select("agreement_version,agreement_accepted_at").Where("mobile_hash=SHA2(?,256)", mobile).Take(&consent).Error; err != nil || consent.Version != "v1.0" || consent.AcceptedAt == nil {
		t.Fatalf("consumer agreement audit missing: %+v err=%v", consent, err)
	}
	me := request(t, router, http.MethodGet, "/api/v1/consumer/me", nil, first.Token)
	if me.Code != http.StatusOK {
		t.Fatalf("consumer me: %+v", me)
	}
	if genericMe := request(t, router, http.MethodGet, "/api/v1/auth/me", nil, first.Token); genericMe.Code != http.StatusUnauthorized {
		t.Fatalf("consumer token must not access unified backend me: %+v", genericMe)
	}
	refreshed := postJSON(t, router, "/api/v1/consumer/auth/refresh", map[string]any{"refreshToken": first.RefreshToken}, "")
	if refreshed.Code != http.StatusOK || refreshed.Token == "" || refreshed.RefreshToken == first.RefreshToken {
		t.Fatalf("consumer refresh: %+v", refreshed)
	}
	old := postJSON(t, router, "/api/v1/consumer/auth/refresh", map[string]any{"refreshToken": first.RefreshToken}, "")
	if old.Code != http.StatusUnauthorized {
		t.Fatalf("old consumer refresh must fail: %+v", old)
	}
	logout := postJSON(t, router, "/api/v1/consumer/auth/logout", nil, refreshed.Token)
	if logout.Code != http.StatusOK {
		t.Fatalf("consumer logout: %+v", logout)
	}
	afterLogout := request(t, router, http.MethodGet, "/api/v1/consumer/me", nil, refreshed.Token)
	if afterLogout.Code != http.StatusUnauthorized {
		t.Fatalf("logged out consumer token must fail: %+v", afterLogout)
	}
}
