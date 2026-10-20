package plans

import "testing"

func TestValidateParticipants(t *testing.T) {
	if !validateParticipants([]Participant{{ParticipantType: "RIGHT_HOLDER"}, {ParticipantType: "CITY_OPERATOR"}, {ParticipantType: "PLATFORM"}}) {
		t.Fatal("expected standard participant codes to be accepted")
	}
	if validateParticipants([]Participant{{ParticipantType: "IP_HOLDER"}}) {
		t.Fatal("legacy IP_HOLDER must be rejected")
	}
}

func TestValidateAuthFields(t *testing.T) {
	month := 12
	cases := []struct {
		name                               string
		mode, termType, validFrom, validTo string
		regionMode, contentType            string
		termValue                          *int
		regionCodes, scenes                []string
		quota                              int
		want                               string
	}{
		{"普通授权按月全国", "NORMAL_SHARE", "MONTH", "", "", "NATIONWIDE", "PRODUCT_ONLY", &month, nil, []string{"PRODUCT_SALE"}, 10, ""},
		{"日期范围指定地区", "NORMAL_SHARE", "DATE_RANGE", "2026-01-01", "2026-12-31", "DESIGNATED", "PRODUCT_AND_ACTIVITY", nil, []string{"350200", "440100"}, []string{"ACTIVITY", "PROMOTION"}, 3, ""},
		{"旧期限码拒绝", "NORMAL_SHARE", "MONTHS", "", "", "NATIONWIDE", "PRODUCT_ONLY", &month, nil, []string{"PRODUCT_SALE"}, 1, "VALIDATION_FORMAT_INVALID"},
		{"旧内容码拒绝", "NORMAL_SHARE", "MONTH", "", "", "NATIONWIDE", "PRODUCT", &month, nil, []string{"PRODUCT_SALE"}, 1, "VALIDATION_FORMAT_INVALID"},
		{"指定地区缺地区编码", "NORMAL_SHARE", "MONTH", "", "", "DESIGNATED", "PRODUCT_ONLY", &month, nil, []string{"PRODUCT_SALE"}, 1, "VALIDATION_FORMAT_INVALID"},
		{"日期范围错误填写期限值", "NORMAL_SHARE", "DATE_RANGE", "2026-01-01", "2026-12-31", "NATIONWIDE", "PRODUCT_ONLY", &month, nil, []string{"PRODUCT_SALE"}, 1, "VALIDATION_FORMAT_INVALID"},
		{"独家买断名额不是一", "EXCLUSIVE_BUYOUT", "YEAR", "", "", "NATIONWIDE", "PRODUCT_ONLY", &month, nil, []string{"PRODUCT_SALE"}, 2, "VALIDATION_FORMAT_INVALID"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := validateAuthFields(item.mode, item.termType, item.termValue, item.validFrom, item.validTo, item.regionMode, item.regionCodes, item.scenes, item.contentType, &item.quota); got != item.want {
				t.Fatalf("validateAuthFields()=%q, want %q", got, item.want)
			}
		})
	}
	if got := validateAuthFields("NORMAL_SHARE", "MONTH", &month, "", "", "NATIONWIDE", nil, []string{"PRODUCT_SALE"}, "PRODUCT_ONLY", nil); got != "" {
		t.Fatalf("unlimited normal authorization rejected: %q", got)
	}
}
