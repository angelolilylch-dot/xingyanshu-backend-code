package avatar

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"time"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idgen"
)

func (s *Service) startCollection(ctx context.Context, cur Current) (any, *httpx.AppError) {
	var out gin.H
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p struct {
			ID                         uint64
			Status                     string
			CurrentVersionNo           uint32
			CurrentCollectionSessionID *uint64
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_profile").Select("id,status,current_version_no,current_collection_session_id").Where("account_id=?", cur.AccountID).Take(&p).Error; e != nil {
			return e
		}
		if p.Status == "DELETION_REQUESTED" || p.Status == "DELETED" {
			return fmt.Errorf("deleting")
		}
		var consent int64
		if e := tx.Table("avatar_consent").Where("account_id=? AND profile_id=? AND status='ACTIVE' AND consent_type='COLLECT_AND_ARCHIVE'", cur.AccountID, p.ID).Count(&consent).Error; e != nil {
			return e
		}
		if consent == 0 {
			return fmt.Errorf("consent")
		}
		if p.CurrentCollectionSessionID != nil {
			var active struct {
				ID     uint64
				Status string
			}
			if e := tx.Table("avatar_collection_session").Select("id,status").Where("id=? AND status IN ('DRAFT','COLLECTING')", *p.CurrentCollectionSessionID).Take(&active).Error; e == nil {
				out = gin.H{"sessionId": fmt.Sprint(active.ID), "status": active.Status, "photoRequiredCount": 8, "videoRequired": true, "requiredSlots": requiredSlotsResponse(), "resumed": true}
				return nil
			}
		}
		id := idgen.Next()
		now := time.Now()
		v := p.CurrentVersionNo + 1
		if e := tx.Table("avatar_collection_session").Create(map[string]any{"id": id, "status": "COLLECTING", "version": 1, "collection_no": fmt.Sprintf("AVCS-%d", id), "profile_id": p.ID, "version_no": v, "photo_required_count": 8, "video_required": true, "expires_at": now.Add(24 * time.Hour), "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(cur.AccountID)}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_profile").Where("id=?", p.ID).Updates(map[string]any{"current_collection_session_id": id, "current_version_no": v, "status": "COLLECTING", "collection_status": "COLLECTING", "updated_at": now, "status_changed_at": now}).Error; e != nil {
			return e
		}
		out = gin.H{"sessionId": fmt.Sprint(id), "status": "COLLECTING", "photoRequiredCount": 8, "videoRequired": true, "requiredSlots": requiredSlotsResponse(), "resumed": false}
		return nil
	})
	if err != nil {
		if err.Error() == "consent" {
			return nil, avatarError(http.StatusPreconditionRequired, "AVATAR_CONSENT_REQUIRED", "请先签署采集与入库授权", nil)
		}
		if err.Error() == "deleting" {
			return nil, avatarError(http.StatusConflict, "AVATAR_PROFILE_DELETION_PENDING", "删除申请处理中", nil)
		}
		return nil, avatarError(500, "INTERNAL_ERROR", "创建采集会话失败", nil)
	}
	return out, nil
}
