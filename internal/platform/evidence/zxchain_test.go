package evidence

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testZXChainSigner struct{}

func (testZXChainSigner) APISign(_, _ string) (string, string, int, error) {
	return "api-signature", "1700000000000", 123456, nil
}
func (testZXChainSigner) Sign(_, plaintext string) (string, error) {
	return "business-signature-" + plaintext, nil
}

func TestZXChainProviderUsesSignedEvidenceAndCertificateEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Secret-Id") != "secret-id" || r.Header.Get("Signature") != "api-signature" || r.Header.Get("Nonce") != "123456" {
			t.Fatalf("missing zxchain authentication headers")
		}
		switch r.URL.Path {
		case "/api/v1/ev/save":
			if r.Method != http.MethodPost {
				t.Fatalf("save method=%s", r.Method)
			}
			w.Write([]byte(`{"retCode":0,"retMsg":"OK","data":{"evId":"ev-1","txId":"tx-1","blockHeight":8}}`))
		case "/api/v1/ev/query":
			if r.URL.Query().Get("evId") != "ev-1" {
				t.Fatalf("query evId=%q", r.URL.Query().Get("evId"))
			}
			w.Write([]byte(`{"retCode":0,"retMsg":"OK","data":{"evId":"ev-1","txId":"tx-1","blockHeight":"8"}}`))
		case "/api/v1/ev/cert":
			if r.Method == http.MethodPost {
				w.Write([]byte(`{"retCode":0,"retMsg":"OK","data":{"status":1}}`))
				return
			}
			if r.URL.Query().Get("evId") != "ev-1" {
				t.Fatalf("cert evId=%q", r.URL.Query().Get("evId"))
			}
			w.Write([]byte(`{"retCode":0,"retMsg":"OK","data":{"status":2,"certUrl":"https://cert.example/ev-1"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	p := newZXChainProvider(server.URL, "secret-id", "secret-key", "public-key", "private-key", time.Second, testZXChainSigner{})
	saved, err := p.Submit(context.Background(), SubmitRequest{RequestNo: "req-1", HashValue: strings.Repeat("a", 64)})
	if err != nil || saved.ReceiptNo != "ev-1" || saved.TransactionHash != "tx-1" || saved.BlockHeight != "8" {
		t.Fatalf("submit=%#v err=%v", saved, err)
	}
	queried, err := p.Query(context.Background(), saved.ReceiptNo)
	if err != nil || queried.TransactionHash != "tx-1" {
		t.Fatalf("query=%#v err=%v", queried, err)
	}
	requested, err := p.RequestCertificate(context.Background(), saved.ReceiptNo)
	if err != nil || requested.Status != "PENDING" || requested.ProviderStatus != "1" {
		t.Fatalf("request cert=%#v err=%v", requested, err)
	}
	certificate, err := p.QueryCertificate(context.Background(), saved.ReceiptNo)
	if err != nil || certificate.Status != "READY" || certificate.CertURL == "" {
		t.Fatalf("query cert=%#v err=%v", certificate, err)
	}
}
