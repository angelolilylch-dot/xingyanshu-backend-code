package plans

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRespondMapsPlanResourceType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ path, route, code string }{{"/auth-plans/1", "/auth-plans/:planId", "AUTH_PLAN_NOT_FOUND"}, {"/distribution-templates/1", "/distribution-templates/:templateId", "DISTRIBUTION_PLAN_NOT_FOUND"}} {
		r := gin.New()
		r.GET(tc.route, func(c *gin.Context) { respond(c, http.StatusOK, nil, "RESOURCE_NOT_FOUND", nil) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		var body struct {
			Code string         `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Code != tc.code || body.Data["field"] == nil {
			t.Fatalf("body=%s", w.Body.String())
		}
	}
}

func TestRespondPreservesStateDetails(t *testing.T) {
	r := gin.New()
	r.POST("/auth-plans/:planId/enable", func(c *gin.Context) {
		respond(c, http.StatusOK, map[string]any{"currentStatus": "DRAFT", "action": "ENABLE", "reasonCode": "AUTH_PLAN_TRANSITION_NOT_ALLOWED"}, "STATE_TRANSITION_NOT_ALLOWED", nil)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth-plans/1/enable", nil))
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data["currentStatus"] != "DRAFT" || body.Data["action"] != "ENABLE" {
		t.Fatalf("body=%s", w.Body.String())
	}
}
