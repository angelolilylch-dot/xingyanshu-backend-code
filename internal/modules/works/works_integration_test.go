package works

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/evidence"
	"xingyanshu-backend/internal/platform/idgen"
)

func TestWorkRightsConfirmationWithMySQL(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1 to run MySQL integration test")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	userID, creatorID, sessionID, ipID, orderID, authorizationID := idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next()
	packID, itemID, materialFileID, previewID, sourceID := idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next()
	defer cleanupWorkFixture(db, userID, creatorID, sessionID, ipID, orderID, authorizationID, packID, []uint64{materialFileID, previewID, sourceID})
	manifest := strings.Repeat("a", 64)
	mustWork(t, db.Table("sys_user").Create(map[string]any{"id": userID, "status": "ACTIVE", "version": 1, "username": fmt.Sprintf("cnt01_%d", userID), "password_hash": "unused", "display_name": "CNT01测试创作者"}).Error)
	mustWork(t, db.Table("creator_profile").Create(map[string]any{"id": creatorID, "user_id": userID, "status": "ACTIVE", "version": 1, "subject_type": "PERSONAL", "display_name": "CNT01测试创作者", "identity_status": "VERIFIED", "letter_status": "OPTIONAL"}).Error)
	mustWork(t, db.Table("auth_session").Create(map[string]any{"id": sessionID, "subject_type": "CREATOR", "subject_id": userID, "client_type": "CREATOR_WEB", "refresh_token_hash": "test", "token_version": 1, "status": "ACTIVE", "expires_at": time.Now().Add(time.Hour)}).Error)
	mustWork(t, db.Table("ip_asset").Create(map[string]any{"id": ipID, "status": "LISTED", "version": 1, "ip_no": fmt.Sprintf("CNT-IP-%d", ipID), "ip_name": "CNT测试IP", "ip_type": "CULTURAL", "source_type": "CITY_COLLECTION", "main_city_org_id": 1, "short_intro": "测试", "detail_content": "测试", "culture_story": "测试", "ip_feature": "测试", "creative_directions_json": "[]", "applicable_product_types_json": "[]", "applicable_activity_types_json": "[]", "public_copyright_notice": "测试", "purchase_notice": "测试", "authorization_mode": "NORMAL_SHARE", "mall_visible": true, "suggested_transaction_price_amount": "1.00"}).Error)
	mustWork(t, db.Table("auth_order").Create(map[string]any{"id": orderID, "status": "PAID", "version": 1, "auth_order_no": fmt.Sprintf("CNT-ORDER-%d", orderID), "creator_id": creatorID, "ip_id": ipID, "auth_plan_version_id": 1, "amount": "1.00", "currency": "CNY", "payment_expire_at": time.Now().Add(time.Hour), "contract_agreed": true, "purchase_snapshot": `{}`, "contract_status": "GENERATED", "evidence_status": "EVIDENCED", "derivative_allowed_snapshot": true, "material_pack_manifest_hash": manifest}).Error)
	mustWork(t, db.Table("creator_authorization").Create(map[string]any{"id": authorizationID, "status": "ACTIVE", "version": 1, "authorization_no": fmt.Sprintf("CNT-AUTH-%d", authorizationID), "creator_id": creatorID, "ip_id": ipID, "auth_order_id": orderID, "authorization_mode": "NORMAL_SHARE", "start_at": time.Now().Add(-time.Hour), "end_at": time.Now().Add(time.Hour), "active_flag": 1}).Error)
	for _, f := range []struct {
		id   uint64
		name string
	}{{materialFileID, "material.png"}, {previewID, "preview.png"}, {sourceID, "source.psd"}} {
		mustWork(t, db.Table("file_object").Create(map[string]any{"id": f.id, "status": "ACTIVE", "version": 1, "created_by": userID, "file_no": fmt.Sprintf("CNT-FILE-%d", f.id), "storage_key": fmt.Sprintf("cnt/%d", f.id), "original_name": f.name, "mime_type": "image/png", "file_size": 8, "file_hash": fmt.Sprintf("%064d", f.id), "security_level": "PRIVATE", "upload_status": "SUCCESS"}).Error)
	}
	mustWork(t, db.Table("ip_material_pack").Create(map[string]any{"id": packID, "status": "FROZEN", "version": 1, "ip_id": ipID, "version_no": 1, "pack_name": "CNT冻结素材包", "manifest_hash": manifest, "evidence_status": "EVIDENCED", "enabled": true, "frozen_at": time.Now()}).Error)
	mustWork(t, db.Table("ip_material_pack_item").Create(map[string]any{"id": itemID, "material_pack_id": packID, "file_id": materialFileID, "file_hash": fmt.Sprintf("%064d", materialFileID), "material_role": "SOURCE", "sort_no": 1, "deliverable": true}).Error)
	svc := NewService(db, contentmoderation.NewProvider(config.ContentSafetyConfig{Provider: "mock"}), evidence.NewService(db, evidence.MockProvider{}, "test"))
	creator := creators.CurrentCreator{UserID: userID, CreatorID: creatorID}
	in := SubmitRequest{AuthorizationID: authorizationID, WorkName: "测试二创作品", WorkType: "IMAGE", CreationDescription: "新增构图与叙事表达", CopyrightDeclaration: "本人确认提交材料真实合法", Declarations: map[string]bool{"SELF_CREATED": true, "AUTHORIZED_IP_MATERIAL": true, "NO_UNAUTHORIZED_THIRD_PARTY": true, "NO_THIRD_PARTY_RIGHTS_INFRINGEMENT": true, "AUTHENTIC_AND_LAWFUL": true}, NewExpressionRatio: 60, NewExpressionDimensions: []string{"构图", "叙事"}, NewExpressionDescription: "新增人物构图和故事叙事", OriginalIPUsageDescription: "使用已授权冻结素材", PreviewFileIDs: []uint64{previewID}, SourceFileIDs: []uint64{sourceID}, MaterialItemIDs: []uint64{itemID}}
	created, code, err := svc.Submit(context.Background(), creator, in)
	if err != nil || code != "" || created["status"] != "PENDING_COPYRIGHT_REVIEW" {
		t.Fatalf("submit=%#v code=%s err=%v", created, code, err)
	}
	workID := created["workId"].(uint64)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": fmt.Sprint(userID), "sid": fmt.Sprint(sessionID), "tv": "1", "clientType": "CREATOR_WEB", "exp": time.Now().Add(time.Hour).Unix()})
	tokenText, err := token.SignedString([]byte(cfg.Auth.JWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := router.Group("/api/v1")
	RegisterRoutes(api, svc, creators.RequireAuthentication(db, cfg.Auth), func(c *gin.Context) { c.Next() })
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/creator/works/%d", workID), nil)
	req.Header.Set("Authorization", "Bearer "+tokenText)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("work detail HTTP status=%d body=%s", rec.Code, rec.Body.String())
	}
	var versionRow struct{ ID uint64 }
	mustWork(t, db.Table("work_version").Select("id").Where("work_id=?", workID).Take(&versionRow).Error)
	versionID := versionRow.ID
	var present int64
	mustWork(t, db.Table("work_version").Where("id=?", versionID).Count(&present).Error)
	if present != 1 {
		t.Fatalf("work version %d missing", versionID)
	}
	reviewer := identity.CurrentAdmin{UserID: 1, PermissionCodes: map[string]struct{}{reviewPermission: {}}}
	if _, code, err = svc.Review(context.Background(), reviewer, versionID, ReviewRequest{ReviewNode: "COPYRIGHT_INITIAL", Result: "PASS"}); err != nil || code != "" {
		t.Fatalf("initial review code=%s err=%v", code, err)
	}
	if _, code, err = svc.Review(context.Background(), reviewer, versionID, ReviewRequest{ReviewNode: "COMPLIANCE_REVIEW", Result: "PASS"}); err != nil || code != "" {
		t.Fatalf("compliance review code=%s err=%v", code, err)
	}
	confirmed, code, err := svc.Confirm(context.Background(), reviewer, workID, ConfirmRequest{ReviewedNewExpressionRatio: 65, Comment: "通过"})
	if err != nil || code != "" || confirmed["status"] != "RIGHTS_CONFIRMED" {
		t.Fatalf("confirm=%#v code=%s err=%v", confirmed, code, err)
	}
	if _, code, err = svc.Resubmit(context.Background(), creator, workID, in); err != nil || code != "WORK_STATUS_INVALID" {
		t.Fatalf("confirmed resubmit code=%s err=%v", code, err)
	}
}

func mustWork(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func cleanupWorkFixture(db *gorm.DB, userID, creatorID, sessionID, ipID, orderID, authorizationID, packID uint64, fileIDs []uint64) {
	db.Exec("DELETE FROM work_material_usage WHERE work_version_id IN (SELECT id FROM work_version WHERE work_id IN (SELECT id FROM derivative_work WHERE creator_id=? AND ip_id=?))", creatorID, ipID)
	db.Exec("DELETE FROM work_file WHERE work_version_id IN (SELECT id FROM work_version WHERE work_id IN (SELECT id FROM derivative_work WHERE creator_id=? AND ip_id=?))", creatorID, ipID)
	db.Exec("DELETE FROM work_review_record WHERE work_version_id IN (SELECT id FROM work_version WHERE work_id IN (SELECT id FROM derivative_work WHERE creator_id=? AND ip_id=?))", creatorID, ipID)
	db.Exec("DELETE FROM work_rights_confirmation WHERE work_id IN (SELECT id FROM derivative_work WHERE creator_id=? AND ip_id=?)", creatorID, ipID)
	db.Exec("DELETE FROM work_version WHERE work_id IN (SELECT id FROM derivative_work WHERE creator_id=? AND ip_id=?)", creatorID, ipID)
	db.Exec("DELETE FROM derivative_work WHERE creator_id=? AND ip_id=?", creatorID, ipID)
	db.Exec("DELETE FROM creator_authorization WHERE id=?", authorizationID)
	db.Exec("DELETE FROM auth_order WHERE id=?", orderID)
	db.Exec("DELETE FROM ip_material_pack_item WHERE material_pack_id=?", packID)
	db.Exec("DELETE FROM ip_material_pack WHERE id=?", packID)
	db.Exec("DELETE FROM file_object WHERE id IN ?", fileIDs)
	db.Exec("DELETE FROM ip_asset WHERE id=?", ipID)
	db.Exec("DELETE FROM auth_session WHERE id=?", sessionID)
	db.Exec("DELETE FROM creator_profile WHERE id=?", creatorID)
	db.Exec("DELETE FROM sys_user WHERE id=?", userID)
	db.Exec("DELETE FROM sys_user WHERE id=(SELECT user_id FROM creator_profile WHERE id=?)", creatorID)
}
