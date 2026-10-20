package avatar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idgen"
)

const qualityPreviewTTL = 5 * time.Minute

type qualityQueueRequest struct {
	ReviewType string `json:"reviewType" binding:"required"`
	ReasonCode string `json:"reasonCode" binding:"required"`
	Reason     string `json:"reason"`
}
type qualityDecisionRequest struct {
	Version    uint   `json:"version" binding:"required"`
	Decision   string `json:"decision" binding:"required"`
	ReasonCode string `json:"reasonCode"`
	Reason     string `json:"reason"`
}
type qualityPreviewGrant struct{ ReviewID, AssetID, AdminID uint64 }

func qualityAdmin(c identity.CurrentAdmin) *httpx.AppError {
	if !hasAdminRole(c, "PLATFORM_ADMIN") || !c.HasPermission("AVATAR_QUALITY_REVIEW") {
		return avatarError(403, "AUTH_FORBIDDEN", "仅具备人工质检权限的平台管理员可操作", nil)
	}
	return nil
}
func (s *Service) listQualityReviews(ctx context.Context, c identity.CurrentAdmin) (any, *httpx.AppError) {
	if e := qualityAdmin(c); e != nil {
		return nil, e
	}
	var rows []map[string]any
	if e := s.db.WithContext(ctx).Table("avatar_quality_review qr").Select("qr.id,qr.review_no,qr.status,qr.review_type,qr.reason_code,qr.created_at,p.profile_no,p.city_code").Joins("JOIN avatar_profile p ON p.id=qr.profile_id").Where("qr.status='PENDING'").Order("qr.created_at").Limit(100).Find(&rows).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "查询人工质检待办失败", nil)
	}
	return gin.H{"items": rows}, nil
}
func (s *Service) queueQualityReview(ctx context.Context, c identity.CurrentAdmin, sessionText string, r qualityQueueRequest, ip string) (any, *httpx.AppError) {
	if e := qualityAdmin(c); e != nil {
		return nil, e
	}
	sid, ok := parseUint(sessionText)
	if !ok || (r.ReviewType != "SAMPLE" && r.ReviewType != "RISK") {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "审核类型仅支持 SAMPLE 或 RISK", nil)
	}
	var out gin.H
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var x struct {
			ID, ProfileID uint64
			Status        string
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_collection_session").Where("id=?", sid).Take(&x).Error; e != nil {
			return e
		}
		if x.Status != "SUBMITTED" {
			return fmt.Errorf("session state")
		}
		var n int64
		if e := tx.Table("avatar_quality_review").Where("collection_session_id=? AND status='PENDING'", sid).Count(&n).Error; e != nil {
			return e
		}
		if n > 0 {
			return fmt.Errorf("pending")
		}
		now := time.Now()
		id := idgen.Next()
		if e := tx.Table("avatar_quality_review").Create(map[string]any{"id": id, "status": "PENDING", "version": 1, "review_no": fmt.Sprintf("AVQR-%d", id), "profile_id": x.ProfileID, "collection_session_id": sid, "review_type": r.ReviewType, "reason_code": r.ReasonCode, "reason": nullable(r.Reason), "reviewer_type": "PLATFORM_ADMIN", "created_by": c.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_processing_task").Where("collection_session_id=? AND task_type='AUTO_QUALITY' AND status IN ('PENDING','RETRY_WAIT')", sid).Updates(map[string]any{"status": "CANCELLED", "updated_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_profile").Where("id=?", x.ProfileID).Updates(map[string]any{"status": "MANUAL_REVIEW", "quality_status": "MANUAL_REVIEW", "updated_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error; e != nil {
			return e
		}
		out = gin.H{"qualityReviewId": fmt.Sprint(id), "status": "PENDING"}
		return nil
	})
	if err != nil {
		return nil, avatarError(409, "AVATAR_QUALITY_REVIEW_CONFLICT", "采集会话不可加入人工质检或已存在待办", nil)
	}
	s.auditQuality(ctx, c, sid, "QUALITY_REVIEW_QUEUE", ip)
	return out, nil
}
func (s *Service) issueQualityPreviews(ctx context.Context, c identity.CurrentAdmin, reviewText, ip string) (any, *httpx.AppError) {
	if e := qualityAdmin(c); e != nil {
		return nil, e
	}
	if s.redis == nil {
		return nil, avatarError(503, "AVATAR_PREVIEW_UNAVAILABLE", "预览服务暂不可用", nil)
	}
	rid, ok := parseUint(reviewText)
	if !ok {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "审核编号不正确", nil)
	}
	var review struct {
		ID, CollectionSessionID uint64
		Status                  string
	}
	if e := s.db.WithContext(ctx).Table("avatar_quality_review").Where("id=?", rid).Take(&review).Error; e != nil {
		return nil, avatarError(404, "AVATAR_QUALITY_REVIEW_NOT_FOUND", "人工质检待办不存在", nil)
	}
	if review.Status != "PENDING" {
		return nil, avatarError(409, "AVATAR_QUALITY_REVIEW_CONFLICT", "人工质检待办已处理", nil)
	}
	var assets []struct {
		ID                               uint64
		AssetType, CaptureSlot, MimeType string
	}
	if e := s.db.WithContext(ctx).Table("avatar_asset").Select("id,asset_type,capture_slot,mime_type").Where("collection_session_id=? AND status='CONFIRMED'", review.CollectionSessionID).Find(&assets).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "查询审核素材失败", nil)
	}
	items := make([]gin.H, 0, len(assets))
	for _, a := range assets {
		token, e := randomToken()
		if e != nil {
			return nil, avatarError(500, "INTERNAL_ERROR", "创建预览令牌失败", nil)
		}
		raw, _ := json.Marshal(qualityPreviewGrant{rid, a.ID, c.UserID})
		if e = s.redis.Set(ctx, "avatar:quality-preview:"+token, raw, qualityPreviewTTL).Err(); e != nil {
			return nil, avatarError(503, "AVATAR_PREVIEW_UNAVAILABLE", "预览服务暂不可用", nil)
		}
		items = append(items, gin.H{"assetId": fmt.Sprint(a.ID), "assetType": a.AssetType, "captureSlot": a.CaptureSlot, "mimeType": a.MimeType, "previewToken": token, "expiresInSeconds": 300})
	}
	s.auditQuality(ctx, c, rid, "QUALITY_PREVIEW_ISSUE", ip)
	return gin.H{"items": items}, nil
}
func (s *Service) consumeQualityPreview(c *gin.Context) {
	current, _ := identity.CurrentAdminFromContext(c)
	if e := qualityAdmin(current); e != nil {
		httpx.AbortWithError(c, e)
		return
	}
	if s.redis == nil || s.storage == nil {
		httpx.AbortWithError(c, avatarError(503, "AVATAR_PREVIEW_UNAVAILABLE", "预览服务暂不可用", nil))
		return
	}
	// Lua keeps consume-once semantics on Redis versions that predate GETDEL.
	v, e := s.redis.Eval(c.Request.Context(), "local v=redis.call('GET',KEYS[1]); if v then redis.call('DEL',KEYS[1]); end; return v", []string{"avatar:quality-preview:" + c.Param("token")}).Result()
	raw, ok := v.(string)
	if e != nil || !ok {
		httpx.AbortWithError(c, avatarError(410, "AVATAR_PREVIEW_EXPIRED", "预览令牌已失效或已使用", nil))
		return
	}
	var grant qualityPreviewGrant
	if json.Unmarshal([]byte(raw), &grant) != nil || grant.AdminID != current.UserID {
		httpx.AbortWithError(c, avatarError(403, "AUTH_FORBIDDEN", "无权使用该预览令牌", nil))
		return
	}
	var a struct {
		ID               uint64
		StorageKeyCipher []byte
		MimeType         string
	}
	if e = s.db.WithContext(c.Request.Context()).Table("avatar_asset a").Joins("JOIN avatar_quality_review q ON q.collection_session_id=a.collection_session_id").Select("a.id,a.storage_key_cipher,a.mime_type").Where("a.id=? AND q.id=? AND q.status='PENDING'", grant.AssetID, grant.ReviewID).Take(&a).Error; e != nil {
		httpx.AbortWithError(c, avatarError(404, "AVATAR_ASSET_NOT_FOUND", "预览素材不存在", nil))
		return
	}
	key, e := decrypt(s.auth.JWTSecret, a.StorageKeyCipher)
	if e != nil {
		httpx.AbortWithError(c, avatarError(500, "INTERNAL_ERROR", "读取预览素材失败", nil))
		return
	}
	body, e := s.storage.Get(c.Request.Context(), key)
	if e != nil {
		httpx.AbortWithError(c, avatarError(503, "AVATAR_STORAGE_UNAVAILABLE", "预览存储暂不可用", nil))
		return
	}
	defer body.Close()
	c.Header("Content-Type", a.MimeType)
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, body)
	s.auditQuality(c.Request.Context(), current, grant.AssetID, "QUALITY_PREVIEW_CONSUME", c.ClientIP())
}
func (s *Service) decideQualityReview(ctx context.Context, c identity.CurrentAdmin, reviewText string, r qualityDecisionRequest, ip string) (any, *httpx.AppError) {
	if e := qualityAdmin(c); e != nil {
		return nil, e
	}
	id, ok := parseUint(reviewText)
	if !ok || (r.Decision != "PASSED" && r.Decision != "RESHOOT_REQUIRED" && r.Decision != "REJECTED") {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "质检决定不正确", nil)
	}
	if r.Decision != "PASSED" && r.ReasonCode == "" {
		return nil, avatarError(400, "VALIDATION_REQUIRED", "补采或驳回须填写原因码", nil)
	}
	now := time.Now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var q struct {
			ID, ProfileID, CollectionSessionID uint64
			Status                             string
			Version                            uint
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_quality_review").Where("id=?", id).Take(&q).Error; e != nil {
			return e
		}
		if q.Status != "PENDING" || q.Version != r.Version {
			return fmt.Errorf("state")
		}
		if e := tx.Table("avatar_quality_review").Where("id=? AND version=?", id, r.Version).Updates(map[string]any{"status": "COMPLETED", "review_result": r.Decision, "reason_code": nullable(r.ReasonCode), "reason": nullable(r.Reason), "reviewer_type": "PLATFORM_ADMIN", "reviewer_id": fmt.Sprint(c.UserID), "reviewed_at": now, "updated_by": c.UserID, "updated_at": now, "version": r.Version + 1, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error; e != nil {
			return e
		}
		sessionStatus, profileStatus, collectionStatus, qualityStatus, archiveStatus := "PASSED", "ARCHIVED", "PASSED", "PASSED", "ARCHIVED"
		assetQuality := "PASSED"
		if r.Decision == "RESHOOT_REQUIRED" {
			sessionStatus, profileStatus, collectionStatus, qualityStatus, archiveStatus, assetQuality = "REJECTED", "RESHOOT_REQUIRED", "REJECTED", "RESHOOT_REQUIRED", "NOT_STARTED", "REJECTED"
		}
		if r.Decision == "REJECTED" {
			sessionStatus, profileStatus, collectionStatus, qualityStatus, archiveStatus, assetQuality = "REJECTED", "REJECTED", "REJECTED", "REJECTED", "NOT_STARTED", "REJECTED"
		}
		if e := tx.Table("avatar_collection_session").Where("id=?", q.CollectionSessionID).Updates(map[string]any{"status": sessionStatus, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error; e != nil {
			return e
		}
		updates := map[string]any{"status": profileStatus, "collection_status": collectionStatus, "quality_status": qualityStatus, "archive_status": archiveStatus, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}
		if r.Decision == "PASSED" {
			until := now.AddDate(0, 0, 365)
			updates["archived_at"] = now
			updates["retention_until"] = until
			if e := tx.Table("avatar_consent").Where("profile_id=? AND status='ACTIVE'", q.ProfileID).Updates(map[string]any{"retention_until": until, "updated_at": now}).Error; e != nil {
				return e
			}
		}
		if e := tx.Table("avatar_profile").Where("id=?", q.ProfileID).Updates(updates).Error; e != nil {
			return e
		}
		if r.Decision == "REJECTED" {
			if e := tx.Table("avatar_roster_member").Where("profile_id=?", q.ProfileID).Updates(map[string]any{"status": "INACTIVE", "registration_status": "REJECTED", "status_reason_code": r.ReasonCode, "status_reason": nullable(r.Reason), "updated_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error; e != nil {
				return e
			}
		}
		if e := tx.Table("avatar_asset").Where("collection_session_id=? AND status='CONFIRMED'", q.CollectionSessionID).Updates(map[string]any{"quality_status": assetQuality, "updated_at": now}).Error; e != nil {
			return e
		}
		return nil
	})
	if err != nil {
		return nil, avatarError(409, "AVATAR_QUALITY_REVIEW_CONFLICT", "人工质检待办已处理或版本已变化", nil)
	}
	s.auditQuality(ctx, c, id, "QUALITY_REVIEW_DECISION_"+r.Decision, ip)
	return gin.H{"qualityReviewId": fmt.Sprint(id), "status": "COMPLETED", "decision": r.Decision}, nil
}
func (s *Service) auditQuality(ctx context.Context, c identity.CurrentAdmin, id uint64, action, ip string) {
	_ = s.db.WithContext(ctx).Table("avatar_access_audit").Create(map[string]any{"id": idgen.Next(), "audit_no": fmt.Sprintf("AVAUD-%d", idgen.Next()), "actor_type": "USER", "actor_id": fmt.Sprint(c.UserID), "actor_role": firstRole(c), "action": action, "object_type": "AVATAR_QUALITY_REVIEW", "object_id": id, "purpose_code": "QUALITY_REVIEW", "result": "SUCCEEDED", "client_ip_hash": hash(ip)})
}
