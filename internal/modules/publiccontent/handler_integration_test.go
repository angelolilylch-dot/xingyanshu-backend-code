package publiccontent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
)

type testStorage struct{}

func (testStorage) Put(context.Context, string, string, io.Reader) error { return nil }
func (testStorage) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("image"))), nil
}
func (testStorage) SignDownloadURL(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

func TestPublicH5HTTPWithMySQL(t *testing.T) {
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
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	base := uint64(time.Now().UnixNano())
	productID, productVersionID, skuID := base+1, base+2, base+3
	activityID, activityVersionID, sessionID, ticketID := base+4, base+5, base+6, base+7
	coverID, detailID := base+8, base+9
	for _, id := range []uint64{coverID, detailID} {
		mustPublic(t, db.Table("file_object").Create(map[string]any{"id": id, "status": "ACTIVE", "version": 1, "file_no": fmt.Sprintf("PUBLIC-%d", id), "storage_key": fmt.Sprintf("public/%d.png", id), "original_name": "image.png", "mime_type": "image/png", "file_size": 5, "file_hash": fmt.Sprintf("%064d", id), "security_level": "PRIVATE", "upload_status": "SUCCESS"}).Error)
	}
	mustPublic(t, db.Table("product").Create(map[string]any{"id": productID, "status": "PUBLISHED", "version": 1, "product_no": fmt.Sprintf("PUBLIC-P-%d", base), "creator_id": 1, "ip_id": 1, "authorization_id": 1, "work_id": 1, "current_version_id": productVersionID, "fulfillment_mode": "CREATOR_SHIP", "sale_region_mode": "NATIONWIDE"}).Error)
	mustPublic(t, db.Table("product_version").Create(map[string]any{"id": productVersionID, "status": "PUBLISHED", "version": 1, "product_id": productID, "version_no": 1, "product_name": "公开商品", "product_type": "CULTURAL_PRODUCT", "cover_file_id": coverID, "detail_files_json": fmt.Sprintf("[%d]", detailID), "public_copyright_notice": "公开版权说明", "freight_mode": "FREE", "freight_amount": "0", "after_sale_template_id": 1, "rule_snapshot": "{}", "check_status": "PASS"}).Error)
	mustPublic(t, db.Table("product_sku").Create(map[string]any{"id": skuID, "status": "ACTIVE", "version": 1, "product_version_id": productVersionID, "sku_code": fmt.Sprintf("SKU-%d", base), "sku_name": "默认规格", "sale_price": "12.00", "stock_total": 10, "sku_status": "ON_SALE"}).Error)
	now := time.Now()
	mustPublic(t, db.Table("activity").Create(map[string]any{"id": activityID, "status": "BOOKING_PAUSED", "version": 1, "activity_no": fmt.Sprintf("PUBLIC-A-%d", base), "creator_id": 1, "ip_id": 1, "authorization_id": 1, "actual_city_code": "350100", "current_version_id": activityVersionID}).Error)
	mustPublic(t, db.Table("activity_version").Create(map[string]any{"id": activityVersionID, "status": "BOOKING_PAUSED", "version": 1, "activity_id": activityID, "version_no": 1, "activity_name": "公开暂停活动", "cover_file_id": coverID, "venue_name": "测试场馆", "map_point_name": "测试点", "longitude": "119.2", "latitude": "26.0", "public_copyright_notice": "公开版权说明", "refund_template_id": 1, "rule_snapshot": "{}", "check_status": "PASS"}).Error)
	mustPublic(t, db.Table("activity_session").Create(map[string]any{"id": sessionID, "status": "ACTIVE", "version": 1, "activity_version_id": activityVersionID, "session_no": "S001", "start_at": now.Add(time.Hour), "end_at": now.Add(2 * time.Hour), "quota_total": 10}).Error)
	mustPublic(t, db.Table("activity_ticket_type").Create(map[string]any{"id": ticketID, "status": "ACTIVE", "version": 1, "session_id": sessionID, "ticket_code": "ADULT", "ticket_name": "成人票", "charge_type": "FREE", "price": "0", "quota_total": 10}).Error)
	defer func() {
		db.Exec("DELETE FROM activity_ticket_type WHERE id=?", ticketID)
		db.Exec("DELETE FROM activity_session WHERE id=?", sessionID)
		db.Exec("DELETE FROM activity_version WHERE id=?", activityVersionID)
		db.Exec("DELETE FROM activity WHERE id=?", activityID)
		db.Exec("DELETE FROM product_sku WHERE id=?", skuID)
		db.Exec("DELETE FROM product_version WHERE id=?", productVersionID)
		db.Exec("DELETE FROM product WHERE id=?", productID)
		db.Exec("DELETE FROM file_object WHERE id IN ?", []uint64{coverID, detailID})
	}()
	r := gin.New()
	RegisterRoutes(r.Group("/api/v1"), NewService(db, testStorage{}))
	for _, path := range []string{"/api/v1/public/home", "/api/v1/public/products", "/api/v1/public/activities", "/api/v1/public/products/" + fmt.Sprint(productID), "/api/v1/public/media/" + fmt.Sprint(detailID), "/api/v1/public/service-contact"} {
		w := servePublic(r, path)
		if w.Code != 200 {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
	w := servePublic(r, "/api/v1/public/activities")
	if !bytes.Contains(w.Body.Bytes(), []byte("bookingPaused")) || !bytes.Contains(w.Body.Bytes(), []byte("true")) {
		t.Fatalf("paused activity must stay visible: %s", w.Body.String())
	}
	w = servePublic(r, "/api/v1/public/products/"+fmt.Sprint(productID))
	if bytes.Contains(w.Body.Bytes(), []byte("ipId")) || !bytes.Contains(w.Body.Bytes(), []byte("/api/v1/public/media/"+fmt.Sprint(detailID))) {
		t.Fatalf("public product leaked IP or omitted detail URL: %s", w.Body.String())
	}
	w = servePublic(r, "/api/v1/public/home")
	if bytes.Contains(w.Body.Bytes(), []byte("ipId")) || !bytes.Contains(w.Body.Bytes(), []byte("公开商品")) || !bytes.Contains(w.Body.Bytes(), []byte("公开暂停活动")) {
		t.Fatalf("home must expose product/activity only, without IP fields: %s", w.Body.String())
	}
	mustPublic(t, db.Table("product").Where("id=?", productID).Update("status", "DELISTED").Error)
	w = servePublic(r, "/api/v1/public/media/"+fmt.Sprint(detailID))
	if w.Code != 404 {
		t.Fatalf("delisted product media status=%d body=%s", w.Code, w.Body.String())
	}
}

func servePublic(r *gin.Engine, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}
func mustPublic(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
