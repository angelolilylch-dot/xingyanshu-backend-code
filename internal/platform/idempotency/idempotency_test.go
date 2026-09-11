package idempotency

import "testing"

// TestKeyValidation 覆盖订单类写接口要求的幂等键基本格式。
func TestKeyValidation(t *testing.T) {
	if !keyPattern.MatchString("order-create-0001") {
		t.Fatal("expected a valid idempotency key")
	}
	if keyPattern.MatchString("too-short") {
		t.Fatal("short key must be rejected")
	}
	if keyPattern.MatchString("key with space 0001") {
		t.Fatal("key containing spaces must be rejected")
	}
}

// TestRequestHash 确保相同请求稳定生成同一摘要，不同请求不可误判为同一请求。
func TestRequestHash(t *testing.T) {
	first := requestHash([]byte(`{"orderId":"1"}`))
	if first != requestHash([]byte(`{"orderId":"1"}`)) {
		t.Fatal("same request hash differs")
	}
	if first == requestHash([]byte(`{"orderId":"2"}`)) {
		t.Fatal("different request hash matches")
	}
}
