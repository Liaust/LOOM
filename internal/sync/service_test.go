package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncedObjectPayloadIncludesLocalVersionIdentity(t *testing.T) {
	sourceMtime := time.Date(2025, 3, 4, 5, 6, 7, 0, time.FixedZone("source", 2*60*60))
	sourceCreatedAt := sourceMtime.Add(-24 * time.Hour)
	input := SyncedObjectInput{
		LocalObjectRef:       "object_local",
		LocalVersionRef:      "version_local",
		LocalSequence:        7,
		LogicalName:          "Project.md",
		SourcePath:           "watched-root://notes/Project.md",
		SourceMtime:          &sourceMtime,
		SourceMtimeBasis:     "source_filesystem_mtime",
		SourceCreatedAt:      &sourceCreatedAt,
		SourceCreatedBasis:   "source_filesystem_birthtime",
		SizeBytes:            12,
		MimeType:             "text/markdown",
		HashURI:              "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IndexPolicy:          IndexPolicyTextLater,
		RawBackupPolicy:      RawBackupPolicyNormal,
		FileClass:            "markdown",
		ClassificationSource: "extension",
		IndexingState:        "indexed",
	}
	payload := jsonObject(syncedObjectPayload(input))
	if payload["local_object_ref"] != input.LocalObjectRef ||
		payload["local_version_ref"] != input.LocalVersionRef ||
		payload["hash_uri"] != input.HashURI {
		t.Fatalf("unexpected payload identity %#v", payload)
	}
	if payload["source_mtime"] == nil || payload["source_created_at"] == nil || payload["source_mtime_basis"] != input.SourceMtimeBasis {
		t.Fatalf("source times missing from synced payload %#v", payload)
	}
	classification := payload["classification"].(map[string]any)
	if payload["file_class"] != input.FileClass ||
		classification["source"] != input.ClassificationSource ||
		classification["indexing_state"] != input.IndexingState {
		t.Fatalf("unexpected classification payload %#v", payload)
	}
}

func TestNormalizeSyncedObjectInputNormalizesSourceTimes(t *testing.T) {
	input := syncedObjectInputForNormalizeTest()
	mtime := time.Date(2026, 8, 17, 14, 30, 0, 0, time.FixedZone("source", 2*60*60))
	created := mtime.Add(-time.Hour)
	input.SourceMtime = &mtime
	input.SourceCreatedAt = &created

	normalized, err := normalizeSyncedObjectInput(input)
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if normalized.SourceMtime.Location() != time.UTC || normalized.SourceCreatedAt.Location() != time.UTC {
		t.Fatalf("source times were not normalized: %#v", normalized)
	}
	if normalized.SourceMtimeBasis != "source_filesystem_mtime" || normalized.SourceCreatedBasis != "source_filesystem_birthtime" {
		t.Fatalf("source time provenance was not defaulted: %#v", normalized)
	}
}

func TestSyncedObjectItemMetadataCarriesCurrentOutboxAndVersion(t *testing.T) {
	input := SyncedObjectInput{
		LocalVersionRef:      "version_current",
		HashURI:              "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		FileClass:            "markdown",
		ClassificationSource: "extension",
		IndexingState:        "too_large",
		Metadata:             json.RawMessage(`{"local_outbox_id":"local_outbox_current"}`),
	}
	metadata := jsonObject(syncedObjectItemMetadata(input, map[string]any{
		"object_id":         "object_main",
		"object_version_id": "version_main",
	}))
	if metadata["local_outbox_id"] != "local_outbox_current" ||
		metadata["local_version_ref"] != "version_current" ||
		metadata["object_id"] != "object_main" ||
		metadata["object_version_id"] != "version_main" {
		t.Fatalf("unexpected item metadata %#v", metadata)
	}
	if metadata["file_class"] != "markdown" ||
		metadata["classification_source"] != "extension" ||
		metadata["indexing_state"] != "too_large" {
		t.Fatalf("classification metadata missing: %#v", metadata)
	}
}

func TestNormalizeSyncedObjectInputBackfillsClassification(t *testing.T) {
	input := syncedObjectInputForNormalizeTest()
	input.FileClass = ""
	input.ClassificationSource = ""
	input.IndexingState = ""
	input.LogicalName = "README"
	input.MimeType = "text/plain"

	normalized, err := normalizeSyncedObjectInput(input)
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if normalized.FileClass != "text" ||
		normalized.ClassificationSource != "mime" ||
		normalized.IndexingState != "indexed" {
		t.Fatalf("classification not backfilled: %#v", normalized)
	}
}

func TestNormalizeSyncedObjectInputAllowsZeroByteContent(t *testing.T) {
	input := syncedObjectInputForNormalizeTest()
	input.SizeBytes = 0
	input.HashURI = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	input.ContentBase64 = ""

	if _, err := normalizeSyncedObjectInput(input); err != nil {
		t.Fatalf("zero-byte synced object should be valid: %v", err)
	}
}

func TestNormalizeSyncedObjectInputRequiresContentForNonEmptyFiles(t *testing.T) {
	input := syncedObjectInputForNormalizeTest()
	input.SizeBytes = 12
	input.ContentBase64 = ""

	if _, err := normalizeSyncedObjectInput(input); err == nil {
		t.Fatal("expected missing content_base64 error for non-empty object")
	}
}

func TestDeletionRequestStatusHelpers(t *testing.T) {
	for _, status := range []string{
		DeletionRequestStatusPendingReview,
		DeletionRequestStatusRecorded,
		DeletionRequestStatusApproved,
		DeletionRequestStatusDenied,
		DeletionRequestStatusCompleted,
	} {
		if !ValidDeletionRequestStatus(status) {
			t.Fatalf("expected status %q to be valid", status)
		}
	}
	if got := NormalizeDeletionRequestStatus("pending"); got != DeletionRequestStatusPendingReview {
		t.Fatalf("pending alias = %q, want pending_review", got)
	}
	if ValidDeletionRequestStatus("archived") {
		t.Fatal("unexpected deletion request status accepted")
	}
	for _, status := range []string{"", DeletionRequestStatusPendingReview, DeletionRequestStatusRecorded} {
		if !DeletionRequestStatusIsActive(status) {
			t.Fatalf("expected %q to be active", status)
		}
	}
	for _, status := range []string{DeletionRequestStatusApproved, DeletionRequestStatusDenied, DeletionRequestStatusCompleted} {
		if DeletionRequestStatusIsActive(status) {
			t.Fatalf("expected %q to be historical", status)
		}
	}
}

func TestDeletionRequestTransitionValidation(t *testing.T) {
	for _, tc := range []struct {
		from string
		to   string
		ok   bool
	}{
		{DeletionRequestStatusRecorded, DeletionRequestStatusPendingReview, true},
		{DeletionRequestStatusPendingReview, DeletionRequestStatusApproved, true},
		{DeletionRequestStatusPendingReview, DeletionRequestStatusDenied, true},
		{DeletionRequestStatusApproved, DeletionRequestStatusCompleted, true},
		{DeletionRequestStatusDenied, DeletionRequestStatusCompleted, false},
		{DeletionRequestStatusCompleted, DeletionRequestStatusApproved, false},
	} {
		if got := deletionRequestTransitionAllowed(tc.from, tc.to); got != tc.ok {
			t.Fatalf("transition %s -> %s = %t, want %t", tc.from, tc.to, got, tc.ok)
		}
	}
}

func TestSanitizedPrivateBackupMetadataPreservesWatchedRootAuditFields(t *testing.T) {
	metadata := jsonObject(sanitizedPrivateBackupMetadata(json.RawMessage(`{
		"source":"loom-node-agent",
		"backup_kind":"watched_root_file",
		"root_key":"notes",
		"relative_path":"Project.md",
		"content_hash_uri":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"backup_mode":"incremental_raw",
		"local_artifact_id":"local_backup_artifact_test",
		"local_item_id":"local_backup_item_test",
		"client_generated_at":"2026-05-25T10:00:00Z",
		"unsafe_storage_ref":"/tmp/secret"
	}`)))
	for _, key := range []string{
		"source",
		"backup_kind",
		"root_key",
		"relative_path",
		"content_hash_uri",
		"backup_mode",
		"local_artifact_id",
		"local_item_id",
		"client_generated_at",
	} {
		if metadata[key] == "" {
			t.Fatalf("metadata missing %s: %#v", key, metadata)
		}
	}
	if _, ok := metadata["unsafe_storage_ref"]; ok {
		t.Fatalf("unsafe metadata was preserved: %#v", metadata)
	}
}

func TestPrivateBackupOperationDirConvergesWatchedArtifactsOnCanonicalBatch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "storage", "backups")
	operationDir, rootKey, batchKey, err := privateBackupOperationDir(root, "workspace-test", "private_backup_operation_test", json.RawMessage(`{
		"root_key":"loom_box__documents",
		"local_batch_id":"local_backup_batch_test"
	}`))
	if err != nil {
		t.Fatalf("privateBackupOperationDir returned error: %v", err)
	}
	want := filepath.Join(root, "workspace-test", "loom_box__documents", "local_backup_batch_test", "artifacts", "private_backup_operation_test")
	if operationDir != want || rootKey != "loom_box__documents" || batchKey != "local_backup_batch_test" {
		t.Fatalf("operation dir = %q root=%q batch=%q, want %q", operationDir, rootKey, batchKey, want)
	}
}

func TestPrivateBackupOperationDirUsesInspectableStandaloneCustody(t *testing.T) {
	root := filepath.Join(t.TempDir(), "storage", "backups")
	operationDir, rootKey, batchKey, err := privateBackupOperationDir(root, "workspace-test", "private_backup_operation_test", json.RawMessage(`{"root_key":"Secrets"}`))
	if err != nil {
		t.Fatalf("privateBackupOperationDir returned error: %v", err)
	}
	want := filepath.Join(root, "workspace-test", "Secrets", "private_backup_operation_test")
	if operationDir != want || rootKey != "Secrets" || batchKey != "private_backup_operation_test" {
		t.Fatalf("operation dir = %q root=%q batch=%q, want %q", operationDir, rootKey, batchKey, want)
	}
}

func TestPrivateBackupOperationDirRejectsUnsafeIdentitySegments(t *testing.T) {
	root := filepath.Join(t.TempDir(), "storage", "backups")
	for name, metadata := range map[string]json.RawMessage{
		"root traversal":  json.RawMessage(`{"root_key":"../outside"}`),
		"batch traversal": json.RawMessage(`{"root_key":"documents","local_batch_id":"../outside"}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := privateBackupOperationDir(root, "workspace-test", "private_backup_operation_test", metadata)
			if err == nil || !strings.Contains(err.Error(), "safe path segment") {
				t.Fatalf("error = %v, want unsafe segment rejection", err)
			}
		})
	}
}

func TestWritePrivateBackupFileIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.tar")
	if err := writePrivateBackupFile(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("first write failed: %v", err)
	}
	if err := writePrivateBackupFile(path, []byte("second"), 0o600); err == nil {
		t.Fatal("exclusive write overwrote existing payload")
	}
	payload, err := os.ReadFile(path)
	if err != nil || string(payload) != "first" {
		t.Fatalf("payload = %q err=%v", payload, err)
	}
}

func TestWritePrivateBackupCustodyPublishesPayloadAndManifest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backups")
	operationDir := filepath.Join(root, "workspace", "notes", "batch", "artifacts", "operation")
	if err := writePrivateBackupCustody(root, operationDir, []byte("payload"), []byte(`{"manifest":true}`)); err != nil {
		t.Fatalf("write custody: %v", err)
	}
	for name, want := range map[string]string{"payload.tar": "payload", "manifest.json": `{"manifest":true}`} {
		got, err := os.ReadFile(filepath.Join(operationDir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q err=%v", name, got, err)
		}
	}
	if err := writePrivateBackupCustody(root, operationDir, []byte("changed"), []byte(`{}`)); err == nil {
		t.Fatal("custody write overwrote an existing operation")
	}
}

func TestWritePrivateBackupCustodyRejectsSymlinkedParent(t *testing.T) {
	temp := t.TempDir()
	root := filepath.Join(temp, "backups")
	external := filepath.Join(temp, "external")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(external, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(external, "sentinel")
	if err := os.WriteFile(sentinel, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "workspace")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	operationDir := filepath.Join(root, "workspace", "notes", "batch", "operation")
	if err := writePrivateBackupCustody(root, operationDir, []byte("payload"), []byte(`{}`)); err == nil {
		t.Fatal("custody write followed a hostile parent symlink")
	}
	got, err := os.ReadFile(sentinel)
	if err != nil || string(got) != "safe" {
		t.Fatalf("external sentinel changed: %q err=%v", got, err)
	}
}

func syncedObjectInputForNormalizeTest() SyncedObjectInput {
	return SyncedObjectInput{
		CredentialToken: "credential-token",
		LocalObjectRef:  "local-object",
		LocalVersionRef: "local-version",
		LocalSequence:   1,
		ScopeRef:        "notes",
		LogicalName:     "empty.md",
		SourcePath:      "watched-root://notes/empty.md",
		SizeBytes:       1,
		HashURI:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IndexPolicy:     IndexPolicyTextLater,
		RawBackupPolicy: RawBackupPolicyNormal,
		ContentBase64:   "YQ==",
	}
}
