package logistics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"xingyanshu-backend/internal/config"
)

func TestCloudMarketQueryParsesSignedTrack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("code") != "auto" || r.URL.Query().Get("no") != "YT123" || r.URL.Query().Get("tel") != "1234" || r.Header.Get("Authorization") == "" || r.Header.Get("X-Date") != "" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"成功","data":{"cpCode":"YTO","logisticsCompanyName":"圆通快递","mailNo":"YT123","logisticsStatus":"SIGN","logisticsTraceDetailList":[{"time":1793239200000,"logisticsStatus":"SIGN","desc":"已签收"}]}}`))
	}))
	defer server.Close()
	p, err := NewProvider(config.LogisticsConfig{Provider: cloudMarketProvider, Endpoint: server.URL, SecretID: "id", SecretKey: "key", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	track, err := p.Query(context.Background(), "YT123", "1234")
	if err != nil || !track.Signed || track.Status != "SIGN" || len(track.Events) != 1 || track.CarrierName != "圆通快递" {
		t.Fatalf("track=%+v err=%v", track, err)
	}
}

// TestCloudMarketQueryLive is opt-in because every real request consumes the
// purchased Cloud Marketplace quota. Secrets are read only from the current
// process environment and are never printed.
func TestCloudMarketQueryLive(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_LOGISTICS_LIVE") != "1" {
		t.Skip("set XINGYANSHU_RUN_LOGISTICS_LIVE=1")
	}
	p, err := NewProvider(config.LogisticsConfig{
		Provider:  cloudMarketProvider,
		Endpoint:  "https://ap-guangzhou.cloudmarket-apigw.com/service-d2zdkymg/express/query",
		SecretID:  os.Getenv("XINGYANSHU_LOGISTICS_SECRET_ID"),
		SecretKey: os.Getenv("XINGYANSHU_LOGISTICS_SECRET_KEY"),
		Timeout:   10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	trackingNo := strings.TrimSpace(os.Getenv("XINGYANSHU_LOGISTICS_TEST_TRACKING_NO"))
	phoneLastFour := strings.TrimSpace(os.Getenv("XINGYANSHU_LOGISTICS_TEST_PHONE_LAST4"))
	track, err := p.Query(context.Background(), trackingNo, phoneLastFour)
	if err != nil {
		t.Fatal(err)
	}
	if track.TrackingNo == "" || track.Status == "" {
		t.Fatalf("invalid live result: status=%q carrier=%q events=%d", track.Status, track.CarrierName, len(track.Events))
	}
	t.Logf("live logistics result: status=%q signed=%t carrier=%q events=%d", track.Status, track.Signed, track.CarrierName, len(track.Events))
}

func TestCloudMarketQueryRejectsEmptyTrackingResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"1","msg":"请输入快递编号","isFee":0,"seqNo":null,"data":null}`))
	}))
	defer server.Close()
	p, err := NewProvider(config.LogisticsConfig{Provider: cloudMarketProvider, Endpoint: server.URL, SecretID: "id", SecretKey: "key", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Query(context.Background(), "YT123", ""); err == nil {
		t.Fatal("empty tracking response must fail")
	}
}
