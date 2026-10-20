// Package plans 提供授权方案和分账结构模板管理；IP具体比例不在本模块保存。
package plans

import (
	"context"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"sort"
	"strings"
	"time"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/apiresponse"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/idgen"
)

type Service struct{ db *gorm.DB }

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

type Participant struct {
	ParticipantType string `json:"participantType" binding:"required,max=40"`
	SortNo          int    `json:"sortNo" binding:"min=0"`
}
type DistributionCreate struct {
	DistributionNo   string        `json:"distributionNo" binding:"required,max=64"`
	DistributionName string        `json:"distributionName" binding:"required,max=200"`
	DistributionType string        `json:"distributionType" binding:"required,oneof=NORMAL_SHARE BUYOUT_FEE"`
	Items            []Participant `json:"items" binding:"required,min=1"`
}
type TemplateUpdate struct {
	DistributionName string        `json:"distributionName" binding:"required,max=200"`
	Items            []Participant `json:"items" binding:"required,min=1"`
	Enabled          *bool         `json:"enabled"`
}
type AuthCreate struct {
	PlanNo            string   `json:"planNo" binding:"required,max=64"`
	PlanName          string   `json:"planName" binding:"required,max=200"`
	AuthorizationMode string   `json:"authorizationMode" binding:"required,oneof=NORMAL_SHARE EXCLUSIVE_BUYOUT"`
	TermType          string   `json:"termType" binding:"required,max=40"`
	TermValue         *int     `json:"termValue"`
	ValidFrom         string   `json:"validFrom" binding:"omitempty,datetime=2006-01-02"`
	ValidTo           string   `json:"validTo" binding:"omitempty,datetime=2006-01-02"`
	RegionMode        string   `json:"regionMode" binding:"required,max=40"`
	RegionCodes       []string `json:"regionCodes"`
	Scenes            []string `json:"scenes" binding:"required,min=1"`
	CommercialAllowed bool     `json:"commercialAllowed"`
	DerivativeAllowed bool     `json:"derivativeAllowed"`
	NCreationAllowed  bool     `json:"nCreationAllowed"`
	ContentType       string   `json:"contentType" binding:"required,max=40"`
	QuotaTotal        *int     `json:"quotaTotal" binding:"omitempty,min=1"`
}

// AuthDraftUpdate 允许管理员修订尚未生效的固定授权方案；编号生成后保持不变，便于历史追溯。
type AuthDraftUpdate struct {
	PlanName          string   `json:"planName" binding:"required,max=200"`
	AuthorizationMode string   `json:"authorizationMode" binding:"required,oneof=NORMAL_SHARE EXCLUSIVE_BUYOUT"`
	TermType          string   `json:"termType" binding:"required,max=40"`
	TermValue         *int     `json:"termValue"`
	ValidFrom         string   `json:"validFrom" binding:"omitempty,datetime=2006-01-02"`
	ValidTo           string   `json:"validTo" binding:"omitempty,datetime=2006-01-02"`
	RegionMode        string   `json:"regionMode" binding:"required,max=40"`
	RegionCodes       []string `json:"regionCodes"`
	Scenes            []string `json:"scenes" binding:"required,min=1"`
	CommercialAllowed bool     `json:"commercialAllowed"`
	DerivativeAllowed bool     `json:"derivativeAllowed"`
	NCreationAllowed  bool     `json:"nCreationAllowed"`
	ContentType       string   `json:"contentType" binding:"required,max=40"`
	QuotaTotal        *int     `json:"quotaTotal" binding:"omitempty,min=1"`
}
type Review struct {
	Result  string `json:"result" binding:"required,oneof=PASS RETURN"`
	Comment string `json:"comment" binding:"required,max=500"`
}
type Status struct {
	Reason string `json:"reason" binding:"required,max=500"`
}

func operator(c identity.CurrentAdmin, ip string) audit.Operator {
	return audit.Operator{Type: "USER", ID: c.UserID, RoleCode: first(c.RoleCodes), ClientIP: ip}
}
func first(v []string) string {
	if len(v) > 0 {
		return v[0]
	}
	return ""
}

// validateParticipants 防止模板存在重复参与方；比例由后续IP录入功能填写，因此模板只保存结构。
func validateParticipants(items []Participant) bool {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		item.ParticipantType = strings.TrimSpace(item.ParticipantType)
		if _, allowed := distributionParticipants[item.ParticipantType]; !allowed {
			return false
		}
		if _, exists := seen[item.ParticipantType]; exists {
			return false
		}
		seen[item.ParticipantType] = struct{}{}
	}
	return true
}

var (
	distributionParticipants = map[string]struct{}{"RIGHT_HOLDER": {}, "CREATOR": {}, "CITY_OPERATOR": {}, "PLATFORM": {}}
	authorizationTermTypes   = map[string]struct{}{"MONTH": {}, "YEAR": {}, "DATE_RANGE": {}}
	authorizationRegions     = map[string]struct{}{"NATIONWIDE": {}, "DESIGNATED": {}}
	authorizationScenes      = map[string]struct{}{"PRODUCT_SALE": {}, "ACTIVITY": {}, "PROMOTION": {}, "ONLINE_CONTENT": {}, "OFFLINE_MATERIAL": {}}
	authorizationContents    = map[string]struct{}{"PRODUCT_ONLY": {}, "ACTIVITY_ONLY": {}, "PRODUCT_AND_ACTIVITY": {}}
)

// validateAuthFields 统一校验授权方案的固定枚举及其组合，避免无效码被写入后在商城筛选和前端下拉中失配。
func validateAuthFields(mode, termType string, termValue *int, validFrom, validTo, regionMode string, regionCodes, scenes []string, contentType string, quotaTotal *int) string {
	if mode != "NORMAL_SHARE" && mode != "EXCLUSIVE_BUYOUT" {
		return "VALIDATION_FORMAT_INVALID"
	}
	if _, ok := authorizationTermTypes[termType]; !ok {
		return "VALIDATION_FORMAT_INVALID"
	}
	if _, ok := authorizationRegions[regionMode]; !ok {
		return "VALIDATION_FORMAT_INVALID"
	}
	if _, ok := authorizationContents[contentType]; !ok || (quotaTotal != nil && *quotaTotal < 1) {
		return "VALIDATION_FORMAT_INVALID"
	}
	if mode == "EXCLUSIVE_BUYOUT" && (quotaTotal == nil || *quotaTotal != 1) {
		return "VALIDATION_FORMAT_INVALID"
	}
	if !uniqueAllowed(scenes, authorizationScenes) {
		return "VALIDATION_FORMAT_INVALID"
	}
	if termType == "DATE_RANGE" {
		if termValue != nil || validFrom == "" || validTo == "" {
			return "VALIDATION_FORMAT_INVALID"
		}
		from, fromErr := time.Parse("2006-01-02", validFrom)
		to, toErr := time.Parse("2006-01-02", validTo)
		if fromErr != nil || toErr != nil || from.After(to) {
			return "VALIDATION_FORMAT_INVALID"
		}
	} else if termValue == nil || *termValue < 1 || validFrom != "" || validTo != "" {
		return "VALIDATION_FORMAT_INVALID"
	}
	if regionMode == "NATIONWIDE" {
		if len(regionCodes) != 0 {
			return "VALIDATION_FORMAT_INVALID"
		}
	} else if !validRegionCodes(regionCodes) {
		return "VALIDATION_FORMAT_INVALID"
	}
	return ""
}

func uniqueAllowed(values []string, allowed map[string]struct{}) bool {
	if len(values) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := allowed[value]; !ok {
			return false
		}
		if _, duplicated := seen[value]; duplicated {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validRegionCodes(codes []string) bool {
	if len(codes) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
			return false
		}
		if _, duplicated := seen[code]; duplicated {
			return false
		}
		seen[code] = struct{}{}
	}
	return true
}

func nullableDate(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableJSON(include bool, value []byte) any {
	if !include {
		return nil
	}
	return string(value)
}

func (s *Service) ListTemplates(ctx context.Context, enabledOnly bool) (map[string]any, error) {
	q := s.db.WithContext(ctx).Table("finance_distribution_plan")
	if enabledOnly {
		q = q.Where("status = ?", "ENABLED")
	}
	var rows []map[string]any
	err := q.Order("created_at DESC").Find(&rows).Error
	return map[string]any{"items": apiresponse.CamelizeMaps(rows)}, err
}
func (s *Service) CreateTemplate(ctx context.Context, c identity.CurrentAdmin, r DistributionCreate, ip string) (map[string]any, string, error) {
	if len(r.Items) == 0 || !validateParticipants(r.Items) {
		return nil, "VALIDATION_REQUIRED", nil
	}
	id := idgen.Next()
	now := time.Now()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("finance_distribution_plan").Create(map[string]any{"id": id, "status": "ENABLED", "version": 1, "created_by": c.UserID, "distribution_no": r.DistributionNo, "distribution_name": r.DistributionName, "distribution_type": r.DistributionType, "current_version_no": 1, "is_bound": false, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error; err != nil {
			return err
		}
		for _, item := range r.Items {
			// 模板内参与方均为必填，IP 入库时必须逐项配置实际比例。
			if err := tx.Table("finance_distribution_plan_item").Create(map[string]any{"id": idgen.Next(), "created_by": c.UserID, "distribution_plan_id": id, "version_no": 1, "participant_type": item.ParticipantType, "is_required": true, "ratio": nil, "sort_no": item.SortNo}).Error; err != nil {
				return err
			}
		}
		return audit.WriteLog(tx, "DISTRIBUTION_TEMPLATE_CREATED", "DISTRIBUTION_TEMPLATE", id, nil, r, "SUCCESS", operator(c, ip))
	})
	if err != nil {
		return nil, "RESOURCE_CONFLICT", err
	}
	return map[string]any{"id": fmt.Sprint(id), "status": "ENABLED"}, "", nil
}

// UpdateTemplate 仅允许未被IP使用的模板变更参与方或状态，避免破坏已冻结IP比例的依据。
func (s *Service) UpdateTemplate(ctx context.Context, c identity.CurrentAdmin, id uint64, version uint32, r TemplateUpdate, ip string) (map[string]any, string, error) {
	if len(r.Items) == 0 || !validateParticipants(r.Items) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var row struct {
		Status  string
		Version uint32
		IsBound bool
	}
	if err := s.db.WithContext(ctx).Table("finance_distribution_plan").Where("id=?", id).First(&row).Error; err != nil {
		return nil, "RESOURCE_NOT_FOUND", nil
	}
	if row.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	if row.IsBound {
		return map[string]any{"currentStatus": row.Status, "action": "CHANGE_DISTRIBUTION_PLAN_STATUS", "reasonCode": "PLAN_ALREADY_BOUND"}, "AUTH_PLAN_BOUND_LOCKED", nil
	}
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	status := "DISABLED"
	if enabled {
		status = "ENABLED"
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("finance_distribution_plan").Where("id=?", id).Updates(map[string]any{"distribution_name": r.DistributionName, "status": status, "version": version + 1, "updated_by": c.UserID}).Error; err != nil {
			return err
		}
		if err := tx.Table("finance_distribution_plan_item").Where("distribution_plan_id=?", id).Delete(nil).Error; err != nil {
			return err
		}
		for _, i := range r.Items {
			if err := tx.Table("finance_distribution_plan_item").Create(map[string]any{"id": idgen.Next(), "created_by": c.UserID, "distribution_plan_id": id, "version_no": 1, "participant_type": i.ParticipantType, "is_required": true, "ratio": nil, "sort_no": i.SortNo}).Error; err != nil {
				return err
			}
		}
		return audit.WriteLog(tx, "DISTRIBUTION_TEMPLATE_UPDATED", "DISTRIBUTION_TEMPLATE", id, nil, r, "SUCCESS", operator(c, ip))
	})
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"id": fmt.Sprint(id), "status": status, "version": version + 1}, "", nil
}

// ChangeTemplateStatus 仅变更启停状态；被IP引用后必须永久保留模板结构和可追溯状态。
func (s *Service) ChangeTemplateStatus(ctx context.Context, c identity.CurrentAdmin, id uint64, version uint32, enabled bool, reason, ip string) (map[string]any, string, error) {
	var row struct {
		Status  string
		Version uint32
		IsBound bool
	}
	if err := s.db.WithContext(ctx).Table("finance_distribution_plan").Where("id=?", id).First(&row).Error; err != nil {
		return nil, "RESOURCE_NOT_FOUND", nil
	}
	if row.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	if row.IsBound {
		return map[string]any{"currentStatus": row.Status, "action": "CHANGE_DISTRIBUTION_PLAN_STATUS", "reasonCode": "PLAN_ALREADY_BOUND"}, "AUTH_PLAN_BOUND_LOCKED", nil
	}
	status := "DISABLED"
	if enabled {
		status = "ENABLED"
	}
	if row.Status == status {
		return map[string]any{"currentStatus": row.Status, "action": "CHANGE_DISTRIBUTION_PLAN_STATUS", "reasonCode": "STATUS_UNCHANGED"}, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("finance_distribution_plan").Where("id=? AND version=?", id, version).Updates(map[string]any{"status": status, "version": version + 1, "status_reason": reason, "status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID), "updated_by": c.UserID}).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "DISTRIBUTION_TEMPLATE_STATUS_CHANGED", "DISTRIBUTION_TEMPLATE", id, nil, map[string]any{"status": status, "reason": reason}, "SUCCESS", operator(c, ip))
	})
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"id": fmt.Sprint(id), "status": status, "version": version + 1}, "", nil
}
func (s *Service) CreateAuth(ctx context.Context, c identity.CurrentAdmin, r AuthCreate, ip string) (map[string]any, string, error) {
	if code := validateAuthFields(r.AuthorizationMode, r.TermType, r.TermValue, r.ValidFrom, r.ValidTo, r.RegionMode, r.RegionCodes, r.Scenes, r.ContentType, r.QuotaTotal); code != "" {
		return nil, code, nil
	}
	// MySQL JSON 列不能直接接收 Go 字符串切片，先编码为合法 JSON 数组再写入版本快照。
	scenesJSON, err := json.Marshal(r.Scenes)
	if err != nil {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	regionCodesJSON, err := json.Marshal(r.RegionCodes)
	if err != nil {
		return nil, "VALIDATION_FORMAT_INVALID", err
	}
	id, vid := idgen.Next(), idgen.Next()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("auth_plan").Create(map[string]any{"id": id, "status": "DRAFT", "version": 1, "created_by": c.UserID, "plan_no": r.PlanNo, "plan_name": r.PlanName, "current_version_id": vid, "is_bound": false}).Error; err != nil {
			return err
		}
		return tx.Table("auth_plan_version").Create(map[string]any{"id": vid, "status": "DRAFT", "version": 1, "created_by": c.UserID, "plan_id": id, "version_no": 1, "authorization_mode": r.AuthorizationMode, "term_type": r.TermType, "term_value": r.TermValue, "valid_from": nullableDate(r.ValidFrom), "valid_to": nullableDate(r.ValidTo), "region_mode": r.RegionMode, "region_codes_json": nullableJSON(r.RegionMode == "DESIGNATED", regionCodesJSON), "scenes_json": string(scenesJSON), "commercial_allowed": r.CommercialAllowed, "derivative_allowed": r.DerivativeAllowed, "n_creation_allowed": r.NCreationAllowed, "content_type": r.ContentType, "quota_total": r.QuotaTotal}).Error
	})
	if err != nil {
		return nil, "RESOURCE_CONFLICT", err
	}
	return map[string]any{"id": fmt.Sprint(id), "versionId": fmt.Sprint(vid), "status": "DRAFT"}, "", nil
}

// UpdateAuthDraft 修改草稿或退回方案的当前版本；绑定IP后方案即为历史依据，不能再改写。
func (s *Service) UpdateAuthDraft(ctx context.Context, c identity.CurrentAdmin, id uint64, version uint32, r AuthDraftUpdate, ip string) (map[string]any, string, error) {
	var plan struct {
		Status           string
		Version          uint32
		IsBound          bool
		CurrentVersionID uint64
	}
	if err := s.db.WithContext(ctx).Table("auth_plan").Where("id = ?", id).First(&plan).Error; err != nil {
		return nil, "RESOURCE_NOT_FOUND", nil
	}
	if plan.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	if plan.IsBound {
		return map[string]any{"currentStatus": plan.Status, "action": "UPDATE_AUTH_PLAN_DRAFT", "reasonCode": "PLAN_ALREADY_BOUND"}, "AUTH_PLAN_BOUND_LOCKED", nil
	}
	if plan.Status != "DRAFT" && plan.Status != "RETURNED" {
		return map[string]any{"currentStatus": plan.Status, "action": "UPDATE_AUTH_PLAN_DRAFT", "reasonCode": "AUTH_PLAN_NOT_EDITABLE"}, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	if code := validateAuthFields(r.AuthorizationMode, r.TermType, r.TermValue, r.ValidFrom, r.ValidTo, r.RegionMode, r.RegionCodes, r.Scenes, r.ContentType, r.QuotaTotal); code != "" {
		return nil, code, nil
	}
	scenesJSON, err := json.Marshal(r.Scenes)
	if err != nil {
		return nil, "VALIDATION_FORMAT_INVALID", err
	}
	regionCodesJSON, err := json.Marshal(r.RegionCodes)
	if err != nil {
		return nil, "VALIDATION_FORMAT_INVALID", err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("auth_plan").Where("id = ? AND version = ?", id, version).Updates(map[string]any{"plan_name": r.PlanName, "version": version + 1, "updated_by": c.UserID}).Error; err != nil {
			return err
		}
		values := map[string]any{"authorization_mode": r.AuthorizationMode, "term_type": r.TermType, "term_value": r.TermValue, "valid_from": nullableDate(r.ValidFrom), "valid_to": nullableDate(r.ValidTo), "region_mode": r.RegionMode, "region_codes_json": nullableJSON(r.RegionMode == "DESIGNATED", regionCodesJSON), "scenes_json": string(scenesJSON), "commercial_allowed": r.CommercialAllowed, "derivative_allowed": r.DerivativeAllowed, "n_creation_allowed": r.NCreationAllowed, "content_type": r.ContentType, "quota_total": r.QuotaTotal, "updated_by": c.UserID}
		if err := tx.Table("auth_plan_version").Where("id = ?", plan.CurrentVersionID).Updates(values).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "AUTH_PLAN_DRAFT_UPDATED", "AUTH_PLAN", id, nil, r, "SUCCESS", operator(c, ip))
	})
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"id": fmt.Sprint(id), "status": plan.Status, "version": version + 1}, "", nil
}

func (s *Service) ListAuthPlans(ctx context.Context, enabledOnly bool) (map[string]any, error) {
	// 不能把 BIGINT 扫描到 map[string]any：database/sql 的数值在通用 map
	// 中可能经过 float64，超过 2^53 的雪花 ID 会发生精度损失。ID 先在 SQL
	// 中转成字符并扫描到强类型响应，确保 auth_plan.id 不会变成版本 ID 或被四舍五入。
	q := s.db.WithContext(ctx).Table("auth_plan p").Select("CAST(p.id AS CHAR) AS id,p.status,p.status_reason_code,p.status_reason,p.version,p.created_by,p.created_at,p.updated_by,p.updated_at,p.status_changed_at,p.status_changed_by_type,p.status_changed_by_id,p.plan_no,p.plan_name,CAST(p.current_version_id AS CHAR) AS current_version_id,p.is_bound,v.authorization_mode,v.content_type,v.quota_total").Joins("left join auth_plan_version v on v.id=p.current_version_id")
	if enabledOnly {
		q = q.Where("p.status=?", "ENABLED")
	}
	var rows []authPlanListItem
	err := q.Order("p.created_at desc").Find(&rows).Error
	return map[string]any{"items": rows}, err
}

type authPlanListItem struct {
	ID                  string     `json:"id" gorm:"column:id"`
	Status              string     `json:"status" gorm:"column:status"`
	StatusReasonCode    *string    `json:"statusReasonCode" gorm:"column:status_reason_code"`
	StatusReason        *string    `json:"statusReason" gorm:"column:status_reason"`
	Version             uint32     `json:"version" gorm:"column:version"`
	CreatedBy           uint64     `json:"createdBy" gorm:"column:created_by"`
	CreatedAt           time.Time  `json:"createdAt" gorm:"column:created_at"`
	UpdatedBy           *uint64    `json:"updatedBy" gorm:"column:updated_by"`
	UpdatedAt           time.Time  `json:"updatedAt" gorm:"column:updated_at"`
	StatusChangedAt     *time.Time `json:"statusChangedAt" gorm:"column:status_changed_at"`
	StatusChangedByType *string    `json:"statusChangedByType" gorm:"column:status_changed_by_type"`
	StatusChangedByID   *string    `json:"statusChangedById" gorm:"column:status_changed_by_id"`
	PlanNo              string     `json:"planNo" gorm:"column:plan_no"`
	PlanName            string     `json:"planName" gorm:"column:plan_name"`
	CurrentVersionID    string     `json:"currentVersionId" gorm:"column:current_version_id"`
	IsBound             bool       `json:"isBound" gorm:"column:is_bound"`
	AuthorizationMode   *string    `json:"authorizationMode" gorm:"column:authorization_mode"`
	ContentType         *string    `json:"contentType" gorm:"column:content_type"`
	QuotaTotal          *int       `json:"quotaTotal" gorm:"column:quota_total"`
}

// sortParticipantItems 给详情接口稳定排序，前端可直接按模板顺序渲染比例输入项。
func sortParticipantItems(items []map[string]any) {
	sort.Slice(items, func(i, j int) bool { return fmt.Sprint(items[i]["sort_no"]) < fmt.Sprint(items[j]["sort_no"]) })
}
func (s *Service) ChangeAuthStatus(ctx context.Context, c identity.CurrentAdmin, id uint64, version uint32, action, result, reason, ip string) (map[string]any, string, error) {
	var row struct {
		Status           string
		Version          uint32
		IsBound          bool
		CurrentVersionID uint64
	}
	if err := s.db.WithContext(ctx).Table("auth_plan").Where("id=?", id).First(&row).Error; err != nil {
		return nil, "RESOURCE_NOT_FOUND", nil
	}
	if row.Version != version {
		return nil, "VERSION_CONFLICT", nil
	}
	next := ""
	switch action {
	case "submit":
		if row.Status == "DRAFT" || row.Status == "RETURNED" {
			next = "PENDING_COMPLIANCE"
		}
	case "review":
		if row.Status == "PENDING_COMPLIANCE" {
			if result == "PASS" {
				next = "CONFIRMED"
			} else {
				next = "RETURNED"
			}
		}
	case "enable":
		if row.Status == "CONFIRMED" {
			next = "ENABLED"
		}
	case "disable":
		if row.Status == "ENABLED" && !row.IsBound {
			next = "DISABLED"
		}
	}
	if next == "" {
		return map[string]any{"currentStatus": row.Status, "action": strings.ToUpper(action), "reasonCode": "AUTH_PLAN_TRANSITION_NOT_ALLOWED"}, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("auth_plan").Where("id=? and version=?", id, version).Updates(map[string]any{"status": next, "version": version + 1, "updated_by": c.UserID, "status_reason": reason, "status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error; err != nil {
			return err
		}
		versionResult := tx.Table("auth_plan_version").Where("id=? AND plan_id=?", row.CurrentVersionID, id).Updates(map[string]any{
			"status": next, "version": gorm.Expr("version + 1"), "updated_by": c.UserID, "status_reason": reason,
			"status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID),
		})
		if versionResult.Error != nil {
			return versionResult.Error
		}
		if versionResult.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if err := audit.WriteStatusHistory(tx, "AUTH_PLAN", id, row.Status, next, reason, operator(c, ip)); err != nil {
			return err
		}
		return audit.WriteLog(tx, "AUTH_PLAN_"+strings.ToUpper(action), "AUTH_PLAN", id, nil, map[string]any{"status": next}, "SUCCESS", operator(c, ip))
	})
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"id": fmt.Sprint(id), "status": next, "version": version + 1}, "", nil
}
