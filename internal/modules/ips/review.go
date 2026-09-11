package ips

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/evidence"
	"xingyanshu-backend/internal/platform/idgen"
)

const complianceIPPermission = "IP_REVIEW_COMPLIANCE"

// ListPendingCompliance 返回版权/合规日常处理的主队列。该队列只包含已提交且尚未完成权属确权的 IP。
func (s *Service) ListPendingCompliance(ctx context.Context, current identity.CurrentAdmin, keyword, dciCertificateStatus string, pageNo, pageSize int) (map[string]any, string, error) {
	if !current.HasPermission(complianceIPPermission) {
		return nil, "AUTH_FORBIDDEN", nil
	}
	query := s.db.WithContext(ctx).Table("ip_asset a").
		Joins("LEFT JOIN sys_org o ON o.id=a.main_city_org_id").
		Joins("LEFT JOIN ip_right_holder h ON h.ip_id=a.id").
		Joins("LEFT JOIN ip_draft_auth_selection s ON s.ip_id=a.id").
		Joins("LEFT JOIN auth_plan_version av ON av.id=s.auth_plan_version_id").
		Joins("LEFT JOIN auth_plan ap ON ap.id=av.plan_id").
		Joins("LEFT JOIN finance_distribution_plan dp ON dp.id=s.distribution_template_id").
		Where("a.status='PENDING_COMPLIANCE'")
	if strings.TrimSpace(keyword) != "" {
		like := "%" + strings.TrimSpace(keyword) + "%"
		query = query.Where("a.ip_no LIKE ? OR a.ip_name LIKE ?", like, like)
	}
	if dciCertificateStatus != "" {
		query = query.Where("a.dci_certificate_status=?", dciCertificateStatus)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	type row struct {
		ID                   uint64    `gorm:"column:id"`
		Version              uint32    `gorm:"column:version"`
		IPNo                 string    `gorm:"column:ip_no"`
		IPName               string    `gorm:"column:ip_name"`
		CityOrgID            uint64    `gorm:"column:main_city_org_id"`
		CityOrgName          string    `gorm:"column:city_org_name"`
		HolderNameMasked     string    `gorm:"column:holder_name_masked"`
		DCICertificateStatus string    `gorm:"column:dci_certificate_status"`
		AuthPlanVersionID    uint64    `gorm:"column:auth_plan_version_id"`
		AuthPlanName         string    `gorm:"column:auth_plan_name"`
		TemplateID           uint64    `gorm:"column:distribution_template_id"`
		TemplateName         string    `gorm:"column:distribution_template_name"`
		HasEvaluationReport  bool      `gorm:"column:has_evaluation_report"`
		UpdatedAt            time.Time `gorm:"column:updated_at"`
	}
	var rows []row
	selects := "a.id,a.version,a.ip_no,a.ip_name,a.main_city_org_id,o.org_name AS city_org_name,h.holder_name_masked,a.dci_certificate_status,s.auth_plan_version_id,ap.plan_name AS auth_plan_name,s.distribution_template_id,dp.distribution_name AS distribution_template_name,EXISTS(SELECT 1 FROM ip_evaluation_report er WHERE er.ip_id=a.id AND er.status='ACTIVE') AS has_evaluation_report,a.updated_at"
	if err := query.Select(selects).Order("a.updated_at ASC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Scan(&rows).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, item := range rows {
		nextAction := "COMPLETE_DCI_MATERIAL"
		if item.DCICertificateStatus == "UPLOADED" {
			nextAction = "VERIFY_DCI"
		} else if item.DCICertificateStatus == "VERIFIED" {
			nextAction = "CONFIRM_RIGHTS"
		}
		items = append(items, map[string]any{
			"id": fmt.Sprint(item.ID), "version": item.Version, "ipNo": item.IPNo, "ipName": item.IPName,
			"mainCityOrgId": fmt.Sprint(item.CityOrgID), "mainCityOrgName": item.CityOrgName,
			"rightHolderNameMasked": item.HolderNameMasked, "dciCertificateStatus": item.DCICertificateStatus,
			"authPlanVersionId": fmt.Sprint(item.AuthPlanVersionID), "authPlanName": item.AuthPlanName,
			"distributionTemplateId": fmt.Sprint(item.TemplateID), "distributionTemplateName": item.TemplateName,
			"hasEvaluationReport": item.HasEvaluationReport, "nextAction": nextAction, "updatedAt": item.UpdatedAt,
		})
	}
	return map[string]any{"items": items, "pageNo": pageNo, "pageSize": pageSize, "total": total, "totalPages": int((total + int64(pageSize) - 1) / int64(pageSize))}, "", nil
}

// ListDCIAssistance 向版权/合规人员提供完整联系方式；读取动作单独审计且审计内容不保存明文。
func (s *Service) ListDCIAssistance(ctx context.Context, current identity.CurrentAdmin, handlingStatus, keyword string, pageNo, pageSize int, clientIP string) (map[string]any, string, error) {
	if !current.HasPermission(complianceIPPermission) {
		return nil, "AUTH_FORBIDDEN", nil
	}
	query := s.db.WithContext(ctx).Table("ip_dci_assistance_request r").Joins("JOIN ip_asset a ON a.id=r.ip_id")
	if handlingStatus != "" {
		query = query.Where("r.handling_status=?", handlingStatus)
	}
	if strings.TrimSpace(keyword) != "" {
		like := "%" + strings.TrimSpace(keyword) + "%"
		query = query.Where("r.request_no LIKE ? OR a.ip_no LIKE ? OR a.ip_name LIKE ?", like, like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	type row struct {
		ID                   uint64    `gorm:"column:id"`
		Version              uint32    `gorm:"column:version"`
		RequestNo            string    `gorm:"column:request_no"`
		IPID                 uint64    `gorm:"column:ip_id"`
		IPNo                 string    `gorm:"column:ip_no"`
		IPName               string    `gorm:"column:ip_name"`
		ContactNameCipher    []byte    `gorm:"column:contact_name_cipher"`
		ContactMobileCipher  []byte    `gorm:"column:contact_mobile_cipher"`
		PreferredContactTime string    `gorm:"column:preferred_contact_time"`
		RequestNote          string    `gorm:"column:request_note"`
		HandlingStatus       string    `gorm:"column:handling_status"`
		HandlingNote         string    `gorm:"column:handling_note"`
		CreatedAt            time.Time `gorm:"column:created_at"`
	}
	var rows []row
	if err := query.Select("r.id,r.version,r.request_no,r.ip_id,a.ip_no,a.ip_name,r.contact_name_cipher,r.contact_mobile_cipher,r.preferred_contact_time,r.request_note,r.handling_status,r.handling_note,r.created_at").Order("r.created_at DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Scan(&rows).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, item := range rows {
		name, err := decrypt(s.secret, item.ContactNameCipher)
		if err != nil {
			return nil, "INTERNAL_ERROR", err
		}
		mobile, err := decrypt(s.secret, item.ContactMobileCipher)
		if err != nil {
			return nil, "INTERNAL_ERROR", err
		}
		items = append(items, map[string]any{"id": fmt.Sprint(item.ID), "version": item.Version, "requestNo": item.RequestNo, "ipId": fmt.Sprint(item.IPID), "ipNo": item.IPNo, "ipName": item.IPName, "contactName": name, "contactMobile": mobile, "preferredContactTime": item.PreferredContactTime, "requestNote": item.RequestNote, "handlingStatus": item.HandlingStatus, "handlingNote": item.HandlingNote, "createdAt": item.CreatedAt})
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return audit.WriteLog(tx, "DCI_ASSISTANCE_CONTACT_LIST_VIEWED", "IP_DCI_ASSISTANCE_REQUEST", 0, nil, map[string]any{"count": len(items)}, "SUCCESS", ipOperator(current, clientIP))
	}); err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"items": items, "pageNo": pageNo, "pageSize": pageSize, "total": total, "totalPages": int((total + int64(pageSize) - 1) / int64(pageSize))}, "", nil
}

// HandleDCIAssistance 只记录代办联系进度，不替代后续 DCI 证书核验。
func (s *Service) HandleDCIAssistance(ctx context.Context, current identity.CurrentAdmin, requestID uint64, version uint32, input DCIHandleRequest, clientIP string) (map[string]any, string, error) {
	if !current.HasPermission(complianceIPPermission) {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if code, err := s.moderate(ctx, requestID, current.UserID, input.HandlingNote); code != "" {
		return nil, code, err
	}
	var request struct {
		Version        uint32
		HandlingStatus string
	}
	if err := s.db.WithContext(ctx).Table("ip_dci_assistance_request").Where("id=?", requestID).Take(&request).Error; err != nil {
		return nil, "RESOURCE_NOT_FOUND", nil
	}
	if request.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	if request.HandlingStatus == "COMPLETED" || request.HandlingStatus == "CLOSED" {
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	now := time.Now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		values := map[string]any{"handling_status": input.HandlingStatus, "handling_note": input.HandlingNote, "handled_by": current.UserID, "handled_at": now, "updated_by": current.UserID, "version": version + 1, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}
		if err := tx.Table("ip_dci_assistance_request").Where("id=? AND version=?", requestID, version).Updates(values).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "IP_DCI_ASSISTANCE_HANDLED", "IP_DCI_ASSISTANCE_REQUEST", requestID, map[string]any{"handlingStatus": request.HandlingStatus}, map[string]any{"handlingStatus": input.HandlingStatus}, "SUCCESS", ipOperator(current, clientIP))
	})
	if err != nil {
		return nil, "RESOURCE_CONFLICT", err
	}
	return map[string]any{"id": fmt.Sprint(requestID), "handlingStatus": input.HandlingStatus, "version": version + 1}, "", nil
}

// VerifyDCI 通过后仅更新 DCI 独立状态；权属确权仍需另行审核确认。
func (s *Service) VerifyDCI(ctx context.Context, current identity.CurrentAdmin, ipID uint64, version uint32, input DCIVerificationRequest, clientIP string) (map[string]any, string, error) {
	asset, code := s.pendingComplianceAsset(ctx, current, ipID, version)
	if code != "" {
		return nil, code, nil
	}
	if asset.DCICertificateStatus != "UPLOADED" {
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	if code, err := s.moderate(ctx, ipID, current.UserID, input.Comment); code != "" {
		return nil, code, err
	}
	now := time.Now()
	nextStatus, dciStatus := "PENDING_COMPLIANCE", "VERIFIED"
	if input.Result == "RETURN" {
		nextStatus, dciStatus = "SUPPLEMENT_REQUIRED", "REJECTED"
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		values := map[string]any{"status": nextStatus, "dci_certificate_status": dciStatus, "status_reason": input.Comment, "version": version + 1, "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}
		if input.Result == "PASS" {
			values["dci_verified_at"] = now
			values["dci_verified_by"] = current.UserID
		}
		if err := tx.Table("ip_asset").Where("id=? AND version=?", ipID, version).Updates(values).Error; err != nil {
			return err
		}
		if err := s.writeReview(tx, ipID, "DCI_VERIFICATION", input.Result, input.Comment, nil, current.UserID); err != nil {
			return err
		}
		if nextStatus != asset.Status {
			return audit.WriteStatusHistory(tx, "IP", ipID, asset.Status, nextStatus, input.Comment, ipOperator(current, clientIP))
		}
		return audit.WriteLog(tx, "IP_DCI_VERIFIED", "IP", ipID, nil, map[string]any{"result": input.Result}, "SUCCESS", ipOperator(current, clientIP))
	})
	if err != nil {
		return nil, "RESOURCE_CONFLICT", err
	}
	return map[string]any{"id": fmt.Sprint(ipID), "status": nextStatus, "dciCertificateStatus": dciStatus, "version": version + 1}, "", nil
}

// ReviewRights 退回必须留下理由；确认则先取得存证成功回执，再冻结方案和比例并自动进入商城上架状态。
func (s *Service) ReviewRights(ctx context.Context, current identity.CurrentAdmin, ipID uint64, version uint32, input RightsReviewRequest, clientIP string) (map[string]any, string, error) {
	asset, code := s.pendingComplianceAsset(ctx, current, ipID, version)
	if code != "" {
		return nil, code, nil
	}
	if code, err := s.moderate(ctx, ipID, current.UserID, strings.Join(append([]string{input.Comment}, input.SupplementItems...), "\n")); code != "" {
		return nil, code, err
	}
	if input.Result == "RETURN" {
		return s.returnForSupplement(ctx, current, ipID, version, input, clientIP)
	}
	if asset.DCICertificateStatus != "VERIFIED" || input.AuthPlanVersionID == 0 || input.DistributionTemplateID == 0 || len(input.DistributionItems) == 0 {
		return nil, "VALIDATION_REQUIRED", nil
	}
	if strings.TrimSpace(asset.ShortIntro) == "" || strings.TrimSpace(asset.DetailContent) == "" || strings.TrimSpace(asset.PublicCopyrightNotice) == "" || strings.TrimSpace(asset.PurchaseNotice) == "" || asset.CoverFileID == 0 {
		return nil, "VALIDATION_REQUIRED", nil
	}
	if code := s.validateSelection(ctx, input.AuthPlanVersionID, input.DistributionTemplateID, input.DistributionItems); code != "" {
		return nil, code, nil
	}
	hashValue, code, err := s.confirmationHash(ctx, ipID, input)
	if code != "" {
		return nil, code, err
	}
	if s.evidence == nil {
		return nil, "EVIDENCE_UNAVAILABLE", nil
	}
	evidenceID, code, err := s.evidence.Submit(ctx, evidence.SubmitRequest{ObjectType: "IP", ObjectID: ipID, EvidenceType: "IP_RIGHTS_CONFIRMATION", HashValue: hashValue})
	if code != "" {
		return nil, code, err
	}
	var evidenceRow struct{ Status, ReceiptNo string }
	if err := s.db.WithContext(ctx).Table("evidence_record").Where("id=?", evidenceID).Take(&evidenceRow).Error; err != nil || evidenceRow.Status != "SUCCESS" || evidenceRow.ReceiptNo == "" {
		return nil, "EVIDENCE_NOT_CONFIRMED", err
	}
	now := time.Now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.saveConfirmedBinding(tx, ipID, current.UserID, input); err != nil {
			return err
		}
		values := map[string]any{"status": "LISTED", "mall_visible": true, "status_reason": input.Comment, "version": version + 1, "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}
		if err := tx.Table("ip_asset").Where("id=? AND version=?", ipID, version).Updates(values).Error; err != nil {
			return err
		}
		if err := s.writeReview(tx, ipID, "RIGHTS_CONFIRMATION", "CONFIRM", input.Comment, nil, current.UserID); err != nil {
			return err
		}
		if err := audit.WriteStatusHistory(tx, "IP", ipID, asset.Status, "LISTED", input.Comment, ipOperator(current, clientIP)); err != nil {
			return err
		}
		return audit.WriteLog(tx, "IP_RIGHTS_CONFIRMED_AND_LISTED", "IP", ipID, nil, map[string]any{"evidenceId": fmt.Sprint(evidenceID), "status": "LISTED", "mallVisible": true}, "SUCCESS", ipOperator(current, clientIP))
	})
	if err != nil {
		return nil, "RESOURCE_CONFLICT", err
	}
	return map[string]any{"id": fmt.Sprint(ipID), "status": "LISTED", "mallVisible": true, "version": version + 1, "evidenceId": fmt.Sprint(evidenceID), "evidenceReceiptNo": evidenceRow.ReceiptNo}, "", nil
}

// Pause 将已上架 IP 暂停销售，但不影响已经生效的历史授权。
func (s *Service) Pause(ctx context.Context, current identity.CurrentAdmin, ipID uint64, version uint32, input IPStatusChangeRequest, clientIP string) (map[string]any, string, error) {
	if !current.HasPermission("AUTH_PLAN_MANAGE") {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if strings.TrimSpace(input.Comment) == "" {
		return nil, "VALIDATION_REQUIRED", nil
	}
	var asset struct {
		Status  string
		Version uint32
	}
	if err := s.db.WithContext(ctx).Table("ip_asset").Where("id=?", ipID).Take(&asset).Error; err != nil {
		return nil, "RESOURCE_NOT_FOUND", nil
	}
	if asset.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	if asset.Status != "LISTED" {
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	now := time.Now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Table("ip_asset").Where("id=? AND version=? AND status='LISTED'", ipID, version).Updates(map[string]any{
			"status": "PAUSED", "mall_visible": false, "status_reason": input.Comment,
			"version": version + 1, "updated_by": current.UserID, "status_changed_at": now,
			"status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := audit.WriteStatusHistory(tx, "IP", ipID, "LISTED", "PAUSED", input.Comment, ipOperator(current, clientIP)); err != nil {
			return err
		}
		return audit.WriteLog(tx, "IP_PAUSED", "IP", ipID, map[string]any{"status": "LISTED", "mallVisible": true}, map[string]any{"status": "PAUSED", "mallVisible": false, "reason": input.Comment}, "SUCCESS", ipOperator(current, clientIP))
	})
	if err == gorm.ErrRecordNotFound {
		return nil, "VERSION_CONFLICT", nil
	}
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"id": fmt.Sprint(ipID), "status": "PAUSED", "mallVisible": false, "version": version + 1}, "", nil
}

// Resume 将暂停 IP 恢复到商城，但恢复前必须重新满足商城展示和购买前置条件。
func (s *Service) Resume(ctx context.Context, current identity.CurrentAdmin, ipID uint64, version uint32, input IPStatusChangeRequest, clientIP string) (map[string]any, string, error) {
	if !current.HasPermission("AUTH_PLAN_MANAGE") {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if strings.TrimSpace(input.Comment) == "" {
		return nil, "VALIDATION_REQUIRED", nil
	}
	var asset struct {
		Status                string
		Version               uint32
		DCICertificateStatus  string `gorm:"column:dci_certificate_status"`
		ShortIntro            string `gorm:"column:short_intro"`
		DetailContent         string `gorm:"column:detail_content"`
		PublicCopyrightNotice string `gorm:"column:public_copyright_notice"`
		PurchaseNotice        string `gorm:"column:purchase_notice"`
		CoverFileID           uint64 `gorm:"column:cover_file_id"`
	}
	if err := s.db.WithContext(ctx).Table("ip_asset").Where("id=?", ipID).Take(&asset).Error; err != nil {
		return nil, "RESOURCE_NOT_FOUND", nil
	}
	if asset.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	if asset.Status != "PAUSED" {
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	if asset.DCICertificateStatus != "VERIFIED" || asset.CoverFileID == 0 || strings.TrimSpace(asset.ShortIntro) == "" || strings.TrimSpace(asset.DetailContent) == "" || strings.TrimSpace(asset.PublicCopyrightNotice) == "" || strings.TrimSpace(asset.PurchaseNotice) == "" {
		return nil, "IP_LISTING_PREREQUISITE_MISSING", nil
	}
	var bindingCount, evidenceCount int64
	if err := s.db.WithContext(ctx).Table("ip_auth_binding b").Joins("JOIN auth_plan_version v ON v.id=b.auth_plan_version_id AND v.status='ENABLED'").Where("b.ip_id=? AND b.status='ACTIVE'", ipID).Count(&bindingCount).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	if err := s.db.WithContext(ctx).Table("evidence_record").Where("object_type='IP' AND object_id=? AND status='SUCCESS'", ipID).Count(&evidenceCount).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	if bindingCount != 1 || evidenceCount == 0 {
		return nil, "IP_LISTING_PREREQUISITE_MISSING", nil
	}
	now := time.Now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Table("ip_asset").Where("id=? AND version=? AND status='PAUSED'", ipID, version).Updates(map[string]any{
			"status": "LISTED", "mall_visible": true, "status_reason": input.Comment,
			"version": version + 1, "updated_by": current.UserID, "status_changed_at": now,
			"status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := audit.WriteStatusHistory(tx, "IP", ipID, "PAUSED", "LISTED", input.Comment, ipOperator(current, clientIP)); err != nil {
			return err
		}
		return audit.WriteLog(tx, "IP_RESUMED", "IP", ipID, map[string]any{"status": "PAUSED", "mallVisible": false}, map[string]any{"status": "LISTED", "mallVisible": true, "reason": input.Comment}, "SUCCESS", ipOperator(current, clientIP))
	})
	if err == gorm.ErrRecordNotFound {
		return nil, "VERSION_CONFLICT", nil
	}
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"id": fmt.Sprint(ipID), "status": "LISTED", "mallVisible": true, "version": version + 1}, "", nil
}

// Delist 将 IP 从商城下架；历史授权和订单继续保留并按原规则履约。
func (s *Service) Delist(ctx context.Context, current identity.CurrentAdmin, ipID uint64, version uint32, input IPStatusChangeRequest, clientIP string) (map[string]any, string, error) {
	if !current.HasPermission("AUTH_PLAN_MANAGE") {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if strings.TrimSpace(input.Comment) == "" {
		return nil, "VALIDATION_REQUIRED", nil
	}
	var asset struct {
		Status  string
		Version uint32
	}
	if err := s.db.WithContext(ctx).Table("ip_asset").Where("id=?", ipID).Take(&asset).Error; err != nil {
		return nil, "RESOURCE_NOT_FOUND", nil
	}
	if asset.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	if asset.Status != "LISTED" && asset.Status != "PAUSED" {
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	now := time.Now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Table("ip_asset").Where("id=? AND version=? AND status IN ('LISTED','PAUSED')", ipID, version).Updates(map[string]any{
			"status": "DELISTED", "mall_visible": false, "status_reason": input.Comment,
			"version": version + 1, "updated_by": current.UserID, "status_changed_at": now,
			"status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := audit.WriteStatusHistory(tx, "IP", ipID, asset.Status, "DELISTED", input.Comment, ipOperator(current, clientIP)); err != nil {
			return err
		}
		return audit.WriteLog(tx, "IP_DELISTED", "IP", ipID, map[string]any{"status": asset.Status, "mallVisible": asset.Status == "LISTED"}, map[string]any{"status": "DELISTED", "mallVisible": false, "reason": input.Comment}, "SUCCESS", ipOperator(current, clientIP))
	})
	if err == gorm.ErrRecordNotFound {
		return nil, "VERSION_CONFLICT", nil
	}
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"id": fmt.Sprint(ipID), "status": "DELISTED", "mallVisible": false, "version": version + 1}, "", nil
}

type complianceAsset struct {
	Status                string `gorm:"column:status"`
	Version               uint32 `gorm:"column:version"`
	DCICertificateStatus  string `gorm:"column:dci_certificate_status"`
	ShortIntro            string `gorm:"column:short_intro"`
	DetailContent         string `gorm:"column:detail_content"`
	PublicCopyrightNotice string `gorm:"column:public_copyright_notice"`
	PurchaseNotice        string `gorm:"column:purchase_notice"`
	CoverFileID           uint64 `gorm:"column:cover_file_id"`
}

func (s *Service) pendingComplianceAsset(ctx context.Context, current identity.CurrentAdmin, ipID uint64, version uint32) (complianceAsset, string) {
	if !current.HasPermission(complianceIPPermission) {
		return complianceAsset{}, "AUTH_FORBIDDEN"
	}
	var asset complianceAsset
	if err := s.db.WithContext(ctx).Table("ip_asset").Where("id=?", ipID).Take(&asset).Error; err != nil {
		return complianceAsset{}, "RESOURCE_NOT_FOUND"
	}
	if asset.Version != version {
		return complianceAsset{}, "VERSION_CONFLICT"
	}
	if asset.Status != "PENDING_COMPLIANCE" {
		return complianceAsset{}, "STATE_TRANSITION_NOT_ALLOWED"
	}
	return asset, ""
}

func (s *Service) returnForSupplement(ctx context.Context, current identity.CurrentAdmin, ipID uint64, version uint32, input RightsReviewRequest, clientIP string) (map[string]any, string, error) {
	items, err := json.Marshal(input.SupplementItems)
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	now := time.Now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("ip_asset").Where("id=? AND version=?", ipID, version).Updates(map[string]any{"status": "SUPPLEMENT_REQUIRED", "status_reason": input.Comment, "version": version + 1, "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}).Error; err != nil {
			return err
		}
		if err := s.writeReview(tx, ipID, "RIGHTS_CONFIRMATION", "RETURN", input.Comment, string(items), current.UserID); err != nil {
			return err
		}
		if err := audit.WriteStatusHistory(tx, "IP", ipID, "PENDING_COMPLIANCE", "SUPPLEMENT_REQUIRED", input.Comment, ipOperator(current, clientIP)); err != nil {
			return err
		}
		return audit.WriteLog(tx, "IP_RIGHTS_RETURNED", "IP", ipID, nil, map[string]any{"supplementCount": len(input.SupplementItems)}, "SUCCESS", ipOperator(current, clientIP))
	})
	if err != nil {
		return nil, "RESOURCE_CONFLICT", err
	}
	return map[string]any{"id": fmt.Sprint(ipID), "status": "SUPPLEMENT_REQUIRED", "version": version + 1}, "", nil
}

func (s *Service) writeReview(tx *gorm.DB, ipID uint64, node, result, comment string, supplement any, reviewerID uint64) error {
	return tx.Table("ip_review_record").Create(map[string]any{"id": idgen.Next(), "created_by": reviewerID, "ip_id": ipID, "review_node": node, "review_result": result, "review_comment": comment, "supplement_items": supplement, "reviewer_id": reviewerID}).Error
}

func (s *Service) saveConfirmedBinding(tx *gorm.DB, ipID, userID uint64, input RightsReviewRequest) error {
	var template struct{ Version uint32 }
	if err := tx.Table("finance_distribution_plan").Select("version").Where("id=?", input.DistributionTemplateID).Take(&template).Error; err != nil {
		return err
	}
	var plan struct {
		PlanID            uint64 `gorm:"column:plan_id"`
		AuthorizationMode string `gorm:"column:authorization_mode"`
	}
	if err := tx.Table("auth_plan_version").Select("plan_id,authorization_mode").Where("id=?", input.AuthPlanVersionID).Take(&plan).Error; err != nil {
		return err
	}
	if err := tx.Table("ip_distribution_item").Where("ip_id=?", ipID).Delete(nil).Error; err != nil {
		return err
	}
	for _, item := range input.DistributionItems {
		if err := tx.Table("ip_distribution_item").Create(map[string]any{"id": idgen.Next(), "ip_id": ipID, "distribution_template_id": input.DistributionTemplateID, "template_version_no": template.Version, "participant_type": item.ParticipantType, "ratio": item.Ratio}).Error; err != nil {
			return err
		}
	}
	if err := tx.Table("ip_auth_binding").Create(map[string]any{"id": idgen.Next(), "status": "ACTIVE", "version": 1, "created_by": userID, "ip_id": ipID, "auth_plan_version_id": input.AuthPlanVersionID, "distribution_plan_id": input.DistributionTemplateID, "distribution_version_no": template.Version, "confirmed_by": userID, "confirmed_at": time.Now()}).Error; err != nil {
		return err
	}
	if err := tx.Table("auth_plan").Where("id=?", plan.PlanID).Updates(map[string]any{"is_bound": true}).Error; err != nil {
		return err
	}
	if err := tx.Table("finance_distribution_plan").Where("id=?", input.DistributionTemplateID).Updates(map[string]any{"is_bound": true}).Error; err != nil {
		return err
	}
	return tx.Table("ip_asset").Where("id=?", ipID).Updates(map[string]any{"authorization_mode": plan.AuthorizationMode}).Error
}

// confirmationHash 将确权时的业务材料、文件 Hash、方案及最终比例组成确定快照，供存证服务验证。
func (s *Service) confirmationHash(ctx context.Context, ipID uint64, input RightsReviewRequest) (string, string, error) {
	var asset map[string]any
	if err := s.db.WithContext(ctx).Table("ip_asset").Where("id=?", ipID).Take(&asset).Error; err != nil {
		return "", "RESOURCE_NOT_FOUND", nil
	}
	var rightDocuments []map[string]any
	if err := s.db.WithContext(ctx).Table("ip_right_document").Where("ip_id=?", ipID).Order("document_no").Find(&rightDocuments).Error; err != nil {
		return "", "INTERNAL_ERROR", err
	}
	var reports []map[string]any
	if err := s.db.WithContext(ctx).Table("ip_evaluation_report").Where("ip_id=?", ipID).Order("report_no").Find(&reports).Error; err != nil {
		return "", "INTERNAL_ERROR", err
	}
	var files []map[string]any
	if err := s.db.WithContext(ctx).Table("file_relation r").Select("r.file_role,r.sort_no,f.file_hash").Joins("JOIN file_object f ON f.id=r.file_id").Where("r.object_type='IP' AND r.object_id=?", ipID).Order("r.file_role,r.sort_no").Find(&files).Error; err != nil {
		return "", "INTERNAL_ERROR", err
	}
	snapshot := map[string]any{"ip": asset, "rightDocuments": rightDocuments, "evaluationReports": reports, "files": files, "authPlanVersionId": input.AuthPlanVersionID, "distributionTemplateId": input.DistributionTemplateID, "distributionItems": input.DistributionItems}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", "INTERNAL_ERROR", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:]), "", nil
}
