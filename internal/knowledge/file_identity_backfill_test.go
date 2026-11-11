package knowledge

import (
	"context"
	"testing"
)

type observedTextReader struct{ currentTextReader }

func (r observedTextReader) ObserveNotesFile(context.Context, string, string, string) (FileSourceObservation, error) {
	return FileSourceObservation{Presence: "present", Size: int64(len(r.text))}, nil
}

type directorySourceReader struct{ observedTextReader }

func (r directorySourceReader) ObserveNotesFile(ctx context.Context, node, root, relative string) (FileSourceObservation, error) {
	if relative == "folder" {
		return FileSourceObservation{}, ErrFileSourceNotRegular
	}
	return r.observedTextReader.ObserveNotesFile(ctx, node, root, relative)
}

func TestFileMetadataAdoptionPostgres(t *testing.T) {
	f := declarationEnrollmentDB(t)
	f.report(f.analysis.Report.WatchedRoots[0])
	f.correlate(t)
	object := f.publish(t)
	root := f.reconcile(t, SourceRootStatusActive)
	s := f.service
	s.fileSourceReader = observedTextReader{currentTextReader{"current complete source"}}
	if _, err := s.ReconcileFileIdentities(t.Context(), FileIdentityReconcileInput{Limit: 10, Confirm: true}); err != nil {
		t.Fatal("initial legacy adoption", err)
	}
	var original string
	if err := f.db.QueryRow(`SELECT canonical_file_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE files.file_metadata SET source_sequence=7,source_presence='present' WHERE object_id=$1`, original); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE knowledge.knowledge_objects SET canonical_file_id=NULL WHERE knowledge_object_id=$1`, object.KnowledgeObjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO notes_workspace.source_inventory_generations(collection_id,generation,record) VALUES($1,'fixture','{}');`, root.NotesSourceRootID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO notes_workspace.source_inventory(collection_id,generation,relative_path,entry_kind,present,fingerprint,size_bytes,sequence,waiting_since,retry_at) VALUES($1,'fixture','fresh.md','text',true,'fixture',23,1,now(),now())`, root.NotesSourceRootID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO notes_workspace.source_inventory(collection_id,generation,relative_path,entry_kind,present,fingerprint,size_bytes,sequence,waiting_since,retry_at) VALUES($1,'fixture','folder','asset',true,'fixture',0,1,now(),now())`, root.NotesSourceRootID); err != nil {
		t.Fatal(err)
	}
	s.fileSourceReader = directorySourceReader{observedTextReader{currentTextReader{"current complete source"}}}
	preview, err := s.ReconcileFileIdentities(t.Context(), FileIdentityReconcileInput{Limit: 10})
	if err != nil || !preview.DryRun || preview.Applied != 0 || len(preview.Mappings) != 3 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if preview.Mappings[0].Reason != "not_regular_file" {
		t.Fatalf("directory was not qualified: %+v", preview.Mappings)
	}
	applied, err := s.ReconcileFileIdentities(t.Context(), FileIdentityReconcileInput{Limit: 10, Confirm: true})
	if err != nil || applied.Applied != 2 {
		t.Fatalf("apply: %+v %v", applied, err)
	}
	var linked string
	var sequence int64
	if err = f.db.QueryRow(`SELECT ko.canonical_file_id,f.source_sequence FROM knowledge.knowledge_objects ko JOIN files.file_metadata f ON f.object_id=ko.canonical_file_id WHERE ko.knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&linked, &sequence); err != nil || linked != original || sequence != 7 {
		t.Fatalf("producer observation replaced: %s %d %v", linked, sequence, err)
	}
	metadata, err := s.SearchFiles(t.Context(), NotesSearchInput{Query: "fresh", Mode: NotesSearchModeLexical})
	if err != nil || len(metadata.Results) != 1 || metadata.Results[0].Content != "metadata_only" {
		t.Fatalf("metadata discovery: %+v %v", metadata, err)
	}
	id := metadata.Results[0].FileID
	read, err := s.ReadFile(t.Context(), FileReadInput{Ref: id})
	if err != nil || read.Text != "current complete source" {
		t.Fatalf("metadata read: %+v %v", read, err)
	}
	var versions int
	if err = f.db.QueryRow(`SELECT count(*) FROM objects.object_versions WHERE object_id=$1`, id).Scan(&versions); err != nil || versions != 0 {
		t.Fatalf("metadata fabricated bytes: %d %v", versions, err)
	}
	if _, err = s.ReconcileFileIdentities(t.Context(), FileIdentityReconcileInput{Limit: 10, Confirm: true}); err != nil {
		t.Fatal("replay", err)
	}
	var parents int
	if err = f.db.QueryRow(`SELECT count(*) FROM files.file_metadata WHERE managed_source_path=$1`, metadata.Results[0].Path).Scan(&parents); err != nil || parents != 1 {
		t.Fatalf("duplicate parent: %d %v", parents, err)
	}
}
