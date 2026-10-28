package avatar

import (
	"context"
	"errors"
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
		if err := s.processQualityFaceIdentity(ctx, task.ID); err != nil {
			return count, err
		}
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

// processQualityFaceIdentity keeps the external 1:1 call inside the
// AUTO_QUALITY worker loop. The identity row remains an audit detail only.
func (s *Service) processQualityFaceIdentity(ctx context.Context, taskID uint64) error {
	var face struct{ ID uint64 }
	err := s.db.WithContext(ctx).Table("avatar_identity_verification v").Select("v.id").Joins("JOIN avatar_processing_task t ON t.collection_session_id=v.collection_session_id").Where("t.id=? AND t.task_type='AUTO_QUALITY' AND v.verification_type=? AND v.status='PENDING'", taskID, faceIdentityVerificationType).Take(&face).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.processPendingFaceIdentity(ctx, face.ID)
}

func (s *Service) processQualityTask(ctx context.Context, taskID uint64) (bool, error) {
	now := time.Now()
	returnValue := false
	var faceVerificationID uint64
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
		var faceState struct{ Status string }
		faceStateErr := tx.Table("avatar_identity_verification").Select("status").Where("collection_session_id=? AND verification_type=?", t.CollectionSessionID, faceIdentityVerificationType).Order("created_at DESC").Take(&faceState).Error
		if faceStateErr != nil && !errors.Is(faceStateErr, gorm.ErrRecordNotFound) {
			return faceStateErr
		}
		if faceStateErr == nil && (faceState.Status == "REJECTED" || faceState.Status == "ERROR") {
			returnValue = true
			return tx.Table("avatar_processing_task").Where("id=?", t.ID).Updates(map[string]any{"status": "FAILED", "finished_at": now, "error_summary": "face identity " + faceState.Status, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error
		}
		if s.environment != "test" {
			returnValue = true
			return tx.Table("avatar_processing_task").Where("id=?", t.ID).Updates(map[string]any{"status": "RETRY_WAIT", "attempt_count": gorm.Expr("attempt_count+1"), "next_retry_at": now.Add(time.Hour), "error_summary": "visual quality provider is not configured", "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error
		}
		until := now.AddDate(0, 0, 365)
		if e := tx.Table("avatar_processing_task").Where("id=?", t.ID).Updates(map[string]any{"status": "RUNNING", "attempt_count": gorm.Expr("attempt_count+1"), "started_at": now, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_collection_session").Where("id=?", t.CollectionSessionID).Updates(map[string]any{"status": "PASSED", "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; e != nil {
			return e
		}
		var face struct {
			ID     uint64
			Status string
		}
		faceErr := tx.Table("avatar_identity_verification").Select("id,status").Where("collection_session_id=? AND verification_type=?", t.CollectionSessionID, faceIdentityVerificationType).Order("created_at DESC").Take(&face).Error
		if faceErr != nil && !errors.Is(faceErr, gorm.ErrRecordNotFound) {
			return faceErr
		}
		if faceErr == nil && face.Status != "PASSED" {
			if face.Status != "PENDING" {
				return tx.Table("avatar_processing_task").Where("id=?", t.ID).Updates(map[string]any{"status": "FAILED", "finished_at": now, "error_summary": "face identity " + face.Status, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error
			}
			faceVerificationID = face.ID
			// The same AUTO_QUALITY task owns the next step. The identity row is
			// an audit detail, never an independently scheduled H5 task.
			return tx.Table("avatar_profile").Where("id=?", t.ProfileID).Updates(map[string]any{"collection_status": "PASSED", "quality_status": "PASSED", "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error
		}
		if e := s.archivePassedCollection(tx, t.ProfileID, t.CollectionSessionID, now, until); e != nil {
			return e
		}
		if e := tx.Table("avatar_processing_task").Where("id=?", t.ID).Updates(map[string]any{"status": "SUCCEEDED", "finished_at": now, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; e != nil {
			return e
		}
		returnValue = true
		return nil
	})
	if err != nil || faceVerificationID == 0 {
		return returnValue, err
	}
	if err := s.processPendingFaceIdentity(ctx, faceVerificationID); err != nil {
		return false, err
	}
	var face struct{ Status string }
	if err := s.db.WithContext(ctx).Table("avatar_identity_verification").Select("status").Where("id=?", faceVerificationID).Take(&face).Error; err != nil {
		return false, err
	}
	finishedAt := time.Now()
	taskStatus := "SUCCEEDED"
	var summary any
	if face.Status != "PASSED" {
		taskStatus, summary = "FAILED", "face identity "+face.Status
	}
	if err := s.db.WithContext(ctx).Table("avatar_processing_task").Where("id=? AND status='RUNNING'", taskID).Updates(map[string]any{"status": taskStatus, "finished_at": finishedAt, "error_summary": summary, "updated_at": finishedAt, "status_changed_at": finishedAt, "status_changed_by_type": "SYSTEM"}).Error; err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) archivePassedCollection(tx *gorm.DB, profileID, sessionID uint64, now, until time.Time) error {
	if e := tx.Table("avatar_profile").Where("id=?", profileID).Updates(map[string]any{"status": "ARCHIVED", "collection_status": "PASSED", "quality_status": "PASSED", "archive_status": "ARCHIVED", "archived_at": now, "retention_until": until, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; e != nil {
		return e
	}
	if e := tx.Table("avatar_asset").Where("collection_session_id=? AND status='CONFIRMED'", sessionID).Updates(map[string]any{"quality_status": "PASSED", "retention_until": until, "updated_at": now}).Error; e != nil {
		return e
	}
	return tx.Table("avatar_consent").Where("profile_id=? AND status='ACTIVE'", profileID).Updates(map[string]any{"retention_until": until, "updated_at": now}).Error
}

var _ = fmt.Sprint
