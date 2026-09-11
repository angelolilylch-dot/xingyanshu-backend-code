package apiresponse

import "testing"

func TestCamelizeMap(t *testing.T) {
	input := map[string]any{
		"ip_no":                  "IP-1",
		"dci_certificate_status": "MISSING",
		"nested_item":            map[string]any{"participant_type": "PLATFORM"},
	}
	result := CamelizeMap(input)
	if result["ipNo"] != "IP-1" || result["dciCertificateStatus"] != "MISSING" {
		t.Fatalf("unexpected scalar conversion: %#v", result)
	}
	nested, ok := result["nestedItem"].(map[string]any)
	if !ok || nested["participantType"] != "PLATFORM" {
		t.Fatalf("unexpected nested conversion: %#v", result)
	}
}
