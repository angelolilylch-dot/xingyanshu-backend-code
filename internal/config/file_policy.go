package config

import (
	"context"
	"log"
	"os"
	"sync/atomic"
	"time"

	"xingyanshu-backend/internal/platform/logx"
)

// FilePolicyProvider 保存当前生效的上传策略，并在配置文件变化后原子替换。
type FilePolicyProvider struct{ value atomic.Value }

func NewFilePolicyProvider(policy FilePolicyConfig) *FilePolicyProvider {
	provider := &FilePolicyProvider{}
	provider.value.Store(policy)
	return provider
}

func (p *FilePolicyProvider) Current() FilePolicyConfig {
	return p.value.Load().(FilePolicyConfig)
}

// WatchFilePolicy 定时检查配置文件修改时间。仅策略热加载，连接与密钥等基础配置不会改变。
func WatchFilePolicy(ctx context.Context, path string, interval time.Duration, provider *FilePolicyProvider) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var lastModified time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				info, err := os.Stat(path)
				if err != nil || (!lastModified.IsZero() && !info.ModTime().After(lastModified)) {
					continue
				}
				cfg, err := Load(path)
				if err != nil {
					logx.Errorf("module=config action=file_policy_reload stage=config.load error_type=%T error=%q", err, logx.Redact(err.Error()))
					log.Printf("file policy reload failed: %v; keeping previous policy", err)
					continue
				}
				provider.value.Store(cfg.File.Policy)
				lastModified = info.ModTime()
				log.Printf("file policy reloaded from %s", path)
			}
		}
	}()
}
