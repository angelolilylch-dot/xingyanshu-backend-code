// Package evidence 定义真实存证链服务的稳定边界，防止业务模块直接依赖某一家链厂商。
package evidence

import (
	"context"
	"fmt"
)

// SubmitRequest 是提交存证的最小不可篡改材料。Hash 必须来自已确定版本的业务快照或文件。
type SubmitRequest struct {
	RequestNo    string
	ObjectType   string
	ObjectID     uint64
	EvidenceType string
	HashValue    string
}

// SubmitResult 只有返回可验证回执且状态为 SUCCESS 时，业务才可标记为存证成功。
type SubmitResult struct {
	Provider        string
	RequestNo       string
	Status          string
	ReceiptNo       string
	TransactionHash string
}

// Provider 是存证服务适配器；真实服务上线前不能以 Mock 成功替代回执。
type Provider interface {
	Submit(ctx context.Context, request SubmitRequest) (SubmitResult, error)
	Query(ctx context.Context, requestNo string) (SubmitResult, error)
}

// NewProvider 根据环境配置创建存证实现；生产环境尚未接入链服务时只能返回未配置实现。
func NewProvider(provider string) Provider {
	if provider == "mock" {
		return MockProvider{}
	}
	return UnconfiguredProvider{}
}

// UnconfiguredProvider 明确表示尚未接入链服务，避免开发期错误推进为已上链。
type UnconfiguredProvider struct{}

func (UnconfiguredProvider) Submit(context.Context, SubmitRequest) (SubmitResult, error) {
	return SubmitResult{}, fmt.Errorf("evidence provider is not configured")
}

func (UnconfiguredProvider) Query(context.Context, string) (SubmitResult, error) {
	return SubmitResult{}, fmt.Errorf("evidence provider is not configured")
}
