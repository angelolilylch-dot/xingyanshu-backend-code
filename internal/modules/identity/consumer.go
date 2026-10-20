package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/platform/idgen"
)

const consumerSMSTTL = 5 * time.Minute

var consumerMobilePattern = regexp.MustCompile(`^1[3-9]\d{9}$`)

type consumerAccount struct {
	ID                  uint64
	Status              string
	MobileCipher        []byte     `gorm:"column:mobile_cipher"`
	MobileHash          string     `gorm:"column:mobile_hash"`
	MobileMasked        string     `gorm:"column:mobile_masked"`
	AgreementVersion    string     `gorm:"column:agreement_version"`
	AgreementAcceptedAt *time.Time `gorm:"column:agreement_accepted_at"`
}

func (consumerAccount) TableName() string { return "consumer_account" }

type ConsumerSMSRequest struct {
	Mobile string `json:"mobile" binding:"required"`
}

type ConsumerLoginRequest struct {
	Mobile            string `json:"mobile" binding:"required"`
	SMSCode           string `json:"smsCode" binding:"required,len=6"`
	AgreementAccepted bool   `json:"agreementAccepted"`
	AgreementVersion  string `json:"agreementVersion" binding:"required,max=64"`
}

// SendConsumerSMS 发出消费者注册/登录验证码。测试环境仅以 mockCode 返回，不对外暴露账号是否已注册。
func (s *LoginService) SendConsumerSMS(ctx context.Context, mobile, clientIP string) (map[string]any, string, error) {
	mobile = strings.TrimSpace(mobile)
	if !consumerMobilePattern.MatchString(mobile) {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	hash := consumerMobileHash(mobile)
	if ok, err := s.redis.SetNX(ctx, "consumer:sms:cooldown:"+hash, "1", time.Minute).Result(); err != nil {
		return nil, "SMS_UNAVAILABLE", err
	} else if !ok {
		return nil, "SMS_SEND_TOO_FREQUENT", nil
	}
	if clientIP != "" {
		key := "consumer:sms:ip:" + consumerMobileHash(clientIP)
		count, err := s.redis.Incr(ctx, key).Result()
		if err != nil {
			return nil, "SMS_UNAVAILABLE", err
		}
		if count == 1 {
			_ = s.redis.Expire(ctx, key, time.Hour).Err()
		}
		if count > 20 {
			return nil, "SMS_SEND_TOO_FREQUENT", nil
		}
	}
	code := "123456"
	if err := s.redis.Set(ctx, "consumer:sms:login:"+hash, code, consumerSMSTTL).Err(); err != nil {
		return nil, "SMS_UNAVAILABLE", err
	}
	now := time.Now()
	codeHash := consumerMobileHash(code)
	if err := s.db.WithContext(ctx).Table("sms_code_log").Create(map[string]any{"id": idgen.Next(), "mobile_hash": hash, "scene": "CONSUMER_LOGIN", "code_hash": codeHash, "expires_at": now.Add(consumerSMSTTL), "send_status": "MOCK_SENT"}).Error; err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	result := map[string]any{"expiresIn": int(consumerSMSTTL.Seconds())}
	if s.environment == "test" {
		result["mockCode"] = code
	}
	return result, "", nil
}

// ConsumerLogin 使用一次性短信登录；未注册手机号在已同意协议后自动创建消费者账号。
func (s *LoginService) ConsumerLogin(ctx context.Context, request ConsumerLoginRequest) (map[string]any, string, error) {
	request.Mobile, request.AgreementVersion = strings.TrimSpace(request.Mobile), strings.TrimSpace(request.AgreementVersion)
	if !consumerMobilePattern.MatchString(request.Mobile) || request.AgreementVersion == "" || !request.AgreementAccepted {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	mobileHash := consumerMobileHash(request.Mobile)
	value, err := s.redis.Eval(ctx, "local v=redis.call('GET',KEYS[1]); if v then redis.call('DEL',KEYS[1]); end; return v", []string{"consumer:sms:login:" + mobileHash}).Text()
	if err == redis.Nil || value != request.SMSCode {
		return nil, "SMS_CODE_INVALID", nil
	}
	if err != nil {
		return nil, "SMS_UNAVAILABLE", err
	}
	var account consumerAccount
	now := time.Now()
	created := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("mobile_hash=?", mobileHash).First(&account).Error
		if err == gorm.ErrRecordNotFound {
			account = consumerAccount{ID: idgen.Next(), Status: "ACTIVE", MobileCipher: []byte(request.Mobile), MobileHash: mobileHash, MobileMasked: maskConsumerMobile(request.Mobile), AgreementVersion: request.AgreementVersion, AgreementAcceptedAt: &now}
			if err := tx.Create(&account).Error; err != nil {
				return err
			}
			created = true
		} else if err != nil {
			return err
		} else if account.Status != "ACTIVE" {
			return errConsumerDisabled
		} else if err := tx.Model(&consumerAccount{}).Where("id=?", account.ID).Updates(map[string]any{"agreement_version": request.AgreementVersion, "agreement_accepted_at": now, "last_login_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		if created {
			return tx.Model(&consumerAccount{}).Where("id=?", account.ID).Updates(map[string]any{"last_login_at": now}).Error
		}
		return nil
	})
	if err == errConsumerDisabled {
		return nil, "AUTH_FORBIDDEN", nil
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return nil, "RESOURCE_CONFLICT", nil
		}
		return nil, "INTERNAL_ERROR", err
	}
	result, err := s.issueConsumerTokens(ctx, account)
	if err != nil {
		return nil, "INTERNAL_ERROR", err
	}
	result["registered"] = created
	return result, "", nil
}

func (s *LoginService) issueConsumerTokens(ctx context.Context, account consumerAccount) (map[string]any, error) {
	sessionID := idgen.Next()
	refresh := formatRefreshToken(sessionID)
	hash, err := bcrypt.GenerateFromPassword([]byte(refresh), 12)
	if err != nil {
		return nil, err
	}
	session := authSession{ID: sessionID, SubjectType: "CONSUMER", SubjectID: account.ID, ClientType: "CONSUMER_H5", RefreshTokenHash: string(hash), TokenVersion: 1, Status: "ACTIVE", ExpiresAt: time.Now().Add(s.cfg.RefreshTokenTTL)}
	if err := s.db.WithContext(ctx).Create(&session).Error; err != nil {
		return nil, err
	}
	return s.consumerTokens(account, session.ID, session.TokenVersion, refresh)
}

func (s *LoginService) consumerTokens(account consumerAccount, sessionID uint64, tokenVersion uint32, refresh string) (map[string]any, error) {
	now := time.Now()
	claims := jwt.MapClaims{"sub": fmt.Sprint(account.ID), "sid": fmt.Sprint(sessionID), "tv": fmt.Sprint(tokenVersion), "clientType": "CONSUMER_H5", "iat": now.Unix(), "exp": now.Add(s.cfg.AccessTokenTTL).Unix(), "jti": randomID()}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		return nil, err
	}
	return map[string]any{"accessToken": token, "refreshToken": refresh, "expiresIn": int(s.cfg.AccessTokenTTL.Seconds()), "tokenType": "Bearer", "consumer": consumerResponse(account)}, nil
}

func (s *LoginService) RefreshConsumer(ctx context.Context, request RefreshRequest) (map[string]any, string, error) {
	return s.refresh(ctx, request, "CONSUMER_H5")
}

func (s *LoginService) ConsumerMe(ctx context.Context, consumerID uint64) (map[string]any, error) {
	var account consumerAccount
	if err := s.db.WithContext(ctx).Where("id=? AND status='ACTIVE'", consumerID).First(&account).Error; err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for key, table := range map[string]string{"orderCount": "product_order", "bookingCount": "activity_booking", "afterSaleCount": "after_sale_ticket"} {
		var count int64
		if err := s.db.WithContext(ctx).Table(table).Where("consumer_id=?", consumerID).Count(&count).Error; err != nil {
			return nil, err
		}
		counts[key] = count
	}
	return map[string]any{"clientType": "CONSUMER_H5", "consumer": consumerResponse(account), "orderCount": counts["orderCount"], "bookingCount": counts["bookingCount"], "afterSaleCount": counts["afterSaleCount"]}, nil
}

func (s *LoginService) ConsumerLogout(ctx context.Context, current CurrentConsumer) error {
	result := s.db.WithContext(ctx).Model(&authSession{}).Where("id=? AND subject_type='CONSUMER' AND subject_id=? AND client_type='CONSUMER_H5' AND status='ACTIVE'", current.SessionID, current.ConsumerID).Updates(map[string]any{"status": "REVOKED", "last_used_at": time.Now()})
	if result.Error != nil || result.RowsAffected == 0 {
		return errInvalidRefresh
	}
	return nil
}

func consumerResponse(account consumerAccount) map[string]any {
	return map[string]any{"id": fmt.Sprint(account.ID), "mobileMasked": account.MobileMasked, "agreementVersion": account.AgreementVersion, "agreementAcceptedAt": account.AgreementAcceptedAt}
}

func consumerMobileHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

func maskConsumerMobile(value string) string {
	if len(value) != 11 {
		return ""
	}
	return value[:3] + "****" + value[7:]
}

var errConsumerDisabled = fmt.Errorf("consumer disabled")
