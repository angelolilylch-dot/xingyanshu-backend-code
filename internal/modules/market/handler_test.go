package market

import "testing"

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" CULTURE, , REGION ,COMMERCIAL ")
	want := []string{"CULTURE", "REGION", "COMMERCIAL"}
	if len(got) != len(want) {
		t.Fatalf("splitCSV length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitCSV[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestStringID(t *testing.T) {
	if got := stringID(1789463518845457200); got != "1789463518845457200" {
		t.Fatalf("stringID lost precision: %q", got)
	}
	if got := stringID(0); got != "" {
		t.Fatalf("stringID(0) = %q, want empty", got)
	}
}

func TestCanPurchase(t *testing.T) {
	cases := []struct {
		name                         string
		identityVerified, authorized bool
		mode                         string
		quota                        *int
		sold                         int64
		want                         bool
	}{
		{"未实名", false, false, "NORMAL_SHARE", intPtr(10), 0, false},
		{"已有有效授权", true, true, "NORMAL_SHARE", intPtr(10), 0, false},
		{"普通授权名额售罄", true, false, "NORMAL_SHARE", intPtr(10), 10, false},
		{"普通授权可购买", true, false, "NORMAL_SHARE", intPtr(10), 9, true},
		{"普通授权不限名额", true, false, "NORMAL_SHARE", nil, 999, true},
		{"独家买断可购买", true, false, "EXCLUSIVE_BUYOUT", intPtr(1), 0, true},
		{"独家买断已售罄", true, false, "EXCLUSIVE_BUYOUT", intPtr(1), 1, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := canPurchase(item.identityVerified, item.authorized, item.mode, item.quota, item.sold); got != item.want {
				t.Fatalf("canPurchase() = %v, want %v", got, item.want)
			}
		})
	}
}

func intPtr(value int) *int { return &value }
