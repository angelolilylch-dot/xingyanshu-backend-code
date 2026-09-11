package identity

import "testing"

func TestIsValidPassword(t *testing.T) {
	valid := []string{"Abcd@123", "Xy9!long-password"}
	for _, value := range valid {
		if !IsValidPassword(value) { t.Errorf("expected valid password: %q", value) }
	}
	invalid := []string{"Abcd1234", "abcd@123", "ABCD@123", "Abcd@abcd", "Abcd@12", "Abcd @123", "Abcd@123\n"}
	for _, value := range invalid {
		if IsValidPassword(value) { t.Errorf("expected invalid password: %q", value) }
	}
}
