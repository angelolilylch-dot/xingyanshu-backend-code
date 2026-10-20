package plans

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/identity"
)

// TestPlansWithMySQL 用真实测试库确认模板结构、授权方案独立维护和状态锁定不会只停留在接口内存中。
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
		PlanNo: planNo, PlanName: "测试普通授权方案", AuthorizationMode: "NORMAL_SHARE", TermType: "DATE_RANGE", ValidFrom: "2026-01-01", ValidTo: "2026-12-31",
		RegionMode: "DESIGNATED", RegionCodes: []string{"350200", "440100"}, Scenes: []string{"PRODUCT_SALE"}, CommercialAllowed: true, DerivativeAllowed: true,
		ContentType: "PRODUCT_ONLY", QuotaTotal: intPlanPtr(10),
	}, "127.0.0.1")
	if err != nil || code != "" {
		t.Fatalf("create auth plan: data=%#v code=%s err=%v", createdPlan, code, err)
	}
	planID := mustPlanID(t, createdPlan["id"].(string))
	defer db.Exec("DELETE FROM auth_plan WHERE id = ?", planID)
	defer db.Exec("DELETE FROM auth_plan_version WHERE plan_id = ?", planID)

	authPlan, code, err := service.GetAuthPlan(context.Background(), planID)
	if err != nil || code != "" {
		t.Fatalf("get auth plan: data=%#v code=%s err=%v", authPlan, code, err)
	}
	if _, exists := authPlan["distributionTemplateId"]; exists {
		t.Fatalf("auth plan must not return a distribution template: data=%#v", authPlan)
	}
	if authPlan["validFrom"] != "2026-01-01" || authPlan["validTo"] != "2026-12-31" || fmt.Sprint(authPlan["regionMode"]) != "DESIGNATED" || fmt.Sprint(authPlan["regionCodes"]) != "[350200 440100]" || fmt.Sprint(authPlan["scenes"]) != "[PRODUCT_SALE]" {
		t.Fatalf("auth plan response did not return normalized scope fields: %#v", authPlan)
	}
	var scope struct {
		TermType               string  `gorm:"column:term_type"`
		TermValue              *int    `gorm:"column:term_value"`
		ValidFrom              string  `gorm:"column:valid_from"`
		ValidTo                string  `gorm:"column:valid_to"`
		RegionMode             string  `gorm:"column:region_mode"`
		RegionCodesJSON        string  `gorm:"column:region_codes_json"`
		DistributionTemplateID *uint64 `gorm:"column:distribution_template_id"`
	}
	if err := db.Table("auth_plan_version").Select("term_type,term_value,DATE_FORMAT(valid_from, '%Y-%m-%d') AS valid_from,DATE_FORMAT(valid_to, '%Y-%m-%d') AS valid_to,region_mode,region_codes_json,distribution_template_id").Where("id=?", createdPlan["versionId"]).Take(&scope).Error; err != nil {
		t.Fatal(err)
	}
	var regionCodes []string
	if err := json.Unmarshal([]byte(scope.RegionCodesJSON), &regionCodes); err != nil {
		t.Fatalf("decode persisted region codes: %v", err)
	}
	if scope.TermType != "DATE_RANGE" || scope.TermValue != nil || scope.ValidFrom != "2026-01-01" || scope.ValidTo != "2026-12-31" || scope.RegionMode != "DESIGNATED" || len(regionCodes) != 2 || regionCodes[0] != "350200" || regionCodes[1] != "440100" {
		t.Fatalf("auth plan scope was not persisted correctly: %#v", scope)
	}
	if scope.DistributionTemplateID != nil {
		t.Fatalf("new auth plan version must not persist a distribution template: %#v", scope)
	}

	listed, err := service.ListAuthPlans(context.Background(), false)
	if err != nil {
		t.Fatalf("list auth plans: %v", err)
	}
	found := false
	for _, item := range listed["items"].([]authPlanListItem) {
		if item.PlanNo == planNo {
			found = true
			if item.ID != fmt.Sprint(planID) {
				t.Fatalf("list returned version ID as plan ID: got=%v want=%v item=%#v", item.ID, planID, item)
			}
			if item.CurrentVersionID != fmt.Sprint(createdPlan["versionId"]) {
				t.Fatalf("list returned wrong current version ID: got=%v want=%v", item.CurrentVersionID, createdPlan["versionId"])
			}
		}
	}
	if !found {
		t.Fatalf("created plan %s missing from list: %#v", planNo, listed)
	}

	for _, step := range []struct {
		action string
		result string
		want   string
	}{
		{action: "submit", want: "PENDING_COMPLIANCE"},
		{action: "review", result: "PASS", want: "CONFIRMED"},
		{action: "enable", want: "ENABLED"},
	} {
		var current struct {
			Version uint32
		}
		if err := db.Table("auth_plan").Select("version").Where("id=?", planID).Take(&current).Error; err != nil {
			t.Fatal(err)
		}
		if _, code, err := service.ChangeAuthStatus(context.Background(), operator, planID, current.Version, step.action, step.result, "状态流转测试", "127.0.0.1"); err != nil || code != "" {
			t.Fatalf("%s auth plan: code=%s err=%v", step.action, code, err)
		}
		var statuses struct {
			PlanStatus    string `gorm:"column:plan_status"`
			VersionStatus string `gorm:"column:version_status"`
		}
		if err := db.Table("auth_plan p").Joins("JOIN auth_plan_version v ON v.id=p.current_version_id").Select("p.status AS plan_status,v.status AS version_status").Where("p.id=?", planID).Take(&statuses).Error; err != nil {
			t.Fatal(err)
		}
		if statuses.PlanStatus != step.want || statuses.VersionStatus != step.want {
			t.Fatalf("%s must synchronize plan and current version status: %#v", step.action, statuses)
		}
	}

	if err := db.Table("finance_distribution_plan").Where("id = ?", templateID).Update("is_bound", true).Error; err != nil {
		t.Fatal(err)
	}
	_, code, err = service.ChangeTemplateStatus(context.Background(), operator, templateID, 1, false, "测试锁定", "127.0.0.1")
	if err != nil || code != "AUTH_PLAN_BOUND_LOCKED" {
		t.Fatalf("bound template should be locked: code=%s err=%v", code, err)
	}
}

func intPlanPtr(value int) *int { return &value }

func intPtr(value int) *int { return &value }

func mustPlanID(t *testing.T, value string) uint64 {
	var id uint64
	if _, err := fmt.Sscan(value, &id); err != nil || id == 0 {
		t.Fatalf("invalid id %q: %v", value, err)
	}
	return id
}
