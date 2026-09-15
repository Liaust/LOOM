package supportbundle

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/redaction"
)

type BundleRedactionReport struct {
	Redactions  int `json:"redactions"`
	PathAliases int `json:"path_aliases"`
}

func RedactFile(file File, opts Options) (File, BundleRedactionReport) {
	report := BundleRedactionReport{}
	if isJSONFile(file) {
		var value any
		if err := json.Unmarshal(file.Data, &value); err == nil {
			redacted, childReport := redactValueForBundle(value, opts)
			report = childReport
			if data, err := marshalJSON(redacted); err == nil {
				file.Data = data
				return file, report
			}
		}
	}

	text, textReport := redaction.Text(string(file.Data), redaction.DefaultProfile())
	report.Redactions += textReport.Redactions
	text, aliases := AliasText(text, opts.PathAliases, opts.IncludeAbsolutePaths)
	report.PathAliases += aliases
	file.Data = []byte(text)
	return file, report
}

func RedactDiagnosticText(value string, opts Options) (string, BundleRedactionReport) {
	text, textReport := redaction.Text(value, redaction.DefaultProfile())
	text, aliases := AliasText(text, opts.PathAliases, opts.IncludeAbsolutePaths)
	return text, BundleRedactionReport{Redactions: textReport.Redactions, PathAliases: aliases}
}

func redactValueForBundle(value any, opts Options) (any, BundleRedactionReport) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		report := BundleRedactionReport{}
		for key, entry := range typed {
			if redaction.IsSensitiveKey(key, redaction.DefaultProfile()) {
				out[key] = redaction.Replacement
				report.Redactions++
				continue
			}
			redacted, childReport := redactValueForBundle(entry, opts)
			out[key] = redacted
			report.merge(childReport)
		}
		return out, report
	case []any:
		out := make([]any, len(typed))
		report := BundleRedactionReport{}
		for i, entry := range typed {
			redacted, childReport := redactValueForBundle(entry, opts)
			out[i] = redacted
			report.merge(childReport)
		}
		return out, report
	case string:
		text, textReport := redaction.Text(typed, redaction.DefaultProfile())
		text, aliases := AliasText(text, opts.PathAliases, opts.IncludeAbsolutePaths)
		return text, BundleRedactionReport{Redactions: textReport.Redactions, PathAliases: aliases}
	default:
		return value, BundleRedactionReport{}
	}
}

func (r *BundleRedactionReport) merge(other BundleRedactionReport) {
	r.Redactions += other.Redactions
	r.PathAliases += other.PathAliases
}

func isJSONFile(file File) bool {
	contentType := strings.ToLower(strings.TrimSpace(file.ContentType))
	if strings.Contains(contentType, "json") {
		return true
	}
	return strings.EqualFold(filepath.Ext(file.Path), ".json")
}
