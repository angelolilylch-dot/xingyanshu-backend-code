package evidence

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/idgen"
)

// Callback 是第三方存证结果通知的最小安全载荷。
type Callback struct {
	RequestNo       string    `json:"requestNo"`
	Status          string    `json:"status"`
	ReceiptNo       string    `json:"receiptNo"`
	TransactionHash string    `json:"transactionHash"`
	BlockHeight     string    `json:"blockHeight"`
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
		if code, callbackErr := s.ApplyCallback(ctx, Callback{RequestNo: request.RequestNo, Status: "SUCCESS", ReceiptNo: result.ReceiptNo, TransactionHash: result.TransactionHash, BlockHeight: result.BlockHeight, OccurredAt: time.Now()}); callbackErr != nil || code != "" {
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
		if callback.BlockHeight != "" {
			values["chain_block_height"] = callback.BlockHeight
		}
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
		if callback.Status == "SUCCESS" {
			if err := s.createCertificateTask(tx, row.ID, callback.ReceiptNo, time.Now()); err != nil {
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
	code, callbackErr := s.ApplyCallback(ctx, Callback{RequestNo: row.RequestNo, Status: "SUCCESS", ReceiptNo: result.ReceiptNo, TransactionHash: result.TransactionHash, BlockHeight: result.BlockHeight, OccurredAt: time.Now()})
	return result, code, callbackErr
}

// CertificateForCreator 返回创作者本人有权访问的业务存证证书元数据。
// 证书地址只从本地 evidence_certificate 读取，绝不把至信链鉴权参数暴露给前端。
func (s *Service) CertificateForCreator(ctx context.Context, creatorID, evidenceID uint64) (map[string]any, string, error) {
	if creatorID == 0 || evidenceID == 0 {
		return nil, "VALIDATION_FORMAT_INVALID", nil
	}
	var row struct {
		EvidenceID        uint64  `gorm:"column:evidence_id"`
		ObjectType        string  `gorm:"column:object_type"`
		ObjectID          uint64  `gorm:"column:object_id"`
		EvidenceType      string  `gorm:"column:evidence_type"`
		EvidenceStatus    string  `gorm:"column:evidence_status"`
		ReceiptNo         *string `gorm:"column:receipt_no"`
		TransactionHash   *string `gorm:"column:transaction_hash"`
		BlockHeight       *string `gorm:"column:block_height"`
		CertificateStatus *string `gorm:"column:certificate_status"`
		Provider          *string `gorm:"column:provider"`
		ProviderStatus    *string `gorm:"column:provider_status"`
		CertificateURL    *string `gorm:"column:certificate_url"`
		ReasonCode        *string `gorm:"column:status_reason_code"`
	}
	err := s.db.WithContext(ctx).Table("evidence_record e").
		Select("e.id AS evidence_id,e.object_type,e.object_id,e.evidence_type,e.status AS evidence_status,e.receipt_no,e.chain_transaction_hash AS transaction_hash,e.chain_block_height AS block_height,c.status AS certificate_status,c.provider,c.provider_status,c.certificate_url,c.status_reason_code").
		Joins("LEFT JOIN evidence_certificate c ON c.evidence_id=e.id").
		Where(`e.id=? AND (
			(e.object_type='DERIVATIVE_WORK' AND EXISTS (SELECT 1 FROM derivative_work w WHERE w.id=e.object_id AND w.creator_id=?))
			OR
			(e.object_type='AUTH_CONTRACT' AND EXISTS (SELECT 1 FROM auth_contract ac JOIN creator_authorization ca ON ca.id=ac.authorization_id WHERE ac.id=e.object_id AND ca.creator_id=?))
		)`, evidenceID, creatorID, creatorID).
		Take(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			// 对无权访问的记录同样返回不存在，避免通过 ID 枚举其他创作者的业务数据。
			return nil, "RESOURCE_NOT_FOUND", nil
		}
		return nil, "INTERNAL_ERROR", err
	}
	certificate := map[string]any{
		"status":         stringOrEmpty(row.CertificateStatus),
		"provider":       stringOrEmpty(row.Provider),
		"providerStatus": stringOrEmpty(row.ProviderStatus),
		"certUrl":        "",
		"reasonCode":     stringOrEmpty(row.ReasonCode),
	}
	if stringOrEmpty(row.CertificateStatus) == "READY" {
		certificate["certUrl"] = stringOrEmpty(row.CertificateURL)
	}
	return map[string]any{
		"evidenceId":      fmt.Sprint(row.EvidenceID),
		"objectType":      row.ObjectType,
		"objectId":        fmt.Sprint(row.ObjectID),
		"evidenceType":    row.EvidenceType,
		"evidenceStatus":  row.EvidenceStatus,
		"receiptNo":       stringOrEmpty(row.ReceiptNo),
		"transactionHash": stringOrEmpty(row.TransactionHash),
		"blockHeight":     stringOrEmpty(row.BlockHeight),
		"certificate":     certificate,
	}, "", nil
}

// ProcessDueCertificates 创建并轮询链上证书。先查询存证记录，再发起证书生成，避免把未确认的 evId 传给证书接口。
func (s *Service) ProcessDueCertificates(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	now := time.Now()
	var missing []struct {
		ID        uint64
		ReceiptNo string `gorm:"column:receipt_no"`
	}
	if err := s.db.WithContext(ctx).Table("evidence_record e").Select("e.id,e.receipt_no").Joins("LEFT JOIN evidence_certificate c ON c.evidence_id=e.id").Where("e.status='SUCCESS' AND e.receipt_no IS NOT NULL AND c.id IS NULL").Order("e.id").Limit(limit).Find(&missing).Error; err != nil {
		return 0, err
	}
	for _, row := range missing {
		if err := s.createCertificateTask(s.db.WithContext(ctx), row.ID, row.ReceiptNo, now); err != nil {
			return 0, err
		}
	}
	var tasks []certificateTask
	if err := s.db.WithContext(ctx).Table("evidence_certificate").Where("status IN ? AND next_run_at<=?", []string{"PENDING_REQUEST", "PENDING"}, now).Order("next_run_at,id").Limit(limit).Find(&tasks).Error; err != nil {
		return 0, err
	}
	for _, task := range tasks {
		if err := s.processCertificate(ctx, task, now); err != nil {
			return 0, err
		}
	}
	return len(tasks), nil
}

type certificateTask struct {
	ID                uint64
	EvidenceID        uint64 `gorm:"column:evidence_id"`
	Status            string
	RequestRetryCount int    `gorm:"column:request_retry_count"`
	QueryRetryCount   int    `gorm:"column:query_retry_count"`
	ReceiptNo         string `gorm:"-"`
}

func (s *Service) createCertificateTask(db *gorm.DB, evidenceID uint64, receiptNo string, now time.Time) error {
	return db.Table("evidence_certificate").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "evidence_id"}},
		DoUpdates: clause.Assignments(map[string]any{"evidence_id": gorm.Expr("evidence_id")}),
	}).Create(map[string]any{"id": idgen.Next(), "status": "PENDING_REQUEST", "version": 1, "evidence_id": evidenceID, "provider": "PENDING", "next_run_at": now}).Error
}

func (s *Service) processCertificate(ctx context.Context, task certificateTask, now time.Time) error {
	var evidenceRow struct {
		ReceiptNo string `gorm:"column:receipt_no"`
	}
	if err := s.db.WithContext(ctx).Table("evidence_record").Select("receipt_no").Where("id=? AND status='SUCCESS'", task.EvidenceID).Take(&evidenceRow).Error; err != nil {
		return err
	}
	// GET /ev/query is deliberately part of the worker chain: only a verifiable evId proceeds to certificate creation.
	chain, err := s.provider.Query(ctx, evidenceRow.ReceiptNo)
	if err != nil {
		return s.recordCertificateError(ctx, task, "EVIDENCE_QUERY_FAILED", err, now)
	}
	if chain.BlockHeight != "" {
		_ = s.db.WithContext(ctx).Table("evidence_record").Where("id=?", task.EvidenceID).Updates(map[string]any{"chain_block_height": chain.BlockHeight, "updated_at": now}).Error
	}
	if task.Status == "PENDING_REQUEST" {
		result, requestErr := s.provider.RequestCertificate(ctx, evidenceRow.ReceiptNo)
		if requestErr != nil {
			return s.recordCertificateError(ctx, task, "CERTIFICATE_REQUEST_FAILED", requestErr, now)
		}
		return s.persistCertificateResult(ctx, task, result, true, now)
	}
	result, queryErr := s.provider.QueryCertificate(ctx, evidenceRow.ReceiptNo)
	if queryErr != nil {
		return s.recordCertificateError(ctx, task, "CERTIFICATE_QUERY_FAILED", queryErr, now)
	}
	return s.persistCertificateResult(ctx, task, result, false, now)
}

func (s *Service) persistCertificateResult(ctx context.Context, task certificateTask, result CertificateResult, requested bool, now time.Time) error {
	values := map[string]any{"provider": result.Provider, "provider_status": result.ProviderStatus, "updated_at": now, "version": gorm.Expr("version+1")}
	if requested {
		values["requested_at"] = now
		values["request_retry_count"] = task.RequestRetryCount + 1
	} else {
		values["last_queried_at"] = now
		values["query_retry_count"] = task.QueryRetryCount + 1
	}
	if result.Status == "READY" && strings.TrimSpace(result.CertURL) != "" {
		values["status"] = "READY"
		values["certificate_url"] = result.CertURL
		values["next_run_at"] = now
		values["status_reason_code"] = nil
		values["status_reason"] = nil
	} else {
		values["status"] = "PENDING"
		values["next_run_at"] = now.Add(time.Minute)
	}
	return s.db.WithContext(ctx).Table("evidence_certificate").Where("id=? AND status IN ?", task.ID, []string{"PENDING_REQUEST", "PENDING"}).Updates(values).Error
}

func (s *Service) recordCertificateError(ctx context.Context, task certificateTask, code string, cause error, now time.Time) error {
	attempts := task.QueryRetryCount
	if task.Status == "PENDING_REQUEST" {
		attempts = task.RequestRetryCount
	}
	attempts++
	values := map[string]any{"status_reason_code": code, "status_reason": truncateError(cause), "updated_at": now, "version": gorm.Expr("version+1")}
	if task.Status == "PENDING_REQUEST" {
		values["request_retry_count"] = attempts
	} else {
		values["query_retry_count"] = attempts
	}
	if attempts >= 5 {
		values["status"] = "FAILED"
	} else {
		values["status"] = task.Status
		values["next_run_at"] = now.Add(time.Duration(attempts) * time.Minute)
	}
	return s.db.WithContext(ctx).Table("evidence_certificate").Where("id=? AND status IN ?", task.ID, []string{"PENDING_REQUEST", "PENDING"}).Updates(values).Error
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

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
