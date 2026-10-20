// Package productorders implements consumer H5 product orders.  Cart state deliberately
// remains in the browser; this package only receives selected SKU lines at preview/create.
package productorders

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/idgen"
)

const (
	statusPending       = "PENDING_PAYMENT"
	statusShipment      = "PENDING_SHIPMENT"
	statusClosed        = "CLOSED"
	reservationReserved = "RESERVED"
	reservationConsumed = "CONSUMED"
	reservationReleased = "RELEASED"
)

type Service struct{ db *gorm.DB }

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

type AddressInput struct {
	RecipientName string `json:"recipientName" binding:"required,max=100"`
	Mobile        string `json:"mobile" binding:"required,max=32"`
	ProvinceCode  string `json:"provinceCode" binding:"required,max=32"`
	CityCode      string `json:"cityCode" binding:"required,max=32"`
	DistrictCode  string `json:"districtCode" binding:"required,max=32"`
	DetailAddress string `json:"detailAddress" binding:"required,max=500"`
	PostalCode    string `json:"postalCode" binding:"omitempty,max=32"`
	IsDefault     bool   `json:"isDefault"`
}
type LineInput struct {
	SKUID    string `json:"skuId" binding:"required"`
	Quantity int    `json:"quantity" binding:"required,min=1,max=99"`
}
type OrderInput struct {
	AddressID string      `json:"addressId" binding:"required"`
	Items     []LineInput `json:"items" binding:"required,min=1,max=99"`
}
type MockPayInput struct {
	Outcome string `json:"outcome" binding:"required,oneof=SUCCESS FAILURE"`
}

type addressRow struct {
	ID         uint64
	ConsumerID uint64 `gorm:"column:consumer_id"`
	Recipient  []byte `gorm:"column:recipient_name_cipher"`
	Mobile     []byte `gorm:"column:mobile_cipher"`
	Province   string `gorm:"column:province_code"`
	City       string `gorm:"column:city_code"`
	District   string `gorm:"column:district_code"`
	Detail     []byte `gorm:"column:detail_address_cipher"`
	Postal     string `gorm:"column:postal_code"`
	IsDefault  bool   `gorm:"column:is_default"`
	Version    uint   `gorm:"column:version"`
}
type orderRow struct {
	ID                uint64
	OrderNo           string `gorm:"column:order_no"`
	ConsumerID        uint64 `gorm:"column:consumer_id"`
	Status            string
	Version           uint
	OrderAmount       string    `gorm:"column:order_amount"`
	FreightAmount     string    `gorm:"column:freight_amount"`
	PayAmount         string    `gorm:"column:pay_amount"`
	PaymentExpireAt   time.Time `gorm:"column:payment_expire_at"`
	RecipientSnapshot []byte    `gorm:"column:recipient_snapshot"`
	FulfillmentStatus string    `gorm:"column:fulfillment_status"`
	AfterSaleStatus   string    `gorm:"column:after_sale_status"`
	CreatedAt         time.Time `gorm:"column:created_at"`
}
type purchasableLine struct {
	ProductID        uint64 `gorm:"column:product_id"`
	ProductVersionID uint64 `gorm:"column:product_version_id"`
	SKUId            uint64 `gorm:"column:sku_id"`
	ProductName      string `gorm:"column:product_name"`
	SKUName          string `gorm:"column:sku_name"`
	SKUCode          string `gorm:"column:sku_code"`
	SalePrice        string `gorm:"column:sale_price"`
	StockTotal       int    `gorm:"column:stock_total"`
	StockLocked      int    `gorm:"column:stock_locked"`
	StockSold        int    `gorm:"column:stock_sold"`
	FreightMode      string `gorm:"column:freight_mode"`
	FreightAmount    string `gorm:"column:freight_amount"`
	RuleSnapshot     []byte `gorm:"column:rule_snapshot"`
}

func parseID(value string) (uint64, bool) {
	n, e := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	return n, e == nil && n > 0
}
func price(v string) (int64, error) {
	f, e := strconv.ParseFloat(v, 64)
	if e != nil || f < 0 {
		return 0, fmt.Errorf("invalid amount")
	}
	return int64(f*100 + .5), nil
}
func money(v int64) string { return fmt.Sprintf("%d.%02d", v/100, v%100) }

func (s *Service) ListAddresses(ctx context.Context, current identity.CurrentConsumer) ([]map[string]any, error) {
	var rows []addressRow
	if err := s.db.WithContext(ctx).Table("consumer_address").Where("consumer_id=? AND status='ACTIVE'", current.ConsumerID).Order("is_default DESC,id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, addressData(r))
	}
	return out, nil
}
func addressData(r addressRow) map[string]any {
	return map[string]any{"addressId": fmt.Sprint(r.ID), "recipientName": string(r.Recipient), "mobile": string(r.Mobile), "provinceCode": r.Province, "cityCode": r.City, "districtCode": r.District, "detailAddress": string(r.Detail), "postalCode": r.Postal, "isDefault": r.IsDefault, "version": r.Version}
}
func (s *Service) AddAddress(ctx context.Context, current identity.CurrentConsumer, in AddressInput) (map[string]any, string, error) {
	var out addressRow
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if e := tx.Table("consumer_address").Where("consumer_id=? AND status='ACTIVE'", current.ConsumerID).Count(&count).Error; e != nil {
			return e
		}
		if count >= 10 {
			code = "ADDRESS_LIMIT_EXCEEDED"
			return nil
		}
		id := idgen.Next()
		first := count == 0
		if in.IsDefault || first {
			if e := tx.Table("consumer_address").Where("consumer_id=? AND status='ACTIVE'", current.ConsumerID).Updates(map[string]any{"is_default": false}).Error; e != nil {
				return e
			}
		}
		now := time.Now()
		out = addressRow{ID: id, ConsumerID: current.ConsumerID, Recipient: []byte(strings.TrimSpace(in.RecipientName)), Mobile: []byte(strings.TrimSpace(in.Mobile)), Province: strings.TrimSpace(in.ProvinceCode), City: strings.TrimSpace(in.CityCode), District: strings.TrimSpace(in.DistrictCode), Detail: []byte(strings.TrimSpace(in.DetailAddress)), Postal: strings.TrimSpace(in.PostalCode), IsDefault: in.IsDefault || first, Version: 1}
		return tx.Table("consumer_address").Create(map[string]any{"id": id, "status": "ACTIVE", "version": 1, "consumer_id": current.ConsumerID, "recipient_name_cipher": out.Recipient, "mobile_cipher": out.Mobile, "province_code": out.Province, "city_code": out.City, "district_code": out.District, "detail_address_cipher": out.Detail, "postal_code": out.Postal, "is_default": out.IsDefault, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.ConsumerID)}).Error
	})
	return addressData(out), code, err
}
func (s *Service) UpdateAddress(ctx context.Context, current identity.CurrentConsumer, id uint64, in AddressInput, version uint) (map[string]any, string, error) {
	var out addressRow
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row addressRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("consumer_address").Where("id=? AND consumer_id=? AND status='ACTIVE'", id, current.ConsumerID).Take(&row).Error; e != nil {
			if e == gorm.ErrRecordNotFound {
				code = "ADDRESS_NOT_FOUND"
				return nil
			}
			return e
		}
		if version > 0 && row.Version != version {
			code = "VERSION_CONFLICT"
			return nil
		}
		if in.IsDefault {
			if e := tx.Table("consumer_address").Where("consumer_id=? AND status='ACTIVE'", current.ConsumerID).Updates(map[string]any{"is_default": false}).Error; e != nil {
				return e
			}
		}
		values := map[string]any{"recipient_name_cipher": []byte(strings.TrimSpace(in.RecipientName)), "mobile_cipher": []byte(strings.TrimSpace(in.Mobile)), "province_code": strings.TrimSpace(in.ProvinceCode), "city_code": strings.TrimSpace(in.CityCode), "district_code": strings.TrimSpace(in.DistrictCode), "detail_address_cipher": []byte(strings.TrimSpace(in.DetailAddress)), "postal_code": strings.TrimSpace(in.PostalCode), "is_default": in.IsDefault, "version": gorm.Expr("version+1")}
		if e := tx.Table("consumer_address").Where("id=?", id).Updates(values).Error; e != nil {
			return e
		}
		out = row
		out.Recipient = values["recipient_name_cipher"].([]byte)
		out.Mobile = values["mobile_cipher"].([]byte)
		out.Province = values["province_code"].(string)
		out.City = values["city_code"].(string)
		out.District = values["district_code"].(string)
		out.Detail = values["detail_address_cipher"].([]byte)
		out.Postal = values["postal_code"].(string)
		out.IsDefault = in.IsDefault
		out.Version++
		return nil
	})
	return addressData(out), code, err
}
func (s *Service) DeleteAddress(ctx context.Context, current identity.CurrentConsumer, id uint64, version uint) (string, error) {
	q := s.db.WithContext(ctx).Table("consumer_address").Where("id=? AND consumer_id=? AND status='ACTIVE'", id, current.ConsumerID)
	if version > 0 {
		q = q.Where("version=?", version)
	}
	r := q.Updates(map[string]any{"status": "DELETED", "version": gorm.Expr("version+1")})
	if r.Error != nil {
		return "", r.Error
	}
	if r.RowsAffected == 0 {
		return "ADDRESS_NOT_FOUND", nil
	}
	return "", nil
}
func (s *Service) SetDefaultAddress(ctx context.Context, current identity.CurrentConsumer, id uint64, version uint) (map[string]any, string, error) {
	var row addressRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("consumer_address").Where("id=? AND consumer_id=? AND status='ACTIVE'", id, current.ConsumerID).Take(&row).Error; e != nil {
			return e
		}
		if version > 0 && row.Version != version {
			return fmt.Errorf("version")
		}
		if e := tx.Table("consumer_address").Where("consumer_id=? AND status='ACTIVE'", current.ConsumerID).Updates(map[string]any{"is_default": false}).Error; e != nil {
			return e
		}
		return tx.Table("consumer_address").Where("id=?", id).Updates(map[string]any{"is_default": true, "version": gorm.Expr("version+1")}).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, "ADDRESS_NOT_FOUND", nil
		}
		if err.Error() == "version" {
			return nil, "VERSION_CONFLICT", nil
		}
		return nil, "", err
	}
	row.IsDefault = true
	row.Version++
	return addressData(row), "", nil
}

func normalize(in []LineInput) (map[uint64]int, string) {
	out := map[uint64]int{}
	for _, v := range in {
		id, ok := parseID(v.SKUID)
		if !ok || v.Quantity < 1 || v.Quantity > 99 {
			return nil, "VALIDATION_FORMAT_INVALID"
		}
		out[id] += v.Quantity
		if out[id] > 99 {
			return nil, "SKU_QUANTITY_EXCEEDED"
		}
	}
	if len(out) == 0 {
		return nil, "VALIDATION_FORMAT_INVALID"
	}
	return out, ""
}
func (s *Service) loadLines(tx *gorm.DB, lines map[uint64]int) ([]purchasableLine, string, error) {
	ids := make([]uint64, 0, len(lines))
	for id := range lines {
		ids = append(ids, id)
	}
	var rows []purchasableLine
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("product_sku sk").Joins("JOIN product_version v ON v.id=sk.product_version_id AND v.status='PUBLISHED'").Joins("JOIN product p ON p.current_version_id=v.id AND p.status='PUBLISHED'").Where("sk.id IN ? AND sk.status='ACTIVE' AND sk.sku_status='ON_SALE'", ids).Select("p.id product_id,v.id product_version_id,sk.id sku_id,v.product_name,sk.sku_name,sk.sku_code,sk.sale_price,sk.stock_total,sk.stock_locked,sk.stock_sold,v.freight_mode,v.freight_amount,v.rule_snapshot").Find(&rows).Error
	if err != nil {
		return nil, "", err
	}
	if len(rows) != len(ids) {
		return nil, "PRODUCT_OR_SKU_NOT_PURCHASABLE", nil
	}
	for _, r := range rows {
		if r.StockTotal-r.StockLocked-r.StockSold < lines[r.SKUId] {
			return nil, "SKU_STOCK_INSUFFICIENT", nil
		}
		if r.FreightMode != "FREE" && r.FreightMode != "FIXED" {
			return nil, "PRODUCT_OR_SKU_NOT_PURCHASABLE", nil
		}
	}
	return rows, "", nil
}
func addressSnapshot(row addressRow) map[string]any {
	return map[string]any{"recipientName": string(row.Recipient), "mobile": string(row.Mobile), "provinceCode": row.Province, "cityCode": row.City, "districtCode": row.District, "detailAddress": string(row.Detail), "postalCode": row.Postal}
}
func calculate(rows []purchasableLine, quantities map[uint64]int) (int64, int64, []map[string]any, error) {
	subtotal, freight := int64(0), int64(0)
	seen := map[uint64]bool{}
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		p, e := price(r.SalePrice)
		if e != nil {
			return 0, 0, nil, e
		}
		q := quantities[r.SKUId]
		subtotal += p * int64(q)
		f := int64(0)
		if !seen[r.ProductID] {
			seen[r.ProductID] = true
			if r.FreightMode == "FIXED" {
				f, e = price(r.FreightAmount)
				if e != nil {
					return 0, 0, nil, e
				}
				freight += f
			}
		}
		var rule any
		_ = json.Unmarshal(r.RuleSnapshot, &rule)
		items = append(items, map[string]any{"productId": fmt.Sprint(r.ProductID), "productVersionId": fmt.Sprint(r.ProductVersionID), "productName": r.ProductName, "skuId": fmt.Sprint(r.SKUId), "skuCode": r.SKUCode, "skuName": r.SKUName, "quantity": q, "unitPrice": money(p), "itemAmount": money(p * int64(q)), "freightMode": r.FreightMode, "freightAmount": money(f), "afterSaleRule": rule, "fulfillmentMode": "PLATFORM_SHIP"})
	}
	return subtotal, freight, items, nil
}
func (s *Service) Preview(ctx context.Context, current identity.CurrentConsumer, in OrderInput) (map[string]any, string, error) {
	qs, code := normalize(in.Items)
	if code != "" {
		return nil, code, nil
	}
	aid, ok := parseID(in.AddressID)
	if !ok {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var out map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var address addressRow
		if e := tx.Table("consumer_address").Where("id=? AND consumer_id=? AND status='ACTIVE'", aid, current.ConsumerID).Take(&address).Error; e != nil {
			code = "ADDRESS_NOT_FOUND"
			return nil
		}
		rows, c, e := s.loadLines(tx, qs)
		if e != nil {
			return e
		}
		if c != "" {
			code = c
			return nil
		}
		sub, freight, items, e := calculate(rows, qs)
		if e != nil {
			return e
		}
		out = map[string]any{"items": items, "orderAmount": money(sub), "freightAmount": money(freight), "payAmount": money(sub + freight), "address": addressSnapshot(address), "previewOnly": true}
		return nil
	})
	return out, code, err
}
func (s *Service) Create(ctx context.Context, current identity.CurrentConsumer, in OrderInput, clientIP string) (map[string]any, string, error) {
	qs, code := normalize(in.Items)
	if code != "" {
		return nil, code, nil
	}
	aid, ok := parseID(in.AddressID)
	if !ok {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var out map[string]any
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var address addressRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("consumer_address").Where("id=? AND consumer_id=? AND status='ACTIVE'", aid, current.ConsumerID).Take(&address).Error; e != nil {
			code = "ADDRESS_NOT_FOUND"
			return nil
		}
		rows, c, e := s.loadLines(tx, qs)
		if e != nil {
			return e
		}
		if c != "" {
			code = c
			return nil
		}
		sub, freight, items, e := calculate(rows, qs)
		if e != nil {
			return e
		}
		now := time.Now()
		expiry := now.Add(30 * time.Minute)
		orderID := idgen.Next()
		for _, r := range rows {
			q := qs[r.SKUId]
			u := tx.Table("product_sku").Where("id=? AND status='ACTIVE' AND sku_status='ON_SALE' AND stock_total-stock_locked-stock_sold>=?", r.SKUId, q).Update("stock_locked", gorm.Expr("stock_locked+?", q))
			if u.Error != nil {
				return u.Error
			}
			if u.RowsAffected != 1 {
				code = "SKU_STOCK_INSUFFICIENT"
				return nil
			}
		}
		snap, _ := json.Marshal(addressSnapshot(address))
		if e = tx.Table("product_order").Create(map[string]any{"id": orderID, "status": statusPending, "version": 1, "created_by": current.ConsumerID, "updated_by": current.ConsumerID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.ConsumerID), "order_no": fmt.Sprintf("ORD-%d", orderID), "consumer_id": current.ConsumerID, "order_amount": money(sub), "freight_amount": money(freight), "pay_amount": money(sub + freight), "payment_expire_at": expiry, "recipient_snapshot": snap, "fulfillment_status": "PENDING_PAYMENT"}).Error; e != nil {
			return e
		}
		for idx, item := range items {
			itemID := idgen.Next()
			raw, _ := json.Marshal(item)
			skuID, _ := parseID(fmt.Sprint(item["skuId"]))
			q := qs[skuID]
			if e = tx.Table("product_order_item").Create(map[string]any{"id": itemID, "created_by": current.ConsumerID, "order_id": orderID, "line_no": idx + 1, "product_id": item["productId"], "product_version_id": item["productVersionId"], "sku_id": skuID, "quantity": q, "unit_price": item["unitPrice"], "item_amount": item["itemAmount"], "item_snapshot": raw}).Error; e != nil {
				return e
			}
			if e = tx.Table("product_stock_reservation").Create(map[string]any{"id": idgen.Next(), "status": reservationReserved, "version": 1, "created_by": current.ConsumerID, "reservation_no": fmt.Sprintf("PSR-%d", idgen.Next()), "sku_id": skuID, "order_item_id": itemID, "quantity": q, "expires_at": expiry, "reservation_status": reservationReserved}).Error; e != nil {
				return e
			}
		}
		op := audit.Operator{Type: "USER", ID: current.ConsumerID, RoleCode: "CONSUMER", ClientIP: clientIP}
		if e = audit.WriteStatusHistory(tx, "PRODUCT_ORDER", orderID, "", statusPending, "创建待支付商品订单", op); e != nil {
			return e
		}
		if e = audit.WriteLog(tx, "CREATE_PRODUCT_ORDER", "PRODUCT_ORDER", orderID, nil, map[string]any{"status": statusPending, "itemCount": len(items)}, "SUCCESS", op); e != nil {
			return e
		}
		out = map[string]any{"productOrderId": fmt.Sprint(orderID), "orderNo": fmt.Sprintf("ORD-%d", orderID), "status": statusPending, "paymentExpireAt": expiry, "orderAmount": money(sub), "freightAmount": money(freight), "payAmount": money(sub + freight)}
		return nil
	})
	return out, code, err
}
func (s *Service) release(tx *gorm.DB, orderID uint64, reason string, consumerID uint64) error {
	var res []struct {
		ID       uint64
		SKUID    uint64 `gorm:"column:sku_id"`
		Quantity int
	}
	if e := tx.Table("product_stock_reservation").Where("order_item_id IN (SELECT id FROM product_order_item WHERE order_id=?) AND reservation_status=?", orderID, reservationReserved).Find(&res).Error; e != nil {
		return e
	}
	for _, r := range res {
		if e := tx.Table("product_sku").Where("id=? AND stock_locked>=?", r.SKUID, r.Quantity).Update("stock_locked", gorm.Expr("stock_locked-?", r.Quantity)).Error; e != nil {
			return e
		}
		if e := tx.Table("product_stock_reservation").Where("id=? AND reservation_status=?", r.ID, reservationReserved).Updates(map[string]any{"reservation_status": reservationReleased, "status": reservationReleased, "status_reason_code": reason, "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) Cancel(ctx context.Context, current identity.CurrentConsumer, id uint64, clientIP string) (map[string]any, string, error) {
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var o orderRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("product_order").Where("id=? AND consumer_id=?", id, current.ConsumerID).Take(&o).Error; e != nil {
			code = "PRODUCT_ORDER_NOT_FOUND"
			return nil
		}
		if o.Status != statusPending {
			code = "STATE_TRANSITION_NOT_ALLOWED"
			out = map[string]any{"currentStatus": o.Status}
			return nil
		}
		if e := s.release(tx, id, "CANCELLED", current.ConsumerID); e != nil {
			return e
		}
		now := time.Now()
		if e := tx.Table("product_order").Where("id=? AND status=?", id, statusPending).Updates(map[string]any{"status": statusClosed, "status_reason_code": "CANCELLED", "status_reason": "消费者取消", "fulfillment_status": "CANCELLED", "version": gorm.Expr("version+1"), "updated_by": current.ConsumerID, "status_changed_at": now}).Error; e != nil {
			return e
		}
		op := audit.Operator{Type: "USER", ID: current.ConsumerID, RoleCode: "CONSUMER", ClientIP: clientIP}
		if e := audit.WriteStatusHistory(tx, "PRODUCT_ORDER", id, statusPending, statusClosed, "消费者取消", op); e != nil {
			return e
		}
		out = map[string]any{"productOrderId": fmt.Sprint(id), "status": statusClosed, "reasonCode": "CANCELLED"}
		return nil
	})
	return out, code, err
}
func (s *Service) MockPay(ctx context.Context, current identity.CurrentConsumer, id uint64, in MockPayInput, clientIP string) (map[string]any, string, error) {
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var o orderRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("product_order").Where("id=? AND consumer_id=?", id, current.ConsumerID).Take(&o).Error; e != nil {
			code = "PRODUCT_ORDER_NOT_FOUND"
			return nil
		}
		if o.Status == statusShipment {
			out = map[string]any{"productOrderId": fmt.Sprint(id), "paymentStatus": "SUCCESS", "status": statusShipment}
			return nil
		}
		if o.Status != statusPending || !o.PaymentExpireAt.After(time.Now()) {
			code = "PRODUCT_ORDER_NOT_PAYABLE"
			return nil
		}
		now := time.Now()
		paymentNo := fmt.Sprintf("MOCKPAY-%d", idgen.Next())
		if in.Outcome == "FAILURE" {
			if e := tx.Table("payment_transaction").Create(map[string]any{"id": idgen.Next(), "status": "FAILED", "version": 1, "payment_no": paymentNo, "biz_type": "PRODUCT_ORDER", "biz_order_id": id, "idempotency_key": paymentNo, "payment_method": "MOCK", "amount": o.PayAmount, "payment_status": "FAILED", "status_reason_code": "MOCK_FAILURE"}).Error; e != nil {
				return e
			}
			out = map[string]any{"productOrderId": fmt.Sprint(id), "paymentStatus": "FAILED", "status": statusPending}
			return nil
		}
		if e := tx.Table("payment_transaction").Create(map[string]any{"id": idgen.Next(), "status": "SUCCESS", "version": 1, "payment_no": paymentNo, "biz_type": "PRODUCT_ORDER", "biz_order_id": id, "idempotency_key": paymentNo, "payment_method": "MOCK", "amount": o.PayAmount, "payment_status": "SUCCESS", "provider_transaction_no": paymentNo, "paid_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Table("product_stock_reservation").Where("order_item_id IN (SELECT id FROM product_order_item WHERE order_id=?) AND reservation_status=?", id, reservationReserved).Updates(map[string]any{"reservation_status": reservationConsumed, "status": reservationConsumed, "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
		var items []struct {
			SKUID    uint64 `gorm:"column:sku_id"`
			Quantity int
		}
		if e := tx.Table("product_order_item").Where("order_id=?", id).Find(&items).Error; e != nil {
			return e
		}
		for _, item := range items {
			if e := tx.Table("product_sku").Where("id=? AND stock_locked>=?", item.SKUID, item.Quantity).Updates(map[string]any{"stock_locked": gorm.Expr("stock_locked-?", item.Quantity), "stock_sold": gorm.Expr("stock_sold+?", item.Quantity)}).Error; e != nil {
				return e
			}
		}
		if e := tx.Table("product_order").Where("id=? AND status=?", id, statusPending).Updates(map[string]any{"status": statusShipment, "fulfillment_status": statusShipment, "version": gorm.Expr("version+1"), "updated_by": current.ConsumerID, "status_changed_at": now}).Error; e != nil {
			return e
		}
		op := audit.Operator{Type: "USER", ID: current.ConsumerID, RoleCode: "CONSUMER", ClientIP: clientIP}
		if e := audit.WriteStatusHistory(tx, "PRODUCT_ORDER", id, statusPending, statusShipment, "Mock 模拟支付成功", op); e != nil {
			return e
		}
		out = map[string]any{"productOrderId": fmt.Sprint(id), "paymentStatus": "SUCCESS", "status": statusShipment, "paymentNo": paymentNo}
		return nil
	})
	return out, code, err
}
func (s *Service) CloseExpired(ctx context.Context) (int, error) {
	count := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []orderRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("product_order").Where("status=? AND payment_expire_at<=?", statusPending, time.Now()).Find(&rows).Error; e != nil {
			return e
		}
		for _, o := range rows {
			if e := s.release(tx, o.ID, "PAYMENT_TIMEOUT", o.ConsumerID); e != nil {
				return e
			}
			if e := tx.Table("product_order").Where("id=? AND status=?", o.ID, statusPending).Updates(map[string]any{"status": statusClosed, "status_reason_code": "PAYMENT_TIMEOUT", "fulfillment_status": "CLOSED", "version": gorm.Expr("version+1")}).Error; e != nil {
				return e
			}
			count++
		}
		return nil
	})
	return count, err
}
func (s *Service) Get(ctx context.Context, current identity.CurrentConsumer, id uint64) (map[string]any, string, error) {
	var o orderRow
	if e := s.db.WithContext(ctx).Table("product_order").Where("id=? AND consumer_id=?", id, current.ConsumerID).Take(&o).Error; e != nil {
		if e == gorm.ErrRecordNotFound {
			return nil, "PRODUCT_ORDER_NOT_FOUND", nil
		}
		return nil, "", e
	}
	return s.orderData(ctx, o), "", nil
}
func (s *Service) List(ctx context.Context, current identity.CurrentConsumer) ([]map[string]any, error) {
	var rows []orderRow
	if e := s.db.WithContext(ctx).Table("product_order").Where("consumer_id=?", current.ConsumerID).Order("created_at DESC").Find(&rows).Error; e != nil {
		return nil, e
	}
	out := make([]map[string]any, 0, len(rows))
	for _, o := range rows {
		out = append(out, s.orderData(ctx, o))
	}
	return out, nil
}
func (s *Service) orderData(ctx context.Context, o orderRow) map[string]any {
	var items []map[string]any
	_ = s.db.WithContext(ctx).Table("product_order_item").Where("order_id=?", o.ID).Select("CAST(sku_id AS CHAR) skuId,quantity,unit_price unitPrice,item_amount itemAmount,item_snapshot itemSnapshot").Order("line_no").Find(&items)
	var address any
	_ = json.Unmarshal(o.RecipientSnapshot, &address)
	return map[string]any{"productOrderId": fmt.Sprint(o.ID), "orderNo": o.OrderNo, "status": o.Status, "version": o.Version, "orderAmount": o.OrderAmount, "freightAmount": o.FreightAmount, "payAmount": o.PayAmount, "paymentExpireAt": o.PaymentExpireAt, "fulfillmentStatus": o.FulfillmentStatus, "afterSaleStatus": o.AfterSaleStatus, "address": address, "items": items, "createdAt": o.CreatedAt}
}
