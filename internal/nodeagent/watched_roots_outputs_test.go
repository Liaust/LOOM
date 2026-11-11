package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/response"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func TestWatchedRootEmptyOutputPlanDoesNotOpenQueueTransaction(t *testing.T) {
	store := Store{DataDir: t.TempDir()}
	plan := watchedroots.OutputPlan{RootKey: "notes"}
	plan.Counts.AlreadyCurrent = 42
	got := applyWatchedRootOutputPlan(context.Background(), store, Config{}, State{}, watchedroots.NewStore(store.DataDir), watchedroots.ValidatedRoot{}, plan)
	if !reflect.DeepEqual(got, plan) {
		t.Fatalf("empty plan changed: %#v", got)
	}
	entries, err := os.ReadDir(store.DataDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty plan touched queue state: entries=%v err=%v", entries, err)
	}
}

func TestWatchedRootPagedOutputResumesWithoutRepeatingHistory(t *testing.T) {
	store, config, state, original := backupSelectionHistory(t, 128)
	config.BackupTransport.StabilityWindow = "0s"
	source := t.TempDir()
	for i := range 75 {
		if err := os.WriteFile(filepath.Join(source, fmt.Sprintf("note-%03d.md", i)), []byte("small current note"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config.Filesystem.SafeRoots = []filesystemconnector.SafeRoot{filesystemconnector.DefaultSafeRoot("notes", source)}
	root, err := watchedroots.ValidateRootConfigForRun(watchedroots.RootConfig{
		RootKey: "notes", SafeRootKey: "notes",
		Scan:         watchedroots.ScanConfig{StabilityWindow: "0s", FullRescanInterval: "6h"},
		BackupPolicy: watchedroots.BackupPolicy{Mode: watchedroots.BackupModeIncrementalRaw, MaxFileBytes: 1 << 20, MaxBatchBytes: 1 << 20},
	}, config.Filesystem)
	if err != nil {
		t.Fatal(err)
	}
	instance := noderuntime.WorkerInstance{WorkerKey: noderuntime.WatchedRootWorkerKey("notes")}
	var lastCursor string
	var changedPath string
	settled := false
	for turn := range 10 {
		if turn == 1 {
			if err := os.WriteFile(filepath.Join(source, changedPath), []byte("new edit behind the saved cursor"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := watchedroots.NewStore(store.DataDir).AddDirtyHint("notes", watchedroots.DirtyHint{RelativePath: changedPath}); err != nil {
				t.Fatal(err)
			}
			lastCursor = ""
		}
		// A fresh value has no memory of the preceding turn's selected records.
		result, err := runWatchedRootReconcileAndPlan(t.Context(), store, config, state, instance, root, watchedroots.ScanModeAuto, true, false, false, "page-test")
		if err != nil || result.OutputPlan == nil || result.OutputPlan.Page == nil || result.OutputPlan.Counts.Failed != 0 {
			t.Fatal(result, err)
		}
		if len(result.OutputPlan.Actions) > watchedRootOutputBatchSize || result.OutputPlan.Page.StatesRead > watchedRootOutputStatePage || result.Persistence["record_reads"] > 8192 {
			t.Fatal("turn cost grew with retained history", result.OutputPlan.Page, result.Persistence)
		}
		if turn > 0 && result.OutputPlan.Page.After != lastCursor {
			t.Fatal("restart lost cursor", result.OutputPlan.Page, lastCursor)
		}
		if turn == 0 {
			changedPath = result.OutputPlan.Actions[0].RelativePath
		}
		after, err := watchedroots.NewStore(store.DataDir).LoadOutputAfter("notes", result.Checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		if result.OutputPlan.Page.More && (after <= lastCursor || !result.Checkpoint.OutputContinuationReady || result.Checkpoint.OutputsCurrent) {
			t.Fatal("unfinished page lost durable continuation", result.Checkpoint)
		}
		lastCursor = after
		if result.Checkpoint.OutputsCurrent {
			settled = true
			if result.Checkpoint.OutputContinuationReady {
				t.Fatal("settled inventory kept continuation", result.Checkpoint)
			}
			break
		}
	}
	if !settled {
		t.Fatal("bounded pages did not settle")
	}
	items, err := store.LoadWatchedRootBackupItems()
	if err != nil || len(items) != len(original.items)+76 {
		t.Fatal("history lost or backup duplicated", len(items), err)
	}
	for i, item := range original.items {
		if !reflect.DeepEqual(items[i], item) {
			t.Fatal("retained history changed", i)
		}
	}
}

func TestWatchedRootTerminalSkippedOutputsConverge(t *testing.T) {
	for _, backup := range []bool{false, true} {
		name := "sync"
		if backup {
			name = "backup"
		}
		t.Run(name, func(t *testing.T) {
			store, config, state, _ := localSyncObjectTestStore(t)
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "note.md"), []byte("oversized"), 0600); err != nil {
				t.Fatal(err)
			}
			config.Filesystem.SafeRoots = []filesystemconnector.SafeRoot{filesystemconnector.DefaultSafeRoot("notes", source)}
			cfg := watchedroots.RootConfig{RootKey: "notes", SafeRootKey: "notes", Scan: watchedroots.ScanConfig{StabilityWindow: "0s", FullRescanInterval: "6h"}}
			if backup {
				cfg.BackupPolicy = watchedroots.BackupPolicy{Mode: watchedroots.BackupModeIncrementalRaw, MaxFileBytes: 1, MaxBatchBytes: 1024}
			} else {
				cfg.SyncPolicy = watchedroots.SyncPolicy{Mode: watchedroots.SyncModeSelectedFiles, ScopeRef: "notes", MaxFileBytes: 1}
			}
			root, err := watchedroots.ValidateRootConfigForRun(cfg, config.Filesystem)
			if err != nil {
				t.Fatal(err)
			}
			instance := noderuntime.WorkerInstance{WorkerKey: noderuntime.WatchedRootWorkerKey("notes")}
			run := func() watchedroots.ScanResult {
				t.Helper()
				result, err := runWatchedRootReconcileAndPlan(t.Context(), store, config, state, instance, root, watchedroots.ScanModeAuto, true, false, false, "test")
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			first := run()
			if first.Checkpoint.OutputsCurrent {
				t.Fatal("new file observation was prematurely marked current")
			}
			settled := run()
			if !settled.Checkpoint.OutputsCurrent || settled.OutputPlan == nil || len(settled.OutputPlan.Actions) != 1 {
				t.Fatalf("terminal skipped output did not converge: %+v", settled)
			}
			if backup && (settled.Status != watchedroots.RunStatusDegraded || settled.OutputPlan.Counts.Failed != 1) {
				t.Fatal("terminal backup finding was hidden")
			}
			ws := watchedroots.NewStore(store.DataDir)
			cp, err := ws.LoadCheckpoint("notes")
			if err != nil || !cp.OutputsCurrent {
				t.Fatal("settled checkpoint was not persisted", cp, err)
			}
			now := time.Now().UTC()
			safety, err := watchedroots.NextNativeReconcile(ws, root, now, now, true, 15*time.Minute)
			if err != nil || cp.LastFullRescanAt == nil || !safety.Equal(cp.LastFullRescanAt.Add(6*time.Hour)) {
				t.Fatal("terminal skip kept the minute planning deadline", safety, err)
			}
			next, err := watchedRootDeliveryDeadline(store, config, state, "notes", safety, now, 15*time.Minute)
			if err != nil || !next.Equal(now.Add(time.Minute)) {
				t.Fatal("settled planning hid pending transport", next, err)
			}
			paths, err := ws.ListPathStates("notes", 0)
			if err != nil || len(paths) != 1 {
				t.Fatal(paths, err)
			}
			// A settled skipped pass must not enumerate path history to replan the
			// same terminal action. Normal source scans still validate these records.
			record := filepath.Join(ws.RootPaths("notes").PathsDir, paths[0].PathKey[:2], paths[0].PathKey+".json")
			if _, err := os.Stat(record); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(record, []byte("invalid unrelated history"), 0600); err != nil {
				t.Fatal(err)
			}
			idle := run()
			if idle.Mode != watchedroots.RunStatusSkipped || !idle.Checkpoint.OutputsCurrent || len(idle.OutputPlan.Actions) != 0 {
				t.Fatalf("settled plan was revisited: %+v", idle)
			}
			if backup {
				findings, err := ws.ListFindings("notes", 0)
				if err != nil || len(findings) != 1 || !watchedroots.FindingIsActive(findings[0]) || findings[0].Kind != watchedroots.FindingBackupFileTooLarge {
					t.Fatal("settled plan lost the terminal finding", findings, err)
				}
			}
		})
	}
}

func TestWatchedRootOutputPlanDoesNotSettleDeferralsOrFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action watchedroots.OutputAction
		counts watchedroots.OutputCounts
	}{
		{name: "stability", action: watchedroots.OutputAction{ActionKind: watchedroots.OutputActionSkipped, Status: watchedroots.OutputStatusSkipped, ReasonCode: watchedroots.HashStatusDeferred}},
		{name: "hash failure", action: watchedroots.OutputAction{ActionKind: watchedroots.OutputActionBackupSkipped, Status: watchedroots.OutputStatusSkipped, ReasonCode: watchedroots.HashStatusNotNeeded}},
		{name: "transport", action: watchedroots.OutputAction{ActionKind: watchedroots.OutputActionSyncObject, Status: watchedroots.OutputStatusQueued}},
		{name: "queue failure", action: watchedroots.OutputAction{ActionKind: watchedroots.OutputActionBackupFile, Status: watchedroots.OutputStatusFailed}, counts: watchedroots.OutputCounts{Failed: 1}},
		{name: "unapplied terminal", action: watchedroots.OutputAction{ActionKind: watchedroots.OutputActionSkipped, Status: watchedroots.OutputStatusPending, ReasonCode: watchedroots.ReasonSkippedTooLarge}},
		{name: "cancelled", counts: watchedroots.OutputCounts{Deferred: 1}},
		{name: "unattributed failure", counts: watchedroots.OutputCounts{Failed: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := watchedroots.OutputPlan{Counts: tc.counts}
			if tc.action.ActionKind != "" {
				plan.Actions = []watchedroots.OutputAction{tc.action}
			}
			if watchedRootOutputPlanSettled(plan) {
				t.Fatal("unfinished output plan settled")
			}
		})
	}
}

func TestWatchedRootIncrementalOutputSelectionRequiresSettledExactDirtyWork(t *testing.T) {
	base := watchedroots.ScanResult{
		Mode: watchedroots.ScanModeDirty, Status: watchedroots.RunStatusHealthy, OutputsPreviouslyCurrent: true,
		Counts:       watchedroots.ScanCounts{Changed: 1},
		ChangedPaths: []watchedroots.PathChange{{RelativePath: "a.md"}},
	}
	if paths, ok := watchedRootIncrementalOutputPaths(base); !ok || !reflect.DeepEqual(paths, []string{"a.md"}) {
		t.Fatal("settled exact delta was not scoped", paths, ok)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*watchedroots.ScanResult)
	}{
		{"unsettled or changed coverage", func(r *watchedroots.ScanResult) { r.OutputsPreviouslyCurrent = false }},
		{"full", func(r *watchedroots.ScanResult) { r.Mode = watchedroots.ScanModeFull }},
		{"skipped", func(r *watchedroots.ScanResult) { r.Mode = watchedroots.RunStatusSkipped }},
		{"blocked", func(r *watchedroots.ScanResult) { r.Status = watchedroots.RunStatusBlocked }},
		{"errors", func(r *watchedroots.ScanResult) { r.Counts.Errors = 1 }},
		{"budget", func(r *watchedroots.ScanResult) { r.Counts.BudgetExhausted = 1 }},
		{"hash deferral", func(r *watchedroots.ScanResult) { r.Counts.HashDeferred = 1 }},
		{"pending coverage", func(r *watchedroots.ScanResult) { r.Checkpoint.PendingRescan = true }},
		{"deferred paths", func(r *watchedroots.ScanResult) { r.Checkpoint.DeferredHashPaths = []string{"b.md"} }},
		{"unfinished full pass", func(r *watchedroots.ScanResult) { r.Checkpoint.FullScanID = "continuation" }},
		{"incomplete changes", func(r *watchedroots.ScanResult) { r.Counts.Changed = 2 }},
		{"uncertified no-op", func(r *watchedroots.ScanResult) { r.Counts.Changed, r.ChangedPaths = 0, nil }},
		{"root hint", func(r *watchedroots.ScanResult) {
			r.ChangedPaths = []watchedroots.PathChange{{RelativePath: "__rescan__"}}
		}},
		{"duplicate", func(r *watchedroots.ScanResult) {
			r.Counts.Changed, r.ChangedPaths = 2, append(r.ChangedPaths, r.ChangedPaths[0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mutate(&r)
			if paths, ok := watchedRootIncrementalOutputPaths(r); ok {
				t.Fatal("uncertain or unsettled work used scoped planning", paths)
			}
		})
	}
	queued := watchedroots.OutputPlan{Actions: []watchedroots.OutputAction{{ActionKind: watchedroots.OutputActionSyncObject, Status: watchedroots.OutputStatusQueued}}}
	if !watchedRootOutputPlanComplete(queued, true) || watchedRootOutputPlanComplete(queued, false) {
		t.Fatal("scoped durable queue handoff changed global planning semantics")
	}
	queued.Counts.Deferred = 1
	if watchedRootOutputPlanComplete(queued, true) {
		t.Fatal("partial scoped application stranded remaining paths")
	}
}

func TestWatchedRootDirtyOutputsStayScopedAndNoOpPreservesSettledPlanning(t *testing.T) {
	store, config, state, _ := localSyncObjectTestStore(t)
	source := t.TempDir()
	for _, path := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(source, path), []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config.Filesystem.SafeRoots = []filesystemconnector.SafeRoot{filesystemconnector.DefaultSafeRoot("notes", source)}
	root, err := watchedroots.ValidateRootConfigForRun(watchedroots.RootConfig{
		RootKey: "notes", SafeRootKey: "notes", Scan: watchedroots.ScanConfig{StabilityWindow: "0s", FullRescanInterval: "6h"},
		SyncPolicy:   watchedroots.SyncPolicy{Mode: watchedroots.SyncModeSelectedFiles, ScopeRef: "notes"},
		DeletePolicy: watchedroots.DeletePolicy{Mode: watchedroots.DeleteModeTombstone},
	}, config.Filesystem)
	if err != nil {
		t.Fatal(err)
	}
	instance := noderuntime.WorkerInstance{WorkerKey: noderuntime.WatchedRootWorkerKey("notes")}
	ws := watchedroots.NewStore(store.DataDir)
	run := func(dirty string) watchedroots.ScanResult {
		t.Helper()
		if dirty != "" {
			if err := ws.AddDirtyHint("notes", watchedroots.DirtyHint{RelativePath: dirty}); err != nil {
				t.Fatal(err)
			}
		}
		result, err := runWatchedRootReconcileAndPlan(t.Context(), store, config, state, instance, root, watchedroots.ScanModeAuto, true, false, false, "test")
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if first := run(""); first.Mode != watchedroots.ScanModeFull || first.Checkpoint.OutputsCurrent {
		t.Fatal("startup did not retain full planning", first)
	}
	if settled := run(""); !settled.Checkpoint.OutputsCurrent {
		t.Fatal("initial planning did not settle", settled)
	}
	b := ws.RootPaths("notes").PathsDir
	bKey := watchedroots.PathKey("notes", "b.md")
	bRecord := filepath.Join(b, bKey[:2], bKey+".json")
	retained, err := os.ReadFile(bRecord)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bRecord, []byte("unrelated malformed history"), 0600); err != nil {
		t.Fatal(err)
	}
	if idle := run("a.md"); !idle.InventoryUnchanged || !idle.Checkpoint.OutputsCurrent || len(idle.OutputPlan.Actions) != 0 {
		t.Fatal("no-op dirty scan invalidated settled planning", idle)
	}
	if err := os.WriteFile(bRecord, retained, 0600); err != nil {
		t.Fatal(err)
	}
	checkDelta := func(path string) {
		t.Helper()
		changed := run(path)
		if changed.Mode != watchedroots.ScanModeDirty || !changed.OutputsPreviouslyCurrent || !changed.Checkpoint.OutputsCurrent || len(changed.OutputPlan.Actions) != 2 {
			t.Fatal("dirty delta did not complete scoped planning", changed)
		}
		for _, action := range changed.OutputPlan.Actions {
			if action.RelativePath != path || action.Status != watchedroots.OutputStatusQueued {
				t.Fatal("dirty delta expanded beyond the exact changed path", action)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(source, "a.md"), []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	checkDelta("a.md")
	if err := os.WriteFile(filepath.Join(source, "c.md"), []byte("new source"), 0600); err != nil {
		t.Fatal(err)
	}
	checkDelta("c.md")
	if err := os.Remove(filepath.Join(source, "a.md")); err != nil {
		t.Fatal(err)
	}
	checkDelta("a.md")
	// Unfinished planning outside a later dirty hint must still be discovered.
	bState, err := ws.LoadPathState("notes", "b.md")
	if err != nil {
		t.Fatal(err)
	}
	bState.LastQueuedFileObservation = ""
	if err := ws.SavePathState(bState); err != nil {
		t.Fatal(err)
	}
	cp, err := ws.LoadCheckpoint("notes")
	if err != nil {
		t.Fatal(err)
	}
	cp.OutputsCurrent = false
	if err := ws.SaveCheckpoint("notes", cp); err != nil {
		t.Fatal(err)
	}
	fallback := run("c.md")
	if fallback.OutputsPreviouslyCurrent || len(fallback.OutputPlan.Actions) != 1 || fallback.OutputPlan.Actions[0].RelativePath != "b.md" {
		t.Fatal("unsettled output outside the dirty hint was stranded", fallback)
	}
}

func TestWatchedRootDeferredHashResumeKeepsFullOutputFallback(t *testing.T) {
	store, config, state, _ := localSyncObjectTestStore(t)
	source := t.TempDir()
	for _, path := range []string{"asset.bin", "other.bin"} {
		if err := os.WriteFile(filepath.Join(source, path), []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config.Filesystem.SafeRoots = []filesystemconnector.SafeRoot{filesystemconnector.DefaultSafeRoot("notes", source)}
	root, err := watchedroots.ValidateRootConfigForRun(watchedroots.RootConfig{
		RootKey: "notes", SafeRootKey: "notes", Scan: watchedroots.ScanConfig{StabilityWindow: "0s", FullRescanInterval: "6h"},
		SyncPolicy: watchedroots.SyncPolicy{Mode: watchedroots.SyncModeSelectedFiles, ScopeRef: "notes"},
	}, config.Filesystem)
	if err != nil {
		t.Fatal(err)
	}
	ws := watchedroots.NewStore(store.DataDir)
	run := func() watchedroots.ScanResult {
		t.Helper()
		result, err := runWatchedRootReconcileAndPlan(t.Context(), store, config, state, noderuntime.WorkerInstance{WorkerKey: "test"}, root, watchedroots.ScanModeAuto, true, false, false, "test")
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	run()
	if settled := run(); !settled.Checkpoint.OutputsCurrent {
		t.Fatal("initial output did not settle", settled)
	}
	root.Config.Scan.StabilityWindow = "1h"
	root.ConfigHash = watchedroots.ConfigHash(root.Config)
	modified := time.Now().Add(time.Second)
	if err := os.Chtimes(filepath.Join(source, "asset.bin"), modified, modified); err != nil {
		t.Fatal(err)
	}
	if err := ws.AddDirtyHint("notes", watchedroots.DirtyHint{RelativePath: "asset.bin"}); err != nil {
		t.Fatal(err)
	}
	deferred := run()
	if deferred.Counts.HashDeferred != 1 || !deferred.Checkpoint.PendingRescan || deferred.Checkpoint.OutputsCurrent {
		t.Fatal("hash deferral was prematurely settled", deferred)
	}
	asset, err := ws.LoadPathState("notes", "asset.bin")
	if err != nil {
		t.Fatal(err)
	}
	ready := time.Now().Add(-2 * time.Hour)
	asset.HashObservedAt = &ready
	if err := ws.SavePathState(asset); err != nil {
		t.Fatal(err)
	}
	other, err := ws.LoadPathState("notes", "other.bin")
	if err != nil {
		t.Fatal(err)
	}
	other.LastQueuedFileObservation = ""
	if err := ws.SavePathState(other); err != nil {
		t.Fatal(err)
	}
	resumed := run()
	if resumed.OutputsPreviouslyCurrent || resumed.InventoryUnchanged || resumed.Counts.HashUnchanged != 1 || len(resumed.ChangedPaths) != 0 || resumed.Checkpoint.PendingRescan {
		t.Fatal("same-content deferred completion was misclassified as settled dirty work", resumed)
	}
	if len(resumed.OutputPlan.Actions) != 1 || resumed.OutputPlan.Actions[0].RelativePath != "other.bin" {
		t.Fatal("empty changed-path list stranded previously unplanned output", resumed)
	}
}

func TestWatchedRootOutputPlanBoundedAndResumable(t *testing.T) {
	testWatchedRootOutputPlanBoundedAndResumable(t, false)
}
func TestQueueRecordsBackupPlanBoundedAndResumable(t *testing.T) {
	testWatchedRootOutputPlanBoundedAndResumable(t, true)
}
func testWatchedRootOutputPlanBoundedAndResumable(t *testing.T, records bool) {
	store, config, state, _ := localSyncObjectTestStore(t)
	if records {
		if _, err := store.MigrateQueueRecords(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	ws := watchedroots.NewStore(store.DataDir)
	root := watchedroots.ValidatedRoot{Config: watchedroots.RootConfig{
		RootKey: "notes", Include: []string{"**/*"},
		BackupPolicy: watchedroots.BackupPolicy{Mode: watchedroots.BackupModeMetadataOnly},
	}}
	const total = watchedRootOutputBatchSize*2 + 3
	for i := 0; i < total; i++ {
		if err := ws.SavePathState(watchedroots.PathState{
			RootKey: "notes", RelativePath: fmt.Sprintf("dir-%03d", i),
			Kind:           watchedroots.PathKindDirectory,
			Classification: watchedroots.Classification{ReasonCode: watchedroots.ReasonExcludedDirectoryMetadata},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for done := 0; done < total; {
		plan, err := watchedroots.PlanOutputs(ws, root, "test")
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Actions) != total-done {
			t.Fatalf("remaining actions = %d, want %d", len(plan.Actions), total-done)
		}
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		stopped := applyWatchedRootOutputPlan(cancelled, store, config, state, ws, root, plan)
		if len(stopped.Actions) != 0 || stopped.Counts.Deferred != total-done {
			t.Fatalf("cancelled plan mutated actions: %#v", stopped)
		}
		plan = applyWatchedRootOutputPlan(context.Background(), store, config, state, ws, root, plan)
		if len(plan.Actions) == 0 || len(plan.Actions) > watchedRootOutputBatchSize || plan.Counts.Failed != 0 {
			t.Fatalf("unbounded or failed plan: %#v", plan)
		}
		done += len(plan.Actions)
		if plan.Counts.Deferred != total-done {
			t.Fatalf("deferred = %d, want %d", plan.Counts.Deferred, total-done)
		}
		queued, err := store.LoadWatchedRootBackupOutbox()
		if err != nil || len(queued) != done {
			t.Fatalf("queue lost or duplicated actions: count=%d err=%v", len(queued), err)
		}
	}
	requests := 0
	if _, _, err := store.QueueWatchedRootBackupAction(config, state, watchedroots.OutputAction{
		ActionKind: watchedroots.OutputActionBackupMetadata,
		RootKey:    "other", RelativePath: "unrelated",
		BackupMode:     watchedroots.BackupModeMetadataOnly,
		BackupItemKind: mainwatchedroots.BackupItemKindDirectory,
	}, ""); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input mainwatchedroots.BackupBatchInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if input.RootKey != "notes" {
			t.Errorf("unrelated root delivered: %s", input.RootKey)
		}
		requests++
		_ = json.NewEncoder(w).Encode(response.Success("test", mainwatchedroots.BackupBatchResult{
			Batch: mainwatchedroots.BackupBatch{WatchedRootBackupBatchID: fmt.Sprintf("batch-%d", requests), Status: mainwatchedroots.BackupBatchStatusAccepted},
			Items: []mainwatchedroots.BackupItem{{LocalItemRef: input.Items[0].LocalItemRef, Status: mainwatchedroots.BackupItemStatusAccepted}},
		}))
	}))
	defer server.Close()
	config.MainURL = server.URL
	if flush := flushWatchedRootBackupsForRoot(context.Background(), store, config, state, "test", "empty"); flush.SubmittedItems != 0 || flush.PendingBefore != 0 || requests != 0 {
		t.Fatalf("empty root flushed unrelated work: %#v", flush)
	}
	for done := 0; done < total; {
		flush := flushWatchedRootBackupsForRoot(context.Background(), store, config, state, "test", "notes")
		if flush.SubmittedItems == 0 || flush.SubmittedItems > watchedRootOutputBatchSize {
			t.Fatalf("unbounded or stalled flush: %#v", flush)
		}
		done += flush.SubmittedItems
		if flush.PendingAfter != total-done || flush.AcceptedAfter != done {
			t.Fatalf("incorrect remaining queue: %#v", flush)
		}
		want := watchedroots.OutputStatusQueued
		if done == total {
			want = watchedroots.OutputStatusRecorded
		}
		if flush.Status != want {
			t.Fatalf("normal partial progress classified as %s, want %s: %#v", flush.Status, want, flush)
		}
	}
	other, err := store.LocalWatchedRootBackupStatus(config, state, "other")
	if err != nil || other.Counts.Pending != 1 || other.Counts.Accepted != 0 {
		t.Fatalf("unrelated queue changed: %#v, err=%v", other, err)
	}
}
