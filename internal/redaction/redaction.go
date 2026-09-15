package redaction

import (
	"encoding/json"
	"regexp"
	"strings"
)

type Category string

const (
	Secret           Category = "secret"
	Credential       Category = "credential"
	Token            Category = "token"
	PrivateKey       Category = "private_key"
	Password         Category = "password"
	EnvSecret        Category = "env_secret"
	SensitivePath    Category = "sensitive_path"
	SensitivePayload Category = "sensitive_payload"
)

const Replacement = "[REDACTED]"

type Profile struct {
	KeyFragments []string
}

type Report struct {
	Redactions int              `json:"redactions"`
	Categories map[Category]int `json:"categories,omitempty"`
}

func DefaultProfile() Profile {
	return Profile{
		KeyFragments: []string{
			"password",
			"secret",
			"token",
			"private_key",
			"apikey",
			"api_key",
			"access_key",
			"credential",
		},
	}
}

func String(value string, categories ...Category) string {
	if len(categories) == 0 {
		return value
	}
	return Replacement
}

func Text(value string, profile Profile) (string, Report) {
	if len(profile.KeyFragments) == 0 {
		profile = DefaultProfile()
	}
	report := Report{}
	value = replacePattern(value, databaseURLCredentialsPattern, "${1}"+Replacement+"@", Credential, &report)
	value = replacePattern(value, authorizationHeaderPattern, "${1}"+Replacement, Token, &report)
	value = replacePattern(value, cookieHeaderPattern, "${1}"+Replacement, Credential, &report)
	value = replacePattern(value, sensitiveAssignmentPattern, "${1}"+Replacement, Secret, &report)
	value = replacePattern(value, privateKeyBlockPattern, Replacement, PrivateKey, &report)
	return value, report
}

func Map(value map[string]any, profile Profile) map[string]any {
	if len(profile.KeyFragments) == 0 {
		profile = DefaultProfile()
	}
	out := make(map[string]any, len(value))
	for key, entry := range value {
		if sensitiveKey(key, profile) {
			out[key] = Replacement
			continue
		}
		out[key] = redactValue(entry, profile)
	}
	return out
}

func MapWithReport(value map[string]any, profile Profile) (map[string]any, Report) {
	if len(profile.KeyFragments) == 0 {
		profile = DefaultProfile()
	}
	out := make(map[string]any, len(value))
	report := Report{}
	for key, entry := range value {
		if sensitiveKey(key, profile) {
			out[key] = Replacement
			report.add(Secret, 1)
			continue
		}
		redacted, childReport := ValueWithReport(entry, profile)
		out[key] = redacted
		report.merge(childReport)
	}
	return out, report
}

func JSON(raw json.RawMessage, profile Profile) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	redacted := redactValue(value, profile)
	payload, err := json.Marshal(redacted)
	if err != nil {
		return raw
	}
	return payload
}

func JSONWithReport(raw json.RawMessage, profile Profile) (json.RawMessage, Report) {
	if len(raw) == 0 {
		return raw, Report{}
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw, Report{}
	}
	redacted, report := ValueWithReport(value, profile)
	payload, err := json.Marshal(redacted)
	if err != nil {
		return raw, Report{}
	}
	return payload, report
}

func ValueWithReport(value any, profile Profile) (any, Report) {
	if len(profile.KeyFragments) == 0 {
		profile = DefaultProfile()
	}
	switch typed := value.(type) {
	case map[string]any:
		return MapWithReport(typed, profile)
	case []any:
		out := make([]any, len(typed))
		report := Report{}
		for i, entry := range typed {
			redacted, childReport := ValueWithReport(entry, profile)
			out[i] = redacted
			report.merge(childReport)
		}
		return out, report
	case string:
		return Text(typed, profile)
	default:
		return value, Report{}
	}
}

func redactValue(value any, profile Profile) any {
	switch typed := value.(type) {
	case map[string]any:
		return Map(typed, profile)
	case []any:
		out := make([]any, len(typed))
		for i, entry := range typed {
			out[i] = redactValue(entry, profile)
		}
		return out
	default:
		return value
	}
}

func sensitiveKey(key string, profile Profile) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	for _, fragment := range profile.KeyFragments {
		if strings.Contains(normalized, strings.ToLower(fragment)) {
			return true
		}
	}
	return false
}

func IsSensitiveKey(key string, profile Profile) bool {
	if len(profile.KeyFragments) == 0 {
		profile = DefaultProfile()
	}
	return sensitiveKey(key, profile)
}

func (r *Report) add(category Category, count int) {
	if count <= 0 {
		return
	}
	r.Redactions += count
	if r.Categories == nil {
		r.Categories = map[Category]int{}
	}
	r.Categories[category] += count
}

func (r *Report) merge(other Report) {
	if other.Redactions == 0 {
		return
	}
	r.Redactions += other.Redactions
	if r.Categories == nil {
		r.Categories = map[Category]int{}
	}
	for category, count := range other.Categories {
		r.Categories[category] += count
	}
}

func replacePattern(value string, pattern *regexp.Regexp, replacement string, category Category, report *Report) string {
	matches := pattern.FindAllStringIndex(value, -1)
	if len(matches) == 0 {
		return value
	}
	report.add(category, len(matches))
	return pattern.ReplaceAllString(value, replacement)
}

var (
	databaseURLCredentialsPattern = regexp.MustCompile(`(?i)\b((?:postgres(?:ql)?|mysql|mariadb|mongodb(?:\+srv)?|redis|amqp|nats)://)([^/\s:@]+):([^@\s/]+)@`)
	authorizationHeaderPattern    = regexp.MustCompile(`(?im)\b(authorization\s*:\s*)(?:bearer\s+|basic\s+)?[^\s]+`)
	cookieHeaderPattern           = regexp.MustCompile(`(?im)\b((?:set-cookie|cookie)\s*:\s*).+$`)
	sensitiveAssignmentPattern    = regexp.MustCompile(`(?i)\b((?:password|passphrase|secret|token|api[_-]?key|apikey|access[_-]?key|credential|private[_-]?key|wireguard[_-]?private[_-]?key|enrollment[_-]?secret|aws[_-]?secret[_-]?access[_-]?key)\b\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	privateKeyBlockPattern        = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.+?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
)
