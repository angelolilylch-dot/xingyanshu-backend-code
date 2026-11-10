package evidence

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	wallet "git.woa.com/zhixinlian/nft_wallet_sdk"

	"xingyanshu-backend/internal/config"
)

// TestZXChainLiveIntegration 仅在明确开启时访问真实至信链。
// 使用固定非业务文本的 SHA-256，不提交用户、合同或文件内容。
func TestZXChainLiveIntegration(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_ZXCHAIN_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_ZXCHAIN_TESTS=1 to run the live zxchain integration test")
	}
	cfg, err := config.Load("../../../config/test.yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Evidence.Provider != "zxchain" {
		t.Fatalf("evidence provider=%q, want zxchain", cfg.Evidence.Provider)
	}
	preflight := "xingyanshu-zxchain-key-pair-preflight"
	signature, err := wallet.SignByPriKey(cfg.Evidence.PrivateKey, preflight)
	if err != nil {
		t.Fatalf("sign private key preflight: %v", err)
	}
	matched, err := wallet.VerifyByPubKey(cfg.Evidence.PublicKey, signature, preflight)
	if err != nil || !matched {
		t.Fatalf("public/private key pair preflight failed")
	}
	provider, err := NewProvider(cfg.Evidence)
	if err != nil {
		t.Fatalf("new zxchain provider: %v", err)
	}
	hash := sha256.Sum256([]byte("xingyanshu-zxchain-live-integration-v1"))
	saved, err := provider.Submit(context.Background(), SubmitRequest{RequestNo: "ZXCHAIN-LIVE-INTEGRATION", ObjectType: "INTEGRATION_TEST", ObjectID: 1, EvidenceType: "INTEGRATION_TEST", HashValue: fmt.Sprintf("%x", hash)})
	if err != nil {
		t.Fatalf("save evidence: %v", err)
	}
	if saved.ReceiptNo == "" || saved.TransactionHash == "" {
		t.Fatalf("save returned incomplete receipt: %#v", saved)
	}
	var queryErr error
	for attempt := 0; attempt < 5; attempt++ {
		if _, queryErr = provider.Query(context.Background(), saved.ReceiptNo); queryErr == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if queryErr != nil {
		t.Fatalf("query evidence after bounded retry: %v", queryErr)
	}
	if _, err := provider.RequestCertificate(context.Background(), saved.ReceiptNo); err != nil {
		t.Fatalf("request certificate: %v", err)
	}
	// 证书生成可能是异步的；本次只确认查询接口可访问，不要求立即获得 certUrl。
	certificate, err := provider.QueryCertificate(context.Background(), saved.ReceiptNo)
	if err != nil {
		t.Fatalf("query certificate: %v", err)
	}
	t.Logf("certificate status=%s url=%s", certificate.Status, certificate.CertURL)
}

// TestZXChainLiveCertificateDisplay 仅读取固定联调 Hash 的证书，不再创建新的链上记录。
func TestZXChainLiveCertificateDisplay(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_ZXCHAIN_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_ZXCHAIN_TESTS=1 to query the live zxchain certificate")
	}
	cfg, err := config.Load("../../../config/test.yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	provider, err := NewProvider(cfg.Evidence)
	if err != nil {
		t.Fatalf("new zxchain provider: %v", err)
	}
	zx, ok := provider.(*ZXChainProvider)
	if !ok {
		t.Fatalf("provider is not zxchain")
	}
	hash := sha256.Sum256([]byte("xingyanshu-zxchain-live-integration-v1"))
	var response struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Data    struct {
			EvID string `json:"evId"`
		} `json:"data"`
	}
	var queryErr error
	for attempt := 0; attempt < 5; attempt++ {
		queryErr = zx.doJSON(context.Background(), http.MethodGet, "/api/v1/ev/query?hash="+url.QueryEscape(fmt.Sprintf("%x", hash)), nil, &response)
		if queryErr == nil && response.RetCode == 0 && response.Data.EvID != "" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if queryErr != nil || response.RetCode != 0 || response.Data.EvID == "" {
		t.Fatalf("query fixed integration evidence: retCode=%d err=%v", response.RetCode, queryErr)
	}
	t.Logf("evidence evId=%s", response.Data.EvID)
	certificate, err := zx.QueryCertificate(context.Background(), response.Data.EvID)
	if err != nil {
		t.Fatalf("query certificate: %v", err)
	}
	t.Logf("certificate status=%s url=%s", certificate.Status, certificate.CertURL)
}
