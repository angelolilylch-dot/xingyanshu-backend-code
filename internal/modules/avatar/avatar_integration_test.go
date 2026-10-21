package avatar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
	archivezip "github.com/yeka/zip"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/cache"
	"xingyanshu-backend/internal/platform/filestore"
	"xingyanshu-backend/internal/platform/idgen"
)

// TestAvatarRegistrationHTTPWithMySQLRedis validates the AVATAR_H5 isolation
// boundary using actual MySQL and Redis, including the H5 three-factor match.
func TestCaptureRequirementValidation(t *testing.T) {
	if len(requiredCaptureSlots) != 9 {
		t.Fatalf("required slots=%d, want 9", len(requiredCaptureSlots))
	}
	if !validAsset("FACE_PHOTO", "FACE_01", "image/jpeg", photoMaxFileSize) || validAsset("FACE_PHOTO", "FACE_01", "image/png", 1) || validAsset("FACE_PHOTO", "FACE_01", "image/jpeg", photoMaxFileSize+1) {
		t.Fatal("photo MIME or size restriction is not enforced")
	}
	if !validAsset("VOICE_VIDEO", "VOICE_VIDEO", "video/mp4", videoMaxFileSize) || !validAsset("VOICE_VIDEO", "VOICE_VIDEO", "video/webm", videoMaxFileSize) || validAsset("VOICE_VIDEO", "VOICE_VIDEO", "video/quicktime", 1) || validAsset("VOICE_VIDEO", "VOICE_VIDEO", "video/mp4", videoMaxFileSize+1) {
		t.Fatal("video MIME or size restriction is not enforced")
	}
}

func TestAvatarRateLimitsWithRedis(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1")
	}
	t.Setenv("XINGYANSHU_COS_SECRET_ID", "avatar-rate-limit-placeholder")
	t.Setenv("XINGYANSHU_COS_SECRET_KEY", "avatar-rate-limit-placeholder")
	t.Setenv("XINGYANSHU_TMS_SECRET_ID", "avatar-rate-limit-placeholder")
	t.Setenv("XINGYANSHU_TMS_SECRET_KEY", "avatar-rate-limit-placeholder")
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	must(t, err)
	r, err := cache.OpenRedis(t.Context(), cfg.Redis)
	must(t, err)
	defer r.Close()
	settings := cfg.Avatar
	settings.SMSDailyLimit = 1
	settings.UploadCredentialParallel = 2
	settings.ProfilePollInterval = time.Minute
	s := NewServiceWithConfig(nil, r, cfg.Auth, "test", settings)
	id := idgen.Next()
	mobileHash := hash(fmt.Sprintf("avatar-rate-%d", id))
	dailyKey := fmt.Sprintf("avatar:sms:daily:%s:%s", time.Now().Format("20060102"), mobileHash)
	pendingKey := fmt.Sprintf("avatar:upload:pending:%d", id)
	pollKey := fmt.Sprintf("avatar:profile-poll:%d", id)
	t.Cleanup(func() { r.Del(t.Context(), dailyKey, pendingKey, pollKey) })
	if e := s.consumeDailySMSQuota(t.Context(), mobileHash); e != nil {
		t.Fatalf("first daily quota=%+v", e)
	}
	if e := s.consumeDailySMSQuota(t.Context(), mobileHash); e == nil || e.Code != "AVATAR_SMS_DAILY_LIMITED" || e.Status != http.StatusTooManyRequests {
		t.Fatalf("daily limit=%+v", e)
	}
	if e := s.reserveUploadCredential(t.Context(), id, "first"); e != nil {
		t.Fatalf("first credential=%+v", e)
	}
	if e := s.reserveUploadCredential(t.Context(), id, "second"); e != nil {
		t.Fatalf("second credential=%+v", e)
	}
	if e := s.reserveUploadCredential(t.Context(), id, "third"); e == nil || e.Code != "AVATAR_UPLOAD_CONCURRENCY_LIMITED" {
		t.Fatalf("upload concurrency=%+v", e)
	}
	s.releaseUploadCredential(t.Context(), id, "first")
	if e := s.reserveUploadCredential(t.Context(), id, "third"); e != nil {
		t.Fatalf("released credential=%+v", e)
	}
	cur := Current{AccountID: id}
	if e := s.limitProfilePoll(t.Context(), cur); e != nil {
		t.Fatalf("first poll=%+v", e)
	}
	if e := s.limitProfilePoll(t.Context(), cur); e == nil || e.Code != "AVATAR_PROFILE_POLL_RATE_LIMITED" {
		t.Fatalf("poll limit=%+v", e)
	}
}

func TestAvatarRegistrationHTTPWithMySQLRedis(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1")
	}
	// This registration test uses MySQL and Redis only. Supply non-secret
	// placeholders so the sanitized COS test configuration can still load.
	t.Setenv("XINGYANSHU_COS_SECRET_ID", "avatar-db-test-placeholder")
	t.Setenv("XINGYANSHU_COS_SECRET_KEY", "avatar-db-test-placeholder")
	t.Setenv("XINGYANSHU_TMS_SECRET_ID", "avatar-db-test-placeholder")
	t.Setenv("XINGYANSHU_TMS_SECRET_KEY", "avatar-db-test-placeholder")
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	r, err := cache.OpenRedis(context.Background(), cfg.Redis)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	base := idgen.Next()
	cityID, configID, qrID, batchID, memberID := base+1, base+2, base+3, base+4, base+5
	code, mobile, name := fmt.Sprintf("avatar-city-%d", base), "13900000001", "实名测试用户"
	// The number is intentionally stable for the real HTTP test; remove a key
	// left by an interrupted prior run before asserting the fresh-send flow.
	r.Del(context.Background(), "avatar:sms:login:"+hash(mobile))
	t.Cleanup(func() {
		db.Exec("DELETE FROM avatar_deletion_request WHERE profile_id IN (SELECT id FROM avatar_profile WHERE roster_member_id=?)", memberID)
		db.Exec("DELETE FROM avatar_collection_session WHERE profile_id IN (SELECT id FROM avatar_profile WHERE roster_member_id=?)", memberID)
		db.Exec("DELETE FROM avatar_consent WHERE account_id IN (SELECT id FROM avatar_h5_account WHERE mobile_hash=?)", hash(mobile))
		db.Exec("DELETE FROM avatar_access_audit WHERE actor_type='USER' AND actor_id IN (SELECT CAST(id AS CHAR) FROM avatar_h5_account WHERE mobile_hash=?)", hash(mobile))
		db.Exec("DELETE FROM avatar_profile WHERE roster_member_id=?", memberID)
		db.Exec("DELETE FROM avatar_identity_verification WHERE account_id IN (SELECT id FROM avatar_h5_account WHERE mobile_hash=?)", hash(mobile))
		db.Exec("DELETE FROM avatar_h5_session WHERE account_id IN (SELECT id FROM avatar_h5_account WHERE mobile_hash=?)", hash(mobile))
		db.Exec("DELETE FROM avatar_h5_account WHERE mobile_hash=?", hash(mobile))
		db.Exec("DELETE FROM avatar_h5_sms_code_log WHERE mobile_hash=?", hash(mobile))
		db.Exec("DELETE FROM avatar_qrcode_scan_log WHERE qrcode_id=?", qrID)
		db.Exec("DELETE FROM avatar_registration_qrcode WHERE id=?", qrID)
		db.Exec("DELETE FROM avatar_city_registration_config WHERE id=?", configID)
		db.Exec("DELETE FROM avatar_roster_member WHERE id=?", memberID)
		db.Exec("DELETE FROM avatar_roster_batch WHERE id=?", batchID)
		r.Del(context.Background(), "avatar:sms:login:"+hash(mobile))
	})
	phoneCipher, err := encrypt(cfg.Auth.JWTSecret, "400-800-1234")
	if err != nil {
		t.Fatal(err)
	}
	must(t, db.Table("avatar_roster_batch").Create(map[string]any{"id": batchID, "status": "COMPLETED", "version": 1, "batch_no": fmt.Sprintf("B-%d", base), "city_org_id": cityID, "city_code": "350100", "source_file_name": "test.xlsx", "source_file_hash": hash("test"), "total_count": 1, "success_count": 1, "imported_by": 1}).Error)
	must(t, db.Table("avatar_roster_member").Create(map[string]any{"id": memberID, "status": "ACTIVE", "version": 1, "roster_batch_id": batchID, "city_org_id": cityID, "city_code": "350100", "person_no": fmt.Sprintf("P-%d", base), "person_type": "PROFESSIONAL_MODEL", "name_cipher": mustEncrypt(cfg.Auth.JWTSecret, name), "name_hash": hash(name), "mobile_cipher": mustEncrypt(cfg.Auth.JWTSecret, mobile), "mobile_hash": hash(mobile), "mobile_masked": maskMobile(mobile), "gender": "FEMALE", "registration_status": "PENDING"}).Error)
	must(t, db.Table("avatar_city_registration_config").Create(map[string]any{"id": configID, "status": "ACTIVE", "version": 1, "city_org_id": cityID, "city_code": "350100", "support_contact_name": "福州运营方", "support_phone_cipher": phoneCipher, "support_phone_masked": "400****1234"}).Error)
	must(t, db.Table("avatar_registration_qrcode").Create(map[string]any{"id": qrID, "status": "ACTIVE", "version": 1, "qrcode_no": fmt.Sprintf("Q-%d", base), "city_registration_config_id": configID, "city_org_id": cityID, "city_code": "350100", "code_hash": hash(code)}).Error)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	store := &exportMemoryStorage{objects: map[string][]byte{}}
	service := NewServiceWithConfig(db, r, cfg.Auth, "test", cfg.Avatar, store)
	RegisterRoutes(router.Group("/api/v1"), service, RequireAuthentication(db, cfg.Auth))
	entry := callAvatar(t, router, http.MethodGet, "/api/v1/avatar/public/registration-context?code="+code, nil, "")
	if entry.Code != 200 || fmt.Sprint(entry.Data["supportPhone"]) != "400-800-1234" {
		t.Fatalf("entry=%+v", entry)
	}
	smsKey := fmt.Sprintf("avatar-sms-%d", base)
	sent := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/auth/sms/send", gin.H{"mobile": mobile, "scene": "AVATAR_LOGIN"}, "", smsKey)
	if sent.Code != 200 || fmt.Sprint(sent.Data["mockCode"]) != "123456" {
		t.Fatalf("sms=%+v", sent)
	}
	sentReplay := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/auth/sms/send", gin.H{"mobile": mobile, "scene": "AVATAR_LOGIN"}, "", smsKey)
	if sentReplay.Code != http.StatusOK || sentReplay.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("sms replay=%+v", sentReplay)
	}
	smsConflict := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/auth/sms/send", gin.H{"mobile": mobile, "scene": "OTHER"}, "", smsKey)
	if smsConflict.Code != http.StatusConflict || !strings.Contains(smsConflict.Body, `"code":"IDEMPOTENCY_CONFLICT"`) {
		t.Fatalf("sms conflict=%+v", smsConflict)
	}
	loginKey := fmt.Sprintf("avatar-login-%d", base)
	logged := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/auth/login", gin.H{"mobile": mobile, "smsCode": "123456"}, "", loginKey)
	if logged.Code != 200 || fmt.Sprint(logged.Data["accessToken"]) == "" {
		t.Fatalf("login=%+v", logged)
	}
	loggedReplay := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/auth/login", gin.H{"mobile": mobile, "smsCode": "123456"}, "", loginKey)
	if loggedReplay.Code != http.StatusOK || loggedReplay.Header.Get("Idempotency-Replayed") != "true" || fmt.Sprint(loggedReplay.Data["refreshToken"]) != fmt.Sprint(logged.Data["refreshToken"]) {
		t.Fatalf("login replay=%+v", loggedReplay)
	}
	if conflict := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/auth/login", gin.H{"mobile": mobile, "smsCode": "000000"}, "", loginKey); conflict.Code != http.StatusConflict {
		t.Fatalf("login conflict=%+v", conflict)
	}
	refreshKey := fmt.Sprintf("avatar-refresh-%d", base)
	refreshed := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/auth/refresh", gin.H{"refreshToken": fmt.Sprint(logged.Data["refreshToken"])}, "", refreshKey)
	if refreshed.Code != 200 || fmt.Sprint(refreshed.Data["accessToken"]) == "" {
		t.Fatalf("refresh=%+v", refreshed)
	}
	refreshedReplay := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/auth/refresh", gin.H{"refreshToken": fmt.Sprint(logged.Data["refreshToken"])}, "", refreshKey)
	if refreshedReplay.Code != http.StatusOK || refreshedReplay.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("refresh replay=%+v", refreshedReplay)
	}
	if conflict := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/auth/refresh", gin.H{"refreshToken": fmt.Sprint(logged.Data["refreshToken"]), "retry": true}, "", refreshKey); conflict.Code != http.StatusConflict {
		t.Fatalf("refresh conflict=%+v", conflict)
	}
	token := fmt.Sprint(refreshed.Data["accessToken"])
	identityKey := fmt.Sprintf("avatar-identity-%d", base)
	identity := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/identity-verifications", gin.H{"verificationMethod": "MOCK"}, token, identityKey)
	if identity.Code != http.StatusCreated {
		t.Fatalf("identity=%+v", identity)
	}
	identityReplay := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/identity-verifications", gin.H{"verificationMethod": "MOCK"}, token, identityKey)
	if identityReplay.Code != http.StatusCreated || identityReplay.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("identity replay=%+v", identityReplay)
	}
	if conflict := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/identity-verifications", gin.H{"verificationMethod": "OTHER"}, token, identityKey); conflict.Code != http.StatusConflict {
		t.Fatalf("identity conflict=%+v", conflict)
	}
	mismatch := callAvatar(t, router, http.MethodPost, "/api/v1/avatar/roster-binding/confirm", gin.H{"registrationCode": code, "name": "不匹配姓名", "gender": "FEMALE"}, token)
	if mismatch.Code != http.StatusUnprocessableEntity || !strings.Contains(mismatch.Body, `"code":"AVATAR_ROSTER_MATCH_NOT_FOUND"`) {
		t.Fatalf("mismatch=%+v", mismatch)
	}
	bound := callAvatar(t, router, http.MethodPost, "/api/v1/avatar/roster-binding/confirm", gin.H{"registrationCode": code, "name": name, "gender": "FEMALE"}, token)
	if bound.Code != 200 || fmt.Sprint(bound.Data["bindingStatus"]) != "BOUND" {
		t.Fatalf("bound=%+v", bound)
	}
	consentKey := fmt.Sprintf("avatar-consent-%d", base)
	consentBody := gin.H{"consentType": "COLLECT_AND_ARCHIVE", "agreementVersion": "v1.0", "accepted": true}
	consent := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/consents", consentBody, token, consentKey)
	if consent.Code != 201 {
		t.Fatalf("consent=%+v", consent)
	}
	consentReplay := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/consents", consentBody, token, consentKey)
	if consentReplay.Code != http.StatusCreated || consentReplay.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("consent replay=%+v", consentReplay)
	}
	if conflict := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/consents", gin.H{"consentType": "COLLECT_AND_ARCHIVE", "agreementVersion": "v1.0", "accepted": false}, token, consentKey); conflict.Code != http.StatusConflict {
		t.Fatalf("consent conflict=%+v", conflict)
	}
	collectionKey := fmt.Sprintf("avatar-collection-%d", base)
	collection := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/collection-sessions", nil, token, collectionKey)
	if collection.Code != 201 || fmt.Sprint(collection.Data["photoRequiredCount"]) != "8" {
		t.Fatalf("collection=%+v", collection)
	}
	if len(collection.Data["requiredSlots"].([]any)) != 9 {
		t.Fatalf("requiredSlots=%+v", collection.Data["requiredSlots"])
	}
	collectionReplay := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/collection-sessions", nil, token, collectionKey)
	if collectionReplay.Code != http.StatusCreated || collectionReplay.Header.Get("Idempotency-Replayed") != "true" || fmt.Sprint(collectionReplay.Data["sessionId"]) != fmt.Sprint(collection.Data["sessionId"]) {
		t.Fatalf("collection replay=%+v", collectionReplay)
	}
	if conflict := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/collection-sessions", gin.H{}, token, collectionKey); conflict.Code != http.StatusConflict {
		t.Fatalf("collection conflict=%+v", conflict)
	}
	bodyKey := fmt.Sprintf("avatar-body-%d", base)
	bodyBody := gin.H{"gender": "FEMALE", "heightCm": 168, "bodyType": "SLIM", "bustCm": 82, "waistCm": 61, "hipsCm": 88}
	body := callAvatarWithKey(t, router, http.MethodPatch, "/api/v1/avatar/collection-sessions/"+fmt.Sprint(collection.Data["sessionId"])+"/body-data", bodyBody, token, bodyKey)
	if body.Code != 200 || fmt.Sprint(body.Data["status"]) != "SAVED" {
		t.Fatalf("body=%+v", body)
	}
	bodyReplay := callAvatarWithKey(t, router, http.MethodPatch, "/api/v1/avatar/collection-sessions/"+fmt.Sprint(collection.Data["sessionId"])+"/body-data", bodyBody, token, bodyKey)
	if bodyReplay.Code != http.StatusOK || bodyReplay.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("body replay=%+v", bodyReplay)
	}
	if conflict := callAvatarWithKey(t, router, http.MethodPatch, "/api/v1/avatar/collection-sessions/"+fmt.Sprint(collection.Data["sessionId"])+"/body-data", gin.H{"gender": "FEMALE", "heightCm": 169, "bodyType": "SLIM"}, token, bodyKey); conflict.Code != http.StatusConflict {
		t.Fatalf("body conflict=%+v", conflict)
	}
	var bodyStored struct{ BodyDataJSONCipher []byte }
	must(t, db.Table("avatar_collection_session").Select("body_data_json_cipher").Where("id=?", collection.Data["sessionId"]).Take(&bodyStored).Error)
	if len(bodyStored.BodyDataJSONCipher) == 0 || bytes.Contains(bodyStored.BodyDataJSONCipher, []byte("SLIM")) {
		t.Fatalf("body data is not encrypted")
	}
	must(t, db.Table("avatar_collection_session").Where("id=?", collection.Data["sessionId"]).Update("status", "PASSED").Error)
	must(t, db.Table("avatar_profile").Where("roster_member_id=?", memberID).Updates(map[string]any{"status": "ARCHIVED", "collection_status": "PASSED", "quality_status": "PASSED", "archive_status": "ARCHIVED"}).Error)
	reshootKey := fmt.Sprintf("avatar-reshoot-%d", base)
	reshootBody := gin.H{"reason": "主动更新采集资料"}
	reshoot := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/profiles/me/reshoot", reshootBody, token, reshootKey)
	if reshoot.Code != 201 || fmt.Sprint(reshoot.Data["reshoot"]) != "true" || fmt.Sprint(reshoot.Data["sessionId"]) == fmt.Sprint(collection.Data["sessionId"]) {
		t.Fatalf("reshoot=%+v", reshoot)
	}
	var inheritedAssets int64
	must(t, db.Table("avatar_asset").Where("collection_session_id=?", reshoot.Data["sessionId"]).Count(&inheritedAssets).Error)
	if inheritedAssets != 0 {
		t.Fatalf("reshoot reused %d old assets", inheritedAssets)
	}
	reshootReplay := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/profiles/me/reshoot", reshootBody, token, reshootKey)
	if reshootReplay.Code != http.StatusCreated || reshootReplay.Header.Get("Idempotency-Replayed") != "true" || fmt.Sprint(reshootReplay.Data["sessionId"]) != fmt.Sprint(reshoot.Data["sessionId"]) {
		t.Fatalf("reshoot replay=%+v", reshootReplay)
	}
	if conflict := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/profiles/me/reshoot", gin.H{"reason": "不同原因"}, token, reshootKey); conflict.Code != http.StatusConflict {
		t.Fatalf("reshoot conflict=%+v", conflict)
	}
	reshootSessionID := fmt.Sprint(reshoot.Data["sessionId"])
	for i, slot := range requiredCaptureSlots {
		captureSlot, assetType := slot["captureSlot"].(string), slot["assetType"].(string)
		mimeType := "image/jpeg"
		if assetType == "VOICE_VIDEO" {
			mimeType = "video/mp4"
		}
		content := []byte(fmt.Sprintf("avatar-asset-%d-%s", base, captureSlot))
		credentialBody := gin.H{"assetType": assetType, "captureSlot": captureSlot, "contentHash": hash(string(content)), "mimeType": mimeType, "fileSize": len(content)}
		credentialKey := fmt.Sprintf("avatar-credential-%d-%d", base, i)
		credentialPath := "/api/v1/avatar/collection-sessions/" + reshootSessionID + "/assets/upload-credentials"
		credential := callAvatarWithKey(t, router, http.MethodPost, credentialPath, credentialBody, token, credentialKey)
		if credential.Code != http.StatusOK {
			t.Fatalf("credential slot=%s result=%+v", captureSlot, credential)
		}
		if i == 0 {
			replay := callAvatarWithKey(t, router, http.MethodPost, credentialPath, credentialBody, token, credentialKey)
			if replay.Code != http.StatusOK || replay.Header.Get("Idempotency-Replayed") != "true" {
				t.Fatalf("credential replay=%+v", replay)
			}
			conflict := callAvatarWithKey(t, router, http.MethodPost, credentialPath, gin.H{"assetType": assetType, "captureSlot": captureSlot, "contentHash": hash("different"), "mimeType": mimeType, "fileSize": len(content)}, token, credentialKey)
			if conflict.Code != http.StatusConflict {
				t.Fatalf("credential conflict=%+v", conflict)
			}
		}
		key := strings.TrimPrefix(fmt.Sprint(credential.Data["uploadURL"]), "https://example.test/upload/")
		store.objects[key] = content
		confirmPath := "/api/v1/avatar/collection-sessions/" + reshootSessionID + "/assets/confirm"
		confirmBody := gin.H{"uploadToken": credential.Data["uploadToken"], "contentHash": hash(string(content))}
		confirmKey := fmt.Sprintf("avatar-confirm-%d-%d", base, i)
		confirmed := callAvatarWithKey(t, router, http.MethodPost, confirmPath, confirmBody, token, confirmKey)
		if confirmed.Code != http.StatusOK {
			t.Fatalf("confirm slot=%s result=%+v", captureSlot, confirmed)
		}
		if i == 0 {
			replay := callAvatarWithKey(t, router, http.MethodPost, confirmPath, confirmBody, token, confirmKey)
			if replay.Code != http.StatusOK || replay.Header.Get("Idempotency-Replayed") != "true" {
				t.Fatalf("confirm replay=%+v", replay)
			}
			if conflict := callAvatarWithKey(t, router, http.MethodPost, confirmPath, gin.H{"uploadToken": credential.Data["uploadToken"], "contentHash": hash("different")}, token, confirmKey); conflict.Code != http.StatusConflict {
				t.Fatalf("confirm conflict=%+v", conflict)
			}
		}
	}
	submitPath := "/api/v1/avatar/collection-sessions/" + reshootSessionID + "/submit"
	submitKey := fmt.Sprintf("avatar-submit-%d", base)
	submitted := callAvatarWithKey(t, router, http.MethodPost, submitPath, nil, token, submitKey)
	if submitted.Code != http.StatusOK || fmt.Sprint(submitted.Data["status"]) != "AUTO_QUALITY_CHECKING" {
		t.Fatalf("submit=%+v", submitted)
	}
	submittedReplay := callAvatarWithKey(t, router, http.MethodPost, submitPath, nil, token, submitKey)
	if submittedReplay.Code != http.StatusOK || submittedReplay.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("submit replay=%+v", submittedReplay)
	}
	if conflict := callAvatarWithKey(t, router, http.MethodPost, submitPath, gin.H{}, token, submitKey); conflict.Code != http.StatusConflict {
		t.Fatalf("submit conflict=%+v", conflict)
	}
	must(t, db.Table("avatar_profile").Where("roster_member_id=?", memberID).Updates(map[string]any{"status": "ARCHIVED", "collection_status": "PASSED", "quality_status": "PASSED", "archive_status": "ARCHIVED"}).Error)
	deletionKey := fmt.Sprintf("avatar-delete-%d", base)
	deletionBody := gin.H{"reason": "测试删除状态"}
	deletion := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/profiles/me/deletion-requests", deletionBody, token, deletionKey)
	if deletion.Code != 201 {
		t.Fatalf("deletion=%+v", deletion)
	}
	deletionReplay := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/profiles/me/deletion-requests", deletionBody, token, deletionKey)
	if deletionReplay.Code != http.StatusCreated || deletionReplay.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("deletion replay=%+v", deletionReplay)
	}
	if conflict := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/profiles/me/deletion-requests", gin.H{"reason": "不同原因"}, token, deletionKey); conflict.Code != http.StatusConflict {
		t.Fatalf("deletion conflict=%+v", conflict)
	}
	latestDeletion := callAvatar(t, router, http.MethodGet, "/api/v1/avatar/profiles/me/deletion-requests/latest", nil, token)
	if latestDeletion.Code != 200 || fmt.Sprint(latestDeletion.Data["status"]) != "PENDING" {
		t.Fatalf("latest deletion=%+v", latestDeletion)
	}
	logoutKey := fmt.Sprintf("avatar-logout-%d", base)
	loggedOut := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/auth/logout", nil, token, logoutKey)
	if loggedOut.Code != http.StatusOK {
		t.Fatalf("logout=%+v", loggedOut)
	}
	loggedOutReplay := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/auth/logout", nil, token, logoutKey)
	if loggedOutReplay.Code != http.StatusOK || loggedOutReplay.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("logout replay=%+v", loggedOutReplay)
	}
	logoutConflict := callAvatarWithKey(t, router, http.MethodPost, "/api/v1/avatar/auth/logout", gin.H{"retry": true}, token, logoutKey)
	if logoutConflict.Code != http.StatusConflict {
		t.Fatalf("logout conflict=%+v", logoutConflict)
	}
	var member struct {
		BoundAccountID     uint64
		RegistrationStatus string
	}
	must(t, db.Table("avatar_roster_member").Select("bound_account_id,registration_status").Where("id=?", memberID).Take(&member).Error)
	if member.BoundAccountID == 0 || member.RegistrationStatus != "BOUND" {
		t.Fatalf("member=%+v", member)
	}
}

func TestAvatarCityOperationsWithMySQL(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	must(t, err)
	db, err := database.OpenMySQLConfig(cfg.Database)
	must(t, err)
	b := idgen.Next()
	city := b + 1
	profileID := b + 2
	var batchID uint64
	admin := identity.CurrentAdmin{UserID: b + 3, OrgID: city, CityCode: "350100", RoleCodes: []string{"CITY_OPERATOR"}, PermissionCodes: map[string]struct{}{"AVATAR_ROSTER_MANAGE": {}, "AVATAR_REGISTRATION_ENTRY_MANAGE": {}, "AVATAR_INVITATION_NOTIFY": {}, "AVATAR_PROGRESS_VIEW": {}}}
	t.Cleanup(func() {
		db.Exec("DELETE FROM avatar_sms_notification WHERE roster_member_id IN (SELECT id FROM avatar_roster_member WHERE roster_batch_id=?)", batchID)
		db.Exec("DELETE FROM avatar_profile WHERE id=?", profileID)
		db.Exec("DELETE FROM avatar_registration_qrcode WHERE city_org_id=?", city)
		db.Exec("DELETE FROM avatar_city_registration_config WHERE city_org_id=?", city)
		db.Exec("DELETE FROM avatar_roster_import_error WHERE roster_batch_id=?", batchID)
		db.Exec("DELETE FROM avatar_roster_member WHERE roster_batch_id=?", batchID)
		db.Exec("DELETE FROM avatar_roster_batch WHERE id=?", batchID)
	})
	x := excelize.NewFile()
	sheet := x.GetSheetName(0)
	_ = x.SetSheetRow(sheet, "A1", &[]any{"姓名", "身份证号", "手机号", "性别"})
	_ = x.SetSheetRow(sheet, "A2", &[]any{"测试甲", "11010519491231002X", "13900000101", "男"})
	_ = x.SetSheetRow(sheet, "A3", &[]any{"测试乙", "11010519491231003X", "13900000101", "女"})
	raw, err := x.WriteToBuffer()
	must(t, err)
	x.Close()
	form := bytes.Buffer{}
	mw := multipart.NewWriter(&form)
	part, err := mw.CreateFormFile("file", "roster.xlsx")
	must(t, err)
	_, err = part.Write(raw.Bytes())
	must(t, err)
	must(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, "/", &form)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	must(t, req.ParseMultipartForm(int64(form.Len()+1024)))
	file, fileHeader, err := req.FormFile("file")
	must(t, err)
	defer file.Close()
	s := NewServiceWithConfig(db, nil, cfg.Auth, "test", cfg.Avatar)
	data, ae := s.importRoster(t.Context(), admin, fileHeader)
	if ae != nil {
		t.Fatalf("import=%+v", ae)
	}
	fmt.Sscan(fmt.Sprint(data.(gin.H)["batchId"]), &batchID)
	if batchID == 0 {
		t.Fatalf("import data=%+v", data)
	}
	var result struct {
		Status                    string
		SuccessCount, FailedCount int
	}
	must(t, db.Table("avatar_roster_batch").Select("status,success_count,failed_count").Where("id=?", batchID).Take(&result).Error)
	if result.Status != "PENDING_IDENTITY" || result.SuccessCount != 0 || result.FailedCount != 1 {
		t.Fatalf("batch=%+v", result)
	}
	processed, err := s.ProcessPendingRosterIdentity(t.Context(), 10)
	must(t, err)
	if processed != 1 {
		t.Fatalf("identity processed=%d", processed)
	}
	must(t, db.Table("avatar_roster_batch").Select("status,success_count,failed_count").Where("id=?", batchID).Take(&result).Error)
	if result.Status != "PARTIAL_FAILED" || result.SuccessCount != 1 || result.FailedCount != 1 {
		t.Fatalf("verified batch=%+v", result)
	}
	var verified struct {
		Status, IdentityProviderCode string
		IDNumberCipher               []byte
	}
	must(t, db.Table("avatar_roster_member").Select("status,identity_provider_code,id_number_cipher").Where("roster_batch_id=?", batchID).Take(&verified).Error)
	if verified.Status != "ACTIVE" || verified.IdentityProviderCode != "MOCK" || len(verified.IDNumberCipher) == 0 || bytes.Contains(verified.IDNumberCipher, []byte("11010519491231002X")) {
		t.Fatalf("verified roster identity=%+v", verified)
	}
	batchDetail, ae := s.rosterBatchDetail(t.Context(), admin, fmt.Sprint(batchID))
	if ae != nil || fmt.Sprint(batchDetail.(map[string]any)["status"]) != "PARTIAL_FAILED" {
		t.Fatalf("batch detail=%+v err=%+v", batchDetail, ae)
	}
	batches, ae := s.listRosterBatches(t.Context(), admin)
	if ae != nil || len(batches.(gin.H)["items"].([]map[string]any)) == 0 {
		t.Fatalf("batches=%+v err=%+v", batches, ae)
	}
	qr, ae := s.createQRCode(t.Context(), admin)
	if ae != nil || fmt.Sprint(qr.(gin.H)["shortCode"]) == "" || !strings.HasPrefix(fmt.Sprint(qr.(gin.H)["registrationURL"]), "https://avatar-h5.test.xingyanshu.example/register?code=") {
		t.Fatalf("qr=%+v err=%+v", qr, ae)
	}
	entry, ae := s.registrationContext(t.Context(), fmt.Sprint(qr.(gin.H)["shortCode"]), "127.0.0.1")
	if ae != nil || fmt.Sprint(entry.(gin.H)["supportPhone"]) != cfg.Avatar.FallbackSupportPhone || entry.(gin.H)["operatorName"] != nil {
		t.Fatalf("entry=%+v err=%+v", entry, ae)
	}
	var member uint64
	must(t, db.Table("avatar_roster_member").Select("id").Where("roster_batch_id=?", batchID).Scan(&member).Error)
	notice, ae := s.sendInvitation(t.Context(), admin, fmt.Sprint(member), notificationRequest{})
	if ae != nil || fmt.Sprint(notice.(gin.H)["status"]) != "SENT" {
		t.Fatalf("notice=%+v err=%+v", notice, ae)
	}
	must(t, db.Table("avatar_profile").Create(map[string]any{"id": profileID, "status": "NOT_STARTED", "version": 1, "profile_no": fmt.Sprintf("OPS-%d", profileID), "roster_member_id": member, "account_id": profileID, "city_org_id": city, "city_code": "350100", "real_name_status": "PENDING", "collection_status": "NOT_STARTED", "quality_status": "NOT_STARTED", "archive_status": "NOT_STARTED"}).Error)
	profiles, ae := s.listCityProfiles(t.Context(), admin)
	if ae != nil || len(profiles.(gin.H)["items"].([]gin.H)) == 0 {
		t.Fatalf("profiles=%+v err=%+v", profiles, ae)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("identity.currentAdmin", admin); c.Next() })
	RegisterAdminRoutes(router.Group("/api/v1"), s, func(c *gin.Context) { c.Next() })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/avatar/roster-batches/%d/errors", batchID), nil))
	if w.Code != http.StatusOK || !bytes.HasPrefix(w.Body.Bytes(), []byte("PK")) {
		t.Fatalf("error receipt status=%d contentType=%q bytes=%d", w.Code, w.Header().Get("Content-Type"), w.Body.Len())
	}
}

func TestAvatarQualityReviewWithMySQLRedis(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	must(t, err)
	db, err := database.OpenMySQLConfig(cfg.Database)
	must(t, err)
	r, err := cache.OpenRedis(t.Context(), cfg.Redis)
	must(t, err)
	defer r.Close()
	b := idgen.Next()
	city, batch, member, profile, session, asset := b+1, b+2, b+3, b+4, b+5, b+6
	admin := identity.CurrentAdmin{UserID: b + 7, RoleCodes: []string{"PLATFORM_ADMIN"}, PermissionCodes: map[string]struct{}{"AVATAR_QUALITY_REVIEW": {}}}
	t.Cleanup(func() {
		db.Exec("DELETE FROM avatar_access_audit WHERE object_id IN (?,?)", session, asset)
		db.Exec("DELETE FROM avatar_quality_review WHERE profile_id=?", profile)
		db.Exec("DELETE FROM avatar_asset WHERE id=?", asset)
		db.Exec("DELETE FROM avatar_collection_session WHERE id=?", session)
		db.Exec("DELETE FROM avatar_profile WHERE id=?", profile)
		db.Exec("DELETE FROM avatar_roster_member WHERE city_org_id=?", city)
		db.Exec("DELETE FROM avatar_roster_batch WHERE id=?", batch)
	})
	mobile := fmt.Sprintf("139%08d", b%100000000)
	must(t, db.Table("avatar_roster_batch").Create(map[string]any{"id": batch, "status": "COMPLETED", "version": 1, "batch_no": fmt.Sprintf("QRB-%d", batch), "city_org_id": city, "city_code": "350100", "source_file_name": "test.xlsx", "source_file_hash": hash("quality"), "imported_by": admin.UserID}).Error)
	must(t, db.Table("avatar_roster_member").Create(map[string]any{"id": member, "status": "ACTIVE", "version": 1, "roster_batch_id": batch, "city_org_id": city, "city_code": "350100", "person_no": fmt.Sprintf("QRM-%d", member), "person_type": "PROFESSIONAL_MODEL", "name_cipher": mustEncrypt(cfg.Auth.JWTSecret, "质检测试"), "name_hash": hash("质检测试"), "mobile_cipher": mustEncrypt(cfg.Auth.JWTSecret, mobile), "mobile_hash": hash(mobile), "mobile_masked": maskMobile(mobile), "gender": "MALE", "registration_status": "BOUND", "profile_id": profile}).Error)
	must(t, db.Table("avatar_profile").Create(map[string]any{"id": profile, "status": "AUTO_QUALITY_CHECKING", "version": 1, "profile_no": fmt.Sprintf("QP-%d", profile), "roster_member_id": member, "account_id": profile, "city_org_id": city, "city_code": "350100", "real_name_status": "PASSED", "collection_status": "SUBMITTED", "quality_status": "PENDING", "archive_status": "NOT_STARTED"}).Error)
	must(t, db.Table("avatar_collection_session").Create(map[string]any{"id": session, "status": "SUBMITTED", "version": 1, "collection_no": fmt.Sprintf("QS-%d", session), "profile_id": profile, "version_no": 1}).Error)
	must(t, db.Table("avatar_asset").Create(map[string]any{"id": asset, "status": "CONFIRMED", "version": 1, "asset_no": fmt.Sprintf("QA-%d", asset), "profile_id": profile, "collection_session_id": session, "asset_type": "FACE_PHOTO", "capture_slot": "FACE_01", "storage_key_cipher": mustEncrypt(cfg.Auth.JWTSecret, "avatar/quality-test"), "cipher_algorithm": "AES", "key_reference": "test", "content_hash": hash("preview"), "mime_type": "image/jpeg", "file_size": 7}).Error)
	s := NewService(db, r, cfg.Auth, "test", &exportMemoryStorage{objects: map[string][]byte{"avatar/quality-test": []byte("preview")}})
	queued, ae := s.queueQualityReview(t.Context(), admin, fmt.Sprint(session), qualityQueueRequest{ReviewType: "SAMPLE", ReasonCode: "MANUAL_SAMPLE"}, "127.0.0.1")
	if ae != nil {
		t.Fatalf("queue=%+v", ae)
	}
	rid := fmt.Sprint(queued.(gin.H)["qualityReviewId"])
	previews, ae := s.issueQualityPreviews(t.Context(), admin, rid, "127.0.0.1")
	if ae != nil || len(previews.(gin.H)["items"].([]gin.H)) != 1 {
		t.Fatalf("previews=%+v err=%+v", previews, ae)
	}
	previewToken := fmt.Sprint(previews.(gin.H)["items"].([]gin.H)[0]["previewToken"])
	if n := r.Exists(t.Context(), "avatar:quality-preview:"+previewToken).Val(); n != 1 {
		t.Fatalf("preview token missing token=%q exists=%d", previewToken, n)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("identity.currentAdmin", admin); c.Next() })
	RegisterAdminRoutes(router.Group("/api/v1"), s, func(c *gin.Context) { c.Next() })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/avatar/quality-previews/"+previewToken, nil))
	if w.Code != http.StatusOK || w.Body.String() != "preview" {
		t.Fatalf("preview status=%d body=%q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/avatar/quality-previews/"+previewToken, nil))
	if w.Code != http.StatusGone {
		t.Fatalf("reused preview status=%d", w.Code)
	}
	var pendingReview struct {
		Status  string
		Version uint
	}
	must(t, db.Table("avatar_quality_review").Select("status,version").Where("id=?", rid).Take(&pendingReview).Error)
	if pendingReview.Status != "PENDING" || pendingReview.Version != 1 {
		t.Fatalf("pending review=%+v", pendingReview)
	}
	decided, ae := s.decideQualityReview(t.Context(), admin, rid, qualityDecisionRequest{Version: 1, Decision: "REJECTED", ReasonCode: "QUALITY_REJECTED"}, "127.0.0.1")
	if ae != nil || fmt.Sprint(decided.(gin.H)["decision"]) != "REJECTED" {
		t.Fatalf("decision=%+v err=%+v", decided, ae)
	}
	var old struct{ Status, RegistrationStatus string }
	must(t, db.Table("avatar_roster_member").Select("status,registration_status").Where("id=?", member).Take(&old).Error)
	if old.Status != "INACTIVE" || old.RegistrationStatus != "REJECTED" {
		t.Fatalf("old=%+v", old)
	}
	must(t, db.Table("avatar_roster_member").Create(map[string]any{"id": member + 100, "status": "ACTIVE", "version": 1, "roster_batch_id": batch, "city_org_id": city, "city_code": "350100", "person_no": fmt.Sprintf("QRM-%d", member+100), "person_type": "PROFESSIONAL_MODEL", "name_cipher": mustEncrypt(cfg.Auth.JWTSecret, "质检测试"), "name_hash": hash("质检测试"), "mobile_cipher": mustEncrypt(cfg.Auth.JWTSecret, mobile), "mobile_hash": hash(mobile), "mobile_masked": maskMobile(mobile), "gender": "MALE", "registration_status": "PENDING"}).Error)
}

func TestAvatarTestIdentityRouteAbsentOutsideTest(t *testing.T) {
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), NewService(nil, nil, config.AuthConfig{}, "production"), func(c *gin.Context) { c.Next() })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/avatar/test/identity-verifications/1/complete", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("production route status=%d", w.Code)
	}
}

func TestAvatarQualityArchiveWithMySQL(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	must(t, err)
	db, err := database.OpenMySQLConfig(cfg.Database)
	must(t, err)
	b := idgen.Next()
	profile, session, task, consent, asset := b+1, b+2, b+3, b+4, b+5
	t.Cleanup(func() {
		db.Exec("DELETE FROM avatar_asset WHERE id=?", asset)
		db.Exec("DELETE FROM avatar_processing_task WHERE id=?", task)
		db.Exec("DELETE FROM avatar_consent WHERE id=?", consent)
		db.Exec("DELETE FROM avatar_collection_session WHERE id=?", session)
		db.Exec("DELETE FROM avatar_profile WHERE id=?", profile)
	})
	n := time.Now()
	must(t, db.Table("avatar_profile").Create(map[string]any{"id": profile, "status": "AUTO_QUALITY_CHECKING", "version": 1, "profile_no": fmt.Sprintf("P-%d", profile), "roster_member_id": profile, "account_id": profile, "city_org_id": 1, "city_code": "350100", "real_name_status": "PASSED", "collection_status": "SUBMITTED", "quality_status": "PENDING", "archive_status": "NOT_STARTED"}).Error)
	must(t, db.Table("avatar_collection_session").Create(map[string]any{"id": session, "status": "SUBMITTED", "version": 1, "collection_no": fmt.Sprintf("C-%d", session), "profile_id": profile, "version_no": 1, "submitted_at": n}).Error)
	must(t, db.Table("avatar_consent").Create(map[string]any{"id": consent, "status": "ACTIVE", "version": 1, "consent_no": fmt.Sprintf("X-%d", consent), "profile_id": profile, "account_id": profile, "consent_type": "COLLECT_AND_ARCHIVE", "agreement_version": "v1", "agreement_digest": hash("v1"), "purpose_snapshot": "test", "retention_days": 365, "retention_notice_snapshot": "test", "signed_at": n}).Error)
	must(t, db.Table("avatar_asset").Create(map[string]any{"id": asset, "status": "CONFIRMED", "version": 1, "asset_no": fmt.Sprintf("A-%d", asset), "profile_id": profile, "collection_session_id": session, "asset_type": "VOICE_VIDEO", "capture_slot": "VOICE_VIDEO", "storage_key_cipher": []byte("x"), "cipher_algorithm": "AES", "key_reference": "test", "content_hash": hash("x"), "mime_type": "video/mp4", "file_size": 1}).Error)
	must(t, db.Table("avatar_processing_task").Create(map[string]any{"id": task, "status": "PENDING", "version": 1, "task_no": fmt.Sprintf("T-%d", task), "profile_id": profile, "collection_session_id": session, "task_type": "AUTO_QUALITY", "input_digest": hash("task")}).Error)
	count, err := NewService(db, nil, cfg.Auth, "test").ProcessDueQuality(t.Context(), 5)
	must(t, err)
	if count < 1 {
		t.Fatal("quality task not processed")
	}
	var p struct {
		Status, ArchiveStatus string
		RetentionUntil        *time.Time
	}
	must(t, db.Table("avatar_profile").Select("status,archive_status,retention_until").Where("id=?", profile).Take(&p).Error)
	if p.Status != "ARCHIVED" || p.ArchiveStatus != "ARCHIVED" || p.RetentionUntil == nil {
		t.Fatalf("profile=%+v", p)
	}
}

func TestAvatarProgressExportWithMySQL(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	must(t, err)
	db, err := database.OpenMySQLConfig(cfg.Database)
	must(t, err)
	b := idgen.Next()
	profile, job := b+1, b+2
	t.Cleanup(func() {
		db.Exec("DELETE FROM avatar_export_item WHERE export_job_id=?", job)
		db.Exec("DELETE FROM avatar_export_job WHERE id=?", job)
		db.Exec("DELETE FROM avatar_profile WHERE id=?", profile)
	})
	must(t, db.Table("avatar_profile").Create(map[string]any{"id": profile, "status": "ARCHIVED", "version": 1, "profile_no": fmt.Sprintf("EP-%d", profile), "roster_member_id": profile, "account_id": profile, "city_org_id": profile, "city_code": "350100", "real_name_status": "PASSED", "collection_status": "PASSED", "quality_status": "PASSED", "archive_status": "ARCHIVED"}).Error)
	must(t, db.Table("avatar_export_job").Create(map[string]any{"id": job, "status": "PENDING", "version": 1, "job_no": fmt.Sprintf("EJ-%d", job), "export_type": "PROGRESS_LIST", "requester_id": 1, "requester_org_id": profile, "requester_role": "CITY_OPERATOR", "city_org_id": profile, "purpose_code": "", "filter_snapshot_json": "{}"}).Error)
	store := &exportMemoryStorage{}
	n, err := NewService(db, nil, cfg.Auth, "test", store).ProcessDueExports(t.Context(), 5)
	must(t, err)
	if n < 1 || len(store.value) == 0 {
		t.Fatalf("export count=%d bytes=%d", n, len(store.value))
	}
	var row struct {
		Status, ResultFileName string
		ItemCount              uint
	}
	must(t, db.Table("avatar_export_job").Select("status,result_file_name,item_count").Where("id=?", job).Take(&row).Error)
	if row.Status != "SUCCEEDED" || row.ItemCount != 1 || row.ResultFileName == "" {
		t.Fatalf("job=%+v", row)
	}
}

func TestAvatarArchivePackageWithMySQL(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	must(t, err)
	db, err := database.OpenMySQLConfig(cfg.Database)
	must(t, err)
	b := idgen.Next()
	profile, asset, job := b+1, b+2, b+3
	t.Cleanup(func() {
		db.Exec("DELETE FROM avatar_export_item WHERE export_job_id=?", job)
		db.Exec("DELETE FROM avatar_export_job WHERE id=?", job)
		db.Exec("DELETE FROM avatar_asset WHERE id=?", asset)
		db.Exec("DELETE FROM avatar_profile WHERE id=?", profile)
	})
	must(t, db.Table("avatar_profile").Create(map[string]any{"id": profile, "status": "ARCHIVED", "version": 1, "profile_no": fmt.Sprintf("AP-%d", profile), "roster_member_id": profile, "account_id": profile, "city_org_id": profile, "city_code": "350100", "real_name_status": "PASSED", "collection_status": "PASSED", "quality_status": "PASSED", "archive_status": "ARCHIVED"}).Error)
	must(t, db.Table("avatar_asset").Create(map[string]any{"id": asset, "status": "CONFIRMED", "version": 1, "asset_no": fmt.Sprintf("AA-%d", asset), "profile_id": profile, "collection_session_id": asset, "asset_type": "VOICE_VIDEO", "capture_slot": "VOICE_VIDEO", "storage_key_cipher": mustEncrypt(cfg.Auth.JWTSecret, "avatar/test-archive-source"), "cipher_algorithm": "AES-256-GCM", "key_reference": "test", "content_hash": hash("archive-content"), "mime_type": "video/mp4", "file_size": 15}).Error)
	must(t, db.Table("avatar_export_job").Create(map[string]any{"id": job, "status": "PENDING", "version": 1, "job_no": fmt.Sprintf("AZ-%d", job), "export_type": "ARCHIVE_PACKAGE", "requester_id": 1, "requester_org_id": 1, "requester_role": "PLATFORM_ADMIN", "city_org_id": profile, "purpose_code": "COMPLIANCE", "filter_snapshot_json": fmt.Sprintf(`{"profileIds":[%d]}`, profile)}).Error)
	store := &exportMemoryStorage{objects: map[string][]byte{"avatar/test-archive-source": []byte("archive-content")}}
	n, err := NewService(db, nil, cfg.Auth, "test", store).ProcessDueExports(t.Context(), 5)
	must(t, err)
	if n != 1 {
		t.Fatalf("archive export count=%d", n)
	}
	var row struct {
		Status, ResultKeyCipher, PasswordCipher, EncryptionKeyReference string
		ItemCount                                                       uint
	}
	must(t, db.Table("avatar_export_job").Select("status,result_key_cipher,password_cipher,encryption_key_reference,item_count").Where("id=?", job).Take(&row).Error)
	if row.Status != "SUCCEEDED" || row.ItemCount != 1 || row.EncryptionKeyReference != archiveZIPKeyReference || row.PasswordCipher == "" {
		t.Fatalf("archive job=%+v", row)
	}
	password, err := decrypt(cfg.Auth.JWTSecret, []byte(row.PasswordCipher))
	must(t, err)
	key, err := decrypt(cfg.Auth.JWTSecret, []byte(row.ResultKeyCipher))
	must(t, err)
	zipBody := store.objects[key]
	if len(zipBody) == 0 || bytes.Contains(zipBody, []byte(password)) || bytes.Contains(zipBody, []byte("archive-content")) {
		t.Fatal("archive package is missing or exposes password/plain asset bytes")
	}
	zr, err := archivezip.NewReader(bytes.NewReader(zipBody), int64(len(zipBody)))
	must(t, err)
	if len(zr.File) != 2 {
		t.Fatalf("zip entries=%d", len(zr.File))
	}
	for _, f := range zr.File {
		if !f.IsEncrypted() {
			t.Fatalf("zip entry %s is not encrypted", f.Name)
		}
		f.SetPassword(password)
		r, err := f.Open()
		must(t, err)
		_, err = io.ReadAll(r)
		r.Close()
		must(t, err)
	}
	var item struct {
		Status             string
		IncludedAssetCount uint
	}
	must(t, db.Table("avatar_export_item").Select("status,included_asset_count").Where("export_job_id=? AND profile_id=?", job, profile).Take(&item).Error)
	if item.Status != "INCLUDED" || item.IncludedAssetCount != 1 {
		t.Fatalf("archive item=%+v", item)
	}
}

// TestAvatarArchivePackageWithRealCOS is opt-in because it writes short-lived
// test objects to the configured private COS bucket and removes them afterwards.
func TestAvatarArchivePackageWithRealCOS(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_COS_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_COS_TESTS=1")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	must(t, err)
	db, err := database.OpenMySQLConfig(cfg.Database)
	must(t, err)
	store, err := filestore.New(cfg.File.Storage)
	must(t, err)
	deleter, ok := store.(interface {
		Delete(context.Context, string) error
	})
	if !ok {
		t.Fatal("configured storage does not support test-object cleanup")
	}
	b := idgen.Next()
	profile, asset, job := b+1, b+2, b+3
	sourceKey := fmt.Sprintf("avatar/integration/%d/source.mp4", b)
	var outputKey string
	t.Cleanup(func() {
		if outputKey != "" {
			_ = deleter.Delete(context.Background(), outputKey)
		}
		_ = deleter.Delete(context.Background(), sourceKey)
		db.Exec("DELETE FROM avatar_export_item WHERE export_job_id=?", job)
		db.Exec("DELETE FROM avatar_export_job WHERE id=?", job)
		db.Exec("DELETE FROM avatar_asset WHERE id=?", asset)
		db.Exec("DELETE FROM avatar_profile WHERE id=?", profile)
	})
	source := []byte("real-cos-archive-content")
	must(t, store.Put(t.Context(), sourceKey, "video/mp4", bytes.NewReader(source)))
	must(t, db.Table("avatar_profile").Create(map[string]any{"id": profile, "status": "ARCHIVED", "version": 1, "profile_no": fmt.Sprintf("CP-%d", profile), "roster_member_id": profile, "account_id": profile, "city_org_id": profile, "city_code": "350100", "real_name_status": "PASSED", "collection_status": "PASSED", "quality_status": "PASSED", "archive_status": "ARCHIVED"}).Error)
	must(t, db.Table("avatar_asset").Create(map[string]any{"id": asset, "status": "CONFIRMED", "version": 1, "asset_no": fmt.Sprintf("CA-%d", asset), "profile_id": profile, "collection_session_id": asset, "asset_type": "VOICE_VIDEO", "capture_slot": "VOICE_VIDEO", "storage_key_cipher": mustEncrypt(cfg.Auth.JWTSecret, sourceKey), "cipher_algorithm": "AES-256-GCM", "key_reference": "test", "content_hash": hash(string(source)), "mime_type": "video/mp4", "file_size": len(source)}).Error)
	must(t, db.Table("avatar_export_job").Create(map[string]any{"id": job, "status": "PENDING", "version": 1, "job_no": fmt.Sprintf("CZ-%d", job), "export_type": "ARCHIVE_PACKAGE", "requester_id": 1, "requester_org_id": 1, "requester_role": "PLATFORM_ADMIN", "city_org_id": profile, "purpose_code": "COMPLIANCE", "filter_snapshot_json": fmt.Sprintf(`{"profileIds":[%d]}`, profile)}).Error)
	n, err := NewService(db, nil, cfg.Auth, "test", store).ProcessDueExports(t.Context(), 5)
	must(t, err)
	if n != 1 {
		t.Fatalf("real COS archive export count=%d", n)
	}
	var row struct{ Status, ResultKeyCipher, PasswordCipher string }
	must(t, db.Table("avatar_export_job").Select("status,result_key_cipher,password_cipher").Where("id=?", job).Take(&row).Error)
	if row.Status != "SUCCEEDED" {
		t.Fatalf("real COS archive status=%s", row.Status)
	}
	outputKey, err = decrypt(cfg.Auth.JWTSecret, []byte(row.ResultKeyCipher))
	must(t, err)
	password, err := decrypt(cfg.Auth.JWTSecret, []byte(row.PasswordCipher))
	must(t, err)
	r, err := store.Get(t.Context(), outputKey)
	must(t, err)
	zipBody, err := io.ReadAll(r)
	r.Close()
	must(t, err)
	zr, err := archivezip.NewReader(bytes.NewReader(zipBody), int64(len(zipBody)))
	must(t, err)
	for _, f := range zr.File {
		f.SetPassword(password)
		content, err := f.Open()
		must(t, err)
		body, err := io.ReadAll(content)
		content.Close()
		must(t, err)
		if filepath.Ext(f.Name) == ".mp4" && !bytes.Equal(body, source) {
			t.Fatal("real COS archive asset content differs from source")
		}
	}
}

func TestAvatarDeletionPurgeWithMySQL(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	must(t, err)
	db, err := database.OpenMySQLConfig(cfg.Database)
	must(t, err)
	b := idgen.Next()
	profile, asset, request := b+1, b+2, b+3
	t.Cleanup(func() {
		db.Exec("DELETE FROM avatar_asset WHERE id=?", asset)
		db.Exec("DELETE FROM avatar_deletion_request WHERE id=?", request)
		db.Exec("DELETE FROM avatar_profile WHERE id=?", profile)
	})
	must(t, db.Table("avatar_profile").Create(map[string]any{"id": profile, "status": "DELETION_PROCESSING", "version": 1, "profile_no": fmt.Sprintf("DP-%d", profile), "roster_member_id": profile, "account_id": profile, "city_org_id": 1, "city_code": "350100", "real_name_status": "PASSED", "collection_status": "PASSED", "quality_status": "PASSED", "archive_status": "ARCHIVED"}).Error)
	must(t, db.Table("avatar_asset").Create(map[string]any{"id": asset, "status": "CONFIRMED", "version": 1, "asset_no": fmt.Sprintf("DA-%d", asset), "profile_id": profile, "collection_session_id": asset, "asset_type": "VOICE_VIDEO", "capture_slot": "VOICE_VIDEO", "storage_key_cipher": mustEncrypt(cfg.Auth.JWTSecret, "avatar/test-delete"), "cipher_algorithm": "AES", "key_reference": "test", "content_hash": hash("x"), "mime_type": "video/mp4", "file_size": 1}).Error)
	must(t, db.Table("avatar_deletion_request").Create(map[string]any{"id": request, "status": "PROCESSING", "version": 1, "request_no": fmt.Sprintf("DR-%d", request), "profile_id": profile, "account_id": profile, "requested_at": time.Now()}).Error)
	store := &exportMemoryStorage{}
	n, err := NewService(db, nil, cfg.Auth, "test", store).ProcessDueDeletion(t.Context(), 5)
	must(t, err)
	if n < 1 || store.deleted != "avatar/test-delete" {
		t.Fatalf("purged=%d deleted=%q", n, store.deleted)
	}
	var p struct{ Status string }
	must(t, db.Table("avatar_profile").Select("status").Where("id=?", profile).Take(&p).Error)
	if p.Status != "DELETED" {
		t.Fatalf("profile=%+v", p)
	}
	var d struct{ Status string }
	must(t, db.Table("avatar_deletion_request").Select("status").Where("id=?", request).Take(&d).Error)
	if d.Status != "COMPLETED" {
		t.Fatalf("deletion=%+v", d)
	}
}

func TestAvatarArchivePasswordOnceWithMySQL(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	must(t, err)
	db, err := database.OpenMySQLConfig(cfg.Database)
	must(t, err)
	old := idgen.Next()
	var regenerated uint64
	admin := identity.CurrentAdmin{UserID: old + 1, OrgID: 1, RoleCodes: []string{"PLATFORM_ADMIN"}}
	t.Cleanup(func() {
		db.Exec("DELETE FROM avatar_access_audit WHERE object_id IN (?, ?)", old, regenerated)
		if regenerated != 0 {
			db.Exec("DELETE FROM avatar_export_job WHERE id IN (?, ?)", old, regenerated)
			return
		}
		db.Exec("DELETE FROM avatar_export_job WHERE id = ?", old)
	})
	must(t, db.Table("avatar_export_job").Create(map[string]any{
		"id": old, "status": "SUCCEEDED", "version": 1, "job_no": fmt.Sprintf("PW-%d", old),
		"export_type": "ARCHIVE_PACKAGE", "requester_id": admin.UserID, "requester_org_id": 1,
		"requester_role": "PLATFORM_ADMIN", "city_org_id": 1, "purpose_code": "COMPLIANCE",
		"filter_snapshot_json": "{}", "password_cipher": mustEncrypt(cfg.Auth.JWTSecret, "only-once"),
	}).Error)

	s := NewService(db, nil, cfg.Auth, "test")
	first, apiErr := s.revealExportPassword(t.Context(), admin, fmt.Sprint(old), "127.0.0.1")
	if apiErr != nil || fmt.Sprint(first.(gin.H)["password"]) != "only-once" {
		t.Fatalf("first=%+v err=%v", first, apiErr)
	}
	_, apiErr = s.revealExportPassword(t.Context(), admin, fmt.Sprint(old), "127.0.0.1")
	if apiErr == nil || apiErr.Code != "AVATAR_EXPORT_PASSWORD_UNAVAILABLE" {
		t.Fatalf("second=%+v", apiErr)
	}
	next, apiErr := s.regenerateExport(t.Context(), admin, fmt.Sprint(old), "127.0.0.1")
	if apiErr != nil || fmt.Sprint(next.(gin.H)["replacesExportJobId"]) != fmt.Sprint(old) {
		t.Fatalf("regen=%+v err=%v", next, apiErr)
	}
	fmt.Sscan(fmt.Sprint(next.(gin.H)["exportJobId"]), &regenerated)
	if regenerated == 0 || regenerated == old {
		t.Fatalf("regenerated export job id=%d", regenerated)
	}
	var status string
	must(t, db.Table("avatar_export_job").Select("status").Where("id = ?", old).Scan(&status).Error)
	if status != "REVOKED" {
		t.Fatalf("old status=%s", status)
	}
}

type exportMemoryStorage struct {
	value   []byte
	deleted string
	objects map[string][]byte
}

func (s *exportMemoryStorage) Put(_ context.Context, key string, _ string, r io.Reader) error {
	b, e := io.ReadAll(r)
	s.value = b
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	s.objects[key] = b
	return e
}

func (s *exportMemoryStorage) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if b, ok := s.objects[key]; ok {
		return io.NopCloser(bytes.NewReader(b)), nil
	}
	return io.NopCloser(bytes.NewReader(s.value)), nil
}
func (s *exportMemoryStorage) SignDownloadURL(context.Context, string, time.Duration) (string, error) {
	return "https://example.test/download", nil
}
func (s *exportMemoryStorage) SignUploadURL(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	return "https://example.test/upload/" + key, nil
}
func (s *exportMemoryStorage) Delete(_ context.Context, key string) error {
	s.deleted = key
	return nil
}

type avatarResult struct {
	Code   int
	Data   map[string]any
	Body   string
	Header http.Header
}

func callAvatar(t *testing.T, r http.Handler, method, path string, body any, token string) avatarResult {
	return callAvatarWithKey(t, r, method, path, body, token, fmt.Sprintf("avatar-test-%d", idgen.Next()))
}

func callAvatarWithKey(t *testing.T, r http.Handler, method, path string, body any, token, key string) avatarResult {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	q := httptest.NewRequest(method, path, bytes.NewReader(raw))
	q.Header.Set("Content-Type", "application/json")
	if method == http.MethodPost || method == http.MethodPatch || method == http.MethodPut || method == http.MethodDelete {
		q.Header.Set("Idempotency-Key", key)
	}
	if token != "" {
		q.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	var payload struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	return avatarResult{Code: w.Code, Data: payload.Data, Body: w.Body.String(), Header: w.Header()}
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

var _ = time.Second
