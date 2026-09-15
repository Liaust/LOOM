package projects

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/requestctx"
)

func projectRestoreStates() (ProjectPhysicalArchiveState, []ProjectPhysicalRestoreState) {
	at := time.Date(2026, 9, 9, 10, 0, 0, 123456000, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	archive := ProjectPhysicalArchiveState{
		SchemaVersion: ProjectPhysicalArchiveStateSchemaVersion, Status: ProjectPhysicalArchiveStatusArchived, Phase: ProjectArchivePhaseComplete, MutationBlocked: true,
		ProjectID: "project_test", ProjectSlug: "restore-test", OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		PlanDigest: digest, WorkspacePlanDigest: digest, ActivePath: "/fixture/active", ArchivePath: "/fixture/archive",
		RuntimeQuiescenceDigest: digest, ArchiveManifestDigest: digest, ActorID: "actor_archive", Reason: "archive fixture",
		StartedAt: at, RuntimeDeactivatedAt: &at, WorkspaceCompletedAt: &at, ArchivedAt: &at,
	}
	start, workspace, complete := at.Add(time.Second), at.Add(2*time.Second), at.Add(3*time.Second)
	pending := ProjectPhysicalRestoreState{
		SchemaVersion: ProjectPhysicalRestoreStateSchemaVersion, Request: requestctx.Context{ActorID: "actor_restore", OriginNodeID: "node_main", CorrelationID: "corr_restore"},
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ", PlanDigest: digest, WorkspacePlanDigest: digest,
		Phase: ProjectRestorePhasePending, Status: "in_progress", MutationBlocked: true, ActivationState: "inactive", StartedAt: start,
	}
	moved := pending
	moved.Phase, moved.WorkspaceCompletedAt, moved.ActiveManifestDigest = ProjectRestorePhaseProjectStatePending, &workspace, digest
	completed := moved
	completed.Phase, completed.Status, completed.RestoredAt = ProjectRestorePhaseComplete, "restored", &complete
	completed.EventID = "event_01ARZ3NDEKTSV4RRFFQ69G5FAZ"
	return archive, []ProjectPhysicalRestoreState{pending, moved, completed}
}

func TestProjectPhysicalRestoreStateAndHistory(t *testing.T) {
	archive, states := projectRestoreStates()
	original, _ := json.Marshal(archive)
	if strings.Contains(string(original), `"restore":`) {
		t.Fatal("fixture unexpectedly includes restore")
	}
	for _, state := range states {
		if err := ValidateProjectPhysicalRestoreState(archive, state); err != nil {
			t.Fatal(err)
		}
		archive.Restore = &state
		payload, _ := json.Marshal(archive)
		decoded, ok := ParseProjectPhysicalArchiveState(payload)
		if !ok || !reflect.DeepEqual(decoded, archive) {
			t.Fatal("nested restore did not roundtrip")
		}
		decoded.Restore = nil
		history, _ := json.Marshal(decoded)
		if string(history) != string(original) {
			t.Fatal("archive history encoding changed")
		}
		if !errors.Is(EnsureProjectMutable(Project{ProjectID: archive.ProjectID, Status: "active", ArchiveState: payload}, "write", "fixture"), ErrProjectRestoreBlocked) {
			t.Fatal("restore reopened project mutations")
		}
		if !errors.Is((runtimeStatus{ProjectID: archive.ProjectID, ProjectStatus: "active", ScopeStatus: "active", ArchiveState: payload}).archivedError(RuntimeRef{}), ErrProjectRestoreBlocked) {
			t.Fatal("restore reopened runtime")
		}
	}
}

func TestProjectPhysicalRestoreStateRejectsInvalidEvidence(t *testing.T) {
	archive, states := projectRestoreStates()
	mutations := map[string]func(*ProjectPhysicalRestoreState){
		"schema":         func(s *ProjectPhysicalRestoreState) { s.SchemaVersion += "bad" },
		"actor":          func(s *ProjectPhysicalRestoreState) { s.Request.ActorID = "" },
		"origin":         func(s *ProjectPhysicalRestoreState) { s.Request.OriginNodeID = " node" },
		"correlation":    func(s *ProjectPhysicalRestoreState) { s.Request.CorrelationID = "" },
		"operation":      func(s *ProjectPhysicalRestoreState) { s.OperationID = archive.OperationID },
		"plan":           func(s *ProjectPhysicalRestoreState) { s.PlanDigest = "bad" },
		"workspace plan": func(s *ProjectPhysicalRestoreState) { s.WorkspacePlanDigest = "bad" },
		"mutation":       func(s *ProjectPhysicalRestoreState) { s.MutationBlocked = false },
		"activation":     func(s *ProjectPhysicalRestoreState) { s.ActivationState = "active" },
		"phase":          func(s *ProjectPhysicalRestoreState) { s.Phase = "invented" },
		"status":         func(s *ProjectPhysicalRestoreState) { s.Status = "in_progress" },
		"start":          func(s *ProjectPhysicalRestoreState) { s.StartedAt = archive.StartedAt.Add(-time.Second) },
		"submicrosecond": func(s *ProjectPhysicalRestoreState) { s.StartedAt = s.StartedAt.Add(time.Nanosecond) },
		"workspace time": func(s *ProjectPhysicalRestoreState) { s.WorkspaceCompletedAt = nil },
		"manifest":       func(s *ProjectPhysicalRestoreState) { s.ActiveManifestDigest = "" },
		"complete time":  func(s *ProjectPhysicalRestoreState) { s.RestoredAt = &archive.StartedAt },
		"event":          func(s *ProjectPhysicalRestoreState) { s.EventID = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			state := states[2]
			mutate(&state)
			if err := ValidateProjectPhysicalRestoreState(archive, state); err == nil {
				t.Fatal("invalid restore evidence accepted")
			}
		})
	}
	for _, index := range []int{0, 1} {
		state := states[index]
		state.EventID = states[2].EventID
		if err := ValidateProjectPhysicalRestoreState(archive, state); err == nil {
			t.Fatal("early completion evidence accepted")
		}
	}
	state := states[0]
	state.WorkspaceCompletedAt = states[1].WorkspaceCompletedAt
	if err := ValidateProjectPhysicalRestoreState(archive, state); err == nil {
		t.Fatal("early workspace evidence accepted")
	}
}

func TestProjectPhysicalRestoreAdjacentReplay(t *testing.T) {
	_, states := projectRestoreStates()
	for i := range states {
		if err := ValidateProjectPhysicalRestoreTransition(states[i], states[i]); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if err := ValidateProjectPhysicalRestoreTransition(states[i], states[i+1]); err != nil {
				t.Fatal(err)
			}
		}
		next := states[i]
		next.Request.CorrelationID += "drift"
		if err := ValidateProjectPhysicalRestoreTransition(states[i], next); err == nil {
			t.Fatal("request change accepted")
		}
	}
	if err := ValidateProjectPhysicalRestoreTransition(states[0], states[2]); err == nil {
		t.Fatal("phase skip accepted")
	}
	if err := ValidateProjectPhysicalRestoreTransition(states[2], states[0]); err == nil {
		t.Fatal("phase regression accepted")
	}
	next := states[2]
	next.ActiveManifestDigest = "sha256:" + strings.Repeat("b", 64)
	if err := ValidateProjectPhysicalRestoreTransition(states[1], next); err == nil {
		t.Fatal("workspace rewrite accepted")
	}
	next = states[2]
	next.EventID = "event_01ARZ3NDEKTSV4RRFFQ69G5FAY"
	if err := ValidateProjectPhysicalRestoreTransition(states[2], next); err == nil {
		t.Fatal("event replay rewrite accepted")
	}
}
