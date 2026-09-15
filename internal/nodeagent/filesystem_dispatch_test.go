package nodeagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
	loomsync "loom.local/loom/internal/sync"
)

func TestExecuteFilesystemDispatchSafeList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("# Note\n"), 0o600); err != nil {
		t.Fatalf("write note: %v", err)
	}
	result, err := executeFilesystemDispatch(context.Background(), Config{
		NodeKey: "workspace-test",
		Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
			filesystemconnector.DefaultSafeRoot("slice13", dir),
		}},
	}, State{NodeID: "node_test"}, Store{}, routing.RemoteDispatchPayload{
		CapabilityAddress: "workspace/workspace-test@filesystem.safe_list",
		Input:             json.RawMessage(`{"root":"slice13","path":"."}`),
	})
	if err != nil {
		t.Fatalf("executeFilesystemDispatch failed: %v", err)
	}
	var payload filesystemconnector.SafeListResult
	if err := json.Unmarshal(result.ResultJSON, &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(payload.Entries) != 1 || payload.Entries[0].RelativePath != "note.md" {
		t.Fatalf("unexpected safe_list payload: %#v", payload)
	}
}

func TestExecuteFilesystemDispatchIngestUploadsLogicalSourcePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	safeRoot := filepath.Join(dir, "safe")
	if err := os.MkdirAll(safeRoot, 0o700); err != nil {
		t.Fatalf("mkdir safe root: %v", err)
	}
	source := filepath.Join(safeRoot, "note.md")
	content := []byte("# Note\n\nslice 13 ingest test\n")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	store := Store{
		ConfigPath: filepath.Join(dir, "config.json"),
		StatePath:  filepath.Join(dir, "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}

	var uploadInput loomsync.SyncedObjectInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/node-agent/sync/object-upload" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&uploadInput); err != nil {
			t.Fatalf("decode upload: %v", err)
		}
		decoded, err := base64.StdEncoding.DecodeString(uploadInput.ContentBase64)
		if err != nil {
			t.Fatalf("decode content: %v", err)
		}
		if string(decoded) != string(content) {
			t.Fatalf("unexpected upload content: %q", decoded)
		}
		now := time.Now().UTC()
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", loomsync.SyncedObjectResult{
			Batch: loomsync.SyncBatch{
				SyncBatchID:   "sync_batch_test",
				OriginNodeID:  "node_test",
				BatchKind:     loomsync.BatchKindObjectBlobs,
				Status:        loomsync.BatchStatusAccepted,
				ItemCount:     1,
				AcceptedCount: 1,
				ReceivedAt:    now,
				CompletedAt:   &now,
				Metadata:      json.RawMessage(`{}`),
			},
			Item: loomsync.SyncItemResult{
				SyncBatchItemID: "sync_batch_item_test",
				LocalRef:        uploadInput.LocalObjectRef,
				ItemKind:        loomsync.ItemKindObjectBlob,
				StreamName:      loomsync.StreamObjectBlobs,
				LocalSequence:   uploadInput.LocalSequence,
				Status:          loomsync.ItemStatusAccepted,
				GlobalRef:       "obj_test",
				PayloadHash:     "payload_hash_test",
				Metadata:        json.RawMessage(`{}`),
			},
			Cursor: loomsync.SyncCursor{
				SyncCursorID:         "sync_cursor_test",
				NodeID:               "node_test",
				StreamName:           loomsync.StreamObjectBlobs,
				LastAcceptedSequence: uploadInput.LocalSequence,
				UpdatedAt:            now,
				Metadata:             json.RawMessage(`{}`),
			},
			Replica: &loomsync.SyncReplica{
				ReplicaID: "sync_replica_test",
			},
			ObjectID:    "obj_test",
			VersionID:   "object_version_test",
			BlobID:      "blob_test",
			HashURI:     uploadInput.HashURI,
			IndexStatus: "indexed",
		}))
	}))
	defer server.Close()

	result, err := executeFilesystemDispatch(context.Background(), Config{
		MainURL: server.URL,
		NodeKey: "workspace-test",
		Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
			filesystemconnector.DefaultSafeRoot("slice13", safeRoot),
		}},
	}, State{NodeID: "node_test", CredentialToken: "node_cred_test"}, store, routing.RemoteDispatchPayload{
		RouteID:           "route_test",
		CapabilityCallID:  "capability_call_test",
		ScopeID:           "scope_test",
		CapabilityAddress: "workspace/workspace-test@filesystem.ingest_file",
		Input:             json.RawMessage(`{"root":"slice13","path":"note.md","index_policy":"default"}`),
	})
	if err != nil {
		t.Fatalf("executeFilesystemDispatch ingest failed: %v", err)
	}
	if uploadInput.SourcePath != "filesystem://slice13/note.md" {
		t.Fatalf("expected logical source path, got %q", uploadInput.SourcePath)
	}
	if uploadInput.SourceMtime == nil || uploadInput.SourceMtimeBasis != "source_filesystem_mtime" {
		t.Fatalf("source modification time missing from upload: %#v", uploadInput)
	}
	if uploadInput.SourceCreatedAt != nil && uploadInput.SourceCreatedBasis != "source_filesystem_birthtime" {
		t.Fatalf("source creation provenance missing from upload: %#v", uploadInput)
	}
	if strings.Contains(uploadInput.SourcePath, safeRoot) {
		t.Fatalf("upload input leaked absolute path: %q", uploadInput.SourcePath)
	}
	if uploadInput.ScopeRef != "scope_test" || uploadInput.ProjectRef != "" {
		t.Fatalf("expected dispatch scope fallback, got project=%q scope=%q", uploadInput.ProjectRef, uploadInput.ScopeRef)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.ResultJSON, &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if payload["object_id"] != "obj_test" || payload["sync_batch_id"] != "sync_batch_test" || payload["replica_id"] != "sync_replica_test" {
		t.Fatalf("unexpected ingest result: %#v", payload)
	}
	status, err := store.LocalSyncStatus(Config{NodeKey: "workspace-test"}, State{NodeID: "node_test"})
	if err != nil {
		t.Fatalf("LocalSyncStatus failed: %v", err)
	}
	if status.Counts.Objects != 1 || status.Counts.Accepted != 1 {
		t.Fatalf("expected local sync object and accepted outbox item, got %#v", status.Counts)
	}
}
