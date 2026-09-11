package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/platform/idgen"
)

const (
	adminAccountPermission    = "ADMIN_ACCOUNT_MANAGE"
	accountPasswordBcryptCost = 12
)

var errInvalidOrgAssignment = fmt.Errorf("invalid account organization")

// AdminAccountCreateRequest 是平台管理员创建后台账号的请求参数。
type AdminAccountCreateRequest struct {
	Username        string `json:"username" binding:"required,min=2,max=50"`
	InitialPassword string `json:"initialPassword" binding:"required,min=8,max=64"`
	DisplayName     string `json:"displayName" binding:"required,max=128"`
	Mobile          string `json:"mobile" binding:"omitempty,len=11,numeric"`
	OrgID           uint64 `json:"orgId,string" binding:"required"`
	RoleCode        string `json:"roleCode" binding:"required,max=64"`
	DataScope       string `json:"dataScope" binding:"required"`
}

// AdminAccountUpdateRequest 是平台管理员可修改的后台账号资料，不允许修改登录账号和初始密码。
type AdminAccountUpdateRequest struct {
	DisplayName string `json:"displayName" binding:"required,max=128"`
	Mobile      string `json:"mobile" binding:"omitempty,len=11,numeric"`
	OrgID       uint64 `json:"orgId,string" binding:"required"`
	RoleCode    string `json:"roleCode" binding:"required,max=64"`
	DataScope   string `json:"dataScope" binding:"required"`
}

// AdminAccountStatusRequest 是后台账号启用或停用的请求参数。
type AdminAccountStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=ACTIVE SUSPENDED"`
	Reason string `json:"reason" binding:"required,max=500"`
}

type managedAccount struct {
	ID                 uint64     `gorm:"column:id"`
	Username           string     `gorm:"column:username"`
	PasswordHash       string     `gorm:"column:password_hash"`
	MustChangePassword bool       `gorm:"column:must_change_password"`
	DisplayName        string     `gorm:"column:display_name"`
	Mobile             *string    `gorm:"column:mobile"`
	OrgID              uint64     `gorm:"column:org_id"`
	DataScope          string     `gorm:"column:data_scope"`
	Status             string     `gorm:"column:status"`
	Version            uint32     `gorm:"column:version"`
	LastLoginAt        *time.Time `gorm:"column:last_login_at"`
	CreatedAt          time.Time  `gorm:"column:created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at"`
}

func (managedAccount) TableName() string { return "sys_user" }

type managedRole struct {
	ID       uint64 `gorm:"column:id"`
	RoleCode string `gorm:"column:role_code"`
	RoleName string `gorm:"column:role_name"`
}

func (managedRole) TableName() string { return "sys_role" }

type managedOrg struct {
	ID        uint64    `gorm:"column:id"`
	Status    string    `gorm:"column:status"`
	Version   uint32    `gorm:"column:version"`
	OrgCode   string    `gorm:"column:org_code"`
	OrgName   string    `gorm:"column:org_name"`
	OrgType   string    `gorm:"column:org_type"`
	ParentID  *uint64   `gorm:"column:parent_id"`
	CityCode  *string   `gorm:"column:city_code"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (managedOrg) TableName() string { return "sys_org" }

type managedUserRole struct {
	ID        uint64 `gorm:"column:id"`
	UserID    uint64 `gorm:"column:user_id"`
	RoleID    uint64 `gorm:"column:role_id"`
	IsPrimary bool   `gorm:"column:is_primary"`
	CreatedBy uint64 `gorm:"column:created_by"`
}

func (managedUserRole) TableName() string { return "sys_user_role" }

type accountAuditLog struct {
	ID             uint64          `gorm:"column:id"`
	CreatedBy      uint64          `gorm:"column:created_by"`
	AuditNo        string          `gorm:"column:audit_no"`
	OperatorID     uint64          `gorm:"column:operator_id"`
	OperatorRole   string          `gorm:"column:operator_role"`
	Action         string          `gorm:"column:action"`
	ObjectType     string          `gorm:"column:object_type"`
	ObjectID       uint64          `gorm:"column:object_id"`
	BeforeSnapshot json.RawMessage `gorm:"column:before_snapshot"`
	AfterSnapshot  json.RawMessage `gorm:"column:after_snapshot"`
	Result         string          `gorm:"column:result"`
	ClientIP       string          `gorm:"column:client_ip"`
}

func (accountAuditLog) TableName() string { return "sys_audit_log" }

// AccountService 负责平台管理员对后台账号的创建、编辑、停用和查询。
type AccountService struct {
	db *gorm.DB
}

func NewAccountService(db *gorm.DB) *AccountService {
	return &AccountService{db: db}
}

// Create 在同一事务内创建账号、绑定唯一角色并写入脱敏审计记录。
func (s *AccountService) Create(ctx context.Context, operator CurrentAdmin, request AdminAccountCreateRequest, clientIP string) (map[string]any, string, error) {
	if !IsValidAdminUsername(request.Username) || !IsValidPassword(request.InitialPassword) || !validDataScope(request.DataScope) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(request.InitialPassword), accountPasswordBcryptCost)
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	account := managedAccount{ID: idgen.Next(), Username: request.Username, PasswordHash: string(passwordHash), MustChangePassword: true, DisplayName: request.DisplayName, Mobile: optionalMobile(request.Mobile), OrgID: request.OrgID, DataScope: request.DataScope, Status: "ACTIVE", Version: 1}
	var response map[string]any
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		role, err := findActiveRole(tx, request.RoleCode)
		if err != nil {
			return err
		}
		org, err := getActiveOrg(tx, request.OrgID)
		if err != nil {
			return err
		}
		if !validAccountOrganization(role.RoleCode, request.DataScope, org) {
			return errInvalidOrgAssignment
		}
		if err := tx.Create(&account).Error; err != nil {
			return err
		}
		if err := tx.Create(&managedUserRole{ID: idgen.Next(), UserID: account.ID, RoleID: role.ID, IsPrimary: true, CreatedBy: operator.UserID}).Error; err != nil {
			return err
		}
		response = accountResponse(account, role, nil)
		return writeAccountAudit(tx, operator, "ADMIN_ACCOUNT_CREATED", account.ID, nil, response, clientIP)
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return nil, "RESOURCE_CONFLICT", nil
		}
		if err == errInvalidOrgAssignment {
			return nil, "VALIDATION_FORMAT_INVALID", nil
		}
		return nil, "INTERNAL_ERROR", err
	}
	return response, "", nil
}

// Update 使用版本号条件更新账号资料、唯一角色和数据范围，并撤销旧会话。
func (s *AccountService) Update(ctx context.Context, operator CurrentAdmin, userID uint64, version uint32, request AdminAccountUpdateRequest, clientIP string) (map[string]any, string, error) {
	if !validDataScope(request.DataScope) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	return s.updateAccount(ctx, operator, userID, version, request.DisplayName, request.Mobile, request.OrgID, request.RoleCode, request.DataScope, clientIP)
}

func (s *AccountService) updateAccount(ctx context.Context, operator CurrentAdmin, userID uint64, version uint32, displayName, mobile string, orgID uint64, roleCode, dataScope, clientIP string) (map[string]any, string, error) {
	var response map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before managedAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", userID).First(&before).Error; err != nil {
			return err
		}
		if before.Version != version {
			return errVersionConflict
		}
		role, err := findActiveRole(tx, roleCode)
		if err != nil {
			return err
		}
		org, err := getActiveOrg(tx, orgID)
		if err != nil {
			return err
		}
		if !validAccountOrganization(role.RoleCode, dataScope, org) {
			return errInvalidOrgAssignment
		}
		updates := map[string]any{"display_name": displayName, "mobile": optionalMobile(mobile), "org_id": orgID, "data_scope": dataScope, "version": before.Version + 1, "updated_by": operator.UserID}
		if err := tx.Model(&managedAccount{}).Where("id = ? AND version = ?", userID, version).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&managedUserRole{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&managedUserRole{ID: idgen.Next(), UserID: userID, RoleID: role.ID, IsPrimary: true, CreatedBy: operator.UserID}).Error; err != nil {
			return err
		}
		if err := revokeAdminSessions(tx, userID); err != nil {
			return err
		}
		after := before
		after.DisplayName, after.Mobile, after.OrgID, after.DataScope, after.Version = displayName, optionalMobile(mobile), orgID, dataScope, before.Version+1
		response = accountResponse(after, role, nil)
		return writeAccountAudit(tx, operator, "ADMIN_ACCOUNT_UPDATED", userID, accountResponse(before, managedRole{}, nil), response, clientIP)
	})
	return accountServiceResult(response, err)
}

// ChangeStatus 按乐观锁启用或停用账号；停用时撤销全部后台会话。
func (s *AccountService) ChangeStatus(ctx context.Context, operator CurrentAdmin, userID uint64, version uint32, request AdminAccountStatusRequest, clientIP string) (map[string]any, string, error) {
	var response map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before managedAccount
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", userID).First(&before).Error; err != nil {
			return err
		}
		if before.Version != version {
			return errVersionConflict
		}
		if err := tx.Model(&managedAccount{}).Where("id = ? AND version = ?", userID, version).Updates(map[string]any{"status": request.Status, "status_reason": request.Reason, "status_reason_code": "ACCOUNT_STATUS_CHANGED", "version": before.Version + 1, "updated_by": operator.UserID, "status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(operator.UserID)}).Error; err != nil {
			return err
		}
		if request.Status == "SUSPENDED" {
			if err := revokeAdminSessions(tx, userID); err != nil {
				return err
			}
		}
		after := before
		after.Status, after.Version = request.Status, before.Version+1
		response = accountResponse(after, managedRole{}, nil)
		return writeAccountAudit(tx, operator, "ADMIN_ACCOUNT_STATUS_CHANGED", userID, accountResponse(before, managedRole{}, nil), response, clientIP)
	})
	return accountServiceResult(response, err)
}

// Get 返回后台账号详情，同时加载其唯一角色和所属组织。
func (s *AccountService) Get(ctx context.Context, userID uint64) (map[string]any, string, error) {
	account, role, org, err := s.findAccount(ctx, userID)
	if err == gorm.ErrRecordNotFound {
		return nil, "RESOURCE_NOT_FOUND", nil
	}
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return accountDetailResponse(account, role, org), "", nil
}

// List 根据固定筛选条件查询后台账号，手机号仅返回脱敏结果。
func (s *AccountService) List(ctx context.Context, keyword string, orgID uint64, roleCode, status string, pageNo, pageSize int) (map[string]any, error) {
	query := s.db.WithContext(ctx).Table("sys_user AS u").
		Select("u.id, u.username, u.display_name, u.mobile, u.org_id, u.data_scope, u.status, u.version, u.last_login_at, u.created_at, u.updated_at, r.id AS role_id, r.role_code, r.role_name, o.org_name").
		Joins("LEFT JOIN sys_user_role AS ur ON ur.user_id = u.id").
		Joins("LEFT JOIN sys_role AS r ON r.id = ur.role_id").
		Joins("LEFT JOIN sys_org AS o ON o.id = u.org_id")
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		query = query.Where("u.username LIKE ? OR u.display_name LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	if orgID > 0 {
		query = query.Where("u.org_id = ?", orgID)
	}
	if roleCode != "" {
		query = query.Where("r.role_code = ?", roleCode)
	}
	if status != "" {
		query = query.Where("u.status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var rows []accountListRow
	if err := query.Order("u.created_at DESC").Offset((pageNo - 1) * pageSize).Limit(pageSize).Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		account := managedAccount{ID: row.ID, Username: row.Username, DisplayName: row.DisplayName, Mobile: row.Mobile, OrgID: row.OrgID, DataScope: row.DataScope, Status: row.Status, Version: row.Version, LastLoginAt: row.LastLoginAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
		items = append(items, accountResponse(account, managedRole{ID: row.RoleID, RoleCode: row.RoleCode, RoleName: row.RoleName}, &managedOrg{ID: row.OrgID, OrgName: row.OrgName}))
	}
	totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
	return map[string]any{"items": items, "pageNo": pageNo, "pageSize": pageSize, "total": total, "totalPages": totalPages}, nil
}

type accountListRow struct {
	ID          uint64     `gorm:"column:id"`
	Username    string     `gorm:"column:username"`
	DisplayName string     `gorm:"column:display_name"`
	Mobile      *string    `gorm:"column:mobile"`
	OrgID       uint64     `gorm:"column:org_id"`
	DataScope   string     `gorm:"column:data_scope"`
	Status      string     `gorm:"column:status"`
	Version     uint32     `gorm:"column:version"`
	LastLoginAt *time.Time `gorm:"column:last_login_at"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
	RoleID      uint64     `gorm:"column:role_id"`
	RoleCode    string     `gorm:"column:role_code"`
	RoleName    string     `gorm:"column:role_name"`
	OrgName     string     `gorm:"column:org_name"`
}

var errVersionConflict = fmt.Errorf("version conflict")

func accountServiceResult(response map[string]any, err error) (map[string]any, string, error) {
	if err == nil {
		return response, "", nil
	}
	if err == errVersionConflict {
		return nil, "VERSION_CONFLICT", nil
	}
	if err == errInvalidOrgAssignment {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	if err == gorm.ErrRecordNotFound {
		return nil, "RESOURCE_NOT_FOUND", nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		return nil, "RESOURCE_CONFLICT", nil
	}
	return nil, "INTERNAL_ERROR", err
}

func findActiveRole(tx *gorm.DB, roleCode string) (managedRole, error) {
	var role managedRole
	err := tx.Where("role_code = ? AND status = ?", roleCode, "ACTIVE").First(&role).Error
	return role, err
}

func getActiveOrg(tx *gorm.DB, orgID uint64) (managedOrg, error) {
	var org managedOrg
	err := tx.Where("id = ? AND status = ?", orgID, "ACTIVE").First(&org).Error
	return org, err
}

// validAccountOrganization 约束城市角色和本城市数据范围必须绑定具有城市编码的城市组织。
func validAccountOrganization(roleCode, dataScope string, org managedOrg) bool {
	if roleCode != "CITY_OPERATOR" && dataScope != "CITY" {
		return true
	}
	return org.OrgType == "CITY" && org.CityCode != nil && *org.CityCode != ""
}

func (s *AccountService) findAccount(ctx context.Context, userID uint64) (managedAccount, managedRole, managedOrg, error) {
	var row accountListRow
	err := s.db.WithContext(ctx).Table("sys_user AS u").
		Select("u.id, u.username, u.password_hash, u.display_name, u.mobile, u.org_id, u.data_scope, u.status, u.version, u.last_login_at, u.created_at, u.updated_at, r.id AS role_id, r.role_code, r.role_name, o.org_name").
		Joins("LEFT JOIN sys_user_role AS ur ON ur.user_id = u.id").
		Joins("LEFT JOIN sys_role AS r ON r.id = ur.role_id").
		Joins("LEFT JOIN sys_org AS o ON o.id = u.org_id").
		Where("u.id = ?", userID).Scan(&row).Error
	if err != nil {
		return managedAccount{}, managedRole{}, managedOrg{}, err
	}
	if row.ID == 0 {
		return managedAccount{}, managedRole{}, managedOrg{}, gorm.ErrRecordNotFound
	}
	return managedAccount{ID: row.ID, Username: row.Username, DisplayName: row.DisplayName, Mobile: row.Mobile, OrgID: row.OrgID, DataScope: row.DataScope, Status: row.Status, Version: row.Version, LastLoginAt: row.LastLoginAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, managedRole{ID: row.RoleID, RoleCode: row.RoleCode, RoleName: row.RoleName}, managedOrg{ID: row.OrgID, OrgName: row.OrgName}, nil
}

func revokeAdminSessions(tx *gorm.DB, userID uint64) error {
	return tx.Model(&authSession{}).Where("subject_type = ? AND subject_id = ? AND client_type = ? AND status = ?", "ADMIN", userID, "ADMIN_WEB", "ACTIVE").Updates(map[string]any{"status": "REVOKED", "last_used_at": time.Now()}).Error
}

func validDataScope(value string) bool {
	return value == "ALL" || value == "CITY" || value == "ORG" || value == "SELF"
}

func accountResponse(account managedAccount, role managedRole, org *managedOrg) map[string]any {
	result := map[string]any{"id": fmt.Sprint(account.ID), "username": account.Username, "displayName": account.DisplayName, "mobileMasked": maskMobile(account.Mobile), "orgId": fmt.Sprint(account.OrgID), "dataScope": account.DataScope, "dataScopeName": scopeName(account.DataScope), "status": account.Status, "version": account.Version, "lastLoginAt": account.LastLoginAt, "createdAt": account.CreatedAt, "updatedAt": account.UpdatedAt}
	if role.RoleCode != "" {
		result["role"] = map[string]string{"code": role.RoleCode, "name": role.RoleName}
	}
	if org != nil {
		result["org"] = map[string]string{"id": fmt.Sprint(org.ID), "name": org.OrgName}
	}
	return result
}

// accountDetailResponse 在账号详情中额外返回完整手机号，供有账号管理权限的编辑页回显。
func accountDetailResponse(account managedAccount, role managedRole, org managedOrg) map[string]any {
	result := accountResponse(account, role, &org)
	if account.Mobile == nil {
		result["mobile"] = ""
		return result
	}
	result["mobile"] = *account.Mobile
	return result
}

func optionalMobile(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func maskMobile(value *string) string {
	if value == nil || len(*value) != 11 {
		return ""
	}
	return (*value)[:3] + "****" + (*value)[7:]
}

func writeAccountAudit(tx *gorm.DB, operator CurrentAdmin, action string, objectID uint64, before, after map[string]any, clientIP string) error {
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	roleCode := ""
	if len(operator.RoleCodes) > 0 {
		roleCode = operator.RoleCodes[0]
	}
	return tx.Create(&accountAuditLog{ID: idgen.Next(), CreatedBy: operator.UserID, AuditNo: fmt.Sprintf("AUD-%d", idgen.Next()), OperatorID: operator.UserID, OperatorRole: roleCode, Action: action, ObjectType: "ADMIN_ACCOUNT", ObjectID: objectID, BeforeSnapshot: beforeJSON, AfterSnapshot: afterJSON, Result: "SUCCESS", ClientIP: clientIP}).Error
}
