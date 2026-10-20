package avatar

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"time"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idgen"
)

func (s *Service) revealExportPassword(ctx context.Context, c identity.CurrentAdmin, idText, ip string) (any, *httpx.AppError) {
	if !hasAdminRole(c, "PLATFORM_ADMIN") {
		return nil, avatarError(403, "AUTH_FORBIDDEN", "仅平台管理员可查看归档包密码", nil)
	}
	id, ok := parseUint(idText)
	if !ok {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "导出任务编号不正确", nil)
	}
	var password string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var j struct {
			ID                 uint64
			ExportType, Status string
			PasswordCipher     []byte
			PasswordRevealedAt *time.Time
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_export_job").Where("id=?", id).Take(&j).Error; e != nil {
			return e
		}
		if j.ExportType != "ARCHIVE_PACKAGE" || j.Status != "SUCCEEDED" || j.PasswordRevealedAt != nil || len(j.PasswordCipher) == 0 {
			return fmt.Errorf("unavailable")
		}
		v, e := decrypt(s.auth.JWTSecret, j.PasswordCipher)
		if e != nil {
			return e
		}
		password = v
		return tx.Table("avatar_export_job").Where("id=?", id).Updates(map[string]any{"password_revealed_at": time.Now(), "password_revealed_by": c.UserID, "updated_at": time.Now()}).Error
	})
	if err != nil {
		return nil, avatarError(http.StatusConflict, "AVATAR_EXPORT_PASSWORD_UNAVAILABLE", "解压密码不可再次查看；如未保存请重新生成归档包", nil)
	}
	s.auditExport(ctx, c, id, "EXPORT_PASSWORD_REVEAL", ip)
	return gin.H{"password": password, "warning": "密码仅展示一次，请立即保存；关闭后只能重新生成归档包。"}, nil
}
func (s *Service) regenerateExport(ctx context.Context, c identity.CurrentAdmin, idText, ip string) (any, *httpx.AppError) {
	if !hasAdminRole(c, "PLATFORM_ADMIN") {
		return nil, avatarError(403, "AUTH_FORBIDDEN", "仅平台管理员可重新生成归档包", nil)
	}
	old, ok := parseUint(idText)
	if !ok {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "导出任务编号不正确", nil)
	}
	var out gin.H
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var j struct {
			ID, CityOrgID, RequesterID, RequesterOrgID uint64
			ExportType, RequesterRole, PurposeCode     string
			FilterSnapshotJSON                         []byte
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_export_job").Where("id=?", old).Take(&j).Error; e != nil {
			return e
		}
		if j.ExportType != "ARCHIVE_PACKAGE" {
			return fmt.Errorf("type")
		}
		now := time.Now()
		if e := tx.Table("avatar_export_job").Where("id=?", old).Updates(map[string]any{"status": "REVOKED", "invalidated_at": now, "invalidated_reason": "PASSWORD_NOT_SAVED_REGENERATED", "download_expires_at": now, "updated_at": now}).Error; e != nil {
			return e
		}
		id := idgen.Next()
		if e := tx.Table("avatar_export_job").Create(map[string]any{"id": id, "status": "PENDING", "version": 1, "job_no": fmt.Sprintf("AVE-%d", id), "export_type": "ARCHIVE_PACKAGE", "requester_id": c.UserID, "requester_org_id": c.OrgID, "requester_role": firstRole(c), "city_org_id": j.CityOrgID, "purpose_code": j.PurposeCode, "filter_snapshot_json": j.FilterSnapshotJSON}).Error; e != nil {
			return e
		}
		out = gin.H{"exportJobId": fmt.Sprint(id), "status": "PENDING", "replacesExportJobId": fmt.Sprint(old)}
		return nil
	})
	if err != nil {
		return nil, avatarError(409, "AVATAR_EXPORT_REGENERATE_UNAVAILABLE", "原归档包不可重新生成", nil)
	}
	s.auditExport(ctx, c, old, "EXPORT_REGENERATE", ip)
	return out, nil
}
