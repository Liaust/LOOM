package enrollmentflow

import (
	"regexp"
	"strings"
)

var sensitiveAssignmentPattern = regexp.MustCompile(`(?i)(token|credential|secret|password|api[_-]?key)=([^ \t\n\r,;]+)`)

func RedactString(value string, secrets Secrets) string {
	out := value
	for _, secret := range []string{secrets.EnrollmentToken, secrets.CredentialToken} {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		out = strings.ReplaceAll(out, secret, "[REDACTED]")
	}
	out = sensitiveAssignmentPattern.ReplaceAllString(out, "$1=[REDACTED]")
	return out
}
