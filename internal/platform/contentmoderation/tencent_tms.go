package contentmoderation

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	common "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	tms "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tms/v20201229"

	"xingyanshu-backend/internal/config"
)

// TencentTMSProvider 使用腾讯云 TextModeration 进行真实的同步文本审核。
type TencentTMSProvider struct {
	client  *tms.Client
	bizType string
}

// NewTencentTMSProvider 校验完整配置并创建 SDK 客户端；缺少凭据时返回错误，绝不静默放行文本。
func NewTencentTMSProvider(cfg config.ContentSafetyConfig) (*TencentTMSProvider, error) {
	if cfg.Provider != "tencent_tms" || cfg.Region == "" || cfg.BizType == "" || cfg.SecretID == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("incomplete Tencent TMS configuration")
	}
	client, err := tms.NewClient(common.NewCredential(cfg.SecretID, cfg.SecretKey), cfg.Region, profile.NewClientProfile())
	if err != nil {
		return nil, fmt.Errorf("create Tencent TMS client: %w", err)
	}
	return &TencentTMSProvider{client: client, bizType: cfg.BizType}, nil
}

// Moderate 将 UTF-8 文本编码为腾讯云规定的 Base64 后同步调用接口，并将处置建议映射为平台固定结论。
func (p *TencentTMSProvider) Moderate(ctx context.Context, input Request) (Result, error) {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return Result{}, fmt.Errorf("text is required")
	}
	if len([]rune(text)) > 10000 {
		return Result{}, fmt.Errorf("text exceeds Tencent TMS 10000 unicode character limit")
	}
	request := tms.NewTextModerationRequest()
	request.Content = common.StringPtr(base64.StdEncoding.EncodeToString([]byte(text)))
	request.BizType = common.StringPtr(p.bizType)
	request.DataId = common.StringPtr(input.DataID)
	request.Type = common.StringPtr("TEXT")
	if input.Language == "" {
		input.Language = "zh"
	}
	request.SourceLanguage = common.StringPtr(input.Language)
	if input.SessionID != "" {
		request.SessionId = common.StringPtr(input.SessionID)
	}
	response, err := p.client.TextModerationWithContext(ctx, request)
	if err != nil {
		return Result{}, fmt.Errorf("Tencent TMS TextModeration: %w", err)
	}
	if response == nil || response.Response == nil || response.Response.Suggestion == nil || response.Response.RequestId == nil {
		return Result{}, fmt.Errorf("Tencent TMS returned incomplete response")
	}
	return Result{
		Provider:          "TENCENT_TMS",
		ProviderRequestNo: deref(response.Response.RequestId),
		Decision:          decisionFromSuggestion(deref(response.Response.Suggestion)),
		Label:             deref(response.Response.Label),
		SubLabel:          deref(response.Response.SubLabel),
		Score:             derefInt(response.Response.Score),
	}, nil
}

// decisionFromSuggestion 将厂商建议收敛为平台状态机可识别的三种结论；未知值一律按复核处理。
func decisionFromSuggestion(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "PASS":
		return DecisionPass
	case "BLOCK":
		return DecisionBlock
	default:
		return DecisionReview
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefInt(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
