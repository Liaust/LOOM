package loomcli

import (
	"path/filepath"
	"strings"
)

// Only explicit caller-relative paths use cwd. A remote owner, absolute path,
// project ID or machine slug is never rewritten from the caller's filesystem.
func declarationReadProjectRef(ref, node string) (string, error) {
	if node == "" && (ref == "." || ref == ".." || strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../")) {
		return filepath.Abs(ref)
	}
	return ref, nil
}
