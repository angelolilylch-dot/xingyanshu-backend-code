// Package idempotency 提供基于 MySQL 的写接口幂等执行器。
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/platform/audit"
	"xingyanshu-backend/internal/platform/idgen"
)

const defaultTTL = 24 * time.Hour

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{15,127}$`)

var (
	ErrKeyRequired = fmt.Errorf("idempotency key is required")
	ErrKeyInvalid  = fmt.Errorf("idempotency key is invalid")
	ErrConflict    = fmt.Errorf("idempotency key conflicts with another request")
)

// Scope 唯一标识一次写接口所属的调用主体和接口。
type Scope struct {
	SubjectType string
	SubjectID   uint64
	Method      string
	Path        string
}

// Result 是业务操作可被持久化并在重复请求中复用的响应内容。
type Result struct {
	HTTPStatus int
	Code       string
	Message    string
	Data       any
}

// Handler 在同一个数据库事务中完成主业务写入、状态历史、审计和首次结果准备。
type Handler func(tx *gorm.DB) (Result, error)

type record struct {
	ID              uint64          `gorm:"column:id;primaryKey"`
	SubjectType     string          `gorm:"column:subject_type"`
	SubjectID       uint64          `gorm:"column:subject_id"`
	RequestMethod   string          `gorm:"column:request_method"`
	RequestPath     string          `gorm:"column:request_path"`
	IdempotencyKey  string          `gorm:"column:idempotency_key"`
	RequestHash     string          `gorm:"column:request_hash"`
	Status          string          `gorm:"column:status"`
	ResponseStatus  int             `gorm:"column:response_status"`
	ResponseCode    string          `gorm:"column:response_code"`
	ResponseMessage string          `gorm:"column:response_message"`
	ResponseData    json.RawMessage `gorm:"column:response_data"`
	CompletedAt     *time.Time      `gorm:"column:completed_at"`
	ExpiresAt       time.Time       `gorm:"column:expires_at"`
}

func (record) TableName() string { return "api_idempotency_record" }

// Execute 对同一主体、接口和幂等键只执行一次业务处理；相同请求返回首次结果，不同请求返回冲突。
func Execute(ctx context.Context, db *gorm.DB, scope Scope, key string, rawRequest []byte, handler Handler) (Result, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Result{}, false, ErrKeyRequired
	}
	if !keyPattern.MatchString(key) || scope.SubjectType == "" || scope.SubjectID == 0 || scope.Method == "" || scope.Path == "" {
		return Result{}, false, ErrKeyInvalid
	}
	hash := requestHash(rawRequest)
	result := Result{}
	replayed := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		item := record{ID: idgen.Next(), SubjectType: scope.SubjectType, SubjectID: scope.SubjectID, RequestMethod: strings.ToUpper(scope.Method), RequestPath: scope.Path, IdempotencyKey: key, RequestHash: hash, Status: "PROCESSING", ExpiresAt: time.Now().Add(defaultTTL)}
		if err := tx.Create(&item).Error; err != nil {
			if !isDuplicateError(err) {
				return err
			}
			return loadReplay(tx, scope, key, hash, &result, &replayed)
		}

		operationResult, err := handler(tx)
		if err != nil {
			return err
		}
		dataJSON, err := json.Marshal(audit.Sanitize(operationResult.Data))
		if err != nil {
			return err
		}
		if operationResult.HTTPStatus == 0 {
			operationResult.HTTPStatus = 200
		}
		if operationResult.Code == "" {
			operationResult.Code = "OK"
		}
		if operationResult.Message == "" {
			operationResult.Message = "成功"
		}
		if err := tx.Model(&record{}).Where("id = ?", item.ID).Updates(map[string]any{"status": "COMPLETED", "response_status": operationResult.HTTPStatus, "response_code": operationResult.Code, "response_message": operationResult.Message, "response_data": json.RawMessage(dataJSON), "completed_at": time.Now()}).Error; err != nil {
			return err
		}
		result = operationResult
		return nil
	})
	return result, replayed, err
}

func loadReplay(tx *gorm.DB, scope Scope, key, hash string, result *Result, replayed *bool) error {
	var item record
	if err := tx.Where("subject_type = ? AND subject_id = ? AND request_method = ? AND request_path = ? AND idempotency_key = ?", scope.SubjectType, scope.SubjectID, strings.ToUpper(scope.Method), scope.Path, key).First(&item).Error; err != nil {
		return err
	}
	if item.RequestHash != hash {
		return ErrConflict
	}
	if item.Status != "COMPLETED" {
		return fmt.Errorf("idempotency request is still processing")
	}
	var data any
	if len(item.ResponseData) > 0 && string(item.ResponseData) != "null" {
		if err := json.Unmarshal(item.ResponseData, &data); err != nil {
			return err
		}
	}
	*result = Result{HTTPStatus: item.ResponseStatus, Code: item.ResponseCode, Message: item.ResponseMessage, Data: data}
	*replayed = true
	return nil
}

func requestHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func isDuplicateError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "duplicate")
}
