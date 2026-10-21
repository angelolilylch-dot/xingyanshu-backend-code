// Package avatar implements the isolated AVATAR_H5 registration boundary.
package avatar

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/filestore"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idempotency"
	"xingyanshu-backend/internal/platform/idgen"
)

const currentKey = "avatar.current"

var mobilePattern = regexp.MustCompile(`^1\d{10}$`)

type Service struct {
	db          *gorm.DB
	redis       *redis.Client
	auth        config.AuthConfig
	environment string
	storage     filestore.Storage
	settings    config.AvatarConfig
	rosterIDV   rosterIdentityVerifier
}
type Current struct{ AccountID, SessionID uint64 }

func bindAvatarJSON(c *gin.Context, value any) ([]byte, bool) {
	raw, ok := avatarRawRequest(c)
	if !ok {
		return nil, false
	}
	return raw, httpx.BindJSON(c, value)
}

func avatarRawRequest(c *gin.Context) ([]byte, bool) {
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		httpx.AbortWithError(c, avatarError(http.StatusBadRequest, "VALIDATION_FORMAT_INVALID", "请求体读取失败", nil))
		return nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, true
}

func avatarWrite(c *gin.Context, s *Service, cur Current, raw []byte, status int, fn func(*Service) (any, *httpx.AppError)) {
	result, replayed, err := idempotency.Execute(c.Request.Context(), s.db, idempotency.Scope{SubjectType: "AVATAR_H5", SubjectID: cur.AccountID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(tx *gorm.DB) (idempotency.Result, error) {
		scoped := *s
		scoped.db = tx
		data, appErr := fn(&scoped)
		if appErr != nil {
			return idempotency.Result{}, appErr
		}
		return idempotency.Result{HTTPStatus: status, Data: data}, nil
	})
	if err != nil {
		if appErr, ok := err.(*httpx.AppError); ok {
			httpx.AbortWithError(c, appErr)
		} else {
			httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(err))
		}
		return
	}
	if replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	httpx.Success(c, result.HTTPStatus, result.Data)
}

// avatarPublicWrite applies the same durable idempotency contract before an
// AVATAR_H5 account has been established.  The deterministic subject is
// derived from an already supplied authentication secret (mobile or refresh
// token), so callers never share a public idempotency namespace.
func avatarPublicWrite(c *gin.Context, s *Service, subject string, raw []byte, status int, fn func(*Service) (any, *httpx.AppError)) {
	cur := Current{AccountID: publicIdempotencySubject(subject)}
	result, replayed, err := idempotency.Execute(c.Request.Context(), s.db, idempotency.Scope{SubjectType: "AVATAR_PUBLIC", SubjectID: cur.AccountID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(tx *gorm.DB) (idempotency.Result, error) {
		scoped := *s
		scoped.db = tx
		data, appErr := fn(&scoped)
		if appErr != nil {
			return idempotency.Result{}, appErr
		}
		return idempotency.Result{HTTPStatus: status, Data: data}, nil
	})
	if err != nil {
		if appErr, ok := err.(*httpx.AppError); ok {
			httpx.AbortWithError(c, appErr)
		} else {
			httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(err))
		}
		return
	}
	if replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	httpx.Success(c, result.HTTPStatus, result.Data)
}

func publicIdempotencySubject(value string) uint64 {
	sum := sha256.Sum256([]byte(value))
	var id uint64
	for _, b := range sum[:8] {
		id = id<<8 | uint64(b)
	}
	if id == 0 {
		return 1
	}
	return id
}

type account struct {
	ID                   uint64
	Status, MobileMasked string
}
type session struct {
	ID, AccountID uint64
	Status        string
	TokenVersion  uint32
	ExpiresAt     time.Time
}

func NewService(db *gorm.DB, r *redis.Client, auth config.AuthConfig, environment string, storage ...filestore.Storage) *Service {
	return NewServiceWithConfig(db, r, auth, environment, config.AvatarConfig{}, storage...)
}

func NewServiceWithConfig(db *gorm.DB, r *redis.Client, auth config.AuthConfig, environment string, settings config.AvatarConfig, storage ...filestore.Storage) *Service {
	settings = config.NormalizeAvatarConfig(settings)
	s := &Service{db: db, redis: r, auth: auth, environment: environment, settings: settings, rosterIDV: newRosterIdentityVerifier(environment, settings.RosterIdentityProvider)}
	if len(storage) > 0 {
		s.storage = storage[0]
	}
	return s
}

func CurrentFromContext(c *gin.Context) (Current, bool) {
	v, ok := c.Get(currentKey)
	x, ok2 := v.(Current)
	return x, ok && ok2
}

// RequireAuthentication only accepts an independently persisted AVATAR_H5 session.
func RequireAuthentication(db *gorm.DB, auth config.AuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if raw == "" || raw == c.GetHeader("Authorization") {
			reject(c)
			return
		}
		claims := jwt.MapClaims{}
		token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
			if t.Method != jwt.SigningMethodHS256 {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return []byte(auth.JWTSecret), nil
		})
		if err != nil || !token.Valid || claims["clientType"] != "AVATAR_H5" {
			reject(c)
			return
		}
		accountID, ok := claimID(claims["sub"])
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
		var s session
		allowLogoutReplay := c.Request.Method == http.MethodPost && c.FullPath() == "/api/v1/avatar/auth/logout"
		if err := db.WithContext(c.Request.Context()).Table("avatar_h5_session").Where("id=? AND account_id=? AND expires_at>?", sessionID, accountID, time.Now()).Take(&s).Error; err != nil || uint64(s.TokenVersion) != version || (s.Status != "ACTIVE" && !(allowLogoutReplay && s.Status == "REVOKED")) {
			reject(c)
			return
		}
		var a account
		if err := db.WithContext(c.Request.Context()).Table("avatar_h5_account").Select("id,status,mobile_masked").Where("id=? AND status='ACTIVE'", accountID).Take(&a).Error; err != nil {
			reject(c)
			return
		}
		c.Set(currentKey, Current{AccountID: accountID, SessionID: sessionID})
		c.Next()
	}
}

func RegisterRoutes(group *gin.RouterGroup, s *Service, middleware gin.HandlerFunc) {
	public := group.Group("/avatar")
	public.GET("/public/registration-context", func(c *gin.Context) {
		data, e := s.registrationContext(c.Request.Context(), c.Query("code"), c.ClientIP())
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	public.POST("/auth/sms/send", func(c *gin.Context) {
		var r smsRequest
		raw, ok := bindAvatarJSON(c, &r)
		if !ok {
			return
		}
		avatarPublicWrite(c, s, "sms:"+r.Mobile, raw, http.StatusOK, func(scoped *Service) (any, *httpx.AppError) { return scoped.sendSMS(c.Request.Context(), r) })
	})
	public.POST("/auth/login", func(c *gin.Context) {
		var r loginRequest
		raw, ok := bindAvatarJSON(c, &r)
		if !ok {
			return
		}
		avatarPublicWrite(c, s, "login:"+r.Mobile, raw, http.StatusOK, func(scoped *Service) (any, *httpx.AppError) { return scoped.login(c.Request.Context(), r) })
	})
	public.POST("/auth/refresh", func(c *gin.Context) {
		var r refreshRequest
		raw, ok := bindAvatarJSON(c, &r)
		if !ok {
			return
		}
		avatarPublicWrite(c, s, "refresh:"+r.RefreshToken, raw, http.StatusOK, func(scoped *Service) (any, *httpx.AppError) { return scoped.refresh(c.Request.Context(), r) })
	})
	protected := public.Group("", middleware)
	protected.POST("/auth/logout", func(c *gin.Context) {
		cur, _ := CurrentFromContext(c)
		raw, ok := avatarRawRequest(c)
		if !ok {
			return
		}
		avatarWrite(c, s, cur, raw, http.StatusOK, func(scoped *Service) (any, *httpx.AppError) {
			if e := scoped.logout(c.Request.Context(), cur); e != nil {
				return nil, e
			}
			return gin.H{}, nil
		})
	})
	protected.POST("/identity-verifications", func(c *gin.Context) {
		var r identityStartRequest
		raw, ok := bindAvatarJSON(c, &r)
		if !ok {
			return
		}
		cur, _ := CurrentFromContext(c)
		avatarWrite(c, s, cur, raw, http.StatusCreated, func(scoped *Service) (any, *httpx.AppError) { return scoped.startIdentity(c.Request.Context(), cur, r) })
	})
	protected.GET("/identity-verifications/latest", func(c *gin.Context) {
		cur, _ := CurrentFromContext(c)
		data, e := s.latestIdentity(c.Request.Context(), cur)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	protected.POST("/roster-binding/confirm", func(c *gin.Context) {
		var r bindingRequest
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			httpx.AbortWithError(c, avatarError(http.StatusBadRequest, "VALIDATION_FORMAT_INVALID", "请求体读取失败", nil))
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(raw))
		if !httpx.BindJSON(c, &r) {
			return
		}
		cur, _ := CurrentFromContext(c)
		result, replayed, e := idempotency.Execute(c.Request.Context(), s.db, idempotency.Scope{SubjectType: "AVATAR_H5", SubjectID: cur.AccountID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(tx *gorm.DB) (idempotency.Result, error) {
			scoped := *s
			scoped.db = tx
			data, appErr := scoped.confirmBinding(c.Request.Context(), cur, r)
			if appErr != nil {
				return idempotency.Result{}, appErr
			}
			return idempotency.Result{HTTPStatus: http.StatusOK, Data: data}, nil
		})
		if e != nil {
			if appErr, ok := e.(*httpx.AppError); ok {
				httpx.AbortWithError(c, appErr)
				return
			}
			httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(e))
			return
		}
		if replayed {
			c.Header("Idempotency-Replayed", "true")
		}
		httpx.Success(c, result.HTTPStatus, result.Data)
	})
	protected.GET("/consents/current", func(c *gin.Context) {
		cur, _ := CurrentFromContext(c)
		httpx.Success(c, http.StatusOK, s.currentConsent(c.Request.Context(), cur))
	})
	protected.POST("/consents", func(c *gin.Context) {
		var r consentRequest
		raw, ok := bindAvatarJSON(c, &r)
		if !ok {
			return
		}
		cur, _ := CurrentFromContext(c)
		avatarWrite(c, s, cur, raw, http.StatusCreated, func(scoped *Service) (any, *httpx.AppError) {
			return scoped.acceptConsent(c.Request.Context(), cur, r, c.ClientIP())
		})
	})
	protected.GET("/profiles/me", func(c *gin.Context) {
		cur, _ := CurrentFromContext(c)
		if e := s.limitProfilePoll(c.Request.Context(), cur); e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		data, e := s.profileMe(c.Request.Context(), cur)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	protected.POST("/profiles/me/deletion-requests", func(c *gin.Context) {
		var r deletionRequest
		raw, ok := bindAvatarJSON(c, &r)
		if !ok {
			return
		}
		cur, _ := CurrentFromContext(c)
		avatarWrite(c, s, cur, raw, http.StatusCreated, func(scoped *Service) (any, *httpx.AppError) {
			return scoped.requestDeletion(c.Request.Context(), cur, r)
		})
	})
	protected.GET("/profiles/me/deletion-requests/latest", func(c *gin.Context) {
		cur, _ := CurrentFromContext(c)
		data, e := s.latestDeletionRequest(c.Request.Context(), cur)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	protected.POST("/profiles/me/reshoot", func(c *gin.Context) {
		var r reshootRequest
		raw, ok := bindAvatarJSON(c, &r)
		if !ok {
			return
		}
		cur, _ := CurrentFromContext(c)
		avatarWrite(c, s, cur, raw, http.StatusCreated, func(scoped *Service) (any, *httpx.AppError) {
			return scoped.reshoot(c.Request.Context(), cur, r, c.ClientIP())
		})
	})
	protected.POST("/collection-sessions", func(c *gin.Context) {
		cur, _ := CurrentFromContext(c)
		raw, ok := avatarRawRequest(c)
		if !ok {
			return
		}
		avatarWrite(c, s, cur, raw, http.StatusCreated, func(scoped *Service) (any, *httpx.AppError) { return scoped.startCollection(c.Request.Context(), cur) })
	})
	protected.POST("/collection-sessions/:id/assets/upload-credentials", func(c *gin.Context) {
		var r assetCredentialRequest
		raw, ok := bindAvatarJSON(c, &r)
		if !ok {
			return
		}
		cur, _ := CurrentFromContext(c)
		avatarWrite(c, s, cur, raw, http.StatusOK, func(scoped *Service) (any, *httpx.AppError) {
			return scoped.assetCredentials(c.Request.Context(), cur, c.Param("id"), r)
		})
	})
	protected.POST("/collection-sessions/:id/assets/confirm", func(c *gin.Context) {
		var r assetConfirmRequest
		raw, ok := bindAvatarJSON(c, &r)
		if !ok {
			return
		}
		cur, _ := CurrentFromContext(c)
		avatarWrite(c, s, cur, raw, http.StatusOK, func(scoped *Service) (any, *httpx.AppError) {
			return scoped.confirmAsset(c.Request.Context(), cur, c.Param("id"), r)
		})
	})
	protected.POST("/collection-sessions/:id/submit", func(c *gin.Context) {
		cur, _ := CurrentFromContext(c)
		raw, ok := avatarRawRequest(c)
		if !ok {
			return
		}
		avatarWrite(c, s, cur, raw, http.StatusOK, func(scoped *Service) (any, *httpx.AppError) {
			return scoped.submitCollection(c.Request.Context(), cur, c.Param("id"))
		})
	})
	protected.PATCH("/collection-sessions/:id/body-data", func(c *gin.Context) {
		var r bodyDataRequest
		raw, ok := bindAvatarJSON(c, &r)
		if !ok {
			return
		}
		cur, _ := CurrentFromContext(c)
		avatarWrite(c, s, cur, raw, http.StatusOK, func(scoped *Service) (any, *httpx.AppError) {
			return scoped.saveBodyData(c.Request.Context(), cur, c.Param("id"), r)
		})
	})
	if s.environment == "test" {
		protected.POST("/test/identity-verifications/:id/complete", func(c *gin.Context) {
			var r testIdentityRequest
			if !httpx.BindJSON(c, &r) {
				return
			}
			cur, _ := CurrentFromContext(c)
			data, e := s.completeIdentityForTest(c.Request.Context(), cur, c.Param("id"), r)
			if e != nil {
				httpx.AbortWithError(c, e)
				return
			}
			httpx.Success(c, http.StatusOK, data)
		})
	}
}

// RegisterAdminRoutes exposes city operations and controlled administration; no H5 route can access them.
func RegisterAdminRoutes(group *gin.RouterGroup, s *Service, middleware gin.HandlerFunc) {
	admin := group.Group("/avatar", middleware)
	admin.GET("/rosters/template", s.rosterTemplate)
	admin.POST("/roster-batches/import", func(c *gin.Context) {
		file, err := c.FormFile("file")
		if err != nil {
			httpx.AbortWithError(c, avatarError(http.StatusBadRequest, "AVATAR_ROSTER_FILE_REQUIRED", "请选择 .xlsx 名单文件", nil))
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.importRoster(c.Request.Context(), current, file)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusCreated, data)
	})
	admin.GET("/roster-batches", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.listRosterBatches(c.Request.Context(), current)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.GET("/roster-batches/:id", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.rosterBatchDetail(c.Request.Context(), current, c.Param("id"))
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.GET("/roster-batches/:id/errors", s.rosterErrorReceipt)
	admin.GET("/roster-members", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.listRosterMembers(c.Request.Context(), current)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.GET("/profiles", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.listCityProfiles(c.Request.Context(), current)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.POST("/roster-members/:id/sms-notifications", func(c *gin.Context) {
		var r notificationRequest
		if !httpx.BindJSON(c, &r) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.sendInvitation(c.Request.Context(), current, c.Param("id"), r)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusCreated, data)
	})
	admin.PUT("/city-registration-config", func(c *gin.Context) {
		var r cityConfigRequest
		if !httpx.BindJSON(c, &r) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.saveCityConfig(c.Request.Context(), current, r)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.POST("/registration-qrcodes", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.createQRCode(c.Request.Context(), current)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusCreated, data)
	})
	admin.GET("/registration-qrcodes", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.listQRCodes(c.Request.Context(), current)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.POST("/registration-qrcodes/:id/pause", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.changeQRCode(c.Request.Context(), current, c.Param("id"), "PAUSED")
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.POST("/registration-qrcodes/:id/resume", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.changeQRCode(c.Request.Context(), current, c.Param("id"), "ACTIVE")
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.GET("/quality-reviews/pending", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.listQualityReviews(c.Request.Context(), current)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.POST("/collection-sessions/:id/quality-reviews", func(c *gin.Context) {
		var r qualityQueueRequest
		if !httpx.BindJSON(c, &r) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.queueQualityReview(c.Request.Context(), current, c.Param("id"), r, c.ClientIP())
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusCreated, data)
	})
	admin.POST("/quality-reviews/:id/previews", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.issueQualityPreviews(c.Request.Context(), current, c.Param("id"), c.ClientIP())
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusCreated, data)
	})
	admin.POST("/quality-reviews/:id/decision", func(c *gin.Context) {
		var r qualityDecisionRequest
		if !httpx.BindJSON(c, &r) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.decideQualityReview(c.Request.Context(), current, c.Param("id"), r, c.ClientIP())
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.GET("/quality-previews/:token", s.consumeQualityPreview)
	admin.POST("/exports", func(c *gin.Context) {
		var r exportRequest
		if !httpx.BindJSON(c, &r) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.createExport(c.Request.Context(), current, r, c.ClientIP())
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusCreated, data)
	})
	admin.GET("/exports", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.listExports(c.Request.Context(), current)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.GET("/exports/:id", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.getExport(c.Request.Context(), current, c.Param("id"))
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.GET("/deletion-requests", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.listDeletionRequests(c.Request.Context(), current)
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.POST("/deletion-requests/:id/decision", func(c *gin.Context) {
		var r deletionDecisionRequest
		if !httpx.BindJSON(c, &r) {
			return
		}
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.decideDeletion(c.Request.Context(), current, c.Param("id"), r, c.ClientIP())
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.POST("/exports/:id/download", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.exportDownload(c.Request.Context(), current, c.Param("id"), c.ClientIP())
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.POST("/exports/:id/password/reveal", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.revealExportPassword(c.Request.Context(), current, c.Param("id"), c.ClientIP())
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusOK, data)
	})
	admin.POST("/exports/:id/regenerate", func(c *gin.Context) {
		current, _ := identity.CurrentAdminFromContext(c)
		data, e := s.regenerateExport(c.Request.Context(), current, c.Param("id"), c.ClientIP())
		if e != nil {
			httpx.AbortWithError(c, e)
			return
		}
		httpx.Success(c, http.StatusCreated, data)
	})
}

type smsRequest struct {
	Mobile string `json:"mobile" binding:"required"`
	Scene  string `json:"scene" binding:"required"`
}
type loginRequest struct {
	Mobile  string `json:"mobile" binding:"required"`
	SMSCode string `json:"smsCode" binding:"required"`
}
type refreshRequest struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}
type identityStartRequest struct {
	VerificationMethod string `json:"verificationMethod" binding:"required"`
}
type bindingRequest struct {
	RegistrationCode string `json:"registrationCode" binding:"required"`
	Name             string `json:"name" binding:"required"`
	Gender           string `json:"gender" binding:"required"`
}
type consentRequest struct {
	ConsentType      string `json:"consentType" binding:"required"`
	AgreementVersion string `json:"agreementVersion" binding:"required"`
	Accepted         bool   `json:"accepted"`
}
type deletionRequest struct {
	Reason string `json:"reason"`
}
type reshootRequest struct {
	Reason string `json:"reason"`
}
type bodyDataRequest struct {
	Gender   string   `json:"gender" binding:"required"`
	HeightCm float64  `json:"heightCm" binding:"required,gt=0,lte=300"`
	BodyType string   `json:"bodyType" binding:"required"`
	BustCm   *float64 `json:"bustCm"`
	WaistCm  *float64 `json:"waistCm"`
	HipsCm   *float64 `json:"hipsCm"`
}
type exportRequest struct {
	ExportType  string   `json:"exportType" binding:"required"`
	CityOrgID   uint64   `json:"cityOrgId"`
	PurposeCode string   `json:"purposeCode"`
	ProfileIDs  []uint64 `json:"profileIds"`
}
type deletionDecisionRequest struct {
	Decision               string `json:"decision" binding:"required"`
	RetentionExceptionCode string `json:"retentionExceptionCode"`
	Reason                 string `json:"reason"`
}
type assetCredentialRequest struct {
	AssetType   string `json:"assetType" binding:"required"`
	CaptureSlot string `json:"captureSlot" binding:"required"`
	ContentHash string `json:"contentHash" binding:"required,len=64"`
	MIMEType    string `json:"mimeType" binding:"required"`
	FileSize    int64  `json:"fileSize" binding:"required,gt=0"`
}
type assetConfirmRequest struct {
	UploadToken string `json:"uploadToken" binding:"required"`
	ContentHash string `json:"contentHash" binding:"required,len=64"`
}
type testIdentityRequest struct {
	Name          string `json:"name" binding:"required"`
	Gender        string `json:"gender" binding:"required"`
	IDNumberLast4 string `json:"idNumberLast4"`
}

func (s *Service) registrationContext(ctx context.Context, code, clientIP string) (any, *httpx.AppError) {
	if strings.TrimSpace(code) == "" {
		return nil, s.invalidQRCodeError()
	}
	var q struct {
		ID, CityRegistrationConfigID uint64
		CityCode, Status             string
		ExpiresAt                    *time.Time
	}
	if err := s.db.WithContext(ctx).Table("avatar_registration_qrcode").Where("code_hash=?", hash(code)).Take(&q).Error; err != nil || q.Status != "ACTIVE" || (q.ExpiresAt != nil && q.ExpiresAt.Before(time.Now())) {
		return nil, s.invalidQRCodeError()
	}
	var city struct {
		CityCode, SupportContactName, SupportPhoneCipher string
		Status                                           string
	}
	if err := s.db.WithContext(ctx).Table("avatar_city_registration_config").Select("city_code,support_contact_name,support_phone_cipher,status").Where("id=?", q.CityRegistrationConfigID).Take(&city).Error; err != nil {
		return nil, avatarError(http.StatusUnprocessableEntity, "AVATAR_QRCODE_INVALID", "建档入口暂不可用", nil)
	}
	phone, err := decrypt(s.auth.JWTSecret, []byte(city.SupportPhoneCipher))
	if err != nil {
		return nil, avatarError(http.StatusInternalServerError, "INTERNAL_ERROR", "建档入口配置异常", nil)
	}
	if city.Status != "ACTIVE" {
		return gin.H{"cityName": city.CityCode, "cityCode": city.CityCode, "operatorName": city.SupportContactName, "supportPhone": phone, "entryStatus": city.Status, "available": false, "notice": "当前城市建档入口暂不可用", "retentionDays": 365}, nil
	}
	now := time.Now()
	_ = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		tx.Table("avatar_registration_qrcode").Where("id=?", q.ID).Update("scan_count", gorm.Expr("scan_count+1"))
		return tx.Table("avatar_qrcode_scan_log").Create(map[string]any{"id": idgen.Next(), "qrcode_id": q.ID, "result_code": "SUCCESS", "scan_at": now, "client_ip_hash": hash(clientIP)}).Error
	})
	return gin.H{"cityName": city.CityCode, "cityCode": city.CityCode, "operatorName": city.SupportContactName, "supportPhone": phone, "entryStatus": "ACTIVE", "retentionDays": 365}, nil
}

func (s *Service) sendSMS(ctx context.Context, r smsRequest) (any, *httpx.AppError) {
	if r.Scene != "AVATAR_LOGIN" || !mobilePattern.MatchString(r.Mobile) {
		return nil, avatarError(http.StatusBadRequest, "VALIDATION_FORMAT_INVALID", "手机号或场景不正确", nil)
	}
	if s.environment != "test" {
		return nil, avatarError(http.StatusServiceUnavailable, "AVATAR_SMS_PROVIDER_NOT_CONFIGURED", "短信服务暂不可用", nil)
	}
	mh := hash(r.Mobile)
	key := "avatar:sms:login:" + mh
	ok, err := s.redis.SetNX(ctx, key, "1", s.settings.SMSResendInterval).Result()
	if err != nil {
		return nil, avatarError(http.StatusServiceUnavailable, "AVATAR_SMS_UNAVAILABLE", "短信服务暂不可用", nil)
	}
	if !ok {
		retryAfter, ttlErr := s.redis.TTL(ctx, key).Result()
		if ttlErr != nil || retryAfter < 1 {
			retryAfter = time.Second
		}
		return nil, avatarError(http.StatusTooManyRequests, "AVATAR_SMS_RATE_LIMITED", "请稍后再试", gin.H{"retryAfter": int(retryAfter.Seconds())})
	}
	if e := s.consumeDailySMSQuota(ctx, mh); e != nil {
		// Release the resend lock because this attempt was not accepted.
		_ = s.redis.Del(ctx, key).Err()
		return nil, e
	}
	now := time.Now()
	if err = s.db.WithContext(ctx).Table("avatar_h5_sms_code_log").Create(map[string]any{"id": idgen.Next(), "status": "SENT", "version": 1, "mobile_hash": mh, "code_hash": hash("123456"), "scene": "AVATAR_LOGIN", "provider_request_no": fmt.Sprintf("TEST-SMS-%d", idgen.Next()), "expires_at": now.Add(5 * time.Minute), "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; err != nil {
		return nil, avatarError(http.StatusInternalServerError, "INTERNAL_ERROR", "短信记录失败", nil)
	}
	return gin.H{"expiresIn": 300, "retryAfter": int(s.settings.SMSResendInterval.Seconds()), "mockCode": "123456"}, nil
}

func (s *Service) invalidQRCodeError() *httpx.AppError {
	data := gin.H{}
	if phone := strings.TrimSpace(s.settings.FallbackSupportPhone); phone != "" {
		data["supportPhone"] = phone
	}
	return avatarError(http.StatusUnprocessableEntity, "AVATAR_QRCODE_INVALID", "建档二维码不可用", data)
}

func (s *Service) login(ctx context.Context, r loginRequest) (any, *httpx.AppError) {
	if !mobilePattern.MatchString(r.Mobile) || strings.TrimSpace(r.SMSCode) == "" {
		return nil, avatarError(http.StatusBadRequest, "VALIDATION_FORMAT_INVALID", "登录信息不正确", nil)
	}
	mh := hash(r.Mobile)
	now := time.Now()
	var code struct {
		ID               uint64
		CodeHash, Status string
		ExpiresAt        time.Time
	}
	if err := s.db.WithContext(ctx).Table("avatar_h5_sms_code_log").Where("mobile_hash=? AND scene='AVATAR_LOGIN' AND status='SENT'", mh).Order("created_at DESC").Take(&code).Error; err != nil || code.ExpiresAt.Before(now) || code.CodeHash != hash(r.SMSCode) {
		return nil, avatarError(http.StatusUnprocessableEntity, "AVATAR_SMS_CODE_INVALID", "验证码不正确或已失效", nil)
	}
	var a account
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_h5_account").Select("id,status,mobile_masked").Where("mobile_hash=?", mh).Take(&a).Error; err == gorm.ErrRecordNotFound {
			a = account{ID: idgen.Next(), Status: "ACTIVE", MobileMasked: maskMobile(r.Mobile)}
			if e := tx.Table("avatar_h5_account").Create(map[string]any{"id": a.ID, "status": "ACTIVE", "version": 1, "account_no": fmt.Sprintf("AVH-%d", a.ID), "mobile_cipher": mustEncrypt(s.auth.JWTSecret, r.Mobile), "mobile_hash": mh, "mobile_masked": a.MobileMasked, "last_login_at": now, "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; e != nil {
				return e
			}
			created = true
		} else if err != nil {
			return err
		} else if a.Status != "ACTIVE" {
			return fmt.Errorf("account disabled")
		} else if e := tx.Table("avatar_h5_account").Where("id=?", a.ID).Updates(map[string]any{"last_login_at": now, "updated_at": now}).Error; e != nil {
			return e
		}
		return tx.Table("avatar_h5_sms_code_log").Where("id=? AND status='SENT'", code.ID).Updates(map[string]any{"status": "VERIFIED", "verified_at": now, "updated_at": now}).Error
	})
	if err != nil {
		return nil, avatarError(http.StatusInternalServerError, "INTERNAL_ERROR", "登录失败", nil)
	}
	out, e := s.issueTokens(ctx, a)
	if e != nil {
		return nil, e
	}
	out["registered"] = created
	return out, nil
}

func (s *Service) issueTokens(ctx context.Context, a account) (gin.H, *httpx.AppError) {
	sid := idgen.Next()
	refresh, err := randomToken()
	if err != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "登录失败", nil)
	}
	now := time.Now()
	if err = s.db.WithContext(ctx).Table("avatar_h5_session").Create(map[string]any{"id": sid, "status": "ACTIVE", "version": 1, "account_id": a.ID, "session_no": fmt.Sprintf("AVS-%d", sid), "refresh_token_hash": hash(refresh), "token_version": 1, "client_type": "AVATAR_H5", "last_used_at": now, "expires_at": now.Add(s.auth.RefreshTokenTTL), "status_changed_at": now, "status_changed_by_type": "SYSTEM"}).Error; err != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "登录失败", nil)
	}
	token, err := s.accessToken(a.ID, sid, 1)
	if err != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "登录失败", nil)
	}
	return gin.H{"accessToken": token, "refreshToken": refresh, "expiresIn": int(s.auth.AccessTokenTTL.Seconds()), "tokenType": "Bearer", "account": gin.H{"id": fmt.Sprint(a.ID), "mobileMasked": a.MobileMasked, "clientType": "AVATAR_H5"}}, nil
}
func (s *Service) accessToken(accountID, sid uint64, version uint32) (string, error) {
	now := time.Now()
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": fmt.Sprint(accountID), "sid": fmt.Sprint(sid), "tv": fmt.Sprint(version), "clientType": "AVATAR_H5", "iat": now.Unix(), "exp": now.Add(s.auth.AccessTokenTTL).Unix(), "jti": fmt.Sprint(idgen.Next())}).SignedString([]byte(s.auth.JWTSecret))
}

func (s *Service) refresh(ctx context.Context, r refreshRequest) (any, *httpx.AppError) {
	if strings.TrimSpace(r.RefreshToken) == "" {
		return nil, avatarError(http.StatusBadRequest, "VALIDATION_REQUIRED", "缺少刷新令牌", nil)
	}
	var current session
	if err := s.db.WithContext(ctx).Table("avatar_h5_session").Where("refresh_token_hash=? AND status='ACTIVE' AND expires_at>?", hash(r.RefreshToken), time.Now()).Take(&current).Error; err != nil {
		return nil, avatarError(http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED", "登录状态已失效", nil)
	}
	var a account
	if err := s.db.WithContext(ctx).Table("avatar_h5_account").Select("id,status,mobile_masked").Where("id=? AND status='ACTIVE'", current.AccountID).Take(&a).Error; err != nil {
		return nil, avatarError(http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED", "登录状态已失效", nil)
	}
	next, err := randomToken()
	if err != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "刷新登录状态失败", nil)
	}
	now := time.Now()
	version := current.TokenVersion + 1
	result := s.db.WithContext(ctx).Table("avatar_h5_session").Where("id=? AND token_version=? AND status='ACTIVE'", current.ID, current.TokenVersion).Updates(map[string]any{"refresh_token_hash": hash(next), "token_version": version, "last_used_at": now, "expires_at": now.Add(s.auth.RefreshTokenTTL), "updated_at": now})
	if result.Error != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "刷新登录状态失败", nil)
	}
	if result.RowsAffected == 0 {
		return nil, avatarError(http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED", "登录状态已失效", nil)
	}
	token, err := s.accessToken(a.ID, current.ID, version)
	if err != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "刷新登录状态失败", nil)
	}
	return gin.H{"accessToken": token, "refreshToken": next, "expiresIn": int(s.auth.AccessTokenTTL.Seconds()), "tokenType": "Bearer", "account": gin.H{"id": fmt.Sprint(a.ID), "mobileMasked": a.MobileMasked, "clientType": "AVATAR_H5"}}, nil
}
func (s *Service) logout(ctx context.Context, cur Current) *httpx.AppError {
	result := s.db.WithContext(ctx).Table("avatar_h5_session").Where("id=? AND account_id=? AND status='ACTIVE'", cur.SessionID, cur.AccountID).Updates(map[string]any{"status": "REVOKED", "revoked_at": time.Now(), "updated_at": time.Now()})
	if result.Error != nil || result.RowsAffected == 0 {
		return avatarError(401, "AUTH_TOKEN_EXPIRED", "登录状态已失效", nil)
	}
	return nil
}

func (s *Service) startIdentity(ctx context.Context, cur Current, r identityStartRequest) (any, *httpx.AppError) {
	if r.VerificationMethod == "" {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "实名方式不正确", nil)
	}
	if s.environment != "test" {
		return nil, avatarError(503, "AVATAR_IDENTITY_PROVIDER_NOT_CONFIGURED", "实名服务暂不可用", nil)
	}
	id := idgen.Next()
	if err := s.db.WithContext(ctx).Table("avatar_identity_verification").Create(map[string]any{"id": id, "status": "PENDING", "version": 1, "account_id": cur.AccountID, "verification_no": fmt.Sprintf("AVI-%d", id), "provider_code": "TEST_ONLY", "provider_request_no": fmt.Sprintf("TEST-IDV-%d", id), "verification_type": r.VerificationMethod, "status_changed_at": time.Now(), "status_changed_by_type": "SYSTEM"}).Error; err != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "创建实名核验失败", nil)
	}
	return gin.H{"verificationId": fmt.Sprint(id), "verificationNo": fmt.Sprintf("AVI-%d", id), "status": "PENDING"}, nil
}
func (s *Service) latestIdentity(ctx context.Context, cur Current) (any, *httpx.AppError) {
	var v struct {
		ID                     uint64
		Status, VerificationNo string
		VerifiedAt             *time.Time
	}
	if err := s.db.WithContext(ctx).Table("avatar_identity_verification").Select("id,status,verification_no,verified_at").Where("account_id=?", cur.AccountID).Order("created_at DESC").Take(&v).Error; err != nil {
		return gin.H{"status": "NOT_STARTED"}, nil
	}
	return gin.H{"verificationId": fmt.Sprint(v.ID), "verificationNo": v.VerificationNo, "status": v.Status, "verifiedAt": v.VerifiedAt}, nil
}
func (s *Service) completeIdentityForTest(ctx context.Context, cur Current, idText string, r testIdentityRequest) (any, *httpx.AppError) {
	id, ok := parseUint(idText)
	if !ok || (r.Gender != "MALE" && r.Gender != "FEMALE") {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "测试实名参数不正确", nil)
	}
	result := s.db.WithContext(ctx).Table("avatar_identity_verification").Where("id=? AND account_id=? AND status='PENDING' AND provider_code='TEST_ONLY'", id, cur.AccountID).Updates(map[string]any{"status": "PASSED", "verified_name_hash": hash(r.Name), "verified_gender": r.Gender, "id_number_last4": nullable(r.IDNumberLast4), "response_digest": hash(r.Name + ":" + r.Gender), "verified_at": time.Now(), "updated_at": time.Now(), "status_changed_at": time.Now(), "status_changed_by_type": "THIRD_PARTY"})
	if result.Error != nil || result.RowsAffected == 0 {
		return nil, avatarError(404, "AVATAR_IDENTITY_NOT_FOUND", "实名核验记录不存在", nil)
	}
	return gin.H{"status": "PASSED"}, nil
}

func (s *Service) confirmBinding(ctx context.Context, cur Current, r bindingRequest) (any, *httpx.AppError) {
	if strings.TrimSpace(r.RegistrationCode) == "" || strings.TrimSpace(r.Name) == "" || (r.Gender != "MALE" && r.Gender != "FEMALE") {
		return nil, avatarError(400, "VALIDATION_FORMAT_INVALID", "建档信息不正确", nil)
	}
	var response gin.H
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var q struct {
			CityOrgID        uint64
			CityCode, Status string
			ExpiresAt        *time.Time
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_registration_qrcode").Where("code_hash=?", hash(r.RegistrationCode)).Take(&q).Error; e != nil || q.Status != "ACTIVE" || (q.ExpiresAt != nil && q.ExpiresAt.Before(time.Now())) {
			return errQRCode
		}
		var a struct {
			ID         uint64
			MobileHash string
		}
		if e := tx.Table("avatar_h5_account").Select("id,mobile_hash").Where("id=? AND status='ACTIVE'", cur.AccountID).Take(&a).Error; e != nil {
			return e
		}
		var member struct {
			ID                             uint64
			PersonType, RegistrationStatus string
			BoundAccountID                 *uint64
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("avatar_roster_member").Select("id,person_type,registration_status,bound_account_id").Where("city_org_id=? AND status='ACTIVE' AND name_hash=? AND mobile_hash=? AND gender=?", q.CityOrgID, hash(r.Name), a.MobileHash, r.Gender).Take(&member).Error; e != nil {
			return errMismatch
		}
		if member.BoundAccountID != nil && *member.BoundAccountID != cur.AccountID {
			return errBound
		}
		profileID := idgen.Next()
		if member.RegistrationStatus == "BOUND" {
			var existing uint64
			if e := tx.Table("avatar_profile").Select("id").Where("roster_member_id=?", member.ID).Take(&existing).Error; e == nil {
				profileID = existing
			}
		} else {
			if e := tx.Table("avatar_profile").Create(map[string]any{"id": profileID, "status": "CONSENT_PENDING", "version": 1, "profile_no": fmt.Sprintf("AVP-%d", profileID), "roster_member_id": member.ID, "account_id": cur.AccountID, "city_org_id": q.CityOrgID, "city_code": q.CityCode, "real_name_status": "PASSED", "collection_status": "NOT_STARTED", "quality_status": "NOT_STARTED", "archive_status": "NOT_STARTED", "status_changed_at": time.Now(), "status_changed_by_type": "SYSTEM"}).Error; e != nil {
				return e
			}
		}
		if e := tx.Table("avatar_roster_member").Where("id=?", member.ID).Updates(map[string]any{"bound_account_id": cur.AccountID, "registration_status": "BOUND", "profile_id": profileID, "updated_at": time.Now()}).Error; e != nil {
			return e
		}
		if e := tx.Table("avatar_h5_account").Where("id=?", cur.AccountID).Updates(map[string]any{"current_roster_member_id": member.ID, "updated_at": time.Now()}).Error; e != nil {
			return e
		}
		response = gin.H{"rosterMemberId": fmt.Sprint(member.ID), "profileId": fmt.Sprint(profileID), "cityCode": q.CityCode, "personType": member.PersonType, "bindingStatus": "BOUND"}
		return nil
	})
	if err == errQRCode {
		return nil, s.invalidQRCodeError()
	}
	if err == errMismatch {
		return nil, s.matchFailure(ctx, r.RegistrationCode)
	}
	if err == errBound {
		return nil, avatarError(409, "AVATAR_ROSTER_ALREADY_BOUND", "该名单已绑定，请联系城市运营方", nil)
	}
	if err != nil {
		return nil, avatarError(500, "INTERNAL_ERROR", "绑定失败", nil)
	}
	return response, nil
}

var errQRCode = fmt.Errorf("qrcode")
var errMismatch = fmt.Errorf("mismatch")
var errBound = fmt.Errorf("bound")

func (s *Service) matchFailure(ctx context.Context, code string) *httpx.AppError {
	var q struct{ CityRegistrationConfigID uint64 }
	_ = s.db.WithContext(ctx).Table("avatar_registration_qrcode").Select("city_registration_config_id").Where("code_hash=?", hash(code)).Take(&q).Error
	var city struct{ SupportPhoneCipher string }
	_ = s.db.WithContext(ctx).Table("avatar_city_registration_config").Select("support_phone_cipher").Where("id=?", q.CityRegistrationConfigID).Take(&city).Error
	phone, _ := decrypt(s.auth.JWTSecret, []byte(city.SupportPhoneCipher))
	return avatarError(422, "AVATAR_ROSTER_MATCH_NOT_FOUND", "暂时无法完成建档，请联系城市运营方核实导入信息。", gin.H{"supportPhone": phone})
}

func reject(c *gin.Context) {
	httpx.AbortWithError(c, httpx.NewError(http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED", "登录状态已失效"))
}
func avatarError(status int, code, msg string, data any) *httpx.AppError {
	return httpx.NewErrorWithData(status, code, msg, data)
}
func hash(v string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(v)))
	return hex.EncodeToString(sum[:])
}
func maskMobile(v string) string {
	if len(v) != 11 {
		return "***"
	}
	return v[:3] + "****" + v[7:]
}
func mustEncrypt(secret, v string) []byte { b, _ := encrypt(secret, v); return b }
func encrypt(secret, v string) ([]byte, error) {
	key := sha256.Sum256([]byte(secret))
	block, e := aes.NewCipher(key[:])
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = io.ReadFull(rand.Reader, nonce); e != nil {
		return nil, e
	}
	return []byte(base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(v), nil))), nil
}
func decrypt(secret string, v []byte) (string, error) {
	key := sha256.Sum256([]byte(secret))
	block, e := aes.NewCipher(key[:])
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	raw, e := base64.StdEncoding.DecodeString(string(v))
	if e != nil || len(raw) < g.NonceSize() {
		return "", fmt.Errorf("invalid encrypted value")
	}
	plain, e := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], nil)
	return string(plain), e
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, e := io.ReadFull(rand.Reader, b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func claimID(v any) (uint64, bool) { return parseUint(fmt.Sprint(v)) }
func parseUint(v string) (uint64, bool) {
	var n uint64
	_, e := fmt.Sscan(v, &n)
	return n, e == nil && n > 0
}
func nullable(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
