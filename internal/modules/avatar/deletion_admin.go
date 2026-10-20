package avatar

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
)

type objectDeleter interface {
	Delete(context.Context, string) error
}

func (s *Service) listDeletionRequests(ctx context.Context, c identity.CurrentAdmin) (any, *httpx.AppError) {
	if !hasAdminRole(c, "PLATFORM_ADMIN") {
		return nil, avatarError(403, "AUTH_FORBIDDEN", "仅平台管理员可处理删除申请", nil)
	}
	var rows []map[string]any
	if e := s.db.WithContext(ctx).Table("avatar_deletion_request").Order("created_at DESC").Limit(100).Find(&rows).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "查询删除申请失败", nil)
	}
	return gin.H{"items": rows}, nil
}
func (s *Service) decideDeletion(ctx context.Context, c identity.CurrentAdmin, idText string, r deletionDecisionRequest, ip string) (any, *httpx.AppError) {
	if !hasAdminRole(c, "PLATFORM_ADMIN") {
		return nil, avatarError(403, "AUTH_FORBIDDEN", "仅平台管理员可处理删除申请", nil)
	}
	id, ok := parseUint(idText)
	if !ok || (r.Decision != "APPROVED" && r.Decision != "REJECTED" && r.Decision != "RETAIN_BY_LAW") {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "删除决定不正确", nil)
	}
	if r.Decision == "RETAIN_BY_LAW" && r.RetentionExceptionCode == "" {
		return nil, avatarError(400, "VALIDATION_REQUIRED", "法定保留须填写原因码", nil)
	}
	now := time.Now()
	var status string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var q struct {
			ID, ProfileID uint64
			Status        string
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_deletion_request").Where("id=?", id).Take(&q).Error; e != nil {
			return e
		}
		if q.Status != "PENDING" {
			return fmt.Errorf("state")
		}
		status = "REJECTED"
		if r.Decision == "APPROVED" {
			status = "PROCESSING"
		}
		if e := tx.Table("avatar_deletion_request").Where("id=?", id).Updates(map[string]any{"status": status, "decision": r.Decision, "retention_exception_code": nullable(r.RetentionExceptionCode), "status_reason": nullable(r.Reason), "approved_by": c.UserID, "approved_at": now, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error; e != nil {
			return e
		}
		pstatus := "ACTIVE"
		if r.Decision == "APPROVED" {
			pstatus = "DELETION_PROCESSING"
		}
		return tx.Table("avatar_profile").Where("id=?", q.ProfileID).Updates(map[string]any{"status": pstatus, "updated_at": now, "status_changed_at": now}).Error
	})
	if err != nil {
		return nil, avatarError(409, "AVATAR_DELETION_DECISION_CONFLICT", "删除申请状态已变化", nil)
	}
	s.auditExport(ctx, c, id, "DELETION_DECISION", ip)
	return gin.H{"deletionRequestId": fmt.Sprint(id), "status": status}, nil
}
func (s *Service) ProcessDueDeletion(ctx context.Context, limit int) (int, error) {
	if limit < 1 {
		limit = 20
	}
	var ids []uint64
	if e := s.db.WithContext(ctx).Table("avatar_deletion_request").Where("status='PROCESSING'").Limit(limit).Pluck("id", &ids).Error; e != nil {
		return 0, e
	}
	n := 0
	for _, id := range ids {
		ok, e := s.purgeDeletion(ctx, id)
		if e != nil {
			return n, e
		}
		if ok {
			n++
		}
	}
	return n, nil
}
func (s *Service) purgeDeletion(ctx context.Context, id uint64) (bool, error) {
	d, ok := s.storage.(objectDeleter)
	if !ok {
		return false, nil
	}
	var q struct {
		ProfileID uint64
		Status    string
	}
	if e := s.db.WithContext(ctx).Table("avatar_deletion_request").Where("id=?", id).Take(&q).Error; e != nil {
		return false, e
	}
	if q.Status != "PROCESSING" {
		return false, nil
	}
	var assets []struct {
		ID               uint64
		StorageKeyCipher []byte
	}
	if e := s.db.WithContext(ctx).Table("avatar_asset").Select("id,storage_key_cipher").Where("profile_id=? AND status!='PURGED'", q.ProfileID).Find(&assets).Error; e != nil {
		return false, e
	}
	for _, a := range assets {
		key, e := decrypt(s.auth.JWTSecret, a.StorageKeyCipher)
		if e != nil {
			return false, e
		}
		if e = d.Delete(ctx, key); e != nil {
			return false, e
		}
	}
	now := time.Now()
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Table("avatar_asset").Where("profile_id=?", q.ProfileID).Updates(map[string]any{"status": "PURGED", "storage_key_cipher": []byte{}, "purged_at": now, "updated_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_profile").Where("id=?", q.ProfileID).Updates(map[string]any{"status": "DELETED", "archive_status": "DELETED", "updated_at": now, "status_changed_at": now}).Error; e != nil {
			return e
		}
		return tx.Table("avatar_deletion_request").Where("id=? AND status='PROCESSING'", id).Updates(map[string]any{"status": "COMPLETED", "purged_at": now, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error
	})
	return e == nil, e
}
