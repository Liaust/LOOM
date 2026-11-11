package knowledge

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestAdmissionScopesRetainUnchangedProgress(t *testing.T) {
	now := time.Now().UTC()
	roots := []SourceRoot{{NotesSourceRootID: "one", RootKind: RootKindBoxNotes, BackendRootKey: "notes"},
		{NotesSourceRootID: "two", RootKind: RootKindBoxTopics, BackendRootKey: "topics"}}
	generation, _ := admissionSourceGeneration(roots)
	cursor, _, err := prepareAdmissionScopes(roots, AdmissionCursor{Incremental: true, BaselineGeneration: generation}, generation, now)
	if err != nil || !cursor.RootCursors["one"].StorageDone || !cursor.RootCursors["two"].SyncedDone {
		t.Fatalf("completed legacy baseline was reset: %+v %v", cursor, err)
	}
	before, _ := json.Marshal(cursor)
	roots[1].SourcePath = "/changed"
	nextGeneration, _ := admissionSourceGeneration(roots)
	next, _, err := prepareAdmissionScopes(roots, cursor, nextGeneration, now)
	if err != nil || next.RootCursors["two"].StorageDone || !reflect.DeepEqual(next.RootCursors["one"], cursor.RootCursors["one"]) {
		t.Fatalf("one changed root reset unrelated progress: %+v %v", next, err)
	}
	after, _ := json.Marshal(cursor)
	if string(before) != string(after) {
		t.Fatal("preparing a page mutated its input checkpoint")
	}
	roots = roots[:1]
	pruned, _, err := prepareAdmissionScopes(roots, next, nextGeneration, now)
	if err != nil || len(pruned.RootCursors) != 1 {
		t.Fatal("removed root retained inventory work", err)
	}
	repaired, _, err := prepareAdmissionScopes(roots, pruned, nextGeneration, now.Add(16*time.Minute))
	if err != nil || !repaired.RootCursors["one"].StorageDone {
		t.Fatal("unchanged inventory restarted at the retired deadline", err)
	}
	repaired, _, err = prepareAdmissionScopes(roots, pruned, nextGeneration, now.Add(7*time.Hour))
	if err != nil || repaired.RootCursors["one"].StorageDone {
		t.Fatal("six-hour safety inventory was lost", err)
	}
}

func TestAdmissionRepairOnlyResetsDueRoot(t *testing.T) {
	now := time.Now().UTC()
	roots := []SourceRoot{{NotesSourceRootID: "one"}, {NotesSourceRootID: "two"}}
	generation, _ := admissionSourceGeneration(roots)
	cursor, _, err := prepareAdmissionScopes(roots, AdmissionCursor{BaselineGeneration: generation}, generation, now)
	if err != nil {
		t.Fatal(err)
	}
	due := cursor.RootCursors["one"]
	due.NextRepairAt = now.Add(-time.Second)
	cursor.RootCursors["one"] = due
	next, _, err := prepareAdmissionScopes(roots, cursor, generation, now)
	if err != nil || next.RootCursors["one"].StorageDone || !reflect.DeepEqual(next.RootCursors["two"], cursor.RootCursors["two"]) {
		t.Fatal("due repair reset another root", next, err)
	}
}

func TestAdmissionScopesRotateWithoutResettingProgress(t *testing.T) {
	roots := []SourceRoot{{NotesSourceRootID: "a"}, {NotesSourceRootID: "b"}, {NotesSourceRootID: "c"}}
	generation, _ := admissionSourceGeneration(roots)
	cursor, _, err := prepareAdmissionScopes(roots, AdmissionCursor{Incremental: true}, generation, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	state := cursor.RootCursors["a"]
	state.StorageAfter = "saved-page"
	cursor.RootCursors["a"] = state
	for _, after := range []string{"a", "b", "c", "between", "removed-root", ""} {
		cursor.InventoryRootAfter = after
		before, _ := json.Marshal(cursor)
		next, ids, err := prepareAdmissionScopes(roots, cursor, generation, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		want := map[string][]string{
			"a": {"b", "c", "a"}, "b": {"c", "a", "b"}, "c": {"a", "b", "c"},
			"between": {"c", "a", "b"}, "removed-root": {"a", "b", "c"}, "": {"a", "b", "c"},
		}[after]
		if !reflect.DeepEqual(ids, want) || next.RootCursors["a"].StorageAfter != "saved-page" {
			t.Fatalf("rotation lost fairness/progress after %q: %+v %+v", after, ids, next)
		}
		afterBytes, _ := json.Marshal(cursor)
		if string(before) != string(afterBytes) {
			t.Fatal("tentative rotation changed the input checkpoint")
		}
	}
}

func TestSourceRootUnchangedIgnoresOnlyObservationAndJSONEncoding(t *testing.T) {
	base := SourceRoot{NotesSourceRootID: "old", RootKind: RootKindBoxNotes,
		Metadata: json.RawMessage(`{"b":2,"a":1}`), AuthorizationMetadata: json.RawMessage(`{}`)}
	next := base
	next.NotesSourceRootID, next.UpdatedAt = "new", time.Now()
	next.Metadata = json.RawMessage(`{ "a": 1, "b": 2 }`)
	if !sourceRootUnchanged(base, next) {
		t.Fatal("unchanged root requires another write")
	}
	next.SourcePath = "/new"
	if sourceRootUnchanged(base, next) {
		t.Fatal("actual binding change was ignored")
	}
	next.SourcePath = base.SourcePath
	next.Metadata = json.RawMessage(`{"a":1,"b":2,"registration_metadata":{"knowledge_source":{"source_hash":"refreshed"}}}`)
	if sourceRootUnchanged(base, next) {
		t.Fatal("metadata-only authority refresh would skip root write and enrollment wake")
	}
}
