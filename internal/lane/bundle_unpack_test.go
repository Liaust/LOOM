package lane

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"loom.local/loom/internal/filesystemmeta"
)

func TestUnpackBundlePromotesExactTreeAndIsIdempotent(t *testing.T) {
	input, plan := stagedBundleFixture(t, "lane_unpack_success")
	result, err := UnpackBundle(input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Idempotent || result.FileCount != plan.FileCount || result.DirCount != plan.DirCount || result.TotalBytes != plan.TotalBytes {
		t.Fatalf("unpack result = %#v", result)
	}
	if err := verifyExtractedBundleTree(input.AcceptedPath, plan); err != nil {
		t.Fatalf("accepted tree mismatch: %v", err)
	}
	assertSharedBundleTree(t, input.AcceptedPath)
	scriptEntry := bundlePlanEntry(t, plan, "script.sh")
	scriptInfo, err := os.Stat(filepath.Join(input.AcceptedPath, "script.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if scriptInfo.Mode().Perm() != os.FileMode(scriptEntry.Mode) || !scriptInfo.ModTime().UTC().Equal(scriptEntry.ModifiedAt.UTC()) {
		t.Fatalf("script metadata mismatch: info=%#v plan=%#v", scriptInfo, scriptEntry)
	}
	if info, err := os.Stat(filepath.Join(input.AcceptedPath, "empty-dir")); err != nil || !info.IsDir() {
		t.Fatalf("empty directory missing: info=%#v err=%v", info, err)
	}
	if err := os.Chmod(input.AcceptedPath, 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := UnpackBundle(input)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Idempotent {
		t.Fatalf("matching retry was not idempotent: %#v", second)
	}
	assertSharedBundleTree(t, input.AcceptedPath)
}

func assertSharedBundleTree(t *testing.T, pathValue string) {
	t.Helper()
	info, err := os.Stat(pathValue)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o770 || info.Mode()&os.ModeSetgid == 0 {
		t.Fatalf("bundle tree mode = %v, want setgid 0770", info.Mode())
	}
}

func TestAcceptBundleCatalogsExtractedFilesNotTransportArtifacts(t *testing.T) {
	input, plan := stagedBundleFixture(t, "lane_bundle_catalog")
	catalog := &fakeLaneCatalog{}
	result, err := AcceptBundle(context.Background(), catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Catalog.FilesCataloged != plan.FileCount || result.Catalog.TotalBytes != plan.TotalBytes {
		t.Fatalf("catalog result = %#v plan=%#v", result.Catalog, plan)
	}
	for _, catalogInput := range catalog.inputs {
		canonicalBatch := result.Catalog.AcceptedPath
		if strings.HasSuffix(catalogInput.RelativeLanePath, ".tar") || strings.HasSuffix(catalogInput.RelativeLanePath, ".json") || !strings.HasPrefix(catalogInput.FinalCustodyPath, canonicalBatch+string(filepath.Separator)) {
			t.Fatalf("transport artifact entered catalog: %#v", catalogInput)
		}
		entry := bundlePlanEntry(t, plan, catalogInput.RelativeLanePath)
		if catalogInput.FilesystemObservation == nil || catalogInput.FilesystemObservation.SourceModifiedAt == nil || !catalogInput.FilesystemObservation.SourceModifiedAt.Equal(entry.ModifiedAt.UTC()) {
			t.Fatalf("catalog source time = %#v, want bundle manifest mtime %s", catalogInput.FilesystemObservation, entry.ModifiedAt.UTC())
		}
		if catalogInput.FilesystemObservation.SourceModifiedBasis != filesystemmeta.SourceTimeBasisFilesystemMtime {
			t.Fatalf("catalog source time basis = %q", catalogInput.FilesystemObservation.SourceModifiedBasis)
		}
		if catalogInput.FilesystemObservation.SourceCreatedAt != nil || catalogInput.FilesystemObservation.SourceCreatedBasis != "" {
			t.Fatalf("catalog source creation = %v/%q, want unset destination birth time", catalogInput.FilesystemObservation.SourceCreatedAt, catalogInput.FilesystemObservation.SourceCreatedBasis)
		}
	}
}

func TestAcceptBundleRecoversCommittedCrossDeviceCustodyWithoutReunpack(t *testing.T) {
	input, plan := stagedBundleFixture(t, "lane_bundle_committed_retry")
	input.AllowCrossDevicePromotion = true
	input.Rename = func(oldPath, newPath string) error {
		if filepath.Clean(oldPath) == filepath.Clean(input.AcceptedPath) {
			return syscall.EXDEV
		}
		return os.Rename(oldPath, newPath)
	}
	input.AvailableBytes = func(string) (int64, error) { return 1 << 40, nil }
	catalog := &fakeLaneCatalog{}
	first, err := AcceptBundle(context.Background(), catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Catalog.PromotionIdempotent {
		t.Fatalf("initial acceptance unexpectedly idempotent: %#v", first)
	}
	if _, err := os.Stat(filepath.Join(input.AcceptedPath, laneCustodyManifestName)); err != nil {
		t.Fatalf("cross-device staging marker missing: %v", err)
	}
	second, err := AcceptBundle(context.Background(), catalog, input)
	if err != nil {
		t.Fatalf("committed retry: %v", err)
	}
	if !second.Unpack.Idempotent || !second.Catalog.PromotionIdempotent || second.Catalog.FilesCataloged != plan.FileCount || second.Catalog.TotalBytes != plan.TotalBytes {
		t.Fatalf("committed retry = %#v", second)
	}
}

func TestUnpackBundleRejectsConflictingAcceptedTree(t *testing.T) {
	input, _ := stagedBundleFixture(t, "lane_unpack_conflict")
	if _, err := UnpackBundle(input); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input.AcceptedPath, "unexpected.txt"), []byte("conflict"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := UnpackBundle(input); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting accepted tree was not rejected: %v", err)
	}
}

func TestUnpackBundleRejectsRemoteBoundaryAndBatchIdentityConfusion(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*BundleAcceptInput)
	}{
		{name: "accepted outside", mutate: func(input *BundleAcceptInput) {
			input.AcceptedPath = filepath.Join(filepath.Dir(input.RemoteRoot), "outside")
		}},
		{name: "wrong accepted batch", mutate: func(input *BundleAcceptInput) {
			input.AcceptedPath = strings.Replace(input.AcceptedPath, input.BatchID, "other", 1)
		}},
		{name: "manifest sibling", mutate: func(input *BundleAcceptInput) {
			input.ManifestPath = filepath.Join(filepath.Dir(filepath.Dir(input.ManifestPath)), bundleManifestFileName)
		}},
		{name: "archive name", mutate: func(input *BundleAcceptInput) {
			input.ArchivePath = filepath.Join(filepath.Dir(input.ArchivePath), "other.tar")
		}},
		{name: "source node", mutate: func(input *BundleAcceptInput) { input.SourceNodeKey = "other" }},
		{name: "batch", mutate: func(input *BundleAcceptInput) { input.BatchID = "other" }},
		{name: "date", mutate: func(input *BundleAcceptInput) { input.AcceptedDate = "17-08-2026" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, _ := stagedBundleFixture(t, "lane_boundary_"+strings.ReplaceAll(test.name, " ", "_"))
			test.mutate(&input)
			if _, err := UnpackBundle(input); err == nil {
				t.Fatalf("confused bundle input passed: %#v", input)
			}
		})
	}
}

func TestUnpackBundleRejectsMaliciousArchivesWithoutAcceptedMutation(t *testing.T) {
	tests := []struct {
		name    string
		headers func(TransferPlan) []tar.Header
	}{
		{name: "absolute", headers: func(plan TransferPlan) []tar.Header {
			header := testTarHeader(firstRegularTestEntry(plan))
			header.Name = "/escape"
			return []tar.Header{header}
		}},
		{name: "traversal", headers: func(plan TransferPlan) []tar.Header {
			header := testTarHeader(firstRegularTestEntry(plan))
			header.Name = "folder/../../escape"
			return []tar.Header{header}
		}},
		{name: "symlink", headers: func(plan TransferPlan) []tar.Header {
			header := testTarHeader(firstRegularTestEntry(plan))
			header.Typeflag = tar.TypeSymlink
			header.Linkname = "outside"
			header.Size = 0
			return []tar.Header{header}
		}},
		{name: "hardlink", headers: func(plan TransferPlan) []tar.Header {
			header := testTarHeader(firstRegularTestEntry(plan))
			header.Typeflag = tar.TypeLink
			header.Linkname = "outside"
			header.Size = 0
			return []tar.Header{header}
		}},
		{name: "fifo", headers: func(plan TransferPlan) []tar.Header {
			header := testTarHeader(firstRegularTestEntry(plan))
			header.Typeflag = tar.TypeFifo
			header.Size = 0
			return []tar.Header{header}
		}},
		{name: "device", headers: func(plan TransferPlan) []tar.Header {
			header := testTarHeader(firstRegularTestEntry(plan))
			header.Typeflag = tar.TypeChar
			header.Size = 0
			return []tar.Header{header}
		}},
		{name: "duplicate", headers: func(plan TransferPlan) []tar.Header {
			header := testTarHeader(plan.Entries[0])
			return []tar.Header{header, header}
		}},
		{name: "oversized", headers: func(plan TransferPlan) []tar.Header {
			entry := firstRegularTestEntry(plan)
			header := testTarHeader(entry)
			header.Size = entry.Bytes + 1
			return []tar.Header{header}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, plan := stagedBundleFixture(t, "lane_malicious_"+test.name)
			writeTestTar(t, input.ArchivePath, test.headers(plan))
			refreshBundlePartEvidence(t, BundleArtifact{ArchivePath: input.ArchivePath, ManifestPath: input.ManifestPath})
			outside := filepath.Join(filepath.Dir(input.RemoteRoot), "escape")
			if _, err := UnpackBundle(input); err == nil {
				t.Fatalf("malicious %s archive passed", test.name)
			}
			if _, err := os.Lstat(input.AcceptedPath); !os.IsNotExist(err) {
				t.Fatalf("malicious archive mutated accepted path: %v", err)
			}
			if _, err := os.Lstat(outside); !os.IsNotExist(err) {
				t.Fatalf("malicious archive escaped staging: %v", err)
			}
			assertNoUnpackTemporaryDirs(t, filepath.Dir(input.ManifestPath))
		})
	}
}

func TestUnpackBundleChecksumAndPartialExtractionFailuresCleanTemporaryTree(t *testing.T) {
	t.Run("checksum", func(t *testing.T) {
		input, _ := stagedBundleFixture(t, "lane_unpack_checksum")
		file, err := os.OpenFile(input.ArchivePath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.Write([]byte("tamper"))
		_ = file.Close()
		if _, err := UnpackBundle(input); err == nil {
			t.Fatal("checksum mismatch passed")
		}
		if _, err := os.Lstat(input.AcceptedPath); !os.IsNotExist(err) {
			t.Fatalf("checksum failure created accepted path: %v", err)
		}
	})

	t.Run("partial extraction", func(t *testing.T) {
		input, _ := stagedBundleFixture(t, "lane_unpack_partial")
		input.BeforeExtractEntry = func(index int, _ TransferEntry) error {
			if index == 1 {
				return errors.New("interrupted extraction")
			}
			return nil
		}
		if _, err := UnpackBundle(input); err == nil || !strings.Contains(err.Error(), "interrupted extraction") {
			t.Fatalf("expected interrupted extraction, got %v", err)
		}
		if _, err := os.Lstat(input.AcceptedPath); !os.IsNotExist(err) {
			t.Fatalf("partial extraction created accepted path: %v", err)
		}
		assertNoUnpackTemporaryDirs(t, filepath.Dir(input.ManifestPath))
	})
}

func TestUnpackBundleRequiresStagingSpaceBeforeExtraction(t *testing.T) {
	input, _ := stagedBundleFixture(t, "lane_unpack_no_space")
	input.AvailableBytes = func(string) (int64, error) { return 0, nil }
	if _, err := UnpackBundle(input); err == nil || !strings.Contains(err.Error(), "insufficient Lane staging free space") {
		t.Fatalf("insufficient staging space error = %v", err)
	}
	if _, err := os.Lstat(input.AcceptedPath); !os.IsNotExist(err) {
		t.Fatalf("space preflight created extracted tree: %v", err)
	}
	assertNoUnpackTemporaryDirs(t, filepath.Dir(input.ManifestPath))
}

func stagedBundleFixture(t *testing.T, batchID string) (BundleAcceptInput, TransferPlan) {
	t.Helper()
	localRoot, lanePath, plan, createdAt := bundleFixture(t)
	artifact, err := CreateBundle(CreateBundleInput{
		LanePath: lanePath, PolicyRoot: localRoot, ArtifactRoot: filepath.Join(localRoot, DefaultStateRelPath, "bundles"),
		BatchID: batchID, SourceNodeKey: "macbook", SourceBoxID: "box-test", Plan: plan, CreatedAt: createdAt,
		AvailableBytes: maxBundleTestSpace,
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteRoot := filepath.Join(t.TempDir(), "lane")
	withLaneRemoteBoundary(t, remoteRoot)
	staging := filepath.Join(remoteRoot, "staging", "macbook", batchID)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(staging, bundleArchiveFileName)
	manifestPath := filepath.Join(staging, bundleManifestFileName)
	if err := copyRegularPath(artifact.ArchivePath, archivePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyRegularPath(artifact.ManifestPath, manifestPath, 0o600); err != nil {
		t.Fatal(err)
	}
	return BundleAcceptInput{
		RemoteRoot: remoteRoot, TrustedRemoteRoot: true, SourceNodeKey: "macbook", SourceBoxID: "box-test", BatchID: batchID,
		AcceptedDate: "2026-08-16", CustodyNodeKey: "main", ManifestPath: manifestPath, ArchivePath: archivePath,
		AcceptedPath: filepath.Join(staging, "tree"), ImportsRoot: filepath.Join(t.TempDir(), "imports"), AcceptedAt: createdAt,
	}, plan
}

func bundlePlanEntry(t *testing.T, plan TransferPlan, relative string) TransferEntry {
	t.Helper()
	for _, entry := range plan.Entries {
		if entry.RelativePath == relative {
			return entry
		}
	}
	t.Fatalf("plan entry %q not found", relative)
	return TransferEntry{}
}

func assertNoUnpackTemporaryDirs(t *testing.T, staging string) {
	t.Helper()
	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".unpack-") {
			t.Fatalf("temporary extraction survived: %s", entry.Name())
		}
	}
}
