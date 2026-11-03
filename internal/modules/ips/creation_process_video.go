package ips

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/platform/idgen"
)

const (
	creationProcessVideoMaxCount       = 3
	creationProcessVideoMaxBytes int64 = 100 * 1024 * 1024
)

// CreationProcessVideoInput 是 IP 入库的私有创作过程佐证视频；不属于后续授权素材包。
type CreationProcessVideoInput struct {
	FileID      uint64 `json:"fileId,string" binding:"required"`
	Description string `json:"description" binding:"required,max=500"`
	RecordedAt  string `json:"recordedAt" binding:"omitempty,datetime=2006-01-02"`
}

type creationProcessVideoFile struct {
	ID       uint64 `gorm:"column:id"`
	FileHash string `gorm:"column:file_hash"`
	MimeType string `gorm:"column:mime_type"`
	FileSize int64  `gorm:"column:file_size"`
}

func (s *Service) validateCreationProcessVideos(ctx context.Context, input IPInput) string {
	videos := input.CreationProcessVideos
	reason := strings.TrimSpace(input.CreationProcessVideoExemptionReason)
	if len(videos) > creationProcessVideoMaxCount {
		return "IP_CREATION_PROCESS_VIDEO_COUNT_EXCEEDED"
	}
	if len(videos) == 0 {
		if input.RightHolder.AcquisitionMethod == "ORIGINAL_CREATION" {
			return "IP_CREATION_PROCESS_VIDEO_REQUIRED"
		}
		if reason == "" {
			return "IP_CREATION_PROCESS_VIDEO_EXEMPTION_REQUIRED"
		}
		return ""
	}
	if reason != "" {
		return "VALIDATION_FORMAT_INVALID"
	}
	seen := map[uint64]bool{}
	var total int64
	for _, video := range videos {
		if video.FileID == 0 || strings.TrimSpace(video.Description) == "" || seen[video.FileID] {
			return "VALIDATION_FORMAT_INVALID"
		}
		seen[video.FileID] = true
		var file creationProcessVideoFile
		if err := s.db.WithContext(ctx).Table("file_object").Select("id,file_hash,mime_type,file_size").Where("id=? AND status='ACTIVE' AND upload_status='SUCCESS'", video.FileID).Take(&file).Error; err != nil {
			return "IP_CREATION_PROCESS_VIDEO_FILE_NOT_FOUND"
		}
		if file.MimeType != "video/mp4" {
			return "IP_CREATION_PROCESS_VIDEO_INVALID_TYPE"
		}
		if file.FileSize <= 0 || file.FileSize > creationProcessVideoMaxBytes {
			return "IP_CREATION_PROCESS_VIDEO_TOO_LARGE"
		}
		total += file.FileSize
		if total > creationProcessVideoMaxBytes {
			return "IP_CREATION_PROCESS_VIDEO_TOTAL_SIZE_EXCEEDED"
		}
	}
	return ""
}

func (s *Service) creationProcessVideosLocked(ctx context.Context, ipID uint64) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Table("ip_creation_process_video").Where("ip_id=? AND submission_locked_at IS NOT NULL", ipID).Count(&count).Error
	return count > 0, err
}

// validateCreationProcessVideoPrerequisite 防止历史 IP 在未经过新版保存表单时绕过视频或豁免校验。
func (s *Service) validateCreationProcessVideoPrerequisite(ctx context.Context, ipID uint64) (string, error) {
	var asset struct {
		ExemptionReason string `gorm:"column:creation_process_video_exemption_reason"`
	}
	if err := s.db.WithContext(ctx).Table("ip_asset").Select("creation_process_video_exemption_reason").Where("id=?", ipID).Take(&asset).Error; err != nil {
		return "INTERNAL_ERROR", err
	}
	var holder struct {
		AcquisitionMethod string `gorm:"column:acquisition_method"`
	}
	if err := s.db.WithContext(ctx).Table("ip_right_holder").Select("acquisition_method").Where("ip_id=?", ipID).Take(&holder).Error; err != nil {
		return "INTERNAL_ERROR", err
	}
	var videos int64
	if err := s.db.WithContext(ctx).Table("ip_creation_process_video v").Joins("JOIN file_object f ON f.id=v.file_id AND f.status='ACTIVE' AND f.upload_status='SUCCESS' AND f.mime_type='video/mp4'").Where("v.ip_id=? AND f.file_size>0 AND f.file_size<=?", ipID, creationProcessVideoMaxBytes).Count(&videos).Error; err != nil {
		return "INTERNAL_ERROR", err
	}
	if holder.AcquisitionMethod == "ORIGINAL_CREATION" && videos == 0 {
		return "IP_CREATION_PROCESS_VIDEO_REQUIRED", nil
	}
	if videos == 0 && strings.TrimSpace(asset.ExemptionReason) == "" {
		return "IP_CREATION_PROCESS_VIDEO_EXEMPTION_REQUIRED", nil
	}
	return "", nil
}

func (s *Service) replaceCreationProcessVideos(tx *gorm.DB, ipID, userID uint64, videos []CreationProcessVideoInput) error {
	var count int64
	if err := tx.Table("ip_creation_process_video").Where("ip_id=? AND submission_locked_at IS NOT NULL", ipID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("creation process videos locked")
	}
	if err := tx.Table("ip_creation_process_video").Where("ip_id=?", ipID).Delete(map[string]any{}).Error; err != nil {
		return err
	}
	if err := tx.Table("file_relation").Where("object_type='IP' AND object_id=? AND file_role='IP_CREATION_PROCESS_VIDEO'", ipID).Delete(map[string]any{}).Error; err != nil {
		return err
	}
	for index, video := range videos {
		var file creationProcessVideoFile
		if err := tx.Table("file_object").Select("id,file_hash,mime_type,file_size").Where("id=? AND status='ACTIVE' AND upload_status='SUCCESS'", video.FileID).Take(&file).Error; err != nil {
			return err
		}
		if err := tx.Table("ip_creation_process_video").Create(map[string]any{"id": idgen.Next(), "status": "ACTIVE", "version": 1, "created_by": userID, "ip_id": ipID, "file_id": video.FileID, "file_hash": file.FileHash, "description": strings.TrimSpace(video.Description), "recorded_at": nullable(video.RecordedAt), "sort_no": index}).Error; err != nil {
			return err
		}
		if err := s.linkFile(tx, video.FileID, "IP", ipID, "IP_CREATION_PROCESS_VIDEO", index, userID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) lockCreationProcessVideos(tx *gorm.DB, ipID uint64, now time.Time) error {
	return tx.Table("ip_creation_process_video").Where("ip_id=? AND submission_locked_at IS NULL", ipID).Updates(map[string]any{"submission_locked_at": now, "updated_at": now}).Error
}
