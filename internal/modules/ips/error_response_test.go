package ips

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRespondProvidesPreciseNotFoundLocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct{ code, field string }{
		{"COVER_FILE_NOT_FOUND", "coverFileId"},
		{"RIGHT_DOCUMENT_FILE_NOT_FOUND", "rightDocuments[].fileId"},
		{"DETAIL_FILE_NOT_FOUND", "detailFileIds[]"},
		{"DCI_CERTIFICATE_FILE_NOT_FOUND", "dciCertificateFileId"},
		{"AUTH_PLAN_VERSION_NOT_FOUND", "authPlanVersionId"},
		{"DISTRIBUTION_PLAN_NOT_FOUND", "distributionTemplateId"},
		{"DCI_ASSISTANCE_REQUEST_NOT_FOUND", "requestId"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			r := gin.New()
			r.GET("/test/:ipId", func(c *gin.Context) { respond(c, http.StatusOK, nil, tc.code, nil) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/test/1", nil))
			if w.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var body struct {
				Code string         `json:"code"`
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != tc.code || body.Data["field"] != tc.field {
				t.Fatalf("body=%s", w.Body.String())
			}
		})
	}
}

func TestRespondStateErrorIncludesActionAndReason(t *testing.T) {
	r := gin.New()
	r.POST("/ips/:ipId/submit", func(c *gin.Context) { respond(c, http.StatusOK, nil, "STATE_TRANSITION_NOT_ALLOWED", nil) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/ips/1/submit", nil))
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data["action"] != "/ips/:ipId/submit" || body.Data["reasonCode"] != "STATE_TRANSITION_NOT_ALLOWED" {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestRespondSubmitFailureIsActionable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/ips/:ipId/submit", func(c *gin.Context) { respond(c, http.StatusOK, nil, "IP_SUBMIT_FAILED", nil) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/ips/1/submit", nil))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"IP_SUBMIT_FAILED"`) || !strings.Contains(w.Body.String(), "提交审核失败") || !strings.Contains(w.Body.String(), "请求ID") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPreciseNotFoundResponseOverHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/ips", func(c *gin.Context) {
		respond(c, http.StatusCreated, nil, "COVER_FILE_NOT_FOUND", nil)
	})
	server := httptest.NewServer(r)
	defer server.Close()

	response, err := http.Post(server.URL+"/ips", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		Code string         `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound || body.Code != "COVER_FILE_NOT_FOUND" || body.Data["field"] != "coverFileId" {
		t.Fatalf("status=%d body=%+v", response.StatusCode, body)
	}
}
