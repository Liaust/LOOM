package bootstrapssh

import (
	"regexp"
	"strings"
)

var enrollmentSecretPattern = regexp.MustCompile(`\b(node_enroll|node_cred)_[A-Za-z0-9._-]+`)

func redactedSpec(spec Spec) Spec {
	spec.SSHKeyPath = ""
	spec.MainSSHKeyPath = ""
	return spec
}

func RedactString(value string, spec Spec) string {
	for _, key := range []string{spec.SSHKeyPath, spec.MainSSHKeyPath} {
		key = strings.TrimSpace(key)
		if key != "" {
			value = strings.ReplaceAll(value, key, "[redacted-ssh-key-path]")
		}
	}
	return RedactEnrollmentSecrets(value)
}

func RedactEnrollmentSecrets(value string) string {
	return enrollmentSecretPattern.ReplaceAllString(value, "[redacted-secret]")
}
