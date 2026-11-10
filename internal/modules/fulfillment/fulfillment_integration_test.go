package fulfillment

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
	"xingyanshu-backend/internal/platform/idgen"
	"xingyanshu-backend/internal/platform/logistics"
)

type signedProvider struct{}

func (signedProvider) Query(context.Context, string, string) (logistics.Track, error) {
	return logistics.Track{CarrierName: "测试快递", TrackingNo: "YT-TEST", Status: "SIGN", Signed: true, Events: []logistics.Event{{Time: time.Now().Add(-time.Minute), Description: "已签收"}}}, nil
}

func TestFulfillmentWithMySQL(t *testing.T) {
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
	consumerID, orderID := base+1, base+2
	t.Cleanup(func() {
		db.Exec("DELETE FROM logistics_event WHERE delivery_id IN (SELECT id FROM delivery_order WHERE order_id=?)", orderID)
		db.Exec("DELETE FROM delivery_order WHERE order_id=?", orderID)
		db.Exec("DELETE FROM product_order WHERE id=?", orderID)
	})
	if err := db.Table("product_order").Create(map[string]any{"id": orderID, "status": "PENDING_SHIPMENT", "version": 1, "order_no": fmt.Sprintf("ORD-%d", orderID), "consumer_id": consumerID, "order_amount": "1.00", "freight_amount": "0.00", "pay_amount": "1.00", "payment_expire_at": time.Now().Add(time.Hour), "recipient_snapshot": []byte(`{"recipientName":"测试用户","mobile":"13900001234"}`), "fulfillment_status": "PENDING_SHIPMENT"}).Error; err != nil {
		t.Fatal(err)
	}
	s := NewService(db, signedProvider{}, config.LogisticsConfig{AutoConfirmAfter: 7 * 24 * time.Hour})
	admin := identity.CurrentAdmin{UserID: base + 3, PermissionCodes: map[string]struct{}{shipmentPermission: {}}}
	shipped, code, err := s.Ship(t.Context(), admin, orderID, ShipInput{CarrierCode: "YTO", TrackingNo: fmt.Sprintf("YT-%d", orderID), Remark: "测试发货备注"}, "127.0.0.1")
	if err != nil || code != "" || shipped["status"] != "SHIPPED" || shipped["remark"] != "测试发货备注" {
		t.Fatalf("ship data=%+v code=%s err=%v", shipped, code, err)
	}
	if n, err := s.RefreshDue(t.Context(), 10); err != nil || n != 1 {
		t.Fatalf("refresh n=%d err=%v", n, err)
	}
	logisticsData, code, err := s.ConsumerLogistics(t.Context(), identity.CurrentConsumer{ConsumerID: consumerID}, orderID)
	if err != nil || code != "" || logisticsData["status"] != "SIGNED" {
		t.Fatalf("logistics=%+v code=%s err=%v", logisticsData, code, err)
	}
	completed, code, err := s.ConfirmReceipt(t.Context(), identity.CurrentConsumer{ConsumerID: consumerID}, orderID, "127.0.0.1")
	if err != nil || code != "" || completed["status"] != "COMPLETED" {
		t.Fatalf("confirm=%+v code=%s err=%v", completed, code, err)
	}
	if err := db.Table("product_order").Where("id=?", orderID).Updates(map[string]any{"status": "SIGNED", "fulfillment_status": "SIGNED", "after_sale_status": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("delivery_order").Where("order_id=?", orderID).Updates(map[string]any{"status": "SIGNED", "signed_at": time.Now().Add(-8 * 24 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if count, err := s.AutoCompleteDue(t.Context()); err != nil || count != 1 {
		t.Fatalf("auto complete count=%d err=%v", count, err)
	}
}
