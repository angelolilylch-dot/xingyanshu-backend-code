package identity

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/platform/httpx"
)

const currentAdminKey = "identity.currentAdmin"

// CurrentAdmin 保存受保护请求的当前后台用户、会话、角色、权限和数据范围。
type CurrentAdmin struct {
	UserID          uint64
	SessionID       uint64
	OrgID           uint64
	CityCode        string
	DataScope       string
	RoleCodes       []string
	PermissionCodes map[string]struct{}
}

// RequireAdminAuthentication 校验 Bearer JWT，并确认关联会话和后台账号仍处于有效状态。
func RequireAdminAuthentication(db *gorm.DB, authCfg config.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		current, err := authenticateAdmin(c, db, authCfg)
		if err != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED", "登录状态已失效"))
			return
		}
		c.Set(currentAdminKey, current)
		c.Next()
	}
}

// CurrentAdminFromContext 读取认证中间件写入的当前后台会话信息。
func CurrentAdminFromContext(c *gin.Context) (CurrentAdmin, bool) {
	value, ok := c.Get(currentAdminKey)
	if !ok {
		return CurrentAdmin{}, false
	}
	current, ok := value.(CurrentAdmin)
	return current, ok
}

func authenticateAdmin(c *gin.Context, db *gorm.DB, authCfg config.AuthConfig) (CurrentAdmin, error) {
	tokenString := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	if tokenString == "" || tokenString == c.GetHeader("Authorization") {
		return CurrentAdmin{}, fmt.Errorf("missing bearer token")
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(authCfg.JWTSecret), nil
	})
	if err != nil || !token.Valid || claims["clientType"] != "ADMIN_WEB" {
		return CurrentAdmin{}, fmt.Errorf("invalid access token")
	}
	userID, ok := parseClaimID(claims["sub"])
	if !ok {
		return CurrentAdmin{}, fmt.Errorf("invalid user claim")
	}
	sessionID, ok := parseClaimID(claims["sid"])
	if !ok {
		return CurrentAdmin{}, fmt.Errorf("invalid session claim")
	}
	tokenVersion, ok := parseClaimID(claims["tv"])
	if !ok {
		return CurrentAdmin{}, fmt.Errorf("invalid token version claim")
	}
	var session authSession
	if err := db.WithContext(c.Request.Context()).Where("id = ? AND subject_type = ? AND subject_id = ? AND client_type = ? AND status = ? AND expires_at > ?", sessionID, "ADMIN", userID, "ADMIN_WEB", "ACTIVE", time.Now()).First(&session).Error; err != nil {
		return CurrentAdmin{}, err
	}
	if uint64(session.TokenVersion) != tokenVersion {
		return CurrentAdmin{}, fmt.Errorf("stale token version")
	}
	var user adminUser
	if err := db.WithContext(c.Request.Context()).Where("id = ? AND status = ?", userID, "ACTIVE").First(&user).Error; err != nil {
		return CurrentAdmin{}, err
	}
	current := CurrentAdmin{UserID: userID, SessionID: sessionID, OrgID: user.OrgID, DataScope: user.DataScope, PermissionCodes: make(map[string]struct{})}
	if err := loadAuthorization(c, db, &current); err != nil {
		return CurrentAdmin{}, err
	}
	return current, nil
}

// HasPermission 判断当前用户是否拥有指定权限码，供路由和领域服务进行接口权限校验。
func (current CurrentAdmin) HasPermission(permissionCode string) bool {
	_, ok := current.PermissionCodes[permissionCode]
	return ok
}

// RequireAnyPermission 要求当前后台用户至少拥有一个指定权限码。
func RequireAnyPermission(permissionCodes ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		current, ok := CurrentAdminFromContext(c)
		if !ok {
			httpx.AbortWithError(c, httpx.NewError(http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED", "登录状态已失效"))
			return
		}
		for _, permissionCode := range permissionCodes {
			if current.HasPermission(permissionCode) {
				c.Next()
				return
			}
		}
		httpx.AbortWithError(c, httpx.NewError(http.StatusForbidden, "AUTH_FORBIDDEN", "无权执行该操作"))
	}
}

type authorizationRole struct {
	RoleCode string `gorm:"column:role_code"`
}

type authorizationPermission struct {
	PermissionCode string `gorm:"column:permission_code"`
}

// loadAuthorization 从角色关联表读取当前有效权限，确保角色调整后旧令牌不会保留原权限。
func loadAuthorization(c *gin.Context, db *gorm.DB, current *CurrentAdmin) error {
	var cityOrg struct {
		CityCode string `gorm:"column:city_code"`
	}
	if err := db.WithContext(c.Request.Context()).Table("sys_org").Select("city_code").Where("id = ? AND status = ?", current.OrgID, "ACTIVE").Scan(&cityOrg).Error; err != nil {
		return err
	}
	current.CityCode = cityOrg.CityCode

	var roles []authorizationRole
	if err := db.WithContext(c.Request.Context()).Table("sys_user_role AS ur").
		Select("r.role_code").
		Joins("JOIN sys_role AS r ON r.id = ur.role_id AND r.status = ?", "ACTIVE").
		Where("ur.user_id = ?", current.UserID).
		Order("ur.is_primary DESC, r.id ASC").
		Scan(&roles).Error; err != nil {
		return err
	}
	for _, role := range roles {
		current.RoleCodes = append(current.RoleCodes, role.RoleCode)
	}

	var permissions []authorizationPermission
	if err := db.WithContext(c.Request.Context()).Table("sys_user_role AS ur").
		Select("DISTINCT p.permission_code").
		Joins("JOIN sys_role_permission AS rp ON rp.role_id = ur.role_id").
		Joins("JOIN sys_permission AS p ON p.id = rp.permission_id AND p.status = ?", "ACTIVE").
		Where("ur.user_id = ?", current.UserID).
		Scan(&permissions).Error; err != nil {
		return err
	}
	for _, permission := range permissions {
		current.PermissionCodes[permission.PermissionCode] = struct{}{}
	}
	return nil
}

func parseClaimID(value any) (uint64, bool) {
	text, ok := value.(string)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseUint(text, 10, 64)
	return id, err == nil && id > 0
}
