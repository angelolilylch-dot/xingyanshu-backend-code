package identity

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/platform/httpx"
)

// RegisterUnifiedRoutes 提供后台与创作者共用的会话退出和当前用户读取入口。
func RegisterUnifiedRoutes(group *gin.RouterGroup, db *gorm.DB, cfg config.AuthConfig, login *LoginService) {
	group.POST("/auth/logout", func(c *gin.Context) {
		subject, ok := unifiedSubject(c, db, cfg)
		if !ok {
			return
		}
		result := db.WithContext(c.Request.Context()).Model(&authSession{}).Where("id=? AND subject_id=? AND client_type=? AND status='ACTIVE'", subject.SessionID, subject.UserID, subject.ClientType).Updates(map[string]any{"status": "REVOKED", "last_used_at": time.Now()})
		if result.Error != nil {
			httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "注销登录失败"))
			return
		}
		httpx.Success(c, http.StatusOK, gin.H{})
	})
	group.GET("/auth/me", func(c *gin.Context) {
		subject, ok := unifiedSubject(c, db, cfg)
		if !ok {
			return
		}
		if subject.ClientType == "ADMIN_WEB" {
			current, err := authenticateAdmin(c, db, cfg)
			if err != nil {
				respondAdminAuthenticationError(c, err)
				return
			}
			data, err := login.Me(c.Request.Context(), current)
			if err != nil {
				httpx.AbortWithError(c, httpx.NewError(http.StatusInternalServerError, "INTERNAL_ERROR", "读取当前用户失败"))
				return
			}
			httpx.Success(c, http.StatusOK, data)
			return
		}
		data, err := login.CreatorMe(c.Request.Context(), subject.UserID)
		if err != nil {
			rejectUnified(c)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
}

type unifiedCurrent struct {
	UserID, SessionID uint64
	ClientType        string
}

func unifiedSubject(c *gin.Context, db *gorm.DB, cfg config.AuthConfig) (unifiedCurrent, bool) {
	text := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	if text == "" || text == c.GetHeader("Authorization") {
		rejectUnified(c)
		return unifiedCurrent{}, false
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(text, claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("invalid signing")
		}
		return []byte(cfg.JWTSecret), nil
	})
	if err != nil || !token.Valid {
		rejectUnified(c)
		return unifiedCurrent{}, false
	}
	userID, ok := parseClaimID(claims["sub"])
	if !ok {
		rejectUnified(c)
		return unifiedCurrent{}, false
	}
	sessionID, ok := parseClaimID(claims["sid"])
	if !ok {
		rejectUnified(c)
		return unifiedCurrent{}, false
	}
	typ, _ := claims["clientType"].(string)
	if typ != "ADMIN_WEB" && typ != "CREATOR_WEB" {
		rejectUnified(c)
		return unifiedCurrent{}, false
	}
	var ses authSession
	if err := db.WithContext(c.Request.Context()).Where("id=? AND subject_id=? AND client_type=? AND status='ACTIVE' AND expires_at>?", sessionID, userID, typ, time.Now()).First(&ses).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			rejectUnified(c)
		} else {
			httpx.AbortWithError(c, httpx.NewError(http.StatusServiceUnavailable, "AUTH_SERVICE_UNAVAILABLE", "登录服务暂不可用，请稍后重试"))
		}
		return unifiedCurrent{}, false
	}
	// JWT 中的令牌版本必须与当前会话一致；密码重置或强制退出后旧令牌会因此失效。
	tokenVersion, ok := parseClaimID(claims["tv"])
	if !ok || tokenVersion != uint64(ses.TokenVersion) {
		rejectUnified(c)
		return unifiedCurrent{}, false
	}
	// 会话客户端和主体类型绑定，防止把创作者令牌当作后台令牌使用。
	expectedSubjectType := "ADMIN"
	if typ == "CREATOR_WEB" {
		expectedSubjectType = "CREATOR"
	}
	if ses.SubjectType != expectedSubjectType {
		rejectUnified(c)
		return unifiedCurrent{}, false
	}
	return unifiedCurrent{userID, sessionID, typ}, true
}
func rejectUnified(c *gin.Context) {
	httpx.AbortWithError(c, httpx.NewError(http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED", "登录状态已失效"))
}
