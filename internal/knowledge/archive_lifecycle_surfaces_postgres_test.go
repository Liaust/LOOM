package knowledge

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"loom.local/loom/internal/notesprojection"
)

func archiveSurfaceCitation(t *testing.T, f *notesArchivePostgresFixture, object KnowledgeObject) NotesPassageInput {
	t.Helper()
	input := NotesPassageInput{KnowledgeObjectID: object.KnowledgeObjectID, SourceHash: object.SourceHash}
	if err := f.s.store.db.QueryRow(`SELECT knowledge_object_version_id,knowledge_chunk_id FROM knowledge.knowledge_chunks
	 WHERE knowledge_object_id=$1 AND metadata->>'text_source'<>'metadata_text' ORDER BY chunk_index LIMIT 1`, object.KnowledgeObjectID).
		Scan(&input.KnowledgeObjectVersionID, &input.KnowledgeChunkID); err != nil {
		t.Fatal(err)
	}
	return input
}

func TestNotesArchiveSurfacesActiveArchivedRestorePostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	f.s.store.db.SetMaxOpenConns(1)
	var object KnowledgeObject
	for _, item := range f.objects {
		if item.RelativePath == "topic-one/source.md" {
			object = item
		}
	}
	citation := archiveSurfaceCitation(t, f, object)
	before, err := f.s.GetNotesPassage(t.Context(), citation)
	if err != nil {
		t.Fatal(err)
	}
	f.archive(t)
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []SourceLifecycleFilter{"", SourceLifecycleFilterActive, SourceLifecycleFilterArchived, SourceLifecycleFilterAll} {
		want, active, archived := 3, 3, 0
		if filter == SourceLifecycleFilterArchived {
			want, active, archived = 2, 0, 2
		}
		if filter == SourceLifecycleFilterAll {
			want, active, archived = 5, 3, 2
		}
		list, err := f.s.store.ListKnowledgeObjects(t.Context(), KnowledgeObjectFilter{SourceLifecycle: filter})
		if err != nil || len(list) != want {
			t.Fatalf("objects %s: %d %v", filter, len(list), err)
		}
		for _, item := range list {
			if item.NotesCustodyContext == nil || item.OriginalPath != item.SourcePath {
				t.Fatal("object custody missing")
			}
			if (item.SourceLifecycle == SourceLifecycleArchived) != strings.HasPrefix(item.RelativePath, "topic-one/") {
				t.Fatal("wrong object lifecycle")
			}
		}
		out, err := f.s.GetNotesOverview(t.Context(), NotesOverviewInput{SourceLifecycle: filter})
		if err != nil || out.Totals.ObjectCount != want || out.Totals.LifecycleCounts != (NotesLifecycleCounts{Active: active, Archived: archived}) {
			t.Fatalf("overview %s: %+v %v", filter, out.Totals, err)
		}
		roots, err := f.s.store.ListReadableSourceRoots(t.Context(), SourceRootFilter{SourceLifecycle: filter, NotesSourceRootID: f.root.NotesSourceRootID})
		if err != nil || len(roots) != 1 || roots[0].LifecycleCounts == nil || *roots[0].LifecycleCounts != (NotesLifecycleCounts{Active: 1, Archived: 2}) {
			t.Fatalf("shared root %s: %+v %v", filter, roots, err)
		}
		read, err := f.s.store.GetKnowledgeObjectWithLifecycle(t.Context(), object.KnowledgeObjectID, filter)
		included := filter == SourceLifecycleFilterArchived || filter == SourceLifecycleFilterAll
		if included && (err != nil || read.KnowledgeObjectID != object.KnowledgeObjectID || read.SourceLifecycle != SourceLifecycleArchived) {
			t.Fatalf("exact get: %+v %v", read, err)
		}
		if !included && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("default exact archive leak: %v", err)
		}
		citation.SourceLifecycle = filter
		passage, err := f.s.GetNotesPassage(t.Context(), citation)
		if included {
			if err != nil || passage.Text != before.Text || passage.ChunkHash != before.ChunkHash || passage.Historical || passage.Custody.SourceLifecycle != SourceLifecycleArchived || passage.Custody.OriginalPath != object.SourcePath {
				t.Fatalf("archive citation: %+v %v", passage, err)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("active passage archive leak: %v", err)
		}
	}
	projection, err := f.s.ListProjectionSources(t.Context(), notesprojection.SourceListInput{})
	if err != nil || len(projection) != 3 {
		t.Fatalf("active projection: %d %v", len(projection), err)
	}
	for _, item := range projection {
		if strings.HasPrefix(item.RelativePath, "topic-one/") {
			t.Fatal("archive materialized")
		}
	}
	// Current privacy still wins over an authenticated historical custody receipt.
	if _, err := f.s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='private_no_index' WHERE object_id=(SELECT metadata->'synced_object'->>'object_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`, object.KnowledgeObjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.store.GetKnowledgeObjectWithLifecycle(t.Context(), object.KnowledgeObjectID, SourceLifecycleFilterAll); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("private exact read: %v", err)
	}
	citation.SourceLifecycle = SourceLifecycleFilterArchived
	if _, err := f.s.GetNotesPassage(t.Context(), citation); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("private passage: %v", err)
	}
	counts, err := f.s.GetNotesOverview(t.Context(), NotesOverviewInput{SourceLifecycle: SourceLifecycleFilterArchived})
	if err != nil || counts.Totals.ObjectCount != 1 {
		t.Fatalf("private aggregate: %+v %v", counts.Totals, err)
	}
	if _, err := f.s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='text_later' WHERE object_id=(SELECT metadata->'synced_object'->>'object_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`, object.KnowledgeObjectID); err != nil {
		t.Fatal(err)
	}
	restore := f.restore(t)
	if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); err != nil {
		t.Fatal(err)
	}
	read, err := f.s.store.GetKnowledgeObjectWithLifecycle(t.Context(), object.KnowledgeObjectID, "")
	if err != nil || read.SourceLifecycle != SourceLifecycleActive || read.KnowledgeObjectID != object.KnowledgeObjectID {
		t.Fatalf("restore exact: %+v %v", read, err)
	}
	citation.SourceLifecycle = ""
	after, err := f.s.GetNotesPassage(t.Context(), citation)
	if err != nil || after.Text != before.Text || after.ChunkHash != before.ChunkHash || after.Custody.SourceLifecycle != SourceLifecycleActive {
		t.Fatalf("restore citation: %+v %v", after, err)
	}
	if f.processingSnapshot(t) != f.before {
		t.Fatal("reads changed processing/identity")
	}
}

func TestNotesArchiveSurfacesProjectInactiveRestorePostgres(t *testing.T) {
	for _, material := range []bool{false, true} {
		testNotesArchiveProjectCompletion(t, material, func(f *notesArchivePostgresFixture, object KnowledgeObject, lifecycle SourceLifecycle) {
			for _, filter := range []SourceLifecycleFilter{"", SourceLifecycleFilterArchived, SourceLifecycleFilterAll} {
				included, _ := SourceLifecycleIncluded(filter, lifecycle)
				want := 0
				if included {
					want = 1
				}
				list, err := f.s.store.ListKnowledgeObjects(t.Context(), KnowledgeObjectFilter{SourceLifecycle: filter, ProjectID: *object.ProjectID})
				if err != nil || len(list) != want {
					t.Fatalf("project objects %s/%s: %d %v", lifecycle, filter, len(list), err)
				}
				roots, err := f.s.store.ListReadableSourceRoots(t.Context(), SourceRootFilter{SourceLifecycle: filter, ProjectID: *object.ProjectID})
				if err != nil || len(roots) != want {
					t.Fatalf("project roots %s/%s: %d %v", lifecycle, filter, len(roots), err)
				}
				overview, err := f.s.GetNotesOverview(t.Context(), NotesOverviewInput{SourceLifecycle: filter, ProjectID: *object.ProjectID})
				if err != nil || overview.Totals.ObjectCount != want {
					t.Fatalf("project overview %s/%s: %+v %v", lifecycle, filter, overview.Totals, err)
				}
				citation := archiveSurfaceCitation(t, f, object)
				citation.SourceLifecycle = filter
				passage, err := f.s.GetNotesPassage(t.Context(), citation)
				if included {
					if err != nil || passage.Custody.SourceLifecycle != lifecycle || (material && passage.CurrentSourceContext.Declaration != "docs") {
						t.Fatalf("project passage: %+v %v", passage, err)
					}
				} else if !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("project passage inclusion: %v", err)
				}
			}
			projection, err := f.s.ListProjectionSources(t.Context(), notesprojection.SourceListInput{})
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range projection {
				if item.KnowledgeObjectID == object.KnowledgeObjectID {
					t.Fatal("disabled project writer materialized")
				}
			}
		})
	}
}
