package knowledge

import (
	"encoding/json"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
)

func TestKnowledgeObjectSourceEquivalent(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 123456000, time.UTC)
	size := int64(12)
	base := KnowledgeObject{
		NotesSourceRootID: "root", RelativePath: "note.md", SourcePath: "/notes/note.md",
		StorageEntryID: stringPtrIfNotEmpty("entry"), SourceNodeID: stringPtrIfNotEmpty("node"),
		ProjectID: stringPtrIfNotEmpty("project"), SizeBytes: &size,
		SourceHash: "hash", SourceRevision: "revision", ProcessingState: ProcessingStateIndexed,
		PipelineKey: "pipeline", PipelineVersion: "v1", RecencyAt: now,
		SourceCreatedAt: &now, SourceModifiedAt: &now, LastProcessedAt: &now,
		Metadata:             json.RawMessage(`{"source_root":{"policy":true},"sequence":9007199254740992}`),
		AbsoluteTimeMetadata: json.RawMessage(`{"basis":"source"}`),
	}
	for name, mutate := range map[string]func(*KnowledgeObject){
		"title":         func(o *KnowledgeObject) { o.Title = "changed" },
		"content":       func(o *KnowledgeObject) { o.SourceHash = "new hash" },
		"version":       func(o *KnowledgeObject) { o.SourceRevision = "new version" },
		"locator":       func(o *KnowledgeObject) { o.SourcePath = "/new/note.md" },
		"storage":       func(o *KnowledgeObject) { o.StorageEntryID = nil },
		"owner":         func(o *KnowledgeObject) { o.SourceNodeID = nil },
		"project":       func(o *KnowledgeObject) { o.ProjectID = nil },
		"size":          func(o *KnowledgeObject) { o.SizeBytes = nil },
		"pipeline":      func(o *KnowledgeObject) { o.PipelineVersion = "v2" },
		"processing":    func(o *KnowledgeObject) { o.ProcessingState = ProcessingStateStale },
		"error":         func(o *KnowledgeObject) { o.LastErrorCode = "failed" },
		"source time":   func(o *KnowledgeObject) { o.SourceModifiedAt = nil },
		"recency":       func(o *KnowledgeObject) { o.RecencyAt = now.Add(time.Second) },
		"time metadata": func(o *KnowledgeObject) { o.AbsoluteTimeMetadata = json.RawMessage(`{}`) },
		"policy":        func(o *KnowledgeObject) { o.Metadata = json.RawMessage(`{"source_root":{"policy":false}}`) },
		"large sequence": func(o *KnowledgeObject) {
			o.Metadata = json.RawMessage(`{"source_root":{"policy":true},"sequence":9007199254740993}`)
		},
		"deletion": func(o *KnowledgeObject) { o.DeletedAt = &now },
	} {
		t.Run(name, func(t *testing.T) {
			next := base
			mutate(&next)
			if knowledgeObjectSourceEquivalent(base, next) {
				t.Fatal("effective change was treated as a replay")
			}
		})
	}
	next := base
	next.KnowledgeObjectID, next.CanonicalFileID = "fresh-id", "serving-id"
	next.CreatedAt, next.UpdatedAt, next.LastSeenAt = now.Add(time.Hour), now.Add(time.Hour), now.Add(time.Hour)
	next.Metadata = json.RawMessage(`{ "sequence": 9007199254740992, "source_root": {"policy": true} }`)
	sameInstant := now.In(time.FixedZone("offset", 3600)).Add(999 * time.Nanosecond)
	next.SourceModifiedAt = &sameInstant
	if !knowledgeObjectSourceEquivalent(base, next) {
		t.Fatal("read time, generated identity, JSON order or sub-microsecond precision forced a change")
	}

	base = observeSourceRefresh(nil, base, now)
	base.LastErrorCode, base.LastErrorMessage = "retained", "retained processing error"
	metadata := jsonObject(base.Metadata)
	metadata["text_pipeline"] = map[string]any{"version": "retained"}
	base.Metadata = mustJSON(t, metadata)
	next = base
	next.ProcessingState, next.PipelineKey = ProcessingStateMetadataOnly, KnowledgeObjectPipelineMetadata
	next.LastProcessedAt, next.LastErrorCode, next.LastErrorMessage = nil, "", ""
	delete(metadata, "source_refresh")
	delete(metadata, "text_pipeline")
	next.Metadata = mustJSON(t, metadata)
	next = preserveKnowledgeObjectProcessing(base, next)
	next = observeSourceRefresh(&base, next, now.Add(time.Hour))
	if !knowledgeObjectSourceEquivalent(base, next) {
		t.Fatal("preserved processing metadata or refresh clock forced a change")
	}
}

func TestKnowledgeObjectSourceNoopPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	input := KnowledgeObjectReconcileInput{SourceRoots: roots, SyncedObjects: entries}
	first, err := s.ReconcileKnowledgeObjects(t.Context(), input)
	if err != nil || first.Changed != len(entries) || first.Unchanged != 0 || len(entries) == 0 {
		t.Fatalf("initial reconcile: %+v %v", first, err)
	}
	object := first.KnowledgeObjects[0]
	// Include physical tuple identity so equal timestamps alone cannot hide an UPDATE.
	snapshot := func() string {
		t.Helper()
		var value string
		err := s.store.db.QueryRow(`SELECT concat_ws('/',ko.xmin::text,ko.ctid::text,row_to_json(ko)::text,
		 (SELECT string_agg(d.xmin::text||':'||d.ctid::text,',' ORDER BY d.search_document_id)
		 FROM search.search_documents d WHERE d.source_id=ko.knowledge_object_id),
		 (SELECT string_agg(l.xmin::text||':'||l.ctid::text,',' ORDER BY l.search_document_id)
		 FROM search.lexical_documents l JOIN search.search_documents d USING(search_document_id)
		 WHERE d.source_id=ko.knowledge_object_id))
		 FROM knowledge.knowledge_objects ko WHERE ko.knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	for i := range input.SyncedObjects {
		input.SyncedObjects[i].LastSeenAt = time.Now().UTC().Add(time.Hour)
	}
	replay, err := s.ReconcileKnowledgeObjects(t.Context(), input)
	if err != nil || replay.Applied != len(entries) || replay.Changed != 0 || replay.Unchanged != len(entries) {
		t.Fatalf("replay reconcile: applied=%d changed=%d unchanged=%d: %v", replay.Applied, replay.Changed, replay.Unchanged, err)
	}
	if snapshot() != before {
		t.Fatal("identical source replay rewrote object or metadata search tuples")
	}
	// Initial pipeline creation advances serving state after source admission.
	// An explicit audit first repairs its derived metadata; the next is a no-op.
	object, err = s.store.UpsertKnowledgeObject(t.Context(), replay.KnowledgeObjects[0])
	if err != nil {
		t.Fatal(err)
	}
	before = snapshot()
	if replayed, err := s.store.UpsertKnowledgeObject(t.Context(), object); err != nil ||
		!replayed.UpdatedAt.Equal(object.UpdatedAt) || !replayed.LastSeenAt.Equal(object.LastSeenAt) || snapshot() != before {
		t.Fatalf("public upsert was not a no-op: %+v %v", replayed, err)
	}
	for _, mutate := range []func(*KnowledgeObject){
		func(o *KnowledgeObject) { o.Title = "changed title" },
		func(o *KnowledgeObject) { o.Metadata = json.RawMessage(`{"changed_source_policy":true}`) },
		func(o *KnowledgeObject) {
			o.SourceHash, o.SourceRevision = hashArtifactValue("new content"), "new-version"
		},
	} {
		next := object
		next.KnowledgeObjectID = ids.NewKnowledgeObjectID()
		next.LastSeenAt = object.LastSeenAt.Add(time.Hour)
		mutate(&next)
		object, changed, err := s.store.upsertKnowledgeObject(t.Context(), next)
		if err != nil || !changed || !object.UpdatedAt.After(next.UpdatedAt) || !object.LastSeenAt.Equal(next.LastSeenAt) {
			t.Fatalf("changed source: changed=%t object=%+v err=%v", changed, object, err)
		}
		if snapshot() == before {
			t.Fatal("changed source did not update the stored tuple")
		}
		before = snapshot()
		if _, changed, err = s.store.upsertKnowledgeObject(t.Context(), next); err != nil || changed || snapshot() != before {
			t.Fatalf("changed source did not converge: changed=%t err=%v", changed, err)
		}
	}
}

func TestKnowledgeObjectSourceNoopRepairsCanonicalBindingPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	objects := reconcileBoxSyncedFixture(t, s, roots)
	object, err := s.store.GetKnowledgeObject(t.Context(), objects[0].KnowledgeObjectID)
	if err != nil {
		t.Fatal(err)
	}
	var canonical string
	if err := s.store.db.QueryRow(`SELECT canonical_file_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&canonical); err != nil || canonical == "" {
		t.Fatalf("initial canonical binding: %s %v", canonical, err)
	}
	if _, err := s.store.db.Exec(`UPDATE knowledge.knowledge_objects SET canonical_file_id=NULL WHERE knowledge_object_id=$1`, object.KnowledgeObjectID); err != nil {
		t.Fatal(err)
	}
	out, changed, err := s.store.upsertKnowledgeObject(t.Context(), object)
	if err != nil || !changed || !out.UpdatedAt.Equal(object.UpdatedAt) {
		t.Fatalf("canonical repair: changed=%t err=%v", changed, err)
	}
	var correct bool
	if err := s.store.db.QueryRow(`SELECT canonical_file_id=$2 FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, object.KnowledgeObjectID, canonical).Scan(&correct); err != nil || !correct {
		t.Fatalf("missing canonical binding was not repaired: %t %v", correct, err)
	}
	if _, changed, err := s.store.upsertKnowledgeObject(t.Context(), object); err != nil || changed {
		t.Fatalf("canonical repair did not converge: changed=%t err=%v", changed, err)
	}
}
