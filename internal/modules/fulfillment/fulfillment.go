package fulfillment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/idgen"
	"xingyanshu-backend/internal/platform/logistics"
)

const shipmentPermission = "FULFILLMENT_SHIPMENT_MANAGE"

type Service struct {
	db               *gorm.DB
	provider         logistics.Provider
	autoConfirmAfter time.Duration
}

type ShipInput struct {
	CarrierCode string `json:"carrierCode" binding:"required,max=64"`
	TrackingNo  string `json:"trackingNo" binding:"required,max=128"`
	ShippedAt   string `json:"shippedAt" binding:"omitempty,max=64"`
	ProofFileID string `json:"proofFileId" binding:"omitempty"`
	Remark      string `json:"remark" binding:"omitempty,max=500"`
}

type deliveryRow struct {
	ID          uint64
	OrderID     uint64 `gorm:"column:order_id"`
	DeliveryNo  string `gorm:"column:delivery_no"`
	Status      string
	CarrierCode string     `gorm:"column:carrier_code"`
	TrackingNo  string     `gorm:"column:tracking_no"`
	ShippedAt   time.Time  `gorm:"column:shipped_at"`
	SignedAt    *time.Time `gorm:"column:signed_at"`
	ProofFileID *uint64    `gorm:"column:proof_file_id"`
	Remark      string     `gorm:"column:remark"`
}

func NewService(db *gorm.DB, provider logistics.Provider, cfg config.LogisticsConfig) *Service {
	return &Service{db: db, provider: provider, autoConfirmAfter: cfg.AutoConfirmAfter}
}

func (s *Service) Ship(ctx context.Context, current identity.CurrentAdmin, orderID uint64, in ShipInput, clientIP string) (map[string]any, string, error) {
	if !current.HasPermission(shipmentPermission) {
		return nil, "AUTH_FORBIDDEN", nil
	}
	carrier, tracking := strings.TrimSpace(in.CarrierCode), strings.TrimSpace(in.TrackingNo)
	if carrier == "" || tracking == "" {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	shippedAt := time.Now()
	if strings.TrimSpace(in.ShippedAt) != "" {
		value, err := time.Parse(time.RFC3339, strings.TrimSpace(in.ShippedAt))
		if err != nil {
			return nil, "VALIDATION_FORMAT_INVALID", nil
		}
		shippedAt = value
	}
	var proofID *uint64
	if value := strings.TrimSpace(in.ProofFileID); value != "" {
		var id uint64
		if _, err := fmt.Sscan(value, &id); err != nil || id == 0 {
			return nil, "VALIDATION_FORMAT_INVALID", nil
		}
		proofID = &id
	}
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order struct {
			ID        uint64
			Status    string
			Recipient []byte `gorm:"column:recipient_snapshot"`
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("product_order").Select("id,status,recipient_snapshot").Where("id=?", orderID).Take(&order).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				code = "PRODUCT_ORDER_NOT_FOUND"
				return nil
			}
			return err
		}
		if order.Status != "PENDING_SHIPMENT" {
			code = "STATE_TRANSITION_NOT_ALLOWED"
			return nil
		}
		id := idgen.Next()
		d := deliveryRow{ID: id, OrderID: orderID, DeliveryNo: fmt.Sprintf("DEL-%d", id), Status: "SHIPPED", CarrierCode: carrier, TrackingNo: tracking, ShippedAt: shippedAt, ProofFileID: proofID, Remark: strings.TrimSpace(in.Remark)}
		if err := tx.Table("delivery_order").Create(map[string]any{"id": d.ID, "status": d.Status, "version": 1, "created_by": current.UserID, "updated_by": current.UserID, "status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID), "delivery_no": d.DeliveryNo, "order_id": d.OrderID, "shipper_type": "PLATFORM", "shipper_id": current.UserID, "carrier_code": d.CarrierCode, "tracking_no": d.TrackingNo, "shipped_at": d.ShippedAt, "proof_file_id": proofID, "remark": d.Remark}).Error; err != nil {
			return err
		}
		if err := tx.Table("product_order").Where("id=? AND status=?", orderID, "PENDING_SHIPMENT").Updates(map[string]any{"status": "SHIPPED", "fulfillment_status": "SHIPPED", "version": gorm.Expr("version+1"), "updated_by": current.UserID, "status_changed_at": time.Now(), "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}).Error; err != nil {
			return err
		}
		op := audit.Operator{Type: "USER", ID: current.UserID, RoleCode: "FULFILLMENT_CUSTOMER_SERVICE", ClientIP: clientIP}
		if err := audit.WriteStatusHistory(tx, "PRODUCT_ORDER", orderID, "PENDING_SHIPMENT", "SHIPPED", "录入发货信息", op); err != nil {
			return err
		}
		if err := audit.WriteLog(tx, "CREATE_DELIVERY_ORDER", "DELIVERY_ORDER", id, nil, map[string]any{"orderId": fmt.Sprint(orderID), "carrierCode": carrier}, "SUCCESS", op); err != nil {
			return err
		}
		out = deliveryData(d, false)
		return nil
	})
	return out, code, err
}

func (s *Service) List(ctx context.Context, current identity.CurrentAdmin, status string) (map[string]any, string, error) {
	if !current.HasPermission(shipmentPermission) {
		return nil, "AUTH_FORBIDDEN", nil
	}
	q := s.db.WithContext(ctx).Table("product_order o").Joins("LEFT JOIN delivery_order d ON d.order_id=o.id").Select("o.id order_id,o.order_no,o.status order_status,d.id,d.delivery_no,d.status delivery_status,d.carrier_code,d.tracking_no,d.shipped_at,d.signed_at,o.recipient_snapshot")
	if value := strings.ToUpper(strings.TrimSpace(status)); value != "" {
		q = q.Where("o.status=?", value)
	}
	var rows []struct {
		OrderID        uint64     `gorm:"column:order_id"`
		OrderNo        string     `gorm:"column:order_no"`
		OrderStatus    string     `gorm:"column:order_status"`
		DeliveryID     *uint64    `gorm:"column:id"`
		DeliveryNo     *string    `gorm:"column:delivery_no"`
		DeliveryStatus *string    `gorm:"column:delivery_status"`
		CarrierCode    *string    `gorm:"column:carrier_code"`
		TrackingNo     *string    `gorm:"column:tracking_no"`
		ShippedAt      *time.Time `gorm:"column:shipped_at"`
		SignedAt       *time.Time `gorm:"column:signed_at"`
		Recipient      []byte     `gorm:"column:recipient_snapshot"`
	}
	if err := q.Order("o.created_at DESC").Find(&rows).Error; err != nil {
		return nil, "", err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		item := map[string]any{"productOrderId": fmt.Sprint(r.OrderID), "orderNo": r.OrderNo, "status": r.OrderStatus, "receiverMasked": maskRecipient(r.Recipient)}
		if r.DeliveryID != nil {
			item["deliveryId"] = fmt.Sprint(*r.DeliveryID)
			item["deliveryNo"] = str(r.DeliveryNo)
			item["deliveryStatus"] = str(r.DeliveryStatus)
			item["carrierCode"] = str(r.CarrierCode)
			item["trackingNo"] = maskTracking(str(r.TrackingNo))
			item["shippedAt"] = r.ShippedAt
			item["signedAt"] = r.SignedAt
		}
		items = append(items, item)
	}
	return map[string]any{"items": items, "total": len(items)}, "", nil
}

func (s *Service) Get(ctx context.Context, current identity.CurrentAdmin, deliveryID uint64) (map[string]any, string, error) {
	if !current.HasPermission(shipmentPermission) {
		return nil, "AUTH_FORBIDDEN", nil
	}
	d, events, code, err := s.load(ctx, deliveryID)
	if err != nil || code != "" {
		return nil, code, err
	}
	out := deliveryData(d, true)
	out["events"] = events
	return out, "", nil
}
func (s *Service) ConsumerLogistics(ctx context.Context, current identity.CurrentConsumer, orderID uint64) (map[string]any, string, error) {
	var count int64
	if err := s.db.WithContext(ctx).Table("product_order").Where("id=? AND consumer_id=?", orderID, current.ConsumerID).Count(&count).Error; err != nil {
		return nil, "", err
	}
	if count != 1 {
		return nil, "PRODUCT_ORDER_NOT_FOUND", nil
	}
	var id uint64
	if err := s.db.WithContext(ctx).Table("delivery_order").Select("id").Where("order_id=?", orderID).Take(&id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return map[string]any{"status": "PENDING_SHIPMENT", "events": []any{}}, "", nil
		}
		return nil, "", err
	}
	d, events, code, err := s.load(ctx, id)
	if err != nil || code != "" {
		return nil, code, err
	}
	out := deliveryData(d, true)
	out["events"] = events
	return out, "", nil
}
func (s *Service) ConfirmReceipt(ctx context.Context, current identity.CurrentConsumer, orderID uint64, clientIP string) (map[string]any, string, error) {
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var o struct {
			ID              uint64
			Status          string
			AfterSaleStatus string `gorm:"column:after_sale_status"`
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("product_order").Select("id,status,after_sale_status").Where("id=? AND consumer_id=?", orderID, current.ConsumerID).Take(&o).Error; e != nil {
			code = "PRODUCT_ORDER_NOT_FOUND"
			return nil
		}
		if o.Status != "SIGNED" || strings.TrimSpace(o.AfterSaleStatus) != "" {
			code = "ORDER_NOT_CONFIRMABLE"
			return nil
		}
		now := time.Now()
		if e := tx.Table("product_order").Where("id=? AND status=?", orderID, "SIGNED").Updates(map[string]any{"status": "COMPLETED", "fulfillment_status": "COMPLETED", "version": gorm.Expr("version+1"), "updated_by": current.ConsumerID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.ConsumerID)}).Error; e != nil {
			return e
		}
		op := audit.Operator{Type: "USER", ID: current.ConsumerID, RoleCode: "CONSUMER", ClientIP: clientIP}
		if e := audit.WriteStatusHistory(tx, "PRODUCT_ORDER", orderID, "SIGNED", "COMPLETED", "消费者确认收货", op); e != nil {
			return e
		}
		out = map[string]any{"productOrderId": fmt.Sprint(orderID), "status": "COMPLETED"}
		return nil
	})
	return out, code, err
}

func (s *Service) RefreshDue(ctx context.Context, limit int) (int, error) {
	if s.provider == nil {
		return 0, nil
	}
	if limit < 1 {
		limit = 20
	}
	var rows []deliveryRow
	if err := s.db.WithContext(ctx).Table("delivery_order").Where("status='SHIPPED'").Order("updated_at").Limit(limit).Find(&rows).Error; err != nil {
		return 0, err
	}
	count := 0
	for _, d := range rows {
		var phone string
		_ = s.db.WithContext(ctx).Table("product_order").Select("recipient_snapshot").Where("id=?", d.OrderID).Row().Scan(&phone)
		phone = last4FromSnapshot([]byte(phone))
		track, err := s.provider.Query(ctx, d.TrackingNo, phone)
		if err != nil {
			continue
		}
		if err = s.persistTrack(ctx, d, track); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
func (s *Service) AutoCompleteDue(ctx context.Context) (int, error) {
	if s.autoConfirmAfter <= 0 {
		return 0, nil
	}
	now := time.Now()
	r := s.db.WithContext(ctx).Table("product_order").Where("status='SIGNED' AND (after_sale_status IS NULL OR after_sale_status='') AND EXISTS (SELECT 1 FROM delivery_order d WHERE d.order_id=product_order.id AND d.signed_at IS NOT NULL AND d.signed_at<=?)", now.Add(-s.autoConfirmAfter)).Updates(map[string]any{"status": "COMPLETED", "fulfillment_status": "COMPLETED", "status_reason_code": "AUTO_CONFIRMED", "status_reason": "签收满售后保护期自动确认", "version": gorm.Expr("version+1"), "status_changed_at": now, "status_changed_by_type": "SYSTEM", "status_changed_by_id": "fulfillment-worker"})
	return int(r.RowsAffected), r.Error
}

func (s *Service) persistTrack(ctx context.Context, d deliveryRow, track logistics.Track) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, e := range track.Events {
			sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%d|%s", d.ID, e.Time.UTC().UnixMilli(), e.Description)))
			eventNo := hex.EncodeToString(sum[:])
			if err := tx.Clauses(clause.Insert{Modifier: "IGNORE"}).Table("logistics_event").Create(map[string]any{"id": idgen.Next(), "delivery_id": d.ID, "event_no": eventNo, "event_status": track.Status, "event_time": e.Time, "event_description": e.Description, "raw_payload_hash": eventNo}).Error; err != nil {
				return err
			}
		}
		if track.Signed {
			now := time.Now()
			if e := tx.Table("delivery_order").Where("id=? AND status='SHIPPED'", d.ID).Updates(map[string]any{"status": "SIGNED", "signed_at": now, "version": gorm.Expr("version+1"), "status_changed_at": now, "status_changed_by_type": "THIRD_PARTY", "status_changed_by_id": "tencent-cloudmarket"}).Error; e != nil {
				return e
			}
			return tx.Table("product_order").Where("id=? AND status='SHIPPED'", d.OrderID).Updates(map[string]any{"status": "SIGNED", "fulfillment_status": "SIGNED", "version": gorm.Expr("version+1"), "status_changed_at": now, "status_changed_by_type": "THIRD_PARTY", "status_changed_by_id": "tencent-cloudmarket"}).Error
		}
		return nil
	})
}
func (s *Service) load(ctx context.Context, id uint64) (deliveryRow, []map[string]any, string, error) {
	var d deliveryRow
	if err := s.db.WithContext(ctx).Table("delivery_order").Where("id=?", id).Take(&d).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return d, nil, "DELIVERY_ORDER_NOT_FOUND", nil
		}
		return d, nil, "", err
	}
	var e []map[string]any
	if err := s.db.WithContext(ctx).Table("logistics_event").Where("delivery_id=?", id).Select("event_status eventStatus,event_time eventTime,event_description description").Order("event_time DESC").Find(&e).Error; err != nil {
		return d, nil, "", err
	}
	return d, e, "", nil
}
func deliveryData(d deliveryRow, _ bool) map[string]any {
	return map[string]any{"deliveryId": fmt.Sprint(d.ID), "deliveryNo": d.DeliveryNo, "productOrderId": fmt.Sprint(d.OrderID), "status": d.Status, "carrierCode": d.CarrierCode, "trackingNo": d.TrackingNo, "shippedAt": d.ShippedAt, "signedAt": d.SignedAt, "proofFileId": d.ProofFileID, "remark": d.Remark}
}
func str(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func maskTracking(v string) string {
	if len(v) <= 6 {
		return v
	}
	return v[:3] + strings.Repeat("*", len(v)-6) + v[len(v)-3:]
}
func maskRecipient(raw []byte) string {
	var v struct {
		RecipientName string `json:"recipientName"`
		Mobile        string `json:"mobile"`
	}
	_ = json.Unmarshal(raw, &v)
	name := []rune(v.RecipientName)
	if len(name) > 1 {
		v.RecipientName = string(name[:1]) + "*"
	}
	if len(v.Mobile) >= 7 {
		v.Mobile = v.Mobile[:3] + "****" + v.Mobile[len(v.Mobile)-4:]
	}
	return v.RecipientName + " " + v.Mobile
}
func last4FromSnapshot(raw []byte) string {
	var v struct {
		Mobile string `json:"mobile"`
	}
	_ = json.Unmarshal(raw, &v)
	if len(v.Mobile) >= 4 {
		return v.Mobile[len(v.Mobile)-4:]
	}
	return ""
}
