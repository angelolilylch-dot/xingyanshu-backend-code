// Package contentmoderation 提供用户主动提交文本的第三方内容审核适配器。
package contentmoderation

import (
	"context"
	"fmt"

	"xingyanshu-backend/internal/config"
)

const (
	DecisionPass   = "PASS"
	DecisionReview = "REVIEW"
	DecisionBlock  = "BLOCK"
)

// Request 是领域服务调用内容审核时的统一输入，不把腾讯云 SDK 类型泄漏到业务模块。
type Request struct {
	Text      string
	DataID    string
	SessionID string
	UserID    string
	Language  string
}

// Result 是统一审核结论。RequestID 用于审计与腾讯云问题定位；业务库不保存原始文本或命中词。
type Result struct {
	Provider          string
	ProviderRequestNo string
	Decision          string
	Label             string
	SubLabel          string
	Score             int64
}

// Provider 屏蔽具体厂商差异，后续 IP、作品、商品与活动只依赖此接口。
type Provider interface {
	Moderate(ctx context.Context, request Request) (Result, error)
}

// NewProvider 根据配置选择真实审核服务。未配置时返回失败实现，避免业务接口静默跳过文本审核。
func NewProvider(cfg config.ContentSafetyConfig) Provider {
	provider, err := NewTencentTMSProvider(cfg)
	if err == nil {
		return provider
	}
	return unavailableProvider{err: err}
}

type unavailableProvider struct{ err error }

func (p unavailableProvider) Moderate(context.Context, Request) (Result, error) {
	return Result{}, fmt.Errorf("content moderation unavailable: %w", p.err)
}
