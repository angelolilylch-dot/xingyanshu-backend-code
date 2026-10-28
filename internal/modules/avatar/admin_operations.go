package avatar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/skip2/go-qrcode"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

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
	_ = f.SetSheetRow(sheet, "A1", &[]any{"姓名", "身份证号", "手机号", "性别"})
	_ = f.SetColWidth(sheet, "A", "A", 18)
	_ = f.SetColWidth(sheet, "B", "B", 24)
	_ = f.SetColWidth(sheet, "C", "C", 20)
	_ = f.SetColWidth(sheet, "D", "D", 12)
	_ = f.AddDataValidation(sheet, &excelize.DataValidation{Sqref: "D2:D1048576", Type: "list", Formula1: "\"男,女\""})
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
	if e != nil || len(rows) < 1 || len(rows[0]) < 4 || strings.TrimSpace(rows[0][0]) != "姓名" || strings.TrimSpace(rows[0][1]) != "身份证号" || strings.TrimSpace(rows[0][2]) != "手机号" || strings.TrimSpace(rows[0][3]) != "性别" {
		_ = s.failBatch(ctx, batchID, "HEADER_INVALID", "表头必须为：姓名、身份证号、手机号、性别")
		return nil, avatarError(400, "AVATAR_ROSTER_HEADER_INVALID", "表头必须为：姓名、身份证号、手机号、性别", nil)
	}
	seenMobile, seenID := map[string]bool{}, map[string]bool{}
	total, success, failed := 0, 0, 0
	errors := make([]map[string]any, 0)
	for i, row := range rows[1:] {
		line := i + 2
		if len(row) == 0 || (len(row) > 0 && strings.TrimSpace(row[0]) == "" && len(row) < 2) {
			continue
		}
		total++
		var name, idNumber, mobile, gender string
		if len(row) > 0 {
			name = strings.TrimSpace(row[0])
		}
		if len(row) > 1 {
			idNumber = strings.ToUpper(strings.TrimSpace(row[1]))
		}
		if len(row) > 2 {
			mobile = strings.TrimSpace(row[2])
		}
		if len(row) > 3 {
			gender = normalizeGender(row[3])
		}
		code := ""
		idHash := hash(idNumber)
		if name == "" || !validChineseIDNumber(idNumber) || !mobilePattern.MatchString(mobile) || gender == "" {
			code = "VALIDATION_ROW_INVALID"
		} else if seenMobile[mobile] || seenID[idHash] {
			code = "DUPLICATE_IN_FILE"
		} else {
			seenMobile[mobile], seenID[idHash] = true, true
			var count int64
			s.db.WithContext(ctx).Table("avatar_roster_member").Where("city_org_id=? AND (mobile_hash=? OR id_number_hash=?) AND status IN ('PENDING_IDENTITY','ACTIVE')", cityID, hash(mobile), idHash).Count(&count)
			if count > 0 {
				code = "DUPLICATE_IN_CITY"
			}
		}
		if code != "" {
			failed++
			errors = append(errors, map[string]any{"id": idgen.Next(), "roster_batch_id": batchID, "row_no": line, "error_code": code, "error_message": detailedRosterImportMessage(code, name, idNumber, mobile, gender), "name_hash": hash(name), "mobile_hash": hash(mobile), "id_number_hash": idHash})
			continue
		}
		memberID := idgen.Next()
		e = s.db.WithContext(ctx).Table("avatar_roster_member").Create(map[string]any{"id": memberID, "status": "PENDING_IDENTITY", "version": 1, "roster_batch_id": batchID, "city_org_id": cityID, "city_code": cityCode, "person_no": fmt.Sprintf("AVRM-%d", memberID), "person_type": "PROFESSIONAL_MODEL", "name_cipher": mustEncrypt(s.auth.JWTSecret, name), "name_hash": hash(name), "id_number_cipher": mustEncrypt(s.auth.JWTSecret, idNumber), "id_number_hash": idHash, "id_number_last4": idNumber[len(idNumber)-4:], "mobile_cipher": mustEncrypt(s.auth.JWTSecret, mobile), "mobile_hash": hash(mobile), "mobile_masked": maskMobile(mobile), "gender": gender, "registration_status": "PENDING", "created_by": c.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error
		if e != nil {
			failed++
			errors = append(errors, map[string]any{"id": idgen.Next(), "roster_batch_id": batchID, "row_no": line, "error_code": "ROW_INSERT_FAILED", "error_message": "该行无法入库，请核对重复数据", "name_hash": hash(name), "mobile_hash": hash(mobile), "id_number_hash": idHash})
			continue
		}
		success++
	}
	if len(errors) > 0 {
		_ = s.db.WithContext(ctx).Table("avatar_roster_import_error").CreateInBatches(errors, 500).Error
	}
	status := "PENDING_IDENTITY"
	if success == 0 {
		status = "FAILED"
	}
	summary := fmt.Sprintf(`{"errorCount":%d}`, failed)
	_ = s.db.WithContext(ctx).Table("avatar_roster_batch").Where("id=?", batchID).Updates(map[string]any{"status": status, "total_count": total, "success_count": 0, "failed_count": failed, "error_summary_json": summary, "imported_at": time.Now(), "updated_at": time.Now(), "status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error
	return gin.H{"batchId": fmt.Sprint(batchID), "status": status, "totalCount": total, "pendingIdentityCount": success, "successCount": 0, "failedCount": failed}, nil
}

func detailedRosterImportMessage(code, name, idNumber, mobile, gender string) string {
	if code != "VALIDATION_ROW_INVALID" {
		switch code {
		case "DUPLICATE_IN_FILE":
			return "同一文件内手机号或身份证号重复，请只保留一行"
		case "DUPLICATE_IN_CITY":
			return "本城市已存在该手机号或身份证号对应的有效名单，无需重复导入"
		default:
			return "该行无法入库，请核对重复数据"
		}
	}
	issues := make([]string, 0, 4)
	if name == "" {
		issues = append(issues, "姓名不能为空")
	}
	if !validChineseIDNumber(idNumber) {
		issues = append(issues, "身份证号格式或校验位不正确")
	}
	if !mobilePattern.MatchString(mobile) {
		issues = append(issues, "手机号必须为11位中国大陆手机号")
	}
	if gender == "" {
		issues = append(issues, "性别必须填写男或女")
	}
	return strings.Join(issues, "；")
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
	var row registrationQRCode
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_registration_qrcode").Select("id,qrcode_no,status,scan_count,expires_at,created_at,updated_at,code_cipher").Where("city_org_id=? AND status IN ('ACTIVE','PAUSED')", city).Order("created_at DESC").Take(&row).Error
		if err == nil {
			if len(row.CodeCipher) == 0 {
				return errQRCodeLegacy
			}
			return nil
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		var createErr error
		row, createErr = s.newPersistentQRCode(tx, city, code, c.UserID)
		created = createErr == nil
		return createErr
	})
	if err == errQRCodeLegacy {
		return nil, avatarError(409, "AVATAR_QRCODE_ROTATION_REQUIRED", "历史二维码无法再次下载，请显式更换二维码", nil)
	}
	if err != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "创建二维码失败", nil)
	}
	data, err := s.qrcodeResponse(row)
	if err != nil {
		return nil, avatarError(503, "AVATAR_H5_ENTRY_NOT_CONFIGURED", "建档 H5 入口尚未配置", nil)
	}
	data["reused"] = !created
	return data, nil
}

var errQRCodeLegacy = fmt.Errorf("qrcode legacy")

type registrationQRCode struct {
	ID         uint64     `gorm:"column:id"`
	QRCodeNo   string     `gorm:"column:qrcode_no"`
	Status     string     `gorm:"column:status"`
	ScanCount  uint64     `gorm:"column:scan_count"`
	ExpiresAt  *time.Time `gorm:"column:expires_at"`
	CreatedAt  *time.Time `gorm:"column:created_at"`
	UpdatedAt  *time.Time `gorm:"column:updated_at"`
	CodeCipher []byte     `gorm:"column:code_cipher"`
}

func (s *Service) newPersistentQRCode(tx *gorm.DB, city uint64, cityCode string, userID uint64) (registrationQRCode, error) {
	token, err := randomToken()
	if err != nil {
		return registrationQRCode{}, err
	}
	cipher, err := encrypt(s.auth.JWTSecret, token)
	if err != nil {
		return registrationQRCode{}, err
	}
	now := time.Now()
	id := idgen.Next()
	row := registrationQRCode{ID: id, QRCodeNo: fmt.Sprintf("AVQR-%d", id), Status: "ACTIVE", CodeCipher: cipher}
	if err := tx.Table("avatar_registration_qrcode").Create(map[string]any{"id": id, "status": row.Status, "version": 1, "qrcode_no": row.QRCodeNo, "city_registration_config_id": nil, "city_org_id": city, "city_code": cityCode, "code_hash": hash(token), "code_cipher": cipher, "created_by": userID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(userID)}).Error; err != nil {
		return registrationQRCode{}, err
	}
	return row, nil
}

func (s *Service) qrcodeResponse(row registrationQRCode) (gin.H, error) {
	token, err := decrypt(s.auth.JWTSecret, row.CodeCipher)
	if err != nil || token == "" {
		return nil, fmt.Errorf("decrypt QR code: %w", err)
	}
	entryURL, err := s.registrationH5URL(token)
	if err != nil {
		return nil, err
	}
	return gin.H{"qrcodeId": fmt.Sprint(row.ID), "qrcodeNo": row.QRCodeNo, "shortCode": token, "registrationURL": entryURL, "registrationPath": "/api/v1/avatar/public/registration-context?code=" + token, "status": row.Status, "scanCount": row.ScanCount, "expiresAt": row.ExpiresAt, "createdAt": row.CreatedAt, "updatedAt": row.UpdatedAt, "downloadPath": "/api/v1/avatar/registration-qrcodes/" + fmt.Sprint(row.ID) + "/download"}, nil
}

// registrationH5URL returns the exact URL that must be encoded into a city QR
// code. The short code is only an opaque city-entry locator, never a credential.
func (s *Service) registrationH5URL(code string) (string, error) {
	base := strings.TrimSpace(s.settings.RegistrationH5URL)
	if base == "" {
		return "", fmt.Errorf("registration h5 URL is empty")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid registration h5 URL")
	}
	query := u.Query()
	query.Set("code", code)
	u.RawQuery = query.Encode()
	return u.String(), nil
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
	var rows []registrationQRCode
	if e := s.db.WithContext(ctx).Table("avatar_registration_qrcode").Select("id,qrcode_no,status,scan_count,expires_at,created_at,updated_at,code_cipher").Where("city_org_id=?", city).Order("created_at DESC").Limit(100).Find(&rows).Error; e != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "查询二维码失败", nil)
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		item := gin.H{"id": fmt.Sprint(row.ID), "qrcodeNo": row.QRCodeNo, "status": row.Status, "scanCount": row.ScanCount, "expiresAt": row.ExpiresAt, "createdAt": row.CreatedAt, "updatedAt": row.UpdatedAt, "canDownload": false}
		if row.Status != "REVOKED" && len(row.CodeCipher) > 0 {
			if data, err := s.qrcodeResponse(row); err == nil {
				item["registrationURL"] = data["registrationURL"]
				item["downloadPath"] = data["downloadPath"]
				item["canDownload"] = true
			}
		}
		items = append(items, item)
	}
	return gin.H{"items": items}, nil
}

func (s *Service) rotateQRCode(ctx context.Context, c identity.CurrentAdmin, idText string) (any, *httpx.AppError) {
	city, code, ae := cityOperator(c, "AVATAR_REGISTRATION_ENTRY_MANAGE")
	if ae != nil {
		return nil, ae
	}
	id, ok := parseUint(idText)
	if !ok {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "二维码编号不正确", nil)
	}
	var row registrationQRCode
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old registrationQRCode
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_registration_qrcode").Select("id,status").Where("id=? AND city_org_id=?", id, city).Take(&old).Error; err != nil {
			return err
		}
		if old.Status == "REVOKED" {
			return fmt.Errorf("revoked")
		}
		now := time.Now()
		if err := tx.Table("avatar_registration_qrcode").Where("id=?", id).Updates(map[string]any{"status": "REVOKED", "updated_by": c.UserID, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error; err != nil {
			return err
		}
		var createErr error
		row, createErr = s.newPersistentQRCode(tx, city, code, c.UserID)
		return createErr
	})
	if err == gorm.ErrRecordNotFound {
		return nil, avatarError(404, "AVATAR_QRCODE_NOT_FOUND", "二维码不存在", nil)
	}
	if err != nil {
		return nil, avatarError(409, "AVATAR_QRCODE_STATE_INVALID", "当前二维码不可更换", nil)
	}
	data, err := s.qrcodeResponse(row)
	if err != nil {
		return nil, avatarError(503, "AVATAR_H5_ENTRY_NOT_CONFIGURED", "建档 H5 入口尚未配置", nil)
	}
	data["rotated"] = true
	return data, nil
}

func (s *Service) downloadQRCode(ctx context.Context, c identity.CurrentAdmin, idText string) ([]byte, string, *httpx.AppError) {
	city, _, ae := cityOperator(c, "AVATAR_REGISTRATION_ENTRY_MANAGE")
	if ae != nil {
		return nil, "", ae
	}
	id, ok := parseUint(idText)
	if !ok {
		return nil, "", avatarError(400, "VALIDATION_FORMAT_INVALID", "二维码编号不正确", nil)
	}
	var row registrationQRCode
	if err := s.db.WithContext(ctx).Table("avatar_registration_qrcode").Select("id,qrcode_no,status,code_cipher").Where("id=? AND city_org_id=?", id, city).Take(&row).Error; err != nil {
		return nil, "", avatarError(404, "AVATAR_QRCODE_NOT_FOUND", "二维码不存在", nil)
	}
	if row.Status == "REVOKED" || len(row.CodeCipher) == 0 {
		return nil, "", avatarError(409, "AVATAR_QRCODE_STATE_INVALID", "当前二维码不可下载", nil)
	}
	data, err := s.qrcodeResponse(row)
	if err != nil {
		return nil, "", avatarError(503, "AVATAR_H5_ENTRY_NOT_CONFIGURED", "建档 H5 入口尚未配置", nil)
	}
	png, err := qrcode.Encode(fmt.Sprint(data["registrationURL"]), qrcode.Medium, 512)
	if err != nil {
		return nil, "", avatarError(500, "INTERNAL_ERROR", "生成二维码图片失败", nil)
	}
	return png, row.QRCodeNo + ".png", nil
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
	if status == "ACTIVE" {
		var count int64
		if e := s.db.WithContext(ctx).Table("avatar_registration_qrcode").Where("city_org_id=? AND status='ACTIVE' AND id<>?", city, id).Count(&count).Error; e != nil {
			return nil, avatarError(500, "INTERNAL_ERROR", "查询二维码失败", nil)
		}
		if count > 0 {
			return nil, avatarError(409, "AVATAR_QRCODE_ACTIVE_EXISTS", "当前城市已有启用二维码", nil)
		}
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
