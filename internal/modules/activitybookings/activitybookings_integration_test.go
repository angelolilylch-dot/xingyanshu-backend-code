package activitybookings_test

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
	"xingyanshu-backend/internal/modules/activitybookings"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/idgen"
)

// TestActivityBookingsHTTPWithMySQL exercises free/paid, failure/success payment,
// idempotency, expiry and consumer JWT isolation against real MySQL.
func TestActivityBookingsHTTPWithMySQL(t *testing.T) {
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
	consumer, other, sid, activity, version, session, paid, free := base+1, base+2, base+3, base+10, base+11, base+12, base+13, base+14
	t.Cleanup(func() {
		db.Exec("DELETE FROM verification_code WHERE booking_id IN (SELECT id FROM activity_booking WHERE activity_id=?)", activity)
		db.Exec("DELETE FROM payment_transaction WHERE biz_type='ACTIVITY_BOOKING' AND biz_order_id IN (SELECT id FROM activity_booking WHERE activity_id=?)", activity)
		db.Exec("DELETE FROM activity_quota_reservation WHERE booking_id IN (SELECT id FROM activity_booking WHERE activity_id=?)", activity)
		db.Exec("DELETE FROM activity_booking WHERE activity_id=?", activity)
		db.Exec("DELETE FROM activity_ticket_type WHERE session_id=?", session)
		db.Exec("DELETE FROM activity_session WHERE id=?", session)
		db.Exec("DELETE FROM activity_version WHERE id=?", version)
		db.Exec("DELETE FROM activity WHERE id=?", activity)
		db.Exec("DELETE FROM auth_session WHERE id=?", sid)
		db.Exec("DELETE FROM consumer_account WHERE id IN ?", []uint64{consumer, other})
	})
	must(t, db.Table("consumer_account").Create(map[string]any{"id": consumer, "status": "ACTIVE", "version": 1, "mobile_cipher": "13900000001", "mobile_hash": fmt.Sprint(consumer), "mobile_masked": "139****0001"}).Error)
	must(t, db.Table("consumer_account").Create(map[string]any{"id": other, "status": "ACTIVE", "version": 1, "mobile_cipher": "13900000002", "mobile_hash": fmt.Sprint(other), "mobile_masked": "139****0002"}).Error)
	must(t, db.Table("auth_session").Create(map[string]any{"id": sid, "subject_type": "CONSUMER", "subject_id": consumer, "client_type": "CONSUMER_H5", "refresh_token_hash": "test", "token_version": 1, "status": "ACTIVE", "expires_at": time.Now().Add(time.Hour)}).Error)
	seed(t, db, activity, version, session, paid, free)
	r := gin.New()
	activitybookings.RegisterRoutes(r.Group("/api/v1"), activitybookings.NewService(db), identity.RequireConsumerAuthentication(db, cfg.Auth), "test")
	token := consumerToken(t, cfg.Auth.JWTSecret, consumer, sid)
	freeBody := map[string]any{"activityId": fmt.Sprint(activity), "sessionId": fmt.Sprint(session), "ticketTypeId": fmt.Sprint(free), "quantity": 2}
	preview := call(t, r, http.MethodPost, "/api/v1/consumer/activity-bookings/preview", freeBody, token, "")
	if preview.Code != 200 || fmt.Sprint(preview.Data["amount"]) != "0.00" {
		t.Fatalf("free preview=%+v", preview)
	}
	freeCreated := call(t, r, http.MethodPost, "/api/v1/consumer/activity-bookings", freeBody, token, "free-create-00001")
	if freeCreated.Code != 201 || fmt.Sprint(freeCreated.Data["status"]) != "PENDING_VERIFICATION" {
		t.Fatalf("free create=%+v", freeCreated)
	}
	freeID := fmt.Sprint(freeCreated.Data["activityBookingId"])
	replay := call(t, r, http.MethodPost, "/api/v1/consumer/activity-bookings", freeBody, token, "free-create-00001")
	if replay.Code != 201 || replay.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay=%+v", replay)
	}
	code := call(t, r, http.MethodGet, "/api/v1/consumer/activity-bookings/"+freeID+"/verification-code", nil, token, "")
	if code.Code != 200 || fmt.Sprint(code.Data["displayCode"]) == "" {
		t.Fatalf("code=%+v", code)
	}
	paidBody := map[string]any{"activityId": fmt.Sprint(activity), "sessionId": fmt.Sprint(session), "ticketTypeId": fmt.Sprint(paid), "quantity": 1}
	created := call(t, r, http.MethodPost, "/api/v1/consumer/activity-bookings", paidBody, token, "paid-create-00001")
	if created.Code != 201 || fmt.Sprint(created.Data["status"]) != "PENDING_PAYMENT" {
		t.Fatalf("paid create=%+v", created)
	}
	paidID := fmt.Sprint(created.Data["activityBookingId"])
	failed := call(t, r, http.MethodPost, "/api/v1/consumer/activity-bookings/"+paidID+"/mock-pay", map[string]any{"outcome": "FAILURE"}, token, "pay-failure-00001")
	if failed.Code != 200 || fmt.Sprint(failed.Data["status"]) != "PENDING_PAYMENT" {
		t.Fatalf("failure=%+v", failed)
	}
	success := call(t, r, http.MethodPost, "/api/v1/consumer/activity-bookings/"+paidID+"/mock-pay", map[string]any{"outcome": "SUCCESS"}, token, "pay-success-00001")
	if success.Code != 200 || fmt.Sprint(success.Data["status"]) != "PENDING_VERIFICATION" {
		t.Fatalf("success=%+v", success)
	}
	quota := call(t, r, http.MethodPost, "/api/v1/consumer/activity-bookings", map[string]any{"activityId": fmt.Sprint(activity), "sessionId": fmt.Sprint(session), "ticketTypeId": fmt.Sprint(free), "quantity": 4}, token, "over-quota-000001")
	if quota.Code != 422 {
		t.Fatalf("session quota protection=%+v", quota)
	}
	pending := call(t, r, http.MethodPost, "/api/v1/consumer/activity-bookings", paidBody, token, "paid-cancel-00001")
	if pending.Code != 201 {
		t.Fatalf("pending=%+v", pending)
	}
	cancelled := call(t, r, http.MethodPost, "/api/v1/consumer/activity-bookings/"+fmt.Sprint(pending.Data["activityBookingId"])+"/cancel-pending-payment", map[string]any{}, token, "paid-cancel-action")
	if cancelled.Code != 200 || fmt.Sprint(cancelled.Data["status"]) != "CLOSED" {
		t.Fatalf("cancelled=%+v", cancelled)
	}
	pending = call(t, r, http.MethodPost, "/api/v1/consumer/activity-bookings", paidBody, token, "paid-expire-00001")
	if pending.Code != 201 {
		t.Fatalf("expiring pending=%+v", pending)
	}
	must(t, db.Table("activity_booking").Where("id=?", pending.Data["activityBookingId"]).Update("payment_expire_at", time.Now().Add(-time.Minute)).Error)
	n, e := activitybookings.NewService(db).CloseExpired(t.Context())
	must(t, e)
	if n < 1 {
		t.Fatal("payment timeout not closed")
	}
	otherToken := consumerToken(t, cfg.Auth.JWTSecret, other, sid)
	denied := call(t, r, http.MethodGet, "/api/v1/consumer/activity-bookings/"+freeID, nil, otherToken, "")
	if denied.Code != 401 {
		t.Fatalf("cross consumer=%+v", denied)
	}
	must(t, db.Table("activity_session").Where("id=?", session).Update("end_at", time.Now().Add(-time.Minute)).Error)
	n, e = activitybookings.NewService(db).ExpireEnded(t.Context())
	must(t, e)
	if n < 2 {
		t.Fatalf("ended bookings expired=%d", n)
	}
}

func TestActivityBookingMockRouteIsAbsentOutsideTest(t *testing.T) {
	r := gin.New()
	activitybookings.RegisterRoutes(r.Group("/api/v1"), activitybookings.NewService(nil), func(c *gin.Context) { c.Next() }, "production")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/consumer/activity-bookings/1/mock-pay", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("production mock route status=%d", w.Code)
	}
}

func seed(t *testing.T, db *gorm.DB, a, v, s, p, f uint64) {
	t.Helper()
	now := time.Now()
	must(t, db.Table("activity").Create(map[string]any{"id": a, "status": "PUBLISHED", "version": 1, "activity_no": fmt.Sprintf("AB-%d", a), "creator_id": a, "ip_id": a, "authorization_id": a, "actual_city_code": "350100", "current_version_id": v}).Error)
	must(t, db.Table("activity_version").Create(map[string]any{"id": v, "status": "PUBLISHED", "version": 1, "activity_id": a, "version_no": 1, "activity_name": "预约测试活动", "cover_file_id": a, "venue_name": "测试场地", "map_point_name": "测试点", "public_copyright_notice": "测试", "refund_template_id": a, "rule_snapshot": "{}", "check_status": "PASS"}).Error)
	must(t, db.Table("activity_session").Create(map[string]any{"id": s, "status": "PUBLISHED", "version": 1, "activity_version_id": v, "session_no": "S1", "session_name": "上午场", "start_at": now.Add(time.Hour), "end_at": now.Add(2 * time.Hour), "quota_total": 4}).Error)
	must(t, db.Table("activity_ticket_type").Create(map[string]any{"id": p, "status": "PUBLISHED", "version": 1, "session_id": s, "ticket_code": "PAID", "ticket_name": "收费票", "charge_type": "PAID", "price": "50.00", "quota_total": 2}).Error)
	must(t, db.Table("activity_ticket_type").Create(map[string]any{"id": f, "status": "PUBLISHED", "version": 1, "session_id": s, "ticket_code": "FREE", "ticket_name": "免费票", "charge_type": "FREE", "price": "0.00", "quota_total": 3}).Error)
}

type result struct {
	Code   int
	Data   map[string]any
	Header http.Header
	Body   string
}

func call(t *testing.T, r http.Handler, m, p string, body any, token, key string) result {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	q := httptest.NewRequest(m, p, bytes.NewReader(raw))
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
func consumerToken(t *testing.T, secret string, consumer, sid uint64) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": fmt.Sprint(consumer), "sid": fmt.Sprint(sid), "tv": "1", "clientType": "CONSUMER_H5", "exp": time.Now().Add(time.Hour).Unix()})
	s, e := tok.SignedString([]byte(secret))
	must(t, e)
	return s
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
