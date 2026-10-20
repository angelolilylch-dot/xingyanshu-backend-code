package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/modules/files"
	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/evidence"
	"xingyanshu-backend/internal/platform/idgen"
)

const (
	maxContractAttempts = 5
	maxEvidenceAttempts = 5
)

// ContractProcessor 处理支付事务之后的 PDF、凭证与存证；任何失败都不会回滚支付或有效授权。
type ContractProcessor struct {
	db       *gorm.DB
	files    *files.Service
	evidence *evidence.Service
}

func NewContractProcessor(db *gorm.DB, fileService *files.Service, evidenceService *evidence.Service) *ContractProcessor {
	return &ContractProcessor{db: db, files: fileService, evidence: evidenceService}
}

type contractTask struct {
	ID              uint64
	Version         uint32
	AuthorizationID uint64 `gorm:"column:authorization_id"`
	AuthOrderID     uint64 `gorm:"column:auth_order_id"`
	CreatorUserID   uint64 `gorm:"column:creator_user_id"`
	RetryCount      int    `gorm:"column:retry_count"`
}

type contractSource struct {
	AuthorizationID  uint64    `gorm:"column:authorization_id"`
	AuthorizationNo  string    `gorm:"column:authorization_no"`
	AuthOrderID      uint64    `gorm:"column:auth_order_id"`
	AuthOrderNo      string    `gorm:"column:auth_order_no"`
	PurchaseSnapshot []byte    `gorm:"column:purchase_snapshot"`
	PaymentNo        string    `gorm:"column:payment_no"`
	PaidAt           time.Time `gorm:"column:paid_at"`
}

// ProcessDueContracts 最多处理 limit 个到期任务。任务失败按 1/5/15/60 分钟退避，五次后标记失败。
func (p *ContractProcessor) ProcessDueContracts(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var tasks []contractTask
	if err := p.db.WithContext(ctx).Table("auth_contract_generation_task").Where("(status='PENDING' AND next_run_at<=?) OR (status='PROCESSING' AND updated_at<?)", time.Now(), time.Now().Add(-10*time.Minute)).Order("next_run_at,id").Limit(limit).Find(&tasks).Error; err != nil {
		return 0, err
	}
	processed := 0
	for _, task := range tasks {
		claimed := p.db.WithContext(ctx).Table("auth_contract_generation_task").Where("id=? AND version=? AND status IN ('PENDING','PROCESSING')", task.ID, task.Version).Updates(map[string]any{"status": "PROCESSING", "version": gorm.Expr("version+1"), "updated_at": time.Now()})
		if claimed.Error != nil {
			return processed, claimed.Error
		}
		if claimed.RowsAffected == 0 {
			continue
		}
		if err := p.generateContract(ctx, task); err != nil {
			if failureErr := p.recordContractFailure(ctx, task, err); failureErr != nil {
				return processed, failureErr
			}
			processed++
			continue
		}
		processed++
	}
	return processed, nil
}

func (p *ContractProcessor) generateContract(ctx context.Context, task contractTask) error {
	if p.files == nil || p.evidence == nil {
		return fmt.Errorf("contract dependencies unavailable")
	}
	var existing struct{ ID uint64 }
	if err := p.db.WithContext(ctx).Table("auth_contract").Select("id").Where("authorization_id=?", task.AuthorizationID).Take(&existing).Error; err == nil {
		return p.markContractTaskGenerated(ctx, task.ID, task.AuthOrderID)
	} else if err != gorm.ErrRecordNotFound {
		return err
	}
	var source contractSource
	err := p.db.WithContext(ctx).Table("creator_authorization a").Select("a.id AS authorization_id,a.authorization_no,a.auth_order_id,o.auth_order_no,o.purchase_snapshot,o.payment_no,o.paid_at").Joins("JOIN auth_order o ON o.id=a.auth_order_id AND o.status='PAID'").Where("a.id=? AND a.auth_order_id=? AND a.status='ACTIVE'", task.AuthorizationID, task.AuthOrderID).Take(&source).Error
	if err != nil {
		return err
	}
	var snapshot map[string]any
	if err := json.Unmarshal(source.PurchaseSnapshot, &snapshot); err != nil {
		return fmt.Errorf("decode order snapshot: %w", err)
	}
	contractNo := fmt.Sprintf("CONTRACT-%d", task.AuthorizationID)
	certificateNo := fmt.Sprintf("CERT-%d", task.AuthorizationID)
	contractPDF := buildContractPDF(contractLines(snapshot, contractNo, source.AuthorizationNo, source.AuthOrderNo, source.PaymentNo, source.PaidAt.Format(time.RFC3339)))
	certificatePDF := buildContractPDF(certificateLines(snapshot, certificateNo, source.AuthorizationNo, source.AuthOrderNo, source.PaidAt.Format(time.RFC3339)))
	contractFile, code, err := p.files.CreateGeneratedForCreator(ctx, task.CreatorUserID, "worker", contractNo+".pdf", "application/pdf", contractPDF)
	if err != nil || code != "" {
		return fmt.Errorf("create contract file: code=%s err=%w", code, err)
	}
	certificateFile, code, err := p.files.CreateGeneratedForCreator(ctx, task.CreatorUserID, "worker", certificateNo+".pdf", "application/pdf", certificatePDF)
	if err != nil || code != "" {
		return fmt.Errorf("create certificate file: code=%s err=%w", code, err)
	}
	contractFileID, contractHash, err := generatedFileValues(contractFile)
	if err != nil {
		return err
	}
	certificateFileID, certificateHash, err := generatedFileValues(certificateFile)
	if err != nil {
		return err
	}
	contractID := idgen.Next()
	certificateID := idgen.Next()
	now := time.Now()
	err = p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("auth_contract").Create(map[string]any{"id": contractID, "status": "ACTIVE", "version": 1, "created_by": task.CreatorUserID, "updated_by": task.CreatorUserID, "status_changed_at": now, "status_changed_by_type": "SYSTEM", "status_changed_by_id": "contract-worker", "contract_no": contractNo, "authorization_id": task.AuthorizationID, "contract_file_id": contractFileID, "contract_snapshot": source.PurchaseSnapshot, "effective_at": source.PaidAt, "file_hash": contractHash, "evidence_status": "PENDING_EVIDENCE"}).Error; err != nil {
			return err
		}
		if err := tx.Table("auth_certificate").Create(map[string]any{"id": certificateID, "status": "ACTIVE", "version": 1, "created_by": task.CreatorUserID, "updated_by": task.CreatorUserID, "status_changed_at": now, "status_changed_by_type": "SYSTEM", "status_changed_by_id": "contract-worker", "certificate_no": certificateNo, "authorization_id": task.AuthorizationID, "file_id": certificateFileID, "scope_summary": scopeSummary(snapshot), "file_hash": certificateHash}).Error; err != nil {
			return err
		}
		if err := p.files.LinkToObject(tx, task.CreatorUserID, contractFileID, contractID, "AUTH_CONTRACT", "CONTRACT_PDF", 0); err != nil {
			return err
		}
		if err := p.files.LinkToObject(tx, task.CreatorUserID, certificateFileID, certificateID, "AUTH_CERTIFICATE", "CERTIFICATE_PDF", 0); err != nil {
			return err
		}
		if err := tx.Table("auth_order").Where("id=?", task.AuthOrderID).Updates(map[string]any{"contract_status": "GENERATED", "evidence_status": "PENDING_EVIDENCE", "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Table("auth_contract_generation_task").Where("id=?", task.ID).Updates(map[string]any{"status": "GENERATED", "last_error": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := audit.WriteStatusHistory(tx, "AUTH_CONTRACT", contractID, "", "ACTIVE", "支付后异步生成正式合同", audit.Operator{Type: "SYSTEM", RoleCode: "SYSTEM"}); err != nil {
			return err
		}
		return audit.WriteLog(tx, "GENERATE_AUTH_CONTRACT", "AUTH_CONTRACT", contractID, nil, map[string]any{"authorizationId": fmt.Sprint(task.AuthorizationID), "contractFileId": fmt.Sprint(contractFileID), "certificateFileId": fmt.Sprint(certificateFileID)}, "SUCCESS", audit.Operator{Type: "SYSTEM", RoleCode: "SYSTEM"})
	})
	if err != nil {
		return err
	}

	evidenceID, evidenceCode, evidenceErr := p.evidence.Submit(ctx, evidence.SubmitRequest{ObjectType: "AUTH_CONTRACT", ObjectID: contractID, EvidenceType: "AUTHORIZATION_CONTRACT", HashValue: contractHash, RequestNo: fmt.Sprintf("AUTH-CONTRACT-%d", contractID)})
	if evidenceErr != nil || evidenceCode != "" {
		return p.markEvidenceRetrying(ctx, contractID, task.AuthOrderID, evidenceID, 1, evidenceErr)
	}
	var evidenceRow struct {
		Status          string
		ReceiptNo       string `gorm:"column:receipt_no"`
		TransactionHash string `gorm:"column:chain_transaction_hash"`
	}
	if err := p.db.WithContext(ctx).Table("evidence_record").Where("id=?", evidenceID).Take(&evidenceRow).Error; err != nil {
		return p.markEvidenceRetrying(ctx, contractID, task.AuthOrderID, evidenceID, 1, err)
	}
	status := "PENDING_EVIDENCE"
	if evidenceRow.Status == "SUCCESS" && evidenceRow.ReceiptNo != "" && evidenceRow.TransactionHash != "" {
		status = "EVIDENCED"
	}
	return p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("auth_contract").Where("id=?", contractID).Updates(map[string]any{"evidence_id": evidenceID, "evidence_status": status, "evidence_receipt_no": nullableString(evidenceRow.ReceiptNo), "evidence_transaction_hash": nullableString(evidenceRow.TransactionHash), "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return tx.Table("auth_order").Where("id=?", task.AuthOrderID).Updates(map[string]any{"evidence_status": status, "updated_at": time.Now()}).Error
	})
}

func (p *ContractProcessor) markContractTaskGenerated(ctx context.Context, taskID, orderID uint64) error {
	return p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("auth_contract_generation_task").Where("id=?", taskID).Updates(map[string]any{"status": "GENERATED", "last_error": nil, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return tx.Table("auth_order").Where("id=?", orderID).Updates(map[string]any{"contract_status": "GENERATED", "updated_at": time.Now()}).Error
	})
}

func (p *ContractProcessor) recordContractFailure(ctx context.Context, task contractTask, cause error) error {
	attempt := task.RetryCount + 1
	status := "PENDING"
	orderStatus := "PENDING_GENERATION"
	if attempt >= maxContractAttempts {
		status = "FAILED"
		orderStatus = "GENERATION_FAILED"
	}
	next := time.Now().Add(contractBackoff(attempt))
	return p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("auth_contract_generation_task").Where("id=?", task.ID).Updates(map[string]any{"status": status, "retry_count": attempt, "next_run_at": next, "last_error": shortError(cause), "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return tx.Table("auth_order").Where("id=?", task.AuthOrderID).Updates(map[string]any{"contract_status": orderStatus, "updated_at": time.Now()}).Error
	})
}

func (p *ContractProcessor) markEvidenceRetrying(ctx context.Context, contractID, orderID, evidenceID uint64, attempt int, cause error) error {
	status := "EVIDENCE_RETRYING"
	if attempt >= maxEvidenceAttempts {
		status = "EVIDENCE_FAILED"
	}
	var next any = time.Now().Add(evidenceBackoff(attempt))
	if status == "EVIDENCE_FAILED" {
		next = nil
	}
	return p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("auth_contract").Where("id=?", contractID).Updates(map[string]any{"evidence_id": nullableID(evidenceID), "evidence_status": status, "evidence_retry_count": attempt, "evidence_next_retry_at": next, "evidence_last_error": shortError(cause), "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return tx.Table("auth_order").Where("id=?", orderID).Updates(map[string]any{"evidence_status": status, "updated_at": time.Now()}).Error
	})
}

// RetryDueEvidence 补偿合同存证失败；最终失败只改变存证状态，不影响支付、授权、合同和凭证。
func (p *ContractProcessor) RetryDueEvidence(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var rows []struct {
		ContractID  uint64 `gorm:"column:contract_id"`
		EvidenceID  uint64 `gorm:"column:evidence_id"`
		AuthOrderID uint64 `gorm:"column:auth_order_id"`
		RetryCount  int    `gorm:"column:evidence_retry_count"`
	}
	err := p.db.WithContext(ctx).Table("auth_contract c").Select("c.id AS contract_id,c.evidence_id,a.auth_order_id,c.evidence_retry_count").Joins("JOIN creator_authorization a ON a.id=c.authorization_id").Where("c.evidence_status='EVIDENCE_RETRYING' AND c.evidence_next_retry_at<=?", time.Now()).Order("c.evidence_next_retry_at,c.id").Limit(limit).Find(&rows).Error
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		if row.RetryCount >= maxEvidenceAttempts {
			if err := p.markEvidenceRetrying(ctx, row.ContractID, row.AuthOrderID, row.EvidenceID, maxEvidenceAttempts, fmt.Errorf("maximum evidence retry attempts reached")); err != nil {
				return 0, err
			}
			continue
		}
		attempt := row.RetryCount + 1
		_, code, retryErr := p.evidence.Retry(ctx, row.EvidenceID)
		if retryErr != nil || code != "" {
			if err := p.markEvidenceRetrying(ctx, row.ContractID, row.AuthOrderID, row.EvidenceID, attempt, retryErr); err != nil {
				return 0, err
			}
		}
	}
	return len(rows), nil
}

func generatedFileValues(value map[string]any) (uint64, string, error) {
	idText, _ := value["id"].(string)
	hash, _ := value["fileHash"].(string)
	var id uint64
	if _, err := fmt.Sscan(idText, &id); err != nil || id == 0 || len(hash) != 64 {
		return 0, "", fmt.Errorf("invalid generated file response")
	}
	return id, hash, nil
}

func scopeSummary(snapshot map[string]any) string {
	auth, _ := snapshot["authorization"].(map[string]any)
	return fmt.Sprintf("授权方式：%v；期限：%v %v；地区：%v；场景：%v；内容类型：%v", auth["authorizationMode"], auth["termType"], auth["termValue"], auth["regionMode"], auth["scenes"], auth["contentType"])
}

func contractBackoff(attempt int) time.Duration {
	minutes := []int{1, 5, 15, 60, 240}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(minutes) {
		attempt = len(minutes)
	}
	return time.Duration(minutes[attempt-1]) * time.Minute
}

func evidenceBackoff(attempt int) time.Duration { return contractBackoff(attempt) }

func shortError(err error) string {
	if err == nil {
		return "external service unavailable"
	}
	value := strings.TrimSpace(err.Error())
	if len([]rune(value)) > 500 {
		value = string([]rune(value)[:500])
	}
	return value
}

func nullableID(id uint64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
