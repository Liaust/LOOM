package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/requestctx"
)

// RetainedSourceLocal is trusted host configuration, not a public request body.
type RetainedSourceLocal struct {
	BoxRoot string
	NodeID  string
}
type RetainedSourceSelector struct {
	ProjectID           string   `json:"project_id"`
	RepositoryID        string   `json:"repository_id"`
	SourceBindingDigest string   `json:"source_binding_digest"`
	LocationDigest      string   `json:"location_digest"`
	SourceRevision      int64    `json:"source_revision"`
	Selection           []string `json:"selection"`
}
type RetainedSourceFacts struct {
	RetainedSourceSelector
	ProjectScopeID string `json:"project_scope_id"`
	OwnerNodeID    string `json:"owner_node_id"`
	MemberPath     string `json:"member_path"`
	RegistrationID string `json:"registration_id"`
	SemanticDigest string `json:"semantic_digest"`
}
type RetainedSource struct {
	Facts RetainedSourceFacts
	Root  *os.File
}

func (s *RetainedSource) Close() error { return s.Root.Close() }

func ValidateRetainedSourceFacts(f RetainedSourceFacts) error {
	if ids.Validate(ids.ProjectPrefix, f.ProjectID) != nil || !projectRepositoryIDPattern.MatchString(f.RepositoryID) || ids.Validate(ids.ScopePrefix, f.ProjectScopeID) != nil || ids.Validate(ids.NodePrefix, f.OwnerNodeID) != nil || ids.Validate(ids.ProjectContractRegistrationPrefix, f.RegistrationID) != nil || f.SourceRevision < 1 {
		return fmt.Errorf("invalid retained source identity")
	}
	for _, digest := range []string{f.SourceBindingDigest, f.LocationDigest, f.SemanticDigest} {
		if !retainedSourceDigest(digest) {
			return fmt.Errorf("invalid retained source revision")
		}
	}
	selection, err := objectstore.PackageSelection(f.Selection)
	if err != nil || !reflect.DeepEqual(selection, f.Selection) {
		return fmt.Errorf("invalid retained source selection")
	}
	if _, err := objectstore.PackageSelection([]string{f.MemberPath}); err != nil {
		return err
	}
	return nil
}
func retainedSourceDigest(s string) bool {
	return len(s) == 71 && strings.HasPrefix(s, "sha256:") && strings.Trim(s[7:], "0123456789abcdef") == ""
}

func (s Service) ResolveRetainedSource(ctx context.Context, req requestctx.Context, local RetainedSourceLocal, selector RetainedSourceSelector) (RetainedSource, error) {
	if s.DB == nil || ids.Validate(ids.ProjectPrefix, selector.ProjectID) != nil || !projectRepositoryIDPattern.MatchString(selector.RepositoryID) || ids.Validate(ids.NodePrefix, local.NodeID) != nil || req.OriginNodeID != local.NodeID || !retainedSourceDigest(selector.SourceBindingDigest) || !retainedSourceDigest(selector.LocationDigest) || selector.SourceRevision < 1 {
		return RetainedSource{}, fmt.Errorf("retained source selector invalid")
	}
	selected, err := objectstore.PackageSelection(selector.Selection)
	if err != nil {
		return RetainedSource{}, err
	}
	selector.Selection = selected
	model, err := s.ReadProjectRepositoryState(ctx, selector.ProjectID)
	if err != nil {
		return RetainedSource{}, err
	}
	project, err := s.ResolveProjectRef(ctx, selector.ProjectID)
	if err != nil {
		return RetainedSource{}, err
	}
	node, err := nodes.NewService(s.DB).GetNode(ctx, local.NodeID)
	if err != nil {
		return RetainedSource{}, err
	}
	if model.Source == nil || project.HomeNodeID == nil || *project.HomeNodeID != local.NodeID || node.NodeID != local.NodeID || node.Status != "active" || model.Source.OwnerNode != node.NodeKey || model.Project.Status != "active" || project.Status != "active" || model.Source.SourceRevision != selector.SourceRevision || model.Source.LocationDigest != selector.LocationDigest {
		return RetainedSource{}, fmt.Errorf("retained source owner or revision unavailable")
	}
	var registrationID, registeredRoot, registeredContract string
	if err := s.DB.QueryRowContext(ctx, `SELECT project_contract_registration_id,project_root,contract_path FROM projects.project_contract_registrations WHERE project_id=$1`, project.ProjectID).Scan(&registrationID, &registeredRoot, &registeredContract); err != nil {
		return RetainedSource{}, err
	}
	if registrationID != model.Source.ProjectContractRegistrationID || registeredRoot != model.Source.ProjectRoot || registeredContract != model.Source.ProjectContractPath {
		return RetainedSource{}, fmt.Errorf("retained source registration location mismatch")
	}
	if err := EnsureRuntimeActive(ctx, s.DB, RuntimeRef{ProjectID: project.ProjectID, ScopeID: project.ProjectScopeID, ResourceKind: "retained_source", ResourceRef: selector.RepositoryID}); err != nil {
		return RetainedSource{}, err
	}
	authority, err := s.GetDeclarationAuthorityForRepositories(ctx, req, project.ProjectID, local.NodeID, []string{selector.RepositoryID})
	if err != nil {
		return RetainedSource{}, err
	}
	if !authority.CanRead {
		return RetainedSource{}, fmt.Errorf("retained source read authority required")
	}
	var member *ProjectRepositoryReadMember
	for i := range model.Members {
		if model.Members[i].RepositoryID == selector.RepositoryID {
			member = &model.Members[i]
			break
		}
	}
	if member == nil || member.RepositoryOwnerProjectID != project.ProjectID || member.MembershipLifecycle != RepositoryLifecycleActive || member.RepositoryLifecycle != RepositoryLifecycleActive || member.Role != ProjectRepositoryRolePrimary && member.Role != ProjectRepositoryRoleComponent || member.SourceBindingDigest != selector.SourceBindingDigest {
		return RetainedSource{}, fmt.Errorf("retained repository membership unavailable")
	}
	box, err := filepath.Abs(local.BoxRoot)
	if err != nil || local.BoxRoot == "" {
		return RetainedSource{}, fmt.Errorf("configured Box required")
	}
	projectRoot, err := filepath.Abs(model.Source.ProjectRoot)
	if err != nil {
		return RetainedSource{}, err
	}
	relative, err := filepath.Rel(box, projectRoot)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return RetainedSource{}, fmt.Errorf("registered source outside Box")
	}
	if _, err := objectstore.PackageSelection([]string{member.Path}); err != nil {
		return RetainedSource{}, err
	}
	// The canonical read model verifies the schema pair against retained source
	// evidence. Its path meaning must not depend on which directories exist.
	memberPath, err := retainedRepositoryPath(ProjectRepositorySourceVersions{
		ProjectContract: model.Source.ProjectContractSchemaVersion,
		ReposContract:   model.Source.ReposContractSchemaVersion,
	}, member.Path)
	if err != nil {
		return RetainedSource{}, err
	}
	root, err := objectstore.OpenPackageRoot(box, filepath.Join(relative, filepath.FromSlash(memberPath)))
	if err != nil {
		return RetainedSource{}, err
	}
	facts := RetainedSourceFacts{selector, project.ProjectScopeID, local.NodeID, memberPath, model.Source.ProjectContractRegistrationID, model.Source.SemanticDigest}
	if err := ValidateRetainedSourceFacts(facts); err != nil {
		root.Close()
		return RetainedSource{}, err
	}
	return RetainedSource{facts, root}, nil
}

func retainedRepositoryPath(pair ProjectRepositorySourceVersions, memberPath string) (string, error) {
	if _, err := objectstore.PackageSelection([]string{memberPath}); err != nil {
		return "", err
	}
	switch pair {
	case projectRepositorySourceV03V03, projectRepositorySourceV03V04, projectRepositorySourceV04V03, projectRepositorySourceV04V04:
		return filepath.ToSlash(filepath.Join("repos", filepath.FromSlash(memberPath))), nil
	case projectRepositorySourceV05V05:
		return memberPath, nil
	default:
		return "", fmt.Errorf("%w: retained source path requires a known schema pair", ErrUnsupportedProjectRepositorySourceVersions)
	}
}

// Recheck re-resolves current authority and custody; recorded byte identity is
// deliberately not treated as a grant or a current registration snapshot.
func (s Service) RecheckRetainedSource(ctx context.Context, req requestctx.Context, local RetainedSourceLocal, original RetainedSource) error {
	current, err := s.ResolveRetainedSource(ctx, req, local, original.Facts.RetainedSourceSelector)
	if err != nil {
		return err
	}
	defer current.Close()
	before, e1 := original.Root.Stat()
	after, e2 := current.Root.Stat()
	a, _ := json.Marshal(original.Facts)
	b, _ := json.Marshal(current.Facts)
	if e1 != nil || e2 != nil || !os.SameFile(before, after) || string(a) != string(b) {
		return fmt.Errorf("retained source changed")
	}
	return nil
}
