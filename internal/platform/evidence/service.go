package evidence

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/idgen"
)

// Callback 是第三方存证结果通知的最小安全载荷。
type Callback struct {
	RequestNo  string    `json:"requestNo"`
	Status     string    `json:"status"`
	ReceiptNo  string    `json:"receiptNo"`
	OccurredAt time.Time `json:"occurredAt"`
}

// Service 统一创建存证记录并消费异步回调；业务模块只调用 Submit，不直接写 evidence_record。
type Service struct {
	db             *gorm.DB
	provider       Provider
	callbackSecret string
}

func NewService(db *gorm.DB, provider Provider, callbackSecret string) *Service {
	return &Service{db: db, provider: provider, callbackSecret: callbackSecret}
}

// Submit 先写待上链记录，再调用服务商；真实服务可异步回调，测试 Mock 可携带测试回执同步完成。
func (s *Service) Submit(ctx context.Context, request SubmitRequest) (uint64, string, error) {
	if request.ObjectType == "" || request.ObjectID == 0 || request.EvidenceType == "" || len(request.HashValue) != 64 {
		return 0, "VALIDATION_FORMAT_INVALID", nil
	}
	if request.RequestNo == "" {
		request.RequestNo = fmt.Sprintf("EVDREQ-%d", idgen.Next())
	}
	id := idgen.Next()
	now := time.Now()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := map[string]any{"id": id, "status": "PENDING_CHAIN", "version": 1, "evidence_no": fmt.Sprintf("EVD-%d", id), "object_type": request.ObjectType, "object_id": request.ObjectID, "evidence_type": request.EvidenceType, "hash_value": request.HashValue, "chain_provider": "PENDING", "request_no": request.RequestNo, "status_changed_at": now, "status_changed_by_type": "SYSTEM", "status_changed_by_id": "evidence"}
		if err := tx.Table("evidence_record").Create(row).Error; err != nil {
			return err
		}
		return audit.WriteStatusHistory(tx, "EVIDENCE", id, "PENDING_HASH", "PENDING_CHAIN", "已提交存证服务", audit.Operator{Type: "SYSTEM"})
	}); err != nil {
		return 0, "RESOURCE_CONFLICT", err
	}
	result, err := s.provider.Submit(ctx, request)
	if err != nil {
		return id, "EXTERNAL_SERVICE_UNAVAILABLE", err
	}
	// 无论同步或异步完成，都先记录实际服务商，避免成功记录仍显示 PENDING。
	_ = s.db.WithContext(ctx).Table("evidence_record").Where("id = ?", id).Updates(map[string]any{"chain_provider": result.Provider, "updated_at": time.Now()}).Error
	if result.Status == "SUCCESS" && result.ReceiptNo != "" {
		if code, callbackErr := s.ApplyCallback(ctx, Callback{RequestNo: request.RequestNo, Status: "SUCCESS", ReceiptNo: result.ReceiptNo, OccurredAt: time.Now()}); callbackErr != nil || code != "" {
			if callbackErr != nil {
				return id, "INTERNAL_ERROR", callbackErr
			}
			return id, code, nil
		}
	}
	return id, "", nil
}

// VerifyCallback 在数据库写入前验证 HMAC，签名不通过时不查询或修改任何业务记录。
func (s *Service) VerifyCallback(body []byte, timestamp, signature string) bool {
	if s.callbackSecret == "" || len(timestamp) == 0 || len(signature) == 0 {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, timestamp)
	if err != nil || time.Since(parsed).Abs() > 5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, []byte(s.callbackSecret))
	mac.Write([]byte(timestamp))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(signature))
}

// ApplyCallback 对同一请求号重复通知保持幂等；成功回执缺失时拒绝推进为 SUCCESS。
func (s *Service) ApplyCallback(ctx context.Context, callback Callback) (string, error) {
	if callback.RequestNo == "" || (callback.Status != "SUCCESS" && callback.Status != "FAILED") || (callback.Status == "SUCCESS" && callback.ReceiptNo == "") {
		return "VALIDATION_FORMAT_INVALID", nil
	}
	return "", s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID        uint64
			Status    string
			ReceiptNo string `gorm:"column:receipt_no"`
		}
		if err := tx.Table("evidence_record").Where("request_no = ?", callback.RequestNo).First(&row).Error; err != nil {
			return err
		}
		if row.Status == callback.Status && row.ReceiptNo == callback.ReceiptNo {
			return nil
		}
		if row.Status == "SUCCESS" {
			return fmt.Errorf("evidence already successful")
		}
		values := map[string]any{"status": callback.Status, "receipt_no": callback.ReceiptNo, "chained_at": callback.OccurredAt, "updated_at": time.Now(), "version": gorm.Expr("version + 1"), "status_changed_at": time.Now(), "status_changed_by_type": "THIRD_PARTY", "status_changed_by_id": "evidence"}
		if err := tx.Table("evidence_record").Where("id = ?", row.ID).Updates(values).Error; err != nil {
			return err
		}
		return audit.WriteStatusHistory(tx, "EVIDENCE", row.ID, row.Status, callback.Status, "存证服务回调", audit.Operator{Type: "THIRD_PARTY"})
	})
}
