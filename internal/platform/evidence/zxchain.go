package evidence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	wallet "git.woa.com/zhixinlian/nft_wallet_sdk"

	"xingyanshu-backend/internal/config"
)

const zxChainProviderName = "ZXCHAIN"

type zxChainSigner interface {
	APISign(secretID, secretKey string) (signature, signatureTime string, nonce int, err error)
	Sign(privateKey, plaintext string) (string, error)
}

type walletSigner struct{}

func (walletSigner) APISign(secretID, secretKey string) (string, string, int, error) {
	result, err := wallet.GenerateApiSign(secretID, secretKey)
	if err != nil {
		return "", "", 0, err
	}
	return result.Signature, result.SignatureTime, result.Nonce, nil
}

func (walletSigner) Sign(privateKey, plaintext string) (string, error) {
	return wallet.SignByPriKey(privateKey, plaintext)
}

// ZXChainProvider 调用至信链可信存证接口；签名由厂商 SDK 生成，私钥不会离开进程内存。
type ZXChainProvider struct {
	endpoint   string
	secretID   string
	secretKey  string
	publicKey  string
	privateKey string
	client     *http.Client
	signer     zxChainSigner
}

func NewZXChainProvider(cfg config.EvidenceConfig) (*ZXChainProvider, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" || cfg.SecretID == "" || cfg.SecretKey == "" || cfg.PublicKey == "" || cfg.PrivateKey == "" || cfg.Timeout <= 0 {
		return nil, fmt.Errorf("zxchain configuration is incomplete")
	}
	return newZXChainProvider(cfg.Endpoint, cfg.SecretID, cfg.SecretKey, cfg.PublicKey, cfg.PrivateKey, cfg.Timeout, walletSigner{}), nil
}

func newZXChainProvider(endpoint, secretID, secretKey, publicKey, privateKey string, timeout time.Duration, signer zxChainSigner) *ZXChainProvider {
	return &ZXChainProvider{endpoint: strings.TrimRight(endpoint, "/"), secretID: secretID, secretKey: secretKey, publicKey: publicKey, privateKey: privateKey, client: &http.Client{Timeout: timeout}, signer: signer}
}

func (p *ZXChainProvider) Submit(ctx context.Context, request SubmitRequest) (SubmitResult, error) {
	plainText := p.publicKey + "_" + request.HashValue + "_"
	signature, err := p.signer.Sign(p.privateKey, plainText)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("sign evidence payload: %w", err)
	}
	var response struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Data    struct {
			EvID        string `json:"evId"`
			TxID        string `json:"txId"`
			BlockHeight any    `json:"blockHeight"`
		} `json:"data"`
	}
	if err := p.doJSON(ctx, http.MethodPost, "/api/v1/ev/save", map[string]any{"publicKey": p.publicKey, "hash": request.HashValue, "sign": signature}, &response); err != nil {
		return SubmitResult{}, err
	}
	if response.RetCode != 0 || response.Data.EvID == "" || response.Data.TxID == "" {
		return SubmitResult{}, zxChainResponseError{Code: response.RetCode, Message: response.RetMsg}
	}
	return SubmitResult{Provider: zxChainProviderName, RequestNo: request.RequestNo, Status: "SUCCESS", ReceiptNo: response.Data.EvID, TransactionHash: response.Data.TxID, BlockHeight: optionalString(response.Data.BlockHeight)}, nil
}

func (p *ZXChainProvider) Query(ctx context.Context, evID string) (SubmitResult, error) {
	var response struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Data    struct {
			EvID        string `json:"evId"`
			TxID        string `json:"txId"`
			BlockHeight any    `json:"blockHeight"`
		} `json:"data"`
	}
	if err := p.doJSON(ctx, http.MethodGet, "/api/v1/ev/query?evId="+url.QueryEscape(evID), nil, &response); err != nil {
		return SubmitResult{}, err
	}
	if response.RetCode != 0 || response.Data.EvID == "" {
		return SubmitResult{}, zxChainResponseError{Code: response.RetCode, Message: response.RetMsg}
	}
	return SubmitResult{Provider: zxChainProviderName, Status: "SUCCESS", ReceiptNo: response.Data.EvID, TransactionHash: response.Data.TxID, BlockHeight: optionalString(response.Data.BlockHeight)}, nil
}

func (p *ZXChainProvider) RequestCertificate(ctx context.Context, evID string) (CertificateResult, error) {
	var response struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Data    struct {
			Status any `json:"status"`
		} `json:"data"`
	}
	if err := p.doJSON(ctx, http.MethodPost, "/api/v1/ev/cert", map[string]any{"evId": evID}, &response); err != nil {
		return CertificateResult{}, err
	}
	if response.RetCode != 0 {
		return CertificateResult{}, zxChainResponseError{Code: response.RetCode, Message: response.RetMsg}
	}
	return CertificateResult{Provider: zxChainProviderName, Status: "PENDING", ProviderStatus: optionalString(response.Data.Status)}, nil
}

func (p *ZXChainProvider) QueryCertificate(ctx context.Context, evID string) (CertificateResult, error) {
	var response struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Data    struct {
			Status  any    `json:"status"`
			CertURL string `json:"certUrl"`
		} `json:"data"`
	}
	if err := p.doJSON(ctx, http.MethodGet, "/api/v1/ev/cert?evId="+url.QueryEscape(evID), nil, &response); err != nil {
		return CertificateResult{}, err
	}
	if response.RetCode != 0 {
		return CertificateResult{}, zxChainResponseError{Code: response.RetCode, Message: response.RetMsg}
	}
	status := "PENDING"
	if strings.TrimSpace(response.Data.CertURL) != "" {
		status = "READY"
	}
	return CertificateResult{Provider: zxChainProviderName, Status: status, ProviderStatus: optionalString(response.Data.Status), CertURL: response.Data.CertURL}, nil
}

func (p *ZXChainProvider) doJSON(ctx context.Context, method, path string, body any, target any) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode zxchain request: %w", err)
		}
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		signature, signatureTime, nonce, signErr := p.signer.APISign(p.secretID, p.secretKey)
		if signErr != nil {
			return fmt.Errorf("sign zxchain api request: %w", signErr)
		}
		request, requestErr := http.NewRequestWithContext(ctx, method, p.endpoint+path, bytes.NewReader(encoded))
		if requestErr != nil {
			return fmt.Errorf("create zxchain request: %w", requestErr)
		}
		request.Header.Set("Content-Type", "application/json;charset=utf-8")
		request.Header.Set("Secret-Id", p.secretID)
		request.Header.Set("Signature-Time", signatureTime)
		request.Header.Set("Signature", signature)
		request.Header.Set("Nonce", strconv.Itoa(nonce))
		response, callErr := p.client.Do(request)
		if callErr != nil {
			return fmt.Errorf("call zxchain: %w", callErr)
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read zxchain response: %w", readErr)
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf("zxchain http status %d", response.StatusCode)
		}
		var envelope struct {
			RetCode int `json:"retCode"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("decode zxchain response: %w", err)
		}
		if envelope.RetCode == 140003 && attempt == 0 {
			lastErr = zxChainResponseError{Code: 140003, Message: "signature expired"}
			continue
		}
		if err := json.Unmarshal(payload, target); err != nil {
			return fmt.Errorf("decode zxchain response: %w", err)
		}
		return nil
	}
	return lastErr
}

type zxChainResponseError struct {
	Code    int
	Message string
}

func (e zxChainResponseError) Error() string {
	return fmt.Sprintf("zxchain retCode=%d retMsg=%s", e.Code, e.Message)
}

func optionalString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
