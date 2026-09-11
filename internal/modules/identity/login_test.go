package identity

import "testing"

// TestIsValidAdminUsername 覆盖后台账号格式的边界和常见非法输入。
func TestIsValidAdminUsername(t *testing.T) {
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "字母数字", value: "admin01", valid: true},
		{name: "含下划线和连字符", value: "city_operator-01", valid: true},
		{name: "最短长度", value: "a1", valid: true},
		{name: "长度不足", value: "a", valid: false},
		{name: "以下划线开头", value: "_admin", valid: false},
		{name: "以连字符结尾", value: "admin-", valid: false},
		{name: "包含空格", value: "admin 01", valid: false},
		{name: "包含中文", value: "管理员", valid: false},
	}

	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			if actual := IsValidAdminUsername(item.value); actual != item.valid {
				t.Fatalf("IsValidAdminUsername(%q) = %v, want %v", item.value, actual, item.valid)
			}
		})
	}
}
