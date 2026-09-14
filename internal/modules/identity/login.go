package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"regexp"
	"strconv"
	"strings"
	"time"
	"xingyanshu-backend/internal/config"
)

var adminUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,48}[A-Za-z0-9]$`)

// AdminLoginRequest 是管理员登录所需的账号、密码和一次性验证码。
type AdminLoginRequest struct {
	Username    string `json:"username" binding:"required,min=2,max=50"`
	Password    string `json:"password" binding:"required,min=8,max=64"`
	CaptchaID   string `json:"captchaId" binding:"required"`
	CaptchaCode string `json:"captchaCode" binding:"required,min=4,max=8"`
}

// IsValidAdminUsername 校验后台账号的固定格式，供登录和后续账号管理接口共用。
func IsValidAdminUsername(value string) bool {
	return adminUsernamePattern.MatchString(value)
}

type adminUser struct {
	ID                 uint64
	Username           string
	PasswordHash       string `gorm:"column:password_hash"`
	MustChangePassword bool   `gorm:"column:must_change_password"`
	DisplayName        string `gorm:"column:display_name"`
	OrgID              uint64 `gorm:"column:org_id"`
	DataScope          string `gorm:"column:data_scope"`
	Status             string
}

type adminRole struct {
	RoleCode string `gorm:"column:role_code"`
	RoleName string `gorm:"column:role_name"`
}

type adminPermission struct {
	PermissionCode string `gorm:"column:permission_code"`
	PermissionName string `gorm:"column:permission_name"`
	PermissionType string `gorm:"column:permission_type"`
	ResourceCode   string `gorm:"column:resource_code"`
}

type authSession struct {
	ID               uint64
	SubjectType      string `gorm:"column:subject_type"`
	SubjectID        uint64 `gorm:"column:subject_id"`
	ClientType       string `gorm:"column:client_type"`
	RefreshTokenHash string `gorm:"column:refresh_token_hash"`
	TokenVersion     uint32 `gorm:"column:token_version"`
	Status           string
	ExpiresAt        time.Time `gorm:"column:expires_at"`
}

func (authSession) TableName() string { return "auth_session" }

func (adminUser) TableName() string { return "sys_user" }

// LoginService 负责管理员登录的验证码、密码和会话令牌处理。
type LoginService struct {
	db          *gorm.DB
	captcha     *CaptchaService
	redis       *redis.Client
	cfg         config.AuthConfig
	environment string
}

func NewLoginService(db *gorm.DB, captcha *CaptchaService, redisClient *redis.Client, cfg config.AuthConfig, environment string) *LoginService {
	return &LoginService{db: db, captcha: captcha, redis: redisClient, cfg: cfg, environment: environment}
}

// VerifyCaptcha 供统一登录的不同后台角色分支复用同一套一次性图形验证码校验。
func (s *LoginService) VerifyCaptcha(ctx context.Context, captchaID, captchaCode string) (bool, error) {
	return s.captcha.Verify(ctx, captchaID, captchaCode)
}

// Login 验证管理员凭证后签发短效访问令牌。
func (s *LoginService) Login(ctx context.Context, request AdminLoginRequest) (map[string]any, string, error) {
	ok, err := s.VerifyCaptcha(ctx, request.CaptchaID, request.CaptchaCode)
	if err != nil {
		return nil, "CAPTCHA_UNAVAILABLE", err
	}
	if !ok {
		return nil, "AUTH_CAPTCHA_INVALID", nil
	}
	return s.loginAdmin(ctx, request)
}

// LoginAdminAfterCaptcha 在统一登录入口已完成验证码校验后验证后台账号，避免验证码被重复消费。
func (s *LoginService) LoginAdminAfterCaptcha(ctx context.Context, request AdminLoginRequest) (map[string]any, string, error) {
	return s.loginAdmin(ctx, request)
}

// IsAdminAccount 判断账号是否属于后台账号，用于统一登录入口自动识别账号类型。
func (s *LoginService) IsAdminAccount(ctx context.Context, username string) bool {
	var user adminUser
	return s.db.WithContext(ctx).Select("id").Where("username = ?", username).First(&user).Error == nil
}

func (s *LoginService) loginAdmin(ctx context.Context, request AdminLoginRequest) (map[string]any, string, error) {
	var user adminUser
	if err := s.db.WithContext(ctx).Where("username = ?", request.Username).First(&user).Error; err != nil {
		return nil, "AUTH_LOGIN_INVALID", nil
	}
	if user.Status != "ACTIVE" {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(request.Password)) != nil {
		return nil, "AUTH_LOGIN_INVALID", nil
	}
	sessionID := uint64(time.Now().UnixNano())
	refreshToken := formatRefreshToken(sessionID)
	hash, err := bcrypt.GenerateFromPassword([]byte(refreshToken), 12)
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	session := authSession{ID: sessionID, SubjectType: "ADMIN", SubjectID: user.ID, ClientType: "ADMIN_WEB", RefreshTokenHash: string(hash), TokenVersion: 1, Status: "ACTIVE", ExpiresAt: time.Now().Add(s.cfg.RefreshTokenTTL)}
	if err := s.db.WithContext(ctx).Create(&session).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	response, err := s.tokens(user, session.ID, session.TokenVersion, refreshToken)
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	response["mustChangePassword"] = user.MustChangePassword
	return response, "", nil
}

// ChangePassword 修改当前账号密码；首次登录和管理员重置后的临时密码必须经过此接口替换。
func (s *LoginService) ChangePassword(ctx context.Context, current CurrentAdmin, oldPassword, newPassword string) (string, error) {
	if !IsValidPassword(newPassword) {
		return "VALIDATION_FORMAT_INVALID", nil
	}
	var user adminUser
	if err := s.db.WithContext(ctx).Where("id = ? AND status = ?", current.UserID, "ACTIVE").First(&user).Error; err != nil {
		return "AUTH_LOGIN_INVALID", nil
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPassword)) != nil {
		return "AUTH_LOGIN_INVALID", nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return "INTERNAL_ERROR", err
	}
	if err := s.db.WithContext(ctx).Model(&adminUser{}).Where("id = ?", current.UserID).Updates(map[string]any{"password_hash": string(hash), "must_change_password": false, "updated_at": time.Now()}).Error; err != nil {
		return "INTERNAL_ERROR", err
	}
	return "", nil
}

// ResetPassword 由平台管理员设置临时密码，并强制目标账号下次登录修改。
func (s *LoginService) ResetPassword(ctx context.Context, userID uint64, newPassword string) (string, error) {
	if !IsValidPassword(newPassword) {
		return "VALIDATION_FORMAT_INVALID", nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return "INTERNAL_ERROR", err
	}
	result := s.db.WithContext(ctx).Model(&adminUser{}).Where("id = ? AND status = ?", userID, "ACTIVE").Updates(map[string]any{"password_hash": string(hash), "must_change_password": true, "updated_at": time.Now()})
	if result.Error != nil {
		return "INTERNAL_ERROR", result.Error
	}
	if result.RowsAffected == 0 {
		return "RESOURCE_NOT_FOUND", nil
	}
	if err := s.db.WithContext(ctx).Model(&authSession{}).Where("subject_type = ? AND subject_id = ? AND client_type = ? AND status = ?", "ADMIN", userID, "ADMIN_WEB", "ACTIVE").Updates(map[string]any{"status": "REVOKED", "last_used_at": time.Now()}).Error; err != nil {
		return "INTERNAL_ERROR", err
	}
	return "", nil
}

// SendPasswordResetSMS 发送管理员找回密码验证码；测试环境返回 mockCode，生产环境需接入真实短信 Provider。
func (s *LoginService) SendPasswordResetSMS(ctx context.Context, mobile string) (map[string]any, string, error) {
	var user adminUser
	if err := s.db.WithContext(ctx).Where("mobile = ? AND status = ?", mobile, "ACTIVE").First(&user).Error; err != nil {
		return map[string]any{"expiresIn": 300}, "", nil
	}
	code := "123456"
	if err := s.redis.Set(ctx, "admin:sms:password-reset:"+mobile, code, 5*time.Minute).Err(); err != nil {
		return nil, "SMS_UNAVAILABLE", err
	}
	data := map[string]any{"expiresIn": 300}
	if s.environment == "test" {
		data["mockCode"] = code
	}
	return data, "", nil
}

// ResetPasswordBySMS 通过手机号和一次性短信验证码设置新密码。
func (s *LoginService) ResetPasswordBySMS(ctx context.Context, mobile, code, newPassword string) (string, error) {
	if !IsValidPassword(newPassword) {
		return "VALIDATION_FORMAT_INVALID", nil
	}
	value, err := s.redis.Eval(ctx, "local v=redis.call('GET',KEYS[1]); if v then redis.call('DEL',KEYS[1]); end; return v", []string{"admin:sms:password-reset:" + mobile}).Text()
	if err == redis.Nil || value != code {
		return "SMS_CODE_INVALID", nil
	}
	if err != nil {
		return "SMS_UNAVAILABLE", err
	}
	var user adminUser
	if err := s.db.WithContext(ctx).Where("mobile = ? AND status = ?", mobile, "ACTIVE").First(&user).Error; err != nil {
		return "AUTH_LOGIN_INVALID", nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return "INTERNAL_ERROR", err
	}
	if err := s.db.WithContext(ctx).Model(&adminUser{}).Where("id = ?", user.ID).Updates(map[string]any{"password_hash": string(hash), "must_change_password": false, "updated_at": time.Now()}).Error; err != nil {
		return "INTERNAL_ERROR", err
	}
	return "", nil
}

// RefreshRequest 是刷新管理员会话令牌的请求体。
type RefreshRequest struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}

// Refresh 校验旧刷新令牌并在事务内替换为新令牌。
func (s *LoginService) Refresh(ctx context.Context, request RefreshRequest) (map[string]any, string, error) {
	id, ok := parseRefreshToken(request.RefreshToken)
	if !ok {
		return nil, "AUTH_TOKEN_EXPIRED", nil
	}
	var result map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session authSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&session).Error; err != nil || session.Status != "ACTIVE" || !session.ExpiresAt.After(time.Now()) {
			return errInvalidRefresh
		}
		if bcrypt.CompareHashAndPassword([]byte(session.RefreshTokenHash), []byte(request.RefreshToken)) != nil {
			return errInvalidRefresh
		}
		var user adminUser
		if err := tx.Where("id = ? AND status = ?", session.SubjectID, "ACTIVE").First(&user).Error; err != nil {
			return errInvalidRefresh
		}
		next := formatRefreshToken(session.ID)
		hash, err := bcrypt.GenerateFromPassword([]byte(next), 12)
		if err != nil {
			return err
		}
		nextTokenVersion := session.TokenVersion + 1
		if err := tx.Model(&session).Updates(map[string]any{"refresh_token_hash": string(hash), "token_version": nextTokenVersion, "last_used_at": time.Now()}).Error; err != nil {
			return err
		}
		if session.ClientType == "CREATOR_WEB" {
			var profile struct {
				ID             uint64
				DisplayName    string `gorm:"column:display_name"`
				IdentityStatus string `gorm:"column:identity_status"`
			}
			if err := tx.Table("creator_profile").Where("user_id = ? AND status = ?", user.ID, "ACTIVE").First(&profile).Error; err != nil {
				return errInvalidRefresh
			}
			result, err = s.creatorTokens(user, profile.ID, profile.DisplayName, profile.IdentityStatus, session.ID, nextTokenVersion, next)
		} else {
			result, err = s.tokens(user, session.ID, nextTokenVersion, next)
		}
		return err
	})
	if err == errInvalidRefresh {
		return nil, "AUTH_TOKEN_EXPIRED", nil
	}
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	return result, "", nil
}

// creatorTokens 复用统一会话的刷新令牌轮换结果，仅签发创作者端访问令牌。
func (s *LoginService) creatorTokens(user adminUser, creatorID uint64, displayName, identityStatus string, sessionID uint64, tokenVersion uint32, refreshToken string) (map[string]any, error) {
	now := time.Now()
	claims := jwt.MapClaims{"sub": fmt.Sprint(user.ID), "sid": fmt.Sprint(sessionID), "tv": fmt.Sprint(tokenVersion), "clientType": "CREATOR_WEB", "iat": now.Unix(), "exp": now.Add(s.cfg.AccessTokenTTL).Unix(), "jti": randomID()}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		return nil, err
	}
	return map[string]any{"accessToken": token, "refreshToken": refreshToken, "expiresIn": int(s.cfg.AccessTokenTTL.Seconds()), "tokenType": "Bearer", "creator": map[string]any{"id": fmt.Sprint(creatorID), "displayName": displayName, "identityStatus": identityStatus}}, nil
}

// Logout 撤销当前会话，使其访问令牌和刷新令牌均不能再用于后续请求。
func (s *LoginService) Logout(ctx context.Context, current CurrentAdmin) error {
	result := s.db.WithContext(ctx).Model(&authSession{}).
		Where("id = ? AND subject_type = ? AND subject_id = ? AND client_type = ? AND status = ?", current.SessionID, "ADMIN", current.UserID, "ADMIN_WEB", "ACTIVE").
		Updates(map[string]any{"status": "REVOKED", "last_used_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errInvalidRefresh
	}
	return nil
}

// Me 汇总当前后台账号的资料、唯一角色及有效权限，供前端恢复登录态和展示菜单。
func (s *LoginService) Me(ctx context.Context, current CurrentAdmin) (map[string]any, error) {
	var user adminUser
	if err := s.db.WithContext(ctx).Where("id = ? AND status = ?", current.UserID, "ACTIVE").First(&user).Error; err != nil {
		return nil, err
	}
	var roles []adminRole
	if err := s.db.WithContext(ctx).Table("sys_user_role AS ur").
		Select("r.role_code, r.role_name").
		Joins("JOIN sys_role AS r ON r.id = ur.role_id AND r.status = ?", "ACTIVE").
		Where("ur.user_id = ?", user.ID).
		Order("ur.is_primary DESC, r.id ASC").
		Scan(&roles).Error; err != nil {
		return nil, err
	}
	var permissions []adminPermission
	if err := s.db.WithContext(ctx).Table("sys_user_role AS ur").
		Select("DISTINCT p.permission_code, p.permission_name, p.permission_type, p.resource_code").
		Joins("JOIN sys_role_permission AS rp ON rp.role_id = ur.role_id").
		Joins("JOIN sys_permission AS p ON p.id = rp.permission_id AND p.status = ?", "ACTIVE").
		Where("ur.user_id = ?", user.ID).
		Order("p.permission_code ASC").
		Scan(&permissions).Error; err != nil {
		return nil, err
	}
	roleItems := make([]map[string]string, 0, len(roles))
	menuItems := make([]map[string]string, 0)
	permissionCodes := make([]string, 0, len(permissions))
	for _, role := range roles {
		roleItems = append(roleItems, map[string]string{"code": role.RoleCode, "name": role.RoleName})
	}
	for _, permission := range permissions {
		permissionCodes = append(permissionCodes, permission.PermissionCode)
		if permission.PermissionType == "MENU" {
			menuItems = append(menuItems, map[string]string{"code": permission.PermissionCode, "name": permission.PermissionName, "resourceCode": permission.ResourceCode})
		}
	}
	return map[string]any{
		"user": map[string]any{
			"id":            fmt.Sprint(user.ID),
			"username":      user.Username,
			"displayName":   user.DisplayName,
			"orgId":         fmt.Sprint(user.OrgID),
			"dataScope":     user.DataScope,
			"dataScopeName": scopeName(user.DataScope),
		},
		"roles":       roleItems,
		"menus":       menuItems,
		"permissions": permissionCodes,
	}, nil
}

var errInvalidRefresh = fmt.Errorf("invalid refresh token")

func formatRefreshToken(id uint64) string {
	return strconv.FormatUint(id, 10) + "." + randomID()
}
func parseRefreshToken(value string) (uint64, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(parts[1]) < 32 {
		return 0, false
	}
	id, err := strconv.ParseUint(parts[0], 10, 64)
	return id, err == nil
}

// tokens 为已校验的后台用户签发与当前会话绑定的访问令牌。
func (s *LoginService) tokens(user adminUser, sessionID uint64, tokenVersion uint32, refreshToken string) (map[string]any, error) {
	now := time.Now()
	claims := jwt.MapClaims{"sub": fmt.Sprint(user.ID), "sid": fmt.Sprint(sessionID), "tv": fmt.Sprint(tokenVersion), "clientType": "ADMIN_WEB", "iat": now.Unix(), "exp": now.Add(s.cfg.AccessTokenTTL).Unix(), "jti": randomID()}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		return nil, err
	}
	return map[string]any{"accessToken": token, "refreshToken": refreshToken, "expiresIn": int(s.cfg.AccessTokenTTL.Seconds()), "tokenType": "Bearer", "user": map[string]any{"id": fmt.Sprint(user.ID), "username": user.Username, "displayName": user.DisplayName, "orgId": fmt.Sprint(user.OrgID), "dataScope": user.DataScope, "dataScopeName": scopeName(user.DataScope)}}, nil
}
func randomID() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func scopeName(scope string) string {
	return map[string]string{"ALL": "全平台", "CITY": "本城市", "ORG": "本组织", "SELF": "仅本人"}[scope]
}
