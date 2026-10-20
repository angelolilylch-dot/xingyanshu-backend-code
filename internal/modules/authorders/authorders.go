package authorders

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/idgen"
)

const (
	statusPending = "PENDING_PAYMENT"
	statusClosed  = "CLOSED"
)

type Service struct{ db *gorm.DB }

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

type CreateInput struct {
	IPID           string `json:"ipId" binding:"required"`
	ContractAgreed bool   `json:"contractAgreed"`
}

type CancelInput struct {
	Reason string `json:"reason"`
}

type orderRow struct {
	ID               uint64     `gorm:"column:id"`
	AuthOrderNo      string     `gorm:"column:auth_order_no"`
	CreatorID        uint64     `gorm:"column:creator_id"`
	IPID             uint64     `gorm:"column:ip_id"`
	PlanVersionID    uint64     `gorm:"column:auth_plan_version_id"`
	Amount           string     `gorm:"column:amount"`
	Currency         string     `gorm:"column:currency"`
	Status           string     `gorm:"column:status"`
	ReasonCode       string     `gorm:"column:status_reason_code"`
	Reason           string     `gorm:"column:status_reason"`
	PaymentExpireAt  time.Time  `gorm:"column:payment_expire_at"`
	PurchaseSnapshot []byte     `gorm:"column:purchase_snapshot"`
	PaymentNo        string     `gorm:"column:payment_no"`
	PaidAt           *time.Time `gorm:"column:paid_at"`
	ContractStatus   string     `gorm:"column:contract_status"`
	EvidenceStatus   string     `gorm:"column:evidence_status"`
}

type purchasableIP struct {
	IPID                  uint64     `gorm:"column:id"`
	IPNo                  string     `gorm:"column:ip_no"`
	IPName                string     `gorm:"column:ip_name"`
	Mode                  string     `gorm:"column:authorization_mode"`
	Price                 string     `gorm:"column:price_amount"`
	PlanVersionID         uint64     `gorm:"column:auth_plan_version_id"`
	PlanName              string     `gorm:"column:plan_name"`
	TermType              string     `gorm:"column:term_type"`
	TermValue             *int       `gorm:"column:term_value"`
	RegionMode            string     `gorm:"column:region_mode"`
	ScenesJSON            string     `gorm:"column:scenes_json"`
	ContentType           string     `gorm:"column:content_type"`
	QuotaTotal            *int       `gorm:"column:quota_total"`
	RightsNotice          string     `gorm:"column:public_copyright_notice"`
	PurchaseNotice        string     `gorm:"column:purchase_notice"`
	ValidFrom             *time.Time `gorm:"column:valid_from"`
	ValidTo               *time.Time `gorm:"column:valid_to"`
	RegionCodesJSON       string     `gorm:"column:region_codes_json"`
	CommercialAllowed     bool       `gorm:"column:commercial_allowed"`
	DerivativeAllowed     bool       `gorm:"column:derivative_allowed"`
	NCreationAllowed      bool       `gorm:"column:n_creation_allowed"`
	DistributionPlanID    uint64     `gorm:"column:distribution_plan_id"`
	DistributionVersionNo uint32     `gorm:"column:distribution_version_no"`
	DistributionName      string     `gorm:"column:distribution_name"`
	DistributionType      string     `gorm:"column:distribution_type"`
	HolderNameMasked      string     `gorm:"column:holder_name_masked"`
	HolderType            string     `gorm:"column:holder_type"`
	AcquisitionMethod     string     `gorm:"column:acquisition_method"`
	RightsDescription     string     `gorm:"column:rights_description"`
	CityOrgName           string     `gorm:"column:city_org_name"`
	MaterialManifestHash  string     `gorm:"column:manifest_hash"`
}

func (s *Service) Create(ctx context.Context, current creators.CurrentCreator, input CreateInput, clientIP string) (map[string]any, string, error) {
	ipID, err := parseID(input.IPID)
	if err != nil {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	if !input.ContractAgreed {
		return nil, "CONTRACT_AGREEMENT_REQUIRED", nil
	}
	var result map[string]any
	code := ""
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.closeExpiredForCreatorIP(tx, current.CreatorID, ipID, time.Now()); err != nil {
			return err
		}
		var profile struct {
			Status         string
			IdentityStatus string `gorm:"column:identity_status"`
			SubjectType    string `gorm:"column:subject_type"`
			DisplayName    string `gorm:"column:display_name"`
		}
		if err := tx.Table("creator_profile").Where("id=?", current.CreatorID).Take(&profile).Error; err != nil || profile.Status != "ACTIVE" || profile.IdentityStatus != "VERIFIED" {
			code = "CREATOR_IDENTITY_REQUIRED"
			return nil
		}
		var item purchasableIP
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("ip_asset a").Joins("JOIN ip_auth_binding b ON b.ip_id=a.id AND b.status='ACTIVE'").Joins("JOIN auth_plan_version v ON v.id=b.auth_plan_version_id AND v.status='ENABLED'").Joins("JOIN auth_plan p ON p.id=v.plan_id AND p.status='ENABLED'").Joins("JOIN ip_material_pack mp ON mp.ip_id=a.id AND mp.status='FROZEN' AND mp.evidence_status='EVIDENCED' AND mp.enabled=1").Joins("LEFT JOIN finance_distribution_plan dp ON dp.id=b.distribution_plan_id").Joins("LEFT JOIN ip_right_holder h ON h.ip_id=a.id AND h.status='ACTIVE'").Joins("LEFT JOIN sys_org city ON city.id=a.main_city_org_id").Where("a.id=? AND a.status='LISTED' AND a.mall_visible=1", ipID).Select("a.id,a.ip_no,a.ip_name,a.authorization_mode,a.suggested_transaction_price_amount AS price_amount,b.auth_plan_version_id,p.plan_name,v.term_type,v.term_value,v.valid_from,v.valid_to,v.region_mode,v.region_codes_json,v.scenes_json,v.commercial_allowed,v.derivative_allowed,v.n_creation_allowed,v.content_type,v.quota_total,a.public_copyright_notice,a.purchase_notice,b.distribution_plan_id,b.distribution_version_no,dp.distribution_name,dp.distribution_type,h.holder_name_masked,h.holder_type,h.acquisition_method,h.rights_description,city.org_name AS city_org_name,mp.manifest_hash").Take(&item).Error
		if err != nil {
			code = "IP_NOT_PURCHASABLE"
			return nil
		}
		var pending orderRow
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("auth_order").Where("creator_id=? AND ip_id=? AND status=? AND payment_expire_at>?", current.CreatorID, ipID, statusPending, time.Now()).Take(&pending).Error
		if err == nil {
			code = "AUTH_ORDER_PENDING_EXISTS"
			result = orderData(pending)
			return nil
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		var mine int64
		if err := tx.Table("creator_authorization").Where("creator_id=? AND ip_id=? AND status='ACTIVE' AND active_flag=1", current.CreatorID, ipID).Count(&mine).Error; err != nil {
			return err
		}
		if mine > 0 {
			code = "AUTHORIZATION_ALREADY_ACTIVE"
			return nil
		}
		if item.Mode == "EXCLUSIVE_BUYOUT" {
			var active int64
			if err := tx.Table("creator_authorization").Where("ip_id=? AND status='ACTIVE' AND active_flag=1", ipID).Count(&active).Error; err != nil {
				return err
			}
			if active > 0 {
				code = "AUTHORIZATION_QUOTA_SOLD_OUT"
				return nil
			}
		}
		now := time.Now()
		expiresAt := now.Add(s.timeout(tx) * time.Minute)
		order := orderRow{ID: idgen.Next(), AuthOrderNo: fmt.Sprintf("AUTH-%s-%d", now.Format("200601"), idgen.Next()), CreatorID: current.CreatorID, IPID: ipID, PlanVersionID: item.PlanVersionID, Amount: item.Price, Currency: "CNY", Status: statusPending, PaymentExpireAt: expiresAt, ContractStatus: "NOT_GENERATED", EvidenceStatus: "NOT_SUBMITTED"}
		var verification struct {
			LegalName        string `gorm:"column:legal_name"`
			CredentialType   string `gorm:"column:credential_type"`
			CredentialCipher []byte `gorm:"column:credential_cipher"`
			VerificationNo   string `gorm:"column:verification_no"`
		}
		if err := tx.Table("creator_identity_verification").Where("creator_id=? AND result_status='VERIFIED'", current.CreatorID).Order("created_at DESC").Take(&verification).Error; err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		legalName := strings.TrimSpace(verification.LegalName)
		if legalName == "" {
			legalName = profile.DisplayName
		}
		credentialMasked := maskCredential(string(verification.CredentialCipher))
		if credentialMasked == "" {
			credentialMasked = verification.VerificationNo
		}
		var platform struct {
			OrgCode string `gorm:"column:org_code"`
			OrgName string `gorm:"column:org_name"`
		}
		_ = tx.Table("sys_org").Select("org_code,org_name").Where("org_type='PLATFORM' AND status='ACTIVE'").Order("id").Take(&platform).Error
		var distributionItems []map[string]any
		if item.DistributionPlanID != 0 {
			if err := tx.Table("finance_distribution_plan_item").Select("participant_type,ratio,sort_no").Where("distribution_plan_id=? AND version_no=?", item.DistributionPlanID, item.DistributionVersionNo).Order("sort_no,id").Find(&distributionItems).Error; err != nil {
				return err
			}
		}
		// 合同、授权详情和后置 PDF 仅以订单快照为事实来源，绝不在生成时回读可变的 IP 或方案数据。
		// 敏感原文不进入快照；购买主体仅保存合同展示所需的最小信息。
		snapshot := map[string]any{
			"snapshotVersion": 1,
			"snapshotAt":      now,
			"order":           map[string]any{"authOrderNo": order.AuthOrderNo, "amount": item.Price, "currency": order.Currency, "contractAgreed": true, "contractAgreedAt": now, "contractTermsVersion": "v0.5"},
			"platform":        map[string]any{"orgCode": platform.OrgCode, "orgName": platform.OrgName},
			"buyer":           map[string]any{"creatorId": fmt.Sprint(current.CreatorID), "displayName": profile.DisplayName, "subjectType": profile.SubjectType, "legalName": legalName, "credentialType": verification.CredentialType, "credentialNoMasked": credentialMasked, "verificationNo": verification.VerificationNo},
			"ip":              map[string]any{"ipId": fmt.Sprint(item.IPID), "ipNo": item.IPNo, "ipName": item.IPName, "cityOrgName": item.CityOrgName, "rightsNotice": item.RightsNotice, "purchaseNotice": item.PurchaseNotice, "rightHolderNameMasked": item.HolderNameMasked, "rightHolderType": item.HolderType, "acquisitionMethod": item.AcquisitionMethod, "rightsDescription": item.RightsDescription},
			"authorization":   map[string]any{"authorizationMode": item.Mode, "planVersionId": fmt.Sprint(item.PlanVersionID), "planName": item.PlanName, "termType": item.TermType, "termValue": item.TermValue, "validFrom": dateText(item.ValidFrom), "validTo": dateText(item.ValidTo), "regionMode": item.RegionMode, "regionCodes": rawJSON(item.RegionCodesJSON), "scenes": rawJSON(item.ScenesJSON), "commercialAllowed": item.CommercialAllowed, "derivativeAllowed": item.DerivativeAllowed, "nCreationAllowed": item.NCreationAllowed, "contentType": item.ContentType, "quotaTotal": item.QuotaTotal},
			"distribution":    map[string]any{"planId": fmt.Sprint(item.DistributionPlanID), "versionNo": item.DistributionVersionNo, "name": item.DistributionName, "type": item.DistributionType, "items": distributionItems},
			"materialPack":    map[string]any{"manifestHash": item.MaterialManifestHash},
		}
		order.PurchaseSnapshot, err = json.Marshal(snapshot)
		if err != nil {
			return err
		}
		needsReservation := item.QuotaTotal != nil || item.Mode == "EXCLUSIVE_BUYOUT"
		if needsReservation {
			var used int64
			if err := tx.Table("creator_authorization").Where("ip_id=? AND status='ACTIVE' AND active_flag=1", ipID).Count(&used).Error; err != nil {
				return err
			}
			var reserved int64
			if err := tx.Table("auth_quota_reservation").Where("ip_id=? AND reservation_status='RESERVED' AND expires_at>?", ipID, now).Count(&reserved).Error; err != nil {
				return err
			}
			quota := 1
			if item.QuotaTotal != nil {
				quota = *item.QuotaTotal
			}
			if used+reserved >= int64(quota) {
				code = "AUTHORIZATION_QUOTA_SOLD_OUT"
				return nil
			}
		}
		if err := tx.Table("auth_order").Create(map[string]any{"id": order.ID, "status": order.Status, "version": 1, "created_by": current.UserID, "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID), "auth_order_no": order.AuthOrderNo, "creator_id": order.CreatorID, "ip_id": order.IPID, "auth_plan_version_id": order.PlanVersionID, "amount": order.Amount, "currency": order.Currency, "payment_expire_at": order.PaymentExpireAt, "contract_agreed": true, "buyer_display_name": profile.DisplayName, "buyer_subject_type": profile.SubjectType, "buyer_legal_name": legalName, "buyer_credential_type": verification.CredentialType, "buyer_credential_masked": credentialMasked, "ip_no_snapshot": item.IPNo, "ip_name_snapshot": item.IPName, "plan_name_snapshot": item.PlanName, "authorization_mode_snapshot": item.Mode, "term_type_snapshot": item.TermType, "term_value_snapshot": item.TermValue, "region_mode_snapshot": item.RegionMode, "content_type_snapshot": item.ContentType, "commercial_allowed_snapshot": item.CommercialAllowed, "derivative_allowed_snapshot": item.DerivativeAllowed, "n_creation_allowed_snapshot": item.NCreationAllowed, "purchase_snapshot": order.PurchaseSnapshot, "material_pack_manifest_hash": item.MaterialManifestHash, "contract_status": order.ContractStatus, "evidence_status": order.EvidenceStatus}).Error; err != nil {
			return err
		}
		if needsReservation {
			if err := tx.Table("auth_quota_reservation").Create(map[string]any{"id": idgen.Next(), "reservation_no": fmt.Sprintf("AQR-%d", idgen.Next()), "auth_order_id": order.ID, "creator_id": current.CreatorID, "ip_id": ipID, "auth_plan_version_id": item.PlanVersionID, "quantity": 1, "reservation_status": "RESERVED", "expires_at": expiresAt}).Error; err != nil {
				return err
			}
		}
		op := audit.Operator{Type: "USER", ID: current.UserID, RoleCode: "CREATOR", ClientIP: clientIP}
		if err := audit.WriteStatusHistory(tx, "AUTH_ORDER", order.ID, "", statusPending, "创建待支付授权订单", op); err != nil {
			return err
		}
		if err := audit.WriteLog(tx, "CREATE_AUTH_ORDER", "AUTH_ORDER", order.ID, nil, snapshot, "SUCCESS", op); err != nil {
			return err
		}
		result = orderData(order)
		return nil
	})
	return result, code, err
}

func (s *Service) Get(ctx context.Context, current creators.CurrentCreator, orderID uint64) (map[string]any, string, error) {
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.closeExpiredForOrder(tx, orderID, time.Now())
	}); err != nil {
		return nil, "", err
	}
	var order orderRow
	if err := s.db.WithContext(ctx).Table("auth_order").Where("id=? AND creator_id=?", orderID, current.CreatorID).Take(&order).Error; err != nil {
		return nil, "AUTH_ORDER_NOT_FOUND", nil
	}
	return orderData(order), "", nil
}

func (s *Service) Cancel(ctx context.Context, current creators.CurrentCreator, orderID uint64, reason, clientIP string) (map[string]any, string, error) {
	var result map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.closeExpiredForOrder(tx, orderID, time.Now()); err != nil {
			return err
		}
		var order orderRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("auth_order").Where("id=? AND creator_id=?", orderID, current.CreatorID).Take(&order).Error; err != nil {
			code = "AUTH_ORDER_NOT_FOUND"
			return nil
		}
		if order.Status == statusClosed {
			result = orderData(order)
			return nil
		}
		if order.Status != statusPending {
			result = map[string]any{"currentStatus": order.Status, "action": "CANCEL_AUTH_ORDER", "reasonCode": "AUTH_ORDER_NOT_PENDING"}
			code = "STATE_TRANSITION_NOT_ALLOWED"
			return nil
		}
		if err := s.closeOrder(tx, order, "USER_CANCELLED", reason, audit.Operator{Type: "USER", ID: current.UserID, RoleCode: "CREATOR", ClientIP: clientIP}); err != nil {
			return err
		}
		order.Status, order.ReasonCode, order.Reason = statusClosed, "USER_CANCELLED", reason
		result = orderData(order)
		return nil
	})
	return result, code, err
}

func (s *Service) CloseExpired(ctx context.Context) (int64, error) {
	var rows []orderRow
	if err := s.db.WithContext(ctx).Table("auth_order").Where("status=? AND payment_expire_at<=?", statusPending, time.Now()).Limit(100).Find(&rows).Error; err != nil {
		return 0, err
	}
	var closed int64
	for _, order := range rows {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return s.closeExpiredForOrder(tx, order.ID, time.Now()) })
		if err != nil {
			return closed, err
		}
		closed++
	}
	return closed, nil
}

func (s *Service) closeExpiredForCreatorIP(tx *gorm.DB, creatorID, ipID uint64, now time.Time) error {
	var rows []orderRow
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("auth_order").Where("creator_id=? AND ip_id=? AND status=? AND payment_expire_at<=?", creatorID, ipID, statusPending, now).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.closeOrder(tx, row, "PAYMENT_TIMEOUT", "支付超时关闭", audit.Operator{Type: "SYSTEM", RoleCode: "SYSTEM"}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) closeExpiredForOrder(tx *gorm.DB, orderID uint64, now time.Time) error {
	var order orderRow
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("auth_order").Where("id=? AND status=? AND payment_expire_at<=?", orderID, statusPending, now).Take(&order).Error
	if err == gorm.ErrRecordNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	return s.closeOrder(tx, order, "PAYMENT_TIMEOUT", "支付超时关闭", audit.Operator{Type: "SYSTEM", RoleCode: "SYSTEM"})
}

func (s *Service) closeOrder(tx *gorm.DB, order orderRow, reasonCode, reason string, op audit.Operator) error {
	now := time.Now()
	updated := tx.Table("auth_order").Where("id=? AND status=?", order.ID, statusPending).Updates(map[string]any{"status": statusClosed, "status_reason_code": reasonCode, "status_reason": reason, "version": gorm.Expr("version+1"), "updated_by": nullableID(op.ID), "status_changed_at": now, "status_changed_by_type": op.Type, "status_changed_by_id": fmt.Sprint(op.ID)})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected == 0 {
		return nil
	}
	if err := tx.Table("auth_quota_reservation").Where("auth_order_id=? AND reservation_status='RESERVED'", order.ID).Updates(map[string]any{"reservation_status": "RELEASED", "released_at": now, "release_reason": reasonCode}).Error; err != nil {
		return err
	}
	if err := audit.WriteStatusHistory(tx, "AUTH_ORDER", order.ID, statusPending, statusClosed, reason, op); err != nil {
		return err
	}
	return audit.WriteLog(tx, "CLOSE_AUTH_ORDER", "AUTH_ORDER", order.ID, map[string]any{"status": statusPending}, map[string]any{"status": statusClosed, "reasonCode": reasonCode}, "SUCCESS", op)
}

func (s *Service) timeout(tx *gorm.DB) time.Duration {
	var value string
	if err := tx.Table("sys_config").Select("config_value").Where("config_key='AUTH_ORDER_PAYMENT_TIMEOUT_MINUTES'").Take(&value).Error; err == nil {
		if minutes, e := strconv.Atoi(value); e == nil && minutes > 0 {
			return time.Duration(minutes)
		}
	}
	return 30
}

func parseID(raw string) (uint64, error) {
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("invalid id")
	}
	return id, nil
}
func nullableID(id uint64) any {
	if id == 0 {
		return nil
	}
	return id
}
func orderData(order orderRow) map[string]any {
	var snapshot any
	_ = json.Unmarshal(order.PurchaseSnapshot, &snapshot)
	return map[string]any{"id": fmt.Sprint(order.ID), "authOrderNo": order.AuthOrderNo, "status": order.Status, "statusReasonCode": order.ReasonCode, "statusReason": order.Reason, "amount": order.Amount, "currency": defaultString(order.Currency, "CNY"), "paymentExpireAt": order.PaymentExpireAt, "paymentNo": order.PaymentNo, "paidAt": order.PaidAt, "contractStatus": defaultString(order.ContractStatus, "NOT_GENERATED"), "evidenceStatus": defaultString(order.EvidenceStatus, "NOT_SUBMITTED"), "purchaseSnapshot": snapshot}
}

func rawJSON(value string) any {
	if strings.TrimSpace(value) == "" {
		return []any{}
	}
	var result any
	if json.Unmarshal([]byte(value), &result) != nil {
		return []any{}
	}
	return result
}

func dateText(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.Format("2006-01-02")
}

func maskCredential(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= 4 {
		return strings.Repeat("*", len(runes))
	}
	visible := 4
	return strings.Repeat("*", len(runes)-visible) + string(runes[len(runes)-visible:])
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
func ErrorStatus(code string) int {
	switch code {
	case "RESOURCE_NOT_FOUND", "AUTH_ORDER_NOT_FOUND":
		return http.StatusNotFound
	case "AUTH_ORDER_PENDING_EXISTS", "AUTHORIZATION_QUOTA_SOLD_OUT":
		return http.StatusConflict
	case "STATE_TRANSITION_NOT_ALLOWED":
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadRequest
	}
}
