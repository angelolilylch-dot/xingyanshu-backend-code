package foundation

import (
	"encoding/json"
	"testing"
)

func TestNormalizePlatformReturnAddress(t *testing.T) {
	value, code := normalizeConfigValue("PLATFORM_RETURN_ADDRESS", `{"recipientName":"星衍树售后中心","mobile":"400-800-1234","provinceCode":"440000","cityCode":"440100","districtCode":"440106","detailAddress":"测试收件地址"}`)
	if code != "" {
		t.Fatalf("normalize return address code=%s", code)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(value), &got); err != nil || got["detailAddress"] != "测试收件地址" {
		t.Fatalf("unexpected normalized value=%s err=%v", value, err)
	}
	if _, code = normalizeConfigValue("PLATFORM_RETURN_ADDRESS", `{"recipientName":"缺少其余字段"}`); code != "VALIDATION_FORMAT_INVALID" {
		t.Fatalf("incomplete address code=%s", code)
	}
}
