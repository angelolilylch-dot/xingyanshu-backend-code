package payments

import (
	"net/http"
	"testing"

	"xingyanshu-backend/internal/platform/httpx"
)

func TestAuthorizationOrderNotFoundContract(t *testing.T) {
	err := httpx.NewErrorWithData(http.StatusNotFound, "AUTH_ORDER_NOT_FOUND", message("AUTH_ORDER_NOT_FOUND"), map[string]any{"resourceType": "AUTH_ORDER", "field": "bizOrderId"})
	if err.Status != http.StatusNotFound || err.Code != "AUTH_ORDER_NOT_FOUND" || err.Data.(map[string]any)["field"] != "bizOrderId" {
		t.Fatalf("unexpected error: %#v", err)
	}
}
