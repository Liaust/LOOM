package filesystemmeta

import (
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

func IsGeneratedAppleMetadata(name string) bool {
	name = strings.TrimSpace(name)
	return name == ".DS_Store" || name == ".localized" || strings.HasPrefix(name, "._")
}

func IsHiddenPath(path, root string) bool {
	path = filepath.Clean(path)
	if root = filepath.Clean(root); root != "." && root != "" {
		if rel, err := filepath.Rel(root, path); err == nil {
			path = rel
		}
	}
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == "" || segment == "." || segment == ".." {
			continue
		}
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func UnicodeFormSummary(value string) string {
	if value == "" {
		return ""
	}
	if isASCII(value) {
		return "ascii"
	}
	nfc := norm.NFC.IsNormalString(value)
	nfd := norm.NFD.IsNormalString(value)
	switch {
	case nfc && nfd:
		return "nfc_nfd"
	case nfc:
		return "nfc"
	case nfd:
		return "nfd"
	default:
		return "mixed"
	}
}

func CasefoldKey(value string) string {
	value = filepath.ToSlash(filepath.Clean(value))
	value = norm.NFC.String(value)
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}

func isASCII(value string) bool {
	for _, r := range value {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}
