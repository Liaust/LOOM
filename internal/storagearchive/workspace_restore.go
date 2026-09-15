package storagearchive

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"reflect"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/storagecatalog"
)

const (
	workspaceRestoreIntentMarkerName = ".restore-intent.json"
	workspaceRestoreIntentMaxBytes   = 32 << 10
)

const (
	BoundaryBeforeRestoreIntent          WorkspaceMoveBoundary = "before_restore_intent"
	BoundaryAfterRestoreIntent           WorkspaceMoveBoundary = "after_restore_intent"
	BoundaryBeforeRestorePayloadMove     WorkspaceMoveBoundary = "before_restore_payload_move"
	BoundaryAfterRestoreCatalogRecheck   WorkspaceMoveBoundary = "after_restore_catalog_recheck"
	BoundaryAfterRestorePayloadMove      WorkspaceMoveBoundary = "after_restore_payload_move"
	BoundaryBeforeRestoreMovedRecord     WorkspaceMoveBoundary = "before_restore_payload_moved_record"
	BoundaryAfterRestoreMovedRecord      WorkspaceMoveBoundary = "after_restore_payload_moved_record"
	BoundaryBeforeRestoredManifest       WorkspaceMoveBoundary = "before_restored_manifest"
	BoundaryAfterRestoredManifestTemp    WorkspaceMoveBoundary = "after_restored_manifest_temp"
	BoundaryBeforeRestoredManifestCommit WorkspaceMoveBoundary = "before_restored_manifest_commit"
	BoundaryAfterRestoredManifest        WorkspaceMoveBoundary = "after_restored_manifest"
	BoundaryBeforeRestoreProjections     WorkspaceMoveBoundary = "before_restore_projections"
	BoundaryAfterRestoreProjections      WorkspaceMoveBoundary = "after_restore_projections"
	BoundaryBeforeRestoreIntentCleanup   WorkspaceMoveBoundary = "before_restore_intent_cleanup"
	BoundaryBeforeRestoreComplete        WorkspaceMoveBoundary = "before_restore_complete"
	BoundaryAfterRestoreComplete         WorkspaceMoveBoundary = "after_restore_complete"
	BoundaryRestoreClaimContended        WorkspaceMoveBoundary = "restore_claim_contended"
)

var errWorkspaceRestoreClaimGone = errors.New("workspace restore claim is no longer named")

type WorkspaceRestorePlanInput struct {
	OperationID        string
	ArchiveOperationID string
	ActorID            string
	Reason             string
	PlannedAt          time.Time
}

type WorkspaceActivationState string

const WorkspaceActivationInactive WorkspaceActivationState = "inactive"

type workspaceRestoreIntentMarker struct {
	SchemaVersion         string                 `json:"schema_version"`
	OperationID           string                 `json:"operation_id"`
	PlanDigest            string                 `json:"plan_digest"`
	ArchiveOperationID    string                 `json:"archive_operation_id"`
	ArchiveManifestDigest string                 `json:"archive_manifest_digest"`
	Kind                  WorkspaceKind          `json:"kind"`
	ObjectID              string                 `json:"object_id"`
	Slug                  string                 `json:"slug"`
	Source                WorkspacePathRef       `json:"source"`
	Destination           WorkspacePathRef       `json:"destination"`
	Authentication        ManifestAuthentication `json:"authentication"`
}

type workspaceRestoreGuard struct {
	sourceChain      *heldDirectoryChain
	destinationChain *heldDirectoryChain
	marker           *heldBoundedFile
}

func (guard *workspaceRestoreGuard) Close() {
	if guard == nil {
		return
	}
	if guard.marker != nil && guard.marker.file != nil {
		_ = unix.Flock(int(guard.marker.file.Fd()), unix.LOCK_UN)
		guard.marker.Close()
	}
	if guard.destinationChain != nil {
		guard.destinationChain.Close()
		guard.destinationChain = nil
	}
	if guard.sourceChain != nil {
		guard.sourceChain.Close()
		guard.sourceChain = nil
	}
}

func (guard *workspaceRestoreGuard) containerFD() int {
	return guard.sourceChain.ParentFD()
}

func (guard *workspaceRestoreGuard) verify() error {
	if guard == nil || guard.sourceChain == nil || guard.destinationChain == nil || guard.marker == nil || guard.marker.file == nil {
		return fmt.Errorf("workspace restore guard is closed")
	}
	if err := guard.sourceChain.verifyNamed(); err != nil {
		return err
	}
	if err := guard.destinationChain.verifyNamed(); err != nil {
		return err
	}
	if err := verifyNamedHeldFile(guard.containerFD(), workspaceRestoreIntentMarkerName, guard.marker); err != nil {
		return fmt.Errorf("restore intent marker was substituted while locked: %w", err)
	}
	payload, err := stableHeldFilePayload(guard.marker.file, workspaceRestoreIntentMaxBytes)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, guard.marker.payload) {
		return fmt.Errorf("restore intent marker bytes changed while locked")
	}
	return nil
}

func (s WorkspaceMoveService) PlanRestore(ctx context.Context, input WorkspaceRestorePlanInput) (WorkspaceArchivePlan, error) {
	if s.Catalog == nil {
		return WorkspaceArchivePlan{}, fmt.Errorf("workspace archive catalog is not configured")
	}
	if s.Journal == nil {
		return WorkspaceArchivePlan{}, fmt.Errorf("workspace archive journal is not configured")
	}
	if input.OperationID == "" {
		input.OperationID = newWorkspaceID("workspace_archive_operation")
	}
	if !workspacePathID(input.OperationID, workspaceOperationID) {
		return WorkspaceArchivePlan{}, fmt.Errorf("invalid workspace restore operation_id")
	}
	archivePlan, archiveOperation, archivedManifest, err := s.loadCompletedArchiveEvidence(ctx, input.ArchiveOperationID)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	paths, err := ResolveWorkspacePaths(s.Roots, archivePlan.Kind, archivePlan.Slug)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	source, destination, sourceAncestors, destinationAncestors, inventory, manifestDigest, err := s.observeRestorePlanEvidence(ctx, paths, archivePlan, archivedManifest, true)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	if !SamePathIdentity(source.Identity, archiveOperation.Source.Identity) || !reflect.DeepEqual(inventory, archivePlan.Inventory) {
		return WorkspaceArchivePlan{}, fmt.Errorf("archived payload no longer matches its completed archive operation")
	}
	catalogRebinds, err := s.planCatalogRebinds(ctx, paths.ArchivePayload, paths.Active)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	plan := WorkspaceArchivePlan{
		SchemaVersion:         WorkspaceArchivePlanSchemaVersion,
		EvidenceKind:          PhysicalWorkspaceMoveEvidence,
		OperationID:           input.OperationID,
		OperationKind:         WorkspaceOperationRestore,
		ArchiveOperationID:    archivePlan.OperationID,
		ArchiveManifestDigest: manifestDigest,
		Kind:                  archivePlan.Kind,
		ObjectID:              archivePlan.ObjectID,
		Slug:                  archivePlan.Slug,
		Source:                source,
		Destination:           destination,
		SourceAncestors:       sourceAncestors,
		DestinationAncestors:  destinationAncestors,
		Inventory:             inventory,
		CatalogRebinds:        catalogRebinds,
		ActorID:               strings.TrimSpace(input.ActorID),
		Reason:                strings.TrimSpace(input.Reason),
		PlannedAt:             normalizedPlannedAt(input.PlannedAt, s.now()),
	}
	if err := SealWorkspaceArchivePlan(&plan); err != nil {
		return WorkspaceArchivePlan{}, err
	}
	if err := s.validateExecutableRestorePlan(plan); err != nil {
		return WorkspaceArchivePlan{}, err
	}
	return plan, nil
}

func (s WorkspaceMoveService) InspectRestorePlan(ctx context.Context, plan WorkspaceArchivePlan) (WorkspaceArchiveStatus, error) {
	if err := s.validateExecutableRestorePlan(plan); err != nil {
		return WorkspaceArchiveStatus{}, err
	}
	findings, err := s.compareLiveRestorePlanEvidence(ctx, plan)
	if err != nil {
		if ctx.Err() != nil {
			return WorkspaceArchiveStatus{}, ctx.Err()
		}
		findings = []WorkspaceArchiveFinding{newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseRestorePlanned, err.Error(), true)}
	}
	status := WorkspaceArchiveStatus{
		SchemaVersion: WorkspaceArchiveStatusSchemaVersion,
		OperationID:   plan.OperationID,
		Phase:         PhaseRestorePlanned,
		Status:        OperationStatusPending,
		UpdatedAt:     s.nowAtLeast(plan.PlannedAt),
	}
	if len(findings) > 0 {
		status.Phase = PhaseBlocked
		status.LastSafePhase = PhaseRestorePlanned
		status.Status = OperationStatusBlocked
		status.Findings = findings
	}
	return status, nil
}

func (s WorkspaceMoveService) ApplyRestore(ctx context.Context, plan WorkspaceArchivePlan, reviewedDigest string) (WorkspaceArchiveInspection, error) {
	if reviewedDigest == "" || reviewedDigest != plan.PlanDigest {
		return WorkspaceArchiveInspection{}, fmt.Errorf("restore apply requires the exact reviewed plan digest")
	}
	if err := s.validateExecutableRestorePlan(plan); err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	journal, ok := s.Journal.(WorkspaceRestoreJournal)
	if !ok || journal == nil {
		return WorkspaceArchiveInspection{}, fmt.Errorf("workspace restore journal is not configured")
	}
	record, exists, err := journal.LoadWorkspaceArchiveJournal(ctx, plan.OperationID)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if exists {
		storedPlan, err := decodeJournalPlan(record)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		operation, err := operationFromJournal(record, storedPlan)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if same, finding := EvaluateReplay(operation, plan); !same {
			if finding != nil {
				return WorkspaceArchiveInspection{}, findingsError([]WorkspaceArchiveFinding{*finding})
			}
			return WorkspaceArchiveInspection{}, fmt.Errorf("operation id conflicts with an existing workspace operation")
		}
		return s.recoverRestore(ctx, journal, record, storedPlan)
	}
	findings, err := s.compareLiveRestorePlanEvidence(ctx, plan)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if len(findings) > 0 {
		return WorkspaceArchiveInspection{}, findingsError(findings)
	}
	if err := s.preflightAuthentication(ctx); err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if err := s.fail(BoundaryBeforeRestoreIntent); err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	intentAt := s.nowAtLeast(plan.PlannedAt)
	intent, err := journalIntentInput(plan, intentAt)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	record, _, err = journal.CommitWorkspaceArchiveIntent(ctx, intent)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	storedPlan, err := decodeJournalPlan(record)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	storedOperation, err := operationFromJournal(record, storedPlan)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if same, finding := EvaluateReplay(storedOperation, plan); !same {
		if finding != nil {
			return WorkspaceArchiveInspection{}, findingsError([]WorkspaceArchiveFinding{*finding})
		}
		return WorkspaceArchiveInspection{}, fmt.Errorf("operation id conflicts with an existing workspace operation")
	}
	if err := s.fail(BoundaryAfterRestoreIntent); err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	return s.recoverRestore(ctx, journal, record, storedPlan)
}

func (s WorkspaceMoveService) InspectOperation(ctx context.Context, operationID string) (WorkspaceArchiveInspection, error) {
	record, plan, err := s.loadOperationPlan(ctx, operationID)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if plan.OperationKind == WorkspaceOperationRestore {
		return s.inspectRestoreJournal(ctx, record, plan)
	}
	if manifest, ok := activeManifestFromRecord(record); ok {
		return s.inspectRestoredArchiveHistory(ctx, record, plan, manifest)
	}
	return s.inspectJournal(ctx, record, plan)
}

func (s WorkspaceMoveService) RecoverOperation(ctx context.Context, operationID string) (WorkspaceArchiveInspection, error) {
	record, plan, err := s.loadOperationPlan(ctx, operationID)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if plan.OperationKind == WorkspaceOperationRestore {
		journal, ok := s.Journal.(WorkspaceRestoreJournal)
		if !ok || journal == nil {
			return WorkspaceArchiveInspection{}, fmt.Errorf("workspace restore journal is not configured")
		}
		return s.recoverRestore(ctx, journal, record, plan)
	}
	if manifest, ok := activeManifestFromRecord(record); ok {
		return s.inspectRestoredArchiveHistory(ctx, record, plan, manifest)
	}
	return s.recoverArchive(ctx, record, plan)
}

func activeManifestFromRecord(record storagecatalog.WorkspaceArchiveJournalRecord) (WorkspaceArchiveManifest, bool) {
	if len(record.ManifestJSON) == 0 {
		return WorkspaceArchiveManifest{}, false
	}
	var manifest WorkspaceArchiveManifest
	if err := json.Unmarshal(record.ManifestJSON, &manifest); err != nil || manifest.LifecycleState != WorkspaceLifecycleActive || manifest.RestoreOperationID == "" {
		return WorkspaceArchiveManifest{}, false
	}
	return manifest, true
}

func (s WorkspaceMoveService) inspectRestoredArchiveHistory(ctx context.Context, record storagecatalog.WorkspaceArchiveJournalRecord, plan WorkspaceArchivePlan, activeManifest WorkspaceArchiveManifest) (WorkspaceArchiveInspection, error) {
	archiveOperation, err := operationFromJournal(record, plan)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if archiveOperation.Phase != PhaseArchiveComplete || archiveOperation.Status != OperationStatusComplete {
		return WorkspaceArchiveInspection{}, fmt.Errorf("active manifest is attached to an incomplete archive operation")
	}
	restoreRecord, restorePlan, err := s.loadOperationPlan(ctx, activeManifest.RestoreOperationID)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if restorePlan.OperationKind != WorkspaceOperationRestore || restorePlan.ArchiveOperationID != plan.OperationID {
		return WorkspaceArchiveInspection{}, fmt.Errorf("active manifest restore binding contradicts archive history")
	}
	restoreInspection, err := s.inspectRestoreJournal(ctx, restoreRecord, restorePlan)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if restoreInspection.Manifest == nil || !sameWorkspaceManifest(*restoreInspection.Manifest, activeManifest) {
		return WorkspaceArchiveInspection{}, fmt.Errorf("active manifest differs across archive and restore journals")
	}
	archivedManifest, err := s.buildArchiveManifest(ctx, archiveOperation, plan)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	archivedJSON, err := json.Marshal(archivedManifest)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	archiveProjection := record
	archiveProjection.ManifestJSON = archivedJSON
	if err := validateDurableWorkspaceProjection(archiveProjection, archiveOperation, archivedManifest); err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	return WorkspaceArchiveInspection{
		Operation: archiveOperation, Plan: plan, Custody: restoreInspection.Custody,
		Manifest: restoreInspection.Manifest, ActivationState: WorkspaceActivationInactive,
	}, nil
}

func (s WorkspaceMoveService) loadOperationPlan(ctx context.Context, operationID string) (storagecatalog.WorkspaceArchiveJournalRecord, WorkspaceArchivePlan, error) {
	if s.Journal == nil {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, WorkspaceArchivePlan{}, fmt.Errorf("workspace archive journal is not configured")
	}
	if !workspacePathID(operationID, workspaceOperationID) {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, WorkspaceArchivePlan{}, fmt.Errorf("invalid workspace operation id")
	}
	record, exists, err := s.Journal.LoadWorkspaceArchiveJournal(ctx, operationID)
	if err != nil {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, WorkspaceArchivePlan{}, err
	}
	if !exists {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, WorkspaceArchivePlan{}, fmt.Errorf("workspace operation %q was not found", operationID)
	}
	plan, err := decodeJournalPlan(record)
	return record, plan, err
}

func (s WorkspaceMoveService) recoverRestore(ctx context.Context, journal WorkspaceRestoreJournal, record storagecatalog.WorkspaceArchiveJournalRecord, plan WorkspaceArchivePlan) (WorkspaceArchiveInspection, error) {
	if err := s.validateExecutableRestorePlan(plan); err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	operation, err := operationFromJournal(record, plan)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if operation.Phase == PhaseManualRepairRequired {
		return s.inspectRestoreJournal(ctx, record, plan)
	}
	lastSafe := lastSafeRestorePhase(operation)
	if lastSafe == PhaseRestoreComplete {
		return s.inspectRestoreJournal(ctx, record, plan)
	}
	archivePlan, archiveOperation, archivedManifest, err := s.loadCompletedArchiveEvidence(ctx, plan.ArchiveOperationID)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	custody, _, finding, err := s.inspectRestoreCustody(ctx, plan, archivePlan, archiveOperation, archivedManifest, operation)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if finding != nil {
		manual := finding.Code == FindingAmbiguousCustody || finding.Code == FindingInvalidBinding
		recorded, recordErr := s.recordFailure(ctx, operation, *finding, manual)
		if recordErr != nil {
			return WorkspaceArchiveInspection{}, recordErr
		}
		inspection, inspectErr := s.inspectRestoreJournal(ctx, recorded, plan)
		if inspectErr != nil {
			return WorkspaceArchiveInspection{}, inspectErr
		}
		return inspection, findingsError([]WorkspaceArchiveFinding{*finding})
	}
	var guard *workspaceRestoreGuard
	if lastSafe == PhaseRestoreIntentCommitted || lastSafe == PhaseRestorePayloadMoved {
		if lastSafe == PhaseRestoreIntentCommitted && custody == CustodyArchived {
			guard, err = s.acquireRestoreGuard(ctx, plan, archivePlan, archivedManifest)
		} else {
			guard, err = s.acquireExistingRestoreGuard(ctx, plan)
		}
	} else if lastSafe == PhaseRestoreProjectionsCommitted {
		guard, err = s.acquireExistingRestoreGuard(ctx, plan)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	}
	if err != nil {
		if !errors.Is(err, errWorkspaceRestoreClaimGone) {
			return WorkspaceArchiveInspection{}, err
		}
		record, plan, err = s.loadOperationPlan(ctx, plan.OperationID)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		operation, err = operationFromJournal(record, plan)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		lastSafe = lastSafeRestorePhase(operation)
		if lastSafe == PhaseRestoreComplete {
			return s.inspectRestoreJournal(ctx, record, plan)
		}
		return WorkspaceArchiveInspection{}, fmt.Errorf("authenticated restore intent marker disappeared before durable completion")
	}
	if guard != nil {
		defer guard.Close()
		record, plan, err = s.loadOperationPlan(ctx, plan.OperationID)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		operation, err = operationFromJournal(record, plan)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		lastSafe = lastSafeRestorePhase(operation)
		if lastSafe == PhaseRestoreComplete {
			return s.inspectRestoreJournal(ctx, record, plan)
		}
		custody, _, finding, err = s.inspectRestoreCustody(ctx, plan, archivePlan, archiveOperation, archivedManifest, operation)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if finding != nil {
			manual := finding.Code == FindingAmbiguousCustody || finding.Code == FindingInvalidBinding
			recorded, recordErr := s.recordFailure(ctx, operation, *finding, manual)
			if recordErr != nil {
				return WorkspaceArchiveInspection{}, recordErr
			}
			inspection, inspectErr := s.inspectRestoreJournal(ctx, recorded, plan)
			if inspectErr != nil {
				return WorkspaceArchiveInspection{}, inspectErr
			}
			return inspection, findingsError([]WorkspaceArchiveFinding{*finding})
		}
	}
	if lastSafe == PhaseRestoreIntentCommitted && custody == CustodyArchived {
		if err := s.fail(BoundaryBeforeRestorePayloadMove); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if err := s.moveRestorePayload(ctx, plan, guard); err != nil {
			finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseRestoreIntentCommitted, err.Error(), true)
			_, _ = s.recordFailure(ctx, operation, finding, false)
			return WorkspaceArchiveInspection{}, err
		}
		if err := s.fail(BoundaryAfterRestorePayloadMove); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		custody = CustodyActive
	}
	if custody != CustodyActive {
		finding := newWorkspaceFinding(plan.OperationID, FindingAmbiguousCustody, lastSafe, "restore recovery could not establish active custody", false)
		_, _ = s.recordFailure(ctx, operation, finding, true)
		return WorkspaceArchiveInspection{}, findingsError([]WorkspaceArchiveFinding{finding})
	}
	if lastSafe == PhaseRestoreIntentCommitted {
		if err := s.fail(BoundaryBeforeRestoreMovedRecord); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		movedAt := s.nowAtLeast(derefTime(operation.IntentCommittedAt, operation.PlannedAt))
		record, err = journal.MarkWorkspaceRestorePayloadMoved(ctx, plan.OperationID, plan.PlanDigest, movedAt)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		operation, err = operationFromJournal(record, plan)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		lastSafe = PhaseRestorePayloadMoved
		if err := s.fail(BoundaryAfterRestoreMovedRecord); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
	}
	var restoredManifest WorkspaceArchiveManifest
	if lastSafe == PhaseRestorePayloadMoved {
		if guard == nil {
			return WorkspaceArchiveInspection{}, fmt.Errorf("restored manifest publication requires its authenticated restore claim")
		}
		if err := s.fail(BoundaryBeforeRestoredManifest); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		restoredManifest, err = s.buildRestoredManifest(ctx, archivedManifest, archiveOperation, operation, plan)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if err := s.publishRestoredManifest(ctx, plan, archivePlan, archivedManifest, restoredManifest, guard); err != nil {
			finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseRestorePayloadMoved, err.Error(), false)
			_, _ = s.recordFailure(ctx, operation, finding, true)
			return WorkspaceArchiveInspection{}, err
		}
		if err := s.fail(BoundaryAfterRestoredManifest); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if err := s.fail(BoundaryBeforeRestoreProjections); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		projectedAt := s.nowAtLeast(derefTime(operation.PayloadMovedAt, operation.UpdatedAt))
		event := eventForRestore(operation, projectedAt)
		projectedOperation := operation
		projectedOperation.Phase = PhaseRestoreProjectionsCommitted
		projectedOperation.LastSafePhase = ""
		projectedOperation.Status = OperationStatusRunning
		projectedOperation.ProjectionsCommittedAt = &projectedAt
		projectedOperation.UpdatedAt = projectedAt
		if err := ValidateWorkspaceArchiveOperation(projectedOperation); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if err := ValidateWorkspaceArchiveManifestForOperations(restoredManifest, archiveOperation, &projectedOperation); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if err := ValidateWorkspaceLifecycleEventForOperation(event, projectedOperation); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		projection, err := journalRestoreProjectionInput(plan, archivedManifest, restoredManifest, event, projectedAt)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		record, err = journal.CommitWorkspaceRestoreProjection(ctx, projection)
		if err != nil {
			finding := newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseRestorePayloadMoved, "catalog or manifest projection no longer matches the reviewed restore plan: "+err.Error(), true)
			_, _ = s.recordFailure(ctx, operation, finding, false)
			return WorkspaceArchiveInspection{}, err
		}
		operation, err = operationFromJournal(record, plan)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		lastSafe = PhaseRestoreProjectionsCommitted
		if err := s.fail(BoundaryAfterRestoreProjections); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
	}
	if lastSafe == PhaseRestoreProjectionsCommitted {
		if _, err := s.inspectRestoreJournal(ctx, record, plan); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if guard != nil {
			if err := s.fail(BoundaryBeforeRestoreIntentCleanup); err != nil {
				return WorkspaceArchiveInspection{}, err
			}
			if err := s.removeRestoreIntentMarker(guard); err != nil {
				return WorkspaceArchiveInspection{}, err
			}
		}
		if err := s.fail(BoundaryBeforeRestoreComplete); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		completedAt := s.nowAtLeast(derefTime(operation.ProjectionsCommittedAt, operation.UpdatedAt))
		record, err = journal.CompleteWorkspaceRestore(ctx, plan.OperationID, plan.PlanDigest, completedAt)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if err := s.fail(BoundaryAfterRestoreComplete); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
	}
	return s.inspectRestoreJournal(ctx, record, plan)
}

func (s WorkspaceMoveService) inspectRestoreJournal(ctx context.Context, record storagecatalog.WorkspaceArchiveJournalRecord, plan WorkspaceArchivePlan) (WorkspaceArchiveInspection, error) {
	operation, err := operationFromJournal(record, plan)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	archivePlan, archiveOperation, archivedManifest, err := s.loadCompletedArchiveEvidence(ctx, plan.ArchiveOperationID)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	custody, manifest, finding, err := s.inspectRestoreCustody(ctx, plan, archivePlan, archiveOperation, archivedManifest, operation)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if finding != nil && !hasEquivalentFinding(operation.Findings, *finding) {
		operation.Findings = append(operation.Findings, *finding)
	}
	if operation.Phase == PhaseRestoreComplete && (custody != CustodyActive || manifest == nil || manifest.LifecycleState != WorkspaceLifecycleActive) {
		return WorkspaceArchiveInspection{}, fmt.Errorf("complete restore operation lacks authenticated active custody")
	}
	if rank, ok := effectivePhaseRank(operation); ok && rank >= 3 {
		if manifest == nil {
			return WorkspaceArchiveInspection{}, fmt.Errorf("projection-committed restore lacks a filesystem manifest")
		}
		if err := validateDurableWorkspaceRestoreProjection(record, archiveOperation, operation, *manifest); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
	}
	return WorkspaceArchiveInspection{
		Operation:       operation,
		Plan:            plan,
		Custody:         custody,
		Manifest:        manifest,
		ActivationState: WorkspaceActivationInactive,
	}, nil
}

func (s WorkspaceMoveService) validateExecutableRestorePlan(plan WorkspaceArchivePlan) error {
	if plan.OperationKind != WorkspaceOperationRestore {
		return fmt.Errorf("restore executor accepts restore plans only")
	}
	if err := ValidateWorkspaceArchivePlan(plan, s.Roots); err != nil {
		return err
	}
	if len(plan.SourceAncestors) < 4 || len(plan.DestinationAncestors) < 2 || plan.SourceAncestors[0].RelativePath != "." || plan.DestinationAncestors[0].RelativePath != "." {
		return fmt.Errorf("restore plan lacks exact root/ancestor evidence")
	}
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return err
	}
	if plan.SourceAncestors[len(plan.SourceAncestors)-1].RelativePath != paths.Mapping.ArchiveContainerPath || plan.DestinationAncestors[len(plan.DestinationAncestors)-1].RelativePath != path.Dir(paths.Mapping.ActiveRelativePath) {
		return fmt.Errorf("restore plan ancestor evidence does not end at the fixed mapped parents")
	}
	if !SamePathIdentity(plan.Source.ParentIdentity, plan.SourceAncestors[len(plan.SourceAncestors)-1].Identity) || !SamePathIdentity(plan.Destination.ParentIdentity, plan.DestinationAncestors[len(plan.DestinationAncestors)-1].Identity) {
		return fmt.Errorf("restore plan parent evidence contradicts its ancestor chain")
	}
	if plan.SourceAncestors[0].Identity.DeviceID != plan.Source.Identity.DeviceID || plan.DestinationAncestors[0].Identity.DeviceID != plan.Source.Identity.DeviceID {
		return fmt.Errorf("restore plan trusted roots and payload are not on one filesystem")
	}
	for index, rebind := range plan.CatalogRebinds {
		if rebind.StoragePhysicalRefID != "" {
			want, changed, err := rebaseCatalogPath(rebind.ExpectedURI, paths.ArchivePayload, paths.Active)
			if err != nil || !changed || want != rebind.NewURI {
				return fmt.Errorf("restore catalog physical-ref rebind %d does not follow the fixed path mapping", index)
			}
			continue
		}
		if rebind.ExpectedOriginalPath != rebind.NewOriginalPath {
			want, changed, err := rebaseCatalogPath(rebind.ExpectedOriginalPath, paths.ArchivePayload, paths.Active)
			if err != nil || !changed || want != rebind.NewOriginalPath {
				return fmt.Errorf("restore catalog original-path rebind %d does not follow the fixed path mapping", index)
			}
		}
		if rebind.ExpectedCurrentViewPath != rebind.NewCurrentViewPath {
			want, changed, err := rebaseCatalogPath(rebind.ExpectedCurrentViewPath, paths.ArchivePayload, paths.Active)
			if err != nil || !changed || want != rebind.NewCurrentViewPath {
				return fmt.Errorf("restore catalog view-path rebind %d does not follow the fixed path mapping", index)
			}
		}
	}
	return nil
}

func (s WorkspaceMoveService) compareLiveRestorePlanEvidence(ctx context.Context, plan WorkspaceArchivePlan) ([]WorkspaceArchiveFinding, error) {
	archivePlan, archiveOperation, archivedManifest, err := s.loadCompletedArchiveEvidence(ctx, plan.ArchiveOperationID)
	if err != nil {
		return nil, err
	}
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return nil, err
	}
	source, destination, sourceAncestors, destinationAncestors, inventory, manifestDigest, err := s.observeRestorePlanEvidence(ctx, paths, archivePlan, archivedManifest, false)
	if err != nil {
		return nil, err
	}
	findings := EvaluatePlanEvidence(plan, source, destination)
	if archiveOperation.OperationID != plan.ArchiveOperationID || archiveOperation.Phase != PhaseArchiveComplete || manifestDigest != plan.ArchiveManifestDigest {
		findings = append(findings, newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseRestorePlanned, "authenticated archive operation or manifest changed after restore review", false))
	}
	if !reflect.DeepEqual(sourceAncestors, plan.SourceAncestors) || !reflect.DeepEqual(inventory, plan.Inventory) {
		findings = append(findings, newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseRestorePlanned, "archived payload evidence changed after restore review", true))
	}
	if !reflect.DeepEqual(destinationAncestors, plan.DestinationAncestors) {
		findings = append(findings, newWorkspaceFinding(plan.OperationID, FindingDestinationSubstitution, PhaseRestorePlanned, "active destination ancestor evidence changed after restore review", true))
	}
	currentCatalog, err := s.planCatalogRebinds(ctx, paths.ArchivePayload, paths.Active)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(currentCatalog, plan.CatalogRebinds) {
		findings = append(findings, newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseRestorePlanned, "storage catalog references changed after restore review", true))
	}
	return uniqueFindings(findings), nil
}

func (s WorkspaceMoveService) observeRestorePlanEvidence(ctx context.Context, paths ResolvedWorkspacePaths, archivePlan WorkspaceArchivePlan, archivedManifest WorkspaceArchiveManifest, requireClean bool) (WorkspacePathBinding, WorkspacePathBinding, []WorkspaceAncestorBinding, []WorkspaceAncestorBinding, NoFollowInventory, string, error) {
	sourceChain, err := openHeldDirectoryChain(s.Roots.StorageRoot, paths.Mapping.ArchiveContainerPath, nil)
	if err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", err
	}
	defer sourceChain.Close()
	if requireClean {
		if err := verifyContainerEntries(sourceChain.ParentFD(), map[string]bool{path.Base(paths.Mapping.ArchivePayloadPath): true, "archive.json": true}); err != nil {
			return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", err
		}
	}
	manifestPayload, present, err := readFileAtNoFollow(sourceChain.ParentFD(), "archive.json", workspaceManifestMaxBytes)
	if err != nil || !present {
		if err == nil {
			err = fmt.Errorf("authenticated archive manifest is absent")
		}
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", err
	}
	if err := s.verifyCanonicalManifestPayload(ctx, archivePlan, archivedManifest, manifestPayload); err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", err
	}
	sourceFD, err := unix.Openat(sourceChain.ParentFD(), path.Base(paths.Mapping.ArchivePayloadPath), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", fmt.Errorf("open archived restore source without following links: %w", err)
	}
	sourceIdentity, identityErr := identityForFD(sourceFD)
	defer unix.Close(sourceFD)
	if identityErr != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", identityErr
	}
	destinationChain, err := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(paths.Mapping.ActiveRelativePath), nil)
	if err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", err
	}
	defer destinationChain.Close()
	if err := sameWorkspaceMounts(sourceFD, sourceChain.ParentFD(), destinationChain.ParentFD()); err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", err
	}
	destinationIdentity := PathIdentity{Presence: PathAbsent}
	if present, err := namePresentNoFollow(destinationChain.ParentFD(), path.Base(paths.Mapping.ActiveRelativePath)); err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", err
	} else if present {
		fd, openErr := unix.Openat(destinationChain.ParentFD(), path.Base(paths.Mapping.ActiveRelativePath), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", fmt.Errorf("active restore destination is not a real directory")
		}
		destinationIdentity, err = identityForFD(fd)
		_ = unix.Close(fd)
		if err != nil {
			return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", err
		}
		if requireClean {
			return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", fmt.Errorf("active restore destination already exists")
		}
	}
	inventory, err := InventoryMappedSource(ctx, s.Roots, WorkspaceOperationRestore, archivePlan.Kind, archivePlan.Slug)
	if err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, "", err
	}
	source := WorkspacePathBinding{Path: paths.ArchivePayload, Identity: sourceIdentity, ParentIdentity: sourceChain.evidence[len(sourceChain.evidence)-1].Identity}
	destination := WorkspacePathBinding{Path: paths.Active, Identity: destinationIdentity, ParentIdentity: destinationChain.evidence[len(destinationChain.evidence)-1].Identity}
	return source, destination, append([]WorkspaceAncestorBinding(nil), sourceChain.evidence...), append([]WorkspaceAncestorBinding(nil), destinationChain.evidence...), inventory, sha256Digest(manifestPayload), nil
}

func (s WorkspaceMoveService) loadCompletedArchiveEvidence(ctx context.Context, operationID string) (WorkspaceArchivePlan, WorkspaceArchiveOperation, WorkspaceArchiveManifest, error) {
	if !workspacePathID(operationID, workspaceOperationID) || s.Journal == nil {
		return WorkspaceArchivePlan{}, WorkspaceArchiveOperation{}, WorkspaceArchiveManifest{}, fmt.Errorf("a valid completed archive operation is required")
	}
	record, exists, err := s.Journal.LoadWorkspaceArchiveJournal(ctx, operationID)
	if err != nil || !exists {
		if err == nil {
			err = fmt.Errorf("archive operation %q was not found", operationID)
		}
		return WorkspaceArchivePlan{}, WorkspaceArchiveOperation{}, WorkspaceArchiveManifest{}, err
	}
	plan, err := decodeJournalPlan(record)
	if err != nil {
		return WorkspaceArchivePlan{}, WorkspaceArchiveOperation{}, WorkspaceArchiveManifest{}, err
	}
	if err := s.validateExecutablePlan(plan); err != nil {
		return WorkspaceArchivePlan{}, WorkspaceArchiveOperation{}, WorkspaceArchiveManifest{}, err
	}
	operation, err := operationFromJournal(record, plan)
	if err != nil {
		return WorkspaceArchivePlan{}, WorkspaceArchiveOperation{}, WorkspaceArchiveManifest{}, err
	}
	if operation.Phase != PhaseArchiveComplete || operation.Status != OperationStatusComplete {
		return WorkspaceArchivePlan{}, WorkspaceArchiveOperation{}, WorkspaceArchiveManifest{}, fmt.Errorf("archive operation is not complete")
	}
	manifest, err := s.buildArchiveManifest(ctx, operation, plan)
	if err != nil {
		return WorkspaceArchivePlan{}, WorkspaceArchiveOperation{}, WorkspaceArchiveManifest{}, err
	}
	if err := s.validateCurrentArchiveManifestEvidence(ctx, record, plan, operation, manifest); err != nil {
		return WorkspaceArchivePlan{}, WorkspaceArchiveOperation{}, WorkspaceArchiveManifest{}, err
	}
	return plan, operation, manifest, nil
}

func (s WorkspaceMoveService) validateCurrentArchiveManifestEvidence(ctx context.Context, record storagecatalog.WorkspaceArchiveJournalRecord, archivePlan WorkspaceArchivePlan, archiveOperation WorkspaceArchiveOperation, archivedManifest WorkspaceArchiveManifest) error {
	if len(record.ManifestJSON) == 0 {
		return fmt.Errorf("completed archive operation lacks durable manifest evidence")
	}
	var current WorkspaceArchiveManifest
	if err := json.Unmarshal(record.ManifestJSON, &current); err != nil {
		return fmt.Errorf("decode current durable workspace manifest: %w", err)
	}
	if current.LifecycleState == WorkspaceLifecycleArchived {
		if !sameWorkspaceManifest(current, archivedManifest) {
			return fmt.Errorf("durable archive manifest does not match its completed operation")
		}
		return nil
	}
	if current.LifecycleState != WorkspaceLifecycleActive || current.RestoreOperationID == "" {
		return fmt.Errorf("durable archive manifest has invalid current lifecycle evidence")
	}
	if err := s.verifyManifest(ctx, current, archivePlan); err != nil {
		return err
	}
	restoreRecord, restorePlan, err := s.loadOperationPlan(ctx, current.RestoreOperationID)
	if err != nil {
		return err
	}
	if restorePlan.OperationKind != WorkspaceOperationRestore || restorePlan.ArchiveOperationID != archiveOperation.OperationID {
		return fmt.Errorf("durable active manifest contradicts its archive operation")
	}
	restoreOperation, err := operationFromJournal(restoreRecord, restorePlan)
	if err != nil {
		return err
	}
	if rank, ok := effectivePhaseRank(restoreOperation); !ok || rank < 3 {
		return fmt.Errorf("durable active manifest precedes restore projection commitment")
	}
	canonical, err := s.buildRestoredManifest(ctx, archivedManifest, archiveOperation, restoreOperation, restorePlan)
	if err != nil {
		return err
	}
	if !sameWorkspaceManifest(current, canonical) {
		return fmt.Errorf("durable active manifest does not match its restore operation")
	}
	return nil
}

func (s WorkspaceMoveService) inspectRestoreCustody(ctx context.Context, plan, archivePlan WorkspaceArchivePlan, archiveOperation WorkspaceArchiveOperation, archivedManifest WorkspaceArchiveManifest, restoreOperation WorkspaceArchiveOperation) (WorkspaceCustodyState, *WorkspaceArchiveManifest, *WorkspaceArchiveFinding, error) {
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	sourceChain, err := openHeldDirectoryChain(s.Roots.StorageRoot, paths.Mapping.ArchiveContainerPath, plan.SourceAncestors)
	if err != nil {
		finding := newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseRestoreIntentCommitted, err.Error(), true)
		return CustodyAmbiguous, nil, &finding, nil
	}
	defer sourceChain.Close()
	destinationChain, err := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(paths.Mapping.ActiveRelativePath), plan.DestinationAncestors)
	if err != nil {
		finding := newWorkspaceFinding(plan.OperationID, FindingDestinationSubstitution, PhaseRestoreIntentCommitted, err.Error(), true)
		return CustodyAmbiguous, nil, &finding, nil
	}
	defer destinationChain.Close()
	allowed := map[string]bool{
		path.Base(paths.Mapping.ArchivePayloadPath): true,
		"archive.json":                               true,
		workspaceRestoreIntentMarkerName:             true,
		".restore.json." + plan.OperationID + ".tmp": true,
	}
	if err := verifyContainerEntries(sourceChain.ParentFD(), allowed); err != nil {
		finding := newWorkspaceFinding(plan.OperationID, FindingAmbiguousCustody, PhaseRestoreIntentCommitted, err.Error(), false)
		return CustodyAmbiguous, nil, &finding, nil
	}
	manifestPayload, present, err := readFileAtNoFollow(sourceChain.ParentFD(), "archive.json", workspaceManifestMaxBytes)
	if err != nil || !present {
		finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseRestorePayloadMoved, "authenticated archive manifest is absent or unreadable", false)
		return CustodyAmbiguous, nil, &finding, nil
	}
	manifest, err := DecodePhysicalWorkspaceArchiveManifest(manifestPayload)
	if err != nil {
		finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseRestorePayloadMoved, err.Error(), false)
		return CustodyAmbiguous, nil, &finding, nil
	}
	if err := s.verifyManifest(ctx, manifest, archivePlan); err != nil {
		finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseRestorePayloadMoved, err.Error(), false)
		return CustodyAmbiguous, nil, &finding, nil
	}
	if manifest.LifecycleState == WorkspaceLifecycleArchived {
		canonicalPayload, marshalErr := json.Marshal(archivedManifest)
		if marshalErr != nil {
			return CustodyAmbiguous, &manifest, nil, marshalErr
		}
		if !bytes.Equal(manifestPayload, canonicalPayload) || sha256Digest(manifestPayload) != plan.ArchiveManifestDigest {
			finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseRestoreIntentCommitted, "archived manifest does not match the restore plan", false)
			return CustodyAmbiguous, &manifest, &finding, nil
		}
	} else {
		canonical, buildErr := s.buildRestoredManifest(ctx, archivedManifest, archiveOperation, restoreOperation, plan)
		canonicalPayload, marshalErr := json.Marshal(canonical)
		if buildErr != nil || marshalErr != nil || !bytes.Equal(manifestPayload, canonicalPayload) {
			finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseRestorePayloadMoved, "active manifest does not match the durable restore operation", false)
			return CustodyAmbiguous, &manifest, &finding, nil
		}
	}
	sourcePresent, sourceMatches, err := inspectNamedDirectory(sourceChain.ParentFD(), path.Base(paths.Mapping.ArchivePayloadPath), plan.Source.Identity)
	if err != nil {
		return CustodyAmbiguous, &manifest, nil, err
	}
	destinationPresent, destinationMatches, err := inspectNamedDirectory(destinationChain.ParentFD(), path.Base(paths.Mapping.ActiveRelativePath), plan.Source.Identity)
	if err != nil {
		return CustodyAmbiguous, &manifest, nil, err
	}
	if sourcePresent && destinationPresent {
		finding := newWorkspaceFinding(plan.OperationID, FindingAmbiguousCustody, PhaseRestoreIntentCommitted, "both archived and active payload paths exist", false)
		return CustodyAmbiguous, &manifest, &finding, nil
	}
	if !sourcePresent && !destinationPresent {
		finding := newWorkspaceFinding(plan.OperationID, FindingAmbiguousCustody, PhaseRestoreIntentCommitted, "neither archived nor active path owns the payload", false)
		return CustodyAmbiguous, &manifest, &finding, nil
	}
	if sourcePresent && !sourceMatches {
		finding := newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseRestoreIntentCommitted, "archived payload identity changed", false)
		return CustodyAmbiguous, &manifest, &finding, nil
	}
	if destinationPresent && !destinationMatches {
		finding := newWorkspaceFinding(plan.OperationID, FindingDestinationSubstitution, PhaseRestoreIntentCommitted, "active destination identity does not match restored payload", false)
		return CustodyAmbiguous, &manifest, &finding, nil
	}
	operationKind := WorkspaceOperationRestore
	if destinationPresent {
		operationKind = WorkspaceOperationArchive
	}
	inventory, err := InventoryMappedSource(ctx, s.Roots, operationKind, plan.Kind, plan.Slug)
	if err != nil || !reflect.DeepEqual(inventory, plan.Inventory) {
		finding := newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseRestorePayloadMoved, "workspace payload inventory changed after restore review", false)
		return CustodyAmbiguous, &manifest, &finding, nil
	}
	if sourcePresent {
		return CustodyArchived, &manifest, nil, nil
	}
	return CustodyActive, &manifest, nil, nil
}

func (s WorkspaceMoveService) acquireRestoreGuard(ctx context.Context, plan, archivePlan WorkspaceArchivePlan, archivedManifest WorkspaceArchiveManifest) (*workspaceRestoreGuard, error) {
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return nil, err
	}
	sourceChain, err := openHeldDirectoryChain(s.Roots.StorageRoot, paths.Mapping.ArchiveContainerPath, plan.SourceAncestors)
	if err != nil {
		return nil, err
	}
	destinationChain, err := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(paths.Mapping.ActiveRelativePath), plan.DestinationAncestors)
	if err != nil {
		sourceChain.Close()
		return nil, err
	}
	guard := &workspaceRestoreGuard{sourceChain: sourceChain, destinationChain: destinationChain}
	marker, err := s.sealRestoreIntentMarker(ctx, plan)
	if err != nil {
		guard.Close()
		return nil, err
	}
	payload, err := json.Marshal(marker)
	if err != nil {
		guard.Close()
		return nil, err
	}
	writeErr := writeExclusiveFileAt(guard.containerFD(), workspaceRestoreIntentMarkerName, payload, 0o600)
	created := writeErr == nil
	if writeErr != nil && !errors.Is(writeErr, unix.EEXIST) {
		guard.Close()
		return nil, writeErr
	}
	held, present, err := openBoundedFileAtNoFollow(guard.containerFD(), workspaceRestoreIntentMarkerName, workspaceRestoreIntentMaxBytes)
	if err != nil || !present {
		guard.Close()
		if err == nil {
			err = fmt.Errorf("restore intent marker disappeared")
		}
		return nil, err
	}
	guard.marker = held
	if err := s.lockRestoreClaim(ctx, held.file); err != nil {
		guard.Close()
		return nil, err
	}
	if present, err := namePresentNoFollow(guard.containerFD(), workspaceRestoreIntentMarkerName); err != nil || !present {
		guard.Close()
		return nil, errWorkspaceRestoreClaimGone
	}
	if err := s.verifyRestoreIntentMarkerPayload(ctx, held.payload, plan); err != nil {
		guard.Close()
		return nil, err
	}
	if created {
		manifestPayload, manifestPresent, err := readFileAtNoFollow(guard.containerFD(), "archive.json", workspaceManifestMaxBytes)
		if err != nil || !manifestPresent || s.verifyCanonicalManifestPayload(ctx, archivePlan, archivedManifest, manifestPayload) != nil || sha256Digest(manifestPayload) != plan.ArchiveManifestDigest {
			_ = unix.Unlinkat(guard.containerFD(), workspaceRestoreIntentMarkerName, 0)
			_ = unix.Fsync(guard.containerFD())
			guard.Close()
			return nil, errWorkspaceRestoreClaimGone
		}
	}
	if err := guard.verify(); err != nil {
		guard.Close()
		return nil, err
	}
	return guard, nil
}

func (s WorkspaceMoveService) acquireExistingRestoreGuard(ctx context.Context, plan WorkspaceArchivePlan) (*workspaceRestoreGuard, error) {
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return nil, err
	}
	sourceChain, err := openHeldDirectoryChain(s.Roots.StorageRoot, paths.Mapping.ArchiveContainerPath, plan.SourceAncestors)
	if err != nil {
		return nil, err
	}
	destinationChain, err := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(paths.Mapping.ActiveRelativePath), plan.DestinationAncestors)
	if err != nil {
		sourceChain.Close()
		return nil, err
	}
	guard := &workspaceRestoreGuard{sourceChain: sourceChain, destinationChain: destinationChain}
	held, present, err := openBoundedFileAtNoFollow(guard.containerFD(), workspaceRestoreIntentMarkerName, workspaceRestoreIntentMaxBytes)
	if err != nil || !present {
		guard.Close()
		if err == nil {
			err = os.ErrNotExist
		}
		return nil, err
	}
	guard.marker = held
	if err := s.lockRestoreClaim(ctx, held.file); err != nil {
		guard.Close()
		return nil, err
	}
	if present, err := namePresentNoFollow(guard.containerFD(), workspaceRestoreIntentMarkerName); err != nil || !present {
		guard.Close()
		return nil, errWorkspaceRestoreClaimGone
	}
	if err := s.verifyRestoreIntentMarkerPayload(ctx, held.payload, plan); err != nil {
		guard.Close()
		return nil, err
	}
	if err := guard.verify(); err != nil {
		guard.Close()
		return nil, err
	}
	return guard, nil
}

func (s WorkspaceMoveService) lockRestoreClaim(ctx context.Context, file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		if hookErr := s.fail(BoundaryRestoreClaimContended); hookErr != nil {
			return hookErr
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s WorkspaceMoveService) moveRestorePayload(ctx context.Context, plan WorkspaceArchivePlan, guard *workspaceRestoreGuard) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := guard.verify(); err != nil {
		return err
	}
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return err
	}
	payloadName := path.Base(paths.Mapping.ArchivePayloadPath)
	activeName := path.Base(paths.Mapping.ActiveRelativePath)
	if err := verifyNamedDirectoryIdentity(guard.containerFD(), payloadName, plan.Source.Identity); err != nil {
		return fmt.Errorf("archived source changed before restore rename: %w", err)
	}
	present, err := namePresentNoFollow(guard.destinationChain.ParentFD(), activeName)
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf("active restore destination exists; refusing overwrite")
	}
	inventory, err := InventoryMappedSource(ctx, s.Roots, WorkspaceOperationRestore, plan.Kind, plan.Slug)
	if err != nil || !reflect.DeepEqual(inventory, plan.Inventory) {
		if err != nil {
			return fmt.Errorf("recheck archived restore inventory: %w", err)
		}
		return fmt.Errorf("archived restore inventory changed after durable intent")
	}
	catalog, err := s.planCatalogRebinds(ctx, paths.ArchivePayload, paths.Active)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(catalog, plan.CatalogRebinds) {
		return fmt.Errorf("storage catalog references changed after restore intent")
	}
	if err := s.fail(BoundaryAfterRestoreCatalogRecheck); err != nil {
		return err
	}
	if err := guard.verify(); err != nil {
		return err
	}
	if err := verifyNamedDirectoryIdentity(guard.containerFD(), payloadName, plan.Source.Identity); err != nil {
		return fmt.Errorf("archived source changed immediately before restore rename: %w", err)
	}
	if present, err := namePresentNoFollow(guard.destinationChain.ParentFD(), activeName); err != nil || present {
		if err != nil {
			return err
		}
		return fmt.Errorf("active restore destination appeared before rename; refusing overwrite")
	}
	if err := verifyWorkspaceRenameMounts(guard.containerFD(), payloadName, guard.destinationChain.ParentFD()); err != nil {
		return err
	}
	if err := renameNoReplaceAt(guard.containerFD(), payloadName, guard.destinationChain.ParentFD(), activeName); err != nil {
		return fmt.Errorf("atomic same-filesystem restore rename: %w", err)
	}
	if err := unix.Fsync(guard.containerFD()); err != nil {
		return fmt.Errorf("sync archive container after restore rename: %w", err)
	}
	if err := unix.Fsync(guard.destinationChain.ParentFD()); err != nil {
		return fmt.Errorf("sync active parent after restore rename: %w", err)
	}
	if err := verifyNamedDirectoryIdentity(guard.destinationChain.ParentFD(), activeName, plan.Source.Identity); err != nil {
		return fmt.Errorf("restored payload identity: %w", err)
	}
	return nil
}

func (s WorkspaceMoveService) buildRestoredManifest(ctx context.Context, archived WorkspaceArchiveManifest, archiveOperation, restoreOperation WorkspaceArchiveOperation, plan WorkspaceArchivePlan) (WorkspaceArchiveManifest, error) {
	if restoreOperation.PayloadMovedAt == nil {
		return WorkspaceArchiveManifest{}, fmt.Errorf("restored manifest requires durable payload-moved time")
	}
	manifest := archived
	manifest.LifecycleState = WorkspaceLifecycleActive
	manifest.RestoreOperationID = restoreOperation.OperationID
	manifest.RestorePlanDigest = restoreOperation.PlanDigest
	manifest.LifecycleEventID = deterministicArchiveEventID(restoreOperation.OperationID)
	restoredAt := *restoreOperation.PayloadMovedAt
	manifest.RestoredAt = &restoredAt
	manifest.Authentication.Tag = ""
	if err := s.signManifest(ctx, &manifest); err != nil {
		return WorkspaceArchiveManifest{}, err
	}
	if err := ValidateWorkspaceArchiveManifestForOperations(manifest, archiveOperation, &restoreOperation); err != nil {
		return WorkspaceArchiveManifest{}, err
	}
	if manifest.RestorePlanDigest != plan.PlanDigest {
		return WorkspaceArchiveManifest{}, fmt.Errorf("restored manifest does not bind the reviewed restore plan")
	}
	return manifest, nil
}

func (s WorkspaceMoveService) publishRestoredManifest(ctx context.Context, plan, archivePlan WorkspaceArchivePlan, archived, restored WorkspaceArchiveManifest, guard *workspaceRestoreGuard) error {
	if err := guard.verify(); err != nil {
		return err
	}
	restoredPayload, err := json.Marshal(restored)
	if err != nil {
		return err
	}
	if len(restoredPayload) > workspaceManifestMaxBytes {
		return fmt.Errorf("restored workspace manifest exceeds %d bytes", workspaceManifestMaxBytes)
	}
	current, present, err := openBoundedFileAtNoFollow(guard.containerFD(), "archive.json", workspaceManifestMaxBytes)
	if err != nil || !present {
		return fmt.Errorf("workspace archive manifest disappeared during restore")
	}
	defer current.Close()
	if bytes.Equal(current.payload, restoredPayload) {
		return nil
	}
	if err := s.verifyCanonicalManifestPayload(ctx, archivePlan, archived, current.payload); err != nil || sha256Digest(current.payload) != plan.ArchiveManifestDigest {
		return fmt.Errorf("workspace archive manifest changed before restored state publication")
	}
	tempName := ".restore.json." + plan.OperationID + ".tmp"
	if err := writeExclusiveFileAt(guard.containerFD(), tempName, restoredPayload, 0o600); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	} else if err == nil {
		if err := s.fail(BoundaryAfterRestoredManifestTemp); err != nil {
			return err
		}
	}
	temp, present, err := openBoundedFileAtNoFollow(guard.containerFD(), tempName, workspaceManifestMaxBytes)
	if err != nil || !present {
		return fmt.Errorf("restored manifest temp disappeared")
	}
	defer temp.Close()
	if !bytes.Equal(temp.payload, restoredPayload) {
		return fmt.Errorf("restored manifest temp replay conflict")
	}
	if err := s.fail(BoundaryBeforeRestoredManifestCommit); err != nil {
		return err
	}
	if err := guard.verify(); err != nil {
		return err
	}
	if err := verifyNamedHeldFile(guard.containerFD(), "archive.json", current); err != nil {
		return fmt.Errorf("workspace archive manifest was substituted before restored state publication: %w", err)
	}
	if err := verifyNamedHeldFile(guard.containerFD(), tempName, temp); err != nil {
		return fmt.Errorf("restored manifest temp was substituted: %w", err)
	}
	if err := unix.Renameat(guard.containerFD(), tempName, guard.containerFD(), "archive.json"); err != nil {
		return fmt.Errorf("publish restored workspace manifest: %w", err)
	}
	if err := unix.Fsync(guard.containerFD()); err != nil {
		return err
	}
	published, present, err := readFileAtNoFollow(guard.containerFD(), "archive.json", workspaceManifestMaxBytes)
	if err != nil || !present || !bytes.Equal(published, restoredPayload) {
		return fmt.Errorf("published restored manifest does not match the durable restore operation")
	}
	return nil
}

func (s WorkspaceMoveService) sealRestoreIntentMarker(ctx context.Context, plan WorkspaceArchivePlan) (workspaceRestoreIntentMarker, error) {
	marker := workspaceRestoreIntentMarker{
		SchemaVersion:         "storage.workspace_restore_intent_marker.v1",
		OperationID:           plan.OperationID,
		PlanDigest:            plan.PlanDigest,
		ArchiveOperationID:    plan.ArchiveOperationID,
		ArchiveManifestDigest: plan.ArchiveManifestDigest,
		Kind:                  plan.Kind,
		ObjectID:              plan.ObjectID,
		Slug:                  plan.Slug,
		Source:                plan.Source.Path,
		Destination:           plan.Destination.Path,
		Authentication:        ManifestAuthentication{Algorithm: "hmac-sha256", KeyID: s.ManifestKeyID},
	}
	unsigned, err := unsignedRestoreIntentJSON(marker)
	if err != nil {
		return workspaceRestoreIntentMarker{}, err
	}
	marker.Authentication.Tag, err = s.hmacTag(ctx, marker.Authentication.KeyID, unsigned)
	return marker, err
}

func (s WorkspaceMoveService) verifyRestoreIntentMarkerPayload(ctx context.Context, payload []byte, plan WorkspaceArchivePlan) error {
	var marker workspaceRestoreIntentMarker
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return fmt.Errorf("decode restore intent marker: %w", err)
	}
	if marker.SchemaVersion != "storage.workspace_restore_intent_marker.v1" || marker.OperationID != plan.OperationID || marker.PlanDigest != plan.PlanDigest || marker.ArchiveOperationID != plan.ArchiveOperationID || marker.ArchiveManifestDigest != plan.ArchiveManifestDigest || marker.Kind != plan.Kind || marker.ObjectID != plan.ObjectID || marker.Slug != plan.Slug || !samePathRef(marker.Source, plan.Source.Path) || !samePathRef(marker.Destination, plan.Destination.Path) || marker.Authentication.Algorithm != "hmac-sha256" || marker.Authentication.KeyID != s.ManifestKeyID || !hmacSHA256DigestPattern.MatchString(marker.Authentication.Tag) {
		return fmt.Errorf("restore intent marker does not match reviewed plan")
	}
	unsigned, err := unsignedRestoreIntentJSON(marker)
	if err != nil {
		return err
	}
	expected, err := s.hmacTag(ctx, marker.Authentication.KeyID, unsigned)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(expected), []byte(marker.Authentication.Tag)) {
		return fmt.Errorf("restore intent marker HMAC authentication failed")
	}
	return nil
}

func unsignedRestoreIntentJSON(marker workspaceRestoreIntentMarker) ([]byte, error) {
	marker.Authentication.Tag = ""
	return json.Marshal(marker)
}

func (s WorkspaceMoveService) removeRestoreIntentMarker(guard *workspaceRestoreGuard) error {
	if err := guard.verify(); err != nil {
		return err
	}
	if err := unix.Unlinkat(guard.containerFD(), workspaceRestoreIntentMarkerName, 0); err != nil {
		return err
	}
	return unix.Fsync(guard.containerFD())
}

func journalRestoreProjectionInput(plan WorkspaceArchivePlan, archived, restored WorkspaceArchiveManifest, event WorkspaceLifecycleEvent, at time.Time) (storagecatalog.WorkspaceRestoreProjectionInput, error) {
	expectedManifestJSON, err := json.Marshal(archived)
	if err != nil {
		return storagecatalog.WorkspaceRestoreProjectionInput{}, err
	}
	manifestJSON, err := json.Marshal(restored)
	if err != nil {
		return storagecatalog.WorkspaceRestoreProjectionInput{}, err
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return storagecatalog.WorkspaceRestoreProjectionInput{}, err
	}
	rebind := storagecatalog.RebindPathsInput{}
	for _, item := range plan.CatalogRebinds {
		if item.StoragePhysicalRefID != "" {
			rebind.PhysicalRefs = append(rebind.PhysicalRefs, storagecatalog.PhysicalRefPathRebind{StoragePhysicalRefID: item.StoragePhysicalRefID, StorageEntryID: item.StorageEntryID, ExpectedURI: item.ExpectedURI, NewURI: item.NewURI})
		}
		if item.ExpectedOriginalPath != item.NewOriginalPath || item.ExpectedCurrentViewPath != item.NewCurrentViewPath {
			rebind.Entries = append(rebind.Entries, storagecatalog.EntryPathRebind{
				StorageEntryID:             item.StorageEntryID,
				ExpectedOriginalSourcePath: item.ExpectedOriginalPath,
				NewOriginalSourcePath:      item.NewOriginalPath,
				ExpectedCurrentViewPath:    item.ExpectedCurrentViewPath,
				NewCurrentViewPath:         item.NewCurrentViewPath,
			})
		}
	}
	return storagecatalog.WorkspaceRestoreProjectionInput{
		ArchiveOperationID:        plan.ArchiveOperationID,
		OperationID:               plan.OperationID,
		PlanDigest:                plan.PlanDigest,
		SourceAbsolutePath:        plan.Source.Path.AbsolutePath,
		CatalogSourceRelativePath: plan.Source.Path.RelativePath,
		Rebind:                    rebind,
		ExpectedManifestJSON:      expectedManifestJSON,
		ManifestJSON:              manifestJSON,
		RestorePlanDigest:         plan.PlanDigest,
		RestoreOperationID:        plan.OperationID,
		AuthenticationKeyID:       restored.Authentication.KeyID,
		AuthenticationTag:         restored.Authentication.Tag,
		RestoredAt:                *restored.RestoredAt,
		EventID:                   event.EventID,
		EventSchemaVersion:        event.SchemaVersion,
		EventKind:                 event.EventKind,
		WorkspaceKind:             string(event.Kind),
		ObjectID:                  event.ObjectID,
		Slug:                      event.Slug,
		Transition:                string(event.Transition),
		FromState:                 string(event.FromState),
		ToState:                   string(event.ToState),
		SourceRoot:                string(event.SourcePath.Root),
		SourceRelativePath:        event.SourcePath.RelativePath,
		DestinationRoot:           string(event.DestinationPath.Root),
		DestinationRelativePath:   event.DestinationPath.RelativePath,
		ActorID:                   event.ActorID,
		Reason:                    event.Reason,
		EventJSON:                 eventJSON,
		CommittedAt:               at,
	}, nil
}

func eventForRestore(operation WorkspaceArchiveOperation, at time.Time) WorkspaceLifecycleEvent {
	return WorkspaceLifecycleEvent{
		SchemaVersion:   WorkspaceLifecycleEventSchemaVersion,
		EventID:         deterministicArchiveEventID(operation.OperationID),
		EventKind:       WorkspaceLifecycleEventKind,
		OperationID:     operation.OperationID,
		Kind:            operation.Kind,
		ObjectID:        operation.ObjectID,
		Slug:            operation.Slug,
		Transition:      WorkspaceTransitionRestored,
		FromState:       WorkspaceLifecycleArchived,
		ToState:         WorkspaceLifecycleActive,
		SourcePath:      operation.Source.Path,
		DestinationPath: operation.Destination.Path,
		ActorID:         operation.ActorID,
		Reason:          operation.Reason,
		OccurredAt:      normalizeWorkspaceTimestamp(at),
	}
}

func validateDurableWorkspaceRestoreProjection(record storagecatalog.WorkspaceArchiveJournalRecord, archiveOperation, restoreOperation WorkspaceArchiveOperation, filesystemManifest WorkspaceArchiveManifest) error {
	if len(record.ManifestJSON) == 0 || len(record.EventJSON) == 0 {
		return fmt.Errorf("projection-committed restore lacks durable manifest or lifecycle event evidence")
	}
	var durableManifest WorkspaceArchiveManifest
	if err := json.Unmarshal(record.ManifestJSON, &durableManifest); err != nil {
		return fmt.Errorf("decode durable restored workspace manifest: %w", err)
	}
	if !sameWorkspaceManifest(durableManifest, filesystemManifest) {
		return fmt.Errorf("filesystem and durable restored workspace manifests differ")
	}
	if err := ValidateWorkspaceArchiveManifestForOperations(durableManifest, archiveOperation, &restoreOperation); err != nil {
		return err
	}
	var event WorkspaceLifecycleEvent
	if err := json.Unmarshal(record.EventJSON, &event); err != nil {
		return fmt.Errorf("decode durable restore lifecycle event: %w", err)
	}
	if event.EventID != durableManifest.LifecycleEventID {
		return fmt.Errorf("restore lifecycle event does not match active manifest")
	}
	if err := ValidateWorkspaceLifecycleEventForOperation(event, restoreOperation); err != nil {
		return err
	}
	if !reflect.DeepEqual(event, eventForRestore(restoreOperation, *restoreOperation.ProjectionsCommittedAt)) {
		return fmt.Errorf("durable restore lifecycle event is not canonical")
	}
	return nil
}

func lastSafeRestorePhase(operation WorkspaceArchiveOperation) WorkspaceArchivePhase {
	if operation.Phase == PhaseBlocked || operation.Phase == PhaseManualRepairRequired {
		return operation.LastSafePhase
	}
	return operation.Phase
}

func normalizedPlannedAt(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return normalizeWorkspaceTimestamp(fallback)
	}
	return normalizeWorkspaceTimestamp(value)
}
