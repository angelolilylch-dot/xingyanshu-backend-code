// Package ips 实现城市运营录入 IP、补充材料和提交版权合规审核的业务入口。
package ips

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/apiresponse"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/evidence"
	"xingyanshu-backend/internal/platform/filestore"
	"xingyanshu-backend/internal/platform/idgen"
	"xingyanshu-backend/internal/platform/logx"
)

const cityIPPermission = "IP_CREATE_CITY"

type Service struct {
	db         *gorm.DB
	moderator  contentmoderation.Provider
	secret     string
	evidence   *evidence.Service
	storage    filestore.Storage
	fileTTL    time.Duration
	filePrefix string
}

func NewService(db *gorm.DB, moderator contentmoderation.Provider, secret string, evidenceService *evidence.Service, storage filestore.Storage, fileCfg config.FileConfig) *Service {
	return &Service{db: db, moderator: moderator, secret: secret, evidence: evidenceService, storage: storage, fileTTL: fileCfg.SignedURLTTL, filePrefix: strings.Trim(fileCfg.Storage.KeyPrefix, "/")}
}

type RightHolder struct {
	HolderType        string `json:"holderType" binding:"required,oneof=PERSONAL ENTERPRISE INSTITUTION COLLECTIVE"`
	HolderName        string `json:"holderName" binding:"required,max=256"`
	AcquisitionMethod string `json:"acquisitionMethod" binding:"required,max=40"`
	RightsDescription string `json:"rightsDescription" binding:"required,max=10000"`
}
type RightDocument struct {
	DocumentType string `json:"documentType" binding:"required,max=40"`
	DocumentNo   string `json:"documentNo" binding:"required,max=128"`
	ValidFrom    string `json:"validFrom" binding:"omitempty,datetime=2006-01-02"`
	ValidTo      string `json:"validTo" binding:"omitempty,datetime=2006-01-02"`
	FileID       uint64 `json:"fileId,string" binding:"required"`
}

// StringUint64List 接收接口约定的字符串 ID 数组，例如 ["123", "456"]。
// 单个 uint64 可以使用 json:",string"，但该标签不会作用于切片元素。
type StringUint64List []uint64

func (ids *StringUint64List) UnmarshalJSON(data []byte) error {
	var values []string
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	parsed := make(StringUint64List, len(values))
	for i, value := range values {
		id, err := strconv.ParseUint(value, 10, 64)
		if err != nil || id == 0 {
			if err == nil {
				err = fmt.Errorf("ID must be positive")
			}
			return fmt.Errorf("detailFileIds[%d]: %w", i, err)
		}
		parsed[i] = id
	}
	*ids = parsed
	return nil
}

type DistributionItem struct {
	ParticipantType string `json:"participantType" binding:"required,max=40"`
	Ratio           string `json:"ratio" binding:"required,max=16"`
}
type IPInput struct {
	IPName                          string             `json:"ipName" binding:"required,max=200"`
	IPType                          string             `json:"ipType" binding:"required,max=40"`
	SourceType                      string             `json:"sourceType" binding:"required,max=40"`
	MainCityOrgID                   uint64             `json:"mainCityOrgId,string" binding:"required"`
	ShortIntro                      string             `json:"shortIntro" binding:"required,max=500"`
	DetailContent                   string             `json:"detailContent" binding:"required,max=10000"`
	CultureStory                    string             `json:"cultureStory" binding:"required,max=10000"`
	IPFeature                       string             `json:"ipFeature" binding:"required,max=5000"`
	CreativeDirections              []string           `json:"creativeDirections" binding:"required,min=1"`
	ApplicableProductTypes          []string           `json:"applicableProductTypes" binding:"required,min=1"`
	ApplicableActivityTypes         []string           `json:"applicableActivityTypes" binding:"required,min=1"`
	RegionTagCodes                  []string           `json:"regionTagCodes"`
	CultureTagCodes                 []string           `json:"cultureTagCodes"`
	CategoryTagCodes                []string           `json:"categoryTagCodes"`
	CommercializationTagCodes       []string           `json:"commercializationTagCodes"`
	PublicCopyrightNotice           string             `json:"publicCopyrightNotice" binding:"required,max=5000"`
	PurchaseNotice                  string             `json:"purchaseNotice" binding:"required,max=5000"`
	SuggestedTransactionPriceAmount string             `json:"suggestedTransactionPriceAmount" binding:"required,max=32"`
	CoverFileID                     uint64             `json:"coverFileId,string" binding:"required"`
	DetailFileIDs                   StringUint64List   `json:"detailFileIds"`
	RightHolder                     RightHolder        `json:"rightHolder" binding:"required"`
	RightDocuments                  []RightDocument    `json:"rightDocuments" binding:"required,min=1"`
	MaterialPack                    *MaterialPackInput `json:"materialPack" binding:"required"`
	DCICertificateNo                string             `json:"dciCertificateNo" binding:"omitempty,max=128"`
	DCICertificateFileID            uint64             `json:"dciCertificateFileId,string"`
	DCIAssistanceRequested          bool               `json:"dciAssistanceRequested"`
	DCIAssistanceRequest            *DCIRequest        `json:"dciAssistanceRequest"`
	AuthPlanVersionID               uint64             `json:"authPlanVersionId,string" binding:"required"`
	DistributionTemplateID          uint64             `json:"distributionTemplateId,string" binding:"required"`
	DistributionItems               []DistributionItem `json:"distributionItems" binding:"required,min=1"`
}
type SubmitRequest struct {
	Comment string `json:"comment" binding:"required,max=500"`
}
type IPStatusChangeRequest struct {
	Comment string `json:"comment" binding:"required,max=500"`
}
type DCIRequest struct {
	ContactName          string `json:"contactName" binding:"required,max=100"`
	ContactMobile        string `json:"contactMobile" binding:"required,max=32"`
	PreferredContactTime string `json:"preferredContactTime" binding:"omitempty,max=100"`
	RequestNote          string `json:"requestNote" binding:"omitempty,max=500"`
}

// DCICertificateInput 是 DCI 代办取得证书后的独立回填数据；不允许借此修改已锁定的 IP 或素材包。
type DCICertificateInput struct {
	CertificateNo     string `json:"certificateNo" binding:"required,max=128"`
	CertificateFileID uint64 `json:"certificateFileId,string" binding:"required"`
}
type DCIPackageRequest struct {
	IncludeEvaluationReport bool `json:"includeEvaluationReport"`
}
type EvaluationRequest struct {
	ReportNo                       string `json:"reportNo" binding:"required,max=128"`
	InstitutionName                string `json:"institutionName" binding:"required,max=256"`
	ReportDate                     string `json:"reportDate" binding:"required,datetime=2006-01-02"`
	ValidTo                        string `json:"validTo" binding:"omitempty,datetime=2006-01-02"`
	FileID                         uint64 `json:"fileId,string" binding:"required"`
	EvaluationSuggestedPriceAmount string `json:"evaluationSuggestedPriceAmount" binding:"omitempty,max=32"`
}
type DCIHandleRequest struct {
	HandlingStatus string `json:"handlingStatus" binding:"required,oneof=CONTACTED PROCESSING COMPLETED CLOSED"`
	HandlingNote   string `json:"handlingNote" binding:"required,max=500"`
}
type DCIVerificationRequest struct {
	Result  string `json:"result" binding:"required,oneof=PASS RETURN"`
	Comment string `json:"comment" binding:"required,max=500"`
}
type RightsReviewRequest struct {
	Result                          string             `json:"result" binding:"required,oneof=CONFIRM RETURN"`
	Comment                         string             `json:"comment" binding:"required,max=500"`
	SupplementItems                 []string           `json:"supplementItems"`
	AuthPlanVersionID               uint64             `json:"authPlanVersionId,string"`
	DistributionTemplateID          uint64             `json:"distributionTemplateId,string"`
	DistributionItems               []DistributionItem `json:"distributionItems"`
	SuggestedTransactionPriceAmount string             `json:"suggestedTransactionPriceAmount"`
}

func ipOperator(c identity.CurrentAdmin, clientIP string) audit.Operator {
	role := ""
	if len(c.RoleCodes) > 0 {
		role = c.RoleCodes[0]
	}
	return audit.Operator{Type: "USER", ID: c.UserID, RoleCode: role, ClientIP: clientIP}
}

// Create 保存 IP；勾选 DCI 代办时在同一事务中创建代办申请单。
func (s *Service) Create(ctx context.Context, current identity.CurrentAdmin, input IPInput, clientIP string) (map[string]any, string, error) {
	if !current.HasPermission(cityIPPermission) || current.OrgID != input.MainCityOrgID {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if code := s.validateInput(ctx, input); code != "" {
		return nil, code, nil
	}
	id := idgen.Next()
	if code, err := s.moderate(ctx, id, current.UserID, input.textForModeration()); code != "" {
		return nil, code, err
	}
	now := time.Now()
	assistanceID := uint64(0)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		asset, err := input.assetValues(id, current.UserID, now)
		if err != nil {
			return err
		}
		if err := tx.Table("ip_asset").Create(asset).Error; err != nil {
			return err
		}
		if err := s.replaceRelated(tx, id, current.UserID, input); err != nil {
			return err
		}
		manifestHash, replaceErr := s.replaceMaterialPack(tx, id, current.UserID, input.MaterialPack)
		if replaceErr != nil {
			return replaceErr
		}
		if input.DCIAssistanceRequested {
			var createErr error
			if _, createErr = s.lockMaterialPack(tx, id, now); createErr != nil {
				return createErr
			}
			assistanceID, createErr = s.createDCIAssistanceRequest(tx, current, id, *input.DCIAssistanceRequest, manifestHash, now, clientIP)
			if createErr != nil {
				return createErr
			}
		}
		if err := audit.WriteStatusHistory(tx, "IP", id, "", "DRAFT", "城市运营保存IP草稿", ipOperator(current, clientIP)); err != nil {
			return err
		}
		return audit.WriteLog(tx, "IP_CREATED", "IP", id, nil, map[string]any{"ipName": input.IPName, "status": "DRAFT"}, "SUCCESS", ipOperator(current, clientIP))
	})
	if err != nil {
		return nil, "RESOURCE_CONFLICT", err
	}
	result := map[string]any{"id": fmt.Sprint(id), "ipNo": fmt.Sprintf("IP-%d", id), "status": "DRAFT", "version": 1, "dciCertificateStatus": input.initialDCIStatus()}
	if assistanceID != 0 {
		result["dciAssistanceRequestId"] = fmt.Sprint(assistanceID)
		result["dciAssistanceRequestNo"] = fmt.Sprintf("DCI-%d", assistanceID)
	}
	return result, "", nil
}

// Update 仅允许城市运营更新自己城市内的草稿或补充材料状态 IP。
func (s *Service) Update(ctx context.Context, current identity.CurrentAdmin, id uint64, version uint32, input IPInput, clientIP string) (map[string]any, string, error) {
	if input.DCIAssistanceRequested || input.DCIAssistanceRequest != nil {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var asset struct {
		Status        string
		Version       uint32
		MainCityOrgID uint64
	}
	if err := s.db.WithContext(ctx).Table("ip_asset").Where("id=?", id).First(&asset).Error; err != nil {
		return nil, "IP_NOT_FOUND", nil
	}
	if !current.HasPermission(cityIPPermission) || current.OrgID != asset.MainCityOrgID || input.MainCityOrgID != asset.MainCityOrgID {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if asset.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	if asset.Status != "DRAFT" && asset.Status != "SUPPLEMENT_REQUIRED" {
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	if locked, err := s.materialPackLocked(ctx, id); err != nil {
		return nil, "INTERNAL_ERROR", err
	} else if locked {
		return nil, "MATERIAL_PACK_LOCKED", nil
	}
	if code := s.validateInput(ctx, input); code != "" {
		return nil, code, nil
	}
	if code, err := s.moderate(ctx, id, current.UserID, input.textForModeration()); code != "" {
		return nil, code, err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		values, err := input.assetValues(id, current.UserID, time.Now())
		if err != nil {
			return err
		}
		delete(values, "id")
		delete(values, "ip_no")
		delete(values, "status")
		delete(values, "created_by")
		delete(values, "created_at")
		values["version"] = version + 1
		values["updated_by"] = current.UserID
		if err := tx.Table("ip_asset").Where("id=? AND version=?", id, version).Updates(values).Error; err != nil {
			return err
		}
		if err := s.replaceRelated(tx, id, current.UserID, input); err != nil {
			return err
		}
		if _, err := s.replaceMaterialPack(tx, id, current.UserID, input.MaterialPack); err != nil {
			return err
		}
		return audit.WriteLog(tx, "IP_UPDATED", "IP", id, nil, map[string]any{"ipName": input.IPName, "version": version + 1}, "SUCCESS", ipOperator(current, clientIP))
	})
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"id": fmt.Sprint(id), "status": asset.Status, "version": version + 1}, "", nil
}

func (s *Service) Submit(ctx context.Context, current identity.CurrentAdmin, id uint64, version uint32, input SubmitRequest, clientIP string) (map[string]any, string, error) {
	var asset struct {
		Status                              string
		Version                             uint32
		MainCityOrgID, DCICertificateFileID uint64
		DCICertificateNo                    string
		DCICertificateStatus                string
	}
	if err := s.db.WithContext(ctx).Table("ip_asset").Where("id=?", id).First(&asset).Error; err != nil {
		return nil, "IP_NOT_FOUND", nil
	}
	if !current.HasPermission(cityIPPermission) || current.OrgID != asset.MainCityOrgID {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if asset.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	if asset.Status != "DRAFT" && asset.Status != "SUPPLEMENT_REQUIRED" {
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	if asset.DCICertificateNo == "" || asset.DCICertificateFileID == 0 || asset.DCICertificateStatus != "UPLOADED" {
		return nil, "IP_DCI_CERTIFICATE_REQUIRED", nil
	}
	if code, err := s.moderate(ctx, id, current.UserID, input.Comment); code != "" {
		return nil, code, err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockMaterialPack(tx, id, time.Now()); err != nil {
			return fmt.Errorf("db.lock_material_pack: %w", err)
		}
		if err := tx.Table("ip_asset").Where("id=? AND version=?", id, version).Updates(map[string]any{"status": "PENDING_COMPLIANCE", "version": version + 1, "updated_by": current.UserID, "status_reason": input.Comment, "status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}).Error; err != nil {
			return fmt.Errorf("db.update_ip_status: %w", err)
		}
		if err := audit.WriteStatusHistory(tx, "IP", id, asset.Status, "PENDING_COMPLIANCE", input.Comment, ipOperator(current, clientIP)); err != nil {
			return fmt.Errorf("db.status_history: %w", err)
		}
		if err := audit.WriteLog(tx, "IP_SUBMITTED", "IP", id, nil, map[string]any{"status": "PENDING_COMPLIANCE"}, "SUCCESS", ipOperator(current, clientIP)); err != nil {
			return fmt.Errorf("db.audit_write: %w", err)
		}
		return nil
	})
	if err != nil {
		logx.Errorf("request_id=%q module=ips action=submit ip_id=%d version=%d operator_id=%d stage=%q error_type=%T error=%q", logx.RequestID(ctx), id, version, current.UserID, submitFailureStage(err), err, logx.Redact(err.Error()))
		return nil, "IP_SUBMIT_FAILED", nil
	}
	return map[string]any{"id": fmt.Sprint(id), "status": "PENDING_COMPLIANCE", "version": version + 1}, "", nil
}

func submitFailureStage(err error) string {
	for _, stage := range []string{"db.lock_material_pack", "db.update_ip_status", "db.status_history", "db.audit_write"} {
		if strings.Contains(err.Error(), stage+":") {
			return stage
		}
	}
	return "db.transaction"
}

// SubmitDCICertificate 只登记代办返回的 DCI 证书，不修改已锁定的原 IP、权属材料或素材包。
func (s *Service) SubmitDCICertificate(ctx context.Context, current identity.CurrentAdmin, ipID uint64, version uint32, input DCICertificateInput, clientIP string) (map[string]any, string, error) {
	if strings.TrimSpace(input.CertificateNo) == "" {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	if !s.fileActive(ctx, input.CertificateFileID) {
		return nil, "DCI_CERTIFICATE_FILE_NOT_FOUND", nil
	}
	var asset struct {
		Status        string
		Version       uint32
		MainCityOrgID uint64
		DCIFileID     uint64 `gorm:"column:dci_certificate_file_id"`
	}
	if err := s.db.WithContext(ctx).Table("ip_asset").Where("id=?", ipID).Take(&asset).Error; err != nil {
		return nil, "IP_NOT_FOUND", nil
	}
	if !current.HasPermission(cityIPPermission) || current.OrgID != asset.MainCityOrgID {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if asset.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	if asset.Status != "DRAFT" && asset.Status != "SUPPLEMENT_REQUIRED" || asset.DCIFileID != 0 {
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	now := time.Now()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("file_relation").Where("object_type='IP' AND object_id=? AND file_role='IP_DCI_CERTIFICATE'", ipID).Delete(map[string]any{}).Error; err != nil {
			return err
		}
		if err := s.linkFile(tx, input.CertificateFileID, "IP", ipID, "IP_DCI_CERTIFICATE", 0, current.UserID); err != nil {
			return err
		}
		if err := tx.Table("ip_asset").Where("id=? AND version=?", ipID, version).Updates(map[string]any{"dci_certificate_no": strings.TrimSpace(input.CertificateNo), "dci_certificate_file_id": input.CertificateFileID, "dci_certificate_status": "UPLOADED", "version": version + 1, "updated_by": current.UserID, "updated_at": now}).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "IP_DCI_CERTIFICATE_SUBMITTED", "IP", ipID, nil, map[string]any{"certificateFileId": fmt.Sprint(input.CertificateFileID)}, "SUCCESS", ipOperator(current, clientIP))
	}); err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"id": fmt.Sprint(ipID), "dciCertificateStatus": "UPLOADED", "version": version + 1}, "", nil
}

// CreateDCIAssistance 在已有草稿 IP 下保存脱敏的代办联系请求，并将 DCI 状态标记为已申请代办。
func (s *Service) CreateDCIAssistance(ctx context.Context, current identity.CurrentAdmin, ipID uint64, version uint32, input DCIRequest, clientIP string) (map[string]any, string, error) {
	var asset struct {
		Status               string
		Version              uint32
		MainCityOrgID        uint64
		DCICertificateFileID uint64
	}
	if err := s.db.WithContext(ctx).Table("ip_asset").Where("id=?", ipID).First(&asset).Error; err != nil {
		return nil, "IP_NOT_FOUND", nil
	}
	if !current.HasPermission(cityIPPermission) || current.OrgID != asset.MainCityOrgID {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if asset.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	if asset.Status != "DRAFT" && asset.Status != "SUPPLEMENT_REQUIRED" {
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	if asset.DCICertificateFileID != 0 {
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	if !validDCIAssistanceRequest(input) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	if strings.TrimSpace(input.RequestNote) != "" {
		if code, err := s.moderate(ctx, ipID, current.UserID, input.RequestNote); code != "" {
			return nil, code, err
		}
	}
	nameCipher, err := encrypt(s.secret, input.ContactName)
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	mobileCipher, err := encrypt(s.secret, input.ContactMobile)
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	id := idgen.Next()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		manifestHash, lockErr := s.lockMaterialPack(tx, ipID, now)
		if lockErr != nil {
			return lockErr
		}
		item := map[string]any{"id": id, "status": "PENDING", "version": 1, "created_by": current.UserID, "request_no": fmt.Sprintf("DCI-%d", id), "ip_id": ipID, "contact_name_cipher": nameCipher, "contact_name_masked": mask(input.ContactName), "contact_mobile_cipher": mobileCipher, "contact_mobile_masked": maskMobile(input.ContactMobile), "preferred_contact_time": input.PreferredContactTime, "request_note": input.RequestNote, "submission_manifest_hash": manifestHash, "submission_locked_at": now, "handling_status": "PENDING"}
		if err := tx.Table("ip_dci_assistance_request").Create(item).Error; err != nil {
			return err
		}
		if err := tx.Table("ip_asset").Where("id=? AND version=?", ipID, version).Updates(map[string]any{"dci_certificate_status": "ASSISTANCE_REQUESTED", "version": version + 1, "updated_by": current.UserID}).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "IP_DCI_ASSISTANCE_REQUESTED", "IP", ipID, nil, map[string]any{"requestId": fmt.Sprint(id)}, "SUCCESS", ipOperator(current, clientIP))
	})
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"id": fmt.Sprint(id), "requestNo": fmt.Sprintf("DCI-%d", id), "handlingStatus": "PENDING", "ipVersion": version + 1}, "", nil
}

func (s *Service) createDCIAssistanceRequest(tx *gorm.DB, current identity.CurrentAdmin, ipID uint64, input DCIRequest, manifestHash string, lockedAt time.Time, clientIP string) (uint64, error) {
	nameCipher, err := encrypt(s.secret, input.ContactName)
	if err != nil {
		return 0, err
	}
	mobileCipher, err := encrypt(s.secret, input.ContactMobile)
	if err != nil {
		return 0, err
	}
	id := idgen.Next()
	item := map[string]any{"id": id, "status": "PENDING", "version": 1, "created_by": current.UserID, "request_no": fmt.Sprintf("DCI-%d", id), "ip_id": ipID, "contact_name_cipher": nameCipher, "contact_name_masked": mask(input.ContactName), "contact_mobile_cipher": mobileCipher, "contact_mobile_masked": maskMobile(input.ContactMobile), "preferred_contact_time": input.PreferredContactTime, "request_note": input.RequestNote, "submission_manifest_hash": manifestHash, "submission_locked_at": lockedAt, "handling_status": "PENDING"}
	if err := tx.Table("ip_dci_assistance_request").Create(item).Error; err != nil {
		return 0, err
	}
	if err := audit.WriteLog(tx, "IP_DCI_ASSISTANCE_REQUESTED", "IP", ipID, nil, map[string]any{"requestId": fmt.Sprint(id)}, "SUCCESS", ipOperator(current, clientIP)); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Service) CreateEvaluation(ctx context.Context, current identity.CurrentAdmin, ipID uint64, input EvaluationRequest, clientIP string) (map[string]any, string, error) {
	var asset struct {
		Status        string
		MainCityOrgID uint64
	}
	if err := s.db.WithContext(ctx).Table("ip_asset").Where("id=?", ipID).First(&asset).Error; err != nil {
		return nil, "IP_NOT_FOUND", nil
	}
	if current.HasPermission(cityIPPermission) && current.OrgID == asset.MainCityOrgID && (asset.Status == "DRAFT" || asset.Status == "SUPPLEMENT_REQUIRED") {
	} else if !current.HasPermission("IP_REVIEW_COMPLIANCE") {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if !s.fileActive(ctx, input.FileID) {
		return nil, "EVALUATION_REPORT_FILE_NOT_FOUND", nil
	}
	if code, err := s.moderate(ctx, ipID, current.UserID, input.ReportNo+"\n"+input.InstitutionName); code != "" {
		return nil, code, err
	}
	if input.EvaluationSuggestedPriceAmount != "" && !validMoney(input.EvaluationSuggestedPriceAmount) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	id := idgen.Next()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("ip_evaluation_report").Create(map[string]any{"id": id, "status": "ACTIVE", "version": 1, "created_by": current.UserID, "ip_id": ipID, "report_no": input.ReportNo, "institution_name": input.InstitutionName, "report_date": input.ReportDate, "valid_to": nullable(input.ValidTo), "file_id": input.FileID, "evaluation_suggested_price_amount": nullable(input.EvaluationSuggestedPriceAmount)}).Error; err != nil {
			return err
		}
		return s.linkFile(tx, input.FileID, "IP", ipID, "IP_EVALUATION_REPORT", 0, current.UserID)
	})
	if err != nil {
		return nil, "RESOURCE_CONFLICT", err
	}
	return map[string]any{"id": fmt.Sprint(id), "reportNo": input.ReportNo}, "", nil
}

func (s *Service) Get(ctx context.Context, current identity.CurrentAdmin, id uint64) (map[string]any, string, error) {
	var row map[string]any
	if err := s.db.WithContext(ctx).Table("ip_asset").Where("id=?", id).Take(&row).Error; err != nil {
		return nil, "IP_NOT_FOUND", nil
	}
	if !canRead(current, row) {
		return nil, "AUTH_FORBIDDEN", nil
	}
	var holder map[string]any
	s.db.WithContext(ctx).Table("ip_right_holder").Where("ip_id=?", id).Take(&holder)
	var docs []map[string]any
	s.db.WithContext(ctx).Table("ip_right_document").Select("id,status,status_reason_code,status_reason,version,created_by,created_at,updated_by,updated_at,status_changed_at,status_changed_by_type,status_changed_by_id,ip_id,document_type,document_no,DATE_FORMAT(valid_from, '%Y-%m-%d') AS valid_from,DATE_FORMAT(valid_to, '%Y-%m-%d') AS valid_to,file_id").Where("ip_id=?", id).Find(&docs)
	var selection map[string]any
	s.db.WithContext(ctx).Table("ip_draft_auth_selection").Where("ip_id=?", id).Take(&selection)
	var ratios []map[string]any
	s.db.WithContext(ctx).Table("ip_distribution_item").Where("ip_id=?", id).Order("participant_type").Find(&ratios)
	var materialPack map[string]any
	s.db.WithContext(ctx).Table("ip_material_pack").Select("id,pack_name,status,manifest_hash,submission_manifest_hash,submission_locked_at,evidence_status,evidence_receipt_no,evidence_transaction_hash,frozen_at,version_no").Where("ip_id=?", id).Take(&materialPack)
	var materialItems []map[string]any
	if materialPack["id"] != nil {
		s.db.WithContext(ctx).Table("ip_material_pack_item i").Select("i.id,i.file_id,f.original_name,f.mime_type,f.file_size,i.file_hash,i.material_role,i.sort_no,i.deliverable,i.scope_description").Joins("JOIN file_object f ON f.id=i.file_id").Where("i.material_pack_id=?", materialPack["id"]).Order("i.sort_no,i.id").Find(&materialItems)
	}
	var rightsEvidence map[string]any
	s.db.WithContext(ctx).Table("evidence_record").Select("id,evidence_no,hash_value,status,receipt_no,chain_transaction_hash,chained_at,chain_provider").Where("object_type='IP' AND object_id=? AND evidence_type='IP_RIGHTS_CONFIRMATION'", id).Order("created_at DESC").Take(&rightsEvidence)
	var dciAssistance map[string]any
	s.db.WithContext(ctx).Table("ip_dci_assistance_request").Select("id,request_no,handling_status,contact_name_cipher,contact_name_masked,contact_mobile_cipher,contact_mobile_masked,preferred_contact_time,request_note,submission_manifest_hash,submission_locked_at").Where("ip_id=?", id).Order("created_at DESC").Take(&dciAssistance)
	if canRevealSensitive(current, row) {
		if encrypted, ok := holder["holder_name_cipher"].([]byte); ok {
			name, e := decrypt(s.secret, encrypted)
			if e != nil {
				return nil, "INTERNAL_ERROR", e
			}
			holder["holder_name"] = name
		}
		if encrypted, ok := dciAssistance["contact_name_cipher"].([]byte); ok {
			name, e := decrypt(s.secret, encrypted)
			if e != nil {
				return nil, "INTERNAL_ERROR", e
			}
			dciAssistance["contact_name"] = name
		}
		if encrypted, ok := dciAssistance["contact_mobile_cipher"].([]byte); ok {
			mobile, e := decrypt(s.secret, encrypted)
			if e != nil {
				return nil, "INTERNAL_ERROR", e
			}
			dciAssistance["contact_mobile"] = mobile
		}
	}
	delete(holder, "holder_name_cipher")
	delete(dciAssistance, "contact_name_cipher")
	delete(dciAssistance, "contact_mobile_cipher")
	result := apiresponse.CamelizeMap(row)
	result["rightHolder"] = apiresponse.CamelizeMap(holder)
	result["rightDocuments"] = apiresponse.CamelizeMaps(docs)
	result["draftAuthSelection"] = apiresponse.CamelizeMap(selection)
	result["distributionItems"] = apiresponse.CamelizeMaps(ratios)
	if materialPack["id"] != nil {
		result["materialPack"] = apiresponse.CamelizeMap(materialPack)
		result["materialItems"] = apiresponse.CamelizeMaps(materialItems)
	}
	if rightsEvidence["id"] != nil {
		result["rightsEvidence"] = apiresponse.CamelizeMap(rightsEvidence)
	}
	if dciAssistance["id"] != nil {
		result["dciAssistance"] = apiresponse.CamelizeMap(dciAssistance)
	}
	return result, "", nil
}

func (s *Service) List(ctx context.Context, current identity.CurrentAdmin, keyword string, pageNo, pageSize int) (map[string]any, string, error) {
	q := s.db.WithContext(ctx).Table("ip_asset a").Select("a.*, (SELECT status FROM ip_material_pack mp WHERE mp.ip_id=a.id ORDER BY mp.id DESC LIMIT 1) AS material_pack_status, (SELECT evidence_status FROM ip_material_pack mp WHERE mp.ip_id=a.id ORDER BY mp.id DESC LIMIT 1) AS material_pack_evidence_status, (SELECT frozen_at FROM ip_material_pack mp WHERE mp.ip_id=a.id ORDER BY mp.id DESC LIMIT 1) AS material_pack_frozen_at")
	if current.HasPermission(cityIPPermission) {
		q = q.Where("a.main_city_org_id=?", current.OrgID)
	}
	if strings.TrimSpace(keyword) != "" {
		q = q.Where("a.ip_no LIKE ? OR a.ip_name LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	var rows []map[string]any
	if err := q.Order("a.updated_at DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"items": apiresponse.CamelizeMaps(rows), "pageNo": pageNo, "pageSize": pageSize, "total": total, "totalPages": int((total + int64(pageSize) - 1) / int64(pageSize))}, "", nil
}

func (s *Service) validateInput(ctx context.Context, input IPInput) string {
	if !validMoney(input.SuggestedTransactionPriceAmount) {
		return "VALIDATION_FORMAT_INVALID"
	}
	if !s.fileActive(ctx, input.CoverFileID) {
		return "COVER_FILE_NOT_FOUND"
	}
	for _, d := range input.RightDocuments {
		if !s.fileActive(ctx, d.FileID) {
			return "RIGHT_DOCUMENT_FILE_NOT_FOUND"
		}
	}
	for _, id := range input.DetailFileIDs {
		if !s.fileActive(ctx, id) {
			return "DETAIL_FILE_NOT_FOUND"
		}
	}
	if input.DCICertificateFileID != 0 && !s.fileActive(ctx, input.DCICertificateFileID) {
		return "DCI_CERTIFICATE_FILE_NOT_FOUND"
	}
	if input.DCIAssistanceRequested {
		if input.DCIAssistanceRequest == nil || strings.TrimSpace(input.DCICertificateNo) != "" || input.DCICertificateFileID != 0 || !validDCIAssistanceRequest(*input.DCIAssistanceRequest) {
			return "VALIDATION_FORMAT_INVALID"
		}
	} else if input.DCIAssistanceRequest != nil {
		return "VALIDATION_FORMAT_INVALID"
	}
	if code := s.validateMaterialPack(ctx, input.MaterialPack); code != "" {
		return code
	}
	return s.validateSelection(ctx, input.AuthPlanVersionID, input.DistributionTemplateID, input.DistributionItems)
}

func validDCIAssistanceRequest(input DCIRequest) bool {
	return strings.TrimSpace(input.ContactName) != "" && len([]rune(input.ContactName)) <= 100 && strings.TrimSpace(input.ContactMobile) != "" && len([]rune(input.ContactMobile)) <= 32 && len([]rune(input.PreferredContactTime)) <= 100 && len([]rune(input.RequestNote)) <= 500
}

var moneyPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,15})(\.[0-9]{1,2})?$`)

func validMoney(value string) bool {
	return moneyPattern.MatchString(strings.TrimSpace(value))
}
func (s *Service) validateSelection(ctx context.Context, authPlanVersionID, distributionTemplateID uint64, distributionItems []DistributionItem) string {
	var plan struct{ AuthorizationMode string }
	if err := s.db.WithContext(ctx).Table("auth_plan p").Select("v.authorization_mode").Joins("JOIN auth_plan_version v ON v.id=p.current_version_id").Where("v.id=? AND p.status='ENABLED'", authPlanVersionID).Take(&plan).Error; err != nil {
		return "AUTH_PLAN_VERSION_NOT_FOUND"
	}
	var template struct {
		DistributionType string
		Version          uint32
	}
	if err := s.db.WithContext(ctx).Table("finance_distribution_plan").Select("distribution_type,version").Where("id=? AND status='ENABLED'", distributionTemplateID).Take(&template).Error; err != nil {
		return "DISTRIBUTION_PLAN_NOT_FOUND"
	}
	want := "NORMAL_SHARE"
	if plan.AuthorizationMode == "EXCLUSIVE_BUYOUT" {
		want = "BUYOUT_FEE"
	}
	if template.DistributionType != want {
		return "VALIDATION_FORMAT_INVALID"
	}
	var items []struct{ ParticipantType string }
	s.db.WithContext(ctx).Table("finance_distribution_plan_item").Select("participant_type").Where("distribution_plan_id=?", distributionTemplateID).Find(&items)
	if len(items) != len(distributionItems) || !ratiosValid(items, distributionItems) {
		return "VALIDATION_FORMAT_INVALID"
	}
	return ""
}
func ratiosValid(expected []struct{ ParticipantType string }, given []DistributionItem) bool {
	seen := map[string]bool{}
	sum := new(big.Rat)
	for _, i := range given {
		v, ok := new(big.Rat).SetString(i.Ratio)
		if !ok || v.Sign() < 0 || v.Cmp(big.NewRat(1, 1)) > 0 || seen[i.ParticipantType] {
			return false
		}
		seen[i.ParticipantType] = true
		sum.Add(sum, v)
	}
	for _, i := range expected {
		if !seen[i.ParticipantType] {
			return false
		}
	}
	return sum.Cmp(big.NewRat(1, 1)) == 0
}
func (s *Service) replaceRelated(tx *gorm.DB, ipID, userID uint64, input IPInput) error {
	if err := tx.Table("ip_right_holder").Where("ip_id=?", ipID).Delete(map[string]any{}).Error; err != nil {
		return err
	}
	cipherText, err := encrypt(s.secret, input.RightHolder.HolderName)
	if err != nil {
		return err
	}
	holder := map[string]any{"id": idgen.Next(), "status": "ACTIVE", "version": 1, "created_by": userID, "ip_id": ipID, "holder_name_cipher": cipherText, "holder_name_masked": mask(input.RightHolder.HolderName), "holder_type": input.RightHolder.HolderType, "acquisition_method": input.RightHolder.AcquisitionMethod, "rights_description": input.RightHolder.RightsDescription}
	if err = tx.Table("ip_right_holder").Create(holder).Error; err != nil {
		return err
	}
	if err = tx.Table("ip_right_document").Where("ip_id=?", ipID).Delete(map[string]any{}).Error; err != nil {
		return err
	}
	for _, d := range input.RightDocuments {
		if err = tx.Table("ip_right_document").Create(map[string]any{"id": idgen.Next(), "status": "ACTIVE", "version": 1, "created_by": userID, "ip_id": ipID, "document_type": d.DocumentType, "document_no": d.DocumentNo, "valid_from": nullable(d.ValidFrom), "valid_to": nullable(d.ValidTo), "file_id": d.FileID}).Error; err != nil {
			return err
		}
		if err = s.linkFile(tx, d.FileID, "IP", ipID, "IP_RIGHT_DOCUMENT", 0, userID); err != nil {
			return err
		}
	}
	if err = tx.Table("file_relation").Where("object_type=? AND object_id=? AND file_role IN ?", "IP", ipID, []string{"IP_COVER", "IP_DETAIL", "IP_DCI_CERTIFICATE", "IP_RIGHT_DOCUMENT"}).Delete(map[string]any{}).Error; err != nil {
		return err
	}
	if err = s.linkFile(tx, input.CoverFileID, "IP", ipID, "IP_COVER", 0, userID); err != nil {
		return err
	}
	for n, fileID := range input.DetailFileIDs {
		if err = s.linkFile(tx, fileID, "IP", ipID, "IP_DETAIL", n, userID); err != nil {
			return err
		}
	}
	if input.DCICertificateFileID != 0 {
		if err = s.linkFile(tx, input.DCICertificateFileID, "IP", ipID, "IP_DCI_CERTIFICATE", 0, userID); err != nil {
			return err
		}
	}
	if err = tx.Table("ip_draft_auth_selection").Where("ip_id=?", ipID).Delete(map[string]any{}).Error; err != nil {
		return err
	}
	var template struct{ Version uint32 }
	if err = tx.Table("finance_distribution_plan").Select("version").Where("id=?", input.DistributionTemplateID).Take(&template).Error; err != nil {
		return err
	}
	if err = tx.Table("ip_draft_auth_selection").Create(map[string]any{"id": idgen.Next(), "ip_id": ipID, "auth_plan_version_id": input.AuthPlanVersionID, "distribution_template_id": input.DistributionTemplateID, "distribution_template_version_no": template.Version, "created_by": userID}).Error; err != nil {
		return err
	}
	if err = tx.Table("ip_distribution_item").Where("ip_id=?", ipID).Delete(map[string]any{}).Error; err != nil {
		return err
	}
	for _, item := range input.DistributionItems {
		if err = tx.Table("ip_distribution_item").Create(map[string]any{"id": idgen.Next(), "ip_id": ipID, "distribution_template_id": input.DistributionTemplateID, "template_version_no": template.Version, "participant_type": item.ParticipantType, "ratio": item.Ratio}).Error; err != nil {
			return err
		}
	}
	if err = tx.Table("finance_distribution_plan").Where("id=?", input.DistributionTemplateID).Updates(map[string]any{"is_bound": true}).Error; err != nil {
		return err
	}
	var authPlanID uint64
	var authorizationMode string
	if err = tx.Table("auth_plan_version").Select("plan_id").Where("id=?", input.AuthPlanVersionID).Scan(&authPlanID).Error; err != nil {
		return err
	}
	if err = tx.Table("auth_plan_version").Select("authorization_mode").Where("id=?", input.AuthPlanVersionID).Scan(&authorizationMode).Error; err != nil {
		return err
	}
	if err = tx.Table("auth_plan").Where("id=?", authPlanID).Updates(map[string]any{"is_bound": true}).Error; err != nil {
		return err
	}
	return tx.Table("ip_asset").Where("id=?", ipID).Updates(map[string]any{"authorization_mode": authorizationMode}).Error
}
func (input IPInput) assetValues(id, userID uint64, now time.Time) (map[string]any, error) {
	enc := func(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }
	creative, e := enc(input.CreativeDirections)
	if e != nil {
		return nil, e
	}
	product, e := enc(input.ApplicableProductTypes)
	if e != nil {
		return nil, e
	}
	activity, e := enc(input.ApplicableActivityTypes)
	if e != nil {
		return nil, e
	}
	region, _ := enc(input.RegionTagCodes)
	culture, _ := enc(input.CultureTagCodes)
	category, _ := enc(input.CategoryTagCodes)
	commercial, _ := enc(input.CommercializationTagCodes)
	dciStatus := input.initialDCIStatus()
	values := map[string]any{"id": id, "status": "DRAFT", "version": 1, "created_by": userID, "created_at": now, "ip_no": fmt.Sprintf("IP-%d", id), "ip_name": input.IPName, "ip_type": input.IPType, "source_type": input.SourceType, "main_city_org_id": input.MainCityOrgID, "cover_file_id": input.CoverFileID, "short_intro": input.ShortIntro, "detail_content": input.DetailContent, "culture_story": input.CultureStory, "ip_feature": input.IPFeature, "creative_directions_json": creative, "applicable_product_types_json": product, "applicable_activity_types_json": activity, "region_tag_codes_json": region, "culture_tag_codes_json": culture, "category_tag_codes_json": category, "commercialization_tag_codes_json": commercial, "public_copyright_notice": input.PublicCopyrightNotice, "purchase_notice": input.PurchaseNotice, "dci_certificate_status": dciStatus, "dci_certificate_no": nullable(input.DCICertificateNo), "dci_certificate_file_id": nullableID(input.DCICertificateFileID), "authorization_mode": "PENDING_SELECTION", "mall_visible": false}
	values["suggested_transaction_price_amount"] = input.SuggestedTransactionPriceAmount
	return values, nil
}
func (input IPInput) textForModeration() string {
	values := append([]string{input.IPName, input.ShortIntro, input.DetailContent, input.CultureStory, input.IPFeature, input.PublicCopyrightNotice, input.PurchaseNotice, input.RightHolder.HolderName, input.RightHolder.RightsDescription}, input.CreativeDirections...)
	if input.DCIAssistanceRequest != nil {
		values = append(values, input.DCIAssistanceRequest.RequestNote)
	}
	return strings.Join(values, "\n")
}

func (input IPInput) initialDCIStatus() string {
	if input.DCIAssistanceRequested {
		return "ASSISTANCE_REQUESTED"
	}
	if input.DCICertificateFileID != 0 {
		return "UPLOADED"
	}
	return "MISSING"
}
func (s *Service) moderate(ctx context.Context, objectID, userID uint64, text string) (string, error) {
	result, err := s.moderator.Moderate(ctx, contentmoderation.Request{Text: text, DataID: fmt.Sprintf("IP-%d", objectID), UserID: fmt.Sprint(userID), Language: "zh"})
	status := "ERROR"
	category := ""
	requestNo := fmt.Sprintf("LOCAL-%d", idgen.Next())
	if err == nil {
		status = result.Decision
		category = result.Label
		if result.ProviderRequestNo != "" {
			requestNo = result.ProviderRequestNo
		}
	}
	record := map[string]any{"id": idgen.Next(), "status": "COMPLETED", "version": 1, "created_by": userID, "detection_no": fmt.Sprintf("DET-%d", idgen.Next()), "object_type": "IP", "object_id": objectID, "text_hash": hashText(text), "provider_code": result.Provider, "provider_request_no": requestNo, "detection_status": status, "risk_category": category}
	if err != nil {
		record["error_summary"] = err.Error()
	}
	if writeErr := s.db.WithContext(ctx).Table("content_detection_record").Create(record).Error; writeErr != nil {
		return "INTERNAL_ERROR", writeErr
	}
	if err != nil {
		return "CONTENT_DETECTION_UNAVAILABLE", err
	}
	if result.Decision != contentmoderation.DecisionPass {
		return "CONTENT_DETECTION_REJECTED", nil
	}
	return "", nil
}
func (s *Service) fileActive(ctx context.Context, id uint64) bool {
	if id == 0 {
		return false
	}
	var n int64
	s.db.WithContext(ctx).Table("file_object").Where("id=? AND status='ACTIVE' AND upload_status='SUCCESS'", id).Count(&n)
	return n == 1
}
func (s *Service) linkFile(tx *gorm.DB, fileID uint64, typ string, objID uint64, role string, sort int, userID uint64) error {
	// 当前IP保存流程先删除可替换的关联，再按本次表单重建；直接 Create 可避免无模型 FirstOrCreate 的隐式写入问题。
	return tx.Table("file_relation").Create(map[string]any{"id": idgen.Next(), "created_by": userID, "file_id": fileID, "object_type": typ, "object_id": objID, "file_role": role, "sort_no": sort}).Error
}
func canRead(c identity.CurrentAdmin, row map[string]any) bool {
	if c.HasPermission(cityIPPermission) {
		return fmt.Sprint(row["main_city_org_id"]) == fmt.Sprint(c.OrgID)
	}
	return true
}

// canRevealSensitive 只在详情接口返回明文，且不将明文写入审计或日志。
// 城市运营者必须是所属城市；版权合规与平台管理员可按其后台权限读取。
func canRevealSensitive(c identity.CurrentAdmin, row map[string]any) bool {
	if c.HasPermission("IP_REVIEW_COMPLIANCE") || hasRole(c, "PLATFORM_ADMIN") {
		return true
	}
	return c.HasPermission(cityIPPermission) && fmt.Sprint(row["main_city_org_id"]) == fmt.Sprint(c.OrgID)
}

func hasRole(c identity.CurrentAdmin, wanted string) bool {
	for _, role := range c.RoleCodes {
		if role == wanted {
			return true
		}
	}
	return false
}
func encrypt(secret, value string) ([]byte, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return []byte(base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(value), nil))), nil
}

// decrypt 仅在版权/合规处理 DCI 代办时还原联系人信息，调用方不得把明文写入审计内容或日志。
func decrypt(secret string, value []byte) (string, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(string(value))
	if err != nil || len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("invalid encrypted value")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
func mask(value string) string {
	r := []rune(strings.TrimSpace(value))
	if len(r) <= 1 {
		return "*"
	}
	return string(r[:1]) + "***"
}
func maskMobile(value string) string {
	v := strings.TrimSpace(value)
	if len(v) < 7 {
		return "***"
	}
	return v[:3] + "****" + v[len(v)-4:]
}
func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
func nullableID(value uint64) any {
	if value == 0 {
		return nil
	}
	return value
}
func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}
