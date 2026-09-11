package contentmoderation

import (
	"context"
	"os"
	"testing"

	"xingyanshu-backend/internal/config"
)

// TestTencentTMSWithRealCredentials 仅在显式开启后请求腾讯云，用于验证测试密钥、BizType 和网络连通性。
// 普通单元测试不调用外部服务，避免无意产生费用或因网络波动失败。
func TestTencentTMSWithRealCredentials(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_TMS_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_TMS_TESTS=1 to call Tencent TMS")
	}
	cfg, err := config.Load("../../../config/cos-test.yaml")
	if err != nil {
		t.Fatalf("load TMS test configuration: %v", err)
	}
	provider, err := NewTencentTMSProvider(cfg.ContentSafety)
	if err != nil {
		t.Fatalf("create Tencent TMS provider: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ContentSafety.Timeout)
	defer cancel()
	result, err := provider.Moderate(ctx, Request{Text: "星衍树城市文化 IP 入库测试文本。", DataID: "xingyanshu-tms-integration", Language: "zh"})
	if err != nil {
		t.Fatalf("call Tencent TMS: %v", err)
	}
	if result.Provider != "TENCENT_TMS" || result.ProviderRequestNo == "" || result.Decision == "" {
		t.Fatalf("unexpected Tencent TMS result: %#v", result)
	}
}
