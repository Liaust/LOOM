package knowledge

import (
	"encoding/json"
	"strings"
	"testing"
)

func processingWithoutRoots(t *testing.T, snapshot string) string {
	t.Helper()
	var parts []json.RawMessage
	if err := json.Unmarshal([]byte(snapshot), &parts); err != nil || len(parts) != 8 {
		t.Fatalf("invalid processing snapshot: %v", err)
	}
	data, err := json.Marshal(parts[:7])
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertNotesCustodyReadable(t *testing.T, s *Service, id string, want bool) {
	t.Helper()
	var got bool
	if err := s.store.db.QueryRow(`SELECT `+visibleNotesCustodyObjectSQL("o", false)+` FROM knowledge.knowledge_objects o WHERE knowledge_object_id=$1`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		var diagnostic []byte
		_ = s.store.db.QueryRow(`SELECT jsonb_build_object('root_status',r.status,'custody',EXISTS (`+notesCurrentCustodySQL("o")+`),'archive_stop',`+notesProjectArchiveStopSQL("r", "o", notesReadArchiveOperationSQL("o"))+`) FROM knowledge.knowledge_objects o JOIN knowledge.notes_source_roots r USING(notes_source_root_id) WHERE o.knowledge_object_id=$1`, id).Scan(&diagnostic)
		t.Logf("disposable read diagnostic: %s", diagnostic)
		t.Fatalf("custody read for %s: %t, want %t", id, got, want)
	}
}

func assertNotesCustodyReadRevocations(t *testing.T, s *Service, object KnowledgeObject, mutations map[string]string) {
	t.Helper()
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			tx, err := s.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(mutation, object.KnowledgeObjectID); err != nil {
				t.Fatal(err)
			}
			var readable bool
			if err := tx.QueryRow(`SELECT `+visibleNotesCustodyObjectSQL("o", false)+` FROM knowledge.knowledge_objects o WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&readable); err != nil {
				t.Fatal(err)
			}
			if readable {
				t.Fatal("current revocation exposed retained Notes")
			}
		})
	}
}

func TestNotesArchiveReadCurrentEvidencePostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	var object KnowledgeObject
	for _, item := range f.objects {
		if item.RelativePath == "topic-one/source.md" {
			object = item
		}
	}
	assertNotesCustodyReadable(t, f.s, object.KnowledgeObjectID, true)
	f.archive(t)
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	assertNotesCustodyReadable(t, f.s, object.KnowledgeObjectID, true)
	var raw []byte
	if err := f.s.store.db.QueryRow(`SELECT `+notesCustodyContextSQL("o")+` FROM knowledge.knowledge_objects o WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var context NotesCustodyContext
	if err := json.Unmarshal(raw, &context); err != nil {
		t.Fatal(err)
	}
	if context.SourceLifecycle != "archived" || context.OriginalPath != object.SourcePath || context.CanonicalPath == object.SourcePath || !strings.HasPrefix(context.CanonicalPath, f.plan.Destination.Path.AbsolutePath+"/") || context.ArchiveOperationID != f.plan.OperationID || context.ArchivedAt == nil || context.OccurredAt == nil {
		t.Fatalf("custody context: %+v", context)
	}
	assertNotesCustodyReadRevocations(t, f.s, object, map[string]string{
		"missing_pointer": `DELETE FROM knowledge.notes_current_custody WHERE knowledge_object_id=$1`,
		"deleted":         `UPDATE knowledge.knowledge_objects SET deleted_at=now() WHERE knowledge_object_id=$1`,
		"root_disabled":   `UPDATE knowledge.notes_source_roots SET status='disabled' WHERE notes_source_root_id=(SELECT notes_source_root_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"declaration":     `UPDATE knowledge.notes_source_roots SET metadata=jsonb_set(metadata,'{registration_metadata,knowledge_source,enabled}','false') WHERE notes_source_root_id=(SELECT notes_source_root_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"private":         `UPDATE files.file_metadata SET index_policy='private_no_index' WHERE object_id=(SELECT metadata->'synced_object'->>'object_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"metadata":        `UPDATE files.file_metadata SET metadata='{"private_no_index":true}' WHERE object_id=(SELECT metadata->'synced_object'->>'object_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"stale_replica":   `UPDATE sync.replicas SET freshness_state='stale' WHERE replica_id=(SELECT metadata->'synced_object'->>'replica_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
		"scope":           `UPDATE objects.object_scope_links SET relevance_status='inactive' WHERE object_id=(SELECT metadata->'synced_object'->>'object_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`,
	})
	restore := f.restore(t)
	if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); err != nil {
		t.Fatal(err)
	}
	assertNotesCustodyReadable(t, f.s, object.KnowledgeObjectID, true)
	assertNotesCustodyReadRevocations(t, f.s, object, map[string]string{
		"old_pointer": `UPDATE knowledge.notes_current_custody c SET workspace_lifecycle_event_id=t.workspace_lifecycle_event_id FROM knowledge.notes_custody_transitions t WHERE c.knowledge_object_id=$1 AND t.knowledge_object_id=c.knowledge_object_id AND t.previous_event_id IS NULL`,
	})
	f.assertCustody(t, "active", 4)
}
