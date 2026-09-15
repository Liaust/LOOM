package projects

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/requestctx"
)

const ProjectPhysicalRestoreStateSchemaVersion = "project.physical_restore_state.v1"

type ProjectPhysicalRestorePhase string

const (
	ProjectRestorePhasePending             ProjectPhysicalRestorePhase = "restore_pending"
	ProjectRestorePhaseProjectStatePending ProjectPhysicalRestorePhase = "project_state_pending"
	ProjectRestorePhaseComplete            ProjectPhysicalRestorePhase = "complete"
)

// The containing archive remains immutable history. Restored custody is not
// authority to reactivate writers, so MutationBlocked remains true throughout.
type ProjectPhysicalRestoreState struct {
	SchemaVersion        string                      `json:"schema_version"`
	Request              requestctx.Context          `json:"request"`
	OperationID          string                      `json:"operation_id"`
	PlanDigest           string                      `json:"plan_digest"`
	WorkspacePlanDigest  string                      `json:"workspace_plan_digest"`
	Phase                ProjectPhysicalRestorePhase `json:"phase"`
	Status               string                      `json:"status"`
	MutationBlocked      bool                        `json:"mutation_blocked"`
	ActivationState      string                      `json:"activation_state"`
	StartedAt            time.Time                   `json:"started_at"`
	WorkspaceCompletedAt *time.Time                  `json:"workspace_completed_at,omitempty"`
	ActiveManifestDigest string                      `json:"active_manifest_digest,omitempty"`
	RestoredAt           *time.Time                  `json:"restored_at,omitempty"`
	EventID              string                      `json:"event_id,omitempty"`
}

type ProjectPhysicalRestoreTransitionInput struct {
	Archive ProjectPhysicalArchiveTransitionInput
	State   ProjectPhysicalRestoreState
}

type ProjectPhysicalRestoreTransitionResult struct {
	Project Project
	State   ProjectPhysicalRestoreState
	Replay  bool
}

var ErrProjectRestoreBlocked = errors.New("project restore requires explicit activation")

type ProjectRestoreBlockedError struct {
	ProjectID   string
	OperationID string
	Phase       ProjectPhysicalRestorePhase
}

func (e ProjectRestoreBlockedError) Error() string {
	return fmt.Sprintf("project %s restore %s is %s; mutation remains blocked pending explicit activation", e.ProjectID, e.OperationID, e.Phase)
}

func (e ProjectRestoreBlockedError) Unwrap() error { return ErrProjectRestoreBlocked }

func ValidateProjectPhysicalRestoreState(archive ProjectPhysicalArchiveState, state ProjectPhysicalRestoreState) error {
	archive.Restore = nil
	if err := ValidateProjectPhysicalArchiveState(archive); err != nil {
		return err
	}
	return validateProjectPhysicalRestoreState(archive, state, true)
}

func validateProjectPhysicalRestoreState(archive ProjectPhysicalArchiveState, state ProjectPhysicalRestoreState, requireEvent bool) error {
	if state.SchemaVersion != ProjectPhysicalRestoreStateSchemaVersion || archive.Phase != ProjectArchivePhaseComplete || archive.ArchivedAt == nil ||
		!projectArchiveOperationIDPattern.MatchString(state.OperationID) || state.OperationID == archive.OperationID ||
		!projectArchiveContractDigestPattern.MatchString(state.PlanDigest) || !projectArchiveContractDigestPattern.MatchString(state.WorkspacePlanDigest) ||
		!state.MutationBlocked || state.ActivationState != ProjectActivationStatusInactive {
		return fmt.Errorf("invalid project restore identity, binding or inactive posture")
	}
	for _, value := range []string{state.Request.ActorID, state.Request.OriginNodeID, state.Request.CorrelationID} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("project restore requires exact request identity")
		}
	}
	if !projectRestoreTimeValid(state.StartedAt) || state.StartedAt.Before(*archive.ArchivedAt) {
		return fmt.Errorf("project restore start time is invalid")
	}
	if state.Phase != ProjectRestorePhasePending && state.Phase != ProjectRestorePhaseProjectStatePending && state.Phase != ProjectRestorePhaseComplete {
		return fmt.Errorf("invalid project restore phase")
	}
	if state.Phase == ProjectRestorePhasePending {
		if state.WorkspaceCompletedAt != nil || state.ActiveManifestDigest != "" {
			return fmt.Errorf("project restore workspace evidence precedes its phase")
		}
	} else if state.WorkspaceCompletedAt == nil || !projectRestoreTimeValid(*state.WorkspaceCompletedAt) || state.WorkspaceCompletedAt.Before(state.StartedAt) || !projectArchiveContractDigestPattern.MatchString(state.ActiveManifestDigest) {
		return fmt.Errorf("project restore workspace evidence is missing or invalid")
	}
	if state.Phase == ProjectRestorePhaseComplete {
		if state.Status != "restored" || state.RestoredAt == nil || !projectRestoreTimeValid(*state.RestoredAt) || state.RestoredAt.Before(*state.WorkspaceCompletedAt) || (!projectRestoreEventIDValid(state.EventID) && (requireEvent || state.EventID != "")) {
			return fmt.Errorf("project restore terminal evidence is invalid")
		}
	} else if state.Status != "in_progress" || state.RestoredAt != nil || state.EventID != "" {
		return fmt.Errorf("project restore terminal evidence precedes completion")
	}
	return nil
}

func projectRestoreTimeValid(at time.Time) bool {
	return !at.IsZero() && at.Equal(at.UTC().Truncate(time.Microsecond))
}

func projectRestoreEventIDValid(value string) bool {
	return strings.HasPrefix(value, "event_") && projectArchiveOperationIDPattern.MatchString("workspace_archive_operation_"+strings.TrimPrefix(value, "event_"))
}

func ValidateProjectPhysicalRestoreTransition(current, next ProjectPhysicalRestoreState) error {
	immutableCurrent, immutableNext := current, next
	immutableCurrent.Phase, immutableNext.Phase = "", ""
	immutableCurrent.Status, immutableNext.Status = "", ""
	immutableCurrent.WorkspaceCompletedAt, immutableNext.WorkspaceCompletedAt = nil, nil
	immutableCurrent.ActiveManifestDigest, immutableNext.ActiveManifestDigest = "", ""
	immutableCurrent.RestoredAt, immutableNext.RestoredAt = nil, nil
	immutableCurrent.EventID, immutableNext.EventID = "", ""
	if !reflect.DeepEqual(immutableCurrent, immutableNext) {
		return fmt.Errorf("project restore immutable request or plan changed")
	}
	if current.Phase == next.Phase {
		if !reflect.DeepEqual(current, next) {
			return fmt.Errorf("project restore replay changed evidence")
		}
		return nil
	}
	if current.Phase == ProjectRestorePhasePending && next.Phase == ProjectRestorePhaseProjectStatePending {
		return nil
	}
	if current.Phase == ProjectRestorePhaseProjectStatePending && next.Phase == ProjectRestorePhaseComplete &&
		equalProjectArchiveTime(current.WorkspaceCompletedAt, next.WorkspaceCompletedAt) && current.ActiveManifestDigest == next.ActiveManifestDigest {
		return nil
	}
	return fmt.Errorf("project restore phase transition is not adjacent or changed workspace evidence")
}

// TransitionProjectPhysicalRestore never moves files or changes source rows.
// Generic custody evidence is bound by the coordinator before entering here.
func (s Service) TransitionProjectPhysicalRestore(ctx context.Context, req requestctx.Context, input ProjectPhysicalRestoreTransitionInput) (ProjectPhysicalRestoreTransitionResult, error) {
	var result ProjectPhysicalRestoreTransitionResult
	archive, next := input.Archive, input.State
	if s.DB == nil || archive.State.Restore != nil || ValidateProjectPhysicalArchiveState(archive.State) != nil || archive.State.Phase != ProjectArchivePhaseComplete ||
		archive.ExpectedScopeID == "" || archive.ExpectedRegistrationID == "" || archive.ExpectedRegistrationRevision <= 0 || !reflect.DeepEqual(req, next.Request) {
		return result, fmt.Errorf("project restore transition requires exact archive and live request binding")
	}
	// A new completion receives its event ID only from AppendTx below. Persisted
	// state and replay always require the actual event identity.
	if err := validateProjectPhysicalRestoreState(archive.State, next, false); err != nil {
		return result, err
	}
	members := append([]ProjectArchiveRepositoryMemberBinding(nil), archive.ExpectedMembers...)
	sort.Slice(members, func(i, j int) bool {
		return members[i].RepositoryID+"\x00"+members[i].MemberKey < members[j].RepositoryID+"\x00"+members[j].MemberKey
	})
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	project, err := scanProject(tx.QueryRowContext(ctx, projectSelectSQL()+` WHERE p.project_id = $1 FOR UPDATE OF p`, archive.State.ProjectID))
	if err != nil {
		return result, err
	}
	current, ok := ParseProjectPhysicalArchiveState(project.ArchiveState)
	previous := current.Restore
	current.Restore = nil
	if !ok || !reflect.DeepEqual(current, archive.State) || project.Slug != current.ProjectSlug || project.ProjectScopeID != archive.ExpectedScopeID {
		return result, fmt.Errorf("project restore original archive binding changed")
	}
	if previous == nil {
		if next.Phase != ProjectRestorePhasePending {
			return result, fmt.Errorf("project restore requires durable intent before advancing")
		}
	} else if err := ValidateProjectPhysicalRestoreTransition(*previous, next); err != nil {
		return result, err
	}
	terminalReplay := previous != nil && previous.Phase == ProjectRestorePhaseComplete
	wantProject, wantRegistration := "archived", ProjectRegistrationStatusArchived
	if terminalReplay {
		wantProject, wantRegistration = "active", ProjectRegistrationStatusRegistered
	}
	var registrationID, registrationStatus, activation, scopeStatus string
	var revision int
	if err := tx.QueryRowContext(ctx, `SELECT project_contract_registration_id, registration_revision, registration_status, activation_status FROM projects.project_contract_registrations WHERE project_id = $1 FOR UPDATE`, project.ProjectID).Scan(&registrationID, &revision, &registrationStatus, &activation); err != nil {
		return result, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT status FROM scopes.scopes WHERE scope_id = $1 FOR UPDATE`, archive.ExpectedScopeID).Scan(&scopeStatus); err != nil {
		return result, err
	}
	if project.Status != wantProject || scopeStatus != wantProject || registrationStatus != wantRegistration || activation != ProjectActivationStatusInactive || registrationID != archive.ExpectedRegistrationID || revision != archive.ExpectedRegistrationRevision {
		return result, fmt.Errorf("project restore current lifecycle or registration changed")
	}
	if err := validateProjectArchiveSourceRevisionTx(ctx, tx, project.ProjectID, archive.ExpectedRepositorySourceRevision); err != nil {
		return result, err
	}
	if err := validateProjectArchiveMembersTx(ctx, tx, project.ProjectID, members, wantProject); err != nil {
		return result, err
	}
	if err := validateProjectArchiveOwnedSetTx(ctx, tx, project.ProjectID, members); err != nil {
		return result, err
	}
	if previous != nil && previous.Phase == next.Phase {
		if terminalReplay {
			if err := validateProjectRestoreEventTx(ctx, tx, current, *previous, archive.ExpectedScopeID); err != nil {
				return result, err
			}
		}
		if err := tx.Commit(); err != nil {
			return result, err
		}
		return ProjectPhysicalRestoreTransitionResult{Project: project, State: *previous, Replay: true}, nil
	}
	if next.Phase == ProjectRestorePhaseComplete {
		if next.EventID != "" {
			return result, fmt.Errorf("project restore completion cannot supply an event ID")
		}
		owned := 0
		for _, member := range members {
			if member.RepositoryOwnerProjectID == project.ProjectID {
				owned++
			}
		}
		for _, statement := range []struct {
			query string
			count int
		}{
			{`UPDATE projects.project_contract_registrations SET registration_status = 'registered', activation_status = 'inactive', updated_at = now() WHERE project_id = $1`, 1},
			{`UPDATE projects.project_repository_memberships SET lifecycle_status = 'active', updated_at = now() WHERE project_id = $1`, len(members)},
			{`UPDATE projects.repositories r SET lifecycle_status = 'active', updated_at = now()
			  WHERE r.owning_project_id = $1 AND EXISTS (
			    SELECT 1 FROM projects.project_repository_memberships m
			    WHERE m.project_id = $1 AND m.repository_id = r.repository_id
			      AND m.repository_owner_project_id = $1 AND m.role IN ('primary', 'component'))`, owned},
		} {
			if err := execProjectRestoreRows(ctx, tx, statement.query, int64(statement.count), project.ProjectID); err != nil {
				return result, err
			}
		}
		if err := execProjectRestoreRows(ctx, tx, `UPDATE scopes.scopes SET status = 'active', updated_at = now() WHERE scope_id = $1`, 1, archive.ExpectedScopeID); err != nil {
			return result, err
		}
		event, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType: events.TypeProjectRestored, EventLevel: "audit", Request: req, ScopeID: archive.ExpectedScopeID,
			TargetKind: "project", TargetID: project.ProjectID, Status: "restored", Result: "ok", VisibilityClass: "internal",
			Payload: projectRestoreEventPayload(current, next),
		})
		if err != nil {
			return result, err
		}
		next.EventID = event.EventID
		project.Status = "active"
	}
	current.Restore = &next
	payload, err := json.Marshal(current)
	if err != nil {
		return result, err
	}
	if err := execProjectRestoreRows(ctx, tx, `UPDATE projects.projects SET archive_state = $2, status = $3, updated_at = now() WHERE project_id = $1`, 1, project.ProjectID, payload, project.Status); err != nil {
		return result, err
	}
	project.ArchiveState = payload
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return ProjectPhysicalRestoreTransitionResult{Project: project, State: next}, nil
}

func execProjectRestoreRows(ctx context.Context, tx *sql.Tx, query string, expected int64, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	actual, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("project restore update affected %d rows; expected %d", actual, expected)
	}
	return nil
}

func projectRestoreEventPayload(archive ProjectPhysicalArchiveState, state ProjectPhysicalRestoreState) map[string]any {
	return map[string]any{"project_id": archive.ProjectID, "project_slug": archive.ProjectSlug,
		"archive_operation_id": archive.OperationID, "restore_operation_id": state.OperationID,
		"plan_digest": state.PlanDigest, "workspace_plan_digest": state.WorkspacePlanDigest,
		"archive_manifest_digest": archive.ArchiveManifestDigest, "active_manifest_digest": state.ActiveManifestDigest,
		"active_path": archive.ActivePath, "activation_state": state.ActivationState, "restored_at": state.RestoredAt,
		"source": "project.physical_restore"}
}

func validateProjectRestoreEventTx(ctx context.Context, tx *sql.Tx, archive ProjectPhysicalArchiveState, state ProjectPhysicalRestoreState, scopeID string) error {
	payload, err := json.Marshal(projectRestoreEventPayload(archive, state))
	if err != nil {
		return err
	}
	var matches bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM events.events WHERE event_id=$1 AND event_type='project.restored'
		AND event_level='audit' AND actor_id=$2 AND origin_node_id=$3 AND scope_id=$4
		AND target_kind='project' AND target_id=$5 AND status='restored' AND result='ok'
		AND payload=$6::jsonb AND correlation_id=$7 AND visibility_class='internal'
	)`, state.EventID, state.Request.ActorID, state.Request.OriginNodeID, scopeID, archive.ProjectID, payload, state.Request.CorrelationID).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return fmt.Errorf("project restore completion event is missing or inconsistent")
	}
	return nil
}

// Retired ownership is retained evidence, not current membership. Exempt only
// archived nonmembers authenticated by this project's earlier owning source.
func validateProjectArchiveOwnedSetTx(ctx context.Context, tx *sql.Tx, projectID string, members []ProjectArchiveRepositoryMemberBinding) error {
	want := map[string]bool{}
	for _, member := range members {
		if member.RepositoryOwnerProjectID == projectID {
			want[member.RepositoryID] = true
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT repository_id, lifecycle_status FROM projects.repositories WHERE owning_project_id = $1 ORDER BY repository_id FOR UPDATE`, projectID)
	if err != nil {
		return err
	}
	retired := map[string]bool{}
	for rows.Next() {
		var id string
		var lifecycle RepositoryLifecycleStatus
		if err := rows.Scan(&id, &lifecycle); err != nil {
			rows.Close()
			return err
		}
		if want[id] {
			delete(want, id)
		} else if lifecycle == RepositoryLifecycleArchived {
			retired[id] = false
		} else {
			rows.Close()
			return fmt.Errorf("project archive owned repository set changed: active nonmember")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(want) != 0 {
		return fmt.Errorf("project archive owned repository set changed: missing current owner")
	}
	if len(retired) == 0 {
		return nil
	}
	current, canonical, err := readProjectRepositorySourceTx(ctx, tx, projectID)
	if err != nil {
		return err
	}
	for _, member := range canonical.Snapshot.Members {
		if _, exists := retired[member.RepositoryID]; exists {
			return fmt.Errorf("project archive retired repository remains in current source")
		}
	}
	history, err := tx.QueryContext(ctx, `
		SELECT to_jsonb(h)-'source_snapshot_json'||jsonb_build_object('source_snapshot',source_snapshot_json)
		FROM projects.project_repository_source_history h
		WHERE project_id = $1 AND source_revision < $2
		ORDER BY source_revision DESC
	`, projectID, current.SourceRevision)
	if err != nil {
		return err
	}
	defer history.Close()
	for history.Next() {
		var raw []byte
		if err := history.Scan(&raw); err != nil {
			return err
		}
		var source ProjectRepositorySource
		if !declarationDecode(raw, &source) || source.ProjectID != projectID {
			return fmt.Errorf("%w: retired repository historical source identity", ErrProjectRepositoryPersistenceCorrupt)
		}
		canonical, err := decodeAndValidateProjectRepositorySourceRow(source)
		if err != nil {
			return err
		}
		for _, member := range canonical.Snapshot.Members {
			if _, exists := retired[member.RepositoryID]; exists &&
				(member.Role == ProjectRepositoryRolePrimary || member.Role == ProjectRepositoryRoleComponent) {
				retired[member.RepositoryID] = true
			}
		}
	}
	if err := history.Err(); err != nil {
		return err
	}
	for _, proven := range retired {
		if !proven {
			return fmt.Errorf("project archive owned repository set changed: unproven retired owner")
		}
	}
	return nil
}
