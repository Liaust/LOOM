package lane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemmeta"
)

func TestV2LaneBundleLocalSmoke(t *testing.T) {
	fixtureStarted := time.Now()
	sourceRoot := t.TempDir()
	lanePath := filepath.Join(sourceRoot, DefaultLaneRelPath)
	writeManySmallLaneFixture(t, lanePath, DefaultBundleSmallFileCountAbove+1)
	fixtureDuration := time.Since(fixtureStarted)

	planStarted := time.Now()
	plan, err := BuildTransferPlan(lanePath, sourceRoot, filepolicy.ProfileFaithful)
	if err != nil {
		t.Fatal(err)
	}
	planDuration := time.Since(planStarted)
	if plan.FileCount != DefaultBundleSmallFileCountAbove+1 || plan.Transport.SelectedMode != TransportModeBundleSeed || plan.Transport.ReasonCode != BundleReasonManySmallFiles {
		t.Fatalf("many-small automatic plan = %#v", plan)
	}

	forcedTree, err := SelectBundleTransport(plan, TransportModeFileTree)
	if err != nil {
		t.Fatal(err)
	}
	forcedBundle, err := SelectBundleTransport(plan, TransportModeBundleSeed)
	if err != nil {
		t.Fatal(err)
	}
	if forcedTree.SelectedMode != TransportModeFileTree || forcedBundle.SelectedMode != TransportModeBundleSeed || !forcedTree.Forced || !forcedBundle.Forced || plan.InventoryHash == "" {
		t.Fatalf("forced transport evidence: tree=%#v bundle=%#v", forcedTree, forcedBundle)
	}

	largeStarted := time.Now()
	largeRoot := t.TempDir()
	largeLanePath := filepath.Join(largeRoot, DefaultLaneRelPath)
	writeLargeFewLaneFixture(t, largeLanePath, 4, 1024*1024)
	largePlan, err := BuildTransferPlan(largeLanePath, largeRoot, filepolicy.ProfileFaithful)
	if err != nil {
		t.Fatal(err)
	}
	largePlanDuration := time.Since(largeStarted)
	if largePlan.FileCount != 4 || largePlan.TotalBytes != 4*1024*1024 || largePlan.Transport.SelectedMode != TransportModeFileTree {
		t.Fatalf("large-few automatic plan = %#v", largePlan)
	}

	createdAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	archiveStarted := time.Now()
	artifact, err := CreateBundle(CreateBundleInput{
		LanePath: lanePath, PolicyRoot: sourceRoot,
		ArtifactRoot: filepath.Join(sourceRoot, DefaultStateRelPath, "bundles"),
		BatchID:      "v2_lane_bundle_local", SourceNodeKey: "workspace", SourceBoxID: "box-local-smoke",
		Plan: plan, CreatedAt: createdAt, AvailableBytes: maxBundleTestSpace,
	})
	if err != nil {
		t.Fatal(err)
	}
	archiveDuration := time.Since(archiveStarted)
	verified, err := VerifyBundle(artifact.ManifestPath, artifact.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	if verified.InventoryHash != plan.InventoryHash || verified.Plan.Profile != plan.Profile || verified.Plan.PolicyFingerprint != plan.PolicyFingerprint {
		t.Fatalf("verified manifest diverged from canonical plan: manifest=%#v plan=%#v", verified, plan)
	}

	remoteRoot := filepath.Join(t.TempDir(), "lane")
	withLaneRemoteBoundary(t, remoteRoot)
	staging := filepath.Join(remoteRoot, "staging", "workspace", "v2_lane_bundle_local")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	stagedArchive := filepath.Join(staging, bundleArchiveFileName)
	stagedManifest := filepath.Join(staging, bundleManifestFileName)
	transferStarted := time.Now()
	if err := copyRegularPath(artifact.ArchivePath, stagedArchive, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyRegularPath(artifact.ManifestPath, stagedManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	transferDuration := time.Since(transferStarted)

	acceptedPath := filepath.Join(staging, "tree")
	acceptInput := BundleAcceptInput{
		RemoteRoot: remoteRoot, SourceNodeKey: "workspace", SourceBoxID: "box-local-smoke",
		BatchID: "v2_lane_bundle_local", AcceptedDate: "2026-08-17", AcceptedAt: createdAt,
		CustodyNodeKey: "main", ManifestPath: stagedManifest, ArchivePath: stagedArchive, AcceptedPath: acceptedPath,
		ImportsRoot: filepath.Join(t.TempDir(), "imports"),
	}
	unpackStarted := time.Now()
	unpack, err := UnpackBundle(acceptInput)
	if err != nil {
		t.Fatal(err)
	}
	unpackDuration := time.Since(unpackStarted)
	if unpack.FileCount != plan.FileCount || unpack.DirCount != plan.DirCount || unpack.TotalBytes != plan.TotalBytes {
		t.Fatalf("unpack totals = %#v plan=%#v", unpack, plan)
	}
	assertBundleTreesEqual(t, lanePath, acceptedPath, plan)

	catalog := &fakeLaneCatalog{}
	catalogStarted := time.Now()
	accepted, err := AcceptBundle(context.Background(), catalog, acceptInput)
	if err != nil {
		t.Fatal(err)
	}
	catalogDuration := time.Since(catalogStarted)
	if !accepted.Unpack.Idempotent || accepted.Catalog.FilesCataloged != plan.FileCount || accepted.Catalog.TotalBytes != plan.TotalBytes || len(catalog.inputs) != plan.FileCount {
		t.Fatalf("catalog acceptance = %#v inputs=%d", accepted, len(catalog.inputs))
	}

	metrics := struct {
		Schema                  string `json:"schema"`
		ManySmallFiles          int    `json:"many_small_files"`
		ManySmallBytes          int64  `json:"many_small_bytes"`
		LargeFewFiles           int    `json:"large_few_files"`
		LargeFewBytes           int64  `json:"large_few_bytes"`
		FixtureMS               int64  `json:"fixture_ms"`
		PlanningMS              int64  `json:"planning_ms"`
		LargePlanningMS         int64  `json:"large_planning_ms"`
		ArchiveMS               int64  `json:"archive_ms"`
		TransferSimulatorMS     int64  `json:"transfer_simulator_ms"`
		UnpackMS                int64  `json:"unpack_ms"`
		CatalogMS               int64  `json:"catalog_ms"`
		EstimatedTemporaryBytes int64  `json:"estimated_temporary_bytes"`
		ArchiveBytes            int64  `json:"archive_bytes"`
	}{
		Schema:         "loom.lane.bundle_local_smoke.v1",
		ManySmallFiles: plan.FileCount, ManySmallBytes: plan.TotalBytes,
		LargeFewFiles: largePlan.FileCount, LargeFewBytes: largePlan.TotalBytes,
		FixtureMS: fixtureDuration.Milliseconds(), PlanningMS: planDuration.Milliseconds(),
		LargePlanningMS: largePlanDuration.Milliseconds(), ArchiveMS: archiveDuration.Milliseconds(),
		TransferSimulatorMS: transferDuration.Milliseconds(), UnpackMS: unpackDuration.Milliseconds(),
		CatalogMS: catalogDuration.Milliseconds(), EstimatedTemporaryBytes: plan.Transport.EstimatedTemporaryBytes,
		ArchiveBytes: artifact.ArchiveBytes,
	}
	payload, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LANE_BUNDLE_LOCAL_METRICS %s", payload)
}

func writeManySmallLaneFixture(t *testing.T, root string, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		pathValue := filepath.Join(root, fmt.Sprintf("group-%02d", index/100), fmt.Sprintf("file-%04d.txt", index))
		if err := os.MkdirAll(filepath.Dir(pathValue), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pathValue, []byte(fmt.Sprintf("%032d", index)), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "empty-dir"), 0o750); err != nil {
		t.Fatal(err)
	}
}

func writeLargeFewLaneFixture(t *testing.T, root string, count, bytesPerFile int) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < count; index++ {
		payload := bytes.Repeat([]byte{byte(index + 1)}, bytesPerFile)
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("large-%02d.bin", index)), payload, 0o640); err != nil {
			t.Fatal(err)
		}
	}
}

func assertBundleTreesEqual(t *testing.T, sourceRoot, acceptedRoot string, plan TransferPlan) {
	t.Helper()
	for _, entry := range plan.Entries {
		sourcePath := filepath.Join(sourceRoot, filepath.FromSlash(entry.RelativePath))
		acceptedPath := filepath.Join(acceptedRoot, filepath.FromSlash(entry.RelativePath))
		sourceInfo, err := os.Lstat(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		acceptedInfo, err := os.Lstat(acceptedPath)
		if err != nil {
			t.Fatal(err)
		}
		if sourceInfo.Mode().Perm() != acceptedInfo.Mode().Perm() || !sourceInfo.ModTime().UTC().Equal(acceptedInfo.ModTime().UTC()) {
			t.Fatalf("metadata differs for %s: source=%#v accepted=%#v", entry.RelativePath, sourceInfo, acceptedInfo)
		}
		if entry.Kind == filesystemmeta.ObjectKindDirectory {
			if !acceptedInfo.IsDir() {
				t.Fatalf("accepted %s is not a directory", entry.RelativePath)
			}
			continue
		}
		sourcePayload, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		acceptedPayload, err := os.ReadFile(acceptedPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(sourcePayload, acceptedPayload) {
			t.Fatalf("payload differs for %s", entry.RelativePath)
		}
	}
}
