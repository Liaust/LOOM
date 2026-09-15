package storagecatalog

import (
	"fmt"
	"path"
	"strings"
)

func NormalizeLogicalPath(value string) (string, error) {
	return normalizeCatalogPath("logical path", value, false)
}

func NormalizeOptionalViewPath(value string) (string, error) {
	return normalizeCatalogPath("view path", value, true)
}

func normalizeCatalogPath(label, value string, allowEmpty bool) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("%w: %s is required", ErrInvalid, label)
	}
	if strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("%w: %s must be relative", ErrInvalid, label)
	}

	cleaned := path.Clean(value)
	if cleaned == "." {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("%w: %s is required", ErrInvalid, label)
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("%w: %s contains unsafe segment %q", ErrInvalid, label, segment)
		}
	}
	if strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %s cannot escape catalog root", ErrInvalid, label)
	}
	return cleaned, nil
}

func NormalizeSourcePath(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
}
