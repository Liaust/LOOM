package projects

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/requestctx"
)

const ProjectPhysicalArchiveStateSchemaVersion = "project.physical_archive_state.v1"

type ProjectPhysicalArchivePhase string

const (
	ProjectArchivePhaseDeactivationPending ProjectPhysicalArchivePhase = "deactivation_pending"
	ProjectArchivePhaseRuntimeDeactivated  ProjectPhysicalArchivePhase = "runtime_deactivated"
	ProjectArchivePhaseProjectStatePending ProjectPhysicalArchivePhase = "project_state_pending"
	ProjectArchivePhaseComplete            ProjectPhysicalArchivePhase = "complete"
)

const (
	ProjectPhysicalArchiveStatusInProgress = "in_progress"
	ProjectPhysicalArchiveStatusArchived   = "archived"
)

var projectArchiveOperationIDPattern = regexp.MustCompile(`^workspace_archive_operation_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

// ProjectPhysicalArchiveState is the durable project-side half of a physical
// archive operation. MutationBlocked is set before runtime deactivation begins,
// so a crash can never reopen an unsafe activation window.
type ProjectPhysicalArchiveState struct {
	SchemaVersion           string                       `json:"schema_version"`
	Status                  string                       `json:"status"`
	Phase                   ProjectPhysicalArchivePhase  `json:"phase"`
	MutationBlocked         bool                         `json:"mutation_blocked"`
	ProjectID               string                       `json:"project_id"`
	ProjectSlug             string                       `json:"project_slug"`
	OperationID             string                       `json:"operation_id"`
	PlanDigest              string                       `json:"plan_digest"`
	WorkspacePlanDigest     string                       `json:"workspace_plan_digest"`
	ActivePath              string                       `json:"active_path"`
	ArchivePath             string                       `json:"archive_path"`
	RuntimeQuiescenceDigest string                       `json:"runtime_quiescence_digest,omitempty"`
	ArchiveManifestDigest   string                       `json:"archive_manifest_digest,omitempty"`
	ActorID                 string                       `json:"actor_id"`
	Reason                  string                       `json:"reason"`
	StartedAt               time.Time                    `json:"started_at"`
	RuntimeDeactivatedAt    *time.Time                   `json:"runtime_deactivated_at,omitempty"`
	WorkspaceCompletedAt    *time.Time                   `json:"workspace_completed_at,omitempty"`
	ArchivedAt              *time.Time                   `json:"archived_at,omitempty"`
	Restore                 *ProjectPhysicalRestoreState `json:"restore,omitempty"`
}

type ProjectArchiveRepositoryMemberBinding struct {
	RepositoryID             string                `json:"repository_id"`
	RepositoryOwnerProjectID string                `json:"repository_owner_project_id"`
	MemberKey                string                `json:"member_key"`
	Role                     ProjectRepositoryRole `json:"role"`
}

type ProjectPhysicalArchiveTransitionInput struct {
	State                            ProjectPhysicalArchiveState
	ExpectedScopeID                  string
	ExpectedRegistrationID           string
	ExpectedRegistrationRevision     int
	ExpectedRepositorySourceRevision int64
	ExpectedMembers                  []ProjectArchiveRepositoryMemberBinding
}

type ProjectPhysicalArchiveTransitionResult struct {
	Project Project
	State   ProjectPhysicalArchiveState
	EventID string
	Replay  bool
}

func ParseProjectPhysicalArchiveState(raw json.RawMessage) (ProjectPhysicalArchiveState, bool) {
	if len(raw) == 0 || string(raw) == "{}" || string(raw) == "null" {
		return ProjectPhysicalArchiveState{}, false
	}
	var state ProjectPhysicalArchiveState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || state.SchemaVersion != ProjectPhysicalArchiveStateSchemaVersion {
		return ProjectPhysicalArchiveState{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ProjectPhysicalArchiveState{}, false
	}
	return state, ValidateProjectPhysicalArchiveState(state) == nil
}

func ValidateProjectPhysicalArchiveState(state ProjectPhysicalArchiveState) error {
	if state.SchemaVersion != ProjectPhysicalArchiveStateSchemaVersion {
		return fmt.Errorf("unsupported project physical archive state schema %q", state.SchemaVersion)
	}
	if strings.TrimSpace(state.ProjectID) == "" || !slugPattern.MatchString(state.ProjectSlug) || !projectArchiveOperationIDPattern.MatchString(state.OperationID) {
		return fmt.Errorf("invalid project physical archive identity")
	}
	if !projectArchiveContractDigestPattern.MatchString(state.PlanDigest) || !projectArchiveContractDigestPattern.MatchString(state.WorkspacePlanDigest) {
		return fmt.Errorf("invalid project physical archive plan digest")
	}
	if strings.TrimSpace(state.ActivePath) == "" || strings.TrimSpace(state.ArchivePath) == "" || strings.TrimSpace(state.ActorID) == "" || strings.TrimSpace(state.Reason) == "" || state.StartedAt.IsZero() || !state.MutationBlocked {
		return fmt.Errorf("incomplete project physical archive state")
	}
	rank, ok := projectPhysicalArchivePhaseRank(state.Phase)
	if !ok {
		return fmt.Errorf("unsupported project physical archive phase %q", state.Phase)
	}
	if rank < 2 && state.RuntimeDeactivatedAt != nil {
		return fmt.Errorf("runtime deactivation time precedes its phase")
	}
	if rank < 2 && state.RuntimeQuiescenceDigest != "" {
		return fmt.Errorf("runtime quiescence evidence precedes its phase")
	}
	if rank >= 2 && (state.RuntimeDeactivatedAt == nil || state.RuntimeDeactivatedAt.Before(state.StartedAt) || !projectArchiveContractDigestPattern.MatchString(state.RuntimeQuiescenceDigest)) {
		return fmt.Errorf("runtime deactivation time is missing or out of order")
	}
	if rank < 3 && (state.WorkspaceCompletedAt != nil || state.ArchiveManifestDigest != "") {
		return fmt.Errorf("workspace completion evidence precedes its phase")
	}
	if rank >= 3 && (state.WorkspaceCompletedAt == nil || state.WorkspaceCompletedAt.Before(*state.RuntimeDeactivatedAt) || !projectArchiveContractDigestPattern.MatchString(state.ArchiveManifestDigest)) {
		return fmt.Errorf("workspace completion evidence is missing or out of order")
	}
	if state.Phase == ProjectArchivePhaseComplete {
		if state.Status != ProjectPhysicalArchiveStatusArchived || state.ArchivedAt == nil || state.ArchivedAt.Before(*state.WorkspaceCompletedAt) {
			return fmt.Errorf("completed project physical archive state is incomplete")
		}
	} else if state.Status != ProjectPhysicalArchiveStatusInProgress || state.ArchivedAt != nil {
		return fmt.Errorf("in-progress project physical archive state has invalid terminal evidence")
	}
	if state.Restore != nil {
		if state.Phase != ProjectArchivePhaseComplete {
			return fmt.Errorf("project restore requires a completed archive")
		}
		return ValidateProjectPhysicalRestoreState(state, *state.Restore)
	}
	return nil
}

func validateProjectPhysicalArchiveStateTransition(current, next ProjectPhysicalArchiveState) error {
	if current.SchemaVersion != next.SchemaVersion || current.MutationBlocked != next.MutationBlocked ||
		current.ProjectID != next.ProjectID || current.ProjectSlug != next.ProjectSlug ||
		current.OperationID != next.OperationID || current.PlanDigest != next.PlanDigest ||
		current.WorkspacePlanDigest != next.WorkspacePlanDigest || current.ActivePath != next.ActivePath ||
		current.ArchivePath != next.ArchivePath || current.ActorID != next.ActorID || current.Reason != next.Reason ||
		!current.StartedAt.Equal(next.StartedAt) {
		return fmt.Errorf("project archive immutable state changed")
	}
	currentRank, currentOK := projectPhysicalArchivePhaseRank(current.Phase)
	nextRank, nextOK := projectPhysicalArchivePhaseRank(next.Phase)
	if !currentOK || !nextOK || nextRank < currentRank || nextRank > currentRank+1 {
		return fmt.Errorf("project archive phase transition is not adjacent")
	}
	if nextRank == currentRank {
		if !reflect.DeepEqual(current, next) {
			return fmt.Errorf("project archive replay changed phase evidence")
		}
		return nil
	}
	if currentRank >= 2 && (!equalProjectArchiveTime(current.RuntimeDeactivatedAt, next.RuntimeDeactivatedAt) || current.RuntimeQuiescenceDigest != next.RuntimeQuiescenceDigest) {
		return fmt.Errorf("project archive runtime evidence changed")
	}
	if currentRank >= 3 && (!equalProjectArchiveTime(current.WorkspaceCompletedAt, next.WorkspaceCompletedAt) || current.ArchiveManifestDigest != next.ArchiveManifestDigest) {
		return fmt.Errorf("project archive workspace evidence changed")
	}
	if currentRank >= 4 && !equalProjectArchiveTime(current.ArchivedAt, next.ArchivedAt) {
		return fmt.Errorf("project archive terminal evidence changed")
	}
	return nil
}

func equalProjectArchiveTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func projectPhysicalArchiveStateRawPresent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "{}" && trimmed != "null"
}

func projectPhysicalArchivePhaseRank(phase ProjectPhysicalArchivePhase) (int, bool) {
	switch phase {
	case ProjectArchivePhaseDeactivationPending:
		return 1, true
	case ProjectArchivePhaseRuntimeDeactivated:
		return 2, true
	case ProjectArchivePhaseProjectStatePending:
		return 3, true
	case ProjectArchivePhaseComplete:
		return 4, true
	default:
		return 0, false
	}
}

func ProjectArchiveLockKeys(projectID, projectSlug, operationID string, repositoryIDs, runtimeFacets []string) []string {
	keys := []string{
		"loom:project-repository:project:id:" + strings.TrimSpace(projectID),
		"loom:project-repository:project:slug:" + strings.TrimSpace(projectSlug),
		"loom:project-archive:archive:" + strings.TrimSpace(operationID),
	}
	for _, repositoryID := range repositoryIDs {
		keys = append(keys, "loom:project-repository:repository:id:"+strings.TrimSpace(repositoryID))
	}
	for _, facet := range runtimeFacets {
		keys = append(keys, "loom:project-archive:runtime:"+strings.TrimSpace(projectID)+":"+strings.TrimSpace(facet))
	}
	return sortedUniqueProjectArchiveLockKeys(keys)
}

func sortedUniqueProjectArchiveLockKeys(keys []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || strings.HasSuffix(key, ":") {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

// AcquireProjectArchiveLocks holds session-level PostgreSQL advisory locks for
// the complete archive transaction. The project and repository keys are the
// same keys used by repository registration, so source/member mutation cannot
// race the reviewed plan.
func (s Service) AcquireProjectArchiveLocks(ctx context.Context, keys []string) (func() error, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("project database is not configured")
	}
	keys = sortedUniqueProjectArchiveLockKeys(keys)
	if len(keys) == 0 {
		return nil, fmt.Errorf("project archive lock keys are required")
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	acquired := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, key); err != nil {
			for index := len(acquired) - 1; index >= 0; index-- {
				_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, acquired[index])
			}
			_ = conn.Close()
			return nil, fmt.Errorf("acquire project archive advisory lock %q: %w", key, err)
		}
		acquired = append(acquired, key)
	}
	released := false
	return func() error {
		if released {
			return nil
		}
		released = true
		var releaseErr error
		for index := len(acquired) - 1; index >= 0; index-- {
			if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, acquired[index]); err != nil {
				releaseErr = errors.Join(releaseErr, fmt.Errorf("release project archive advisory lock %q: %w", acquired[index], err))
			}
		}
		return errors.Join(releaseErr, conn.Close())
	}, nil
}

// TransitionProjectPhysicalArchive records an in-progress recovery phase or
// atomically commits every current project lifecycle projection. Repository
// source and source-history rows are intentionally read-only here.
func (s Service) TransitionProjectPhysicalArchive(ctx context.Context, req requestctx.Context, input ProjectPhysicalArchiveTransitionInput) (ProjectPhysicalArchiveTransitionResult, error) {
	if input.State.Restore != nil {
		return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("archive transition cannot write project restore state")
	}
	if err := ValidateProjectPhysicalArchiveState(input.State); err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}
	input.ExpectedScopeID = strings.TrimSpace(input.ExpectedScopeID)
	input.ExpectedRegistrationID = strings.TrimSpace(input.ExpectedRegistrationID)
	if input.ExpectedScopeID == "" || input.ExpectedRegistrationID == "" || input.ExpectedRegistrationRevision <= 0 {
		return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("project archive transition binding is incomplete")
	}
	expectedMembers := append([]ProjectArchiveRepositoryMemberBinding(nil), input.ExpectedMembers...)
	sort.Slice(expectedMembers, func(i, j int) bool {
		return expectedMembers[i].RepositoryID+"\x00"+expectedMembers[i].MemberKey < expectedMembers[j].RepositoryID+"\x00"+expectedMembers[j].MemberKey
	})
	for index, member := range expectedMembers {
		if strings.TrimSpace(member.RepositoryID) == "" || strings.TrimSpace(member.MemberKey) == "" || strings.TrimSpace(member.RepositoryOwnerProjectID) == "" ||
			(index > 0 && expectedMembers[index-1].RepositoryID+"\x00"+expectedMembers[index-1].MemberKey >= member.RepositoryID+"\x00"+member.MemberKey) {
			return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("project archive member binding is invalid")
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}
	defer tx.Rollback()

	project, err := scanProject(tx.QueryRowContext(ctx, projectSelectSQL()+` WHERE p.project_id = $1 FOR UPDATE OF p`, input.State.ProjectID))
	if err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}
	if project.ProjectID != input.State.ProjectID || project.Slug != input.State.ProjectSlug || project.ProjectScopeID != input.ExpectedScopeID {
		return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("project archive transition project identity changed")
	}
	var registrationID, registrationStatus, activationStatus string
	var registrationRevision int
	if err := tx.QueryRowContext(ctx, `
		SELECT project_contract_registration_id, registration_revision, registration_status, activation_status
		FROM projects.project_contract_registrations
		WHERE project_id = $1
		FOR UPDATE
	`, input.State.ProjectID).Scan(&registrationID, &registrationRevision, &registrationStatus, &activationStatus); err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}
	if registrationID != input.ExpectedRegistrationID || registrationRevision != input.ExpectedRegistrationRevision {
		return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("project archive transition registration revision changed")
	}
	if err := validateProjectArchiveSourceRevisionTx(ctx, tx, input.State.ProjectID, input.ExpectedRepositorySourceRevision); err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}
	if err := validateProjectArchiveMembersTx(ctx, tx, input.State.ProjectID, expectedMembers, project.Status); err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}

	if err := validateProjectArchiveOwnedSetTx(ctx, tx, input.State.ProjectID, expectedMembers); err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}

	currentState, hasCurrentState := ParseProjectPhysicalArchiveState(project.ArchiveState)
	if hasCurrentState && currentState.Restore != nil {
		return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("project archive has entered restore; archive replay is read-only history")
	}
	if projectPhysicalArchiveStateRawPresent(project.ArchiveState) && !hasCurrentState {
		return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("project physical archive state is corrupt")
	}
	var scopeStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM scopes.scopes WHERE scope_id = $1 FOR UPDATE`, input.ExpectedScopeID).Scan(&scopeStatus); err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}
	if project.Status == "archived" {
		if registrationStatus != ProjectRegistrationStatusArchived || activationStatus != ProjectActivationStatusInactive {
			return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("archived project registration lifecycle is inconsistent")
		}
		if scopeStatus != "archived" {
			return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("archived project scope lifecycle is inconsistent")
		}
		if input.State.Phase != ProjectArchivePhaseComplete || !hasCurrentState || currentState.Phase != ProjectArchivePhaseComplete || !reflect.DeepEqual(currentState, input.State) {
			return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("project is archived by a different physical archive operation")
		}
		if err := tx.Commit(); err != nil {
			return ProjectPhysicalArchiveTransitionResult{}, err
		}
		return ProjectPhysicalArchiveTransitionResult{Project: project, State: currentState, Replay: true}, nil
	}
	if project.Status != "active" || scopeStatus != "active" || registrationStatus != ProjectRegistrationStatusRegistered {
		return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("project archive transition requires active registered project state")
	}
	if hasCurrentState {
		if err := validateProjectPhysicalArchiveStateTransition(currentState, input.State); err != nil {
			return ProjectPhysicalArchiveTransitionResult{}, err
		}
		if currentState.Phase == input.State.Phase {
			if err := tx.Commit(); err != nil {
				return ProjectPhysicalArchiveTransitionResult{}, err
			}
			return ProjectPhysicalArchiveTransitionResult{Project: project, State: currentState, Replay: true}, nil
		}
	}

	stateJSON, err := json.Marshal(input.State)
	if err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}
	if input.State.Phase != ProjectArchivePhaseComplete {
		if _, err := tx.ExecContext(ctx, `UPDATE projects.projects SET archive_state = $2, updated_at = now() WHERE project_id = $1`, input.State.ProjectID, stateJSON); err != nil {
			return ProjectPhysicalArchiveTransitionResult{}, err
		}
		project.ArchiveState = append(json.RawMessage(nil), stateJSON...)
		if err := tx.Commit(); err != nil {
			return ProjectPhysicalArchiveTransitionResult{}, err
		}
		return ProjectPhysicalArchiveTransitionResult{Project: project, State: input.State, Replay: hasCurrentState && currentState.Phase == input.State.Phase}, nil
	}

	if !hasCurrentState {
		return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("project archive completion requires durable in-progress state")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects.projects
		SET archive_state = $2, status = 'archived', updated_at = now()
		WHERE project_id = $1
	`, input.State.ProjectID, stateJSON); err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects.project_contract_registrations
		SET registration_status = 'archived', activation_status = 'inactive', updated_at = now()
		WHERE project_id = $1
	`, input.State.ProjectID); err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}
	if result, err := tx.ExecContext(ctx, `
		UPDATE projects.project_repository_memberships
		SET lifecycle_status = 'archived', updated_at = now()
		WHERE project_id = $1
	`, input.State.ProjectID); err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	} else if affected, err := result.RowsAffected(); err != nil || affected != int64(len(expectedMembers)) {
		return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("project archive member lifecycle count changed: got %d want %d", affected, len(expectedMembers))
	}
	ownedCount := 0
	for _, member := range expectedMembers {
		if member.RepositoryOwnerProjectID == input.State.ProjectID {
			ownedCount++
		}
	}
	if result, err := tx.ExecContext(ctx, `
		UPDATE projects.repositories r
		SET lifecycle_status = 'archived', updated_at = now()
		WHERE r.owning_project_id = $1
		  AND EXISTS (SELECT 1 FROM projects.project_repository_memberships m
		              WHERE m.project_id = $1 AND m.repository_id = r.repository_id
		                AND m.repository_owner_project_id = $1 AND m.role IN ('primary', 'component'))
	`, input.State.ProjectID); err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	} else if affected, err := result.RowsAffected(); err != nil || affected != int64(ownedCount) {
		return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("project archive repository lifecycle count changed: got %d want %d", affected, ownedCount)
	}
	if result, err := tx.ExecContext(ctx, `UPDATE scopes.scopes SET status = 'archived', updated_at = now() WHERE scope_id = $1`, input.ExpectedScopeID); err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	} else if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return ProjectPhysicalArchiveTransitionResult{}, fmt.Errorf("project archive scope lifecycle update affected %d rows", affected)
	}
	project.Status = "archived"
	project.ArchiveState = append(json.RawMessage(nil), stateJSON...)
	event, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeProjectArchived,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    input.ExpectedScopeID,
		TargetKind: "project",
		TargetID:   input.State.ProjectID,
		Status:     "archived",
		Result:     "ok",
		Payload: map[string]any{
			"project_id":              input.State.ProjectID,
			"project_slug":            input.State.ProjectSlug,
			"archive_operation_id":    input.State.OperationID,
			"plan_digest":             input.State.PlanDigest,
			"workspace_plan_digest":   input.State.WorkspacePlanDigest,
			"archive_path":            input.State.ArchivePath,
			"archive_manifest_digest": input.State.ArchiveManifestDigest,
			"source":                  "project.physical_archive",
		},
		VisibilityClass: "internal",
	})
	if err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectPhysicalArchiveTransitionResult{}, err
	}
	return ProjectPhysicalArchiveTransitionResult{Project: project, State: input.State, EventID: event.EventID}, nil
}

func validateProjectArchiveSourceRevisionTx(ctx context.Context, tx *sql.Tx, projectID string, expected int64) error {
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT source_revision FROM projects.project_repository_sources WHERE project_id = $1 FOR UPDATE`, projectID).Scan(&revision)
	if expected == 0 && errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if revision != expected {
		return fmt.Errorf("project archive repository source revision changed: got %d want %d", revision, expected)
	}
	return nil
}

func validateProjectArchiveMembersTx(ctx context.Context, tx *sql.Tx, projectID string, expected []ProjectArchiveRepositoryMemberBinding, projectStatus string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT m.repository_id, m.repository_owner_project_id, m.member_key, m.role,
		       m.lifecycle_status, r.lifecycle_status
		FROM projects.project_repository_memberships m
		JOIN projects.repositories r ON r.repository_id = m.repository_id
		WHERE m.project_id = $1
		ORDER BY m.repository_id, m.member_key
		FOR UPDATE OF m, r
	`, projectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type rowBinding struct {
		ProjectArchiveRepositoryMemberBinding
		membership RepositoryLifecycleStatus
		repository RepositoryLifecycleStatus
	}
	actual := []rowBinding{}
	for rows.Next() {
		var row rowBinding
		if err := rows.Scan(&row.RepositoryID, &row.RepositoryOwnerProjectID, &row.MemberKey, &row.Role, &row.membership, &row.repository); err != nil {
			return err
		}
		actual = append(actual, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("project archive repository member count changed: got %d want %d", len(actual), len(expected))
	}
	wantLifecycle := RepositoryLifecycleActive
	if projectStatus == "archived" {
		wantLifecycle = RepositoryLifecycleArchived
	}
	for index := range expected {
		if actual[index].ProjectArchiveRepositoryMemberBinding != expected[index] || actual[index].membership != wantLifecycle {
			return fmt.Errorf("project archive repository member %d changed", index)
		}
		wantRepository := RepositoryLifecycleActive
		if actual[index].RepositoryOwnerProjectID == projectID && projectStatus == "archived" {
			wantRepository = RepositoryLifecycleArchived
		}
		if actual[index].repository != wantRepository {
			return fmt.Errorf("project archive repository %s lifecycle changed", actual[index].RepositoryID)
		}
	}
	return nil
}
