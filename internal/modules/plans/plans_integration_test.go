package plans

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/identity"
)

// TestPlansWithMySQL 用真实测试库确认模板结构、授权关联和状态锁定不会只停留在接口内存中。
func TestPlansWithMySQL(t *testing.T) {
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
	operator := identity.CurrentAdmin{UserID: 10401, RoleCodes: []string{"PLATFORM_ADMIN"}}
	suffix := time.Now().UnixNano()
	templateNo := fmt.Sprintf("PLAN_TEMPLATE_%d", suffix)
	planNo := fmt.Sprintf("AUTH_PLAN_%d", suffix)
	template, code, err := service.CreateTemplate(context.Background(), operator, DistributionCreate{
		DistributionNo: templateNo, DistributionName: "测试普通授权分账结构", DistributionType: "NORMAL_SHARE",
		Items: []Participant{{ParticipantType: "RIGHT_HOLDER", SortNo: 10}, {ParticipantType: "CITY_OPERATOR", SortNo: 20}, {ParticipantType: "PLATFORM", SortNo: 30}},
	}, "127.0.0.1")
	if err != nil || code != "" {
		t.Fatalf("create template: data=%#v code=%s err=%v", template, code, err)
	}
	templateID := mustPlanID(t, template["id"].(string))
	defer db.Exec("DELETE FROM finance_distribution_plan WHERE id = ?", templateID)
	defer db.Exec("DELETE FROM finance_distribution_plan_item WHERE distribution_plan_id = ?", templateID)

	detail, code, err := service.GetTemplate(context.Background(), templateID)
	if err != nil || code != "" || len(detail["items"].([]map[string]any)) != 3 {
		t.Fatalf("get template: data=%#v code=%s err=%v", detail, code, err)
	}

	createdPlan, code, err := service.CreateAuth(context.Background(), operator, AuthCreate{
		PlanNo: planNo, PlanName: "测试普通授权方案", AuthorizationMode: "NORMAL_SHARE", TermType: "MONTH", TermValue: intPtr(12),
		RegionMode: "NATIONWIDE", Scenes: []string{"PRODUCT"}, CommercialAllowed: true, DerivativeAllowed: true,
		ContentType: "PRODUCT", QuotaTotal: 10, DistributionTemplateID: templateID,
	}, "127.0.0.1")
	if err != nil || code != "" {
		t.Fatalf("create auth plan: data=%#v code=%s err=%v", createdPlan, code, err)
	}
	planID := mustPlanID(t, createdPlan["id"].(string))
	defer db.Exec("DELETE FROM auth_plan WHERE id = ?", planID)
	defer db.Exec("DELETE FROM auth_plan_version WHERE plan_id = ?", planID)

	authPlan, code, err := service.GetAuthPlan(context.Background(), planID)
	if err != nil || code != "" || fmt.Sprint(authPlan["distributionTemplateId"]) != fmt.Sprint(templateID) {
		t.Fatalf("auth plan template association missing: data=%#v code=%s err=%v", authPlan, code, err)
	}

	if err := db.Table("finance_distribution_plan").Where("id = ?", templateID).Update("is_bound", true).Error; err != nil {
		t.Fatal(err)
	}
	_, code, err = service.ChangeTemplateStatus(context.Background(), operator, templateID, 1, false, "测试锁定", "127.0.0.1")
	if err != nil || code != "AUTH_PLAN_BOUND_LOCKED" {
		t.Fatalf("bound template should be locked: code=%s err=%v", code, err)
	}
}

func intPtr(value int) *int { return &value }

func mustPlanID(t *testing.T, value string) uint64 {
	var id uint64
	if _, err := fmt.Sscan(value, &id); err != nil || id == 0 {
		t.Fatalf("invalid id %q: %v", value, err)
	}
	return id
}
