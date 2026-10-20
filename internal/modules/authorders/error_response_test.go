package authorders

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRespondLocatesMissingAuthorizationOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/creator/auth-orders/:authOrderId", func(c *gin.Context) {
		respond(c, http.StatusOK, nil, "AUTH_ORDER_NOT_FOUND", nil)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/creator/auth-orders/1", nil))
	var body struct {
		Code string         `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusNotFound || body.Code != "AUTH_ORDER_NOT_FOUND" || body.Data["field"] != "authOrderId" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestRespondPreservesAuthorizationOrderStateDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/creator/auth-orders/:authOrderId/cancel", func(c *gin.Context) {
		respond(c, http.StatusOK, map[string]any{"currentStatus": "PAID", "action": "CANCEL_AUTH_ORDER", "reasonCode": "AUTH_ORDER_NOT_PENDING"}, "STATE_TRANSITION_NOT_ALLOWED", nil)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/creator/auth-orders/1/cancel", nil))
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data["currentStatus"] != "PAID" || body.Data["action"] != "CANCEL_AUTH_ORDER" {
		t.Fatalf("body=%s", w.Body.String())
	}
}
