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
	PlanNo                 string   `json:"planNo" binding:"required,max=64"`
	PlanName               string   `json:"planName" binding:"required,max=200"`
	AuthorizationMode      string   `json:"authorizationMode" binding:"required,oneof=NORMAL_SHARE EXCLUSIVE_BUYOUT"`
	TermType               string   `json:"termType" binding:"required,max=40"`
	TermValue              *int     `json:"termValue"`
	RegionMode             string   `json:"regionMode" binding:"required,max=40"`
	Scenes                 []string `json:"scenes" binding:"required,min=1"`
	CommercialAllowed      bool     `json:"commercialAllowed"`
	DerivativeAllowed      bool     `json:"derivativeAllowed"`
	NCreationAllowed       bool     `json:"nCreationAllowed"`
	ContentType            string   `json:"contentType" binding:"required,max=40"`
	QuotaTotal             int      `json:"quotaTotal" binding:"min=1"`
	PriceAmount            string   `json:"priceAmount" binding:"required,max=32"`
	DistributionTemplateID uint64   `json:"distributionTemplateId" binding:"required"`
}

// AuthDraftUpdate 允许管理员修订尚未生效的固定授权方案；编号生成后保持不变，便于历史追溯。
type AuthDraftUpdate struct {
	PlanName               string   `json:"planName" binding:"required,max=200"`
	AuthorizationMode      string   `json:"authorizationMode" binding:"required,oneof=NORMAL_SHARE EXCLUSIVE_BUYOUT"`
	TermType               string   `json:"termType" binding:"required,max=40"`
	TermValue              *int     `json:"termValue"`
	RegionMode             string   `json:"regionMode" binding:"required,max=40"`
	Scenes                 []string `json:"scenes" binding:"required,min=1"`
	CommercialAllowed      bool     `json:"commercialAllowed"`
	DerivativeAllowed      bool     `json:"derivativeAllowed"`
	NCreationAllowed       bool     `json:"nCreationAllowed"`
	ContentType            string   `json:"contentType" binding:"required,max=40"`
	QuotaTotal             int      `json:"quotaTotal" binding:"min=1"`
	PriceAmount            string   `json:"priceAmount" binding:"required,max=32"`
	DistributionTemplateID uint64   `json:"distributionTemplateId" binding:"required"`
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
		if item.ParticipantType == "" {
			return false
		}
		if _, exists := seen[item.ParticipantType]; exists {
			return false
		}
		seen[item.ParticipantType] = struct{}{}
	}
	return true
}

func templateTypeForAuthorization(mode string) string {
	if mode == "EXCLUSIVE_BUYOUT" {
		return "BUYOUT_FEE"
	}
	return "NORMAL_SHARE"
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
		return nil, "AUTH_PLAN_BOUND_LOCKED", nil
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
		return nil, "AUTH_PLAN_BOUND_LOCKED", nil
	}
	status := "DISABLED"
	if enabled {
		status = "ENABLED"
	}
	if row.Status == status {
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
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
	if code := s.validateAuthorizationTemplate(ctx, r.AuthorizationMode, r.DistributionTemplateID); code != "" {
		return nil, code, nil
	}
	// MySQL JSON 列不能直接接收 Go 字符串切片，先编码为合法 JSON 数组再写入版本快照。
	scenesJSON, err := json.Marshal(r.Scenes)
	if err != nil {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	id, vid := idgen.Next(), idgen.Next()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("auth_plan").Create(map[string]any{"id": id, "status": "DRAFT", "version": 1, "created_by": c.UserID, "plan_no": r.PlanNo, "plan_name": r.PlanName, "current_version_id": vid, "is_bound": false}).Error; err != nil {
			return err
		}
		return tx.Table("auth_plan_version").Create(map[string]any{"id": vid, "status": "DRAFT", "version": 1, "created_by": c.UserID, "plan_id": id, "distribution_template_id": r.DistributionTemplateID, "version_no": 1, "authorization_mode": r.AuthorizationMode, "term_type": r.TermType, "term_value": r.TermValue, "region_mode": r.RegionMode, "scenes_json": string(scenesJSON), "commercial_allowed": r.CommercialAllowed, "derivative_allowed": r.DerivativeAllowed, "n_creation_allowed": r.NCreationAllowed, "content_type": r.ContentType, "quota_total": r.QuotaTotal, "price_amount": r.PriceAmount}).Error
	})
	if err != nil {
		return nil, "RESOURCE_CONFLICT", err
	}
	return map[string]any{"id": fmt.Sprint(id), "versionId": fmt.Sprint(vid), "distributionTemplateId": fmt.Sprint(r.DistributionTemplateID), "status": "DRAFT"}, "", nil
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
		return nil, "AUTH_PLAN_BOUND_LOCKED", nil
	}
	if plan.Status != "DRAFT" && plan.Status != "RETURNED" {
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	if code := s.validateAuthorizationTemplate(ctx, r.AuthorizationMode, r.DistributionTemplateID); code != "" {
		return nil, code, nil
	}
	scenesJSON, err := json.Marshal(r.Scenes)
	if err != nil {
		return nil, "VALIDATION_FORMAT_INVALID", err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("auth_plan").Where("id = ? AND version = ?", id, version).Updates(map[string]any{"plan_name": r.PlanName, "version": version + 1, "updated_by": c.UserID}).Error; err != nil {
			return err
		}
		values := map[string]any{"authorization_mode": r.AuthorizationMode, "term_type": r.TermType, "term_value": r.TermValue, "region_mode": r.RegionMode, "scenes_json": string(scenesJSON), "commercial_allowed": r.CommercialAllowed, "derivative_allowed": r.DerivativeAllowed, "n_creation_allowed": r.NCreationAllowed, "content_type": r.ContentType, "quota_total": r.QuotaTotal, "price_amount": r.PriceAmount, "distribution_template_id": r.DistributionTemplateID, "updated_by": c.UserID}
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

// validateAuthorizationTemplate 校验授权方式只能绑定同类型、启用中的分账结构模板。
func (s *Service) validateAuthorizationTemplate(ctx context.Context, authorizationMode string, templateID uint64) string {
	var template struct{ DistributionType string }
	if err := s.db.WithContext(ctx).Table("finance_distribution_plan").Select("distribution_type").Where("id = ? AND status = ?", templateID, "ENABLED").Take(&template).Error; err != nil {
		return "RESOURCE_NOT_FOUND"
	}
	if template.DistributionType != templateTypeForAuthorization(authorizationMode) {
		return "VALIDATION_FORMAT_INVALID"
	}
	return ""
}

func (s *Service) ListAuthPlans(ctx context.Context, enabledOnly bool) (map[string]any, error) {
	q := s.db.WithContext(ctx).Table("auth_plan p").Select("p.*,v.authorization_mode,v.content_type,v.quota_total,v.price_amount,v.distribution_template_id").Joins("left join auth_plan_version v on v.id=p.current_version_id")
	if enabledOnly {
		q = q.Where("p.status=?", "ENABLED")
	}
	var rows []map[string]any
	err := q.Order("p.created_at desc").Find(&rows).Error
	return map[string]any{"items": apiresponse.CamelizeMaps(rows)}, err
}

// sortParticipantItems 给详情接口稳定排序，前端可直接按模板顺序渲染比例输入项。
func sortParticipantItems(items []map[string]any) {
	sort.Slice(items, func(i, j int) bool { return fmt.Sprint(items[i]["sort_no"]) < fmt.Sprint(items[j]["sort_no"]) })
}
func (s *Service) ChangeAuthStatus(ctx context.Context, c identity.CurrentAdmin, id uint64, version uint32, action, result, reason, ip string) (map[string]any, string, error) {
	var row struct {
		Status  string
		Version uint32
		IsBound bool
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
		return nil, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("auth_plan").Where("id=? and version=?", id, version).Updates(map[string]any{"status": next, "version": version + 1, "updated_by": c.UserID, "status_reason": reason, "status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(c.UserID)}).Error; err != nil {
			return err
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
