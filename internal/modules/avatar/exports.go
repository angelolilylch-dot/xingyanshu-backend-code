package avatar

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"time"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idgen"
)

func (s *Service) createExport(ctx context.Context, c identity.CurrentAdmin, r exportRequest, ip string) (any, *httpx.AppError) {
	if !c.HasPermission("AVATAR_PROGRESS_EXPORT") && !c.HasPermission("AVATAR_ARCHIVE_EXPORT") {
		return nil, avatarError(403, "AUTH_FORBIDDEN", "无权创建导出任务", nil)
	}
	platform := hasAdminRole(c, "PLATFORM_ADMIN")
	if r.ExportType != "PROGRESS_LIST" && r.ExportType != "ARCHIVE_PACKAGE" {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "导出类型不正确", nil)
	}
	if r.ExportType == "ARCHIVE_PACKAGE" && (!platform || !c.HasPermission("AVATAR_ARCHIVE_EXPORT") || r.PurposeCode == "") {
		return nil, avatarError(403, "AUTH_FORBIDDEN", "受控归档包仅平台管理员可按用途申请", nil)
	}
	city := r.CityOrgID
	if !platform {
		city = c.OrgID
		if r.ExportType != "PROGRESS_LIST" {
			return nil, avatarError(403, "AUTH_FORBIDDEN", "城市运营者仅可导出脱敏进度清单", nil)
		}
	}
	if city == 0 {
		return nil, avatarError(400, "VALIDATION_REQUIRED", "缺少城市组织", nil)
	}
	id := idgen.Next()
	f, _ := json.Marshal(gin.H{"profileIds": r.ProfileIDs, "cityOrgId": city})
	now := time.Now()
	if e := s.db.WithContext(ctx).Table("avatar_export_job").Create(map[string]any{"id": id, "status": "PENDING", "version": 1, "job_no": fmt.Sprintf("AVE-%d", id), "export_type": r.ExportType, "requester_id": c.UserID, "requester_org_id": c.OrgID, "requester_role": firstRole(c), "city_org_id": city, "purpose_code": r.PurposeCode, "filter_snapshot_json": string(f), "status_reason_code": "QUEUED", "updated_at": now}).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "创建导出任务失败", nil)
	}
	s.auditExport(ctx, c, id, "EXPORT_REQUEST", ip)
	return gin.H{"exportJobId": fmt.Sprint(id), "status": "PENDING", "exportType": r.ExportType}, nil
}
func (s *Service) listExports(ctx context.Context, c identity.CurrentAdmin) (any, *httpx.AppError) {
	q := s.db.WithContext(ctx).Table("avatar_export_job").Select("id,job_no,export_type,status,city_org_id,item_count,created_at,completed_at,download_expires_at").Order("created_at DESC")
	if !hasAdminRole(c, "PLATFORM_ADMIN") {
		q = q.Where("requester_org_id=?", c.OrgID)
	}
	var rows []map[string]any
	if e := q.Limit(100).Find(&rows).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "查询导出任务失败", nil)
	}
	return gin.H{"items": rows}, nil
}
func (s *Service) getExport(ctx context.Context, c identity.CurrentAdmin, idText string) (any, *httpx.AppError) {
	id, ok := parseUint(idText)
	if !ok {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "导出任务编号不正确", nil)
	}
	var x map[string]any
	if e := s.db.WithContext(ctx).Table("avatar_export_job").Where("id=?", id).Take(&x).Error; e != nil {
		return nil, avatarError(404, "AVATAR_EXPORT_NOT_FOUND", "导出任务不存在", nil)
	}
	if !hasAdminRole(c, "PLATFORM_ADMIN") && fmt.Sprint(x["requester_org_id"]) != fmt.Sprint(c.OrgID) {
		return nil, avatarError(403, "AUTH_FORBIDDEN", "无权查看该导出任务", nil)
	}
	return x, nil
}
func hasAdminRole(c identity.CurrentAdmin, w string) bool {
	for _, r := range c.RoleCodes {
		if r == w {
			return true
		}
	}
	return false
}
func firstRole(c identity.CurrentAdmin) string {
	if len(c.RoleCodes) > 0 {
		return c.RoleCodes[0]
	}
	return "ADMIN"
}
func (s *Service) auditExport(ctx context.Context, c identity.CurrentAdmin, id uint64, action, ip string) {
	_ = s.db.WithContext(ctx).Table("avatar_access_audit").Create(map[string]any{"id": idgen.Next(), "audit_no": fmt.Sprintf("AVAUD-%d", idgen.Next()), "actor_type": "USER", "actor_id": fmt.Sprint(c.UserID), "actor_role": firstRole(c), "action": action, "object_type": "AVATAR_EXPORT_JOB", "object_id": id, "purpose_code": "EXPORT", "result": "SUCCEEDED", "client_ip_hash": hash(ip)})
}
