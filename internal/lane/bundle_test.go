package lane

import (
	"archive/tar"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateBundleBuildsDeterministicPAXArtifactWithoutTouchingSource(t *testing.T) {
	root, lanePath, plan, createdAt := bundleFixture(t)
	artifactRoot := filepath.Join(root, DefaultStateRelPath, "bundles")
	before := snapshotBundleSource(t, lanePath)

	first, err := CreateBundle(CreateBundleInput{
		LanePath:       lanePath,
		PolicyRoot:     root,
		ArtifactRoot:   artifactRoot,
		BatchID:        "lane_bundle_one",
		SourceNodeKey:  "macbook",
		SourceBoxID:    "box-test",
		Plan:           plan,
		CreatedAt:      createdAt,
		AvailableBytes: func(string) (int64, error) { return math.MaxInt64, nil },
	})
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	if first.ArchiveBytes <= 0 || !validSHA256URI(first.ArchiveSHA256) || !validSHA256URI(first.ManifestSHA256) {
		t.Fatalf("artifact evidence missing: %#v", first)
	}
	manifest, err := VerifyBundle(first.ManifestPath, first.ArchivePath)
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	if manifest.Plan.InventoryHash != plan.InventoryHash || manifest.SourceFingerprintBefore != plan.InventoryHash || manifest.SourceFingerprintAfter != plan.InventoryHash {
		t.Fatalf("manifest inventory evidence diverged: %#v", manifest)
	}
	if got := snapshotBundleSource(t, lanePath); got != before {
		t.Fatalf("bundle creation changed visible source:\nbefore=%s\nafter=%s", before, got)
	}

	second, err := CreateBundle(CreateBundleInput{
		LanePath:       lanePath,
		PolicyRoot:     root,
		ArtifactRoot:   artifactRoot,
		BatchID:        "lane_bundle_two",
		SourceNodeKey:  "macbook",
		SourceBoxID:    "box-test",
		Plan:           plan,
		CreatedAt:      createdAt,
		AvailableBytes: func(string) (int64, error) { return math.MaxInt64, nil },
	})
	if err != nil {
		t.Fatalf("second CreateBundle: %v", err)
	}
	if first.ArchiveSHA256 != second.ArchiveSHA256 || first.ArchiveBytes != second.ArchiveBytes {
		t.Fatalf("archive was not deterministic: first=%#v second=%#v", first, second)
	}
}

func TestCreateBundleForcesPAXForShortWholeSecondEntry(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	mustLaneFile(t, filepath.Join(lanePath, "short.txt"), "payload")
	wholeSecond := time.Date(2026, 8, 28, 11, 4, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(lanePath, "short.txt"), wholeSecond, wholeSecond); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildTransferPlan(lanePath, root, "faithful")
	if err != nil {
		t.Fatal(err)
	}
	plan.Transport, err = SelectBundleTransport(plan, TransportModeBundleSeed)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := CreateBundle(CreateBundleInput{
		LanePath: lanePath, PolicyRoot: root, ArtifactRoot: filepath.Join(root, DefaultStateRelPath, "bundles"),
		BatchID: "lane_whole_second", SourceNodeKey: "macbook", Plan: plan, CreatedAt: wholeSecond,
		AvailableBytes: func(string) (int64, error) { return math.MaxInt64, nil },
	})
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	archive, err := os.Open(artifact.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	header, err := tar.NewReader(archive).Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Format != tar.FormatPAX || header.PAXRecords[bundlePAXSchemaKey] != bundlePAXSchemaValue {
		t.Fatalf("header format=%v PAXRecords=%v", header.Format, header.PAXRecords)
	}
}

func TestCreateBundleRejectsSourceMutationBeforeAndDuringCreation(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		root, lanePath, plan, createdAt := bundleFixture(t)
		mustLaneFile(t, filepath.Join(lanePath, "zero.txt"), "changed")
		_, err := CreateBundle(CreateBundleInput{
			LanePath: lanePath, PolicyRoot: root, ArtifactRoot: filepath.Join(root, DefaultStateRelPath, "bundles"),
			BatchID: "lane_mutated_before", SourceNodeKey: "macbook", Plan: plan, CreatedAt: createdAt,
			AvailableBytes: func(string) (int64, error) { return math.MaxInt64, nil },
		})
		if err == nil || !strings.Contains(err.Error(), "changed before") {
			t.Fatalf("expected pre-create mutation failure, got %v", err)
		}
	})

	t.Run("during", func(t *testing.T) {
		root, lanePath, plan, createdAt := bundleFixture(t)
		mutated := false
		artifactRoot := filepath.Join(root, DefaultStateRelPath, "bundles")
		_, err := CreateBundle(CreateBundleInput{
			LanePath: lanePath, PolicyRoot: root, ArtifactRoot: artifactRoot,
			BatchID: "lane_mutated_during", SourceNodeKey: "macbook", Plan: plan, CreatedAt: createdAt,
			AvailableBytes: func(string) (int64, error) { return math.MaxInt64, nil },
			BeforeEntry: func(_ int, entry TransferEntry) error {
				if !mutated && entry.RelativePath > "script.sh" {
					mutated = true
					pathValue := filepath.Join(lanePath, "script.sh")
					info, statErr := os.Stat(pathValue)
					if statErr != nil {
						return statErr
					}
					return os.Chtimes(pathValue, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second))
				}
				return nil
			},
		})
		if err == nil || (!strings.Contains(err.Error(), "metadata changed") && !strings.Contains(err.Error(), "changed during")) {
			t.Fatalf("expected during-create mutation failure, got %v", err)
		}
		entries, readErr := os.ReadDir(artifactRoot)
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("incomplete artifact survived mutation: %#v", entries)
		}
	})
}

func TestCreateBundleRejectsSymlinkSwapAndCleansInterruptedTemporaryArtifact(t *testing.T) {
	t.Run("symlink swap", func(t *testing.T) {
		root, lanePath, plan, createdAt := bundleFixture(t)
		external := filepath.Join(root, "external.txt")
		mustLaneFile(t, external, "outside")
		swapped := false
		_, err := CreateBundle(CreateBundleInput{
			LanePath: lanePath, PolicyRoot: root, ArtifactRoot: filepath.Join(root, DefaultStateRelPath, "bundles"),
			BatchID: "lane_symlink_swap", SourceNodeKey: "macbook", Plan: plan, CreatedAt: createdAt,
			AvailableBytes: func(string) (int64, error) { return math.MaxInt64, nil },
			BeforeEntry: func(_ int, entry TransferEntry) error {
				if !swapped && entry.RelativePath == "zero.txt" {
					swapped = true
					pathValue := filepath.Join(lanePath, "zero.txt")
					if err := os.Remove(pathValue); err != nil {
						return err
					}
					return os.Symlink(external, pathValue)
				}
				return nil
			},
		})
		if err == nil {
			t.Fatal("symlink replacement was followed")
		}
		payload, readErr := os.ReadFile(external)
		if readErr != nil || string(payload) != "outside" {
			t.Fatalf("external target changed: payload=%q err=%v", payload, readErr)
		}
	})

	t.Run("interrupted", func(t *testing.T) {
		root, lanePath, plan, createdAt := bundleFixture(t)
		artifactRoot := filepath.Join(root, DefaultStateRelPath, "bundles")
		_, err := CreateBundle(CreateBundleInput{
			LanePath: lanePath, PolicyRoot: root, ArtifactRoot: artifactRoot,
			BatchID: "lane_interrupted", SourceNodeKey: "macbook", Plan: plan, CreatedAt: createdAt,
			AvailableBytes: func(string) (int64, error) { return math.MaxInt64, nil },
			BeforeEntry:    func(int, TransferEntry) error { return errors.New("interrupted") },
		})
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("expected interruption, got %v", err)
		}
		entries, readErr := os.ReadDir(artifactRoot)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("temporary artifact survived interruption: %#v", entries)
		}
	})
}

func TestCreateBundleFailsSpaceCheckBeforeArtifactWrite(t *testing.T) {
	root, lanePath, plan, createdAt := bundleFixture(t)
	artifactRoot := filepath.Join(root, DefaultStateRelPath, "bundles")
	_, err := CreateBundle(CreateBundleInput{
		LanePath: lanePath, PolicyRoot: root, ArtifactRoot: artifactRoot,
		BatchID: "lane_no_space", SourceNodeKey: "macbook", Plan: plan, CreatedAt: createdAt,
		AvailableBytes: func(string) (int64, error) { return 0, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "insufficient free disk space") {
		t.Fatalf("expected disk-space failure, got %v", err)
	}
	entries, readErr := os.ReadDir(artifactRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("space failure wrote an artifact: %#v", entries)
	}
}

func bundleFixture(t *testing.T) (string, string, TransferPlan, time.Time) {
	t.Helper()
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	createdAt := time.Date(2026, 8, 16, 11, 12, 13, 123456789, time.UTC)
	longName := strings.Repeat("long-path-", 14) + "payload.txt"
	mustLaneFile(t, filepath.Join(lanePath, "unicodé", longName), "pax payload")
	mustLaneFile(t, filepath.Join(lanePath, "docs", "README.md"), "user documentation")
	mustLaneFile(t, filepath.Join(lanePath, "script.sh"), "#!/bin/sh\nexit 0\n")
	mustLaneFile(t, filepath.Join(lanePath, "zero.txt"), "")
	if err := os.MkdirAll(filepath.Join(lanePath, "empty-dir"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(lanePath, "script.sh"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := filepath.Walk(lanePath, func(pathValue string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(pathValue, createdAt, createdAt)
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildTransferPlan(lanePath, root, "faithful")
	if err != nil {
		t.Fatal(err)
	}
	plan.Transport, err = SelectBundleTransport(plan, TransportModeBundleSeed)
	if err != nil {
		t.Fatal(err)
	}
	return root, lanePath, plan, createdAt
}

func snapshotBundleSource(t *testing.T, root string) string {
	t.Helper()
	var rows []string
	if err := filepath.Walk(root, func(pathValue string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, pathValue)
		if err != nil {
			return err
		}
		rows = append(rows, strings.Join([]string{filepath.ToSlash(relative), info.Mode().String(), info.ModTime().UTC().Format(time.RFC3339Nano)}, "|"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return strings.Join(rows, "\n")
}
