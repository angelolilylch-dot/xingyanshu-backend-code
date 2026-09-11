package identity

import "testing"

// TestBuildNewOrg 覆盖组织编码、城市组织上级和城市编码的基础校验。
func TestBuildNewOrg(t *testing.T) {
	valid := OrgCreateRequest{OrgCode: "xm_city", OrgName: "厦门城市组织", OrgType: "CITY", ParentID: "10001", CityCode: "350200"}
	item, parentID, code, err := buildNewOrg(valid)
	if err != nil || code != "XM_CITY" || parentID == nil || *parentID != 10001 || item.CityCode == nil || *item.CityCode != "350200" {
		t.Fatalf("valid city organization was rejected: item=%+v parent=%v code=%s err=%v", item, parentID, code, err)
	}

	invalid := []OrgCreateRequest{
		{OrgCode: "BAD CODE", OrgName: "非法编码", OrgType: "GROUP"},
		{OrgCode: "CITY_NO_PARENT", OrgName: "缺少上级", OrgType: "CITY", CityCode: "350200"},
		{OrgCode: "GROUP_WITH_CITY", OrgName: "错误城市编码", OrgType: "GROUP", CityCode: "350200"},
	}
	for _, request := range invalid {
		if _, _, _, err := buildNewOrg(request); err == nil {
			t.Fatalf("invalid organization was accepted: %+v", request)
		}
	}
}

// TestValidAccountOrganization 验证城市账号和本城市范围必须关联城市组织。
func TestValidAccountOrganization(t *testing.T) {
	cityCode := "350200"
	cityOrg := managedOrg{OrgType: "CITY", CityCode: &cityCode}
	platformOrg := managedOrg{OrgType: "PLATFORM"}
	if !validAccountOrganization("CITY_OPERATOR", "CITY", cityOrg) {
		t.Fatal("city operator should be allowed on city organization")
	}
	if validAccountOrganization("CITY_OPERATOR", "CITY", platformOrg) {
		t.Fatal("city operator should not be allowed on platform organization")
	}
	if validAccountOrganization("COPYRIGHT_COMPLIANCE", "CITY", platformOrg) {
		t.Fatal("city data scope should not be allowed on platform organization")
	}
}
