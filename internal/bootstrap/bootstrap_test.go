package bootstrap

import "testing"

// TestSeedDefinitions 验证平台管理员角色和每类预置角色均已定义。
func TestSeedDefinitions(t *testing.T) {
	if len(roles) != 7 {
		t.Fatalf("role count = %d, want 7", len(roles))
	}
	if roles[0].Code != platformAdminRole {
		t.Fatalf("first role = %s, want %s", roles[0].Code, platformAdminRole)
	}
	if len(permissions) == 0 {
		t.Fatal("permissions must not be empty")
	}
	if len(fixedDictionaries) == 0 {
		t.Fatal("fixed dictionaries must not be empty")
	}
	for _, item := range fixedDictionaries {
		if item.Type == "" || item.Code == "" || item.Name == "" {
			t.Fatalf("invalid fixed dictionary definition: %#v", item)
		}
	}
}
