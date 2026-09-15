package supportbundle

import (
	"context"
	"net/url"
	"strings"
)

// ConfigSummary contains only fields explicitly classified as safe by the
// configuration package. Bundle redaction and path aliasing still run over the
// serialized summary as defense in depth.
type ConfigSummary struct {
	Fields map[string]string `json:"fields"`
}

func collectConfig(_ context.Context, collection CollectionContext) (CollectorOutput, error) {
	if len(collection.Options.SafeConfigFields) == 0 {
		return CollectorOutput{SkipReason: "safe_config_fields_unavailable"}, nil
	}
	return jsonSummary(
		"summaries/config.json",
		ConfigSummary{Fields: safeConfigSummaryFields(collection.Options.SafeConfigFields)},
		PrivacyDiagnosticSummary,
	)
}

func safeConfigSummaryFields(input map[string]string) map[string]string {
	fields := cloneStringMap(input)
	for key, value := range fields {
		if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(key)), "_url") {
			continue
		}
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			fields[key] = "configured_invalid_url"
			continue
		}
		parsed.User = nil
		parsed.Path = ""
		parsed.RawPath = ""
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		fields[key] = parsed.String()
	}
	return fields
}
