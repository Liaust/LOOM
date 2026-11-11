package projectstate

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"loom.local/loom/internal/projects"
)

// The custody owner supplies this execution-only location. Revalidate guards
// the selected archive and registry binding before captured output is returned.
type RepositoryReadLocation struct {
	Source     *projects.ProjectRepositoryReadSource
	Archive    *ArchivedSourceContext
	Revalidate func(context.Context) error
}

type ArchivedSourceContext struct {
	ReadOnlyRoot   string    `json:"read_only_root,omitempty"`
	OperationID    string    `json:"operation_id"`
	ManifestDigest string    `json:"manifest_digest"`
	ArchivedAt     time.Time `json:"archived_at"`
}

type ProjectRepositorySourceResolver interface {
	ResolveRepositoryReadSource(context.Context, ProjectDevelopmentInput, projects.ProjectRepositoryReadModel) (RepositoryReadLocation, error)
}

func (s Service) repositoryReadLocation(ctx context.Context, model projects.ProjectRepositoryReadModel) (RepositoryReadLocation, error) {
	if model.Project.Status != "archived" {
		return RepositoryReadLocation{Source: model.Source}, nil
	}
	resolver, ok := s.ProjectDevelopment.(ProjectRepositorySourceResolver)
	if !ok || model.Source == nil {
		return RepositoryReadLocation{}, fmt.Errorf("%w: archive_source_unverified", ErrProjectStateUnavailable)
	}
	location, err := resolver.ResolveRepositoryReadSource(ctx, ProjectDevelopmentInput{
		ProjectRoot: model.Source.ProjectRoot, ProjectID: model.Project.ProjectID,
		OwnerNode: model.Source.OwnerNode, LocalNode: s.LocalNode, Lifecycle: model.Project.Status,
		SourceRevision: model.Source.SourceRevision,
	}, model)
	if err != nil || location.Source == nil || location.Archive == nil || location.Revalidate == nil {
		return RepositoryReadLocation{}, fmt.Errorf("%w: archive_source_unverified", ErrProjectStateUnavailable)
	}
	if _, err := repositoryMembershipSource(location.Source); err != nil {
		return RepositoryReadLocation{}, err
	}
	return location, nil
}

// RelocateRepositoryReadSource retains registered relative locators; the caller
// must first authenticate the retained root using the existing archive owner.
func RelocateRepositoryReadSource(source *projects.ProjectRepositoryReadSource, retainedRoot string) (*projects.ProjectRepositoryReadSource, error) {
	if _, err := repositoryMembershipSource(source); err != nil {
		return nil, err
	}
	projectRef, _ := filepath.Rel(source.ProjectRoot, source.ProjectContractPath)
	reposRef, _ := filepath.Rel(source.ProjectRoot, source.ReposContractPath)
	selected := *source
	selected.ProjectRoot = retainedRoot
	selected.ProjectContractPath = filepath.Join(retainedRoot, projectRef)
	selected.ReposContractPath = filepath.Join(retainedRoot, reposRef)
	if _, err := repositoryMembershipSource(&selected); err != nil {
		return nil, err
	}
	return &selected, nil
}

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
	if (source.ProjectContractSchemaVersion == projects.ProjectRepositoryProjectSchemaV05 || source.ProjectContractSchemaVersion == projects.ProjectRepositoryProjectSchemaV06) && source.ProjectContractPath != source.ReposContractPath {
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
	if source.ProjectContractSchemaVersion == projects.ProjectRepositoryProjectSchemaV05 || source.ProjectContractSchemaVersion == projects.ProjectRepositoryProjectSchemaV06 {
		return memberPath, nil
	}
	return "repos/" + memberPath, nil
}

func safeRepositoryRelativePath(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.HasPrefix(value, "../") &&
		strings.TrimSpace(value) == value && !path.IsAbs(value) && path.Clean(value) == value && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\\\x00\r\n")
}
