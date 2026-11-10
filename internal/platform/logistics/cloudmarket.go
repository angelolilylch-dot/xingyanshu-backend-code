// Package logistics adapts external shipment-tracking providers to the small
// domain shape used by fulfilment workflows. It never exposes provider secrets.
package logistics

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"xingyanshu-backend/internal/config"
)

const cloudMarketProvider = "tencent_cloudmarket_track"

type Event struct {
	Time        time.Time
	Description string
}

type Track struct {
	CarrierName string
	TrackingNo  string
	Status      string
	Signed      bool
	Events      []Event
}

type Provider interface {
	Query(context.Context, string, string) (Track, error)
}

type cloudMarket struct {
	endpoint  string
	secretID  string
	secretKey string
	client    *http.Client
	now       func() time.Time
}

// NewProvider returns nil when logistics is intentionally unconfigured. This
// prevents a deployment from silently treating unavailable tracking as signed.
func NewProvider(cfg config.LogisticsConfig) (Provider, error) {
	if cfg.Provider == "" {
		return nil, nil
	}
	if cfg.Provider != cloudMarketProvider {
		return nil, fmt.Errorf("unsupported logistics provider %q", cfg.Provider)
	}
	if cfg.Endpoint == "" || cfg.SecretID == "" || cfg.SecretKey == "" || cfg.Timeout <= 0 {
		return nil, fmt.Errorf("incomplete logistics configuration")
	}
	return &cloudMarket{endpoint: cfg.Endpoint, secretID: cfg.SecretID, secretKey: cfg.SecretKey, client: &http.Client{Timeout: cfg.Timeout}, now: time.Now}, nil
}

type cloudMarketResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data *struct {
		CarrierCode string `json:"cpCode"`
		CarrierName string `json:"logisticsCompanyName"`
		MailNo      string `json:"mailNo"`
		Status      string `json:"logisticsStatus"`
		Traces      []struct {
			Time   int64  `json:"time"`
			Status string `json:"logisticsStatus"`
			Desc   string `json:"desc"`
		} `json:"logisticsTraceDetailList"`
	} `json:"data"`
}

func (p *cloudMarket) Query(ctx context.Context, trackingNo, phoneLastFour string) (Track, error) {
	trackingNo = strings.TrimSpace(trackingNo)
	if trackingNo == "" {
		return Track{}, fmt.Errorf("tracking number is required")
	}
	endpoint, err := url.Parse(p.endpoint)
	if err != nil {
		return Track{}, fmt.Errorf("parse logistics endpoint: %w", err)
	}
	query := endpoint.Query()
	// The current Cloud Marketplace product identifies the carrier itself when
	// code=auto is supplied; no is the actual tracking number.
	query.Set("code", "auto")
	query.Set("no", trackingNo)
	if value := strings.TrimSpace(phoneLastFour); value != "" {
		query.Set("tel", value)
	}
	endpoint.RawQuery = query.Encode()

	date := p.now().UTC().Format(http.TimeFormat)
	authorization := cloudMarketAuthorization(p.secretID, p.secretKey, date)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Track{}, err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Request-Id", requestID())
	resp, err := p.client.Do(req)
	if err != nil {
		return Track{}, fmt.Errorf("query logistics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Track{}, fmt.Errorf("logistics response status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var body cloudMarketResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Track{}, fmt.Errorf("decode logistics response: %w", err)
	}
	if body.Code != "0" || body.Data == nil || strings.TrimSpace(body.Data.MailNo) == "" {
		message := strings.TrimSpace(body.Msg)
		if message == "" {
			message = "物流查询未返回有效运单"
		}
		return Track{}, fmt.Errorf("%s", message)
	}
	carrier := strings.TrimSpace(body.Data.CarrierName)
	if carrier == "" {
		carrier = strings.TrimSpace(body.Data.CarrierCode)
	}
	status := strings.TrimSpace(body.Data.Status)
	track := Track{CarrierName: carrier, TrackingNo: strings.TrimSpace(body.Data.MailNo), Status: status, Signed: status == "SIGN", Events: make([]Event, 0, len(body.Data.Traces))}
	for _, item := range body.Data.Traces {
		if item.Time <= 0 || strings.TrimSpace(item.Desc) == "" {
			continue
		}
		track.Events = append(track.Events, Event{Time: time.UnixMilli(item.Time), Description: strings.TrimSpace(item.Desc)})
	}
	return track, nil
}

func requestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("logistics-%d", time.Now().UnixNano())
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func cloudMarketAuthorization(secretID, secretKey, date string) string {
	mac := hmac.New(sha1.New, []byte(secretKey))
	_, _ = mac.Write([]byte("x-date: " + date))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf(`{"id":"%s", "x-date":"%s", "signature":"%s"}`, secretID, date, signature)
}
