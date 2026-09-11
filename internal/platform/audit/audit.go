// Package audit 提供状态历史、操作审计和敏感字段脱敏的通用写入能力。
package audit

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"xingyanshu-backend/internal/platform/idgen"
)

// Operator 描述触发本次业务动作的账号或系统主体。
type Operator struct {
	Type     string
	ID       uint64
	RoleCode string
	ClientIP string
}

// WriteStatusHistory 在当前业务事务中记录一次状态变化。
func WriteStatusHistory(tx *gorm.DB, objectType string, objectID uint64, fromStatus, toStatus, reason string, operator Operator) error {
	return tx.Table("biz_status_history").Create(map[string]any{
		"id":            idgen.Next(),
		"created_by":    operator.ID,
		"object_type":   objectType,
		"object_id":     objectID,
		"from_status":   fromStatus,
		"to_status":     toStatus,
		"reason_code":   "STATE_CHANGED",
		"reason":        reason,
		"operator_type": operator.Type,
		"operator_id":   fmt.Sprint(operator.ID),
	}).Error
}

// WriteLog 记录操作快照；快照在写入前会递归脱敏，避免密码和令牌进入审计表。
func WriteLog(tx *gorm.DB, action, objectType string, objectID uint64, before, after any, result string, operator Operator) error {
	beforeJSON, err := json.Marshal(Sanitize(before))
	if err != nil {
		return err
	}
	afterJSON, err := json.Marshal(Sanitize(after))
	if err != nil {
		return err
	}
	return tx.Table("sys_audit_log").Create(map[string]any{
		"id":              idgen.Next(),
		"created_by":      operator.ID,
		"audit_no":        fmt.Sprintf("AUD-%d", idgen.Next()),
		"operator_id":     operator.ID,
		"operator_role":   operator.RoleCode,
		"action":          action,
		"object_type":     objectType,
		"object_id":       objectID,
		"before_snapshot": beforeJSON,
		"after_snapshot":  afterJSON,
		"result":          result,
		"client_ip":       operator.ClientIP,
	}).Error
}

// Sanitize 递归复制快照值，并替换常见敏感字段的原值。
func Sanitize(value any) any {
	switch item := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(item))
		for key, nested := range item {
			if sensitiveKey(key) {
				result[key] = "***"
				continue
			}
			result[key] = Sanitize(nested)
		}
		return result
	case []any:
		result := make([]any, len(item))
		for index, nested := range item {
			result[index] = Sanitize(nested)
		}
		return result
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "_", ""))
	for _, fragment := range []string{"password", "token", "secret", "credential", "idnumber", "bankaccount", "mobile", "phone"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

// ExpireBefore 清理指定时间前到期的幂等记录时可复用该业务时间工具。
func ExpireBefore(ttl time.Duration) time.Time {
	return time.Now().Add(ttl)
}
