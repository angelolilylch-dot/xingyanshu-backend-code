package avatar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/platform/filestore"
	"xingyanshu-backend/internal/platform/idgen"
)

// TestTencentImageRecognitionWithLiveRosterPhoto is an opt-in test fixture for
// an authorized real-person test. It never embeds identity data or credentials
// in source: all values are read from the invoking process environment.
func TestTencentImageRecognitionWithLiveRosterPhoto(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_FACEID_LIVE_TEST") != "1" {
		t.Skip("set XINGYANSHU_RUN_FACEID_LIVE_TEST=1 for an authorized Tencent FaceID call")
	}
	name := strings.TrimSpace(os.Getenv("XINGYANSHU_FACEID_TEST_NAME"))
	idNumber := strings.ToUpper(strings.TrimSpace(os.Getenv("XINGYANSHU_FACEID_TEST_ID_NUMBER")))
	mobile := strings.TrimSpace(os.Getenv("XINGYANSHU_FACEID_TEST_MOBILE"))
	imagePath := strings.TrimSpace(os.Getenv("XINGYANSHU_FACEID_TEST_IMAGE_PATH"))
	if name == "" || !validChineseIDNumber(idNumber) || !mobilePattern.MatchString(mobile) || imagePath == "" {
		t.Fatal("set XINGYANSHU_FACEID_TEST_NAME, XINGYANSHU_FACEID_TEST_ID_NUMBER, XINGYANSHU_FACEID_TEST_MOBILE and XINGYANSHU_FACEID_TEST_IMAGE_PATH")
	}
	image, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatalf("read FACE_01: %v", err)
	}
	if len(image) == 0 || len(image) > 3*1024*1024 || !strings.EqualFold(filepath.Ext(imagePath), ".jpg") && !strings.EqualFold(filepath.Ext(imagePath), ".jpeg") && !strings.EqualFold(filepath.Ext(imagePath), ".png") {
		t.Fatal("FACE_01 must be a jpg/jpeg/png no larger than 3 MiB")
	}
	cfg, err := config.Load("../../../config/test.yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	storage, err := filestore.New(cfg.File.Storage)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	service := NewServiceWithConfig(db, nil, cfg.Auth, cfg.App.Environment, cfg.Avatar, storage)
	if _, unavailable := service.faceIDV.(unavailableFaceIdentityVerifier); unavailable {
		t.Fatal("Tencent FaceID provider is not configured")
	}
	var city struct {
		ID      uint64
		OrgCode string `gorm:"column:org_code"`
	}
	if err := db.Table("sys_org").Select("id,org_code").Where("org_type='CITY' AND status='ACTIVE'").Order("id ASC").Take(&city).Error; err != nil {
		t.Fatalf("find active city organization: %v", err)
	}
	var duplicate int64
	if err := db.Table("avatar_roster_member").Where("city_org_id=? AND (mobile_hash=? OR id_number_hash=?)", city.ID, hash(mobile), hash(idNumber)).Count(&duplicate).Error; err != nil {
		t.Fatal(err)
	}
	var profileID, sessionID, verificationID uint64
	if duplicate != 0 {
		var existing struct {
			VerificationID, ProfileID, CollectionSessionID uint64
		}
		if err := db.Table("avatar_identity_verification v").Select("v.id verification_id,v.profile_id,v.collection_session_id").Joins("JOIN avatar_roster_member m ON m.id=v.roster_member_id").Where("m.city_org_id=? AND m.mobile_hash=? AND v.verification_type=?", city.ID, hash(mobile), faceIdentityVerificationType).Order("v.created_at DESC").Take(&existing).Error; err != nil {
			t.Fatalf("find existing live face identity fixture: %v", err)
		}
		verificationID, profileID, sessionID = existing.VerificationID, existing.ProfileID, existing.CollectionSessionID
		if err := db.Table("avatar_identity_verification").Where("id=?", verificationID).Updates(map[string]any{"status": "PENDING", "provider_request_no": nil, "provider_result_code": nil, "face_similarity": nil, "status_reason_code": nil, "status_reason": nil, "verified_at": nil, "updated_at": time.Now(), "status_changed_at": time.Now(), "status_changed_by_type": "SYSTEM"}).Error; err != nil {
			t.Fatalf("requeue existing live face identity fixture: %v", err)
		}
	} else {
		now := time.Now()
		batchID, memberID, accountID, assetID := idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next()
		profileID, sessionID, verificationID = idgen.Next(), idgen.Next(), idgen.Next()
		storageKey := fmt.Sprintf("avatar/private/%d/%d/live-faceid/FACE_01", profileID, sessionID)
		contentHash := fmt.Sprintf("%x", sha256.Sum256(image))
		if err := storage.Put(context.Background(), storageKey, "image/jpeg", bytes.NewReader(image)); err != nil {
			t.Fatalf("store FACE_01: %v", err)
		}
		// GORM transaction is kept explicit below so the roster, profile, asset and
		// pending verification are either all visible to the worker or none are.
		err = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Table("avatar_roster_batch").Create(map[string]any{"id": batchID, "status": "COMPLETED", "version": 1, "batch_no": fmt.Sprintf("LIVE-FACEID-%d", batchID), "city_org_id": city.ID, "city_code": city.OrgCode, "source_file_name": "authorized-live-faceid-test", "source_file_hash": contentHash, "total_count": 1, "success_count": 1, "imported_by": 1, "imported_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; err != nil {
				return err
			}
			if err := tx.Table("avatar_h5_account").Create(map[string]any{"id": accountID, "status": "ACTIVE", "version": 1, "account_no": fmt.Sprintf("AVH-%d", accountID), "mobile_cipher": mustEncrypt(cfg.Auth.JWTSecret, mobile), "mobile_hash": hash(mobile), "mobile_masked": maskMobile(mobile), "last_login_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; err != nil {
				return err
			}
			if err := tx.Table("avatar_roster_member").Create(map[string]any{"id": memberID, "status": "ACTIVE", "version": 1, "roster_batch_id": batchID, "city_org_id": city.ID, "city_code": city.OrgCode, "person_no": fmt.Sprintf("LIVE-%d", memberID), "person_type": "PROFESSIONAL_MODEL", "name_cipher": mustEncrypt(cfg.Auth.JWTSecret, name), "name_hash": hash(name), "id_number_cipher": mustEncrypt(cfg.Auth.JWTSecret, idNumber), "id_number_hash": hash(idNumber), "id_number_last4": idNumber[len(idNumber)-4:], "mobile_cipher": mustEncrypt(cfg.Auth.JWTSecret, mobile), "mobile_hash": hash(mobile), "mobile_masked": maskMobile(mobile), "gender": "MALE", "registration_status": "BOUND", "bound_account_id": accountID, "profile_id": profileID, "identity_provider_code": "LIVE_FACEID", "identity_verified_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; err != nil {
				return err
			}
			if err := tx.Table("avatar_profile").Create(map[string]any{"id": profileID, "status": "COLLECTING", "version": 1, "profile_no": fmt.Sprintf("AVP-%d", profileID), "roster_member_id": memberID, "account_id": accountID, "city_org_id": city.ID, "city_code": city.OrgCode, "current_collection_session_id": sessionID, "current_version_no": 1, "real_name_status": "PENDING", "collection_status": "COLLECTING", "quality_status": "NOT_STARTED", "archive_status": "NOT_STARTED", "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; err != nil {
				return err
			}
			if err := tx.Table("avatar_collection_session").Create(map[string]any{"id": sessionID, "status": "COLLECTING", "version": 1, "collection_no": fmt.Sprintf("AVCS-%d", sessionID), "profile_id": profileID, "version_no": 1, "photo_required_count": 8, "photo_uploaded_count": 1, "video_required": true, "video_uploaded": false, "expires_at": now.Add(24 * time.Hour), "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; err != nil {
				return err
			}
			if err := tx.Table("avatar_asset").Create(map[string]any{"id": assetID, "status": "CONFIRMED", "version": 1, "asset_no": fmt.Sprintf("AVA-%d", assetID), "profile_id": profileID, "collection_session_id": sessionID, "asset_type": "FACE_PHOTO", "capture_slot": "FACE_01", "storage_key_cipher": mustEncrypt(cfg.Auth.JWTSecret, storageKey), "cipher_algorithm": "AES-256-GCM", "key_reference": "APP_AUTH_SECRET_V1", "content_hash": contentHash, "mime_type": "image/jpeg", "file_size": len(image), "quality_status": "PENDING", "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; err != nil {
				return err
			}
			return tx.Table("avatar_identity_verification").Create(map[string]any{"id": verificationID, "status": "PENDING", "version": 1, "account_id": accountID, "profile_id": profileID, "roster_member_id": memberID, "collection_session_id": sessionID, "asset_id": assetID, "verification_no": fmt.Sprintf("AVI-%d", verificationID), "provider_code": "TENCENT_IMAGE_RECOGNITION_V2", "verification_type": faceIdentityVerificationType, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error
		})
		if err != nil {
			t.Fatalf("create live face identity fixture: %v", err)
		}
	}
	count, err := service.ProcessPendingFaceIdentity(context.Background(), 1)
	if err != nil || count != 1 {
		t.Fatalf("process Tencent FaceID verification count=%d err=%v", count, err)
	}
	var result struct {
		Status, ProviderRequestNo, ProviderResultCode string
		FaceSimilarity                                float64
	}
	if err := db.Table("avatar_identity_verification").Select("status,provider_request_no,provider_result_code,face_similarity").Where("id=?", verificationID).Take(&result).Error; err != nil {
		t.Fatal(err)
	}
	if result.ProviderRequestNo == "" || (result.Status != "PASSED" && result.Status != "REJECTED") {
		t.Fatalf("unexpected live FaceID result: status=%s providerResult=%s requestIDPresent=%t", result.Status, result.ProviderResultCode, result.ProviderRequestNo != "")
	}
	t.Logf("live Tencent FaceID stored: profileID=%d collectionSessionID=%d verificationID=%d status=%s similarity=%.2f requestID=%s", profileID, sessionID, verificationID, result.Status, result.FaceSimilarity, result.ProviderRequestNo)
}
