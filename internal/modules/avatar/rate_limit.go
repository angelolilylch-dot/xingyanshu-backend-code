package avatar

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"xingyanshu-backend/internal/platform/httpx"
)

// consumeDailySMSQuota counts accepted sends in a calendar day. The key expires
// at the next local midnight, so an interrupted client cannot accumulate quota.
func (s *Service) consumeDailySMSQuota(ctx context.Context, mobileHash string) *httpx.AppError {
	if s.redis == nil {
		return nil
	}
	now := time.Now()
	nextDay := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	key := fmt.Sprintf("avatar:sms:daily:%s:%s", now.Format("20060102"), mobileHash)
	n, err := s.redis.Incr(ctx, key).Result()
	if err != nil {
		return avatarError(503, "AVATAR_SMS_UNAVAILABLE", "短信服务暂不可用", nil)
	}
	if n == 1 {
		if err := s.redis.Expire(ctx, key, time.Until(nextDay)).Err(); err != nil {
			return avatarError(503, "AVATAR_SMS_UNAVAILABLE", "短信服务暂不可用", nil)
		}
	}
	if n > int64(s.settings.SMSDailyLimit) {
		return avatarError(429, "AVATAR_SMS_DAILY_LIMITED", "今日验证码发送次数已达上限", gin.H{"retryAfter": int(time.Until(nextDay).Seconds())})
	}
	return nil
}

// reserveUploadCredential caps outstanding COS direct-upload grants per
// collection session. Entries expire together with the ten-minute grant, and
// confirmation releases the slot immediately.
func (s *Service) reserveUploadCredential(ctx context.Context, sessionID uint64, token string) *httpx.AppError {
	if s.redis == nil {
		return avatarError(503, "AVATAR_UPLOAD_TOKEN_UNAVAILABLE", "上传凭证暂不可用", nil)
	}
	key := fmt.Sprintf("avatar:upload:pending:%d", sessionID)
	now := time.Now()
	script := redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
local count = redis.call('ZCARD', KEYS[1])
if count >= tonumber(ARGV[2]) then return -1 end
redis.call('ZADD', KEYS[1], ARGV[3], ARGV[4])
redis.call('EXPIRE', KEYS[1], ARGV[5])
return count + 1`)
	result, err := script.Run(ctx, s.redis, []string{key}, now.Unix(), s.settings.UploadCredentialParallel, now.Add(10*time.Minute).Unix(), token, int((10 * time.Minute).Seconds())).Int64()
	if err != nil {
		return avatarError(503, "AVATAR_UPLOAD_TOKEN_UNAVAILABLE", "上传凭证暂不可用", nil)
	}
	if result < 0 {
		return avatarError(429, "AVATAR_UPLOAD_CONCURRENCY_LIMITED", "请等待已有素材上传完成", gin.H{"retryAfter": 1, "maxParallel": s.settings.UploadCredentialParallel})
	}
	return nil
}

func (s *Service) releaseUploadCredential(ctx context.Context, sessionID uint64, token string) {
	if s.redis != nil {
		_ = s.redis.ZRem(ctx, fmt.Sprintf("avatar:upload:pending:%d", sessionID), token).Err()
	}
}

func (s *Service) limitProfilePoll(ctx context.Context, cur Current) *httpx.AppError {
	if s.redis == nil {
		return nil
	}
	key := fmt.Sprintf("avatar:profile-poll:%d", cur.AccountID)
	ok, err := s.redis.SetNX(ctx, key, "1", s.settings.ProfilePollInterval).Result()
	if err != nil {
		return avatarError(503, "AVATAR_RATE_LIMIT_UNAVAILABLE", "状态查询暂不可用", nil)
	}
	if ok {
		return nil
	}
	ttl, err := s.redis.TTL(ctx, key).Result()
	if err != nil || ttl < time.Second {
		ttl = time.Second
	}
	return avatarError(429, "AVATAR_PROFILE_POLL_RATE_LIMITED", "查询过于频繁，请稍后再试", gin.H{"retryAfter": int(ttl.Seconds())})
}
