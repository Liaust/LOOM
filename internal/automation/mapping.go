package automation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func NormalizeMappingProfile(raw json.RawMessage) (json.RawMessage, MappingProfile, error) {
	raw, err := normalizeJSONObject(raw, "mapping_profile")
	if err != nil {
		return nil, MappingProfile{}, err
	}
	profile := MappingProfile{
		FieldMappings: map[string]string{},
		Defaults:      map[string]any{},
		Required:      []string{},
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		return nil, MappingProfile{}, fmt.Errorf("mapping_profile is invalid JSON: %w", err)
	}
	if profile.FieldMappings == nil {
		profile.FieldMappings = map[string]string{}
	}
	if profile.Defaults == nil {
		profile.Defaults = map[string]any{}
	}
	if profile.Required == nil {
		profile.Required = []string{}
	}
	cleanMappings := map[string]string{}
	for dest, expr := range profile.FieldMappings {
		dest = strings.TrimSpace(dest)
		expr = strings.TrimSpace(expr)
		if dest == "" {
			return nil, MappingProfile{}, fmt.Errorf("mapping destination cannot be empty")
		}
		if expr == "" {
			return nil, MappingProfile{}, fmt.Errorf("mapping expression for %q cannot be empty", dest)
		}
		if strings.Contains(dest, "..") {
			return nil, MappingProfile{}, fmt.Errorf("mapping destination %q is invalid", dest)
		}
		if !strings.HasPrefix(expr, "$.") && !strings.HasPrefix(expr, "literal:") {
			return nil, MappingProfile{}, fmt.Errorf("mapping expression for %q must use $.path or literal:", dest)
		}
		cleanMappings[dest] = expr
	}
	profile.FieldMappings = cleanMappings
	cleanRequired := []string{}
	for _, field := range profile.Required {
		field = strings.TrimSpace(field)
		if field == "" {
			return nil, MappingProfile{}, fmt.Errorf("required mapping field cannot be empty")
		}
		if strings.Contains(field, "..") {
			return nil, MappingProfile{}, fmt.Errorf("required mapping field %q is invalid", field)
		}
		cleanRequired = append(cleanRequired, field)
	}
	profile.Required = cleanRequired
	normalized, err := json.Marshal(profile)
	if err != nil {
		return nil, MappingProfile{}, err
	}
	return json.RawMessage(normalized), profile, nil
}

func ApplyMappingPreview(mappingRaw json.RawMessage, input MappingPreviewInput) (json.RawMessage, []string, int, error) {
	_, profile, err := NormalizeMappingProfile(mappingRaw)
	if err != nil {
		return nil, nil, 0, err
	}
	body, err := rawObjectMap(input.BodyJSON, "body_json")
	if err != nil {
		return nil, nil, 0, err
	}
	headers, err := rawObjectMap(input.HeadersJSON, "headers_json")
	if err != nil {
		return nil, nil, 0, err
	}
	query, err := rawObjectMap(input.QueryJSON, "query_json")
	if err != nil {
		return nil, nil, 0, err
	}
	normalizedHeaders := map[string]any{}
	for key, value := range headers {
		normalizedHeaders[strings.ToLower(key)] = value
	}

	source := map[string]any{
		"body":    body,
		"headers": normalizedHeaders,
		"query":   query,
	}
	output := cloneObject(profile.Defaults)
	keys := make([]string, 0, len(profile.FieldMappings))
	for key := range profile.FieldMappings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, dest := range keys {
		value, ok, err := evaluateMappingExpression(profile.FieldMappings[dest], source)
		if err != nil {
			return nil, nil, 0, err
		}
		if !ok {
			continue
		}
		if err := setObjectPath(output, dest, value); err != nil {
			return nil, nil, 0, err
		}
	}
	missing := []string{}
	for _, required := range profile.Required {
		if _, ok, err := getObjectPath(output, required); err != nil {
			return nil, nil, 0, err
		} else if !ok {
			missing = append(missing, required)
		}
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return nil, nil, 0, err
	}
	return json.RawMessage(raw), missing, countLeaves(output), nil
}

func rawObjectMap(raw json.RawMessage, name string) (map[string]any, error) {
	normalized, err := normalizeJSONObject(raw, name)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(normalized, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func evaluateMappingExpression(expr string, source map[string]any) (any, bool, error) {
	expr = strings.TrimSpace(expr)
	if literal, ok := strings.CutPrefix(expr, "literal:"); ok {
		return literal, true, nil
	}
	if !strings.HasPrefix(expr, "$.") {
		return nil, false, fmt.Errorf("unsupported mapping expression: %s", expr)
	}
	return getObjectPath(source, strings.TrimPrefix(expr, "$."))
}

func cloneObject(value map[string]any) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func setObjectPath(target map[string]any, path string, value any) error {
	parts := cleanPathParts(path)
	if len(parts) == 0 {
		return fmt.Errorf("object path is required")
	}
	current := target
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
	return nil
}

func getObjectPath(source map[string]any, path string) (any, bool, error) {
	parts := cleanPathParts(path)
	if len(parts) == 0 {
		return nil, false, fmt.Errorf("object path is required")
	}
	var current any = source
	for _, part := range parts {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		value, ok := object[part]
		if !ok {
			return nil, false, nil
		}
		current = value
	}
	return current, true, nil
}

func cleanPathParts(path string) []string {
	raw := strings.Split(strings.Trim(strings.TrimSpace(path), "."), ".")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func countLeaves(value any) int {
	object, ok := value.(map[string]any)
	if !ok {
		return 1
	}
	total := 0
	for _, nested := range object {
		total += countLeaves(nested)
	}
	return total
}
