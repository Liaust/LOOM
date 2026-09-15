package storagearchive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

func TestArchiveSingleFileCreatesManifestAndArchiveEntry(t *testing.T) {
	ctx := context.Background()
	source := writeArchiveSourceFile(t, "receipt data")
	entry, detail := archiveSourceDetail(t, source, "macbook/Backups/Documents/current/Taxes/receipt.pdf")
	catalog := newArchiveFakeCatalog(detail)
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
	service.Now = fixedArchiveNow

	result, err := service.Archive(ctx, ArchiveInput{
		SourceRef:  entry.CurrentViewPath,
		TargetPath: "main/Archive/Documents/Taxes-2026/receipt.pdf",
	})
	if err != nil {
		t.Fatalf("Archive returned error: %v", err)
	}
	if result.ArchiveManifest.ArchiveManifestID == "" || result.ArchiveManifest.Status != "complete" {
		t.Fatalf("unexpected manifest: %#v", result.ArchiveManifest)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(result.Entries))
	}
	archived := result.Entries[0]
	if archived.ArchiveViewPath != "main/Archive/Documents/Taxes-2026/receipt.pdf" {
		t.Fatalf("archive view path = %q", archived.ArchiveViewPath)
	}
	if got := readArchiveObject(t, archived.ArchiveObjectPath); got != "receipt data" {
		t.Fatalf("archive object = %q", got)
	}
	if _, err := os.Stat(result.ManifestPath); err != nil {
		t.Fatalf("manifest file missing: %v", err)
	}
	archiveKeyRoot := filepath.Join(service.ArchiveRoot, result.Manifest.ArchiveKey)
	if result.ManifestPath != filepath.Join(archiveKeyRoot, "manifest.json") {
		t.Fatalf("manifest path = %q, want archive-key custody", result.ManifestPath)
	}
	if !strings.HasPrefix(archived.ArchiveObjectPath, filepath.Join(archiveKeyRoot, "objects")+string(filepath.Separator)) {
		t.Fatalf("archive object escaped archive-key custody: %q", archived.ArchiveObjectPath)
	}
	if len(catalog.manifests) != 1 || len(catalog.archiveItems) != 1 {
		t.Fatalf("catalog did not record manifest/items: manifests=%d items=%d", len(catalog.manifests), len(catalog.archiveItems))
	}
	if len(result.Manifest.Entries) != 1 {
		t.Fatalf("manifest entries = %d", len(result.Manifest.Entries))
	}
	manifestItem := result.Manifest.Entries[0]
	if manifestItem.SourceStorageEntryID != entry.StorageEntryID ||
		manifestItem.SourceViewPath != entry.CurrentViewPath ||
		manifestItem.ChecksumHex == "" ||
		manifestItem.ArchiveStorageEntryID == "" {
		t.Fatalf("manifest item missing source/checksum/archive id: %#v", manifestItem)
	}
}

func TestArchiveDerivesPortableSHA256AndSizeWhenCatalogEvidenceIsMissing(t *testing.T) {
	ctx := context.Background()
	contents := []byte("readable source without catalog checksum evidence")
	source := writeArchiveSourceFile(t, string(contents))
	entry, detail := archiveSourceDetail(t, source, "source/missing-evidence.txt")
	detail.Entry.ChecksumAlgorithm = ""
	detail.Entry.ChecksumHex = ""
	detail.Entry.SizeBytes = nil
	detail.PhysicalRefs[0].ContentAddress = ""
	catalog := newArchiveFakeCatalog(detail)
	catalog.failCommitPhase = "manifest"
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
	service.Now = fixedArchiveNow
	input := ArchiveInput{SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/Documents/missing-evidence.txt", ArchiveKey: "missing-evidence"}

	if _, err := service.Archive(ctx, input); err == nil {
		t.Fatal("expected injected catalog failure")
	}
	manifestPath := filepath.Join(service.ArchiveRoot, input.ArchiveKey, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest ManifestDocument
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(contents)
	wantChecksum := hex.EncodeToString(wantHash[:])
	item := manifest.Entries[0]
	if item.ChecksumAlgorithm != "sha256" || item.ChecksumHex != wantChecksum || item.SizeBytes == nil || *item.SizeBytes != int64(len(contents)) {
		t.Fatalf("derived manifest evidence = %#v", item)
	}
	if item.ContentKey != "sha256:"+wantChecksum || !strings.HasPrefix(filepath.ToSlash(item.ArchiveObjectPath), "objects/sha256/") {
		t.Fatalf("derived content identity = %#v", item)
	}

	catalog.failCommitPhase = ""
	result, err := service.Archive(ctx, input)
	if err != nil {
		t.Fatalf("retry with derived evidence: %v", err)
	}
	archived := catalog.entries[result.Entries[0].ArchiveStorageEntryID]
	if archived.ChecksumAlgorithm != "sha256" || archived.ChecksumHex != wantChecksum || archived.SizeBytes == nil || *archived.SizeBytes != int64(len(contents)) {
		t.Fatalf("archive catalog lost derived evidence: %#v", archived)
	}
}

func TestArchiveRejectsMismatchedSourceContentAddressBeforePublication(t *testing.T) {
	source := writeArchiveSourceFile(t, "content address mismatch")
	entry, detail := archiveSourceDetail(t, source, "source/mismatch.txt")
	detail.Entry.ChecksumAlgorithm = ""
	detail.Entry.ChecksumHex = ""
	detail.Entry.SizeBytes = nil
	detail.PhysicalRefs[0].ContentAddress = "sha256:" + strings.Repeat("0", 64)
	catalog := newArchiveFakeCatalog(detail)
	root := filepath.Join(t.TempDir(), "archive")
	service := NewService(catalog, root)
	_, err := service.Archive(context.Background(), ArchiveInput{
		SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/Documents/mismatch.txt", ArchiveKey: "mismatch",
	})
	if err == nil || !strings.Contains(err.Error(), "content address changed") {
		t.Fatalf("content-address mismatch error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "mismatch")); !os.IsNotExist(statErr) {
		t.Fatalf("mismatched source published archive key: %v", statErr)
	}
	if len(catalog.manifests) != 0 {
		t.Fatalf("mismatched source changed catalog: %#v", catalog.manifests)
	}
}

func TestArchiveCatalogContractUsesMandatoryAtomicCommit(t *testing.T) {
	ctx := context.Background()
	source := writeArchiveSourceFile(t, "atomic only")
	entry, detail := archiveSourceDetail(t, source, "source/atomic.txt")
	inner := newArchiveFakeCatalog(detail)
	catalog := &atomicOnlyArchiveCatalog{inner: inner}
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
	if _, err := service.Archive(ctx, ArchiveInput{SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/Documents/atomic.txt"}); err != nil {
		t.Fatalf("archive with atomic-only catalog: %v", err)
	}
	if catalog.commitCalls != 1 || len(inner.manifests) != 1 || len(inner.archiveItems) != 1 {
		t.Fatalf("atomic commits=%d manifests=%d items=%d", catalog.commitCalls, len(inner.manifests), len(inner.archiveItems))
	}
}

func TestArchiveCatalogCommitFailuresExposeNoPartialStateAndRetry(t *testing.T) {
	for _, phase := range []string{"manifest", "entry", "ref", "item", "source"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			source := writeArchiveSourceFile(t, "retryable archive")
			entry, detail := archiveSourceDetail(t, source, "macbook/Backups/Documents/current/Retry/file.txt")
			catalog := newArchiveFakeCatalog(detail)
			catalog.failCommitPhase = phase
			service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
			service.Now = fixedArchiveNow
			input := ArchiveInput{
				SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/Documents/Retry/file.txt",
				MarkSourceArchived: true,
			}
			first, err := service.Archive(ctx, input)
			if err == nil {
				t.Fatalf("expected injected %s failure", phase)
			}
			if len(catalog.manifests) != 0 || len(catalog.archiveItems) != 0 || len(catalog.markedArchived) != 0 {
				t.Fatalf("partial catalog state after %s failure: manifests=%d items=%d archived=%v", phase, len(catalog.manifests), len(catalog.archiveItems), catalog.markedArchived)
			}
			if got := catalog.entries[entry.StorageEntryID].AvailabilityState; got != storagecatalog.AvailabilityStateAvailable {
				t.Fatalf("source state after %s failure = %q", phase, got)
			}
			if first.ManifestPath != "" {
				t.Fatalf("failed result unexpectedly reported a manifest path: %#v", first)
			}
			normalized, normalizeErr := service.normalizeInput(input)
			if normalizeErr != nil {
				t.Fatal(normalizeErr)
			}
			key := archiveKeyForInput(normalized)
			if _, err := os.Stat(filepath.Join(service.ArchiveRoot, key, "manifest.json")); err != nil {
				t.Fatalf("complete filesystem commit should survive catalog retry: %v", err)
			}

			catalog.failCommitPhase = ""
			result, err := service.Archive(ctx, input)
			if err != nil {
				t.Fatalf("retry after %s failure: %v", phase, err)
			}
			if len(catalog.manifests) != 1 || len(catalog.archiveItems) != 1 || len(catalog.markedArchived) != 1 {
				t.Fatalf("retry did not atomically commit catalog: manifests=%d items=%d archived=%v", len(catalog.manifests), len(catalog.archiveItems), catalog.markedArchived)
			}
			if _, err := service.Archive(ctx, input); err != nil {
				t.Fatalf("idempotent repeated retry: %v", err)
			}
			if len(catalog.manifests) != 1 || len(catalog.archiveItems) != 1 || len(catalog.markedArchived) != 1 {
				t.Fatalf("repeated retry duplicated state: manifests=%d items=%d archived=%v", len(catalog.manifests), len(catalog.archiveItems), catalog.markedArchived)
			}
			if result.Manifest.Entries[0].ArchiveObjectPath == "" || filepath.IsAbs(result.Manifest.Entries[0].ArchiveObjectPath) {
				t.Fatalf("manifest object evidence is not portable: %#v", result.Manifest.Entries[0])
			}
		})
	}
}

func TestArchiveCatalogRecoveryUsesPublishedCustodyWithoutReadableSource(t *testing.T) {
	ctx := context.Background()
	source := writeArchiveSourceFile(t, "published custody is the recovery source")
	entry, detail := archiveSourceDetail(t, source, "macbook/Backups/Documents/current/Recovery/file.txt")
	catalog := newArchiveFakeCatalog(detail)
	catalog.failCommitPhase = "manifest"
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
	service.Now = fixedArchiveNow
	input := ArchiveInput{
		SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/Documents/Recovery/file.txt",
		ArchiveKey: "published-recovery", MarkSourceArchived: true,
	}

	if _, err := service.Archive(ctx, input); err == nil {
		t.Fatal("expected injected catalog failure")
	}
	manifestPath := filepath.Join(service.ArchiveRoot, input.ArchiveKey, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest ManifestDocument
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	item := manifest.Entries[0]
	if manifest.CatalogRecovery != CatalogRecoverySchemaVersion || item.SourceOriginalPath != entry.CurrentViewPath || item.SourceMimeType != entry.MimeType ||
		item.SourceRefKind != detail.PhysicalRefs[0].RefKind || item.SourceRefURI != detail.PhysicalRefs[0].URI {
		t.Fatalf("published manifest lacks catalog recovery evidence: %#v", item)
	}

	inspectCalls := catalog.inspectCalls
	catalog.failCommitPhase = ""
	catalog.failInspect = true
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	result, err := service.Archive(ctx, input)
	if err != nil {
		t.Fatalf("recover from published custody: %v", err)
	}
	if catalog.inspectCalls != inspectCalls {
		t.Fatalf("recovery inspected original catalog source: before=%d after=%d", inspectCalls, catalog.inspectCalls)
	}
	if got := catalog.entries[entry.StorageEntryID].AvailabilityState; got != storagecatalog.AvailabilityStateArchived {
		t.Fatalf("source disposition = %q, want archived", got)
	}
	archived := catalog.entries[result.Entries[0].ArchiveStorageEntryID]
	if archived.OriginalSourcePath != entry.CurrentViewPath || archived.MimeType != entry.MimeType || archived.ChecksumHex != entry.ChecksumHex {
		t.Fatalf("recovered archive entry lost exact manifest evidence: %#v", archived)
	}
	var archiveMetadata map[string]any
	if err := json.Unmarshal(archived.Metadata, &archiveMetadata); err != nil {
		t.Fatal(err)
	}
	if archiveMetadata["source_ref_kind"] != detail.PhysicalRefs[0].RefKind || archiveMetadata["source_uri"] != detail.PhysicalRefs[0].URI ||
		archiveMetadata["source_storage_entry_id"] != entry.StorageEntryID {
		t.Fatalf("recovered archive metadata lost source evidence: %#v", archiveMetadata)
	}
	if result.Entries[0].ArchiveRef == nil || result.Entries[0].ArchiveRef.StoragePhysicalRefID != item.ArchivePhysicalRefID ||
		result.Entries[0].ArchiveStorageEntryID != item.ArchiveStorageEntryID {
		t.Fatalf("recovery changed persisted archive identities: %#v", result.Entries[0])
	}
	if _, err := service.Archive(ctx, input); err != nil {
		t.Fatalf("idempotent published-custody retry: %v", err)
	}
	if catalog.inspectCalls != inspectCalls {
		t.Fatalf("idempotent retry inspected original source: before=%d after=%d", inspectCalls, catalog.inspectCalls)
	}
}

func TestArchiveUnknownCatalogResultRetriesFromPublishedEvidence(t *testing.T) {
	source := writeArchiveSourceFile(t, "unknown result retry")
	entry, detail := archiveSourceDetail(t, source, "source/unknown.txt")
	catalog := newArchiveFakeCatalog(detail)
	catalog.failAfterCommit = true
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
	input := ArchiveInput{
		SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/Documents/unknown.txt",
		ArchiveKey: "unknown-result", MarkSourceArchived: true,
	}
	if _, err := service.Archive(context.Background(), input); err == nil || !strings.Contains(err.Error(), "unknown result") {
		t.Fatalf("unknown catalog result error = %v", err)
	}
	if len(catalog.manifests) != 1 || len(catalog.archiveItems) != 1 || len(catalog.markedArchived) != 1 {
		t.Fatalf("unknown result did not commit exactly once: manifests=%d items=%d archived=%v", len(catalog.manifests), len(catalog.archiveItems), catalog.markedArchived)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	catalog.failInspect = true
	result, err := service.Archive(context.Background(), input)
	if err != nil {
		t.Fatalf("retry unknown result from published custody: %v", err)
	}
	if len(catalog.manifests) != 1 || len(catalog.archiveItems) != 1 || len(catalog.markedArchived) != 1 || len(result.Entries) != 1 {
		t.Fatalf("unknown-result retry duplicated catalog state: manifests=%d items=%d archived=%v result=%#v", len(catalog.manifests), len(catalog.archiveItems), catalog.markedArchived, result)
	}
}

func TestArchiveCatalogRecoveryRejectsTamperedPublishedObjectWithoutSource(t *testing.T) {
	source := writeArchiveSourceFile(t, "published object tamper evidence")
	entry, detail := archiveSourceDetail(t, source, "source/object.txt")
	catalog := newArchiveFakeCatalog(detail)
	catalog.failCommitPhase = "manifest"
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
	input := ArchiveInput{SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/Documents/object.txt", ArchiveKey: "tampered-object"}
	if _, err := service.Archive(context.Background(), input); err == nil {
		t.Fatal("expected injected catalog failure")
	}
	manifestRaw, err := os.ReadFile(filepath.Join(service.ArchiveRoot, input.ArchiveKey, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest ManifestDocument
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(service.ArchiveRoot, input.ArchiveKey, filepath.FromSlash(manifest.Entries[0].ArchiveObjectPath))
	payload, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	payload[0] ^= 0xff
	if err := os.WriteFile(objectPath, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	catalog.failCommitPhase = ""
	catalog.failInspect = true
	if _, err := service.Archive(context.Background(), input); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered published object error = %v", err)
	}
	if len(catalog.manifests) != 0 || len(catalog.archiveItems) != 0 {
		t.Fatalf("tampered object changed catalog: manifests=%d items=%d", len(catalog.manifests), len(catalog.archiveItems))
	}
}

func TestArchiveCatalogRecoveryRejectsInRootObjectParentSymlink(t *testing.T) {
	source := writeArchiveSourceFile(t, "published parent path evidence")
	entry, detail := archiveSourceDetail(t, source, "source/parent.txt")
	catalog := newArchiveFakeCatalog(detail)
	catalog.failCommitPhase = "manifest"
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
	input := ArchiveInput{SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/Documents/parent.txt", ArchiveKey: "parent-symlink"}
	if _, err := service.Archive(context.Background(), input); err == nil {
		t.Fatal("expected injected catalog failure")
	}
	manifestRaw, err := os.ReadFile(filepath.Join(service.ArchiveRoot, input.ArchiveKey, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest ManifestDocument
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	objectRelative := filepath.FromSlash(manifest.Entries[0].ArchiveObjectPath)
	algorithmDirectory := filepath.Join(service.ArchiveRoot, input.ArchiveKey, filepath.Dir(filepath.Dir(objectRelative)))
	realDirectory := algorithmDirectory + "-real"
	if err := os.Rename(algorithmDirectory, realDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(realDirectory), algorithmDirectory); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	catalog.failCommitPhase = ""
	catalog.failInspect = true
	if _, err := service.Archive(context.Background(), input); err == nil || !strings.Contains(err.Error(), "must be a real directory") {
		t.Fatalf("in-root parent symlink error = %v", err)
	}
	if len(catalog.manifests) != 0 || len(catalog.archiveItems) != 0 {
		t.Fatalf("parent symlink changed catalog: manifests=%d items=%d", len(catalog.manifests), len(catalog.archiveItems))
	}
}

func TestArchivePublishedObjectVerificationHonorsCancellationAndReleasesKey(t *testing.T) {
	source := writeArchiveSourceFile(t, strings.Repeat("x", 3<<20))
	entry, detail := archiveSourceDetail(t, source, "source/large.bin")
	catalog := newArchiveFakeCatalog(detail)
	catalog.failCommitPhase = "manifest"
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
	input := ArchiveInput{SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/Documents/large.bin", ArchiveKey: "cancel-recovery"}
	if _, err := service.Archive(context.Background(), input); err == nil {
		t.Fatal("expected injected catalog failure")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	catalog.failCommitPhase = ""
	catalog.failInspect = true
	cancelContext := &cancelAfterChecksContext{Context: context.Background(), cancelAt: 3}
	if _, err := service.Archive(cancelContext, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled verification error = %v", err)
	}
	if cancelContext.checks < cancelContext.cancelAt {
		t.Fatalf("verification did not observe deterministic cancellation: %#v", cancelContext)
	}
	if _, err := service.Archive(context.Background(), input); err != nil {
		t.Fatalf("archive-key operation remained locked after cancellation: %v", err)
	}
}

func TestArchiveFilesystemFailuresDoNotPublishPartialKeyAndRetry(t *testing.T) {
	ctx := context.Background()
	source := writeArchiveSourceFile(t, "filesystem retry")
	entry, detail := archiveSourceDetail(t, source, "macbook/Backups/Documents/current/Retry/fs.txt")
	catalog := newArchiveFakeCatalog(detail)
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
	input := ArchiveInput{SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/Documents/Retry/fs.txt"}
	normalized, err := service.normalizeInput(input)
	if err != nil {
		t.Fatal(err)
	}
	key := archiveKeyForInput(normalized)

	service.writeManifestHook = func(string, []byte) error { return fmt.Errorf("injected manifest publication failure") }
	if _, err := service.Archive(ctx, input); err == nil || !strings.Contains(err.Error(), "manifest publication") {
		t.Fatalf("manifest publication error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(service.ArchiveRoot, key)); !os.IsNotExist(err) {
		t.Fatalf("manifest failure published archive key: %v", err)
	}
	if len(catalog.manifests) != 0 {
		t.Fatalf("manifest failure changed catalog: %#v", catalog.manifests)
	}
	service.writeManifestHook = nil
	if _, err := service.Archive(ctx, input); err != nil {
		t.Fatalf("retry after manifest publication failure: %v", err)
	}

	secondRoot := filepath.Join(t.TempDir(), "archive")
	secondCatalog := newArchiveFakeCatalog(detail)
	second := NewService(secondCatalog, secondRoot)
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Archive(ctx, input); err == nil {
		t.Fatal("expected object materialization failure")
	}
	if _, err := os.Stat(filepath.Join(secondRoot, key)); !os.IsNotExist(err) {
		t.Fatalf("object failure published archive key: %v", err)
	}
	if len(secondCatalog.manifests) != 0 {
		t.Fatalf("object failure changed catalog: %#v", secondCatalog.manifests)
	}
	if err := os.WriteFile(source, []byte("filesystem retry"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Archive(ctx, input); err != nil {
		t.Fatalf("retry after object materialization failure: %v", err)
	}
}

func TestArchiveRetryRejectsTamperedPublishedCustodyIdentity(t *testing.T) {
	for _, mutate := range []struct {
		name string
		edit func(*ManifestItem)
	}{
		{name: "logical path", edit: func(item *ManifestItem) {
			item.ArchiveLogicalPath = "Documents/Other/file.txt"
			item.ArchiveViewPath = "main/Archive/Documents/Other/file.txt"
		}},
		{name: "content key", edit: func(item *ManifestItem) {
			item.ContentKey = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "object path", edit: func(item *ManifestItem) {
			item.ArchiveObjectPath = filepath.ToSlash(filepath.Join("objects", "sha256", "00", strings.Repeat("0", 64)))
		}},
		{name: "source ref", edit: func(item *ManifestItem) {
			item.SourceRefURI = ""
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			ctx := context.Background()
			source := writeArchiveSourceFile(t, "tamper evidence")
			entry, detail := archiveSourceDetail(t, source, "source/file.txt")
			catalog := newArchiveFakeCatalog(detail)
			catalog.failCommitPhase = "manifest"
			service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
			input := ArchiveInput{SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/Documents/file.txt", ArchiveKey: "tamper-key"}
			if _, err := service.Archive(ctx, input); err == nil {
				t.Fatal("expected injected catalog failure")
			}
			manifestPath := filepath.Join(service.ArchiveRoot, input.ArchiveKey, "manifest.json")
			raw, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			var manifest ManifestDocument
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			mutate.edit(&manifest.Entries[0])
			raw, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, raw, 0o640); err != nil {
				t.Fatal(err)
			}
			catalog.failCommitPhase = ""
			if _, err := service.Archive(ctx, input); err == nil {
				t.Fatal("tampered custody manifest was accepted on retry")
			}
			if len(catalog.manifests) != 0 || len(catalog.archiveItems) != 0 {
				t.Fatalf("tampered retry changed catalog: manifests=%d items=%d", len(catalog.manifests), len(catalog.archiveItems))
			}
		})
	}
}

func TestArchiveRejectsUnsafeOrCollidingArchiveKey(t *testing.T) {
	ctx := context.Background()
	source := writeArchiveSourceFile(t, "archive data")
	entry, detail := archiveSourceDetail(t, source, "source/file.txt")
	catalog := newArchiveFakeCatalog(detail)
	root := filepath.Join(t.TempDir(), "archive")
	service := NewService(catalog, root)

	for _, key := range []string{"../outside", "nested/key", "objects", "manifests", ".hidden", ".locks", ".key.preparing"} {
		_, err := service.Archive(ctx, ArchiveInput{SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/file.txt", ArchiveKey: key})
		if err == nil || !strings.Contains(err.Error(), "archive_key") {
			t.Fatalf("unsafe key %q error = %v", key, err)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("rejected archive key mutated archive root: %v", err)
	}
	if len(catalog.manifests) != 0 {
		t.Fatalf("rejected archive key changed catalog: %#v", catalog.manifests)
	}
	key := "explicit-archive"
	manifestPath := filepath.Join(root, key, "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatalf("mkdir manifest parent: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte(`{"existing":true}`), 0o600); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	_, err := service.Archive(ctx, ArchiveInput{SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/file.txt", ArchiveKey: key})
	if err == nil || !strings.Contains(err.Error(), "different custody evidence") {
		t.Fatalf("colliding key error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, key, "objects")); !os.IsNotExist(err) {
		t.Fatalf("collision created objects before failing: %v", err)
	}
}

func TestArchiveKeySegmentKeepsAcceptedUserKeysVisible(t *testing.T) {
	for _, key := range []string{"committed-key", "_underscored", "punctuation-2026_08"} {
		got, err := archiveKeySegment(key)
		if err != nil || got != key || strings.HasPrefix(got, ".") {
			t.Fatalf("archiveKeySegment(%q) = %q, %v", key, got, err)
		}
	}
	automatic := archiveKeyForInput(ArchiveInput{
		SourceRef: "source/file.txt", TargetPath: "main/Archive/.hidden/file.txt",
		ArchiveKind: "document_archive", OwnerNodeKey: "main",
	})
	if strings.HasPrefix(automatic, ".") {
		t.Fatalf("automatic archive key entered hidden namespace: %q", automatic)
	}
	if _, err := archiveKeySegment(automatic); err != nil {
		t.Fatalf("automatic visible archive key was rejected: %v", err)
	}
}

func TestArchiveRejectsSymlinkedArchiveKeyWithoutChangingExternalFiles(t *testing.T) {
	ctx := context.Background()
	temp := t.TempDir()
	archiveRoot := filepath.Join(temp, "archive")
	external := filepath.Join(temp, "external")
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(external, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(external, "sentinel")
	if err := os.WriteFile(sentinel, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(archiveRoot, "hostile-key")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	source := writeArchiveSourceFile(t, "archive data")
	entry, detail := archiveSourceDetail(t, source, "source/file.txt")
	service := NewService(newArchiveFakeCatalog(detail), archiveRoot)
	_, err := service.Archive(ctx, ArchiveInput{SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/file.txt", ArchiveKey: "hostile-key"})
	if err == nil {
		t.Fatal("archive followed hostile archive-key symlink")
	}
	got, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(got) != "safe" {
		t.Fatalf("external sentinel changed: %q err=%v", got, readErr)
	}
}

func TestArchiveFolderTreeCreatesArchiveSubtree(t *testing.T) {
	ctx := context.Background()
	firstSource := writeArchiveSourceFile(t, "alpha")
	secondSource := writeArchiveSourceFile(t, "beta")
	_, firstDetail := archiveSourceDetail(t, firstSource, "macbook/Backups/Documents/current/Taxes/receipt-a.txt")
	_, secondDetail := archiveSourceDetail(t, secondSource, "macbook/Backups/Documents/current/Taxes/Sub/receipt-b.txt")
	catalog := newArchiveFakeCatalog(firstDetail, secondDetail)
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))

	result, err := service.Archive(ctx, ArchiveInput{
		SourceRef:  "macbook/Backups/Documents/current/Taxes",
		TargetPath: "main/Archive/Documents/Taxes-2026",
	})
	if err != nil {
		t.Fatalf("Archive returned error: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(result.Entries))
	}
	gotPaths := archiveResultPaths(result)
	wantPaths := []string{
		"main/Archive/Documents/Taxes-2026/Sub/receipt-b.txt",
		"main/Archive/Documents/Taxes-2026/receipt-a.txt",
	}
	if fmt.Sprint(gotPaths) != fmt.Sprint(wantPaths) {
		t.Fatalf("archive paths = %v, want %v", gotPaths, wantPaths)
	}
}

func TestArchiveSourceListingPaginatesBeforeMarkingArchived(t *testing.T) {
	ctx := context.Background()
	firstSource := writeArchiveSourceFile(t, "alpha")
	secondSource := writeArchiveSourceFile(t, "beta")
	firstEntry, firstDetail := archiveSourceDetail(t, firstSource, "macbook/Backups/Documents/current/Paged/a.txt")
	secondEntry, secondDetail := archiveSourceDetail(t, secondSource, "macbook/Backups/Documents/current/Paged/b.txt")
	catalog := newArchiveFakeCatalog(firstDetail, secondDetail)
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
	service.MaxEntriesPerArchive = 1

	result, err := service.Archive(ctx, ArchiveInput{
		SourceRef:          "macbook/Backups/Documents/current/Paged",
		TargetPath:         "main/Archive/Documents/Paged",
		MarkSourceArchived: true,
	})
	if err != nil {
		t.Fatalf("Archive returned error: %v", err)
	}
	if !result.Manifest.Complete || result.Manifest.ExpectedEntryCount != 2 || result.Manifest.ArchivedEntryCount != 2 {
		t.Fatalf("manifest completeness = %#v", result.Manifest)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(result.Entries))
	}
	if fmt.Sprint(catalog.markedArchived) != fmt.Sprint([]string{firstEntry.StorageEntryID, secondEntry.StorageEntryID}) &&
		fmt.Sprint(catalog.markedArchived) != fmt.Sprint([]string{secondEntry.StorageEntryID, firstEntry.StorageEntryID}) {
		t.Fatalf("marked archived = %v", catalog.markedArchived)
	}
	if len(catalog.listFilters) < 3 || catalog.listFilters[0].Offset != 0 || catalog.listFilters[1].Offset != 1 || catalog.listFilters[2].Offset != 2 {
		t.Fatalf("list filters did not paginate: %#v", catalog.listFilters)
	}
	for _, filter := range catalog.listFilters {
		if filter.Limit != 1 || filter.SourceArea != storagecatalog.SourceAreaDocuments || filter.OriginNodeKey != "macbook" {
			t.Fatalf("archive source lookup was not catalog-bounded: %#v", filter)
		}
		if fmt.Sprint(filter.PathPrefixes) != fmt.Sprint([]string{"macbook/Backups/Documents/current/Paged", "Paged"}) {
			t.Fatalf("archive source path prefixes = %v", filter.PathPrefixes)
		}
	}
}

func TestArchiveRejectsIncompleteFinalKeyWithoutOverwritingObjects(t *testing.T) {
	ctx := context.Background()
	source := writeArchiveSourceFile(t, "correct")
	entry, detail := archiveSourceDetail(t, source, "macbook/Backups/Documents/current/Dedupe/file.txt")
	catalog := newArchiveFakeCatalog(detail)
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))
	ref := detail.PhysicalRefs[0]
	objectPath := service.archiveObjectPath("dedupe-test", contentKeyForEntry(entry, ref), entry)
	if err := os.MkdirAll(filepath.Dir(objectPath), 0o770); err != nil {
		t.Fatalf("mkdir archive object dir: %v", err)
	}
	if err := os.WriteFile(objectPath, []byte("wrong"), 0o640); err != nil {
		t.Fatalf("seed bad object: %v", err)
	}

	_, err := service.Archive(ctx, ArchiveInput{
		SourceRef:  entry.CurrentViewPath,
		TargetPath: "main/Archive/Documents/Dedupe/file.txt",
		ArchiveKey: "dedupe-test",
	})
	if err == nil || !strings.Contains(err.Error(), "without a committed manifest") {
		t.Fatalf("expected incomplete archive-key failure, got %v", err)
	}
	if got := readArchiveObject(t, objectPath); got != "wrong" {
		t.Fatalf("bad existing object should not be overwritten, got %q", got)
	}
}

func TestArchiveDedupesDuplicateContentWithinRun(t *testing.T) {
	ctx := context.Background()
	firstSource := writeArchiveSourceFile(t, "same")
	secondSource := writeArchiveSourceFile(t, "same")
	_, firstDetail := archiveSourceDetail(t, firstSource, "macbook/Backups/Documents/current/Duplicates/a.txt")
	_, secondDetail := archiveSourceDetail(t, secondSource, "macbook/Backups/Documents/current/Duplicates/b.txt")
	catalog := newArchiveFakeCatalog(firstDetail, secondDetail)
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))

	result, err := service.Archive(ctx, ArchiveInput{
		SourceRef:  "macbook/Backups/Documents/current/Duplicates",
		TargetPath: "main/Archive/Documents/Duplicates",
	})
	if err != nil {
		t.Fatalf("Archive returned error: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(result.Entries))
	}
	if result.Entries[0].ArchiveObjectPath != result.Entries[1].ArchiveObjectPath {
		t.Fatalf("duplicate content should share object path: %#v", result.Entries)
	}
	if !result.Entries[1].Deduped {
		t.Fatalf("second duplicate should be marked deduped: %#v", result.Entries)
	}
}

func TestArchiveDedupesDuplicateContentAcrossArchiveKeys(t *testing.T) {
	ctx := context.Background()
	source := writeArchiveSourceFile(t, "same across keys")
	entry, detail := archiveSourceDetail(t, source, "macbook/Backups/Documents/current/Duplicates/across.txt")
	catalog := newArchiveFakeCatalog(detail)
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))

	first, err := service.Archive(ctx, ArchiveInput{
		SourceRef:  entry.CurrentViewPath,
		TargetPath: "main/Archive/Documents/First/across.txt",
		ArchiveKey: "first-key",
	})
	if err != nil {
		t.Fatalf("first Archive returned error: %v", err)
	}
	second, err := service.Archive(ctx, ArchiveInput{
		SourceRef:  entry.CurrentViewPath,
		TargetPath: "main/Archive/Documents/Second/across.txt",
		ArchiveKey: "second-key",
	})
	if err != nil {
		t.Fatalf("second Archive returned error: %v", err)
	}
	firstPath := first.Entries[0].ArchiveObjectPath
	secondPath := second.Entries[0].ArchiveObjectPath
	if firstPath == secondPath || !second.Entries[0].Deduped {
		t.Fatalf("cross-key result did not preserve distinct custody paths with dedup evidence: first=%#v second=%#v", first.Entries[0], second.Entries[0])
	}
	firstInfo, err := os.Stat(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(firstInfo, secondInfo) {
		t.Fatal("cross-key archive objects do not share physical content")
	}
}

func TestArchiveRejectsSymlinkedObjectComponentWithoutChangingExternalFiles(t *testing.T) {
	ctx := context.Background()
	temp := t.TempDir()
	archiveRoot := filepath.Join(temp, "archive")
	external := filepath.Join(temp, "external")
	if err := os.MkdirAll(filepath.Join(archiveRoot, "hostile", "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(external, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(external, "sentinel")
	if err := os.WriteFile(sentinel, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(archiveRoot, "hostile", "objects", "sha256")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	source := writeArchiveSourceFile(t, "archive data")
	entry, detail := archiveSourceDetail(t, source, "source/file.txt")
	service := NewService(newArchiveFakeCatalog(detail), archiveRoot)
	_, err := service.Archive(ctx, ArchiveInput{SourceRef: entry.CurrentViewPath, TargetPath: "main/Archive/file.txt", ArchiveKey: "hostile"})
	if err == nil {
		t.Fatalf("symlinked object component error = %v", err)
	}
	got, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(got) != "safe" {
		t.Fatalf("external sentinel changed: %q err=%v", got, readErr)
	}
}

func TestArchiveCanMarkSourceBranchArchived(t *testing.T) {
	ctx := context.Background()
	source := writeArchiveSourceFile(t, "archive me")
	entry, detail := archiveSourceDetail(t, source, "macbook/Backups/Documents/current/ArchiveMe/file.txt")
	catalog := newArchiveFakeCatalog(detail)
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))

	result, err := service.Archive(ctx, ArchiveInput{
		SourceRef:          "macbook/Backups/Documents/current/ArchiveMe",
		TargetPath:         "main/Archive/Documents/ArchiveMe",
		MarkSourceArchived: true,
	})
	if err != nil {
		t.Fatalf("Archive returned error: %v", err)
	}
	if len(catalog.markedArchived) != 1 || catalog.markedArchived[0] != entry.StorageEntryID {
		t.Fatalf("marked archived = %v", catalog.markedArchived)
	}
	updated := catalog.entries[entry.StorageEntryID]
	if updated.AvailabilityState != storagecatalog.AvailabilityStateArchived {
		t.Fatalf("source state = %q", updated.AvailabilityState)
	}
	if result.ArchiveManifest.ArchiveManifestID == "" {
		t.Fatalf("missing archive manifest id")
	}
}

func TestArchiveCanMarkSourceBranchSuperseded(t *testing.T) {
	ctx := context.Background()
	source := writeArchiveSourceFile(t, "supersede me")
	entry, detail := archiveSourceDetail(t, source, "macbook/Backups/Documents/current/SupersedeMe/file.txt")
	catalog := newArchiveFakeCatalog(detail)
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))

	_, err := service.Archive(ctx, ArchiveInput{
		SourceRef:            "macbook/Backups/Documents/current/SupersedeMe",
		TargetPath:           "main/Archive/Documents/SupersedeMe",
		MarkSourceSuperseded: true,
	})
	if err != nil {
		t.Fatalf("Archive returned error: %v", err)
	}
	if len(catalog.markedSuperseded) != 1 || catalog.markedSuperseded[0] != entry.StorageEntryID {
		t.Fatalf("marked superseded = %v", catalog.markedSuperseded)
	}
	updated := catalog.entries[entry.StorageEntryID]
	if updated.AvailabilityState != storagecatalog.AvailabilityStateSuperseded {
		t.Fatalf("source state = %q", updated.AvailabilityState)
	}
}

func TestArchiveEntryAppearsUnderMainArchiveView(t *testing.T) {
	ctx := context.Background()
	source := writeArchiveSourceFile(t, "visible")
	_, detail := archiveSourceDetail(t, source, "macbook/Backups/Documents/current/Visible/file.txt")
	catalog := newArchiveFakeCatalog(detail)
	service := NewService(catalog, filepath.Join(t.TempDir(), "archive"))

	_, err := service.Archive(ctx, ArchiveInput{
		SourceRef:  "macbook/Backups/Documents/current/Visible",
		TargetPath: "main/Archive/Documents/Visible",
	})
	if err != nil {
		t.Fatalf("Archive returned error: %v", err)
	}
	archivedDetail, err := catalog.InspectEntry(ctx, "main/Archive/Documents/Visible/file.txt")
	if err != nil {
		t.Fatalf("archive path not found in catalog: %v", err)
	}
	if archivedDetail.Entry.CurrentViewPath != "main/Archive/Documents/Visible/file.txt" || len(archivedDetail.PhysicalRefs) != 1 {
		t.Fatalf("unexpected archive catalog detail: %#v", archivedDetail)
	}
}

type archiveFakeCatalog struct {
	entries          map[string]storagecatalog.Entry
	details          map[string]storagecatalog.EntryDetail
	manifests        map[string]storagecatalog.ArchiveManifest
	archiveItems     []storagecatalog.ArchiveItemInput
	markedArchived   []string
	markedSuperseded []string
	listFilters      []storagecatalog.ListFilter
	failCommitPhase  string
	failAfterCommit  bool
	failInspect      bool
	inspectCalls     int
}

// atomicOnlyArchiveCatalog deliberately exposes no piecemeal catalog mutation
// methods. It protects the service boundary from regressing to a silent
// RegisterEntry/RegisterPhysicalRef/AddArchiveItem fallback.
type atomicOnlyArchiveCatalog struct {
	inner       *archiveFakeCatalog
	commitCalls int
}

func (c *atomicOnlyArchiveCatalog) ListEntries(ctx context.Context, filter storagecatalog.ListFilter) ([]storagecatalog.Entry, error) {
	return c.inner.ListEntries(ctx, filter)
}

func (c *atomicOnlyArchiveCatalog) InspectEntry(ctx context.Context, ref string) (storagecatalog.EntryDetail, error) {
	return c.inner.InspectEntry(ctx, ref)
}

func (c *atomicOnlyArchiveCatalog) CommitArchive(ctx context.Context, input storagecatalog.ArchiveCommitInput) (storagecatalog.ArchiveCommitResult, error) {
	c.commitCalls++
	return c.inner.CommitArchive(ctx, input)
}

func newArchiveFakeCatalog(details ...storagecatalog.EntryDetail) *archiveFakeCatalog {
	catalog := &archiveFakeCatalog{
		entries:   map[string]storagecatalog.Entry{},
		details:   map[string]storagecatalog.EntryDetail{},
		manifests: map[string]storagecatalog.ArchiveManifest{},
	}
	for _, detail := range details {
		catalog.entries[detail.Entry.StorageEntryID] = detail.Entry
		catalog.details[detail.Entry.StorageEntryID] = detail
	}
	return catalog
}

func (c *archiveFakeCatalog) ListEntries(_ context.Context, filter storagecatalog.ListFilter) ([]storagecatalog.Entry, error) {
	c.listFilters = append(c.listFilters, filter)
	entries := make([]storagecatalog.Entry, 0, len(c.entries))
	for _, entry := range c.entries {
		if filter.SourceArea != "" && entry.SourceArea != filter.SourceArea {
			continue
		}
		if filter.OriginNodeKey != "" && entry.OriginNodeKey != filter.OriginNodeKey {
			continue
		}
		if entry.DeletedAt != nil && !filter.IncludeDeleted {
			continue
		}
		if len(filter.PathPrefixes) > 0 && !archiveFakeEntryMatchesPrefixes(entry, filter.PathPrefixes) {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CurrentViewPath < entries[j].CurrentViewPath
	})
	if filter.Offset > 0 {
		if filter.Offset >= len(entries) {
			return nil, nil
		}
		entries = entries[filter.Offset:]
	}
	if filter.Limit > 0 && filter.Limit < len(entries) {
		entries = entries[:filter.Limit]
	}
	return entries, nil
}

func archiveFakeEntryMatchesPrefixes(entry storagecatalog.Entry, prefixes []string) bool {
	for _, prefix := range prefixes {
		prefix = strings.Trim(strings.ReplaceAll(prefix, "\\", "/"), "/")
		for _, value := range []string{entry.CurrentViewPath, entry.LogicalPath, entry.OriginalSourcePath} {
			value = strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/")
			if value == prefix || strings.HasPrefix(value, prefix+"/") {
				return true
			}
		}
	}
	return false
}

func (c *archiveFakeCatalog) InspectEntry(_ context.Context, ref string) (storagecatalog.EntryDetail, error) {
	c.inspectCalls++
	if c.failInspect {
		return storagecatalog.EntryDetail{}, fmt.Errorf("injected source inspection failure")
	}
	if detail, ok := c.details[ref]; ok {
		return detail, nil
	}
	for _, detail := range c.details {
		if detail.Entry.CurrentViewPath == ref || detail.Entry.LogicalPath == ref {
			return detail, nil
		}
	}
	return storagecatalog.EntryDetail{}, fmt.Errorf("not found: %s", ref)
}

func (c *archiveFakeCatalog) CommitArchive(_ context.Context, input storagecatalog.ArchiveCommitInput) (storagecatalog.ArchiveCommitResult, error) {
	entries := make(map[string]storagecatalog.Entry, len(c.entries)+len(input.Entries))
	details := make(map[string]storagecatalog.EntryDetail, len(c.details)+len(input.Entries))
	manifests := make(map[string]storagecatalog.ArchiveManifest, len(c.manifests)+1)
	for key, value := range c.entries {
		entries[key] = value
	}
	for key, value := range c.details {
		detail := value
		detail.PhysicalRefs = append([]storagecatalog.PhysicalRef(nil), value.PhysicalRefs...)
		details[key] = detail
	}
	for key, value := range c.manifests {
		manifests[key] = value
	}
	archiveItems := append([]storagecatalog.ArchiveItemInput(nil), c.archiveItems...)
	markedArchived := append([]string(nil), c.markedArchived...)
	markedSuperseded := append([]string(nil), c.markedSuperseded...)
	if c.failCommitPhase == "manifest" {
		return storagecatalog.ArchiveCommitResult{}, fmt.Errorf("injected archive manifest failure")
	}
	now := fixedArchiveNow()
	manifest := storagecatalog.ArchiveManifest{
		ArchiveManifestID: input.Manifest.ArchiveManifestID,
		ArchiveKey:        input.Manifest.ArchiveKey, ArchiveKind: input.Manifest.ArchiveKind,
		OwnerNodeKey: input.Manifest.OwnerNodeKey, SourceRef: input.Manifest.SourceRef,
		Status: input.Manifest.Status, ManifestJSON: input.Manifest.ManifestJSON,
		CreatedAt: now, FinalizedAt: input.Manifest.FinalizedAt,
	}
	if current, ok := manifests[manifest.ArchiveManifestID]; ok && !jsonEqual(current.ManifestJSON, manifest.ManifestJSON) {
		return storagecatalog.ArchiveCommitResult{}, fmt.Errorf("archive manifest evidence conflict")
	}
	manifests[manifest.ArchiveManifestID] = manifest
	result := storagecatalog.ArchiveCommitResult{Manifest: manifest}
	for _, tuple := range input.Entries {
		if c.failCommitPhase == "entry" {
			return storagecatalog.ArchiveCommitResult{}, fmt.Errorf("injected archive entry failure")
		}
		entry := archiveFakeEntry(tuple.Entry, now)
		if existing, ok := entries[entry.StorageEntryID]; ok {
			entry = existing
		} else {
			entries[entry.StorageEntryID] = entry
			details[entry.StorageEntryID] = storagecatalog.EntryDetail{Entry: entry}
		}
		if c.failCommitPhase == "ref" {
			return storagecatalog.ArchiveCommitResult{}, fmt.Errorf("injected archive ref failure")
		}
		ref := archiveFakeRef(tuple.Ref, now)
		detail := details[entry.StorageEntryID]
		foundRef := false
		for _, existing := range detail.PhysicalRefs {
			if existing.StoragePhysicalRefID == ref.StoragePhysicalRefID {
				ref = existing
				foundRef = true
				break
			}
		}
		if !foundRef {
			detail.PhysicalRefs = append(detail.PhysicalRefs, ref)
		}
		details[entry.StorageEntryID] = detail
		if c.failCommitPhase == "item" {
			return storagecatalog.ArchiveCommitResult{}, fmt.Errorf("injected archive item failure")
		}
		foundItem := false
		for _, existing := range archiveItems {
			if existing.ArchiveManifestID == tuple.Item.ArchiveManifestID && existing.StorageEntryID == tuple.Item.StorageEntryID {
				foundItem = true
				break
			}
		}
		if !foundItem {
			archiveItems = append(archiveItems, tuple.Item)
		}
		result.Entries = append(result.Entries, entry)
		result.Refs = append(result.Refs, ref)
	}
	if c.failCommitPhase == "source" && len(input.SourceDispositions) > 0 {
		return storagecatalog.ArchiveCommitResult{}, fmt.Errorf("injected archive source disposition failure")
	}
	for _, disposition := range input.SourceDispositions {
		entry, ok := entries[disposition.StorageEntryID]
		if !ok || (entry.AvailabilityState != disposition.ExpectedAvailability && entry.AvailabilityState != disposition.AvailabilityState) {
			return storagecatalog.ArchiveCommitResult{}, fmt.Errorf("source disposition conflicts with catalog evidence")
		}
		entry.AvailabilityState = disposition.AvailabilityState
		entries[entry.StorageEntryID] = entry
		detail := details[entry.StorageEntryID]
		detail.Entry = entry
		details[entry.StorageEntryID] = detail
		if disposition.AvailabilityState == storagecatalog.AvailabilityStateArchived {
			if !containsString(markedArchived, disposition.StorageEntryID) {
				markedArchived = append(markedArchived, disposition.StorageEntryID)
			}
		} else {
			if !containsString(markedSuperseded, disposition.StorageEntryID) {
				markedSuperseded = append(markedSuperseded, disposition.StorageEntryID)
			}
		}
	}
	c.entries, c.details, c.manifests = entries, details, manifests
	c.archiveItems, c.markedArchived, c.markedSuperseded = archiveItems, markedArchived, markedSuperseded
	if c.failAfterCommit {
		c.failAfterCommit = false
		return storagecatalog.ArchiveCommitResult{}, fmt.Errorf("injected unknown result after catalog commit")
	}
	return result, nil
}

func containsString(values []string, value string) bool {
	for _, current := range values {
		if current == value {
			return true
		}
	}
	return false
}

func jsonEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftJSON, leftErr := json.Marshal(leftValue)
	rightJSON, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func archiveFakeEntry(input storagecatalog.RegisterEntryInput, now time.Time) storagecatalog.Entry {
	entry := storagecatalog.Entry{
		StorageEntryID: input.StorageEntryID, StorageClass: input.StorageClass, SourceArea: input.SourceArea,
		OriginNodeKey: input.OriginNodeKey, LogicalPath: input.LogicalPath, OriginalSourcePath: input.OriginalSourcePath,
		CurrentViewPath: input.CurrentViewPath, ChecksumAlgorithm: input.ChecksumAlgorithm, ChecksumHex: input.ChecksumHex,
		SizeBytes: input.SizeBytes, MimeType: input.MimeType, FileClass: input.FileClass,
		ProcessingState: input.ProcessingState, AvailabilityState: input.AvailabilityState,
		RetentionState: input.RetentionState, Metadata: input.Metadata, CreatedAt: now, UpdatedAt: now,
	}
	if input.ArchiveManifestID != "" {
		entry.ArchiveManifestID = &input.ArchiveManifestID
	}
	return entry
}

func archiveFakeRef(input storagecatalog.RegisterPhysicalRefInput, now time.Time) storagecatalog.PhysicalRef {
	return storagecatalog.PhysicalRef{
		StoragePhysicalRefID: input.StoragePhysicalRefID, StorageEntryID: input.StorageEntryID,
		RefKind: input.RefKind, URI: input.URI, NodeKey: input.NodeKey,
		ContentAddress: input.ContentAddress, Status: input.Status, Metadata: input.Metadata,
		CreatedAt: now, UpdatedAt: now,
	}
}

func (c *archiveFakeCatalog) RegisterEntry(_ context.Context, input storagecatalog.RegisterEntryInput) (storagecatalog.Entry, error) {
	if input.StorageEntryID == "" {
		input.StorageEntryID = ids.NewStorageEntryID()
	}
	now := fixedArchiveNow()
	entry := storagecatalog.Entry{
		StorageEntryID:     input.StorageEntryID,
		StorageClass:       input.StorageClass,
		SourceArea:         input.SourceArea,
		OriginNodeKey:      input.OriginNodeKey,
		LogicalPath:        input.LogicalPath,
		OriginalSourcePath: input.OriginalSourcePath,
		CurrentViewPath:    input.CurrentViewPath,
		ChecksumAlgorithm:  input.ChecksumAlgorithm,
		ChecksumHex:        input.ChecksumHex,
		SizeBytes:          input.SizeBytes,
		MimeType:           input.MimeType,
		FileClass:          input.FileClass,
		ProcessingState:    input.ProcessingState,
		AvailabilityState:  input.AvailabilityState,
		RetentionState:     input.RetentionState,
		Metadata:           input.Metadata,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if input.ArchiveManifestID != "" {
		entry.ArchiveManifestID = &input.ArchiveManifestID
	}
	c.entries[entry.StorageEntryID] = entry
	c.details[entry.StorageEntryID] = storagecatalog.EntryDetail{Entry: entry}
	return entry, nil
}

func (c *archiveFakeCatalog) RegisterPhysicalRef(_ context.Context, input storagecatalog.RegisterPhysicalRefInput) (storagecatalog.PhysicalRef, error) {
	if input.StoragePhysicalRefID == "" {
		input.StoragePhysicalRefID = ids.NewStoragePhysicalRefID()
	}
	now := fixedArchiveNow()
	ref := storagecatalog.PhysicalRef{
		StoragePhysicalRefID: input.StoragePhysicalRefID,
		StorageEntryID:       input.StorageEntryID,
		RefKind:              input.RefKind,
		URI:                  input.URI,
		NodeKey:              input.NodeKey,
		ContentAddress:       input.ContentAddress,
		Status:               input.Status,
		Metadata:             input.Metadata,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	detail := c.details[input.StorageEntryID]
	detail.PhysicalRefs = append(detail.PhysicalRefs, ref)
	c.details[input.StorageEntryID] = detail
	return ref, nil
}

func (c *archiveFakeCatalog) FindAvailablePhysicalRefsByContentAddresses(_ context.Context, refKind string, contentAddresses []string) (map[string]storagecatalog.PhysicalRef, error) {
	wanted := make(map[string]struct{}, len(contentAddresses))
	for _, contentAddress := range contentAddresses {
		wanted[contentAddress] = struct{}{}
	}
	selected := make(map[string]storagecatalog.PhysicalRef, len(wanted))
	for _, detail := range c.details {
		for _, ref := range detail.PhysicalRefs {
			if _, ok := wanted[ref.ContentAddress]; !ok || ref.RefKind != refKind || ref.Status != storagecatalog.PhysicalRefStatusAvailable {
				continue
			}
			current, found := selected[ref.ContentAddress]
			if !found || ref.UpdatedAt.After(current.UpdatedAt) || (ref.UpdatedAt.Equal(current.UpdatedAt) && ref.StoragePhysicalRefID < current.StoragePhysicalRefID) {
				selected[ref.ContentAddress] = ref
			}
		}
	}
	return selected, nil
}

func (c *archiveFakeCatalog) CreateArchiveManifest(_ context.Context, input storagecatalog.CreateArchiveManifestInput) (storagecatalog.ArchiveManifest, error) {
	if input.ArchiveManifestID == "" {
		input.ArchiveManifestID = ids.NewStorageArchiveManifestID()
	}
	now := fixedArchiveNow()
	manifest := storagecatalog.ArchiveManifest{
		ArchiveManifestID: input.ArchiveManifestID,
		ArchiveKey:        input.ArchiveKey,
		ArchiveKind:       input.ArchiveKind,
		OwnerNodeKey:      input.OwnerNodeKey,
		SourceRef:         input.SourceRef,
		Status:            input.Status,
		ManifestJSON:      input.ManifestJSON,
		CreatedAt:         now,
		FinalizedAt:       input.FinalizedAt,
	}
	c.manifests[manifest.ArchiveManifestID] = manifest
	return manifest, nil
}

func (c *archiveFakeCatalog) AddArchiveItem(_ context.Context, input storagecatalog.ArchiveItemInput) error {
	c.archiveItems = append(c.archiveItems, input)
	return nil
}

func (c *archiveFakeCatalog) MarkEntriesArchived(_ context.Context, storageEntryIDs []string, _ string) error {
	c.markedArchived = append(c.markedArchived, storageEntryIDs...)
	c.markEntriesState(storageEntryIDs, storagecatalog.AvailabilityStateArchived)
	return nil
}

func (c *archiveFakeCatalog) MarkEntriesSuperseded(_ context.Context, storageEntryIDs []string, _ string) error {
	c.markedSuperseded = append(c.markedSuperseded, storageEntryIDs...)
	c.markEntriesState(storageEntryIDs, storagecatalog.AvailabilityStateSuperseded)
	return nil
}

func (c *archiveFakeCatalog) markEntriesState(storageEntryIDs []string, state string) {
	for _, id := range storageEntryIDs {
		entry := c.entries[id]
		entry.AvailabilityState = state
		c.entries[id] = entry
		detail := c.details[id]
		detail.Entry = entry
		c.details[id] = detail
	}
}

func archiveSourceDetail(t *testing.T, sourcePath, viewPath string) (storagecatalog.Entry, storagecatalog.EntryDetail) {
	t.Helper()
	bytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	sum := sha256.Sum256(bytes)
	size := int64(len(bytes))
	now := fixedArchiveNow()
	entry := storagecatalog.Entry{
		StorageEntryID:     ids.NewStorageEntryID(),
		StorageClass:       storagecatalog.StorageClassPrivateBackup,
		SourceArea:         storagecatalog.SourceAreaDocuments,
		OriginNodeKey:      "macbook",
		LogicalPath:        stringsAfter(viewPath, "macbook/Backups/Documents/current/"),
		OriginalSourcePath: viewPath,
		CurrentViewPath:    viewPath,
		ChecksumAlgorithm:  "sha256",
		ChecksumHex:        hex.EncodeToString(sum[:]),
		SizeBytes:          &size,
		MimeType:           "text/plain",
		FileClass:          storagecatalog.ClassifyPath(viewPath, "text/plain"),
		ProcessingState:    storagecatalog.ProcessingStateBackupOnly,
		AvailabilityState:  storagecatalog.AvailabilityStateAvailable,
		RetentionState:     storagecatalog.RetentionStateRetained,
		Metadata:           json.RawMessage(`{"source":"archive-test"}`),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	ref := storagecatalog.PhysicalRef{
		StoragePhysicalRefID: ids.NewStoragePhysicalRefID(),
		StorageEntryID:       entry.StorageEntryID,
		RefKind:              storagecatalog.PhysicalRefKindLocalPath,
		URI:                  sourcePath,
		NodeKey:              "macbook",
		ContentAddress:       entry.ChecksumAlgorithm + ":" + entry.ChecksumHex,
		Status:               storagecatalog.PhysicalRefStatusAvailable,
		Metadata:             json.RawMessage(`{"source":"archive-test"}`),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	return entry, storagecatalog.EntryDetail{Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}}
}

func writeArchiveSourceFile(t *testing.T, contents string) string {
	t.Helper()
	pathValue := filepath.Join(t.TempDir(), ids.NewStorageEntryID()+".txt")
	if err := os.WriteFile(pathValue, []byte(contents), 0o640); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	return pathValue
}

func readArchiveObject(t *testing.T, pathValue string) string {
	t.Helper()
	bytes, err := os.ReadFile(pathValue)
	if err != nil {
		t.Fatalf("read archive object: %v", err)
	}
	return string(bytes)
}

func archiveResultPaths(result ArchiveResult) []string {
	paths := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		paths = append(paths, entry.ArchiveViewPath)
	}
	sort.Strings(paths)
	return paths
}

func stringsAfter(value, prefix string) string {
	if after, ok := strings.CutPrefix(value, prefix); ok {
		return after
	}
	return path.Base(value)
}

func fixedArchiveNow() time.Time {
	return time.Date(2026, 6, 5, 18, 0, 0, 0, time.UTC)
}

type cancelAfterChecksContext struct {
	context.Context
	cancelAt int
	checks   int
}

func (c *cancelAfterChecksContext) Err() error {
	c.checks++
	if c.checks >= c.cancelAt {
		return context.Canceled
	}
	return nil
}
