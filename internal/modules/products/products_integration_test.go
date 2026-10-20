package products

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/filestore"
	"xingyanshu-backend/internal/platform/idgen"
)

type productTestStorage struct{}

func (productTestStorage) Put(context.Context, string, string, io.Reader) error { return nil }
func (productTestStorage) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("not implemented")
}
func (productTestStorage) SignDownloadURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://example.invalid/" + key, nil
}

var _ filestore.Storage = productTestStorage{}

func TestProductTwoStageReviewWithMySQL(t *testing.T) {
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
	u, creatorID, ipID, orderID, authID, workID, templateID := idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next()
	files := []uint64{idgen.Next(), idgen.Next(), idgen.Next()}
	defer cleanupProductFixture(db, u, creatorID, ipID, orderID, authID, workID, templateID, files)
	now := time.Now()
	mustProduct(t, db.Table("sys_user").Create(map[string]any{"id": u, "status": "ACTIVE", "version": 1, "username": fmt.Sprintf("cnt02_%d", u), "password_hash": "unused", "display_name": "CNT02测试创作者"}).Error)
	mustProduct(t, db.Table("creator_profile").Create(map[string]any{"id": creatorID, "user_id": u, "status": "ACTIVE", "version": 1, "subject_type": "PERSONAL", "display_name": "CNT02测试创作者", "identity_status": "VERIFIED", "letter_status": "OPTIONAL"}).Error)
	mustProduct(t, db.Table("ip_asset").Create(map[string]any{"id": ipID, "status": "LISTED", "version": 1, "ip_no": fmt.Sprintf("IP-202609-%05d", ipID%100000), "ip_name": "CNT02测试IP", "ip_type": "CULTURAL", "source_type": "CITY_COLLECTION", "main_city_org_id": 1, "short_intro": "测试", "detail_content": "测试", "culture_story": "测试", "ip_feature": "测试", "creative_directions_json": "[]", "applicable_product_types_json": "[]", "applicable_activity_types_json": "[]", "public_copyright_notice": "测试", "purchase_notice": "测试", "authorization_mode": "NORMAL_SHARE", "mall_visible": true, "suggested_transaction_price_amount": "1.00"}).Error)
	mustProduct(t, db.Table("auth_order").Create(map[string]any{"id": orderID, "status": "PAID", "version": 1, "auth_order_no": fmt.Sprintf("CNT02-ORDER-%d", orderID), "creator_id": creatorID, "ip_id": ipID, "auth_plan_version_id": 1, "amount": "1.00", "currency": "CNY", "payment_expire_at": now.Add(time.Hour), "contract_agreed": true, "purchase_snapshot": `{}`, "contract_status": "GENERATED", "evidence_status": "EVIDENCED", "commercial_allowed_snapshot": true, "derivative_allowed_snapshot": true}).Error)
	mustProduct(t, db.Table("creator_authorization").Create(map[string]any{"id": authID, "status": "ACTIVE", "version": 1, "authorization_no": fmt.Sprintf("CNT02-AUTH-%d", authID), "creator_id": creatorID, "ip_id": ipID, "auth_order_id": orderID, "authorization_mode": "NORMAL_SHARE", "start_at": now.Add(-time.Hour), "end_at": now.Add(time.Hour), "active_flag": true}).Error)
	mustProduct(t, db.Table("derivative_work").Create(map[string]any{"id": workID, "status": "RIGHTS_CONFIRMED", "version": 1, "work_no": fmt.Sprintf("WORK-202609-%05d", workID%100000), "creator_id": creatorID, "ip_id": ipID, "authorization_id": authID}).Error)
	mustProduct(t, db.Table("after_sale_rule_template").Create(map[string]any{"id": templateID, "status": "ACTIVE", "version": 1, "template_no": fmt.Sprintf("CNT02-TPL-%d", templateID), "template_name": "商品售后", "biz_type": "PRODUCT", "conditions_json": `{}`, "refund_rule_json": `{}`, "enabled": true}).Error)
	for _, fileID := range files {
		mustProduct(t, db.Table("file_object").Create(map[string]any{"id": fileID, "status": "ACTIVE", "version": 1, "created_by": u, "file_no": fmt.Sprintf("CNT02-FILE-%d", fileID), "storage_key": fmt.Sprintf("cnt02/%d", fileID), "original_name": "product.png", "mime_type": "image/png", "file_size": 8, "file_hash": fmt.Sprintf("%064d", fileID), "security_level": "PRIVATE", "upload_status": "SUCCESS"}).Error)
	}
	svc := NewService(db, contentmoderation.NewProvider(config.ContentSafetyConfig{Provider: "mock"}), productTestStorage{})
	creator := creators.CurrentCreator{UserID: u, CreatorID: creatorID}
	in := Input{AuthorizationID: authID, WorkID: workID, ProductName: "CNT02 商品", ProductIntro: "二创衍生商品", ProductType: "CULTURAL_CREATIVE", CoverFileID: files[0], DetailFileIDs: []uint64{files[1]}, Attachments: []Attachment{{FileID: files[2], FileType: "QUALITY_REPORT"}}, PublicCopyrightNotice: "已获得授权并关联确权作品", SaleRegionMode: "NATIONAL", FulfillmentMode: "CREATOR_SHIP", FreightMode: "FREE", FreightAmount: "0", AfterSaleTemplateID: templateID, SKUs: []SKU{{SKUName: "标准款", SalePrice: "88.00", StockTotal: 0}}}
	created, code, err := svc.Create(context.Background(), creator, in)
	if err != nil || code != "" || created["status"] != "PENDING_COPYRIGHT_REVIEW" {
		t.Fatalf("create=%#v code=%s err=%v", created, code, err)
	}
	pid := parseProductID(t, created["productId"])
	vid := parseProductID(t, created["productVersionId"])
	var sku struct {
		Code string `gorm:"column:sku_code"`
	}
	mustProduct(t, db.Table("product_sku").Select("sku_code").Where("product_version_id=?", vid).Take(&sku).Error)
	if sku.Code == "" || !containsAll(sku.Code, "SKU-", "IP-", "WORK-", "-01") {
		t.Fatalf("unexpected generated sku=%s", sku.Code)
	}
	reviewer := identity.CurrentAdmin{UserID: u, PermissionCodes: map[string]struct{}{reviewPermission: {}}}
	if _, code, err = svc.Review(context.Background(), reviewer, vid, ReviewInput{ReviewNode: "COPYRIGHT_INITIAL", Result: "PASS"}); err != nil || code != "" {
		t.Fatalf("initial code=%s err=%v", code, err)
	}
	if _, code, err = svc.Review(context.Background(), reviewer, vid, ReviewInput{ReviewNode: "COMPLIANCE_REVIEW", Result: "PASS"}); err != nil || code != "" {
		t.Fatalf("compliance code=%s err=%v", code, err)
	}
	if _, code, err = svc.Delist(context.Background(), creatorID, pid); err != nil || code != "" {
		t.Fatalf("delist code=%s err=%v", code, err)
	}
	updated, code, err := svc.Update(context.Background(), creator, pid, in)
	if err != nil || code != "" || updated["status"] != "PENDING_COPYRIGHT_REVIEW" {
		t.Fatalf("update=%#v code=%s err=%v", updated, code, err)
	}
	if updated["productVersionId"] == created["productVersionId"] {
		t.Fatalf("delisted product must create a new version")
	}
	var published int64
	mustProduct(t, db.Table("content_publish_record").Where("object_type='PRODUCT' AND object_id=?", pid).Count(&published).Error)
	if published != 1 {
		t.Fatalf("publish record count=%d", published)
	}
}

func parseProductID(t *testing.T, value any) uint64 {
	t.Helper()
	var id uint64
	if _, err := fmt.Sscan(fmt.Sprint(value), &id); err != nil || id == 0 {
		t.Fatalf("invalid id %#v", value)
	}
	return id
}
func containsAll(s string, values ...string) bool {
	for _, v := range values {
		if !strings.Contains(s, v) {
			return false
		}
	}
	return true
}
func mustProduct(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func cleanupProductFixture(db *gorm.DB, userID, creatorID, ipID, orderID, authID, workID, templateID uint64, fileIDs []uint64) {
	db.Exec("DELETE FROM content_publish_record WHERE object_type='PRODUCT' AND object_id IN (SELECT id FROM product WHERE creator_id=?)", creatorID)
	db.Exec("DELETE FROM content_detection_record WHERE object_type='PRODUCT' AND object_id IN (SELECT id FROM product WHERE creator_id=?)", creatorID)
	db.Exec("DELETE FROM product_review_record WHERE product_version_id IN (SELECT id FROM product_version WHERE product_id IN (SELECT id FROM product WHERE creator_id=?))", creatorID)
	db.Exec("DELETE FROM product_sku WHERE product_version_id IN (SELECT id FROM product_version WHERE product_id IN (SELECT id FROM product WHERE creator_id=?))", creatorID)
	db.Exec("DELETE FROM product_version WHERE product_id IN (SELECT id FROM product WHERE creator_id=?)", creatorID)
	db.Exec("DELETE FROM product WHERE creator_id=?", creatorID)
	db.Exec("DELETE FROM derivative_work WHERE id=?", workID)
	db.Exec("DELETE FROM creator_authorization WHERE id=?", authID)
	db.Exec("DELETE FROM auth_order WHERE id=?", orderID)
	db.Exec("DELETE FROM after_sale_rule_template WHERE id=?", templateID)
	db.Exec("DELETE FROM file_object WHERE id IN ?", fileIDs)
	db.Exec("DELETE FROM ip_asset WHERE id=?", ipID)
	db.Exec("DELETE FROM creator_profile WHERE id=?", creatorID)
	db.Exec("DELETE FROM sys_user WHERE id=?", userID)
}
