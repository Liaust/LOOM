package knowledge

import (
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/storagearchive"
)

func TestNotesCustodyInventoryMembershipNotPathPrefix(t *testing.T) {
	inventory := map[string]storagearchive.NoFollowInventoryEntry{
		"notes/current.md": {Kind: storagearchive.InventoryEntryFile},
	}
	for _, test := range []struct {
		name, path, relative  string
		bound, member, absent bool
	}{
		{"retained_rename", "/box/topic/notes/old.md", "old.md", true, false, true},
		{"present_still_validated", "/box/topic/notes/current.md", "current.md", true, false, false},
		{"prior_member_missing_still_refuses", "/box/topic/notes/old.md", "old.md", true, true, false},
		{"unbound_not_silently_skipped", "/box/topic/notes/old.md", "old.md", false, false, false},
		{"outside_not_silently_skipped", "/box/outside.md", "old.md", true, false, false},
		{"root_not_a_file_member", "/box/topic", "..", true, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := notesCustodyCandidate{Object: KnowledgeObject{SourcePath: test.path, RelativePath: test.relative},
				Root: SourceRoot{SourcePath: "/box/topic/notes"}, Bound: test.bound, ArchiveMember: test.member}
			if got := notesCustodyAbsentFromInventory(c, "/box/topic", inventory); got != test.absent {
				t.Fatalf("absent=%v, want %v", got, test.absent)
			}
		})
	}
}

func TestNotesArchiveRetainedRenameMembershipPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	source := f.plan.Source.Path.AbsolutePath
	oldPath := filepath.Join(source, "source.md")
	var oldID string
	for _, object := range f.objects {
		if object.SourcePath == oldPath {
			oldID = object.KnowledgeObjectID
		}
	}
	if oldID == "" {
		t.Fatal("fixture lacks retained source")
	}
	if err := os.Rename(oldPath, filepath.Join(source, "renamed.md")); err != nil {
		t.Fatal(err)
	}
	plan, err := f.p.Workspace.PlanArchive(t.Context(), storagearchive.WorkspaceArchivePlanInput{
		Kind: f.plan.Kind, ObjectID: f.plan.ObjectID, Slug: f.plan.Slug, ActorID: f.actor, Reason: "archive current files after rename"})
	if err != nil {
		t.Fatal(err)
	}
	f.plan = plan
	f.archive(t)
	projected, err := f.p.ProjectOperation(t.Context(), plan.OperationID)
	if err != nil || projected.Projected != 1 || projected.NotMoved != 1 {
		t.Fatalf("renamed history blocked current payload: %+v %v", projected, err)
	}
	var history int
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_custody_transitions WHERE knowledge_object_id=$1`, oldID).Scan(&history); err != nil || history != 0 {
		t.Fatalf("absent file received false archive custody: %d %v", history, err)
	}
	var caughtUp bool
	readFence := `SELECT ` + notesCustodyReadCaughtUpSQL("o") + ` FROM knowledge.knowledge_objects o WHERE knowledge_object_id=$1`
	if err := f.s.store.db.QueryRow(readFence, oldID).Scan(&caughtUp); err != nil || caughtUp {
		t.Fatalf("unmoved old path became available while archived: %v %v", caughtUp, err)
	}
	restored := f.restore(t)
	projected, err = f.p.ProjectOperation(t.Context(), restored.OperationID)
	if err != nil || projected.Projected != 1 || projected.NotMoved != 1 {
		t.Fatalf("restore projected absent history: %+v %v", projected, err)
	}
	if err := f.s.store.db.QueryRow(readFence, oldID).Scan(&caughtUp); err != nil || !caughtUp {
		t.Fatalf("restored nonmember has a permanent pending-event fence: %v %v", caughtUp, err)
	}
	if f.processingSnapshot(t) != f.before {
		t.Fatal("membership projection rewrote retained source/publication history")
	}
}
