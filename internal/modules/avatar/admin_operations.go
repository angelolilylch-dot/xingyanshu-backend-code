package avatar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idgen"
)

type cityConfigRequest struct {
	SupportContactName  string `json:"supportContactName" binding:"required"`
	SupportContactPhone string `json:"supportContactPhone" binding:"required"`
}
type notificationRequest struct {
	TemplateCode string `json:"templateCode"`
}

func cityOperator(c identity.CurrentAdmin, permission string) (uint64, string, *httpx.AppError) {
	if !hasAdminRole(c, "CITY_OPERATOR") || !c.HasPermission(permission) || c.OrgID == 0 || c.CityCode == "" {
		return 0, "", avatarError(403, "AUTH_FORBIDDEN", "无权管理城市数字人档案", nil)
	}
	return c.OrgID, c.CityCode, nil
}

func (s *Service) rosterTemplate(c *gin.Context) {
	f := excelize.NewFile()
	defer f.Close()
	sheet := f.GetSheetName(0)
	f.SetSheetName(sheet, "名单导入")
	sheet = "名单导入"
	_ = f.SetSheetRow(sheet, "A1", &[]any{"姓名", "手机号", "性别"})
	_ = f.SetColWidth(sheet, "A", "A", 18)
	_ = f.SetColWidth(sheet, "B", "B", 20)
	_ = f.SetColWidth(sheet, "C", "C", 12)
	_ = f.AddDataValidation(sheet, &excelize.DataValidation{Sqref: "C2:C1048576", Type: "list", Formula1: "\"男,女\""})
	f.SetPanes(sheet, &excelize.Panes{Freeze: true, Split: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", `attachment; filename="avatar-roster-template.xlsx"`)
	_ = f.Write(c.Writer)
}

func (s *Service) importRoster(ctx context.Context, c identity.CurrentAdmin, header *multipart.FileHeader) (any, *httpx.AppError) {
	cityID, cityCode, ae := cityOperator(c, "AVATAR_ROSTER_MANAGE")
	if ae != nil {
		return nil, ae
	}
	if header == nil || strings.ToLower(filepath.Ext(header.Filename)) != ".xlsx" {
		return nil, avatarError(400, "AVATAR_ROSTER_FILE_INVALID", "仅支持 .xlsx 名单文件", nil)
	}
	source, e := header.Open()
	if e != nil {
		return nil, avatarError(400, "AVATAR_ROSTER_FILE_INVALID", "读取导入文件失败", nil)
	}
	defer source.Close()
	body, e := io.ReadAll(source)
	if e != nil {
		return nil, avatarError(400, "AVATAR_ROSTER_FILE_INVALID", "读取导入文件失败", nil)
	}
	sum := sha256.Sum256(body)
	batchID := idgen.Next()
	now := time.Now()
	if e = s.db.WithContext(ctx).Table("avatar_roster_batch").Create(map[string]any{"id": batchID, "status": "IMPORTING", "version": 1, "batch_no": fmt.Sprintf("AVRB-%d", batchID), "city_org_id": cityID, "city_code": cityCode, "source_file_name": header.Filename, "source_file_hash": fmt.Sprintf("%x", sum[:]), "imported_by": c.UserID, "created_by": c.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "创建导入批次失败", nil)
	}
	book, e := excelize.OpenReader(bytes.NewReader(body))
	if e != nil {
		_ = s.failBatch(ctx, batchID, "FILE_PARSE_FAILED", "无法解析 xlsx 文件")
		return nil, avatarError(400, "AVATAR_ROSTER_FILE_INVALID", "无法解析 xlsx 文件", nil)
	}
	defer book.Close()
	sheets := book.GetSheetList()
	if len(sheets) == 0 {
		_ = s.failBatch(ctx, batchID, "FILE_EMPTY", "名单文件为空")
		return nil, avatarError(400, "AVATAR_ROSTER_FILE_INVALID", "名单文件为空", nil)
	}
	rows, e := book.GetRows(sheets[0])
	if e != nil || len(rows) < 1 || len(rows[0]) < 3 || strings.TrimSpace(rows[0][0]) != "姓名" || strings.TrimSpace(rows[0][1]) != "手机号" || strings.TrimSpace(rows[0][2]) != "性别" {
		_ = s.failBatch(ctx, batchID, "HEADER_INVALID", "表头必须为：姓名、手机号、性别")
		return nil, avatarError(400, "AVATAR_ROSTER_HEADER_INVALID", "表头必须为：姓名、手机号、性别", nil)
	}
	seen := map[string]bool{}
	total, success, failed := 0, 0, 0
	errors := make([]map[string]any, 0)
	for i, row := range rows[1:] {
		line := i + 2
		if len(row) == 0 || (len(row) > 0 && strings.TrimSpace(row[0]) == "" && len(row) < 2) {
			continue
		}
		total++
		var name, mobile, gender string
		if len(row) > 0 {
			name = strings.TrimSpace(row[0])
		}
		if len(row) > 1 {
			mobile = strings.TrimSpace(row[1])
		}
		if len(row) > 2 {
			gender = normalizeGender(row[2])
		}
		code, msg := "", ""
		if name == "" || !mobilePattern.MatchString(mobile) || gender == "" {
			code, msg = "VALIDATION_ROW_INVALID", "姓名、11位手机号和性别（男/女）均为必填"
		} else if seen[mobile] {
			code, msg = "DUPLICATE_IN_FILE", "同一文件内手机号重复"
		} else {
			seen[mobile] = true
			var count int64
			s.db.WithContext(ctx).Table("avatar_roster_member").Where("city_org_id=? AND mobile_hash=? AND status='ACTIVE'", cityID, hash(mobile)).Count(&count)
			if count > 0 {
				code, msg = "DUPLICATE_IN_CITY", "本城市已存在有效名单手机号"
			}
		}
		if code != "" {
			failed++
			errors = append(errors, map[string]any{"id": idgen.Next(), "roster_batch_id": batchID, "row_no": line, "error_code": code, "error_message": msg})
			continue
		}
		memberID := idgen.Next()
		e = s.db.WithContext(ctx).Table("avatar_roster_member").Create(map[string]any{"id": memberID, "status": "ACTIVE", "version": 1, "roster_batch_id": batchID, "city_org_id": cityID, "city_code": cityCode, "person_no": fmt.Sprintf("AVRM-%d", memberID), "person_type": "PROFESSIONAL_MODEL", "name_cipher": mustEncrypt(s.auth.JWTSecret, name), "name_hash": hash(name), "mobile_cipher": mustEncrypt(s.auth.JWTSecret, mobile), "mobile_hash": hash(mobile), "mobile_masked": maskMobile(mobile), "gender": gender, "registration_status": "PENDING", "created_by": c.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error
		if e != nil {
			failed++
			errors = append(errors, map[string]any{"id": idgen.Next(), "roster_batch_id": batchID, "row_no": line, "error_code": "ROW_INSERT_FAILED", "error_message": "该行无法入库，请核对重复数据"})
			continue
		}
		success++
	}
	if len(errors) > 0 {
		_ = s.db.WithContext(ctx).Table("avatar_roster_import_error").CreateInBatches(errors, 500).Error
	}
	status := "COMPLETED"
	if failed > 0 && success > 0 {
		status = "PARTIAL_FAILED"
	}
	if failed > 0 && success == 0 {
		status = "FAILED"
	}
	summary := fmt.Sprintf(`{"errorCount":%d}`, failed)
	_ = s.db.WithContext(ctx).Table("avatar_roster_batch").Where("id=?", batchID).Updates(map[string]any{"status": status, "total_count": total, "success_count": success, "failed_count": failed, "error_summary_json": summary, "imported_at": time.Now(), "updated_at": time.Now(), "status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error
	return gin.H{"batchId": fmt.Sprint(batchID), "status": status, "totalCount": total, "successCount": success, "failedCount": failed}, nil
}
func normalizeGender(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "男", "MALE", "M":
		return "MALE"
	case "女", "FEMALE", "F":
		return "FEMALE"
	}
	return ""
}
func (s *Service) failBatch(ctx context.Context, id uint64, code, msg string) error {
	return s.db.WithContext(ctx).Table("avatar_roster_batch").Where("id=?", id).Updates(map[string]any{"status": "FAILED", "status_reason_code": code, "status_reason": msg, "updated_at": time.Now()}).Error
}

func (s *Service) saveCityConfig(ctx context.Context, c identity.CurrentAdmin, r cityConfigRequest) (any, *httpx.AppError) {
	city, code, ae := cityOperator(c, "AVATAR_REGISTRATION_ENTRY_MANAGE")
	if ae != nil {
		return nil, ae
	}
	if !mobilePattern.MatchString(r.SupportContactPhone) {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "联系电话须为11位手机号", nil)
	}
	now := time.Now()
	cipher := mustEncrypt(s.auth.JWTSecret, r.SupportContactPhone)
	var exists int64
	s.db.WithContext(ctx).Table("avatar_city_registration_config").Where("city_org_id=?", city).Count(&exists)
	if exists == 0 {
		id := idgen.Next()
		e := s.db.WithContext(ctx).Table("avatar_city_registration_config").Create(map[string]any{"id": id, "status": "ACTIVE", "version": 1, "city_org_id": city, "city_code": code, "support_contact_name": r.SupportContactName, "support_phone_cipher": cipher, "support_phone_masked": maskMobile(r.SupportContactPhone), "created_by": c.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error
		if e != nil {
			return nil, avatarError(500, "INTERNAL_ERROR", "保存城市入口失败", nil)
		}
	} else {
		s.db.WithContext(ctx).Table("avatar_city_registration_config").Where("city_org_id=?", city).Updates(map[string]any{"status": "ACTIVE", "support_contact_name": r.SupportContactName, "support_phone_cipher": cipher, "support_phone_masked": maskMobile(r.SupportContactPhone), "updated_by": c.UserID, "updated_at": now})
	}
	return gin.H{"cityOrgId": fmt.Sprint(city), "supportContactName": r.SupportContactName, "supportPhoneMasked": maskMobile(r.SupportContactPhone)}, nil
}
func (s *Service) createQRCode(ctx context.Context, c identity.CurrentAdmin) (any, *httpx.AppError) {
	city, code, ae := cityOperator(c, "AVATAR_REGISTRATION_ENTRY_MANAGE")
	if ae != nil {
		return nil, ae
	}
	var conf struct{ ID uint64 }
	if e := s.db.WithContext(ctx).Table("avatar_city_registration_config").Where("city_org_id=? AND status='ACTIVE'", city).Take(&conf).Error; e != nil {
		return nil, avatarError(422, "AVATAR_CITY_CONFIG_REQUIRED", "请先配置城市建档联系人", nil)
	}
	token, e := randomToken()
	if e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "生成二维码失败", nil)
	}
	id := idgen.Next()
	now := time.Now()
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Table("avatar_registration_qrcode").Where("city_org_id=? AND status='ACTIVE'", city).Updates(map[string]any{"status": "REVOKED", "updated_at": now}).Error; e != nil {
			return e
		}
		return tx.Table("avatar_registration_qrcode").Create(map[string]any{"id": id, "status": "ACTIVE", "version": 1, "qrcode_no": fmt.Sprintf("AVQR-%d", id), "city_registration_config_id": conf.ID, "city_org_id": city, "city_code": code, "code_hash": hash(token), "created_by": c.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error
	})
	if e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "创建二维码失败", nil)
	}
	return gin.H{"qrcodeId": fmt.Sprint(id), "shortCode": token, "registrationPath": "/api/v1/avatar/public/registration-context?code=" + token, "status": "ACTIVE"}, nil
}

func (s *Service) listRosterBatches(ctx context.Context, c identity.CurrentAdmin) (any, *httpx.AppError) {
	city, _, ae := cityOperator(c, "AVATAR_PROGRESS_VIEW")
	if ae != nil {
		return nil, ae
	}
	var rows []map[string]any
	if e := s.db.WithContext(ctx).Table("avatar_roster_batch").Select("id,batch_no,status,total_count,success_count,failed_count,source_file_name,imported_at,created_at").Where("city_org_id=?", city).Order("created_at DESC").Limit(100).Find(&rows).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "查询导入批次失败", nil)
	}
	return gin.H{"items": rows}, nil
}
func (s *Service) rosterBatchDetail(ctx context.Context, c identity.CurrentAdmin, idText string) (any, *httpx.AppError) {
	city, _, ae := cityOperator(c, "AVATAR_PROGRESS_VIEW")
	if ae != nil {
		return nil, ae
	}
	id, ok := parseUint(idText)
	if !ok {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "导入批次编号不正确", nil)
	}
	var row map[string]any
	if e := s.db.WithContext(ctx).Table("avatar_roster_batch").Select("id,batch_no,status,total_count,success_count,failed_count,source_file_name,error_summary_json,imported_at,created_at").Where("id=? AND city_org_id=?", id, city).Take(&row).Error; e != nil {
		return nil, avatarError(404, "AVATAR_ROSTER_BATCH_NOT_FOUND", "导入批次不存在", nil)
	}
	return row, nil
}
func (s *Service) rosterErrorReceipt(c *gin.Context) {
	current, _ := identity.CurrentAdminFromContext(c)
	city, _, ae := cityOperator(current, "AVATAR_PROGRESS_VIEW")
	if ae != nil {
		httpx.AbortWithError(c, ae)
		return
	}
	id, ok := parseUint(c.Param("id"))
	if !ok {
		httpx.AbortWithError(c, avatarError(400, "VALIDATION_FORMAT_INVALID", "导入批次编号不正确", nil))
		return
	}
	var n int64
	s.db.WithContext(c.Request.Context()).Table("avatar_roster_batch").Where("id=? AND city_org_id=?", id, city).Count(&n)
	if n == 0 {
		httpx.AbortWithError(c, avatarError(404, "AVATAR_ROSTER_BATCH_NOT_FOUND", "导入批次不存在", nil))
		return
	}
	var rows []struct {
		RowNo                   int
		ErrorCode, ErrorMessage string
	}
	if e := s.db.WithContext(c.Request.Context()).Table("avatar_roster_import_error").Select("row_no,error_code,error_message").Where("roster_batch_id=?", id).Order("row_no").Find(&rows).Error; e != nil {
		httpx.AbortWithError(c, avatarError(500, "INTERNAL_ERROR", "生成错误回执失败", nil))
		return
	}
	book := excelize.NewFile()
	defer book.Close()
	sheet := book.GetSheetName(0)
	_ = book.SetSheetName(sheet, "导入错误回执")
	sheet = "导入错误回执"
	_ = book.SetSheetRow(sheet, "A1", &[]any{"原始行号", "错误代码", "错误说明"})
	for i, r := range rows {
		_ = book.SetSheetRow(sheet, fmt.Sprintf("A%d", i+2), &[]any{r.RowNo, r.ErrorCode, r.ErrorMessage})
	}
	_ = book.SetColWidth(sheet, "A", "A", 12)
	_ = book.SetColWidth(sheet, "B", "B", 28)
	_ = book.SetColWidth(sheet, "C", "C", 48)
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="avatar-roster-errors-%d.xlsx"`, id))
	_ = book.Write(c.Writer)
}
func (s *Service) listRosterMembers(ctx context.Context, c identity.CurrentAdmin) (any, *httpx.AppError) {
	city, _, ae := cityOperator(c, "AVATAR_PROGRESS_VIEW")
	if ae != nil {
		return nil, ae
	}
	var rows []struct {
		ID                                       uint64
		NameCipher                               []byte
		MobileMasked, Gender, RegistrationStatus string
		ProfileID                                *uint64
		CreatedAt                                time.Time
	}
	if e := s.db.WithContext(ctx).Table("avatar_roster_member").Select("id,name_cipher,mobile_masked,gender,registration_status,profile_id,created_at").Where("city_org_id=?", city).Order("created_at DESC").Limit(100).Find(&rows).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "查询名单失败", nil)
	}
	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		name, _ := decrypt(s.auth.JWTSecret, r.NameCipher)
		items = append(items, gin.H{"id": fmt.Sprint(r.ID), "nameMasked": maskName(name), "mobileMasked": r.MobileMasked, "gender": r.Gender, "registrationStatus": r.RegistrationStatus, "profileId": idText(r.ProfileID), "createdAt": r.CreatedAt})
	}
	return gin.H{"items": items}, nil
}
func (s *Service) listCityProfiles(ctx context.Context, c identity.CurrentAdmin) (any, *httpx.AppError) {
	city, _, ae := cityOperator(c, "AVATAR_PROGRESS_VIEW")
	if ae != nil {
		return nil, ae
	}
	var rows []struct {
		ID                                                                uint64
		ProfileNo, Status, CollectionStatus, QualityStatus, ArchiveStatus string
		RetentionUntil                                                    *time.Time
	}
	if e := s.db.WithContext(ctx).Table("avatar_profile").Select("id,profile_no,status,collection_status,quality_status,archive_status,retention_until").Where("city_org_id=?", city).Order("created_at DESC").Limit(100).Find(&rows).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "查询城市档案进度失败", nil)
	}
	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		items = append(items, gin.H{"id": fmt.Sprint(r.ID), "profileNo": r.ProfileNo, "status": r.Status, "collectionStatus": r.CollectionStatus, "qualityStatus": r.QualityStatus, "archiveStatus": r.ArchiveStatus, "retentionUntil": r.RetentionUntil})
	}
	return gin.H{"items": items}, nil
}
func maskName(v string) string {
	r := []rune(strings.TrimSpace(v))
	if len(r) == 0 {
		return ""
	}
	if len(r) == 1 {
		return "*"
	}
	return string(r[0]) + strings.Repeat("*", len(r)-1)
}
func idText(v *uint64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(*v)
}
func (s *Service) listQRCodes(ctx context.Context, c identity.CurrentAdmin) (any, *httpx.AppError) {
	city, _, ae := cityOperator(c, "AVATAR_REGISTRATION_ENTRY_MANAGE")
	if ae != nil {
		return nil, ae
	}
	var rows []map[string]any
	if e := s.db.WithContext(ctx).Table("avatar_registration_qrcode").Select("id,qrcode_no,status,scan_count,expires_at,created_at,updated_at").Where("city_org_id=?", city).Order("created_at DESC").Limit(100).Find(&rows).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "查询二维码失败", nil)
	}
	return gin.H{"items": rows}, nil
}
func (s *Service) changeQRCode(ctx context.Context, c identity.CurrentAdmin, idText, status string) (any, *httpx.AppError) {
	city, _, ae := cityOperator(c, "AVATAR_REGISTRATION_ENTRY_MANAGE")
	if ae != nil {
		return nil, ae
	}
	id, ok := parseUint(idText)
	if !ok {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "二维码编号不正确", nil)
	}
	r := s.db.WithContext(ctx).Table("avatar_registration_qrcode").Where("id=? AND city_org_id=?", id, city).Updates(map[string]any{"status": status, "updated_by": c.UserID, "updated_at": time.Now(), "status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)})
	if r.Error != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "更新二维码失败", nil)
	}
	if r.RowsAffected == 0 {
		return nil, avatarError(404, "AVATAR_QRCODE_NOT_FOUND", "二维码不存在", nil)
	}
	return gin.H{"qrcodeId": fmt.Sprint(id), "status": status}, nil
}
func (s *Service) sendInvitation(ctx context.Context, c identity.CurrentAdmin, idText string, r notificationRequest) (any, *httpx.AppError) {
	city, _, ae := cityOperator(c, "AVATAR_INVITATION_NOTIFY")
	if ae != nil {
		return nil, ae
	}
	id, ok := parseUint(idText)
	if !ok {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "名单人员编号不正确", nil)
	}
	var m struct {
		ID                 uint64
		MobileHash         string
		RegistrationStatus string
	}
	if e := s.db.WithContext(ctx).Table("avatar_roster_member").Where("id=? AND city_org_id=? AND status='ACTIVE'", id, city).Take(&m).Error; e != nil {
		return nil, avatarError(404, "AVATAR_ROSTER_MEMBER_NOT_FOUND", "名单人员不存在", nil)
	}
	if m.RegistrationStatus != "PENDING" {
		return nil, avatarError(422, "AVATAR_INVITATION_NOT_ALLOWED", "当前人员无需发送建档提醒", nil)
	}
	var recent int64
	s.db.WithContext(ctx).Table("avatar_sms_notification").Where("roster_member_id=? AND created_at>?", id, time.Now().Add(-24*time.Hour)).Count(&recent)
	if recent > 0 {
		return nil, avatarError(429, "AVATAR_INVITATION_RATE_LIMITED", "该人员24小时内已发送提醒", nil)
	}
	if s.environment != "test" {
		return nil, avatarError(503, "AVATAR_SMS_PROVIDER_NOT_CONFIGURED", "短信服务暂不可用", nil)
	}
	template := r.TemplateCode
	if template == "" {
		template = "AVATAR_ARCHIVE_INVITE"
	}
	nid := idgen.Next()
	now := time.Now()
	if e := s.db.WithContext(ctx).Table("avatar_sms_notification").Create(map[string]any{"id": nid, "status": "SENT", "version": 1, "notification_no": fmt.Sprintf("AVSN-%d", nid), "roster_member_id": id, "mobile_hash": m.MobileHash, "template_code": template, "send_channel": "SMS", "provider_request_no": fmt.Sprintf("TEST-AVATAR-INVITE-%d", nid), "sent_at": now, "created_by": c.UserID, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "记录短信通知失败", nil)
	}
	return gin.H{"notificationId": fmt.Sprint(nid), "status": "SENT"}, nil
}
