package avatar

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"io"
	"strings"
	"time"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idgen"
)

type uploadGrant struct {
	SessionID, ProfileID                               uint64
	AssetType, CaptureSlot, ContentHash, MIMEType, Key string
	FileSize                                           int64
}

type assetCancelRequest struct {
	UploadToken string `json:"uploadToken" binding:"required,min=16,max=256"`
}
type uploadSigner interface {
	SignUploadURL(context.Context, string, string, time.Duration) (string, error)
}

func (s *Service) assetCredentials(ctx context.Context, cur Current, idText string, r assetCredentialRequest) (any, *httpx.AppError) {
	id, ok := parseUint(idText)
	if !ok || !validAsset(r.AssetType, r.CaptureSlot, r.MIMEType, r.FileSize) || len(r.ContentHash) != 64 {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "素材参数不正确", nil)
	}
	var row struct {
		ID, ProfileID uint64
		Status        string
	}
	if e := s.db.WithContext(ctx).Table("avatar_collection_session cs").Select("cs.id,cs.profile_id,cs.status").Joins("JOIN avatar_profile p ON p.id=cs.profile_id").Where("cs.id=? AND p.account_id=?", id, cur.AccountID).Take(&row).Error; e != nil || row.Status != "COLLECTING" {
		return nil, avatarError(404, "AVATAR_COLLECTION_NOT_FOUND", "采集会话不存在或不可上传", nil)
	}
	if s.storage == nil {
		return nil, avatarError(503, "AVATAR_STORAGE_UNAVAILABLE", "素材存储暂不可用", nil)
	}
	token, e := randomToken()
	if e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "创建上传凭证失败", nil)
	}
	key := fmt.Sprintf("avatar/private/%d/%d/%s/%s", row.ProfileID, row.ID, token, r.CaptureSlot)
	signer, ok := s.storage.(uploadSigner)
	if !ok {
		return nil, avatarError(503, "AVATAR_STORAGE_UNAVAILABLE", "素材存储暂不支持直传", nil)
	}
	if e := s.reserveUploadCredential(ctx, row.ID, token); e != nil {
		return nil, e
	}
	url, e := signer.SignUploadURL(ctx, key, r.MIMEType, 10*time.Minute)
	if e != nil {
		s.releaseUploadCredential(ctx, row.ID, token)
		return nil, avatarError(503, "AVATAR_STORAGE_UNAVAILABLE", "素材存储暂不可用", nil)
	}
	raw, _ := json.Marshal(uploadGrant{row.ID, row.ProfileID, r.AssetType, r.CaptureSlot, strings.ToLower(r.ContentHash), r.MIMEType, key, r.FileSize})
	if e = s.redis.Set(ctx, "avatar:upload:"+token, raw, 10*time.Minute).Err(); e != nil {
		s.releaseUploadCredential(ctx, row.ID, token)
		return nil, avatarError(503, "AVATAR_UPLOAD_TOKEN_UNAVAILABLE", "上传凭证暂不可用", nil)
	}
	return gin.H{"uploadToken": token, "uploadURL": url, "expiresIn": 600, "method": "PUT", "headers": gin.H{"Content-Type": r.MIMEType}}, nil
}
func validSlot(t, slot string) bool {
	if t == "VOICE_VIDEO" {
		return slot == "VOICE_VIDEO"
	}
	if t != "FACE_PHOTO" {
		return false
	}
	if len(slot) != 7 || !strings.HasPrefix(slot, "FACE_0") {
		return false
	}
	return slot[6] >= '1' && slot[6] <= '8'
}

func validAsset(assetType, slot, mimeType string, size int64) bool {
	if !validSlot(assetType, slot) || size <= 0 {
		return false
	}
	if assetType == "FACE_PHOTO" {
		return mimeType == "image/jpeg" && size <= photoMaxFileSize
	}
	return (mimeType == "video/mp4" || mimeType == "video/webm") && size <= videoMaxFileSize
}
func (s *Service) confirmAsset(ctx context.Context, cur Current, idText string, r assetConfirmRequest) (any, *httpx.AppError) {
	id, ok := parseUint(idText)
	if !ok {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "采集会话不正确", nil)
	}
	raw, e := s.redis.Get(ctx, "avatar:upload:"+r.UploadToken).Bytes()
	if e != nil {
		return nil, avatarError(422, "AVATAR_UPLOAD_TOKEN_INVALID", "上传凭证已失效", nil)
	}
	var g uploadGrant
	if json.Unmarshal(raw, &g) != nil || g.SessionID != id || g.ContentHash != strings.ToLower(r.ContentHash) {
		return nil, avatarError(422, "AVATAR_UPLOAD_TOKEN_INVALID", "上传凭证不匹配", nil)
	}
	if s.storage == nil {
		return nil, avatarError(503, "AVATAR_STORAGE_UNAVAILABLE", "素材存储暂不可用", nil)
	}
	object, e := s.storage.Get(ctx, g.Key)
	if e != nil {
		return nil, avatarError(422, "AVATAR_UPLOAD_NOT_FOUND", "未检测到已上传素材", nil)
	}
	checksum := sha256.New()
	size, copyErr := io.Copy(checksum, object)
	closeErr := object.Close()
	if copyErr != nil || closeErr != nil || size != g.FileSize || fmt.Sprintf("%x", checksum.Sum(nil)) != g.ContentHash {
		return nil, avatarError(422, "AVATAR_UPLOAD_INTEGRITY_INVALID", "素材大小或完整性校验失败", nil)
	}
	var p uint64
	if e := s.db.WithContext(ctx).Table("avatar_collection_session cs").Select("p.id").Joins("JOIN avatar_profile p ON p.id=cs.profile_id").Where("cs.id=? AND p.account_id=? AND cs.status='COLLECTING'", id, cur.AccountID).Scan(&p).Error; e != nil || p != g.ProfileID {
		return nil, avatarError(404, "AVATAR_COLLECTION_NOT_FOUND", "采集会话不存在或不可确认", nil)
	}
	assetID := idgen.Next()
	now := time.Now()
	if e = s.db.WithContext(ctx).Table("avatar_asset").Where("collection_session_id=? AND asset_type=? AND capture_slot=?", id, g.AssetType, g.CaptureSlot).Delete(nil).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "替换素材失败", nil)
	}
	if e := s.db.WithContext(ctx).Table("avatar_asset").Create(map[string]any{"id": assetID, "status": "CONFIRMED", "version": 1, "asset_no": fmt.Sprintf("AVA-%d", assetID), "profile_id": g.ProfileID, "collection_session_id": id, "asset_type": g.AssetType, "capture_slot": g.CaptureSlot, "storage_key_cipher": mustEncrypt(s.auth.JWTSecret, g.Key), "cipher_algorithm": "AES-256-GCM", "key_reference": "APP_AUTH_SECRET_V1", "content_hash": g.ContentHash, "mime_type": g.MIMEType, "file_size": g.FileSize, "quality_status": "PENDING", "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(cur.AccountID)}).Error; e != nil {
		return nil, avatarError(409, "RESOURCE_CONFLICT", "素材确认冲突", nil)
	}
	s.redis.Del(ctx, "avatar:upload:"+r.UploadToken)
	s.releaseUploadCredential(ctx, g.SessionID, r.UploadToken)
	return gin.H{"assetId": fmt.Sprint(assetID), "captureSlot": g.CaptureSlot, "status": "CONFIRMED"}, nil
}

// cancelAssetUpload immediately releases a credential reservation when the
// client abandons an upload. The signed COS URL remains time-limited, but can
// no longer be confirmed after the token is removed.
func (s *Service) cancelAssetUpload(ctx context.Context, cur Current, idText string, r assetCancelRequest) (any, *httpx.AppError) {
	id, ok := parseUint(idText)
	if !ok || strings.TrimSpace(r.UploadToken) == "" {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "上传取消参数不正确", nil)
	}
	raw, e := s.redis.Get(ctx, "avatar:upload:"+r.UploadToken).Bytes()
	if e != nil {
		// Cancellation is intentionally repeatable so a client can safely retry
		// after a network interruption or a first successful cancellation.
		return gin.H{"uploadToken": r.UploadToken, "status": "CANCELLED"}, nil
	}
	var g uploadGrant
	if json.Unmarshal(raw, &g) != nil || g.SessionID != id {
		return nil, avatarError(422, "AVATAR_UPLOAD_TOKEN_INVALID", "上传凭证不匹配", nil)
	}
	var owned uint64
	if e := s.db.WithContext(ctx).Table("avatar_collection_session cs").Select("cs.id").Joins("JOIN avatar_profile p ON p.id=cs.profile_id").Where("cs.id=? AND p.account_id=? AND cs.status='COLLECTING'", id, cur.AccountID).Scan(&owned).Error; e != nil || owned == 0 {
		return nil, avatarError(404, "AVATAR_COLLECTION_NOT_FOUND", "采集会话不存在或不可取消", nil)
	}
	if e := s.redis.Del(ctx, "avatar:upload:"+r.UploadToken).Err(); e != nil {
		return nil, avatarError(503, "AVATAR_UPLOAD_TOKEN_UNAVAILABLE", "上传凭证暂不可用", nil)
	}
	s.releaseUploadCredential(ctx, g.SessionID, r.UploadToken)
	return gin.H{"uploadToken": r.UploadToken, "status": "CANCELLED"}, nil
}
func (s *Service) submitCollection(ctx context.Context, cur Current, idText string) (any, *httpx.AppError) {
	id, ok := parseUint(idText)
	if !ok {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "采集会话不正确", nil)
	}
	if s.faceIDVErr != nil {
		return nil, avatarError(503, "AVATAR_FACE_IDENTITY_PROVIDER_NOT_CONFIGURED", "本人一致性核验服务暂不可用", nil)
	}
	var assets []struct{ AssetType, CaptureSlot string }
	s.db.WithContext(ctx).Table("avatar_asset a").Select("a.asset_type,a.capture_slot").Joins("JOIN avatar_collection_session cs ON cs.id=a.collection_session_id JOIN avatar_profile p ON p.id=cs.profile_id").Where("a.collection_session_id=? AND p.account_id=? AND a.status='CONFIRMED'", id, cur.AccountID).Find(&assets)
	confirmed := map[string]bool{}
	var photos, videos int64
	for _, asset := range assets {
		confirmed[asset.CaptureSlot] = true
		if asset.AssetType == "FACE_PHOTO" {
			photos++
		} else if asset.AssetType == "VOICE_VIDEO" {
			videos++
		}
	}
	missing := make([]string, 0)
	for _, item := range requiredCaptureSlots {
		if !confirmed[item["captureSlot"].(string)] {
			missing = append(missing, item["captureSlot"].(string))
		}
	}
	if len(missing) > 0 {
		return nil, avatarError(422, "AVATAR_COLLECTION_INCOMPLETE", "请完成8张照片和朗读视频采集", gin.H{"photoCount": photos, "videoCount": videos, "missingSlots": missing})
	}
	var taskID uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ProfileID, RosterMemberID uint64
			Status                    string
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_collection_session cs").Select("cs.profile_id,p.roster_member_id,cs.status").Joins("JOIN avatar_profile p ON p.id=cs.profile_id").Where("cs.id=? AND p.account_id=?", id, cur.AccountID).Take(&row).Error; e != nil {
			return e
		}
		if row.Status != "COLLECTING" {
			return fmt.Errorf("invalid status")
		}
		if _, unavailable := s.faceIDV.(unavailableFaceIdentityVerifier); !unavailable {
			var faceAsset struct{ ID uint64 }
			if e := tx.Table("avatar_asset").Select("id").Where("collection_session_id=? AND capture_slot='FACE_01' AND status='CONFIRMED'", id).Take(&faceAsset).Error; e != nil {
				return fmt.Errorf("face asset missing")
			}
			if e := s.createFaceIdentityVerification(tx, cur.AccountID, row.ProfileID, row.RosterMemberID, id, faceAsset.ID); e != nil {
				return e
			}
		}
		now := time.Now()
		taskID = idgen.Next()
		if e := tx.Table("avatar_processing_task").Create(map[string]any{"id": taskID, "status": "PENDING", "version": 1, "task_no": fmt.Sprintf("AVT-%d", taskID), "profile_id": row.ProfileID, "collection_session_id": id, "task_type": "AUTO_QUALITY", "input_digest": hash(fmt.Sprintf("%d:%d:%d", id, photos, videos)), "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_collection_session").Where("id=?", id).Updates(map[string]any{"status": "SUBMITTED", "submitted_at": now, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(cur.AccountID)}).Error; e != nil {
			return e
		}
		profileUpdate := map[string]any{"status": "AUTO_QUALITY_CHECKING", "collection_status": "SUBMITTED", "quality_status": "PENDING", "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}
		if _, unavailable := s.faceIDV.(unavailableFaceIdentityVerifier); !unavailable {
			// The roster import check has already passed. Once FACE_01 is queued for
			// the submitted collection, this field represents the pending 1:1 check.
			profileUpdate["real_name_status"] = "PENDING"
		}
		return tx.Table("avatar_profile").Where("id=?", row.ProfileID).Updates(profileUpdate).Error
	})
	if err != nil {
		return nil, avatarError(409, "AVATAR_COLLECTION_SUBMIT_CONFLICT", "采集会话状态已变化", nil)
	}
	return gin.H{"status": "AUTO_QUALITY_CHECKING", "processingTaskId": fmt.Sprint(taskID)}, nil
}
