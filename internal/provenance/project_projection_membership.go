package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
)

// ProjectRepositoryBinding contains only registry evidence, not filesystem or
// Git observations. The digest also binds the registered source location.
type ProjectRepositoryBinding struct {
	RepositoryID        string `json:"repository_id"`
	OwnerProjectID      string `json:"owner_project_id"`
	Key                 string `json:"key"`
	Role                string `json:"role"`
	Path                string `json:"path"`
	StateRoot           string `json:"state_root"`
	BindingDigest       string `json:"binding_digest"`
	MembershipLifecycle string `json:"membership_lifecycle"`
	RepositoryLifecycle string `json:"repository_lifecycle"`
}

func projectRepositoryBindings(project projectstate.ProjectProjection) ([]ProjectRepositoryBinding, error) {
	if len(project.Members) > projects.ProjectRepositoryReadMemberLimit {
		return nil, errors.New("project membership exceeds projection bound")
	}
	bindings := make([]ProjectRepositoryBinding, 0, len(project.Members))
	seen := map[string]bool{}
	for _, member := range project.Members {
		if ids.Validate("repo", member.RepositoryID) != nil || ids.Validate("project", member.RepositoryOwnerProjectID) != nil || !validProjectionDigest(member.SourceBindingDigest) || seen[member.RepositoryID] || !validProjectMembershipPath(member.RelativeSource) || member.StateRoot != "" && !validProjectMembershipPath(member.StateRoot) {
			return nil, errors.New("project membership has invalid or duplicate binding evidence")
		}
		seen[member.RepositoryID] = true
		bindings = append(bindings, projectRepositoryBinding(member))
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].RepositoryID < bindings[j].RepositoryID })
	return bindings, nil
}

func projectRepositoryBinding(member projectstate.RepositoryProjection) ProjectRepositoryBinding {
	return ProjectRepositoryBinding{
		RepositoryID: member.RepositoryID, OwnerProjectID: member.RepositoryOwnerProjectID,
		Key: member.Key, Role: string(member.Role), Path: member.RelativeSource, StateRoot: member.StateRoot,
		BindingDigest:       member.SourceBindingDigest,
		MembershipLifecycle: string(member.MembershipLifecycle), RepositoryLifecycle: string(member.RepositoryLifecycle),
	}
}

func validProjectMembershipPath(value string) bool {
	return value != "" && !path.IsAbs(value) && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../") && !strings.Contains(value, "\\")
}

func (store *Store) lockProjectProjection(ctx context.Context, projectID string) error {
	if _, err := store.q.exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "loom:provenance:project-projection:"+projectID); err != nil {
		return fmt.Errorf("lock project projection revision: %w", err)
	}
	return nil
}

// The source observation precedes the transaction lock. Do not let an older
// automatic capture displace a newer stored membership or development state.
func validateContextOnlyObservation(previous ProjectProjectionSnapshot, next projectSnapshot) error {
	if previous.SnapshotID == "" {
		return nil
	}
	olderBinding := next.SourceRevision < previous.SourceRevision &&
		previous.SourceSchemaVersion != unregisteredProjectProjectionSourceSchemaVersion &&
		next.SourceSchemaVersion != unregisteredProjectProjectionSourceSchemaVersion
	olderCapture := next.SourceRevision == previous.SourceRevision && next.ObservedAt.Before(previous.ObservedAt) &&
		(next.SnapshotDigest != previous.SnapshotDigest || next.SourceDigest != previous.SourceDigest)
	if olderBinding || olderCapture {
		return &FoundationConflictError{Err: errors.New("context-only observation predates the latest captured project source")}
	}
	return nil
}

func (store *Store) carryProjectRepositories(ctx context.Context, previous ProjectProjectionSnapshot, next projectSnapshot) ([]repositorySnapshot, error) {
	if previous.SnapshotID == "" || len(previous.Context.Memberships) == 0 {
		return nil, nil
	}
	current, err := decodeProjectContext(next.ProjectionJSON, next.ProjectID)
	if err != nil {
		return nil, err
	}
	eligible := unchangedProjectBindings(previous.Context, current)
	if len(eligible) == 0 {
		return nil, nil
	}
	rows, err := store.q.query(ctx, `
		SELECT DISTINCT ON (repository_id) repository_id, source_identity_digest, card_json
		FROM provenance.repository_projection_snapshots
		WHERE project_id=$1 AND project_snapshot_id=$2::uuid
		ORDER BY repository_id, source_revision DESC, id
		LIMIT $3
	`, next.ProjectID, string(previous.SnapshotID), projects.ProjectRepositoryReadMemberLimit+1)
	if err != nil {
		return nil, fmt.Errorf("read retained repository captures: %w", err)
	}
	defer rows.Close()
	result := []repositorySnapshot{}
	count := 0
	for rows.Next() {
		count++
		if count > projects.ProjectRepositoryReadMemberLimit {
			return nil, errors.New("retained repository captures exceed membership bound")
		}
		var repositoryID, sourceIdentity string
		var payload []byte
		if err := rows.Scan(&repositoryID, &sourceIdentity, &payload); err != nil {
			return nil, fmt.Errorf("scan retained repository capture: %w", err)
		}
		binding, ok := eligible[repositoryID]
		if !ok {
			continue
		}
		var card RepositoryCard
		if err := json.Unmarshal(payload, &card); err != nil {
			return nil, fmt.Errorf("decode retained repository capture: %w", err)
		}
		if card.RepositoryID != repositoryID || card.OwningProject.ProjectID != next.ProjectID {
			return nil, errors.New("retained repository capture has mismatched identity")
		}
		snapshot, err := reboundRepositorySnapshot(card, binding, next, current)
		if err != nil {
			return nil, err
		}
		if previous.SnapshotID == next.ID {
			// Same parent needs no rebind; retain manual/carry identity so an
			// unchanged automatic poll cannot append a new repository revision.
			snapshot.SourceIdentityDigest = sourceIdentity
		}
		result = append(result, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retained repository captures: %w", err)
	}
	return result, nil
}

func unchangedProjectBindings(previous, next ProjectContextProjection) map[string]ProjectRepositoryBinding {
	old := make(map[string]ProjectRepositoryBinding, len(previous.Memberships))
	for _, binding := range previous.Memberships {
		old[binding.RepositoryID] = binding
	}
	eligible := map[string]ProjectRepositoryBinding{}
	for _, binding := range next.Memberships {
		if binding.OwnerProjectID != next.ProjectID || binding.Role != string(projects.ProjectRepositoryRolePrimary) && binding.Role != string(projects.ProjectRepositoryRoleComponent) || binding.Path == "" || !validProjectionDigest(binding.BindingDigest) {
			continue
		}
		if before, ok := old[binding.RepositoryID]; ok && before == binding {
			eligible[binding.RepositoryID] = binding
		}
	}
	return eligible
}

func reboundRepositorySnapshot(card RepositoryCard, binding ProjectRepositoryBinding, next projectSnapshot, current ProjectContextProjection) (repositorySnapshot, error) {
	card.OwningProject = RepositoryOwningProject{ProjectID: current.ProjectID, Name: current.Name,
		Slug: current.Slug, Lifecycle: current.Lifecycle, NavigationRef: current.NavigationRef}
	// Freshness and all repository capture fields deliberately remain untouched.
	payload, err := json.Marshal(card)
	if err != nil {
		return repositorySnapshot{}, err
	}
	digest, err := deterministicRepositoryCardDigest(card)
	if err != nil {
		return repositorySnapshot{}, err
	}
	identity, err := json.Marshal(struct {
		SchemaVersion string                   `json:"schema_version"`
		Parent        SemanticID               `json:"project_snapshot_id"`
		Binding       ProjectRepositoryBinding `json:"binding"`
		CardDigest    string                   `json:"card_digest"`
	}{"loom.provenance.repository_context_rebind.v1", next.ID, binding, digest})
	if err != nil {
		return repositorySnapshot{}, err
	}
	return repositorySnapshot{
		ProjectID: next.ProjectID, ProjectSnapshotID: next.ID, RepositoryID: card.RepositoryID,
		SourceIdentityDigest: digestProjection(identity), SnapshotDigest: digest,
		SourceVersion: card.Freshness.SourceVersion, SourceDigest: card.Freshness.SourceDigest,
		ObservedCommit: card.Freshness.ObservedCommit, ObservedAt: card.Freshness.ObservedAt,
		TrackingStatus: card.TrackingStatus, SearchableSummary: repositorySearchableSummary(card),
		CardJSON: payload, CreatedAt: next.CreatedAt,
	}, nil
}
