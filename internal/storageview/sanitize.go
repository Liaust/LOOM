package storageview

import (
	"fmt"
	"path"
	"strings"
	"unicode"
)

func SanitizeRelativePath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("path must be relative")
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return "", fmt.Errorf("path is required")
	}

	segments := strings.Split(cleaned, "/")
	out := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("path contains unsafe segment %q", segment)
		}
		out = append(out, SanitizeLabel(segment))
	}
	return strings.Join(out, "/"), nil
}

func SanitizeLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "untitled"
	}
	var builder strings.Builder
	lastWasSpace := false
	for _, r := range value {
		switch {
		case r == '/' || r == '\\' || r == 0:
			if !lastWasSpace {
				builder.WriteByte(' ')
				lastWasSpace = true
			}
		case unicode.IsControl(r):
			continue
		case unicode.IsSpace(r):
			if !lastWasSpace {
				builder.WriteByte(' ')
				lastWasSpace = true
			}
		default:
			builder.WriteRune(r)
			lastWasSpace = false
		}
	}
	cleaned := strings.TrimSpace(builder.String())
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "untitled"
	}
	return cleaned
}

func shortStorageID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "entry"
	}
	if len(id) <= 10 {
		return id
	}
	return id[len(id)-10:]
}
