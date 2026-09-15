package runtimes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/workers"
)

func TestObjectStoreIntegrityRuntimeDescriptorAndInstance(t *testing.T) {
	runtime := NewObjectStoreIntegrityRuntime(nil, maintenance.Service{}, "/var/lib/loom/object-store")
	if runtime.Kind() != workers.KindObjectStore {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindObjectStore)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindObjectStore {
		t.Fatalf("descriptor kind = %q, want %q", descriptor.WorkerKind, workers.KindObjectStore)
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("default instance count = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.object_store_integrity_sample" {
		t.Fatalf("worker key = %q, want main.object_store_integrity_sample", instances[0].WorkerKey)
	}
}

func TestObjectStoreIntegrityRuntimeValidateConfig(t *testing.T) {
	runtime := NewObjectStoreIntegrityRuntime(nil, maintenance.Service{}, "/var/lib/loom/object-store")
	if err := runtime.ValidateConfig(context.Background(), runtime.DefaultConfig()); err != nil {
		t.Fatalf("ValidateConfig returned error: %v", err)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"mode":"full"}`),
		json.RawMessage(`{"detect_orphans":true}`),
		json.RawMessage(`{"auto_repair":true}`),
	} {
		err := runtime.ValidateConfig(context.Background(), raw)
		if !errors.Is(err, workers.ErrInvalid) {
			t.Fatalf("ValidateConfig(%s) error = %v, want workers.ErrInvalid", raw, err)
		}
	}
}

func TestResolveObjectStoreBlobPath(t *testing.T) {
	root := t.TempDir()
	path, err := resolveObjectStoreBlobPath(root, "blobs/sha256/aa/file")
	if err != nil {
		t.Fatalf("resolve relative path returned error: %v", err)
	}
	if path != filepath.Join(root, "blobs", "sha256", "aa", "file") {
		t.Fatalf("relative path = %q", path)
	}
	_, err = resolveObjectStoreBlobPath(root, filepath.Join(root, "..", "escape"))
	if !errors.Is(err, workers.ErrInvalid) {
		t.Fatalf("escape error = %v, want workers.ErrInvalid", err)
	}
}

func TestVerifyObjectStoreBlobValid(t *testing.T) {
	root := t.TempDir()
	content := []byte("valid blob\n")
	hashHex := writeObjectStoreBlob(t, root, content)
	candidate := objectStoreBlobCandidate{
		BlobID:        "blob_test",
		HashAlgorithm: "sha256",
		HashHex:       hashHex,
		HashURI:       "sha256:" + hashHex,
		SizeBytes:     int64(len(content)),
		StoragePath:   filepath.Join(root, "blobs", hashHex),
		Status:        "pending",
	}
	check := verifyObjectStoreBlob(root, objectStoreIntegrityConfig{VerifyHashes: true}, candidate)
	if check.Status != "verified" {
		t.Fatalf("status = %q, message=%q", check.Status, check.Message)
	}
	if check.ActualSHA256 != hashHex {
		t.Fatalf("actual hash = %q, want %q", check.ActualSHA256, hashHex)
	}
}

func TestVerifyObjectStoreBlobMissing(t *testing.T) {
	root := t.TempDir()
	candidate := objectStoreBlobCandidate{
		BlobID:      "blob_missing",
		HashHex:     "0000000000000000000000000000000000000000000000000000000000000000",
		SizeBytes:   10,
		StoragePath: filepath.Join(root, "missing"),
	}
	check := verifyObjectStoreBlob(root, objectStoreIntegrityConfig{VerifyHashes: true}, candidate)
	if check.Status != "missing" || check.FindingKind != maintenance.FindingKindObjectStoreBlobMissing {
		t.Fatalf("check = %#v, want missing finding", check)
	}
}

func TestVerifyObjectStoreBlobSizeMismatch(t *testing.T) {
	root := t.TempDir()
	hashHex := writeObjectStoreBlob(t, root, []byte("short"))
	candidate := objectStoreBlobCandidate{
		BlobID:      "blob_size",
		HashHex:     hashHex,
		SizeBytes:   100,
		StoragePath: filepath.Join(root, "blobs", hashHex),
	}
	check := verifyObjectStoreBlob(root, objectStoreIntegrityConfig{VerifyHashes: true}, candidate)
	if check.Status != "corrupt" || check.FindingKind != maintenance.FindingKindObjectStoreBlobSizeMismatch {
		t.Fatalf("check = %#v, want size mismatch", check)
	}
}

func TestVerifyObjectStoreBlobHashMismatch(t *testing.T) {
	root := t.TempDir()
	hashHex := writeObjectStoreBlob(t, root, []byte("actual"))
	candidate := objectStoreBlobCandidate{
		BlobID:      "blob_hash",
		HashHex:     "0000000000000000000000000000000000000000000000000000000000000000",
		SizeBytes:   int64(len("actual")),
		StoragePath: filepath.Join(root, "blobs", hashHex),
	}
	check := verifyObjectStoreBlob(root, objectStoreIntegrityConfig{VerifyHashes: true}, candidate)
	if check.Status != "corrupt" || check.FindingKind != maintenance.FindingKindObjectStoreBlobCorrupt {
		t.Fatalf("check = %#v, want hash corrupt", check)
	}
}

func TestTargetBlobRefFromRunMetadata(t *testing.T) {
	raw := json.RawMessage(`{"schema_version":"worker_run.metadata.v0.2","request_metadata":{"target_blob_ref":"blob_target"}}`)
	if got := targetBlobRefFromRunMetadata(raw); got != "blob_target" {
		t.Fatalf("target = %q, want blob_target", got)
	}
}

func writeObjectStoreBlob(t *testing.T, root string, content []byte) string {
	t.Helper()
	sum := sha256.Sum256(content)
	hashHex := hex.EncodeToString(sum[:])
	path := filepath.Join(root, "blobs", hashHex)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir blob dir: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	return hashHex
}
