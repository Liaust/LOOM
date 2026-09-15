package minidashboard

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	MaxNodeLabelLength = 20
	MaxNodeKeyLength   = 64
	MaxPublicLabel     = 64
)

var (
	safeTokenPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
	uriPattern       = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://`)
	windowsPath      = regexp.MustCompile(`(?i)\b[a-z]:\\`)
	pathBoundary     = `[[:space:]\[\](){},:;='"<>]`
	unixPath         = regexp.MustCompile(`(^|` + pathBoundary + `)/(?:[^[:space:]]+/)*[^[:space:]]*`)
	uncPath          = regexp.MustCompile(`(^|` + pathBoundary + `)\\\\[^\\[:space:]]+\\[^\\[:space:]]+`)
)

func SanitizeLabel(value string, limit int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("label is required")
	}
	if limit <= 0 || limit > MaxPublicLabel {
		limit = MaxPublicLabel
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("label contains control characters")
		}
	}
	if uriPattern.MatchString(value) || windowsPath.MatchString(value) || unixPath.MatchString(value) || uncPath.MatchString(value) {
		return "", fmt.Errorf("label contains a path or URI")
	}
	runes := []rune(value)
	if len(runes) > limit {
		return strings.TrimSpace(string(runes[:limit-1])) + "…", nil
	}
	return value, nil
}

func SafeLabel(value, fallback string, limit int) string {
	clean, err := SanitizeLabel(value, limit)
	if err == nil {
		return clean
	}
	clean, err = SanitizeLabel(fallback, limit)
	if err == nil {
		return clean
	}
	return "Unavailable"
}

func SafeToken(value, fallback string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit || !safeTokenPattern.MatchString(value) {
		return fallback
	}
	return value
}

func ValidCondition(condition Condition) bool {
	if !safeTokenPattern.MatchString(condition.Code) || !validSeverity(condition.Severity) {
		return false
	}
	_, err := SanitizeLabel(condition.Label, MaxPublicLabel)
	return err == nil
}
