package foundation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
)

// TestFoundationWithMySQL 用真实测试库验证基础数据的创建、版本化和查询规则。
func TestFoundationWithMySQL(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1 to run MySQL integration test")
	}
	root := filepath.Join("..", "..", "..")
	cfg, err := config.Load(filepath.Join(root, "config", "test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	service := NewService(db)
	operator := Operator{ID: 10401, RoleCode: "PLATFORM_ADMIN", ClientIP: "127.0.0.1"}
	ctx := context.Background()

	// 固定枚举由 bootstrap 初始化，页面通过字典接口读取中文名称和稳定编码。
	dictionaries, code, err := service.ListDictionary(ctx, "IP_TYPE", true)
	if err != nil || code != "" || len(dictionaries["items"].([]map[string]any)) != 8 {
		t.Fatalf("list fixed dictionary: %#v code=%s err=%v", dictionaries, code, err)
	}

	region, code, err := service.CreateRegion(ctx, operator, RegionCreateRequest{RegionCode: "359900", RegionName: "测试地区", SortNo: 999, Enabled: true})
	if err != nil || code != "" {
		t.Fatalf("create region: code=%s err=%v", code, err)
	}
	regionID := region["id"].(string)
	defer db.Exec("DELETE FROM sys_dict_item WHERE id = ?", regionID)
	regions, code, err := service.ListRegions(ctx, "测试地区", "CITY", "350000")
	if err != nil || code != "" || len(regions["items"].([]map[string]any)) != 1 {
		t.Fatalf("list regions: %#v code=%s err=%v", regions, code, err)
	}

	tag, code, err := service.CreateTag(ctx, operator, TagCreateRequest{TagType: "CULTURE", TagName: "测试文化标签", SortNo: 999})
	if err != nil || code != "" {
		t.Fatalf("create tag: code=%s err=%v", code, err)
	}
	tagID := tag["id"].(string)
	defer db.Exec("DELETE FROM sys_dict_item WHERE id = ?", tagID)
	_, code, err = service.ChangeTagStatus(ctx, operator, mustUint64(t, tagID), tag["version"].(uint32), EnabledRequest{Enabled: false, Reason: "集成测试"})
	if err != nil || code != "" {
		t.Fatalf("disable tag: code=%s err=%v", code, err)
	}
	tags, code, err := service.ListTags(ctx, "CULTURE", "测试文化标签", false)
	if err != nil || code != "" || len(tags["items"].([]map[string]any)) != 0 {
		t.Fatalf("disabled tag should be hidden: %#v code=%s err=%v", tags, code, err)
	}

	configs, err := service.ListConfigs(ctx)
	if err != nil || len(configs["items"].([]map[string]any)) == 0 {
		t.Fatalf("list configs: %#v err=%v", configs, err)
	}

	no := fmt.Sprintf("PRODUCT_TEST_%d", tag["version"].(uint32))
	created, code, err := service.CreateTemplate(ctx, operator, TemplateCreateRequest{TemplateNo: no, TemplateName: "集成测试商品规则", BizType: "PRODUCT", Conditions: []byte(`{"applyAfterDelivery":true}`), RefundRule: []byte(`{"refundMode":"RETURN_REFUND"}`), ReturnFeeBearer: "CONSUMER"})
	if err != nil || code != "" {
		t.Fatalf("create template: code=%s err=%v", code, err)
	}
	templateID := created["id"].(string)
	defer db.Exec("DELETE FROM after_sale_rule_template WHERE template_no = ?", no)
	next, code, err := service.CreateTemplateVersion(ctx, operator, mustUint64(t, templateID), created["version"].(uint32), TemplateVersionRequest{TemplateName: "集成测试商品规则 V2", Conditions: []byte(`{"applyAfterDelivery":true,"needEvidence":false}`), RefundRule: []byte(`{"refundMode":"RETURN_REFUND","maxRefundRate":1}`), ReturnFeeBearer: "CONSUMER"})
	if err != nil || code != "" || next["versionNo"].(uint32) != 2 {
		t.Fatalf("create template version: %#v code=%s err=%v", next, code, err)
	}
	var previous template
	if err := db.Where("id = ?", templateID).First(&previous).Error; err != nil || previous.Enabled {
		t.Fatalf("previous version should be disabled: %#v err=%v", previous, err)
	}
}

func mustUint64(t *testing.T, value string) uint64 {
	var id uint64
	if _, err := fmt.Sscan(value, &id); err != nil || id == 0 {
		t.Fatalf("invalid id %q: %v", value, err)
	}
	return id
}
