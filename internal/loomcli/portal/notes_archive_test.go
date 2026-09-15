package portal

import (
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/knowledge"
)

func TestNotesArchivePortalRequestIdentity(t *testing.T) {
	active := notesSearchInputFromQuery("cobalt lifecycle:active")
	archived := notesSearchInputFromQuery("cobalt lifecycle:archived")
	all := notesSearchInputFromQuery("cobalt lifecycle:all")
	if active.SourceLifecycle != knowledge.SourceLifecycleFilterActive || archived.SourceLifecycle != knowledge.SourceLifecycleFilterArchived || all.SourceLifecycle != knowledge.SourceLifecycleFilterAll {
		t.Fatal("lifecycle token lost")
	}
	if notesSearchInputEqual(active, archived) || notesSearchInputEqual(archived, all) || !notesSearchInputEqual(all, all) {
		t.Fatal("stale lifecycle response identity")
	}
	changed := all
	changed.SourceCategory = "topics"
	if notesSearchInputEqual(all, changed) {
		t.Fatal("stale category response identity")
	}
}

func TestNotesArchivePortalLabelsInspectAndScroll(t *testing.T) {
	when := time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)
	active := knowledge.NotesSearchResult{KnowledgeObjectID: "object_active", Title: "Active title", NotesCustodyContext: knowledge.NotesCustodyContext{SourceLifecycle: knowledge.SourceLifecycleActive}}
	archived := knowledge.NotesSearchResult{KnowledgeObjectID: "object_archived", Title: "Archived title", NotesCustodyContext: knowledge.NotesCustodyContext{SourceLifecycle: knowledge.SourceLifecycleArchived, OriginalPath: "Box/original", CanonicalPath: "Archive/canonical", ArchiveOperationID: "archive_exact", ArchivedAt: &when}}
	data := NotesSearchData{Query: "cobalt", Status: ScreenLoadLoaded, ResultSet: knowledge.NotesSearchResultSet{Query: "cobalt", SourceLifecycle: knowledge.SourceLifecycleFilterAll, ResultCount: 2, Results: []knowledge.NotesSearchResult{active, archived}, LifecycleGroups: []knowledge.NotesSearchLifecycleGroup{{SourceLifecycle: knowledge.SourceLifecycleActive, Offset: 0, ResultCount: 1}, {SourceLifecycle: knowledge.SourceLifecycleArchived, Offset: 1, ResultCount: 1}}}}
	out := RenderNotesSearchWithSelection(testMode(), data.Query, data, 1, 160, 60)
	for _, want := range []string{"active Notes", "archived Notes", "lifecycle=all", "lifecycle=archived", "Archive/canonical", "2026-09-10T01:02:03Z"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
	if strings.Index(out, "active Notes") >= strings.Index(out, "archived Notes") {
		t.Fatal("groups reordered")
	}
	if notesSearchResultLineCount(archived, false) != notesSearchResultLineCount(active, false)+1 {
		t.Fatal("custody scroll line unaccounted")
	}
	if notesSearchSelectedBodyLine(data, 1) != 3+2+notesSearchResultLineCount(active, false) {
		t.Fatal("group headings not counted")
	}
	action := NewNotesSearchResultInspectAction(archived)
	if strings.Join(action.RawCommand, " ") != "loom notes objects show object_archived --source-lifecycle archived" || action.Executor.Payload["source_lifecycle"] != "archived" || action.Executor.Payload["original_path"] != "Box/original" || action.Executor.Payload["archive_operation_id"] != "archive_exact" {
		t.Fatalf("inspect identity: %+v", action)
	}
	roots := notesVisibleRoots(knowledge.NotesOverview{Nodes: []knowledge.NotesNodeOverview{{Roots: []knowledge.NotesRootOverview{{NotesSourceRootID: "restored", Status: knowledge.SourceRootStatusDisabled, Totals: knowledge.NotesOverviewTotals{LifecycleCounts: knowledge.NotesLifecycleCounts{Active: 1}}}}}}}, false)
	if len(roots) != 1 || roots[0].Status != knowledge.SourceRootStatusDisabled {
		t.Fatal("readable inactive restore hidden or writer reactivated")
	}
}
