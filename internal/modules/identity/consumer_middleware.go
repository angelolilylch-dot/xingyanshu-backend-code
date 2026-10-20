package identity

import (
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

const currentConsumerKey = "identity.currentConsumer"

type CurrentConsumer struct {
	ConsumerID uint64
	SessionID  uint64
}

func CurrentConsumerFromContext(c *gin.Context) (CurrentConsumer, bool) {
	v, ok := c.Get(currentConsumerKey)
	if !ok {
		return CurrentConsumer{}, false
	}
	current, ok := v.(CurrentConsumer)
	return current, ok
}

// RequireConsumerAuthentication 只接受消费者 H5 会话，禁止后台和创作者令牌跨端调用。
func RequireConsumerAuthentication(db *gorm.DB, authCfg config.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		text := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if text == "" || text == c.GetHeader("Authorization") {
			rejectConsumer(c)
			return
		}
		claims := jwt.MapClaims{}
		token, err := jwt.ParseWithClaims(text, claims, func(t *jwt.Token) (any, error) {
			if t.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return []byte(authCfg.JWTSecret), nil
		})
		if err != nil || !token.Valid || claims["clientType"] != "CONSUMER_H5" {
			rejectConsumer(c)
			return
		}
		consumerID, ok := parseClaimID(claims["sub"])
		if !ok {
			rejectConsumer(c)
			return
		}
		sessionID, ok := parseClaimID(claims["sid"])
		if !ok {
			rejectConsumer(c)
			return
		}
		version, ok := parseClaimID(claims["tv"])
		if !ok {
			rejectConsumer(c)
			return
		}
		var session authSession
		if err := db.WithContext(c.Request.Context()).Where("id=? AND subject_type='CONSUMER' AND subject_id=? AND client_type='CONSUMER_H5' AND status='ACTIVE' AND expires_at>?", sessionID, consumerID, time.Now()).First(&session).Error; err != nil || uint64(session.TokenVersion) != version {
			rejectConsumer(c)
			return
		}
		var account consumerAccount
		if err := db.WithContext(c.Request.Context()).Where("id=? AND status='ACTIVE'", consumerID).First(&account).Error; err != nil {
			rejectConsumer(c)
			return
		}
		c.Set(currentConsumerKey, CurrentConsumer{ConsumerID: consumerID, SessionID: sessionID})
		c.Next()
	}
}

func rejectConsumer(c *gin.Context) {
	httpx.AbortWithError(c, httpx.NewError(http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED", "登录状态已失效"))
}
