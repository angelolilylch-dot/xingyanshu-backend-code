// Package activitybookings implements consumer H5 activity booking. It deliberately
// stops at verification-code issuance; on-site verification belongs to API-FUL-02.
package activitybookings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	statusPendingPayment      = "PENDING_PAYMENT"
	statusPendingVerification = "PENDING_VERIFICATION"
	statusClosed              = "CLOSED"
	statusExpired             = "EXPIRED"
	reservationReserved       = "RESERVED"
	reservationConsumed       = "CONSUMED"
	reservationReleased       = "RELEASED"
)

type Service struct{ db *gorm.DB }

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

type Input struct {
	ActivityID   string `json:"activityId" binding:"required"`
	SessionID    string `json:"sessionId" binding:"required"`
	TicketTypeID string `json:"ticketTypeId" binding:"required"`
	Quantity     int    `json:"quantity" binding:"required,min=1,max=4"`
}
type MockPayInput struct {
	Outcome string `json:"outcome" binding:"required,oneof=SUCCESS FAILURE"`
}

type bookingRow struct {
	ID                 uint64
	BookingNo          string `gorm:"column:booking_no"`
	ConsumerID         uint64 `gorm:"column:consumer_id"`
	ActivityID         uint64 `gorm:"column:activity_id"`
	SessionID          uint64 `gorm:"column:session_id"`
	TicketTypeID       uint64 `gorm:"column:ticket_type_id"`
	Quantity           int
	Amount             string
	Status             string
	Version            uint
	PaymentExpireAt    *time.Time `gorm:"column:payment_expire_at"`
	RuleSnapshot       []byte     `gorm:"column:rule_snapshot"`
	VerificationStatus string     `gorm:"column:verification_status"`
	CreatedAt          time.Time  `gorm:"column:created_at"`
}
type purchasable struct {
	ActivityID     uint64    `gorm:"column:activity_id"`
	ActivityStatus string    `gorm:"column:activity_status"`
	ActivityName   string    `gorm:"column:activity_name"`
	City           string    `gorm:"column:actual_city_code"`
	SessionID      uint64    `gorm:"column:session_id"`
	SessionName    string    `gorm:"column:session_name"`
	SessionStatus  string    `gorm:"column:session_status"`
	StartAt        time.Time `gorm:"column:start_at"`
	EndAt          time.Time `gorm:"column:end_at"`
	SessionQuota   int       `gorm:"column:session_quota"`
	TicketID       uint64    `gorm:"column:ticket_id"`
	TicketName     string    `gorm:"column:ticket_name"`
	TicketCode     string    `gorm:"column:ticket_code"`
	TicketStatus   string    `gorm:"column:ticket_status"`
	ChargeType     string    `gorm:"column:charge_type"`
	Price          string
	TicketQuota    int    `gorm:"column:ticket_quota"`
	TicketLocked   int    `gorm:"column:quota_locked"`
	TicketUsed     int    `gorm:"column:quota_used"`
	RuleSnapshot   []byte `gorm:"column:rule_snapshot"`
}

func parseID(v string) (uint64, bool) {
	n, e := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	return n, e == nil && n > 0
}
func cents(v string) (int64, error) {
	var n float64
	_, e := fmt.Sscan(v, &n)
	if e != nil || n < 0 {
		return 0, fmt.Errorf("invalid amount")
	}
	return int64(n*100 + .5), nil
}
func money(v int64) string { return fmt.Sprintf("%d.%02d", v/100, v%100) }

func (s *Service) load(tx *gorm.DB, in Input, lock bool) (purchasable, string, error) {
	a, ok1 := parseID(in.ActivityID)
	se, ok2 := parseID(in.SessionID)
	ti, ok3 := parseID(in.TicketTypeID)
	if !ok1 || !ok2 || !ok3 || in.Quantity < 1 || in.Quantity > 4 {
		return purchasable{}, "VALIDATION_FORMAT_INVALID", nil
	}
	q := tx.Table("activity a").Joins("JOIN activity_version v ON v.id=a.current_version_id").Joins("JOIN activity_session s ON s.id=? AND s.activity_version_id=v.id", se).Joins("JOIN activity_ticket_type t ON t.id=? AND t.session_id=s.id", ti).Where("a.id=?", a)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row purchasable
	e := q.Select("a.id activity_id,a.status activity_status,a.actual_city_code,v.activity_name,v.rule_snapshot,s.id session_id,s.status session_status,s.session_name,s.start_at,s.end_at,s.quota_total session_quota,t.id ticket_id,t.status ticket_status,t.ticket_name,t.ticket_code,t.charge_type,t.price,t.quota_total ticket_quota,t.quota_locked,t.quota_used").Take(&row).Error
	if e == gorm.ErrRecordNotFound {
		return row, "ACTIVITY_TICKET_NOT_BOOKABLE", nil
	}
	if e != nil {
		return row, "", e
	}
	if row.ActivityStatus != "PUBLISHED" || row.SessionStatus != "PUBLISHED" || row.TicketStatus != "PUBLISHED" || !row.EndAt.After(time.Now()) {
		return row, "ACTIVITY_TICKET_NOT_BOOKABLE", nil
	}
	if row.ChargeType != "FREE" && row.ChargeType != "PAID" {
		return row, "ACTIVITY_TICKET_NOT_BOOKABLE", nil
	}
	if row.ChargeType == "FREE" && row.Price != "0.00" && row.Price != "0" {
		return row, "ACTIVITY_TICKET_NOT_BOOKABLE", nil
	}
	return row, "", nil
}

func (s *Service) remainingSession(tx *gorm.DB, sessionID uint64) (int, error) {
	var used int
	if e := tx.Table("activity_booking").Where("session_id=? AND status IN ?", sessionID, []string{statusPendingPayment, statusPendingVerification}).Select("COALESCE(SUM(quantity),0)").Scan(&used).Error; e != nil {
		return 0, e
	}
	return used, nil
}
func bookingSnapshot(p purchasable) map[string]any {
	var rule any
	_ = json.Unmarshal(p.RuleSnapshot, &rule)
	return map[string]any{"activityId": fmt.Sprint(p.ActivityID), "activityName": p.ActivityName, "actualCityCode": p.City, "sessionId": fmt.Sprint(p.SessionID), "sessionName": p.SessionName, "sessionStartAt": p.StartAt, "sessionEndAt": p.EndAt, "ticketTypeId": fmt.Sprint(p.TicketID), "ticketCode": p.TicketCode, "ticketName": p.TicketName, "chargeType": p.ChargeType, "price": p.Price, "rule": rule}
}
func data(b bookingRow) map[string]any {
	var snap any
	_ = json.Unmarshal(b.RuleSnapshot, &snap)
	return map[string]any{"activityBookingId": fmt.Sprint(b.ID), "bookingNo": b.BookingNo, "status": b.Status, "version": b.Version, "activityId": fmt.Sprint(b.ActivityID), "sessionId": fmt.Sprint(b.SessionID), "ticketTypeId": fmt.Sprint(b.TicketTypeID), "quantity": b.Quantity, "amount": b.Amount, "paymentExpireAt": b.PaymentExpireAt, "verificationStatus": b.VerificationStatus, "snapshot": snap, "createdAt": b.CreatedAt}
}

func (s *Service) Preview(ctx context.Context, current identity.CurrentConsumer, in Input) (map[string]any, string, error) {
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		p, c, e := s.load(tx, in, false)
		if e != nil {
			return e
		}
		if c != "" {
			code = c
			return nil
		}
		sessionUsed, e := s.remainingSession(tx, p.SessionID)
		if e != nil {
			return e
		}
		if p.SessionQuota-sessionUsed < in.Quantity || p.TicketQuota-p.TicketLocked-p.TicketUsed < in.Quantity {
			code = "ACTIVITY_QUOTA_INSUFFICIENT"
			return nil
		}
		unit, e := cents(p.Price)
		if e != nil {
			return e
		}
		out = bookingSnapshot(p)
		out["quantity"] = in.Quantity
		out["amount"] = money(unit * int64(in.Quantity))
		out["previewOnly"] = true
		return nil
	})
	return out, code, err
}

func (s *Service) Create(ctx context.Context, current identity.CurrentConsumer, in Input, clientIP string) (map[string]any, string, error) {
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		p, c, e := s.load(tx, in, true)
		if e != nil {
			return e
		}
		if c != "" {
			code = c
			return nil
		}
		sessionUsed, e := s.remainingSession(tx, p.SessionID)
		if e != nil {
			return e
		}
		if p.SessionQuota-sessionUsed < in.Quantity || p.TicketQuota-p.TicketLocked-p.TicketUsed < in.Quantity {
			code = "ACTIVITY_QUOTA_INSUFFICIENT"
			return nil
		}
		unit, e := cents(p.Price)
		if e != nil {
			return e
		}
		amount := money(unit * int64(in.Quantity))
		now := time.Now()
		bid := idgen.Next()
		bno := fmt.Sprintf("BOOK-%d", bid)
		status := statusPendingVerification
		var expiry *time.Time
		if p.ChargeType == "PAID" {
			status = statusPendingPayment
			v := now.Add(30 * time.Minute)
			expiry = &v
			u := tx.Table("activity_ticket_type").Where("id=? AND quota_total-quota_locked-quota_used>=?", p.TicketID, in.Quantity).Update("quota_locked", gorm.Expr("quota_locked+?", in.Quantity))
			if u.Error != nil {
				return u.Error
			}
			if u.RowsAffected != 1 {
				code = "ACTIVITY_QUOTA_INSUFFICIENT"
				return nil
			}
		} else {
			u := tx.Table("activity_ticket_type").Where("id=? AND quota_total-quota_locked-quota_used>=?", p.TicketID, in.Quantity).Update("quota_used", gorm.Expr("quota_used+?", in.Quantity))
			if u.Error != nil {
				return u.Error
			}
			if u.RowsAffected != 1 {
				code = "ACTIVITY_QUOTA_INSUFFICIENT"
				return nil
			}
		}
		snap, _ := json.Marshal(bookingSnapshot(p))
		values := map[string]any{"id": bid, "status": status, "version": 1, "created_by": current.ConsumerID, "updated_by": current.ConsumerID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.ConsumerID), "booking_no": bno, "consumer_id": current.ConsumerID, "activity_id": p.ActivityID, "session_id": p.SessionID, "ticket_type_id": p.TicketID, "quantity": in.Quantity, "amount": amount, "payment_expire_at": expiry, "rule_snapshot": snap, "verification_status": "PENDING"}
		if e = tx.Table("activity_booking").Create(values).Error; e != nil {
			return e
		}
		if p.ChargeType == "PAID" {
			if e = tx.Table("activity_quota_reservation").Create(map[string]any{"id": idgen.Next(), "status": reservationReserved, "version": 1, "created_by": current.ConsumerID, "reservation_no": fmt.Sprintf("AQR-%d", idgen.Next()), "ticket_type_id": p.TicketID, "booking_id": bid, "quantity": in.Quantity, "expires_at": *expiry, "reservation_status": reservationReserved}).Error; e != nil {
				return e
			}
		} else if e = s.createCode(tx, bid, p.EndAt, current.ConsumerID); e != nil {
			return e
		}
		op := audit.Operator{Type: "USER", ID: current.ConsumerID, RoleCode: "CONSUMER", ClientIP: clientIP}
		if e = audit.WriteStatusHistory(tx, "ACTIVITY_BOOKING", bid, "", status, "创建活动预约", op); e != nil {
			return e
		}
		if e = audit.WriteLog(tx, "CREATE_ACTIVITY_BOOKING", "ACTIVITY_BOOKING", bid, nil, map[string]any{"status": status, "chargeType": p.ChargeType}, "SUCCESS", op); e != nil {
			return e
		}
		b := bookingRow{ID: bid, BookingNo: bno, ConsumerID: current.ConsumerID, ActivityID: p.ActivityID, SessionID: p.SessionID, TicketTypeID: p.TicketID, Quantity: in.Quantity, Amount: amount, Status: status, Version: 1, PaymentExpireAt: expiry, RuleSnapshot: snap, VerificationStatus: "PENDING", CreatedAt: now}
		out = data(b)
		return nil
	})
	return out, code, err
}
func (s *Service) createCode(tx *gorm.DB, bookingID uint64, expires time.Time, consumerID uint64) error {
	raw := fmt.Sprintf("VFY-%d", idgen.Next())
	h := sha256.Sum256([]byte(raw))
	return tx.Table("verification_code").Create(map[string]any{"id": idgen.Next(), "status": "ACTIVE", "version": 1, "created_by": consumerID, "booking_id": bookingID, "code_hash": hex.EncodeToString(h[:]), "display_code": raw, "expires_at": expires, "status_changed_at": time.Now(), "status_changed_by_type": "SYSTEM", "status_changed_by_id": "activity-booking"}).Error
}

func (s *Service) release(tx *gorm.DB, b bookingRow, reason string) error {
	var r struct {
		ID       uint64
		Quantity int
	}
	e := tx.Table("activity_quota_reservation").Where("booking_id=? AND reservation_status=?", b.ID, reservationReserved).Take(&r).Error
	if e == gorm.ErrRecordNotFound {
		return nil
	}
	if e != nil {
		return e
	}
	if e = tx.Table("activity_ticket_type").Where("id=? AND quota_locked>=?", b.TicketTypeID, r.Quantity).Update("quota_locked", gorm.Expr("quota_locked-?", r.Quantity)).Error; e != nil {
		return e
	}
	return tx.Table("activity_quota_reservation").Where("id=? AND reservation_status=?", r.ID, reservationReserved).Updates(map[string]any{"status": reservationReleased, "reservation_status": reservationReleased, "status_reason_code": reason, "version": gorm.Expr("version+1")}).Error
}
func (s *Service) CancelPendingPayment(ctx context.Context, current identity.CurrentConsumer, id uint64, clientIP string) (map[string]any, string, error) {
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var b bookingRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("activity_booking").Where("id=? AND consumer_id=?", id, current.ConsumerID).Take(&b).Error; e != nil {
			code = "ACTIVITY_BOOKING_NOT_FOUND"
			return nil
		}
		if b.Status != statusPendingPayment {
			code = "STATE_TRANSITION_NOT_ALLOWED"
			return nil
		}
		if e := s.release(tx, b, "CANCELLED"); e != nil {
			return e
		}
		now := time.Now()
		if e := tx.Table("activity_booking").Where("id=? AND status=?", id, statusPendingPayment).Updates(map[string]any{"status": statusClosed, "status_reason_code": "CANCELLED", "status_reason": "消费者取消待支付预约", "version": gorm.Expr("version+1"), "status_changed_at": now}).Error; e != nil {
			return e
		}
		op := audit.Operator{Type: "USER", ID: current.ConsumerID, RoleCode: "CONSUMER", ClientIP: clientIP}
		if e := audit.WriteStatusHistory(tx, "ACTIVITY_BOOKING", id, statusPendingPayment, statusClosed, "消费者取消待支付预约", op); e != nil {
			return e
		}
		out = map[string]any{"activityBookingId": fmt.Sprint(id), "status": statusClosed, "reasonCode": "CANCELLED"}
		return nil
	})
	return out, code, err
}

func (s *Service) MockPay(ctx context.Context, current identity.CurrentConsumer, id uint64, in MockPayInput, clientIP string) (map[string]any, string, error) {
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var b bookingRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("activity_booking").Where("id=? AND consumer_id=?", id, current.ConsumerID).Take(&b).Error; e != nil {
			code = "ACTIVITY_BOOKING_NOT_FOUND"
			return nil
		}
		if b.Status == statusPendingVerification {
			out = map[string]any{"activityBookingId": fmt.Sprint(id), "paymentStatus": "SUCCESS", "status": b.Status}
			return nil
		}
		if b.Status != statusPendingPayment || b.PaymentExpireAt == nil || !b.PaymentExpireAt.After(time.Now()) {
			code = "ACTIVITY_BOOKING_NOT_PAYABLE"
			return nil
		}
		now := time.Now()
		payNo := fmt.Sprintf("MOCKPAY-%d", idgen.Next())
		if in.Outcome == "FAILURE" {
			if e := tx.Table("payment_transaction").Create(map[string]any{"id": idgen.Next(), "status": "FAILED", "version": 1, "payment_no": payNo, "biz_type": "ACTIVITY_BOOKING", "biz_order_id": id, "idempotency_key": payNo, "payment_method": "MOCK", "amount": b.Amount, "payment_status": "FAILED", "status_reason_code": "MOCK_FAILURE"}).Error; e != nil {
				return e
			}
			out = map[string]any{"activityBookingId": fmt.Sprint(id), "paymentStatus": "FAILED", "status": statusPendingPayment}
			return nil
		}
		if e := tx.Table("payment_transaction").Create(map[string]any{"id": idgen.Next(), "status": "SUCCESS", "version": 1, "payment_no": payNo, "biz_type": "ACTIVITY_BOOKING", "biz_order_id": id, "idempotency_key": payNo, "payment_method": "MOCK", "amount": b.Amount, "payment_status": "SUCCESS", "provider_transaction_no": payNo, "paid_at": now}).Error; e != nil {
			return e
		}
		var r struct {
			ID       uint64
			Quantity int
		}
		if e := tx.Table("activity_quota_reservation").Where("booking_id=? AND reservation_status=?", id, reservationReserved).Take(&r).Error; e != nil {
			return e
		}
		if e := tx.Table("activity_ticket_type").Where("id=? AND quota_locked>=?", b.TicketTypeID, r.Quantity).Updates(map[string]any{"quota_locked": gorm.Expr("quota_locked-?", r.Quantity), "quota_used": gorm.Expr("quota_used+?", r.Quantity)}).Error; e != nil {
			return e
		}
		if e := tx.Table("activity_quota_reservation").Where("id=?", r.ID).Updates(map[string]any{"status": reservationConsumed, "reservation_status": reservationConsumed, "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
		var end time.Time
		if e := tx.Table("activity_session").Where("id=?", b.SessionID).Select("end_at").Take(&end).Error; e != nil {
			return e
		}
		if e := s.createCode(tx, id, end, current.ConsumerID); e != nil {
			return e
		}
		if e := tx.Table("activity_booking").Where("id=? AND status=?", id, statusPendingPayment).Updates(map[string]any{"status": statusPendingVerification, "payment_expire_at": nil, "version": gorm.Expr("version+1"), "status_changed_at": now}).Error; e != nil {
			return e
		}
		op := audit.Operator{Type: "USER", ID: current.ConsumerID, RoleCode: "CONSUMER", ClientIP: clientIP}
		if e := audit.WriteStatusHistory(tx, "ACTIVITY_BOOKING", id, statusPendingPayment, statusPendingVerification, "Mock 模拟支付成功", op); e != nil {
			return e
		}
		out = map[string]any{"activityBookingId": fmt.Sprint(id), "paymentStatus": "SUCCESS", "status": statusPendingVerification, "paymentNo": payNo}
		return nil
	})
	return out, code, err
}

func (s *Service) CloseExpired(ctx context.Context) (int, error) {
	count := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []bookingRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("activity_booking").Where("status=? AND payment_expire_at<=?", statusPendingPayment, time.Now()).Find(&rows).Error; e != nil {
			return e
		}
		for _, b := range rows {
			if e := s.release(tx, b, "PAYMENT_TIMEOUT"); e != nil {
				return e
			}
			if e := tx.Table("activity_booking").Where("id=? AND status=?", b.ID, statusPendingPayment).Updates(map[string]any{"status": statusClosed, "status_reason_code": "PAYMENT_TIMEOUT", "version": gorm.Expr("version+1")}).Error; e != nil {
				return e
			}
			count++
		}
		return nil
	})
	return count, err
}
func (s *Service) ExpireEnded(ctx context.Context) (int, error) {
	count := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []bookingRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("activity_booking b").Joins("JOIN activity_session s ON s.id=b.session_id").Where("b.status=? AND s.end_at<=?", statusPendingVerification, time.Now()).Find(&rows).Error; e != nil {
			return e
		}
		for _, b := range rows {
			if e := tx.Table("activity_booking").Where("id=? AND status=?", b.ID, statusPendingVerification).Updates(map[string]any{"status": statusExpired, "status_reason_code": "ACTIVITY_ENDED", "verification_status": "EXPIRED", "version": gorm.Expr("version+1")}).Error; e != nil {
				return e
			}
			if e := tx.Table("verification_code").Where("booking_id=? AND status='ACTIVE'", b.ID).Updates(map[string]any{"status": "EXPIRED", "status_reason_code": "ACTIVITY_ENDED", "version": gorm.Expr("version+1")}).Error; e != nil {
				return e
			}
			count++
		}
		return nil
	})
	return count, err
}
func (s *Service) Get(ctx context.Context, current identity.CurrentConsumer, id uint64) (map[string]any, string, error) {
	var b bookingRow
	if e := s.db.WithContext(ctx).Table("activity_booking").Where("id=? AND consumer_id=?", id, current.ConsumerID).Take(&b).Error; e != nil {
		if e == gorm.ErrRecordNotFound {
			return nil, "ACTIVITY_BOOKING_NOT_FOUND", nil
		}
		return nil, "", e
	}
	return data(b), "", nil
}
func (s *Service) List(ctx context.Context, current identity.CurrentConsumer) ([]map[string]any, error) {
	var rows []bookingRow
	if e := s.db.WithContext(ctx).Table("activity_booking").Where("consumer_id=?", current.ConsumerID).Order("created_at DESC").Find(&rows).Error; e != nil {
		return nil, e
	}
	out := make([]map[string]any, 0, len(rows))
	for _, b := range rows {
		out = append(out, data(b))
	}
	return out, nil
}
func (s *Service) VerificationCode(ctx context.Context, current identity.CurrentConsumer, id uint64) (map[string]any, string, error) {
	var b bookingRow
	if e := s.db.WithContext(ctx).Table("activity_booking").Where("id=? AND consumer_id=?", id, current.ConsumerID).Take(&b).Error; e != nil {
		if e == gorm.ErrRecordNotFound {
			return nil, "ACTIVITY_BOOKING_NOT_FOUND", nil
		}
		return nil, "", e
	}
	if b.Status != statusPendingVerification {
		return nil, "VERIFICATION_CODE_NOT_AVAILABLE", nil
	}
	var row struct {
		Display string    `gorm:"column:display_code"`
		Expires time.Time `gorm:"column:expires_at"`
		Status  string
	}
	if e := s.db.WithContext(ctx).Table("verification_code").Where("booking_id=? AND status='ACTIVE' AND expires_at>?", id, time.Now()).Take(&row).Error; e != nil {
		return nil, "VERIFICATION_CODE_NOT_AVAILABLE", nil
	}
	return map[string]any{"activityBookingId": fmt.Sprint(id), "displayCode": row.Display, "expiresAt": row.Expires, "status": row.Status}, "", nil
}
