package audit

import "testing"

// TestSanitize 确保审计快照不写入常见敏感字段原值。
func TestSanitize(t *testing.T) {
	result := Sanitize(map[string]any{"password": "secret", "nested": map[string]any{"mobile": "13800138000"}, "name": "测试"}).(map[string]any)
	if result["password"] != "***" || result["nested"].(map[string]any)["mobile"] != "***" || result["name"] != "测试" {
		t.Fatalf("unexpected sanitized value: %#v", result)
	}
}
