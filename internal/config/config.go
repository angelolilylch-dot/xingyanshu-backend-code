package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App struct {
		Name        string `yaml:"name"`
		Environment string `yaml:"environment"`
		Server      struct {
			Address string `yaml:"address"`
		} `yaml:"server"`
		Worker struct {
			PollInterval time.Duration `yaml:"poll_interval"`
		} `yaml:"worker"`
	} `yaml:"app"`
	Logging       LoggingConfig       `yaml:"logging"`
	Database      DatabaseConfig      `yaml:"database"`
	Redis         RedisConfig         `yaml:"redis"`
	Auth          AuthConfig          `yaml:"auth"`
	CORS          CORSConfig          `yaml:"cors"`
	File          FileConfig          `yaml:"file"`
	ContentSafety ContentSafetyConfig `yaml:"content_safety"`
	Evidence      EvidenceConfig      `yaml:"evidence"`
	Avatar        AvatarConfig        `yaml:"avatar"`
	Bootstrap     BootstrapConfig     `yaml:"bootstrap"`
}

// AvatarConfig controls public support information and abuse-protection limits
// for the independent AVATAR_H5.  The support phone is public contact data,
// not a credential, and is returned only when a registration QR code is invalid.
type AvatarConfig struct {
	FallbackSupportPhone     string        `yaml:"fallback_support_phone"`
	SMSResendInterval        time.Duration `yaml:"sms_resend_interval"`
	SMSDailyLimit            int           `yaml:"sms_daily_limit"`
	UploadCredentialParallel int           `yaml:"upload_credential_parallel"`
	ProfilePollInterval      time.Duration `yaml:"profile_poll_interval"`
}

// LoggingConfig controls local, plain-text operational logs. Credentials are
// always redacted; detailed request/SQL diagnostics are test-only.
type LoggingConfig struct {
	Directory     string `yaml:"directory"`
	RetentionDays int    `yaml:"retention_days"`
	AlsoStdout    bool   `yaml:"also_stdout"`
}

type DatabaseConfig struct {
	Driver   string `yaml:"driver"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Name     string `yaml:"name"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// RedisConfig 保存验证码、会话撤销和短期缓存使用的 Redis 连接信息。
type RedisConfig struct {
	Address  string `yaml:"address"`
	Password string `yaml:"password"`
	Database int    `yaml:"database"`
}

// AuthConfig 保存令牌签发和会话有效期的认证配置。
type AuthConfig struct {
	JWTSecret       string        `yaml:"jwt_secret"`
	AccessTokenTTL  time.Duration `yaml:"access_token_ttl"`
	RefreshTokenTTL time.Duration `yaml:"refresh_token_ttl"`
}

// CORSConfig 保存允许跨域调用 API 的前端来源。空列表表示不开放浏览器跨域访问。
type CORSConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// ContentSafetyConfig 保存腾讯云内容安全连接信息。
// 服务会同步审核用户提交文本；CNT-02 商品还审核关联图片。密钥只允许置于本机私有配置或本机环境变量中。
type ContentSafetyConfig struct {
	Provider  string        `yaml:"provider"`
	Region    string        `yaml:"region"`
	BizType   string        `yaml:"biz_type"`
	SecretID  string        `yaml:"secret_id"`
	SecretKey string        `yaml:"secret_key"`
	Timeout   time.Duration `yaml:"timeout"`
}

// EvidenceConfig 为真实存证链服务预留接入参数。当前尚未确定服务商，禁止用配置伪造成功回执。
type EvidenceConfig struct {
	Provider       string `yaml:"provider"`
	CallbackSecret string `yaml:"callback_secret"`
}

// FileConfig 保存文件存储连接和上传策略。只有 Policy 会被运行时热加载，存储连接配置仍需重启生效。
type FileConfig struct {
	Storage       FileStorageConfig `yaml:"storage"`
	Policy        FilePolicyConfig  `yaml:"policy"`
	SignedURLTTL  time.Duration     `yaml:"signed_url_ttl"`
	WatchInterval time.Duration     `yaml:"watch_interval"`
}

// FileStorageConfig 定义 Mock 或 COS 存储实现的连接参数；COS 凭据只能存于本地私有配置。
type FileStorageConfig struct {
	Provider  string        `yaml:"provider"`
	LocalRoot string        `yaml:"local_root"`
	KeyPrefix string        `yaml:"key_prefix"`
	COS       COSFileConfig `yaml:"cos"`
}

type COSFileConfig struct {
	BucketURL string `yaml:"bucket_url"`
	SecretID  string `yaml:"secret_id"`
	SecretKey string `yaml:"secret_key"`
}

// FilePolicyConfig 将图片、文档和媒体分别限制格式与文件大小，单位为 MB。
type FilePolicyConfig struct {
	Images    FileCategoryPolicy `yaml:"images"`
	Documents FileCategoryPolicy `yaml:"documents"`
	Media     FileCategoryPolicy `yaml:"media"`
}

type FileCategoryPolicy struct {
	Extensions []string `yaml:"extensions"`
	MaxSizeMB  int64    `yaml:"max_size_mb"`
}

// BootstrapConfig 保存首次初始化平台基础数据所需的本地配置。
// 密码仅用于生成 bcrypt 哈希，程序不会把明文写入数据库或日志。
type BootstrapConfig struct {
	PlatformOrgName string `yaml:"platform_org_name"`
	InitialAdmin    struct {
		Username    string `yaml:"username"`
		Password    string `yaml:"password"`
		DisplayName string `yaml:"display_name"`
	} `yaml:"initial_admin"`
}

func Load(path string) (Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	expandEnvironment(&cfg)
	cfg.Logging = normalizeLoggingConfig(cfg.Logging, cfg.App.Environment)
	cfg.File = normalizeFileConfig(cfg.File)
	cfg.Avatar = NormalizeAvatarConfig(cfg.Avatar)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func normalizeLoggingConfig(value LoggingConfig, environment string) LoggingConfig {
	if value.Directory == "" {
		value.Directory = "logs/" + environment
	}
	if value.RetentionDays <= 0 {
		value.RetentionDays = 90
	}
	return value
}

// expandEnvironment 允许本机环境变量填入第三方凭据，避免把密钥提交到配置模板。
func expandEnvironment(cfg *Config) {
	file := &cfg.File
	file.Storage.COS.BucketURL = os.ExpandEnv(file.Storage.COS.BucketURL)
	file.Storage.COS.SecretID = os.ExpandEnv(file.Storage.COS.SecretID)
	file.Storage.COS.SecretKey = os.ExpandEnv(file.Storage.COS.SecretKey)
	cfg.ContentSafety.SecretID = os.ExpandEnv(cfg.ContentSafety.SecretID)
	cfg.ContentSafety.SecretKey = os.ExpandEnv(cfg.ContentSafety.SecretKey)
	cfg.Evidence.CallbackSecret = os.ExpandEnv(cfg.Evidence.CallbackSecret)
}

// DefaultFileConfig 给未显式配置的测试环境提供安全的 Mock 存储与一期上传限制。
func DefaultFileConfig() FileConfig {
	return FileConfig{
		Storage: FileStorageConfig{Provider: "cos", KeyPrefix: "xingyanshu/test"},
		Policy: FilePolicyConfig{
			Images:    FileCategoryPolicy{Extensions: []string{"jpg", "jpeg", "png", "webp"}, MaxSizeMB: 10},
			Documents: FileCategoryPolicy{Extensions: []string{"pdf", "doc", "docx", "xls", "xlsx"}, MaxSizeMB: 30},
			Media:     FileCategoryPolicy{Extensions: []string{"mp4", "mp3", "wav"}, MaxSizeMB: 200},
		},
		SignedURLTTL:  10 * time.Minute,
		WatchInterval: 2 * time.Second,
	}
}

func normalizeFileConfig(value FileConfig) FileConfig {
	defaults := DefaultFileConfig()
	if value.Storage.Provider == "" {
		value.Storage.Provider = defaults.Storage.Provider
	}
	if value.Storage.KeyPrefix == "" {
		value.Storage.KeyPrefix = defaults.Storage.KeyPrefix
	}
	if len(value.Policy.Images.Extensions) == 0 {
		value.Policy.Images = defaults.Policy.Images
	}
	if len(value.Policy.Documents.Extensions) == 0 {
		value.Policy.Documents = defaults.Policy.Documents
	}
	if len(value.Policy.Media.Extensions) == 0 {
		value.Policy.Media = defaults.Policy.Media
	}
	if value.SignedURLTTL <= 0 {
		value.SignedURLTTL = defaults.SignedURLTTL
	}
	if value.WatchInterval <= 0 {
		value.WatchInterval = defaults.WatchInterval
	}
	return value
}

func NormalizeAvatarConfig(value AvatarConfig) AvatarConfig {
	if value.SMSResendInterval <= 0 {
		value.SMSResendInterval = time.Minute
	}
	if value.SMSDailyLimit <= 0 {
		value.SMSDailyLimit = 10
	}
	if value.UploadCredentialParallel <= 0 {
		value.UploadCredentialParallel = 3
	}
	if value.ProfilePollInterval <= 0 {
		value.ProfilePollInterval = 2 * time.Second
	}
	return value
}

func (c Config) Validate() error {
	if c.App.Name == "" || c.App.Environment == "" || c.App.Server.Address == "" {
		return fmt.Errorf("app.name, app.environment and app.server.address are required")
	}
	if (c.Logging.Directory == "") != (c.Logging.RetentionDays == 0) || c.Logging.RetentionDays < 0 {
		return fmt.Errorf("logging.directory and logging.retention_days must be configured together")
	}
	if c.App.Worker.PollInterval <= 0 {
		return fmt.Errorf("app.worker.poll_interval must be positive")
	}
	if c.Database.Driver == "" || c.Database.Host == "" || c.Database.Port <= 0 || c.Database.Name == "" {
		return fmt.Errorf("database.driver, database.host, database.port and database.name are required")
	}
	if c.Redis.Address == "" || c.Auth.JWTSecret == "" || c.Auth.AccessTokenTTL <= 0 || c.Auth.RefreshTokenTTL <= 0 {
		return fmt.Errorf("redis.address, auth.jwt_secret, auth.access_token_ttl and auth.refresh_token_ttl are required")
	}
	if c.App.Environment == "production" {
		if len(c.CORS.AllowedOrigins) == 0 {
			return fmt.Errorf("cors.allowed_origins is required in production")
		}
		for _, origin := range c.CORS.AllowedOrigins {
			if origin == "*" {
				return fmt.Errorf("cors.allowed_origins must not contain wildcard * in production")
			}
		}
		if c.Avatar.FallbackSupportPhone == "" {
			return fmt.Errorf("avatar.fallback_support_phone is required in production")
		}
	}
	if c.Bootstrap.PlatformOrgName == "" || c.Bootstrap.InitialAdmin.Username == "" || c.Bootstrap.InitialAdmin.Password == "" || c.Bootstrap.InitialAdmin.DisplayName == "" {
		return fmt.Errorf("bootstrap.platform_org_name and bootstrap.initial_admin fields are required")
	}
	if c.File.Storage.Provider != "cos" {
		return fmt.Errorf("file.storage.provider must be cos")
	}
	if c.File.Storage.KeyPrefix == "" || c.File.SignedURLTTL <= 0 || c.File.WatchInterval <= 0 {
		return fmt.Errorf("file storage key_prefix, signed_url_ttl and watch_interval are required")
	}
	for _, policy := range []FileCategoryPolicy{c.File.Policy.Images, c.File.Policy.Documents, c.File.Policy.Media} {
		if len(policy.Extensions) == 0 || policy.MaxSizeMB <= 0 {
			return fmt.Errorf("file policy extensions and max_size_mb must be positive")
		}
	}
	cos := c.File.Storage.COS
	// 当前文件服务只支持 COS。启动前完整校验连接信息，避免服务启动到一半才因空 Bucket 地址失败。
	if cos.BucketURL == "" || cos.SecretID == "" || cos.SecretKey == "" {
		return fmt.Errorf("file.storage.cos bucket_url, secret_id and secret_key are required when provider is cos")
	}
	if c.ContentSafety.Provider != "" && c.ContentSafety.Provider != "tencent_tms" && c.ContentSafety.Provider != "mock" {
		return fmt.Errorf("content_safety.provider must be tencent_tms or mock when configured")
	}
	if contentSafetyConfigured(c.ContentSafety) {
		if c.ContentSafety.Provider == "mock" {
			if c.App.Environment != "test" {
				return fmt.Errorf("content_safety.provider mock is only allowed in test environment")
			}
		} else if c.ContentSafety.Provider != "tencent_tms" || c.ContentSafety.Region == "" || c.ContentSafety.BizType == "" || c.ContentSafety.SecretID == "" || c.ContentSafety.SecretKey == "" {
			return fmt.Errorf("content_safety provider, region, biz_type, secret_id and secret_key are required when configured")
		} else if c.ContentSafety.Timeout <= 0 {
			return fmt.Errorf("content_safety.timeout must be positive when configured")
		}
	}
	if c.Evidence.Provider != "" && c.Evidence.Provider != "mock" {
		return fmt.Errorf("evidence.provider is unsupported: %s", c.Evidence.Provider)
	}
	if c.Evidence.Provider == "mock" && (c.App.Environment != "test" || c.Evidence.CallbackSecret == "") {
		return fmt.Errorf("evidence mock is only allowed in test with callback_secret")
	}
	return nil
}

// contentSafetyConfigured 判断是否已开始填写腾讯云审核配置，避免配置半成品时静默降级为未启用。
func contentSafetyConfigured(value ContentSafetyConfig) bool {
	return value.Provider != "" || value.Region != "" || value.BizType != "" || value.SecretID != "" || value.SecretKey != "" || value.Timeout != 0
}
