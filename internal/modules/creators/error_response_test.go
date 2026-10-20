package creators

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRespondMapsCreatorNotFoundCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, code := range []string{"CREATOR_NOT_FOUND", "IDENTITY_VERIFICATION_NOT_FOUND", "AUTHORIZATION_LETTER_FILE_NOT_FOUND"} {
		r := gin.New()
		r.GET("/test", func(c *gin.Context) { respond(c, http.StatusOK, nil, code, nil) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/test", nil))
		var body struct {
			Code string         `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusNotFound || body.Code != code || body.Data["resourceType"] == nil {
			t.Fatalf("body=%s", w.Body.String())
		}
	}
}

func TestRespondPreservesCreatorStateDetails(t *testing.T) {
	detail := map[string]any{"currentStatus": "PERSONAL", "action": "FILE_AUTHORIZATION_LETTER", "reasonCode": "PERSONAL_SUBJECT_NOT_REQUIRED"}
	r := gin.New()
	r.POST("/test", func(c *gin.Context) { respond(c, http.StatusOK, detail, "STATE_TRANSITION_NOT_ALLOWED", nil) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/test", nil))
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data["reasonCode"] != "PERSONAL_SUBJECT_NOT_REQUIRED" {
		t.Fatalf("body=%s", w.Body.String())
	}
}
