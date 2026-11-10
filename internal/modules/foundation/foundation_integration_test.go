package foundation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/creators"
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
	regions, code, err = service.ListRegions(ctx, "测试地区", "city", " 350000 ")
	if err != nil || code != "" || len(regions["items"].([]map[string]any)) != 1 {
		t.Fatalf("list regions with normalized filters: %#v code=%s err=%v", regions, code, err)
	}

	// 全国地区快照须能按全国→省→市→区县四级逐级查询；避免地址表单只停留在市级。
	assertRegionCode(t, service, ctx, "1", "000000", "440000")
	assertRegionCode(t, service, ctx, "2", "440000", "440100")
	assertRegionCode(t, service, ctx, "3", "440100", "440103")

	ticketID := uint64(8_800_000_001)
	mustFoundation(t, db.Exec("DELETE FROM sys_dict_item WHERE id = ?", ticketID).Error)
	mustFoundation(t, db.Table("sys_dict_item").Create(map[string]any{"id": ticketID, "status": "ACTIVE", "version": 1, "dict_type": "ACTIVITY_TICKET_TYPE", "dict_code": "TEST_TICKET", "dict_name": "测试票种", "sort_no": 99, "enabled": true}).Error)
	defer db.Exec("DELETE FROM sys_dict_item WHERE id = ?", ticketID)
	tickets, err := service.ListActivityTicketTypes(ctx)
	if err != nil || len(tickets["items"].([]map[string]any)) == 0 {
		t.Fatalf("list activity ticket types: %#v err=%v", tickets, err)
	}
	updated, code, err := service.UpdateActivityTicketType(ctx, operator, "test_ticket", 1, ActivityTicketTypeUpdateRequest{Enabled: false, SortNo: 10, Reason: "集成测试停用"})
	if err != nil || code != "" || updated["enabled"] != false || updated["sortNo"] != 10 {
		t.Fatalf("update activity ticket type: %#v code=%s err=%v", updated, code, err)
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
	var returnAddressConfig systemConfig
	if err := db.Where("config_key = ?", "PLATFORM_RETURN_ADDRESS").First(&returnAddressConfig).Error; err != nil {
		t.Fatalf("load return-address config: %v", err)
	}
	originalValue, originalVersion := returnAddressConfig.ConfigValue, returnAddressConfig.Version
	t.Cleanup(func() {
		db.Model(&systemConfig{}).Where("id = ?", returnAddressConfig.ID).Updates(map[string]any{"config_value": originalValue, "version": originalVersion})
	})
	updatedConfig, code, err := service.UpdateConfig(ctx, operator, "PLATFORM_RETURN_ADDRESS", returnAddressConfig.Version, ConfigUpdateRequest{Value: `{"recipientName":"测试售后中心","mobile":"400-800-1234","provinceCode":"440000","cityCode":"440100","districtCode":"440106","detailAddress":"测试收件地址"}`, Reason: "集成测试更新固定退货地址"})
	if err != nil || code != "" {
		t.Fatalf("update return-address config: %#v code=%s err=%v", updatedConfig, code, err)
	}
	var savedAddress map[string]string
	if err := json.Unmarshal([]byte(updatedConfig["value"].(string)), &savedAddress); err != nil || savedAddress["detailAddress"] != "测试收件地址" {
		t.Fatalf("return-address config must be canonical JSON: %#v err=%v", updatedConfig, err)
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

	// 创作者下拉只能读取已启用的对应业务模板，不依赖不存在的
	// /platform/refund-templates 设计路径。
	router := gin.New()
	RegisterCreatorRoutes(router.Group("/api/v1"), service, func(c *gin.Context) {
		c.Set("creators.current", creators.CurrentCreator{CreatorID: 10401, UserID: 10401})
		c.Next()
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/creator/after-sale-templates?bizType=PRODUCT&pageNo=1&pageSize=100", nil))
	var body struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK || len(body.Data.Items) == 0 || body.Data.Items[0]["id"] != next["id"] {
		t.Fatalf("creator after-sale template options: status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
}

func assertRegionCode(t *testing.T, service *Service, ctx context.Context, level, parentCode, expectedCode string) {
	t.Helper()
	regions, code, err := service.ListRegions(ctx, "", level, parentCode)
	if err != nil || code != "" {
		t.Fatalf("list %s regions under %s: code=%s err=%v", level, parentCode, code, err)
	}
	for _, region := range regions["items"].([]map[string]any) {
		if region["code"] == expectedCode {
			return
		}
	}
	t.Fatalf("expected region %s in %s under %s: %#v", expectedCode, level, parentCode, regions)
}

func mustUint64(t *testing.T, value string) uint64 {
	var id uint64
	if _, err := fmt.Sscan(value, &id); err != nil || id == 0 {
		t.Fatalf("invalid id %q: %v", value, err)
	}
	return id
}

func mustFoundation(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
