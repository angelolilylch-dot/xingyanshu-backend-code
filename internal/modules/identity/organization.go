package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/platform/idgen"
)

const adminOrgPermission = "ADMIN_ORG_MANAGE"

var orgCodePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,62}[A-Z0-9]$`)

// OrgCreateRequest 是平台管理员创建集团、平台或城市组织的请求参数。
type OrgCreateRequest struct {
	OrgCode  string `json:"orgCode" binding:"required,min=2,max=64"`
	OrgName  string `json:"orgName" binding:"required,max=128"`
	OrgType  string `json:"orgType" binding:"required,oneof=GROUP PLATFORM CITY"`
	ParentID string `json:"parentId" binding:"omitempty,max=20"`
	CityCode string `json:"cityCode" binding:"omitempty,max=32"`
}

// OrgUpdateRequest 仅允许更新组织名称、上级组织和城市编码，组织编码和类型保持稳定。
type OrgUpdateRequest struct {
	OrgName  string `json:"orgName" binding:"required,max=128"`
	ParentID string `json:"parentId" binding:"omitempty,max=20"`
	CityCode string `json:"cityCode" binding:"omitempty,max=32"`
}

// OrgStatusRequest 是启用或停用组织的请求参数。
type OrgStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=ACTIVE SUSPENDED"`
	Reason string `json:"reason" binding:"required,max=500"`
}

// OrganizationService 负责组织层级、城市归属和账号组织下拉数据。
type OrganizationService struct {
	db *gorm.DB
}

func NewOrganizationService(db *gorm.DB) *OrganizationService {
	return &OrganizationService{db: db}
}

// Create 在事务中创建组织并记录操作审计，城市组织必须拥有上级和城市编码。
func (s *OrganizationService) Create(ctx context.Context, operator CurrentAdmin, request OrgCreateRequest, clientIP string) (map[string]any, string, error) {
	item, parentID, code, err := buildNewOrg(request)
	if err != nil {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	item.ID = idgen.Next()
	item.OrgCode = code
	var response map[string]any
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateOrgParent(tx, item.OrgType, parentID, 0); err != nil {
			return err
		}
		item.ParentID = parentID
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		response = orgResponse(item, nil)
		return writeOrgAudit(tx, operator, "ORG_CREATED", item.ID, nil, response, clientIP)
	})
	return organizationServiceResult(response, err)
}

// Get 返回组织详情及其上级组织摘要。
func (s *OrganizationService) Get(ctx context.Context, orgID uint64) (map[string]any, string, error) {
	item, parent, err := s.find(ctx, orgID)
	if err != nil {
		return nil, organizationErrorCode(err), nil
	}
	return orgResponse(item, parent), "", nil
}

// List 分页查询组织，提供账号筛选和组织管理列表使用。
func (s *OrganizationService) List(ctx context.Context, keyword, orgType, status string, pageNo, pageSize int) (map[string]any, error) {
	query := s.db.WithContext(ctx).Table("sys_org AS o").
		Select("o.id, o.status, o.version, o.org_code, o.org_name, o.org_type, o.parent_id, o.city_code, o.created_at, o.updated_at, p.org_name AS parent_name").
		Joins("LEFT JOIN sys_org AS p ON p.id = o.parent_id")
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		query = query.Where("o.org_code LIKE ? OR o.org_name LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	if orgType != "" {
		query = query.Where("o.org_type = ?", orgType)
	}
	if status != "" {
		query = query.Where("o.status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var rows []orgRow
	if err := query.Order("o.org_type ASC, o.org_name ASC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, orgResponse(row.organization(), row.parent()))
	}
	return map[string]any{"items": items, "pageNo": pageNo, "pageSize": pageSize, "total": total, "totalPages": int((total + int64(pageSize) - 1) / int64(pageSize))}, nil
}

// Options 只返回已启用组织的必要字段，供账号管理表单安全选择 orgId。
func (s *OrganizationService) Options(ctx context.Context) (map[string]any, error) {
	var rows []orgRow
	if err := s.db.WithContext(ctx).Table("sys_org AS o").
		Select("o.id, o.status, o.version, o.org_code, o.org_name, o.org_type, o.parent_id, o.city_code, o.created_at, o.updated_at, p.org_name AS parent_name").
		Joins("LEFT JOIN sys_org AS p ON p.id = o.parent_id").
		Where("o.status = ?", "ACTIVE").Order("o.org_type ASC, o.org_name ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		org := row.organization()
		item := map[string]any{"id": fmt.Sprint(org.ID), "orgName": org.OrgName, "orgType": org.OrgType, "cityCode": stringValue(org.CityCode)}
		if org.ParentID != nil {
			item["parentId"] = fmt.Sprint(*org.ParentID)
		}
		items = append(items, item)
	}
	return map[string]any{"items": items}, nil
}

// Update 使用乐观锁更新组织允许变更的资料，并阻止把组织挂到自身或下级组织下。
func (s *OrganizationService) Update(ctx context.Context, operator CurrentAdmin, orgID uint64, version uint32, request OrgUpdateRequest, clientIP string) (map[string]any, string, error) {
	orgName := strings.TrimSpace(request.OrgName)
	parentID, err := parseOptionalID(request.ParentID)
	if orgName == "" || err != nil {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var response map[string]any
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before managedOrg
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", orgID).First(&before).Error; err != nil {
			return err
		}
		if before.Version != version {
			return errVersionConflict
		}
		cityCode := strings.TrimSpace(request.CityCode)
		if before.OrgType == "CITY" && (parentID == nil || cityCode == "") {
			return errInvalidOrg
		}
		if before.OrgType != "CITY" && cityCode != "" {
			return errInvalidOrg
		}
		if err := validateOrgParent(tx, before.OrgType, parentID, orgID); err != nil {
			return err
		}
		updates := map[string]any{"org_name": orgName, "parent_id": parentID, "city_code": optionalString(cityCode), "version": before.Version + 1, "updated_by": operator.UserID}
		if err := tx.Model(&managedOrg{}).Where("id = ? AND version = ?", orgID, version).Updates(updates).Error; err != nil {
			return err
		}
		after := before
		after.OrgName, after.ParentID, after.CityCode, after.Version = orgName, parentID, optionalString(cityCode), before.Version+1
		response = orgResponse(after, nil)
		return writeOrgAudit(tx, operator, "ORG_UPDATED", orgID, orgResponse(before, nil), response, clientIP)
	})
	return organizationServiceResult(response, err)
}

// ChangeStatus 停用组织前确认不存在启用账号和启用下级组织，避免破坏在用数据范围。
func (s *OrganizationService) ChangeStatus(ctx context.Context, operator CurrentAdmin, orgID uint64, version uint32, request OrgStatusRequest, clientIP string) (map[string]any, string, error) {
	var response map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before managedOrg
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", orgID).First(&before).Error; err != nil {
			return err
		}
		if before.Version != version {
			return errVersionConflict
		}
		if request.Status == "SUSPENDED" {
			var activeUserCount, activeChildCount int64
			if err := tx.Table("sys_user").Where("org_id = ? AND status = ?", orgID, "ACTIVE").Count(&activeUserCount).Error; err != nil {
				return err
			}
			if err := tx.Table("sys_org").Where("parent_id = ? AND status = ?", orgID, "ACTIVE").Count(&activeChildCount).Error; err != nil {
				return err
			}
			if activeUserCount > 0 || activeChildCount > 0 {
				return errOrgInUse
			}
		}
		updates := map[string]any{"status": request.Status, "status_reason_code": "ORG_STATUS_CHANGED", "status_reason": request.Reason, "version": before.Version + 1, "updated_by": operator.UserID, "status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(operator.UserID)}
		if err := tx.Model(&managedOrg{}).Where("id = ? AND version = ?", orgID, version).Updates(updates).Error; err != nil {
			return err
		}
		after := before
		after.Status, after.Version = request.Status, before.Version+1
		response = orgResponse(after, nil)
		return writeOrgAudit(tx, operator, "ORG_STATUS_CHANGED", orgID, orgResponse(before, nil), response, clientIP)
	})
	return organizationServiceResult(response, err)
}

type orgRow struct {
	ID         uint64    `gorm:"column:id"`
	Status     string    `gorm:"column:status"`
	Version    uint32    `gorm:"column:version"`
	OrgCode    string    `gorm:"column:org_code"`
	OrgName    string    `gorm:"column:org_name"`
	OrgType    string    `gorm:"column:org_type"`
	ParentID   *uint64   `gorm:"column:parent_id"`
	CityCode   *string   `gorm:"column:city_code"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
	ParentName string    `gorm:"column:parent_name"`
}

func (row orgRow) organization() managedOrg {
	return managedOrg{ID: row.ID, Status: row.Status, Version: row.Version, OrgCode: row.OrgCode, OrgName: row.OrgName, OrgType: row.OrgType, ParentID: row.ParentID, CityCode: row.CityCode, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func (row orgRow) parent() *managedOrg {
	if row.ParentID == nil {
		return nil
	}
	return &managedOrg{ID: *row.ParentID, OrgName: row.ParentName}
}

var (
	errInvalidOrg = fmt.Errorf("invalid organization")
	errOrgInUse   = fmt.Errorf("organization is in use")
)

func buildNewOrg(request OrgCreateRequest) (managedOrg, *uint64, string, error) {
	code := strings.ToUpper(strings.TrimSpace(request.OrgCode))
	name := strings.TrimSpace(request.OrgName)
	cityCode := strings.TrimSpace(request.CityCode)
	parentID, err := parseOptionalID(request.ParentID)
	if !orgCodePattern.MatchString(code) || name == "" || err != nil {
		return managedOrg{}, nil, "", errInvalidOrg
	}
	if request.OrgType == "CITY" && (parentID == nil || cityCode == "") {
		return managedOrg{}, nil, "", errInvalidOrg
	}
	if request.OrgType != "CITY" && cityCode != "" {
		return managedOrg{}, nil, "", errInvalidOrg
	}
	return managedOrg{Status: "ACTIVE", Version: 1, OrgName: name, OrgType: request.OrgType, CityCode: optionalString(cityCode)}, parentID, code, nil
}

func parseOptionalID(value string) (*uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil || id == 0 {
		return nil, errInvalidOrg
	}
	return &id, nil
}

func validateOrgParent(tx *gorm.DB, orgType string, parentID *uint64, currentID uint64) error {
	if orgType == "CITY" && parentID == nil {
		return errInvalidOrg
	}
	if parentID == nil {
		return nil
	}
	if currentID != 0 && *parentID == currentID {
		return errInvalidOrg
	}
	parent := *parentID
	for parent != 0 {
		var item managedOrg
		if err := tx.Where("id = ? AND status = ?", parent, "ACTIVE").First(&item).Error; err != nil {
			return err
		}
		if item.ID == currentID {
			return errInvalidOrg
		}
		if item.ParentID == nil {
			return nil
		}
		parent = *item.ParentID
	}
	return nil
}

func (s *OrganizationService) find(ctx context.Context, orgID uint64) (managedOrg, *managedOrg, error) {
	var row orgRow
	err := s.db.WithContext(ctx).Table("sys_org AS o").
		Select("o.id, o.status, o.version, o.org_code, o.org_name, o.org_type, o.parent_id, o.city_code, o.created_at, o.updated_at, p.org_name AS parent_name").
		Joins("LEFT JOIN sys_org AS p ON p.id = o.parent_id").Where("o.id = ?", orgID).Scan(&row).Error
	if err != nil {
		return managedOrg{}, nil, err
	}
	if row.ID == 0 {
		return managedOrg{}, nil, gorm.ErrRecordNotFound
	}
	return row.organization(), row.parent(), nil
}

func organizationServiceResult(response map[string]any, err error) (map[string]any, string, error) {
	if err == nil {
		return response, "", nil
	}
	return nil, organizationErrorCode(err), nil
}

func organizationErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case err == gorm.ErrRecordNotFound:
		return "RESOURCE_NOT_FOUND"
	case err == errVersionConflict:
		return "VERSION_CONFLICT"
	case err == errInvalidOrg:
		return "VALIDATION_FORMAT_INVALID"
	case err == errOrgInUse:
		return "RESOURCE_CONFLICT"
	case strings.Contains(strings.ToLower(err.Error()), "duplicate"):
		return "RESOURCE_CONFLICT"
	default:
		return "INTERNAL_ERROR"
	}
}

func orgResponse(org managedOrg, parent *managedOrg) map[string]any {
	result := map[string]any{"id": fmt.Sprint(org.ID), "orgCode": org.OrgCode, "orgName": org.OrgName, "orgType": org.OrgType, "cityCode": stringValue(org.CityCode), "status": org.Status, "version": org.Version, "createdAt": org.CreatedAt, "updatedAt": org.UpdatedAt}
	if parent != nil {
		result["parent"] = map[string]string{"id": fmt.Sprint(parent.ID), "name": parent.OrgName}
		result["parentId"] = fmt.Sprint(parent.ID)
	}
	return result
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func writeOrgAudit(tx *gorm.DB, operator CurrentAdmin, action string, objectID uint64, before, after map[string]any, clientIP string) error {
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	roleCode := ""
	if len(operator.RoleCodes) > 0 {
		roleCode = operator.RoleCodes[0]
	}
	return tx.Create(&accountAuditLog{ID: idgen.Next(), CreatedBy: operator.UserID, AuditNo: fmt.Sprintf("AUD-%d", idgen.Next()), OperatorID: operator.UserID, OperatorRole: roleCode, Action: action, ObjectType: "ORG", ObjectID: objectID, BeforeSnapshot: beforeJSON, AfterSnapshot: afterJSON, Result: "SUCCESS", ClientIP: clientIP}).Error
}
