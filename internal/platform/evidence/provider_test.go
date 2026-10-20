package evidence

import (
	"context"
	"testing"
)

func TestUnconfiguredProviderNeverReturnsSuccess(t *testing.T) {
	result, err := (UnconfiguredProvider{}).Submit(context.Background(), SubmitRequest{RequestNo: "REQ-1", HashValue: "hash"})
	if err == nil || result.Status == "SUCCESS" || result.ReceiptNo != "" || result.TransactionHash != "" {
		t.Fatalf("unconfigured provider must not create a success receipt: %#v, %v", result, err)
	}
}

func TestMockProviderReturnsReceiptAndTransactionHash(t *testing.T) {
	result, err := (MockProvider{}).Submit(context.Background(), SubmitRequest{RequestNo: "REQ-1"})
	if err != nil || result.Status != "SUCCESS" || result.ReceiptNo == "" || result.TransactionHash == "" {
		t.Fatalf("mock provider must return a success receipt and transaction hash: %#v, %v", result, err)
	}
}
