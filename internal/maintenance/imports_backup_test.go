package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCanonicalImportsEvidenceValidatesAfterCompleteInventory(t *testing.T) {
	root := t.TempDir()
	batchRoot := filepath.Join(root, "node-a", "2026-08-28", "batch-a")
	if err := os.MkdirAll(batchRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, 8, 28, 12, 34, 56, 123_000_000, time.UTC)
	payloadPath := filepath.Join(batchRoot, "z-payload.bin")
	if err := os.WriteFile(payloadPath, []byte("canonical Imports payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(payloadPath, modified, modified); err != nil {
		t.Fatal(err)
	}
	entries := []laneCustodyManifestEntry{{
		RelativePath: "z-payload.bin",
		Kind:         "regular_file",
		SizeBytes:    int64(len("canonical Imports payload")),
		Mode:         0o640,
		ModifiedAt:   modified,
		SHA256:       sha256HexForTest([]byte("canonical Imports payload")),
	}}
	writeLaneCustodyManifestForTest(t, batchRoot, "node-a", "2026-08-28", "batch-a", entries)

	evidencePath := filepath.Join(t.TempDir(), "imports-evidence.json")
	evidence, err := WriteImportsBackupEvidence(context.Background(), root, evidencePath, ImportsBackupPolicyCanonical, ImportsSnapshotSharedStore, 2)
	if err != nil {
		t.Fatalf("WriteImportsBackupEvidence returned error: %v", err)
	}
	if evidence.FileCount != 2 {
		t.Fatalf("file count = %d, want payload plus commit marker", evidence.FileCount)
	}
	if err := ValidateImportsBackup(context.Background(), root, evidencePath, ImportsBackupPolicyCanonical, ImportsSnapshotSharedStore); err != nil {
		t.Fatalf("ValidateImportsBackup returned error: %v", err)
	}
}

func TestCanonicalImportsEvidenceRejectsMissingAndTamperedPayload(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(string) error
	}{
		{name: "missing", mutate: os.Remove},
		{name: "tampered", mutate: func(pathValue string) error { return os.WriteFile(pathValue, []byte("tampered"), 0o640) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			batchRoot := filepath.Join(root, "node-a", "2026-08-28", "batch-a")
			if err := os.MkdirAll(batchRoot, 0o750); err != nil {
				t.Fatal(err)
			}
			payloadPath := filepath.Join(batchRoot, "payload.bin")
			if err := os.WriteFile(payloadPath, []byte("payload"), 0o640); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(payloadPath)
			if err != nil {
				t.Fatal(err)
			}
			entries := []laneCustodyManifestEntry{{RelativePath: "payload.bin", Kind: "regular_file", SizeBytes: 7, Mode: 0o640, ModifiedAt: info.ModTime().UTC(), SHA256: sha256HexForTest([]byte("payload"))}}
			writeLaneCustodyManifestForTest(t, batchRoot, "node-a", "2026-08-28", "batch-a", entries)
			evidencePath := filepath.Join(t.TempDir(), "imports-evidence.json")
			if _, err := WriteImportsBackupEvidence(context.Background(), root, evidencePath, ImportsBackupPolicyCanonical, ImportsSnapshotSharedStore, 2); err != nil {
				t.Fatal(err)
			}
			if err := testCase.mutate(payloadPath); err != nil {
				t.Fatal(err)
			}
			if err := ValidateImportsBackup(context.Background(), root, evidencePath, ImportsBackupPolicyCanonical, ImportsSnapshotSharedStore); err == nil {
				t.Fatal("corrupt canonical Imports backup passed verification")
			}
		})
	}
}

func TestWriteImportsEvidenceRejectsOversizeWithoutPublishing(t *testing.T) {
	directory := t.TempDir()
	pathValue := filepath.Join(directory, "imports-evidence.json")
	evidence := ImportsBackupEvidence{
		Schema:        ImportsBackupEvidenceSchema,
		Policy:        ImportsBackupPolicyLegacy,
		InventoryHash: "sha256:" + string(make([]byte, 64)),
		Entries: []ImportsBackupEvidenceEntry{{
			RelativePath: "a-legal-but-long-relative-path/payload.bin",
			Kind:         "regular_file",
			SHA256:       string(make([]byte, 64)),
		}},
	}
	if err := writeImportsEvidenceAtomicWithLimit(pathValue, evidence, 128); err == nil {
		t.Fatal("oversized streaming evidence writer returned success")
	}
	if _, err := os.Lstat(pathValue); !os.IsNotExist(err) {
		t.Fatalf("oversized evidence published destination: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("oversized evidence left temporary artifacts: %#v", entries)
	}
}

func TestReadImportsEvidenceRejectsOversizedTailAndSymlink(t *testing.T) {
	directory := t.TempDir()
	evidence := ImportsBackupEvidence{Schema: ImportsBackupEvidenceSchema, Policy: ImportsBackupPolicyLegacy, Entries: []ImportsBackupEvidenceEntry{}}
	inventoryHash, err := hashImportsEvidenceEntries(evidence.Entries)
	if err != nil {
		t.Fatal(err)
	}
	evidence.InventoryHash = inventoryHash
	raw, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(directory, "oversized.json")
	payload := append(append([]byte{}, raw...), '\n', ' ', ' ')
	if err := os.WriteFile(oversized, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := readImportsBackupEvidenceWithLimit(oversized, int64(len(payload)-1)); err == nil || !strings.Contains(err.Error(), "symmetric limit") {
		t.Fatalf("oversized valid JSON tail error = %v", err)
	}

	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, raw, 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "evidence.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readImportsBackupEvidence(link); err == nil || !strings.Contains(err.Error(), "no-follow regular") {
		t.Fatalf("symlink evidence error = %v", err)
	}
}

func TestImportsEvidencePreservesWhitespaceNamesThroughRehydration(t *testing.T) {
	backupDir := t.TempDir()
	importsRoot := filepath.Join(backupDir, "imports")
	directoryName := " leading directory "
	fileName := " payload file "
	linkName := " trailing link "
	directory := filepath.Join(importsRoot, directoryName)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(directory, fileName)
	content := []byte("literal whitespace custody")
	if err := os.WriteFile(payload, content, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fileName, filepath.Join(directory, linkName)); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, 8, 28, 14, 15, 16, 456_000_000, time.UTC)
	if err := os.Chtimes(payload, modified, modified); err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(backupDir, "imports-evidence.json")
	evidence, err := WriteImportsBackupEvidence(context.Background(), importsRoot, evidencePath, ImportsBackupPolicyLegacy, ImportsSnapshotSharedStore, 1)
	if err != nil {
		t.Fatalf("WriteImportsBackupEvidence returned error: %v", err)
	}
	wantPaths := map[string]bool{
		directoryName:                  false,
		directoryName + "/" + fileName: false,
		directoryName + "/" + linkName: false,
	}
	for _, entry := range evidence.Entries {
		if _, ok := wantPaths[entry.RelativePath]; ok {
			wantPaths[entry.RelativePath] = true
		}
	}
	for pathValue, found := range wantPaths {
		if !found {
			t.Fatalf("literal evidence path %q was not preserved: %#v", pathValue, evidence.Entries)
		}
	}
	writeRehydrationManifestForTest(t, backupDir, evidencePath, ImportsBackupPolicyLegacy)
	if err := os.Chmod(payload, 0o644); err != nil {
		t.Fatal(err)
	}
	truncated := modified.Truncate(time.Second)
	if err := os.Chtimes(payload, truncated, truncated); err != nil {
		t.Fatal(err)
	}
	if err := RehydrateLegacyTreeImportsMetadata(context.Background(), backupDir); err != nil {
		t.Fatalf("RehydrateLegacyTreeImportsMetadata returned error: %v", err)
	}
	if err := ValidateImportsBackup(context.Background(), importsRoot, evidencePath, ImportsBackupPolicyLegacy, ImportsSnapshotSharedStore); err != nil {
		t.Fatalf("whitespace Imports backup failed strict validation: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(directory, linkName)); err != nil || target != fileName {
		t.Fatalf("whitespace symlink target = %q err=%v", target, err)
	}
}

func TestRehydrateRejectsDuplicateEvidenceBeforeMutation(t *testing.T) {
	backupDir := t.TempDir()
	importsRoot := filepath.Join(backupDir, "imports")
	if err := os.MkdirAll(importsRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(importsRoot, "payload.bin")
	if err := os.WriteFile(payload, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	evidence, err := InspectImportsCustody(context.Background(), importsRoot, ImportsBackupPolicyLegacy)
	if err != nil {
		t.Fatal(err)
	}
	evidence.SnapshotMethod = ImportsSnapshotSharedStore
	evidence.SharedObjectLinkCount = evidence.FileCount
	evidence.Entries = append(evidence.Entries, evidence.Entries[0])
	evidence.FileCount++
	evidence.TotalBytes += evidence.Entries[0].SizeBytes
	evidence.InventoryHash, err = hashImportsEvidenceEntries(evidence.Entries)
	if err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(backupDir, "imports-evidence.json")
	if err := writeImportsEvidenceAtomic(evidencePath, evidence); err != nil {
		t.Fatal(err)
	}
	writeRehydrationManifestForTest(t, backupDir, evidencePath, ImportsBackupPolicyLegacy)
	called := false
	err = rehydrateLegacyTreeImportsMetadata(context.Background(), backupDir, func(ImportsBackupEvidenceEntry) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "duplicates relative path") {
		t.Fatalf("duplicate evidence error = %v", err)
	}
	if called {
		t.Fatal("metadata mutation hook ran before duplicate evidence was rejected")
	}
}

func TestRehydrateRejectsFileToSymlinkSubstitutionWithoutTouchingExternalTarget(t *testing.T) {
	backupDir := t.TempDir()
	importsRoot := filepath.Join(backupDir, "imports")
	if err := os.MkdirAll(importsRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(importsRoot, "payload.bin")
	if err := os.WriteFile(payload, []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(backupDir, "imports-evidence.json")
	if _, err := WriteImportsBackupEvidence(context.Background(), importsRoot, evidencePath, ImportsBackupPolicyLegacy, ImportsSnapshotSharedStore, 1); err != nil {
		t.Fatal(err)
	}
	writeRehydrationManifestForTest(t, backupDir, evidencePath, ImportsBackupPolicyLegacy)
	external := filepath.Join(t.TempDir(), "external-sentinel")
	if err := os.WriteFile(external, []byte("external sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalTime := time.Date(2025, 1, 2, 3, 4, 5, 678_000_000, time.UTC)
	if err := os.Chtimes(external, externalTime, externalTime); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(external)
	if err != nil {
		t.Fatal(err)
	}
	swapped := false
	err = rehydrateLegacyTreeImportsMetadata(context.Background(), backupDir, func(entry ImportsBackupEvidenceEntry) error {
		if entry.RelativePath != "payload.bin" || swapped {
			return nil
		}
		swapped = true
		if err := os.Remove(payload); err != nil {
			return err
		}
		return os.Symlink(external, payload)
	})
	if err == nil {
		t.Fatal("file-to-symlink substitution passed metadata rehydration")
	}
	after, statErr := os.Stat(external)
	if statErr != nil {
		t.Fatal(statErr)
	}
	content, readErr := os.ReadFile(external)
	if readErr != nil || string(content) != "external sentinel" || after.Mode().Perm() != before.Mode().Perm() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("external sentinel changed: before=%#v after=%#v content=%q err=%v", before, after, content, readErr)
	}
}

func writeRehydrationManifestForTest(t *testing.T, backupDir, evidencePath, policy string) {
	t.Helper()
	hash, size, err := hashImportsNoFollowRegular(context.Background(), evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := BackupManifest{
		Schema:     BackupManifestSchemaV09,
		BackupKind: "test",
		Paths: BackupManifestPaths{
			Imports:         "imports",
			ImportsEvidence: "imports-evidence.json",
		},
		Policies: BackupManifestPolicies{
			Imports:         policy,
			ImportsSnapshot: ImportsSnapshotSharedStore,
		},
		Artifacts: []BackupManifestArtifact{{Kind: ArtifactKindImportsEvidence, Path: "imports-evidence.json", SizeBytes: &size, SHA256: hash}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "manifest.json"), raw, 0o640); err != nil {
		t.Fatal(err)
	}
}

func writeLaneCustodyManifestForTest(t *testing.T, batchRoot, source, acceptedDate, batch string, entries []laneCustodyManifestEntry) {
	t.Helper()
	inventoryHash, err := hashLaneCustodyManifestEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	manifest := laneCustodyManifestEvidence{
		SchemaVersion: laneCustodyManifestSchema,
		SourceNodeKey: source,
		BatchID:       batch,
		AcceptedDate:  acceptedDate,
		InventoryHash: inventoryHash,
		Entries:       entries,
	}
	for _, entry := range entries {
		switch entry.Kind {
		case "regular_file":
			manifest.FileCount++
			manifest.TotalBytes += entry.SizeBytes
		case "directory":
			manifest.DirectoryCount++
		case "symlink":
			manifest.SymlinkCount++
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batchRoot, laneCustodyManifestName), raw, 0o640); err != nil {
		t.Fatal(err)
	}
}

func sha256HexForTest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
