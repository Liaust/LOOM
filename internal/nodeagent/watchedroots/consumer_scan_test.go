package watchedroots

import (
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/filepolicy"
)

func TestIndependentSelectionRescansPreserveObservationAndBackupDeletion(t *testing.T) {
	for _, mode := range []string{ScanModeFull, ScanModeDirty, "subtree"} {
		t.Run(mode, func(t *testing.T) {
			dir, store := t.TempDir(), NewStore(t.TempDir())
			if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
				t.Fatal(err)
			}
			for _, relative := range []string{"folder/note.md", "folder/asset.pdf"} {
				if err := os.WriteFile(filepath.Join(dir, relative), []byte(relative), 0600); err != nil {
					t.Fatal(err)
				}
			}
			config := independentConsumerConfig()
			root := testScanRoot(t, dir, config)
			scan := func(mode string) ScanResult {
				t.Helper()
				result, err := Reconcile(t.Context(), store, ScanRequest{Mode: mode, Root: root})
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			scan(ScanModeFull)
			state, err := store.LoadPathState("notes", "folder/asset.pdf")
			if err != nil {
				t.Fatal(err)
			}
			state.LocalObjectID, state.MainObjectID = "local-object", "main-object"
			if err := store.SavePathState(state); err != nil {
				t.Fatal(err)
			}
			beforeHash := state.ContentHashURI
			config.ConsumerSelections = &ConsumerSelections{
				Sync: &PathSelection{Include: []string{"folder/note.md"}}, Index: &PathSelection{Include: []string{"folder/note.md"}},
			}
			root = testScanRoot(t, dir, config)
			// A stale row must not leak its previous sync/index policy even before
			// the policy-change sweep reaches that path.
			assertIndependentBinaryPlan(t, planOutputActions(root, state), false)
			result := scan(ScanModeAuto)
			if result.Mode != ScanModeFull || result.Counts.Deleted != 0 || result.Counts.MissingDeferred != 0 {
				t.Fatalf("selector change inferred source deletion: %+v", result)
			}
			state, err = store.LoadPathState("notes", "folder/asset.pdf")
			if err != nil {
				t.Fatal(err)
			}
			if state.Status != PathStatusIncluded || state.DeletedAt != nil || state.ContentHashURI != beforeHash || state.Classification.Policies.Sync != SyncModeNone || state.Classification.Policies.Index != IndexModeNone || state.LocalObjectID != "local-object" {
				t.Fatalf("consumer exclusion changed source identity/admission or retained stale policies: %+v", state)
			}
			assertIndependentBinaryPlan(t, planOutputActions(root, state), false)
			if err := os.Remove(filepath.Join(dir, "folder/asset.pdf")); err != nil {
				t.Fatal(err)
			}
			requestMode := mode
			if mode != ScanModeFull {
				hint := DirtyHint{RelativePath: "folder/asset.pdf", HintKind: DirtyHintDeleted}
				if mode == "subtree" {
					requestMode = ScanModeDirty
					hint = DirtyHint{RelativePath: "folder", HintKind: DirtyHintSubtree, Generation: "removed-asset"}
				}
				if err := store.AddDirtyHint("notes", hint); err != nil {
					t.Fatal(err)
				}
			}
			result = scan(requestMode)
			if result.Counts.Deleted != 1 {
				t.Fatalf("real source deletion was lost: %+v", result.Counts)
			}
			state, err = store.LoadPathState("notes", "folder/asset.pdf")
			if err != nil {
				t.Fatal(err)
			}
			assertIndependentBinaryPlan(t, planOutputActions(root, state), true)
		})
	}
}

func assertIndependentBinaryPlan(t *testing.T, actions []OutputAction, deleted bool) {
	t.Helper()
	wantBackup := OutputActionBackupFile
	if deleted {
		wantBackup = OutputActionBackupDeletionMarker
	}
	foundBackup := false
	for _, action := range actions {
		switch action.ActionKind {
		case OutputActionSyncObject, OutputActionSyncMetadata, OutputActionDeletionRequest:
			t.Fatalf("sync-excluded file generated sync/global deletion: %+v", action)
		case wantBackup:
			foundBackup = true
		}
	}
	if !foundBackup {
		t.Fatalf("sync/index exclusion dropped backup output %s: %+v", wantBackup, actions)
	}
}

func TestIndependentSelectionPlannerUsesCurrentBackupPolicy(t *testing.T) {
	root := testScanRoot(t, t.TempDir(), independentConsumerConfig())
	state := PathState{RelativePath: "note.md", Status: PathStatusIncluded, Kind: PathKindFile, ContentHashURI: "sha256:body",
		Classification: Classification{Policies: EffectivePolicies(root.Config, "note.md")}}
	root.Config.ConsumerSelections = &ConsumerSelections{Backup: &PathSelection{Include: []string{"elsewhere/**"}}}
	for _, status := range []string{PathStatusIncluded, PathStatusDeleted} {
		state.Status = status
		if _, ok := planBackupOutputAction(root, state); ok {
			t.Fatalf("stale stored backup decision escaped current selection: %s", status)
		}
	}
	root.Config.Include = []string{"elsewhere/**"}
	state.LocalObjectID = "known-object"
	if actions := planOutputActions(root, state); len(actions) != 0 {
		t.Fatalf("old deleted state escaped current observation envelope: %+v", actions)
	}
}

func TestIndependentDirectoryRuleConflictCannotPruneObservedChildren(t *testing.T) {
	for _, subtree := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "subtree"}[subtree], func(t *testing.T) {
			dir, store := t.TempDir(), NewStore(t.TempDir())
			if err := os.Mkdir(filepath.Join(dir, "ab"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "ab/note.md"), []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			config := independentConsumerConfig()
			root := testScanRoot(t, dir, config)
			if _, err := Reconcile(t.Context(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
				t.Fatal(err)
			}
			config.Files = &filepolicy.FolderPolicy{Rules: []filepolicy.FolderRule{
				{Path: "a*", Search: "none"}, {Path: "*b", Search: "text"},
			}}
			root = testScanRoot(t, dir, config)
			mode := ScanModeFull
			if subtree {
				mode = ScanModeDirty
				if err := store.AddDirtyHint("notes", DirtyHint{RelativePath: "ab", HintKind: DirtyHintSubtree, Generation: "policy-change"}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := Reconcile(t.Context(), store, ScanRequest{Mode: mode, Root: root})
			if err != nil {
				t.Fatal(err)
			}
			state, err := store.LoadPathState("notes", "ab/note.md")
			if err != nil || result.Counts.Deleted != 0 || state.Status != PathStatusIncluded {
				t.Fatalf("directory consumer conflict pruned source observation: %+v, %+v, %v", result.Counts, state, err)
			}
			if _, ok := planBackupOutputAction(root, state); !ok {
				t.Fatal("directory consumer conflict dropped unaffected descendant backup")
			}
		})
	}
}
