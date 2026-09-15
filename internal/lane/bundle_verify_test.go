package lane

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyBundleRejectsChecksumTruncationAndManifestMismatch(t *testing.T) {
	t.Run("checksum", func(t *testing.T) {
		artifact := createBundleTestArtifact(t, "lane_checksum")
		file, err := os.OpenFile(artifact.ArchivePath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("tamper")); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyBundle(artifact.ManifestPath, artifact.ArchivePath); err == nil || !strings.Contains(err.Error(), "checksum") {
			t.Fatalf("checksum tamper passed: %v", err)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		artifact := createBundleTestArtifact(t, "lane_truncated")
		info, err := os.Stat(artifact.ArchivePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(artifact.ArchivePath, info.Size()/2); err != nil {
			t.Fatal(err)
		}
		refreshBundlePartEvidence(t, artifact)
		if _, err := VerifyBundle(artifact.ManifestPath, artifact.ArchivePath); err == nil {
			t.Fatal("truncated archive passed verification")
		}
	})

	t.Run("manifest mismatch", func(t *testing.T) {
		artifact := createBundleTestArtifact(t, "lane_manifest_mismatch")
		manifest, err := ReadBundleManifest(artifact.ManifestPath)
		if err != nil {
			t.Fatal(err)
		}
		manifest.SourceFingerprintAfter = "sha256:mismatch"
		payload, _, err := marshalBundleManifest(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(artifact.ManifestPath, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyBundle(artifact.ManifestPath, artifact.ArchivePath); err == nil {
			t.Fatal("mismatched manifest passed verification")
		}
	})
}

func TestVerifyBundleRejectsUnsafeAndUnsupportedHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers func(TransferPlan) []tar.Header
	}{
		{name: "absolute", headers: func(plan TransferPlan) []tar.Header {
			header := testTarHeader(plan.Entries[0])
			header.Name = "/absolute"
			return []tar.Header{header}
		}},
		{name: "traversal", headers: func(plan TransferPlan) []tar.Header {
			header := testTarHeader(plan.Entries[0])
			header.Name = "../escape"
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
		{name: "duplicate", headers: func(plan TransferPlan) []tar.Header {
			header := testTarHeader(plan.Entries[0])
			return []tar.Header{header, header}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact := createBundleTestArtifact(t, "lane_header_"+test.name)
			manifest, err := ReadBundleManifest(artifact.ManifestPath)
			if err != nil {
				t.Fatal(err)
			}
			writeTestTar(t, artifact.ArchivePath, test.headers(manifest.Plan))
			refreshBundlePartEvidence(t, artifact)
			if _, err := VerifyBundle(artifact.ManifestPath, artifact.ArchivePath); err == nil {
				t.Fatalf("unsafe %s archive passed", test.name)
			}
		})
	}
}

func createBundleTestArtifact(t *testing.T, batchID string) BundleArtifact {
	t.Helper()
	root, lanePath, plan, createdAt := bundleFixture(t)
	artifact, err := CreateBundle(CreateBundleInput{
		LanePath: lanePath, PolicyRoot: root, ArtifactRoot: filepath.Join(root, DefaultStateRelPath, "bundles"),
		BatchID: batchID, SourceNodeKey: "macbook", Plan: plan, CreatedAt: createdAt,
		AvailableBytes: maxBundleTestSpace,
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func testTarHeader(entry TransferEntry) tar.Header {
	name := entry.RelativePath
	typeFlag := byte(tar.TypeReg)
	if entry.Kind == "directory" {
		name += "/"
		typeFlag = tar.TypeDir
	}
	return tar.Header{
		Name: name, Typeflag: typeFlag, Mode: int64(entry.Mode), Size: entry.Bytes,
		ModTime: entry.ModifiedAt, Format: tar.FormatPAX,
		PAXRecords: map[string]string{bundlePAXSchemaKey: bundlePAXSchemaValue},
	}
}

func firstRegularTestEntry(plan TransferPlan) TransferEntry {
	for _, entry := range plan.Entries {
		if entry.Kind == "regular_file" {
			return entry
		}
	}
	return TransferEntry{}
}

func writeTestTar(t *testing.T, pathValue string, headers []tar.Header) {
	t.Helper()
	file, err := os.OpenFile(pathValue, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	for _, value := range headers {
		header := value
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := writer.Write(make([]byte, header.Size)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func refreshBundlePartEvidence(t *testing.T, artifact BundleArtifact) {
	t.Helper()
	manifest, err := ReadBundleManifest(artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	checksum, size, err := fileSHA256(artifact.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ArchiveParts[0].SHA256 = "sha256:" + checksum
	manifest.ArchiveParts[0].SizeBytes = size
	payload, _, err := marshalBundleManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact.ManifestPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}
