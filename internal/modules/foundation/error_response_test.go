package foundation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWriteResponseMapsFoundationResource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct{ route, path, code, field string }{
		{"/admin/regions/:regionId/status", "/admin/regions/1/status", "REGION_NOT_FOUND", "regionId"},
		{"/admin/tags/:tagId/status", "/admin/tags/1/status", "TAG_NOT_FOUND", "tagId"},
		{"/admin/configs/:configKey", "/admin/configs/X", "SYSTEM_CONFIG_NOT_FOUND", "configKey"},
		{"/admin/after-sale-templates/:templateId/status", "/admin/after-sale-templates/1/status", "AFTER_SALE_TEMPLATE_NOT_FOUND", "templateId"},
	}
	for _, tc := range cases {
		r := gin.New()
		r.PATCH(tc.route, func(c *gin.Context) { writeResponse(c, http.StatusOK, nil, "RESOURCE_NOT_FOUND", nil) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, tc.path, nil))
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
	}
}
