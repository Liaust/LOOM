package provenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/repostate"
)

const (
	ProjectProjectionSchemaVersion    = "loom.provenance.project_projection.v1"
	RepositoryCardSchemaVersion       = "loom.provenance.repository_card.v1"
	DefaultRepositoryProjectionLimit  = 8
	MaximumRepositoryProjectionLimit  = 8
	MaximumRepositoryAcceptedContext  = 4
	MaximumRepositoryProjectionFilter = 256
	maximumProjectionSummaryRunes     = 4000
	maximumProjectionListValues       = 16
	maximumProjectionStateValues      = 8
	maximumProjectionDiagnostics      = 16
	maximumProjectionTextRunes        = 1024
)

var ErrProjectionReplayConflict = errors.New("projection source version has a different deterministic digest")
var ErrRepositoryProjectionNotFound = errors.New("repository projection was not found")
var ErrProjectProjectionNotFound = errors.New("project projection was not found")

type RepositoryTrackingStatus string

const (
	RepositoryTrackingValid             RepositoryTrackingStatus = "valid"
	RepositoryTrackingNotEnabled        RepositoryTrackingStatus = "not_enabled"
	RepositoryTrackingStale             RepositoryTrackingStatus = "stale"
	RepositoryTrackingMalformed         RepositoryTrackingStatus = "malformed"
	RepositoryTrackingMismatchedOwner   RepositoryTrackingStatus = "mismatched_owner"
	RepositoryTrackingNotObserved       RepositoryTrackingStatus = "not_observed"
	RepositoryTrackingRemoteUnavailable RepositoryTrackingStatus = "remote_unavailable"
)

type RepositoryOwningProject struct {
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	Slug          string `json:"slug,omitempty"`
	Lifecycle     string `json:"lifecycle"`
	NavigationRef string `json:"navigation_ref"`
}

type RepositoryAcceptedContext struct {
	RecordID      SemanticID `json:"record_id"`
	Summary       string     `json:"summary"`
	Qualification string     `json:"qualification"`
}

type RepositoryFreshness struct {
	Posture         string     `json:"posture"`
	ObservedAt      *time.Time `json:"observed_at"`
	SourceVersion   *string    `json:"source_version"`
	SourceDigest    *string    `json:"source_digest"`
	ObservedCommit  *string    `json:"observed_commit"`
	SourceBranch    *string    `json:"source_branch"`
	FrontierPosture string     `json:"frontier_posture"`
}

type RepositoryFieldSource struct {
	Source  string `json:"source"`
	Posture string `json:"posture"`
}

// RepositoryCard deliberately omits technical roots, raw contracts, Git
// commands, and observation payloads. AcceptedContext is joined independently
// at read time and is never stored in the deterministic card snapshot.
type RepositoryCard struct {
	SchemaVersion         string                           `json:"schema_version"`
	RepositoryID          string                           `json:"repository_id"`
	Name                  string                           `json:"name"`
	Aliases               []string                         `json:"aliases"`
	OwningProject         RepositoryOwningProject          `json:"owning_project"`
	Role                  string                           `json:"role"`
	Purpose               string                           `json:"purpose"`
	Topics                []string                         `json:"topics"`
	CurrentState          string                           `json:"current_state"`
	ActiveFocus           []string                         `json:"active_focus"`
	RecentOutcomes        []string                         `json:"recent_outcomes"`
	NextPriorities        []string                         `json:"next_priorities"`
	Blockers              []string                         `json:"blockers"`
	AcceptedContext       []RepositoryAcceptedContext      `json:"accepted_context"`
	Freshness             RepositoryFreshness              `json:"freshness"`
	PortableNavigationRef string                           `json:"portable_navigation_ref"`
	TrackingStatus        RepositoryTrackingStatus         `json:"tracking_status"`
	Diagnostics           []string                         `json:"diagnostics"`
	FieldSources          map[string]RepositoryFieldSource `json:"field_sources"`
}

type RepositoryProjectionListRequest struct {
	Query          string                   `json:"query,omitempty"`
	Project        string                   `json:"project,omitempty"`
	Topic          string                   `json:"topic,omitempty"`
	Role           string                   `json:"role,omitempty"`
	TrackingStatus RepositoryTrackingStatus `json:"tracking_status,omitempty"`
	Limit          int                      `json:"limit,omitempty"`
}

type RepositoryProjectionList struct {
	SchemaVersion string           `json:"schema_version"`
	Ordering      string           `json:"ordering"`
	Items         []RepositoryCard `json:"items"`
	Returned      int              `json:"returned"`
	Truncated     bool             `json:"truncated"`
}

type ProjectProjectionSyncInput struct {
	Project      projectstate.ProjectProjection
	Repositories []repostate.ProvenanceProjection
	// ContextOnly carries forward captured repository cards only when the
	// authoritative membership evidence still matches their previous parent.
	ContextOnly bool
}

type ProjectProjectionSyncRepositoryReceipt struct {
	RepositoryID   string                   `json:"repository_id"`
	SnapshotID     SemanticID               `json:"snapshot_id"`
	SourceVersion  int64                    `json:"source_version"`
	TrackingStatus RepositoryTrackingStatus `json:"tracking_status"`
	Replayed       bool                     `json:"replayed"`
}

type ProjectProjectionSyncReceipt struct {
	SchemaVersion        string                                   `json:"schema_version"`
	ProjectID            string                                   `json:"project_id"`
	ProjectSnapshotID    SemanticID                               `json:"project_snapshot_id"`
	ProjectSourceVersion int64                                    `json:"project_source_version"`
	ProjectReplayed      bool                                     `json:"project_replayed"`
	Repositories         []ProjectProjectionSyncRepositoryReceipt `json:"repositories"`
	Replayed             bool                                     `json:"replayed"`
	SyncedAt             time.Time                                `json:"synced_at"`
}

type AuthorizedProjectProjectionSync struct {
	Execution ExecutionAuthority           `json:"execution_authority"`
	Receipt   ProjectProjectionSyncReceipt `json:"receipt"`
}

type ProjectionTransport interface {
	SyncProjectProjection(context.Context, ProjectProjectionSyncInput) (ProjectProjectionSyncReceipt, error)
	ListRepositoryProjections(context.Context, RepositoryProjectionListRequest) (RepositoryProjectionList, error)
	GetRepositoryProjection(context.Context, string) (RepositoryCard, error)
}

// ProjectContextProjection is captured project context. Development is absent
// in legacy identity-only snapshots, not reconstructed from current files.
type ProjectContextProjection struct {
	SchemaVersion       string                                `json:"schema_version"`
	ProjectID           string                                `json:"project_id"`
	Name                string                                `json:"name"`
	Slug                string                                `json:"slug"`
	Lifecycle           string                                `json:"lifecycle"`
	NavigationRef       string                                `json:"navigation_ref"`
	Development         *projectstate.ProjectDevelopmentState `json:"development,omitempty"`
	Memberships         []ProjectRepositoryBinding            `json:"memberships,omitempty"`
	MembershipsCaptured bool                                  `json:"memberships_captured,omitempty"`
}

type projectSnapshot struct {
	ID                   SemanticID
	ProjectID            string
	ProjectionRevision   int64
	SourceRevision       int64
	SourceIdentityDigest string
	SourceDigest         string
	SourceSchemaVersion  string
	SnapshotDigest       string
	ObservedAt           time.Time
	SearchableSummary    string
	ProjectionJSON       json.RawMessage
	CreatedAt            time.Time
}

type repositorySnapshot struct {
	ID                   SemanticID
	ProjectSnapshotID    SemanticID
	ProjectID            string
	RepositoryID         string
	SourceRevision       int64
	SourceIdentityDigest string
	MembershipRevision   int64
	MembershipDigest     string
	SourceVersion        *string
	SourceDigest         *string
	SnapshotDigest       string
	ObservedCommit       *string
	TrackingStatus       RepositoryTrackingStatus
	ObservedAt           *time.Time
	SearchableSummary    string
	CardJSON             json.RawMessage
	CreatedAt            time.Time
}

func (api *FoundationAPI) SyncProjectProjection(ctx context.Context, input ProjectProjectionSyncInput) (ProjectProjectionSyncReceipt, error) {
	if api == nil || api.store == nil || api.service == nil {
		return ProjectProjectionSyncReceipt{}, errors.New("provenance projection runtime is not ready")
	}
	project, repositories, err := prepareProjectProjectionSync(input, api.service.clock().UTC())
	if err != nil {
		return ProjectProjectionSyncReceipt{}, &FoundationValidationError{Err: err}
	}
	receipt := ProjectProjectionSyncReceipt{
		SchemaVersion:        ProjectProjectionSchemaVersion,
		ProjectID:            project.ProjectID,
		ProjectSourceVersion: project.SourceRevision,
		Repositories:         []ProjectProjectionSyncRepositoryReceipt{}, SyncedAt: project.CreatedAt,
		Replayed: true,
	}
	err = api.store.Transact(ctx, func(store *Store) error {
		var previous ProjectProjectionSnapshot
		if input.ContextOnly {
			if lockErr := store.lockProjectProjection(ctx, project.ProjectID); lockErr != nil {
				return lockErr
			}
			var readErr error
			previous, readErr = (&FoundationAPI{store: store}).GetProjectProjection(ctx, project.ProjectID, "")
			if readErr != nil && !errors.Is(readErr, ErrProjectProjectionNotFound) {
				return readErr
			}
			if err := validateContextOnlyObservation(previous, project); err != nil {
				return err
			}
		}
		projectID, projectProjectionRevision, replayed, appendErr := store.appendProjectProjection(ctx, project)
		if appendErr != nil {
			return appendErr
		}
		receipt.ProjectSnapshotID = projectID
		receipt.ProjectReplayed = replayed
		receipt.Replayed = replayed
		project.ID, project.ProjectionRevision = projectID, projectProjectionRevision
		if input.ContextOnly {
			var carryErr error
			repositories, carryErr = store.carryProjectRepositories(ctx, previous, project)
			if carryErr != nil {
				return carryErr
			}
		}
		for index := range repositories {
			repositories[index].ProjectSnapshotID = projectID
			if !input.ContextOnly {
				sourceIdentityDigest, identityErr := repositoryProjectionSourceIdentity(project, repositories[index])
				if identityErr != nil {
					return identityErr
				}
				repositories[index].SourceIdentityDigest = sourceIdentityDigest
			}
			repositoryID, repositorySourceRevision, repositoryReplayed, repositoryErr := store.appendRepositoryProjection(ctx, repositories[index])
			if repositoryErr != nil {
				return repositoryErr
			}
			receipt.Repositories = append(receipt.Repositories, ProjectProjectionSyncRepositoryReceipt{
				RepositoryID: repositories[index].RepositoryID, SnapshotID: repositoryID,
				SourceVersion:  repositorySourceRevision,
				TrackingStatus: repositories[index].TrackingStatus, Replayed: repositoryReplayed,
			})
			receipt.Replayed = receipt.Replayed && repositoryReplayed
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrProjectionReplayConflict) {
			return ProjectProjectionSyncReceipt{}, &FoundationConflictError{Err: err}
		}
		return ProjectProjectionSyncReceipt{}, err
	}
	return receipt, nil
}

func prepareProjectProjectionSync(input ProjectProjectionSyncInput, createdAt time.Time) (projectSnapshot, []repositorySnapshot, error) {
	projection := input.Project
	if projection.SchemaVersion != projectstate.SchemaVersion || projection.ObservedAt.IsZero() {
		return projectSnapshot{}, nil, errors.New("project projection schema version and observed_at are required")
	}
	if err := ids.Validate("project", projection.Project.ProjectID); err != nil {
		return projectSnapshot{}, nil, fmt.Errorf("project projection id: %w", err)
	}
	if len(input.Repositories) > projects.ProjectRepositoryReadMemberLimit {
		return projectSnapshot{}, nil, fmt.Errorf("project projection exceeds %d repositories", projects.ProjectRepositoryReadMemberLimit)
	}
	if input.ContextOnly && len(input.Repositories) != 0 {
		return projectSnapshot{}, nil, errors.New("context-only project sync cannot supply new repository observations")
	}
	contextProjection := ProjectContextProjection{
		SchemaVersion: ProjectProjectionSchemaVersion,
		ProjectID:     projection.Project.ProjectID, Name: boundedProjectionText(projection.Project.Name, maximumProjectionTextRunes),
		Slug: projection.Project.Slug, Lifecycle: projection.Project.Lifecycle,
		NavigationRef: "loom-project://" + projection.Project.ProjectID,
	}
	development, err := capturedProjectDevelopment(projection)
	if err != nil {
		return projectSnapshot{}, nil, err
	}
	contextProjection.Development = development
	contextProjection.Memberships, err = projectRepositoryBindings(projection)
	if err != nil {
		return projectSnapshot{}, nil, err
	}
	contextProjection.MembershipsCaptured = input.ContextOnly || development != nil || len(projection.Members) > 0
	projectJSON, err := json.Marshal(contextProjection)
	if err != nil {
		return projectSnapshot{}, nil, err
	}
	projectDigest, err := deterministicProjectContextDigest(contextProjection)
	if err != nil {
		return projectSnapshot{}, nil, err
	}
	sourceDigest, sourceSchemaVersion, err := projectionProjectSource(projection, projectDigest)
	if err != nil {
		return projectSnapshot{}, nil, err
	}
	project := projectSnapshot{
		ProjectID: projection.Project.ProjectID, SourceRevision: projection.Source.SourceRevision,
		SourceDigest: sourceDigest, SourceSchemaVersion: sourceSchemaVersion,
		SnapshotDigest: projectDigest, ObservedAt: projection.ObservedAt.UTC(),
		SearchableSummary: projectSearchableSummary(contextProjection),
		ProjectionJSON:    projectJSON, CreatedAt: createdAt,
	}
	project.SourceIdentityDigest, err = projectProjectionSourceIdentity(project)
	if err != nil {
		return projectSnapshot{}, nil, err
	}
	repositories := make([]repositorySnapshot, 0, len(input.Repositories))
	seen := map[string]struct{}{}
	bindings := make(map[string]ProjectRepositoryBinding, len(contextProjection.Memberships))
	for _, binding := range contextProjection.Memberships {
		bindings[binding.RepositoryID] = binding
	}
	for _, repository := range input.Repositories {
		source := repository.Source
		if !source.Owned || source.Projection.RepositoryOwnerProjectID != projection.Project.ProjectID {
			return projectSnapshot{}, nil, fmt.Errorf("repository %s is not owned by projected project", source.Projection.RepositoryID)
		}
		if source.SourceVersion <= 0 {
			return projectSnapshot{}, nil, fmt.Errorf("repository %s source version must be positive", source.Projection.RepositoryID)
		}
		if len(bindings) > 0 {
			if binding, ok := bindings[source.Projection.RepositoryID]; !ok || binding != projectRepositoryBinding(source.Projection) {
				return projectSnapshot{}, nil, errors.New("repository capture differs from project membership evidence")
			}
		}
		if !validProjectionDigest(source.Projection.SourceBindingDigest) {
			return projectSnapshot{}, nil, fmt.Errorf("repository %s source binding digest must be valid", source.Projection.RepositoryID)
		}
		if err := ids.Validate("repo", source.Projection.RepositoryID); err != nil {
			return projectSnapshot{}, nil, fmt.Errorf("repository projection id: %w", err)
		}
		if _, duplicate := seen[source.Projection.RepositoryID]; duplicate {
			return projectSnapshot{}, nil, fmt.Errorf("duplicate repository projection %s", source.Projection.RepositoryID)
		}
		seen[source.Projection.RepositoryID] = struct{}{}
		card := buildRepositoryCard(contextProjection, repository)
		cardJSON, marshalErr := json.Marshal(card)
		if marshalErr != nil {
			return projectSnapshot{}, nil, marshalErr
		}
		snapshotDigest, digestErr := deterministicRepositoryCardDigest(card)
		if digestErr != nil {
			return projectSnapshot{}, nil, digestErr
		}
		item := repositorySnapshot{
			ProjectID: project.ProjectID, RepositoryID: card.RepositoryID,
			MembershipRevision: source.SourceVersion, MembershipDigest: source.Projection.SourceBindingDigest,
			SourceVersion: card.Freshness.SourceVersion,
			SourceDigest:  card.Freshness.SourceDigest, SnapshotDigest: snapshotDigest,
			ObservedCommit: card.Freshness.ObservedCommit, TrackingStatus: card.TrackingStatus,
			ObservedAt:        card.Freshness.ObservedAt,
			SearchableSummary: repositorySearchableSummary(card), CardJSON: cardJSON, CreatedAt: createdAt,
		}
		repositories = append(repositories, item)
	}
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].RepositoryID < repositories[j].RepositoryID })
	return project, repositories, nil
}

const unregisteredProjectProjectionSourceSchemaVersion = "loom.provenance.project_projection.unregistered_source.v1"

func projectionProjectSource(projection projectstate.ProjectProjection, projectDigest string) (string, string, error) {
	switch projection.Source.Posture {
	case projectstate.SourcePostureRegistered:
		if projection.Source.SourceRevision <= 0 || !validProjectionDigest(projection.Source.SemanticDigest) {
			return "", "", errors.New("registered project projection requires a positive source revision and valid semantic digest")
		}
		return projection.Source.SemanticDigest, projection.SchemaVersion, nil
	case projectstate.SourcePostureNotRegistered:
		if projection.Source.SourceRevision != 0 || strings.TrimSpace(projection.Source.SemanticDigest) != "" ||
			len(projection.Members) != 0 {
			return "", "", errors.New("unregistered project projection requires zero source revision, no semantic digest, and no members")
		}
		payload, err := json.Marshal(struct {
			SchemaVersion  string `json:"schema_version"`
			ProjectID      string `json:"project_id"`
			SnapshotDigest string `json:"snapshot_digest"`
		}{
			SchemaVersion:  unregisteredProjectProjectionSourceSchemaVersion,
			ProjectID:      projection.Project.ProjectID,
			SnapshotDigest: projectDigest,
		})
		if err != nil {
			return "", "", err
		}
		return digestProjection(payload), unregisteredProjectProjectionSourceSchemaVersion, nil
	default:
		return "", "", fmt.Errorf("unsupported project projection source posture %q", projection.Source.Posture)
	}
}

func projectProjectionSourceIdentity(project projectSnapshot) (string, error) {
	if project.SourceRevision < 0 || !validProjectionDigest(project.SourceDigest) || !validProjectionDigest(project.SnapshotDigest) ||
		strings.TrimSpace(project.SourceSchemaVersion) == "" {
		return "", errors.New("project projection requires valid deterministic source identity")
	}
	payload, err := json.Marshal(struct {
		SchemaVersion       string `json:"schema_version"`
		SourceRevision      int64  `json:"source_revision"`
		SourceDigest        string `json:"source_digest"`
		SourceSchemaVersion string `json:"source_schema_version"`
		SnapshotDigest      string `json:"snapshot_digest"`
	}{
		SchemaVersion:       ProjectProjectionSchemaVersion,
		SourceRevision:      project.SourceRevision,
		SourceDigest:        project.SourceDigest,
		SourceSchemaVersion: project.SourceSchemaVersion,
		SnapshotDigest:      project.SnapshotDigest,
	})
	if err != nil {
		return "", err
	}
	return digestProjection(payload), nil
}

// repositoryProjectionSourceIdentity contains the complete deterministic card
// digest, accepted membership binding, and exact parent project snapshot. It
// therefore advances for every substantive card/source input and every project
// source revision, but excludes the sole volatile observation wall-clock field
// through deterministicRepositoryCardDigest. appendRepositoryProjection still
// compares the stored card and parent for the same identity, so forged or
// inconsistent internal inputs fail closed.
func repositoryProjectionSourceIdentity(project projectSnapshot, snapshot repositorySnapshot) (string, error) {
	if snapshot.MembershipRevision <= 0 || !validProjectionDigest(snapshot.MembershipDigest) {
		return "", errors.New("repository projection requires a positive binding revision and valid binding digest")
	}
	if project.ID == "" || project.ProjectionRevision <= 0 || project.SourceRevision < 0 || !validProjectionDigest(project.SnapshotDigest) || !validProjectionDigest(snapshot.SnapshotDigest) {
		return "", errors.New("repository projection requires a valid deterministic card digest")
	}
	payload, err := json.Marshal(struct {
		SchemaVersion             string     `json:"schema_version"`
		ProjectSnapshotID         SemanticID `json:"project_snapshot_id"`
		ProjectProjectionRevision int64      `json:"project_projection_revision"`
		ProjectSourceRevision     int64      `json:"project_source_revision"`
		ProjectSnapshotDigest     string     `json:"project_snapshot_digest"`
		MembershipSourceRevision  int64      `json:"membership_source_revision"`
		MembershipSourceDigest    string     `json:"membership_source_digest"`
		CardDigest                string     `json:"card_digest"`
	}{
		SchemaVersion:             RepositoryCardSchemaVersion,
		ProjectSnapshotID:         project.ID,
		ProjectProjectionRevision: project.ProjectionRevision,
		ProjectSourceRevision:     project.SourceRevision,
		ProjectSnapshotDigest:     project.SnapshotDigest,
		MembershipSourceRevision:  snapshot.MembershipRevision,
		MembershipSourceDigest:    snapshot.MembershipDigest,
		CardDigest:                snapshot.SnapshotDigest,
	})
	if err != nil {
		return "", err
	}
	return digestProjection(payload), nil
}

func buildRepositoryCard(project ProjectContextProjection, projection repostate.ProvenanceProjection) RepositoryCard {
	source := projection.Source.Projection
	status := repositoryTrackingStatus(projection)
	card := RepositoryCard{
		SchemaVersion: RepositoryCardSchemaVersion,
		RepositoryID:  source.RepositoryID, Name: boundedProjectionText(source.Key, maximumProjectionTextRunes),
		Aliases:       []string{},
		OwningProject: RepositoryOwningProject{ProjectID: project.ProjectID, Name: project.Name, Slug: project.Slug, Lifecycle: project.Lifecycle, NavigationRef: project.NavigationRef},
		Role:          string(source.Role), Purpose: "", Topics: []string{},
		CurrentState: string(source.RepositoryLifecycle), ActiveFocus: []string{}, RecentOutcomes: []string{}, NextPriorities: []string{}, Blockers: []string{},
		AcceptedContext:       []RepositoryAcceptedContext{},
		Freshness:             RepositoryFreshness{Posture: "not_observed", FrontierPosture: "not_observed"},
		PortableNavigationRef: "loom-repo://" + source.RepositoryID,
		TrackingStatus:        status, Diagnostics: repositoryDiagnostics(projection),
		FieldSources: repositoryDefaultFieldSources(),
	}
	if card.Name == "" {
		card.Name = source.RepositoryID
	}
	if projection.Available {
		applyRepositoryDiscovery(&card, projection.Extraction)
	}
	card.Freshness = repositoryFreshness(projection, status)
	card.FieldSources["freshness"] = RepositoryFieldSource{Source: "git_observation", Posture: "observed"}
	if card.Freshness.ObservedCommit == nil {
		card.FieldSources["freshness"] = RepositoryFieldSource{Source: "repo_state_validation", Posture: "observed"}
	}
	return card
}

func applyRepositoryDiscovery(card *RepositoryCard, extraction repostate.Extraction) {
	for _, field := range extraction.DiscoveryFields {
		posture := string(field.Source.Posture)
		sourceName := "repo_state"
		switch field.Name {
		case "name":
			if value := projectionString(field.Value); value != "" {
				card.Name = boundedProjectionText(value, maximumProjectionTextRunes)
				card.FieldSources[field.Name] = RepositoryFieldSource{Source: sourceName, Posture: posture}
			}
		case "aliases":
			card.Aliases = boundedProjectionStrings(projectionStrings(field.Value), maximumProjectionListValues)
			card.FieldSources[field.Name] = RepositoryFieldSource{Source: sourceName, Posture: posture}
		case "role":
			if value := projectionString(field.Value); value != "" {
				card.Role = value
				card.FieldSources[field.Name] = RepositoryFieldSource{Source: sourceName, Posture: posture}
			}
		case "purpose":
			card.Purpose = boundedProjectionText(projectionString(field.Value), maximumProjectionTextRunes)
			card.FieldSources[field.Name] = RepositoryFieldSource{Source: sourceName, Posture: posture}
		case "topics":
			card.Topics = boundedProjectionStrings(projectionStrings(field.Value), maximumProjectionListValues)
			card.FieldSources[field.Name] = RepositoryFieldSource{Source: sourceName, Posture: posture}
		case "current_state":
			if value := projectionString(field.Value); value != "" {
				card.CurrentState = boundedProjectionText(value, maximumProjectionTextRunes)
				card.FieldSources[field.Name] = RepositoryFieldSource{Source: sourceName, Posture: posture}
			}
		case "active_focus":
			card.ActiveFocus = boundedProjectionOrderedStrings(projectionStrings(field.Value), maximumProjectionStateValues)
			card.FieldSources[field.Name] = RepositoryFieldSource{Source: sourceName, Posture: posture}
		case "recent_outcomes":
			card.RecentOutcomes = boundedProjectionOrderedStrings(projectionStrings(field.Value), maximumProjectionStateValues)
			card.FieldSources[field.Name] = RepositoryFieldSource{Source: sourceName, Posture: posture}
		case "next_priorities":
			card.NextPriorities = boundedProjectionOrderedStrings(projectionStrings(field.Value), maximumProjectionStateValues)
			card.FieldSources[field.Name] = RepositoryFieldSource{Source: sourceName, Posture: posture}
		case "blockers":
			card.Blockers = boundedProjectionOrderedStrings(projectionStrings(field.Value), maximumProjectionStateValues)
			card.FieldSources[field.Name] = RepositoryFieldSource{Source: sourceName, Posture: posture}
		}
	}
}

func repositoryDefaultFieldSources() map[string]RepositoryFieldSource {
	fields := map[string]RepositoryFieldSource{}
	for _, field := range []string{"name", "aliases", "role", "purpose", "topics", "current_state", "active_focus", "recent_outcomes", "next_priorities", "blockers"} {
		fields[field] = RepositoryFieldSource{Source: "project_membership", Posture: "declared"}
	}
	for _, field := range []string{"repository_id", "portable_navigation_ref"} {
		fields[field] = RepositoryFieldSource{Source: "project_membership", Posture: "deterministic"}
	}
	fields["owning_project"] = RepositoryFieldSource{Source: "project_context", Posture: "deterministic"}
	fields["accepted_context"] = RepositoryFieldSource{Source: "accepted_records", Posture: "accepted"}
	fields["freshness"] = RepositoryFieldSource{Source: "repo_state_validation", Posture: "observed"}
	fields["tracking_status"] = RepositoryFieldSource{Source: "repo_state_validation", Posture: "observed"}
	fields["diagnostics"] = RepositoryFieldSource{Source: "repo_state_validation", Posture: "observed"}
	return fields
}

func repositoryTrackingStatus(projection repostate.ProvenanceProjection) RepositoryTrackingStatus {
	if projection.Source.Projection.ObservationPosture == projects.ProjectRepositoryObservationRemoteUnavailable || projection.ReasonCode == "remote_unavailable" {
		return RepositoryTrackingRemoteUnavailable
	}
	if !projection.Available {
		return RepositoryTrackingNotObserved
	}
	switch projection.Extraction.TrackingStatus {
	case repostate.TrackingValid:
		return RepositoryTrackingValid
	case repostate.TrackingNotEnabled:
		return RepositoryTrackingNotEnabled
	case repostate.TrackingStaleVersion:
		return RepositoryTrackingStale
	case repostate.TrackingMalformed:
		return RepositoryTrackingMalformed
	case repostate.TrackingMismatchedOwner, repostate.TrackingMismatchedRepository,
		repostate.TrackingMismatchedMembership, repostate.TrackingMembershipUnresolved:
		return RepositoryTrackingMismatchedOwner
	default:
		return RepositoryTrackingNotObserved
	}
}

func repositoryFreshness(projection repostate.ProvenanceProjection, status RepositoryTrackingStatus) RepositoryFreshness {
	result := RepositoryFreshness{Posture: "not_observed", FrontierPosture: "not_observed"}
	if !projection.Available {
		return result
	}
	extraction := projection.Extraction
	if extraction.Manifest != nil && strings.TrimSpace(extraction.Manifest.SchemaVersion) != "" {
		value := extraction.Manifest.SchemaVersion
		result.SourceVersion = &value
	}
	if field, ok := repositoryDiscoveryField(extraction, "source_digest"); ok {
		if value := projectionString(field.Value); validProjectionDigest(value) {
			result.SourceDigest = &value
		}
	}
	if result.SourceDigest == nil && validProjectionDigest(extraction.ManifestSource.Digest) {
		value := extraction.ManifestSource.Digest
		result.SourceDigest = &value
	}
	if !extraction.Freshness.ObservedAt.IsZero() {
		value := extraction.Freshness.ObservedAt.UTC()
		result.ObservedAt = &value
	}
	if strings.TrimSpace(extraction.Freshness.SourceCommit) != "" {
		value := strings.ToLower(strings.TrimSpace(extraction.Freshness.SourceCommit))
		result.ObservedCommit = &value
		result.FrontierPosture = "observed_not_accepted_frontier"
		if projection.Source.Projection.Git != nil && strings.TrimSpace(projection.Source.Projection.Git.CurrentBranch) != "" {
			branch := projection.Source.Projection.Git.CurrentBranch
			result.SourceBranch = &branch
		}
	}
	if status == RepositoryTrackingValid && result.ObservedCommit != nil && len(extraction.Freshness.Reasons) == 0 {
		result.Posture = "observed_current"
	} else if status == RepositoryTrackingStale || len(extraction.Freshness.Reasons) > 0 {
		result.Posture = "stale"
	}
	return result
}

func repositoryDiagnostics(projection repostate.ProvenanceProjection) []string {
	values := []string{}
	if projection.ReasonCode != "" {
		values = append(values, projection.ReasonCode)
	}
	if projection.Source.Projection.ReasonCode != "" {
		values = append(values, projection.Source.Projection.ReasonCode)
	}
	if projection.Source.Projection.DevelopmentState.ReasonCode != "" {
		values = append(values, projection.Source.Projection.DevelopmentState.ReasonCode)
	}
	for _, problem := range projection.Source.Projection.Problems {
		values = append(values, problem.Code)
	}
	for _, issue := range projection.Extraction.Validation.Issues {
		values = append(values, issue.Code)
	}
	values = append(values, projection.Extraction.Freshness.Reasons...)
	if projection.Available && projection.Extraction.TrackingStatus == repostate.TrackingNotEnabled {
		values = append(values, "optional_repo_state_not_enabled")
	}
	return boundedProjectionStrings(values, maximumProjectionDiagnostics)
}

func repositoryDiscoveryField(extraction repostate.Extraction, name string) (repostate.ExtractedField, bool) {
	for _, field := range extraction.DiscoveryFields {
		if field.Name == name {
			return field, true
		}
	}
	return repostate.ExtractedField{}, false
}

func projectionString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

func projectionStrings(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if value := projectionString(item); value != "" {
				result = append(result, value)
			}
		}
		return result
	case string:
		if strings.TrimSpace(typed) == "" {
			return []string{}
		}
		return []string{strings.TrimSpace(typed)}
	default:
		return []string{}
	}
}

func boundedProjectionStrings(values []string, limit int) []string {
	result := boundedProjectionOrderedStrings(values, limit)
	sort.Strings(result)
	return result
}

func boundedProjectionOrderedStrings(values []string, limit int) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		value = boundedProjectionText(strings.TrimSpace(value), maximumProjectionTextRunes)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func boundedProjectionText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit]))
}

func boundedProjectionSummary(value string) string {
	return boundedProjectionText(value, maximumProjectionSummaryRunes)
}

func repositorySearchableSummary(card RepositoryCard) string {
	parts := []string{card.RepositoryID, card.Name, card.OwningProject.ProjectID, card.OwningProject.Name, card.OwningProject.Slug, card.Role, card.Purpose, card.CurrentState, string(card.TrackingStatus)}
	parts = append(parts, card.Aliases...)
	parts = append(parts, card.Topics...)
	parts = append(parts, card.ActiveFocus...)
	parts = append(parts, card.RecentOutcomes...)
	parts = append(parts, card.NextPriorities...)
	parts = append(parts, card.Blockers...)
	return boundedProjectionSummary(strings.Join(parts, " "))
}

func digestProjection(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// deterministicRepositoryCardDigest intentionally excludes only the wall
// clock at which an unchanged source was observed. That time remains in the
// append-only card payload, but it cannot turn a same-version/same-digest
// manual replay into a conflict. A changed source field, commit, posture, or
// declared summary still changes this digest.
func deterministicRepositoryCardDigest(card RepositoryCard) (string, error) {
	card.Freshness.ObservedAt = nil
	payload, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	return digestProjection(payload), nil
}

func stableProjectionID(parts ...string) SemanticID {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = digest.Write([]byte(fmt.Sprintf("%d:", len(part))))
		_, _ = digest.Write([]byte(part))
	}
	value := digest.Sum(nil)[:16]
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	return SemanticID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]))
}

func validProjectionDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && strings.ToLower(value) == value
}

func (store *Store) appendProjectProjection(ctx context.Context, snapshot projectSnapshot) (SemanticID, int64, bool, error) {
	if err := store.lockProjectProjection(ctx, snapshot.ProjectID); err != nil {
		return "", 0, false, err
	}
	var rawID, sourceIdentity, digest, sourceDigest, sourceSchemaVersion string
	var sourceRevision, projectionRevision int64
	err := store.q.queryRow(ctx, `
		SELECT id::text, projection_revision, source_identity_digest, source_revision, source_schema_version, snapshot_digest, source_digest
		FROM provenance.project_projection_snapshots
		WHERE project_id=$1
		ORDER BY projection_revision DESC, id
		LIMIT 1
	`, snapshot.ProjectID).Scan(&rawID, &projectionRevision, &sourceIdentity, &sourceRevision, &sourceSchemaVersion, &digest, &sourceDigest)
	if err == nil {
		if sourceIdentity == snapshot.SourceIdentityDigest {
			if sourceRevision != snapshot.SourceRevision || digest != snapshot.SnapshotDigest || sourceDigest != snapshot.SourceDigest {
				return "", 0, false, fmt.Errorf("%w: project=%s source_identity=%s", ErrProjectionReplayConflict, snapshot.ProjectID, snapshot.SourceIdentityDigest)
			}
			return SemanticID(rawID), projectionRevision, true, nil
		}
		if sourceRevision == snapshot.SourceRevision && sourceDigest != snapshot.SourceDigest &&
			sourceSchemaVersion != unregisteredProjectProjectionSourceSchemaVersion && snapshot.SourceSchemaVersion != unregisteredProjectProjectionSourceSchemaVersion {
			return "", 0, false, fmt.Errorf("%w: project=%s source_version=%d", ErrProjectionReplayConflict, snapshot.ProjectID, snapshot.SourceRevision)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", 0, false, fmt.Errorf("read latest project projection: %w", err)
	}
	if projectionRevision == math.MaxInt64 {
		return "", 0, false, fmt.Errorf("project projection revision overflow for %s", snapshot.ProjectID)
	}
	snapshot.ProjectionRevision = projectionRevision + 1
	snapshot.ID = stableProjectionID("project", snapshot.ProjectID, fmt.Sprint(snapshot.ProjectionRevision), snapshot.SnapshotDigest)
	rows, err := store.q.exec(ctx, `
		INSERT INTO provenance.project_projection_snapshots(
			id, project_id, projection_revision, source_revision, source_identity_digest,
			source_digest, source_schema_version, snapshot_digest, observed_at,
			searchable_summary, projection_json, created_at
		) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12)
	`, string(snapshot.ID), snapshot.ProjectID, snapshot.ProjectionRevision, snapshot.SourceRevision,
		snapshot.SourceIdentityDigest, snapshot.SourceDigest, snapshot.SourceSchemaVersion,
		snapshot.SnapshotDigest, snapshot.ObservedAt, snapshot.SearchableSummary,
		[]byte(snapshot.ProjectionJSON), snapshot.CreatedAt)
	if err != nil {
		return "", 0, false, fmt.Errorf("append project projection: %w", err)
	}
	if rows != 1 {
		return "", 0, false, fmt.Errorf("append project projection inserted %d rows", rows)
	}
	return snapshot.ID, snapshot.ProjectionRevision, false, nil
}

func (store *Store) appendRepositoryProjection(ctx context.Context, snapshot repositorySnapshot) (SemanticID, int64, bool, error) {
	if _, err := store.q.exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "loom:provenance:repository-projection:"+snapshot.RepositoryID); err != nil {
		return "", 0, false, fmt.Errorf("lock repository projection revision: %w", err)
	}
	var rawID, projectSnapshotID, sourceIdentity, digest string
	var sourceDigest *string
	var sourceRevision int64
	err := store.q.queryRow(ctx, `
		SELECT id::text, project_snapshot_id::text, source_revision, source_identity_digest, snapshot_digest, source_digest
		FROM provenance.repository_projection_snapshots
		WHERE repository_id=$1
		ORDER BY source_revision DESC, id
		LIMIT 1
	`, snapshot.RepositoryID).Scan(&rawID, &projectSnapshotID, &sourceRevision, &sourceIdentity, &digest, &sourceDigest)
	if err == nil {
		if sourceIdentity == snapshot.SourceIdentityDigest {
			if projectSnapshotID != string(snapshot.ProjectSnapshotID) || digest != snapshot.SnapshotDigest || !sameOptionalProjectionString(sourceDigest, snapshot.SourceDigest) {
				return "", 0, false, fmt.Errorf("%w: repository=%s source_identity=%s", ErrProjectionReplayConflict, snapshot.RepositoryID, snapshot.SourceIdentityDigest)
			}
			return SemanticID(rawID), sourceRevision, true, nil
		}
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", 0, false, fmt.Errorf("read repository projection identity: %w", err)
	}
	latestSourceRevision := sourceRevision
	if latestSourceRevision == math.MaxInt64 {
		return "", 0, false, fmt.Errorf("repository projection source revision overflow for %s", snapshot.RepositoryID)
	}
	snapshot.SourceRevision = latestSourceRevision + 1
	snapshot.ID = stableProjectionID("repository", snapshot.ProjectID, snapshot.RepositoryID, fmt.Sprint(snapshot.SourceRevision), snapshot.SnapshotDigest)
	rows, err := store.q.exec(ctx, `
		INSERT INTO provenance.repository_projection_snapshots(
			id, project_snapshot_id, project_id, repository_id, source_revision,
			source_identity_digest, source_version, source_digest, snapshot_digest, observed_commit,
			tracking_status, observed_at, searchable_summary, card_json, created_at
		) VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::jsonb, $15)
	`, string(snapshot.ID), string(snapshot.ProjectSnapshotID), snapshot.ProjectID,
		snapshot.RepositoryID, snapshot.SourceRevision, snapshot.SourceIdentityDigest, snapshot.SourceVersion,
		snapshot.SourceDigest, snapshot.SnapshotDigest, snapshot.ObservedCommit,
		snapshot.TrackingStatus, snapshot.ObservedAt, snapshot.SearchableSummary,
		[]byte(snapshot.CardJSON), snapshot.CreatedAt)
	if err != nil {
		return "", 0, false, fmt.Errorf("append repository projection: %w", err)
	}
	if rows != 1 {
		return "", 0, false, fmt.Errorf("append repository projection inserted %d rows", rows)
	}
	return snapshot.ID, snapshot.SourceRevision, false, nil
}

func sameOptionalProjectionString(stored, expected *string) bool {
	if stored == nil || expected == nil {
		return stored == nil && expected == nil
	}
	return *stored == *expected
}

func (api *FoundationAPI) ListRepositoryProjections(ctx context.Context, request RepositoryProjectionListRequest) (RepositoryProjectionList, error) {
	if api == nil || api.store == nil {
		return RepositoryProjectionList{}, errors.New("provenance projection runtime is not ready")
	}
	normalized, err := normalizeRepositoryProjectionListRequest(request)
	if err != nil {
		return RepositoryProjectionList{}, &FoundationValidationError{Err: err}
	}
	queryTerms, requiredQueryMatches := repositoryProjectionQueryTerms(normalized.Query)
	rows, err := api.store.q.query(ctx, `
		WITH latest_projects AS (
			SELECT DISTINCT ON (project_id) id, project_id
			FROM provenance.project_projection_snapshots
			ORDER BY project_id, projection_revision DESC, id
		), latest AS (
			SELECT DISTINCT ON (repository_id)
			       repository.repository_id, repository.project_id, repository.source_revision,
			       repository.searchable_summary, repository.card_json
			FROM provenance.repository_projection_snapshots AS repository
			JOIN latest_projects AS project
			  ON project.project_id=repository.project_id
			 AND project.id=repository.project_snapshot_id
			ORDER BY repository.repository_id, repository.source_revision DESC, repository.created_at DESC, repository.id
		)
		SELECT card_json
		FROM latest
		WHERE ($1::boolean OR (
			SELECT count(*)
			FROM unnest($2::text[]) AS query_term(value)
			WHERE position(query_term.value IN lower(searchable_summary)) > 0
		) >= $3::integer)
		  AND ($4::text = '' OR project_id = $4::text OR lower(card_json->'owning_project'->>'slug') = lower($4::text))
		  AND ($5::text = '' OR coalesce(card_json->'topics', '[]'::jsonb) ? lower($5::text))
		  AND ($6::text = '' OR lower(card_json->>'role') = lower($6::text))
		  AND ($7::text = '' OR card_json->>'tracking_status' = $7::text)
		ORDER BY repository_id
		LIMIT $8::integer
	`, normalized.Query == "", queryTerms, requiredQueryMatches, normalized.Project,
		normalized.Topic, normalized.Role, normalized.TrackingStatus, normalized.Limit+1)
	if err != nil {
		return RepositoryProjectionList{}, fmt.Errorf("list repository projections: %w", err)
	}
	defer rows.Close()
	items := []RepositoryCard{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return RepositoryProjectionList{}, fmt.Errorf("scan repository projection: %w", err)
		}
		var card RepositoryCard
		if err := json.Unmarshal(payload, &card); err != nil {
			return RepositoryProjectionList{}, fmt.Errorf("decode repository projection: %w", err)
		}
		items = append(items, card)
	}
	if err := rows.Err(); err != nil {
		return RepositoryProjectionList{}, fmt.Errorf("iterate repository projections: %w", err)
	}
	truncated := len(items) > normalized.Limit
	if truncated {
		items = items[:normalized.Limit]
	}
	for index := range items {
		items[index].AcceptedContext, err = api.store.acceptedRepositoryContext(ctx, items[index].OwningProject.ProjectID, items[index].RepositoryID)
		if err != nil {
			return RepositoryProjectionList{}, err
		}
	}
	return RepositoryProjectionList{
		SchemaVersion: RepositoryCardSchemaVersion, Ordering: "repository_id_asc",
		Items: items, Returned: len(items), Truncated: truncated,
	}, nil
}

func repositoryProjectionQueryTerms(query string) ([]string, int) {
	if query == "" {
		return []string{}, 0
	}
	terms := normalizeRepositoryProjectionQueryTerms(query)
	if len(terms) == 0 {
		return []string{}, 1
	}
	return terms, (len(terms) + 1) / 2
}

func normalizeRepositoryProjectionQueryTerms(query string) []string {
	stopWords := map[string]struct{}{
		"a": {}, "an": {}, "and": {}, "are": {}, "at": {}, "be": {}, "current": {},
		"did": {}, "do": {}, "does": {}, "find": {}, "for": {}, "from": {}, "has": {},
		"have": {}, "how": {}, "i": {}, "in": {}, "is": {}, "it": {}, "may": {},
		"must": {}, "now": {}, "of": {}, "on": {}, "open": {}, "or": {}, "our": {},
		"repository": {}, "repositories": {}, "should": {}, "show": {}, "the": {},
		"to": {}, "us": {}, "what": {}, "where": {}, "which": {}, "with": {},
		"working": {}, "why": {},
	}
	terms := []string{}
	seen := map[string]struct{}{}
	token := []rune{}
	flush := func() {
		if len(token) == 0 {
			return
		}
		rawValue := strings.ToLower(string(token))
		token = token[:0]
		if _, stop := stopWords[rawValue]; stop || utf8.RuneCountInString(rawValue) > MaximumSearchTermRunes {
			return
		}
		value := stemSearchTerm(rawValue)
		if _, duplicate := seen[value]; duplicate || len(terms) >= MaximumSearchTerms {
			return
		}
		seen[value] = struct{}{}
		terms = append(terms, value)
	}
	for _, character := range query {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			token = append(token, unicode.ToLower(character))
		} else {
			flush()
		}
	}
	flush()
	return terms
}

func (api *FoundationAPI) GetRepositoryProjection(ctx context.Context, repositoryID string) (RepositoryCard, error) {
	if api == nil || api.store == nil {
		return RepositoryCard{}, errors.New("provenance projection runtime is not ready")
	}
	repositoryID = strings.TrimSpace(repositoryID)
	if err := ids.Validate("repo", repositoryID); err != nil {
		return RepositoryCard{}, &FoundationValidationError{Err: fmt.Errorf("repository id: %w", err)}
	}
	var payload []byte
	err := api.store.q.queryRow(ctx, `
		WITH latest_projects AS (
			SELECT DISTINCT ON (project_id) id, project_id
			FROM provenance.project_projection_snapshots
			ORDER BY project_id, projection_revision DESC, id
		)
		SELECT repository.card_json
		FROM provenance.repository_projection_snapshots AS repository
		JOIN latest_projects AS project
		  ON project.project_id=repository.project_id
		 AND project.id=repository.project_snapshot_id
		WHERE repository.repository_id=$1
		ORDER BY repository.source_revision DESC, repository.created_at DESC, repository.id
		LIMIT 1
	`, repositoryID).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return RepositoryCard{}, ErrRepositoryProjectionNotFound
	}
	if err != nil {
		return RepositoryCard{}, fmt.Errorf("get repository projection: %w", err)
	}
	var card RepositoryCard
	if err := json.Unmarshal(payload, &card); err != nil {
		return RepositoryCard{}, fmt.Errorf("decode repository projection: %w", err)
	}
	card.AcceptedContext, err = api.store.acceptedRepositoryContext(ctx, card.OwningProject.ProjectID, card.RepositoryID)
	if err != nil {
		return RepositoryCard{}, err
	}
	return card, nil
}

func normalizeRepositoryProjectionListRequest(request RepositoryProjectionListRequest) (RepositoryProjectionListRequest, error) {
	request.Query = strings.TrimSpace(request.Query)
	request.Project = strings.TrimSpace(request.Project)
	request.Topic = strings.ToLower(strings.TrimSpace(request.Topic))
	request.Role = strings.ToLower(strings.TrimSpace(request.Role))
	request.TrackingStatus = RepositoryTrackingStatus(strings.ToLower(strings.TrimSpace(string(request.TrackingStatus))))
	for name, value := range map[string]string{"query": request.Query, "project": request.Project, "topic": request.Topic, "role": request.Role, "tracking_status": string(request.TrackingStatus)} {
		if len(value) > MaximumRepositoryProjectionFilter {
			return RepositoryProjectionListRequest{}, fmt.Errorf("repository %s filter exceeds %d bytes", name, MaximumRepositoryProjectionFilter)
		}
	}
	if request.Limit == 0 {
		request.Limit = DefaultRepositoryProjectionLimit
	}
	if request.Limit < 1 || request.Limit > MaximumRepositoryProjectionLimit {
		return RepositoryProjectionListRequest{}, fmt.Errorf("repository projection limit must be between 1 and %d", MaximumRepositoryProjectionLimit)
	}
	if request.Role != "" && request.Role != "primary" && request.Role != "component" {
		return RepositoryProjectionListRequest{}, errors.New("repository role filter must be primary or component")
	}
	if request.TrackingStatus != "" && !validRepositoryTrackingStatus(request.TrackingStatus) {
		return RepositoryProjectionListRequest{}, fmt.Errorf("unsupported repository tracking status %q", request.TrackingStatus)
	}
	return request, nil
}

func validRepositoryTrackingStatus(status RepositoryTrackingStatus) bool {
	switch status {
	case RepositoryTrackingValid, RepositoryTrackingNotEnabled, RepositoryTrackingStale,
		RepositoryTrackingMalformed, RepositoryTrackingMismatchedOwner,
		RepositoryTrackingNotObserved, RepositoryTrackingRemoteUnavailable:
		return true
	default:
		return false
	}
}

func (store *Store) acceptedRepositoryContext(ctx context.Context, projectID, repositoryID string) ([]RepositoryAcceptedContext, error) {
	rows, err := store.q.query(ctx, `
		SELECT record.id::text, record.claim, record.assertion_posture, record.record_context
		FROM provenance.records AS record
		WHERE (
			coalesce(record.anchors_json->'entities', '[]'::jsonb) ? $1::text OR
			coalesce(record.anchors_json->'projects', '[]'::jsonb) ? $2::text
		)
		AND NOT EXISTS (
			SELECT 1 FROM provenance.relationships AS relationship
			WHERE relationship.to_record_id = record.id
			  AND lower(btrim(relationship.relationship_type)) IN ('supersedes', 'corrects', 'correcting', 'refines')
			  AND NOT EXISTS (
				SELECT 1 FROM provenance.relationship_events AS event
				WHERE event.relationship_id=relationship.id AND event.event_type='ended'
			  )
		)
		AND NOT EXISTS (
			SELECT 1 FROM provenance.case_members AS member
			JOIN provenance.resolution_cases AS resolution_case ON resolution_case.id=member.case_id
			WHERE member.record_id=record.id
			  AND lower(btrim(resolution_case.initial_status)) NOT IN ('closed', 'resolved', 'dismissed')
			  AND NOT EXISTS (
				SELECT 1 FROM provenance.case_events AS event
				WHERE event.case_id=resolution_case.id
				  AND lower(btrim(event.outcome)) IN ('closed', 'resolved', 'dismissed')
			  )
		)
		ORDER BY
			CASE WHEN coalesce(record.anchors_json->'entities', '[]'::jsonb) ? $1::text THEN 0 ELSE 1 END,
			record.created_at DESC, record.id
		LIMIT $3::integer
	`, repositoryID, projectID, MaximumRepositoryAcceptedContext)
	if err != nil {
		return nil, fmt.Errorf("read accepted repository context: %w", err)
	}
	defer rows.Close()
	result := []RepositoryAcceptedContext{}
	for rows.Next() {
		var rawID, claim, posture, recordContext string
		if err := rows.Scan(&rawID, &claim, &posture, &recordContext); err != nil {
			return nil, fmt.Errorf("scan accepted repository context: %w", err)
		}
		qualification := posture
		if strings.TrimSpace(recordContext) != "" {
			qualification += ": " + recordContext
		}
		result = append(result, RepositoryAcceptedContext{
			RecordID: SemanticID(rawID), Summary: boundedProjectionText(claim, 280),
			Qualification: boundedProjectionText(qualification, 280),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accepted repository context: %w", err)
	}
	return result, nil
}
