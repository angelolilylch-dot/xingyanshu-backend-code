package payments

import (
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
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/authorizations"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/modules/files"
	"xingyanshu-backend/internal/platform/evidence"
	"xingyanshu-backend/internal/platform/filestore"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idgen"
)

type contractTestStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (s *contractTestStorage) Put(_ context.Context, key, _ string, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
	return nil
}
func (s *contractTestStorage) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("missing object %s", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (s *contractTestStorage) SignDownloadURL(_ context.Context, key string, _ time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objects[key]; !ok {
		return "", fmt.Errorf("missing object %s", key)
	}
	return "https://download.test/" + key, nil
}

var _ filestore.Storage = (*contractTestStorage)(nil)

type failingEvidenceProvider struct{}

func (failingEvidenceProvider) Submit(context.Context, evidence.SubmitRequest) (evidence.SubmitResult, error) {
	return evidence.SubmitResult{}, fmt.Errorf("simulated evidence outage")
}
func (failingEvidenceProvider) Query(context.Context, string) (evidence.SubmitResult, error) {
	return evidence.SubmitResult{}, fmt.Errorf("simulated evidence outage")
}

type failingContractStorage struct{}

func (failingContractStorage) Put(context.Context, string, string, io.Reader) error {
	return fmt.Errorf("simulated file storage outage")
}
func (failingContractStorage) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("simulated file storage outage")
}
func (failingContractStorage) SignDownloadURL(context.Context, string, time.Duration) (string, error) {
	return "", fmt.Errorf("simulated file storage outage")
}

func TestMockPaymentContractAndEvidenceWithMySQLHTTP(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1 to run MySQL HTTP integration test")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
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

	userID, creatorID, sessionID := idgen.Next(), idgen.Next(), idgen.Next()
	orderID, ipID, planVersionID := idgen.Next(), idgen.Next(), idgen.Next()
	materialFileID, materialPackID, materialItemID := idgen.Next(), idgen.Next(), idgen.Next()
	materialManifest := strings.Repeat("a", 64)
	defer cleanupPaymentFixture(db, userID, creatorID, orderID, ipID)
	mustPayment(t, db.Table("sys_user").Create(map[string]any{"id": userID, "status": "ACTIVE", "version": 1, "username": fmt.Sprintf("aut03_%d", userID), "password_hash": "not-used", "display_name": "AUT03 测试创作者"}).Error)
	mustPayment(t, db.Table("creator_profile").Create(map[string]any{"id": creatorID, "user_id": userID, "status": "ACTIVE", "version": 1, "subject_type": "ENTERPRISE", "display_name": "AUT03 创作工作室", "identity_status": "VERIFIED", "letter_status": "OPTIONAL"}).Error)
	mustPayment(t, db.Table("auth_session").Create(map[string]any{"id": sessionID, "subject_type": "CREATOR", "subject_id": userID, "client_type": "CREATOR_WEB", "refresh_token_hash": "test", "token_version": 1, "status": "ACTIVE", "expires_at": time.Now().Add(time.Hour)}).Error)
	snapshot := map[string]any{
		"snapshotVersion": 1,
		"platform":        map[string]any{"orgName": "星衍树测试平台", "orgCode": "PLATFORM"},
		"buyer":           map[string]any{"creatorId": fmt.Sprint(creatorID), "displayName": "AUT03 创作工作室", "legalName": "测试文化有限公司", "subjectType": "ENTERPRISE", "credentialType": "UNIFIED_SOCIAL_CREDIT_CODE", "credentialNoMasked": "**************1234", "verificationNo": "IDV-AUT03"},
		"ip":              map[string]any{"ipId": fmt.Sprint(ipID), "ipNo": "IP-AUT03", "ipName": "AUT03 测试 IP", "cityOrgName": "测试城市", "rightHolderNameMasked": "测***", "acquisitionMethod": "ORIGINAL", "rightsNotice": "测试权利说明", "purchaseNotice": "测试购买须知"},
		"authorization":   map[string]any{"authorizationMode": "NORMAL_SHARE", "planVersionId": fmt.Sprint(planVersionID), "planName": "AUT03 测试授权方案", "termType": "MONTH", "termValue": 12, "validFrom": "", "validTo": "", "regionMode": "NATIONWIDE", "regionCodes": []string{}, "scenes": []string{"PRODUCT_SALE"}, "commercialAllowed": true, "derivativeAllowed": true, "nCreationAllowed": false, "contentType": "PRODUCT_ONLY", "quotaTotal": 10},
		"distribution":    map[string]any{"planId": "1", "versionNo": 1, "name": "测试分账", "type": "NORMAL_SHARE", "items": []any{}},
		"materialPack":    map[string]any{"manifestHash": materialManifest},
		"order":           map[string]any{"authOrderNo": fmt.Sprintf("AUT03-%d", orderID), "amount": "199.00", "currency": "CNY", "contractAgreed": true},
	}
	snapshotJSON, _ := json.Marshal(snapshot)
	mustPayment(t, db.Table("auth_order").Create(map[string]any{"id": orderID, "status": "PENDING_PAYMENT", "version": 1, "auth_order_no": fmt.Sprintf("AUT03-%d", orderID), "creator_id": creatorID, "ip_id": ipID, "auth_plan_version_id": planVersionID, "amount": "199.00", "currency": "CNY", "payment_expire_at": time.Now().Add(30 * time.Minute), "contract_agreed": true, "purchase_snapshot": snapshotJSON, "contract_status": "NOT_GENERATED", "evidence_status": "NOT_SUBMITTED"}).Error)
	mustPayment(t, db.Table("auth_quota_reservation").Create(map[string]any{"id": idgen.Next(), "reservation_no": fmt.Sprintf("AQR-%d", idgen.Next()), "auth_order_id": orderID, "creator_id": creatorID, "ip_id": ipID, "auth_plan_version_id": planVersionID, "quantity": 1, "reservation_status": "RESERVED", "expires_at": time.Now().Add(30 * time.Minute)}).Error)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": fmt.Sprint(userID), "sid": fmt.Sprint(sessionID), "tv": "1", "clientType": "CREATOR_WEB", "exp": time.Now().Add(time.Hour).Unix()})
	tokenText, err := token.SignedString([]byte(cfg.Auth.JWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(httpx.RequestIDMiddleware())
	api := router.Group("/api/v1")
	storage := &contractTestStorage{objects: map[string][]byte{}}
	fileService := files.NewService(db, storage, config.NewFilePolicyProvider(cfg.File.Policy), cfg.File)
	materialKey := fmt.Sprintf("test/material/%d.png", materialFileID)
	storage.objects[materialKey] = []byte("material")
	mustPayment(t, db.Table("file_object").Create(map[string]any{"id": materialFileID, "status": "ACTIVE", "version": 1, "created_by": userID, "file_no": fmt.Sprintf("MAT-%d", materialFileID), "storage_key": materialKey, "original_name": "授权素材.png", "mime_type": "image/png", "file_size": 8, "file_hash": strings.Repeat("b", 64), "security_level": "PRIVATE", "upload_status": "SUCCESS"}).Error)
	mustPayment(t, db.Table("ip_material_pack").Create(map[string]any{"id": materialPackID, "status": "FROZEN", "version": 1, "created_by": userID, "ip_id": ipID, "version_no": 1, "pack_name": "AUT04 冻结素材包", "manifest_hash": materialManifest, "submission_locked_at": time.Now(), "evidence_status": "EVIDENCED", "evidence_receipt_no": "MAT-RCPT", "evidence_transaction_hash": "MAT-TX", "frozen_at": time.Now(), "enabled": true}).Error)
	mustPayment(t, db.Table("ip_material_pack_item").Create(map[string]any{"id": materialItemID, "material_pack_id": materialPackID, "file_id": materialFileID, "file_hash": strings.Repeat("b", 64), "material_role": "SOURCE", "sort_no": 1, "deliverable": true, "created_by": userID}).Error)
	authorizationService := authorizations.NewService(db, fileService)
	RegisterMockRoute(api, NewService(db), creators.RequireAuthentication(db, cfg.Auth), "test")
	authorizations.RegisterRoutes(api, authorizationService, creators.RequireAuthentication(db, cfg.Auth))
	server := httptest.NewServer(router)
	defer server.Close()
	body := fmt.Sprintf(`{"bizType":"AUTH_ORDER","bizOrderId":"%d"}`, orderID)
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/payments/mock/pay", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+tokenText)
	request.Header.Set("Idempotency-Key", fmt.Sprintf("aut03-payment-%d", orderID))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !bytes.Contains(responseBody, []byte(`"paymentStatus":"SUCCESS"`)) {
		t.Fatalf("payment HTTP status=%d body=%s", response.StatusCode, responseBody)
	}

	request2, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/payments/mock/pay", strings.NewReader(body))
	request2.Header = request.Header.Clone()
	response2, err := http.DefaultClient.Do(request2)
	if err != nil {
		t.Fatal(err)
	}
	defer response2.Body.Close()
	if response2.StatusCode != http.StatusOK || response2.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replayed payment status=%d replay=%s", response2.StatusCode, response2.Header.Get("Idempotency-Replayed"))
	}
	var paymentCount, authorizationCount, taskCount int64
	mustPayment(t, db.Table("payment_transaction").Where("biz_type='AUTH_ORDER' AND biz_order_id=?", orderID).Count(&paymentCount).Error)
	mustPayment(t, db.Table("creator_authorization").Where("auth_order_id=?", orderID).Count(&authorizationCount).Error)
	mustPayment(t, db.Table("auth_contract_generation_task").Where("auth_order_id=?", orderID).Count(&taskCount).Error)
	if paymentCount != 1 || authorizationCount != 1 || taskCount != 1 {
		t.Fatalf("idempotency counts payment=%d authorization=%d task=%d", paymentCount, authorizationCount, taskCount)
	}

	processor := NewContractProcessor(db, fileService, evidence.NewService(db, evidence.MockProvider{}, "test-secret"))
	processed, err := processor.ProcessDueContracts(context.Background(), 10)
	if err != nil || processed != 1 {
		t.Fatalf("process contracts count=%d err=%v", processed, err)
	}
	var authorizationID uint64
	mustPayment(t, db.Table("creator_authorization").Select("id").Where("auth_order_id=?", orderID).Take(&authorizationID).Error)
	detail, code, err := authorizationService.Get(context.Background(), creators.CurrentCreator{UserID: userID, CreatorID: creatorID}, authorizationID)
	if err != nil || code != "" {
		t.Fatalf("authorization detail code=%s err=%v", code, err)
	}
	evidenceData := detail["evidence"].(map[string]any)
	if evidenceData["status"] != "EVIDENCED" || evidenceData["transactionHash"] == "" {
		t.Fatalf("authorization evidence=%#v", evidenceData)
	}
	download, code, err := authorizationService.ContractDownload(context.Background(), creators.CurrentCreator{UserID: userID, CreatorID: creatorID}, authorizationID, "127.0.0.1")
	if err != nil || code != "" || !strings.HasPrefix(fmt.Sprint(download["downloadUrl"]), "https://download.test/") {
		t.Fatalf("contract download=%#v code=%s err=%v", download, code, err)
	}
	packsRequest, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/creator/authorizations/%d/material-packs", server.URL, authorizationID), nil)
	packsRequest.Header.Set("Authorization", "Bearer "+tokenText)
	packsResponse, err := http.DefaultClient.Do(packsRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer packsResponse.Body.Close()
	packsBody, _ := io.ReadAll(packsResponse.Body)
	if packsResponse.StatusCode != http.StatusOK || !bytes.Contains(packsBody, []byte(fmt.Sprint(materialItemID))) || !bytes.Contains(packsBody, []byte(materialManifest)) {
		t.Fatalf("material packs status=%d body=%s", packsResponse.StatusCode, packsBody)
	}
	downloadBody := fmt.Sprintf(`{"materialItemId":"%d"}`, materialItemID)
	materialRequest, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/creator/authorizations/%d/material-downloads", server.URL, authorizationID), strings.NewReader(downloadBody))
	materialRequest.Header.Set("Authorization", "Bearer "+tokenText)
	materialRequest.Header.Set("Content-Type", "application/json")
	materialResponse, err := http.DefaultClient.Do(materialRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer materialResponse.Body.Close()
	materialResponseBody, _ := io.ReadAll(materialResponse.Body)
	if materialResponse.StatusCode != http.StatusOK || !bytes.Contains(materialResponseBody, []byte(`"downloadUrl"`)) {
		t.Fatalf("material download status=%d body=%s", materialResponse.StatusCode, materialResponseBody)
	}
	listRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/creator/authorizations?pageNo=1&pageSize=20", nil)
	listRequest.Header.Set("Authorization", "Bearer "+tokenText)
	listResponse, err := http.DefaultClient.Do(listRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer listResponse.Body.Close()
	listBody, _ := io.ReadAll(listResponse.Body)
	if listResponse.StatusCode != http.StatusOK || !bytes.Contains(listBody, []byte(`"downloadReady":true`)) || !bytes.Contains(listBody, []byte(`"EVIDENCED"`)) {
		t.Fatalf("authorization list HTTP status=%d body=%s", listResponse.StatusCode, listBody)
	}
	contractRequest, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/creator/authorizations/%d/contract", server.URL, authorizationID), nil)
	contractRequest.Header.Set("Authorization", "Bearer "+tokenText)
	contractResponse, err := http.DefaultClient.Do(contractRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer contractResponse.Body.Close()
	contractBody, _ := io.ReadAll(contractResponse.Body)
	if contractResponse.StatusCode != http.StatusOK || !bytes.Contains(contractBody, []byte(`"downloadUrl":"https://download.test/`)) {
		t.Fatalf("contract HTTP status=%d body=%s", contractResponse.StatusCode, contractBody)
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	var pdfCount int
	for _, content := range storage.objects {
		if bytes.HasPrefix(content, []byte("%PDF-1.4")) {
			pdfCount++
		}
	}
	if pdfCount != 2 {
		t.Fatalf("generated PDF count=%d, want contract and certificate", pdfCount)
	}
}

func TestEvidenceFinalFailureDoesNotRollbackPaymentAuthorizationOrContract(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1 to run MySQL integration test")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	orderID, authorizationID, contractID, evidenceID := idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next()
	defer func() {
		db.Exec("DELETE FROM evidence_retry_log WHERE evidence_id=?", evidenceID)
		db.Exec("DELETE FROM evidence_record WHERE id=?", evidenceID)
		db.Exec("DELETE FROM auth_contract WHERE id=?", contractID)
		db.Exec("DELETE FROM creator_authorization WHERE id=?", authorizationID)
		db.Exec("DELETE FROM auth_order WHERE id=?", orderID)
	}()
	now := time.Now()
	mustPayment(t, db.Table("auth_order").Create(map[string]any{"id": orderID, "status": "PAID", "version": 2, "auth_order_no": fmt.Sprintf("AUT03-FAIL-%d", orderID), "creator_id": idgen.Next(), "ip_id": idgen.Next(), "auth_plan_version_id": idgen.Next(), "amount": "1.00", "currency": "CNY", "payment_expire_at": now, "contract_agreed": true, "purchase_snapshot": []byte(`{"order":{"amount":"1.00","currency":"CNY"}}`), "payment_no": fmt.Sprintf("PAY-%d", orderID), "paid_at": now, "contract_status": "GENERATED", "evidence_status": "EVIDENCE_RETRYING"}).Error)
	mustPayment(t, db.Table("creator_authorization").Create(map[string]any{"id": authorizationID, "status": "ACTIVE", "version": 1, "authorization_no": fmt.Sprintf("AUTHZ-%d", authorizationID), "creator_id": idgen.Next(), "ip_id": idgen.Next(), "auth_order_id": orderID, "authorization_mode": "NORMAL_SHARE", "start_at": now, "end_at": now.AddDate(1, 0, 0), "active_flag": true}).Error)
	mustPayment(t, db.Table("evidence_record").Create(map[string]any{"id": evidenceID, "status": "FAILED", "version": 1, "evidence_no": fmt.Sprintf("EVD-%d", evidenceID), "object_type": "AUTH_CONTRACT", "object_id": contractID, "evidence_type": "AUTHORIZATION_CONTRACT", "hash_value": strings.Repeat("a", 64), "chain_provider": "TEST", "request_no": fmt.Sprintf("AUTH-CONTRACT-%d", contractID)}).Error)
	mustPayment(t, db.Table("auth_contract").Create(map[string]any{"id": contractID, "status": "ACTIVE", "version": 1, "contract_no": fmt.Sprintf("CONTRACT-%d", contractID), "authorization_id": authorizationID, "contract_file_id": idgen.Next(), "contract_snapshot": []byte(`{"order":{"amount":"1.00"}}`), "effective_at": now, "file_hash": strings.Repeat("a", 64), "evidence_status": "EVIDENCE_RETRYING", "evidence_id": evidenceID, "evidence_retry_count": 4, "evidence_next_retry_at": now.Add(-time.Minute)}).Error)
	processor := NewContractProcessor(db, nil, evidence.NewService(db, failingEvidenceProvider{}, "test-secret"))
	count, err := processor.RetryDueEvidence(context.Background(), 10)
	if err != nil || count != 1 {
		t.Fatalf("retry evidence count=%d err=%v", count, err)
	}
	var states struct {
		ContractEvidence string `gorm:"column:contract_evidence"`
		OrderStatus      string `gorm:"column:order_status"`
		OrderEvidence    string `gorm:"column:order_evidence"`
		Authorization    string `gorm:"column:authorization_status"`
	}
	mustPayment(t, db.Table("auth_contract c").Select("c.evidence_status AS contract_evidence,o.status AS order_status,o.evidence_status AS order_evidence,a.status AS authorization_status").Joins("JOIN creator_authorization a ON a.id=c.authorization_id").Joins("JOIN auth_order o ON o.id=a.auth_order_id").Where("c.id=?", contractID).Take(&states).Error)
	if states.ContractEvidence != "EVIDENCE_FAILED" || states.OrderEvidence != "EVIDENCE_FAILED" || states.OrderStatus != "PAID" || states.Authorization != "ACTIVE" {
		t.Fatalf("unexpected states: %#v", states)
	}
}

func TestContractGenerationFailureDoesNotRollbackPaymentOrAuthorization(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1 to run MySQL integration test")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	creatorUserID, orderID, authorizationID, taskID := idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next()
	defer func() {
		cleanupGeneratedFiles(db, creatorUserID)
		db.Exec("DELETE FROM auth_contract_generation_task WHERE id=?", taskID)
		db.Exec("DELETE FROM creator_authorization WHERE id=?", authorizationID)
		db.Exec("DELETE FROM auth_order WHERE id=?", orderID)
	}()
	now := time.Now()
	snapshot := []byte(`{"order":{"amount":"1.00","currency":"CNY"},"buyer":{"legalName":"测试主体"},"ip":{"ipName":"测试IP"},"authorization":{"authorizationMode":"NORMAL_SHARE","termType":"MONTH","termValue":12}}`)
	mustPayment(t, db.Table("auth_order").Create(map[string]any{"id": orderID, "status": "PAID", "version": 2, "auth_order_no": fmt.Sprintf("AUT03-FILE-FAIL-%d", orderID), "creator_id": idgen.Next(), "ip_id": idgen.Next(), "auth_plan_version_id": idgen.Next(), "amount": "1.00", "currency": "CNY", "payment_expire_at": now, "contract_agreed": true, "purchase_snapshot": snapshot, "payment_no": fmt.Sprintf("PAY-%d", orderID), "paid_at": now, "contract_status": "PENDING_GENERATION", "evidence_status": "PENDING_EVIDENCE"}).Error)
	mustPayment(t, db.Table("creator_authorization").Create(map[string]any{"id": authorizationID, "status": "ACTIVE", "version": 1, "authorization_no": fmt.Sprintf("AUTHZ-%d", authorizationID), "creator_id": idgen.Next(), "ip_id": idgen.Next(), "auth_order_id": orderID, "authorization_mode": "NORMAL_SHARE", "start_at": now, "end_at": now.AddDate(1, 0, 0), "active_flag": true}).Error)
	mustPayment(t, db.Table("auth_contract_generation_task").Create(map[string]any{"id": taskID, "status": "PENDING", "version": 1, "authorization_id": authorizationID, "auth_order_id": orderID, "creator_user_id": creatorUserID, "next_run_at": now}).Error)
	fileService := files.NewService(db, failingContractStorage{}, config.NewFilePolicyProvider(cfg.File.Policy), cfg.File)
	processor := NewContractProcessor(db, fileService, evidence.NewService(db, evidence.MockProvider{}, "test-secret"))
	count, err := processor.ProcessDueContracts(context.Background(), 10)
	if err != nil || count != 1 {
		t.Fatalf("process failing contract count=%d err=%v", count, err)
	}
	var state struct {
		OrderStatus    string `gorm:"column:order_status"`
		ContractStatus string `gorm:"column:contract_status"`
		Authorization  string `gorm:"column:authorization_status"`
		TaskStatus     string `gorm:"column:task_status"`
		TaskRetryCount int    `gorm:"column:retry_count"`
	}
	mustPayment(t, db.Table("auth_order o").Select("o.status AS order_status,o.contract_status,a.status AS authorization_status,t.status AS task_status,t.retry_count").Joins("JOIN creator_authorization a ON a.auth_order_id=o.id").Joins("JOIN auth_contract_generation_task t ON t.auth_order_id=o.id").Where("o.id=?", orderID).Take(&state).Error)
	if state.OrderStatus != "PAID" || state.Authorization != "ACTIVE" || state.ContractStatus != "PENDING_GENERATION" || state.TaskStatus != "PENDING" || state.TaskRetryCount != 1 {
		t.Fatalf("unexpected failure states: %#v", state)
	}
}

func mustPayment(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func cleanupPaymentFixture(db *gorm.DB, userID, creatorID, orderID, ipID uint64) {
	var authorizationIDs, contractIDs, evidenceIDs, fileIDs []uint64
	db.Table("creator_authorization").Where("auth_order_id=?", orderID).Pluck("id", &authorizationIDs)
	if len(authorizationIDs) > 0 {
		db.Table("auth_contract").Where("authorization_id IN ?", authorizationIDs).Pluck("id", &contractIDs)
		db.Table("auth_contract").Where("authorization_id IN ?", authorizationIDs).Pluck("contract_file_id", &fileIDs)
		var certificateFiles []uint64
		db.Table("auth_certificate").Where("authorization_id IN ?", authorizationIDs).Pluck("file_id", &certificateFiles)
		fileIDs = append(fileIDs, certificateFiles...)
	}
	if len(contractIDs) > 0 {
		db.Table("evidence_record").Where("object_type='AUTH_CONTRACT' AND object_id IN ?", contractIDs).Pluck("id", &evidenceIDs)
	}
	if len(evidenceIDs) > 0 {
		db.Exec("DELETE FROM evidence_retry_log WHERE evidence_id IN ?", evidenceIDs)
		db.Exec("DELETE FROM biz_status_history WHERE object_type='EVIDENCE' AND object_id IN ?", evidenceIDs)
		db.Exec("DELETE FROM evidence_record WHERE id IN ?", evidenceIDs)
	}
	if len(fileIDs) > 0 {
		db.Exec("DELETE FROM file_relation WHERE file_id IN ?", fileIDs)
		db.Exec("DELETE FROM biz_status_history WHERE object_type='FILE' AND object_id IN ?", fileIDs)
		db.Exec("DELETE FROM sys_audit_log WHERE object_type='FILE' AND object_id IN ?", fileIDs)
		db.Exec("DELETE FROM file_object WHERE id IN ?", fileIDs)
	}
	db.Exec("DELETE FROM biz_status_history WHERE object_type='AUTH_ORDER' AND object_id=?", orderID)
	db.Exec("DELETE FROM sys_audit_log WHERE object_type='AUTH_ORDER' AND object_id=?", orderID)
	if len(authorizationIDs) > 0 {
		db.Exec("DELETE FROM biz_status_history WHERE object_type='CREATOR_AUTHORIZATION' AND object_id IN ?", authorizationIDs)
		db.Exec("DELETE FROM sys_audit_log WHERE object_type='CREATOR_AUTHORIZATION' AND object_id IN ?", authorizationIDs)
	}
	if len(contractIDs) > 0 {
		db.Exec("DELETE FROM biz_status_history WHERE object_type='AUTH_CONTRACT' AND object_id IN ?", contractIDs)
		db.Exec("DELETE FROM sys_audit_log WHERE object_type='AUTH_CONTRACT' AND object_id IN ?", contractIDs)
	}
	db.Exec("DELETE FROM auth_certificate WHERE authorization_id IN ?", authorizationIDs)
	db.Exec("DELETE FROM auth_contract WHERE authorization_id IN ?", authorizationIDs)
	db.Exec("DELETE FROM auth_contract_generation_task WHERE auth_order_id=?", orderID)
	db.Exec("DELETE FROM auth_scope_snapshot WHERE authorization_id IN ?", authorizationIDs)
	db.Exec("DELETE FROM creator_authorization WHERE auth_order_id=?", orderID)
	db.Exec("DELETE FROM payment_transaction WHERE biz_type='AUTH_ORDER' AND biz_order_id=?", orderID)
	db.Exec("DELETE FROM auth_quota_reservation WHERE auth_order_id=?", orderID)
	db.Exec("DELETE FROM api_idempotency_record WHERE subject_type='CREATOR' AND subject_id=?", creatorID)
	db.Exec("DELETE FROM auth_order WHERE id=?", orderID)
	var materialPackIDs, materialFileIDs []uint64
	db.Table("ip_material_pack").Where("ip_id=?", ipID).Pluck("id", &materialPackIDs)
	if len(materialPackIDs) > 0 {
		db.Table("ip_material_pack_item").Where("material_pack_id IN ?", materialPackIDs).Pluck("file_id", &materialFileIDs)
		db.Exec("DELETE FROM ip_material_pack_item WHERE material_pack_id IN ?", materialPackIDs)
		db.Exec("DELETE FROM ip_material_pack WHERE id IN ?", materialPackIDs)
	}
	if len(materialFileIDs) > 0 {
		db.Exec("DELETE FROM file_relation WHERE file_id IN ?", materialFileIDs)
		db.Exec("DELETE FROM file_object WHERE id IN ?", materialFileIDs)
	}
	db.Exec("DELETE FROM auth_session WHERE id IN (SELECT id FROM (SELECT id FROM auth_session WHERE subject_id=?) x)", userID)
	db.Exec("DELETE FROM creator_profile WHERE id=?", creatorID)
	db.Exec("DELETE FROM sys_user WHERE id=?", userID)
	_ = ipID
}

func cleanupGeneratedFiles(db *gorm.DB, creatorUserID uint64) {
	var fileIDs []uint64
	db.Table("file_object").Where("created_by=?", creatorUserID).Pluck("id", &fileIDs)
	if len(fileIDs) == 0 {
		return
	}
	db.Exec("DELETE FROM file_relation WHERE file_id IN ?", fileIDs)
	db.Exec("DELETE FROM biz_status_history WHERE object_type='FILE' AND object_id IN ?", fileIDs)
	db.Exec("DELETE FROM sys_audit_log WHERE object_type='FILE' AND object_id IN ?", fileIDs)
	db.Exec("DELETE FROM file_object WHERE id IN ?", fileIDs)
}
