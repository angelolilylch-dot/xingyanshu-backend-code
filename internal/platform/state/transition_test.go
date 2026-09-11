package state

import "testing"

// TestValidateTransition 验证状态流转只能走业务显式允许的边。
func TestValidateTransition(t *testing.T) {
	rule := NewRule(map[string][]string{"PENDING": {"PAID", "CANCELLED"}})
	if err := ValidateTransition(rule, "PENDING", "PAID"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTransition(rule, "PAID", "CANCELLED"); err == nil {
		t.Fatal("invalid transition was accepted")
	}
}
