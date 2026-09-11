package creators

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

const currentCreatorKey = "creators.current"

// CurrentCreator 是创作者端 JWT 验证后写入请求上下文的最小身份信息。
type CurrentCreator struct {
	UserID    uint64
	CreatorID uint64
	SessionID uint64
}

func CurrentCreatorFromContext(c *gin.Context) (CurrentCreator, bool) {
	value, ok := c.Get(currentCreatorKey)
	if !ok {
		return CurrentCreator{}, false
	}
	current, ok := value.(CurrentCreator)
	return current, ok
}

// RequireAuthentication 同时检查 JWT、服务端会话、账号状态与创作者档案状态，确保停用即时生效。
func RequireAuthentication(db *gorm.DB, auth config.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenText := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if tokenText == "" || tokenText == c.GetHeader("Authorization") {
			reject(c)
			return
		}
		claims := jwt.MapClaims{}
		token, err := jwt.ParseWithClaims(tokenText, claims, func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return []byte(auth.JWTSecret), nil
		})
		if err != nil || !token.Valid || claims["clientType"] != "CREATOR_WEB" {
			reject(c)
			return
		}
		userID, ok := claimID(claims["sub"])
		if !ok {
			reject(c)
			return
		}
		sessionID, ok := claimID(claims["sid"])
		if !ok {
			reject(c)
			return
		}
		version, ok := claimID(claims["tv"])
		if !ok {
			reject(c)
			return
		}
		var ses session
		if err := db.WithContext(c.Request.Context()).Where("id=? AND subject_type='CREATOR' AND subject_id=? AND client_type='CREATOR_WEB' AND status='ACTIVE' AND expires_at>?", sessionID, userID, time.Now()).First(&ses).Error; err != nil || uint64(ses.TokenVersion) != version {
			reject(c)
			return
		}
		var row struct {
			CreatorID uint64 `gorm:"column:creator_id"`
		}
		err = db.WithContext(c.Request.Context()).Table("creator_profile AS cp").Select("cp.id AS creator_id").Joins("JOIN sys_user AS u ON u.id=cp.user_id AND u.status='ACTIVE'").Where("cp.user_id=? AND cp.status='ACTIVE'", userID).Scan(&row).Error
		if err != nil || row.CreatorID == 0 {
			reject(c)
			return
		}
		c.Set(currentCreatorKey, CurrentCreator{UserID: userID, CreatorID: row.CreatorID, SessionID: sessionID})
		c.Next()
	}
}
func claimID(value any) (uint64, bool) {
	text, ok := value.(string)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseUint(text, 10, 64)
	return id, err == nil && id > 0
}
func reject(c *gin.Context) {
	httpx.AbortWithError(c, httpx.NewError(http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED", "登录状态已失效"))
}
