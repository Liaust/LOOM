package knowledge

import "strings"

func extractBoundedPlainTextDocument(content string, warnings []string) TextDocument {
	document := ExtractPlainTextDocument(content)
	document.Warnings = append(document.Warnings, warnings...)
	return document
}

func nonEmptyWarning(prefix string, err error) []string {
	if err == nil {
		return nil
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return []string{err.Error()}
	}
	return []string{prefix + ": " + err.Error()}
}
