package aftersales

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/modules/identity"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/httpx"
	"xingyanshu-backend/internal/platform/idempotency"
	"xingyanshu-backend/internal/platform/idgen"
)

const (
	statusPendingRefund = "PENDING_REFUND"
	statusRefunding     = "REFUND_PROCESSING"
	statusRefunded      = "REFUNDED"
	statusRefundFailed  = "REFUND_FAILED"
)

type ReviewInput struct {
	Approved       bool   `json:"approved"`
	ApprovedAmount string `json:"approvedAmount" binding:"omitempty,numeric"`
	Comment        string `json:"comment" binding:"omitempty,max=2000"`
}

type ReceiptInput struct {
	Received bool   `json:"received"`
	Comment  string `json:"comment" binding:"omitempty,max=2000"`
}

type RefundInput struct {
	Comment string `json:"comment" binding:"omitempty,max=2000"`
}

// RegisterAdminRoutes provides the AFS-02 back-office workflow. Customer
// service may decide a case and confirm returned goods; Finance alone may
// create/retry a refund.
func RegisterAdminRoutes(g *gin.RouterGroup, s *Service, auth gin.HandlerFunc) {
	r := g.Group("/admin/after-sales", auth)
	service := r.Group("", identity.RequireAnyPermission("FULFILLMENT_AFTER_SALE_MANAGE"))
	service.GET("", func(c *gin.Context) {
		d, err := s.AdminList(c, c.Query("status"), c.Query("bizType"))
		adminRespond(c, http.StatusOK, d, "", err)
	})
	service.GET("/:afterSaleId", func(c *gin.Context) {
		id, ok := adminID(c, "afterSaleId")
		if ok {
			d, code, err := s.AdminGet(c, id)
			adminRespond(c, http.StatusOK, d, code, err)
		}
	})
	service.POST("/:afterSaleId/review", func(c *gin.Context) {
		id, ok := adminID(c, "afterSaleId")
		if !ok {
			return
		}
		var in ReviewInput
		if raw, ok := adminBind(c, &in); ok {
			adminWrite(c, s, raw, http.StatusOK, func() (any, string, error) {
				return s.Review(c, adminCurrent(c), id, in, adminVersion(c), c.ClientIP())
			})
		}
	})
	service.POST("/:afterSaleId/receipt-confirmation", func(c *gin.Context) {
		id, ok := adminID(c, "afterSaleId")
		if !ok {
			return
		}
		var in ReceiptInput
		if raw, ok := adminBind(c, &in); ok {
			adminWrite(c, s, raw, http.StatusOK, func() (any, string, error) {
				return s.ConfirmReceipt(c, adminCurrent(c), id, in, adminVersion(c), c.ClientIP())
			})
		}
	})
	finance := r.Group("", identity.RequireAnyPermission("FINANCE_REFUND_MANAGE"))
	finance.POST("/:afterSaleId/refunds", func(c *gin.Context) {
		id, ok := adminID(c, "afterSaleId")
		if !ok {
			return
		}
		var in RefundInput
		if raw, ok := adminBind(c, &in); ok {
			adminWrite(c, s, raw, http.StatusCreated, func() (any, string, error) { return s.Refund(c, adminCurrent(c), id, in, c.ClientIP()) })
		}
	})
}

func adminCurrent(c *gin.Context) identity.CurrentAdmin {
	v, _ := identity.CurrentAdminFromContext(c)
	return v
}
func adminID(c *gin.Context, key string) (uint64, bool) {
	v, e := strconv.ParseUint(c.Param(key), 10, 64)
	if e != nil || v == 0 {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "售后单ID格式错误"))
		return 0, false
	}
	return v, true
}
func adminVersion(c *gin.Context) uint {
	v, _ := strconv.ParseUint(c.GetHeader("If-Match-Version"), 10, 32)
	return uint(v)
}
func adminBind(c *gin.Context, into any) ([]byte, bool) {
	raw, e := io.ReadAll(c.Request.Body)
	if e != nil {
		httpx.AbortWithError(c, httpx.NewValidationError("VALIDATION_FORMAT_INVALID", "请求体读取失败"))
		return nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, httpx.BindJSON(c, into)
}
func adminWrite(c *gin.Context, s *Service, raw []byte, status int, fn func() (any, string, error)) {
	u := adminCurrent(c)
	r, replayed, e := idempotency.Execute(c, s.db, idempotency.Scope{SubjectType: "ADMIN", SubjectID: u.UserID, Method: c.Request.Method, Path: c.FullPath()}, c.GetHeader("Idempotency-Key"), raw, func(_ *gorm.DB) (idempotency.Result, error) {
		d, code, err := fn()
		if err != nil {
			return idempotency.Result{}, err
		}
		return idempotency.Result{HTTPStatus: adminHTTPStatus(status, code), Code: code, Data: d}, nil
	})
	if e != nil {
		httpx.AbortWithError(c, httpx.AppErrorFromIdempotency(e))
		return
	}
	if replayed {
		c.Header("Idempotency-Replayed", "true")
	}
	adminRespond(c, r.HTTPStatus, r.Data, r.Code, nil)
}
func adminHTTPStatus(ok int, code string) int {
	switch code {
	case "AFTER_SALE_NOT_FOUND":
		return http.StatusNotFound
	case "VERSION_CONFLICT", "AFTER_SALE_REVIEW_NOT_ALLOWED", "RETURN_RECEIPT_NOT_ALLOWED", "REFUND_NOT_ALLOWED", "REFUND_AMOUNT_EXCEEDED":
		return http.StatusUnprocessableEntity
	default:
		if code != "" {
			return http.StatusBadRequest
		}
		return ok
	}
}
func adminRespond(c *gin.Context, status int, data any, code string, err error) {
	if err != nil {
		httpx.AbortWithError(c, httpx.NewError(500, "INTERNAL_ERROR", "售后处理失败"))
		return
	}
	if code != "" {
		httpx.AbortWithError(c, httpx.NewError(status, code, adminMessage(code)))
		return
	}
	httpx.Success(c, status, data)
}
func adminMessage(code string) string {
	switch code {
	case "AFTER_SALE_REVIEW_NOT_ALLOWED":
		return "当前售后单不能审核"
	case "RETURN_RECEIPT_NOT_ALLOWED":
		return "当前售后单不能确认收货"
	case "REFUND_NOT_ALLOWED":
		return "当前售后单不能退款"
	case "REFUND_AMOUNT_EXCEEDED":
		return "确认退款金额超过申请金额"
	default:
		return message(code)
	}
}

func (s *Service) AdminList(ctx context.Context, status, bizType string) (map[string]any, error) {
	var rows []ticketRow
	q := s.db.WithContext(ctx).Table("after_sale_ticket")
	if status = strings.TrimSpace(status); status != "" {
		q = q.Where("status=?", status)
	}
	if bizType = strings.TrimSpace(bizType); bizType != "" {
		q = q.Where("biz_type=?", bizType)
	}
	if err := q.Order("created_at DESC,id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.ticketData(ctx, row, true))
	}
	return map[string]any{"items": items, "total": len(items)}, nil
}
func (s *Service) AdminGet(ctx context.Context, id uint64) (map[string]any, string, error) {
	var row ticketRow
	if err := s.db.WithContext(ctx).Table("after_sale_ticket").Where("id=?", id).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, "AFTER_SALE_NOT_FOUND", nil
		}
		return nil, "", err
	}
	return s.ticketData(ctx, row, true), "", nil
}

func (s *Service) Review(ctx context.Context, current identity.CurrentAdmin, id uint64, in ReviewInput, version uint, clientIP string) (map[string]any, string, error) {
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t ticketRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("after_sale_ticket").Where("id=?", id).Take(&t).Error; e != nil {
			code = "AFTER_SALE_NOT_FOUND"
			return nil
		}
		if t.Version != version {
			code = "VERSION_CONFLICT"
			return nil
		}
		if t.Status != statusReview {
			code = "AFTER_SALE_REVIEW_NOT_ALLOWED"
			return nil
		}
		now := time.Now()
		op := audit.Operator{Type: "USER", ID: current.UserID, RoleCode: "FULFILLMENT_CUSTOMER_SERVICE", ClientIP: clientIP}
		if !in.Approved {
			if e := tx.Table("after_sale_ticket").Where("id=? AND version=?", id, version).Updates(map[string]any{"status": statusRejected, "reviewer_id": current.UserID, "version": version + 1, "status_reason_code": "REJECTED_BY_SERVICE", "status_reason": strings.TrimSpace(in.Comment), "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}).Error; e != nil {
				return e
			}
			if e := s.restoreBusiness(tx, t, current.UserID, now); e != nil {
				return e
			}
			if e := audit.WriteStatusHistory(tx, "AFTER_SALE", id, statusReview, statusRejected, "履约客服驳回售后", op); e != nil {
				return e
			}
			out = map[string]any{"afterSaleId": fmt.Sprint(id), "status": statusRejected}
			return nil
		}
		amount := t.ApplyAmount
		if strings.TrimSpace(in.ApprovedAmount) != "" {
			want, e := cents(in.ApprovedAmount)
			if e != nil {
				code = "VALIDATION_FORMAT_INVALID"
				return nil
			}
			max, _ := cents(t.ApplyAmount)
			if want > max {
				code = "REFUND_AMOUNT_EXCEEDED"
				return nil
			}
			amount = money(want)
		}
		next := statusPendingRefund
		if t.BizType == bizProduct && t.AfterSaleType == "RETURN_REFUND" {
			next = statusReturning
		}
		if e := tx.Table("after_sale_ticket").Where("id=? AND version=?", id, version).Updates(map[string]any{"status": next, "approved_amount": amount, "reviewer_id": current.UserID, "version": version + 1, "status_reason": strings.TrimSpace(in.Comment), "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID)}).Error; e != nil {
			return e
		}
		if t.BizType == bizProduct {
			if e := tx.Table("product_order").Where("id=?", t.BizOrderID).Updates(map[string]any{"after_sale_status": next, "updated_by": current.UserID, "version": gorm.Expr("version+1")}).Error; e != nil {
				return e
			}
		}
		if e := audit.WriteStatusHistory(tx, "AFTER_SALE", id, statusReview, next, "履约客服同意售后", op); e != nil {
			return e
		}
		if e := audit.WriteLog(tx, "REVIEW_AFTER_SALE", "AFTER_SALE", id, nil, map[string]any{"status": next, "approvedAmount": amount}, "SUCCESS", op); e != nil {
			return e
		}
		out = map[string]any{"afterSaleId": fmt.Sprint(id), "status": next, "approvedAmount": amount, "version": version + 1}
		return nil
	})
	return out, code, err
}

func (s *Service) ConfirmReceipt(ctx context.Context, current identity.CurrentAdmin, id uint64, in ReceiptInput, version uint, clientIP string) (map[string]any, string, error) {
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t ticketRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("after_sale_ticket").Where("id=?", id).Take(&t).Error; e != nil {
			code = "AFTER_SALE_NOT_FOUND"
			return nil
		}
		if t.Version != version {
			code = "VERSION_CONFLICT"
			return nil
		}
		if t.Status != statusReceipt {
			code = "RETURN_RECEIPT_NOT_ALLOWED"
			return nil
		}
		now := time.Now()
		op := audit.Operator{Type: "USER", ID: current.UserID, RoleCode: "FULFILLMENT_CUSTOMER_SERVICE", ClientIP: clientIP}
		if !in.Received {
			if e := tx.Table("after_sale_ticket").Where("id=? AND version=?", id, version).Updates(map[string]any{"status": statusRejected, "version": version + 1, "status_reason_code": "RETURN_REJECTED", "status_reason": strings.TrimSpace(in.Comment), "updated_by": current.UserID, "status_changed_at": now}).Error; e != nil {
				return e
			}
			if e := s.restoreBusiness(tx, t, current.UserID, now); e != nil {
				return e
			}
			if e := audit.WriteStatusHistory(tx, "AFTER_SALE", id, statusReceipt, statusRejected, "履约客服拒收退货", op); e != nil {
				return e
			}
			out = map[string]any{"afterSaleId": fmt.Sprint(id), "status": statusRejected}
			return nil
		}
		if e := tx.Table("return_logistics").Where("after_sale_id=?", id).Updates(map[string]any{"status": "RECEIVED", "received_at": now, "updated_by": current.UserID, "status_changed_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Table("after_sale_ticket").Where("id=? AND version=?", id, version).Updates(map[string]any{"status": statusPendingRefund, "version": version + 1, "status_reason": strings.TrimSpace(in.Comment), "updated_by": current.UserID, "status_changed_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Table("product_order").Where("id=?", t.BizOrderID).Updates(map[string]any{"after_sale_status": statusPendingRefund, "updated_by": current.UserID, "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
		if e := audit.WriteStatusHistory(tx, "AFTER_SALE", id, statusReceipt, statusPendingRefund, "履约客服确认收货", op); e != nil {
			return e
		}
		out = map[string]any{"afterSaleId": fmt.Sprint(id), "status": statusPendingRefund, "version": version + 1}
		return nil
	})
	return out, code, err
}

func (s *Service) Refund(ctx context.Context, current identity.CurrentAdmin, id uint64, in RefundInput, clientIP string) (map[string]any, string, error) {
	var out map[string]any
	code := ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t ticketRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("after_sale_ticket").Where("id=?", id).Take(&t).Error; e != nil {
			code = "AFTER_SALE_NOT_FOUND"
			return nil
		}
		if t.Status == statusRefunded {
			var r struct {
				RefundNo string `gorm:"column:refund_no"`
			}
			_ = tx.Table("refund_order").Select("refund_no").Where("after_sale_id=?", id).Take(&r).Error
			out = map[string]any{"afterSaleId": fmt.Sprint(id), "status": statusRefunded, "refundNo": r.RefundNo}
			return nil
		}
		if t.Status != statusPendingRefund {
			code = "REFUND_NOT_ALLOWED"
			return nil
		}
		amount := t.ApplyAmount
		if t.ApprovedAmount != "" {
			amount = t.ApprovedAmount
		}
		var payment struct{ ID uint64 }
		pt := "PRODUCT_ORDER"
		if t.BizType == bizActivity {
			pt = "ACTIVITY_BOOKING"
		}
		_ = tx.Table("payment_transaction").Select("id").Where("biz_type=? AND biz_order_id=? AND payment_status='SUCCESS'", pt, t.BizOrderID).Order("id DESC").Take(&payment).Error
		now := time.Now()
		rid := idgen.Next()
		rno := fmt.Sprintf("REFUND-%d", rid)
		op := audit.Operator{Type: "USER", ID: current.UserID, RoleCode: "FINANCE", ClientIP: clientIP}
		if e := tx.Table("refund_order").Create(map[string]any{"id": rid, "status": statusRefunding, "version": 1, "created_by": current.UserID, "updated_by": current.UserID, "status_changed_at": now, "status_changed_by_type": "USER", "status_changed_by_id": fmt.Sprint(current.UserID), "refund_no": rno, "after_sale_id": id, "original_payment_id": payment.ID, "refund_amount": amount, "finance_reviewer_id": current.UserID, "review_comment": blank(in.Comment), "refund_method": "MOCK"}).Error; e != nil {
			return e
		}
		if e := tx.Table("after_sale_ticket").Where("id=? AND status=?", id, statusPendingRefund).Updates(map[string]any{"status": statusRefunding, "version": gorm.Expr("version+1"), "updated_by": current.UserID, "status_changed_at": now}).Error; e != nil {
			return e
		}
		// The current provider is deliberately Mock. The successful branch is a single transaction so a retry cannot duplicate refund, inventory or quota restoration.
		if e := s.restoreCapacity(tx, t, current.UserID, now); e != nil {
			return e
		}
		if e := s.restoreBusiness(tx, t, current.UserID, now); e != nil {
			return e
		}
		if e := tx.Table("refund_order").Where("id=?", rid).Updates(map[string]any{"status": statusRefunded, "provider_refund_no": rno, "refunded_at": now, "updated_at": now}).Error; e != nil {
			return e
		}
		if e := tx.Table("after_sale_ticket").Where("id=? AND status=?", id, statusRefunding).Updates(map[string]any{"status": statusRefunded, "version": gorm.Expr("version+1"), "updated_by": current.UserID, "status_changed_at": now}).Error; e != nil {
			return e
		}
		if e := audit.WriteStatusHistory(tx, "AFTER_SALE", id, statusPendingRefund, statusRefunded, "财务完成模拟退款", op); e != nil {
			return e
		}
		if e := audit.WriteStatusHistory(tx, "REFUND_ORDER", rid, statusRefunding, statusRefunded, "模拟退款成功", op); e != nil {
			return e
		}
		if e := audit.WriteLog(tx, "REFUND_AFTER_SALE", "AFTER_SALE", id, nil, map[string]any{"refundNo": rno, "refundAmount": amount, "method": "MOCK"}, "SUCCESS", op); e != nil {
			return e
		}
		out = map[string]any{"afterSaleId": fmt.Sprint(id), "status": statusRefunded, "refundId": fmt.Sprint(rid), "refundNo": rno, "refundAmount": amount, "refundMethod": "MOCK"}
		return nil
	})
	return out, code, err
}

func (s *Service) restoreBusiness(tx *gorm.DB, t ticketRow, userID uint64, now time.Time) error {
	if t.BizType == bizProduct {
		return tx.Table("product_order").Where("id=?", t.BizOrderID).Updates(map[string]any{"status": t.OriginalBizStatus, "after_sale_status": nil, "updated_by": userID, "status_changed_at": now, "version": gorm.Expr("version+1")}).Error
	}
	return tx.Table("activity_booking").Where("id=?", t.BizOrderID).Updates(map[string]any{"status": t.OriginalBizStatus, "updated_by": userID, "status_changed_at": now, "version": gorm.Expr("version+1")}).Error
}
func (s *Service) restoreCapacity(tx *gorm.DB, t ticketRow, userID uint64, now time.Time) error {
	if t.BizType == bizProduct {
		if t.ProductOrderItemID == nil {
			return fmt.Errorf("product after-sale missing item")
		}
		var item struct {
			SKUID uint64 `gorm:"column:sku_id"`
		}
		if e := tx.Table("product_order_item").Select("sku_id").Where("id=?", *t.ProductOrderItemID).Take(&item).Error; e != nil {
			return e
		}
		return tx.Table("product_sku").Where("id=? AND stock_sold>=?", item.SKUID, t.ApplyQuantity).Updates(map[string]any{"stock_sold": gorm.Expr("stock_sold-?", t.ApplyQuantity), "updated_by": userID, "updated_at": now, "version": gorm.Expr("version+1")}).Error
	}
	var b struct {
		TicketTypeID uint64 `gorm:"column:ticket_type_id"`
		Quantity     int
	}
	if e := tx.Table("activity_booking").Select("ticket_type_id,quantity").Where("id=?", t.BizOrderID).Take(&b).Error; e != nil {
		return e
	}
	return tx.Table("activity_ticket_type").Where("id=? AND quota_used>=?", b.TicketTypeID, b.Quantity).Updates(map[string]any{"quota_used": gorm.Expr("quota_used-?", b.Quantity), "updated_by": userID, "updated_at": now, "version": gorm.Expr("version+1")}).Error
}

var _ = clause.Locking{}
