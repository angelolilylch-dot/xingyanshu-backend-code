package identity

import (
	"context"
	"fmt"
	"github.com/mojocn/base64Captcha"
	"github.com/redis/go-redis/v9"
	"strings"
	"time"
)

const captchaTTL = 5 * time.Minute

// CaptchaResponse 是图形验证码接口返回给管理后台的公开数据。
type CaptchaResponse struct {
	CaptchaID   string `json:"captchaId"`
	ImageBase64 string `json:"imageBase64"`
	ExpiresAt   string `json:"expiresAt"`
}

// CaptchaService 负责生成验证码并将答案仅保存到 Redis。
type CaptchaService struct {
	redis     *redis.Client
	generator *base64Captcha.Captcha
}

// NewCaptchaService 创建六位数字验证码服务。
func NewCaptchaService(redisClient *redis.Client) *CaptchaService {
	driver := base64Captcha.NewDriverDigit(80, 240, 6, 0.7, 80)
	return &CaptchaService{redis: redisClient, generator: base64Captcha.NewCaptcha(driver, base64Captcha.NewMemoryStore(1, time.Minute))}
}

// Create 生成验证码图片和随机答案，答案写入 Redis 后不再返回调用方。
func (s *CaptchaService) Create(ctx context.Context) (CaptchaResponse, error) {
	id, image, answer, err := s.generator.Generate()
	if err != nil {
		return CaptchaResponse{}, fmt.Errorf("generate captcha: %w", err)
	}
	if err := s.redis.Set(ctx, "captcha:"+id, strings.ToLower(answer), captchaTTL).Err(); err != nil {
		return CaptchaResponse{}, fmt.Errorf("store captcha: %w", err)
	}
	expiresAt := time.Now().Add(captchaTTL).Format(time.RFC3339)
	return CaptchaResponse{CaptchaID: id, ImageBase64: image, ExpiresAt: expiresAt}, nil
}

// Verify 校验并删除验证码，任何一次校验都会使该验证码失效。
func (s *CaptchaService) Verify(ctx context.Context, id, answer string) (bool, error) {
	key := "captcha:" + id
	// Lua 在旧版 Redis 上同样原子执行“读取后删除”，避免依赖 Redis 6.2 的 GETDEL。
	expected, err := s.redis.Eval(ctx, "local v=redis.call('GET',KEYS[1]); if v then redis.call('DEL',KEYS[1]); end; return v", []string{key}).Text()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read captcha: %w", err)
	}
	return strings.EqualFold(expected, strings.TrimSpace(answer)), nil
}
