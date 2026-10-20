package avatar

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProcessDueQuality runs only the internal state transition. A real visual quality
// provider must be configured outside test before production may archive material.
func (s *Service) ProcessDueQuality(ctx context.Context, limit int) (int, error) {
	if limit < 1 {
		limit = 20
	}
	var tasks []struct{ ID uint64 }
	if err := s.db.WithContext(ctx).Table("avatar_processing_task").Select("id").Where("task_type='AUTO_QUALITY' AND status IN ('PENDING','RETRY_WAIT') AND (next_retry_at IS NULL OR next_retry_at<=?)", time.Now()).Order("created_at").Limit(limit).Find(&tasks).Error; err != nil {
		return 0, err
	}
	count := 0
	for _, task := range tasks {
		ok, err := s.processQualityTask(ctx, task.ID)
		if err != nil {
			return count, err
		}
		if ok {
			count++
		}
	}
	return count, nil
}
func (s *Service) processQualityTask(ctx context.Context, taskID uint64) (bool, error) {
	now := time.Now()
	returnValue := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t struct {
			ID, ProfileID, CollectionSessionID uint64
			Status                             string
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_processing_task").Where("id=? AND task_type='AUTO_QUALITY'", taskID).Take(&t).Error; e != nil {
			return e
		}
		if t.Status != "PENDING" && t.Status != "RETRY_WAIT" {
			return nil
		}
		if s.environment != "test" {
			returnValue = true
			return tx.Table("avatar_processing_task").Where("id=?", t.ID).Updates(map[string]any{"status": "RETRY_WAIT", "attempt_count": gorm.Expr("attempt_count+1"), "next_retry_at": now.Add(time.Hour), "error_summary": "visual quality provider is not configured", "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error
		}
		until := now.AddDate(0, 0, 365)
		if e := tx.Table("avatar_processing_task").Where("id=?", t.ID).Updates(map[string]any{"status": "SUCCEEDED", "attempt_count": gorm.Expr("attempt_count+1"), "started_at": now, "finished_at": now, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_collection_session").Where("id=?", t.CollectionSessionID).Updates(map[string]any{"status": "PASSED", "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_profile").Where("id=?", t.ProfileID).Updates(map[string]any{"status": "ARCHIVED", "collection_status": "PASSED", "quality_status": "PASSED", "archive_status": "ARCHIVED", "archived_at": now, "retention_until": until, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_asset").Where("collection_session_id=? AND status='CONFIRMED'", t.CollectionSessionID).Updates(map[string]any{"quality_status": "PASSED", "retention_until": until, "updated_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_consent").Where("profile_id=? AND status='ACTIVE'", t.ProfileID).Updates(map[string]any{"retention_until": until, "updated_at": now}).Error; e != nil {
			return e
		}
		returnValue = true
		return nil
	})
	return returnValue, err
}

var _ = fmt.Sprint
