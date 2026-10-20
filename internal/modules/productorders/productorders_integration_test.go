package productorders_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/modules/productorders"
	"xingyanshu-backend/internal/platform/idgen"
)

// TestProductOrdersHTTPWithMySQL covers the H5-02 boundary against real MySQL.
// It is opt-in so ordinary unit test runs do not require local infrastructure.
func TestProductOrdersHTTPWithMySQL(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_DB_TESTS") != "1" {
		t.Skip("set XINGYANSHU_RUN_DB_TESTS=1")
	}
	cfg, err := config.Load(filepath.Join("..", "..", "..", "config", "test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenMySQLConfig(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	base := idgen.Next()
	consumerID, sessionID := base+1, base+2
	productA, versionA, skuA := base+10, base+11, base+12
	productB, versionB, skuB := base+20, base+21, base+22
	otherConsumer := base + 30
	cleanup := []uint64{productA, productB}
	t.Cleanup(func() {
		db.Exec("DELETE FROM payment_transaction WHERE biz_type='PRODUCT_ORDER' AND biz_order_id IN (SELECT id FROM product_order WHERE consumer_id IN ?)", []uint64{consumerID, otherConsumer})
		db.Exec("DELETE FROM product_stock_reservation WHERE order_item_id IN (SELECT id FROM product_order_item WHERE order_id IN (SELECT id FROM product_order WHERE consumer_id IN ?))", []uint64{consumerID, otherConsumer})
		db.Exec("DELETE FROM product_order_item WHERE order_id IN (SELECT id FROM product_order WHERE consumer_id IN ?)", []uint64{consumerID, otherConsumer})
		db.Exec("DELETE FROM product_order WHERE consumer_id IN ?", []uint64{consumerID, otherConsumer})
		db.Exec("DELETE FROM consumer_address WHERE consumer_id IN ?", []uint64{consumerID, otherConsumer})
		db.Exec("DELETE FROM auth_session WHERE id=?", sessionID)
		db.Exec("DELETE FROM consumer_account WHERE id IN ?", []uint64{consumerID, otherConsumer})
		db.Exec("DELETE FROM product_sku WHERE product_version_id IN ?", []uint64{versionA, versionB})
		db.Exec("DELETE FROM product_version WHERE id IN ?", []uint64{versionA, versionB})
		db.Exec("DELETE FROM product WHERE id IN ?", cleanup)
	})
	must(t, db.Table("consumer_account").Create(map[string]any{"id": consumerID, "status": "ACTIVE", "version": 1, "mobile_cipher": "13900000001", "mobile_hash": fmt.Sprint(consumerID), "mobile_masked": "139****0001"}).Error)
	must(t, db.Table("consumer_account").Create(map[string]any{"id": otherConsumer, "status": "ACTIVE", "version": 1, "mobile_cipher": "13900000002", "mobile_hash": fmt.Sprint(otherConsumer), "mobile_masked": "139****0002"}).Error)
	must(t, db.Table("auth_session").Create(map[string]any{"id": sessionID, "subject_type": "CONSUMER", "subject_id": consumerID, "client_type": "CONSUMER_H5", "refresh_token_hash": "test", "token_version": 1, "status": "ACTIVE", "expires_at": time.Now().Add(time.Hour)}).Error)
	seedProduct(t, db, productA, versionA, skuA, "12.00", "FIXED", "5.00", 10)
	seedProduct(t, db, productB, versionB, skuB, "20.00", "FREE", "0", 10)
	r := gin.New()
	productorders.RegisterRoutes(r.Group("/api/v1"), productorders.NewService(db), identity.RequireConsumerAuthentication(db, cfg.Auth), "test")
	token := consumerToken(t, cfg.Auth.JWTSecret, consumerID, sessionID)
	address := call(t, r, http.MethodPost, "/api/v1/consumer/addresses", map[string]any{"recipientName": "张三", "mobile": "13900000001", "provinceCode": "35", "cityCode": "3501", "districtCode": "350102", "detailAddress": "测试路1号", "postalCode": "350000", "isDefault": true}, token, "address-key-0000001")
	if address.Code != 201 {
		t.Fatalf("create address: %+v", address)
	}
	if address.Data["addressId"] == nil {
		t.Fatalf("address data missing: %+v", address)
	}
	addressID := fmt.Sprint(address.Data["addressId"])
	for i := 0; i < 9; i++ {
		extra := call(t, r, http.MethodPost, "/api/v1/consumer/addresses", map[string]any{"recipientName": fmt.Sprintf("收货人%d", i), "mobile": "13900000001", "provinceCode": "35", "cityCode": "3501", "districtCode": "350102", "detailAddress": fmt.Sprintf("测试路%d号", i), "isDefault": false}, token, fmt.Sprintf("address-extra-%08d", i))
		if extra.Code != 201 {
			t.Fatalf("address %d: %+v", i, extra)
		}
	}
	limit := call(t, r, http.MethodPost, "/api/v1/consumer/addresses", map[string]any{"recipientName": "第十一条", "mobile": "13900000001", "provinceCode": "35", "cityCode": "3501", "districtCode": "350102", "detailAddress": "测试路11号"}, token, "address-limit-0001")
	if limit.Code != http.StatusBadRequest {
		t.Fatalf("address limit: %+v", limit)
	}
	body := map[string]any{"addressId": addressID, "items": []map[string]any{{"skuId": fmt.Sprint(skuA), "quantity": 2}, {"skuId": fmt.Sprint(skuB), "quantity": 1}}}
	preview := call(t, r, http.MethodPost, "/api/v1/consumer/product-orders/preview", body, token, "")
	if preview.Code != 200 || fmt.Sprint(preview.Data["payAmount"]) != "49.00" {
		t.Fatalf("preview: %+v", preview)
	}
	created := call(t, r, http.MethodPost, "/api/v1/consumer/product-orders", body, token, "order-create-00001")
	if created.Code != 201 {
		t.Fatalf("create: %+v", created)
	}
	orderID := fmt.Sprint(created.Data["productOrderId"])
	replayed := call(t, r, http.MethodPost, "/api/v1/consumer/product-orders", body, token, "order-create-00001")
	if replayed.Code != 201 || replayed.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("create replay: %+v", replayed)
	}
	var locked int
	must(t, db.Table("product_sku").Select("stock_locked").Where("id=?", skuA).Scan(&locked).Error)
	if locked != 2 {
		t.Fatalf("locked=%d want 2", locked)
	}
	failed := call(t, r, http.MethodPost, "/api/v1/consumer/product-orders/"+orderID+"/mock-pay", map[string]any{"outcome": "FAILURE"}, token, "payment-failure-01")
	if failed.Code != 200 || fmt.Sprint(failed.Data["status"]) != "PENDING_PAYMENT" {
		t.Fatalf("failed pay: %+v", failed)
	}
	paid := call(t, r, http.MethodPost, "/api/v1/consumer/product-orders/"+orderID+"/mock-pay", map[string]any{"outcome": "SUCCESS"}, token, "payment-success-01")
	if paid.Code != 200 || fmt.Sprint(paid.Data["status"]) != "PENDING_SHIPMENT" {
		t.Fatalf("success pay: %+v", paid)
	}
	detail := call(t, r, http.MethodGet, "/api/v1/consumer/product-orders/"+orderID, nil, token, "")
	if detail.Code != 200 || detail.Data["address"] == nil {
		t.Fatalf("order address snapshot: %+v", detail)
	}
	deleted := call(t, r, http.MethodDelete, "/api/v1/consumer/addresses/"+addressID, nil, token, "address-delete-001")
	if deleted.Code != 200 {
		t.Fatalf("delete address: %+v", deleted)
	}
	still := call(t, r, http.MethodGet, "/api/v1/consumer/product-orders/"+orderID, nil, token, "")
	if still.Code != 200 || still.Data["address"] == nil {
		t.Fatalf("snapshot lost after delete: %+v", still)
	}
	otherToken := consumerToken(t, cfg.Auth.JWTSecret, otherConsumer, sessionID)
	forbidden := call(t, r, http.MethodGet, "/api/v1/consumer/product-orders/"+orderID, nil, otherToken, "")
	if forbidden.Code != 401 { // session subject mismatch rejects before order lookup
		t.Fatalf("cross consumer must be rejected: %+v", forbidden)
	}
	cancelled := call(t, r, http.MethodPost, "/api/v1/consumer/product-orders", map[string]any{"addressId": fmt.Sprint(address.Data["addressId"]), "items": []map[string]any{{"skuId": fmt.Sprint(skuA), "quantity": 1}}}, token, "order-cancel-0001")
	if cancelled.Code != 404 { // deleted addresses cannot create an order
		t.Fatalf("deleted address rejected: %+v", cancelled)
	}
	// A separate pending order is expired and the service releases the exact reserved quantity.
	var addresses struct{ ID uint64 }
	must(t, db.Table("consumer_address").Select("id").Where("consumer_id=? AND status='ACTIVE'", consumerID).Take(&addresses).Error)
	pending := call(t, r, http.MethodPost, "/api/v1/consumer/product-orders", map[string]any{"addressId": fmt.Sprint(addresses.ID), "items": []map[string]any{{"skuId": fmt.Sprint(skuB), "quantity": 1}}}, token, "order-cancel-pending")
	if pending.Code != 201 {
		t.Fatalf("cancel order create: %+v", pending)
	}
	cancel := call(t, r, http.MethodPost, "/api/v1/consumer/product-orders/"+fmt.Sprint(pending.Data["productOrderId"])+"/cancel", map[string]any{}, token, "order-cancel-action")
	if cancel.Code != 200 || fmt.Sprint(cancel.Data["reasonCode"]) != "CANCELLED" {
		t.Fatalf("cancel: %+v", cancel)
	}
	expiring := call(t, r, http.MethodPost, "/api/v1/consumer/product-orders", map[string]any{"addressId": fmt.Sprint(addresses.ID), "items": []map[string]any{{"skuId": fmt.Sprint(skuB), "quantity": 1}}}, token, "order-expire-00001")
	if expiring.Code != 201 {
		t.Fatalf("expire order create: %+v", expiring)
	}
	expireID := fmt.Sprint(expiring.Data["productOrderId"])
	must(t, db.Table("product_order").Where("id=?", expireID).Update("payment_expire_at", time.Now().Add(-time.Minute)).Error)
	n, e := productorders.NewService(db).CloseExpired(t.Context())
	must(t, e)
	if n < 1 {
		t.Fatal("expired order was not closed")
	}
	var sold, lockedB int
	must(t, db.Table("product_sku").Select("stock_sold,stock_locked").Where("id=?", skuB).Row().Scan(&sold, &lockedB))
	if sold != 1 || lockedB != 0 {
		t.Fatalf("sku B sold/locked=%d/%d", sold, lockedB)
	}
}

type result struct {
	Code   int
	Data   map[string]any
	Header http.Header
	Body   string
}

func call(t *testing.T, r http.Handler, method, path string, body any, token, key string) result {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	q := httptest.NewRequest(method, path, bytes.NewReader(raw))
	q.Header.Set("Content-Type", "application/json")
	if token != "" {
		q.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		q.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	var v struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	return result{w.Code, v.Data, w.Header(), w.Body.String()}
}
func consumerToken(t *testing.T, secret string, consumer, session uint64) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": fmt.Sprint(consumer), "sid": fmt.Sprint(session), "tv": "1", "clientType": "CONSUMER_H5", "exp": time.Now().Add(time.Hour).Unix()})
	s, e := tok.SignedString([]byte(secret))
	must(t, e)
	return s
}
func seedProduct(t *testing.T, db *gorm.DB, product, version, sku uint64, price, mode, freight string, stock int) {
	t.Helper()
	must(t, db.Table("product").Create(map[string]any{"id": product, "status": "PUBLISHED", "version": 1, "product_no": fmt.Sprintf("H5-%d", product), "creator_id": product, "ip_id": product, "authorization_id": product, "work_id": product, "current_version_id": version, "fulfillment_mode": "PLATFORM_SHIP", "sale_region_mode": "NATIONWIDE"}).Error)
	must(t, db.Table("product_version").Create(map[string]any{"id": version, "status": "PUBLISHED", "version": 1, "product_id": product, "version_no": 1, "product_name": fmt.Sprintf("测试商品-%d", product), "product_type": "CULTURAL_PRODUCT", "cover_file_id": product, "public_copyright_notice": "测试版权说明", "freight_mode": mode, "freight_amount": freight, "after_sale_template_id": product, "rule_snapshot": "{}", "check_status": "PASS"}).Error)
	must(t, db.Table("product_sku").Create(map[string]any{"id": sku, "status": "ACTIVE", "version": 1, "product_version_id": version, "sku_code": fmt.Sprintf("SKU-%d", sku), "sku_name": "默认规格", "sale_price": price, "stock_total": stock, "sku_status": "ON_SALE"}).Error)
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
