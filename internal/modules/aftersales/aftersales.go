// Package aftersales implements the consumer side of the after-sale lifecycle.
// Approval, return receipt and payment refund deliberately remain in AFS-02.
package aftersales

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/contentmoderation"
	"xingyanshu-backend/internal/platform/idgen"
)

const (
	bizProduct      = "PRODUCT"
	bizActivity     = "ACTIVITY"
	statusReview    = "PENDING_REVIEW"
	statusReturning = "RETURNING"
	statusReceipt   = "PENDING_RECEIPT_CONFIRMATION"
	statusWithdrawn = "WITHDRAWN"
	statusRejected  = "REJECTED"
)

type Service struct {
	db                    *gorm.DB
	fallbackReturnAddress config.ReturnAddressConfig
	moderation            contentmoderation.Provider
}

func NewService(db *gorm.DB, cfg config.AfterSaleConfig, moderation contentmoderation.Provider) *Service {
	return &Service{db: db, fallbackReturnAddress: cfg.ReturnAddress, moderation: moderation}
}

type ProductApplyInput struct {
	ProductOrderID     string   `json:"productOrderId" binding:"required"`
	ProductOrderItemID string   `json:"productOrderItemId" binding:"required"`
	AfterSaleType      string   `json:"afterSaleType" binding:"required,oneof=CANCEL_REFUND RETURN_REFUND"`
	ApplyQuantity      int      `json:"applyQuantity" binding:"required,min=1,max=99"`
	ReasonCode         string   `json:"reasonCode" binding:"required,max=40"`
	ReasonText         string   `json:"reasonText" binding:"omitempty,max=2000"`
	EvidenceFileIDs    []string `json:"evidenceFileIds" binding:"max=9"`
}

type ActivityApplyInput struct {
	ActivityBookingID string   `json:"activityBookingId" binding:"required"`
	ReasonCode        string   `json:"reasonCode" binding:"required,max=40"`
	ReasonText        string   `json:"reasonText" binding:"omitempty,max=2000"`
	EvidenceFileIDs   []string `json:"evidenceFileIds" binding:"max=9"`
}

type ReturnLogisticsInput struct {
	CarrierCode string `json:"carrierCode" binding:"required,max=64"`
	TrackingNo  string `json:"trackingNo" binding:"required,max=128"`
	ShippedAt   string `json:"shippedAt" binding:"omitempty,datetime=2006-01-02T15:04:05Z07:00"`
}

type ticketRow struct {
	ID                 uint64
	AfterSaleNo        string  `gorm:"column:after_sale_no"`
	BizType            string  `gorm:"column:biz_type"`
	BizOrderID         uint64  `gorm:"column:biz_order_id"`
	ProductOrderItemID *uint64 `gorm:"column:product_order_item_id"`
	ConsumerID         uint64  `gorm:"column:consumer_id"`
	Status             string
	Version            uint
	AfterSaleType      string    `gorm:"column:after_sale_type"`
	ApplyReasonCode    string    `gorm:"column:apply_reason_code"`
	ApplyReasonText    string    `gorm:"column:apply_reason_text"`
	ApplyQuantity      int       `gorm:"column:apply_quantity"`
	ApplyAmount        string    `gorm:"column:apply_amount"`
	ApprovedAmount     string    `gorm:"column:approved_amount"`
	OriginalBizStatus  string    `gorm:"column:original_biz_status"`
	RuleSnapshot       []byte    `gorm:"column:rule_snapshot"`
	ReturnAddress      []byte    `gorm:"column:return_address_snapshot"`
	CreatedAt          time.Time `gorm:"column:created_at"`
}

func parseID(value string) (uint64, bool) {
	v, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	return v, err == nil && v > 0
}

func cents(value string) (int64, error) {
	v, err := strconv.ParseFloat(value, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid amount")
	}
	return int64(v*100 + .5), nil
}
func money(value int64) string { return fmt.Sprintf("%d.%02d", value/100, value%100) }

// returnAddressForTicket gives the administrator-maintained platform address
// priority. The private YAML value is a deployment fallback for a fresh system
// before the platform administrator has completed the configuration page.
func (s *Service) returnAddressForTicket(tx *gorm.DB) config.ReturnAddressConfig {
	var item struct {
		Value string `gorm:"column:config_value"`
	}
	if err := tx.Table("sys_config").Select("config_value").Where("config_key=? AND status=?", "PLATFORM_RETURN_ADDRESS", "ACTIVE").Take(&item).Error; err == nil {
		var address config.ReturnAddressConfig
		if json.Unmarshal([]byte(item.Value), &address) == nil && address.Valid() {
			return address
		}
	}
	return s.fallbackReturnAddress
}

func (s *Service) ApplyProduct(ctx context.Context, current identity.CurrentConsumer, in ProductApplyInput, clientIP string) (map[string]any, string, error) {
	orderID, ok := parseID(in.ProductOrderID)
	if !ok {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	itemID, ok := parseID(in.ProductOrderItemID)
	if !ok {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	if strings.TrimSpace(in.ReasonCode) == "OTHER" && strings.TrimSpace(in.ReasonText) == "" {
		return nil, "AFTER_SALE_REASON_REQUIRED", nil
	}
	if code := s.moderateReason(ctx, current.ConsumerID, in.ReasonText); code != "" {
		return nil, code, nil
	}
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order struct {
			ID              uint64
			ConsumerID      uint64 `gorm:"column:consumer_id"`
			Status          string
			Version         uint
			AfterSaleStatus string `gorm:"column:after_sale_status"`
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("product_order").Where("id=? AND consumer_id=?", orderID, current.ConsumerID).Take(&order).Error; err != nil {
			code = "PRODUCT_ORDER_NOT_FOUND"
			return nil
		}
		if order.Status != "PENDING_SHIPMENT" && order.Status != "SHIPPED" && order.Status != "SIGNED" && order.Status != "COMPLETED" {
			code = "AFTER_SALE_NOT_ALLOWED"
			return nil
		}
		if order.Status == "PENDING_SHIPMENT" && in.AfterSaleType != "CANCEL_REFUND" {
			code = "AFTER_SALE_TYPE_NOT_ALLOWED"
			return nil
		}
		if (order.Status == "SHIPPED" || order.Status == "SIGNED" || order.Status == "COMPLETED") && in.AfterSaleType != "RETURN_REFUND" {
			code = "AFTER_SALE_TYPE_NOT_ALLOWED"
			return nil
		}
		var item struct {
			ID         uint64
			OrderID    uint64 `gorm:"column:order_id"`
			Quantity   int
			ItemAmount string `gorm:"column:item_amount"`
			Snapshot   []byte `gorm:"column:item_snapshot"`
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("product_order_item").Where("id=? AND order_id=?", itemID, orderID).Take(&item).Error; err != nil {
			code = "PRODUCT_ORDER_ITEM_NOT_FOUND"
			return nil
		}
		if in.ApplyQuantity > item.Quantity {
			code = "AFTER_SALE_QUANTITY_EXCEEDED"
			return nil
		}
		if exists, err := activeTicket(tx, bizProduct, orderID, itemID); err != nil {
			return err
		} else if exists {
			code = "AFTER_SALE_CONFLICT"
			return nil
		}
		if !allowsType(item.Snapshot, in.AfterSaleType) {
			code = "AFTER_SALE_TYPE_NOT_ALLOWED"
			return nil
		}
		amount, err := cents(item.ItemAmount)
		if err != nil {
			return err
		}
		applyAmount := money(amount * int64(in.ApplyQuantity) / int64(item.Quantity))
		if code = validateEvidence(tx, in.EvidenceFileIDs); code != "" {
			return nil
		}
		return s.createTicket(tx, ticketCreate{BizType: bizProduct, BizOrderID: orderID, ProductOrderItemID: &itemID, ConsumerID: current.ConsumerID, Type: in.AfterSaleType, Quantity: in.ApplyQuantity, ReasonCode: in.ReasonCode, ReasonText: in.ReasonText, Amount: applyAmount, OriginalStatus: order.Status, Rule: item.Snapshot, Evidence: in.EvidenceFileIDs, ClientIP: clientIP}, &out, &code)
	})
	return out, code, err
}

func (s *Service) ApplyActivity(ctx context.Context, current identity.CurrentConsumer, in ActivityApplyInput, clientIP string) (map[string]any, string, error) {
	bookingID, ok := parseID(in.ActivityBookingID)
	if !ok {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	if strings.TrimSpace(in.ReasonCode) == "OTHER" && strings.TrimSpace(in.ReasonText) == "" {
		return nil, "AFTER_SALE_REASON_REQUIRED", nil
	}
	if code := s.moderateReason(ctx, current.ConsumerID, in.ReasonText); code != "" {
		return nil, code, nil
	}
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var b struct {
			ID         uint64
			ConsumerID uint64 `gorm:"column:consumer_id"`
			Status     string
			Quantity   int
			Amount     string
			Rule       []byte `gorm:"column:rule_snapshot"`
			SessionID  uint64 `gorm:"column:session_id"`
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("activity_booking").Where("id=? AND consumer_id=?", bookingID, current.ConsumerID).Take(&b).Error; err != nil {
			code = "ACTIVITY_BOOKING_NOT_FOUND"
			return nil
		}
		if b.Status != "PENDING_VERIFICATION" {
			code = "AFTER_SALE_NOT_ALLOWED"
			return nil
		}
		var start time.Time
		if err := tx.Table("activity_session").Select("start_at").Where("id=?", b.SessionID).Take(&start).Error; err != nil {
			return err
		}
		if !start.After(time.Now()) {
			code = "ACTIVITY_AFTER_SALE_DEADLINE_EXPIRED"
			return nil
		}
		if exists, err := activeTicket(tx, bizActivity, bookingID, 0); err != nil {
			return err
		} else if exists {
			code = "AFTER_SALE_CONFLICT"
			return nil
		}
		if !allowsType(b.Rule, "CANCEL_REFUND") {
			code = "AFTER_SALE_TYPE_NOT_ALLOWED"
			return nil
		}
		if code = validateEvidence(tx, in.EvidenceFileIDs); code != "" {
			return nil
		}
		return s.createTicket(tx, ticketCreate{BizType: bizActivity, BizOrderID: bookingID, ConsumerID: current.ConsumerID, Type: "CANCEL_REFUND", Quantity: b.Quantity, ReasonCode: in.ReasonCode, ReasonText: in.ReasonText, Amount: b.Amount, OriginalStatus: b.Status, Rule: b.Rule, Evidence: in.EvidenceFileIDs, ClientIP: clientIP}, &out, &code)
	})
	return out, code, err
}

func (s *Service) moderateReason(ctx context.Context, consumerID uint64, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if s.moderation == nil {
		return "AFTER_SALE_TEXT_CHECK_ERROR"
	}
	result, err := s.moderation.Moderate(ctx, contentmoderation.Request{Text: value, DataID: fmt.Sprint(idgen.Next()), UserID: fmt.Sprint(consumerID), Language: "zh"})
	if err != nil {
		return "AFTER_SALE_TEXT_CHECK_ERROR"
	}
	if result.Decision == contentmoderation.DecisionBlock {
		return "AFTER_SALE_TEXT_REJECTED"
	}
	return ""
}

type ticketCreate struct {
	BizType                                        string
	BizOrderID                                     uint64
	ProductOrderItemID                             *uint64
	ConsumerID                                     uint64
	Type                                           string
	Quantity                                       int
	ReasonCode, ReasonText, Amount, OriginalStatus string
	Rule                                           []byte
	Evidence                                       []string
	ClientIP                                       string
}

func (s *Service) createTicket(tx *gorm.DB, in ticketCreate, out *map[string]any, code *string) error {
	now := time.Now()
	id := idgen.Next()
	no := fmt.Sprintf("AFTER-%d", id)
	address, err := json.Marshal(s.returnAddressForTicket(tx))
	if err != nil {
		return err
	}
	if err = tx.Table("after_sale_ticket").Create(map[string]any{"id": id, "status": statusReview, "version": 1, "created_by": in.ConsumerID, "updated_by": in.ConsumerID, "created_at": now, "updated_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(in.ConsumerID), "after_sale_no": no, "biz_type": in.BizType, "biz_order_id": in.BizOrderID, "product_order_item_id": in.ProductOrderItemID, "consumer_id": in.ConsumerID, "after_sale_type": in.Type, "apply_quantity": in.Quantity, "apply_reason_code": strings.TrimSpace(in.ReasonCode), "apply_reason_text": blank(in.ReasonText), "apply_amount": in.Amount, "original_biz_status": in.OriginalStatus, "rule_snapshot": in.Rule, "return_address_snapshot": address}).Error; err != nil {
		return err
	}
	for _, raw := range in.Evidence {
		fid, _ := parseID(raw)
		if err = tx.Table("after_sale_evidence").Create(map[string]any{"id": idgen.Next(), "created_by": in.ConsumerID, "after_sale_id": id, "file_id": fid, "evidence_type": "APPLY"}).Error; err != nil {
			return err
		}
	}
	if in.BizType == bizProduct {
		err = tx.Table("product_order").Where("id=? AND status=?", in.BizOrderID, in.OriginalStatus).Updates(map[string]any{"status": "AFTER_SALE", "after_sale_status": statusReview, "version": gorm.Expr("version+1"), "updated_by": in.ConsumerID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(in.ConsumerID)}).Error
	} else {
		err = tx.Table("activity_booking").Where("id=? AND status=?", in.BizOrderID, in.OriginalStatus).Updates(map[string]any{"status": "REFUNDING", "version": gorm.Expr("version+1"), "updated_by": in.ConsumerID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(in.ConsumerID)}).Error
	}
	if err != nil {
		return err
	}
	op := audit.Operator{Type: "USER", ID: in.ConsumerID, RoleCode: "CONSUMER", ClientIP: in.ClientIP}
	if err = audit.WriteStatusHistory(tx, "AFTER_SALE", id, "", statusReview, "消费者提交售后申请", op); err != nil {
		return err
	}
	if err = audit.WriteLog(tx, "CREATE_AFTER_SALE", "AFTER_SALE", id, nil, map[string]any{"bizType": in.BizType, "bizOrderId": in.BizOrderID, "status": statusReview}, "SUCCESS", op); err != nil {
		return err
	}
	*out = map[string]any{"afterSaleId": fmt.Sprint(id), "afterSaleNo": no, "status": statusReview, "applyAmount": in.Amount, "applyQuantity": in.Quantity}
	return nil
}

func activeTicket(tx *gorm.DB, biz string, orderID, itemID uint64) (bool, error) {
	q := tx.Table("after_sale_ticket").Where("biz_type=? AND biz_order_id=? AND status IN ?", biz, orderID, []string{statusReview, statusReturning, statusReceipt, "REFUND_PROCESSING"})
	if itemID > 0 {
		q = q.Where("product_order_item_id=?", itemID)
	}
	var c int64
	err := q.Count(&c).Error
	return c > 0, err
}
func validateEvidence(tx *gorm.DB, files []string) string {
	seen := map[uint64]bool{}
	for _, raw := range files {
		id, ok := parseID(raw)
		if !ok || seen[id] {
			return "VALIDATION_FORMAT_INVALID"
		}
		seen[id] = true
		var c int64
		if err := tx.Table("file_object").Where("id=? AND status='ACTIVE' AND upload_status='SUCCESS'", id).Count(&c).Error; err != nil || c != 1 {
			return "AFTER_SALE_EVIDENCE_FILE_NOT_FOUND"
		}
	}
	return ""
}
func allowsType(raw []byte, want string) bool {
	if len(raw) == 0 {
		return false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	return findTypes(v, want)
}
func findTypes(v any, want string) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "afterSaleTypes" {
				if arr, ok := val.([]any); ok {
					for _, a := range arr {
						if fmt.Sprint(a) == want {
							return true
						}
					}
				}
			}
			if findTypes(val, want) {
				return true
			}
		}
	case []any:
		for _, a := range x {
			if findTypes(a, want) {
				return true
			}
		}
	case string:
		// 旧售后模板会把 conditions 作为 JSON 字符串保存，继续解析以兼容该结构。
		trimmed := strings.TrimSpace(x)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var nested any
			if json.Unmarshal([]byte(trimmed), &nested) == nil {
				return findTypes(nested, want)
			}
		}
	}
	return false
}
func blank(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return strings.TrimSpace(v)
}

func (s *Service) List(ctx context.Context, current identity.CurrentConsumer, status string) (map[string]any, error) {
	var rows []ticketRow
	q := s.db.WithContext(ctx).Table("after_sale_ticket").Where("consumer_id=?", current.ConsumerID)
	if strings.TrimSpace(status) != "" {
		q = q.Where("status=?", strings.TrimSpace(status))
	}
	if err := q.Order("created_at DESC,id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.ticketData(ctx, row, false))
	}
	return map[string]any{"items": items, "total": len(items)}, nil
}
func (s *Service) Get(ctx context.Context, current identity.CurrentConsumer, id uint64) (map[string]any, string, error) {
	var row ticketRow
	if err := s.db.WithContext(ctx).Table("after_sale_ticket").Where("id=? AND consumer_id=?", id, current.ConsumerID).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, "AFTER_SALE_NOT_FOUND", nil
		}
		return nil, "", err
	}
	return s.ticketData(ctx, row, true), "", nil
}
func (s *Service) ticketData(ctx context.Context, row ticketRow, detail bool) map[string]any {
	out := map[string]any{"afterSaleId": fmt.Sprint(row.ID), "afterSaleNo": row.AfterSaleNo, "bizType": row.BizType, "bizOrderId": fmt.Sprint(row.BizOrderID), "status": row.Status, "version": row.Version, "afterSaleType": row.AfterSaleType, "applyReasonCode": row.ApplyReasonCode, "applyReasonText": row.ApplyReasonText, "applyQuantity": row.ApplyQuantity, "applyAmount": row.ApplyAmount, "createdAt": row.CreatedAt}
	if row.ProductOrderItemID != nil {
		out["productOrderItemId"] = fmt.Sprint(*row.ProductOrderItemID)
	}
	if detail {
		var evidence []map[string]any
		s.db.WithContext(ctx).Table("after_sale_evidence e").Joins("JOIN file_object f ON f.id=e.file_id").Select("CAST(e.file_id AS CHAR) AS file_id,e.evidence_type,f.original_name,f.mime_type,f.file_size").Where("e.after_sale_id=?", row.ID).Find(&evidence)
		out["evidences"] = evidence
		if row.Status == statusReturning || row.Status == statusReceipt {
			var address any
			_ = json.Unmarshal(row.ReturnAddress, &address)
			out["returnAddress"] = address
		}
	}
	return out
}

func (s *Service) Withdraw(ctx context.Context, current identity.CurrentConsumer, id uint64, version uint, clientIP string) (map[string]any, string, error) {
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t ticketRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("after_sale_ticket").Where("id=? AND consumer_id=?", id, current.ConsumerID).Take(&t).Error; err != nil {
			code = "AFTER_SALE_NOT_FOUND"
			return nil
		}
		if t.Version != version {
			code = "VERSION_CONFLICT"
			return nil
		}
		if t.Status != statusReview {
			code = "AFTER_SALE_NOT_WITHDRAWABLE"
			return nil
		}
		now := time.Now()
		if err := tx.Table("after_sale_ticket").Where("id=? AND version=?", id, version).Updates(map[string]any{"status": statusWithdrawn, "version": version + 1, "updated_by": current.ConsumerID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.ConsumerID)}).Error; err != nil {
			return err
		}
		if t.BizType == bizProduct {
			if err := tx.Table("product_order").Where("id=? AND status='AFTER_SALE'", t.BizOrderID).Updates(map[string]any{"status": t.OriginalBizStatus, "after_sale_status": nil, "version": gorm.Expr("version+1"), "updated_by": current.ConsumerID, "status_changed_at": now}).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Table("activity_booking").Where("id=? AND status='REFUNDING'", t.BizOrderID).Updates(map[string]any{"status": t.OriginalBizStatus, "version": gorm.Expr("version+1"), "updated_by": current.ConsumerID, "status_changed_at": now}).Error; err != nil {
				return err
			}
		}
		op := audit.Operator{Type: "USER", ID: current.ConsumerID, RoleCode: "CONSUMER", ClientIP: clientIP}
		if err := audit.WriteStatusHistory(tx, "AFTER_SALE", id, statusReview, statusWithdrawn, "消费者撤回售后", op); err != nil {
			return err
		}
		out = map[string]any{"afterSaleId": fmt.Sprint(id), "status": statusWithdrawn}
		return nil
	})
	return out, code, err
}

func (s *Service) SubmitReturnLogistics(ctx context.Context, current identity.CurrentConsumer, id uint64, in ReturnLogisticsInput, version uint, clientIP string) (map[string]any, string, error) {
	if strings.TrimSpace(in.CarrierCode) == "" || strings.TrimSpace(in.TrackingNo) == "" {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	shipped := time.Now()
	if strings.TrimSpace(in.ShippedAt) != "" {
		v, e := time.Parse(time.RFC3339, strings.TrimSpace(in.ShippedAt))
		if e != nil {
			return nil, "VALIDATION_FORMAT_INVALID", nil
		}
		shipped = v
	}
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t ticketRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("after_sale_ticket").Where("id=? AND consumer_id=?", id, current.ConsumerID).Take(&t).Error; err != nil {
			code = "AFTER_SALE_NOT_FOUND"
			return nil
		}
		if t.Version != version {
			code = "VERSION_CONFLICT"
			return nil
		}
		if t.Status != statusReturning {
			code = "RETURN_LOGISTICS_NOT_ALLOWED"
			return nil
		}
		var count int64
		if err := tx.Table("return_logistics").Where("tracking_no=?", strings.TrimSpace(in.TrackingNo)).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			code = "RETURN_LOGISTICS_TRACKING_CONFLICT"
			return nil
		}
		now := time.Now()
		if err := tx.Table("return_logistics").Create(map[string]any{"id": idgen.Next(), "status": "SHIPPED", "version": 1, "created_by": current.ConsumerID, "updated_by": current.ConsumerID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.ConsumerID), "after_sale_id": id, "carrier_code": strings.TrimSpace(in.CarrierCode), "tracking_no": strings.TrimSpace(in.TrackingNo), "shipped_at": shipped}).Error; err != nil {
			return err
		}
		if err := tx.Table("after_sale_ticket").Where("id=? AND version=?", id, version).Updates(map[string]any{"status": statusReceipt, "version": version + 1, "updated_by": current.ConsumerID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.ConsumerID)}).Error; err != nil {
			return err
		}
		if t.BizType == bizProduct {
			if err := tx.Table("product_order").Where("id=? AND status=?", t.BizOrderID, "AFTER_SALE").Updates(map[string]any{"after_sale_status": statusReceipt, "updated_by": current.ConsumerID, "version": gorm.Expr("version + 1")}).Error; err != nil {
				return err
			}
		}
		op := audit.Operator{Type: "USER", ID: current.ConsumerID, RoleCode: "CONSUMER", ClientIP: clientIP}
		if err := audit.WriteStatusHistory(tx, "AFTER_SALE", id, statusReturning, statusReceipt, "消费者提交退货物流", op); err != nil {
			return err
		}
		out = map[string]any{"afterSaleId": fmt.Sprint(id), "status": statusReceipt, "trackingNo": strings.TrimSpace(in.TrackingNo)}
		return nil
	})
	return out, code, err
}
