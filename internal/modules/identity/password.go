package identity

import (
	"regexp"
	"strings"
)

var (
	passwordUpper   = regexp.MustCompile(`[A-Z]`)
	passwordLower   = regexp.MustCompile(`[a-z]`)
	passwordDigit   = regexp.MustCompile(`[0-9]`)
	passwordSpecial = regexp.MustCompile(`[^A-Za-z0-9]`)
)

// IsValidPassword 要求密码包含大小写字母、数字和特殊字符，且不允许空格。
func IsValidPassword(value string) bool {
	return len(value) >= 8 && len(value) <= 64 &&
		!strings.ContainsAny(value, " \t\r\n") &&
		passwordUpper.MatchString(value) && passwordLower.MatchString(value) &&
		passwordDigit.MatchString(value) && passwordSpecial.MatchString(value)
}
