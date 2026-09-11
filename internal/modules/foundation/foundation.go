// Package foundation 提供字典、地区、标签、平台配置和固定售后模板的基础管理能力。
package foundation

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/idgen"
)

const (
	regionDictType        = "REGION"
	adminConfigPermission = "ADMIN_CONFIG_MANAGE"
)

var (
	dictTypePattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	regionCodePattern = regexp.MustCompile(`^(000000|[1-9][0-9]{5})$`)
	tagTypes          = map[string]struct{}{"REGION": {}, "CULTURE": {}, "CATEGORY": {}, "COMMERCIAL": {}}
	fixedDictTypes    = map[string]struct{}{
		"DATA_SCOPE": {}, "ORG_TYPE": {}, "IP_TYPE": {}, "RIGHT_HOLDER_TYPE": {}, "CREATOR_SUBJECT_TYPE": {}, "CREATOR_IDENTITY_STATUS": {}, "AUTHORIZATION_MODE": {}, "CONTENT_TYPE": {}, "APPLICABLE_PRODUCT_TYPE": {}, "APPLICABLE_ACTIVITY_TYPE": {}, "PRODUCT_STATUS": {}, "ACTIVITY_STATUS": {}, "PAYMENT_STATUS": {}, "EVIDENCE_STATUS": {}, "CONTENT_DETECTION_STATUS": {}, "FULFILLMENT_MODE": {}, "TICKET_FEE_TYPE": {}, "FINANCE_SHARE_TYPE": {}, "DCI_CERTIFICATE_STATUS": {}, "DCI_ASSISTANCE_STATUS": {}, "SALE_REGION_SCOPE": {}, "FREIGHT_MODE": {}, "PRODUCT_AFTER_SALE_TYPE": {}, "RETURN_FEE_BEARER": {}, "ACTIVITY_AFTER_SALE_TYPE": {}, "REVIEW_RESULT": {}, "AUDIT_RESULT": {},
	}
)

// Operator 是基础数据写操作所需的最小操作人信息，避免领域层依赖具体鉴权实现。
type Operator struct {
	ID       uint64
	RoleCode string
	ClientIP string
}

// Service 负责平台级主数据和固定规则模板。
type Service struct{ db *gorm.DB }

// NewService 创建基础数据服务。
func NewService(db *gorm.DB) *Service { return &Service{db: db} }

type dictionaryItem struct {
	ID        uint64    `gorm:"column:id;primaryKey"`
	Status    string    `gorm:"column:status"`
	Version   uint32    `gorm:"column:version"`
	DictType  string    `gorm:"column:dict_type"`
	DictCode  string    `gorm:"column:dict_code"`
	DictName  string    `gorm:"column:dict_name"`
	SortNo    int       `gorm:"column:sort_no"`
	Enabled   bool      `gorm:"column:enabled"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (dictionaryItem) TableName() string { return "sys_dict_item" }

type systemConfig struct {
	ID          uint64    `gorm:"column:id;primaryKey"`
	Status      string    `gorm:"column:status"`
	Version     uint32    `gorm:"column:version"`
	ConfigKey   string    `gorm:"column:config_key"`
	ConfigValue string    `gorm:"column:config_value"`
	ValueType   string    `gorm:"column:value_type"`
	IsSecret    bool      `gorm:"column:is_secret"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (systemConfig) TableName() string { return "sys_config" }

type template struct {
	ID              uint64          `gorm:"column:id;primaryKey"`
	Status          string          `gorm:"column:status"`
	Version         uint32          `gorm:"column:version"`
	TemplateNo      string          `gorm:"column:template_no"`
	TemplateName    string          `gorm:"column:template_name"`
	BizType         string          `gorm:"column:biz_type"`
	VersionNo       uint32          `gorm:"column:version_no"`
	ConditionsJSON  json.RawMessage `gorm:"column:conditions_json"`
	RefundRuleJSON  json.RawMessage `gorm:"column:refund_rule_json"`
	ReturnFeeBearer *string         `gorm:"column:return_fee_bearer"`
	Enabled         bool            `gorm:"column:enabled"`
	CreatedAt       time.Time       `gorm:"column:created_at"`
	UpdatedAt       time.Time       `gorm:"column:updated_at"`
}

func (template) TableName() string { return "after_sale_rule_template" }

// TagCreateRequest 定义平台维护四类 IP 标签的入参。
type TagCreateRequest struct {
	TagType string `json:"tagType" binding:"required,oneof=REGION CULTURE CATEGORY COMMERCIAL"`
	TagName string `json:"tagName" binding:"required,max=128"`
	SortNo  int    `json:"sortNo" binding:"min=0,max=999999"`
}

// EnabledRequest 用于启用或停用标签、地区和模板。
type EnabledRequest struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason" binding:"required,max=500"`
}

// RegionCreateRequest 定义平台地区主数据维护入参；地区层级由标准六码编码推导。
type RegionCreateRequest struct {
	RegionCode string `json:"regionCode" binding:"required,len=6"`
	RegionName string `json:"regionName" binding:"required,max=128"`
	SortNo     int    `json:"sortNo" binding:"min=0,max=999999"`
	Enabled    bool   `json:"enabled"`
}

// ConfigUpdateRequest 仅接收非敏感配置的字符串化值。
type ConfigUpdateRequest struct {
	Value  string `json:"value" binding:"required,max=4000"`
	Reason string `json:"reason" binding:"required,max=500"`
}

// TemplateCreateRequest 使用 JSON 对象保存可配置的条件和退款计算规则，具体参数由平台管理员维护。
type TemplateCreateRequest struct {
	TemplateNo      string          `json:"templateNo" binding:"required,min=2,max=64"`
	TemplateName    string          `json:"templateName" binding:"required,max=200"`
	BizType         string          `json:"bizType" binding:"required,oneof=PRODUCT ACTIVITY"`
	Conditions      json.RawMessage `json:"conditions" binding:"required"`
	RefundRule      json.RawMessage `json:"refundRule" binding:"required"`
	ReturnFeeBearer string          `json:"returnFeeBearer" binding:"omitempty,oneof=CREATOR_PLATFORM CONSUMER"`
}

// TemplateVersionRequest 创建模板的新版本，历史商品/活动版本继续引用旧模板 ID。
type TemplateVersionRequest struct {
	TemplateName    string          `json:"templateName" binding:"required,max=200"`
	Conditions      json.RawMessage `json:"conditions" binding:"required"`
	RefundRule      json.RawMessage `json:"refundRule" binding:"required"`
	ReturnFeeBearer string          `json:"returnFeeBearer" binding:"omitempty,oneof=CREATOR_PLATFORM CONSUMER"`
}

// ListDictionary 返回指定类型的字典项；只允许读取固定公开枚举，避免把内部控制项暴露为前端数据源。
func (s *Service) ListDictionary(ctx context.Context, dictType string, enabledOnly bool) (map[string]any, string, error) {
	dictType = strings.ToUpper(strings.TrimSpace(dictType))
	if !dictTypePattern.MatchString(dictType) || !allowedDictionaryType(dictType) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	items, err := s.listDictionaryItems(ctx, dictType, enabledOnly)
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return map[string]any{"items": dictionaryResponses(items)}, "", nil
}

// ListRegions 对游客开放已启用地区，地区等级和父级通过标准行政区划编码推导。
func (s *Service) ListRegions(ctx context.Context, keyword, level, parentCode string) (map[string]any, string, error) {
	query := s.db.WithContext(ctx).Where("dict_type = ? AND enabled = ? AND status = ?", regionDictType, true, "ACTIVE")
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		query = query.Where("dict_code LIKE ? OR dict_name LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	var items []dictionaryItem
	if err := query.Order("sort_no ASC, dict_code ASC").Find(&items).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		regionLevel, parent := regionHierarchy(item.DictCode)
		if level != "" && level != regionLevel {
			continue
		}
		if parentCode != "" && parentCode != parent {
			continue
		}
		result = append(result, map[string]any{"code": item.DictCode, "name": item.DictName, "level": regionLevel, "parentCode": parent})
	}
	return map[string]any{"items": result}, "", nil
}

// ListRegionsForAdmin 返回全部地区主数据，供平台管理员维护时查看停用项。
func (s *Service) ListRegionsForAdmin(ctx context.Context, keyword string) (map[string]any, error) {
	query := s.db.WithContext(ctx).Where("dict_type = ?", regionDictType)
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		query = query.Where("dict_code LIKE ? OR dict_name LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	var items []dictionaryItem
	if err := query.Order("sort_no ASC, dict_code ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		level, parent := regionHierarchy(item.DictCode)
		result = append(result, map[string]any{"id": fmt.Sprint(item.ID), "code": item.DictCode, "name": item.DictName, "level": level, "parentCode": parent, "sortNo": item.SortNo, "enabled": item.Enabled, "version": item.Version})
	}
	return map[string]any{"items": result}, nil
}

// CreateRegion 新增标准行政区划编码，区域层级和父级由六码编码规则推导。
func (s *Service) CreateRegion(ctx context.Context, operator Operator, request RegionCreateRequest) (map[string]any, string, error) {
	code, name := strings.TrimSpace(request.RegionCode), strings.TrimSpace(request.RegionName)
	if !regionCodePattern.MatchString(code) || name == "" {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	status := "SUSPENDED"
	if request.Enabled {
		status = "ACTIVE"
	}
	item := dictionaryItem{ID: idgen.Next(), Status: status, Version: 1, DictType: regionDictType, DictCode: code, DictName: name, SortNo: request.SortNo, Enabled: request.Enabled}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "REGION_CREATED", "REGION", item.ID, nil, regionResponse(item), "SUCCESS", toAuditOperator(operator))
	})
	return foundationResult(regionResponse(item), err)
}

// ChangeRegionStatus 启用或停用地区；停用地区不会出现在公开接口和后续表单选择器中。
func (s *Service) ChangeRegionStatus(ctx context.Context, operator Operator, regionID uint64, version uint32, request EnabledRequest) (map[string]any, string, error) {
	var response map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before dictionaryItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND dict_type = ?", regionID, regionDictType).First(&before).Error; err != nil {
			return err
		}
		if before.Version != version {
			return errVersionConflict
		}
		status := "SUSPENDED"
		if request.Enabled {
			status = "ACTIVE"
		}
		if err := tx.Model(&dictionaryItem{}).Where("id = ? AND version = ?", regionID, version).Updates(map[string]any{"enabled": request.Enabled, "status": status, "status_reason": request.Reason, "version": before.Version + 1, "updated_by": operator.ID}).Error; err != nil {
			return err
		}
		after := before
		after.Enabled, after.Status, after.Version = request.Enabled, status, before.Version+1
		response = regionResponse(after)
		return audit.WriteLog(tx, "REGION_STATUS_CHANGED", "REGION", regionID, regionResponse(before), response, "SUCCESS", toAuditOperator(operator))
	})
	return foundationResult(response, err)
}

// ListTags 返回已登录用户可使用的标签库；默认隐藏停用项。
func (s *Service) ListTags(ctx context.Context, tagType, keyword string, includeDisabled bool) (map[string]any, string, error) {
	tagType = strings.ToUpper(strings.TrimSpace(tagType))
	if tagType != "" {
		if _, ok := tagTypes[tagType]; !ok {
			return nil, "VALIDATION_FORMAT_INVALID", nil
		}
	}
	query := s.db.WithContext(ctx).Model(&dictionaryItem{}).Where("dict_type LIKE ?", "TAG_%")
	if tagType != "" {
		query = query.Where("dict_type = ?", tagDictType(tagType))
	}
	if !includeDisabled {
		query = query.Where("enabled = ? AND status = ?", true, "ACTIVE")
	}
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		query = query.Where("dict_name LIKE ?", "%"+keyword+"%")
	}
	var items []dictionaryItem
	if err := query.Order("dict_type ASC, sort_no ASC, dict_name ASC").Find(&items).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, tagResponse(item))
	}
	return map[string]any{"items": result}, "", nil
}

// CreateTag 新增受控标签；标签编码由服务端按雪花 ID 生成，前端不能自行指定。
func (s *Service) CreateTag(ctx context.Context, operator Operator, request TagCreateRequest) (map[string]any, string, error) {
	tagName := strings.TrimSpace(request.TagName)
	if tagName == "" {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	item := dictionaryItem{ID: idgen.Next(), Status: "ACTIVE", Version: 1, DictType: tagDictType(request.TagType), DictName: tagName, SortNo: request.SortNo, Enabled: true}
	item.DictCode = fmt.Sprintf("TAG_%d", item.ID)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "TAG_CREATED", "TAG", item.ID, nil, tagResponse(item), "SUCCESS", toAuditOperator(operator))
	})
	return foundationResult(tagResponse(item), err)
}

// ChangeTagStatus 启用或停用标签；历史 IP 版本保存的标签快照不受影响。
func (s *Service) ChangeTagStatus(ctx context.Context, operator Operator, tagID uint64, version uint32, request EnabledRequest) (map[string]any, string, error) {
	var response map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before dictionaryItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND dict_type LIKE ?", tagID, "TAG_%").First(&before).Error; err != nil {
			return err
		}
		if before.Version != version {
			return errVersionConflict
		}
		status := "SUSPENDED"
		if request.Enabled {
			status = "ACTIVE"
		}
		if err := tx.Model(&dictionaryItem{}).Where("id = ? AND version = ?", tagID, version).Updates(map[string]any{"enabled": request.Enabled, "status": status, "status_reason": request.Reason, "version": before.Version + 1, "updated_by": operator.ID}).Error; err != nil {
			return err
		}
		after := before
		after.Enabled, after.Status, after.Version = request.Enabled, status, before.Version+1
		response = tagResponse(after)
		return audit.WriteLog(tx, "TAG_STATUS_CHANGED", "TAG", tagID, tagResponse(before), response, "SUCCESS", toAuditOperator(operator))
	})
	return foundationResult(response, err)
}

// ListConfigs 返回平台基础配置；敏感配置不回传实际值。
func (s *Service) ListConfigs(ctx context.Context) (map[string]any, error) {
	var items []systemConfig
	if err := s.db.WithContext(ctx).Order("config_key ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, configResponse(item))
	}
	return map[string]any{"items": result}, nil
}

// UpdateConfig 更新非敏感平台配置；外部密钥始终由私有配置文件管理，不通过此接口写入。
func (s *Service) UpdateConfig(ctx context.Context, operator Operator, configKey string, version uint32, request ConfigUpdateRequest) (map[string]any, string, error) {
	if !allowedConfigKey(configKey) || strings.TrimSpace(request.Value) == "" {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var response map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before systemConfig
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("config_key = ?", configKey).First(&before).Error; err != nil {
			return err
		}
		if before.IsSecret || before.Version != version {
			if before.IsSecret {
				return errSecretConfig
			}
			return errVersionConflict
		}
		if err := tx.Model(&systemConfig{}).Where("id = ? AND version = ?", before.ID, version).Updates(map[string]any{"config_value": strings.TrimSpace(request.Value), "version": before.Version + 1, "updated_by": operator.ID}).Error; err != nil {
			return err
		}
		after := before
		after.ConfigValue, after.Version = strings.TrimSpace(request.Value), before.Version+1
		response = configResponse(after)
		return audit.WriteLog(tx, "CONFIG_UPDATED", "SYS_CONFIG", before.ID, configResponse(before), response, "SUCCESS", toAuditOperator(operator))
	})
	return foundationResult(response, err)
}

// ListTemplates 查询固定售后模板的所有版本，供平台维护页面使用。
func (s *Service) ListTemplates(ctx context.Context, bizType string, enabledOnly bool) (map[string]any, string, error) {
	query := s.db.WithContext(ctx).Model(&template{})
	if bizType != "" && bizType != "PRODUCT" && bizType != "ACTIVITY" {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	if bizType != "" {
		query = query.Where("biz_type = ?", bizType)
	}
	if enabledOnly {
		query = query.Where("enabled = ? AND status = ?", true, "ACTIVE")
	}
	var items []template
	if err := query.Order("template_no ASC, version_no DESC").Find(&items).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, templateResponse(item))
	}
	return map[string]any{"items": result}, "", nil
}

// CreateTemplate 新建第一个模板版本。条件和规则必须是 JSON 对象，保留为后续订单快照的原始配置。
func (s *Service) CreateTemplate(ctx context.Context, operator Operator, request TemplateCreateRequest) (map[string]any, string, error) {
	if !validJSONObject(request.Conditions) || !validJSONObject(request.RefundRule) || (request.BizType == "ACTIVITY" && request.ReturnFeeBearer != "") {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	item := template{ID: idgen.Next(), Status: "ACTIVE", Version: 1, TemplateNo: strings.ToUpper(strings.TrimSpace(request.TemplateNo)), TemplateName: strings.TrimSpace(request.TemplateName), BizType: request.BizType, VersionNo: 1, ConditionsJSON: request.Conditions, RefundRuleJSON: request.RefundRule, Enabled: true}
	if request.ReturnFeeBearer != "" {
		value := request.ReturnFeeBearer
		item.ReturnFeeBearer = &value
	}
	if item.TemplateName == "" || !dictTypePattern.MatchString(item.TemplateNo) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		return audit.WriteLog(tx, "AFTER_SALE_TEMPLATE_CREATED", "AFTER_SALE_TEMPLATE", item.ID, nil, templateResponse(item), "SUCCESS", toAuditOperator(operator))
	})
	return foundationResult(templateResponse(item), err)
}

// CreateTemplateVersion 永远创建新记录并停用原版本，避免已发布内容引用的规则被原地改写。
func (s *Service) CreateTemplateVersion(ctx context.Context, operator Operator, templateID uint64, version uint32, request TemplateVersionRequest) (map[string]any, string, error) {
	if !validJSONObject(request.Conditions) || !validJSONObject(request.RefundRule) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var response map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before template
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", templateID).First(&before).Error; err != nil {
			return err
		}
		if before.Version != version {
			return errVersionConflict
		}
		if before.BizType == "ACTIVITY" && request.ReturnFeeBearer != "" {
			return errInvalidTemplate
		}
		var maxVersion uint32
		if err := tx.Model(&template{}).Where("template_no = ?", before.TemplateNo).Select("COALESCE(MAX(version_no), 0)").Scan(&maxVersion).Error; err != nil {
			return err
		}
		next := template{ID: idgen.Next(), Status: "ACTIVE", Version: 1, TemplateNo: before.TemplateNo, TemplateName: strings.TrimSpace(request.TemplateName), BizType: before.BizType, VersionNo: maxVersion + 1, ConditionsJSON: request.Conditions, RefundRuleJSON: request.RefundRule, Enabled: true}
		if next.TemplateName == "" {
			return errInvalidTemplate
		}
		if request.ReturnFeeBearer != "" {
			value := request.ReturnFeeBearer
			next.ReturnFeeBearer = &value
		}
		if err := tx.Model(&template{}).Where("id = ? AND version = ?", before.ID, version).Updates(map[string]any{"enabled": false, "status": "SUSPENDED", "status_reason": "已创建新版本", "version": before.Version + 1, "updated_by": operator.ID}).Error; err != nil {
			return err
		}
		if err := tx.Create(&next).Error; err != nil {
			return err
		}
		response = templateResponse(next)
		return audit.WriteLog(tx, "AFTER_SALE_TEMPLATE_VERSION_CREATED", "AFTER_SALE_TEMPLATE", next.ID, templateResponse(before), response, "SUCCESS", toAuditOperator(operator))
	})
	return foundationResult(response, err)
}

// ChangeTemplateStatus 允许停用当前模板；历史版本不能重新启用，避免创作者选到过期规则。
func (s *Service) ChangeTemplateStatus(ctx context.Context, operator Operator, templateID uint64, version uint32, request EnabledRequest) (map[string]any, string, error) {
	var response map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before template
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", templateID).First(&before).Error; err != nil {
			return err
		}
		if before.Version != version {
			return errVersionConflict
		}
		if request.Enabled {
			var newerCount int64
			if err := tx.Model(&template{}).Where("template_no = ? AND version_no > ?", before.TemplateNo, before.VersionNo).Count(&newerCount).Error; err != nil {
				return err
			}
			if newerCount > 0 {
				return errInvalidTemplate
			}
		}
		status := "SUSPENDED"
		if request.Enabled {
			status = "ACTIVE"
		}
		if err := tx.Model(&template{}).Where("id = ? AND version = ?", templateID, version).Updates(map[string]any{"enabled": request.Enabled, "status": status, "status_reason": request.Reason, "version": before.Version + 1, "updated_by": operator.ID}).Error; err != nil {
			return err
		}
		after := before
		after.Enabled, after.Status, after.Version = request.Enabled, status, before.Version+1
		response = templateResponse(after)
		return audit.WriteLog(tx, "AFTER_SALE_TEMPLATE_STATUS_CHANGED", "AFTER_SALE_TEMPLATE", templateID, templateResponse(before), response, "SUCCESS", toAuditOperator(operator))
	})
	return foundationResult(response, err)
}

func (s *Service) listDictionaryItems(ctx context.Context, dictType string, enabledOnly bool) ([]dictionaryItem, error) {
	query := s.db.WithContext(ctx).Where("dict_type = ?", dictType)
	if enabledOnly {
		query = query.Where("enabled = ? AND status = ?", true, "ACTIVE")
	}
	var items []dictionaryItem
	return items, query.Order("sort_no ASC, dict_code ASC").Find(&items).Error
}
func dictionaryResponses(items []dictionaryItem) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{"code": item.DictCode, "name": item.DictName, "sortNo": item.SortNo, "enabled": item.Enabled})
	}
	return result
}
func tagDictType(tagType string) string { return "TAG_" + strings.ToUpper(tagType) }
func tagResponse(item dictionaryItem) map[string]any {
	return map[string]any{"id": fmt.Sprint(item.ID), "tagType": strings.TrimPrefix(item.DictType, "TAG_"), "tagCode": item.DictCode, "tagName": item.DictName, "sortNo": item.SortNo, "enabled": item.Enabled, "version": item.Version, "updatedAt": item.UpdatedAt}
}
func regionResponse(item dictionaryItem) map[string]any {
	level, parent := regionHierarchy(item.DictCode)
	return map[string]any{"id": fmt.Sprint(item.ID), "code": item.DictCode, "name": item.DictName, "level": level, "parentCode": parent, "sortNo": item.SortNo, "enabled": item.Enabled, "version": item.Version}
}
func configResponse(item systemConfig) map[string]any {
	result := map[string]any{"configKey": item.ConfigKey, "valueType": item.ValueType, "isSecret": item.IsSecret, "version": item.Version, "updatedAt": item.UpdatedAt}
	if item.IsSecret {
		result["hasValue"] = item.ConfigValue != ""
	} else {
		result["value"] = item.ConfigValue
	}
	return result
}
func templateResponse(item template) map[string]any {
	return map[string]any{"id": fmt.Sprint(item.ID), "templateNo": item.TemplateNo, "templateName": item.TemplateName, "bizType": item.BizType, "versionNo": item.VersionNo, "conditions": json.RawMessage(item.ConditionsJSON), "refundRule": json.RawMessage(item.RefundRuleJSON), "returnFeeBearer": stringValue(item.ReturnFeeBearer), "enabled": item.Enabled, "status": item.Status, "version": item.Version, "updatedAt": item.UpdatedAt}
}
func regionHierarchy(code string) (string, string) {
	if code == "000000" {
		return "NATION", ""
	}
	if strings.HasSuffix(code, "0000") {
		return "PROVINCE", "000000"
	}
	if strings.HasSuffix(code, "00") {
		return "CITY", code[:2] + "0000"
	}
	return "DISTRICT", code[:4] + "00"
}
func allowedDictionaryType(value string) bool {
	_, tag := tagTypes[strings.TrimPrefix(value, "TAG_")]
	if strings.HasPrefix(value, "TAG_") && tag {
		return true
	}
	_, ok := fixedDictTypes[value]
	return ok
}
func allowedConfigKey(key string) bool {
	switch key {
	case "PLATFORM_SERVICE_PHONE", "PLATFORM_SERVICE_HOURS", "PLATFORM_SERVICE_DESCRIPTION", "PLATFORM_RETURN_ADDRESS", "AUTH_ORDER_PAYMENT_TIMEOUT_MINUTES", "AFTER_SALE_PROTECTION_DAYS", "INTEGRATION_SMS_ENABLED", "INTEGRATION_LOGISTICS_ENABLED", "INTEGRATION_IDENTITY_ENABLED", "INTEGRATION_CONTENT_SAFETY_ENABLED", "INTEGRATION_EVIDENCE_ENABLED":
		return true
	}
	return false
}
func validJSONObject(value json.RawMessage) bool {
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}
func toAuditOperator(operator Operator) audit.Operator {
	return audit.Operator{Type: "USER", ID: operator.ID, RoleCode: operator.RoleCode, ClientIP: operator.ClientIP}
}
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var (
	errVersionConflict = fmt.Errorf("version conflict")
	errSecretConfig    = fmt.Errorf("secret config")
	errInvalidTemplate = fmt.Errorf("invalid template")
)

func foundationResult(response map[string]any, err error) (map[string]any, string, error) {
	if err == nil {
		return response, "", nil
	}
	switch {
	case err == gorm.ErrRecordNotFound:
		return nil, "RESOURCE_NOT_FOUND", nil
	case err == errVersionConflict:
		return nil, "VERSION_CONFLICT", nil
	case err == errSecretConfig, err == errInvalidTemplate:
		return nil, "VALIDATION_FORMAT_INVALID", nil
	case strings.Contains(strings.ToLower(err.Error()), "duplicate"):
		return nil, "RESOURCE_CONFLICT", nil
	default:
		return nil, "INTERNAL_ERROR", err
	}
}
