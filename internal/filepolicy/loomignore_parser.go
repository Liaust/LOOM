package filepolicy

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

func parsePolicyFile(root, filePath string, content []byte) (PolicyFile, error) {
	relativePath, err := filepath.Rel(root, filePath)
	if err != nil || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return PolicyFile{}, fmt.Errorf(".loomignore %q is outside policy root %q", filePath, root)
	}
	relativePath = filepath.ToSlash(relativePath)
	baseDir := filepath.ToSlash(filepath.Dir(relativePath))
	if baseDir == "." {
		baseDir = ""
	}
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
	result := PolicyFile{
		Path:         filePath,
		RelativePath: relativePath,
		BaseDir:      baseDir,
		ContentHash:  hash,
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		raw := strings.TrimSuffix(scanner.Text(), "\r")
		original := trimUnescapedTrailingSpaces(raw)
		if original == "" {
			continue
		}
		escapedLeading := strings.HasPrefix(original, `\#`) || strings.HasPrefix(original, `\!`)
		if strings.HasPrefix(original, "#") && !escapedLeading {
			continue
		}

		pattern := original
		negated := false
		if escapedLeading {
			pattern = pattern[1:]
		} else if strings.HasPrefix(pattern, "!") {
			negated = true
			pattern = pattern[1:]
		}
		if pattern == "" {
			return PolicyFile{}, fmt.Errorf("%s:%d: empty .loomignore pattern", filePath, lineNumber)
		}
		anchored := strings.HasPrefix(pattern, "/")
		pattern = strings.TrimPrefix(pattern, "/")
		directoryOnly := strings.HasSuffix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/")
		if pattern == "" {
			return PolicyFile{}, fmt.Errorf("%s:%d: empty .loomignore pattern", filePath, lineNumber)
		}
		if _, err := doublestar.Match(pattern, "__loom_policy_validation__"); err != nil {
			return PolicyFile{}, fmt.Errorf("%s:%d: malformed .loomignore pattern %q: %w", filePath, lineNumber, original, err)
		}
		result.Rules = append(result.Rules, Rule{
			OriginalPattern:   original,
			NormalizedPattern: pattern,
			SourceFile:        filePath,
			SourceLine:        lineNumber,
			ContentHash:       hash,
			BaseDir:           baseDir,
			Negated:           negated,
			DirectoryOnly:     directoryOnly,
			Anchored:          anchored,
		})
	}
	if err := scanner.Err(); err != nil {
		return PolicyFile{}, fmt.Errorf("read .loomignore %q: %w", filePath, err)
	}
	return result, nil
}

func trimUnescapedTrailingSpaces(value string) string {
	for strings.HasSuffix(value, " ") {
		backslashes := 0
		for index := len(value) - 2; index >= 0 && value[index] == '\\'; index-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			value = value[:len(value)-2] + " "
			break
		}
		value = strings.TrimSuffix(value, " ")
	}
	return value
}
