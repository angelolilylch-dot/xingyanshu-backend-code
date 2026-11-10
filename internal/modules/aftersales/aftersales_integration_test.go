package aftersales_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/aftersales"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/idgen"
)

func TestAfterSalesHTTPWithMySQL(t *testing.T) {
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
	if _, err = database.ApplyMigrations(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	base := idgen.Next()
	consumerID, sessionID := base+1, base+2
	orderID, itemID := base+10, base+11
	skuID := base + 14
	bookingID, session := base+20, base+21
	fileID := base + 30
	t.Cleanup(func() {
		db.Exec("DELETE FROM return_logistics WHERE after_sale_id IN (SELECT id FROM after_sale_ticket WHERE consumer_id=?)", consumerID)
		db.Exec("DELETE FROM after_sale_evidence WHERE after_sale_id IN (SELECT id FROM after_sale_ticket WHERE consumer_id=?)", consumerID)
		db.Exec("DELETE FROM after_sale_ticket WHERE consumer_id=?", consumerID)
		db.Exec("DELETE FROM product_order_item WHERE id=?", itemID)
		db.Exec("DELETE FROM product_sku WHERE id=?", skuID)
		db.Exec("DELETE FROM payment_transaction WHERE biz_order_id IN (?,?)", orderID, bookingID)
		db.Exec("DELETE FROM activity_ticket_type WHERE id=?", base+24)
		db.Exec("DELETE FROM product_order WHERE id=?", orderID)
		db.Exec("DELETE FROM activity_booking WHERE id=?", bookingID)
		db.Exec("DELETE FROM activity_session WHERE id=?", session)
		db.Exec("DELETE FROM file_object WHERE id=?", fileID)
		db.Exec("DELETE FROM auth_session WHERE id=?", sessionID)
		db.Exec("DELETE FROM consumer_account WHERE id=?", consumerID)
	})
	must(t, db.Table("consumer_account").Create(map[string]any{"id": consumerID, "status": "ACTIVE", "version": 1, "mobile_cipher": "13900000001", "mobile_hash": fmt.Sprint(consumerID), "mobile_masked": "139****0001"}).Error)
	must(t, db.Table("auth_session").Create(map[string]any{"id": sessionID, "subject_type": "CONSUMER", "subject_id": consumerID, "client_type": "CONSUMER_H5", "refresh_token_hash": "test", "token_version": 1, "status": "ACTIVE", "expires_at": time.Now().Add(time.Hour)}).Error)
	must(t, db.Table("product_order").Create(map[string]any{"id": orderID, "status": "PENDING_SHIPMENT", "version": 1, "order_no": fmt.Sprintf("ORD-%d", orderID), "consumer_id": consumerID, "order_amount": "30.00", "freight_amount": "0.00", "pay_amount": "30.00", "payment_expire_at": time.Now().Add(time.Hour), "recipient_snapshot": []byte(`{"recipientName":"测试用户"}`), "fulfillment_status": "PENDING_SHIPMENT"}).Error)
	must(t, db.Table("product_order_item").Create(map[string]any{"id": itemID, "order_id": orderID, "line_no": 1, "product_id": base + 12, "product_version_id": base + 13, "sku_id": base + 14, "quantity": 3, "unit_price": "10.00", "item_amount": "30.00", "item_snapshot": []byte(`{"afterSaleTypes":["CANCEL_REFUND","RETURN_REFUND"]}`)}).Error)
	must(t, db.Table("product_sku").Create(map[string]any{"id": skuID, "status": "ACTIVE", "version": 1, "product_version_id": base + 13, "sku_code": fmt.Sprintf("AFS-SKU-%d", skuID), "sku_name": "测试规格", "sale_price": "10.00", "stock_total": 10, "stock_sold": 1, "sku_status": "ON_SALE"}).Error)
	must(t, db.Table("payment_transaction").Create(map[string]any{"id": base + 15, "status": "SUCCESS", "version": 1, "payment_no": fmt.Sprintf("PAY-%d", base+15), "biz_type": "PRODUCT_ORDER", "biz_order_id": orderID, "idempotency_key": fmt.Sprintf("PAY-%d", base+15), "payment_method": "MOCK", "amount": "30.00", "payment_status": "SUCCESS"}).Error)
	must(t, db.Table("file_object").Create(map[string]any{"id": fileID, "status": "ACTIVE", "version": 1, "file_no": fmt.Sprintf("AFS-%d", fileID), "storage_key": fmt.Sprintf("afs/%d", fileID), "original_name": "evidence.jpg", "mime_type": "image/jpeg", "file_size": 12, "file_hash": fmt.Sprintf("%064d", fileID), "security_level": "PRIVATE", "upload_status": "SUCCESS"}).Error)
	// The activity case is only refundable before the session starts.
	must(t, db.Table("activity_session").Create(map[string]any{"id": session, "status": "ACTIVE", "version": 1, "activity_version_id": base + 22, "session_no": "S1", "session_name": "测试场", "start_at": time.Now().Add(time.Hour), "end_at": time.Now().Add(2 * time.Hour), "quota_total": 10}).Error)
	must(t, db.Table("activity_booking").Create(map[string]any{"id": bookingID, "status": "PENDING_VERIFICATION", "version": 1, "booking_no": fmt.Sprintf("BOOK-%d", bookingID), "consumer_id": consumerID, "activity_id": base + 23, "session_id": session, "ticket_type_id": base + 24, "quantity": 1, "amount": "20.00", "rule_snapshot": []byte(`{"afterSaleTypes":["CANCEL_REFUND"]}`), "verification_status": "PENDING"}).Error)
	must(t, db.Table("activity_ticket_type").Create(map[string]any{"id": base + 24, "status": "ACTIVE", "version": 1, "session_id": session, "ticket_code": fmt.Sprintf("AFS-TICKET-%d", base), "ticket_name": "测试票", "charge_type": "PAID", "price": "20.00", "quota_total": 10, "quota_used": 1}).Error)
	must(t, db.Table("payment_transaction").Create(map[string]any{"id": base + 25, "status": "SUCCESS", "version": 1, "payment_no": fmt.Sprintf("PAY-%d", base+25), "biz_type": "ACTIVITY_BOOKING", "biz_order_id": bookingID, "idempotency_key": fmt.Sprintf("PAY-%d", base+25), "payment_method": "MOCK", "amount": "20.00", "payment_status": "SUCCESS"}).Error)
	r := gin.New()
	aftersales.RegisterRoutes(r.Group("/api/v1"), aftersales.NewService(db, cfg.AfterSale, contentmoderation.NewProvider(cfg.ContentSafety)), identity.RequireConsumerAuthentication(db, cfg.Auth))
	token := token(t, cfg.Auth.JWTSecret, consumerID, sessionID)
	productBody := map[string]any{"productOrderId": fmt.Sprint(orderID), "productOrderItemId": fmt.Sprint(itemID), "afterSaleType": "CANCEL_REFUND", "applyQuantity": 2, "reasonCode": "QUALITY_ISSUE", "evidenceFileIds": []string{fmt.Sprint(fileID)}}
	created := call(t, r, http.MethodPost, "/api/v1/consumer/after-sales/product", productBody, token, "afs-product-create-001")
	if created.Code != http.StatusCreated || fmt.Sprint(created.Data["applyQuantity"]) != "2" {
		t.Fatalf("create=%+v", created)
	}
	replay := call(t, r, http.MethodPost, "/api/v1/consumer/after-sales/product", productBody, token, "afs-product-create-001")
	if replay.Code != http.StatusCreated || replay.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay=%+v", replay)
	}
	conflict := call(t, r, http.MethodPost, "/api/v1/consumer/after-sales/product", map[string]any{"productOrderId": fmt.Sprint(orderID), "productOrderItemId": fmt.Sprint(itemID), "afterSaleType": "CANCEL_REFUND", "applyQuantity": 1, "reasonCode": "QUALITY_ISSUE"}, token, "afs-product-create-001")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict=%+v", conflict)
	}
	withdraw := call(t, r, http.MethodPost, "/api/v1/consumer/after-sales/"+fmt.Sprint(created.Data["afterSaleId"])+"/withdraw", map[string]any{}, token, "afs-withdraw-000001", "1")
	if withdraw.Code != http.StatusOK || fmt.Sprint(withdraw.Data["status"]) != "WITHDRAWN" {
		t.Fatalf("withdraw=%+v", withdraw)
	}
	var orderStatus string
	must(t, db.Table("product_order").Select("status").Where("id=?", orderID).Scan(&orderStatus).Error)
	if orderStatus != "PENDING_SHIPMENT" {
		t.Fatalf("withdraw must restore order, got %s", orderStatus)
	}
	invalid := call(t, r, http.MethodPost, "/api/v1/consumer/after-sales/product", map[string]any{"productOrderId": fmt.Sprint(orderID), "productOrderItemId": fmt.Sprint(itemID), "afterSaleType": "RETURN_REFUND", "applyQuantity": 1, "reasonCode": "QUALITY_ISSUE"}, token, "afs-product-invalid-01")
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unshipped return must fail=%+v", invalid)
	}
	// AFS-02 approval exposes the fixed address and advances a return case.
	must(t, db.Table("product_order").Where("id=?", orderID).Updates(map[string]any{"status": "SHIPPED", "fulfillment_status": "SHIPPED", "after_sale_status": nil}).Error)
	returnApply := call(t, r, http.MethodPost, "/api/v1/consumer/after-sales/product", map[string]any{"productOrderId": fmt.Sprint(orderID), "productOrderItemId": fmt.Sprint(itemID), "afterSaleType": "RETURN_REFUND", "applyQuantity": 1, "reasonCode": "QUALITY_ISSUE"}, token, "afs-product-return-001")
	if returnApply.Code != http.StatusCreated {
		t.Fatalf("return apply=%+v", returnApply)
	}
	returnID := fmt.Sprint(returnApply.Data["afterSaleId"])
	pendingDetail := call(t, r, http.MethodGet, "/api/v1/consumer/after-sales/"+returnID, nil, token, "")
	if pendingDetail.Code != http.StatusOK || pendingDetail.Data["returnAddress"] != nil {
		t.Fatalf("return address must remain hidden before approval=%+v", pendingDetail)
	}
	svc := aftersales.NewService(db, cfg.AfterSale, contentmoderation.NewProvider(cfg.ContentSafety))
	returnIDValue := mustID(t, returnID)
	reviewed, code, err := svc.Review(t.Context(), identity.CurrentAdmin{UserID: base + 90}, returnIDValue, aftersales.ReviewInput{Approved: true}, 1, "127.0.0.1")
	must(t, err)
	if code != "" || fmt.Sprint(reviewed["status"]) != "RETURNING" {
		t.Fatalf("return review=%+v code=%s", reviewed, code)
	}
	returnDetail := call(t, r, http.MethodGet, "/api/v1/consumer/after-sales/"+returnID, nil, token, "")
	if returnDetail.Code != http.StatusOK || returnDetail.Data["returnAddress"] == nil {
		t.Fatalf("approved return must reveal fixed address=%+v", returnDetail)
	}
	returnLogistics := call(t, r, http.MethodPost, "/api/v1/consumer/after-sales/"+returnID+"/return-logistics", map[string]any{"carrierCode": "YTO", "trackingNo": fmt.Sprintf("RETURN-%d", base)}, token, "afs-return-logistics-01", "2")
	if returnLogistics.Code != http.StatusOK || fmt.Sprint(returnLogistics.Data["status"]) != "PENDING_RECEIPT_CONFIRMATION" {
		t.Fatalf("return logistics=%+v", returnLogistics)
	}
	var afterSaleStatus string
	must(t, db.Table("product_order").Select("after_sale_status").Where("id=?", orderID).Scan(&afterSaleStatus).Error)
	if afterSaleStatus != "PENDING_RECEIPT_CONFIRMATION" {
		t.Fatalf("return logistics must synchronize product after_sale_status, got %s", afterSaleStatus)
	}
	received, code, err := svc.ConfirmReceipt(t.Context(), identity.CurrentAdmin{UserID: base + 90}, returnIDValue, aftersales.ReceiptInput{Received: true}, 3, "127.0.0.1")
	must(t, err)
	if code != "" || fmt.Sprint(received["status"]) != "PENDING_REFUND" {
		t.Fatalf("return receipt=%+v code=%s", received, code)
	}
	refunded, code, err := svc.Refund(t.Context(), identity.CurrentAdmin{UserID: base + 91}, returnIDValue, aftersales.RefundInput{Comment: "财务复核"}, "127.0.0.1")
	must(t, err)
	if code != "" || fmt.Sprint(refunded["status"]) != "REFUNDED" {
		t.Fatalf("return refund=%+v code=%s", refunded, code)
	}
	var sold int
	must(t, db.Table("product_sku").Select("stock_sold").Where("id=?", skuID).Scan(&sold).Error)
	if sold != 0 {
		t.Fatalf("refund must restore sold stock, got %d", sold)
	}
	activity := call(t, r, http.MethodPost, "/api/v1/consumer/after-sales/activity", map[string]any{"activityBookingId": fmt.Sprint(bookingID), "reasonCode": "PERSONAL_UNAVAILABLE"}, token, "afs-activity-create-01")
	if activity.Code != http.StatusCreated {
		t.Fatalf("activity create=%+v", activity)
	}
	activityID := mustID(t, fmt.Sprint(activity.Data["afterSaleId"]))
	reviewedActivity, code, err := svc.Review(t.Context(), identity.CurrentAdmin{UserID: base + 90}, activityID, aftersales.ReviewInput{Approved: true}, 1, "127.0.0.1")
	must(t, err)
	if code != "" || fmt.Sprint(reviewedActivity["status"]) != "PENDING_REFUND" {
		t.Fatalf("activity review=%+v code=%s", reviewedActivity, code)
	}
	refundedActivity, code, err := svc.Refund(t.Context(), identity.CurrentAdmin{UserID: base + 91}, activityID, aftersales.RefundInput{}, "127.0.0.1")
	must(t, err)
	if code != "" || fmt.Sprint(refundedActivity["status"]) != "REFUNDED" {
		t.Fatalf("activity refund=%+v code=%s", refundedActivity, code)
	}
	var used int
	must(t, db.Table("activity_ticket_type").Select("quota_used").Where("id=?", base+24).Scan(&used).Error)
	if used != 0 {
		t.Fatalf("refund must restore used quota, got %d", used)
	}
	// A session that has started cannot enter the refund flow.
	must(t, db.Table("activity_session").Where("id=?", session).Update("start_at", time.Now().Add(-time.Minute)).Error)
	secondBooking := bookingID + 100
	must(t, db.Table("activity_booking").Create(map[string]any{"id": secondBooking, "status": "PENDING_VERIFICATION", "version": 1, "booking_no": fmt.Sprintf("BOOK-%d", secondBooking), "consumer_id": consumerID, "activity_id": base + 23, "session_id": session, "ticket_type_id": base + 24, "quantity": 1, "amount": "20.00", "rule_snapshot": []byte(`{"afterSaleTypes":["CANCEL_REFUND"]}`), "verification_status": "PENDING"}).Error)
	t.Cleanup(func() { db.Exec("DELETE FROM activity_booking WHERE id=?", secondBooking) })
	expired := call(t, r, http.MethodPost, "/api/v1/consumer/after-sales/activity", map[string]any{"activityBookingId": fmt.Sprint(secondBooking), "reasonCode": "PERSONAL_UNAVAILABLE"}, token, "afs-activity-expired-1")
	if expired.Code != http.StatusUnprocessableEntity {
		t.Fatalf("started session must fail=%+v", expired)
	}
}

type response struct {
	Code   int
	Data   map[string]any
	Header http.Header
	Body   string
}

func call(t *testing.T, r http.Handler, method, path string, body any, access, key string, version ...string) response {
	t.Helper()
	raw, _ := json.Marshal(body)
	q := httptest.NewRequest(method, path, bytes.NewReader(raw))
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("Authorization", "Bearer "+access)
	if key != "" {
		q.Header.Set("Idempotency-Key", key)
	}
	if len(version) > 0 {
		q.Header.Set("If-Match-Version", version[0])
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	var out struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return response{w.Code, out.Data, w.Header(), w.Body.String()}
}
func token(t *testing.T, secret string, consumer, session uint64) string {
	t.Helper()
	v := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": fmt.Sprint(consumer), "sid": fmt.Sprint(session), "tv": "1", "clientType": "CONSUMER_H5", "exp": time.Now().Add(time.Hour).Unix()})
	out, e := v.SignedString([]byte(secret))
	must(t, e)
	return out
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}

func mustID(t *testing.T, value string) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(value, 10, 64)
	must(t, err)
	return v
}

var _ *gorm.DB
