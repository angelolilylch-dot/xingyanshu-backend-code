// Package evidence 定义真实存证链服务的稳定边界，防止业务模块直接依赖某一家链厂商。
package evidence

import (
	"context"
	"fmt"

	"xingyanshu-backend/internal/config"
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
	BlockHeight     string
}

// CertificateResult 是链上存证证书的受控元数据；CertURL 只能由后端认证接口按权限返回。
type CertificateResult struct {
	Provider       string
	Status         string
	ProviderStatus string
	CertURL        string
}

// Provider 是存证服务适配器；真实服务上线前不能以 Mock 成功替代回执。
type Provider interface {
	Submit(ctx context.Context, request SubmitRequest) (SubmitResult, error)
	Query(ctx context.Context, requestNo string) (SubmitResult, error)
	RequestCertificate(ctx context.Context, receiptNo string) (CertificateResult, error)
	QueryCertificate(ctx context.Context, receiptNo string) (CertificateResult, error)
}

// NewProvider 根据环境配置创建存证实现；生产环境尚未接入链服务时只能返回未配置实现。
func NewProvider(cfg config.EvidenceConfig) (Provider, error) {
	switch cfg.Provider {
	case "mock":
		return MockProvider{}, nil
	case "zxchain":
		return NewZXChainProvider(cfg)
	default:
		return UnconfiguredProvider{}, nil
	}
}

// UnconfiguredProvider 明确表示尚未接入链服务，避免开发期错误推进为已上链。
type UnconfiguredProvider struct{}

func (UnconfiguredProvider) Submit(context.Context, SubmitRequest) (SubmitResult, error) {
	return SubmitResult{}, fmt.Errorf("evidence provider is not configured")
}

func (UnconfiguredProvider) Query(context.Context, string) (SubmitResult, error) {
	return SubmitResult{}, fmt.Errorf("evidence provider is not configured")
}

func (UnconfiguredProvider) RequestCertificate(context.Context, string) (CertificateResult, error) {
	return CertificateResult{}, fmt.Errorf("evidence provider is not configured")
}

func (UnconfiguredProvider) QueryCertificate(context.Context, string) (CertificateResult, error) {
	return CertificateResult{}, fmt.Errorf("evidence provider is not configured")
}
