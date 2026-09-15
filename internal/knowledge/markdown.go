package knowledge

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type TextDocument struct {
	Text                   string                  `json:"text"`
	Frontmatter            map[string]any          `json:"frontmatter,omitempty"`
	FrontmatterRaw         string                  `json:"frontmatter_raw,omitempty"`
	AbsoluteTimeCandidates []AbsoluteTimeCandidate `json:"absolute_time_candidates,omitempty"`
	AbsoluteTimeWarnings   []AbsoluteTimeWarning   `json:"absolute_time_warnings,omitempty"`
	Headings               []TextHeading           `json:"headings,omitempty"`
	Links                  []ExtractedLink         `json:"links,omitempty"`
	Warnings               []string                `json:"warnings,omitempty"`
}

type TextHeading struct {
	Level          int    `json:"level"`
	Text           string `json:"text"`
	StructuralPath string `json:"structural_path"`
	StartOffset    int    `json:"start_offset"`
	EndOffset      int    `json:"end_offset"`
}

func ExtractMarkdownDocument(content string) TextDocument {
	text := normalizeTextNewlines(content)
	body, frontmatter, rawFrontmatter, warnings := splitMarkdownFrontmatter(text)
	candidates, timeWarnings, _ := normalizeAbsoluteTimeCandidates(markdownAbsoluteTimeCandidates(frontmatter))
	for _, warning := range timeWarnings {
		warnings = appendUniqueString(warnings, fmt.Sprintf("%s: %s: %s", warning.Code, warning.Basis, warning.RawValue))
	}
	headings := extractMarkdownHeadings(body)
	links := ExtractMarkdownLinks(body)
	return TextDocument{
		Text:                   body,
		Frontmatter:            frontmatter,
		FrontmatterRaw:         rawFrontmatter,
		AbsoluteTimeCandidates: candidates,
		AbsoluteTimeWarnings:   timeWarnings,
		Headings:               headings,
		Links:                  links,
		Warnings:               warnings,
	}
}

func markdownAbsoluteTimeCandidates(frontmatter map[string]any) []AbsoluteTimeCandidate {
	if len(frontmatter) == 0 {
		return nil
	}
	candidates := []AbsoluteTimeCandidate{}
	for _, field := range []struct {
		key   string
		kind  string
		basis string
	}{
		{key: "updated_at", kind: AbsoluteTimeKindModified, basis: AbsoluteTimeBasisFrontmatterUpdatedAt},
		{key: "created_at", kind: AbsoluteTimeKindCreated, basis: AbsoluteTimeBasisFrontmatterCreatedAt},
	} {
		value, ok := frontmatter[field.key]
		if !ok {
			continue
		}
		raw := strings.TrimSpace(fmt.Sprint(value))
		if raw != "" && raw != "<nil>" {
			candidates = append(candidates, AbsoluteTimeCandidate{Kind: field.kind, Basis: field.basis, RawValue: raw})
		}
	}
	return candidates
}

func ExtractPlainTextDocument(content string) TextDocument {
	text := normalizeTextNewlines(content)
	return TextDocument{Text: text}
}

func normalizeTextNewlines(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	return strings.ReplaceAll(content, "\r", "\n")
}

func splitMarkdownFrontmatter(text string) (string, map[string]any, string, []string) {
	if !strings.HasPrefix(text, "---\n") {
		return text, nil, "", nil
	}
	offset := len("---\n")
	for offset <= len(text) {
		next := strings.IndexByte(text[offset:], '\n')
		lineEnd := len(text)
		if next >= 0 {
			lineEnd = offset + next
		}
		line := strings.TrimSpace(text[offset:lineEnd])
		if line == "---" || line == "..." {
			raw := text[len("---\n"):offset]
			bodyStart := lineEnd
			if bodyStart < len(text) && text[bodyStart] == '\n' {
				bodyStart++
			}
			frontmatter, err := parseFrontmatterYAML(raw)
			if err != nil {
				return text, nil, raw, []string{"frontmatter_invalid_yaml: " + err.Error()}
			}
			return text[bodyStart:], frontmatter, raw, nil
		}
		if next < 0 {
			break
		}
		offset = lineEnd + 1
	}
	return text, nil, "", []string{"frontmatter_missing_closing_delimiter"}
}

func parseFrontmatterYAML(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var decoded map[string]any
	if err := yaml.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, err
	}
	normalized, ok := normalizeYAMLValue(decoded).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("frontmatter root must be a mapping")
	}
	return normalized, nil
}

func normalizeYAMLValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeYAMLValue(item)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[fmt.Sprint(key)] = normalizeYAMLValue(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, normalizeYAMLValue(item))
		}
		return out
	default:
		payload, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		var normalized any
		if err := json.Unmarshal(payload, &normalized); err != nil {
			return fmt.Sprint(typed)
		}
		return normalized
	}
}

func extractMarkdownHeadings(text string) []TextHeading {
	headings := []TextHeading{}
	stack := map[int]string{}
	lineStart := 0
	for lineStart <= len(text) {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart
		}
		line := text[lineStart:lineEnd]
		if level, title, ok := parseMarkdownHeadingLine(line); ok {
			for existingLevel := level; existingLevel <= 6; existingLevel++ {
				delete(stack, existingLevel)
			}
			stack[level] = title
			headings = append(headings, TextHeading{
				Level:          level,
				Text:           title,
				StructuralPath: structuralPathFromHeadingStack(stack),
				StartOffset:    lineStart,
				EndOffset:      lineEnd,
			})
		}
		if lineEnd == len(text) {
			break
		}
		lineStart = lineEnd + 1
	}
	return headings
}

func parseMarkdownHeadingLine(line string) (int, string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "#") {
		return 0, "", false
	}
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	title := strings.TrimSpace(line[level+1:])
	title = strings.TrimSpace(strings.TrimRight(title, "#"))
	if title == "" {
		return 0, "", false
	}
	return level, title, true
}

func structuralPathFromHeadingStack(stack map[int]string) string {
	parts := []string{}
	for level := 1; level <= 6; level++ {
		if value := strings.TrimSpace(stack[level]); value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " / ")
}
