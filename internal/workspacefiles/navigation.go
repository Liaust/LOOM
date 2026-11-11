package workspacefiles

import (
	"io/fs"
	"strings"

	"loom.local/loom/internal/filepolicy"
)

// Navigation parsing skips generated/runtime names without changing original
// browsing, backup or transfer eligibility. Seeded names use this same guard.
func (s *Service) NavigationPath(root, relative string) error {
	if _, e := s.root(root); e != nil {
		return e
	}
	p, e := normalizePath(relative)
	if e != nil {
		return e
	}
	if p == "" || p == "." {
		return nil
	}
	if mandatoryExcluded(p) || rebuildable(p) || filepolicy.IsIndexingExcludedPath(p) {
		return fs.ErrPermission
	}
	if p != "" && p != "." {
		for _, part := range strings.Split(p, "/") {
			if strings.HasPrefix(part, ".") {
				return fs.ErrPermission
			}
		}
	}
	return nil
}
