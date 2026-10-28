package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.yaml")
	content := []byte("app:\n  name: xingyanshu\n  environment: test\n  server:\n    address: ':8080'\n  worker:\n    poll_interval: 5s\ndatabase:\n  driver: mysql\n  host: 127.0.0.1\n  port: 3306\n  name: xingyanshu_test\nredis:\n  address: 127.0.0.1:6379\nauth:\n  jwt_secret: test_secret\n  access_token_ttl: 2h\n  refresh_token_ttl: 168h\nfile:\n  storage:\n    provider: cos\n    key_prefix: xingyanshu/test\n    cos:\n      bucket_url: https://test-0.cos.ap-guangzhou.myqcloud.com\n      secret_id: test-secret-id\n      secret_key: test-secret-key\nbootstrap:\n  platform_org_name: 测试平台\n  initial_admin:\n    username: test_admin\n    password: Admin@123456\n    display_name: 测试管理员\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App.Worker.PollInterval.String() != "5s" || cfg.Database.Name != "xingyanshu_test" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestLoadAppliesDatabaseEnvironmentOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.yaml")
	content := []byte("app:\n  name: xingyanshu\n  environment: test\n  server:\n    address: ':8080'\n  worker:\n    poll_interval: 5s\ndatabase:\n  driver: mysql\n  host: 127.0.0.1\n  port: 3306\n  name: local\nredis:\n  address: 127.0.0.1:6379\nauth:\n  jwt_secret: test_secret\n  access_token_ttl: 2h\n  refresh_token_ttl: 168h\nfile:\n  storage:\n    provider: cos\n    key_prefix: test\n    cos:\n      bucket_url: https://test-0.cos.ap-guangzhou.myqcloud.com\n      secret_id: test-id\n      secret_key: test-key\nbootstrap:\n  platform_org_name: 测试平台\n  initial_admin:\n    username: test_admin\n    password: Admin@123456\n    display_name: 测试管理员\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XINGYANSHU_DB_HOST", "db.test.internal")
	t.Setenv("XINGYANSHU_DB_NAME", "remote_test")
	t.Setenv("XINGYANSHU_DB_USERNAME", "remote_user")
	t.Setenv("XINGYANSHU_DB_PASSWORD", "test-password")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Host != "db.test.internal" || cfg.Database.Name != "remote_test" || cfg.Database.Username != "remote_user" || cfg.Database.Password != "test-password" {
		t.Fatalf("database environment overrides not applied")
	}
}

// TestContentSafetyConfigRejectsPartialCredentials 防止误填一半密钥后运行时静默跳过真实文本审核。
func TestContentSafetyConfigRejectsPartialCredentials(t *testing.T) {
	cfg := validTestConfig()
	cfg.ContentSafety = ContentSafetyConfig{Provider: "tencent_tms", Region: "ap-guangzhou", BizType: "ip_center", SecretID: "only-id", Timeout: 10 * time.Second}
	if err := cfg.Validate(); err == nil {
		t.Fatal("partial Tencent TMS configuration must be rejected")
	}
}

func TestContentSafetyMockOnlyAllowedInTest(t *testing.T) {
	cfg := validTestConfig()
	cfg.ContentSafety = ContentSafetyConfig{Provider: "mock"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("mock provider should be valid in test: %v", err)
	}
	cfg.App.Environment = "production"
	if err := cfg.Validate(); err == nil {
		t.Fatal("mock provider must be rejected outside test")
	}
}

func TestRosterIdentityTencentRequiresCompleteConfiguration(t *testing.T) {
	cfg := validTestConfig()
	cfg.Avatar.RosterIdentityProvider = "tencent_phone_verification"
	cfg.Avatar.RosterIdentity = RosterIdentityConfig{Region: "ap-guangzhou", Timeout: 10 * time.Second}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Tencent roster identity provider must require credentials")
	}
	cfg.Avatar.RosterIdentity.SecretID = "test-id"
	cfg.Avatar.RosterIdentity.SecretKey = "test-key"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("complete Tencent roster identity config should be valid: %v", err)
	}
	cfg.Avatar.RosterIdentity.VerifyMode = "2"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unsupported roster identity verify mode must be rejected")
	}
}

func TestFileStorageMockOnlyAllowedInTest(t *testing.T) {
	cfg := validTestConfig()
	cfg.File.Storage = FileStorageConfig{Provider: "mock", KeyPrefix: "xingyanshu/test", LocalRoot: t.TempDir(), MockUploadBaseURL: "http://127.0.0.1:8080"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("mock storage should be valid in test: %v", err)
	}
	cfg.App.Environment = "production"
	if err := cfg.Validate(); err == nil {
		t.Fatal("mock storage must be rejected outside test")
	}
}

func TestAvatarAssetStorageCanUseMockWithoutReplacingGlobalCOS(t *testing.T) {
	cfg := validTestConfig()
	cfg.Avatar.AssetStorage = &FileStorageConfig{Provider: "mock", LocalRoot: t.TempDir(), MockUploadBaseURL: "http://127.0.0.1:8080"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("isolated avatar mock storage should be valid in test: %v", err)
	}
	if cfg.File.Storage.Provider != "cos" {
		t.Fatalf("global storage must remain COS, got %q", cfg.File.Storage.Provider)
	}
	cfg.App.Environment = "production"
	if err := cfg.Validate(); err == nil {
		t.Fatal("avatar mock storage must be rejected outside test")
	}
}

func validTestConfig() Config {
	var cfg Config
	cfg.App.Name = "xingyanshu"
	cfg.App.Environment = "test"
	cfg.App.Server.Address = ":8080"
	cfg.App.Worker.PollInterval = 5 * time.Second
	cfg.Database = DatabaseConfig{Driver: "mysql", Host: "127.0.0.1", Port: 3306, Name: "xingyanshu_test"}
	cfg.Redis.Address = "127.0.0.1:6379"
	cfg.Auth = AuthConfig{JWTSecret: "test_secret", AccessTokenTTL: time.Hour, RefreshTokenTTL: time.Hour}
	cfg.File = DefaultFileConfig()
	cfg.File.Storage.COS = COSFileConfig{BucketURL: "https://test-0.cos.ap-guangzhou.myqcloud.com", SecretID: "test-secret-id", SecretKey: "test-secret-key"}
	cfg.Bootstrap.PlatformOrgName = "测试平台"
	cfg.Bootstrap.InitialAdmin.Username = "test_admin"
	cfg.Bootstrap.InitialAdmin.Password = "Admin@123456"
	cfg.Bootstrap.InitialAdmin.DisplayName = "测试管理员"
	return cfg
}

// TestWatchFilePolicy 验证只热加载上传策略，配置写错时不会替换当前可用策略。
func TestWatchFilePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-policy.yaml")
	writePolicyConfig(t, path, 10)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	provider := NewFilePolicyProvider(cfg.File.Policy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	WatchFilePolicy(ctx, path, 10*time.Millisecond, provider)
	time.Sleep(20 * time.Millisecond)
	writePolicyConfig(t, path, 11)
	deadline := time.Now().Add(500 * time.Millisecond)
	for provider.Current().Images.MaxSizeMB != 11 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if provider.Current().Images.MaxSizeMB != 11 {
		t.Fatalf("image policy was not reloaded: %#v", provider.Current())
	}
}

func writePolicyConfig(t *testing.T, path string, imageMaxMB int) {
	t.Helper()
	content := []byte("app:\n  name: xingyanshu\n  environment: test\n  server:\n    address: ':8080'\n  worker:\n    poll_interval: 5s\ndatabase:\n  driver: mysql\n  host: 127.0.0.1\n  port: 3306\n  name: xingyanshu_test\nredis:\n  address: 127.0.0.1:6379\nauth:\n  jwt_secret: test_secret\n  access_token_ttl: 2h\n  refresh_token_ttl: 168h\nfile:\n  storage:\n    provider: cos\n    key_prefix: xingyanshu/test\n    cos:\n      bucket_url: https://test-0.cos.ap-guangzhou.myqcloud.com\n      secret_id: test-secret-id\n      secret_key: test-secret-key\n  policy:\n    images:\n      extensions: [jpg]\n      max_size_mb: " + fmt.Sprint(imageMaxMB) + "\n    documents:\n      extensions: [pdf]\n      max_size_mb: 30\n    media:\n      extensions: [mp4]\n      max_size_mb: 200\nbootstrap:\n  platform_org_name: 测试平台\n  initial_admin:\n    username: test_admin\n    password: Admin@123456\n    display_name: 测试管理员\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
