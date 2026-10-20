package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/database"
	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/idgen"
)

func TestActivityLifecycleWithMySQL(t *testing.T) {
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
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	userID, creatorID, ipID, orderID, authID, scopeID, templateID, fileID := idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next(), idgen.Next()
	defer cleanup(t, db, userID, creatorID, ipID, orderID, authID, scopeID, templateID, fileID)
	now := time.Now()
	city := "350100"
	must(t, db.Table("sys_user").Create(map[string]any{"id": userID, "status": "ACTIVE", "version": 1, "username": fmt.Sprintf("cnt03_%d", userID), "password_hash": "unused", "display_name": "CNT03测试创作者"}).Error)
	must(t, db.Table("creator_profile").Create(map[string]any{"id": creatorID, "user_id": userID, "status": "ACTIVE", "version": 1, "subject_type": "PERSONAL", "display_name": "CNT03测试创作者", "identity_status": "VERIFIED", "letter_status": "OPTIONAL"}).Error)
	must(t, db.Table("ip_asset").Create(map[string]any{"id": ipID, "status": "LISTED", "version": 1, "ip_no": fmt.Sprintf("IP-CNT03-%d", ipID), "ip_name": "CNT03测试IP", "ip_type": "CULTURAL", "source_type": "CITY_COLLECTION", "main_city_org_id": 1, "short_intro": "测试", "detail_content": "测试", "culture_story": "测试", "ip_feature": "测试", "creative_directions_json": "[]", "applicable_product_types_json": "[]", "applicable_activity_types_json": "[\"STUDY_TOUR\"]", "public_copyright_notice": "测试", "purchase_notice": "测试", "authorization_mode": "NORMAL_SHARE", "mall_visible": true, "suggested_transaction_price_amount": "1.00"}).Error)
	must(t, db.Table("auth_order").Create(map[string]any{"id": orderID, "status": "PAID", "version": 1, "auth_order_no": fmt.Sprintf("CNT03-ORDER-%d", orderID), "creator_id": creatorID, "ip_id": ipID, "auth_plan_version_id": 1, "amount": "1.00", "currency": "CNY", "payment_expire_at": now.Add(time.Hour), "contract_agreed": true, "purchase_snapshot": "{}", "contract_status": "GENERATED", "evidence_status": "EVIDENCED", "commercial_allowed_snapshot": true, "content_type_snapshot": "ACTIVITY_ONLY"}).Error)
	must(t, db.Table("creator_authorization").Create(map[string]any{"id": authID, "status": "ACTIVE", "version": 1, "authorization_no": fmt.Sprintf("CNT03-AUTH-%d", authID), "creator_id": creatorID, "ip_id": ipID, "auth_order_id": orderID, "authorization_mode": "NORMAL_SHARE", "start_at": now.Add(-time.Hour), "end_at": now.Add(time.Hour), "active_flag": true, "scope_snapshot_id": scopeID}).Error)
	scope, _ := json.Marshal(map[string]any{"regionMode": "DESIGNATED", "regionCodes": []string{city}, "scenes": []string{"ACTIVITY"}, "contentType": "ACTIVITY_ONLY"})
	must(t, db.Table("auth_scope_snapshot").Create(map[string]any{"id": scopeID, "authorization_id": authID, "ip_snapshot": "{}", "plan_snapshot": string(scope), "distribution_snapshot": "{}", "scope_json": string(scope)}).Error)
	must(t, db.Table("after_sale_rule_template").Create(map[string]any{"id": templateID, "status": "ACTIVE", "version": 1, "template_no": fmt.Sprintf("CNT03-TPL-%d", templateID), "template_name": "活动退款规则", "biz_type": "ACTIVITY", "conditions_json": "{}", "refund_rule_json": "{}", "enabled": true}).Error)
	must(t, db.Table("file_object").Create(map[string]any{"id": fileID, "status": "ACTIVE", "version": 1, "created_by": userID, "file_no": fmt.Sprintf("CNT03-FILE-%d", fileID), "storage_key": fmt.Sprintf("cnt03/%d", fileID), "original_name": "cover.png", "mime_type": "image/png", "file_size": 8, "file_hash": fmt.Sprintf("%064d", fileID), "security_level": "PRIVATE", "upload_status": "SUCCESS"}).Error)
	ensureTicketDict(t, db)
	svc := NewService(db, contentmoderation.NewProvider(config.ContentSafetyConfig{Provider: "mock"}))
	creator := creators.CurrentCreator{UserID: userID, CreatorID: creatorID}
	in := Input{AuthorizationID: authID, ActivityName: "CNT03 文化研学", ActivityIntro: "研学活动", CoverFileID: fileID, VenueName: "测试场馆", MapPointName: "测试点位", Longitude: "119.2965", Latitude: "26.0745", ActualCityCode: city, Notice: "注意事项", PublicCopyrightNotice: "版权说明", RefundTemplateID: templateID, Sessions: []Session{{SessionNo: "S001", SessionName: "上午场", StartAt: now.Add(24 * time.Hour), EndAt: now.Add(26 * time.Hour), QuotaTotal: 100, Tickets: []Ticket{{TicketTypeCode: "ADULT", ChargeType: "PAID", Price: "88.00", QuotaTotal: 60}, {TicketTypeCode: "EARLY_BIRD", ChargeType: "FREE", Price: "0", QuotaTotal: 40}}}}}
	created, code, err := svc.Create(context.Background(), creator, in)
	if err != nil || code != "" || created["status"] != "PUBLISHED" {
		t.Fatalf("create=%#v code=%s err=%v", created, code, err)
	}
	activityID := parseActivityID(t, created["activityId"])
	activityVersionID := parseActivityID(t, created["activityVersionId"])
	listed, code, err := svc.List(context.Background(), creatorID, ListInput{IPID: ipID, PageNo: 1, PageSize: 20})
	if err != nil || code != "" || listed["total"].(int64) != 1 {
		t.Fatalf("list=%#v code=%s err=%v", listed, code, err)
	}
	if _, code, err = svc.ChangeBooking(context.Background(), creator, activityID, false); err != nil || code != "" {
		t.Fatalf("pause code=%s err=%v", code, err)
	}
	if _, code, err = svc.ChangeBooking(context.Background(), creator, activityID, true); err != nil || code != "" {
		t.Fatalf("resume code=%s err=%v", code, err)
	}
	must(t, db.Table("activity_booking").Create(map[string]any{"id": idgen.Next(), "status": "PENDING_VERIFICATION", "version": 1, "booking_no": fmt.Sprintf("CNT03-BOOK-%d", activityID), "consumer_id": 1, "activity_id": activityID, "session_id": 1, "ticket_type_id": 1, "quantity": 1, "amount": "0.00", "rule_snapshot": "{}"}).Error)
	if _, code, err = svc.Delist(context.Background(), creator, activityID); err != nil || code != "ACTIVITY_HAS_SUCCESSFUL_BOOKINGS" {
		t.Fatalf("delist with booking code=%s err=%v", code, err)
	}
	must(t, db.Table("activity_booking").Where("activity_id=?", activityID).Delete(nil).Error)
	if _, code, err = svc.Delist(context.Background(), creator, activityID); err != nil || code != "" {
		t.Fatalf("delist code=%s err=%v", code, err)
	}
	// A separate published activity with a completed session is closed by the worker method.
	must(t, db.Table("activity").Where("id=?", activityID).Update("status", "PUBLISHED").Error)
	must(t, db.Table("activity_session").Where("activity_version_id=?", activityVersionID).Update("end_at", now.Add(-time.Hour)).Error)
	if count, err := svc.CloseEnded(context.Background()); err != nil || count < 1 {
		t.Fatalf("close ended count=%d err=%v", count, err)
	}
}

func ensureTicketDict(t *testing.T, db *gorm.DB) {
	t.Helper()
	for i, item := range []struct{ Code, Name string }{{"ADULT", "成人票"}, {"EARLY_BIRD", "早鸟票"}} {
		var n int64
		must(t, db.Table("sys_dict_item").Where("dict_type='ACTIVITY_TICKET_TYPE' AND dict_code=?", item.Code).Count(&n).Error)
		if n == 0 {
			must(t, db.Table("sys_dict_item").Create(map[string]any{"id": idgen.Next(), "status": "ACTIVE", "version": 1, "dict_type": "ACTIVITY_TICKET_TYPE", "dict_code": item.Code, "dict_name": item.Name, "sort_no": i + 1, "enabled": true}).Error)
		}
	}
}
func parseActivityID(t *testing.T, value any) uint64 {
	t.Helper()
	var id uint64
	if _, err := fmt.Sscan(fmt.Sprint(value), &id); err != nil || id == 0 {
		t.Fatalf("invalid activity id %#v", value)
	}
	return id
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func cleanup(t *testing.T, db *gorm.DB, userID, creatorID, ipID, orderID, authID, scopeID, templateID, fileID uint64) {
	t.Helper()
	db.Exec("DELETE FROM content_publish_record WHERE object_type='ACTIVITY' AND object_id IN (SELECT id FROM activity WHERE creator_id=?)", creatorID)
	db.Exec("DELETE FROM content_detection_record WHERE object_type='ACTIVITY' AND object_id IN (SELECT id FROM activity WHERE creator_id=?)", creatorID)
	db.Exec("DELETE FROM activity_booking WHERE activity_id IN (SELECT id FROM activity WHERE creator_id=?)", creatorID)
	db.Exec("DELETE FROM activity_ticket_type WHERE session_id IN (SELECT id FROM activity_session WHERE activity_version_id IN (SELECT id FROM activity_version WHERE activity_id IN (SELECT id FROM activity WHERE creator_id=?)))", creatorID)
	db.Exec("DELETE FROM activity_session WHERE activity_version_id IN (SELECT id FROM activity_version WHERE activity_id IN (SELECT id FROM activity WHERE creator_id=?))", creatorID)
	db.Exec("DELETE FROM activity_version WHERE activity_id IN (SELECT id FROM activity WHERE creator_id=?)", creatorID)
	db.Exec("DELETE FROM activity WHERE creator_id=?", creatorID)
	db.Exec("DELETE FROM auth_scope_snapshot WHERE id=?", scopeID)
	db.Exec("DELETE FROM creator_authorization WHERE id=?", authID)
	db.Exec("DELETE FROM auth_order WHERE id=?", orderID)
	db.Exec("DELETE FROM after_sale_rule_template WHERE id=?", templateID)
	db.Exec("DELETE FROM file_object WHERE id=?", fileID)
	db.Exec("DELETE FROM ip_asset WHERE id=?", ipID)
	db.Exec("DELETE FROM creator_profile WHERE id=?", creatorID)
	db.Exec("DELETE FROM sys_user WHERE id=?", userID)
}
