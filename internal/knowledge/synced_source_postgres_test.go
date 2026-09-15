package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func syncedReaderFixture(t *testing.T) (*Service, KnowledgeObject, SyncedObjectEntry, string) {
	t.Helper()
	s, roots := boxSyncedFixture(t)
	source := filepath.Join(roots[0].SourcePath, "source.md")
	blobPath := filepath.Join(t.TempDir(), "retained-blob")
	if err := os.Rename(source, blobPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`UPDATE files.blobs SET storage_path=$1 WHERE storage_path=$2`, blobPath, source); err != nil {
		t.Fatal(err)
	}
	objects := reconcileBoxSyncedFixture(t, s, roots)
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), roots[0].NotesSourceRootID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("synced source: %d %v", len(entries), err)
	}
	for _, object := range objects {
		if object.NotesSourceRootID == roots[0].NotesSourceRootID {
			if object.StorageEntryID != nil {
				t.Fatal("fixture must exercise synced Objects, not catalog fallback")
			}
			return s, object, entries[0], blobPath
		}
	}
	t.Fatal("Notes source missing")
	return nil, KnowledgeObject{}, SyncedObjectEntry{}, ""
}

func TestSyncedSourceForeignPathPostgres(t *testing.T) {
	s, object, _, blob := syncedReaderFixture(t)
	for _, localExists := range []bool{false, true} {
		if localExists {
			if err := os.WriteFile(object.SourcePath, []byte("unrelated local impostor"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		extraction, err := s.ExtractObject(t.Context(), TextPipelineInput{Object: object, Content: "untrusted supplied content", MaxBytes: 1024})
		if err != nil || extraction.Status != ExtractionStatusExtracted || !strings.Contains(extraction.Document.Text, "cobalt observatory") || strings.Contains(extraction.Document.Text, "impostor") {
			t.Fatalf("local exists=%t: retained body not extracted: status=%s text=%q error=%v", localExists, extraction.Status, extraction.Document.Text, err)
		}
	}
	advanceBoxSyncedFixture(t, s)
	assertSyncedSearchBody(t, s, object, "cobalt observatory")
	if work := advanceBoxSyncedFixture(t, s); work != 0 {
		t.Fatalf("unchanged source created %d stages", work)
	}
	payload, err := os.ReadFile(blob)
	if err != nil || hashArtifactValue(string(payload)) != object.SourceHash {
		t.Fatalf("source hash changed: %v", err)
	}
}

func assertSyncedSearchBody(t *testing.T, s *Service, object KnowledgeObject, query string) {
	t.Helper()
	result, err := s.SearchNotes(t.Context(), NotesSearchInput{Query: query, NotesSourceRootID: object.NotesSourceRootID, Mode: NotesSearchModeLexical})
	if err != nil || len(result.Results) != 1 {
		t.Fatalf("body search returned %d: %v", len(result.Results), err)
	}
	got := result.Results[0]
	if got.MetadataOnly || got.KnowledgeObjectID != object.KnowledgeObjectID || got.SourcePath != object.SourcePath || got.SourceNodeKey != object.SourceNodeKey || got.RelativePath != object.RelativePath || got.KnowledgeObjectVersionID == "" || got.Citation.SourceRef != object.KnowledgeObjectID+"#"+got.KnowledgeChunkID {
		t.Fatalf("body/citation identity: %+v", got)
	}
	version, err := s.store.getKnowledgeObjectVersion(t.Context(), got.KnowledgeObjectVersionID)
	if err != nil || version.SourceHash != object.SourceHash || version.SourceRevision != object.SourceRevision {
		t.Fatalf("search version identity: %+v %v", version, err)
	}
}

func TestSyncedSourceCurrentBindingPostgres(t *testing.T) {
	s, object, entry, blob := syncedReaderFixture(t)
	for _, test := range []struct{ name, change, restore string }{
		{"private", `UPDATE files.file_metadata SET index_policy='private_no_index'`, `UPDATE files.file_metadata SET index_policy='text_later'`},
		{"root-disabled", `UPDATE knowledge.notes_source_roots SET status='blocked'`, `UPDATE knowledge.notes_source_roots SET status='active'`},
		{"scope-disabled", `UPDATE scopes.scopes SET status='disabled'`, `UPDATE scopes.scopes SET status='active'`},
		{"stale-replica", `UPDATE sync.replicas SET freshness_state='stale'`, `UPDATE sync.replicas SET freshness_state='fresh'`},
		{"replica-kind", `UPDATE sync.replicas SET replicated_kind='other'`, `UPDATE sync.replicas SET replicated_kind='object_version'`},
		{"replica-hash", `UPDATE sync.replicas SET storage_ref='wrong'`, `UPDATE sync.replicas r SET storage_ref=v.content_hash FROM objects.object_versions v WHERE r.replicated_id=v.object_version_id`},
		{"blob-unverified", `UPDATE files.blobs SET status='pending'`, `UPDATE files.blobs SET status='verified'`},
		{"blob-size", `UPDATE files.blobs SET size_bytes=size_bytes+1`, `UPDATE files.blobs SET size_bytes=size_bytes-1`},
		{"version-size", `UPDATE objects.object_versions SET size_bytes=size_bytes+1`, `UPDATE objects.object_versions SET size_bytes=size_bytes-1`},
		{"version-retired", `UPDATE objects.object_versions SET status='superseded'`, `UPDATE objects.object_versions SET status='active'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.store.db.Exec(test.change); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := s.store.db.Exec(test.restore); err != nil {
					t.Fatal(err)
				}
			}()
			if payload, err := s.readSyncedObjectSource(t.Context(), object, 1024); !errors.Is(err, ErrConflict) || len(payload) != 0 {
				t.Fatalf("ineligible source supplied bytes: %q %v", payload, err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(*KnowledgeObject)
	}{
		{"claim-hash", func(o *KnowledgeObject) { o.SourceHash = hashArtifactValue("other") }},
		{"claim-owner", func(o *KnowledgeObject) { o.SourceNodeKey = "other" }},
		{"claim-path", func(o *KnowledgeObject) { o.SourcePath = blob }},
		{"claim-revision", func(o *KnowledgeObject) { o.SourceRevision += "wrong" }},
		{"claim-replica", func(o *KnowledgeObject) {
			var metadata map[string]any
			json.Unmarshal(o.Metadata, &metadata)
			metadata["synced_object"].(map[string]any)["replica_id"] = "replica_wrong"
			o.Metadata = mustJSON(t, metadata)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			claim := object
			test.mutate(&claim)
			if _, err := s.readSyncedObjectSource(t.Context(), claim, 1024); !errors.Is(err, ErrConflict) {
				t.Fatalf("invalid claim read: %v", err)
			}
		})
	}
	// Even canonical metadata cannot nominate a different physical file.
	if _, err := s.store.db.Exec(`UPDATE knowledge.knowledge_objects SET metadata=jsonb_set(metadata,'{synced_object,blob_storage_path}',to_jsonb($2::text)) WHERE knowledge_object_id=$1`, object.KnowledgeObjectID, object.SourcePath); err != nil {
		t.Fatal(err)
	}
	current, err := s.store.GetKnowledgeObject(t.Context(), object.KnowledgeObjectID)
	if err != nil {
		t.Fatal(err)
	}
	if payload, err := s.readSyncedObjectSource(t.Context(), current, 1024); err != nil || hashArtifactValue(string(payload)) != entry.SourceHash {
		t.Fatalf("metadata path displaced database blob: %v", err)
	}
}

func TestSyncedSourcePostReadFencePostgres(t *testing.T) {
	s, object, _, blob := syncedReaderFixture(t)
	for _, boundary := range []string{"private", "blob-path", "cancelled"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			read := func(ctx context.Context, binding syncedSourceBinding, max int64) ([]byte, error) {
				payload, err := readVerifiedSyncedBlob(ctx, binding, max)
				if err != nil {
					return nil, err
				}
				switch boundary {
				case "private":
					_, err = s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='private_no_index'`)
				case "blob-path":
					_, err = s.store.db.Exec(`UPDATE files.blobs SET storage_path=storage_path||'.moved' WHERE storage_path=$1`, blob)
				case "cancelled":
					cancel()
				}
				return payload, err
			}
			payload, err := s.readSyncedObjectSourceWith(ctx, object, 1024, read)
			if err == nil || len(payload) != 0 {
				t.Fatalf("changed source escaped final fence: %q %v", payload, err)
			}
			if _, err = s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='text_later'`); err != nil {
				t.Fatal(err)
			}
			if _, err = s.store.db.Exec(`UPDATE files.blobs SET storage_path=$1 WHERE storage_path=$1||'.moved'`, blob); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSyncedSourceNativeRetryPostgres(t *testing.T) {
	s, object, _, blob := syncedReaderFixture(t)
	if err := os.Rename(blob, blob+".unavailable"); err != nil {
		t.Fatal(err)
	}
	advanceBoxSyncedFixture(t, s)
	before, err := s.InspectPipeline(t.Context(), object.KnowledgeObjectID)
	if err != nil || before.Run.Status != FilePipelineStatusCompleteWithWarning {
		t.Fatalf("unavailable run: %+v %v", before.Run, err)
	}
	if err = os.Rename(blob+".unavailable", blob); err != nil {
		t.Fatal(err)
	}
	if advanceBoxSyncedFixture(t, s) != 0 {
		t.Fatal("completed run was automatically retried")
	}
	retried, err := s.RetryPipeline(t.Context(), before.Run.KnowledgePipelineRunID, PipelineRetryInput{StageKey: FilePipelineStageNativeText})
	if err != nil {
		t.Fatal(err)
	}
	advanceBoxSyncedFixture(t, s)
	after, err := s.InspectPipeline(t.Context(), object.KnowledgeObjectID)
	if err != nil || after.Run.KnowledgePipelineRunID != retried.KnowledgePipelineRunID || after.Run.Generation != before.Run.Generation+1 || after.Run.SourceHash != before.Run.SourceHash || after.Run.KnowledgeObjectVersionID != before.Run.KnowledgeObjectVersionID {
		t.Fatalf("retry identity: %+v %v", after.Run, err)
	}
	assertSyncedSearchBody(t, s, object, "cobalt observatory")
	var runs, versions int
	if err := s.store.db.QueryRow(`SELECT (SELECT count(*) FROM knowledge.pipeline_runs WHERE knowledge_object_id=$1),(SELECT count(*) FROM knowledge.knowledge_object_versions WHERE knowledge_object_id=$1)`, object.KnowledgeObjectID).Scan(&runs, &versions); err != nil || runs != 2 || versions != 1 {
		t.Fatalf("retry rewrote source history: %d %d %v", runs, versions, err)
	}
	old, err := s.InspectPipeline(t.Context(), before.Run.KnowledgePipelineRunID)
	if err != nil || old.Run.Status != FilePipelineStatusStale || old.Run.CompletedAt == nil || !old.Run.CompletedAt.Equal(*before.Run.CompletedAt) || advanceBoxSyncedFixture(t, s) != 0 {
		t.Fatalf("retry lost old receipt or repeated work: %v", err)
	}
	for i, stage := range old.Stages {
		if stage.KnowledgePipelineStageRunID != before.Stages[i].KnowledgePipelineStageRunID || stage.Status != before.Stages[i].Status {
			t.Fatal("retry rewrote historical stage evidence")
		}
	}
}

func TestSyncedSourceCustodyFencePostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	var object KnowledgeObject
	for _, candidate := range f.objects {
		if candidate.RelativePath == "topic-one/source.md" {
			object = candidate
		}
	}
	if object.KnowledgeObjectID == "" {
		t.Fatal("archive source missing")
	}
	read := func(ctx context.Context, binding syncedSourceBinding, max int64) ([]byte, error) {
		payload, err := readVerifiedSyncedBlob(ctx, binding, max)
		if err == nil {
			f.archive(t)
		}
		return payload, err
	}
	if payload, err := f.s.readSyncedObjectSourceWith(t.Context(), object, 1024, read); !errors.Is(err, ErrConflict) || len(payload) != 0 {
		t.Fatalf("archive during read escaped fence: %q %v", payload, err)
	}
	if _, err := f.s.readSyncedObjectSource(t.Context(), object, 1024); !errors.Is(err, ErrConflict) {
		t.Fatalf("archive projection lag did not stop read: %v", err)
	}
}

func TestSyncedSourcePathExtractorPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	path := writeTestDOCX(t, map[string]string{"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Verified docx constellation.</w:t></w:r></w:p></w:body></w:document>`})
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := hashArtifactValue(string(payload))
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), roots[0].NotesSourceRootID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("fixture entry: %v", err)
	}
	e := entries[0]
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`UPDATE files.blobs SET hash_hex=$2,hash_uri=$3,size_bytes=$4,storage_path=$5 WHERE storage_path=$1`, []any{e.BlobStoragePath, strings.TrimPrefix(hash, "sha256:"), hash, len(payload), path}},
		{`UPDATE objects.object_versions SET content_hash=$2,size_bytes=$3,source_path=replace(source_path,'.md','.docx'),mime_type='application/vnd.openxmlformats-officedocument.wordprocessingml.document',metadata='{"file_class":"office_document"}' WHERE object_version_id=$1`, []any{e.ObjectVersionID, hash, len(payload)}},
		{`UPDATE files.file_metadata SET source_path=replace(source_path,'.md','.docx'),logical_name='source.docx',mime_type='application/vnd.openxmlformats-officedocument.wordprocessingml.document' WHERE object_id=$1`, []any{e.ObjectID}},
		{`UPDATE sync.replicas SET storage_ref=$2 WHERE replica_id=$1`, []any{e.ReplicaID, hash}},
		{`UPDATE knowledge.notes_source_roots SET metadata=jsonb_set(metadata,'{registration_metadata,knowledge_source,include}','["**/*"]') WHERE notes_source_root_id=$1`, []any{roots[0].NotesSourceRootID}},
	} {
		if _, err = s.store.db.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	roots, err = s.store.ListSourceRoots(t.Context(), SourceRootFilter{})
	if err != nil {
		t.Fatal(err)
	}
	objects := reconcileBoxSyncedFixture(t, s, roots)
	var object KnowledgeObject
	for _, o := range objects {
		if o.FileClass == storagecatalog.FileClassOfficeDocument {
			object = o
		}
	}
	if object.KnowledgeObjectID == "" {
		t.Fatal("DOCX object missing")
	}
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	for _, bad := range []bool{false, true} {
		if bad {
			broken := []byte("not a zip document")
			if err = os.WriteFile(path, broken, 0600); err != nil {
				t.Fatal(err)
			}
		}
		result, err := s.ExtractObject(t.Context(), TextPipelineInput{Object: object, MaxBytes: 1 << 20})
		if !bad && (err != nil || !strings.Contains(result.Document.Text, "Verified docx constellation")) {
			t.Fatalf("DOCX body: %+v %v", result, err)
		}
		if bad && err == nil {
			t.Fatal("tampered DOCX was extracted")
		}
		files, err := os.ReadDir(temp)
		if err != nil || len(files) != 0 {
			t.Fatalf("temporary snapshot remained: %v %v", files, err)
		}
	}
	current, err := s.store.GetKnowledgeObject(t.Context(), object.KnowledgeObjectID)
	if err != nil || current.SourcePath != object.SourcePath || current.SourceHash != hash {
		t.Fatalf("snapshot changed citation: %v", err)
	}
}
