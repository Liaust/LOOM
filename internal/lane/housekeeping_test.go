package lane

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSuccessfulTransportArtifactWaitsForDurableCompletedRecord(t *testing.T) {
	for _, mode := range []TransportMode{TransportModeFileTree, TransportModeBundleSeed} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			mustLaneFile(t, filepath.Join(root, DefaultLaneRelPath, "payload.txt"), "payload")
			batchID := "lane_durable_record_" + string(mode)
			input := bundleSendTestInput(root, batchID, false)
			input.RequestedTransport = mode
			input.AfterCompletedRecord = func(record BatchRecord) error {
				persisted, err := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", batchID+".json"))
				if err != nil {
					return err
				}
				if persisted.CompletedAt == nil || persisted.Status != BatchStatusLocalCleanupDone || persisted.LocalCleanupQuarantinePath == "" {
					t.Fatalf("completed record was not durable before safety cleanup: %#v", persisted)
				}
				if _, err := os.Lstat(record.LocalSafetyPath); err != nil {
					t.Fatalf("transport safety artifact was removed before completed record callback: %v", err)
				}
				if _, err := os.Lstat(record.LocalCleanupQuarantinePath); err != nil {
					t.Fatalf("cleanup quarantine was not durable before safety cleanup: %v", err)
				}
				return nil
			}
			result, err := Send(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			assertSuccessfulSafetyArtifactRetired(t, result)
			if _, err := os.Lstat(result.LocalCleanupQuarantinePath); err != nil {
				t.Fatalf("current successful quarantine missing: %v", err)
			}
		})
	}
}

func TestSuccessfulKeepLocalPreservesVisibleSourceWithoutRecoveryCopies(t *testing.T) {
	for _, mode := range []TransportMode{TransportModeFileTree, TransportModeBundleSeed} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			payloadPath := filepath.Join(root, DefaultLaneRelPath, "payload.txt")
			mustLaneFile(t, payloadPath, "keep local")
			input := bundleSendTestInput(root, "lane_keep_local_"+string(mode), false)
			input.RequestedTransport = mode
			input.KeepLocal = true
			result, err := Send(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != BatchStatusCataloged || result.LocalCleanupQuarantinePath != "" || result.RecoveryStorage.RetainedBytes != 0 {
				t.Fatalf("keep-local recovery state = %#v", result)
			}
			assertLaneFile(t, payloadPath, "keep local")
			assertSuccessfulSafetyArtifactRetired(t, result)
		})
	}
}

func TestNewSuccessfulBatchRetiresPreviousQuarantineImmediately(t *testing.T) {
	root := t.TempDir()
	firstTime := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	first := successfulHousekeepingSend(t, root, "lane_success_first", TransportModeFileTree, firstTime, "first")
	if _, err := os.Lstat(first.LocalCleanupQuarantinePath); err != nil {
		t.Fatal(err)
	}
	secondTime := firstTime.Add(5 * time.Minute)
	second := successfulHousekeepingSend(t, root, "lane_success_second", TransportModeBundleSeed, secondTime, "second")
	if _, err := os.Lstat(first.LocalCleanupQuarantinePath); !os.IsNotExist(err) {
		t.Fatalf("previous successful quarantine survived newer success: %v", err)
	}
	if _, err := os.Lstat(second.LocalCleanupQuarantinePath); err != nil {
		t.Fatalf("new successful quarantine missing: %v", err)
	}
	oldRecord, err := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", first.BatchID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if oldRecord.LocalCleanupQuarantineState != CleanupQuarantineRemovedAfterGrace || oldRecord.LocalCleanupQuarantineRemovalReason != CleanupRemovalReasonNewerSuccess || oldRecord.LocalCleanupQuarantineRemovedAt == nil {
		t.Fatalf("previous quarantine retirement is not audited: %#v", oldRecord)
	}
	accounting, err := InspectRecoveryStorage(root, DefaultStateRelPath)
	if err != nil {
		t.Fatal(err)
	}
	if accounting.SuccessfulQuarantineCount != 1 || accounting.SuccessfulGraceBytes != int64(len("second")) || accounting.TransportSafetyBytes != 0 || accounting.RetainedBytes != int64(len("second")) {
		t.Fatalf("successful steady-state accounting = %#v", accounting)
	}
}

func TestSuccessfulKeepLocalBatchRetiresPreviousQuarantineImmediately(t *testing.T) {
	for _, mode := range []TransportMode{TransportModeFileTree, TransportModeBundleSeed} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			firstTime := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
			first := successfulHousekeepingSend(t, root, "lane_before_keep_local_"+string(mode), mode, firstTime, "first")
			if _, err := os.Lstat(first.LocalCleanupQuarantinePath); err != nil {
				t.Fatal(err)
			}

			payloadPath := filepath.Join(root, DefaultLaneRelPath, "payload.txt")
			mustLaneFile(t, payloadPath, "keep-local newer")
			secondTime := firstTime.Add(5 * time.Minute)
			input := bundleSendTestInput(root, "lane_keep_local_newer_"+string(mode), false)
			input.RequestedTransport = mode
			input.KeepLocal = true
			input.Now = func() time.Time { return secondTime }
			second, err := Send(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}

			if second.Status != BatchStatusCataloged || second.LocalCleanupQuarantinePath != "" {
				t.Fatalf("keep-local result = %#v", second)
			}
			assertLaneFile(t, payloadPath, "keep-local newer")
			if _, err := os.Lstat(first.LocalCleanupQuarantinePath); !os.IsNotExist(err) {
				t.Fatalf("older quarantine survived keep-local success: %v", err)
			}
			oldRecord, err := readBatchRecord(filepath.Join(root, DefaultStateRelPath, "batches", first.BatchID+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if oldRecord.LocalCleanupQuarantineState != CleanupQuarantineRemovedAfterGrace || oldRecord.LocalCleanupQuarantineRemovalReason != CleanupRemovalReasonNewerSuccess || oldRecord.LocalCleanupQuarantineRemovedAt == nil {
				t.Fatalf("keep-local retirement is not audited: %#v", oldRecord)
			}
			if second.RecoveryStorage.SuccessfulQuarantineCount != 0 || second.RecoveryStorage.SuccessfulGraceBytes != 0 || second.RecoveryStorage.RetainedBytes != 0 {
				t.Fatalf("keep-local recovery storage did not converge to zero: %#v", second.RecoveryStorage)
			}
		})
	}
}

func TestRestartHousekeepingUsesLatestCompletedSuccessWithoutQuarantine(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, DefaultStateRelPath)
	firstTime := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	first := successfulHousekeepingSend(t, root, "lane_restart_before_keep_local", TransportModeFileTree, firstTime, "first")
	visiblePath := filepath.Join(root, DefaultLaneRelPath, "keep-local.txt")
	mustLaneFile(t, visiblePath, "visible survives")

	secondTime := firstTime.Add(5 * time.Minute)
	secondRecord := BatchRecord{
		SchemaVersion:           BatchSchemaVersion,
		BatchID:                 "lane_restart_keep_local",
		Status:                  BatchStatusPublishedStorageView,
		CompletedAt:             &secondTime,
		SelectedTransport:       TransportModeBundleSeed,
		LocalSafetyCleanupState: SafetyArtifactRemovedAfterSuccess,
	}
	if err := writeBatchRecord(statePath, secondRecord); err != nil {
		t.Fatal(err)
	}

	result, err := Housekeep(HousekeepingInput{RootPath: root, Now: func() time.Time { return secondTime }})
	if err != nil {
		t.Fatal(err)
	}
	if !equalStringSlices(result.RemovedCleanupQuarantineBatches, []string{first.BatchID}) || result.RecoveryStorage.RetainedBytes != 0 || result.RecoveryStorage.SuccessfulQuarantineCount != 0 {
		t.Fatalf("restart housekeeping did not elect quarantine-free latest success: %#v", result)
	}
	assertLaneFile(t, visiblePath, "visible survives")
	oldRecord, err := readBatchRecord(filepath.Join(statePath, "batches", first.BatchID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if oldRecord.LocalCleanupQuarantineRemovalReason != CleanupRemovalReasonNewerSuccess {
		t.Fatalf("restart retirement reason = %q", oldRecord.LocalCleanupQuarantineRemovalReason)
	}
}

func TestCurrentCompletedSuccessfulBatchRejectsNonSuccessfulRequestedRecord(t *testing.T) {
	firstTime := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	secondTime := firstTime.Add(5 * time.Minute)
	batches := []housekeepingBatch{
		{record: BatchRecord{BatchID: "lane_first_success", Status: BatchStatusLocalCleanupDone, CompletedAt: &firstTime}},
		{record: BatchRecord{BatchID: "lane_latest_success", Status: BatchStatusPublishedStorageView, CompletedAt: &secondTime}},
		{record: BatchRecord{BatchID: "lane_requested_active", Status: BatchStatusTransferring, CompletedAt: &secondTime}},
	}
	current, ok := currentCompletedSuccessfulBatch(batches, "lane_requested_active")
	if !ok || current.record.BatchID != "lane_latest_success" {
		t.Fatalf("non-successful requested batch was not rejected: %#v %t", current, ok)
	}
	current, ok = currentCompletedSuccessfulBatch(batches, "lane_first_success")
	if !ok || current.record.BatchID != "lane_first_success" {
		t.Fatalf("persisted successful requested batch was not selected: %#v %t", current, ok)
	}
}

func TestSuccessfulQuarantineExpiresAtGraceDeadlineAndHousekeepingIsIdempotent(t *testing.T) {
	root := t.TempDir()
	completed := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	result := successfulHousekeepingSend(t, root, "lane_success_expiry", TransportModeFileTree, completed, "grace payload")

	before, err := Housekeep(HousekeepingInput{RootPath: root, Now: func() time.Time { return completed.Add(SuccessfulCleanupQuarantineGrace - time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.RemovedCleanupQuarantineBatches) != 0 {
		t.Fatalf("quarantine expired before deadline: %#v", before)
	}
	assertLaneFile(t, filepath.Join(result.LocalCleanupQuarantinePath, "payload.txt"), "grace payload")

	atDeadline, err := Housekeep(HousekeepingInput{RootPath: root, Now: func() time.Time { return completed.Add(SuccessfulCleanupQuarantineGrace) }})
	if err != nil {
		t.Fatal(err)
	}
	if !equalStringSlices(atDeadline.RemovedCleanupQuarantineBatches, []string{result.BatchID}) || atDeadline.RecoveryStorage.RetainedBytes != 0 {
		t.Fatalf("deadline housekeeping = %#v", atDeadline)
	}
	if _, err := os.Lstat(result.LocalCleanupQuarantinePath); !os.IsNotExist(err) {
		t.Fatalf("expired quarantine still exists: %v", err)
	}

	restarted, err := Housekeep(HousekeepingInput{RootPath: root, Now: func() time.Time { return completed.Add(time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.RemovedCleanupQuarantineBatches) != 0 || restarted.RecoveryStorage.RetainedBytes != 0 {
		t.Fatalf("restart/idempotent housekeeping = %#v", restarted)
	}
}

func TestHousekeepingConvergesRemovalPendingRecordsAfterRestart(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, DefaultStateRelPath)
	completed := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	batchID := "lane_pending_restart"
	quarantinePath := filepath.Join(statePath, "cleanup", batchID)
	safetyPath := filepath.Join(statePath, "sent", batchID)
	mustLaneFile(t, filepath.Join(quarantinePath, "payload.txt"), "q")
	mustLaneFile(t, filepath.Join(safetyPath, "payload.txt"), "s")
	expires := completed.Add(SuccessfulCleanupQuarantineGrace)
	record := BatchRecord{
		SchemaVersion: BatchSchemaVersion, BatchID: batchID, Status: BatchStatusLocalCleanupDone,
		SelectedTransport: TransportModeFileTree, LocalSafetyPath: safetyPath, LocalSafetyCleanupState: SafetyArtifactRemovalPending,
		LocalCleanupQuarantinePath: quarantinePath, LocalCleanupQuarantineState: CleanupQuarantineRemovalPending,
		LocalCleanupQuarantineExpiresAt: &expires, LocalCleanupQuarantineRemovalReason: CleanupRemovalReasonGraceExpired,
		CompletedAt: &completed,
	}
	if err := writeBatchRecord(statePath, record); err != nil {
		t.Fatal(err)
	}
	result, err := Housekeep(HousekeepingInput{RootPath: root, Now: func() time.Time { return expires }})
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(result.RemovedSafetyArtifactBatches, batchID) || !containsString(result.RemovedCleanupQuarantineBatches, batchID) {
		t.Fatalf("pending removals did not converge: %#v", result)
	}
	final, err := readBatchRecord(filepath.Join(statePath, "batches", batchID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if final.LocalSafetyCleanupState != SafetyArtifactRemovedAfterSuccess || final.LocalCleanupQuarantineState != CleanupQuarantineRemovedAfterGrace {
		t.Fatalf("pending record did not converge: %#v", final)
	}
}

func TestHousekeepingPreservesProtectedEvidenceRegardlessOfAgeOrAttention(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, DefaultStateRelPath)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		status    string
		attention string
	}{
		{name: "failed", status: BatchStatusFailed, attention: AttentionStatusActive},
		{name: "interrupted", status: BatchStatusTransferring},
		{name: "active", status: BatchStatusBundleReady},
		{name: "withheld", status: BatchStatusLocalCleanupWithheld, attention: AttentionStatusActive},
		{name: "withheld_acknowledged", status: BatchStatusLocalCleanupWithheld, attention: AttentionStatusAcknowledged},
		{name: "withheld_archived", status: BatchStatusLocalCleanupWithheld, attention: AttentionStatusArchived},
	}
	for _, item := range cases {
		batchID := "lane_protected_" + item.name
		quarantinePath := filepath.Join(statePath, "cleanup", batchID)
		safetyPath := filepath.Join(statePath, "sent", batchID)
		mustLaneFile(t, filepath.Join(quarantinePath, "payload.txt"), item.name)
		mustLaneFile(t, filepath.Join(safetyPath, "payload.txt"), item.name)
		record := BatchRecord{
			SchemaVersion: BatchSchemaVersion, BatchID: batchID, Status: item.status, AttentionStatus: item.attention,
			SelectedTransport: TransportModeFileTree, LocalSafetyPath: safetyPath, LocalSafetyCleanupState: SafetyArtifactRetainedForRetry,
			LocalCleanupQuarantinePath: quarantinePath, LocalCleanupQuarantineState: CleanupQuarantineRetainedForRecovery,
			CompletedAt: &old,
		}
		if err := writeBatchRecord(statePath, record); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Housekeep(HousekeepingInput{RootPath: root, Now: func() time.Time { return old.Add(10 * 365 * 24 * time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RemovedSafetyArtifactBatches) != 0 || len(result.RemovedCleanupQuarantineBatches) != 0 {
		t.Fatalf("protected evidence was removed: %#v", result)
	}
	for _, item := range cases {
		batchID := "lane_protected_" + item.name
		assertLaneFile(t, filepath.Join(statePath, "cleanup", batchID, "payload.txt"), item.name)
		assertLaneFile(t, filepath.Join(statePath, "sent", batchID, "payload.txt"), item.name)
	}
	if result.RecoveryStorage.ProtectedEvidenceCount != len(cases)*2 || result.RecoveryStorage.ProtectedEvidenceBytes == 0 {
		t.Fatalf("protected accounting = %#v", result.RecoveryStorage)
	}
}

func TestRecoveryStorageStatusIsReadOnlyAndWarnsForMaterialProtectedEvidence(t *testing.T) {
	root := t.TempDir()
	lanePath := filepath.Join(root, DefaultLaneRelPath)
	if err := os.MkdirAll(lanePath, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, DefaultStateRelPath)
	batchID := "lane_status_protected"
	safetyPath := filepath.Join(statePath, "sent", batchID)
	mustLaneFile(t, filepath.Join(safetyPath, "payload.txt"), "protected")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := writeBatchRecord(statePath, BatchRecord{
		SchemaVersion: BatchSchemaVersion, BatchID: batchID, Status: BatchStatusFailed,
		SelectedTransport: TransportModeFileTree, LocalSafetyPath: safetyPath, LocalSafetyCleanupState: SafetyArtifactRetainedForRetry,
		CompletedAt: &old,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(safetyPath)
	if err != nil {
		t.Fatal(err)
	}
	status := BuildStatus(StatusInput{
		RootPath: root, RecoveryWarningBytes: 1,
		LookupPath:     func(name string) (string, error) { return "/usr/bin/" + name, nil },
		SSHConfigCheck: func(string) error { return nil },
	})
	if status.RecoveryStorage.ProtectedEvidenceBytes != int64(len("protected")) || !hasLaneDiagnostic(status.Diagnostics, "lane.recovery_storage_protected") {
		t.Fatalf("status recovery diagnostics = %#v %#v", status.RecoveryStorage, status.Diagnostics)
	}
	after, err := os.Stat(safetyPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("read-only status mutated protected storage: before=%#v after=%#v err=%v", before, after, err)
	}
}

func TestHousekeepingRejectsSymlinkedRecoveryParentsWithoutDeletingTarget(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, DefaultStateRelPath)
	outside := t.TempDir()
	batchID := "lane_symlink_guard"
	mustLaneFile(t, filepath.Join(outside, batchID, "payload.txt"), "must survive")
	if err := os.MkdirAll(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(statePath, "sent")); err != nil {
		t.Fatal(err)
	}
	completed := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	if err := writeBatchRecord(statePath, BatchRecord{
		SchemaVersion: BatchSchemaVersion, BatchID: batchID, Status: BatchStatusPublishedStorageView,
		SelectedTransport: TransportModeFileTree, LocalSafetyPath: filepath.Join(statePath, "sent", batchID),
		LocalSafetyCleanupState: SafetyArtifactRetainedForRetry, CompletedAt: &completed,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Housekeep(HousekeepingInput{RootPath: root, Now: func() time.Time { return completed }}); err == nil {
		t.Fatal("housekeeping accepted a symlinked recovery parent")
	}
	assertLaneFile(t, filepath.Join(outside, batchID, "payload.txt"), "must survive")
}

func successfulHousekeepingSend(t *testing.T, root, batchID string, mode TransportMode, now time.Time, payload string) SendResult {
	t.Helper()
	mustLaneFile(t, filepath.Join(root, DefaultLaneRelPath, "payload.txt"), payload)
	input := bundleSendTestInput(root, batchID, false)
	input.RequestedTransport = mode
	input.Now = func() time.Time { return now }
	result, err := Send(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BatchStatusLocalCleanupDone || result.LocalCleanupQuarantineExpiresAt == nil || !result.LocalCleanupQuarantineExpiresAt.Equal(now.Add(SuccessfulCleanupQuarantineGrace)) {
		t.Fatalf("successful Lane result = %#v", result)
	}
	return result
}
