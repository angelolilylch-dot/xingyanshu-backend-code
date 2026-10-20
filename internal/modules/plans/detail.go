package plans

import (
	"context"
	"encoding/json"
	"fmt"

	"xingyanshu-backend/internal/platform/apiresponse"
)

// GetTemplate 返回模板及其参与方结构，不返回任何IP实际比例。
func (s *Service) GetTemplate(ctx context.Context, id uint64) (map[string]any, string, error) {
	var row map[string]any
	if err := s.db.WithContext(ctx).Table("finance_distribution_plan").Where("id=?", id).Take(&row).Error; err != nil {
		return nil, "RESOURCE_NOT_FOUND", nil
	}
	var items []map[string]any
	if err := s.db.WithContext(ctx).Table("finance_distribution_plan_item").Where("distribution_plan_id=?", id).Find(&items).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	sortParticipantItems(items)
	result := apiresponse.CamelizeMap(row)
	result["items"] = apiresponse.CamelizeMaps(items)
	return result, "", nil
}

// GetAuthPlan 返回授权范围与当前版本，供城市运营选择及管理员编辑回显。
func (s *Service) GetAuthPlan(ctx context.Context, id uint64) (map[string]any, string, error) {
	var row map[string]any
	err := s.db.WithContext(ctx).Table("auth_plan p").Select("p.*,v.authorization_mode,v.term_type,v.term_value,DATE_FORMAT(v.valid_from, '%Y-%m-%d') AS valid_from,DATE_FORMAT(v.valid_to, '%Y-%m-%d') AS valid_to,v.region_mode,v.region_codes_json,v.scenes_json,v.commercial_allowed,v.derivative_allowed,v.n_creation_allowed,v.content_type,v.quota_total").Joins("left join auth_plan_version v on v.id=p.current_version_id").Where("p.id=?", id).Take(&row).Error
	if err != nil {
		return nil, "RESOURCE_NOT_FOUND", nil
	}
	result := apiresponse.CamelizeMap(row)
	scenes, err := decodePlanStringList(result["scenesJson"])
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	result["scenes"] = scenes
	delete(result, "scenesJson")
	regionCodes, err := decodePlanStringList(result["regionCodesJson"])
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	result["regionCodes"] = regionCodes
	delete(result, "regionCodesJson")
	return result, "", nil
}

func decodePlanStringList(value any) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
	var raw []byte
	switch value := value.(type) {
	case string:
		raw = []byte(value)
	case []byte:
		raw = value
	default:
		return nil, fmt.Errorf("unexpected JSON value type %T", value)
	}
	if len(raw) == 0 {
		return []string{}, nil
	}
	var result []string
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}
