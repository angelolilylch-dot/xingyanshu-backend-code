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

func TestPlatformAdminMenuDefinitionsIncludeB04AndB05(t *testing.T) {
	want := map[string]string{"MENU_AUTH_PLANS": "B04", "MENU_DISTRIBUTION_TEMPLATES": "B05"}
	for code, resource := range want {
		found := false
		for _, item := range permissions {
			if item.Code == code {
				found = true
				if item.PermissionType != "MENU" || item.ResourceCode != resource {
					t.Fatalf("%s = type %s resource %s, want MENU %s", code, item.PermissionType, item.ResourceCode, resource)
				}
			}
		}
		if !found {
			t.Fatalf("missing menu permission %s", code)
		}
	}
}

func TestCopyrightComplianceMenuDefinitionsIncludeAuthPlanReview(t *testing.T) {
	for _, item := range permissions {
		if item.Code == "MENU_AUTH_PLAN_REVIEW" {
			if item.PermissionType != "MENU" || item.ResourceCode != "B04" {
				t.Fatalf("auth plan review menu = type %s resource %s, want MENU B04", item.PermissionType, item.ResourceCode)
			}
			return
		}
	}
	t.Fatal("missing copyright compliance auth plan review menu")
}

func TestCreatorMenuDefinitionsIncludeOnlyCompletedC01AndC02(t *testing.T) {
	want := map[string]string{
		"MENU_CREATOR_WORKSPACE":  "C01",
		"MENU_CREATOR_ONBOARDING": "C01",
		"MENU_CREATOR_IP_MALL":    "C02",
	}
	for code, resource := range want {
		for _, item := range permissions {
			if item.Code != code {
				continue
			}
			if item.PermissionType != "MENU" || item.ResourceCode != resource {
				t.Fatalf("%s = type %s resource %s, want MENU %s", code, item.PermissionType, item.ResourceCode, resource)
			}
			goto found
		}
		t.Fatalf("missing creator menu permission %s", code)
	found:
	}
	for _, item := range permissions {
		if item.Code == "MENU_CREATOR_CONTENT" {
			t.Fatal("MENU_CREATOR_CONTENT must not be seeded before C05 is implemented")
		}
	}
}
