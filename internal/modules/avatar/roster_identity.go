package avatar

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
	"xingyanshu-backend/internal/platform/idgen"
)

// rosterIdentityVerifier represents the provider call performed while an
// operator imports a roster. Inputs are plaintext only in process memory; the
// service must never log them. Persisting the ID number happens after PASS.
type rosterIdentityVerifier interface {
	Verify(context.Context, rosterIdentityInput) (rosterIdentityResult, error)
}

type rosterIdentityInput struct {
	Name, IDNumber, Mobile string
}
type rosterIdentityResult struct {
	ProviderCode, ProviderRequestNo, ResponseDigest string
}

type unavailableRosterIdentityVerifier struct{}

func (unavailableRosterIdentityVerifier) Verify(context.Context, rosterIdentityInput) (rosterIdentityResult, error) {
	return rosterIdentityResult{}, fmt.Errorf("roster identity provider is not configured")
}

type mockRosterIdentityVerifier struct{}

func (mockRosterIdentityVerifier) Verify(_ context.Context, input rosterIdentityInput) (rosterIdentityResult, error) {
	if strings.TrimSpace(input.Name) == "" || !mobilePattern.MatchString(input.Mobile) || !validChineseIDNumber(input.IDNumber) {
		return rosterIdentityResult{}, fmt.Errorf("three-factor identity verification failed")
	}
	digest := sha256.Sum256([]byte(input.Name + ":" + input.IDNumber + ":" + input.Mobile + ":PASSED"))
	requestNo := fmt.Sprintf("MOCK-ROSTER-IDV-%d", idgen.Next())
	return rosterIdentityResult{ProviderCode: "MOCK", ProviderRequestNo: requestNo, ResponseDigest: fmt.Sprintf("%x", digest[:])}, nil
}

func newRosterIdentityVerifier(environment, provider string) rosterIdentityVerifier {
	if environment == "test" && (strings.TrimSpace(provider) == "" || strings.EqualFold(strings.TrimSpace(provider), "mock")) {
		return mockRosterIdentityVerifier{}
	}
	return unavailableRosterIdentityVerifier{}
}

var chineseIDNumberPattern = regexp.MustCompile(`^(?:\d{15}|\d{17}[0-9Xx])$`)

func validChineseIDNumber(value string) bool {
	value = strings.ToUpper(strings.TrimSpace(value))
	if !chineseIDNumberPattern.MatchString(value) {
		return false
	}
	if len(value) == 15 {
		return true
	}
	weights := [...]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	checks := "10X98765432"
	sum := 0
	for i, weight := range weights {
		sum += int(value[i]-'0') * weight
	}
	return value[17] == checks[sum%11]
}

// ProcessPendingRosterIdentity is the second stage of bulk roster import.
// It decrypts one already-encrypted record only for the duration of the
// provider request, then writes a result with no plaintext identity fields.
func (s *Service) ProcessPendingRosterIdentity(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 20
	}
	if _, unavailable := s.rosterIDV.(unavailableRosterIdentityVerifier); unavailable {
		return 0, nil
	}
	var rows []struct {
		ID, RosterBatchID                        uint64
		NameCipher, IDNumberCipher, MobileCipher []byte
	}
	if err := s.db.WithContext(ctx).Table("avatar_roster_member").Select("id,roster_batch_id,name_cipher,id_number_cipher,mobile_cipher").Where("status='PENDING_IDENTITY'").Order("created_at ASC").Limit(limit).Find(&rows).Error; err != nil {
		return 0, err
	}
	processed := 0
	for _, row := range rows {
		if err := s.processPendingRosterIdentity(ctx, row); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func (s *Service) processPendingRosterIdentity(ctx context.Context, row struct {
	ID, RosterBatchID                        uint64
	NameCipher, IDNumberCipher, MobileCipher []byte
}) error {
	name, nameErr := decrypt(s.auth.JWTSecret, row.NameCipher)
	idNumber, idErr := decrypt(s.auth.JWTSecret, row.IDNumberCipher)
	mobile, mobileErr := decrypt(s.auth.JWTSecret, row.MobileCipher)
	if nameErr != nil || idErr != nil || mobileErr != nil {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return tx.Table("avatar_roster_member").Where("id=? AND status='PENDING_IDENTITY'", row.ID).Updates(map[string]any{"status": "INACTIVE", "status_reason_code": "IDENTITY_DATA_DECRYPT_FAILED", "status_reason": "实名核验数据不可用", "updated_at": time.Now(), "status_changed_at": time.Now(), "status_changed_by_type": "SYSTEM"}).Error
		})
	}
	result, err := s.rosterIDV.Verify(ctx, rosterIdentityInput{Name: name, IDNumber: idNumber, Mobile: mobile})
	now := time.Now()
	updates := map[string]any{"updated_at": now, "status_changed_at": now, "status_changed_by_type": "THIRD_PARTY"}
	if err != nil {
		updates["status"] = "INACTIVE"
		updates["status_reason_code"] = "IDENTITY_VERIFICATION_FAILED"
		updates["status_reason"] = "实名核验未通过，请核对姓名、身份证号和手机号"
	} else {
		updates["status"] = "ACTIVE"
		updates["identity_provider_code"] = result.ProviderCode
		updates["identity_provider_request_no"] = result.ProviderRequestNo
		updates["identity_response_digest"] = result.ResponseDigest
		updates["identity_verified_at"] = now
	}
	if dbErr := s.db.WithContext(ctx).Table("avatar_roster_member").Where("id=? AND status='PENDING_IDENTITY'", row.ID).Updates(updates).Error; dbErr != nil {
		return dbErr
	}
	if err != nil {
		_ = s.db.WithContext(ctx).Table("avatar_roster_import_error").Create(map[string]any{"id": idgen.Next(), "roster_batch_id": row.RosterBatchID, "row_no": 0, "error_code": "IDENTITY_VERIFICATION_FAILED", "error_message": "实名核验未通过，请核对姓名、身份证号和手机号"}).Error
	}
	return s.refreshRosterBatchIdentityStatus(ctx, row.RosterBatchID)
}

func (s *Service) refreshRosterBatchIdentityStatus(ctx context.Context, batchID uint64) error {
	var batch struct{ TotalCount int }
	if err := s.db.WithContext(ctx).Table("avatar_roster_batch").Select("total_count").Where("id=?", batchID).Take(&batch).Error; err != nil {
		return err
	}
	var counts []struct {
		Status string
		Count  int
	}
	if err := s.db.WithContext(ctx).Table("avatar_roster_member").Select("status,COUNT(*) count").Where("roster_batch_id=?", batchID).Group("status").Scan(&counts).Error; err != nil {
		return err
	}
	active, pending := 0, 0
	for _, item := range counts {
		if item.Status == "ACTIVE" {
			active = item.Count
		} else if item.Status == "PENDING_IDENTITY" {
			pending += item.Count
		}
	}
	failed := batch.TotalCount - active - pending
	status := "PENDING_IDENTITY"
	if pending == 0 && failed == 0 {
		status = "COMPLETED"
	} else if pending == 0 && active == 0 {
		status = "FAILED"
	} else if pending == 0 {
		status = "PARTIAL_FAILED"
	}
	return s.db.WithContext(ctx).Table("avatar_roster_batch").Where("id=?", batchID).Updates(map[string]any{"status": status, "success_count": active, "failed_count": failed, "updated_at": time.Now(), "status_changed_at": time.Now(), "status_changed_by_type": "SYSTEM"}).Error
}
