package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/response"
	loomsync "loom.local/loom/internal/sync"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func TestQueueWatchedRootBackupActionStagesArtifactAndAvoidsDuplicates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "Project.md")
	content := []byte("# Project\n")
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	store := Store{DataDir: filepath.Join(dir, "data")}
	config := Config{NodeKey: "workspace-test"}
	state := State{NodeID: "node_test"}
	modified := time.Now().UTC()
	action := watchedroots.OutputAction{
		ActionKind:            watchedroots.OutputActionBackupFile,
		RootKey:               "notes",
		RelativePath:          "Project.md",
		ContentHashURI:        "sha256:" + rawBytesHashHex(content),
		SizeBytes:             int64(len(content)),
		ModifiedAt:            &modified,
		BackupMode:            watchedroots.BackupModeIncrementalRaw,
		BackupItemKind:        "file",
		BackupMaxFileBytes:    1024,
		BackupMaxBatchBytes:   1024,
		BackupMaxPendingItems: 10,
		BackupMaxPendingBytes: 1024,
		Fidelity: &filesystemmeta.Observation{
			Kind:       filesystemmeta.ObjectKindRegularFile,
			SourceMode: 0o600,
		},
	}

	batch, outbox, err := store.QueueWatchedRootBackupAction(config, state, action, sourcePath)
	if err != nil {
		t.Fatalf("QueueWatchedRootBackupAction failed: %v", err)
	}
	if batch.LocalBatchID == "" || outbox.LocalOutboxID == "" || batch.TotalBytes != int64(len(content)) {
		t.Fatalf("unexpected queued backup batch=%#v outbox=%#v", batch, outbox)
	}
	if batch.BatchKind != mainwatchedroots.BackupBatchKindWatchedRoot {
		t.Fatalf("batch kind = %q, want %q", batch.BatchKind, mainwatchedroots.BackupBatchKindWatchedRoot)
	}
	status, err := store.LocalWatchedRootBackupStatus(config, state, "notes")
	if err != nil {
		t.Fatalf("LocalWatchedRootBackupStatus failed: %v", err)
	}
	if status.Counts.Artifacts != 1 || status.Counts.Batches != 1 || status.Counts.Items != 1 || status.Counts.Pending != 1 || status.Counts.PendingBytes != int64(len(content)) {
		t.Fatalf("unexpected backup status counts %#v", status.Counts)
	}
	items, err := store.LoadWatchedRootBackupItems()
	if err != nil {
		t.Fatalf("LoadWatchedRootBackupItems failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one backup item, got %#v", items)
	}
	var itemMetadata map[string]any
	if err := json.Unmarshal(items[0].Metadata, &itemMetadata); err != nil {
		t.Fatalf("decode item metadata: %v", err)
	}
	if itemMetadata["filesystem_observation"] == nil {
		t.Fatalf("expected filesystem observation in item metadata %#v", itemMetadata)
	}
	input := buildWatchedRootBackupBatchInput(state, batch, items)
	if input.BatchKind != mainwatchedroots.BackupBatchKindWatchedRoot {
		t.Fatalf("outbound batch kind = %q, want %q", input.BatchKind, mainwatchedroots.BackupBatchKindWatchedRoot)
	}
	var inputMetadata map[string]any
	if err := json.Unmarshal(input.Items[0].Metadata, &inputMetadata); err != nil {
		t.Fatalf("decode input metadata: %v", err)
	}
	if inputMetadata["filesystem_observation"] == nil {
		t.Fatalf("expected filesystem observation in outbound item metadata %#v", inputMetadata)
	}
	artifacts, err := store.LoadWatchedRootBackupArtifacts()
	if err != nil {
		t.Fatalf("LoadWatchedRootBackupArtifacts failed: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected one artifact, got %#v", artifacts)
	}
	staged, err := os.ReadFile(artifacts[0].LocalContentPath)
	if err != nil {
		t.Fatalf("read staged artifact: %v", err)
	}
	if string(staged) != string(content) {
		t.Fatalf("staged artifact content mismatch: %q", string(staged))
	}

	secondBatch, secondOutbox, err := store.QueueWatchedRootBackupAction(config, state, action, sourcePath)
	if err != nil {
		t.Fatalf("second QueueWatchedRootBackupAction failed: %v", err)
	}
	if secondBatch.LocalBatchID != batch.LocalBatchID || secondOutbox.LocalOutboxID != outbox.LocalOutboxID {
		t.Fatalf("expected duplicate queue to reuse existing records, got batch=%#v outbox=%#v", secondBatch, secondOutbox)
	}
	status, err = store.LocalWatchedRootBackupStatus(config, state, "notes")
	if err != nil {
		t.Fatalf("second LocalWatchedRootBackupStatus failed: %v", err)
	}
	if status.Counts.Artifacts != 1 || status.Counts.Batches != 1 || status.Counts.Pending != 1 {
		t.Fatalf("expected no duplicate records, got %#v", status.Counts)
	}
}

func TestBuildWatchedRootPrivateBackupInputCarriesCanonicalBatchIdentity(t *testing.T) {
	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "artifact")
	if err := os.WriteFile(payloadPath, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	batch := LocalWatchedRootBackupBatch{LocalBatchID: "local_backup_batch_test", BackupMode: "incremental_raw"}
	item := LocalWatchedRootBackupItem{LocalItemID: "local_backup_item_test", RelativePath: "Project.md"}
	artifact := LocalWatchedRootBackupArtifact{
		LocalArtifactID:  "local_backup_artifact_test",
		RootKey:          "notes",
		RelativePath:     "Project.md",
		ContentHashURI:   "sha256:239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5",
		LocalContentPath: payloadPath,
	}
	input, err := buildWatchedRootPrivateBackupInput(State{NodeID: "node_test"}, batch, item, artifact)
	if err != nil {
		t.Fatalf("build input: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(input.Metadata, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata["local_batch_id"] != batch.LocalBatchID || metadata["root_key"] != artifact.RootKey {
		t.Fatalf("canonical custody identity missing: %#v", metadata)
	}
	second, err := buildWatchedRootPrivateBackupInput(State{NodeID: "node_test"}, batch, item, artifact)
	if err != nil {
		t.Fatalf("build identical retry input: %v", err)
	}
	if second.IdempotencyKey != input.IdempotencyKey {
		t.Fatalf("identical retry idempotency key changed: %q != %q", second.IdempotencyKey, input.IdempotencyKey)
	}
	renamedBatch := batch
	renamedBatch.LocalBatchID = "local_backup_batch_renamed"
	renamedItem := item
	renamedItem.LocalBatchID = renamedBatch.LocalBatchID
	renamedItem.LocalItemID = "local_backup_item_renamed"
	renamedItem.RelativePath = "Renamed Project.md"
	renamedArtifact := artifact
	renamedArtifact.LocalArtifactID = "local_backup_artifact_renamed"
	renamedArtifact.RelativePath = renamedItem.RelativePath
	renamed, err := buildWatchedRootPrivateBackupInput(State{NodeID: "node_test"}, renamedBatch, renamedItem, renamedArtifact)
	if err != nil {
		t.Fatalf("build renamed same-content input: %v", err)
	}
	if renamed.IdempotencyKey == input.IdempotencyKey {
		t.Fatalf("renamed same-content generation reused idempotency key %q", input.IdempotencyKey)
	}
}

func TestStageWatchedRootBackupArtifactReusesBytesButNotRemoteOperationIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "Project.md")
	content := []byte("same bytes in a later generation\n")
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	store := Store{DataDir: filepath.Join(dir, "data")}
	config := Config{}
	modified := time.Now().UTC()
	action := watchedroots.OutputAction{
		ActionKind:     watchedroots.OutputActionBackupFile,
		RootKey:        "notes",
		RelativePath:   "Project.md",
		ContentHashURI: "sha256:" + rawBytesHashHex(content),
		SizeBytes:      int64(len(content)),
		ModifiedAt:     &modified,
	}
	first, artifacts, err := store.stageWatchedRootBackupArtifact(config, nil, action, sourcePath, modified)
	if err != nil {
		t.Fatalf("stage first artifact: %v", err)
	}
	artifacts[0].PrivateBackupOperationID = "private_backup_old"
	artifacts[0].FileTransferID = "file_transfer_old"
	artifacts[0].Status = localBackupStatusAccepted
	second, artifacts, err := store.stageWatchedRootBackupArtifact(config, artifacts, action, sourcePath, modified.Add(time.Minute))
	if err != nil {
		t.Fatalf("stage later generation: %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifact records=%d want 2", len(artifacts))
	}
	if second.LocalArtifactID == first.LocalArtifactID {
		t.Fatalf("later generation reused remote-bound artifact identity %q", first.LocalArtifactID)
	}
	if second.LocalContentPath != first.LocalContentPath {
		t.Fatalf("same bytes should reuse staged content path: %q != %q", second.LocalContentPath, first.LocalContentPath)
	}
	if second.PrivateBackupOperationID != "" || second.FileTransferID != "" {
		t.Fatalf("later generation inherited remote operation identity: %#v", second)
	}
}

func TestPrivateBackupSourceIdentityUsesNamedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Protected Folder")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	path, key, err := privateBackupSourceIdentity(root)
	if err != nil {
		t.Fatalf("privateBackupSourceIdentity: %v", err)
	}
	if path != filepath.Clean(root) || key != "Protected Folder" {
		t.Fatalf("identity path=%q key=%q", path, key)
	}
}

func TestLocalWatchedRootBackupStatusIncludesProtectionCounts(t *testing.T) {
	dir := t.TempDir()
	store := Store{DataDir: filepath.Join(dir, "data")}
	watchedStore := watchedroots.NewStore(store.DataDir)
	if err := watchedStore.EnsureRoot("documents"); err != nil {
		t.Fatal(err)
	}
	if err := watchedStore.SaveLatestSummary("documents", watchedroots.RootSummary{
		SchemaVersion:     watchedroots.SummarySchemaVersion,
		RootKey:           "documents",
		Included:          7,
		Excluded:          3,
		PolicyVersion:     "loom-file-policy-v1",
		PolicyFingerprint: "sha256:test",
	}); err != nil {
		t.Fatal(err)
	}
	status, err := store.LocalWatchedRootBackupStatus(Config{NodeKey: "workspace"}, State{NodeID: "node_test"}, "documents")
	if err != nil {
		t.Fatal(err)
	}
	if status.Counts.Protected != 7 || status.Counts.Ignored != 3 || status.PolicyVersion != "loom-file-policy-v1" || status.PolicyFingerprint != "sha256:test" {
		t.Fatalf("unexpected protection status: %#v", status)
	}
}

func TestQueueWatchedRootBackupActionQueuesDirectoryMetadataWithoutArtifact(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := Store{DataDir: filepath.Join(dir, "data")}
	config := Config{NodeKey: "workspace-test"}
	state := State{NodeID: "node_test"}
	modified := time.Now().UTC()
	action := watchedroots.OutputAction{
		ActionKind:            watchedroots.OutputActionBackupMetadata,
		RootKey:               "documents",
		RelativePath:          "Empty",
		ContentHashURI:        "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes:             64,
		ModifiedAt:            &modified,
		BackupMode:            watchedroots.BackupModeIncrementalRaw,
		BackupItemKind:        mainwatchedroots.BackupItemKindDirectory,
		BackupMaxBatchBytes:   1024,
		BackupMaxPendingItems: 10,
		BackupMaxPendingBytes: 1024,
		Fidelity: &filesystemmeta.Observation{
			Kind:       filesystemmeta.ObjectKindDirectory,
			SourceMode: 0o755,
		},
	}

	batch, outbox, err := store.QueueWatchedRootBackupAction(config, state, action, "")
	if err != nil {
		t.Fatalf("QueueWatchedRootBackupAction failed: %v", err)
	}
	if batch.LocalBatchID == "" || outbox.LocalOutboxID == "" || batch.ArtifactCount != 0 || batch.TotalBytes != 0 {
		t.Fatalf("unexpected queued directory batch=%#v outbox=%#v", batch, outbox)
	}
	artifacts, err := store.LoadWatchedRootBackupArtifacts()
	if err != nil {
		t.Fatalf("LoadWatchedRootBackupArtifacts failed: %v", err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("directory metadata should not stage artifacts, got %#v", artifacts)
	}
	items, err := store.LoadWatchedRootBackupItems()
	if err != nil {
		t.Fatalf("LoadWatchedRootBackupItems failed: %v", err)
	}
	if len(items) != 1 || items[0].ItemKind != mainwatchedroots.BackupItemKindDirectory {
		t.Fatalf("expected one directory item, got %#v", items)
	}
	input := buildWatchedRootBackupBatchInput(state, batch, items)
	if len(input.Items) != 1 || input.Items[0].ItemKind != mainwatchedroots.BackupItemKindDirectory || input.Items[0].ArtifactKind != "" {
		t.Fatalf("unexpected outbound directory input %#v", input.Items)
	}
	if batch.BatchKind != mainwatchedroots.BackupBatchKindWatchedRoot || input.BatchKind != mainwatchedroots.BackupBatchKindWatchedRoot {
		t.Fatalf("directory batch kind was derived from item kind: local=%q outbound=%q", batch.BatchKind, input.BatchKind)
	}
}

func TestBuildWatchedRootBackupBatchInputCanonicalizesQueuedLegacyBatchKind(t *testing.T) {
	batch := LocalWatchedRootBackupBatch{
		LocalBatchID:  "local_backup_batch_legacy",
		RootKey:       "documents",
		BackupMode:    watchedroots.BackupModeIncrementalRaw,
		BatchKind:     mainwatchedroots.BackupItemKindDirectory,
		LocalSequence: 7,
	}
	items := []LocalWatchedRootBackupItem{{
		LocalItemID:  "local_backup_item_legacy",
		LocalBatchID: batch.LocalBatchID,
		RootKey:      batch.RootKey,
		ItemKind:     mainwatchedroots.BackupItemKindDirectory,
		BackupMode:   batch.BackupMode,
		RelativePath: "Empty",
		Status:       localBackupStatusPending,
	}}
	input := buildWatchedRootBackupBatchInput(State{NodeID: "node_test"}, batch, items)
	if input.BatchKind != mainwatchedroots.BackupBatchKindWatchedRoot {
		t.Fatalf("outbound batch kind = %q, want canonical kind", input.BatchKind)
	}
	if input.Items[0].ItemKind != mainwatchedroots.BackupItemKindDirectory {
		t.Fatalf("item kind changed unexpectedly: %#v", input.Items[0])
	}
}

func TestWatchedRootBackupQueueCompactionPreservesCounts(t *testing.T) {
	t.Parallel()
	store := Store{DataDir: filepath.Join(t.TempDir(), "data")}
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	if err := store.SaveWatchedRootBackupArtifacts([]LocalWatchedRootBackupArtifact{
		{LocalArtifactID: "artifact_pending", RootKey: "documents", RelativePath: "a.txt", Status: localBackupStatusPending, CreatedAt: now},
		{LocalArtifactID: "artifact_accepted", RootKey: "documents", RelativePath: "b.txt", Status: localBackupStatusAccepted, CreatedAt: now},
	}); err != nil {
		t.Fatalf("SaveWatchedRootBackupArtifacts failed: %v", err)
	}
	if err := store.SaveWatchedRootBackupBatches([]LocalWatchedRootBackupBatch{
		{LocalBatchID: "batch_pending", RootKey: "documents", Status: localBackupStatusPending, CreatedAt: now},
		{LocalBatchID: "batch_failed", RootKey: "documents", Status: localBackupStatusFailed, CreatedAt: now},
	}); err != nil {
		t.Fatalf("SaveWatchedRootBackupBatches failed: %v", err)
	}
	if err := store.SaveWatchedRootBackupItems([]LocalWatchedRootBackupItem{
		{LocalItemID: "item_accepted", LocalBatchID: "batch_pending", RootKey: "documents", RelativePath: "a.txt", Status: localBackupStatusAccepted, CreatedAt: now},
		{LocalItemID: "item_failed", LocalBatchID: "batch_failed", RootKey: "documents", RelativePath: "b.txt", Status: localBackupStatusFailed, CreatedAt: now},
	}); err != nil {
		t.Fatalf("SaveWatchedRootBackupItems failed: %v", err)
	}
	if err := store.SaveWatchedRootBackupOutbox([]LocalWatchedRootBackupOutboxItem{
		{LocalOutboxID: "outbox_pending", LocalRef: "batch_pending", RootKey: "documents", Status: localBackupStatusPending, CreatedAt: now},
		{LocalOutboxID: "outbox_accepted", LocalRef: "batch_accepted", RootKey: "documents", Status: localBackupStatusAccepted, CreatedAt: now},
		{LocalOutboxID: "outbox_failed", LocalRef: "batch_failed", RootKey: "documents", Status: localBackupStatusFailed, CreatedAt: now},
	}); err != nil {
		t.Fatalf("SaveWatchedRootBackupOutbox failed: %v", err)
	}
	compaction, err := store.CompactWatchedRootBackupQueues()
	if err != nil {
		t.Fatalf("CompactWatchedRootBackupQueues failed: %v", err)
	}
	if compaction.Counts.Artifacts.Total != 2 || compaction.Counts.Artifacts.Pending != 1 || compaction.Counts.Artifacts.Accepted != 1 {
		t.Fatalf("artifact counts not preserved: %#v", compaction.Counts.Artifacts)
	}
	if compaction.Counts.Outbox.Total != 3 || compaction.Counts.Outbox.Pending != 1 || compaction.Counts.Outbox.Accepted != 1 || compaction.Counts.Outbox.Failed != 1 {
		t.Fatalf("outbox counts not preserved: %#v", compaction.Counts.Outbox)
	}
	outbox, err := store.LoadWatchedRootBackupOutbox()
	if err != nil {
		t.Fatalf("LoadWatchedRootBackupOutbox failed: %v", err)
	}
	if len(outbox) != 3 {
		t.Fatalf("outbox count after compaction = %d", len(outbox))
	}
	events, err := os.ReadFile(store.watchedRootBackupQueueEventsPath())
	if err != nil {
		t.Fatalf("read queue events: %v", err)
	}
	if !strings.Contains(string(events), `"event":"compacted"`) {
		t.Fatalf("queue events did not contain compaction marker:\n%s", string(events))
	}
}

func TestQueueWatchedRootBackupActionCompactsOversizedEventLogAfterUnlock(t *testing.T) {
	dir := t.TempDir()
	store := Store{DataDir: filepath.Join(dir, "data")}
	if err := store.EnsureWatchedRootBackupDataDirs(); err != nil {
		t.Fatalf("EnsureWatchedRootBackupDataDirs failed: %v", err)
	}
	if err := os.WriteFile(store.watchedRootBackupQueueEventsPath(), bytes.Repeat([]byte("x"), localBackupQueueEventsCompactBytes), 0o600); err != nil {
		t.Fatalf("seed oversized queue event log: %v", err)
	}

	sourcePath := filepath.Join(dir, "queued.txt")
	content := []byte("queued\n")
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	modified := time.Now().UTC()
	action := watchedroots.OutputAction{
		ActionKind:            watchedroots.OutputActionBackupFile,
		RootKey:               "documents",
		RelativePath:          "queued.txt",
		ContentHashURI:        "sha256:" + rawBytesHashHex(content),
		SizeBytes:             int64(len(content)),
		ModifiedAt:            &modified,
		BackupMode:            watchedroots.BackupModeIncrementalRaw,
		BackupItemKind:        "file",
		BackupMaxFileBytes:    1024,
		BackupMaxBatchBytes:   1024,
		BackupMaxPendingItems: 10,
		BackupMaxPendingBytes: 1024,
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := store.QueueWatchedRootBackupAction(
			Config{NodeKey: "workspace-test"},
			State{NodeID: "node_test"},
			action,
			sourcePath,
		)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("QueueWatchedRootBackupAction failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("QueueWatchedRootBackupAction deadlocked while compacting queue events")
	}

	events, err := os.ReadFile(store.watchedRootBackupQueueEventsPath())
	if err != nil {
		t.Fatalf("read compacted queue events: %v", err)
	}
	if !strings.Contains(string(events), `"event":"compacted"`) {
		t.Fatalf("queue events were not compacted after releasing the queue lock: %q", string(events))
	}
}

func TestQueueWatchedRootBackupActionEnforcesPendingLimits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "Project.md")
	content := []byte("# Project\n")
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	store := Store{DataDir: filepath.Join(dir, "data")}
	config := Config{NodeKey: "workspace-test"}
	state := State{NodeID: "node_test"}
	action := watchedroots.OutputAction{
		ActionKind:            watchedroots.OutputActionBackupFile,
		RootKey:               "notes",
		RelativePath:          "Project.md",
		ContentHashURI:        "sha256:" + rawBytesHashHex(content),
		SizeBytes:             int64(len(content)),
		BackupMode:            watchedroots.BackupModeIncrementalRaw,
		BackupItemKind:        "file",
		BackupMaxFileBytes:    1024,
		BackupMaxBatchBytes:   1024,
		BackupMaxPendingItems: 10,
		BackupMaxPendingBytes: int64(len(content) - 1),
	}
	_, _, err := store.QueueWatchedRootBackupAction(config, state, action, sourcePath)
	if err == nil {
		t.Fatal("expected pending byte limit error")
	}
	if watchedRootBackupErrorCode(err) != watchedroots.FindingBackupQueueLimit {
		t.Fatalf("unexpected backup queue error code %q from %v", watchedRootBackupErrorCode(err), err)
	}
}

func TestFlushWatchedRootBackupsUploadsArtifactAndRecordsBatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "Project.md")
	content := []byte("# Project\n")
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	store := Store{DataDir: filepath.Join(dir, "data")}
	config := Config{NodeKey: "workspace-test"}
	state := State{NodeID: "node_test", CredentialToken: "node_cred_secret"}
	hashURI := "sha256:" + rawBytesHashHex(content)
	now := time.Now().UTC()
	watchedStore := watchedroots.NewStore(store.DataDir)
	if err := watchedStore.SavePathState(watchedroots.PathState{
		SchemaVersion:  watchedroots.PathStateSchemaVersion,
		RootKey:        "notes",
		PathKey:        watchedroots.PathKey("notes", "Project.md"),
		RelativePath:   "Project.md",
		Status:         watchedroots.PathStatusIncluded,
		Kind:           watchedroots.PathKindFile,
		ContentHashURI: hashURI,
		FirstSeenAt:    now,
		LastSeenAt:     now,
		LastScannedAt:  now,
	}); err != nil {
		t.Fatalf("save path state: %v", err)
	}
	action := watchedroots.OutputAction{
		ActionKind:         watchedroots.OutputActionBackupFile,
		RootKey:            "notes",
		RelativePath:       "Project.md",
		ContentHashURI:     hashURI,
		SizeBytes:          int64(len(content)),
		ModifiedAt:         &now,
		BackupMode:         watchedroots.BackupModeIncrementalRaw,
		BackupItemKind:     mainwatchedroots.BackupItemKindFile,
		BackupMaxFileBytes: 1024,
	}
	batch, _, err := store.QueueWatchedRootBackupAction(config, state, action, sourcePath)
	if err != nil {
		t.Fatalf("QueueWatchedRootBackupAction failed: %v", err)
	}

	var sawPrivateBackup bool
	var sawBackupBatch bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/node-agent/sync/private-backup":
			sawPrivateBackup = true
			var input loomsync.PrivateBackupInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode private backup input: %v", err)
			}
			if input.NodeRef != state.NodeID || input.CredentialToken != state.CredentialToken || input.CoarseSizeBytes <= 0 {
				t.Fatalf("unexpected private backup input %#v", input)
			}
			_ = json.NewEncoder(w).Encode(response.Success("corr_test", loomsync.PrivateBackupResult{
				Operation: loomsync.PrivateBackupOperation{
					PrivateBackupOperationID: "private_backup_test",
					OriginNodeID:             state.NodeID,
					Status:                   "stored",
					CoarseSizeBytes:          input.CoarseSizeBytes,
				},
			}))
		case "/v1/node-agent/watched-roots/backup-batches":
			sawBackupBatch = true
			var input mainwatchedroots.BackupBatchInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode backup batch input: %v", err)
			}
			if input.RootKey != "notes" || input.IdempotencyKey == "" || len(input.Items) != 1 {
				t.Fatalf("unexpected backup batch input %#v", input)
			}
			if input.Items[0].PrivateBackupOperationID != "private_backup_test" {
				t.Fatalf("private backup operation not linked: %#v", input.Items[0])
			}
			_ = json.NewEncoder(w).Encode(response.Success("corr_test", mainwatchedroots.BackupBatchResult{
				Batch: mainwatchedroots.BackupBatch{
					WatchedRootBackupBatchID: "watched_root_backup_batch_test",
					Status:                   mainwatchedroots.BackupBatchStatusAccepted,
				},
				Items: []mainwatchedroots.BackupItem{{
					WatchedRootBackupItemID:  "watched_root_backup_item_test",
					LocalItemRef:             input.Items[0].LocalItemRef,
					Status:                   mainwatchedroots.BackupItemStatusAccepted,
					PrivateBackupOperationID: stringPtrForTest("private_backup_test"),
				}},
			}))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	config.MainURL = server.URL

	flush := flushWatchedRootBackups(context.Background(), store, config, state, "corr_test")
	if flush.Status != watchedroots.OutputStatusRecorded || flush.SubmittedItems != 1 {
		t.Fatalf("unexpected backup flush %#v", flush)
	}
	if !sawPrivateBackup || !sawBackupBatch {
		t.Fatalf("expected both private backup and backup batch endpoints private=%t batch=%t", sawPrivateBackup, sawBackupBatch)
	}
	status, err := store.LocalWatchedRootBackupStatus(config, state, "notes")
	if err != nil {
		t.Fatalf("LocalWatchedRootBackupStatus failed: %v", err)
	}
	if status.Counts.Pending != 0 || status.Counts.Accepted != 1 {
		t.Fatalf("unexpected local backup status %#v", status.Counts)
	}
	pathState, err := watchedStore.LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState failed: %v", err)
	}
	if pathState.LastBackedUpHashURI != hashURI ||
		pathState.MainBackupBatchID != "watched_root_backup_batch_test" ||
		pathState.MainBackupItemID != "watched_root_backup_item_test" ||
		pathState.PrivateBackupOperationID != "private_backup_test" {
		t.Fatalf("path state was not updated after backup: %#v", pathState)
	}
	if batch.LocalBatchID == "" {
		t.Fatal("expected queued batch id")
	}
}

func TestFlushWatchedRootBackupsRecoversLegacyDirectOperationBinding(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "renamed.txt")
	content := []byte("same bytes under a new batch identity\n")
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	store := Store{DataDir: filepath.Join(dir, "data")}
	config := Config{NodeKey: "workspace-test"}
	state := State{NodeID: "node_test", CredentialToken: "node_cred_secret"}
	now := time.Now().UTC()
	action := watchedroots.OutputAction{
		ActionKind:         watchedroots.OutputActionBackupFile,
		RootKey:            "notes",
		RelativePath:       "renamed.txt",
		ContentHashURI:     "sha256:" + rawBytesHashHex(content),
		SizeBytes:          int64(len(content)),
		ModifiedAt:         &now,
		BackupMode:         watchedroots.BackupModeIncrementalRaw,
		BackupItemKind:     mainwatchedroots.BackupItemKindFile,
		BackupMaxFileBytes: 1024,
	}
	if _, _, err := store.QueueWatchedRootBackupAction(config, state, action, sourcePath); err != nil {
		t.Fatalf("queue backup: %v", err)
	}
	artifacts, _ := store.LoadWatchedRootBackupArtifacts()
	items, _ := store.LoadWatchedRootBackupItems()
	artifacts[0].PrivateBackupOperationID = "private_backup_legacy_replay"
	artifacts[0].Status = localBackupStatusAccepted
	items[0].PrivateBackupOperationID = artifacts[0].PrivateBackupOperationID
	if err := store.SaveWatchedRootBackupArtifacts(artifacts); err != nil {
		t.Fatalf("seed artifact binding: %v", err)
	}
	if err := store.SaveWatchedRootBackupItems(items); err != nil {
		t.Fatalf("seed item binding: %v", err)
	}

	privateUploads := 0
	batchAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/node-agent/sync/private-backup":
			privateUploads++
			var input loomsync.PrivateBackupInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode private backup: %v", err)
			}
			_ = json.NewEncoder(w).Encode(response.Success("corr_test", loomsync.PrivateBackupResult{Operation: loomsync.PrivateBackupOperation{PrivateBackupOperationID: "private_backup_batch_scoped", OriginNodeID: state.NodeID, Status: "stored", CoarseSizeBytes: input.CoarseSizeBytes}}))
		case "/v1/node-agent/watched-roots/backup-batches":
			batchAttempts++
			var input mainwatchedroots.BackupBatchInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode backup batch: %v", err)
			}
			if batchAttempts == 1 {
				if input.Items[0].PrivateBackupOperationID != "private_backup_legacy_replay" {
					t.Fatalf("first attempt did not use seeded legacy binding: %#v", input.Items[0])
				}
				http.Error(w, "identity mismatch", http.StatusBadRequest)
				return
			}
			if input.Items[0].PrivateBackupOperationID != "private_backup_batch_scoped" {
				t.Fatalf("retry did not use batch-scoped operation: %#v", input.Items[0])
			}
			_ = json.NewEncoder(w).Encode(response.Success("corr_test", mainwatchedroots.BackupBatchResult{Batch: mainwatchedroots.BackupBatch{WatchedRootBackupBatchID: "watched_root_backup_batch_recovered", Status: mainwatchedroots.BackupBatchStatusAccepted}, Items: []mainwatchedroots.BackupItem{{WatchedRootBackupItemID: "watched_root_backup_item_recovered", LocalItemRef: input.Items[0].LocalItemRef, Status: mainwatchedroots.BackupItemStatusAccepted, PrivateBackupOperationID: stringPtrForTest("private_backup_batch_scoped")}}}))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	config.MainURL = server.URL

	first := flushWatchedRootBackups(context.Background(), store, config, state, "corr_test")
	if first.Status != watchedroots.OutputStatusFailed || privateUploads != 0 || batchAttempts != 1 {
		t.Fatalf("legacy binding attempt=%#v private=%d batches=%d", first, privateUploads, batchAttempts)
	}
	artifacts, _ = store.LoadWatchedRootBackupArtifacts()
	items, _ = store.LoadWatchedRootBackupItems()
	if artifacts[0].PrivateBackupOperationID != "" || items[0].PrivateBackupOperationID != "" {
		t.Fatalf("failed batch did not clear local direct binding: artifact=%#v item=%#v", artifacts[0], items[0])
	}
	second := flushWatchedRootBackups(context.Background(), store, config, state, "corr_test")
	if second.Status != watchedroots.OutputStatusRecorded || second.PendingAfter != 0 || privateUploads != 1 || batchAttempts != 2 {
		t.Fatalf("batch-scoped recovery=%#v private=%d batches=%d", second, privateUploads, batchAttempts)
	}
}

func TestFlushWatchedRootBackupsUploadsLargeFileViaTransfer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "large.pdf")
	content := bytes.Repeat([]byte("x"), loomsync.MaxPrivateBackupBytes+1)
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	store := Store{DataDir: filepath.Join(dir, "data")}
	config := Config{
		NodeKey: "workspace-test",
		BackupTransport: BackupTransportConfig{
			ChunkSizeBytes:  256 * 1024,
			StabilityWindow: "0s",
		},
	}
	state := State{NodeID: "node_test", CredentialToken: "node_cred_secret"}
	hashURI := "sha256:" + rawBytesHashHex(content)
	now := time.Now().UTC()
	watchedStore := watchedroots.NewStore(store.DataDir)
	if err := watchedStore.SavePathState(watchedroots.PathState{
		SchemaVersion:  watchedroots.PathStateSchemaVersion,
		RootKey:        "documents",
		PathKey:        watchedroots.PathKey("documents", "large.pdf"),
		RelativePath:   "large.pdf",
		Status:         watchedroots.PathStatusIncluded,
		Kind:           watchedroots.PathKindFile,
		ContentHashURI: hashURI,
		FirstSeenAt:    now,
		LastSeenAt:     now,
		LastScannedAt:  now,
	}); err != nil {
		t.Fatalf("save path state: %v", err)
	}
	action := watchedroots.OutputAction{
		ActionKind:            watchedroots.OutputActionBackupFile,
		RootKey:               "documents",
		RelativePath:          "large.pdf",
		ContentHashURI:        hashURI,
		SizeBytes:             int64(len(content)),
		ModifiedAt:            &now,
		BackupMode:            watchedroots.BackupModeIncrementalRaw,
		BackupItemKind:        mainwatchedroots.BackupItemKindFile,
		BackupMaxFileBytes:    int64(len(content)) + 1024,
		BackupMaxBatchBytes:   int64(len(content)) + 1024,
		BackupMaxPendingItems: 10,
		BackupMaxPendingBytes: int64(len(content)) + 1024,
	}
	if _, _, err := store.QueueWatchedRootBackupAction(config, state, action, sourcePath); err != nil {
		t.Fatalf("QueueWatchedRootBackupAction failed: %v", err)
	}
	fakeTransfers := newFakeWatchedRootTransferServer(t, state, "documents")
	defer fakeTransfers.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fakeTransfers.Handle(w, r) {
			return
		}
		switch r.URL.Path {
		case "/v1/node-agent/watched-roots/backup-batches":
			var input mainwatchedroots.BackupBatchInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode backup batch input: %v", err)
			}
			if len(input.Items) != 1 || input.Items[0].ArtifactKind != mainwatchedroots.BackupArtifactKindFileTransfer || input.Items[0].ArtifactRef == "" {
				t.Fatalf("large backup item was not linked to a file transfer: %#v", input)
			}
			_ = json.NewEncoder(w).Encode(response.Success("corr_test", mainwatchedroots.BackupBatchResult{
				Batch: mainwatchedroots.BackupBatch{
					WatchedRootBackupBatchID: "watched_root_backup_batch_large",
					Status:                   mainwatchedroots.BackupBatchStatusAccepted,
				},
				Items: []mainwatchedroots.BackupItem{{
					WatchedRootBackupItemID: "watched_root_backup_item_large",
					LocalItemRef:            input.Items[0].LocalItemRef,
					Status:                  mainwatchedroots.BackupItemStatusAccepted,
					ArtifactKind:            input.Items[0].ArtifactKind,
					ArtifactRef:             input.Items[0].ArtifactRef,
				}},
			}))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	config.MainURL = server.URL

	flush := flushWatchedRootBackups(context.Background(), store, config, state, "corr_test")
	if flush.Status != watchedroots.OutputStatusRecorded || flush.PendingAfter != 0 || flush.AcceptedAfter != 1 {
		t.Fatalf("unexpected large transfer flush %#v", flush)
	}
	status, err := store.LocalWatchedRootBackupStatus(config, state, "documents")
	if err != nil {
		t.Fatalf("LocalWatchedRootBackupStatus failed: %v", err)
	}
	if status.Counts.Pending != 0 || status.Counts.Accepted != 1 || status.Counts.Failed != 0 {
		t.Fatalf("large transfer item should be accepted: %#v", status.Counts)
	}
	pathState, err := watchedStore.LoadPathState("documents", "large.pdf")
	if err != nil {
		t.Fatalf("LoadPathState failed: %v", err)
	}
	if pathState.BackupStatus != watchedroots.OutputStatusRecorded || pathState.FileTransferID == "" || pathState.LastBackedUpHashURI != hashURI {
		t.Fatalf("large transfer path state should be recorded with transfer id, got %#v", pathState)
	}
}

func TestFlushWatchedRootBackupsDoesNotBlockSmallFileAfterPermanentFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := Store{DataDir: filepath.Join(dir, "data")}
	config := Config{
		NodeKey: "workspace-test",
		BackupTransport: BackupTransportConfig{
			ChunkSizeBytes:  256 * 1024,
			StabilityWindow: "0s",
		},
	}
	state := State{NodeID: "node_test", CredentialToken: "node_cred_secret"}
	watchedStore := watchedroots.NewStore(store.DataDir)
	now := time.Now().UTC()

	largePath := filepath.Join(dir, "large.pdf")
	largeContent := bytes.Repeat([]byte("x"), loomsync.MaxPrivateBackupBytes+1)
	if err := os.WriteFile(largePath, largeContent, 0o600); err != nil {
		t.Fatalf("write large source: %v", err)
	}
	largeHash := "sha256:" + rawBytesHashHex(largeContent)
	if err := watchedStore.SavePathState(watchedroots.PathState{
		SchemaVersion:  watchedroots.PathStateSchemaVersion,
		RootKey:        "documents",
		PathKey:        watchedroots.PathKey("documents", "large.pdf"),
		RelativePath:   "large.pdf",
		Status:         watchedroots.PathStatusIncluded,
		Kind:           watchedroots.PathKindFile,
		ContentHashURI: largeHash,
		FirstSeenAt:    now,
		LastSeenAt:     now,
		LastScannedAt:  now,
	}); err != nil {
		t.Fatalf("save large path state: %v", err)
	}
	largeAction := watchedroots.OutputAction{
		ActionKind:            watchedroots.OutputActionBackupFile,
		RootKey:               "documents",
		RelativePath:          "large.pdf",
		ContentHashURI:        largeHash,
		SizeBytes:             int64(len(largeContent)),
		ModifiedAt:            &now,
		BackupMode:            watchedroots.BackupModeIncrementalRaw,
		BackupItemKind:        mainwatchedroots.BackupItemKindFile,
		BackupMaxFileBytes:    int64(len(largeContent)) + 1024,
		BackupMaxBatchBytes:   int64(len(largeContent)) + 1024,
		BackupMaxPendingItems: 10,
		BackupMaxPendingBytes: int64(len(largeContent)) + 1024 + 1024,
	}
	if _, _, err := store.QueueWatchedRootBackupAction(config, state, largeAction, largePath); err != nil {
		t.Fatalf("queue large backup: %v", err)
	}

	smallPath := filepath.Join(dir, "small.txt")
	smallContent := []byte("small file\n")
	if err := os.WriteFile(smallPath, smallContent, 0o600); err != nil {
		t.Fatalf("write small source: %v", err)
	}
	smallHash := "sha256:" + rawBytesHashHex(smallContent)
	if err := watchedStore.SavePathState(watchedroots.PathState{
		SchemaVersion:  watchedroots.PathStateSchemaVersion,
		RootKey:        "documents",
		PathKey:        watchedroots.PathKey("documents", "small.txt"),
		RelativePath:   "small.txt",
		Status:         watchedroots.PathStatusIncluded,
		Kind:           watchedroots.PathKindFile,
		ContentHashURI: smallHash,
		FirstSeenAt:    now,
		LastSeenAt:     now,
		LastScannedAt:  now,
	}); err != nil {
		t.Fatalf("save small path state: %v", err)
	}
	smallAction := watchedroots.OutputAction{
		ActionKind:            watchedroots.OutputActionBackupFile,
		RootKey:               "documents",
		RelativePath:          "small.txt",
		ContentHashURI:        smallHash,
		SizeBytes:             int64(len(smallContent)),
		ModifiedAt:            &now,
		BackupMode:            watchedroots.BackupModeIncrementalRaw,
		BackupItemKind:        mainwatchedroots.BackupItemKindFile,
		BackupMaxFileBytes:    1024,
		BackupMaxBatchBytes:   1024,
		BackupMaxPendingItems: 10,
		BackupMaxPendingBytes: int64(len(largeContent)) + 1024 + 1024,
	}
	if _, _, err := store.QueueWatchedRootBackupAction(config, state, smallAction, smallPath); err != nil {
		t.Fatalf("queue small backup: %v", err)
	}

	var sawPrivateBackup bool
	backupBatchCount := 0
	fakeTransfers := newFakeWatchedRootTransferServer(t, state, "documents")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fakeTransfers.Handle(w, r) {
			return
		}
		switch r.URL.Path {
		case "/v1/node-agent/sync/private-backup":
			sawPrivateBackup = true
			var input loomsync.PrivateBackupInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode private backup input: %v", err)
			}
			if input.CoarseSizeBytes <= 0 || input.CoarseSizeBytes > loomsync.MaxPrivateBackupBytes {
				t.Fatalf("unexpected private backup input %#v", input)
			}
			_ = json.NewEncoder(w).Encode(response.Success("corr_test", loomsync.PrivateBackupResult{
				Operation: loomsync.PrivateBackupOperation{
					PrivateBackupOperationID: "private_backup_small",
					OriginNodeID:             state.NodeID,
					Status:                   "stored",
					CoarseSizeBytes:          input.CoarseSizeBytes,
				},
			}))
		case "/v1/node-agent/watched-roots/backup-batches":
			backupBatchCount++
			var input mainwatchedroots.BackupBatchInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode backup batch input: %v", err)
			}
			if len(input.Items) != 1 {
				t.Fatalf("unexpected backup batch input item count %#v", input)
			}
			item := input.Items[0]
			resultItem := mainwatchedroots.BackupItem{
				WatchedRootBackupItemID: "watched_root_backup_item_" + strings.TrimSuffix(item.RelativePath, filepath.Ext(item.RelativePath)),
				LocalItemRef:            item.LocalItemRef,
				Status:                  mainwatchedroots.BackupItemStatusAccepted,
				ArtifactKind:            item.ArtifactKind,
				ArtifactRef:             item.ArtifactRef,
			}
			switch item.RelativePath {
			case "large.pdf":
				if item.ArtifactKind != mainwatchedroots.BackupArtifactKindFileTransfer || item.ArtifactRef == "" {
					t.Fatalf("large item should use file transfer: %#v", item)
				}
			case "small.txt":
				if item.PrivateBackupOperationID != "private_backup_small" {
					t.Fatalf("small item should use private backup: %#v", item)
				}
				resultItem.PrivateBackupOperationID = stringPtrForTest("private_backup_small")
			default:
				t.Fatalf("unexpected backup path %q", item.RelativePath)
			}
			_ = json.NewEncoder(w).Encode(response.Success("corr_test", mainwatchedroots.BackupBatchResult{
				Batch: mainwatchedroots.BackupBatch{
					WatchedRootBackupBatchID: "watched_root_backup_batch_" + strings.TrimSuffix(item.RelativePath, filepath.Ext(item.RelativePath)),
					Status:                   mainwatchedroots.BackupBatchStatusAccepted,
				},
				Items: []mainwatchedroots.BackupItem{resultItem},
			}))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	config.MainURL = server.URL

	flush := flushWatchedRootBackups(context.Background(), store, config, state, "corr_test")
	if flush.Status != watchedroots.OutputStatusRecorded || flush.PendingAfter != 0 || flush.AcceptedAfter != 2 || flush.FailedAfter != 0 || flush.ManualAfter != 0 {
		t.Fatalf("unexpected mixed backup flush %#v", flush)
	}
	if flush.SubmittedItems != 2 || !sawPrivateBackup || backupBatchCount != 2 || fakeTransfers.CreatedCount() != 1 {
		t.Fatalf("expected large transfer and small private submissions, flush=%#v private=%t batches=%d transfers=%d", flush, sawPrivateBackup, backupBatchCount, fakeTransfers.CreatedCount())
	}
	status, err := store.LocalWatchedRootBackupStatus(config, state, "documents")
	if err != nil {
		t.Fatalf("LocalWatchedRootBackupStatus failed: %v", err)
	}
	if status.Counts.Pending != 0 || status.Counts.Accepted != 2 || status.Counts.Failed != 0 || status.Counts.ManualAction != 0 {
		t.Fatalf("unexpected mixed local backup status %#v", status.Counts)
	}
	largeState, err := watchedStore.LoadPathState("documents", "large.pdf")
	if err != nil {
		t.Fatalf("LoadPathState large failed: %v", err)
	}
	if largeState.BackupStatus != watchedroots.OutputStatusRecorded || largeState.LastBackedUpHashURI != largeHash || largeState.FileTransferID == "" {
		t.Fatalf("expected large recorded path state, got %#v", largeState)
	}
	smallState, err := watchedStore.LoadPathState("documents", "small.txt")
	if err != nil {
		t.Fatalf("LoadPathState small failed: %v", err)
	}
	if smallState.BackupStatus != watchedroots.OutputStatusRecorded ||
		smallState.LastBackedUpHashURI != smallHash ||
		smallState.PrivateBackupOperationID != "private_backup_small" {
		t.Fatalf("expected small recorded path state, got %#v", smallState)
	}
}

func TestFlushWatchedRootBackupsResumesInterruptedFileTransfer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "video.mov")
	content := bytes.Repeat([]byte("video"), 300*1024)
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	store := Store{DataDir: filepath.Join(dir, "data")}
	config := Config{
		NodeKey: "workspace-test",
		BackupTransport: BackupTransportConfig{
			DirectMaxBytes:  1024,
			ChunkSizeBytes:  256 * 1024,
			StabilityWindow: "0s",
		},
	}
	state := State{NodeID: "node_test", CredentialToken: "node_cred_secret"}
	hashURI := "sha256:" + rawBytesHashHex(content)
	now := time.Now().UTC()
	watchedStore := watchedroots.NewStore(store.DataDir)
	if err := watchedStore.SavePathState(watchedroots.PathState{
		SchemaVersion:  watchedroots.PathStateSchemaVersion,
		RootKey:        "documents",
		PathKey:        watchedroots.PathKey("documents", "video.mov"),
		RelativePath:   "video.mov",
		Status:         watchedroots.PathStatusIncluded,
		Kind:           watchedroots.PathKindFile,
		ContentHashURI: hashURI,
		FirstSeenAt:    now,
		LastSeenAt:     now,
		LastScannedAt:  now,
	}); err != nil {
		t.Fatalf("save path state: %v", err)
	}
	action := watchedroots.OutputAction{
		ActionKind:            watchedroots.OutputActionBackupFile,
		RootKey:               "documents",
		RelativePath:          "video.mov",
		ContentHashURI:        hashURI,
		SizeBytes:             int64(len(content)),
		ModifiedAt:            &now,
		BackupMode:            watchedroots.BackupModeIncrementalRaw,
		BackupItemKind:        mainwatchedroots.BackupItemKindFile,
		BackupMaxFileBytes:    int64(len(content)) + 1024,
		BackupMaxBatchBytes:   int64(len(content)) + 1024,
		BackupMaxPendingItems: 10,
		BackupMaxPendingBytes: int64(len(content)) + 1024,
	}
	if _, _, err := store.QueueWatchedRootBackupAction(config, state, action, sourcePath); err != nil {
		t.Fatalf("QueueWatchedRootBackupAction failed: %v", err)
	}

	fakeTransfers := newFakeWatchedRootTransferServer(t, state, "documents")
	fakeTransfers.FailNextChunkUpload()
	backupBatchCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fakeTransfers.Handle(w, r) {
			return
		}
		if r.URL.Path != "/v1/node-agent/watched-roots/backup-batches" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		backupBatchCount++
		var input mainwatchedroots.BackupBatchInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode backup batch input: %v", err)
		}
		if len(input.Items) != 1 || input.Items[0].ArtifactKind != mainwatchedroots.BackupArtifactKindFileTransfer || input.Items[0].ArtifactRef == "" {
			t.Fatalf("expected file-transfer backup batch input, got %#v", input)
		}
		_ = json.NewEncoder(w).Encode(response.Success("corr_test", mainwatchedroots.BackupBatchResult{
			Batch: mainwatchedroots.BackupBatch{
				WatchedRootBackupBatchID: "watched_root_backup_batch_resume",
				Status:                   mainwatchedroots.BackupBatchStatusAccepted,
			},
			Items: []mainwatchedroots.BackupItem{{
				WatchedRootBackupItemID: "watched_root_backup_item_resume",
				LocalItemRef:            input.Items[0].LocalItemRef,
				Status:                  mainwatchedroots.BackupItemStatusAccepted,
				ArtifactKind:            input.Items[0].ArtifactKind,
				ArtifactRef:             input.Items[0].ArtifactRef,
			}},
		}))
	}))
	defer server.Close()
	config.MainURL = server.URL

	first := flushWatchedRootBackups(context.Background(), store, config, state, "corr_test")
	if first.Status != watchedroots.OutputStatusFailed || first.PendingAfter != 1 || first.RetryableAfter != 1 || first.AcceptedAfter != 0 {
		t.Fatalf("first interrupted flush should remain retryable pending, got %#v", first)
	}
	if fakeTransfers.CreatedCount() != 1 || backupBatchCount != 0 {
		t.Fatalf("interrupted transfer should create once and not submit batch, transfers=%d batches=%d", fakeTransfers.CreatedCount(), backupBatchCount)
	}

	second := flushWatchedRootBackups(context.Background(), store, config, state, "corr_test")
	if second.Status != watchedroots.OutputStatusRecorded || second.PendingAfter != 0 || second.AcceptedAfter != 1 || second.SubmittedItems != 1 {
		t.Fatalf("second flush should resume and record, got %#v", second)
	}
	if fakeTransfers.CreatedCount() != 1 || backupBatchCount != 1 {
		t.Fatalf("resume should reuse existing transfer and submit one batch, transfers=%d batches=%d", fakeTransfers.CreatedCount(), backupBatchCount)
	}
	pathState, err := watchedStore.LoadPathState("documents", "video.mov")
	if err != nil {
		t.Fatalf("LoadPathState failed: %v", err)
	}
	if pathState.BackupStatus != watchedroots.OutputStatusRecorded || pathState.FileTransferID == "" || pathState.LastBackedUpHashURI != hashURI {
		t.Fatalf("resumed transfer path state was not recorded: %#v", pathState)
	}
}

func TestFlushWatchedRootBackupsKeepsTransientUploadErrorRetryable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "retry.txt")
	content := []byte("retry later\n")
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	store := Store{DataDir: filepath.Join(dir, "data")}
	config := Config{NodeKey: "workspace-test"}
	state := State{NodeID: "node_test", CredentialToken: "node_cred_secret"}
	hashURI := "sha256:" + rawBytesHashHex(content)
	now := time.Now().UTC()
	watchedStore := watchedroots.NewStore(store.DataDir)
	if err := watchedStore.SavePathState(watchedroots.PathState{
		SchemaVersion:  watchedroots.PathStateSchemaVersion,
		RootKey:        "documents",
		PathKey:        watchedroots.PathKey("documents", "retry.txt"),
		RelativePath:   "retry.txt",
		Status:         watchedroots.PathStatusIncluded,
		Kind:           watchedroots.PathKindFile,
		ContentHashURI: hashURI,
		FirstSeenAt:    now,
		LastSeenAt:     now,
		LastScannedAt:  now,
	}); err != nil {
		t.Fatalf("save path state: %v", err)
	}
	action := watchedroots.OutputAction{
		ActionKind:            watchedroots.OutputActionBackupFile,
		RootKey:               "documents",
		RelativePath:          "retry.txt",
		ContentHashURI:        hashURI,
		SizeBytes:             int64(len(content)),
		ModifiedAt:            &now,
		BackupMode:            watchedroots.BackupModeIncrementalRaw,
		BackupItemKind:        mainwatchedroots.BackupItemKindFile,
		BackupMaxFileBytes:    1024,
		BackupMaxBatchBytes:   1024,
		BackupMaxPendingItems: 10,
		BackupMaxPendingBytes: 1024,
	}
	if _, _, err := store.QueueWatchedRootBackupAction(config, state, action, sourcePath); err != nil {
		t.Fatalf("QueueWatchedRootBackupAction failed: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/node-agent/sync/private-backup" {
			t.Fatalf("transient upload should fail before batch upload, got %s", r.URL.Path)
		}
		http.Error(w, "temporary server error", http.StatusInternalServerError)
	}))
	defer server.Close()
	config.MainURL = server.URL

	flush := flushWatchedRootBackups(context.Background(), store, config, state, "corr_test")
	if flush.Status != watchedroots.OutputStatusFailed || flush.PendingAfter != 1 || flush.RetryableAfter != 1 || flush.FailedAfter != 0 || flush.ManualAfter != 0 {
		t.Fatalf("unexpected transient flush %#v", flush)
	}
	status, err := store.LocalWatchedRootBackupStatus(config, state, "documents")
	if err != nil {
		t.Fatalf("LocalWatchedRootBackupStatus failed: %v", err)
	}
	if status.Counts.Pending != 1 || status.Counts.Retryable != 1 || status.Counts.Failed != 0 || status.Counts.ManualAction != 0 {
		t.Fatalf("transient upload should remain retryable pending, got %#v", status.Counts)
	}
	pathState, err := watchedStore.LoadPathState("documents", "retry.txt")
	if err != nil {
		t.Fatalf("LoadPathState failed: %v", err)
	}
	if pathState.BackupStatus != watchedroots.OutputStatusQueued || pathState.LastBackupErrorCode != watchedroots.FindingBackupArtifactUploadFailed {
		t.Fatalf("retryable path state should stay queued with reason, got %#v", pathState)
	}
}

type fakeWatchedRootTransferServer struct {
	t         *testing.T
	state     State
	rootKey   string
	statuses  map[string]filetransfer.Status
	created   int
	completed int
	failChunk bool
}

func newFakeWatchedRootTransferServer(t *testing.T, state State, rootKey string) *fakeWatchedRootTransferServer {
	t.Helper()
	return &fakeWatchedRootTransferServer{
		t:        t,
		state:    state,
		rootKey:  rootKey,
		statuses: map[string]filetransfer.Status{},
	}
}

func (s *fakeWatchedRootTransferServer) Close() {}

func (s *fakeWatchedRootTransferServer) CreatedCount() int {
	return s.created
}

func (s *fakeWatchedRootTransferServer) FailNextChunkUpload() {
	s.failChunk = true
}

func (s *fakeWatchedRootTransferServer) Handle(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/v1/file-transfers" && r.Method == http.MethodPost {
		s.handleCreate(w, r)
		return true
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/file-transfers/") {
		return false
	}
	transferID, action, subref := splitFakeTransferPath(r.URL.Path)
	switch {
	case action == "" && r.Method == http.MethodGet:
		status, ok := s.statuses[transferID]
		if !ok {
			http.NotFound(w, r)
			return true
		}
		_ = json.NewEncoder(w).Encode(response.Success("corr_test", status))
	case action == "chunks" && r.Method == http.MethodPut:
		s.handleChunk(w, r, transferID, subref)
	case action == "complete" && r.Method == http.MethodPost:
		s.handleComplete(w, transferID)
	default:
		s.t.Fatalf("unexpected file-transfer request %s %s", r.Method, r.URL.Path)
	}
	return true
}

func (s *fakeWatchedRootTransferServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	var manifest filetransfer.Manifest
	if err := json.NewDecoder(r.Body).Decode(&manifest); err != nil {
		s.t.Fatalf("decode transfer manifest: %v", err)
	}
	if manifest.SourceNodeID != s.state.NodeID || manifest.SourceRootKey != s.rootKey || manifest.TransferKind != filetransfer.KindWatchedRootBackup {
		s.t.Fatalf("unexpected transfer manifest %#v", manifest)
	}
	now := time.Now().UTC()
	normalized, err := filetransfer.NormalizeManifest(manifest, now)
	if err != nil {
		s.t.Fatalf("normalize transfer manifest: %v", err)
	}
	chunks, err := filetransfer.PlanChunks(normalized.TransferID, normalized.FileSizeBytes, normalized.ChunkSizeBytes)
	if err != nil {
		s.t.Fatalf("plan transfer chunks: %v", err)
	}
	for idx := range chunks {
		chunks[idx].TransferID = normalized.TransferID
	}
	status := filetransfer.Status{
		Manifest:      normalized,
		Chunks:        chunks,
		MissingChunks: fakeMissingChunks(chunks),
	}
	s.statuses[normalized.TransferID] = status
	s.created++
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(response.Success("corr_test", status))
}

func (s *fakeWatchedRootTransferServer) handleChunk(w http.ResponseWriter, r *http.Request, transferID, subref string) {
	status, ok := s.statuses[transferID]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if s.failChunk {
		s.failChunk = false
		http.Error(w, "temporary chunk failure", http.StatusInternalServerError)
		return
	}
	index, err := parseFakeChunkIndex(subref)
	if err != nil {
		s.t.Fatalf("parse chunk index: %v", err)
	}
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		s.t.Fatalf("read chunk body: %v", err)
	}
	for idx := range status.Chunks {
		if status.Chunks[idx].Index != index {
			continue
		}
		if int64(len(payload)) != status.Chunks[idx].SizeBytes {
			s.t.Fatalf("chunk %d size = %d, want %d", index, len(payload), status.Chunks[idx].SizeBytes)
		}
		status.Chunks[idx].Status = filetransfer.ChunkStatusUploaded
		status.Chunks[idx].ChecksumAlgorithm = filetransfer.ChecksumSHA256
		status.Chunks[idx].ChecksumHex = filetransfer.SHA256Hex(payload)
		status.Chunks[idx].ReceivedBytes = int64(len(payload))
	}
	status.Manifest.Status = filetransfer.StatusUploading
	status.MissingChunks = fakeMissingChunks(status.Chunks)
	status.UploadedChunks = int64(len(status.Chunks) - len(status.MissingChunks))
	s.statuses[transferID] = status
	result := filetransfer.UploadChunkResult{
		Manifest:      status.Manifest,
		Status:        status,
		ChecksumHex:   filetransfer.SHA256Hex(payload),
		ReceivedBytes: int64(len(payload)),
	}
	_ = json.NewEncoder(w).Encode(response.Success("corr_test", result))
}

func (s *fakeWatchedRootTransferServer) handleComplete(w http.ResponseWriter, transferID string) {
	status, ok := s.statuses[transferID]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	status.MissingChunks = fakeMissingChunks(status.Chunks)
	if len(status.MissingChunks) != 0 {
		http.Error(w, "missing chunks", http.StatusBadRequest)
		return
	}
	for idx := range status.Chunks {
		status.Chunks[idx].Status = filetransfer.ChunkStatusAccepted
	}
	status.Manifest.Status = filetransfer.StatusAccepted
	status.Manifest.AcceptedPath = "watched-root-backups/workspace-test/" + s.rootKey + "/" + transferID + "/" + status.Manifest.DestinationLogicalPath
	status.Manifest.StorageEntryID = "storage_entry_" + transferID[len("file_transfer_"):]
	status.AcceptedPath = status.Manifest.AcceptedPath
	status.AcceptedChunks = int64(len(status.Chunks))
	status.UploadedChunks = int64(len(status.Chunks))
	s.statuses[transferID] = status
	s.completed++
	result := filetransfer.CompleteResult{
		Manifest:       status.Manifest,
		Status:         status,
		AcceptedPath:   status.Manifest.AcceptedPath,
		StorageEntryID: status.Manifest.StorageEntryID,
		ChecksumHex:    status.Manifest.ChecksumHex,
	}
	_ = json.NewEncoder(w).Encode(response.Success("corr_test", result))
}

func fakeMissingChunks(chunks []filetransfer.Chunk) []int64 {
	missing := []int64{}
	for _, chunk := range chunks {
		if chunk.Status != filetransfer.ChunkStatusUploaded && chunk.Status != filetransfer.ChunkStatusAccepted {
			missing = append(missing, chunk.Index)
		}
	}
	return missing
}

func splitFakeTransferPath(path string) (string, string, string) {
	ref := strings.TrimPrefix(path, "/v1/file-transfers/")
	parts := strings.Split(strings.Trim(ref, "/"), "/")
	if len(parts) == 0 {
		return "", "", ""
	}
	if len(parts) == 1 {
		return parts[0], "", ""
	}
	if len(parts) >= 3 && parts[1] == "chunks" {
		return parts[0], parts[1], parts[2]
	}
	return parts[0], parts[1], ""
}

func parseFakeChunkIndex(value string) (int64, error) {
	var index int64
	if _, err := fmt.Sscanf(value, "%d", &index); err != nil {
		return 0, err
	}
	return index, nil
}

func stringPtrForTest(value string) *string {
	return &value
}
