package filetransfer

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTransferIDGenerationAndValidation(t *testing.T) {
	id := NewTransferID()
	if err := ValidateTransferID(id); err != nil {
		t.Fatalf("generated transfer id did not validate: %v", err)
	}
	if err := ValidateTransferID("watched_root_backup_batch_not_a_transfer"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ValidateTransferID error = %v, want ErrInvalid", err)
	}
	chunkID := NewChunkID()
	if err := ValidateChunkID(chunkID); err != nil {
		t.Fatalf("generated chunk id did not validate: %v", err)
	}
}

func TestNormalizeManifestDefaultsAndIdempotency(t *testing.T) {
	mtime := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 7, 12, 1, 0, 0, time.UTC)
	checksum := strings.Repeat("a", 64)

	manifest, err := NormalizeManifest(Manifest{
		SourceNodeKey:          "macbook",
		SourceRootKey:          "loom_box__documents",
		SourceRelativePath:     "./reports/report.pdf",
		DestinationLogicalPath: "/macbook/Backups/Documents/current/reports/report.pdf",
		TransferKind:           KindWatchedRootBackup,
		CustodyMode:            CustodyModeBackupCopy,
		FileSizeBytes:          10,
		ModTime:                &mtime,
		ChecksumAlgorithm:      ChecksumSHA256,
		ChecksumHex:            "sha256:" + checksum,
		ChunkSizeBytes:         4,
	}, now)
	if err != nil {
		t.Fatalf("NormalizeManifest returned error: %v", err)
	}
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.TransferID == "" {
		t.Fatalf("unexpected manifest identity: %#v", manifest)
	}
	if manifest.SourceRelativePath != "reports/report.pdf" {
		t.Fatalf("source relative path = %q", manifest.SourceRelativePath)
	}
	if manifest.DestinationLogicalPath != "macbook/Backups/Documents/current/reports/report.pdf" {
		t.Fatalf("destination path = %q", manifest.DestinationLogicalPath)
	}
	if manifest.ChunkCount != 3 {
		t.Fatalf("chunk count = %d, want 3", manifest.ChunkCount)
	}
	if manifest.Status != StatusPending || manifest.ChecksumHex != checksum {
		t.Fatalf("unexpected status/checksum: %#v", manifest)
	}
	if manifest.IdempotencyKey == "" || !strings.HasPrefix(manifest.IdempotencyKey, "sha256:") {
		t.Fatalf("idempotency key = %q", manifest.IdempotencyKey)
	}

	again := manifest
	again.TransferID = ""
	again.IdempotencyKey = ""
	again.CreatedAt = time.Time{}
	again.UpdatedAt = time.Time{}
	again, err = NormalizeManifest(again, now)
	if err != nil {
		t.Fatalf("NormalizeManifest repeated returned error: %v", err)
	}
	if again.IdempotencyKey != manifest.IdempotencyKey {
		t.Fatalf("idempotency key changed: %s != %s", again.IdempotencyKey, manifest.IdempotencyKey)
	}
}

func TestNormalizeManifestRejectsUnsupportedWorkflow(t *testing.T) {
	_, err := NormalizeManifest(Manifest{
		SourceRootKey:          "documents",
		SourceRelativePath:     "file.bin",
		DestinationLogicalPath: "macbook/Backups/Documents/file.bin",
		TransferKind:           "unknown",
		CustodyMode:            CustodyModeBackupCopy,
		FileSizeBytes:          1,
	}, time.Now())
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("NormalizeManifest error = %v, want ErrInvalid", err)
	}
}

func TestPlanAndNormalizeChunks(t *testing.T) {
	manifest, err := NormalizeManifest(testManifest(10, 4), time.Now())
	if err != nil {
		t.Fatalf("NormalizeManifest returned error: %v", err)
	}
	chunks, err := PlanChunks(manifest.TransferID, manifest.FileSizeBytes, manifest.ChunkSizeBytes)
	if err != nil {
		t.Fatalf("PlanChunks returned error: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunk length = %d, want 3", len(chunks))
	}
	if chunks[0].OffsetBytes != 0 || chunks[0].SizeBytes != 4 ||
		chunks[1].OffsetBytes != 4 || chunks[1].SizeBytes != 4 ||
		chunks[2].OffsetBytes != 8 || chunks[2].SizeBytes != 2 {
		t.Fatalf("unexpected chunk plan: %#v", chunks)
	}
	chunks[0].ChecksumAlgorithm = ChecksumSHA256
	chunks[0].ChecksumHex = SHA256Hex([]byte("abcd"))
	normalized, err := NormalizeChunks(manifest, chunks, time.Now())
	if err != nil {
		t.Fatalf("NormalizeChunks returned error: %v", err)
	}
	if normalized[0].Status != ChunkStatusPending || normalized[0].ChunkID == "" {
		t.Fatalf("unexpected normalized chunks: %#v", normalized[0])
	}
}

func TestNormalizeChunksRejectsMissingChunk(t *testing.T) {
	manifest, err := NormalizeManifest(testManifest(10, 4), time.Now())
	if err != nil {
		t.Fatalf("NormalizeManifest returned error: %v", err)
	}
	chunks, err := PlanChunks(manifest.TransferID, manifest.FileSizeBytes, manifest.ChunkSizeBytes)
	if err != nil {
		t.Fatalf("PlanChunks returned error: %v", err)
	}
	_, err = NormalizeChunks(manifest, chunks[:2], time.Now())
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("NormalizeChunks error = %v, want ErrInvalid", err)
	}
}

func TestStatusTransitions(t *testing.T) {
	now := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	manifest, err := NormalizeManifest(testManifest(5, 2), now)
	if err != nil {
		t.Fatalf("NormalizeManifest returned error: %v", err)
	}
	manifest, err = TransitionManifestStatus(manifest, StatusUploading, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("pending -> uploading returned error: %v", err)
	}
	if manifest.StartedAt == nil || manifest.Status != StatusUploading {
		t.Fatalf("uploading transition did not set fields: %#v", manifest)
	}
	manifest, err = TransitionManifestStatus(manifest, StatusCompleting, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("uploading -> completing returned error: %v", err)
	}
	manifest, err = TransitionManifestStatus(manifest, StatusAccepted, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("completing -> accepted returned error: %v", err)
	}
	if manifest.CompletedAt == nil || manifest.Status != StatusAccepted {
		t.Fatalf("accepted transition did not set fields: %#v", manifest)
	}
	if _, err := TransitionManifestStatus(manifest, StatusUploading, now.Add(4*time.Minute)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted -> uploading error = %v, want ErrInvalid", err)
	}
}

func TestChecksumMismatch(t *testing.T) {
	if err := VerifyChecksum(ChecksumSHA256, SHA256Hex([]byte("hello")), []byte("hello")); err != nil {
		t.Fatalf("VerifyChecksum returned error for matching payload: %v", err)
	}
	if err := VerifyChecksum(ChecksumSHA256, SHA256Hex([]byte("hello")), []byte("world")); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("VerifyChecksum error = %v, want ErrChecksumMismatch", err)
	}
}

func testManifest(size, chunkSize int64) Manifest {
	return Manifest{
		SourceNodeKey:          "macbook",
		SourceRootKey:          "loom_box__documents",
		SourceRelativePath:     "large/video.mov",
		DestinationLogicalPath: "macbook/Backups/Documents/current/large/video.mov",
		TransferKind:           KindWatchedRootBackup,
		CustodyMode:            CustodyModeBackupCopy,
		FileSizeBytes:          size,
		ChecksumAlgorithm:      ChecksumSHA256,
		ChecksumHex:            strings.Repeat("b", 64),
		ChunkSizeBytes:         chunkSize,
	}
}
