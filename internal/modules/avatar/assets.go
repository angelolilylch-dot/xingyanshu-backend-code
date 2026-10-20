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
type uploadSigner interface {
	SignUploadURL(context.Context, string, string, time.Duration) (string, error)
}

func (s *Service) assetCredentials(ctx context.Context, cur Current, idText string, r assetCredentialRequest) (any, *httpx.AppError) {
	id, ok := parseUint(idText)
	if !ok || !validSlot(r.AssetType, r.CaptureSlot) || len(r.ContentHash) != 64 || r.FileSize > 200*1024*1024 {
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
	url, e := signer.SignUploadURL(ctx, key, r.MIMEType, 10*time.Minute)
	if e != nil {
		return nil, avatarError(503, "AVATAR_STORAGE_UNAVAILABLE", "素材存储暂不可用", nil)
	}
	raw, _ := json.Marshal(uploadGrant{row.ID, row.ProfileID, r.AssetType, r.CaptureSlot, strings.ToLower(r.ContentHash), r.MIMEType, key, r.FileSize})
	if e = s.redis.Set(ctx, "avatar:upload:"+token, raw, 10*time.Minute).Err(); e != nil {
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
	return gin.H{"assetId": fmt.Sprint(assetID), "captureSlot": g.CaptureSlot, "status": "CONFIRMED"}, nil
}
func (s *Service) submitCollection(ctx context.Context, cur Current, idText string) (any, *httpx.AppError) {
	id, ok := parseUint(idText)
	if !ok {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "采集会话不正确", nil)
	}
	var counts struct{ Photos, Videos int64 }
	q := s.db.WithContext(ctx).Table("avatar_asset a").Select("SUM(CASE WHEN asset_type='FACE_PHOTO' AND status='CONFIRMED' THEN 1 ELSE 0 END) photos,SUM(CASE WHEN asset_type='VOICE_VIDEO' AND status='CONFIRMED' THEN 1 ELSE 0 END) videos").Joins("JOIN avatar_collection_session cs ON cs.id=a.collection_session_id JOIN avatar_profile p ON p.id=cs.profile_id").Where("a.collection_session_id=? AND p.account_id=?", id, cur.AccountID)
	q.Scan(&counts)
	if counts.Photos != 8 || counts.Videos != 1 {
		return nil, avatarError(422, "AVATAR_COLLECTION_INCOMPLETE", "请完成8张照片和朗读视频采集", gin.H{"photoCount": counts.Photos, "videoCount": counts.Videos})
	}
	var taskID uint64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ProfileID uint64
			Status    string
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_collection_session cs").Select("cs.profile_id,cs.status").Joins("JOIN avatar_profile p ON p.id=cs.profile_id").Where("cs.id=? AND p.account_id=?", id, cur.AccountID).Take(&row).Error; e != nil {
			return e
		}
		if row.Status != "COLLECTING" {
			return fmt.Errorf("invalid status")
		}
		now := time.Now()
		taskID = idgen.Next()
		if e := tx.Table("avatar_processing_task").Create(map[string]any{"id": taskID, "status": "PENDING", "version": 1, "task_no": fmt.Sprintf("AVT-%d", taskID), "profile_id": row.ProfileID, "collection_session_id": id, "task_type": "AUTO_QUALITY", "input_digest": hash(fmt.Sprintf("%d:%d:%d", id, counts.Photos, counts.Videos)), "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_collection_session").Where("id=?", id).Updates(map[string]any{"status": "SUBMITTED", "submitted_at": now, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(cur.AccountID)}).Error; e != nil {
			return e
		}
		return tx.Table("avatar_profile").Where("id=?", row.ProfileID).Updates(map[string]any{"status": "AUTO_QUALITY_CHECKING", "collection_status": "SUBMITTED", "quality_status": "PENDING", "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error
	})
	if err != nil {
		return nil, avatarError(409, "AVATAR_COLLECTION_SUBMIT_CONFLICT", "采集会话状态已变化", nil)
	}
	return gin.H{"status": "AUTO_QUALITY_CHECKING", "processingTaskId": fmt.Sprint(taskID)}, nil
}
