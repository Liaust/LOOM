package watchedroots

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorePersistsSummaryAndPathState(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	if err := store.EnsureRoot("notes"); err != nil {
		t.Fatalf("EnsureRoot failed: %v", err)
	}
	paths := store.RootPaths("notes")
	if _, err := os.Stat(paths.PathsDir); err != nil {
		t.Fatalf("expected paths dir: %v", err)
	}

	now := time.Now().UTC()
	summary := RootSummary{
		RootKey:       "notes",
		WorkerKey:     "node-agent.watched_root.notes",
		Status:        "unscanned",
		RootReachable: true,
		GeneratedAt:   now,
	}
	if err := store.SaveLatestSummary("notes", summary); err != nil {
		t.Fatalf("SaveLatestSummary failed: %v", err)
	}
	loadedSummary, err := store.LoadLatestSummary("notes")
	if err != nil {
		t.Fatalf("LoadLatestSummary failed: %v", err)
	}
	if loadedSummary.RootKey != "notes" || loadedSummary.SchemaVersion != SummarySchemaVersion {
		t.Fatalf("unexpected loaded summary %#v", loadedSummary)
	}

	state := PathState{
		RootKey:      "notes",
		RelativePath: "Project.md",
		Status:       PathStatusIncluded,
		Kind:         PathKindFile,
		LastSeenAt:   now,
	}
	if err := store.SavePathState(state); err != nil {
		t.Fatalf("SavePathState failed: %v", err)
	}
	loadedState, err := store.LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState failed: %v", err)
	}
	if loadedState.PathKey != PathKey("notes", "Project.md") || loadedState.SchemaVersion != PathStateSchemaVersion {
		t.Fatalf("unexpected loaded path state %#v", loadedState)
	}
	states, err := store.ListPathStates("notes", 0)
	if err != nil {
		t.Fatalf("ListPathStates failed: %v", err)
	}
	if len(states) != 1 || states[0].RelativePath != "Project.md" {
		t.Fatalf("unexpected states %#v", states)
	}
	filtered, err := store.ListPathStatesByStatus("notes", PathStatusIncluded, 0)
	if err != nil {
		t.Fatalf("ListPathStatesByStatus failed: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("unexpected filtered states %#v", filtered)
	}
}

func TestProtectedFolderReconcileStatePersistsAtomically(t *testing.T) {
	store := NewStore(t.TempDir())
	want := ProtectedFolderReconcileState{AppliedRevision: 3, ConfigHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RootKeys: []string{"z", "a"}}
	if err := store.SaveProtectedFolderReconcileState(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadProtectedFolderReconcileState()
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != ProtectedFolderReconcileStateSchemaVersion || got.AppliedRevision != 3 || len(got.RootKeys) != 2 || got.RootKeys[0] != "a" {
		t.Fatalf("state=%#v", got)
	}
	if err := store.DeleteProtectedFolderReconcileState(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadProtectedFolderReconcileState(); !os.IsNotExist(err) {
		t.Fatalf("deleted state error=%v", err)
	}
}

func TestStoreDirtyHintsCoalesceAndClear(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	if err := store.AddDirtyHint("notes", DirtyHint{
		RelativePath: "Project.md",
		HintKind:     DirtyHintModified,
		Source:       DirtyHintSourceManual,
	}); err != nil {
		t.Fatalf("AddDirtyHint failed: %v", err)
	}
	if err := store.AddDirtyHint("notes", DirtyHint{
		RelativePath: "Project.md",
		HintKind:     DirtyHintModified,
		Source:       DirtyHintSourceManual,
	}); err != nil {
		t.Fatalf("AddDirtyHint second failed: %v", err)
	}
	hints, err := store.ListDirtyHints("notes")
	if err != nil {
		t.Fatalf("ListDirtyHints failed: %v", err)
	}
	if len(hints) != 1 || hints[0].Count != 2 {
		t.Fatalf("expected coalesced hint, got %#v", hints)
	}
	if err := store.MarkRescanRequired("notes", "overflow"); err != nil {
		t.Fatalf("MarkRescanRequired failed: %v", err)
	}
	hints, err = store.ListDirtyHints("notes")
	if err != nil {
		t.Fatalf("ListDirtyHints after rescan failed: %v", err)
	}
	if len(hints) != 2 {
		t.Fatalf("expected two hints, got %#v", hints)
	}
	if err := store.ClearDirtyHints("notes", []string{"Project.md"}); err != nil {
		t.Fatalf("ClearDirtyHints failed: %v", err)
	}
	hints, err = store.ListDirtyHints("notes")
	if err != nil {
		t.Fatalf("ListDirtyHints after clear failed: %v", err)
	}
	if len(hints) != 1 || hints[0].HintKind != DirtyHintRescanRequired {
		t.Fatalf("unexpected hints after clear %#v", hints)
	}
	if err := store.ClearDirtyHints("notes", nil); err != nil {
		t.Fatalf("ClearDirtyHints all failed: %v", err)
	}
	hints, err = store.ListDirtyHints("notes")
	if err != nil {
		t.Fatalf("ListDirtyHints final failed: %v", err)
	}
	if len(hints) != 0 {
		t.Fatalf("expected no hints, got %#v", hints)
	}
}

func TestStoreResolveMissingFindingsPreservesCurrentAndIgnored(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	now := time.Now().UTC()
	current := Finding{
		RootKey:      "notes",
		Kind:         FindingPermissionDenied,
		RelativePath: "locked",
		Status:       FindingStatusOpen,
		Summary:      "path could not be scanned",
		FirstSeenAt:  now.Add(-time.Hour),
		LastSeenAt:   now.Add(-time.Hour),
	}
	stale := Finding{
		RootKey:     "notes",
		Kind:        FindingRootUnavailable,
		Status:      FindingStatusOpen,
		Summary:     "watched root path is not reachable",
		FirstSeenAt: now.Add(-time.Hour),
		LastSeenAt:  now.Add(-time.Hour),
	}
	ignored := Finding{
		RootKey:      "notes",
		Kind:         FindingPathCollisionWarning,
		RelativePath: "Case.md",
		Status:       FindingStatusIgnored,
		Summary:      "ignored by operator",
		FirstSeenAt:  now.Add(-time.Hour),
		LastSeenAt:   now.Add(-time.Hour),
	}
	for _, finding := range []Finding{current, stale, ignored} {
		if err := store.SaveFinding(finding); err != nil {
			t.Fatalf("SaveFinding failed: %v", err)
		}
	}

	resolvedAt := now
	resolved, err := store.ResolveMissingFindings("notes", []Finding{current}, resolvedAt)
	if err != nil {
		t.Fatalf("ResolveMissingFindings failed: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("resolved = %d, want 1", resolved)
	}
	findings, err := store.ListFindings("notes", 0)
	if err != nil {
		t.Fatalf("ListFindings failed: %v", err)
	}
	byKind := map[string]Finding{}
	for _, finding := range findings {
		byKind[finding.Kind] = finding
	}
	if byKind[FindingPermissionDenied].Status != FindingStatusOpen {
		t.Fatalf("current finding should remain open: %#v", byKind[FindingPermissionDenied])
	}
	if byKind[FindingRootUnavailable].Status != FindingStatusResolved {
		t.Fatalf("stale finding should be resolved: %#v", byKind[FindingRootUnavailable])
	}
	if byKind[FindingPathCollisionWarning].Status != FindingStatusIgnored {
		t.Fatalf("ignored finding should be preserved: %#v", byKind[FindingPathCollisionWarning])
	}
}

func TestProjectWatchStateIsScopedAndKeepsPendingRevision(t *testing.T) {
	store := NewStore(t.TempDir())
	first := ProjectWatchReconcileState{OperationID: "job_01ARZ3NDEKTSV4RRFFQ69G5FAV", TokenHash: "sha256:" + strings.Repeat("a", 64), ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", ProjectRoot: "/fixture/project-a", SafeRootKey: "project-a", PendingRevision: 3, PendingHash: "sha256:" + strings.Repeat("b", 64), RootKeys: []string{"root-a"}}
	if err := store.SaveProjectWatchReconcileState(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	second.ProjectRoot = "/fixture/project-b"
	second.SafeRootKey = "project-b"
	second.RootKeys = []string{"root-b"}
	if err := store.SaveProjectWatchReconcileState(second); err != nil {
		t.Fatal(err)
	}
	restored, err := NewStore(store.DataDir).LoadProjectWatchReconcileState(first.ProjectID)
	if err != nil || restored.PendingRevision != 3 || restored.AppliedRevision != 0 || len(restored.RootKeys) != 1 || restored.RootKeys[0] != "root-a" {
		t.Fatalf("scoped pending state: %+v %v", restored, err)
	}
	first.ProjectID = "../../escape"
	if err = store.SaveProjectWatchReconcileState(first); err == nil {
		t.Fatal("invalid state filename accepted")
	}
}

func TestProjectWatchStateEncodingBound(t *testing.T) {
	store := NewStore(t.TempDir())
	state := ProjectWatchReconcileState{OperationID: "job_01ARZ3NDEKTSV4RRFFQ69G5FAV", TokenHash: "sha256:" + strings.Repeat("a", 64), ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", ProjectRoot: "/fixture/project", SafeRootKey: "declaration_project_fixture", PendingRevision: 1, PendingHash: "sha256:" + strings.Repeat("b", 64), RootKeys: []string{"root"}}
	if err := store.SaveProjectWatchReconcileState(state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.DataDir, "watched-roots", "projects", state.ProjectID+".json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{append(append([]byte{}, original...), []byte(` {}`)...), []byte(strings.Repeat(" ", ProjectWatchReconcileStateMaxBytes+1)), []byte(strings.Replace(string(original), `"root_keys"`, `"unknown":true,"root_keys"`, 1))} {
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = store.LoadProjectWatchReconcileState(state.ProjectID); err == nil {
			t.Fatal("invalid state encoding accepted")
		}
	}
	if err = os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	state.ProjectRoot = "/" + strings.Repeat("x", ProjectWatchReconcileStateMaxBytes)
	if err = store.SaveProjectWatchReconcileState(state); err == nil {
		t.Fatal("oversized persisted state accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(original) != string(after) {
		t.Fatal("invalid state replaced original")
	}
}
