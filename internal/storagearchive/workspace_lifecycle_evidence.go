package storagearchive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"loom.local/loom/internal/storagecatalog"
)

// WorkspaceLifecycleEvidence authenticates a completed historical transition.
// It is not a current filesystem availability/fidelity or activation receipt.
// ReadLifecycleEvidence performs no journal write or filesystem operation.
type WorkspaceLifecycleEvidence struct {
	Operation      WorkspaceArchiveOperation
	Plan           WorkspaceArchivePlan
	ArchivePlan    WorkspaceArchivePlan
	Event          WorkspaceLifecycleEvent
	Manifest       WorkspaceArchiveManifest
	ManifestDigest string
}

func (s WorkspaceMoveService) ReadLifecycleEvidence(ctx context.Context, operationID string) (WorkspaceLifecycleEvidence, error) {
	refuse := func(err error) (WorkspaceLifecycleEvidence, error) {
		return WorkspaceLifecycleEvidence{}, fmt.Errorf("workspace lifecycle evidence: %w", err)
	}
	record, plan, operation, err := s.loadCompletedLifecycleOperation(ctx, operationID)
	if err != nil {
		return refuse(err)
	}
	archiveRecord, archivePlan, archiveOperation := record, plan, operation
	if plan.OperationKind == WorkspaceOperationRestore {
		archiveRecord, archivePlan, archiveOperation, err = s.loadCompletedLifecycleOperation(ctx, plan.ArchiveOperationID)
		if err != nil {
			return refuse(err)
		}
	}
	if archivePlan.OperationKind != WorkspaceOperationArchive {
		return refuse(fmt.Errorf("archive owner is not an archive operation"))
	}
	if len(archiveRecord.ManifestJSON) == 0 || len(archiveRecord.ManifestJSON) > workspaceManifestMaxBytes {
		return refuse(fmt.Errorf("manifest evidence is missing or oversized"))
	}
	current, err := DecodePhysicalWorkspaceArchiveManifest(archiveRecord.ManifestJSON)
	if err != nil {
		return refuse(err)
	}
	if err := s.verifyManifest(ctx, current, archivePlan); err != nil {
		return refuse(err)
	}
	archived, err := s.buildArchiveManifest(ctx, archiveOperation, archivePlan)
	if err != nil {
		return refuse(err)
	}
	archivedJSON, err := json.Marshal(archived)
	if err != nil {
		return refuse(err)
	}
	requested := archived
	if current.LifecycleState == WorkspaceLifecycleActive {
		_, restorePlan, restoreOperation, err := s.loadCompletedLifecycleOperation(ctx, current.RestoreOperationID)
		if err != nil {
			return refuse(err)
		}
		if restorePlan.OperationKind != WorkspaceOperationRestore || restorePlan.ArchiveOperationID != archiveOperation.OperationID || restorePlan.ArchiveManifestDigest != sha256Digest(archivedJSON) {
			return refuse(fmt.Errorf("restore does not bind the original authenticated archive"))
		}
		restored, err := s.buildRestoredManifest(ctx, archived, archiveOperation, restoreOperation, restorePlan)
		if err != nil {
			return refuse(err)
		}
		if !reflect.DeepEqual(current, restored) {
			return refuse(fmt.Errorf("durable manifest differs from completed restore evidence"))
		}
		if plan.OperationKind == WorkspaceOperationRestore {
			if plan.OperationID != restorePlan.OperationID || plan.PlanDigest != restorePlan.PlanDigest {
				return refuse(fmt.Errorf("requested restore differs from authenticated manifest"))
			}
			requested = restored
		}
	} else if !reflect.DeepEqual(current, archived) || plan.OperationKind != WorkspaceOperationArchive {
		return refuse(fmt.Errorf("durable archive manifest or requested transition differs"))
	}
	var event WorkspaceLifecycleEvent
	if len(record.EventJSON) == 0 || len(record.EventJSON) > 32768 {
		return refuse(fmt.Errorf("event evidence is missing or oversized"))
	}
	decoder := json.NewDecoder(bytes.NewReader(record.EventJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return refuse(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return refuse(fmt.Errorf("event has trailing data"))
	}
	if err := ValidateWorkspaceLifecycleEventForOperation(event, operation); err != nil {
		return refuse(err)
	}
	if event.EventID != requested.LifecycleEventID {
		return refuse(fmt.Errorf("event is not the authenticated transition"))
	}
	raw, err := json.Marshal(requested)
	if err != nil {
		return refuse(err)
	}
	return WorkspaceLifecycleEvidence{Operation: operation, Plan: plan, ArchivePlan: archivePlan, Event: event, Manifest: requested, ManifestDigest: sha256Digest(raw)}, nil
}

func (s WorkspaceMoveService) loadCompletedLifecycleOperation(ctx context.Context, operationID string) (storagecatalog.WorkspaceArchiveJournalRecord, WorkspaceArchivePlan, WorkspaceArchiveOperation, error) {
	var record storagecatalog.WorkspaceArchiveJournalRecord
	var plan WorkspaceArchivePlan
	var operation WorkspaceArchiveOperation
	fail := func(err error) (storagecatalog.WorkspaceArchiveJournalRecord, WorkspaceArchivePlan, WorkspaceArchiveOperation, error) {
		return record, plan, operation, err
	}
	if s.Journal == nil || s.ManifestKey == nil || s.ManifestKeyID == "" || !workspaceOperationID.MatchString(operationID) {
		return fail(fmt.Errorf("trusted journal, manifest key and exact operation are required"))
	}
	loaded, found, err := s.Journal.LoadWorkspaceArchiveJournal(ctx, operationID)
	if err != nil {
		return fail(err)
	}
	if !found {
		return fail(fmt.Errorf("operation is not present"))
	}
	if loaded.OperationID != operationID {
		return fail(fmt.Errorf("journal returned another operation"))
	}
	record = loaded
	if len(record.PlanJSON) == 0 || len(record.PlanJSON) > 268435456 || !json.Valid(record.PlanJSON) {
		return fail(fmt.Errorf("plan evidence is missing, malformed or oversized"))
	}
	plan, err = decodeJournalPlan(record)
	if err != nil {
		return fail(err)
	}
	if err := ValidateWorkspaceArchivePlan(plan, s.Roots); err != nil {
		return fail(err)
	}
	operation, err = operationFromJournal(record, plan)
	if err != nil {
		return fail(err)
	}
	if (operation.OperationKind == WorkspaceOperationArchive && operation.Phase != PhaseArchiveComplete) ||
		(operation.OperationKind == WorkspaceOperationRestore && operation.Phase != PhaseRestoreComplete) {
		return fail(fmt.Errorf("operation is not complete"))
	}
	return record, plan, operation, nil
}
