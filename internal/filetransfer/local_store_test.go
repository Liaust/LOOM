package filetransfer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalStorePersistsManifestAndChunks(t *testing.T) {
	store := NewLocalStore(t.TempDir())
	now := time.Date(2026, 6, 7, 15, 0, 0, 0, time.UTC)
	manifest, err := NormalizeManifest(testManifest(12, 5), now)
	if err != nil {
		t.Fatalf("NormalizeManifest returned error: %v", err)
	}
	chunks, err := PlanChunks(manifest.TransferID, manifest.FileSizeBytes, manifest.ChunkSizeBytes)
	if err != nil {
		t.Fatalf("PlanChunks returned error: %v", err)
	}
	chunks[0].Status = ChunkStatusUploaded
	chunks[0].ReceivedBytes = chunks[0].SizeBytes

	if err := store.SaveState(manifest, chunks); err != nil {
		t.Fatalf("SaveState returned error: %v", err)
	}
	dir, err := store.TransferDir(manifest.TransferID)
	if err != nil {
		t.Fatalf("TransferDir returned error: %v", err)
	}
	for _, name := range []string{"manifest.json", "chunks.json"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
	loadedManifest, err := store.LoadManifest(manifest.TransferID)
	if err != nil {
		t.Fatalf("LoadManifest returned error: %v", err)
	}
	if loadedManifest.TransferID != manifest.TransferID || loadedManifest.ChunkCount != 3 {
		t.Fatalf("unexpected loaded manifest: %#v", loadedManifest)
	}
	loadedChunks, err := store.LoadChunks(manifest.TransferID)
	if err != nil {
		t.Fatalf("LoadChunks returned error: %v", err)
	}
	if len(loadedChunks) != 3 || loadedChunks[0].Status != ChunkStatusUploaded {
		t.Fatalf("unexpected loaded chunks: %#v", loadedChunks)
	}
}

func TestLocalStoreListsNewestManifestsFirst(t *testing.T) {
	store := NewLocalStore(t.TempDir())
	first, err := NormalizeManifest(testManifest(1, 1), time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NormalizeManifest first returned error: %v", err)
	}
	first.UpdatedAt = time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	second, err := NormalizeManifest(testManifest(2, 1), time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NormalizeManifest second returned error: %v", err)
	}
	second.SourceRelativePath = "newer.bin"
	second.UpdatedAt = time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC)
	for _, manifest := range []Manifest{first, second} {
		chunks, err := PlanChunks(manifest.TransferID, manifest.FileSizeBytes, manifest.ChunkSizeBytes)
		if err != nil {
			t.Fatalf("PlanChunks returned error: %v", err)
		}
		if err := store.SaveState(manifest, chunks); err != nil {
			t.Fatalf("SaveState returned error: %v", err)
		}
	}
	manifests, err := store.ListManifests(0)
	if err != nil {
		t.Fatalf("ListManifests returned error: %v", err)
	}
	if len(manifests) != 2 || manifests[0].TransferID != second.TransferID {
		t.Fatalf("unexpected list order: %#v", manifests)
	}
}

func TestLocalStoreRejectsUnsafeTransferID(t *testing.T) {
	store := NewLocalStore(t.TempDir())
	_, err := store.TransferDir("../file_transfer_01JTESTBADBADBADBADBAD")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("TransferDir error = %v, want ErrInvalid", err)
	}
}
