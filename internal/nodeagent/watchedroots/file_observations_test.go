package watchedroots

import (
	"testing"
	"time"

	"loom.local/loom/internal/ids"
)

func TestFileObservationPlannerNeedsNoContentAndDoesNotRepeat(t *testing.T) {
	now := time.Now().UTC()
	root := ValidatedRoot{Config: RootConfig{RootKey: "notes", DeletePolicy: DeletePolicy{Mode: DeleteModeIgnore}},
		ConfigHash: "root-generation", RootReachable: true}
	state := PathState{RelativePath: "large.pdf", Status: PathStatusIncluded, Kind: PathKindFile,
		SizeBytes: 2 << 30, ModifiedAt: &now, HashStatus: HashStatusDeferred}
	first, ok := planFileObservation(root, state)
	if !ok || first.SourcePresence != "present" || first.ContentHashURI != "" || first.LocalVersionID != "" {
		t.Fatalf("metadata required a content version: %+v", first)
	}
	if err := ids.Validate("object", first.LocalObjectID); err != nil {
		t.Fatal(err)
	}
	state.LocalObjectID, state.LastQueuedFileObservation = first.LocalObjectID, first.ObservationFingerprint
	state.LastSeenAt = now.Add(time.Hour)
	if _, ok := planFileObservation(root, state); ok {
		t.Fatal("unchanged scan queued another observation")
	}
	state.Status = PathStatusDeleted
	missing, ok := planFileObservation(root, state)
	if !ok || missing.SourcePresence != "missing" || missing.LocalObjectID != first.LocalObjectID {
		t.Fatalf("withdrawal lost identity or required backup deletion: %+v", missing)
	}
	if _, ok := planDeletionAction(root, state); ok {
		t.Fatal("presence observation expanded deletion authority")
	}
	root.RootReachable = false
	if _, ok := planFileObservation(root, state); ok {
		t.Fatal("offline root manufactured per-file deletion")
	}
	root.RootReachable = true
	for _, status := range []string{PathStatusExcluded, PathStatusMissingDeferred, PathStatusError} {
		state.Status = status
		if _, ok := planFileObservation(root, state); ok {
			t.Fatalf("incomplete/unadmitted path queued a deletion: %s", status)
		}
	}
	state.Classification.ReasonCode = ReasonSkippedPermissionDenied
	if unavailable, ok := planFileObservation(root, state); !ok || unavailable.SourcePresence != "unavailable" {
		t.Fatal("known unreadable file was not marked unavailable")
	}
}

func TestFileObservationPlanningRetainsCoverageAnchorButSendsCurrentAudit(t *testing.T) {
	store := NewStore(t.TempDir())
	config := NormalizeRootConfig(RootConfig{RootKey: "notes", DeletePolicy: DeletePolicy{Mode: DeleteModeIgnore}})
	root := ValidatedRoot{Config: config, ConfigHash: ConfigHash(config), RootReachable: true}
	now := time.Now().UTC()
	state := PathState{RootKey: "notes", RelativePath: "note.md", Status: PathStatusIncluded, Kind: PathKindFile, SizeBytes: 5, ModifiedAt: &now}
	first, ok := planFileObservation(root, state)
	if !ok {
		t.Fatal("missing initial observation")
	}
	state.LocalObjectID, state.LastQueuedFileObservation = first.LocalObjectID, first.ObservationFingerprint
	if err := store.SavePathState(state); err != nil {
		t.Fatal(err)
	}
	checkpoint := RootCheckpoint{ConfigHash: root.ConfigHash, ContentConfigHash: ContentConfigHash(config), ObservationConfigHash: root.ConfigHash}
	if err := store.SaveCheckpoint("notes", checkpoint); err != nil {
		t.Fatal(err)
	}
	config.Scan.FullRescanInterval = "12h"
	config.Scan.StabilityWindow = "4s"
	config.Watch.DebounceWindow = "9s"
	root.Config, root.ConfigHash = config, ConfigHash(config)
	if root.ConfigHash == checkpoint.ConfigHash || ContentConfigHash(config) != checkpoint.ContentConfigHash {
		t.Fatal("fixture did not distinguish timing from coverage")
	}
	plan := func() OutputPlan {
		t.Helper()
		out, err := PlanOutputs(store, root, "test")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := plan(); len(out.Actions) != 0 {
		t.Fatalf("timing-only change requeued unchanged observations: %+v", out)
	}
	checkpoint.ConfigHash = root.ConfigHash
	if err := store.SaveCheckpoint("notes", checkpoint); err != nil {
		t.Fatal(err)
	}
	if out := plan(); len(out.Actions) != 0 {
		t.Fatalf("advanced audit receipt lost the observation anchor: %+v", out)
	}
	modified := now.Add(time.Minute)
	state.ModifiedAt, state.SizeBytes = &modified, 6
	if err := store.SavePathState(state); err != nil {
		t.Fatal(err)
	}
	changed := plan()
	if len(changed.Actions) != 1 || changed.Actions[0].SourceGeneration != root.ConfigHash || changed.Actions[0].LocalObjectID != first.LocalObjectID {
		t.Fatalf("changed file lost current audit or object identity: %+v", changed)
	}
	anchored := root
	anchored.ObservationConfigHash = checkpoint.ObservationConfigHash
	want, ok := planFileObservation(anchored, state)
	if !ok || changed.Actions[0].ObservationFingerprint != want.ObservationFingerprint {
		t.Fatal("changed observation fingerprint did not retain coverage anchor")
	}
	state.LastQueuedFileObservation = changed.Actions[0].ObservationFingerprint
	if err := store.SavePathState(state); err != nil {
		t.Fatal(err)
	}
	config.Include = []string{"note.md"}
	root.Config, root.ConfigHash = config, ConfigHash(config)
	semantic := plan()
	if len(semantic.Actions) != 1 || semantic.Actions[0].SourceGeneration != root.ConfigHash || semantic.Actions[0].ObservationFingerprint == state.LastQueuedFileObservation {
		t.Fatalf("genuine coverage change reused the old anchor: %+v", semantic)
	}
}
