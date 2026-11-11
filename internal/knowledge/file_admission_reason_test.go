package knowledge

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestFileAdmissionReasonContract(t *testing.T) {
	for _, tc := range []struct {
		status, presence, reason string
		authority                bool
		physical                 error
	}{
		{status: SourceRootStatusBlocked, reason: "source_root_blocked"},
		{status: SourceRootStatusDisabled, reason: "source_root_disabled"},
		{status: SourceRootStatusStale, reason: "source_authority_unavailable"},
		{status: SourceRootStatusActive, reason: "source_authority_unavailable"},
		{status: SourceRootStatusActive, authority: true, reason: "source_not_published"},
		{status: SourceRootStatusActive, authority: true, presence: "missing", physical: ErrFileSourceMissing},
		{status: SourceRootStatusActive, authority: true, presence: "unavailable", physical: ErrFileSourceUnavailable},
	} {
		err := fileAdmissionReason(SourceRoot{NotesSourceRootID: "root-reference", Status: tc.status}, tc.authority, tc.presence)
		if tc.physical != nil {
			if !errors.Is(err, tc.physical) {
				t.Fatalf("physical source: %v", err)
			}
			continue
		}
		var reason *FileAdmissionError
		if !errors.As(err, &reason) || reason.Reason != tc.reason || reason.SourceRootID != "root-reference" || !errors.Is(err, sql.ErrNoRows) || reason.Unwrap() != sql.ErrNoRows || strings.Contains(reason.Error(), "root-reference") {
			t.Fatalf("dispatch error contract: %+v %v", reason, err)
		}
	}
	if err := fileAdmissionReason(SourceRoot{Status: "deleted"}, false, "present"); err != sql.ErrNoRows {
		t.Fatal("unknown root lifecycle must not disclose a reason", err)
	}
}

func TestFileAdmissionPathPrivacyAndExactQuery(t *testing.T) {
	root := SourceRoot{RootKind: RootKindProjectMaterial}
	for _, relative := range []string{"", "../source.md", "/source.md", ".private/source.md", "credentials/secret.md", "private_no_index/source.md", "loom.notes.yaml", "notes/loom.notes.yml", "source.exe", "source.md/../secret.md"} {
		if fileAdmissionPathAllowed(root, relative) {
			t.Fatal("excluded/unsupported path gets a diagnostic", relative)
		}
	}
	if !fileAdmissionPathAllowed(root, "public/source.md") {
		t.Fatal("public managed path rejected")
	}
	if !fileAdmissionPathAllowed(root, "Navigation/People.base") {
		t.Fatal("Base configuration path rejected")
	}
	query := fileAdmissionReferenceSQL()
	if !strings.HasPrefix(query, "WITH exact_objects AS MATERIALIZED (") || strings.Count(query, "LIMIT 2") != 2 || !strings.Contains(query, "exact_files AS MATERIALIZED") {
		t.Fatal("diagnostics must resolve bounded exact references before authority")
	}
	for _, fence := range []string{"private_no_index", "object_scope_links", "notes_custody_transitions", "newer_version", "index_policy", "scope_link.relevance_status='active'"} {
		if !strings.Contains(query, fence) {
			t.Fatal("known-reference privacy/current-scope fence missing", fence)
		}
	}
}

func TestFileAdmissionKnownReferenceAndPrivacyPostgres(t *testing.T) {
	f := declarationBoundEnrollmentDB(t)
	object := f.publish(t)
	root := f.reconcile(t, SourceRootStatusActive)
	if _, err := f.service.resolveFileReadTarget(t.Context(), object.KnowledgeObjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE knowledge.notes_source_roots SET metadata=jsonb_set(metadata,'{registration_metadata,declaration_adapter,group_hash}',to_jsonb('sha256:'||repeat('0',64))) WHERE notes_source_root_id=$1`, root.NotesSourceRootID); err != nil {
		t.Fatal(err)
	}
	assertReason := func(want string) {
		t.Helper()
		_, err := f.service.resolveFileReadTarget(t.Context(), object.KnowledgeObjectID)
		var reason *FileAdmissionError
		if !errors.As(err, &reason) || reason.Reason != want || reason.SourceRootID != root.NotesSourceRootID {
			t.Fatalf("known reference classification: %+v %v", reason, err)
		}
	}
	assertReason("source_authority_unavailable")
	if _, err := f.db.Exec(`UPDATE knowledge.notes_source_roots SET metadata=$2 WHERE notes_source_root_id=$1`, root.NotesSourceRootID, root.Metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE knowledge.knowledge_objects SET metadata=jsonb_set(metadata,'{source_root,knowledge_source,config_hash}',to_jsonb('sha256:'||repeat('0',64))) WHERE knowledge_object_id=$1`, object.KnowledgeObjectID); err != nil {
		t.Fatal(err)
	}
	assertReason("source_not_published")
	if _, err := f.db.Exec(`UPDATE knowledge.knowledge_objects SET metadata=$2 WHERE knowledge_object_id=$1`, object.KnowledgeObjectID, object.Metadata); err != nil {
		t.Fatal(err)
	}
	// Root lifecycle changes affect known public references, not arbitrary files.
	for _, status := range []string{SourceRootStatusBlocked, SourceRootStatusDisabled} {
		if _, err := f.db.Exec(`UPDATE knowledge.notes_source_roots SET status=$2 WHERE notes_source_root_id=$1`, root.NotesSourceRootID, status); err != nil {
			t.Fatal(err)
		}
		_, err := f.service.resolveFileReadTarget(t.Context(), object.SourcePath)
		var reason *FileAdmissionError
		want := "source_root_blocked"
		if status == SourceRootStatusDisabled {
			want = "source_root_disabled"
		}
		if !errors.As(err, &reason) || reason.Reason != want || reason.SourceRootID != root.NotesSourceRootID || !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("known source: %+v %v", reason, err)
		}
	}
	if _, err := f.db.Exec(`UPDATE storage.storage_entries SET metadata='{"private_no_index":true}' WHERE storage_entry_id=$1`, object.StorageEntryID); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{object.SourcePath, object.KnowledgeObjectID, root.SourcePath + "/unknown.md", root.SourcePath + "/credentials/secret.md"} {
		_, err := f.service.resolveFileReadTarget(t.Context(), ref)
		var reason *FileAdmissionError
		if !errors.Is(err, sql.ErrNoRows) || errors.As(err, &reason) {
			t.Fatalf("private/unknown reference got a diagnostic: %+v %v", reason, err)
		}
	}
}
