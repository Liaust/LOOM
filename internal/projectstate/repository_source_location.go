package projectstate

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"loom.local/loom/internal/projects"
)

// repositoryMembershipSource uses only validated registry locations. It never
// searches the filesystem or substitutes a conventional contract filename.
func repositoryMembershipSource(source *projects.ProjectRepositoryReadSource) (string, error) {
	invalid := func() (string, error) {
		return "", fmt.Errorf("%w: invalid registered repository source binding", ErrProjectStateUnavailable)
	}
	if source == nil {
		return invalid()
	}
	versions := projects.ProjectRepositorySourceVersions{ProjectContract: source.ProjectContractSchemaVersion, ReposContract: source.ReposContractSchemaVersion}
	if !slices.Contains(projects.SupportedProjectRepositorySourceVersions(), versions) {
		return invalid()
	}
	for _, locator := range []string{source.ProjectRoot, source.ProjectContractPath, source.ReposContractPath} {
		if !filepath.IsAbs(locator) || strings.TrimSpace(locator) != locator || filepath.Clean(locator) != locator || !utf8.ValidString(locator) || strings.ContainsAny(locator, "\\\x00\r\n") {
			return invalid()
		}
	}
	for _, locator := range []string{source.ProjectContractPath, source.ReposContractPath} {
		relative, err := filepath.Rel(source.ProjectRoot, locator)
		if err != nil || !safeRepositoryRelativePath(filepath.ToSlash(relative)) {
			return invalid()
		}
	}
	if source.ProjectContractSchemaVersion == projects.ProjectRepositoryProjectSchemaV05 && source.ProjectContractPath != source.ReposContractPath {
		return invalid()
	}
	relative, _ := filepath.Rel(source.ProjectRoot, source.ReposContractPath)
	return filepath.ToSlash(relative), nil
}

// repositorySourceLocation preserves legacy repos-relative members and the
// v0.5 project-relative representation without probing alternate layouts.
func repositorySourceLocation(source *projects.ProjectRepositoryReadSource, memberPath string) (string, error) {
	if _, err := repositoryMembershipSource(source); err != nil {
		return "", err
	}
	if !safeRepositoryRelativePath(memberPath) {
		return "", ErrPathInvalid
	}
	if source.ProjectContractSchemaVersion == projects.ProjectRepositoryProjectSchemaV05 {
		return memberPath, nil
	}
	return "repos/" + memberPath, nil
}

func safeRepositoryRelativePath(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.HasPrefix(value, "../") &&
		strings.TrimSpace(value) == value && !path.IsAbs(value) && path.Clean(value) == value && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\\\x00\r\n")
}
