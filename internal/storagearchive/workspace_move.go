package storagearchive

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"golang.org/x/sys/unix"

	"loom.local/loom/internal/storagecatalog"
)

const (
	workspaceIntentMarkerName = ".archive-intent.json"
	workspaceManifestMaxBytes = 1 << 20
	workspaceIntentMaxBytes   = 32 << 10
)

type WorkspaceMoveCatalog interface {
	ListWorkspaceArchiveCatalogEntries(context.Context, string, string) ([]storagecatalog.EntryDetail, error)
}

type WorkspaceMoveJournal interface {
	LoadWorkspaceArchiveJournal(context.Context, string) (storagecatalog.WorkspaceArchiveJournalRecord, bool, error)
	CommitWorkspaceArchiveIntent(context.Context, storagecatalog.WorkspaceArchiveIntentInput) (storagecatalog.WorkspaceArchiveJournalRecord, bool, error)
	MarkWorkspaceArchivePayloadMoved(context.Context, string, string, time.Time) (storagecatalog.WorkspaceArchiveJournalRecord, error)
	CommitWorkspaceArchiveProjection(context.Context, storagecatalog.WorkspaceArchiveProjectionInput) (storagecatalog.WorkspaceArchiveJournalRecord, error)
	CompleteWorkspaceArchive(context.Context, string, string, time.Time) (storagecatalog.WorkspaceArchiveJournalRecord, error)
	RecordWorkspaceArchiveFailure(context.Context, storagecatalog.WorkspaceArchiveFailureInput) (storagecatalog.WorkspaceArchiveJournalRecord, error)
}

type WorkspaceRestoreJournal interface {
	WorkspaceMoveJournal
	MarkWorkspaceRestorePayloadMoved(context.Context, string, string, time.Time) (storagecatalog.WorkspaceArchiveJournalRecord, error)
	CommitWorkspaceRestoreProjection(context.Context, storagecatalog.WorkspaceRestoreProjectionInput) (storagecatalog.WorkspaceArchiveJournalRecord, error)
	CompleteWorkspaceRestore(context.Context, string, string, time.Time) (storagecatalog.WorkspaceArchiveJournalRecord, error)
}

type WorkspaceManifestKeyLookup func(context.Context, string) ([]byte, error)

type WorkspaceMoveBoundary string

const (
	BoundaryBeforeIntent              WorkspaceMoveBoundary = "before_intent"
	BoundaryAfterIntent               WorkspaceMoveBoundary = "after_intent"
	BoundaryBeforePayloadMove         WorkspaceMoveBoundary = "before_payload_move"
	BoundaryAfterCatalogRecheck       WorkspaceMoveBoundary = "after_catalog_recheck"
	BoundaryAfterPayloadMove          WorkspaceMoveBoundary = "after_payload_move"
	BoundaryBeforeMovedRecord         WorkspaceMoveBoundary = "before_payload_moved_record"
	BoundaryAfterMovedRecord          WorkspaceMoveBoundary = "after_payload_moved_record"
	BoundaryPublicationClaimContended WorkspaceMoveBoundary = "publication_claim_contended"
	BoundaryBeforeManifest            WorkspaceMoveBoundary = "before_manifest"
	BoundaryAfterManifestTemp         WorkspaceMoveBoundary = "after_manifest_temp"
	BoundaryBeforeManifestPublish     WorkspaceMoveBoundary = "before_manifest_publish"
	BoundaryAfterManifest             WorkspaceMoveBoundary = "after_manifest"
	BoundaryBeforeProjections         WorkspaceMoveBoundary = "before_projections"
	BoundaryAfterProjections          WorkspaceMoveBoundary = "after_projections"
	BoundaryBeforeIntentCleanup       WorkspaceMoveBoundary = "before_intent_cleanup"
	BoundaryBeforeComplete            WorkspaceMoveBoundary = "before_complete"
	BoundaryAfterComplete             WorkspaceMoveBoundary = "after_complete"
)

type WorkspaceMoveFailureHook func(WorkspaceMoveBoundary) error

type WorkspaceMoveService struct {
	Roots         TrustedWorkspaceRoots
	Catalog       WorkspaceMoveCatalog
	Journal       WorkspaceMoveJournal
	ManifestKeyID string
	ManifestKey   WorkspaceManifestKeyLookup
	Now           func() time.Time
	FailureHook   WorkspaceMoveFailureHook
}

type WorkspaceArchivePlanInput struct {
	OperationID string
	Kind        WorkspaceKind
	ObjectID    string
	Slug        string
	ActorID     string
	Reason      string
	PlannedAt   time.Time
}

type WorkspaceCustodyState string

const (
	CustodyActive    WorkspaceCustodyState = "active"
	CustodyArchived  WorkspaceCustodyState = "archived"
	CustodyAmbiguous WorkspaceCustodyState = "ambiguous"
)

type WorkspaceArchiveInspection struct {
	Operation       WorkspaceArchiveOperation `json:"operation"`
	Plan            WorkspaceArchivePlan      `json:"plan"`
	Custody         WorkspaceCustodyState     `json:"custody"`
	Manifest        *WorkspaceArchiveManifest `json:"manifest,omitempty"`
	ActivationState WorkspaceActivationState  `json:"activation_state,omitempty"`
}

type workspaceIntentMarker struct {
	SchemaVersion  string                 `json:"schema_version"`
	OperationID    string                 `json:"operation_id"`
	PlanDigest     string                 `json:"plan_digest"`
	Kind           WorkspaceKind          `json:"kind"`
	ObjectID       string                 `json:"object_id"`
	Slug           string                 `json:"slug"`
	Destination    WorkspacePathRef       `json:"destination"`
	Authentication ManifestAuthentication `json:"authentication"`
}

var errWorkspacePublicationClaimGone = errors.New("workspace archive publication claim is no longer named")

type workspacePublicationBoundaryError struct{ err error }

func (e workspacePublicationBoundaryError) Error() string { return e.err.Error() }
func (e workspacePublicationBoundaryError) Unwrap() error { return e.err }

type heldBoundedFile struct {
	file    *os.File
	info    os.FileInfo
	payload []byte
}

func (f *heldBoundedFile) Close() {
	if f != nil && f.file != nil {
		_ = f.file.Close()
		f.file = nil
	}
}

type workspacePublicationGuard struct {
	chain             *heldDirectoryChain
	containerFD       int
	containerName     string
	containerIdentity PathIdentity
	marker            *heldBoundedFile
}

func (g *workspacePublicationGuard) Close() {
	if g == nil {
		return
	}
	if g.marker != nil && g.marker.file != nil {
		_ = unix.Flock(int(g.marker.file.Fd()), unix.LOCK_UN)
		g.marker.Close()
	}
	if g.containerFD >= 0 {
		_ = unix.Close(g.containerFD)
		g.containerFD = -1
	}
	if g.chain != nil {
		g.chain.Close()
		g.chain = nil
	}
}

type heldDirectoryChain struct {
	fds      []int
	names    []string
	evidence []WorkspaceAncestorBinding
}

func (c *heldDirectoryChain) Close() {
	if c == nil {
		return
	}
	for index := len(c.fds) - 1; index >= 0; index-- {
		_ = unix.Close(c.fds[index])
	}
	c.fds = nil
}

func (c *heldDirectoryChain) ParentFD() int { return c.fds[len(c.fds)-1] }

func (c *heldDirectoryChain) verifyNamed() error {
	if len(c.fds) == 0 {
		return fmt.Errorf("directory chain is closed")
	}
	rootPath := c.names[0]
	info, err := os.Lstat(rootPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("trusted root was replaced: %w", err)
	}
	held, err := identityForFD(c.fds[0])
	if err != nil {
		return err
	}
	named, err := pathIdentityFromInfo(info)
	if err != nil || !sameStableDirectoryIdentity(named, held) {
		return fmt.Errorf("trusted root was substituted")
	}
	for index := 1; index < len(c.fds); index++ {
		child, err := unix.Openat(c.fds[index-1], c.names[index], unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("mapped ancestor %q was replaced: %w", c.evidence[index].RelativePath, err)
		}
		observed, observeErr := identityForFD(child)
		_ = unix.Close(child)
		if observeErr != nil || !sameStableDirectoryIdentity(observed, c.evidence[index].Identity) {
			return fmt.Errorf("mapped ancestor %q was substituted", c.evidence[index].RelativePath)
		}
	}
	return nil
}

func (g *workspacePublicationGuard) verify() error {
	if g == nil || g.chain == nil || g.containerFD < 0 || g.marker == nil || g.marker.file == nil {
		return fmt.Errorf("workspace archive publication guard is closed")
	}
	if err := g.chain.verifyNamed(); err != nil {
		return err
	}
	if err := verifyNamedDirectoryIdentity(g.chain.ParentFD(), g.containerName, g.containerIdentity); err != nil {
		return fmt.Errorf("archive container was substituted while publication was locked: %w", err)
	}
	markerPayload, err := stableHeldFilePayload(g.marker.file, workspaceIntentMaxBytes)
	if err != nil {
		return fmt.Errorf("archive intent marker changed while publication was locked: %w", err)
	}
	if !bytes.Equal(markerPayload, g.marker.payload) {
		return fmt.Errorf("archive intent marker bytes changed while publication was locked")
	}
	if err := verifyNamedHeldFile(g.containerFD, workspaceIntentMarkerName, g.marker); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errWorkspacePublicationClaimGone
		}
		return fmt.Errorf("archive intent marker was substituted while publication was locked: %w", err)
	}
	return nil
}

func (s WorkspaceMoveService) PlanArchive(ctx context.Context, input WorkspaceArchivePlanInput) (WorkspaceArchivePlan, error) {
	if s.Catalog == nil {
		return WorkspaceArchivePlan{}, fmt.Errorf("workspace archive catalog is not configured")
	}
	if input.OperationID == "" {
		input.OperationID = newWorkspaceID("workspace_archive_operation")
	}
	paths, err := ResolveWorkspacePaths(s.Roots, input.Kind, input.Slug)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	if !workspacePathID(input.OperationID, workspaceOperationID) {
		return WorkspaceArchivePlan{}, fmt.Errorf("invalid workspace archive operation_id")
	}
	source, destination, sourceAncestors, destinationAncestors, inventory, err := observeArchivePlanEvidence(ctx, s.Roots, paths, true)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	catalogRebinds, err := s.planCatalogRebinds(ctx, paths.Active, paths.ArchivePayload)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	plan := WorkspaceArchivePlan{
		SchemaVersion:        WorkspaceArchivePlanSchemaVersion,
		EvidenceKind:         PhysicalWorkspaceMoveEvidence,
		OperationID:          input.OperationID,
		OperationKind:        WorkspaceOperationArchive,
		Kind:                 input.Kind,
		ObjectID:             strings.TrimSpace(input.ObjectID),
		Slug:                 input.Slug,
		Source:               source,
		Destination:          destination,
		SourceAncestors:      sourceAncestors,
		DestinationAncestors: destinationAncestors,
		Inventory:            inventory,
		CatalogRebinds:       catalogRebinds,
		ActorID:              strings.TrimSpace(input.ActorID),
		Reason:               strings.TrimSpace(input.Reason),
		PlannedAt:            normalizedPlannedAt(input.PlannedAt, s.now()),
	}
	if err := SealWorkspaceArchivePlan(&plan); err != nil {
		return WorkspaceArchivePlan{}, err
	}
	if err := s.validateExecutablePlan(plan); err != nil {
		return WorkspaceArchivePlan{}, err
	}
	return plan, nil
}

func (s WorkspaceMoveService) InspectPlan(ctx context.Context, plan WorkspaceArchivePlan) (WorkspaceArchiveStatus, error) {
	if err := s.validateExecutablePlan(plan); err != nil {
		return WorkspaceArchiveStatus{}, err
	}
	findings, err := s.compareLivePlanEvidence(ctx, plan, true)
	if err != nil {
		if ctx.Err() != nil {
			return WorkspaceArchiveStatus{}, ctx.Err()
		}
		findings = []WorkspaceArchiveFinding{newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseArchivePlanned, err.Error(), true)}
	}
	status := WorkspaceArchiveStatus{
		SchemaVersion: WorkspaceArchiveStatusSchemaVersion,
		OperationID:   plan.OperationID,
		Phase:         PhaseArchivePlanned,
		Status:        OperationStatusPending,
		UpdatedAt:     s.nowAtLeast(plan.PlannedAt),
	}
	if len(findings) > 0 {
		status.Phase = PhaseBlocked
		status.LastSafePhase = PhaseArchivePlanned
		status.Status = OperationStatusBlocked
		status.Findings = findings
	}
	return status, nil
}

func (s WorkspaceMoveService) ApplyArchive(ctx context.Context, plan WorkspaceArchivePlan, reviewedDigest string) (WorkspaceArchiveInspection, error) {
	if reviewedDigest == "" || reviewedDigest != plan.PlanDigest {
		return WorkspaceArchiveInspection{}, fmt.Errorf("apply requires the exact reviewed plan digest")
	}
	if err := s.validateExecutablePlan(plan); err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if s.Journal == nil {
		return WorkspaceArchiveInspection{}, fmt.Errorf("workspace archive journal is not configured")
	}
	record, exists, err := s.Journal.LoadWorkspaceArchiveJournal(ctx, plan.OperationID)
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
				return WorkspaceArchiveInspection{}, fmt.Errorf("%s: %s", finding.Code, finding.Summary)
			}
			return WorkspaceArchiveInspection{}, fmt.Errorf("operation id conflicts with an existing archive operation")
		}
		return s.recoverArchive(ctx, record, storedPlan)
	}
	findings, err := s.compareLivePlanEvidence(ctx, plan, true)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if len(findings) > 0 {
		return WorkspaceArchiveInspection{}, findingsError(findings)
	}
	if err := s.preflightAuthentication(ctx); err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if err := s.fail(BoundaryBeforeIntent); err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	intentAt := s.nowAtLeast(plan.PlannedAt)
	input, err := journalIntentInput(plan, intentAt)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	record, _, err = s.Journal.CommitWorkspaceArchiveIntent(ctx, input)
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
		return WorkspaceArchiveInspection{}, fmt.Errorf("operation id conflicts with an existing archive operation")
	}
	if err := s.fail(BoundaryAfterIntent); err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	return s.recoverArchive(ctx, record, storedPlan)
}

func (s WorkspaceMoveService) InspectArchive(ctx context.Context, operationID string) (WorkspaceArchiveInspection, error) {
	if s.Journal == nil {
		return WorkspaceArchiveInspection{}, fmt.Errorf("workspace archive journal is not configured")
	}
	record, exists, err := s.Journal.LoadWorkspaceArchiveJournal(ctx, operationID)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if !exists {
		return WorkspaceArchiveInspection{}, fmt.Errorf("workspace archive operation %q was not found", operationID)
	}
	plan, err := decodeJournalPlan(record)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	return s.inspectJournal(ctx, record, plan)
}

func (s WorkspaceMoveService) RecoverArchive(ctx context.Context, operationID string) (WorkspaceArchiveInspection, error) {
	if s.Journal == nil {
		return WorkspaceArchiveInspection{}, fmt.Errorf("workspace archive journal is not configured")
	}
	record, exists, err := s.Journal.LoadWorkspaceArchiveJournal(ctx, operationID)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if !exists {
		return WorkspaceArchiveInspection{}, fmt.Errorf("workspace archive operation %q was not found", operationID)
	}
	plan, err := decodeJournalPlan(record)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	return s.recoverArchive(ctx, record, plan)
}

func (s WorkspaceMoveService) recoverArchive(ctx context.Context, record storagecatalog.WorkspaceArchiveJournalRecord, plan WorkspaceArchivePlan) (WorkspaceArchiveInspection, error) {
	if err := s.validateExecutablePlan(plan); err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	operation, err := operationFromJournal(record, plan)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if operation.Phase == PhaseManualRepairRequired {
		return s.inspectJournal(ctx, record, plan)
	}
	lastSafe := operation.Phase
	if operation.Phase == PhaseBlocked {
		lastSafe = operation.LastSafePhase
	}
	if lastSafe == PhaseArchiveComplete {
		return s.inspectJournal(ctx, record, plan)
	}
	custody, manifest, finding, err := s.inspectCustody(ctx, plan)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if finding != nil {
		manual := finding.Code == FindingAmbiguousCustody || finding.Code == FindingInvalidBinding
		record, recordErr := s.recordFailure(ctx, operation, *finding, manual)
		if recordErr != nil {
			return WorkspaceArchiveInspection{}, recordErr
		}
		inspection, inspectErr := s.inspectJournal(ctx, record, plan)
		if inspectErr != nil {
			return WorkspaceArchiveInspection{}, inspectErr
		}
		return inspection, findingsError([]WorkspaceArchiveFinding{*finding})
	}
	if lastSafe == PhaseArchiveIntentCommitted && custody == CustodyActive {
		if err := s.fail(BoundaryBeforePayloadMove); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if err := s.movePayload(ctx, plan); err != nil {
			finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseArchiveIntentCommitted, err.Error(), true)
			_, _ = s.recordFailure(ctx, operation, finding, false)
			return WorkspaceArchiveInspection{}, err
		}
		if err := s.fail(BoundaryAfterPayloadMove); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		custody = CustodyArchived
	}
	if custody != CustodyArchived {
		finding := newWorkspaceFinding(plan.OperationID, FindingAmbiguousCustody, lastSafe, "archive recovery could not establish destination custody", false)
		_, _ = s.recordFailure(ctx, operation, finding, true)
		return WorkspaceArchiveInspection{}, findingsError([]WorkspaceArchiveFinding{finding})
	}
	if lastSafe == PhaseArchiveIntentCommitted {
		if err := s.fail(BoundaryBeforeMovedRecord); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		movedAt := s.nowAtLeast(derefTime(operation.IntentCommittedAt, operation.PlannedAt))
		record, err = s.Journal.MarkWorkspaceArchivePayloadMoved(ctx, plan.OperationID, plan.PlanDigest, movedAt)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		operation, err = operationFromJournal(record, plan)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		lastSafe = PhaseArchivePayloadMoved
		if err := s.fail(BoundaryAfterMovedRecord); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
	}
	var publicationGuard *workspacePublicationGuard
	if lastSafe == PhaseArchivePayloadMoved || lastSafe == PhaseArchiveProjectionsCommitted {
		publicationGuard, err = s.acquirePublicationGuard(ctx, plan)
		if err != nil {
			if !errors.Is(err, errWorkspacePublicationClaimGone) {
				return WorkspaceArchiveInspection{}, err
			}
			var exists bool
			record, exists, err = s.Journal.LoadWorkspaceArchiveJournal(ctx, plan.OperationID)
			if err != nil {
				return WorkspaceArchiveInspection{}, err
			}
			if !exists {
				return WorkspaceArchiveInspection{}, fmt.Errorf("workspace archive operation disappeared while publication claim changed")
			}
			operation, err = operationFromJournal(record, plan)
			if err != nil {
				return WorkspaceArchiveInspection{}, err
			}
			lastSafe = lastSafeArchivePhase(operation)
			if lastSafe == PhaseArchivePayloadMoved {
				finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseArchivePayloadMoved, "authenticated archive intent marker disappeared before projection commit", false)
				_, _ = s.recordFailure(ctx, operation, finding, true)
				return WorkspaceArchiveInspection{}, findingsError([]WorkspaceArchiveFinding{finding})
			}
		} else {
			defer publicationGuard.Close()
			var exists bool
			record, exists, err = s.Journal.LoadWorkspaceArchiveJournal(ctx, plan.OperationID)
			if err != nil {
				return WorkspaceArchiveInspection{}, err
			}
			if !exists {
				return WorkspaceArchiveInspection{}, fmt.Errorf("workspace archive operation disappeared while publication was locked")
			}
			operation, err = operationFromJournal(record, plan)
			if err != nil {
				return WorkspaceArchiveInspection{}, err
			}
			lastSafe = lastSafeArchivePhase(operation)
			if lastSafe == PhaseArchiveComplete {
				return s.inspectJournal(ctx, record, plan)
			}
			custody, manifest, finding, err = s.inspectCustody(ctx, plan)
			if err != nil {
				return WorkspaceArchiveInspection{}, err
			}
			if finding != nil {
				record, recordErr := s.recordFailure(ctx, operation, *finding, true)
				if recordErr != nil {
					return WorkspaceArchiveInspection{}, recordErr
				}
				inspection, inspectErr := s.inspectJournal(ctx, record, plan)
				if inspectErr != nil {
					return WorkspaceArchiveInspection{}, inspectErr
				}
				return inspection, findingsError([]WorkspaceArchiveFinding{*finding})
			}
			if custody != CustodyArchived {
				return WorkspaceArchiveInspection{}, fmt.Errorf("publication-locked recovery lost authenticated archive custody")
			}
		}
	}
	if lastSafe == PhaseArchivePayloadMoved {
		if publicationGuard == nil {
			return WorkspaceArchiveInspection{}, fmt.Errorf("archive manifest publication requires its authenticated operation claim")
		}
		if err := s.fail(BoundaryBeforeManifest); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		created, err := s.buildArchiveManifest(ctx, operation, plan)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if err := s.publishManifest(ctx, plan, created, publicationGuard); err != nil {
			var boundaryErr workspacePublicationBoundaryError
			if errors.As(err, &boundaryErr) {
				return WorkspaceArchiveInspection{}, err
			}
			finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseArchivePayloadMoved, err.Error(), false)
			_, _ = s.recordFailure(ctx, operation, finding, true)
			return WorkspaceArchiveInspection{}, err
		}
		manifest = &created
		if err := s.fail(BoundaryAfterManifest); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if err := s.fail(BoundaryBeforeProjections); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		projectedAt := s.nowAtLeast(derefTime(operation.PayloadMovedAt, operation.UpdatedAt))
		event := eventForArchive(operation, *manifest, projectedAt)
		projectedOperation := operation
		projectedOperation.Phase = PhaseArchiveProjectionsCommitted
		projectedOperation.LastSafePhase = ""
		projectedOperation.Status = OperationStatusRunning
		projectedOperation.ProjectionsCommittedAt = &projectedAt
		projectedOperation.UpdatedAt = projectedAt
		if err := ValidateWorkspaceArchiveOperation(projectedOperation); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if err := ValidateWorkspaceArchiveManifestForOperations(*manifest, projectedOperation, nil); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if err := ValidateWorkspaceLifecycleEventForOperation(event, projectedOperation); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		projection, err := journalProjectionInput(plan, *manifest, event, projectedAt)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		record, err = s.Journal.CommitWorkspaceArchiveProjection(ctx, projection)
		if err != nil {
			finding := newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseArchivePayloadMoved, "catalog projection no longer matches the reviewed plan: "+err.Error(), true)
			_, _ = s.recordFailure(ctx, operation, finding, false)
			return WorkspaceArchiveInspection{}, err
		}
		operation, err = operationFromJournal(record, plan)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		lastSafe = PhaseArchiveProjectionsCommitted
		if err := s.fail(BoundaryAfterProjections); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
	}
	if lastSafe == PhaseArchiveProjectionsCommitted {
		if _, err := s.inspectJournal(ctx, record, plan); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if publicationGuard != nil {
			if err := s.fail(BoundaryBeforeIntentCleanup); err != nil {
				return WorkspaceArchiveInspection{}, err
			}
			if err := s.removeIntentMarker(ctx, plan, publicationGuard); err != nil {
				return WorkspaceArchiveInspection{}, err
			}
		}
		if err := s.fail(BoundaryBeforeComplete); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		completedAt := s.nowAtLeast(derefTime(operation.ProjectionsCommittedAt, operation.UpdatedAt))
		record, err = s.Journal.CompleteWorkspaceArchive(ctx, plan.OperationID, plan.PlanDigest, completedAt)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if err := s.fail(BoundaryAfterComplete); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
	}
	return s.inspectJournal(ctx, record, plan)
}

func (s WorkspaceMoveService) inspectJournal(ctx context.Context, record storagecatalog.WorkspaceArchiveJournalRecord, plan WorkspaceArchivePlan) (WorkspaceArchiveInspection, error) {
	operation, err := operationFromJournal(record, plan)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	custody, manifest, finding, err := s.inspectCustody(ctx, plan)
	if err != nil {
		return WorkspaceArchiveInspection{}, err
	}
	if finding != nil && !hasEquivalentFinding(operation.Findings, *finding) {
		operation.Findings = append(operation.Findings, *finding)
	}
	if operation.Phase == PhaseArchiveComplete && (custody != CustodyArchived || manifest == nil) {
		return WorkspaceArchiveInspection{}, fmt.Errorf("complete archive operation lacks authenticated destination custody")
	}
	if rank, ok := effectivePhaseRank(operation); ok && rank >= 2 && manifest != nil {
		canonical, err := s.buildArchiveManifest(ctx, operation, plan)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		if !sameWorkspaceManifest(*manifest, canonical) {
			return WorkspaceArchiveInspection{}, fmt.Errorf("filesystem archive manifest does not match the canonical durable operation manifest")
		}
		if err := s.verifyCanonicalManifestFile(ctx, plan, canonical); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
	}
	if rank, ok := effectivePhaseRank(operation); ok && rank >= 3 {
		if manifest == nil {
			return WorkspaceArchiveInspection{}, fmt.Errorf("projection-committed archive operation lacks a filesystem manifest")
		}
		if err := validateDurableWorkspaceProjection(record, operation, *manifest); err != nil {
			return WorkspaceArchiveInspection{}, err
		}
	}
	return WorkspaceArchiveInspection{Operation: operation, Plan: plan, Custody: custody, Manifest: manifest}, nil
}

func (s WorkspaceMoveService) verifyCanonicalManifestFile(ctx context.Context, plan WorkspaceArchivePlan, canonical WorkspaceArchiveManifest) error {
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return err
	}
	chain, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(paths.Mapping.ArchiveContainerPath), plan.DestinationAncestors)
	if err != nil {
		return err
	}
	defer chain.Close()
	containerFD, err := unix.Openat(chain.ParentFD(), plan.Slug, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(containerFD)
	payload, present, err := readFileAtNoFollow(containerFD, "archive.json", workspaceManifestMaxBytes)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("canonical archive manifest is absent")
	}
	return s.verifyCanonicalManifestPayload(ctx, plan, canonical, payload)
}

func (s WorkspaceMoveService) validateExecutablePlan(plan WorkspaceArchivePlan) error {
	if plan.OperationKind != WorkspaceOperationArchive {
		return fmt.Errorf("Slice 2 accepts archive plans only")
	}
	if err := ValidateWorkspaceArchivePlan(plan, s.Roots); err != nil {
		return err
	}
	if len(plan.SourceAncestors) < 2 || len(plan.DestinationAncestors) < 2 {
		return fmt.Errorf("archive plan lacks exact root/ancestor evidence")
	}
	if plan.SourceAncestors[0].RelativePath != "." || plan.DestinationAncestors[0].RelativePath != "." {
		return fmt.Errorf("archive plan ancestor evidence does not start at trusted roots")
	}
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return err
	}
	wantSourceParent := path.Dir(paths.Mapping.ActiveRelativePath)
	wantDestinationParent := path.Dir(paths.Mapping.ArchiveContainerPath)
	if plan.SourceAncestors[len(plan.SourceAncestors)-1].RelativePath != wantSourceParent || plan.DestinationAncestors[len(plan.DestinationAncestors)-1].RelativePath != wantDestinationParent {
		return fmt.Errorf("archive plan ancestor evidence does not end at the fixed mapped parents")
	}
	if !SamePathIdentity(plan.Source.ParentIdentity, plan.SourceAncestors[len(plan.SourceAncestors)-1].Identity) || !SamePathIdentity(plan.Destination.ParentIdentity, plan.DestinationAncestors[len(plan.DestinationAncestors)-1].Identity) {
		return fmt.Errorf("archive plan parent evidence contradicts its ancestor chain")
	}
	if plan.SourceAncestors[0].Identity.DeviceID != plan.Source.Identity.DeviceID || plan.DestinationAncestors[0].Identity.DeviceID != plan.Source.Identity.DeviceID {
		return fmt.Errorf("archive plan trusted roots and payload are not on one filesystem")
	}
	for index, rebind := range plan.CatalogRebinds {
		if rebind.StoragePhysicalRefID != "" {
			want, changed, err := rebaseCatalogPath(rebind.ExpectedURI, paths.Active, paths.ArchivePayload)
			if err != nil || !changed || want != rebind.NewURI {
				return fmt.Errorf("catalog physical-ref rebind %d does not follow the fixed path mapping", index)
			}
			continue
		}
		if rebind.ExpectedOriginalPath != rebind.NewOriginalPath {
			want, changed, err := rebaseCatalogPath(rebind.ExpectedOriginalPath, paths.Active, paths.ArchivePayload)
			if err != nil || !changed || want != rebind.NewOriginalPath {
				return fmt.Errorf("catalog entry original-path rebind %d does not follow the fixed path mapping", index)
			}
		}
		if rebind.ExpectedCurrentViewPath != rebind.NewCurrentViewPath {
			want, changed, err := rebaseCatalogPath(rebind.ExpectedCurrentViewPath, paths.Active, paths.ArchivePayload)
			if err != nil || !changed || want != rebind.NewCurrentViewPath {
				return fmt.Errorf("catalog entry view-path rebind %d does not follow the fixed path mapping", index)
			}
		}
	}
	return nil
}

func (s WorkspaceMoveService) compareLivePlanEvidence(ctx context.Context, plan WorkspaceArchivePlan, requireNoContainer bool) ([]WorkspaceArchiveFinding, error) {
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return nil, err
	}
	source, destination, sourceAncestors, destinationAncestors, inventory, err := observeArchivePlanEvidence(ctx, s.Roots, paths, false)
	if err != nil {
		return nil, err
	}
	findings := EvaluatePlanEvidence(plan, source, destination)
	if !reflect.DeepEqual(sourceAncestors, plan.SourceAncestors) {
		findings = append(findings, newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseArchivePlanned, "source root or ancestor evidence changed after plan review", true))
	}
	if !reflect.DeepEqual(destinationAncestors, plan.DestinationAncestors) {
		findings = append(findings, newWorkspaceFinding(plan.OperationID, FindingDestinationSubstitution, PhaseArchivePlanned, "destination root or ancestor evidence changed after plan review", true))
	}
	if inventory.Digest != plan.Inventory.Digest || !reflect.DeepEqual(inventory, plan.Inventory) {
		findings = append(findings, newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseArchivePlanned, "source no-follow inventory changed after plan review", true))
	}
	currentCatalog, err := s.planCatalogRebinds(ctx, paths.Active, paths.ArchivePayload)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(currentCatalog, plan.CatalogRebinds) {
		findings = append(findings, newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseArchivePlanned, "storage catalog references changed after plan review", true))
	}
	if requireNoContainer {
		category := path.Dir(paths.Mapping.ArchiveContainerPath)
		chain, err := openHeldDirectoryChain(s.Roots.StorageRoot, category, plan.DestinationAncestors)
		if err != nil {
			return nil, err
		}
		defer chain.Close()
		present, err := namePresentNoFollow(chain.ParentFD(), path.Base(paths.Mapping.ArchiveContainerPath))
		if err != nil {
			return nil, err
		}
		if present {
			findings = append(findings, newWorkspaceFinding(plan.OperationID, FindingDestinationCollision, PhaseArchivePlanned, "archive container exists at the reviewed-absent destination", true))
		}
	}
	return uniqueFindings(findings), nil
}

func (s WorkspaceMoveService) planCatalogRebinds(ctx context.Context, source, destination WorkspacePathRef) ([]WorkspaceCatalogRebind, error) {
	details, err := s.Catalog.ListWorkspaceArchiveCatalogEntries(ctx, source.AbsolutePath, source.RelativePath)
	if err != nil {
		return nil, fmt.Errorf("list workspace catalog entries: %w", err)
	}
	var result []WorkspaceCatalogRebind
	for _, detail := range details {
		original, originalChanged, err := rebaseCatalogPath(detail.Entry.OriginalSourcePath, source, destination)
		if err != nil {
			return nil, err
		}
		view, viewChanged, err := rebaseCatalogPath(detail.Entry.CurrentViewPath, source, destination)
		if err != nil {
			return nil, err
		}
		if originalChanged || viewChanged {
			result = append(result, WorkspaceCatalogRebind{
				StorageEntryID:          detail.Entry.StorageEntryID,
				ExpectedOriginalPath:    detail.Entry.OriginalSourcePath,
				NewOriginalPath:         original,
				ExpectedCurrentViewPath: detail.Entry.CurrentViewPath,
				NewCurrentViewPath:      view,
			})
		}
		for _, ref := range detail.PhysicalRefs {
			uri, changed, err := rebaseCatalogPath(ref.URI, source, destination)
			if err != nil {
				return nil, err
			}
			if changed {
				result = append(result, WorkspaceCatalogRebind{
					StorageEntryID:       detail.Entry.StorageEntryID,
					StoragePhysicalRefID: ref.StoragePhysicalRefID,
					ExpectedURI:          ref.URI,
					NewURI:               uri,
				})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StorageEntryID != result[j].StorageEntryID {
			return result[i].StorageEntryID < result[j].StorageEntryID
		}
		return result[i].StoragePhysicalRefID < result[j].StoragePhysicalRefID
	})
	if err := validateWorkspaceCatalogRebinds(result); err != nil {
		return nil, err
	}
	return result, nil
}

func observeArchivePlanEvidence(ctx context.Context, roots TrustedWorkspaceRoots, paths ResolvedWorkspacePaths, requireAbsentContainer bool) (WorkspacePathBinding, WorkspacePathBinding, []WorkspaceAncestorBinding, []WorkspaceAncestorBinding, NoFollowInventory, error) {
	sourceParent := path.Dir(paths.Mapping.ActiveRelativePath)
	sourceChain, err := openHeldDirectoryChain(roots.BoxRoot, sourceParent, nil)
	if err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, err
	}
	defer sourceChain.Close()
	sourceName := path.Base(paths.Mapping.ActiveRelativePath)
	sourceFD, err := unix.Openat(sourceChain.ParentFD(), sourceName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, fmt.Errorf("open archive source without following links: %w", err)
	}
	sourceIdentity, err := identityForFD(sourceFD)
	defer unix.Close(sourceFD)
	if err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, err
	}
	destinationParent := path.Dir(paths.Mapping.ArchiveContainerPath)
	destinationChain, err := openHeldDirectoryChain(roots.StorageRoot, destinationParent, nil)
	if err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, err
	}
	defer destinationChain.Close()
	if err := sameWorkspaceMounts(sourceFD, sourceChain.ParentFD(), destinationChain.ParentFD()); err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, err
	}
	present, err := namePresentNoFollow(destinationChain.ParentFD(), path.Base(paths.Mapping.ArchiveContainerPath))
	if err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, err
	}
	if present && requireAbsentContainer {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, fmt.Errorf("archive destination container already exists")
	}
	inventory, err := InventoryMappedSource(ctx, roots, WorkspaceOperationArchive, paths.Mapping.Kind, path.Base(paths.Mapping.ActiveRelativePath))
	if err != nil {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, err
	}
	if !SamePathIdentity(sourceIdentity, inventory.RootIdentity) {
		return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, fmt.Errorf("archive source changed between identity and inventory review")
	}
	source := WorkspacePathBinding{Path: paths.Active, Identity: sourceIdentity, ParentIdentity: sourceChain.evidence[len(sourceChain.evidence)-1].Identity}
	destinationIdentity := PathIdentity{Presence: PathAbsent}
	if present {
		containerFD, openErr := unix.Openat(destinationChain.ParentFD(), path.Base(paths.Mapping.ArchiveContainerPath), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, fmt.Errorf("inspect archive destination container: %w", openErr)
		}
		payloadFD, payloadErr := unix.Openat(containerFD, path.Base(paths.Mapping.ArchivePayloadPath), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(containerFD)
		if payloadErr == nil {
			destinationIdentity, err = identityForFD(payloadFD)
			_ = unix.Close(payloadFD)
			if err != nil {
				return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, err
			}
		} else if !errors.Is(payloadErr, unix.ENOENT) {
			return WorkspacePathBinding{}, WorkspacePathBinding{}, nil, nil, NoFollowInventory{}, fmt.Errorf("inspect archive destination payload: %w", payloadErr)
		}
	}
	destination := WorkspacePathBinding{Path: paths.ArchivePayload, Identity: destinationIdentity, ParentIdentity: destinationChain.evidence[len(destinationChain.evidence)-1].Identity}
	return source, destination, append([]WorkspaceAncestorBinding(nil), sourceChain.evidence...), append([]WorkspaceAncestorBinding(nil), destinationChain.evidence...), inventory, nil
}

func openHeldDirectoryChain(rootPath, relative string, expected []WorkspaceAncestorBinding) (*heldDirectoryChain, error) {
	fd, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open trusted root without following links: %w", err)
	}
	chain := &heldDirectoryChain{fds: []int{fd}, names: []string{rootPath}}
	identity, err := identityForFD(fd)
	if err != nil {
		chain.Close()
		return nil, err
	}
	chain.evidence = append(chain.evidence, WorkspaceAncestorBinding{RelativePath: ".", Identity: identity})
	current := fd
	currentRelative := ""
	if relative != "." && relative != "" {
		for _, component := range strings.Split(relative, "/") {
			if component == "" || component == "." || component == ".." || strings.ContainsAny(component, `/\\`) {
				chain.Close()
				return nil, fmt.Errorf("unsafe mapped ancestor component")
			}
			next, err := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				chain.Close()
				return nil, fmt.Errorf("open mapped ancestor %q: %w", component, err)
			}
			nextIdentity, err := identityForFD(next)
			if err != nil || nextIdentity.DeviceID != identity.DeviceID {
				_ = unix.Close(next)
				chain.Close()
				if err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("mapped ancestor %q crosses a filesystem boundary", component)
			}
			chain.fds = append(chain.fds, next)
			chain.names = append(chain.names, component)
			currentRelative = path.Join(currentRelative, component)
			chain.evidence = append(chain.evidence, WorkspaceAncestorBinding{RelativePath: currentRelative, Identity: nextIdentity})
			current = next
		}
	}
	if len(expected) > 0 {
		if len(expected) != len(chain.evidence) {
			chain.Close()
			return nil, fmt.Errorf("mapped ancestor chain length changed")
		}
		for index := range expected {
			if expected[index].RelativePath != chain.evidence[index].RelativePath || !sameStableDirectoryIdentity(expected[index].Identity, chain.evidence[index].Identity) {
				chain.Close()
				return nil, fmt.Errorf("mapped ancestor %q was substituted", expected[index].RelativePath)
			}
		}
	}
	if err := chain.verifyNamed(); err != nil {
		chain.Close()
		return nil, err
	}
	return chain, nil
}

func (s WorkspaceMoveService) movePayload(ctx context.Context, plan WorkspaceArchivePlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return err
	}
	sourceChain, err := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(paths.Mapping.ActiveRelativePath), plan.SourceAncestors)
	if err != nil {
		return err
	}
	defer sourceChain.Close()
	destinationChain, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(paths.Mapping.ArchiveContainerPath), plan.DestinationAncestors)
	if err != nil {
		return err
	}
	defer destinationChain.Close()
	if err := verifyWorkspaceRenameMounts(sourceChain.ParentFD(), path.Base(paths.Mapping.ActiveRelativePath), destinationChain.ParentFD()); err != nil {
		return err
	}
	if err := verifyNamedDirectoryIdentity(sourceChain.ParentFD(), path.Base(paths.Mapping.ActiveRelativePath), plan.Source.Identity); err != nil {
		return fmt.Errorf("source changed before rename: %w", err)
	}
	inventory, err := InventoryMappedSource(ctx, s.Roots, WorkspaceOperationArchive, plan.Kind, plan.Slug)
	if err != nil || !reflect.DeepEqual(inventory, plan.Inventory) {
		if err != nil {
			return fmt.Errorf("recheck source inventory before rename: %w", err)
		}
		return fmt.Errorf("source inventory changed after durable intent")
	}
	catalog, err := s.planCatalogRebinds(ctx, paths.Active, paths.ArchivePayload)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(catalog, plan.CatalogRebinds) {
		return fmt.Errorf("storage catalog references changed after durable intent")
	}
	if err := s.fail(BoundaryAfterCatalogRecheck); err != nil {
		return err
	}
	containerFD, err := s.ensureIntentContainer(ctx, destinationChain, plan)
	if err != nil {
		return err
	}
	defer unix.Close(containerFD)
	payloadName := path.Base(paths.Mapping.ArchivePayloadPath)
	present, err := namePresentNoFollow(containerFD, payloadName)
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf("archive payload destination exists; refusing overwrite")
	}
	if err := sourceChain.verifyNamed(); err != nil {
		return err
	}
	if err := destinationChain.verifyNamed(); err != nil {
		return err
	}
	if err := verifyNamedDirectoryIdentity(sourceChain.ParentFD(), path.Base(paths.Mapping.ActiveRelativePath), plan.Source.Identity); err != nil {
		return fmt.Errorf("source changed immediately before rename: %w", err)
	}
	if err := verifyWorkspaceRenameMounts(sourceChain.ParentFD(), path.Base(paths.Mapping.ActiveRelativePath), containerFD); err != nil {
		return err
	}
	if err := renameNoReplaceAt(sourceChain.ParentFD(), path.Base(paths.Mapping.ActiveRelativePath), containerFD, payloadName); err != nil {
		return fmt.Errorf("atomic same-filesystem archive rename: %w", err)
	}
	if err := unix.Fsync(sourceChain.ParentFD()); err != nil {
		return fmt.Errorf("sync source parent after archive rename: %w", err)
	}
	if err := unix.Fsync(containerFD); err != nil {
		return fmt.Errorf("sync archive container after archive rename: %w", err)
	}
	if err := verifyNamedDirectoryIdentity(containerFD, payloadName, plan.Source.Identity); err != nil {
		return fmt.Errorf("moved payload identity: %w", err)
	}
	return nil
}

func verifyWorkspaceRenameMounts(sourceParent int, sourceName string, destinationParent int) error {
	source, err := unix.Openat(sourceParent, sourceName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open workspace move source mount: %w", err)
	}
	defer unix.Close(source)
	return sameWorkspaceMounts(source, sourceParent, destinationParent)
}

func (s WorkspaceMoveService) ensureIntentContainer(ctx context.Context, category *heldDirectoryChain, plan WorkspaceArchivePlan) (int, error) {
	if err := category.verifyNamed(); err != nil {
		return -1, err
	}
	containerName := plan.Slug
	if fd, err := unix.Openat(category.ParentFD(), containerName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0); err == nil {
		if verifyErr := s.verifyIntentMarker(ctx, fd, plan); verifyErr != nil {
			_ = unix.Close(fd)
			return -1, verifyErr
		}
		if verifyErr := verifyContainerEntries(fd, map[string]bool{workspaceIntentMarkerName: true}); verifyErr != nil {
			_ = unix.Close(fd)
			return -1, verifyErr
		}
		return fd, nil
	} else if !errors.Is(err, unix.ENOENT) {
		return -1, fmt.Errorf("inspect archive container: %w", err)
	}
	stageName := "." + plan.Slug + "." + plan.OperationID + ".preparing"
	stageFD, err := unix.Openat(category.ParentFD(), stageName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if !errors.Is(err, unix.ENOENT) {
			return -1, fmt.Errorf("inspect archive intent stage: %w", err)
		}
		marker, err := s.sealIntentMarker(ctx, plan)
		if err != nil {
			return -1, err
		}
		payload, err := json.Marshal(marker)
		if err != nil {
			return -1, err
		}
		if err := unix.Mkdirat(category.ParentFD(), stageName, 0o750); err != nil {
			return -1, fmt.Errorf("create archive intent stage: %w", err)
		}
		stageFD, err = unix.Openat(category.ParentFD(), stageName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return -1, err
		}
		if err := writeExclusiveFileAt(stageFD, workspaceIntentMarkerName, payload, 0o600); err != nil {
			_ = unix.Close(stageFD)
			return -1, err
		}
	} else {
		if err := s.verifyIntentMarker(ctx, stageFD, plan); err != nil {
			_ = unix.Close(stageFD)
			return -1, err
		}
		if err := verifyContainerEntries(stageFD, map[string]bool{workspaceIntentMarkerName: true}); err != nil {
			_ = unix.Close(stageFD)
			return -1, err
		}
	}
	if err := unix.Fsync(stageFD); err != nil {
		_ = unix.Close(stageFD)
		return -1, err
	}
	_ = unix.Close(stageFD)
	if err := renameNoReplaceAt(category.ParentFD(), stageName, category.ParentFD(), containerName); err != nil {
		return -1, fmt.Errorf("publish archive intent container: %w", err)
	}
	if err := unix.Fsync(category.ParentFD()); err != nil {
		return -1, err
	}
	fd, err := unix.Openat(category.ParentFD(), containerName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := s.verifyIntentMarker(ctx, fd, plan); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func (s WorkspaceMoveService) inspectCustody(ctx context.Context, plan WorkspaceArchivePlan) (WorkspaceCustodyState, *WorkspaceArchiveManifest, *WorkspaceArchiveFinding, error) {
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	sourceChain, err := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(paths.Mapping.ActiveRelativePath), plan.SourceAncestors)
	if err != nil {
		finding := newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseArchiveIntentCommitted, err.Error(), true)
		return CustodyAmbiguous, nil, &finding, nil
	}
	defer sourceChain.Close()
	destinationChain, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(paths.Mapping.ArchiveContainerPath), plan.DestinationAncestors)
	if err != nil {
		finding := newWorkspaceFinding(plan.OperationID, FindingDestinationSubstitution, PhaseArchiveIntentCommitted, err.Error(), true)
		return CustodyAmbiguous, nil, &finding, nil
	}
	defer destinationChain.Close()
	sourcePresent, sourceMatches, err := inspectNamedDirectory(sourceChain.ParentFD(), plan.Slug, plan.Source.Identity)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	containerFD, openErr := unix.Openat(destinationChain.ParentFD(), plan.Slug, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	containerPresent := openErr == nil
	if openErr != nil && !errors.Is(openErr, unix.ENOENT) {
		finding := newWorkspaceFinding(plan.OperationID, FindingDestinationSubstitution, PhaseArchiveIntentCommitted, "archive container is not a confined real directory", true)
		return CustodyAmbiguous, nil, &finding, nil
	}
	if !containerPresent {
		if sourcePresent && sourceMatches {
			return CustodyActive, nil, nil, nil
		}
		finding := newWorkspaceFinding(plan.OperationID, FindingAmbiguousCustody, PhaseArchiveIntentCommitted, "neither reviewed source nor authenticated archive container owns the payload", false)
		return CustodyAmbiguous, nil, &finding, nil
	}
	defer unix.Close(containerFD)
	allowed := map[string]bool{
		workspaceIntentMarkerName:                    true,
		path.Base(paths.Mapping.ArchivePayloadPath):  true,
		"archive.json":                               true,
		".archive.json." + plan.OperationID + ".tmp": true,
	}
	if err := verifyContainerEntries(containerFD, allowed); err != nil {
		finding := newWorkspaceFinding(plan.OperationID, FindingAmbiguousCustody, PhaseArchiveIntentCommitted, err.Error(), false)
		return CustodyAmbiguous, nil, &finding, nil
	}
	tempName := ".archive.json." + plan.OperationID + ".tmp"
	var tempManifest *WorkspaceArchiveManifest
	if tempPayload, tempPresent, tempErr := readFileAtNoFollow(containerFD, tempName, workspaceManifestMaxBytes); tempErr != nil {
		finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseArchivePayloadMoved, tempErr.Error(), false)
		return CustodyAmbiguous, nil, &finding, nil
	} else if tempPresent {
		decoded, decodeErr := DecodePhysicalWorkspaceArchiveManifest(tempPayload)
		if decodeErr != nil {
			finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseArchivePayloadMoved, decodeErr.Error(), false)
			return CustodyAmbiguous, nil, &finding, nil
		}
		if verifyErr := s.verifyManifest(ctx, decoded, plan); verifyErr != nil {
			finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseArchivePayloadMoved, verifyErr.Error(), false)
			return CustodyAmbiguous, nil, &finding, nil
		}
		tempManifest = &decoded
	}
	manifest, manifestPresent, manifestErr := s.readAndVerifyManifestAt(ctx, containerFD, plan)
	markerErr := s.verifyIntentMarker(ctx, containerFD, plan)
	if manifestErr != nil {
		finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseArchivePayloadMoved, manifestErr.Error(), false)
		return CustodyAmbiguous, nil, &finding, nil
	}
	if !manifestPresent && markerErr != nil {
		finding := newWorkspaceFinding(plan.OperationID, FindingAmbiguousCustody, PhaseArchiveIntentCommitted, "archive container has neither valid intent nor authenticated manifest evidence", false)
		return CustodyAmbiguous, nil, &finding, nil
	}
	if tempManifest != nil && manifestPresent && !sameWorkspaceManifest(*tempManifest, *manifest) {
		finding := newWorkspaceFinding(plan.OperationID, FindingInvalidBinding, PhaseArchivePayloadMoved, "temporary and published archive manifests disagree", false)
		return CustodyAmbiguous, nil, &finding, nil
	}
	payloadName := path.Base(paths.Mapping.ArchivePayloadPath)
	payloadPresent, payloadMatches, err := inspectNamedDirectory(containerFD, payloadName, plan.Source.Identity)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	if sourcePresent && payloadPresent {
		finding := newWorkspaceFinding(plan.OperationID, FindingAmbiguousCustody, PhaseArchiveIntentCommitted, "both active and archived payload paths exist", false)
		return CustodyAmbiguous, manifest, &finding, nil
	}
	if sourcePresent {
		if tempManifest != nil || manifestPresent {
			finding := newWorkspaceFinding(plan.OperationID, FindingAmbiguousCustody, PhaseArchiveIntentCommitted, "archive manifest evidence exists before payload custody moved", false)
			return CustodyAmbiguous, manifest, &finding, nil
		}
		if !sourceMatches || payloadPresent {
			finding := newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseArchiveIntentCommitted, "active source identity changed", true)
			return CustodyAmbiguous, manifest, &finding, nil
		}
		return CustodyActive, manifest, nil, nil
	}
	if !payloadPresent || !payloadMatches {
		finding := newWorkspaceFinding(plan.OperationID, FindingAmbiguousCustody, PhaseArchiveIntentCommitted, "archived payload does not have the reviewed source identity", false)
		return CustodyAmbiguous, manifest, &finding, nil
	}
	inventory, err := InventoryMappedSource(ctx, s.Roots, WorkspaceOperationRestore, plan.Kind, plan.Slug)
	if err != nil || !reflect.DeepEqual(inventory, plan.Inventory) {
		finding := newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseArchivePayloadMoved, "archived payload inventory does not match the reviewed source", false)
		return CustodyAmbiguous, manifest, &finding, nil
	}
	return CustodyArchived, manifest, nil, nil
}

func (s WorkspaceMoveService) buildArchiveManifest(ctx context.Context, operation WorkspaceArchiveOperation, plan WorkspaceArchivePlan) (WorkspaceArchiveManifest, error) {
	archivedAt := derefTime(operation.PayloadMovedAt, operation.UpdatedAt)
	manifest := WorkspaceArchiveManifest{
		SchemaVersion:         WorkspaceArchiveManifestSchemaVersion,
		EvidenceKind:          PhysicalWorkspaceMoveEvidence,
		ArchiveOperationID:    plan.OperationID,
		Kind:                  plan.Kind,
		ObjectID:              plan.ObjectID,
		Slug:                  plan.Slug,
		LifecycleState:        WorkspaceLifecycleArchived,
		ActivePath:            plan.Source.Path,
		ArchivePayloadPath:    plan.Destination.Path,
		ArchiveSourceIdentity: plan.Source.Identity,
		InventoryDigest:       plan.Inventory.Digest,
		PlanDigest:            plan.PlanDigest,
		LifecycleEventID:      deterministicArchiveEventID(plan.OperationID),
		ActorID:               plan.ActorID,
		Reason:                plan.Reason,
		ArchivedAt:            archivedAt,
		Authentication:        ManifestAuthentication{Algorithm: "hmac-sha256", KeyID: s.ManifestKeyID},
	}
	if err := s.signManifest(ctx, &manifest); err != nil {
		return WorkspaceArchiveManifest{}, err
	}
	if err := ValidateWorkspaceArchiveManifestForOperations(manifest, operation, nil); err != nil {
		return WorkspaceArchiveManifest{}, err
	}
	return manifest, nil
}

func (s WorkspaceMoveService) publishManifest(ctx context.Context, plan WorkspaceArchivePlan, manifest WorkspaceArchiveManifest, guard *workspacePublicationGuard) error {
	if err := s.verifyManifest(ctx, manifest, plan); err != nil {
		return err
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(payload) > workspaceManifestMaxBytes {
		return fmt.Errorf("workspace archive manifest exceeds %d bytes", workspaceManifestMaxBytes)
	}
	if err := guard.verify(); err != nil {
		return err
	}
	if existingPayload, present, readErr := readFileAtNoFollow(guard.containerFD, "archive.json", workspaceManifestMaxBytes); readErr != nil {
		return readErr
	} else if present {
		existing, decodeErr := DecodePhysicalWorkspaceArchiveManifest(existingPayload)
		if decodeErr != nil {
			return decodeErr
		}
		if verifyErr := s.verifyManifest(ctx, existing, plan); verifyErr != nil {
			return verifyErr
		}
		if !bytes.Equal(existingPayload, payload) {
			return fmt.Errorf("authenticated archive manifest replay conflict")
		}
		return s.removeManifestTemp(ctx, plan, manifest, guard)
	}
	tempName := ".archive.json." + plan.OperationID + ".tmp"
	if err := writeExclusiveFileAt(guard.containerFD, tempName, payload, 0o600); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return err
		}
	} else if err := s.fail(BoundaryAfterManifestTemp); err != nil {
		return workspacePublicationBoundaryError{err: err}
	}
	temp, present, err := openBoundedFileAtNoFollow(guard.containerFD, tempName, workspaceManifestMaxBytes)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("archive manifest publication temp disappeared")
	}
	defer temp.Close()
	if err := s.verifyCanonicalManifestPayload(ctx, plan, manifest, temp.payload); err != nil {
		return err
	}
	if err := s.fail(BoundaryBeforeManifestPublish); err != nil {
		return workspacePublicationBoundaryError{err: err}
	}
	if err := guard.verify(); err != nil {
		return err
	}
	if err := verifyNamedHeldFile(guard.containerFD, tempName, temp); err != nil {
		return fmt.Errorf("archive manifest publication temp was substituted: %w", err)
	}
	if err := unix.Linkat(guard.containerFD, tempName, guard.containerFD, "archive.json", 0); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("publish archive manifest without overwrite: %w", err)
	}
	if err := unix.Fsync(guard.containerFD); err != nil {
		return err
	}
	storedPayload, storedPresent, err := readFileAtNoFollow(guard.containerFD, "archive.json", workspaceManifestMaxBytes)
	if err != nil {
		return err
	}
	if !storedPresent || !bytes.Equal(storedPayload, payload) {
		return fmt.Errorf("published archive manifest bytes do not match the canonical operation manifest")
	}
	if err := s.verifyCanonicalManifestPayload(ctx, plan, manifest, storedPayload); err != nil {
		return err
	}
	if err := s.removeHeldContainerFile(guard, tempName, temp); err != nil {
		return err
	}
	if err := unix.Fsync(guard.containerFD); err != nil {
		return err
	}
	return nil
}

func (s WorkspaceMoveService) verifyCanonicalManifestPayload(ctx context.Context, plan WorkspaceArchivePlan, canonical WorkspaceArchiveManifest, payload []byte) error {
	decoded, err := DecodePhysicalWorkspaceArchiveManifest(payload)
	if err != nil {
		return err
	}
	if err := s.verifyManifest(ctx, decoded, plan); err != nil {
		return err
	}
	canonicalPayload, err := json.Marshal(canonical)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, canonicalPayload) {
		return fmt.Errorf("authenticated archive manifest does not exactly match canonical operation bytes")
	}
	return nil
}

func (s WorkspaceMoveService) readAndVerifyManifestAt(ctx context.Context, containerFD int, plan WorkspaceArchivePlan) (*WorkspaceArchiveManifest, bool, error) {
	payload, present, err := readFileAtNoFollow(containerFD, "archive.json", workspaceManifestMaxBytes)
	if err != nil || !present {
		return nil, present, err
	}
	manifest, err := DecodePhysicalWorkspaceArchiveManifest(payload)
	if err != nil {
		return nil, true, err
	}
	if err := s.verifyManifest(ctx, manifest, plan); err != nil {
		return nil, true, err
	}
	return &manifest, true, nil
}

func (s WorkspaceMoveService) signManifest(ctx context.Context, manifest *WorkspaceArchiveManifest) error {
	if manifest == nil {
		return fmt.Errorf("workspace archive manifest is required")
	}
	payload, err := unsignedManifestJSON(*manifest)
	if err != nil {
		return err
	}
	tag, err := s.hmacTag(ctx, manifest.Authentication.KeyID, payload)
	if err != nil {
		return err
	}
	manifest.Authentication.Tag = tag
	return nil
}

func (s WorkspaceMoveService) verifyManifest(ctx context.Context, manifest WorkspaceArchiveManifest, plan WorkspaceArchivePlan) error {
	if err := ValidateWorkspaceArchiveManifestForRoots(manifest, s.Roots); err != nil {
		return err
	}
	if manifest.Authentication.KeyID != s.ManifestKeyID || manifest.ArchiveOperationID != plan.OperationID || manifest.PlanDigest != plan.PlanDigest || manifest.InventoryDigest != plan.Inventory.Digest || manifest.Kind != plan.Kind || manifest.ObjectID != plan.ObjectID || manifest.Slug != plan.Slug || !SamePathIdentity(manifest.ArchiveSourceIdentity, plan.Source.Identity) {
		return fmt.Errorf("archive manifest does not match reviewed operation/source/path/inventory evidence")
	}
	payload, err := unsignedManifestJSON(manifest)
	if err != nil {
		return err
	}
	expected, err := s.hmacTag(ctx, manifest.Authentication.KeyID, payload)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(expected), []byte(manifest.Authentication.Tag)) {
		return fmt.Errorf("archive manifest HMAC authentication failed")
	}
	return nil
}

func unsignedManifestJSON(manifest WorkspaceArchiveManifest) ([]byte, error) {
	manifest.Authentication.Tag = ""
	return json.Marshal(manifest)
}

// Compare the authenticated representation, not time.Time's process-local
// location pointers. Timestamp spelling and every other serialized field stay exact.
func sameWorkspaceManifest(left, right WorkspaceArchiveManifest) bool {
	a, err := json.Marshal(left)
	if err != nil {
		return false
	}
	b, err := json.Marshal(right)
	return err == nil && bytes.Equal(a, b)
}

func (s WorkspaceMoveService) sealIntentMarker(ctx context.Context, plan WorkspaceArchivePlan) (workspaceIntentMarker, error) {
	marker := workspaceIntentMarker{
		SchemaVersion:  "storage.workspace_archive_intent_marker.v1",
		OperationID:    plan.OperationID,
		PlanDigest:     plan.PlanDigest,
		Kind:           plan.Kind,
		ObjectID:       plan.ObjectID,
		Slug:           plan.Slug,
		Destination:    plan.Destination.Path,
		Authentication: ManifestAuthentication{Algorithm: "hmac-sha256", KeyID: s.ManifestKeyID},
	}
	payload, err := unsignedIntentJSON(marker)
	if err != nil {
		return workspaceIntentMarker{}, err
	}
	marker.Authentication.Tag, err = s.hmacTag(ctx, marker.Authentication.KeyID, payload)
	return marker, err
}

func (s WorkspaceMoveService) verifyIntentMarker(ctx context.Context, containerFD int, plan WorkspaceArchivePlan) error {
	payload, present, err := readFileAtNoFollow(containerFD, workspaceIntentMarkerName, workspaceIntentMaxBytes)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("archive intent marker is absent")
	}
	return s.verifyIntentMarkerPayload(ctx, payload, plan)
}

func (s WorkspaceMoveService) verifyIntentMarkerPayload(ctx context.Context, payload []byte, plan WorkspaceArchivePlan) error {
	var marker workspaceIntentMarker
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return fmt.Errorf("decode archive intent marker: %w", err)
	}
	if marker.SchemaVersion != "storage.workspace_archive_intent_marker.v1" || marker.OperationID != plan.OperationID || marker.PlanDigest != plan.PlanDigest || marker.Kind != plan.Kind || marker.ObjectID != plan.ObjectID || marker.Slug != plan.Slug || !samePathRef(marker.Destination, plan.Destination.Path) || marker.Authentication.Algorithm != "hmac-sha256" || marker.Authentication.KeyID != s.ManifestKeyID || !manifestKeyIDPattern.MatchString(marker.Authentication.KeyID) || !hmacSHA256DigestPattern.MatchString(marker.Authentication.Tag) {
		return fmt.Errorf("archive intent marker does not match reviewed plan")
	}
	unsigned, err := unsignedIntentJSON(marker)
	if err != nil {
		return err
	}
	expected, err := s.hmacTag(ctx, marker.Authentication.KeyID, unsigned)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(expected), []byte(marker.Authentication.Tag)) {
		return fmt.Errorf("archive intent marker HMAC authentication failed")
	}
	return nil
}

func unsignedIntentJSON(marker workspaceIntentMarker) ([]byte, error) {
	marker.Authentication.Tag = ""
	return json.Marshal(marker)
}

func (s WorkspaceMoveService) hmacTag(ctx context.Context, keyID string, payload []byte) (string, error) {
	if !manifestKeyIDPattern.MatchString(keyID) || s.ManifestKey == nil {
		return "", fmt.Errorf("workspace archive manifest authentication is not configured")
	}
	key, err := s.ManifestKey(ctx, keyID)
	if err != nil {
		return "", fmt.Errorf("load workspace archive manifest key: %w", err)
	}
	defer zeroWorkspaceManifestKey(key)
	if len(key) < 32 {
		return "", fmt.Errorf("workspace archive manifest key must contain at least 32 bytes")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil)), nil
}

func (s WorkspaceMoveService) preflightAuthentication(ctx context.Context) error {
	_, err := s.hmacTag(ctx, s.ManifestKeyID, []byte("workspace-archive-authentication-preflight"))
	return err
}

func (s WorkspaceMoveService) acquirePublicationGuard(ctx context.Context, plan WorkspaceArchivePlan) (*workspacePublicationGuard, error) {
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return nil, err
	}
	chain, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(paths.Mapping.ArchiveContainerPath), plan.DestinationAncestors)
	if err != nil {
		return nil, err
	}
	containerFD, err := unix.Openat(chain.ParentFD(), plan.Slug, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		chain.Close()
		return nil, err
	}
	containerIdentity, err := identityForFD(containerFD)
	if err != nil {
		_ = unix.Close(containerFD)
		chain.Close()
		return nil, err
	}
	marker, present, err := openBoundedFileAtNoFollow(containerFD, workspaceIntentMarkerName, workspaceIntentMaxBytes)
	if err != nil {
		_ = unix.Close(containerFD)
		chain.Close()
		return nil, err
	}
	if !present {
		_ = unix.Close(containerFD)
		chain.Close()
		return nil, errWorkspacePublicationClaimGone
	}
	if err := s.verifyIntentMarkerPayload(ctx, marker.payload, plan); err != nil {
		marker.Close()
		_ = unix.Close(containerFD)
		chain.Close()
		return nil, err
	}
	for {
		err = unix.Flock(int(marker.file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			marker.Close()
			_ = unix.Close(containerFD)
			chain.Close()
			return nil, err
		}
		if hookErr := s.fail(BoundaryPublicationClaimContended); hookErr != nil {
			marker.Close()
			_ = unix.Close(containerFD)
			chain.Close()
			return nil, hookErr
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			marker.Close()
			_ = unix.Close(containerFD)
			chain.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	guard := &workspacePublicationGuard{
		chain:             chain,
		containerFD:       containerFD,
		containerName:     plan.Slug,
		containerIdentity: containerIdentity,
		marker:            marker,
	}
	if err := guard.verify(); err != nil {
		guard.Close()
		return nil, err
	}
	if err := s.verifyIntentMarkerPayload(ctx, marker.payload, plan); err != nil {
		guard.Close()
		return nil, err
	}
	return guard, nil
}

func (s WorkspaceMoveService) removeIntentMarker(ctx context.Context, plan WorkspaceArchivePlan, guard *workspacePublicationGuard) error {
	if err := s.verifyIntentMarkerPayload(ctx, guard.marker.payload, plan); err != nil {
		return err
	}
	if err := s.removeHeldContainerFile(guard, workspaceIntentMarkerName, guard.marker); err != nil {
		return err
	}
	return unix.Fsync(guard.containerFD)
}

func (s WorkspaceMoveService) removeManifestTemp(ctx context.Context, plan WorkspaceArchivePlan, canonical WorkspaceArchiveManifest, guard *workspacePublicationGuard) error {
	name := ".archive.json." + plan.OperationID + ".tmp"
	temp, present, err := openBoundedFileAtNoFollow(guard.containerFD, name, workspaceManifestMaxBytes)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	defer temp.Close()
	if err := s.verifyCanonicalManifestPayload(ctx, plan, canonical, temp.payload); err != nil {
		return err
	}
	if err := s.removeHeldContainerFile(guard, name, temp); err != nil {
		return err
	}
	return unix.Fsync(guard.containerFD)
}

func (s WorkspaceMoveService) removeHeldContainerFile(guard *workspacePublicationGuard, name string, held *heldBoundedFile) error {
	if err := guard.verify(); err != nil {
		return err
	}
	if err := verifyNamedHeldFile(guard.containerFD, name, held); err != nil {
		return fmt.Errorf("refuse to unlink substituted archive container entry %q: %w", name, err)
	}
	if err := unix.Unlinkat(guard.containerFD, name, 0); err != nil {
		return err
	}
	return nil
}

func journalIntentInput(plan WorkspaceArchivePlan, at time.Time) (storagecatalog.WorkspaceArchiveIntentInput, error) {
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return storagecatalog.WorkspaceArchiveIntentInput{}, err
	}
	sourceIdentity, _ := json.Marshal(plan.Source.Identity)
	sourceParent, _ := json.Marshal(plan.Source.ParentIdentity)
	destinationIdentity, _ := json.Marshal(plan.Destination.Identity)
	destinationParent, _ := json.Marshal(plan.Destination.ParentIdentity)
	return storagecatalog.WorkspaceArchiveIntentInput{
		OperationID:                   plan.OperationID,
		SchemaVersion:                 WorkspaceArchiveOperationSchemaVersion,
		EvidenceKind:                  plan.EvidenceKind,
		OperationKind:                 string(plan.OperationKind),
		WorkspaceKind:                 string(plan.Kind),
		ObjectID:                      plan.ObjectID,
		Slug:                          plan.Slug,
		PlanDigest:                    plan.PlanDigest,
		PlanJSON:                      planJSON,
		SourceRoot:                    string(plan.Source.Path.Root),
		SourceRelativePath:            plan.Source.Path.RelativePath,
		SourceIdentityJSON:            sourceIdentity,
		SourceParentIdentityJSON:      sourceParent,
		DestinationRoot:               string(plan.Destination.Path.Root),
		DestinationRelativePath:       plan.Destination.Path.RelativePath,
		DestinationIdentityJSON:       destinationIdentity,
		DestinationParentIdentityJSON: destinationParent,
		InventoryDigest:               plan.Inventory.Digest,
		ActorID:                       plan.ActorID,
		Reason:                        plan.Reason,
		PlannedAt:                     plan.PlannedAt,
		IntentCommittedAt:             at,
	}, nil
}

func journalProjectionInput(plan WorkspaceArchivePlan, manifest WorkspaceArchiveManifest, event WorkspaceLifecycleEvent, at time.Time) (storagecatalog.WorkspaceArchiveProjectionInput, error) {
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return storagecatalog.WorkspaceArchiveProjectionInput{}, err
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return storagecatalog.WorkspaceArchiveProjectionInput{}, err
	}
	identityJSON, _ := json.Marshal(plan.Source.Identity)
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
	return storagecatalog.WorkspaceArchiveProjectionInput{
		OperationID:               plan.OperationID,
		PlanDigest:                plan.PlanDigest,
		SourceAbsolutePath:        plan.Source.Path.AbsolutePath,
		Rebind:                    rebind,
		ManifestSchemaVersion:     manifest.SchemaVersion,
		EvidenceKind:              manifest.EvidenceKind,
		WorkspaceKind:             string(manifest.Kind),
		ObjectID:                  manifest.ObjectID,
		Slug:                      manifest.Slug,
		InventoryDigest:           manifest.InventoryDigest,
		ArchiveSourceIdentityJSON: identityJSON,
		ManifestJSON:              manifestJSON,
		AuthenticationKeyID:       manifest.Authentication.KeyID,
		AuthenticationTag:         manifest.Authentication.Tag,
		ArchivedAt:                manifest.ArchivedAt,
		EventID:                   event.EventID,
		EventSchemaVersion:        event.SchemaVersion,
		EventKind:                 event.EventKind,
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

func eventForArchive(operation WorkspaceArchiveOperation, manifest WorkspaceArchiveManifest, at time.Time) WorkspaceLifecycleEvent {
	return WorkspaceLifecycleEvent{
		SchemaVersion:   WorkspaceLifecycleEventSchemaVersion,
		EventID:         manifest.LifecycleEventID,
		EventKind:       WorkspaceLifecycleEventKind,
		OperationID:     operation.OperationID,
		Kind:            operation.Kind,
		ObjectID:        operation.ObjectID,
		Slug:            operation.Slug,
		Transition:      WorkspaceTransitionArchived,
		FromState:       WorkspaceLifecycleActive,
		ToState:         WorkspaceLifecycleArchived,
		SourcePath:      operation.Source.Path,
		DestinationPath: operation.Destination.Path,
		ActorID:         operation.ActorID,
		Reason:          operation.Reason,
		OccurredAt:      normalizeWorkspaceTimestamp(at),
	}
}

func decodeJournalPlan(record storagecatalog.WorkspaceArchiveJournalRecord) (WorkspaceArchivePlan, error) {
	var plan WorkspaceArchivePlan
	decoder := json.NewDecoder(bytes.NewReader(record.PlanJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return WorkspaceArchivePlan{}, fmt.Errorf("decode durable workspace archive plan: %w", err)
	}
	if plan.OperationID != record.OperationID || plan.PlanDigest != record.PlanDigest {
		return WorkspaceArchivePlan{}, fmt.Errorf("durable workspace archive plan contradicts journal columns")
	}
	return plan, nil
}

func operationFromJournal(record storagecatalog.WorkspaceArchiveJournalRecord, plan WorkspaceArchivePlan) (WorkspaceArchiveOperation, error) {
	if record.OperationID != plan.OperationID || record.PlanDigest != plan.PlanDigest || !record.PlannedAt.Equal(plan.PlannedAt) {
		return WorkspaceArchiveOperation{}, fmt.Errorf("durable workspace archive journal contradicts its reviewed plan")
	}
	operation := WorkspaceArchiveOperation{
		SchemaVersion:          WorkspaceArchiveOperationSchemaVersion,
		EvidenceKind:           PhysicalWorkspaceMoveEvidence,
		OperationID:            plan.OperationID,
		OperationKind:          plan.OperationKind,
		Kind:                   plan.Kind,
		ObjectID:               plan.ObjectID,
		Slug:                   plan.Slug,
		Phase:                  WorkspaceArchivePhase(record.Phase),
		LastSafePhase:          WorkspaceArchivePhase(record.LastSafePhase),
		Status:                 WorkspaceArchiveOperationStatus(record.TerminalStatus),
		PlanDigest:             plan.PlanDigest,
		Source:                 plan.Source,
		Destination:            plan.Destination,
		ActorID:                plan.ActorID,
		Reason:                 plan.Reason,
		PlannedAt:              record.PlannedAt,
		IntentCommittedAt:      record.IntentCommittedAt,
		PayloadMovedAt:         record.PayloadMovedAt,
		ProjectionsCommittedAt: record.ProjectionsCommittedAt,
		CompletedAt:            record.CompletedAt,
		UpdatedAt:              record.UpdatedAt,
	}
	for _, item := range record.Findings {
		var evidence []WorkspaceArchiveEvidenceField
		if len(item.Evidence) > 0 && string(item.Evidence) != "null" {
			if err := json.Unmarshal(item.Evidence, &evidence); err != nil {
				return WorkspaceArchiveOperation{}, fmt.Errorf("decode workspace archive finding evidence: %w", err)
			}
		}
		operation.Findings = append(operation.Findings, WorkspaceArchiveFinding{
			SchemaVersion: WorkspaceArchiveFindingSchemaVersion,
			FindingID:     item.FindingID,
			OperationID:   plan.OperationID,
			Code:          WorkspaceArchiveFindingCode(item.Code),
			Severity:      WorkspaceArchiveFindingSeverity(item.Severity),
			AtPhase:       WorkspaceArchivePhase(item.AtPhase),
			Summary:       item.Summary,
			Repairable:    item.Repairable,
			Evidence:      evidence,
			CreatedAt:     item.CreatedAt,
		})
	}
	if err := ValidateWorkspaceArchiveOperation(operation); err != nil {
		return WorkspaceArchiveOperation{}, fmt.Errorf("invalid durable workspace archive operation: %w", err)
	}
	return operation, nil
}

func (s WorkspaceMoveService) recordFailure(ctx context.Context, operation WorkspaceArchiveOperation, finding WorkspaceArchiveFinding, manual bool) (storagecatalog.WorkspaceArchiveJournalRecord, error) {
	finding.FindingID = newWorkspaceID("workspace_archive_finding")
	finding.CreatedAt = s.nowAtLeast(operation.UpdatedAt)
	evidenceJSON, _ := json.Marshal(finding.Evidence)
	phase, status := PhaseBlocked, OperationStatusBlocked
	if manual {
		phase, status = PhaseManualRepairRequired, OperationStatusManualRepair
	}
	lastSafe := operation.Phase
	if operation.Phase == PhaseBlocked || operation.Phase == PhaseManualRepairRequired {
		lastSafe = operation.LastSafePhase
	}
	return s.Journal.RecordWorkspaceArchiveFailure(ctx, storagecatalog.WorkspaceArchiveFailureInput{
		OperationID:   operation.OperationID,
		PlanDigest:    operation.PlanDigest,
		Phase:         string(phase),
		LastSafePhase: string(lastSafe),
		Status:        string(status),
		FindingID:     finding.FindingID,
		FindingCode:   string(finding.Code),
		Severity:      string(finding.Severity),
		AtPhase:       string(finding.AtPhase),
		Summary:       finding.Summary,
		Repairable:    finding.Repairable,
		EvidenceJSON:  evidenceJSON,
		RecordedAt:    finding.CreatedAt,
	})
}

func rebaseCatalogPath(value string, source, destination WorkspacePathRef) (string, bool, error) {
	if value == "" {
		return "", false, nil
	}
	if strings.HasPrefix(value, "file://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "file" || parsed.Host != "" || !filepath.IsAbs(parsed.Path) {
			return value, false, nil
		}
		clean := filepath.Clean(parsed.Path)
		rebased, changed, err := rebaseCatalogPath(clean, source, destination)
		if err != nil || !changed {
			return rebased, changed, err
		}
		return (&url.URL{Scheme: "file", Path: rebased}).String(), true, nil
	}
	if filepath.IsAbs(value) {
		clean := filepath.Clean(value)
		rel, err := filepath.Rel(source.AbsolutePath, clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return value, false, nil
		}
		if clean != value {
			return "", false, fmt.Errorf("catalog path below the reviewed source is not clean: %q", value)
		}
		return filepath.Join(destination.AbsolutePath, rel), true, nil
	}
	slashValue := filepath.ToSlash(value)
	clean := path.Clean(slashValue)
	if clean != source.RelativePath && !strings.HasPrefix(clean, source.RelativePath+"/") {
		return value, false, nil
	}
	if clean != slashValue {
		return "", false, fmt.Errorf("catalog path below the reviewed source is not clean: %q", value)
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(clean, source.RelativePath), "/")
	return path.Join(destination.RelativePath, rel), true, nil
}

func identityForFD(fd int) (PathIdentity, error) {
	dup, err := unix.Dup(fd)
	if err != nil {
		return PathIdentity{}, err
	}
	file := os.NewFile(uintptr(dup), "held-directory")
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return PathIdentity{}, statErr
	}
	if closeErr != nil {
		return PathIdentity{}, closeErr
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return PathIdentity{}, fmt.Errorf("held path is not a real directory")
	}
	return pathIdentityFromInfo(info)
}

func namePresentNoFollow(parentFD int, name string) (bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	return err == nil, err
}

func verifyNamedDirectoryIdentity(parentFD int, name string, expected PathIdentity) error {
	present, matches, err := inspectNamedDirectory(parentFD, name, expected)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("path is absent")
	}
	if !matches {
		return fmt.Errorf("path identity changed")
	}
	return nil
}

func inspectNamedDirectory(parentFD int, name string, expected PathIdentity) (bool, bool, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, false, nil
	}
	if err != nil {
		return true, false, nil
	}
	identity, statErr := identityForFD(fd)
	_ = unix.Close(fd)
	if statErr != nil {
		return true, false, statErr
	}
	return true, sameStablePayloadIdentity(identity, expected), nil
}

func sameStableDirectoryIdentity(left, right PathIdentity) bool {
	return left.Presence == PathPresent && right.Presence == PathPresent && left.ObjectKind == string(InventoryEntryDirectory) && right.ObjectKind == string(InventoryEntryDirectory) && left.DeviceID == right.DeviceID && left.Inode == right.Inode && left.Mode == right.Mode
}

func sameStablePayloadIdentity(left, right PathIdentity) bool {
	return sameStableDirectoryIdentity(left, right)
}

func writeExclusiveFileAt(parentFD int, name string, payload []byte, mode uint32) error {
	fd, err := unix.Openat(parentFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	_, writeErr := file.Write(payload)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func readFileAtNoFollow(parentFD int, name string, limit int64) ([]byte, bool, error) {
	held, present, err := openBoundedFileAtNoFollow(parentFD, name, limit)
	if err != nil || !present {
		return nil, present, err
	}
	defer held.Close()
	return append([]byte(nil), held.payload...), true, nil
}

func openBoundedFileAtNoFollow(parentFD int, name string, limit int64) (*heldBoundedFile, bool, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	file := os.NewFile(uintptr(fd), name)
	payload, readErr := stableHeldFilePayload(file, limit)
	if readErr != nil {
		_ = file.Close()
		return nil, true, fmt.Errorf("read bounded %s: %w", name, readErr)
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, true, statErr
	}
	held := &heldBoundedFile{file: file, info: info, payload: payload}
	if err := verifyNamedHeldFile(parentFD, name, held); err != nil {
		held.Close()
		return nil, true, fmt.Errorf("%s was substituted while being read: %w", name, err)
	}
	return held, true, nil
}

func stableHeldFilePayload(file *os.File, limit int64) ([]byte, error) {
	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > limit {
		return nil, fmt.Errorf("file is not a bounded regular file")
	}
	payload, err := io.ReadAll(io.NewSectionReader(file, 0, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, fmt.Errorf("file exceeds the bounded read limit")
	}
	after, err := file.Stat()
	if err != nil || !stableInventoryFile(before, after) {
		return nil, fmt.Errorf("file changed while being read")
	}
	return payload, nil
}

func verifyNamedHeldFile(parentFD int, name string, held *heldBoundedFile) error {
	if held == nil || held.file == nil {
		return fmt.Errorf("held file is closed")
	}
	namedFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	namedFile := os.NewFile(uintptr(namedFD), name)
	namedInfo, namedErr := namedFile.Stat()
	closeErr := namedFile.Close()
	if namedErr != nil {
		return namedErr
	}
	if closeErr != nil {
		return closeErr
	}
	currentInfo, err := held.file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(currentInfo, namedInfo) || !os.SameFile(held.info, currentInfo) {
		return fmt.Errorf("file identity changed")
	}
	return nil
}

func verifyContainerEntries(fd int, allowed map[string]bool) error {
	dup, err := unix.Dup(fd)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(dup), "archive-container")
	names, readErr := directory.Readdirnames(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, name := range names {
		if !allowed[name] {
			return fmt.Errorf("archive container contains unexpected entry %q", name)
		}
	}
	return nil
}

func validateDurableWorkspaceProjection(record storagecatalog.WorkspaceArchiveJournalRecord, operation WorkspaceArchiveOperation, filesystemManifest WorkspaceArchiveManifest) error {
	if len(record.ManifestJSON) == 0 || len(record.EventJSON) == 0 {
		return fmt.Errorf("projection-committed operation lacks durable manifest or lifecycle event evidence")
	}
	var durableManifest WorkspaceArchiveManifest
	if err := json.Unmarshal(record.ManifestJSON, &durableManifest); err != nil {
		return fmt.Errorf("decode durable workspace archive manifest: %w", err)
	}
	if !sameWorkspaceManifest(durableManifest, filesystemManifest) {
		return fmt.Errorf("filesystem and durable workspace archive manifests differ")
	}
	if err := ValidateWorkspaceArchiveManifestForOperations(durableManifest, operation, nil); err != nil {
		return err
	}
	var event WorkspaceLifecycleEvent
	if err := json.Unmarshal(record.EventJSON, &event); err != nil {
		return fmt.Errorf("decode durable workspace lifecycle event: %w", err)
	}
	if event.EventID != durableManifest.LifecycleEventID {
		return fmt.Errorf("workspace lifecycle event does not match archive manifest")
	}
	if err := ValidateWorkspaceLifecycleEventForOperation(event, operation); err != nil {
		return err
	}
	canonicalEvent := eventForArchive(operation, durableManifest, *operation.ProjectionsCommittedAt)
	if !reflect.DeepEqual(event, canonicalEvent) {
		return fmt.Errorf("durable workspace lifecycle event does not match the canonical operation event")
	}
	return nil
}

func newWorkspaceID(prefix string) string {
	return prefix + "_" + ulid.MustNew(ulid.Timestamp(time.Now().UTC()), rand.Reader).String()
}

func deterministicArchiveEventID(operationID string) string {
	return "workspace_lifecycle_event_" + strings.TrimPrefix(operationID, "workspace_archive_operation_")
}

func lastSafeArchivePhase(operation WorkspaceArchiveOperation) WorkspaceArchivePhase {
	if operation.Phase == PhaseBlocked || operation.Phase == PhaseManualRepairRequired {
		return operation.LastSafePhase
	}
	return operation.Phase
}

func workspacePathID(value string, pattern interface{ MatchString(string) bool }) bool {
	return pattern.MatchString(value)
}

func (s WorkspaceMoveService) now() time.Time {
	if s.Now != nil {
		return normalizeWorkspaceTimestamp(s.Now())
	}
	return normalizeWorkspaceTimestamp(time.Now())
}

func (s WorkspaceMoveService) nowAtLeast(minimum time.Time) time.Time {
	now := s.now()
	minimum = normalizeWorkspaceTimestamp(minimum)
	if now.Before(minimum) {
		return minimum
	}
	return now
}

func normalizeWorkspaceTimestamp(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func (s WorkspaceMoveService) fail(boundary WorkspaceMoveBoundary) error {
	if s.FailureHook == nil {
		return nil
	}
	return s.FailureHook(boundary)
}

func newWorkspaceFinding(operationID string, code WorkspaceArchiveFindingCode, phase WorkspaceArchivePhase, summary string, repairable bool) WorkspaceArchiveFinding {
	if len(summary) > maxWorkspaceArchiveSummaryBytes {
		summary = summary[:maxWorkspaceArchiveSummaryBytes]
	}
	return WorkspaceArchiveFinding{SchemaVersion: WorkspaceArchiveFindingSchemaVersion, OperationID: operationID, Code: code, Severity: FindingSeverityError, AtPhase: phase, Summary: strings.TrimSpace(summary), Repairable: repairable}
}

func findingsError(findings []WorkspaceArchiveFinding) error {
	parts := make([]string, 0, len(findings))
	for _, finding := range findings {
		parts = append(parts, string(finding.Code)+": "+finding.Summary)
	}
	return errors.New(strings.Join(parts, "; "))
}

func uniqueFindings(findings []WorkspaceArchiveFinding) []WorkspaceArchiveFinding {
	seen := map[string]bool{}
	result := make([]WorkspaceArchiveFinding, 0, len(findings))
	for _, finding := range findings {
		key := string(finding.Code) + "\x00" + finding.Summary
		if !seen[key] {
			seen[key] = true
			result = append(result, finding)
		}
	}
	return result
}

func hasEquivalentFinding(findings []WorkspaceArchiveFinding, candidate WorkspaceArchiveFinding) bool {
	for _, finding := range findings {
		if finding.Code == candidate.Code && finding.Summary == candidate.Summary {
			return true
		}
	}
	return false
}

func derefTime(value *time.Time, fallback time.Time) time.Time {
	if value == nil {
		return fallback
	}
	return *value
}
