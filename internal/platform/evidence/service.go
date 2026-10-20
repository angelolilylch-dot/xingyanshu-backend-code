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
	RequestNo       string    `json:"requestNo"`
	Status          string    `json:"status"`
	ReceiptNo       string    `json:"receiptNo"`
	TransactionHash string    `json:"transactionHash"`
	OccurredAt      time.Time `json:"occurredAt"`
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
		_ = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if updateErr := tx.Table("evidence_record").Where("id=? AND status<>'SUCCESS'", id).Updates(map[string]any{"status": "FAILED", "status_reason_code": "PROVIDER_SUBMIT_FAILED", "status_reason": truncateError(err), "updated_at": time.Now(), "version": gorm.Expr("version+1"), "status_changed_at": time.Now(), "status_changed_by_type": "SYSTEM", "status_changed_by_id": "evidence"}).Error; updateErr != nil {
				return updateErr
			}
			return audit.WriteStatusHistory(tx, "EVIDENCE", id, "PENDING_CHAIN", "FAILED", "存证服务提交失败", audit.Operator{Type: "SYSTEM"})
		})
		return id, "EXTERNAL_SERVICE_UNAVAILABLE", err
	}
	// 无论同步或异步完成，都先记录实际服务商，避免成功记录仍显示 PENDING。
	_ = s.db.WithContext(ctx).Table("evidence_record").Where("id = ?", id).Updates(map[string]any{"chain_provider": result.Provider, "updated_at": time.Now()}).Error
	if result.Status == "SUCCESS" && result.ReceiptNo != "" && result.TransactionHash != "" {
		if code, callbackErr := s.ApplyCallback(ctx, Callback{RequestNo: request.RequestNo, Status: "SUCCESS", ReceiptNo: result.ReceiptNo, TransactionHash: result.TransactionHash, OccurredAt: time.Now()}); callbackErr != nil || code != "" {
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
	if callback.RequestNo == "" || (callback.Status != "SUCCESS" && callback.Status != "FAILED") || (callback.Status == "SUCCESS" && (callback.ReceiptNo == "" || callback.TransactionHash == "")) {
		return "VALIDATION_FORMAT_INVALID", nil
	}
	return "", s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID              uint64
			Status          string
			ObjectType      string `gorm:"column:object_type"`
			ObjectID        uint64 `gorm:"column:object_id"`
			ReceiptNo       string `gorm:"column:receipt_no"`
			TransactionHash string `gorm:"column:chain_transaction_hash"`
		}
		if err := tx.Table("evidence_record").Where("request_no = ?", callback.RequestNo).First(&row).Error; err != nil {
			return err
		}
		if row.Status == callback.Status && row.ReceiptNo == callback.ReceiptNo && row.TransactionHash == callback.TransactionHash {
			return nil
		}
		if row.Status == "SUCCESS" {
			return fmt.Errorf("evidence already successful")
		}
		values := map[string]any{"status": callback.Status, "receipt_no": callback.ReceiptNo, "chain_transaction_hash": callback.TransactionHash, "chained_at": callback.OccurredAt, "updated_at": time.Now(), "version": gorm.Expr("version + 1"), "status_changed_at": time.Now(), "status_changed_by_type": "THIRD_PARTY", "status_changed_by_id": "evidence"}
		if err := tx.Table("evidence_record").Where("id = ?", row.ID).Updates(values).Error; err != nil {
			return err
		}
		if row.ObjectType == "AUTH_CONTRACT" {
			contractStatus := "EVIDENCE_RETRYING"
			contractValues := map[string]any{"evidence_status": contractStatus, "evidence_receipt_no": callback.ReceiptNo, "evidence_transaction_hash": callback.TransactionHash, "evidence_last_error": "存证服务返回失败", "evidence_retry_count": gorm.Expr("evidence_retry_count+1"), "evidence_next_retry_at": time.Now().Add(time.Minute), "updated_at": time.Now()}
			if callback.Status == "SUCCESS" {
				contractStatus = "EVIDENCED"
				contractValues = map[string]any{"evidence_status": contractStatus, "evidence_receipt_no": callback.ReceiptNo, "evidence_transaction_hash": callback.TransactionHash, "evidence_last_error": nil, "evidence_next_retry_at": nil, "updated_at": time.Now()}
			}
			if err := tx.Table("auth_contract").Where("id=?", row.ObjectID).Updates(contractValues).Error; err != nil {
				return err
			}
			var contract struct {
				AuthorizationID uint64 `gorm:"column:authorization_id"`
			}
			if err := tx.Table("auth_contract").Select("authorization_id").Where("id=?", row.ObjectID).Take(&contract).Error; err != nil {
				return err
			}
			var authorization struct {
				AuthOrderID uint64 `gorm:"column:auth_order_id"`
			}
			if err := tx.Table("creator_authorization").Select("auth_order_id").Where("id=?", contract.AuthorizationID).Take(&authorization).Error; err != nil {
				return err
			}
			if err := tx.Table("auth_order").Where("id=?", authorization.AuthOrderID).Updates(map[string]any{"evidence_status": contractStatus, "updated_at": time.Now()}).Error; err != nil {
				return err
			}
		}
		return audit.WriteStatusHistory(tx, "EVIDENCE", row.ID, row.Status, callback.Status, "存证服务回调", audit.Operator{Type: "THIRD_PARTY"})
	})
}

// Retry 对已创建但未成功的存证记录重试，同一 requestNo 和 evidence_record 始终复用。
func (s *Service) Retry(ctx context.Context, evidenceID uint64) (SubmitResult, string, error) {
	var row struct {
		ID           uint64
		Status       string
		RequestNo    string `gorm:"column:request_no"`
		ObjectType   string `gorm:"column:object_type"`
		ObjectID     uint64 `gorm:"column:object_id"`
		EvidenceType string `gorm:"column:evidence_type"`
		HashValue    string `gorm:"column:hash_value"`
	}
	if err := s.db.WithContext(ctx).Table("evidence_record").Where("id=?", evidenceID).Take(&row).Error; err != nil {
		return SubmitResult{}, "RESOURCE_NOT_FOUND", err
	}
	if row.Status == "SUCCESS" {
		return SubmitResult{RequestNo: row.RequestNo, Status: "SUCCESS"}, "", nil
	}
	var retryNo int64
	if err := s.db.WithContext(ctx).Table("evidence_retry_log").Where("evidence_id=?", evidenceID).Count(&retryNo).Error; err != nil {
		return SubmitResult{}, "INTERNAL_ERROR", err
	}
	request := SubmitRequest{RequestNo: row.RequestNo, ObjectType: row.ObjectType, ObjectID: row.ObjectID, EvidenceType: row.EvidenceType, HashValue: row.HashValue}
	result, err := s.provider.Submit(ctx, request)
	resultStatus := "FAILED"
	errorSummary := ""
	if err != nil {
		errorSummary = truncateError(err)
	} else if result.Status == "SUCCESS" && result.ReceiptNo != "" && result.TransactionHash != "" {
		resultStatus = "SUCCESS"
	}
	logErr := s.db.WithContext(ctx).Table("evidence_retry_log").Create(map[string]any{"id": idgen.Next(), "evidence_id": evidenceID, "retry_no": retryNo + 1, "requested_at": time.Now(), "result_status": resultStatus, "error_summary": nullableText(errorSummary)}).Error
	if logErr != nil {
		return result, "INTERNAL_ERROR", logErr
	}
	if resultStatus != "SUCCESS" {
		_ = s.db.WithContext(ctx).Table("evidence_record").Where("id=? AND status<>'SUCCESS'", evidenceID).Updates(map[string]any{"status": "FAILED", "status_reason_code": "PROVIDER_RETRY_FAILED", "status_reason": errorSummary, "updated_at": time.Now(), "version": gorm.Expr("version+1")}).Error
		if err == nil {
			err = fmt.Errorf("evidence provider did not return complete success receipt")
		}
		return result, "EXTERNAL_SERVICE_UNAVAILABLE", err
	}
	code, callbackErr := s.ApplyCallback(ctx, Callback{RequestNo: row.RequestNo, Status: "SUCCESS", ReceiptNo: result.ReceiptNo, TransactionHash: result.TransactionHash, OccurredAt: time.Now()})
	return result, code, callbackErr
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len([]rune(text)) > 500 {
		return string([]rune(text)[:500])
	}
	return text
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
