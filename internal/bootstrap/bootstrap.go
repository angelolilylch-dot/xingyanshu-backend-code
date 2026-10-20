// Package bootstrap 提供系统首次部署后的基础数据初始化能力。
package bootstrap

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/config"
)

const (
	platformOrgCode   = "PLATFORM"
	platformAdminRole = "PLATFORM_ADMIN"
	bcryptCost        = 12
)

// Result 汇总本次初始化识别到的平台组织和管理员记录。
type Result struct {
	PlatformOrgID uint64
	AdminUserID   uint64
	Created       bool
}

type org struct {
	ID        uint64    `gorm:"column:id;primaryKey"`
	Status    string    `gorm:"column:status"`
	Version   uint32    `gorm:"column:version"`
	OrgCode   string    `gorm:"column:org_code"`
	OrgName   string    `gorm:"column:org_name"`
	OrgType   string    `gorm:"column:org_type"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (org) TableName() string { return "sys_org" }

type role struct {
	ID              uint64 `gorm:"column:id;primaryKey"`
	Status          string `gorm:"column:status"`
	Version         uint32 `gorm:"column:version"`
	RoleCode        string `gorm:"column:role_code"`
	RoleName        string `gorm:"column:role_name"`
	RoleDescription string `gorm:"column:role_description"`
}

func (role) TableName() string { return "sys_role" }

type permission struct {
	ID             uint64 `gorm:"column:id;primaryKey"`
	Status         string `gorm:"column:status"`
	Version        uint32 `gorm:"column:version"`
	PermissionCode string `gorm:"column:permission_code"`
	PermissionName string `gorm:"column:permission_name"`
	PermissionType string `gorm:"column:permission_type"`
	ResourceCode   string `gorm:"column:resource_code"`
}

func (permission) TableName() string { return "sys_permission" }

type user struct {
	ID           uint64 `gorm:"column:id;primaryKey"`
	Status       string `gorm:"column:status"`
	Version      uint32 `gorm:"column:version"`
	Username     string `gorm:"column:username"`
	PasswordHash string `gorm:"column:password_hash"`
	DisplayName  string `gorm:"column:display_name"`
	OrgID        uint64 `gorm:"column:org_id"`
}

func (user) TableName() string { return "sys_user" }

type userRole struct {
	ID        uint64 `gorm:"column:id;primaryKey"`
	UserID    uint64 `gorm:"column:user_id"`
	RoleID    uint64 `gorm:"column:role_id"`
	IsPrimary bool   `gorm:"column:is_primary"`
}

func (userRole) TableName() string { return "sys_user_role" }

type rolePermission struct {
	ID           uint64 `gorm:"column:id;primaryKey"`
	RoleID       uint64 `gorm:"column:role_id"`
	PermissionID uint64 `gorm:"column:permission_id"`
}

func (rolePermission) TableName() string { return "sys_role_permission" }

type systemConfig struct {
	ID          uint64 `gorm:"column:id;primaryKey"`
	Status      string `gorm:"column:status"`
	Version     uint32 `gorm:"column:version"`
	ConfigKey   string `gorm:"column:config_key"`
	ConfigValue string `gorm:"column:config_value"`
	ValueType   string `gorm:"column:value_type"`
	IsSecret    bool   `gorm:"column:is_secret"`
}

func (systemConfig) TableName() string { return "sys_config" }

type dictionaryItem struct {
	ID       uint64 `gorm:"column:id;primaryKey"`
	Status   string `gorm:"column:status"`
	Version  uint32 `gorm:"column:version"`
	DictType string `gorm:"column:dict_type"`
	DictCode string `gorm:"column:dict_code"`
	DictName string `gorm:"column:dict_name"`
	SortNo   int    `gorm:"column:sort_no"`
	Enabled  bool   `gorm:"column:enabled"`
}

func (dictionaryItem) TableName() string { return "sys_dict_item" }

type roleSeed struct {
	Code        string
	Name        string
	Description string
}

type permissionSeed struct {
	Code           string
	Name           string
	PermissionType string
	ResourceCode   string
	RoleCodes      []string
}

type configSeed struct {
	Key       string
	Value     string
	ValueType string
}

type dictionarySeed struct {
	Type string
	Code string
	Name string
	Sort int
}

var roles = []roleSeed{
	{platformAdminRole, "平台管理员", "平台组织、账号权限、配置与异常处置管理"},
	{"GROUP_MANAGER", "集团管理层", "全平台经营与风险数据只读"},
	{"CITY_OPERATOR", "城市运营", "本城市 IP 录入、材料提交与查询"},
	{"COPYRIGHT_COMPLIANCE", "版权/合规", "IP 与作品版权审核、合规复核和确权"},
	{"CREATOR", "创作者", "购买授权并创建作品、商品和活动"},
	{"FINANCE", "财务", "退款金额复核、分账与结算处理"},
	{"FULFILLMENT_CUSTOMER_SERVICE", "履约客服", "商品履约、活动核销和售后处理"},
}

// permissions 先写入职责级权限码，后续 B1 鉴权模块以这些编码加载接口权限。
var permissions = []permissionSeed{
	{"ADMIN_ACCOUNT_MANAGE", "后台账号管理", "ACTION", "A03", []string{platformAdminRole}},
	{"ADMIN_ORG_MANAGE", "组织管理", "ACTION", "A09", []string{platformAdminRole}},
	{"ADMIN_ROLE_MANAGE", "角色权限配置", "ACTION", "A04", []string{platformAdminRole}},
	{"ADMIN_CONFIG_MANAGE", "平台基础配置", "ACTION", "A05", []string{platformAdminRole}},
	{"AUTH_PLAN_MANAGE", "授权与分账方案管理", "ACTION", "B05", []string{platformAdminRole}},
	{"AUTH_PLAN_REVIEW", "授权方案合规审核", "ACTION", "B05", []string{"COPYRIGHT_COMPLIANCE"}},
	{"ADMIN_EXCEPTION_MANAGE", "平台异常处置", "ACTION", "ADMIN_EXCEPTION", []string{platformAdminRole}},
	{"DASHBOARD_VIEW_ALL", "全平台经营看板", "MENU", "E03", []string{"GROUP_MANAGER"}},
	{"MENU_PLATFORM_ACCOUNTS", "后台账号管理", "MENU", "A03", []string{platformAdminRole}},
	{"MENU_PLATFORM_ROLES", "角色与权限配置", "MENU", "A04", []string{platformAdminRole}},
	{"MENU_PLATFORM_CONFIG", "平台基础配置", "MENU", "A05", []string{platformAdminRole}},
	{"MENU_PLATFORM_TEMPLATES", "取消退款模板", "MENU", "A06", []string{platformAdminRole}},
	{"MENU_PLATFORM_DETECTIONS", "敏感检测记录", "MENU", "A07", []string{platformAdminRole}},
	{"MENU_PLATFORM_AUDIT", "文件、存证与审计", "MENU", "A08", []string{platformAdminRole}},
	{"MENU_PLATFORM_ORGS", "组织管理", "MENU", "A09", []string{platformAdminRole}},
	{"MENU_AUTH_PLANS", "授权方案管理", "MENU", "B04", []string{platformAdminRole}},
	{"MENU_DISTRIBUTION_TEMPLATES", "分账结构模板", "MENU", "B05", []string{platformAdminRole}},
	{"MENU_AUTH_PLAN_REVIEW", "授权方案合规审核", "MENU", "B04", []string{"COPYRIGHT_COMPLIANCE"}},
	{"MENU_CITY_IP", "城市 IP 管理", "MENU", "B01", []string{"CITY_OPERATOR"}},
	{"MENU_COMPLIANCE_IP", "IP 版权合规审核", "MENU", "B03", []string{"COPYRIGHT_COMPLIANCE"}},
	// 创作者端仅初始化已完成的 C01/C02 菜单；后续业务完成后再随对应接口开放菜单。
	{"MENU_CREATOR_WORKSPACE", "创作者工作台", "MENU", "C01", []string{"CREATOR"}},
	{"MENU_CREATOR_ONBOARDING", "注册/实名认证", "MENU", "C01", []string{"CREATOR"}},
	{"MENU_CREATOR_IP_MALL", "IP 商城", "MENU", "C02", []string{"CREATOR"}},
	{"MENU_FINANCE_SETTLEMENT", "分账结算管理", "MENU", "E01", []string{"FINANCE"}},
	{"MENU_FULFILLMENT", "履约与售后管理", "MENU", "D01", []string{"FULFILLMENT_CUSTOMER_SERVICE"}},
	{"IP_CREATE_CITY", "本城市 IP 录入", "ACTION", "B02", []string{"CITY_OPERATOR"}},
	{"IP_REVIEW_COMPLIANCE", "IP 版权合规审核", "ACTION", "B03", []string{"COPYRIGHT_COMPLIANCE"}},
	{"WORK_REVIEW_COMPLIANCE", "作品版权合规审核", "ACTION", "C05", []string{"COPYRIGHT_COMPLIANCE"}},
}

// configs 仅写入一期已明确的初始值；客服电话、退货地址和退款参数由平台管理员后续维护。
var configs = []configSeed{
	{"PLATFORM_SERVICE_PHONE", "", "STRING"},
	{"PLATFORM_SERVICE_HOURS", "", "STRING"},
	{"PLATFORM_SERVICE_DESCRIPTION", "", "STRING"},
	{"PLATFORM_RETURN_ADDRESS", "{}", "JSON"},
	{"AUTH_ORDER_PAYMENT_TIMEOUT_MINUTES", "30", "INTEGER"},
	{"AFTER_SALE_PROTECTION_DAYS", "0", "INTEGER"},
	{"INTEGRATION_SMS_ENABLED", "false", "BOOLEAN"},
	{"INTEGRATION_LOGISTICS_ENABLED", "false", "BOOLEAN"},
	{"INTEGRATION_IDENTITY_ENABLED", "false", "BOOLEAN"},
	{"INTEGRATION_CONTENT_SAFETY_ENABLED", "false", "BOOLEAN"},
	{"INTEGRATION_EVIDENCE_ENABLED", "false", "BOOLEAN"},
}

// fixedDictionaries 是一期页面展示和业务校验共用的只读枚举。地区与标签属于可维护主数据，不在这里写死。
var fixedDictionaries = []dictionarySeed{
	{"DATA_SCOPE", "ALL", "全平台", 1}, {"DATA_SCOPE", "CITY", "本城市", 2}, {"DATA_SCOPE", "ORG", "本组织", 3}, {"DATA_SCOPE", "SELF", "仅本人", 4},
	{"ORG_TYPE", "GROUP", "集团", 1}, {"ORG_TYPE", "PLATFORM", "平台", 2}, {"ORG_TYPE", "CITY", "城市", 3},
	{"IP_TYPE", "CHARACTER", "人物形象", 1}, {"IP_TYPE", "HISTORY_CULTURE", "历史文化", 2}, {"IP_TYPE", "INTANGIBLE_HERITAGE", "非遗技艺", 3}, {"IP_TYPE", "LOCAL_SPECIALTY", "地方特产", 4}, {"IP_TYPE", "SCENIC_LANDMARK", "景区/地标", 5}, {"IP_TYPE", "MUSEUM_RESOURCE", "文博资源", 6}, {"IP_TYPE", "FESTIVAL_EVENT", "节庆活动", 7}, {"IP_TYPE", "OTHER_CULTURAL_RESOURCE", "其他文化资源", 8},
	{"RIGHT_HOLDER_TYPE", "PERSONAL", "个人", 1}, {"RIGHT_HOLDER_TYPE", "ENTERPRISE", "企业", 2}, {"RIGHT_HOLDER_TYPE", "INSTITUTION", "机构", 3}, {"RIGHT_HOLDER_TYPE", "COLLECTIVE_ORGANIZATION", "集体组织", 4},
	{"CREATOR_SUBJECT_TYPE", "PERSONAL", "个人", 1}, {"CREATOR_SUBJECT_TYPE", "ENTERPRISE", "企业", 2}, {"CREATOR_SUBJECT_TYPE", "STUDIO", "工作室", 3},
	{"CREATOR_IDENTITY_STATUS", "UNVERIFIED", "未认证", 1}, {"CREATOR_IDENTITY_STATUS", "VERIFYING", "认证中", 2}, {"CREATOR_IDENTITY_STATUS", "VERIFIED", "已认证", 3},
	{"AUTHORIZATION_MODE", "NORMAL_SHARE", "普通分账授权", 1}, {"AUTHORIZATION_MODE", "EXCLUSIVE_BUYOUT", "独家买断授权", 2},
	{"AUTH_PLAN_TERM_TYPE", "MONTH", "按月授权", 1}, {"AUTH_PLAN_TERM_TYPE", "YEAR", "按年授权", 2}, {"AUTH_PLAN_TERM_TYPE", "DATE_RANGE", "指定起止日期", 3},
	{"AUTH_PLAN_REGION_MODE", "NATIONWIDE", "全国", 1}, {"AUTH_PLAN_REGION_MODE", "DESIGNATED", "指定地区", 2},
	{"AUTH_PLAN_SCENE", "PRODUCT_SALE", "商品销售", 1}, {"AUTH_PLAN_SCENE", "ACTIVITY", "文旅活动", 2}, {"AUTH_PLAN_SCENE", "PROMOTION", "宣传推广", 3}, {"AUTH_PLAN_SCENE", "ONLINE_CONTENT", "线上内容", 4}, {"AUTH_PLAN_SCENE", "OFFLINE_MATERIAL", "线下物料", 5},
	{"CONTENT_TYPE", "PRODUCT_ONLY", "仅商品", 1}, {"CONTENT_TYPE", "ACTIVITY_ONLY", "仅文旅活动", 2}, {"CONTENT_TYPE", "PRODUCT_AND_ACTIVITY", "商品和文旅活动", 3},
	{"APPLICABLE_PRODUCT_TYPE", "CULTURAL_CREATIVE", "文创纪念品", 1}, {"APPLICABLE_PRODUCT_TYPE", "FASHION_ACCESSORY", "服饰配件", 2}, {"APPLICABLE_PRODUCT_TYPE", "HOME_LIVING", "家居生活", 3}, {"APPLICABLE_PRODUCT_TYPE", "FOOD_GIFT", "食品礼盒", 4}, {"APPLICABLE_PRODUCT_TYPE", "DIGITAL_DERIVATIVE", "数字内容衍生品", 5}, {"APPLICABLE_PRODUCT_TYPE", "OTHER", "其他", 6},
	{"APPLICABLE_ACTIVITY_TYPE", "EXHIBITION", "展览展示", 1}, {"APPLICABLE_ACTIVITY_TYPE", "STUDY_TOUR", "研学体验", 2}, {"APPLICABLE_ACTIVITY_TYPE", "PERFORMANCE", "演艺活动", 3}, {"APPLICABLE_ACTIVITY_TYPE", "THEMED_MARKET", "主题市集", 4}, {"APPLICABLE_ACTIVITY_TYPE", "FESTIVAL", "节庆活动", 5}, {"APPLICABLE_ACTIVITY_TYPE", "OTHER", "其他", 6},
	{"PRODUCT_STATUS", "SUBMITTING", "提交中", 1}, {"PRODUCT_STATUS", "AUTO_CHECKING", "自动校验中", 2}, {"PRODUCT_STATUS", "PUBLISHED", "已发布", 3}, {"PRODUCT_STATUS", "CHECK_REJECTED", "校验不通过", 4}, {"PRODUCT_STATUS", "CHECK_ERROR", "校验异常", 5}, {"PRODUCT_STATUS", "DELISTED", "已下架", 6},
	{"ACTIVITY_STATUS", "SUBMITTING", "提交中", 1}, {"ACTIVITY_STATUS", "AUTO_CHECKING", "自动校验中", 2}, {"ACTIVITY_STATUS", "PUBLISHED", "报名中", 3}, {"ACTIVITY_STATUS", "CHECK_REJECTED", "校验不通过", 4}, {"ACTIVITY_STATUS", "CHECK_ERROR", "校验异常", 5}, {"ACTIVITY_STATUS", "BOOKING_PAUSED", "暂停报名", 6}, {"ACTIVITY_STATUS", "DELISTED", "已下架", 7}, {"ACTIVITY_STATUS", "ENDED", "已结束", 8},
	{"PAYMENT_STATUS", "PENDING", "待支付", 1}, {"PAYMENT_STATUS", "SUCCESS", "支付成功", 2}, {"PAYMENT_STATUS", "FAILED", "支付失败", 3}, {"PAYMENT_STATUS", "CLOSED", "已关闭", 4}, {"PAYMENT_STATUS", "REFUNDED", "已退款", 5},
	{"EVIDENCE_STATUS", "PENDING_HASH", "待生成摘要", 1}, {"EVIDENCE_STATUS", "PENDING_CHAIN", "待上链", 2}, {"EVIDENCE_STATUS", "CHAINING", "上链中", 3}, {"EVIDENCE_STATUS", "SUCCESS", "上链成功", 4}, {"EVIDENCE_STATUS", "FAILED", "上链失败", 5},
	{"CONTENT_DETECTION_STATUS", "PENDING", "待检测", 1}, {"CONTENT_DETECTION_STATUS", "CHECKING", "检测中", 2}, {"CONTENT_DETECTION_STATUS", "PASSED", "通过", 3}, {"CONTENT_DETECTION_STATUS", "SUSPECTED", "疑似风险", 4}, {"CONTENT_DETECTION_STATUS", "REJECTED", "不通过", 5}, {"CONTENT_DETECTION_STATUS", "ERROR", "异常", 6},
	{"FULFILLMENT_MODE", "CREATOR_SHIP", "创作者自行发货", 1}, {"FULFILLMENT_MODE", "PLATFORM_SHIP", "平台统一发货", 2},
	{"TICKET_FEE_TYPE", "FREE", "免费", 1}, {"TICKET_FEE_TYPE", "PAID", "收费", 2},
	{"ACTIVITY_TICKET_TYPE", "ADULT", "成人票", 1}, {"ACTIVITY_TICKET_TYPE", "CHILD", "儿童票", 2}, {"ACTIVITY_TICKET_TYPE", "STUDENT", "学生票", 3}, {"ACTIVITY_TICKET_TYPE", "SENIOR", "长者票", 4}, {"ACTIVITY_TICKET_TYPE", "FAMILY", "家庭票", 5}, {"ACTIVITY_TICKET_TYPE", "GROUP", "团体票", 6}, {"ACTIVITY_TICKET_TYPE", "EARLY_BIRD", "早鸟票", 7}, {"ACTIVITY_TICKET_TYPE", "OTHER", "其他", 99},
	{"FINANCE_SHARE_TYPE", "NORMAL_SHARE", "普通授权分账", 1}, {"FINANCE_SHARE_TYPE", "BUYOUT_FEE", "独家买断费用分配", 2}, {"FINANCE_SHARE_TYPE", "CREATOR_OWNED", "创作者自有收入", 3},
	{"DCI_CERTIFICATE_STATUS", "MISSING", "未上传", 1}, {"DCI_CERTIFICATE_STATUS", "ASSISTANCE_REQUESTED", "已申请代办", 2}, {"DCI_CERTIFICATE_STATUS", "UPLOADED", "已上传待核验", 3}, {"DCI_CERTIFICATE_STATUS", "VERIFIED", "已核验", 4}, {"DCI_CERTIFICATE_STATUS", "REJECTED", "核验不通过", 5},
	{"DCI_ASSISTANCE_STATUS", "PENDING", "待处理", 1}, {"DCI_ASSISTANCE_STATUS", "CONTACTED", "已联系", 2}, {"DCI_ASSISTANCE_STATUS", "PROCESSING", "办理中", 3}, {"DCI_ASSISTANCE_STATUS", "COMPLETED", "已完成", 4}, {"DCI_ASSISTANCE_STATUS", "CLOSED", "已关闭", 5},
	{"SALE_REGION_SCOPE", "NATIONWIDE", "全国", 1}, {"SALE_REGION_SCOPE", "DESIGNATED", "指定地区", 2},
	{"FREIGHT_MODE", "FIXED", "固定运费", 1}, {"FREIGHT_MODE", "FREE", "免运费", 2},
	{"PRODUCT_AFTER_SALE_TYPE", "CANCEL_REFUND", "取消退款", 1}, {"PRODUCT_AFTER_SALE_TYPE", "RETURN_REFUND", "退货退款", 2},
	{"RETURN_FEE_BEARER", "CREATOR_PLATFORM", "创作者/平台承担", 1}, {"RETURN_FEE_BEARER", "CONSUMER", "消费者承担", 2},
	{"ACTIVITY_AFTER_SALE_TYPE", "CANCEL_REFUND", "取消退款", 1},
	{"REVIEW_RESULT", "PASS", "通过", 1}, {"REVIEW_RESULT", "RETURN", "退回补充", 2}, {"REVIEW_RESULT", "REJECT", "驳回", 3},
	{"AUDIT_RESULT", "SUCCESS", "成功", 1}, {"AUDIT_RESULT", "FAILED", "失败", 2}, {"AUDIT_RESULT", "REJECTED", "拒绝", 3}, {"AUDIT_RESULT", "ERROR", "异常", 4},
}

// Run 在一个事务内创建或复用基础数据。业务编码和账号名是幂等键，因此可安全重复执行。
func Run(ctx context.Context, db *gorm.DB, cfg config.BootstrapConfig) (Result, error) {
	if len(cfg.InitialAdmin.Password) > 72 {
		return Result{}, fmt.Errorf("bootstrap initial admin password exceeds bcrypt 72-byte limit")
	}

	result := Result{}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		platform, created, err := ensurePlatformOrg(tx, cfg.PlatformOrgName)
		if err != nil {
			return err
		}
		result.PlatformOrgID = platform.ID
		result.Created = result.Created || created

		roleIDs, created, err := ensureRoles(tx)
		if err != nil {
			return err
		}
		result.Created = result.Created || created
		permissionIDs, created, err := ensurePermissions(tx)
		if err != nil {
			return err
		}
		result.Created = result.Created || created
		created, err = ensureRolePermissions(tx, roleIDs, permissionIDs)
		if err != nil {
			return err
		}
		result.Created = result.Created || created
		created, err = ensureSystemConfigs(tx)
		if err != nil {
			return err
		}
		result.Created = result.Created || created
		created, err = ensureFixedDictionaries(tx)
		if err != nil {
			return err
		}
		result.Created = result.Created || created

		admin, created, err := ensureInitialAdmin(tx, platform.ID, roleIDs[platformAdminRole], cfg)
		if err != nil {
			return err
		}
		result.AdminUserID = admin.ID
		result.Created = result.Created || created
		return nil
	})
	return result, err
}

// ensureSystemConfigs 写入可由平台管理员维护的基础配置；已存在记录绝不覆盖人工配置。
func ensureSystemConfigs(tx *gorm.DB) (bool, error) {
	created := false
	nextID, err := nextSeedID(tx, "sys_config", 10600)
	if err != nil {
		return false, err
	}
	for _, seed := range configs {
		var item systemConfig
		err := tx.Where("config_key = ?", seed.Key).First(&item).Error
		if err == nil {
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return false, err
		}
		item = systemConfig{ID: nextID, Status: "ACTIVE", Version: 1, ConfigKey: seed.Key, ConfigValue: seed.Value, ValueType: seed.ValueType, IsSecret: false}
		if err := tx.Create(&item).Error; err != nil {
			return false, err
		}
		nextID++
		created = true
	}
	return created, nil
}

// ensureFixedDictionaries 只按“类型 + 编码”补齐固定值，不覆盖人工维护的地区、标签或已存在字典名称。
func ensureFixedDictionaries(tx *gorm.DB) (bool, error) {
	created := false
	nextID, err := nextSeedID(tx, "sys_dict_item", 10700)
	if err != nil {
		return false, err
	}
	for _, seed := range fixedDictionaries {
		var item dictionaryItem
		err := tx.Where("dict_type = ? AND dict_code = ?", seed.Type, seed.Code).First(&item).Error
		if err == nil {
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return false, err
		}
		item = dictionaryItem{ID: nextID, Status: "ACTIVE", Version: 1, DictType: seed.Type, DictCode: seed.Code, DictName: seed.Name, SortNo: seed.Sort, Enabled: true}
		if err := tx.Create(&item).Error; err != nil {
			return false, err
		}
		nextID++
		created = true
	}
	return created, nil
}

// ensurePlatformOrg 用组织编码识别唯一的平台根组织。
func ensurePlatformOrg(tx *gorm.DB, orgName string) (org, bool, error) {
	var existing org
	err := tx.Where("org_code = ?", platformOrgCode).First(&existing).Error
	if err == nil {
		return existing, false, nil
	}
	if err != nil && err != gorm.ErrRecordNotFound {
		return org{}, false, err
	}
	item := org{ID: 10001, Status: "ACTIVE", Version: 1, OrgCode: platformOrgCode, OrgName: orgName, OrgType: "PLATFORM"}
	if err := tx.Create(&item).Error; err != nil {
		return org{}, false, err
	}
	return item, true, nil
}

// ensureRoles 写入一期固定的七类后台角色，并返回其数据库主键。
func ensureRoles(tx *gorm.DB) (map[string]uint64, bool, error) {
	ids := make(map[string]uint64, len(roles))
	created := false
	for index, seed := range roles {
		var item role
		err := tx.Where("role_code = ?", seed.Code).First(&item).Error
		if err == nil {
			ids[seed.Code] = item.ID
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return nil, false, err
		}
		item = role{ID: uint64(10101 + index), Status: "ACTIVE", Version: 1, RoleCode: seed.Code, RoleName: seed.Name, RoleDescription: seed.Description}
		if err := tx.Create(&item).Error; err != nil {
			return nil, false, err
		}
		ids[seed.Code] = item.ID
		created = true
	}
	return ids, created, nil
}

// ensurePermissions 写入职责级权限码，并返回其数据库主键。
func ensurePermissions(tx *gorm.DB) (map[string]uint64, bool, error) {
	ids := make(map[string]uint64, len(permissions))
	created := false
	nextID, err := nextSeedID(tx, "sys_permission", 10200)
	if err != nil {
		return nil, false, err
	}
	for _, seed := range permissions {
		var item permission
		err := tx.Where("permission_code = ?", seed.Code).First(&item).Error
		if err == nil {
			ids[seed.Code] = item.ID
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return nil, false, err
		}
		item = permission{ID: nextID, Status: "ACTIVE", Version: 1, PermissionCode: seed.Code, PermissionName: seed.Name, PermissionType: seed.PermissionType, ResourceCode: seed.ResourceCode}
		if err := tx.Create(&item).Error; err != nil {
			return nil, false, err
		}
		ids[seed.Code] = item.ID
		nextID++
		created = true
	}
	return ids, created, nil
}

// ensureRolePermissions 为每个预置权限写入职责角色关联，冲突时保留原记录。
func ensureRolePermissions(tx *gorm.DB, roleIDs, permissionIDs map[string]uint64) (bool, error) {
	created := false
	nextID, err := nextSeedID(tx, "sys_role_permission", 10300)
	if err != nil {
		return false, err
	}
	for _, seed := range permissions {
		for _, roleCode := range seed.RoleCodes {
			item := rolePermission{ID: nextID, RoleID: roleIDs[roleCode], PermissionID: permissionIDs[seed.Code]}
			result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "role_id"}, {Name: "permission_id"}}, DoNothing: true}).Create(&item)
			if result.Error != nil {
				return false, result.Error
			}
			created = created || result.RowsAffected > 0
			if result.RowsAffected > 0 {
				nextID++
			}
		}
	}
	return created, nil
}

// nextSeedID 从当前表的最大主键继续分配，保证新增种子不会与已部署环境中的旧种子 ID 冲突。
func nextSeedID(tx *gorm.DB, table string, floor uint64) (uint64, error) {
	var maxID uint64
	if err := tx.Table(table).Select("COALESCE(MAX(id), ?)", floor).Scan(&maxID).Error; err != nil {
		return 0, err
	}
	if maxID < floor {
		maxID = floor
	}
	return maxID + 1, nil
}

// ensureInitialAdmin 创建首个平台管理员；账号已存在时不覆盖其密码或个人资料。
func ensureInitialAdmin(tx *gorm.DB, orgID, roleID uint64, cfg config.BootstrapConfig) (user, bool, error) {
	var item user
	err := tx.Where("username = ?", cfg.InitialAdmin.Username).First(&item).Error
	created := false
	if err == gorm.ErrRecordNotFound {
		// 成本因子与技术规范保持一致，避免测试初始化账号弱于正式登录账号。
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(cfg.InitialAdmin.Password), bcryptCost)
		if hashErr != nil {
			return user{}, false, fmt.Errorf("hash initial admin password: %w", hashErr)
		}
		item = user{ID: 10401, Status: "ACTIVE", Version: 1, Username: cfg.InitialAdmin.Username, PasswordHash: string(hash), DisplayName: cfg.InitialAdmin.DisplayName, OrgID: orgID}
		if err := tx.Create(&item).Error; err != nil {
			return user{}, false, err
		}
		created = true
	} else if err != nil {
		return user{}, false, err
	} else if needsPasswordCostUpgrade(item.PasswordHash, cfg.InitialAdmin.Password) {
		// 仅当配置密码仍与现有管理员匹配时升级哈希成本，避免重置人工改过的密码。
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(cfg.InitialAdmin.Password), bcryptCost)
		if hashErr != nil {
			return user{}, false, fmt.Errorf("upgrade initial admin password hash: %w", hashErr)
		}
		if err := tx.Model(&item).Update("password_hash", string(hash)).Error; err != nil {
			return user{}, false, err
		}
	}

	link := userRole{ID: 10501, UserID: item.ID, RoleID: roleID, IsPrimary: true}
	result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "role_id"}}, DoNothing: true}).Create(&link)
	if result.Error != nil {
		return user{}, false, result.Error
	}
	return item, created || result.RowsAffected > 0, nil
}

// needsPasswordCostUpgrade 判断现有哈希是否可由初始化密码验证且低于当前成本要求。
func needsPasswordCostUpgrade(hash, password string) bool {
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil || cost >= bcryptCost {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
