package storagearchive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceObjectCanonicalProjectIdentity(t *testing.T) {
	for _, test := range []struct {
		id          string
		projectOnly bool
		valid       bool
	}{
		{"project_01ARZ3NDEKTSV4RRFFQ69G5FAV", true, true},
		{"project_7ZZZZZZZZZZZZZZZZZZZZZZZZZ", true, true},
		{"project_81ARZ3NDEKTSV4RRFFQ69G5FAV", true, false},
		{"project_01ARZ3NDEKTSV4RRFFQ69G5FAI", true, false},
		{"project_01ARZ3NDEKTSV4RRFFQ69G5FA", true, false},
		{"repo_01ARZ3NDEKTSV4RRFFQ69G5FAV", true, false},
		{"PROJECT_01ARZ3NDEKTSV4RRFFQ69G5FAV", true, false},
		{"project_Legacy", true, false},
		{"legacy_object_1", false, true},
	} {
		for _, kind := range []WorkspaceKind{WorkspaceKindProject, WorkspaceKindTopic, WorkspaceKindLibraryItem} {
			t.Run(string(kind)+"/"+test.id, func(t *testing.T) {
				want := test.valid && (!test.projectOnly || kind == WorkspaceKindProject)
				if got := validateWorkspaceObject(kind, test.id, "fixture-project") == nil; got != want {
					t.Fatalf("accepted=%t, want %t", got, want)
				}
			})
		}
	}
	if validWorkspaceActorID("project_01ARZ3NDEKTSV4RRFFQ69G5FAV") {
		t.Fatal("project identity broadened actor validation")
	}
}

const (
	testWorkspaceOperationID = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	testWorkspaceRestoreID   = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	testWorkspaceEventID     = "workspace_lifecycle_event_01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

func TestWorkspaceMappingIsClosed(t *testing.T) {
	tests := []struct {
		kind      WorkspaceKind
		active    string
		container string
		payload   string
	}{
		{WorkspaceKindTopic, "Topics/topic-one", "archive/topics/topic-one", "archive/topics/topic-one/content"},
		{WorkspaceKindProject, "Projects/topic-one", "archive/projects/topic-one", "archive/projects/topic-one/project"},
		{WorkspaceKindLibraryItem, "Library/topic-one", "archive/library/topic-one", "archive/library/topic-one/content"},
	}
	for _, test := range tests {
		mapping, err := WorkspaceMapping(test.kind, "topic-one")
		if err != nil {
			t.Fatalf("WorkspaceMapping(%q): %v", test.kind, err)
		}
		if mapping.ActiveRoot != WorkspaceRootBox || mapping.ArchiveRoot != WorkspaceRootStorage || mapping.ActiveRelativePath != test.active || mapping.ArchiveContainerPath != test.container || mapping.ArchivePayloadPath != test.payload || mapping.ArchiveManifestPath != test.container+"/archive.json" {
			t.Fatalf("WorkspaceMapping(%q) = %#v", test.kind, mapping)
		}
	}

	for _, invalid := range []struct {
		kind WorkspaceKind
		slug string
	}{
		{"manual", "topic-one"},
		{WorkspaceKindTopic, "../escape"},
		{WorkspaceKindProject, "Topic-One"},
		{WorkspaceKindLibraryItem, "ab"},
		{WorkspaceKindTopic, "topic_one"},
	} {
		if _, err := WorkspaceMapping(invalid.kind, invalid.slug); err == nil {
			t.Fatalf("WorkspaceMapping(%q, %q) accepted an open kind or unsafe slug", invalid.kind, invalid.slug)
		}
	}
}

func TestWorkspaceActorIDAcceptsAuthenticatedTypedIdentity(t *testing.T) {
	if !validWorkspaceActorID("actor_01ARZ3NDEKTSV4RRFFQ69G5FAV") {
		t.Fatal("typed bootstrap actor ID was rejected")
	}
	if !validWorkspaceActorID("actor_workspace_test") {
		t.Fatal("bounded compatibility actor ID was rejected")
	}
	for _, unsafe := range []string{"", "actor_../../secret", "ACTOR_01ARZ3NDEKTSV4RRFFQ69G5FAV", "actor_81ARZ3NDEKTSV4RRFFQ69G5FAV"} {
		if validWorkspaceActorID(unsafe) {
			t.Fatalf("unsafe actor ID accepted: %q", unsafe)
		}
	}
}

func TestWorkspaceTimestampUsesPostgresExactPrecision(t *testing.T) {
	input := time.Date(2026, 9, 2, 12, 34, 56, 123456789, time.FixedZone("test", 3600))
	got := normalizeWorkspaceTimestamp(input)
	if got.Location() != time.UTC || got.Nanosecond() != 123456000 {
		t.Fatalf("normalized workspace timestamp = %s", got.Format(time.RFC3339Nano))
	}
	if got != got.Truncate(time.Microsecond) {
		t.Fatal("workspace timestamp exceeds PostgreSQL microsecond precision")
	}
}

func TestResolveWorkspacePathsRejectsUntrustedRootShape(t *testing.T) {
	roots := newWorkspaceRoots(t)
	outside := t.TempDir()
	link := filepath.Join(t.TempDir(), "box-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWorkspacePaths(TrustedWorkspaceRoots{BoxRoot: link, StorageRoot: roots.StorageRoot}, WorkspaceKindTopic, "topic-one"); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlink root error = %v", err)
	}
	if _, err := ResolveWorkspacePaths(TrustedWorkspaceRoots{BoxRoot: "relative", StorageRoot: roots.StorageRoot}, WorkspaceKindTopic, "topic-one"); err == nil || !strings.Contains(err.Error(), "absolute and clean") {
		t.Fatalf("relative root error = %v", err)
	}
}

func TestInventoryMappedSourceNoFollow(t *testing.T) {
	roots := newWorkspaceRoots(t)
	source := filepath.Join(roots.BoxRoot, "Topics", "topic-one")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload.txt"), []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../payload.txt", filepath.Join(source, "nested", "payload-link")); err != nil {
		t.Fatal(err)
	}

	inventory, err := InventoryMappedSource(context.Background(), roots, WorkspaceOperationArchive, WorkspaceKindTopic, "topic-one")
	if err != nil {
		t.Fatalf("InventoryMappedSource: %v", err)
	}
	if inventory.EntryCount != 3 || inventory.FileCount != 1 || inventory.DirectoryCount != 1 || inventory.SymlinkCount != 1 || inventory.TotalBytes != int64(len("payload")) {
		t.Fatalf("inventory summary = %#v", inventory)
	}
	if err := ValidateNoFollowInventory(inventory); err != nil {
		t.Fatalf("ValidateNoFollowInventory: %v", err)
	}
	var symlinkEntry *NoFollowInventoryEntry
	for index := range inventory.Entries {
		if inventory.Entries[index].Kind == InventoryEntrySymlink {
			symlinkEntry = &inventory.Entries[index]
		}
	}
	if symlinkEntry == nil || symlinkEntry.SymlinkTarget != "../payload.txt" || symlinkEntry.ContentDigest != "" || !sha256DigestPattern.MatchString(symlinkEntry.TargetDigest) {
		t.Fatalf("symlink inventory entry = %#v", symlinkEntry)
	}

	inventory.Entries[0].RelativePath = "../escape"
	if err := ValidateNoFollowInventory(inventory); err == nil {
		t.Fatal("inventory validation accepted relative path escape")
	}
}

func TestInventoryMappedSourceRejectsSymlinkEscape(t *testing.T) {
	roots := newWorkspaceRoots(t)
	source := filepath.Join(roots.BoxRoot, "Topics", "topic-one")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../outside", filepath.Join(source, "nested", "escape")); err != nil {
		t.Fatal(err)
	}
	_, err := InventoryMappedSource(context.Background(), roots, WorkspaceOperationArchive, WorkspaceKindTopic, "topic-one")
	if err == nil || !strings.Contains(err.Error(), "escapes inventory root") {
		t.Fatalf("symlink escape error = %v", err)
	}
}

func TestInventoryMappedSourceRejectsIntermediateSymlink(t *testing.T) {
	roots := newWorkspaceRoots(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "topic-one"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(roots.BoxRoot, "Topics")); err != nil {
		t.Fatal(err)
	}
	_, err := InventoryMappedSource(context.Background(), roots, WorkspaceOperationArchive, WorkspaceKindTopic, "topic-one")
	if err == nil || !strings.Contains(err.Error(), "must be a real directory") {
		t.Fatalf("intermediate symlink error = %v", err)
	}
}

func TestWorkspaceArchivePlanBindsMappingAndDigest(t *testing.T) {
	roots := newWorkspaceRoots(t)
	plan := validWorkspaceArchivePlan(t, roots, WorkspaceOperationArchive)
	if err := ValidateWorkspaceArchivePlan(plan, roots); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}

	tampered := plan
	tampered.Source.Path.AbsolutePath = filepath.Join(roots.BoxRoot, "Projects", "other")
	if err := SealWorkspaceArchivePlan(&tampered); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkspaceArchivePlan(tampered, roots); err == nil || !strings.Contains(err.Error(), "closed kind/root mapping") {
		t.Fatalf("tampered path error = %v", err)
	}

	crossDevice := plan
	crossDevice.Destination.ParentIdentity.DeviceID++
	if err := SealWorkspaceArchivePlan(&crossDevice); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkspaceArchivePlan(crossDevice, roots); err == nil || !strings.Contains(err.Error(), "same filesystem") {
		t.Fatalf("cross-device error = %v", err)
	}

	drifted := plan
	drifted.Inventory.Entries[0].SizeBytes++
	if err := ValidateWorkspaceArchivePlan(drifted, roots); err == nil || !strings.Contains(err.Error(), "inventory") {
		t.Fatalf("inventory mutation error = %v", err)
	}
}

func TestAdversarialEvidenceFixtures(t *testing.T) {
	roots := newWorkspaceRoots(t)
	fixtures := loadEvidenceFixtures(t)
	wantScenarios := []string{"collision", "cross_device_roots", "destination_substitution", "replay_conflict", "source_drift", "source_parent_substitution", "symlink_escape"}
	gotScenarios := make([]string, 0, len(fixtures))
	for _, fixture := range fixtures {
		gotScenarios = append(gotScenarios, fixture.Scenario)
		plan := validWorkspaceArchivePlan(t, roots, WorkspaceOperationArchive)
		observedSource := plan.Source
		observedDestination := plan.Destination
		var findings []WorkspaceArchiveFinding
		var got WorkspaceArchiveFindingCode
		switch fixture.Scenario {
		case "collision":
			observedDestination.Identity = presentIdentity(plan.Source.Identity.DeviceID, 900, InventoryEntryDirectory)
			findings = EvaluatePlanEvidence(plan, observedSource, observedDestination)
		case "cross_device_roots":
			observedDestination.ParentIdentity.DeviceID += uint64(fixture.Mutation.DeviceIDDelta)
			findings = EvaluatePlanEvidence(plan, observedSource, observedDestination)
		case "destination_substitution":
			observedDestination.ParentIdentity.Inode += uint64(fixture.Mutation.InodeDelta)
			findings = EvaluatePlanEvidence(plan, observedSource, observedDestination)
		case "source_drift":
			observedSource.Identity.Inode += uint64(fixture.Mutation.InodeDelta)
			findings = EvaluatePlanEvidence(plan, observedSource, observedDestination)
		case "source_parent_substitution":
			observedSource.ParentIdentity.Inode += uint64(fixture.Mutation.InodeDelta)
			findings = EvaluatePlanEvidence(plan, observedSource, observedDestination)
		case "replay_conflict":
			existing := operationAtPhase(t, plan, PhaseArchivePlanned, "")
			incoming := plan
			incoming.Reason = "different reviewed reason"
			if err := SealWorkspaceArchivePlan(&incoming); err != nil {
				t.Fatal(err)
			}
			_, finding := EvaluateReplay(existing, incoming)
			if finding != nil {
				got = finding.Code
			}
		case "symlink_escape":
			entry := plan.Inventory.Entries[0]
			entry.Kind = InventoryEntrySymlink
			entry.ContentDigest = ""
			entry.SymlinkTarget = fixture.Mutation.SymlinkTarget
			entry.TargetDigest = sha256Digest([]byte(entry.SymlinkTarget))
			if err := validateSymlinkTarget(entry.RelativePath, entry.SymlinkTarget); err != nil {
				got = FindingSymlinkEscape
			}
		default:
			t.Fatalf("unknown adversarial fixture scenario %q", fixture.Scenario)
		}
		if got == "" && hasFindingCode(findings, fixture.ExpectedFinding) {
			got = fixture.ExpectedFinding
		}
		if got != fixture.ExpectedFinding {
			t.Fatalf("fixture %q findings = %#v, want %q", fixture.Scenario, findings, fixture.ExpectedFinding)
		}
	}
	sort.Strings(gotScenarios)
	sort.Strings(wantScenarios)
	if !reflect.DeepEqual(gotScenarios, wantScenarios) {
		t.Fatalf("evidence fixture scenarios = %v, want %v", gotScenarios, wantScenarios)
	}
}

func TestCrashPhaseFixtureCoversEveryBoundary(t *testing.T) {
	var fixture crashPhaseFixture
	readFixture(t, "crash_phases.json", &fixture)
	if fixture.SchemaVersion != "storage.workspace_archive_crash_fixture.v1" {
		t.Fatalf("crash fixture schema = %q", fixture.SchemaVersion)
	}
	roots := newWorkspaceRoots(t)
	plans := map[WorkspaceOperationKind]WorkspaceArchivePlan{
		WorkspaceOperationArchive: validWorkspaceArchivePlan(t, roots, WorkspaceOperationArchive),
		WorkspaceOperationRestore: validWorkspaceArchivePlan(t, roots, WorkspaceOperationRestore),
	}
	covered := map[WorkspaceArchivePhase]bool{}
	for _, boundary := range fixture.CrashBoundaries {
		covered[boundary.LastSafePhase] = true
		for _, terminal := range []WorkspaceArchivePhase{PhaseBlocked, PhaseManualRepairRequired} {
			operationPlan, ok := plans[boundary.OperationKind]
			if !ok {
				t.Fatalf("unsupported fixture operation kind %q", boundary.OperationKind)
			}
			operation := operationAtPhase(t, operationPlan, terminal, boundary.LastSafePhase)
			if err := ValidateWorkspaceArchiveOperation(operation); err != nil {
				t.Fatalf("%s after %s rejected: %v", terminal, boundary.LastSafePhase, err)
			}
			covered[terminal] = true
		}
	}
	for _, terminal := range fixture.CompletionPhases {
		operationPlan := plans[WorkspaceOperationArchive]
		if strings.HasPrefix(string(terminal), "restore_") {
			operationPlan = plans[WorkspaceOperationRestore]
		}
		operation := operationAtPhase(t, operationPlan, terminal, "")
		if err := ValidateWorkspaceArchiveOperation(operation); err != nil {
			t.Fatalf("completion phase %s rejected: %v", terminal, err)
		}
		covered[terminal] = true
	}
	for _, phase := range AllWorkspaceArchivePhases() {
		if !covered[phase] {
			t.Fatalf("crash fixture does not cover phase %q", phase)
		}
	}
}

func TestOperationPhaseRequiresExactDurableBoundary(t *testing.T) {
	roots := newWorkspaceRoots(t)
	for _, operationKind := range []WorkspaceOperationKind{WorkspaceOperationArchive, WorkspaceOperationRestore} {
		plan := validWorkspaceArchivePlan(t, roots, operationKind)
		phases := []WorkspaceArchivePhase{
			PhaseArchivePlanned,
			PhaseArchiveIntentCommitted,
			PhaseArchivePayloadMoved,
			PhaseArchiveProjectionsCommitted,
			PhaseArchiveComplete,
		}
		if operationKind == WorkspaceOperationRestore {
			phases = []WorkspaceArchivePhase{
				PhaseRestorePlanned,
				PhaseRestoreIntentCommitted,
				PhaseRestorePayloadMoved,
				PhaseRestoreProjectionsCommitted,
				PhaseRestoreComplete,
			}
		}
		for _, phase := range phases {
			assertEveryTimestampMutationRejects(t, operationAtPhase(t, plan, phase, ""))
			if phase != phases[len(phases)-1] {
				for _, terminal := range []WorkspaceArchivePhase{PhaseBlocked, PhaseManualRepairRequired} {
					assertEveryTimestampMutationRejects(t, operationAtPhase(t, plan, terminal, phase))
				}
			}
		}
	}
}

func TestOperationAndStatusRejectForeignFindingsAndInvalidLastSafePhase(t *testing.T) {
	plan := validWorkspaceArchivePlan(t, newWorkspaceRoots(t), WorkspaceOperationArchive)
	foreignFinding := WorkspaceArchiveFinding{
		SchemaVersion: WorkspaceArchiveFindingSchemaVersion,
		OperationID:   testWorkspaceRestoreID,
		Code:          FindingSourceDrift,
		Severity:      FindingSeverityError,
		AtPhase:       PhaseArchivePlanned,
		Summary:       "foreign operation finding",
		Repairable:    true,
	}
	operation := operationAtPhase(t, plan, PhaseArchivePlanned, "")
	operation.Findings = []WorkspaceArchiveFinding{foreignFinding}
	if err := ValidateWorkspaceArchiveOperation(operation); err == nil || !strings.Contains(err.Error(), "owning operation") {
		t.Fatalf("foreign operation finding error = %v", err)
	}

	status := WorkspaceArchiveStatus{
		SchemaVersion: WorkspaceArchiveStatusSchemaVersion,
		OperationID:   testWorkspaceOperationID,
		Phase:         PhaseBlocked,
		LastSafePhase: PhaseArchivePlanned,
		Status:        OperationStatusBlocked,
		UpdatedAt:     time.Unix(1_800_000_001, 0).UTC(),
	}
	status.Findings = []WorkspaceArchiveFinding{foreignFinding}
	if err := ValidateWorkspaceArchiveStatus(status); err == nil || !strings.Contains(err.Error(), "owning status") {
		t.Fatalf("foreign status finding error = %v", err)
	}
	status.Findings = nil
	for _, invalid := range []WorkspaceArchivePhase{"arbitrary", PhaseArchiveComplete, PhaseRestoreComplete, PhaseBlocked, PhaseManualRepairRequired} {
		status.LastSafePhase = invalid
		if err := ValidateWorkspaceArchiveStatus(status); err == nil {
			t.Fatalf("status accepted invalid last_safe_phase %q", invalid)
		}
	}
	for _, valid := range []WorkspaceArchivePhase{PhaseArchivePlanned, PhaseArchiveProjectionsCommitted, PhaseRestorePlanned, PhaseRestoreProjectionsCommitted} {
		status.LastSafePhase = valid
		if err := ValidateWorkspaceArchiveStatus(status); err != nil {
			t.Fatalf("status rejected valid last_safe_phase %q: %v", valid, err)
		}
	}
}

func TestHistoricalCopyManifestsNeverBecomePhysicalMoveEvidence(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "historical_copy_*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("historical fixture count = %d, want 3", len(paths))
	}
	for _, fixturePath := range paths {
		payload, err := os.ReadFile(fixturePath)
		if err != nil {
			t.Fatal(err)
		}
		compatibility, err := ClassifyManifestEvidence(payload)
		if strings.Contains(filepath.Base(fixturePath), "spoof") {
			if err == nil || !strings.Contains(err.Error(), "cannot claim physical-move evidence") {
				t.Fatalf("spoof fixture classification = %#v err=%v", compatibility, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("classify %s: %v", fixturePath, err)
		}
		if compatibility.Class != ManifestEvidenceHistoricalCopy || compatibility.HistoricalCopy == nil || compatibility.Physical != nil {
			t.Fatalf("historical manifest classification = %#v", compatibility)
		}
	}
}

func TestPhysicalManifestAndLifecycleEventContracts(t *testing.T) {
	roots := newWorkspaceRoots(t)
	paths, err := ResolveWorkspacePaths(roots, WorkspaceKindTopic, "topic-one")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	manifest := WorkspaceArchiveManifest{
		SchemaVersion:         WorkspaceArchiveManifestSchemaVersion,
		EvidenceKind:          PhysicalWorkspaceMoveEvidence,
		ArchiveOperationID:    testWorkspaceOperationID,
		Kind:                  WorkspaceKindTopic,
		ObjectID:              "topic_object_one",
		Slug:                  "topic-one",
		LifecycleState:        WorkspaceLifecycleArchived,
		ActivePath:            paths.Active,
		ArchivePayloadPath:    paths.ArchivePayload,
		ArchiveSourceIdentity: presentIdentity(10, 101, InventoryEntryDirectory),
		InventoryDigest:       sha256Digest([]byte("inventory")),
		PlanDigest:            sha256Digest([]byte("plan")),
		LifecycleEventID:      testWorkspaceEventID,
		ActorID:               "actor_archive_test",
		Reason:                "archive completed topic",
		ArchivedAt:            now,
		Authentication:        ManifestAuthentication{Algorithm: "hmac-sha256", KeyID: "workspace-archive-main", Tag: "hmac-sha256:" + strings.Repeat("a", 64)},
	}
	if err := ValidateWorkspaceArchiveManifest(manifest); err != nil {
		t.Fatalf("manifest rejected: %v", err)
	}
	if err := ValidateWorkspaceArchiveManifestForRoots(manifest, roots); err != nil {
		t.Fatalf("manifest rejected against trusted roots: %v", err)
	}
	tamperedManifest := manifest
	tamperedManifest.ActivePath.AbsolutePath = filepath.Join(t.TempDir(), "Topics", "topic-one")
	if err := ValidateWorkspaceArchiveManifestForRoots(tamperedManifest, roots); err == nil {
		t.Fatal("manifest with substituted absolute root passed trusted-root validation")
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	compatibility, err := ClassifyManifestEvidence(payload)
	if err != nil || compatibility.Class != ManifestEvidencePhysicalMove || compatibility.Physical == nil || compatibility.HistoricalCopy != nil {
		t.Fatalf("physical manifest classification = %#v err=%v", compatibility, err)
	}

	event := WorkspaceLifecycleEvent{
		SchemaVersion:   WorkspaceLifecycleEventSchemaVersion,
		EventID:         testWorkspaceEventID,
		EventKind:       WorkspaceLifecycleEventKind,
		OperationID:     testWorkspaceOperationID,
		Kind:            WorkspaceKindTopic,
		ObjectID:        "topic_object_one",
		Slug:            "topic-one",
		Transition:      WorkspaceTransitionArchived,
		FromState:       WorkspaceLifecycleActive,
		ToState:         WorkspaceLifecycleArchived,
		SourcePath:      paths.Active,
		DestinationPath: paths.ArchivePayload,
		ActorID:         "actor_archive_test",
		Reason:          "archive completed topic",
		OccurredAt:      now,
	}
	if err := ValidateWorkspaceLifecycleEvent(event); err != nil {
		t.Fatalf("lifecycle event rejected: %v", err)
	}
	if err := ValidateWorkspaceLifecycleEventForRoots(event, roots); err != nil {
		t.Fatalf("lifecycle event rejected against trusted roots: %v", err)
	}
	tamperedEvent := event
	tamperedEvent.SourcePath.AbsolutePath = filepath.Join(t.TempDir(), "Topics", "topic-one")
	if err := ValidateWorkspaceLifecycleEventForRoots(tamperedEvent, roots); err == nil {
		t.Fatal("lifecycle event with substituted absolute root passed trusted-root validation")
	}
	event.EventKind = "storage.copy_completed"
	if err := ValidateWorkspaceLifecycleEvent(event); err == nil {
		t.Fatal("lifecycle event accepted a second event kind")
	}
}

func TestOperationManifestAndEventRelations(t *testing.T) {
	roots := newWorkspaceRoots(t)
	archivePlan := validWorkspaceArchivePlan(t, roots, WorkspaceOperationArchive)
	archiveOperation := operationAtPhase(t, archivePlan, PhaseArchiveComplete, "")
	manifest := manifestForOperation(t, roots, archiveOperation)
	if err := ValidateWorkspaceArchiveOperationForRoots(archiveOperation, roots); err != nil {
		t.Fatalf("archive operation rejected against trusted roots: %v", err)
	}
	if err := ValidateWorkspaceArchiveManifestForOperations(manifest, archiveOperation, nil); err != nil {
		t.Fatalf("archive relation rejected: %v", err)
	}

	substitutedOperation := archiveOperation
	substitutedOperation.Source.Path.AbsolutePath = filepath.Join(t.TempDir(), "Topics", "topic-one")
	if err := ValidateWorkspaceArchiveOperation(substitutedOperation); err != nil {
		t.Fatalf("standalone operation unexpectedly rejected suffix-valid substituted root: %v", err)
	}
	if err := ValidateWorkspaceArchiveOperationForRoots(substitutedOperation, roots); err == nil {
		t.Fatal("operation with substituted absolute root passed trusted-root validation")
	}

	wrong := archiveOperation
	wrong.OperationKind = WorkspaceOperationRestore
	wrong.Phase = PhaseRestoreComplete
	if err := ValidateWorkspaceArchiveManifestForOperations(manifest, wrong, nil); err == nil {
		t.Fatal("manifest accepted a restore operation as its archive operation")
	}
	for _, mutate := range []func(*WorkspaceArchiveOperation){
		func(operation *WorkspaceArchiveOperation) { operation.ObjectID = "topic_object_other" },
		func(operation *WorkspaceArchiveOperation) { operation.Slug = "topic-other" },
		func(operation *WorkspaceArchiveOperation) { operation.PlanDigest = sha256Digest([]byte("other plan")) },
	} {
		wrong = archiveOperation
		mutate(&wrong)
		if err := ValidateWorkspaceArchiveManifestForOperations(manifest, wrong, nil); err == nil {
			t.Fatal("manifest accepted a contradictory archive operation")
		}
	}

	restorePlan := validWorkspaceArchivePlan(t, roots, WorkspaceOperationRestore)
	restoreOperation := operationAtPhase(t, restorePlan, PhaseRestoreComplete, "")
	manifest.LifecycleState = WorkspaceLifecycleActive
	manifest.RestoreOperationID = restoreOperation.OperationID
	manifest.RestorePlanDigest = restoreOperation.PlanDigest
	manifest.RestoredAt = restoreOperation.CompletedAt
	if err := ValidateWorkspaceArchiveManifestForOperations(manifest, archiveOperation, &restoreOperation); err != nil {
		t.Fatalf("restore relation rejected: %v", err)
	}
	for _, mutate := range []func(*WorkspaceArchiveOperation){
		func(operation *WorkspaceArchiveOperation) {
			operation.OperationKind = WorkspaceOperationArchive
			operation.Phase = PhaseArchiveComplete
		},
		func(operation *WorkspaceArchiveOperation) { operation.ObjectID = "topic_object_other" },
		func(operation *WorkspaceArchiveOperation) { operation.Slug = "topic-other" },
		func(operation *WorkspaceArchiveOperation) {
			operation.PlanDigest = sha256Digest([]byte("wrong restore plan"))
		},
	} {
		wrongRestore := restoreOperation
		mutate(&wrongRestore)
		if err := ValidateWorkspaceArchiveManifestForOperations(manifest, archiveOperation, &wrongRestore); err == nil {
			t.Fatal("manifest accepted contradictory restore operation")
		}
	}

	event := eventForOperation(archiveOperation)
	if err := ValidateWorkspaceLifecycleEventForOperation(event, archiveOperation); err != nil {
		t.Fatalf("event relation rejected: %v", err)
	}
	for _, mutate := range []func(*WorkspaceLifecycleEvent){
		func(event *WorkspaceLifecycleEvent) { event.OperationID = testWorkspaceRestoreID },
		func(event *WorkspaceLifecycleEvent) { event.Kind = WorkspaceKindProject },
		func(event *WorkspaceLifecycleEvent) { event.ObjectID = "topic_object_other" },
		func(event *WorkspaceLifecycleEvent) { event.Slug = "topic-other" },
		func(event *WorkspaceLifecycleEvent) { event.Transition = WorkspaceTransitionRestored },
		func(event *WorkspaceLifecycleEvent) { event.SourcePath = event.DestinationPath },
		func(event *WorkspaceLifecycleEvent) { event.ActorID = "actor_archive_other" },
		func(event *WorkspaceLifecycleEvent) { event.Reason = "contradictory reason" },
		func(event *WorkspaceLifecycleEvent) { event.OccurredAt = event.OccurredAt.Add(time.Second) },
	} {
		contradictory := event
		mutate(&contradictory)
		if err := ValidateWorkspaceLifecycleEventForOperation(contradictory, archiveOperation); err == nil {
			t.Fatal("event relation accepted contradictory owning-operation evidence")
		}
	}
}

func TestManifestAndOperationChronology(t *testing.T) {
	roots := newWorkspaceRoots(t)
	plan := validWorkspaceArchivePlan(t, roots, WorkspaceOperationArchive)
	operation := operationAtPhase(t, plan, PhaseArchiveComplete, "")
	before := plan.PlannedAt.Add(-time.Second)
	operation.IntentCommittedAt = &before
	if err := ValidateWorkspaceArchiveOperation(operation); err == nil || !strings.Contains(err.Error(), "precedes") {
		t.Fatalf("operation chronology error = %v", err)
	}

	manifest := manifestForOperation(t, roots, operationAtPhase(t, plan, PhaseArchiveComplete, ""))
	manifest.LifecycleState = WorkspaceLifecycleActive
	manifest.RestoreOperationID = testWorkspaceRestoreID
	manifest.RestorePlanDigest = sha256Digest([]byte("restore plan"))
	restoredAt := manifest.ArchivedAt.Add(-time.Second)
	manifest.RestoredAt = &restoredAt
	if err := ValidateWorkspaceArchiveManifest(manifest); err == nil || !strings.Contains(err.Error(), "precedes") {
		t.Fatalf("manifest chronology error = %v", err)
	}
}

func assertEveryTimestampMutationRejects(t *testing.T, operation WorkspaceArchiveOperation) {
	t.Helper()
	if err := ValidateWorkspaceArchiveOperation(operation); err != nil {
		t.Fatalf("valid phase %q boundary rejected: %v", operation.Phase, err)
	}
	now := operation.UpdatedAt
	mutations := []struct {
		name   string
		mutate func(*WorkspaceArchiveOperation)
	}{
		{"intent_committed_at", func(candidate *WorkspaceArchiveOperation) {
			if candidate.IntentCommittedAt == nil {
				candidate.IntentCommittedAt = &now
			} else {
				candidate.IntentCommittedAt = nil
			}
		}},
		{"payload_moved_at", func(candidate *WorkspaceArchiveOperation) {
			if candidate.PayloadMovedAt == nil {
				candidate.PayloadMovedAt = &now
			} else {
				candidate.PayloadMovedAt = nil
			}
		}},
		{"projections_committed_at", func(candidate *WorkspaceArchiveOperation) {
			if candidate.ProjectionsCommittedAt == nil {
				candidate.ProjectionsCommittedAt = &now
			} else {
				candidate.ProjectionsCommittedAt = nil
			}
		}},
		{"completed_at", func(candidate *WorkspaceArchiveOperation) {
			if candidate.CompletedAt == nil {
				candidate.CompletedAt = &now
			} else {
				candidate.CompletedAt = nil
			}
		}},
	}
	for _, mutation := range mutations {
		candidate := operation
		mutation.mutate(&candidate)
		if err := ValidateWorkspaceArchiveOperation(candidate); err == nil {
			t.Fatalf("phase %q last_safe_phase %q accepted toggled %s", operation.Phase, operation.LastSafePhase, mutation.name)
		}
	}
}

func manifestForOperation(t *testing.T, roots TrustedWorkspaceRoots, operation WorkspaceArchiveOperation) WorkspaceArchiveManifest {
	t.Helper()
	paths, err := ResolveWorkspacePaths(roots, operation.Kind, operation.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if operation.CompletedAt == nil {
		t.Fatal("manifest fixture requires completed operation")
	}
	return WorkspaceArchiveManifest{
		SchemaVersion:         WorkspaceArchiveManifestSchemaVersion,
		EvidenceKind:          PhysicalWorkspaceMoveEvidence,
		ArchiveOperationID:    operation.OperationID,
		Kind:                  operation.Kind,
		ObjectID:              operation.ObjectID,
		Slug:                  operation.Slug,
		LifecycleState:        WorkspaceLifecycleArchived,
		ActivePath:            paths.Active,
		ArchivePayloadPath:    paths.ArchivePayload,
		ArchiveSourceIdentity: operation.Source.Identity,
		InventoryDigest:       sha256Digest([]byte("inventory")),
		PlanDigest:            operation.PlanDigest,
		LifecycleEventID:      testWorkspaceEventID,
		ActorID:               operation.ActorID,
		Reason:                operation.Reason,
		ArchivedAt:            *operation.CompletedAt,
		Authentication: ManifestAuthentication{
			Algorithm: "hmac-sha256",
			KeyID:     "workspace-archive-main",
			Tag:       "hmac-sha256:" + strings.Repeat("a", 64),
		},
	}
}

func eventForOperation(operation WorkspaceArchiveOperation) WorkspaceLifecycleEvent {
	transition := WorkspaceTransitionArchived
	fromState, toState := WorkspaceLifecycleActive, WorkspaceLifecycleArchived
	if operation.OperationKind == WorkspaceOperationRestore {
		transition = WorkspaceTransitionRestored
		fromState, toState = WorkspaceLifecycleArchived, WorkspaceLifecycleActive
	}
	return WorkspaceLifecycleEvent{
		SchemaVersion:   WorkspaceLifecycleEventSchemaVersion,
		EventID:         testWorkspaceEventID,
		EventKind:       WorkspaceLifecycleEventKind,
		OperationID:     operation.OperationID,
		Kind:            operation.Kind,
		ObjectID:        operation.ObjectID,
		Slug:            operation.Slug,
		Transition:      transition,
		FromState:       fromState,
		ToState:         toState,
		SourcePath:      operation.Source.Path,
		DestinationPath: operation.Destination.Path,
		ActorID:         operation.ActorID,
		Reason:          operation.Reason,
		OccurredAt:      *operation.ProjectionsCommittedAt,
	}
}

type adversarialEvidenceFixture struct {
	SchemaVersion   string                      `json:"schema_version"`
	Scenario        string                      `json:"scenario"`
	ExpectedFinding WorkspaceArchiveFindingCode `json:"expected_finding"`
	Mutation        struct {
		DeviceIDDelta       int    `json:"device_id_delta,omitempty"`
		InodeDelta          int    `json:"inode_delta,omitempty"`
		SymlinkTarget       string `json:"symlink_target,omitempty"`
		DestinationPresence string `json:"destination_presence,omitempty"`
		PlanDigest          string `json:"plan_digest,omitempty"`
	} `json:"mutation"`
}

type crashPhaseFixture struct {
	SchemaVersion   string `json:"schema_version"`
	CrashBoundaries []struct {
		OperationKind WorkspaceOperationKind `json:"operation_kind"`
		LastSafePhase WorkspaceArchivePhase  `json:"last_safe_phase"`
	} `json:"crash_boundaries"`
	CompletionPhases []WorkspaceArchivePhase `json:"completion_phases"`
}

func loadEvidenceFixtures(t *testing.T) []adversarialEvidenceFixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "evidence_*.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixtures := make([]adversarialEvidenceFixture, 0, len(paths))
	for _, fixturePath := range paths {
		var fixture adversarialEvidenceFixture
		payload, err := os.ReadFile(fixturePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(payload, &fixture); err != nil {
			t.Fatalf("decode %s: %v", fixturePath, err)
		}
		if fixture.SchemaVersion != "storage.workspace_archive_adversarial_fixture.v1" {
			t.Fatalf("fixture %s schema = %q", fixturePath, fixture.SchemaVersion)
		}
		fixtures = append(fixtures, fixture)
	}
	return fixtures
}

func readFixture(t *testing.T, name string, target any) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

func validWorkspaceArchivePlan(t *testing.T, roots TrustedWorkspaceRoots, operation WorkspaceOperationKind) WorkspaceArchivePlan {
	t.Helper()
	paths, err := ResolveWorkspacePaths(roots, WorkspaceKindTopic, "topic-one")
	if err != nil {
		t.Fatal(err)
	}
	sourcePath, destinationPath := paths.Active, paths.ArchivePayload
	operationID := testWorkspaceOperationID
	if operation == WorkspaceOperationRestore {
		sourcePath, destinationPath = paths.ArchivePayload, paths.Active
		operationID = testWorkspaceRestoreID
	}
	sourceIdentity := presentIdentity(10, 101, InventoryEntryDirectory)
	inventory := NoFollowInventory{
		SchemaVersion: NoFollowInventorySchemaVersion,
		RootIdentity:  sourceIdentity,
		Entries: []NoFollowInventoryEntry{{
			RelativePath:   "payload.txt",
			Kind:           InventoryEntryFile,
			DeviceID:       10,
			Inode:          102,
			Mode:           uint32(0o640),
			SizeBytes:      3,
			ModifiedUnixNS: 1_700_000_000_000_000_000,
			ContentDigest:  sha256Digest([]byte("abc")),
		}},
		EntryCount: 1,
		FileCount:  1,
		TotalBytes: 3,
	}
	inventory.Digest, err = noFollowInventoryDigest(inventory)
	if err != nil {
		t.Fatal(err)
	}
	plan := WorkspaceArchivePlan{
		SchemaVersion: WorkspaceArchivePlanSchemaVersion,
		EvidenceKind:  PhysicalWorkspaceMoveEvidence,
		OperationID:   operationID,
		OperationKind: operation,
		Kind:          WorkspaceKindTopic,
		ObjectID:      "topic_object_one",
		Slug:          "topic-one",
		Source:        WorkspacePathBinding{Path: sourcePath, Identity: sourceIdentity, ParentIdentity: presentIdentity(10, 100, InventoryEntryDirectory)},
		Destination:   WorkspacePathBinding{Path: destinationPath, Identity: PathIdentity{Presence: PathAbsent}, ParentIdentity: presentIdentity(10, 200, InventoryEntryDirectory)},
		Inventory:     inventory,
		ActorID:       "actor_archive_test",
		Reason:        "archive completed topic",
		PlannedAt:     time.Unix(1_800_000_000, 0).UTC(),
	}
	if err := SealWorkspaceArchivePlan(&plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func operationAtPhase(t *testing.T, plan WorkspaceArchivePlan, phase, lastSafe WorkspaceArchivePhase) WorkspaceArchiveOperation {
	t.Helper()
	now := time.Unix(1_800_000_000, 0).UTC()
	status, ok := statusForPhase(phase)
	if !ok {
		t.Fatalf("unknown phase %q", phase)
	}
	operation := WorkspaceArchiveOperation{
		SchemaVersion: WorkspaceArchiveOperationSchemaVersion,
		EvidenceKind:  PhysicalWorkspaceMoveEvidence,
		OperationID:   plan.OperationID,
		OperationKind: plan.OperationKind,
		Kind:          plan.Kind,
		ObjectID:      plan.ObjectID,
		Slug:          plan.Slug,
		Phase:         phase,
		LastSafePhase: lastSafe,
		Status:        status,
		PlanDigest:    plan.PlanDigest,
		Source:        plan.Source,
		Destination:   plan.Destination,
		ActorID:       plan.ActorID,
		Reason:        plan.Reason,
		PlannedAt:     plan.PlannedAt,
		UpdatedAt:     now,
	}
	effective := phase
	if phase == PhaseBlocked || phase == PhaseManualRepairRequired {
		effective = lastSafe
	}
	rank, ok := phaseRank(plan.OperationKind, effective)
	if !ok {
		t.Fatalf("phase %q does not match operation %q", effective, plan.OperationKind)
	}
	if rank >= 1 {
		operation.IntentCommittedAt = &now
	}
	if rank >= 2 {
		operation.PayloadMovedAt = &now
	}
	if rank >= 3 {
		operation.ProjectionsCommittedAt = &now
	}
	if rank >= 4 {
		operation.CompletedAt = &now
	}
	return operation
}

func newWorkspaceRoots(t *testing.T) TrustedWorkspaceRoots {
	t.Helper()
	base := t.TempDir()
	roots := TrustedWorkspaceRoots{BoxRoot: filepath.Join(base, "box"), StorageRoot: filepath.Join(base, "storage")}
	if err := os.MkdirAll(roots.BoxRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(roots.StorageRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	return roots
}

func presentIdentity(device, inode uint64, kind InventoryEntryKind) PathIdentity {
	mode := uint32(0o640)
	if kind == InventoryEntryDirectory {
		mode = uint32(os.ModeDir | 0o750)
	}
	return PathIdentity{
		Presence:       PathPresent,
		ObjectKind:     string(kind),
		DeviceID:       device,
		Inode:          inode,
		Mode:           mode,
		SizeBytes:      1,
		ModifiedUnixNS: 1_700_000_000_000_000_000,
	}
}

func hasFindingCode(findings []WorkspaceArchiveFinding, code WorkspaceArchiveFindingCode) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
