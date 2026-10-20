// Package contentmoderation 提供用户主动提交文本的第三方内容审核适配器。
package contentmoderation

import (
	"context"
	"fmt"
	"strings"

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

// ImageRequest 是图片内容审核的统一输入。URL 必须是服务端生成的短期签名地址，
// 不能由客户端直接传入，避免审核服务访问任意外部地址。
type ImageRequest struct {
	URL    string
	DataID string
	UserID string
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

// ImageProvider 是图片审核扩展。商品模块要求该能力；既有只审核文本的模块不受影响。
type ImageProvider interface {
	ModerateImage(ctx context.Context, request ImageRequest) (Result, error)
}

// NewProvider 根据配置选择真实审核服务。未配置时返回失败实现，避免业务接口静默跳过内容审核。
func NewProvider(cfg config.ContentSafetyConfig) Provider {
	if cfg.Provider == "mock" {
		return mockProvider{}
	}
	provider, err := NewTencentTMSProvider(cfg)
	if err == nil {
		return provider
	}
	return unavailableProvider{err: err}
}

// mockProvider 仅供本地测试环境使用，模拟审核通过，避免联调必须依赖外部审核服务。
// 生产环境是否允许启用由 config.Validate 按运行环境拦截。
type mockProvider struct{}

func (mockProvider) Moderate(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(request.Text) == "" {
		return Result{}, fmt.Errorf("text is required")
	}
	return Result{Provider: "MOCK", ProviderRequestNo: "MOCK-" + request.DataID, Decision: DecisionPass}, nil
}

func (mockProvider) ModerateImage(ctx context.Context, request ImageRequest) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(request.URL) == "" {
		return Result{}, fmt.Errorf("image URL is required")
	}
	return Result{Provider: "MOCK", ProviderRequestNo: "MOCK-IMAGE-" + request.DataID, Decision: DecisionPass}, nil
}

type unavailableProvider struct{ err error }

func (p unavailableProvider) Moderate(context.Context, Request) (Result, error) {
	return Result{}, fmt.Errorf("content moderation unavailable: %w", p.err)
}

func (p unavailableProvider) ModerateImage(context.Context, ImageRequest) (Result, error) {
	return Result{}, fmt.Errorf("content moderation unavailable: %w", p.err)
}
