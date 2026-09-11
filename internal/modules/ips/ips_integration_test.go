package ips

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/evidence"
	"xingyanshu-backend/internal/platform/idgen"
)

type passModerator struct{ sequence int }

func (m *passModerator) Moderate(_ context.Context, _ contentmoderation.Request) (contentmoderation.Result, error) {
	m.sequence++
	return contentmoderation.Result{Provider: "TEST_TMS", ProviderRequestNo: fmt.Sprintf("TEST-TMS-%d-%d", time.Now().UnixNano(), m.sequence), Decision: contentmoderation.DecisionPass}, nil
}

// TestIPEntryWithMySQL 验证城市 IP 草稿、分账比例、DCI 代办和提交前置校验写入真实测试库。
func TestIPEntryWithMySQL(t *testing.T) {
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
	suffix := time.Now().UnixNano()
	cityID := idgen.Next()
	templateID := idgen.Next()
	versionID := idgen.Next()
	planID := idgen.Next()
	fileIDs := []uint64{idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next()}
	mustIPExec(t, db.Table("sys_org").Create(map[string]any{"id": cityID, "status": "ACTIVE", "version": 1, "org_code": fmt.Sprintf("TEST_CITY_%d", suffix), "org_name": "IP录入测试城市", "org_type": "CITY", "city_code": "359999"}).Error)
	defer db.Exec("DELETE FROM sys_org WHERE id = ?", cityID)
	for i, id := range fileIDs {
		mustIPExec(t, db.Table("file_object").Create(map[string]any{"id": id, "status": "ACTIVE", "version": 1, "created_by": 10401, "file_no": fmt.Sprintf("IPFILE-%d", id), "storage_key": fmt.Sprintf("test/ip/%d", id), "original_name": fmt.Sprintf("test-%d.pdf", i), "mime_type": "application/pdf", "file_size": 10, "file_hash": fmt.Sprintf("hash-%d", id), "security_level": "PRIVATE", "upload_status": "SUCCESS"}).Error)
		defer db.Exec("DELETE FROM file_object WHERE id = ?", id)
	}
	mustIPExec(t, db.Table("finance_distribution_plan").Create(map[string]any{"id": templateID, "status": "ENABLED", "version": 1, "created_by": 10401, "distribution_no": fmt.Sprintf("DST_IP_%d", suffix), "distribution_name": "IP录入测试模板", "distribution_type": "NORMAL_SHARE", "current_version_no": 1, "is_bound": false}).Error)
	defer db.Exec("DELETE FROM finance_distribution_plan_item WHERE distribution_plan_id = ?", templateID)
	defer db.Exec("DELETE FROM finance_distribution_plan WHERE id = ?", templateID)
	for _, name := range []string{"RIGHT_HOLDER", "CITY_OPERATOR", "PLATFORM"} {
		mustIPExec(t, db.Table("finance_distribution_plan_item").Create(map[string]any{"id": idgen.Next(), "created_by": 10401, "distribution_plan_id": templateID, "version_no": 1, "participant_type": name, "is_required": true, "ratio": nil}).Error)
	}
	mustIPExec(t, db.Table("auth_plan").Create(map[string]any{"id": planID, "status": "ENABLED", "version": 1, "created_by": 10401, "plan_no": fmt.Sprintf("AP_IP_%d", suffix), "plan_name": "IP录入测试授权方案", "current_version_id": versionID, "is_bound": false}).Error)
	defer db.Exec("DELETE FROM auth_plan_version WHERE plan_id = ?", planID)
	defer db.Exec("DELETE FROM auth_plan WHERE id = ?", planID)
	mustIPExec(t, db.Table("auth_plan_version").Create(map[string]any{"id": versionID, "status": "ENABLED", "version": 1, "created_by": 10401, "plan_id": planID, "distribution_template_id": templateID, "version_no": 1, "authorization_mode": "NORMAL_SHARE", "term_type": "MONTH", "term_value": 12, "region_mode": "NATIONWIDE", "scenes_json": "[\"PRODUCT\"]", "commercial_allowed": true, "derivative_allowed": true, "n_creation_allowed": false, "content_type": "PRODUCT", "quota_total": 10, "price_amount": "100.00"}).Error)

	service := NewService(db, &passModerator{}, cfg.Auth.JWTSecret, evidence.NewService(db, evidence.MockProvider{}, "test-callback-secret"))
	operator := identity.CurrentAdmin{UserID: 10401, OrgID: cityID, RoleCodes: []string{"CITY_OPERATOR"}, PermissionCodes: map[string]struct{}{cityIPPermission: {}}}
	input := testIPInput(cityID, versionID, templateID, fileIDs)
	created, code, err := service.Create(context.Background(), operator, input, "127.0.0.1")
	if err != nil || code != "" {
		t.Fatalf("create IP: data=%#v code=%s err=%v", created, code, err)
	}
	ipID := mustIPID(t, created["id"].(string))
	defer cleanupIP(t, db, ipID)
	var distributionCount int64
	mustIPExec(t, db.Table("ip_distribution_item").Where("ip_id=?", ipID).Count(&distributionCount).Error)
	if distributionCount != 3 {
		t.Fatalf("expected 3 distribution items, got %d", distributionCount)
	}
	assistance, code, err := service.CreateDCIAssistance(context.Background(), operator, ipID, 1, DCIRequest{ContactName: "测试联系人", ContactMobile: "13800138000", RequestNote: "请协助办理"}, "127.0.0.1")
	if err != nil || code != "" {
		t.Fatalf("create DCI assistance: data=%#v code=%s err=%v", assistance, code, err)
	}
	_, code, err = service.Submit(context.Background(), operator, ipID, 2, SubmitRequest{Comment: "资料齐全提交"}, "127.0.0.1")
	if err != nil || code != "IP_DCI_CERTIFICATE_REQUIRED" {
		t.Fatalf("submit without DCI should fail: code=%s err=%v", code, err)
	}
	input.DCICertificateNo = "DCI-TEST-001"
	input.DCICertificateFileID = fileIDs[3]
	updated, code, err := service.Update(context.Background(), operator, ipID, 2, input, "127.0.0.1")
	if err != nil || code != "" {
		t.Fatalf("update DCI: data=%#v code=%s err=%v", updated, code, err)
	}
	submitted, code, err := service.Submit(context.Background(), operator, ipID, 3, SubmitRequest{Comment: "资料齐全提交"}, "127.0.0.1")
	if err != nil || code != "" || submitted["status"] != "PENDING_COMPLIANCE" {
		t.Fatalf("submit IP: data=%#v code=%s err=%v", submitted, code, err)
	}
	compliance := identity.CurrentAdmin{UserID: 10402, RoleCodes: []string{"COPYRIGHT_COMPLIANCE"}, PermissionCodes: map[string]struct{}{complianceIPPermission: {}}}
	pending, code, err := service.ListPendingCompliance(context.Background(), compliance, "测试文化", "UPLOADED", 1, 20)
	if err != nil || code != "" || len(pending["items"].([]map[string]any)) != 1 || pending["items"].([]map[string]any)[0]["nextAction"] != "VERIFY_DCI" {
		t.Fatalf("list pending compliance IP: data=%#v code=%s err=%v", pending, code, err)
	}
	requests, code, err := service.ListDCIAssistance(context.Background(), compliance, "PENDING", "", 1, 20, "127.0.0.1")
	if err != nil || code != "" || len(requests["items"].([]map[string]any)) != 1 || requests["items"].([]map[string]any)[0]["contactMobile"] != "13800138000" {
		t.Fatalf("list DCI assistance: data=%#v code=%s err=%v", requests, code, err)
	}
	requestID := mustIPID(t, assistance["id"].(string))
	_, code, err = service.HandleDCIAssistance(context.Background(), compliance, requestID, 1, DCIHandleRequest{HandlingStatus: "CONTACTED", HandlingNote: "已联系申请人"}, "127.0.0.1")
	if err != nil || code != "" {
		t.Fatalf("handle DCI assistance: code=%s err=%v", code, err)
	}
	verified, code, err := service.VerifyDCI(context.Background(), compliance, ipID, 4, DCIVerificationRequest{Result: "PASS", Comment: "DCI证书核验通过"}, "127.0.0.1")
	if err != nil || code != "" || verified["dciCertificateStatus"] != "VERIFIED" {
		t.Fatalf("verify DCI: data=%#v code=%s err=%v", verified, code, err)
	}
	pending, code, err = service.ListPendingCompliance(context.Background(), compliance, "", "VERIFIED", 1, 20)
	if err != nil || code != "" || len(pending["items"].([]map[string]any)) != 1 || pending["items"].([]map[string]any)[0]["nextAction"] != "CONFIRM_RIGHTS" {
		t.Fatalf("list DCI-verified pending IP: data=%#v code=%s err=%v", pending, code, err)
	}
	confirmed, code, err := service.ReviewRights(context.Background(), compliance, ipID, 5, RightsReviewRequest{Result: "CONFIRM", Comment: "权属与授权边界确认", AuthPlanVersionID: versionID, DistributionTemplateID: templateID, DistributionItems: input.DistributionItems}, "127.0.0.1")
	if err != nil || code != "" || confirmed["status"] != "LISTED" || confirmed["mallVisible"] != true || confirmed["evidenceReceiptNo"] == "" {
		t.Fatalf("confirm rights: data=%#v code=%s err=%v", confirmed, code, err)
	}
	var bindingCount, successEvidenceCount int64
	var chainProvider string
	mustIPExec(t, db.Table("ip_auth_binding").Where("ip_id=?", ipID).Count(&bindingCount).Error)
	mustIPExec(t, db.Table("evidence_record").Where("object_type='IP' AND object_id=? AND status='SUCCESS'", ipID).Count(&successEvidenceCount).Error)
	mustIPExec(t, db.Table("evidence_record").Select("chain_provider").Where("object_type='IP' AND object_id=?", ipID).Scan(&chainProvider).Error)
	if bindingCount != 1 || successEvidenceCount != 1 || chainProvider != "MOCK_EVIDENCE" {
		t.Fatalf("expected binding and evidence success, binding=%d evidence=%d provider=%s", bindingCount, successEvidenceCount, chainProvider)
	}
	var asset struct {
		Status      string
		MallVisible bool `gorm:"column:mall_visible"`
		Version     uint32
	}
	mustIPExec(t, db.Table("ip_asset").Where("id=?", ipID).Take(&asset).Error)
	if asset.Status != "LISTED" || !asset.MallVisible || asset.Version != 6 {
		t.Fatalf("expected IP auto-listed in same confirmation, status=%s mallVisible=%v version=%d", asset.Status, asset.MallVisible, asset.Version)
	}
	var history struct{ FromStatus, ToStatus string }
	mustIPExec(t, db.Table("biz_status_history").Where("object_type='IP' AND object_id=?", ipID).Order("id DESC").Take(&history).Error)
	if history.FromStatus != "PENDING_COMPLIANCE" || history.ToStatus != "LISTED" {
		t.Fatalf("expected direct status history PENDING_COMPLIANCE -> LISTED, got %s -> %s", history.FromStatus, history.ToStatus)
	}
	var auditAction string
	mustIPExec(t, db.Table("sys_audit_log").Select("action").Where("object_type='IP' AND object_id=?", ipID).Order("id DESC").Take(&auditAction).Error)
	if auditAction != "IP_RIGHTS_CONFIRMED_AND_LISTED" {
		t.Fatalf("expected auto-list audit action, got %s", auditAction)
	}
}

func TestIPRoutesDoNotExposeManualListing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), nil, func(c *gin.Context) {})
	for _, route := range router.Routes() {
		if route.Method == "POST" && route.Path == "/api/v1/ips/:ipId/list" {
			t.Fatalf("manual IP listing route must not be exposed")
		}
	}
}

func testIPInput(cityID, planVersionID, templateID uint64, fileIDs []uint64) IPInput {
	return IPInput{IPName: "测试文化IP", IPType: "CULTURAL", SourceType: "CITY_COLLECTION", MainCityOrgID: cityID, ShortIntro: "测试简介", DetailContent: "测试完整介绍", CultureStory: "测试文化故事", IPFeature: "测试特色", CreativeDirections: []string{"文创商品"}, ApplicableProductTypes: []string{"CULTURAL_PRODUCT"}, ApplicableActivityTypes: []string{"EXHIBITION"}, PublicCopyrightNotice: "版权说明", PurchaseNotice: "购买须知", CoverFileID: fileIDs[0], DetailFileIDs: []uint64{fileIDs[1]}, RightHolder: RightHolder{HolderType: "INSTITUTION", HolderName: "测试权利机构", AcquisitionMethod: "ORIGINAL_CREATION", RightsDescription: "测试权属说明"}, RightDocuments: []RightDocument{{DocumentType: "COPYRIGHT_CERTIFICATE", DocumentNo: "RIGHT-TEST-001", FileID: fileIDs[2]}}, AuthPlanVersionID: planVersionID, DistributionTemplateID: templateID, DistributionItems: []DistributionItem{{ParticipantType: "RIGHT_HOLDER", Ratio: "0.6"}, {ParticipantType: "CITY_OPERATOR", Ratio: "0.2"}, {ParticipantType: "PLATFORM", Ratio: "0.2"}}}
}
func cleanupIP(t *testing.T, db *gorm.DB, ipID uint64) {
	for _, table := range []string{"ip_auth_binding", "ip_distribution_item", "ip_draft_auth_selection", "ip_right_document", "ip_right_holder", "ip_dci_assistance_request", "ip_evaluation_report", "ip_review_record"} {
		if err := db.Table(table).Where("ip_id=?", ipID).Delete(nil).Error; err != nil {
			t.Logf("cleanup %s: %v", table, err)
		}
	}
	if err := db.Table("evidence_record").Where("object_type='IP' AND object_id=?", ipID).Delete(nil).Error; err != nil {
		t.Logf("cleanup evidence_record: %v", err)
	}
	db.Table("file_relation").Where("object_type='IP' AND object_id=?", ipID).Delete(nil)
	db.Table("ip_asset").Where("id=?", ipID).Delete(nil)
}
func mustIPExec(t *testing.T, err error) {
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
}
func mustIPID(t *testing.T, value string) uint64 {
	var id uint64
	if _, err := fmt.Sscan(value, &id); err != nil || id == 0 {
		t.Fatalf("invalid ID %q: %v", value, err)
	}
	return id
}
