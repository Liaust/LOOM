package watchedroots

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

func MatchAny(patterns []string, relativePath string) (bool, string) {
	relativePath = normalizeMatchPath(relativePath)
	for _, pattern := range patterns {
		matched, err := MatchPattern(pattern, relativePath)
		if err != nil {
			continue
		}
		if matched {
			return true, pattern
		}
	}
	return false, ""
}

func MatchPattern(pattern, relativePath string) (bool, error) {
	pattern = normalizeMatchPath(pattern)
	relativePath = normalizeMatchPath(relativePath)
	if err := validatePattern(pattern); err != nil {
		return false, err
	}
	patternParts := splitMatchPath(pattern)
	pathParts := splitMatchPath(relativePath)
	return matchParts(patternParts, pathParts), nil
}

func validatePatterns(patterns []string) error {
	for _, pattern := range patterns {
		if err := validatePattern(pattern); err != nil {
			return fmt.Errorf("%q: %w", pattern, err)
		}
	}
	return nil
}

func ValidatePatterns(patterns []string) error {
	return validatePatterns(patterns)
}

func validatePattern(pattern string) error {
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	if pattern == "" {
		return fmt.Errorf("pattern is empty")
	}
	if strings.HasPrefix(pattern, "/") || filepath.IsAbs(pattern) {
		return fmt.Errorf("absolute patterns are not allowed")
	}
	parts := strings.Split(pattern, "/")
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return fmt.Errorf("parent traversal is not allowed")
		}
		if part == "**" {
			continue
		}
		if _, err := path.Match(part, "x"); err != nil {
			return err
		}
	}
	return nil
}

func matchParts(patternParts, pathParts []string) bool {
	if len(patternParts) == 0 {
		return len(pathParts) == 0
	}
	if patternParts[0] == "**" {
		if matchParts(patternParts[1:], pathParts) {
			return true
		}
		for i := range pathParts {
			if matchParts(patternParts[1:], pathParts[i+1:]) {
				return true
			}
		}
		return false
	}
	if len(pathParts) == 0 {
		return false
	}
	matched, err := path.Match(patternParts[0], pathParts[0])
	if err != nil || !matched {
		return false
	}
	return matchParts(patternParts[1:], pathParts[1:])
}

func normalizeMatchPath(value string) string {
	value = strings.TrimSpace(filepath.ToSlash(value))
	if value == "" {
		return "."
	}
	cleaned := path.Clean(value)
	if cleaned == "" {
		return "."
	}
	return cleaned
}

func splitMatchPath(value string) []string {
	value = normalizeMatchPath(value)
	if value == "." {
		return nil
	}
	return strings.Split(value, "/")
}
