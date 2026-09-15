package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projectstate"
)

// ProjectProjectionSnapshot returns captured content, never a later filesystem
// read. SourceRevision/SourceDigest describe registration; SnapshotDigest also
// covers deterministic development context, excluding Git commit IDs when the
// selected source and Git posture have not changed.
type ProjectProjectionSnapshot struct {
	SchemaVersion       string                   `json:"schema_version"`
	SnapshotID          SemanticID               `json:"snapshot_id"`
	ProjectID           string                   `json:"project_id"`
	ProjectionRevision  int64                    `json:"projection_revision"`
	SourceRevision      int64                    `json:"source_revision"`
	SourceDigest        string                   `json:"source_digest"`
	SourceSchemaVersion string                   `json:"source_schema_version"`
	SnapshotDigest      string                   `json:"snapshot_digest"`
	ObservedAt          time.Time                `json:"observed_at"`
	CreatedAt           time.Time                `json:"created_at"`
	Context             ProjectContextProjection `json:"context"`
}

type ProjectProjectionReader interface {
	GetProjectProjection(ctx context.Context, projectID string, snapshotID SemanticID) (ProjectProjectionSnapshot, error)
}

// GetProjectProjection selects the latest snapshot when snapshotID is empty.
// An explicit snapshot must belong to projectID, including historical captures.
func (api *FoundationAPI) GetProjectProjection(ctx context.Context, projectID string, snapshotID SemanticID) (ProjectProjectionSnapshot, error) {
	if api == nil || api.store == nil {
		return ProjectProjectionSnapshot{}, errors.New("provenance projection runtime is not ready")
	}
	projectID = strings.TrimSpace(projectID)
	if err := ids.Validate("project", projectID); err != nil {
		return ProjectProjectionSnapshot{}, &FoundationValidationError{Err: fmt.Errorf("project id: %w", err)}
	}
	if snapshotID != "" {
		if err := snapshotID.Validate(); err != nil {
			return ProjectProjectionSnapshot{}, &FoundationValidationError{Err: fmt.Errorf("project snapshot id: %w", err)}
		}
	}
	result := ProjectProjectionSnapshot{SchemaVersion: ProjectProjectionSchemaVersion}
	var rawID string
	var payload []byte
	err := api.store.q.queryRow(ctx, `
		SELECT id::text, project_id, projection_revision, source_revision,
		       source_digest, source_schema_version, snapshot_digest, observed_at,
		       created_at, projection_json
		FROM provenance.project_projection_snapshots
		WHERE project_id=$1 AND ($2::uuid IS NULL OR id=$2::uuid)
		ORDER BY projection_revision DESC, id
		LIMIT 1
	`, projectID, optionalProjectSnapshotID(snapshotID)).Scan(
		&rawID, &result.ProjectID, &result.ProjectionRevision, &result.SourceRevision,
		&result.SourceDigest, &result.SourceSchemaVersion, &result.SnapshotDigest,
		&result.ObservedAt, &result.CreatedAt, &payload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectProjectionSnapshot{}, ErrProjectProjectionNotFound
	}
	if err != nil {
		return ProjectProjectionSnapshot{}, fmt.Errorf("get project projection: %w", err)
	}
	result.SnapshotID, err = ParseSemanticID(rawID)
	if err != nil {
		return ProjectProjectionSnapshot{}, fmt.Errorf("decode project snapshot id: %w", err)
	}
	result.Context, err = decodeProjectContext(payload, projectID)
	if err != nil {
		return ProjectProjectionSnapshot{}, err
	}
	if result.ProjectID != projectID || snapshotID != "" && result.SnapshotID != snapshotID {
		return ProjectProjectionSnapshot{}, errors.New("project snapshot has mismatched identity")
	}
	result.ObservedAt = result.ObservedAt.UTC()
	result.CreatedAt = result.CreatedAt.UTC()
	return result, nil
}

func optionalProjectSnapshotID(id SemanticID) any {
	if id == "" {
		return nil
	}
	return string(id)
}

func decodeProjectContext(payload []byte, projectID string) (ProjectContextProjection, error) {
	var projection ProjectContextProjection
	if err := json.Unmarshal(payload, &projection); err != nil {
		return ProjectContextProjection{}, fmt.Errorf("decode project projection: %w", err)
	}
	if projection.ProjectID != projectID {
		return ProjectContextProjection{}, errors.New("project projection has mismatched identity")
	}
	if projection.Development != nil && projection.Development.ProjectID != projectID {
		return ProjectContextProjection{}, errors.New("project development has mismatched identity")
	}
	return projection, nil
}

func capturedProjectDevelopment(projection projectstate.ProjectProjection) (*projectstate.ProjectDevelopmentState, error) {
	development := projection.Development
	if development.Posture == "" && development.ProjectID == "" {
		// Preserve legacy callers' identity-only payload and deterministic digest.
		if !reflect.DeepEqual(development, projectstate.ProjectDevelopmentState{}) {
			return nil, errors.New("project development requires identity and posture")
		}
		return nil, nil
	}
	if development.ProjectID != projection.Project.ProjectID || development.SourceRevision != projection.Source.SourceRevision {
		return nil, errors.New("project development does not match its registered source identity")
	}
	switch development.Posture {
	case projectstate.ProjectDevelopmentReady, projectstate.ProjectDevelopmentMissing,
		projectstate.ProjectDevelopmentUnavailable, projectstate.ProjectDevelopmentInvalid,
		projectstate.ProjectDevelopmentMismatch, projectstate.ProjectDevelopmentArchived,
		projectstate.ProjectDevelopmentPartial, projectstate.ProjectDevelopmentNotRegistered:
	default:
		return nil, errors.New("project development posture is invalid")
	}
	for _, digest := range []string{development.RootDigest, development.SourceDigest} {
		if digest != "" && !validProjectionDigest(digest) {
			return nil, errors.New("project development digest is invalid")
		}
	}
	if development.OmittedFiles < 0 || development.CapturedBytes < 0 || development.CapturedBytes > projectstate.ProjectDevelopmentBytesLimit || len(development.Documents) > projectstate.ProjectDevelopmentFilesLimit || len(development.Features) > projectstate.ProjectDevelopmentFilesLimit || len(development.Decisions) > projectstate.ProjectDevelopmentFilesLimit {
		return nil, errors.New("project development exceeds capture bounds")
	}
	for _, document := range development.Documents {
		if !validProjectDevelopmentPath(document.Path) || document.Hash != "" && !validProjectionDigest(document.Hash) {
			return nil, errors.New("project development document has invalid source identity")
		}
	}
	for _, feature := range development.Features {
		if !validProjectDevelopmentPath(feature.Path) || !validProjectionDigest(feature.Hash) {
			return nil, errors.New("project development feature has invalid source identity")
		}
	}
	for _, decision := range development.Decisions {
		if !validProjectDevelopmentPath(decision.Path) || !validProjectionDigest(decision.Hash) {
			return nil, errors.New("project development decision has invalid source identity")
		}
	}
	payload, err := json.Marshal(development)
	if err != nil {
		return nil, err
	}
	// Leave room for envelope and snapshot metadata within the exact-get bound.
	if len(payload) > MaximumFoundationResponseBytes/2 {
		return nil, errors.New("project development exceeds encoded capture bound")
	}
	return &development, nil
}

func validProjectDevelopmentPath(value string) bool {
	if value == ".loom/project.yaml" || value == "AGENTS.md" {
		return true
	}
	return strings.HasPrefix(value, ".project/") && path.Clean(value) == value && !strings.Contains(value, "\\")
}

func deterministicProjectContextDigest(project ProjectContextProjection) (string, error) {
	if project.Development != nil && project.Development.Git != nil {
		development := *project.Development
		git := *development.Git
		// Unrelated commits may advance HEAD while every selected source byte
		// stays identical. Keep the earlier captured Git IDs on replay; a real
		// source or committed/uncommitted posture change still appends history.
		git.Head, git.Commit = "", ""
		development.Git = &git
		project.Development = &development
	}
	payload, err := json.Marshal(project)
	if err != nil {
		return "", err
	}
	return digestProjection(payload), nil
}

func projectSearchFields(project ProjectContextProjection) []searchField {
	fields := []searchField{
		{name: "project_id", value: project.ProjectID},
		{name: "name", value: project.Name},
		{name: "slug", value: project.Slug},
		{name: "lifecycle", value: project.Lifecycle},
	}
	if development := project.Development; development != nil {
		fields = append(fields,
			searchField{name: "purpose", value: development.Purpose},
			searchField{name: "current_focus", value: development.CurrentFocus},
			searchField{name: "progress", value: development.Progress},
			searchField{name: "blockers", value: development.Blockers},
			searchField{name: "next_action", value: development.NextAction},
			searchField{name: "roadmap", value: development.Roadmap},
			searchField{name: "structure", value: development.Structure},
		)
		for _, feature := range development.Features {
			fields = append(fields, searchField{name: "features", value: strings.Join([]string{feature.Slug, feature.Title, feature.Status, feature.NextAction, feature.BlockedReason}, " ")})
		}
		for _, decision := range development.Decisions {
			fields = append(fields, searchField{name: "decisions", value: strings.Join([]string{decision.ID, decision.Title, decision.Status, decision.Date}, " ")})
		}
	}
	return fields
}

func projectSearchableSummary(project ProjectContextProjection) string {
	var values []string
	for _, field := range projectSearchFields(project) {
		values = append(values, field.value)
	}
	return boundedProjectionSummary(strings.Join(values, " "))
}

func projectSearchCurrentness(project ProjectContextProjection) string {
	if project.Lifecycle == "archived" {
		return "archived"
	}
	if project.Development != nil && project.Development.Posture != "" {
		return string(project.Development.Posture)
	}
	return "not_observed"
}

func projectSearchSourceTruncated(project ProjectContextProjection) bool {
	if development := project.Development; development != nil {
		if !development.Complete || development.OmittedFiles > 0 {
			return true
		}
		for _, document := range development.Documents {
			if document.Truncated {
				return true
			}
		}
	}
	return false
}
