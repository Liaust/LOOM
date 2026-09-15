package serviceregistry

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"loom.local/loom/internal/redaction"
)

const (
	DefaultLogLines      = 200
	DefaultLogBytes      = 32 * 1024
	DefaultLogLineBytes  = 2 * 1024
	DefaultLogAgeSeconds = 60 * 60
	MaximumLogLines      = 500
	MaximumLogBytes      = 64 * 1024
	MaximumLogLineBytes  = 4 * 1024
	MaximumLogAgeSeconds = 24 * 60 * 60
	maximumMessageBytes  = 4 * 1024

	RedactedPath    = "[REDACTED_PATH]"
	RedactedURI     = "[REDACTED_URI]"
	RedactedControl = "[REDACTED_CONTROL]"
)

var (
	serviceURIPattern            = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	servicePOSIXPathPattern      = regexp.MustCompile(`(^|[\s="'(:\[\{])/(?:[^ \t\r\n"'(),;\]\}]+)`)
	serviceWindowsPathPattern    = regexp.MustCompile(`(?i)\b[A-Z]:\\[^\s"'<>\]\}]+`)
	serviceWindowsUNCPathPattern = regexp.MustCompile(`\\\\[A-Za-z0-9._-]+\\[^\s"'<>\]\}]+`)
)

type LogLimits struct {
	MaxLines      int `json:"max_lines" yaml:"max_lines"`
	MaxBytes      int `json:"max_bytes" yaml:"max_bytes"`
	MaxLineBytes  int `json:"max_line_bytes" yaml:"max_line_bytes"`
	MaxAgeSeconds int `json:"max_age_seconds" yaml:"max_age_seconds"`
}

func ValidateLogSelectors(lines, maxBytes, maxAgeSeconds int, limits LogLimits) error {
	if lines < 0 || maxBytes < 0 || maxAgeSeconds < 0 {
		return fmt.Errorf("log selectors cannot be negative")
	}
	normalized, err := limits.Normalize()
	if err != nil {
		return err
	}
	if lines > normalized.MaxLines || maxBytes > normalized.MaxBytes || maxAgeSeconds > normalized.MaxAgeSeconds {
		return fmt.Errorf("log request exceeds reviewed bounds")
	}
	return nil
}

func (limits LogLimits) Normalize() (LogLimits, error) {
	if limits.MaxLines < 0 || limits.MaxBytes < 0 || limits.MaxLineBytes < 0 || limits.MaxAgeSeconds < 0 {
		return LogLimits{}, fmt.Errorf("log limits cannot be negative")
	}
	if limits.MaxLines == 0 {
		limits.MaxLines = DefaultLogLines
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = DefaultLogBytes
	}
	if limits.MaxLineBytes == 0 {
		limits.MaxLineBytes = DefaultLogLineBytes
	}
	if limits.MaxAgeSeconds == 0 {
		limits.MaxAgeSeconds = DefaultLogAgeSeconds
	}
	if limits.MaxLines > MaximumLogLines {
		return LogLimits{}, fmt.Errorf("max_lines exceeds %d", MaximumLogLines)
	}
	if limits.MaxBytes > MaximumLogBytes {
		return LogLimits{}, fmt.Errorf("max_bytes exceeds %d", MaximumLogBytes)
	}
	if limits.MaxLineBytes > MaximumLogLineBytes || limits.MaxLineBytes > limits.MaxBytes {
		return LogLimits{}, fmt.Errorf("max_line_bytes exceeds its bounded maximum")
	}
	if limits.MaxAgeSeconds > MaximumLogAgeSeconds {
		return LogLimits{}, fmt.Errorf("max_age_seconds exceeds %d", MaximumLogAgeSeconds)
	}
	return limits, nil
}

type ResultRedactionReport struct {
	Secrets        int  `json:"secrets"`
	Paths          int  `json:"paths"`
	URIs           int  `json:"uris"`
	Controls       int  `json:"controls"`
	LinesDropped   int  `json:"lines_dropped"`
	BytesTruncated bool `json:"bytes_truncated"`
}

func RedactManagerResult(result ManagerResult, limits LogLimits) (ManagerResult, ResultRedactionReport, error) {
	if _, err := StandardOperationPolicy(result.Operation); err != nil {
		return ManagerResult{}, ResultRedactionReport{}, err
	}
	if !ValidObservedProcessState(result.ProcessState) {
		return ManagerResult{}, ResultRedactionReport{}, fmt.Errorf("invalid observed process state %q", result.ProcessState)
	}
	limits, err := limits.Normalize()
	if err != nil {
		return ManagerResult{}, ResultRedactionReport{}, err
	}

	report := ResultRedactionReport{}
	result.Message = redactServiceText(result.Message, maximumMessageBytes, &report)
	rawLines := result.LogLines
	if len(rawLines) > limits.MaxLines {
		report.LinesDropped += len(rawLines) - limits.MaxLines
		rawLines = rawLines[:limits.MaxLines]
		result.Truncated = true
	}
	result.LogLines = make([]string, 0, len(rawLines))
	remaining := limits.MaxBytes
	for index, line := range rawLines {
		line = redactServiceText(line, limits.MaxLineBytes, &report)
		if len(line) > remaining {
			line = truncateUTF8(line, remaining)
			report.BytesTruncated = true
			result.Truncated = true
		}
		if line != "" || remaining > 0 {
			result.LogLines = append(result.LogLines, line)
		}
		remaining -= len(line)
		if remaining <= 0 {
			report.LinesDropped += len(rawLines) - index - 1
			if index < len(rawLines)-1 {
				result.Truncated = true
			}
			break
		}
	}
	if report.BytesTruncated {
		result.Truncated = true
	}
	return result, report, nil
}

func redactServiceText(value string, maxBytes int, report *ResultRedactionReport) string {
	value = replaceControlCharacters(value, report)
	redacted, secretReport := redaction.Text(value, redaction.DefaultProfile())
	report.Secrets += secretReport.Redactions
	value = redacted
	value = replaceAndCount(value, serviceURIPattern, RedactedURI, &report.URIs)
	value = replacePathAndCount(value, servicePOSIXPathPattern, &report.Paths)
	value = replaceAndCount(value, serviceWindowsUNCPathPattern, RedactedPath, &report.Paths)
	value = replaceAndCount(value, serviceWindowsPathPattern, RedactedPath, &report.Paths)
	if len(value) > maxBytes {
		value = truncateUTF8(value, maxBytes)
		report.BytesTruncated = true
	}
	return value
}

func replaceControlCharacters(value string, report *ResultRedactionReport) string {
	var builder strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) {
			builder.WriteByte(' ')
			builder.WriteString(RedactedControl)
			builder.WriteByte(' ')
			report.Controls++
			continue
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

func replaceAndCount(value string, pattern *regexp.Regexp, replacement string, count *int) string {
	matches := pattern.FindAllStringIndex(value, -1)
	*count += len(matches)
	return pattern.ReplaceAllString(value, replacement)
}

func replacePathAndCount(value string, pattern *regexp.Regexp, count *int) string {
	matches := pattern.FindAllStringSubmatchIndex(value, -1)
	*count += len(matches)
	return pattern.ReplaceAllString(value, `${1}`+RedactedPath)
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
