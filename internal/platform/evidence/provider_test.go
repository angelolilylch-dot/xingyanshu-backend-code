package evidence

import (
	"context"
	"testing"
)

func TestUnconfiguredProviderNeverReturnsSuccess(t *testing.T) {
	result, err := (UnconfiguredProvider{}).Submit(context.Background(), SubmitRequest{RequestNo: "REQ-1", HashValue: "hash"})
	if err == nil || result.Status == "SUCCESS" || result.ReceiptNo != "" {
		t.Fatalf("unconfigured provider must not create a success receipt: %#v, %v", result, err)
	}
}
