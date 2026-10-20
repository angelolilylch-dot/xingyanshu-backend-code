package ips

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/evidence"
	"xingyanshu-backend/internal/platform/filestore"
	"xingyanshu-backend/internal/platform/idgen"
)

type passModerator struct{ sequence int }

type packageTestStorage struct{ objects map[string][]byte }

func (s *packageTestStorage) Put(_ context.Context, key, _ string, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.objects[key] = data
	return nil
}
func (s *packageTestStorage) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("missing test storage object %s", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (s *packageTestStorage) SignDownloadURL(_ context.Context, key string, _ time.Duration) (string, error) {
	if _, ok := s.objects[key]; !ok {
		return "", fmt.Errorf("missing test storage object %s", key)
	}
	return "https://download.test/" + key, nil
}

var _ filestore.Storage = (*packageTestStorage)(nil)

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
	storage := &packageTestStorage{objects: map[string][]byte{}}
	mustIPExec(t, db.Table("sys_org").Create(map[string]any{"id": cityID, "status": "ACTIVE", "version": 1, "org_code": fmt.Sprintf("TEST_CITY_%d", suffix), "org_name": "IP录入测试城市", "org_type": "CITY", "city_code": "359999"}).Error)
	defer db.Exec("DELETE FROM sys_org WHERE id = ?", cityID)
	for i, id := range fileIDs {
		mustIPExec(t, db.Table("file_object").Create(map[string]any{"id": id, "status": "ACTIVE", "version": 1, "created_by": 10401, "file_no": fmt.Sprintf("IPFILE-%d", id), "storage_key": fmt.Sprintf("test/ip/%d", id), "original_name": fmt.Sprintf("test-%d.pdf", i), "mime_type": "application/pdf", "file_size": 10, "file_hash": fmt.Sprintf("hash-%d", id), "security_level": "PRIVATE", "upload_status": "SUCCESS"}).Error)
		storage.objects[fmt.Sprintf("test/ip/%d", id)] = []byte(fmt.Sprintf("fixture-%d", i))
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
	mustIPExec(t, db.Table("auth_plan_version").Create(map[string]any{"id": versionID, "status": "ENABLED", "version": 1, "created_by": 10401, "plan_id": planID, "distribution_template_id": templateID, "version_no": 1, "authorization_mode": "NORMAL_SHARE", "term_type": "MONTH", "term_value": 12, "region_mode": "NATIONWIDE", "scenes_json": "[\"PRODUCT_SALE\"]", "commercial_allowed": true, "derivative_allowed": true, "n_creation_allowed": false, "content_type": "PRODUCT_ONLY", "quota_total": 10}).Error)

	service := NewService(db, &passModerator{}, cfg.Auth.JWTSecret, evidence.NewService(db, evidence.MockProvider{}, "test-callback-secret"), storage, cfg.File)
	operator := identity.CurrentAdmin{UserID: 10401, OrgID: cityID, RoleCodes: []string{"CITY_OPERATOR"}, PermissionCodes: map[string]struct{}{cityIPPermission: {}}}
	input := testIPInput(cityID, versionID, templateID, fileIDs)
	input.DCIAssistanceRequested = true
	input.DCIAssistanceRequest = &DCIRequest{ContactName: "测试联系人", ContactMobile: "13800138000", RequestNote: "请协助办理"}
	invalidInput := input
	invalidInput.DCICertificateNo = "DCI-MUTEX-001"
	if _, code, err := service.Create(context.Background(), operator, invalidInput, "127.0.0.1"); err != nil || code != "VALIDATION_FORMAT_INVALID" {
		t.Fatalf("DCI assistance and certificate must be mutually exclusive: code=%s err=%v", code, err)
	}
	created, code, err := service.Create(context.Background(), operator, input, "127.0.0.1")
	if err != nil || code != "" {
		t.Fatalf("create IP: data=%#v code=%s err=%v", created, code, err)
	}
	ipID := mustIPID(t, created["id"].(string))
	defer cleanupIP(t, db, ipID)
	if created["dciCertificateStatus"] != "ASSISTANCE_REQUESTED" || created["dciAssistanceRequestId"] == nil {
		t.Fatalf("expected IP and DCI assistance request in one transaction, got %#v", created)
	}
	requestID := mustIPID(t, created["dciAssistanceRequestId"].(string))
	var distributionCount int64
	mustIPExec(t, db.Table("ip_distribution_item").Where("ip_id=?", ipID).Count(&distributionCount).Error)
	if distributionCount != 3 {
		t.Fatalf("expected 3 distribution items, got %d", distributionCount)
	}
	report, code, err := service.CreateEvaluation(context.Background(), operator, ipID, EvaluationRequest{ReportNo: fmt.Sprintf("EVAL-%d", suffix), InstitutionName: "测试评估机构", ReportDate: "2026-09-11", FileID: fileIDs[1], EvaluationSuggestedPriceAmount: "299.00"}, "127.0.0.1")
	if err != nil || code != "" || report["reportNo"] == nil {
		t.Fatalf("create evaluation report: data=%#v code=%s err=%v", report, code, err)
	}
	var evaluationPrice string
	mustIPExec(t, db.Table("ip_evaluation_report").Select("evaluation_suggested_price_amount").Where("ip_id=?", ipID).Take(&evaluationPrice).Error)
	if evaluationPrice != "299.00" {
		t.Fatalf("expected optional evaluation suggested price 299.00, got %s", evaluationPrice)
	}
	complianceCurrent := identity.CurrentAdmin{UserID: 10402, RoleCodes: []string{"COPYRIGHT_COMPLIANCE"}, PermissionCodes: map[string]struct{}{complianceIPPermission: {}}}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), service, func(c *gin.Context) {
		c.Set("identity.currentAdmin", complianceCurrent)
		c.Next()
	})
	packageRequest := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/compliance/dci-assistance-requests/%d/application-package", requestID), strings.NewReader(`{"includeEvaluationReport":true}`))
	packageRequest.Header.Set("Content-Type", "application/json")
	packageRequest.Header.Set("Idempotency-Key", fmt.Sprintf("dci-package-%d", suffix))
	packageResponse := httptest.NewRecorder()
	router.ServeHTTP(packageResponse, packageRequest)
	if packageResponse.Code != http.StatusOK {
		t.Fatalf("POST DCI assistance package status=%d body=%s", packageResponse.Code, packageResponse.Body.String())
	}
	var packageBody struct {
		Code string `json:"code"`
		Data struct {
			PackageFileID string `json:"packageFileId"`
			DownloadURL   string `json:"downloadUrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(packageResponse.Body.Bytes(), &packageBody); err != nil || packageBody.Code != "OK" || packageBody.Data.PackageFileID == "" || !strings.Contains(packageBody.Data.DownloadURL, "https://download.test/") {
		t.Fatalf("unexpected DCI assistance package response: body=%s err=%v", packageResponse.Body.String(), err)
	}
	packageFileID := mustIPID(t, packageBody.Data.PackageFileID)
	defer db.Exec("DELETE FROM file_relation WHERE file_id = ?", packageFileID)
	defer db.Exec("DELETE FROM file_object WHERE id = ?", packageFileID)
	assertDCIPackageContents(t, storage, fmt.Sprint(packageFileID))
	_, code, err = service.Submit(context.Background(), operator, ipID, 1, SubmitRequest{Comment: "资料齐全提交"}, "127.0.0.1")
	if err != nil || code != "IP_DCI_CERTIFICATE_REQUIRED" {
		t.Fatalf("submit without DCI should fail: code=%s err=%v", code, err)
	}
	updated, code, err := service.SubmitDCICertificate(context.Background(), operator, ipID, 1, DCICertificateInput{CertificateNo: "DCI-TEST-001", CertificateFileID: fileIDs[3]}, "127.0.0.1")
	if err != nil || code != "" {
		t.Fatalf("update DCI: data=%#v code=%s err=%v", updated, code, err)
	}
	mustIPExec(t, db.Table("ip_asset").Where("id=?", ipID).Update("dci_certificate_status", "REJECTED").Error)
	_, code, err = service.Submit(context.Background(), operator, ipID, 2, SubmitRequest{Comment: "资料齐全提交"}, "127.0.0.1")
	if err != nil || code != "IP_DCI_CERTIFICATE_REQUIRED" {
		t.Fatalf("submit with rejected DCI should fail: code=%s err=%v", code, err)
	}
	mustIPExec(t, db.Table("ip_asset").Where("id=?", ipID).Update("dci_certificate_status", "UPLOADED").Error)
	submitted, code, err := service.Submit(context.Background(), operator, ipID, 2, SubmitRequest{Comment: "资料齐全提交"}, "127.0.0.1")
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
	_, code, err = service.HandleDCIAssistance(context.Background(), compliance, requestID, 1, DCIHandleRequest{HandlingStatus: "CONTACTED", HandlingNote: "已联系申请人"}, "127.0.0.1")
	if err != nil || code != "" {
		t.Fatalf("handle DCI assistance: code=%s err=%v", code, err)
	}
	verified, code, err := service.VerifyDCI(context.Background(), compliance, ipID, 3, DCIVerificationRequest{Result: "PASS", Comment: "DCI证书核验通过"}, "127.0.0.1")
	if err != nil || code != "" || verified["dciCertificateStatus"] != "VERIFIED" {
		t.Fatalf("verify DCI: data=%#v code=%s err=%v", verified, code, err)
	}
	pending, code, err = service.ListPendingCompliance(context.Background(), compliance, "", "VERIFIED", 1, 20)
	if err != nil || code != "" || len(pending["items"].([]map[string]any)) != 1 || pending["items"].([]map[string]any)[0]["nextAction"] != "CONFIRM_RIGHTS" {
		t.Fatalf("list DCI-verified pending IP: data=%#v code=%s err=%v", pending, code, err)
	}
	confirmed, code, err := service.ReviewRights(context.Background(), compliance, ipID, 4, RightsReviewRequest{Result: "CONFIRM", Comment: "权属与授权边界确认", AuthPlanVersionID: versionID, DistributionTemplateID: templateID, DistributionItems: input.DistributionItems, SuggestedTransactionPriceAmount: "199.00"}, "127.0.0.1")
	if err != nil || code != "" || confirmed["status"] != "LISTED" || confirmed["mallVisible"] != true || confirmed["evidenceReceiptNo"] == "" || confirmed["evidenceTransactionHash"] == "" {
		t.Fatalf("confirm rights: data=%#v code=%s err=%v", confirmed, code, err)
	}
	var bindingCount, successEvidenceCount int64
	var evidence struct {
		ChainProvider   string `gorm:"column:chain_provider"`
		TransactionHash string `gorm:"column:chain_transaction_hash"`
	}
	mustIPExec(t, db.Table("ip_auth_binding").Where("ip_id=?", ipID).Count(&bindingCount).Error)
	mustIPExec(t, db.Table("evidence_record").Where("object_type='IP' AND object_id=? AND status='SUCCESS'", ipID).Count(&successEvidenceCount).Error)
	mustIPExec(t, db.Table("evidence_record").Select("chain_provider,chain_transaction_hash").Where("object_type='IP' AND object_id=?", ipID).Take(&evidence).Error)
	if bindingCount != 1 || successEvidenceCount != 1 || evidence.ChainProvider != "MOCK_EVIDENCE" || evidence.TransactionHash == "" || confirmed["evidenceTransactionHash"] != evidence.TransactionHash {
		t.Fatalf("expected binding and evidence success with transaction hash, binding=%d evidence=%d provider=%s hash=%s", bindingCount, successEvidenceCount, evidence.ChainProvider, evidence.TransactionHash)
	}
	var asset struct {
		Status      string
		MallVisible bool `gorm:"column:mall_visible"`
		Version     uint32
	}
	mustIPExec(t, db.Table("ip_asset").Where("id=?", ipID).Take(&asset).Error)
	if asset.Status != "LISTED" || !asset.MallVisible || asset.Version != 5 {
		t.Fatalf("expected IP auto-listed in same confirmation, status=%s mallVisible=%v version=%d", asset.Status, asset.MallVisible, asset.Version)
	}
	var transactionPrice string
	mustIPExec(t, db.Table("ip_asset").Select("suggested_transaction_price_amount").Where("id=?", ipID).Scan(&transactionPrice).Error)
	if transactionPrice != "199.00" {
		t.Fatalf("expected confirmed transaction price 199.00, got %s", transactionPrice)
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
	paused, code, err := service.Pause(context.Background(), identity.CurrentAdmin{UserID: 10403, RoleCodes: []string{"PLATFORM_ADMIN"}, PermissionCodes: map[string]struct{}{"AUTH_PLAN_MANAGE": {}}}, ipID, 5, IPStatusChangeRequest{Comment: "运营风险排查"}, "127.0.0.1")
	if err != nil || code != "" || paused["status"] != "PAUSED" || paused["mallVisible"] != false {
		t.Fatalf("pause listed IP: data=%#v code=%s err=%v", paused, code, err)
	}
	resumed, code, err := service.Resume(context.Background(), identity.CurrentAdmin{UserID: 10403, RoleCodes: []string{"PLATFORM_ADMIN"}, PermissionCodes: map[string]struct{}{"AUTH_PLAN_MANAGE": {}}}, ipID, 6, IPStatusChangeRequest{Comment: "风险排查完成"}, "127.0.0.1")
	if err != nil || code != "" || resumed["status"] != "LISTED" || resumed["mallVisible"] != true {
		t.Fatalf("resume paused IP: data=%#v code=%s err=%v", resumed, code, err)
	}
	delisted, code, err := service.Delist(context.Background(), identity.CurrentAdmin{UserID: 10403, RoleCodes: []string{"PLATFORM_ADMIN"}, PermissionCodes: map[string]struct{}{"AUTH_PLAN_MANAGE": {}}}, ipID, 7, IPStatusChangeRequest{Comment: "停止授权销售"}, "127.0.0.1")
	if err != nil || code != "" || delisted["status"] != "DELISTED" || delisted["mallVisible"] != false {
		t.Fatalf("delist listed IP: data=%#v code=%s err=%v", delisted, code, err)
	}
}

func assertDCIPackageContents(t *testing.T, storage *packageTestStorage, packageFileID string) {
	t.Helper()
	var zipData []byte
	for key, value := range storage.objects {
		if strings.HasSuffix(key, packageFileID+".zip") {
			zipData = value
			break
		}
	}
	if len(zipData) == 0 {
		t.Fatal("generated DCI package zip not found in storage")
	}
	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatalf("open DCI package zip: %v", err)
	}
	entries := map[string]string{}
	for _, file := range reader.File {
		body, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, readErr := io.ReadAll(body)
		_ = body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		entries[file.Name] = string(content)
	}
	if !strings.Contains(entries["00_DCI代办申请资料.md"], "测试联系人") || !strings.Contains(entries["02_IP作品资料/IP作品说明.md"], "测试文化IP") {
		t.Fatalf("generated Markdown files missing expected contents: %#v", entries)
	}
	if len(entries) < 5 {
		t.Fatalf("expected Markdown plus source material files, entries=%#v", entries)
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
	foundPause := false
	for _, route := range router.Routes() {
		if route.Method == "POST" && route.Path == "/api/v1/ips/:ipId/pause" {
			foundPause = true
		}
	}
	if !foundPause {
		t.Fatalf("IP pause route must be exposed")
	}
	foundResume := false
	for _, route := range router.Routes() {
		if route.Method == "POST" && route.Path == "/api/v1/ips/:ipId/resume" {
			foundResume = true
		}
	}
	if !foundResume {
		t.Fatalf("IP resume route must be exposed")
	}
	foundDelist := false
	for _, route := range router.Routes() {
		if route.Method == "POST" && route.Path == "/api/v1/ips/:ipId/delist" {
			foundDelist = true
		}
	}
	if !foundDelist {
		t.Fatalf("IP delist route must be exposed")
	}
}

func testIPInput(cityID, planVersionID, templateID uint64, fileIDs []uint64) IPInput {
	return IPInput{IPName: "测试文化IP", IPType: "CULTURAL", SourceType: "CITY_COLLECTION", MainCityOrgID: cityID, ShortIntro: "测试简介", DetailContent: "测试完整介绍", CultureStory: "测试文化故事", IPFeature: "测试特色", CreativeDirections: []string{"文创商品"}, ApplicableProductTypes: []string{"CULTURAL_PRODUCT"}, ApplicableActivityTypes: []string{"EXHIBITION"}, PublicCopyrightNotice: "版权说明", PurchaseNotice: "购买须知", SuggestedTransactionPriceAmount: "199.00", CoverFileID: fileIDs[0], DetailFileIDs: []uint64{fileIDs[1]}, RightHolder: RightHolder{HolderType: "INSTITUTION", HolderName: "测试权利机构", AcquisitionMethod: "ORIGINAL_CREATION", RightsDescription: "测试权属说明"}, RightDocuments: []RightDocument{{DocumentType: "COPYRIGHT_CERTIFICATE", DocumentNo: "RIGHT-TEST-001", FileID: fileIDs[2]}}, MaterialPack: &MaterialPackInput{PackName: "测试入库素材包", Items: []MaterialItemInput{{FileID: fileIDs[1], MaterialRole: "SOURCE", SortNo: 1, Deliverable: true}}}, AuthPlanVersionID: planVersionID, DistributionTemplateID: templateID, DistributionItems: []DistributionItem{{ParticipantType: "RIGHT_HOLDER", Ratio: "0.6"}, {ParticipantType: "CITY_OPERATOR", Ratio: "0.2"}, {ParticipantType: "PLATFORM", Ratio: "0.2"}}}
}
func cleanupIP(t *testing.T, db *gorm.DB, ipID uint64) {
	var packIDs []uint64
	db.Table("ip_material_pack").Where("ip_id=?", ipID).Pluck("id", &packIDs)
	for _, packID := range packIDs {
		db.Table("ip_material_pack_item").Where("material_pack_id=?", packID).Delete(nil)
		db.Table("file_relation").Where("object_type='IP_MATERIAL_PACK' AND object_id=?", packID).Delete(nil)
	}
	db.Table("ip_material_pack").Where("ip_id=?", ipID).Delete(nil)
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
