package cloudstorage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanOffloadBuildsManifestWithoutRemoteMutation(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "storage-archive", "objects", ".loom-acceptance", "offload-demo")
	writeCloudTestFile(t, filepath.Join(sourceDir, "payload.txt"), []byte("offload payload\n"))
	cfg := enabledTestCloudConfig(t, tmp)
	driver := &recordingSnapshotDriver{}

	result, err := PlanOffload(context.Background(), OffloadInput{
		Config:           cfg,
		Driver:           driver,
		SourceStorageRef: "main/Archive/.loom-acceptance/offload-demo",
		SourcePath:       sourceDir,
		NodeID:           "loom-main",
		Now:              fixedCloudNow,
	})
	if err != nil {
		t.Fatalf("PlanOffload returned error: %v", err)
	}
	if result.Status != OffloadStatusPlanned {
		t.Fatalf("status = %q", result.Status)
	}
	if len(driver.calls) != 0 {
		t.Fatalf("plan called remote driver: %#v", driver.calls)
	}
	if result.Manifest == nil || result.Manifest.SchemaVersion != OffloadManifestSchema {
		t.Fatalf("manifest = %#v", result.Manifest)
	}
	if !strings.Contains(result.RemotePrefix, "full-offload/loom-main/main-Archive-.loom-acceptance-offload-demo/cloud_offload_") {
		t.Fatalf("unexpected remote prefix: %s", result.RemotePrefix)
	}
	if result.FileCount != 1 || result.TotalBytes == 0 {
		t.Fatalf("unexpected inventory: files=%d bytes=%d", result.FileCount, result.TotalBytes)
	}
}

func TestApplyOffloadUploadsVerifiesAndRetainsLocalBytes(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "storage-archive", "objects", ".loom-acceptance", "offload-demo")
	sourceFile := filepath.Join(sourceDir, "payload.txt")
	writeCloudTestFile(t, sourceFile, []byte("offload payload\n"))
	cfg := enabledTestCloudConfig(t, tmp)
	driver := &recordingSnapshotDriver{}

	result, err := ApplyOffload(context.Background(), OffloadInput{
		Config:           cfg,
		Driver:           driver,
		SourceStorageRef: "main/Archive/.loom-acceptance/offload-demo",
		SourcePath:       sourceDir,
		NodeID:           "loom-main",
		Confirm:          true,
		Now:              fixedCloudNow,
	})
	if err != nil {
		t.Fatalf("ApplyOffload returned error: %v", err)
	}
	if result.Status != OffloadStatusSucceeded {
		t.Fatalf("status = %q error=%q checks=%#v", result.Status, result.Error, result.Checks)
	}
	if _, err := os.Stat(sourceFile); err != nil {
		t.Fatalf("source bytes should be retained after offload: %v", err)
	}
	manifest, err := ReadOffloadManifest(result.LocalManifestPath)
	if err != nil {
		t.Fatalf("local offload manifest invalid: %v", err)
	}
	if manifest.Custody != OffloadCustodyReplicatedToCloud || manifest.LocalSourceAction != OffloadLocalSourceRetained {
		t.Fatalf("unexpected custody/local action: %#v", manifest)
	}
	joinedCalls := strings.Join(driver.calls, "\n")
	for _, want := range []string{
		"copy_to:" + sourceDir + "->" + result.PayloadRemotePrefix,
		"check:" + sourceDir + "->" + result.PayloadRemotePrefix,
		"copy_to:" + result.LocalManifestPath + "->" + result.ManifestRemotePath,
	} {
		if !strings.Contains(joinedCalls, want) {
			t.Fatalf("calls do not contain %q:\n%s", want, joinedCalls)
		}
	}
}

func TestApplyOffloadRequiresConfirmation(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	sourceDir := filepath.Join(tmp, "storage-archive", "objects", ".loom-acceptance", "offload-demo")
	writeCloudTestFile(t, filepath.Join(sourceDir, "payload.txt"), []byte("offload payload\n"))
	cfg := enabledTestCloudConfig(t, tmp)
	driver := &recordingSnapshotDriver{}

	result, err := ApplyOffload(context.Background(), OffloadInput{
		Config:           cfg,
		Driver:           driver,
		SourceStorageRef: "main/Archive/.loom-acceptance/offload-demo",
		SourcePath:       sourceDir,
		NodeID:           "loom-main",
		Confirm:          false,
		Now:              fixedCloudNow,
	})
	if err != nil {
		t.Fatalf("ApplyOffload returned error: %v", err)
	}
	if result.Status != OffloadStatusConfirmationRequired {
		t.Fatalf("status = %q", result.Status)
	}
	if len(driver.calls) != 0 {
		t.Fatalf("unconfirmed apply called remote driver: %#v", driver.calls)
	}
}
