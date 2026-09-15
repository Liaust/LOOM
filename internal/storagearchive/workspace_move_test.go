package storagearchive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

const (
	moveTestOperationID  = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAX"
	moveTestEntryID      = "storage_entry_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	moveTestRefID        = "storage_physical_ref_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	moveTestFileURIRefID = "storage_physical_ref_01ARZ3NDEKTSV4RRFFQ69G5FAW"
)

func TestWorkspaceArchivePlanIsReadOnlyAndBindsExactEvidence(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan, err := fixture.service.PlanArchive(context.Background(), fixture.planInput())
	if err != nil {
		t.Fatalf("PlanArchive: %v", err)
	}
	if plan.OperationID != moveTestOperationID || plan.PlanDigest == "" || plan.Inventory.FileCount != 1 || plan.Inventory.SymlinkCount != 1 {
		t.Fatalf("plan evidence = %#v", plan)
	}
	if len(plan.SourceAncestors) != 2 || len(plan.DestinationAncestors) != 3 || len(plan.CatalogRebinds) != 2 {
		t.Fatalf("ancestor/catalog evidence = source:%#v destination:%#v catalog:%#v", plan.SourceAncestors, plan.DestinationAncestors, plan.CatalogRebinds)
	}
	if fixture.journal.exists {
		t.Fatal("read-only plan committed a journal record")
	}
	if _, err := os.Lstat(fixture.paths.ArchiveContainer.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only plan created archive container: %v", err)
	}
	status, err := fixture.service.InspectPlan(context.Background(), plan)
	if err != nil || status.Phase != PhaseArchivePlanned || status.Status != OperationStatusPending {
		t.Fatalf("InspectPlan = %#v, %v", status, err)
	}
}

func TestWorkspaceArchiveMovePreservesPayloadAndProjectsOnce(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	before := snapshotWorkspacePayload(t, fixture.paths.Active.AbsolutePath)
	plan := fixture.plan(t)
	result, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest)
	if err != nil {
		t.Fatalf("ApplyArchive: %v", err)
	}
	if result.Operation.Phase != PhaseArchiveComplete || result.Custody != CustodyArchived || result.Manifest == nil {
		t.Fatalf("archive result = %#v", result)
	}
	if _, err := os.Lstat(fixture.paths.Active.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active source still exists: %v", err)
	}
	after := snapshotWorkspacePayload(t, fixture.paths.ArchivePayload.AbsolutePath)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("payload fidelity changed\nbefore=%#v\nafter=%#v", before, after)
	}
	if _, err := os.Lstat(filepath.Join(fixture.paths.ArchiveContainer.AbsolutePath, workspaceIntentMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("intent marker remains after completion: %v", err)
	}
	if fixture.journal.eventCommits != 1 || fixture.journal.catalogCommits != 1 {
		t.Fatalf("projection counts catalog=%d event=%d", fixture.journal.catalogCommits, fixture.journal.eventCommits)
	}
	if got := fixture.journal.refURIs[moveTestRefID]; got != filepath.Join(fixture.paths.ArchivePayload.AbsolutePath, "payload.txt") {
		t.Fatalf("rebound physical ref = %q", got)
	}
	result, err = fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest)
	if err != nil || result.Operation.Phase != PhaseArchiveComplete {
		t.Fatalf("idempotent ApplyArchive = %#v, %v", result, err)
	}
	if fixture.journal.eventCommits != 1 || fixture.journal.catalogCommits != 1 {
		t.Fatalf("idempotent retry duplicated projection catalog=%d event=%d", fixture.journal.catalogCommits, fixture.journal.eventCommits)
	}
}

func TestWorkspaceArchivePhaseTimestampsAreTruthfulAndReplayStable(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan := fixture.plan(t)
	clock := plan.PlannedAt
	fixture.service.Now = func() time.Time { return clock }
	fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		switch boundary {
		case BoundaryBeforeIntent:
			clock = plan.PlannedAt.Add(time.Second)
		case BoundaryBeforeMovedRecord:
			clock = plan.PlannedAt.Add(2 * time.Second)
		case BoundaryBeforeProjections:
			clock = plan.PlannedAt.Add(3 * time.Second)
		case BoundaryBeforeComplete:
			clock = plan.PlannedAt.Add(4 * time.Second)
		}
		return nil
	}

	result, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	operation := result.Operation
	if operation.PayloadMovedAt == nil || operation.ProjectionsCommittedAt == nil || operation.CompletedAt == nil {
		t.Fatalf("complete operation timestamps = %#v", operation)
	}
	if !operation.PayloadMovedAt.Before(*operation.ProjectionsCommittedAt) || !operation.ProjectionsCommittedAt.Before(*operation.CompletedAt) {
		t.Fatalf("phase timestamps are not truthful: moved=%s projected=%s completed=%s", operation.PayloadMovedAt, operation.ProjectionsCommittedAt, operation.CompletedAt)
	}
	if result.Manifest == nil || !result.Manifest.ArchivedAt.Equal(*operation.PayloadMovedAt) {
		t.Fatalf("manifest archived_at = %#v, want payload_moved_at %s", result.Manifest, operation.PayloadMovedAt)
	}
	var event WorkspaceLifecycleEvent
	if err := json.Unmarshal(fixture.journal.record.EventJSON, &event); err != nil {
		t.Fatal(err)
	}
	if !event.OccurredAt.Equal(*operation.ProjectionsCommittedAt) {
		t.Fatalf("event occurred_at = %s, want projections_committed_at %s", event.OccurredAt, operation.ProjectionsCommittedAt)
	}

	storedManifestJSON := string(fixture.journal.record.ManifestJSON)
	storedEventJSON := string(fixture.journal.record.EventJSON)
	clock = plan.PlannedAt.Add(time.Hour)
	fixture.service.FailureHook = nil
	replayed, err := fixture.service.RecoverArchive(context.Background(), plan.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed.Operation, operation) || !reflect.DeepEqual(replayed.Manifest, result.Manifest) {
		t.Fatalf("replay changed durable phase evidence\nfirst=%#v\nreplay=%#v", result, replayed)
	}
	if string(fixture.journal.record.ManifestJSON) != storedManifestJSON || string(fixture.journal.record.EventJSON) != storedEventJSON {
		t.Fatal("replay changed durable manifest or lifecycle event bytes")
	}
}

func TestWorkspaceArchivePlanAndProjectionPreserveFileURIRepresentation(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	detail := fixture.catalog.details[moveTestEntryID]
	oldURI := (&url.URL{Scheme: "file", Path: filepath.Join(fixture.paths.Active.AbsolutePath, "payload.txt")}).String()
	newURI := (&url.URL{Scheme: "file", Path: filepath.Join(fixture.paths.ArchivePayload.AbsolutePath, "payload.txt")}).String()
	detail.PhysicalRefs = append(detail.PhysicalRefs, storagecatalog.PhysicalRef{
		StoragePhysicalRefID: moveTestFileURIRefID,
		StorageEntryID:       moveTestEntryID,
		RefKind:              storagecatalog.PhysicalRefKindLocalPath,
		URI:                  oldURI,
		Status:               storagecatalog.PhysicalRefStatusAvailable,
	})
	fixture.catalog.details[moveTestEntryID] = detail
	fixture.journal.refURIs[moveTestFileURIRefID] = oldURI

	plan := fixture.plan(t)
	if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err != nil {
		t.Fatal(err)
	}
	if got := fixture.journal.refURIs[moveTestFileURIRefID]; got != newURI {
		t.Fatalf("rebound file URI = %q, want %q", got, newURI)
	}
}

func TestWorkspaceArchiveCrashRecoverEveryDurableBoundary(t *testing.T) {
	boundaries := []WorkspaceMoveBoundary{
		BoundaryBeforeIntent,
		BoundaryAfterIntent,
		BoundaryBeforePayloadMove,
		BoundaryAfterPayloadMove,
		BoundaryBeforeMovedRecord,
		BoundaryAfterMovedRecord,
		BoundaryBeforeManifest,
		BoundaryAfterManifestTemp,
		BoundaryBeforeManifestPublish,
		BoundaryAfterManifest,
		BoundaryBeforeProjections,
		BoundaryAfterProjections,
		BoundaryBeforeIntentCleanup,
		BoundaryBeforeComplete,
		BoundaryAfterComplete,
	}
	for _, boundary := range boundaries {
		t.Run(string(boundary), func(t *testing.T) {
			fixture := newWorkspaceMoveFixture(t)
			plan := fixture.plan(t)
			injected := false
			fixture.service.FailureHook = func(observed WorkspaceMoveBoundary) error {
				if !injected && observed == boundary {
					injected = true
					return fmt.Errorf("injected failure at %s", boundary)
				}
				return nil
			}
			if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "injected failure") {
				t.Fatalf("injected ApplyArchive error = %v", err)
			}
			if !injected {
				t.Fatalf("boundary %s was not reached", boundary)
			}
			if boundary == BoundaryBeforeIntent {
				status, inspectErr := fixture.service.InspectPlan(context.Background(), plan)
				if inspectErr != nil || status.Phase != PhaseArchivePlanned || status.Status != OperationStatusPending {
					t.Fatalf("planned boundary after %s = %#v, %v", boundary, status, inspectErr)
				}
			} else {
				partial, inspectErr := fixture.service.InspectArchive(context.Background(), plan.OperationID)
				if inspectErr != nil {
					t.Fatalf("inspect partial state after %s: %v", boundary, inspectErr)
				}
				wantPhase, wantCustody := expectedPartialState(boundary)
				if partial.Operation.Phase != wantPhase || partial.Custody != wantCustody {
					t.Fatalf("partial state after %s phase=%s custody=%s, want phase=%s custody=%s", boundary, partial.Operation.Phase, partial.Custody, wantPhase, wantCustody)
				}
			}
			fixture.service.FailureHook = nil
			var result WorkspaceArchiveInspection
			var err error
			if boundary == BoundaryBeforeIntent {
				result, err = fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest)
			} else {
				result, err = fixture.service.RecoverArchive(context.Background(), plan.OperationID)
			}
			if err != nil {
				t.Fatalf("recover after %s: %v", boundary, err)
			}
			if result.Operation.Phase != PhaseArchiveComplete || result.Custody != CustodyArchived {
				t.Fatalf("recovery after %s = %#v", boundary, result)
			}
			if fixture.journal.eventCommits != 1 || fixture.journal.catalogCommits != 1 {
				t.Fatalf("recovery after %s duplicated projection catalog=%d event=%d", boundary, fixture.journal.catalogCommits, fixture.journal.eventCommits)
			}
			if _, sourceErr := os.Lstat(fixture.paths.Active.AbsolutePath); !errors.Is(sourceErr, os.ErrNotExist) {
				t.Fatalf("source exists after %s recovery: %v", boundary, sourceErr)
			}
		})
	}
}

func TestWorkspaceArchiveConcurrentRecoverySerializesManifestPublication(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan := fixture.plan(t)
	fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryAfterMovedRecord {
			return errors.New("stop after durable payload-moved record")
		}
		return nil
	}
	if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil {
		t.Fatal("expected payload-moved stop")
	}

	tempPath := filepath.Join(fixture.paths.ArchiveContainer.AbsolutePath, ".archive.json."+plan.OperationID+".tmp")
	firstTempReady := make(chan os.FileInfo, 1)
	releaseFirst := make(chan struct{})
	first := fixture.service
	first.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryAfterManifestTemp {
			info, err := os.Lstat(tempPath)
			if err != nil {
				return err
			}
			firstTempReady <- info
			<-releaseFirst
		}
		return nil
	}
	secondContended := make(chan struct{})
	var contendedOnce sync.Once
	second := fixture.service
	second.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryPublicationClaimContended {
			contendedOnce.Do(func() { close(secondContended) })
		}
		return nil
	}
	type recoveryResult struct {
		inspection WorkspaceArchiveInspection
		err        error
	}
	results := make(chan recoveryResult, 2)
	go func() {
		inspection, err := first.RecoverArchive(context.Background(), plan.OperationID)
		results <- recoveryResult{inspection: inspection, err: err}
	}()

	var firstTempInfo os.FileInfo
	select {
	case firstTempInfo = <-firstTempReady:
	case <-time.After(5 * time.Second):
		t.Fatal("first recovery did not pause after manifest temp creation")
	}
	go func() {
		inspection, err := second.RecoverArchive(context.Background(), plan.OperationID)
		results <- recoveryResult{inspection: inspection, err: err}
	}()
	select {
	case <-secondContended:
	case <-time.After(5 * time.Second):
		t.Fatal("second recovery did not contend on the authenticated publication claim")
	}
	stillNamed, err := os.Lstat(tempPath)
	if err != nil {
		t.Fatalf("peer removed the first recovery's in-progress temp: %v", err)
	}
	if !os.SameFile(firstTempInfo, stillNamed) {
		t.Fatal("peer substituted the first recovery's in-progress temp inode")
	}
	close(releaseFirst)

	for index := 0; index < 2; index++ {
		select {
		case result := <-results:
			if result.err != nil || result.inspection.Operation.Phase != PhaseArchiveComplete || result.inspection.Custody != CustodyArchived {
				t.Fatalf("concurrent recovery %d = %#v, %v", index, result.inspection, result.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("concurrent recovery %d did not finish", index)
		}
	}
	if fixture.journal.eventCommits != 1 || fixture.journal.catalogCommits != 1 {
		t.Fatalf("concurrent projection counts catalog=%d event=%d", fixture.journal.catalogCommits, fixture.journal.eventCommits)
	}
	if _, err := os.Lstat(tempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest temp residue after convergence: %v", err)
	}
	publishedInfo, err := os.Lstat(fixture.paths.ArchiveManifest.AbsolutePath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(firstTempInfo, publishedInfo) {
		t.Fatal("archive.json was not atomically published from the guarded temp inode")
	}
	manifestPayload, err := os.ReadFile(fixture.paths.ArchiveManifest.AbsolutePath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest WorkspaceArchiveManifest
	if err := json.Unmarshal(manifestPayload, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.LifecycleEventID != deterministicArchiveEventID(plan.OperationID) {
		t.Fatalf("manifest event id = %q", manifest.LifecycleEventID)
	}
	later, err := fixture.service.RecoverArchive(context.Background(), plan.OperationID)
	if err != nil || later.Operation.Phase != PhaseArchiveComplete || later.Manifest == nil {
		t.Fatalf("later replay = %#v, %v", later, err)
	}
}

func TestWorkspaceArchiveAuthenticatedManifestReplayRequiresCanonicalBytes(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan := fixture.plan(t)
	fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryAfterManifest {
			return errors.New("stop after manifest")
		}
		return nil
	}
	if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil {
		t.Fatal("expected manifest stop")
	}
	payload, err := os.ReadFile(fixture.paths.ArchiveManifest.AbsolutePath)
	if err != nil {
		t.Fatal(err)
	}
	payload = append([]byte(" \n"), payload...)
	if err := os.WriteFile(fixture.paths.ArchiveManifest.AbsolutePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.service.FailureHook = nil
	if _, err := fixture.service.RecoverArchive(context.Background(), plan.OperationID); err == nil || !strings.Contains(err.Error(), "replay conflict") {
		t.Fatalf("non-canonical authenticated replay error = %v", err)
	}
	if fixture.journal.eventCommits != 0 || fixture.journal.catalogCommits != 0 {
		t.Fatal("non-canonical manifest projected durable state")
	}
}

func TestWorkspaceArchiveIntentCleanupRejectsMarkerAndContainerSubstitution(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*workspaceMoveFixture) (string, string)
	}{
		{
			name: "marker",
			mutate: func(f *workspaceMoveFixture) (string, string) {
				marker := filepath.Join(f.paths.ArchiveContainer.AbsolutePath, workspaceIntentMarkerName)
				original := marker + ".held-original"
				if err := os.Rename(marker, original); err != nil {
					t.Fatal(err)
				}
				payload, err := os.ReadFile(original)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(marker, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				return original, marker
			},
		},
		{
			name: "container",
			mutate: func(f *workspaceMoveFixture) (string, string) {
				container := f.paths.ArchiveContainer.AbsolutePath
				original := container + "-held-original"
				if err := os.Rename(container, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(container, 0o750); err != nil {
					t.Fatal(err)
				}
				payload, err := os.ReadFile(filepath.Join(original, workspaceIntentMarkerName))
				if err != nil {
					t.Fatal(err)
				}
				replacement := filepath.Join(container, workspaceIntentMarkerName)
				if err := os.WriteFile(replacement, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(original, workspaceIntentMarkerName), replacement
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkspaceMoveFixture(t)
			plan := fixture.plan(t)
			fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
				if boundary == BoundaryAfterProjections {
					return errors.New("stop after projections")
				}
				return nil
			}
			if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil {
				t.Fatal("expected projection stop")
			}
			var original, replacement string
			fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
				if boundary == BoundaryBeforeIntentCleanup {
					original, replacement = test.mutate(fixture)
				}
				return nil
			}
			if _, err := fixture.service.RecoverArchive(context.Background(), plan.OperationID); err == nil || !strings.Contains(err.Error(), "substituted") {
				t.Fatalf("cleanup substitution error = %v", err)
			}
			if _, err := os.Lstat(original); err != nil {
				t.Fatalf("held authenticated marker was unlinked: %v", err)
			}
			if _, err := os.Lstat(replacement); err != nil {
				t.Fatalf("same-named replacement was unlinked: %v", err)
			}
			if fixture.journal.record.Phase != string(PhaseArchiveProjectionsCommitted) {
				t.Fatalf("cleanup substitution changed journal phase: %#v", fixture.journal.record)
			}
		})
	}
}

func TestWorkspaceArchiveProjectionRejectsCatalogPhantomAfterPreRenameRecheck(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan := fixture.plan(t)
	fixture.journal.projectionCheck = func(storagecatalog.WorkspaceArchiveProjectionInput) error {
		current, err := fixture.service.planCatalogRebinds(context.Background(), fixture.paths.Active, fixture.paths.ArchivePayload)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, plan.CatalogRebinds) {
			return errors.New("workspace archive catalog evidence changed after review")
		}
		return nil
	}
	inserted := false
	fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary != BoundaryAfterCatalogRecheck || inserted {
			return nil
		}
		inserted = true
		entry := storagecatalog.Entry{
			StorageEntryID:     "storage_entry_01ARZ3NDEKTSV4RRFFQ69G5FAT",
			LogicalPath:        "Topics/topic-one/late.txt",
			OriginalSourcePath: filepath.Join(fixture.paths.Active.AbsolutePath, "late.txt"),
			CurrentViewPath:    "Topics/topic-one/late.txt",
		}
		ref := storagecatalog.PhysicalRef{
			StoragePhysicalRefID: "storage_physical_ref_01ARZ3NDEKTSV4RRFFQ69G5FAT",
			StorageEntryID:       entry.StorageEntryID,
			URI:                  entry.OriginalSourcePath,
			Status:               "available",
		}
		fixture.catalog.entries = append(fixture.catalog.entries, entry)
		fixture.catalog.details[entry.StorageEntryID] = storagecatalog.EntryDetail{Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}}
		return nil
	}
	if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "catalog evidence changed after review") {
		t.Fatalf("late catalog phantom projection error = %v", err)
	}
	if !inserted {
		t.Fatal("late catalog row was not inserted after the pre-rename recheck")
	}
	if fixture.journal.eventCommits != 0 || fixture.journal.catalogCommits != 0 || len(fixture.journal.record.ManifestJSON) != 0 || fixture.journal.record.Phase != string(PhaseBlocked) || fixture.journal.record.LastSafePhase != string(PhaseArchivePayloadMoved) {
		t.Fatalf("late catalog phantom leaked successful projection: %#v", fixture.journal)
	}
	if _, err := os.Stat(fixture.paths.Active.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("payload did not remain in truthful archive custody: %v", err)
	}
	if _, err := os.Stat(fixture.paths.ArchivePayload.AbsolutePath); err != nil {
		t.Fatalf("archived payload missing after refused projection: %v", err)
	}
}

func TestWorkspaceArchiveBlockedRecoveryResumesOnlyAfterExactEvidenceReturns(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan := fixture.plan(t)
	fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryAfterIntent {
			return errors.New("stop after intent")
		}
		return nil
	}
	if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil {
		t.Fatal("expected injected stop")
	}
	fixture.service.FailureHook = nil
	detail := fixture.catalog.details[moveTestEntryID]
	detail.PhysicalRefs[0].URI = filepath.Join(fixture.roots.BoxRoot, "changed", "payload.txt")
	fixture.catalog.details[moveTestEntryID] = detail
	if _, err := fixture.service.RecoverArchive(context.Background(), plan.OperationID); err == nil {
		t.Fatal("catalog drift after intent was accepted")
	}
	blocked, err := fixture.service.InspectArchive(context.Background(), plan.OperationID)
	if err != nil || blocked.Operation.Phase != PhaseBlocked || blocked.Operation.LastSafePhase != PhaseArchiveIntentCommitted || blocked.Custody != CustodyActive {
		t.Fatalf("blocked state = %#v, %v", blocked, err)
	}
	detail.PhysicalRefs[0].URI = filepath.Join(fixture.paths.Active.AbsolutePath, "payload.txt")
	fixture.catalog.details[moveTestEntryID] = detail
	complete, err := fixture.service.RecoverArchive(context.Background(), plan.OperationID)
	if err != nil || complete.Operation.Phase != PhaseArchiveComplete || complete.Custody != CustodyArchived {
		t.Fatalf("resolved blocked recovery = %#v, %v", complete, err)
	}
}

func TestWorkspaceArchiveParentReplacementAfterIntentIsBlocked(t *testing.T) {
	for _, destination := range []bool{false, true} {
		name := "source_parent"
		if destination {
			name = "destination_parent"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newWorkspaceMoveFixture(t)
			plan := fixture.plan(t)
			fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
				if boundary == BoundaryAfterIntent {
					return errors.New("stop after intent")
				}
				return nil
			}
			if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil {
				t.Fatal("expected injected stop")
			}
			fixture.service.FailureHook = nil
			if destination {
				parent := filepath.Join(fixture.roots.StorageRoot, "archive", "topics")
				if err := os.Rename(parent, parent+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0o750); err != nil {
					t.Fatal(err)
				}
			} else {
				parent := filepath.Join(fixture.roots.BoxRoot, "Topics")
				if err := os.Rename(parent, parent+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0o750); err != nil {
					t.Fatal(err)
				}
				copyWorkspaceFixture(t, filepath.Join(parent+"-old", "topic-one"), fixture.paths.Active.AbsolutePath)
			}
			if _, err := fixture.service.RecoverArchive(context.Background(), plan.OperationID); err == nil {
				t.Fatal("parent replacement after intent was accepted")
			}
			if fixture.journal.record.Phase != string(PhaseBlocked) || fixture.journal.record.LastSafePhase != string(PhaseArchiveIntentCommitted) {
				t.Fatalf("parent replacement state = %#v", fixture.journal.record)
			}
		})
	}
}

func TestWorkspaceArchiveReplayConflictIsPlanBound(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan := fixture.plan(t)
	fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryAfterIntent {
			return errors.New("stop after intent")
		}
		return nil
	}
	if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil {
		t.Fatal("expected injected stop")
	}
	fixture.service.FailureHook = nil
	conflict := plan
	conflict.Reason = "different reviewed reason"
	if err := SealWorkspaceArchivePlan(&conflict); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ApplyArchive(context.Background(), conflict, conflict.PlanDigest); err == nil || !strings.Contains(err.Error(), "replay_conflict") {
		t.Fatalf("replay conflict error = %v", err)
	}
	inspection, err := fixture.service.RecoverArchive(context.Background(), plan.OperationID)
	if err != nil || inspection.Operation.Phase != PhaseArchiveComplete {
		t.Fatalf("original plan recovery = %#v, %v", inspection, err)
	}
}

func TestWorkspaceArchiveSourceInodeSubstitutionFailsBeforeMutation(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan := fixture.plan(t)
	original := fixture.paths.Active.AbsolutePath + "-original"
	if err := os.Rename(fixture.paths.Active.AbsolutePath, original); err != nil {
		t.Fatal(err)
	}
	copyWorkspaceFixture(t, original, fixture.paths.Active.AbsolutePath)
	if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "source_drift") {
		t.Fatalf("same-content inode substitution error = %v", err)
	}
	if fixture.journal.exists {
		t.Fatal("source substitution committed durable intent")
	}
	if _, err := os.Stat(fixture.paths.ArchivePayload.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source substitution mutated destination: %v", err)
	}
}

func TestWorkspaceArchiveParentAndRootSubstitutionRefused(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*workspaceMoveFixture)
	}{
		{name: "source_parent", mutate: func(f *workspaceMoveFixture) {
			old := filepath.Join(f.roots.BoxRoot, "Topics-old")
			if err := os.Rename(filepath.Join(f.roots.BoxRoot, "Topics"), old); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(f.roots.BoxRoot, "Topics"), 0o750); err != nil {
				t.Fatal(err)
			}
			copyWorkspaceFixture(t, filepath.Join(old, "topic-one"), f.paths.Active.AbsolutePath)
		}},
		{name: "destination_parent", mutate: func(f *workspaceMoveFixture) {
			old := filepath.Join(f.roots.StorageRoot, "archive", "topics-old")
			if err := os.Rename(filepath.Join(f.roots.StorageRoot, "archive", "topics"), old); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(f.roots.StorageRoot, "archive", "topics"), 0o750); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "destination_parent_symlink", mutate: func(f *workspaceMoveFixture) {
			parent := filepath.Join(f.roots.StorageRoot, "archive", "topics")
			old := parent + "-old"
			if err := os.Rename(parent, old); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(old, parent); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "source_root", mutate: func(f *workspaceMoveFixture) {
			old := f.roots.BoxRoot + "-old"
			if err := os.Rename(f.roots.BoxRoot, old); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(f.roots.BoxRoot, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(f.roots.BoxRoot, "Topics"), 0o750); err != nil {
				t.Fatal(err)
			}
			copyWorkspaceFixture(t, filepath.Join(old, "Topics", "topic-one"), f.paths.Active.AbsolutePath)
		}},
		{name: "destination_root", mutate: func(f *workspaceMoveFixture) {
			old := f.roots.StorageRoot + "-old"
			if err := os.Rename(f.roots.StorageRoot, old); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(f.roots.StorageRoot, "archive", "topics"), 0o750); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkspaceMoveFixture(t)
			plan := fixture.plan(t)
			test.mutate(fixture)
			if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil {
				t.Fatal("substitution was accepted")
			}
			if fixture.journal.exists {
				t.Fatal("substitution committed durable intent")
			}
		})
	}
}

func TestWorkspaceArchiveDestinationCollisionAndDigestMismatchDoNotMutate(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan := fixture.plan(t)
	if _, err := fixture.service.ApplyArchive(context.Background(), plan, "sha256:"+strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "exact reviewed plan digest") {
		t.Fatalf("digest mismatch error = %v", err)
	}
	if fixture.journal.exists {
		t.Fatal("digest mismatch committed intent")
	}
	if err := os.MkdirAll(fixture.paths.ArchiveContainer.AbsolutePath, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil {
		t.Fatal("destination collision was accepted")
	}
	if fixture.journal.exists {
		t.Fatal("destination collision committed intent")
	}
}

func TestWorkspaceArchiveAuthenticationFailureOccursBeforeMutation(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan := fixture.plan(t)
	fixture.service.ManifestKey = func(context.Context, string) ([]byte, error) {
		return nil, errors.New("key unavailable")
	}
	if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "key unavailable") {
		t.Fatalf("authentication preflight error = %v", err)
	}
	if fixture.journal.exists {
		t.Fatal("authentication failure committed intent")
	}
	if _, err := os.Lstat(fixture.paths.ArchiveContainer.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authentication failure mutated destination: %v", err)
	}
	if _, err := os.Stat(fixture.paths.Active.AbsolutePath); err != nil {
		t.Fatalf("authentication failure moved source: %v", err)
	}
}

func TestWorkspaceArchivePlannedInspectionReportsDriftWithoutMutation(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan := fixture.plan(t)
	if err := os.WriteFile(filepath.Join(fixture.paths.Active.AbsolutePath, "late.txt"), []byte("late"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := fixture.service.InspectPlan(context.Background(), plan)
	if err != nil || status.Phase != PhaseBlocked || status.LastSafePhase != PhaseArchivePlanned || !hasFindingCode(status.Findings, FindingSourceDrift) {
		t.Fatalf("drifted planned inspection = %#v, %v", status, err)
	}
	if fixture.journal.exists {
		t.Fatal("planned drift inspection committed intent")
	}
}

func TestWorkspaceArchiveManifestHMACTamperRequiresManualRepair(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan := fixture.plan(t)
	fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryAfterManifest {
			return errors.New("stop after manifest")
		}
		return nil
	}
	if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil {
		t.Fatal("expected injected manifest stop")
	}
	manifestPath := fixture.paths.ArchiveManifest.AbsolutePath
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	document["reason"] = "tampered"
	payload, _ = json.Marshal(document)
	if err := os.WriteFile(manifestPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.service.FailureHook = nil
	if _, err := fixture.service.RecoverArchive(context.Background(), plan.OperationID); err == nil {
		t.Fatal("tampered HMAC manifest was accepted")
	}
	if fixture.journal.record.Phase != string(PhaseManualRepairRequired) || fixture.journal.record.LastSafePhase != string(PhaseArchivePayloadMoved) {
		t.Fatalf("tamper journal state = %#v", fixture.journal.record)
	}
	inspection, err := fixture.service.InspectArchive(context.Background(), plan.OperationID)
	if err != nil || inspection.Operation.Phase != PhaseManualRepairRequired || inspection.Custody != CustodyAmbiguous {
		t.Fatalf("manual-repair inspection = %#v, %v", inspection, err)
	}
}

func TestWorkspaceArchiveDestinationInodeSubstitutionRequiresManualRepair(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	plan := fixture.plan(t)
	fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryAfterPayloadMove {
			return errors.New("stop after payload move")
		}
		return nil
	}
	if _, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil {
		t.Fatal("expected injected payload stop")
	}
	original := filepath.Join(fixture.roots.StorageRoot, "original-moved-payload")
	if err := os.Rename(fixture.paths.ArchivePayload.AbsolutePath, original); err != nil {
		t.Fatal(err)
	}
	copyWorkspaceFixture(t, original, fixture.paths.ArchivePayload.AbsolutePath)
	fixture.service.FailureHook = nil
	if _, err := fixture.service.RecoverArchive(context.Background(), plan.OperationID); err == nil {
		t.Fatal("same-content destination inode substitution was accepted")
	}
	if fixture.journal.record.Phase != string(PhaseManualRepairRequired) || fixture.journal.record.LastSafePhase != string(PhaseArchiveIntentCommitted) {
		t.Fatalf("destination substitution state = %#v", fixture.journal.record)
	}
}

func TestWorkspaceArchiveRejectsSpecialFileBeforeIntent(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	if err := syscall.Mkfifo(filepath.Join(fixture.paths.Active.AbsolutePath, "named-pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.PlanArchive(context.Background(), fixture.planInput()); err == nil || !strings.Contains(err.Error(), "unsupported special-file") {
		t.Fatalf("special-file plan error = %v", err)
	}
	if fixture.journal.exists {
		t.Fatal("special-file rejection committed intent")
	}
}

func TestWorkspaceArchiveSparseLargeAndManyFilesUsesSameInodesAndBlocks(t *testing.T) {
	fixture := newWorkspaceMoveFixture(t)
	sparse := filepath.Join(fixture.paths.Active.AbsolutePath, "sparse.bin")
	file, err := os.OpenFile(sparse, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("tail"), (64<<20)-4); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	many := filepath.Join(fixture.paths.Active.AbsolutePath, "many")
	if err := os.Mkdir(many, 0o750); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 256; index++ {
		if err := os.WriteFile(filepath.Join(many, fmt.Sprintf("item-%03d", index)), []byte{byte(index)}, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	before := allocatedBlocksAndInodes(t, fixture.paths.Active.AbsolutePath)
	plan := fixture.plan(t)
	result, err := fixture.service.ApplyArchive(context.Background(), plan, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	after := allocatedBlocksAndInodes(t, fixture.paths.ArchivePayload.AbsolutePath)
	if result.Operation.Phase != PhaseArchiveComplete || !reflect.DeepEqual(before, after) {
		t.Fatalf("rename allocated a duplicate or changed inodes\nbefore=%#v\nafter=%#v", before, after)
	}
	if before.blocks >= (64<<20)/512 {
		t.Fatalf("sparse fixture unexpectedly allocated its logical size: %d blocks", before.blocks)
	}
}

type workspaceMoveFixture struct {
	t       *testing.T
	roots   TrustedWorkspaceRoots
	paths   ResolvedWorkspacePaths
	catalog *moveFakeCatalog
	journal *moveFakeJournal
	service WorkspaceMoveService
}

func newWorkspaceMoveFixture(t *testing.T) *workspaceMoveFixture {
	t.Helper()
	base := t.TempDir()
	roots := TrustedWorkspaceRoots{BoxRoot: filepath.Join(base, "box"), StorageRoot: filepath.Join(base, "storage")}
	for _, directory := range []string{
		filepath.Join(roots.BoxRoot, "Topics", "topic-one", "nested"),
		filepath.Join(roots.StorageRoot, "archive", "topics"),
	} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := ResolveWorkspacePaths(roots, WorkspaceKindTopic, "topic-one")
	if err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(paths.Active.AbsolutePath, "payload.txt")
	if err := os.WriteFile(payloadPath, []byte("workspace payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(1_800_000_000, 123456000)
	if err := os.Chtimes(payloadPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../payload.txt", filepath.Join(paths.Active.AbsolutePath, "nested", "payload-link")); err != nil {
		t.Fatal(err)
	}
	entry := storagecatalog.Entry{
		StorageEntryID:     moveTestEntryID,
		LogicalPath:        "Topics/topic-one/payload.txt",
		OriginalSourcePath: payloadPath,
		CurrentViewPath:    "Topics/topic-one/payload.txt",
	}
	ref := storagecatalog.PhysicalRef{StoragePhysicalRefID: moveTestRefID, StorageEntryID: moveTestEntryID, URI: payloadPath, Status: "available"}
	catalog := &moveFakeCatalog{entries: []storagecatalog.Entry{entry}, details: map[string]storagecatalog.EntryDetail{moveTestEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}}}}
	journal := newMoveFakeJournal(entry, ref)
	service := WorkspaceMoveService{
		Roots:         roots,
		Catalog:       catalog,
		Journal:       journal,
		ManifestKeyID: "workspace.archive.test",
		ManifestKey:   func(context.Context, string) ([]byte, error) { return []byte("0123456789abcdef0123456789abcdef"), nil },
		Now:           func() time.Time { return time.Unix(1_900_000_000, 0).UTC() },
	}
	return &workspaceMoveFixture{t: t, roots: roots, paths: paths, catalog: catalog, journal: journal, service: service}
}

func (f *workspaceMoveFixture) planInput() WorkspaceArchivePlanInput {
	return WorkspaceArchivePlanInput{OperationID: moveTestOperationID, Kind: WorkspaceKindTopic, ObjectID: "topic_object_one", Slug: "topic-one", ActorID: "actor_archive_test", Reason: "archive completed topic"}
}

func (f *workspaceMoveFixture) plan(t *testing.T) WorkspaceArchivePlan {
	t.Helper()
	plan, err := f.service.PlanArchive(context.Background(), f.planInput())
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

type moveFakeCatalog struct {
	entries []storagecatalog.Entry
	details map[string]storagecatalog.EntryDetail
}

func TestWorkspaceArchiveUTCJournalRepresentationRecovery(t *testing.T) {
	f := newWorkspaceMoveFixture(t)
	plan := f.plan(t)
	before := snapshotWorkspacePayload(t, f.paths.Active.AbsolutePath)
	f.service.FailureHook = func(b WorkspaceMoveBoundary) error {
		if b == BoundaryBeforeComplete {
			return errors.New("preserve published manifest before completion")
		}
		return nil
	}
	if _, err := f.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil {
		t.Fatal("expected completion interruption")
	}
	manifestPath := filepath.Join(f.paths.ArchiveContainer.AbsolutePath, "archive.json")
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	localUTC := time.FixedZone("pgx Local UTC", 0)
	moved := f.journal.record.PayloadMovedAt.In(localUTC)
	f.journal.record.PayloadMovedAt = &moved
	f.service.FailureHook = nil
	recovered, err := f.service.RecoverArchive(context.Background(), plan.OperationID)
	if err != nil || recovered.Operation.Phase != PhaseArchiveComplete {
		t.Fatalf("same serialized UTC journal time must recover: %v", err)
	}
	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil || !bytes.Equal(manifestBefore, manifestAfter) {
		t.Fatal("recovery rewrote the authenticated manifest")
	}
	restore := planRestore(t, f, plan.OperationID)
	if _, err := f.service.ApplyRestore(context.Background(), restore, restore.PlanDigest); err != nil {
		t.Fatal(err)
	}
	if got := snapshotWorkspacePayload(t, f.paths.Active.AbsolutePath); !reflect.DeepEqual(got, before) {
		t.Fatal("UTC journal round trip changed payload")
	}
}

func (c *moveFakeCatalog) ListWorkspaceArchiveCatalogEntries(context.Context, string, string) ([]storagecatalog.EntryDetail, error) {
	result := make([]storagecatalog.EntryDetail, 0, len(c.entries))
	for _, entry := range c.entries {
		detail, ok := c.details[entry.StorageEntryID]
		if !ok {
			return nil, fmt.Errorf("missing entry %s", entry.StorageEntryID)
		}
		result = append(result, detail)
	}
	return result, nil
}

type moveFakeJournal struct {
	mu                    sync.Mutex
	exists                bool
	record                storagecatalog.WorkspaceArchiveJournalRecord
	refURIs               map[string]string
	entryPaths            map[string][2]string
	catalogCommits        int
	eventCommits          int
	projectionCheck       func(storagecatalog.WorkspaceArchiveProjectionInput) error
	restoreRecords        map[string]storagecatalog.WorkspaceArchiveJournalRecord
	restoreIntentCommits  int
	restoreCatalogCommits int
	restoreEventCommits   int
}

func newMoveFakeJournal(entry storagecatalog.Entry, ref storagecatalog.PhysicalRef) *moveFakeJournal {
	return &moveFakeJournal{refURIs: map[string]string{ref.StoragePhysicalRefID: ref.URI}, entryPaths: map[string][2]string{entry.StorageEntryID: {entry.OriginalSourcePath, entry.CurrentViewPath}}, restoreRecords: map[string]storagecatalog.WorkspaceArchiveJournalRecord{}}
}

func (j *moveFakeJournal) LoadWorkspaceArchiveJournal(_ context.Context, operationID string) (storagecatalog.WorkspaceArchiveJournalRecord, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.exists || j.record.OperationID != operationID {
		record, ok := j.restoreRecords[operationID]
		return cloneMoveRecord(record), ok, nil
	}
	return cloneMoveRecord(j.record), true, nil
}

func (j *moveFakeJournal) CommitWorkspaceArchiveIntent(_ context.Context, input storagecatalog.WorkspaceArchiveIntentInput) (storagecatalog.WorkspaceArchiveJournalRecord, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.exists {
		if input.OperationID != j.record.OperationID {
			if record, ok := j.restoreRecords[input.OperationID]; ok {
				return cloneMoveRecord(record), false, nil
			}
			if input.OperationKind != string(WorkspaceOperationRestore) {
				return storagecatalog.WorkspaceArchiveJournalRecord{}, false, errors.New("unsupported second operation")
			}
			record := storagecatalog.WorkspaceArchiveJournalRecord{
				OperationID: input.OperationID, PlanDigest: input.PlanDigest, PlanJSON: append(json.RawMessage(nil), input.PlanJSON...),
				Phase: string(PhaseRestoreIntentCommitted), TerminalStatus: string(OperationStatusRunning),
				PlannedAt: input.PlannedAt, IntentCommittedAt: timePointer(input.IntentCommittedAt), UpdatedAt: input.IntentCommittedAt,
			}
			j.restoreRecords[input.OperationID] = record
			j.restoreIntentCommits++
			return cloneMoveRecord(record), true, nil
		}
		return cloneMoveRecord(j.record), false, nil
	}
	j.exists = true
	j.record = storagecatalog.WorkspaceArchiveJournalRecord{
		OperationID: input.OperationID, PlanDigest: input.PlanDigest, PlanJSON: append(json.RawMessage(nil), input.PlanJSON...),
		Phase: string(PhaseArchiveIntentCommitted), TerminalStatus: string(OperationStatusRunning),
		PlannedAt: input.PlannedAt, IntentCommittedAt: timePointer(input.IntentCommittedAt), UpdatedAt: input.IntentCommittedAt,
	}
	return cloneMoveRecord(j.record), true, nil
}

func (j *moveFakeJournal) MarkWorkspaceArchivePayloadMoved(_ context.Context, operationID, digest string, at time.Time) (storagecatalog.WorkspaceArchiveJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.exists || j.record.OperationID != operationID || j.record.PlanDigest != digest {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("journal conflict")
	}
	if j.record.Phase == string(PhaseArchiveIntentCommitted) || (j.record.Phase == string(PhaseBlocked) && j.record.LastSafePhase == string(PhaseArchiveIntentCommitted)) {
		j.record.Phase = string(PhaseArchivePayloadMoved)
		j.record.LastSafePhase = ""
		j.record.TerminalStatus = string(OperationStatusRunning)
		j.record.PayloadMovedAt = timePointer(at)
		j.record.UpdatedAt = at
	}
	return cloneMoveRecord(j.record), nil
}

func (j *moveFakeJournal) CommitWorkspaceArchiveProjection(_ context.Context, input storagecatalog.WorkspaceArchiveProjectionInput) (storagecatalog.WorkspaceArchiveJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.exists || j.record.OperationID != input.OperationID || j.record.PlanDigest != input.PlanDigest {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("journal conflict")
	}
	if j.projectionCheck != nil {
		if err := j.projectionCheck(input); err != nil {
			return storagecatalog.WorkspaceArchiveJournalRecord{}, err
		}
	}
	changed := false
	for _, item := range input.Rebind.PhysicalRefs {
		current := j.refURIs[item.StoragePhysicalRefID]
		if current == item.NewURI {
			continue
		}
		if current != item.ExpectedURI {
			return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("physical ref changed since review")
		}
		j.refURIs[item.StoragePhysicalRefID] = item.NewURI
		changed = true
	}
	for _, item := range input.Rebind.Entries {
		current := j.entryPaths[item.StorageEntryID]
		want := [2]string{item.NewOriginalSourcePath, item.NewCurrentViewPath}
		if current == want {
			continue
		}
		if current != [2]string{item.ExpectedOriginalSourcePath, item.ExpectedCurrentViewPath} {
			return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("entry paths changed since review")
		}
		j.entryPaths[item.StorageEntryID] = want
		changed = true
	}
	if len(j.record.ManifestJSON) == 0 {
		j.record.ManifestJSON = append(json.RawMessage(nil), input.ManifestJSON...)
		j.record.EventJSON = append(json.RawMessage(nil), input.EventJSON...)
		j.eventCommits++
	} else if !jsonDocumentsEqualForTest(j.record.ManifestJSON, input.ManifestJSON) || !jsonDocumentsEqualForTest(j.record.EventJSON, input.EventJSON) {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("projection replay conflict")
	}
	if changed {
		j.catalogCommits++
	}
	if j.record.Phase != string(PhaseArchiveComplete) {
		j.record.Phase = string(PhaseArchiveProjectionsCommitted)
		j.record.LastSafePhase = ""
		j.record.TerminalStatus = string(OperationStatusRunning)
		if j.record.ProjectionsCommittedAt == nil {
			j.record.ProjectionsCommittedAt = timePointer(input.CommittedAt)
		}
		j.record.UpdatedAt = input.CommittedAt
	}
	return cloneMoveRecord(j.record), nil
}

func (j *moveFakeJournal) CompleteWorkspaceArchive(_ context.Context, operationID, digest string, at time.Time) (storagecatalog.WorkspaceArchiveJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.exists || j.record.OperationID != operationID || j.record.PlanDigest != digest {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("journal conflict")
	}
	if j.record.Phase == string(PhaseArchiveProjectionsCommitted) || (j.record.Phase == string(PhaseBlocked) && j.record.LastSafePhase == string(PhaseArchiveProjectionsCommitted)) {
		j.record.Phase = string(PhaseArchiveComplete)
		j.record.LastSafePhase = ""
		j.record.TerminalStatus = string(OperationStatusComplete)
		j.record.CompletedAt = timePointer(at)
		j.record.UpdatedAt = at
	}
	return cloneMoveRecord(j.record), nil
}

func (j *moveFakeJournal) RecordWorkspaceArchiveFailure(_ context.Context, input storagecatalog.WorkspaceArchiveFailureInput) (storagecatalog.WorkspaceArchiveJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.exists || j.record.OperationID != input.OperationID || j.record.PlanDigest != input.PlanDigest {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("journal conflict")
	}
	if j.record.Phase != input.LastSafePhase && !((j.record.Phase == string(PhaseBlocked) || j.record.Phase == string(PhaseManualRepairRequired)) && j.record.LastSafePhase == input.LastSafePhase) {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("journal advanced beyond failure boundary")
	}
	j.record.Phase = input.Phase
	j.record.LastSafePhase = input.LastSafePhase
	j.record.TerminalStatus = input.Status
	j.record.UpdatedAt = input.RecordedAt
	for _, existing := range j.record.Findings {
		if existing.Code == input.FindingCode && existing.Summary == input.Summary {
			return cloneMoveRecord(j.record), nil
		}
	}
	j.record.Findings = append(j.record.Findings, storagecatalog.WorkspaceArchiveFindingRecord{
		FindingID: input.FindingID, Code: input.FindingCode, Severity: input.Severity,
		AtPhase: input.AtPhase, Summary: input.Summary, Repairable: input.Repairable,
		Evidence: append(json.RawMessage(nil), input.EvidenceJSON...), CreatedAt: input.RecordedAt,
	})
	return cloneMoveRecord(j.record), nil
}

func cloneMoveRecord(record storagecatalog.WorkspaceArchiveJournalRecord) storagecatalog.WorkspaceArchiveJournalRecord {
	record.PlanJSON = append(json.RawMessage(nil), record.PlanJSON...)
	record.ManifestJSON = append(json.RawMessage(nil), record.ManifestJSON...)
	record.EventJSON = append(json.RawMessage(nil), record.EventJSON...)
	record.Findings = append([]storagecatalog.WorkspaceArchiveFindingRecord(nil), record.Findings...)
	return record
}

type payloadSnapshotEntry struct {
	path    string
	mode    os.FileMode
	mtimeNS int64
	inode   uint64
	bytes   string
	target  string
}

func snapshotWorkspacePayload(t *testing.T, root string) []payloadSnapshotEntry {
	t.Helper()
	var result []payloadSnapshotEntry
	err := filepath.Walk(root, func(current string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, current)
		stat := info.Sys().(*syscall.Stat_t)
		entry := payloadSnapshotEntry{path: filepath.ToSlash(rel), mode: info.Mode(), mtimeNS: info.ModTime().UnixNano(), inode: uint64(stat.Ino)}
		if info.Mode().IsRegular() {
			payload, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			entry.bytes = string(payload)
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			entry.target = target
		}
		result = append(result, entry)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func copyWorkspaceFixture(t *testing.T, source, destination string) {
	t.Helper()
	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	err := filepath.Walk(source, func(current string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(source, current)
		if rel == "." {
			return nil
		}
		target := filepath.Join(destination, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(current)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			payload, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, payload, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chtimes(target, info.ModTime(), info.ModTime())
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

type blocksAndInodes struct {
	blocks int64
	inodes []uint64
}

func allocatedBlocksAndInodes(t *testing.T, root string) blocksAndInodes {
	t.Helper()
	result := blocksAndInodes{}
	if err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		stat := info.Sys().(*syscall.Stat_t)
		result.blocks += stat.Blocks
		result.inodes = append(result.inodes, uint64(stat.Ino))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func timePointer(value time.Time) *time.Time { return &value }

func jsonDocumentsEqualForTest(left, right []byte) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}

func expectedPartialState(boundary WorkspaceMoveBoundary) (WorkspaceArchivePhase, WorkspaceCustodyState) {
	switch boundary {
	case BoundaryAfterIntent, BoundaryBeforePayloadMove:
		return PhaseArchiveIntentCommitted, CustodyActive
	case BoundaryAfterPayloadMove, BoundaryBeforeMovedRecord:
		return PhaseArchiveIntentCommitted, CustodyArchived
	case BoundaryAfterMovedRecord, BoundaryBeforeManifest, BoundaryAfterManifestTemp, BoundaryBeforeManifestPublish, BoundaryAfterManifest, BoundaryBeforeProjections:
		return PhaseArchivePayloadMoved, CustodyArchived
	case BoundaryAfterProjections, BoundaryBeforeIntentCleanup, BoundaryBeforeComplete:
		return PhaseArchiveProjectionsCommitted, CustodyArchived
	case BoundaryAfterComplete:
		return PhaseArchiveComplete, CustodyArchived
	default:
		return PhaseArchivePlanned, CustodyActive
	}
}
