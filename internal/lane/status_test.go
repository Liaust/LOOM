package lane

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildStatusUsesExternalRuntimeStatePath(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "box-state", "lane")
	status := BuildStatus(StatusInput{
		RootPath:       root,
		StatePath:      statePath,
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
	})
	if status.StatePath != statePath {
		t.Fatalf("state path = %q, want %q", status.StatePath, statePath)
	}
	if strings.Contains(filepath.ToSlash(status.StatePath), "/.loom/state/") {
		t.Fatalf("external Lane state unexpectedly resolved inside visible Box: %s", status.StatePath)
	}
}

func TestBuildStatusScansPendingLaneAndIgnoresReadme(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	if err := os.MkdirAll(filepath.Join(lanePath, "dataset"), 0o755); err != nil {
		t.Fatalf("mkdir lane: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, "README.md"), []byte("docs"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, ".DS_Store"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("write ds store: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, "video.mov"), []byte("12345"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, "dataset", "a.bin"), []byte("abc"), 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(lanePath, "dataset", "node_modules", "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir generated nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, "dataset", "node_modules", "pkg", "index.js"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write generated nested: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(lanePath, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir generated top-level: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, ".git", "config"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write generated top-level: %v", err)
	}

	status := BuildStatus(StatusInput{
		RootPath: root,
		LookupPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		SSHConfigCheck: func(string) error { return nil },
	})

	if status.State != StatePending {
		t.Fatalf("state = %q, want pending: %#v", status.State, status)
	}
	if status.PendingItems != 4 || status.PendingFiles != 5 || status.PendingDirs != 4 || status.PendingBytes != 26 {
		t.Fatalf("unexpected pending counts: %#v", status)
	}
	if status.IgnoredEntryCount != 1 || status.IgnoredFileCount != 1 || status.IgnoredBytes != 4 {
		t.Fatalf("ignored README accounting unexpected: %#v", status)
	}
	if status.Profile != "faithful" || status.PolicyFingerprint == "" || status.InventoryHash == "" {
		t.Fatalf("policy evidence missing: %#v", status)
	}
	if status.Transport.SchemaVersion != BundlePlanSchemaVersion || status.Transport.SelectedMode == "" {
		t.Fatalf("transport evidence missing: %#v", status.Transport)
	}
	if status.Preflight.Status != PreflightReady || status.Preflight.SSHConfigStatus != "ok" {
		t.Fatalf("unexpected preflight: %#v", status.Preflight)
	}
}

func TestBuildStatusReadsLatestTransfer(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DefaultLaneRelPath), 0o755); err != nil {
		t.Fatalf("mkdir lane: %v", err)
	}
	batches := filepath.Join(root, ".loom", "state", "lane", "batches")
	if err := os.MkdirAll(batches, 0o755); err != nil {
		t.Fatalf("mkdir batches: %v", err)
	}
	started := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	completed := started.Add(time.Minute)
	record := `{
  "schema_version": "loom.lane.batch.v0.6.3",
  "batch_id": "lane_batch_test",
  "status": "accepted",
  "visible_storage_path": "macbook/Lane/2026-06-14/lane_batch_test",
  "file_count": 2,
  "total_bytes": 8,
  "started_at": "` + started.Format(time.RFC3339) + `",
  "completed_at": "` + completed.Format(time.RFC3339) + `"
}`
	path := filepath.Join(batches, "lane_batch_test.json")
	if err := os.WriteFile(path, []byte(record), 0o644); err != nil {
		t.Fatalf("write batch: %v", err)
	}

	status := BuildStatus(StatusInput{
		RootPath:       root,
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
		Now:            func() time.Time { return completed },
		LaneRelPath:    DefaultLaneRelPath,
		StateRelPath:   DefaultStateRelPath,
		MainHost:       "loom-main",
	})
	if status.State != StateEmpty {
		t.Fatalf("state = %q, want empty", status.State)
	}
	if status.LastTransfer == nil || status.LastTransfer.BatchID != "lane_batch_test" || status.LastTransfer.VisibleStoragePath == "" {
		t.Fatalf("latest transfer missing: %#v", status.LastTransfer)
	}
}

func TestBuildStatusPrefersAcceptedTransferOverSupersededCleanupRecord(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DefaultLaneRelPath), 0o755); err != nil {
		t.Fatalf("mkdir lane: %v", err)
	}
	statePath := filepath.Join(root, DefaultStateRelPath)
	oldStarted := time.Date(2026, 6, 14, 11, 59, 0, 0, time.UTC)
	cleanupCompleted := time.Date(2026, 6, 14, 12, 1, 0, 0, time.UTC)
	if err := writeBatchRecord(statePath, BatchRecord{
		SchemaVersion: BatchSchemaVersion,
		BatchID:       "lane_20260614T115900Z",
		Status:        BatchStatusSuperseded,
		StartedAt:     &oldStarted,
		CompletedAt:   &cleanupCompleted,
		ErrorMessage:  "superseded by lane_20260614T120000Z",
	}); err != nil {
		t.Fatalf("write superseded batch: %v", err)
	}
	started := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	completed := started.Add(30 * time.Second)
	if err := writeBatchRecord(statePath, BatchRecord{
		SchemaVersion:      BatchSchemaVersion,
		BatchID:            "lane_20260614T120000Z",
		Status:             BatchStatusAccepted,
		VisibleStoragePath: "macbook/Lane/2026-06-14/lane_20260614T120000Z",
		FileCount:          1,
		TotalBytes:         1024,
		StartedAt:          &started,
		CompletedAt:        &completed,
	}); err != nil {
		t.Fatalf("write accepted batch: %v", err)
	}

	status := BuildStatus(StatusInput{
		RootPath:       root,
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
		LaneRelPath:    DefaultLaneRelPath,
		StateRelPath:   DefaultStateRelPath,
		MainHost:       "loom-main",
	})
	if status.LastTransfer == nil || status.LastTransfer.BatchID != "lane_20260614T120000Z" || status.LastTransfer.Status != BatchStatusAccepted {
		t.Fatalf("latest transfer = %#v, want accepted retry batch", status.LastTransfer)
	}
}

func TestBuildPreflightReportsMissingTools(t *testing.T) {
	status := BuildPreflight(StatusInput{
		LookupPath: func(string) (string, error) {
			return "", errors.New("missing")
		},
	})
	if status.Status != PreflightMissingTools {
		t.Fatalf("status = %q, want missing_tools: %#v", status.Status, status)
	}
	if len(status.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v", status.Diagnostics)
	}
}

func TestBuildStatusReportsFailedResumableBundleAndRecoveryAction(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DefaultLaneRelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	artifactPath := filepath.Join(root, DefaultStateRelPath, "bundles", "lane_bundle_failed")
	if err := writeBatchRecord(filepath.Join(root, DefaultStateRelPath), BatchRecord{
		SchemaVersion: BatchSchemaVersion, BatchID: "lane_bundle_failed", Status: BatchStatusFailed,
		SourceNodeKey: "macbook", StartedAt: &now, SelectedTransport: TransportModeBundleSeed,
		RequestedTransport: TransportModeAuto, TransportReason: "many small files",
		BundleArtifactPath: artifactPath, BundleArchiveSHA256: "sha256:" + strings.Repeat("a", 64),
		BundleCleanupState: BundleCleanupRetainedForRetry,
	}); err != nil {
		t.Fatal(err)
	}
	status := BuildStatus(StatusInput{
		RootPath:       root,
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
	})
	if status.LastTransfer == nil || status.LastTransfer.SelectedTransport != TransportModeBundleSeed || status.LastTransfer.BundleArtifactPath != artifactPath || status.LastTransfer.BundleCleanupState != BundleCleanupRetainedForRetry {
		t.Fatalf("resumable bundle status missing: %#v", status.LastTransfer)
	}
	var resume bool
	for _, action := range status.LastTransfer.NextActions {
		if action.Key == "resume_bundle" && strings.Contains(action.Command, "--resume") {
			resume = true
		}
	}
	if !resume {
		t.Fatalf("bundle recovery action missing: %#v", status.LastTransfer.NextActions)
	}
}

func TestBuildStatusReportsInterruptedTransferredBundleRecoveryAction(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DefaultLaneRelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	artifactPath := filepath.Join(root, DefaultStateRelPath, "bundles", "lane_bundle_transferred")
	if err := writeBatchRecord(filepath.Join(root, DefaultStateRelPath), BatchRecord{
		SchemaVersion: BatchSchemaVersion, BatchID: "lane_bundle_transferred", Status: BatchStatusTransferred,
		SourceNodeKey: "macbook", StartedAt: &now, SelectedTransport: TransportModeBundleSeed,
		RequestedTransport: TransportModeAuto, TransportReason: "many small files",
		BundleArtifactPath: artifactPath, BundleArchiveSHA256: "sha256:" + strings.Repeat("a", 64),
		BundleCleanupState: BundleCleanupRetainedForRetry,
	}); err != nil {
		t.Fatal(err)
	}
	status := BuildStatus(StatusInput{
		RootPath:       root,
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
	})
	if status.LastTransfer == nil || status.LastTransfer.Status != BatchStatusTransferred {
		t.Fatalf("interrupted transfer missing: %#v", status.LastTransfer)
	}
	for _, action := range status.LastTransfer.NextActions {
		if action.Key == "resume_bundle" && strings.Contains(action.Command, "--resume") {
			return
		}
	}
	t.Fatalf("interrupted bundle recovery action missing: %#v", status.LastTransfer.NextActions)
}

func TestBuildStatusPlansTransportWhenToolsAreMissing(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	if err := os.MkdirAll(lanePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lanePath, "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	status := BuildStatus(StatusInput{
		RootPath:   root,
		LookupPath: func(string) (string, error) { return "", errors.New("missing") },
	})
	if status.Preflight.Status != PreflightMissingTools || status.State != StatePending {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status.Transport.SelectedMode != TransportModeFileTree || status.Transport.RegularFileCount != 1 || status.InventoryHash == "" {
		t.Fatalf("offline transport plan missing: %#v", status)
	}
}

func TestPendingLaneAcknowledgementClearsAttentionUntilItemChanges(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	if err := os.MkdirAll(lanePath, 0o755); err != nil {
		t.Fatalf("mkdir lane: %v", err)
	}
	now := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	itemPath := filepath.Join(lanePath, "gmail-automation")
	if err := os.WriteFile(itemPath, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write pending item: %v", err)
	}
	if err := os.Chtimes(itemPath, old, old); err != nil {
		t.Fatalf("chtimes pending item: %v", err)
	}

	input := StatusInput{
		RootPath:       root,
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
		Now:            func() time.Time { return now },
	}
	status := BuildStatus(input)
	if status.ActivePendingItems != 1 || status.AcknowledgedPendingItems != 0 || !hasLaneDiagnostic(status.Diagnostics, "lane.pending_stale") {
		t.Fatalf("expected active stale pending attention: %#v", status)
	}

	result, err := AcknowledgePendingItem(AcknowledgePendingInput{
		RootPath:       root,
		RelativePath:   "gmail-automation",
		Now:            func() time.Time { return now.Add(time.Minute) },
		LookupPath:     input.LookupPath,
		SSHConfigCheck: input.SSHConfigCheck,
	})
	if err != nil {
		t.Fatalf("AcknowledgePendingItem returned error: %v", err)
	}
	if result.AttentionStatus != AttentionStatusAcknowledged {
		t.Fatalf("ack result = %#v", result)
	}

	status = BuildStatus(input)
	if status.ActivePendingItems != 0 || status.AcknowledgedPendingItems != 1 || hasLaneDiagnostic(status.Diagnostics, "lane.pending_stale") {
		t.Fatalf("acknowledged item should clear active attention: %#v", status)
	}
	if got := status.Items[0].AttentionStatus; got != AttentionStatusAcknowledged {
		t.Fatalf("item attention = %q, want acknowledged", got)
	}

	if err := os.WriteFile(itemPath, []byte("changed payload"), 0o644); err != nil {
		t.Fatalf("change pending item: %v", err)
	}
	if err := os.Chtimes(itemPath, old, old); err != nil {
		t.Fatalf("chtimes changed item: %v", err)
	}
	status = BuildStatus(input)
	if status.ActivePendingItems != 1 || status.AcknowledgedPendingItems != 0 || !hasLaneDiagnostic(status.Diagnostics, "lane.pending_stale") {
		t.Fatalf("changed item should re-enter active attention: %#v", status)
	}
}

func TestFailedLaneTransferAcknowledgementClearsActiveAttention(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DefaultLaneRelPath), 0o755); err != nil {
		t.Fatalf("mkdir lane: %v", err)
	}
	now := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	statePath := filepath.Join(root, DefaultStateRelPath)
	if err := writeBatchRecord(statePath, BatchRecord{
		SchemaVersion: BatchSchemaVersion,
		BatchID:       "lane_failed",
		Status:        BatchStatusFailed,
		FileCount:     2,
		TotalBytes:    42,
		StartedAt:     &now,
		ErrorMessage:  "rsync failed",
	}); err != nil {
		t.Fatalf("write failed batch: %v", err)
	}

	input := StatusInput{
		RootPath:       root,
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
		Now:            func() time.Time { return now },
	}
	status := BuildStatus(input)
	if status.LastTransfer == nil || status.LastTransfer.AttentionStatus != AttentionStatusActive || !hasLaneDiagnostic(status.Diagnostics, "lane.transfer_failed") {
		t.Fatalf("expected active failed transfer attention: %#v", status)
	}

	result, err := AcknowledgeTransferAttention(AcknowledgeTransferInput{
		RootPath: root,
		BatchID:  "lane_failed",
		Now:      func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatalf("AcknowledgeTransferAttention returned error: %v", err)
	}
	if result.AttentionStatus != AttentionStatusAcknowledged {
		t.Fatalf("ack result = %#v", result)
	}

	status = BuildStatus(input)
	if status.LastTransfer == nil || status.LastTransfer.AttentionStatus != AttentionStatusAcknowledged || hasLaneDiagnostic(status.Diagnostics, "lane.transfer_failed") {
		t.Fatalf("acknowledged failed transfer should clear active attention: %#v", status)
	}
}

func TestAcceptedOnMainIsVisibleRepairAttentionAndCanBeAcknowledged(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DefaultLaneRelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	statePath := filepath.Join(root, DefaultStateRelPath)
	if err := writeBatchRecord(statePath, BatchRecord{
		SchemaVersion: BatchSchemaVersion,
		BatchID:       "lane_accepted_crash",
		Status:        BatchStatusAcceptedOnMain,
		FileCount:     1,
		TotalBytes:    7,
		StartedAt:     &now,
	}); err != nil {
		t.Fatal(err)
	}
	input := StatusInput{
		RootPath:       root,
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
		Now:            func() time.Time { return now },
	}
	status := BuildStatus(input)
	if status.LastTransfer == nil || status.LastTransfer.Status != BatchStatusAcceptedOnMain || status.LastTransfer.AttentionStatus != AttentionStatusActive || !hasLaneDiagnostic(status.Diagnostics, "lane.transfer_failed") {
		t.Fatalf("accepted_on_main crash window is not active attention: %#v", status)
	}
	if len(status.Diagnostics) == 0 || !strings.Contains(status.Diagnostics[0].Suggestion, "loom lane repair lane_accepted_crash") {
		t.Fatalf("accepted_on_main diagnostic does not expose repair: %#v", status.Diagnostics)
	}
	for _, key := range []string{"inspect", "repair_main_custody", "acknowledge_transfer", "archive_transfer"} {
		if !hasLaneAction(status.LastTransfer.NextActions, key) {
			t.Fatalf("accepted_on_main actions missing %q: %#v", key, status.LastTransfer.NextActions)
		}
	}
	if _, err := AcknowledgeTransferAttention(AcknowledgeTransferInput{RootPath: root, BatchID: "lane_accepted_crash", Now: func() time.Time { return now.Add(time.Minute) }}); err != nil {
		t.Fatalf("acknowledge accepted_on_main: %v", err)
	}
	status = BuildStatus(input)
	if status.LastTransfer == nil || status.LastTransfer.AttentionStatus != AttentionStatusAcknowledged || hasLaneDiagnostic(status.Diagnostics, "lane.transfer_failed") {
		t.Fatalf("acknowledged accepted_on_main state = %#v", status)
	}
	if _, err := ArchiveTransferAttention(AcknowledgeTransferInput{RootPath: root, BatchID: "lane_accepted_crash", Now: func() time.Time { return now.Add(2 * time.Minute) }}); err != nil {
		t.Fatalf("archive accepted_on_main: %v", err)
	}
	status = BuildStatus(input)
	if status.LastTransfer == nil || status.LastTransfer.AttentionStatus != AttentionStatusArchived || hasLaneDiagnostic(status.Diagnostics, "lane.transfer_failed") {
		t.Fatalf("archived accepted_on_main state = %#v", status)
	}
}

func hasLaneDiagnostic(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
