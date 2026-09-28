package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/requestctx"
)

type ProjectReactivationState struct {
	Request       requestctx.Context `json:"request"`
	ActivatedAt   time.Time          `json:"activated_at"`
	ReleaseDigest string             `json:"release_digest"`
	EventID       string             `json:"event_id"`
}

// CompleteProjectReactivation changes only the project mutation guard. Runtime
// resources remain disabled until an ordinary apply/activation requests them.
func (s Service) CompleteProjectReactivation(ctx context.Context, req requestctx.Context, expected ProjectPhysicalArchiveState, digest string) (ProjectReactivationState, error) {
	var zero ProjectReactivationState
	if s.DB == nil || ValidateProjectPhysicalArchiveState(expected) != nil || expected.Restore == nil || expected.Restore.Phase != ProjectRestorePhaseComplete ||
		req.ActorID == "" || req.OriginNodeID == "" || req.CorrelationID == "" || !projectArchiveContractDigestPattern.MatchString(digest) {
		return zero, fmt.Errorf("reactivation requires exact completed restore and release evidence")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	p, err := scanProject(tx.QueryRowContext(ctx, projectSelectSQL()+` WHERE p.project_id=$1 FOR UPDATE OF p`, expected.ProjectID))
	if err != nil {
		return zero, err
	}
	current, ok := ParseProjectPhysicalArchiveState(p.ArchiveState)
	if !ok || !reflect.DeepEqual(current, expected) || p.Status != "active" || req.ScopeID != p.ProjectScopeID {
		return zero, fmt.Errorf("reactivation project or restore binding changed")
	}
	if err := validateProjectRestoreEventTx(ctx, tx, current, *current.Restore, p.ProjectScopeID); err != nil {
		return zero, err
	}
	if current.Restore.Reactivation != nil {
		if current.Restore.Reactivation.ReleaseDigest != digest {
			return zero, fmt.Errorf("reactivation release changed")
		}
		return *current.Restore.Reactivation, tx.Commit()
	}
	if err := execProjectRestoreRows(ctx, tx, `UPDATE projects.project_contract_registrations SET activation_status='base_active', updated_at=now() WHERE project_id=$1 AND registration_status='registered' AND activation_status='inactive'`, 1, p.ProjectID); err != nil {
		return zero, err
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	if at.Before(*current.Restore.RestoredAt) {
		at = *current.Restore.RestoredAt
	}
	receipt := ProjectReactivationState{Request: req, ActivatedAt: at, ReleaseDigest: digest}
	event, err := events.AppendTx(ctx, tx, events.AppendInput{EventType: events.TypeProjectBaseActivated, EventLevel: "audit", Request: req, ScopeID: p.ProjectScopeID,
		TargetKind: "project", TargetID: p.ProjectID, Status: "active", Result: "ok", VisibilityClass: "internal",
		Payload: map[string]any{"source": "project.physical_reactivation", "project_id": p.ProjectID, "archive_operation_id": current.OperationID, "restore_operation_id": current.Restore.OperationID, "release_digest": digest, "activated_at": at, "runtime_started": false}})
	if err != nil {
		return zero, err
	}
	receipt.EventID = event.EventID
	current.Restore.Reactivation = &receipt
	raw, err := json.Marshal(current)
	if err != nil {
		return zero, err
	}
	if err := execProjectRestoreRows(ctx, tx, `UPDATE projects.projects SET archive_state=$2, updated_at=now() WHERE project_id=$1`, 1, p.ProjectID, raw); err != nil {
		return zero, err
	}
	return receipt, tx.Commit()
}
