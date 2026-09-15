package storagearchive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

const restoreTestOperationID = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAY"

func TestWorkspaceRestoreUTCJournalRepresentationRecovery(t *testing.T) {
	f, archive, before := archivedRestoreFixture(t)
	plan := planRestore(t, f, archive.OperationID)
	f.service.FailureHook = func(b WorkspaceMoveBoundary) error {
		if b == BoundaryBeforeRestoreComplete {
			return errors.New("preserve restored manifest before completion")
		}
		return nil
	}
	if _, err := f.service.ApplyRestore(context.Background(), plan, plan.PlanDigest); err == nil {
		t.Fatal("expected restore completion interruption")
	}
	manifestPath := filepath.Join(f.paths.ArchiveContainer.AbsolutePath, "archive.json")
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	record := f.journal.restoreRecords[plan.OperationID]
	moved := record.PayloadMovedAt.In(time.FixedZone("pgx Local UTC", 0))
	record.PayloadMovedAt = &moved
	f.journal.restoreRecords[plan.OperationID] = record
	f.service.FailureHook = nil
	result, err := f.service.RecoverOperation(context.Background(), plan.OperationID)
	if err != nil || result.Operation.Phase != PhaseRestoreComplete || result.ActivationState != WorkspaceActivationInactive {
		t.Fatalf("same serialized UTC restore time must recover inactive: %v", err)
	}
	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil || !bytes.Equal(manifestBefore, manifestAfter) {
		t.Fatal("restore recovery rewrote authenticated bytes")
	}
	if got := snapshotWorkspacePayload(t, f.paths.Active.AbsolutePath); !reflect.DeepEqual(got, before) {
		t.Fatal("UTC restore recovery changed payload")
	}
}

func TestWorkspaceManifestEqualityPreservesEverySerializedField(t *testing.T) {
	f, archive, _ := archivedRestoreFixture(t)
	plan := planRestore(t, f, archive.OperationID)
	result, err := f.service.ApplyRestore(context.Background(), plan, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	original := *result.Manifest
	other := original
	other.ArchivedAt = original.ArchivedAt.In(time.FixedZone("same zero offset", 0))
	at := original.RestoredAt.In(time.FixedZone("another zero offset", 0))
	other.RestoredAt = &at
	if reflect.DeepEqual(original, other) || !sameWorkspaceManifest(original, other) {
		t.Fatal("equal serialized time representations were not distinguished from structural equality")
	}
	payload, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	for name := range fields {
		t.Run(name, func(t *testing.T) {
			var changed map[string]json.RawMessage
			if err := json.Unmarshal(payload, &changed); err != nil {
				t.Fatal(err)
			}
			delete(changed, name)
			raw, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			var candidate WorkspaceArchiveManifest
			if err := json.Unmarshal(raw, &candidate); err != nil {
				t.Fatal(err)
			}
			if sameWorkspaceManifest(original, candidate) {
				t.Fatal("serialized field was ignored")
			}
		})
	}
	for _, delta := range []time.Duration{time.Nanosecond, time.Microsecond} {
		other = original
		other.ArchivedAt = original.ArchivedAt.Add(delta)
		if sameWorkspaceManifest(original, other) {
			t.Fatal("timestamp drift was tolerated")
		}
	}
	other = original
	other.ArchivedAt = original.ArchivedAt.In(time.FixedZone("different wire offset", 3600))
	if sameWorkspaceManifest(original, other) {
		t.Fatal("different authenticated timestamp spelling was accepted")
	}
	other = original
	other.ArchivedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if sameWorkspaceManifest(other, other) {
		t.Fatal("unserializable evidence was accepted")
	}
}

func TestWorkspaceRestoreNoOverwritePreservesPayloadAndRemainsInactive(t *testing.T) {
	fixture, archivePlan, before := archivedRestoreFixture(t)
	restorePlan := planRestore(t, fixture, archivePlan.OperationID)
	result, err := fixture.service.ApplyRestore(context.Background(), restorePlan, restorePlan.PlanDigest)
	if err != nil {
		t.Fatalf("ApplyRestore: %v", err)
	}
	if result.Operation.Phase != PhaseRestoreComplete || result.Custody != CustodyActive || result.ActivationState != WorkspaceActivationInactive || result.Manifest == nil || result.Manifest.LifecycleState != WorkspaceLifecycleActive {
		t.Fatalf("restore result = %#v", result)
	}
	if _, err := os.Lstat(fixture.paths.ArchivePayload.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archived payload remains after restore: %v", err)
	}
	if after := snapshotWorkspacePayload(t, fixture.paths.Active.AbsolutePath); !reflect.DeepEqual(before, after) {
		t.Fatalf("payload fidelity changed\nbefore=%#v\nafter=%#v", before, after)
	}
	if got := fixture.journal.refURIs[moveTestRefID]; got != filepath.Join(fixture.paths.Active.AbsolutePath, "payload.txt") {
		t.Fatalf("restored physical ref = %q", got)
	}
	if fixture.journal.restoreCatalogCommits != 1 || fixture.journal.restoreEventCommits != 1 {
		t.Fatalf("restore projections catalog=%d event=%d", fixture.journal.restoreCatalogCommits, fixture.journal.restoreEventCommits)
	}
	replayed, err := fixture.service.ApplyRestore(context.Background(), restorePlan, restorePlan.PlanDigest)
	if err != nil || replayed.Operation.Phase != PhaseRestoreComplete || fixture.journal.restoreCatalogCommits != 1 || fixture.journal.restoreEventCommits != 1 {
		t.Fatalf("idempotent restore replay = %#v, %v", replayed, err)
	}
}

func TestWorkspaceArchiveHistoryRemainsInspectableAfterRestore(t *testing.T) {
	fixture, archivePlan, _ := archivedRestoreFixture(t)
	restorePlan := planRestore(t, fixture, archivePlan.OperationID)
	result, err := fixture.service.ApplyRestore(context.Background(), restorePlan, restorePlan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	// The production catalog returns the current manifest row for both the
	// archive operation and its linked restore operation.
	fixture.journal.record.ManifestJSON = append(json.RawMessage(nil), fixture.journal.restoreRecords[restorePlan.OperationID].ManifestJSON...)
	history, err := fixture.service.InspectOperation(context.Background(), archivePlan.OperationID)
	if err != nil {
		t.Fatalf("InspectOperation archive history: %v", err)
	}
	if history.Operation.OperationID != archivePlan.OperationID || history.Operation.Phase != PhaseArchiveComplete || history.Custody != CustodyActive || history.ActivationState != WorkspaceActivationInactive || !reflect.DeepEqual(history.Manifest, result.Manifest) {
		t.Fatalf("archive history = %#v", history)
	}
}

func TestWorkspaceRestoreRefusesExistingDestinationWithoutMutation(t *testing.T) {
	fixture, archivePlan, before := archivedRestoreFixture(t)
	if err := os.MkdirAll(fixture.paths.Active.AbsolutePath, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.PlanRestore(context.Background(), WorkspaceRestorePlanInput{
		OperationID: restoreTestOperationID, ArchiveOperationID: archivePlan.OperationID,
		ActorID: "actor_archive_test", Reason: "restore safely",
	}); err == nil {
		t.Fatal("restore plan accepted an existing active destination")
	}
	if after := snapshotWorkspacePayload(t, fixture.paths.ArchivePayload.AbsolutePath); !reflect.DeepEqual(before, after) {
		t.Fatal("failed restore plan changed archived payload")
	}
	if fixture.journal.restoreIntentCommits != 0 {
		t.Fatal("failed restore plan committed intent")
	}
}

func TestWorkspaceRestoreNoOverwriteSurvivesDestinationRace(t *testing.T) {
	fixture, archivePlan, before := archivedRestoreFixture(t)
	plan := planRestore(t, fixture, archivePlan.OperationID)
	fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryBeforeRestorePayloadMove {
			return os.MkdirAll(fixture.paths.Active.AbsolutePath, 0o750)
		}
		return nil
	}
	if _, err := fixture.service.ApplyRestore(context.Background(), plan, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "refusing overwrite") {
		t.Fatalf("destination race error=%v", err)
	}
	if after := snapshotWorkspacePayload(t, fixture.paths.ArchivePayload.AbsolutePath); !reflect.DeepEqual(before, after) {
		t.Fatal("destination race changed archived payload")
	}
}

func TestWorkspaceRestoreRecoversEveryDurableBoundary(t *testing.T) {
	boundaries := []WorkspaceMoveBoundary{
		BoundaryAfterRestoreIntent,
		BoundaryBeforeRestorePayloadMove,
		BoundaryAfterRestoreCatalogRecheck,
		BoundaryAfterRestorePayloadMove,
		BoundaryBeforeRestoreMovedRecord,
		BoundaryAfterRestoreMovedRecord,
		BoundaryBeforeRestoredManifest,
		BoundaryAfterRestoredManifestTemp,
		BoundaryBeforeRestoredManifestCommit,
		BoundaryAfterRestoredManifest,
		BoundaryBeforeRestoreProjections,
		BoundaryAfterRestoreProjections,
		BoundaryBeforeRestoreIntentCleanup,
		BoundaryBeforeRestoreComplete,
		BoundaryAfterRestoreComplete,
	}
	for _, boundary := range boundaries {
		t.Run(string(boundary), func(t *testing.T) {
			fixture, archivePlan, before := archivedRestoreFixture(t)
			plan := planRestore(t, fixture, archivePlan.OperationID)
			injected := errors.New("injected restore interruption")
			fired := false
			fixture.service.FailureHook = func(observed WorkspaceMoveBoundary) error {
				if !fired && observed == boundary {
					fired = true
					return injected
				}
				return nil
			}
			if _, err := fixture.service.ApplyRestore(context.Background(), plan, plan.PlanDigest); !errors.Is(err, injected) {
				t.Fatalf("ApplyRestore error = %v", err)
			}
			fixture.service.FailureHook = nil
			result, err := fixture.service.ApplyRestore(context.Background(), plan, plan.PlanDigest)
			if err != nil {
				t.Fatalf("recover by replay: %v", err)
			}
			if result.Operation.Phase != PhaseRestoreComplete || result.Custody != CustodyActive || result.ActivationState != WorkspaceActivationInactive {
				t.Fatalf("recovered result = %#v", result)
			}
			if after := snapshotWorkspacePayload(t, fixture.paths.Active.AbsolutePath); !reflect.DeepEqual(before, after) {
				t.Fatal("recovery changed payload")
			}
		})
	}
}

func TestWorkspaceRestoreConcurrentRecoverySerializesAuthenticatedClaim(t *testing.T) {
	fixture, archivePlan, _ := archivedRestoreFixture(t)
	plan := planRestore(t, fixture, archivePlan.OperationID)
	fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryAfterRestoreMovedRecord {
			return errors.New("stop after durable restore payload-moved record")
		}
		return nil
	}
	if _, err := fixture.service.ApplyRestore(context.Background(), plan, plan.PlanDigest); err == nil {
		t.Fatal("expected payload-moved stop")
	}

	firstReady := make(chan struct{})
	releaseFirst := make(chan struct{})
	first := fixture.service
	first.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryAfterRestoredManifestTemp {
			close(firstReady)
			<-releaseFirst
		}
		return nil
	}
	secondContended := make(chan struct{})
	var contendedOnce sync.Once
	second := fixture.service
	second.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryRestoreClaimContended {
			contendedOnce.Do(func() { close(secondContended) })
		}
		return nil
	}
	type result struct {
		inspection WorkspaceArchiveInspection
		err        error
	}
	results := make(chan result, 2)
	go func() {
		inspection, err := first.ApplyRestore(context.Background(), plan, plan.PlanDigest)
		results <- result{inspection, err}
	}()
	select {
	case <-firstReady:
	case <-time.After(5 * time.Second):
		t.Fatal("first restore recovery did not pause")
	}
	go func() {
		inspection, err := second.ApplyRestore(context.Background(), plan, plan.PlanDigest)
		results <- result{inspection, err}
	}()
	select {
	case <-secondContended:
	case <-time.After(5 * time.Second):
		t.Fatal("second restore recovery did not contend")
	}
	close(releaseFirst)
	for index := 0; index < 2; index++ {
		select {
		case got := <-results:
			if got.err != nil || got.inspection.Operation.Phase != PhaseRestoreComplete || got.inspection.Custody != CustodyActive {
				t.Fatalf("concurrent recovery %d = %#v, %v", index, got.inspection, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("concurrent recovery %d did not finish", index)
		}
	}
	if fixture.journal.restoreCatalogCommits != 1 || fixture.journal.restoreEventCommits != 1 {
		t.Fatalf("concurrent restore projections catalog=%d event=%d", fixture.journal.restoreCatalogCommits, fixture.journal.restoreEventCommits)
	}
}

func TestWorkspaceRestoreRejectsDigestAndManifestSubstitution(t *testing.T) {
	t.Run("digest", func(t *testing.T) {
		fixture, archivePlan, _ := archivedRestoreFixture(t)
		plan := planRestore(t, fixture, archivePlan.OperationID)
		if _, err := fixture.service.ApplyRestore(context.Background(), plan, "sha256:"+string(make([]byte, 64))); err == nil {
			t.Fatal("restore accepted substituted digest")
		}
	})
	t.Run("manifest", func(t *testing.T) {
		fixture, archivePlan, _ := archivedRestoreFixture(t)
		plan := planRestore(t, fixture, archivePlan.OperationID)
		manifestPath := filepath.Join(fixture.paths.ArchiveContainer.AbsolutePath, "archive.json")
		payload, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		var manifest WorkspaceArchiveManifest
		if err := json.Unmarshal(payload, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.Reason = "substituted"
		payload, _ = json.Marshal(manifest)
		if err := os.WriteFile(manifestPath, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.ApplyRestore(context.Background(), plan, plan.PlanDigest); err == nil {
			t.Fatal("restore accepted substituted manifest")
		}
		if _, err := os.Lstat(fixture.paths.Active.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("manifest substitution moved payload: %v", err)
		}
	})
	t.Run("durable manifest", func(t *testing.T) {
		fixture, archivePlan, _ := archivedRestoreFixture(t)
		fixture.journal.record.ManifestJSON = json.RawMessage(`{"schema_version":"substituted"}`)
		if _, err := fixture.service.PlanRestore(context.Background(), WorkspaceRestorePlanInput{
			OperationID: restoreTestOperationID, ArchiveOperationID: archivePlan.OperationID,
			ActorID: "actor_archive_test", Reason: "restore safely",
		}); err == nil {
			t.Fatal("restore accepted substituted durable manifest")
		}
		if _, err := os.Lstat(fixture.paths.Active.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("durable manifest substitution moved payload: %v", err)
		}
	})
}

func TestWorkspaceRestoreRejectsLockedIntentMarkerSubstitution(t *testing.T) {
	fixture, archivePlan, _ := archivedRestoreFixture(t)
	plan := planRestore(t, fixture, archivePlan.OperationID)
	var original, replacement string
	fixture.service.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary != BoundaryBeforeRestoreIntentCleanup || original != "" {
			return nil
		}
		marker := filepath.Join(fixture.paths.ArchiveContainer.AbsolutePath, workspaceRestoreIntentMarkerName)
		original = marker + ".held-original"
		replacement = marker
		if err := os.Rename(marker, original); err != nil {
			return err
		}
		payload, err := os.ReadFile(original)
		if err != nil {
			return err
		}
		return os.WriteFile(replacement, payload, 0o600)
	}
	if _, err := fixture.service.ApplyRestore(context.Background(), plan, plan.PlanDigest); err == nil || !strings.Contains(err.Error(), "substituted") {
		t.Fatalf("marker substitution error=%v", err)
	}
	if original == "" || replacement == "" {
		t.Fatal("substitution hook did not run")
	}
}

func TestWorkspaceSurfaceReviewAndInspectionAreRedacted(t *testing.T) {
	fixture, archivePlan, _ := archivedRestoreFixture(t)
	plan := planRestore(t, fixture, archivePlan.OperationID)
	review, err := NewWorkspaceMovePlanReview(plan)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(review)
	if bytesContainAny(payload, []byte(fixture.roots.BoxRoot), []byte(fixture.roots.StorageRoot), []byte("payload.txt")) {
		t.Fatalf("review exposed absolute or per-entry path: %s", payload)
	}
	if _, err := fixture.service.ApplyRestore(context.Background(), plan, plan.PlanDigest); err != nil {
		t.Fatal(err)
	}
	inspection, err := fixture.service.InspectOperation(context.Background(), plan.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	inspection.Operation.Findings = append(inspection.Operation.Findings, WorkspaceArchiveFinding{
		Code: FindingSourceDrift, Severity: FindingSeverityError, AtPhase: PhaseRestoreComplete,
		Summary: "unexpected path under " + fixture.roots.BoxRoot, Repairable: true,
	})
	summary := NewWorkspaceMoveInspectionSummary(inspection)
	payload, _ = json.Marshal(summary)
	if len(payload) > MaximumWorkspaceSurfaceResponseBytes || bytesContainAny(payload, []byte(fixture.roots.BoxRoot), []byte(fixture.roots.StorageRoot), []byte("authentication")) {
		t.Fatalf("inspection was not bounded/redacted: %s", payload)
	}
}

func TestWorkspaceReviewedPlanReplanningRejectsSurfaceFieldSubstitution(t *testing.T) {
	fixture, archivePlan, _ := archivedRestoreFixture(t)
	plan := planRestore(t, fixture, archivePlan.OperationID)
	review, err := NewWorkspaceMovePlanReview(plan)
	if err != nil {
		t.Fatal(err)
	}
	if replanned, err := fixture.service.ReplanReviewed(context.Background(), review); err != nil || replanned.PlanDigest != plan.PlanDigest {
		t.Fatalf("exact replan=%#v err=%v", replanned, err)
	}
	review.Source.RelativePath = "archive/topics/substituted/workspace"
	if _, err := fixture.service.ReplanReviewed(context.Background(), review); err == nil {
		t.Fatal("accepted substituted review surface with another plan's digest")
	}
}

func bytesContainAny(payload []byte, values ...[]byte) bool {
	for _, value := range values {
		if len(value) > 0 && bytes.Contains(payload, value) {
			return true
		}
	}
	return false
}

func archivedRestoreFixture(t *testing.T) (*workspaceMoveFixture, WorkspaceArchivePlan, []payloadSnapshotEntry) {
	t.Helper()
	fixture := newWorkspaceMoveFixture(t)
	before := snapshotWorkspacePayload(t, fixture.paths.Active.AbsolutePath)
	archivePlan := fixture.plan(t)
	if _, err := fixture.service.ApplyArchive(context.Background(), archivePlan, archivePlan.PlanDigest); err != nil {
		t.Fatalf("archive fixture: %v", err)
	}
	archivedPayload := fixture.paths.ArchivePayload.AbsolutePath
	detail := fixture.catalog.details[moveTestEntryID]
	detail.Entry.OriginalSourcePath = filepath.Join(archivedPayload, "payload.txt")
	detail.Entry.CurrentViewPath = filepath.ToSlash(filepath.Join(fixture.paths.ArchivePayload.RelativePath, "payload.txt"))
	detail.PhysicalRefs[0].URI = filepath.Join(archivedPayload, "payload.txt")
	fixture.catalog.entries[0] = detail.Entry
	fixture.catalog.details[moveTestEntryID] = detail
	return fixture, archivePlan, before
}

func planRestore(t *testing.T, fixture *workspaceMoveFixture, archiveOperationID string) WorkspaceArchivePlan {
	t.Helper()
	plan, err := fixture.service.PlanRestore(context.Background(), WorkspaceRestorePlanInput{
		OperationID: restoreTestOperationID, ArchiveOperationID: archiveOperationID,
		ActorID: "actor_archive_test", Reason: "restore safely",
	})
	if err != nil {
		t.Fatalf("PlanRestore: %v", err)
	}
	return plan
}

func (j *moveFakeJournal) MarkWorkspaceRestorePayloadMoved(_ context.Context, operationID, digest string, at time.Time) (storagecatalog.WorkspaceArchiveJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.restoreRecords[operationID]
	if !ok || record.PlanDigest != digest {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("restore journal conflict")
	}
	if record.Phase == string(PhaseRestoreIntentCommitted) || (record.Phase == string(PhaseBlocked) && record.LastSafePhase == string(PhaseRestoreIntentCommitted)) {
		record.Phase, record.LastSafePhase, record.TerminalStatus = string(PhaseRestorePayloadMoved), "", string(OperationStatusRunning)
		record.PayloadMovedAt, record.UpdatedAt = timePointer(at), at
		j.restoreRecords[operationID] = record
	}
	return cloneMoveRecord(record), nil
}

func (j *moveFakeJournal) CommitWorkspaceRestoreProjection(_ context.Context, input storagecatalog.WorkspaceRestoreProjectionInput) (storagecatalog.WorkspaceArchiveJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.restoreRecords[input.OperationID]
	if !ok || record.PlanDigest != input.PlanDigest {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("restore journal conflict")
	}
	changed := false
	for _, item := range input.Rebind.PhysicalRefs {
		current := j.refURIs[item.StoragePhysicalRefID]
		if current == item.NewURI {
			continue
		}
		if current != item.ExpectedURI {
			return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("restore physical ref changed since review")
		}
		j.refURIs[item.StoragePhysicalRefID], changed = item.NewURI, true
	}
	for _, item := range input.Rebind.Entries {
		current := j.entryPaths[item.StorageEntryID]
		want := [2]string{item.NewOriginalSourcePath, item.NewCurrentViewPath}
		if current == want {
			continue
		}
		if current != [2]string{item.ExpectedOriginalSourcePath, item.ExpectedCurrentViewPath} {
			return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("restore entry changed since review")
		}
		j.entryPaths[item.StorageEntryID], changed = want, true
	}
	if len(record.ManifestJSON) == 0 {
		record.ManifestJSON = append(json.RawMessage(nil), input.ManifestJSON...)
		record.EventJSON = append(json.RawMessage(nil), input.EventJSON...)
		j.restoreEventCommits++
	} else if !jsonDocumentsEqualForTest(record.ManifestJSON, input.ManifestJSON) || !jsonDocumentsEqualForTest(record.EventJSON, input.EventJSON) {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("restore projection replay conflict")
	}
	if changed {
		j.restoreCatalogCommits++
	}
	if record.Phase != string(PhaseRestoreComplete) {
		record.Phase, record.LastSafePhase, record.TerminalStatus = string(PhaseRestoreProjectionsCommitted), "", string(OperationStatusRunning)
		if record.ProjectionsCommittedAt == nil {
			record.ProjectionsCommittedAt = timePointer(input.CommittedAt)
		}
		record.UpdatedAt = input.CommittedAt
	}
	j.restoreRecords[input.OperationID] = record
	return cloneMoveRecord(record), nil
}

func (j *moveFakeJournal) CompleteWorkspaceRestore(_ context.Context, operationID, digest string, at time.Time) (storagecatalog.WorkspaceArchiveJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.restoreRecords[operationID]
	if !ok || record.PlanDigest != digest {
		return storagecatalog.WorkspaceArchiveJournalRecord{}, errors.New("restore journal conflict")
	}
	if record.Phase == string(PhaseRestoreProjectionsCommitted) || (record.Phase == string(PhaseBlocked) && record.LastSafePhase == string(PhaseRestoreProjectionsCommitted)) {
		record.Phase, record.LastSafePhase, record.TerminalStatus = string(PhaseRestoreComplete), "", string(OperationStatusComplete)
		record.CompletedAt, record.UpdatedAt = timePointer(at), at
		j.restoreRecords[operationID] = record
	}
	return cloneMoveRecord(record), nil
}
