package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/modules/creators"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/idgen"
)

// Service 处理授权订单支付。Mock 入口只在 test 环境注册，真实支付由后续 Provider 复用同一支付成功事务。
type Service struct{ db *gorm.DB }

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

type MockPayInput struct {
	BizType    string `json:"bizType" binding:"required,oneof=AUTH_ORDER"`
	BizOrderID string `json:"bizOrderId" binding:"required"`
}

type orderRow struct {
	ID               uint64    `gorm:"column:id"`
	CreatorID        uint64    `gorm:"column:creator_id"`
	Status           string    `gorm:"column:status"`
	Amount           string    `gorm:"column:amount"`
	IPID             uint64    `gorm:"column:ip_id"`
	PaymentExpireAt  time.Time `gorm:"column:payment_expire_at"`
	PurchaseSnapshot []byte    `gorm:"column:purchase_snapshot"`
	ContractStatus   string    `gorm:"column:contract_status"`
	EvidenceStatus   string    `gorm:"column:evidence_status"`
}

// MockPay 在调用方的幂等事务内执行，确保一笔订单至多生成一项支付、授权、范围快照和合同生成任务。
func (s *Service) MockPay(ctx context.Context, tx *gorm.DB, current creators.CurrentCreator, input MockPayInput, clientIP string) (map[string]any, string, error) {
	var id uint64
	if _, err := fmt.Sscan(input.BizOrderID, &id); err != nil || id == 0 {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var order orderRow
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Table("auth_order").Where("id=? AND creator_id=?", id, current.CreatorID).Take(&order).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, "AUTH_ORDER_NOT_FOUND", nil
		}
		return nil, "", err
	}
	if order.Status == "PAID" {
		var authorization struct{ ID uint64 }
		if err := tx.Table("creator_authorization").Select("id").Where("auth_order_id=?", order.ID).Take(&authorization).Error; err != nil {
			return nil, "", err
		}
		return paymentResult(order.ID, authorization.ID, order.ContractStatus, order.EvidenceStatus), "", nil
	}
	if order.Status != "PENDING_PAYMENT" || !order.PaymentExpireAt.After(time.Now()) {
		return map[string]any{"currentStatus": order.Status, "action": "PAY_AUTH_ORDER", "reasonCode": "AUTH_ORDER_NOT_PAYABLE"}, "AUTH_ORDER_PAYMENT_EXPIRED", nil
	}

	var snapshot map[string]any
	if err := json.Unmarshal(order.PurchaseSnapshot, &snapshot); err != nil {
		return nil, "", fmt.Errorf("decode purchase snapshot: %w", err)
	}
	auth, ok := snapshot["authorization"].(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("purchase snapshot missing authorization")
	}
	mode, _ := auth["authorizationMode"].(string)
	termType, _ := auth["termType"].(string)
	termValue := number(auth["termValue"])
	if mode == "" || termType == "" || (termType != "DATE_RANGE" && termValue <= 0) {
		return nil, "", fmt.Errorf("purchase snapshot lacks authorization term")
	}
	now := time.Now()
	startAt, endAt := calculatePeriod(now, auth)
	if startAt.IsZero() || endAt.IsZero() || !endAt.After(startAt) {
		return nil, "", fmt.Errorf("unsupported authorization term type %s", termType)
	}
	authorizationID := idgen.Next()
	paymentNo := fmt.Sprintf("MOCKPAY-%d", idgen.Next())
	operator := audit.Operator{Type: "USER", ID: current.UserID, RoleCode: "CREATOR", ClientIP: clientIP}
	if err := tx.Table("payment_transaction").Create(map[string]any{"id": idgen.Next(), "status": "SUCCESS", "version": 1, "payment_no": paymentNo, "biz_type": "AUTH_ORDER", "biz_order_id": order.ID, "idempotency_key": paymentNo, "payment_method": "MOCK", "amount": order.Amount, "payment_status": "SUCCESS", "provider_transaction_no": paymentNo, "paid_at": now, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}).Error; err != nil {
		return nil, "", err
	}
	updated := tx.Table("auth_order").Where("id=? AND status='PENDING_PAYMENT'", order.ID).Updates(map[string]any{"status": "PAID", "payment_no": paymentNo, "paid_at": now, "contract_status": "PENDING_GENERATION", "evidence_status": "PENDING_EVIDENCE", "version": gorm.Expr("version+1"), "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)})
	if updated.Error != nil {
		return nil, "", updated.Error
	}
	if updated.RowsAffected != 1 {
		return map[string]any{"currentStatus": order.Status, "action": "PAY_AUTH_ORDER", "reasonCode": "AUTH_ORDER_STATUS_CHANGED"}, "STATE_TRANSITION_NOT_ALLOWED", nil
	}
	reservation := tx.Table("auth_quota_reservation").Where("auth_order_id=? AND reservation_status='RESERVED'", order.ID).Updates(map[string]any{"reservation_status": "CONSUMED"})
	if reservation.Error != nil {
		return nil, "", reservation.Error
	}
	if (mode == "EXCLUSIVE_BUYOUT" || auth["quotaTotal"] != nil) && reservation.RowsAffected != 1 {
		return nil, "", fmt.Errorf("active quota reservation missing for limited authorization order")
	}
	scopeID := idgen.Next()
	scopeJSON, _ := json.Marshal(auth)
	ipJSON, _ := json.Marshal(snapshot["ip"])
	distributionJSON, _ := json.Marshal(snapshot["distribution"])
	if err := tx.Table("auth_scope_snapshot").Create(map[string]any{"id": scopeID, "created_by": current.UserID, "authorization_id": authorizationID, "ip_snapshot": ipJSON, "plan_snapshot": scopeJSON, "distribution_snapshot": distributionJSON, "scope_json": scopeJSON}).Error; err != nil {
		return nil, "", err
	}
	if err := tx.Table("creator_authorization").Create(map[string]any{"id": authorizationID, "status": "ACTIVE", "version": 1, "created_by": current.UserID, "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID), "authorization_no": fmt.Sprintf("AUTHZ-%d", authorizationID), "creator_id": order.CreatorID, "ip_id": order.IPID, "auth_order_id": order.ID, "authorization_mode": mode, "start_at": startAt, "end_at": endAt, "active_flag": true, "scope_snapshot_id": scopeID}).Error; err != nil {
		return nil, "", err
	}
	if err := tx.Table("auth_contract_generation_task").Create(map[string]any{"id": idgen.Next(), "status": "PENDING", "version": 1, "authorization_id": authorizationID, "auth_order_id": order.ID, "creator_user_id": current.UserID, "next_run_at": now}).Error; err != nil {
		return nil, "", err
	}
	if err := audit.WriteStatusHistory(tx, "AUTH_ORDER", order.ID, "PENDING_PAYMENT", "PAID", "Mock 模拟支付成功", operator); err != nil {
		return nil, "", err
	}
	if err := audit.WriteStatusHistory(tx, "CREATOR_AUTHORIZATION", authorizationID, "", "ACTIVE", "支付成功后授权生效", operator); err != nil {
		return nil, "", err
	}
	if err := audit.WriteLog(tx, "MOCK_PAY_AUTH_ORDER", "AUTH_ORDER", order.ID, map[string]any{"status": "PENDING_PAYMENT"}, map[string]any{"status": "PAID", "paymentNo": paymentNo, "authorizationId": fmt.Sprint(authorizationID)}, "SUCCESS", operator); err != nil {
		return nil, "", err
	}
	return paymentResult(order.ID, authorizationID, "PENDING_GENERATION", "PENDING_EVIDENCE"), "", nil
}

func paymentResult(orderID, authorizationID uint64, contractStatus, evidenceStatus string) map[string]any {
	if contractStatus == "" {
		contractStatus = "PENDING_GENERATION"
	}
	if evidenceStatus == "" {
		evidenceStatus = "PENDING_EVIDENCE"
	}
	return map[string]any{"authOrderId": fmt.Sprint(orderID), "paymentStatus": "SUCCESS", "authorizationId": fmt.Sprint(authorizationID), "contractStatus": contractStatus, "evidenceStatus": evidenceStatus}
}

func number(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}
func calculatePeriod(paidAt time.Time, scope map[string]any) (time.Time, time.Time) {
	termType, _ := scope["termType"].(string)
	value := number(scope["termValue"])
	switch termType {
	case "MONTH":
		return paidAt, paidAt.AddDate(0, value, 0)
	case "YEAR":
		return paidAt, paidAt.AddDate(value, 0, 0)
	case "DATE_RANGE":
		from, fromErr := time.ParseInLocation("2006-01-02", fmt.Sprint(scope["validFrom"]), paidAt.Location())
		to, toErr := time.ParseInLocation("2006-01-02", fmt.Sprint(scope["validTo"]), paidAt.Location())
		if fromErr == nil && toErr == nil {
			return from, to.AddDate(0, 0, 1).Add(-time.Nanosecond)
		}
	}
	return time.Time{}, time.Time{}
}
