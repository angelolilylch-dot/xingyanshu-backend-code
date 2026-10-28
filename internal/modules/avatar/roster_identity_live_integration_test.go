package avatar

import (
	"context"
	"os"
	"testing"
	"time"

	"xingyanshu-backend/internal/config"
)

// TestTencentPhoneVerificationLive is intentionally opt-in. It verifies the
// real Tencent request path with synthetic, format-valid data and never writes
// a roster member or plaintext identity data to the database.
func TestTencentPhoneVerificationLive(t *testing.T) {
	if os.Getenv("XINGYANSHU_RUN_ROSTER_IDV_LIVE_TEST") != "1" {
		t.Skip("set XINGYANSHU_RUN_ROSTER_IDV_LIVE_TEST=1 for an authorized Tencent PhoneVerification call")
	}
	settings := config.AvatarConfig{RosterIdentityProvider: "tencent_phone_verification"}
	settings.RosterIdentity = config.RosterIdentityConfig{
		SecretID:   os.Getenv("XINGYANSHU_ROSTER_IDV_SECRET_ID"),
		SecretKey:  os.Getenv("XINGYANSHU_ROSTER_IDV_SECRET_KEY"),
		Region:     "ap-guangzhou",
		VerifyMode: "0",
		Timeout:    15 * time.Second,
	}
	verifier := newRosterIdentityVerifier("test", settings)
	result, err := verifier.Verify(context.Background(), rosterIdentityInput{Name: "测试用户", IDNumber: "11010519491231002X", Mobile: "13800000000"})
	if result.ProviderCode != "TENCENT_PHONE_VERIFICATION" || result.ProviderRequestNo == "" || result.ResponseDigest == "" {
		t.Fatalf("Tencent PhoneVerification did not return an auditable response: result=%+v err=%v", result, err)
	}
	// Synthetic identity data is expected to be inconsistent or absent. A
	// business mismatch is still proof that the live provider was reached.
	if err == nil {
		t.Log("Tencent PhoneVerification returned a match for synthetic test data")
	}
}
