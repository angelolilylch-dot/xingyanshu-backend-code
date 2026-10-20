package authorders

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/platform/idgen"
)

func TestAuthOrderWithMySQL(t *testing.T) {
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
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	suffix := time.Now().UnixNano()
	cityID, creatorID, creatorUserID := idgen.Next(), idgen.Next(), idgen.Next()
	planID, planVersionID, ipID, bindingID, distributionID := idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next()
	defer cleanupFixture(db, cityID, creatorID, planID, planVersionID, ipID, bindingID, distributionID)
	must(t, db.Table("sys_org").Create(map[string]any{"id": cityID, "status": "ACTIVE", "version": 1, "org_code": fmt.Sprintf("ORDER_CITY_%d", suffix), "org_name": "授权订单测试城市", "org_type": "CITY", "city_code": "350200"}).Error)
	must(t, db.Table("creator_profile").Create(map[string]any{"id": creatorID, "user_id": creatorUserID, "status": "ACTIVE", "version": 1, "subject_type": "ENTERPRISE", "display_name": "授权订单测试创作者", "identity_status": "VERIFIED", "letter_status": "OPTIONAL"}).Error)
	must(t, db.Table("creator_identity_verification").Create(map[string]any{"id": idgen.Next(), "status": "ACTIVE", "version": 1, "creator_id": creatorID, "subject_type": "ENTERPRISE", "legal_name": "授权订单测试有限公司", "verification_no": fmt.Sprintf("IDV-%d", suffix), "credential_type": "UNIFIED_SOCIAL_CREDIT_CODE", "credential_cipher": []byte("91350000AUT0312345X"), "credential_hash": fmt.Sprintf("%064d", suffix%1000000), "provider_code": "MOCK", "result_status": "VERIFIED"}).Error)
	must(t, db.Table("auth_plan").Create(map[string]any{"id": planID, "status": "ENABLED", "version": 1, "plan_no": fmt.Sprintf("ORDER_PLAN_%d", suffix), "plan_name": "授权订单测试方案", "current_version_id": planVersionID}).Error)
	must(t, db.Table("auth_plan_version").Create(map[string]any{"id": planVersionID, "status": "ENABLED", "version": 1, "plan_id": planID, "version_no": 1, "authorization_mode": "NORMAL_SHARE", "term_type": "MONTH", "term_value": 12, "region_mode": "NATIONWIDE", "scenes_json": "[\"PRODUCT_SALE\"]", "commercial_allowed": true, "derivative_allowed": true, "n_creation_allowed": false, "content_type": "PRODUCT_ONLY", "quota_total": 1}).Error)
	must(t, db.Table("finance_distribution_plan").Create(map[string]any{"id": distributionID, "status": "ENABLED", "version": 1, "distribution_no": fmt.Sprintf("ORDER_DIST_%d", suffix), "distribution_name": "授权订单测试分账", "distribution_type": "NORMAL_SHARE", "current_version_no": 1}).Error)
	must(t, db.Table("finance_distribution_plan_item").Create(map[string]any{"id": idgen.Next(), "distribution_plan_id": distributionID, "version_no": 1, "participant_type": "CREATOR", "ratio": "1.000000", "sort_no": 1}).Error)
	must(t, db.Table("ip_asset").Create(map[string]any{"id": ipID, "status": "LISTED", "version": 1, "ip_no": fmt.Sprintf("ORDER-IP-%d", suffix), "ip_name": "授权订单测试 IP", "ip_type": "CULTURAL", "source_type": "CITY_COLLECTION", "main_city_org_id": cityID, "short_intro": "订单测试", "detail_content": "授权订单测试详情", "culture_story": "测试文化故事", "ip_feature": "测试特征", "creative_directions_json": "[]", "applicable_product_types_json": "[]", "applicable_activity_types_json": "[]", "public_copyright_notice": "测试版权说明", "purchase_notice": "测试购买须知", "authorization_mode": "NORMAL_SHARE", "mall_visible": true, "suggested_transaction_price_amount": "199.00"}).Error)
	must(t, db.Table("ip_right_holder").Create(map[string]any{"id": idgen.Next(), "status": "ACTIVE", "version": 1, "ip_id": ipID, "holder_name_cipher": []byte("测试权利人"), "holder_name_masked": "测***", "holder_type": "ENTERPRISE", "acquisition_method": "ORIGINAL", "rights_description": "完整权利说明"}).Error)
	must(t, db.Table("ip_auth_binding").Create(map[string]any{"id": bindingID, "status": "ACTIVE", "version": 1, "ip_id": ipID, "auth_plan_version_id": planVersionID, "distribution_plan_id": distributionID, "distribution_version_no": 1, "confirmed_by": 1, "confirmed_at": time.Now()}).Error)
	materialFileID, materialPackID := idgen.Next(), idgen.Next()
	must(t, db.Table("file_object").Create(map[string]any{"id":materialFileID,"status":"ACTIVE","version":1,"created_by":1,"file_no":fmt.Sprintf("MAT-%d",suffix),"storage_key":fmt.Sprintf("test/material/%d",materialFileID),"original_name":"授权素材.png","mime_type":"image/png","file_size":12,"file_hash":fmt.Sprintf("%064d",suffix%1000000),"security_level":"PRIVATE","upload_status":"SUCCESS"}).Error)
	must(t, db.Table("ip_material_pack").Create(map[string]any{"id":materialPackID,"status":"FROZEN","version":1,"ip_id":ipID,"version_no":1,"pack_name":"冻结素材包","manifest_hash":fmt.Sprintf("%064d",suffix%1000000),"evidence_status":"EVIDENCED","enabled":true,"submission_locked_at":time.Now(),"frozen_at":time.Now()}).Error)
	must(t, db.Table("ip_material_pack_item").Create(map[string]any{"id":idgen.Next(),"material_pack_id":materialPackID,"file_id":materialFileID,"file_hash":fmt.Sprintf("%064d",suffix%1000000),"material_role":"SOURCE","sort_no":1,"deliverable":true}).Error)
	defer db.Exec("DELETE FROM file_object WHERE id=?",materialFileID)

	service := NewService(db)
	current := creators.CurrentCreator{UserID: creatorUserID, CreatorID: creatorID}
	created, code, err := service.Create(context.Background(), current, CreateInput{IPID: fmt.Sprint(ipID), ContractAgreed: true}, "127.0.0.1")
	if err != nil || code != "" || created["status"] != statusPending {
		t.Fatalf("create auth order: data=%#v code=%s err=%v", created, code, err)
	}
	orderID := mustID(t, created["id"])
	snapshot := created["purchaseSnapshot"].(map[string]any)
	buyer := snapshot["buyer"].(map[string]any)
	authorization := snapshot["authorization"].(map[string]any)
	distribution := snapshot["distribution"].(map[string]any)
	if buyer["legalName"] != "授权订单测试有限公司" || buyer["credentialNoMasked"] != "***************345X" || authorization["commercialAllowed"] != true || distribution["name"] != "授权订单测试分账" {
		t.Fatalf("incomplete purchase snapshot: %#v", snapshot)
	}
	var persisted struct {
		BuyerLegalName string `gorm:"column:buyer_legal_name"`
		IPName         string `gorm:"column:ip_name_snapshot"`
		PlanName       string `gorm:"column:plan_name_snapshot"`
		ContractStatus string `gorm:"column:contract_status"`
	}
	must(t, db.Table("auth_order").Select("buyer_legal_name,ip_name_snapshot,plan_name_snapshot,contract_status").Where("id=?", orderID).Take(&persisted).Error)
	if persisted.BuyerLegalName == "" || persisted.IPName == "" || persisted.PlanName == "" || persisted.ContractStatus != "NOT_GENERATED" {
		t.Fatalf("incomplete order columns: %#v", persisted)
	}
	var reservation struct {
		Status string `gorm:"column:reservation_status"`
	}
	must(t, db.Table("auth_quota_reservation").Select("reservation_status").Where("auth_order_id=?", orderID).Take(&reservation).Error)
	if reservation.Status != "RESERVED" {
		t.Fatalf("reservation status=%s, want RESERVED", reservation.Status)
	}
	_, code, err = service.Create(context.Background(), current, CreateInput{IPID: fmt.Sprint(ipID), ContractAgreed: true}, "127.0.0.1")
	if err != nil || code != "AUTH_ORDER_PENDING_EXISTS" {
		t.Fatalf("duplicate pending order code=%s err=%v", code, err)
	}
	cancelled, code, err := service.Cancel(context.Background(), current, orderID, "暂不购买", "127.0.0.1")
	if err != nil || code != "" || cancelled["status"] != statusClosed {
		t.Fatalf("cancel auth order: data=%#v code=%s err=%v", cancelled, code, err)
	}
	must(t, db.Table("auth_quota_reservation").Select("reservation_status").Where("auth_order_id=?", orderID).Take(&reservation).Error)
	if reservation.Status != "RELEASED" {
		t.Fatalf("reservation status=%s, want RELEASED after cancel", reservation.Status)
	}
	created, code, err = service.Create(context.Background(), current, CreateInput{IPID: fmt.Sprint(ipID), ContractAgreed: true}, "127.0.0.1")
	if err != nil || code != "" {
		t.Fatalf("create after release: data=%#v code=%s err=%v", created, code, err)
	}
	secondOrderID := mustID(t, created["id"])
	must(t, db.Table("auth_order").Where("id=?", secondOrderID).Update("payment_expire_at", time.Now().Add(-time.Minute)).Error)
	if _, err := service.CloseExpired(context.Background()); err != nil {
		t.Fatalf("close expired: %v", err)
	}
	var order orderRow
	must(t, db.Table("auth_order").Where("id=?", secondOrderID).Take(&order).Error)
	if order.Status != statusClosed || order.ReasonCode != "PAYMENT_TIMEOUT" {
		t.Fatalf("expired order=%#v, want CLOSED/PAYMENT_TIMEOUT", order)
	}
	must(t, db.Table("auth_quota_reservation").Select("reservation_status").Where("auth_order_id=?", secondOrderID).Take(&reservation).Error)
	if reservation.Status != "RELEASED" {
		t.Fatalf("reservation status=%s, want RELEASED after timeout", reservation.Status)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func mustID(t *testing.T, raw any) uint64 {
	t.Helper()
	id, err := parseID(fmt.Sprint(raw))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func cleanupFixture(db *gorm.DB, cityID, creatorID, planID, planVersionID, ipID, bindingID, distributionID uint64) {
	db.Exec("DELETE h FROM biz_status_history h JOIN auth_order o ON h.object_type='AUTH_ORDER' AND h.object_id=o.id WHERE o.ip_id=?", ipID)
	db.Exec("DELETE l FROM sys_audit_log l JOIN auth_order o ON l.object_type='AUTH_ORDER' AND l.object_id=o.id WHERE o.ip_id=?", ipID)
	db.Exec("DELETE FROM auth_quota_reservation WHERE ip_id=?", ipID)
	db.Exec("DELETE FROM auth_order WHERE ip_id=?", ipID)
	db.Exec("DELETE i FROM ip_material_pack_item i JOIN ip_material_pack p ON p.id=i.material_pack_id WHERE p.ip_id=?", ipID)
	db.Exec("DELETE FROM ip_material_pack WHERE ip_id=?", ipID)
	db.Exec("DELETE FROM ip_auth_binding WHERE id=?", bindingID)
	db.Exec("DELETE FROM ip_right_holder WHERE ip_id=?", ipID)
	db.Exec("DELETE FROM ip_asset WHERE id=?", ipID)
	db.Exec("DELETE FROM finance_distribution_plan_item WHERE distribution_plan_id=?", distributionID)
	db.Exec("DELETE FROM finance_distribution_plan WHERE id=?", distributionID)
	db.Exec("DELETE FROM auth_plan_version WHERE id=?", planVersionID)
	db.Exec("DELETE FROM auth_plan WHERE id=?", planID)
	db.Exec("DELETE FROM creator_identity_verification WHERE creator_id=?", creatorID)
	db.Exec("DELETE FROM creator_profile WHERE id=?", creatorID)
	db.Exec("DELETE FROM sys_org WHERE id=?", cityID)
}
