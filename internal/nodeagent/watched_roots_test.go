package nodeagent

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
	loomsync "loom.local/loom/internal/sync"
)

func TestWatchedRootsCommandsAddStatusAndExplain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatalf("mkdir vault: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vault, "Project.md"), []byte("# Project\n"), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vault, "Project.tmp"), []byte("temp\n"), 0o600); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	store := Store{
		ConfigPath: filepath.Join(dir, "config.json"),
		StatePath:  filepath.Join(dir, "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	if err := store.SaveConfig(Config{
		MainURL:     "http://10.44.0.2:8080",
		NodeKey:     "workspace-test",
		DisplayName: "Workspace Test",
		Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
			filesystemconnector.DefaultSafeRoot("slice09", vault),
		}},
	}); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	opts := rootOptions{
		configPath: store.ConfigPath,
		statePath:  store.StatePath,
		dataDir:    store.DataDir,
		jsonOutput: true,
		out:        io.Discard,
		errOut:     io.Discard,
	}
	addCmd := newWatchedRootsCommand(&opts)
	addCmd.SetArgs([]string{
		"add", "notes",
		"--safe-root", "slice09",
		"--path", ".",
		"--include", "**/*.md",
		"--exclude", "**/*.tmp",
	})
	if err := addCmd.Execute(); err != nil {
		t.Fatalf("watched-roots add failed: %v", err)
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	instance, err := runtimeStore.LoadInstance(noderuntime.WatchedRootWorkerKey("notes"))
	if err != nil {
		t.Fatalf("LoadInstance failed: %v", err)
	}
	if instance.Kind != noderuntime.KindWatchedRoot || instance.ConfigHash == "" {
		t.Fatalf("unexpected watched-root instance %#v", instance)
	}
	var savedRootConfig watchedroots.RootConfig
	if err := json.Unmarshal(instance.ConfigJSON, &savedRootConfig); err != nil {
		t.Fatalf("decode watched-root instance config: %v", err)
	}
	savedRootConfig.Scan.StabilityWindow = "0s"
	savedConfigJSON, err := json.Marshal(savedRootConfig)
	if err != nil {
		t.Fatalf("encode watched-root instance config: %v", err)
	}
	instance.ConfigJSON = savedConfigJSON
	if err := runtimeStore.SaveInstance(instance); err != nil {
		t.Fatalf("SaveInstance failed: %v", err)
	}

	var runOut bytes.Buffer
	opts.out = &runOut
	runCmd := newWatchedRootsCommand(&opts)
	runCmd.SetArgs([]string{"run", "notes", "--once", "--mode", "full", "--stability-window", "0s"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("watched-roots run failed: %v", err)
	}
	var runEnvelope response.Envelope[watchedroots.ScanResult]
	if err := json.Unmarshal(runOut.Bytes(), &runEnvelope); err != nil {
		t.Fatalf("decode run: %v\n%s", err, runOut.String())
	}
	if !runEnvelope.OK || runEnvelope.Data.Counts.Included != 1 || runEnvelope.Data.Counts.Excluded == 0 {
		t.Fatalf("unexpected run envelope %#v", runEnvelope)
	}

	var statusOut bytes.Buffer
	opts.out = &statusOut
	statusCmd := newWatchedRootsCommand(&opts)
	statusCmd.SetArgs([]string{"status", "notes"})
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("watched-roots status failed: %v", err)
	}
	var statusEnvelope response.Envelope[watchedRootStatusResult]
	if err := json.Unmarshal(statusOut.Bytes(), &statusEnvelope); err != nil {
		t.Fatalf("decode status: %v\n%s", err, statusOut.String())
	}
	if !statusEnvelope.OK || len(statusEnvelope.Data.Roots) != 1 || statusEnvelope.Data.Roots[0].Summary == nil {
		t.Fatalf("unexpected status envelope %#v", statusEnvelope)
	}

	var explainOut bytes.Buffer
	opts.out = &explainOut
	explainCmd := newWatchedRootsCommand(&opts)
	explainCmd.SetArgs([]string{"explain", "notes", "--path", "Project.md"})
	if err := explainCmd.Execute(); err != nil {
		t.Fatalf("watched-roots explain failed: %v", err)
	}
	var explainEnvelope response.Envelope[watchedroots.ExplainResult]
	if err := json.Unmarshal(explainOut.Bytes(), &explainEnvelope); err != nil {
		t.Fatalf("decode explain: %v\n%s", err, explainOut.String())
	}
	if !explainEnvelope.OK || !explainEnvelope.Data.Classification.Included {
		t.Fatalf("expected included explanation, got %#v", explainEnvelope)
	}
	if !explainEnvelope.Data.KnownFromState || explainEnvelope.Data.State == nil || explainEnvelope.Data.State.ContentHashURI == "" {
		t.Fatalf("expected explanation to include stored path state, got %#v", explainEnvelope.Data)
	}

	explainOut.Reset()
	opts.out = &explainOut
	explainCmd = newWatchedRootsCommand(&opts)
	explainCmd.SetArgs([]string{"explain", "notes", "--path", "Project.tmp"})
	if err := explainCmd.Execute(); err != nil {
		t.Fatalf("watched-roots explain excluded failed: %v", err)
	}
	if err := json.Unmarshal(explainOut.Bytes(), &explainEnvelope); err != nil {
		t.Fatalf("decode excluded explain: %v\n%s", err, explainOut.String())
	}
	if explainEnvelope.Data.Classification.Included || explainEnvelope.Data.Classification.ReasonCode != watchedroots.ReasonExcludedByPattern {
		t.Fatalf("expected excluded explanation, got %#v", explainEnvelope.Data.Classification)
	}

	if err := watchedroots.NewStore(store.DataDir).MarkRescanRequired("notes", "runtime-test"); err != nil {
		t.Fatalf("MarkRescanRequired failed: %v", err)
	}
	var workerRunOut bytes.Buffer
	opts.out = &workerRunOut
	workerRunCmd := newRuntimeWorkersRunCommand(&opts)
	workerRunCmd.SetArgs([]string{noderuntime.WatchedRootWorkerKey("notes"), "--once"})
	if err := workerRunCmd.Execute(); err != nil {
		t.Fatalf("watched-root runtime worker failed: %v", err)
	}
	var workerRunEnvelope response.Envelope[noderuntime.RunOutput]
	if err := json.Unmarshal(workerRunOut.Bytes(), &workerRunEnvelope); err != nil {
		t.Fatalf("decode worker run: %v\n%s", err, workerRunOut.String())
	}
	if !workerRunEnvelope.OK || workerRunEnvelope.Data.Run.Status != noderuntime.RunStatusSucceeded {
		t.Fatalf("unexpected worker run envelope %#v", workerRunEnvelope)
	}
	if workerRunEnvelope.Data.Health.Status != noderuntime.WorkerStatusHealthy {
		t.Fatalf("expected healthy watched-root worker, got %#v", workerRunEnvelope.Data.Health)
	}
	if workerRunEnvelope.Data.Checkpoint.LastSuccessAt == nil || workerRunEnvelope.Data.Health.QueueSummaryJSON == nil {
		t.Fatalf("expected watched-root checkpoint and queue summary, got %#v", workerRunEnvelope.Data)
	}

	workerRunOut.Reset()
	opts.out = &workerRunOut
	workerRunCmd = newRuntimeWorkersRunCommand(&opts)
	workerRunCmd.SetArgs([]string{noderuntime.WatchedRootWorkerKey("notes"), "--once"})
	if err := workerRunCmd.Execute(); err != nil {
		t.Fatalf("watched-root runtime skip worker failed: %v", err)
	}
	if err := json.Unmarshal(workerRunOut.Bytes(), &workerRunEnvelope); err != nil {
		t.Fatalf("decode skipped worker run: %v\n%s", err, workerRunOut.String())
	}
	var skippedResult watchedroots.ScanResult
	if err := json.Unmarshal(workerRunEnvelope.Data.Run.ResultJSON, &skippedResult); err != nil {
		t.Fatalf("decode skipped scan result: %v", err)
	}
	var skippedSummary watchedroots.RootSummary
	if err := json.Unmarshal(workerRunEnvelope.Data.Health.QueueSummaryJSON, &skippedSummary); err != nil {
		t.Fatalf("decode skipped queue summary: %v", err)
	}
	if skippedResult.Mode != watchedroots.RunStatusSkipped || skippedSummary.Included != 1 || skippedSummary.Excluded != 1 {
		t.Fatalf("expected skipped worker to preserve state counts, got result=%#v summary=%#v", skippedResult, skippedSummary)
	}

	disableOut := bytes.Buffer{}
	opts.out = &disableOut
	disableCmd := newWatchedRootsCommand(&opts)
	disableCmd.SetArgs([]string{"disable", "notes", "--reason", "project archived", "--yes"})
	if err := disableCmd.Execute(); err != nil {
		t.Fatalf("watched-roots disable failed: %v", err)
	}
	var disableEnvelope response.Envelope[watchedRootDisableResult]
	if err := json.Unmarshal(disableOut.Bytes(), &disableEnvelope); err != nil {
		t.Fatalf("decode disable: %v\n%s", err, disableOut.String())
	}
	if !disableEnvelope.OK || !disableEnvelope.Data.Changed || disableEnvelope.Data.Worker.Enabled {
		t.Fatalf("unexpected disable result %#v", disableEnvelope)
	}
	disabledInstance, err := runtimeStore.LoadInstance(noderuntime.WatchedRootWorkerKey("notes"))
	if err != nil || disabledInstance.Enabled {
		t.Fatalf("expected preserved disabled instance, instance=%#v err=%v", disabledInstance, err)
	}
	if _, err := runtimeStore.LoadCheckpoint(disabledInstance.WorkerKey); err != nil {
		t.Fatalf("disable removed worker checkpoint: %v", err)
	}
	if _, err := watchedroots.NewStore(store.DataDir).LoadLatestSummary("notes"); err != nil {
		t.Fatalf("disable removed watched-root summary: %v", err)
	}

	statusOut.Reset()
	opts.out = &statusOut
	statusCmd = newWatchedRootsCommand(&opts)
	statusCmd.SetArgs([]string{"status", "notes"})
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("disabled watched-root status failed: %v", err)
	}
	if err := json.Unmarshal(statusOut.Bytes(), &statusEnvelope); err != nil {
		t.Fatalf("decode disabled status: %v\n%s", err, statusOut.String())
	}
	if got := statusEnvelope.Data.Roots[0].Status; got != "disabled" {
		t.Fatalf("disabled watched root reported status %q", got)
	}

	disableOut.Reset()
	opts.out = &disableOut
	disableCmd = newWatchedRootsCommand(&opts)
	disableCmd.SetArgs([]string{"disable", "notes", "--reason", "idempotent archive retry", "--yes"})
	if err := disableCmd.Execute(); err != nil {
		t.Fatalf("idempotent watched-roots disable failed: %v", err)
	}
	if err := json.Unmarshal(disableOut.Bytes(), &disableEnvelope); err != nil {
		t.Fatalf("decode idempotent disable: %v\n%s", err, disableOut.String())
	}
	if disableEnvelope.Data.Changed {
		t.Fatalf("idempotent disable unexpectedly changed state: %#v", disableEnvelope.Data)
	}

	opts.out = io.Discard
	disabledRunCmd := newWatchedRootsCommand(&opts)
	disabledRunCmd.SetArgs([]string{"run", "notes", "--once", "--mode", "full"})
	if err := disabledRunCmd.Execute(); err == nil || !strings.Contains(err.Error(), "is disabled") {
		t.Fatalf("manual run of disabled root should fail closed, got %v", err)
	}
}

func TestWatchedRootsApplyPlanConfiguresProjectRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	projectRoot := filepath.Join(dir, "research-notes")
	notesDir := filepath.Join(projectRoot, "notes")
	boxRoot := filepath.Join(dir, "LOOM Box")
	if err := os.MkdirAll(notesDir, 0o700); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}
	if err := os.MkdirAll(boxRoot, 0o700); err != nil {
		t.Fatalf("mkdir box: %v", err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "Project.md"), []byte("# Project\n"), 0o600); err != nil {
		t.Fatalf("write note: %v", err)
	}
	store := Store{
		ConfigPath: filepath.Join(dir, "config.json"),
		StatePath:  filepath.Join(dir, "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	if err := store.SaveConfig(Config{
		MainURL:     "http://10.44.0.2:8080",
		NodeKey:     "workspace",
		DisplayName: "Workspace",
		Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
			filesystemconnector.DefaultSafeRoot("loom_box", boxRoot),
		}},
	}); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	if err := store.SaveState(State{}); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	rootConfig, err := watchedroots.ValidatePortableRootConfig(watchedroots.RootConfig{
		RootKey:          "research_notes__notes",
		DisplayName:      "notes",
		SafeRootKey:      "project",
		RootRelativePath: "notes",
		Include:          []string{"**/*.md"},
		SyncPolicy: watchedroots.SyncPolicy{
			Mode:       watchedroots.SyncModeSelectedFiles,
			ProjectRef: "research-notes",
		},
		IndexPolicy:  watchedroots.IndexPolicy{Mode: watchedroots.IndexModeMarkdownText},
		DeletePolicy: watchedroots.DeletePolicy{Mode: watchedroots.DeleteModeTombstone},
	})
	if err != nil {
		t.Fatalf("ValidatePortableRootConfig failed: %v", err)
	}
	configJSON, err := json.Marshal(rootConfig)
	if err != nil {
		t.Fatalf("marshal root config: %v", err)
	}
	plan := struct {
		ProjectRoot  string                                    `json:"project_root"`
		WatchedRoots []projectcontracts.ProjectWatchedRootItem `json:"watched_roots"`
	}{
		ProjectRoot: projectRoot,
		WatchedRoots: []projectcontracts.ProjectWatchedRootItem{{
			Key:              "notes",
			BackendRootKey:   "research_notes__notes",
			WorkerKey:        noderuntime.WatchedRootWorkerKey("research_notes__notes"),
			OwnerNode:        "workspace",
			SafeRootKey:      "project",
			RootRelativePath: "notes",
			DisplayName:      "notes",
			SyncMode:         watchedroots.SyncModeSelectedFiles,
			IndexMode:        watchedroots.IndexModeMarkdownText,
			DeleteMode:       watchedroots.DeleteModeTombstone,
			ConfigHash:       watchedroots.ConfigHash(rootConfig),
			ConfigJSON:       configJSON,
		}},
	}
	planPath := filepath.Join(dir, "watch-plan.json")
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	if err := os.WriteFile(planPath, planJSON, 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	var out bytes.Buffer
	opts := rootOptions{
		configPath:    store.ConfigPath,
		statePath:     store.StatePath,
		dataDir:       store.DataDir,
		jsonOutput:    true,
		out:           &out,
		errOut:        io.Discard,
		correlationID: "corr-apply-plan",
	}
	cmd := newWatchedRootsCommand(&opts)
	cmd.SetArgs([]string{"apply-plan", planPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("watched-roots apply-plan failed: %v", err)
	}
	var envelope response.Envelope[watchedRootApplyPlanResult]
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("decode apply-plan: %v\n%s", err, out.String())
	}
	if !envelope.OK || len(envelope.Data.Applied) != 1 {
		t.Fatalf("unexpected apply-plan envelope %#v", envelope)
	}
	config, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	safeRoot, ok := filesystemconnector.FindSafeRoot(config.Filesystem, "project")
	wantProjectRoot, err := canonicalDirectoryPath(projectRoot)
	if err != nil {
		t.Fatalf("canonicalDirectoryPath failed: %v", err)
	}
	if !ok || safeRoot.AbsolutePath != wantProjectRoot || !safeRoot.AllowList || !safeRoot.AllowIngest {
		t.Fatalf("project safe root not configured: %#v ok=%t", safeRoot, ok)
	}
	boxSafeRoot, ok := filesystemconnector.FindSafeRoot(config.Filesystem, "loom_box")
	if !ok || boxSafeRoot.AbsolutePath != boxRoot {
		t.Fatalf("loom_box safe root was not preserved: %#v ok=%t", boxSafeRoot, ok)
	}
	instance, err := noderuntime.NewStore(store.DataDir).LoadInstance(noderuntime.WatchedRootWorkerKey("research_notes__notes"))
	if err != nil {
		t.Fatalf("LoadInstance failed: %v", err)
	}
	if instance.LocalRootKey != "notes" {
		t.Fatalf("project local root key = %q, want notes", instance.LocalRootKey)
	}
	if instance.IntervalSeconds != watchedroots.DefaultWorkerIntervalSeconds() {
		t.Fatalf("non-Box watched root interval = %d, want %d", instance.IntervalSeconds, watchedroots.DefaultWorkerIntervalSeconds())
	}
	savedConfig, err := watchedRootConfigFromInstance(instance)
	if err != nil {
		t.Fatalf("decode saved config: %v", err)
	}
	if savedConfig.SyncPolicy.ProjectRef != "research-notes" || savedConfig.RootRelativePath != "notes" {
		t.Fatalf("unexpected saved watched-root config %#v", savedConfig)
	}
	if _, err := watchedroots.NewStore(store.DataDir).LoadCheckpoint("research_notes__notes"); !watchedroots.IsNotExist(err) {
		t.Fatalf("apply-plan should configure root without running it, checkpoint err=%v", err)
	}
}

func TestWatchedRootsApplyPlanUsesFastIntervalForLoomBoxRoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	legacyBoxRoot := filepath.Join(dir, "legacy-box")
	boxRoot := filepath.Join(dir, "LOOM Box")
	if err := os.MkdirAll(legacyBoxRoot, 0o700); err != nil {
		t.Fatalf("mkdir legacy Box: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(boxRoot, "Documents"), 0o700); err != nil {
		t.Fatalf("mkdir documents: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(boxRoot, "Notes"), 0o700); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}
	store := Store{
		ConfigPath: filepath.Join(dir, "config.json"),
		StatePath:  filepath.Join(dir, "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	if err := store.SaveConfig(Config{
		MainURL:     "http://10.44.0.2:8080",
		NodeKey:     "macbook",
		DisplayName: "MacBook",
		Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
			filesystemconnector.DefaultSafeRoot("loom_box", legacyBoxRoot),
		}},
	}); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	if err := store.SaveState(State{}); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	rootConfig, err := watchedroots.ValidatePortableRootConfig(watchedroots.RootConfig{
		RootKey:          "loom_box__documents",
		DisplayName:      "LOOM Box Documents",
		SafeRootKey:      "loom_box",
		RootRelativePath: "Documents",
		Include:          []string{"**/*"},
		Scan:             watchedroots.ScanConfig{MaxHashFileBytes: 512 * 1024 * 1024},
		BackupPolicy: watchedroots.BackupPolicy{
			Mode:          watchedroots.BackupModeIncrementalRaw,
			MaxFileBytes:  512 * 1024 * 1024,
			MaxBatchBytes: 512 * 1024 * 1024,
		},
		IndexPolicy:  watchedroots.IndexPolicy{Mode: watchedroots.IndexModeMetadataOnly},
		DeletePolicy: watchedroots.DeletePolicy{Mode: watchedroots.DeleteModeLocalStateOnly},
	})
	if err != nil {
		t.Fatalf("ValidatePortableRootConfig failed: %v", err)
	}
	configJSON, err := json.Marshal(rootConfig)
	if err != nil {
		t.Fatalf("marshal root config: %v", err)
	}
	notesConfig, err := watchedroots.ValidatePortableRootConfig(watchedroots.RootConfig{
		RootKey:          "loom_box__notes",
		DisplayName:      "LOOM Box Notes",
		SafeRootKey:      "loom_box",
		RootRelativePath: "Notes",
		Include:          []string{"**/*"},
		Scan:             watchedroots.ScanConfig{MaxHashFileBytes: 50 * 1024 * 1024},
		BackupPolicy: watchedroots.BackupPolicy{
			Mode:          watchedroots.BackupModeIncrementalRaw,
			MaxFileBytes:  50 * 1024 * 1024,
			MaxBatchBytes: 50 * 1024 * 1024,
		},
		SyncPolicy:   watchedroots.SyncPolicy{Mode: watchedroots.SyncModeSelectedFiles, ScopeRef: "loom_box:box_test:notes"},
		IndexPolicy:  watchedroots.IndexPolicy{Mode: watchedroots.IndexModeMarkdownText},
		DeletePolicy: watchedroots.DeletePolicy{Mode: watchedroots.DeleteModeTombstone},
	})
	if err != nil {
		t.Fatalf("ValidatePortableRootConfig notes failed: %v", err)
	}
	notesConfigJSON, err := json.Marshal(notesConfig)
	if err != nil {
		t.Fatalf("marshal notes config: %v", err)
	}
	plan := struct {
		ProjectRoot  string                                    `json:"project_root"`
		WatchedRoots []projectcontracts.ProjectWatchedRootItem `json:"watched_roots"`
	}{
		ProjectRoot: boxRoot,
		WatchedRoots: []projectcontracts.ProjectWatchedRootItem{
			{
				Key:              "documents",
				BackendRootKey:   "loom_box__documents",
				WorkerKey:        noderuntime.WatchedRootWorkerKey("loom_box__documents"),
				OwnerNode:        "macbook",
				SafeRootKey:      "loom_box",
				RootRelativePath: "Documents",
				DisplayName:      "LOOM Box Documents",
				BackupMode:       watchedroots.BackupModeIncrementalRaw,
				IndexMode:        watchedroots.IndexModeMetadataOnly,
				DeleteMode:       watchedroots.DeleteModeLocalStateOnly,
				ConfigHash:       watchedroots.ConfigHash(rootConfig),
				ConfigJSON:       configJSON,
			},
			{
				Key:              "notes",
				BackendRootKey:   "loom_box__notes",
				WorkerKey:        noderuntime.WatchedRootWorkerKey("loom_box__notes"),
				OwnerNode:        "macbook",
				SafeRootKey:      "loom_box",
				RootRelativePath: "Notes",
				DisplayName:      "LOOM Box Notes",
				BackupMode:       watchedroots.BackupModeIncrementalRaw,
				SyncMode:         watchedroots.SyncModeSelectedFiles,
				IndexMode:        watchedroots.IndexModeMarkdownText,
				DeleteMode:       watchedroots.DeleteModeTombstone,
				ConfigHash:       watchedroots.ConfigHash(notesConfig),
				ConfigJSON:       notesConfigJSON,
			},
		},
	}
	planPath := filepath.Join(dir, "watch-plan.json")
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	if err := os.WriteFile(planPath, planJSON, 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	var out bytes.Buffer
	opts := rootOptions{
		configPath:    store.ConfigPath,
		statePath:     store.StatePath,
		dataDir:       store.DataDir,
		jsonOutput:    true,
		out:           &out,
		errOut:        io.Discard,
		correlationID: "corr-box-apply-plan",
	}
	cmd := newWatchedRootsCommand(&opts)
	cmd.SetArgs([]string{"apply-plan", planPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("watched-roots apply-plan failed: %v", err)
	}
	instance, err := noderuntime.NewStore(store.DataDir).LoadInstance(noderuntime.WatchedRootWorkerKey("loom_box__documents"))
	if err != nil {
		t.Fatalf("LoadInstance failed: %v", err)
	}
	if instance.IntervalSeconds != 60 {
		t.Fatalf("LOOM Box watched root interval = %d, want 60", instance.IntervalSeconds)
	}
	notesInstance, err := noderuntime.NewStore(store.DataDir).LoadInstance(noderuntime.WatchedRootWorkerKey("loom_box__notes"))
	if err != nil {
		t.Fatalf("LoadInstance notes failed: %v", err)
	}
	if notesInstance.IntervalSeconds != 60 {
		t.Fatalf("LOOM Box notes interval = %d, want 60", notesInstance.IntervalSeconds)
	}
	config, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	safeRoot, ok := filesystemconnector.FindSafeRoot(config.Filesystem, "loom_box")
	if !ok {
		t.Fatalf("loom_box safe root missing after apply-plan")
	}
	wantBoxRoot, err := canonicalDirectoryPath(boxRoot)
	if err != nil {
		t.Fatalf("canonicalize Box root: %v", err)
	}
	if safeRoot.AbsolutePath != wantBoxRoot {
		t.Fatalf("loom_box safe root path = %q, want %q", safeRoot.AbsolutePath, wantBoxRoot)
	}
	if safeRoot.MaxFileBytes != 512*1024*1024 {
		t.Fatalf("loom_box safe root max_file_bytes = %d, want 512MiB", safeRoot.MaxFileBytes)
	}
}

func TestWatchedRootsApplyPlanCreatesBackupContractSafeRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "main-data")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	store := Store{
		ConfigPath: filepath.Join(dir, "config.json"),
		StatePath:  filepath.Join(dir, "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	if err := store.SaveConfig(Config{
		MainURL:     "http://10.44.0.2:8080",
		NodeKey:     "main",
		DisplayName: "Main",
	}); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	if err := store.SaveState(State{}); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	item, err := backupcontracts.WatchedRootItem(backupcontracts.Contract{
		Key:         "field-data",
		DisplayName: "Field Data Backup",
		OwnerNode:   "main",
		Target: backupcontracts.TargetSpec{
			Scope: backupcontracts.TargetScopeOwnerNodeAbsolute,
			Path:  target,
		},
		Backup: backupcontracts.BackupPolicy{
			Mode:          backupcontracts.BackupModeIncrementalRaw,
			MaxFileBytes:  2 * 1024 * 1024,
			MaxBatchBytes: 2 * 1024 * 1024,
		},
	}, filepath.Join(dir, "LOOM Box", ".loom", "contracts", "backup", "field-data.yaml"), backupcontracts.WatchPlanOptions{
		BoxRoot:   filepath.Join(dir, "LOOM Box"),
		BoxID:     "box_main",
		OwnerNode: "main",
	})
	if err != nil {
		t.Fatalf("WatchedRootItem returned error: %v", err)
	}
	plan := struct {
		ProjectRoot  string                                    `json:"project_root"`
		WatchedRoots []projectcontracts.ProjectWatchedRootItem `json:"watched_roots"`
	}{
		WatchedRoots: []projectcontracts.ProjectWatchedRootItem{item},
	}
	planPath := filepath.Join(dir, "backup-watch-plan.json")
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	if err := os.WriteFile(planPath, planJSON, 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	var out bytes.Buffer
	opts := rootOptions{
		configPath: store.ConfigPath,
		statePath:  store.StatePath,
		dataDir:    store.DataDir,
		jsonOutput: true,
		out:        &out,
		errOut:     io.Discard,
	}
	cmd := newWatchedRootsCommand(&opts)
	cmd.SetArgs([]string{"apply-plan", planPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("watched-roots apply-plan failed: %v", err)
	}
	var envelope response.Envelope[watchedRootApplyPlanResult]
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("decode apply-plan: %v\n%s", err, out.String())
	}
	if !envelope.OK || len(envelope.Data.Applied) != 1 {
		t.Fatalf("unexpected apply-plan envelope %#v", envelope)
	}
	config, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	safeRoot, ok := filesystemconnector.FindSafeRoot(config.Filesystem, "backup_field-data")
	if !ok {
		t.Fatalf("backup safe root was not configured")
	}
	wantTarget, err := canonicalDirectoryPath(target)
	if err != nil {
		t.Fatalf("canonicalDirectoryPath failed: %v", err)
	}
	if safeRoot.AbsolutePath != wantTarget || !safeRoot.PrivateBackupOnly || safeRoot.MaxFileBytes != 2*1024*1024 {
		t.Fatalf("unexpected backup safe root: %#v", safeRoot)
	}
	instance, err := noderuntime.NewStore(store.DataDir).LoadInstance(noderuntime.WatchedRootWorkerKey("loom_box_backup__field-data"))
	if err != nil {
		t.Fatalf("LoadInstance failed: %v", err)
	}
	savedConfig, err := watchedRootConfigFromInstance(instance)
	if err != nil {
		t.Fatalf("decode saved config: %v", err)
	}
	if savedConfig.SafeRootKey != "backup_field-data" || savedConfig.RootRelativePath != "." || savedConfig.SyncPolicy.Mode != watchedroots.SyncModeNone {
		t.Fatalf("unexpected saved config: %#v", savedConfig)
	}
}

func TestWatchedRootsRunQueuesSelectedFileSyncOutputs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatalf("mkdir vault: %v", err)
	}
	projectPath := filepath.Join(vault, "Project.md")
	if err := os.WriteFile(projectPath, []byte("# Project\n"), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	store := Store{
		ConfigPath: filepath.Join(dir, "config.json"),
		StatePath:  filepath.Join(dir, "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	config := Config{
		MainURL:     "http://10.44.0.2:8080",
		NodeKey:     "workspace-test",
		DisplayName: "Workspace Test",
		Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
			filesystemconnector.DefaultSafeRoot("slice10", vault),
		}},
	}
	if err := store.SaveConfig(config); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	state := State{NodeID: "node_test"}
	if err := store.SaveState(state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	opts := rootOptions{
		configPath: store.ConfigPath,
		statePath:  store.StatePath,
		dataDir:    store.DataDir,
		jsonOutput: true,
		out:        io.Discard,
		errOut:     io.Discard,
	}
	addCmd := newWatchedRootsCommand(&opts)
	addCmd.SetArgs([]string{
		"add", "notes",
		"--safe-root", "slice10",
		"--path", ".",
		"--include", "**/*.md",
		"--sync-mode", watchedroots.SyncModeSelectedFiles,
		"--project", "project_test",
		"--index-mode", watchedroots.IndexModeMarkdownText,
		"--delete-mode", watchedroots.DeleteModeTombstone,
	})
	if err := addCmd.Execute(); err != nil {
		t.Fatalf("watched-roots add failed: %v", err)
	}

	var runOut bytes.Buffer
	opts.out = &runOut
	runCmd := newWatchedRootsCommand(&opts)
	runCmd.SetArgs([]string{"run", "notes", "--once", "--mode", "full", "--stability-window", "0s"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("watched-roots run failed: %v", err)
	}
	var runEnvelope response.Envelope[watchedroots.ScanResult]
	if err := json.Unmarshal(runOut.Bytes(), &runEnvelope); err != nil {
		t.Fatalf("decode first run: %v\n%s", err, runOut.String())
	}
	if runEnvelope.Data.OutputPlan == nil ||
		runEnvelope.Data.OutputPlan.Counts.SyncObjects != 1 ||
		runEnvelope.Data.OutputPlan.Actions[0].Status != watchedroots.OutputStatusQueued {
		t.Fatalf("expected queued sync output, got %#v", runEnvelope.Data.OutputPlan)
	}
	status, err := store.LocalSyncStatus(config, state)
	if err != nil {
		t.Fatalf("LocalSyncStatus failed: %v", err)
	}
	if status.Counts.Objects != 1 || status.Counts.Pending != 1 {
		t.Fatalf("expected one pending local sync object, got %#v", status.Counts)
	}
	pathState, err := watchedroots.NewStore(store.DataDir).LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState failed: %v", err)
	}
	if pathState.LocalObjectID == "" ||
		pathState.LocalVersionID == "" ||
		pathState.LastQueuedHashURI != pathState.ContentHashURI ||
		pathState.SyncStatus != watchedroots.OutputStatusQueued ||
		pathState.IndexStatus != watchedroots.OutputStatusQueued {
		t.Fatalf("expected queued output state, got %#v", pathState)
	}

	runOut.Reset()
	opts.out = &runOut
	runCmd = newWatchedRootsCommand(&opts)
	runCmd.SetArgs([]string{"run", "notes", "--once", "--mode", "full", "--stability-window", "0s"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("watched-roots second run failed: %v", err)
	}
	if err := json.Unmarshal(runOut.Bytes(), &runEnvelope); err != nil {
		t.Fatalf("decode second run: %v\n%s", err, runOut.String())
	}
	if runEnvelope.Data.OutputPlan == nil ||
		runEnvelope.Data.OutputPlan.Counts.SyncObjects != 0 ||
		runEnvelope.Data.OutputPlan.Counts.AlreadyCurrent != 1 {
		t.Fatalf("expected already-current output plan, got %#v", runEnvelope.Data.OutputPlan)
	}
	status, err = store.LocalSyncStatus(config, state)
	if err != nil {
		t.Fatalf("LocalSyncStatus second failed: %v", err)
	}
	if status.Counts.Objects != 1 || status.Counts.Pending != 1 {
		t.Fatalf("expected no duplicate local sync object, got %#v", status.Counts)
	}
	previousObjectID := pathState.LocalObjectID
	previousVersionID := pathState.LocalVersionID

	if err := os.WriteFile(projectPath, []byte("# Project changed\n"), 0o600); err != nil {
		t.Fatalf("modify project: %v", err)
	}
	runOut.Reset()
	opts.out = &runOut
	runCmd = newWatchedRootsCommand(&opts)
	runCmd.SetArgs([]string{"run", "notes", "--once", "--mode", "full", "--stability-window", "0s"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("watched-roots changed run failed: %v", err)
	}
	if err := json.Unmarshal(runOut.Bytes(), &runEnvelope); err != nil {
		t.Fatalf("decode changed run: %v\n%s", err, runOut.String())
	}
	if runEnvelope.Data.OutputPlan == nil ||
		runEnvelope.Data.OutputPlan.Counts.SyncObjects != 1 ||
		runEnvelope.Data.OutputPlan.Actions[0].LocalObjectID != previousObjectID ||
		runEnvelope.Data.OutputPlan.Actions[0].LocalVersionID == previousVersionID {
		t.Fatalf("expected changed file to reuse object and create a new version, got %#v", runEnvelope.Data.OutputPlan)
	}
	status, err = store.LocalSyncStatus(config, state)
	if err != nil {
		t.Fatalf("LocalSyncStatus changed failed: %v", err)
	}
	if status.Counts.Objects != 2 || status.Counts.Pending != 2 {
		t.Fatalf("expected second pending local sync version, got %#v", status.Counts)
	}
	pathState, err = watchedroots.NewStore(store.DataDir).LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState changed failed: %v", err)
	}
	if pathState.LocalObjectID != previousObjectID ||
		pathState.LocalVersionID == previousVersionID ||
		pathState.LastQueuedHashURI != pathState.ContentHashURI {
		t.Fatalf("expected stable object identity and new version state, got %#v", pathState)
	}

	if err := os.Remove(projectPath); err != nil {
		t.Fatalf("remove project: %v", err)
	}
	runOut.Reset()
	opts.out = &runOut
	runCmd = newWatchedRootsCommand(&opts)
	runCmd.SetArgs([]string{"run", "notes", "--once", "--mode", "full", "--stability-window", "0s"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("watched-roots delete run failed: %v", err)
	}
	if err := json.Unmarshal(runOut.Bytes(), &runEnvelope); err != nil {
		t.Fatalf("decode delete run: %v\n%s", err, runOut.String())
	}
	if runEnvelope.Data.OutputPlan == nil ||
		runEnvelope.Data.OutputPlan.Counts.DeletionRequests != 1 ||
		runEnvelope.Data.OutputPlan.Actions[0].ActionKind != watchedroots.OutputActionDeletionRequest {
		t.Fatalf("expected deletion request output, got %#v", runEnvelope.Data.OutputPlan)
	}
	status, err = store.LocalSyncStatus(config, state)
	if err != nil {
		t.Fatalf("LocalSyncStatus delete failed: %v", err)
	}
	if status.Counts.Deletions != 1 || status.Counts.Pending != 3 {
		t.Fatalf("expected pending deletion request, got %#v", status.Counts)
	}
	pathState, err = watchedroots.NewStore(store.DataDir).LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState deleted failed: %v", err)
	}
	if pathState.Status != watchedroots.PathStatusDeleted ||
		pathState.DeletionStatus != watchedroots.OutputStatusQueued ||
		pathState.DeletionRequestID == "" {
		t.Fatalf("expected queued deletion path state, got %#v", pathState)
	}
}

func TestWatchedRootsRunQueuesBackupOutputsAndStatusCLI(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatalf("mkdir vault: %v", err)
	}
	projectPath := filepath.Join(vault, "Project.md")
	if err := os.WriteFile(projectPath, []byte("# Project\n"), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	store := Store{
		ConfigPath: filepath.Join(dir, "config.json"),
		StatePath:  filepath.Join(dir, "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	config := Config{
		MainURL:     "http://10.44.0.2:8080",
		NodeKey:     "workspace-test",
		DisplayName: "Workspace Test",
		Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
			filesystemconnector.DefaultSafeRoot("slice11", vault),
		}},
	}
	if err := store.SaveConfig(config); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	state := State{NodeID: "node_test"}
	if err := store.SaveState(state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	opts := rootOptions{
		configPath: store.ConfigPath,
		statePath:  store.StatePath,
		dataDir:    store.DataDir,
		jsonOutput: true,
		out:        io.Discard,
		errOut:     io.Discard,
	}
	addCmd := newWatchedRootsCommand(&opts)
	addCmd.SetArgs([]string{
		"add", "notes",
		"--safe-root", "slice11",
		"--path", ".",
		"--include", "**/*.md",
		"--backup-mode", watchedroots.BackupModeIncrementalRaw,
		"--backup-max-file-bytes", "1048576",
		"--backup-max-batch-bytes", "1048576",
		"--sync-mode", watchedroots.SyncModeNone,
		"--index-mode", watchedroots.IndexModeNone,
	})
	if err := addCmd.Execute(); err != nil {
		t.Fatalf("watched-roots add failed: %v", err)
	}

	var runOut bytes.Buffer
	opts.out = &runOut
	runCmd := newWatchedRootsCommand(&opts)
	runCmd.SetArgs([]string{"run", "notes", "--once", "--mode", "full", "--stability-window", "0s"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("watched-roots run failed: %v", err)
	}
	var runEnvelope response.Envelope[watchedroots.ScanResult]
	if err := json.Unmarshal(runOut.Bytes(), &runEnvelope); err != nil {
		t.Fatalf("decode run: %v\n%s", err, runOut.String())
	}
	if runEnvelope.Data.OutputPlan == nil ||
		runEnvelope.Data.OutputPlan.Counts.BackupFiles != 1 ||
		runEnvelope.Data.OutputPlan.Counts.SyncObjects != 0 ||
		runEnvelope.Data.BackupStatus == nil ||
		runEnvelope.Data.BackupStatus.Pending != 1 {
		t.Fatalf("expected queued backup output, got result=%#v", runEnvelope.Data)
	}
	status, err := store.LocalWatchedRootBackupStatus(config, state, "notes")
	if err != nil {
		t.Fatalf("LocalWatchedRootBackupStatus failed: %v", err)
	}
	if status.Counts.Artifacts != 1 || status.Counts.Batches != 1 || status.Counts.Pending != 1 {
		t.Fatalf("unexpected local backup status %#v", status.Counts)
	}
	pathState, err := watchedroots.NewStore(store.DataDir).LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState failed: %v", err)
	}
	if pathState.BackupStatus != watchedroots.OutputStatusQueued ||
		pathState.LastQueuedBackupHashURI != pathState.ContentHashURI ||
		pathState.LocalBackupBatchID == "" ||
		pathState.LocalBackupOutboxID == "" {
		t.Fatalf("expected queued backup path state, got %#v", pathState)
	}

	var statusOut bytes.Buffer
	opts.out = &statusOut
	statusCmd := newWatchedRootsCommand(&opts)
	statusCmd.SetArgs([]string{"backups", "status", "notes"})
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("watched-roots backups status failed: %v", err)
	}
	var statusEnvelope response.Envelope[LocalWatchedRootBackupStatus]
	if err := json.Unmarshal(statusOut.Bytes(), &statusEnvelope); err != nil {
		t.Fatalf("decode backup status: %v\n%s", err, statusOut.String())
	}
	if statusEnvelope.Data.Counts.Pending != 1 || statusEnvelope.Data.Counts.Artifacts != 1 {
		t.Fatalf("unexpected backup status envelope %#v", statusEnvelope.Data)
	}
	if statusEnvelope.Data.Limits == nil || statusEnvelope.Data.Limits.Mode != watchedroots.BackupModeIncrementalRaw {
		t.Fatalf("expected backup status limits, got %#v", statusEnvelope.Data.Limits)
	}

	var outboxOut bytes.Buffer
	opts.out = &outboxOut
	outboxCmd := newWatchedRootsCommand(&opts)
	outboxCmd.SetArgs([]string{"backups", "outbox", "notes"})
	if err := outboxCmd.Execute(); err != nil {
		t.Fatalf("watched-roots backups outbox failed: %v", err)
	}
	var outboxEnvelope response.Envelope[[]LocalWatchedRootBackupOutboxItem]
	if err := json.Unmarshal(outboxOut.Bytes(), &outboxEnvelope); err != nil {
		t.Fatalf("decode backup outbox: %v\n%s", err, outboxOut.String())
	}
	if len(outboxEnvelope.Data) != 1 || outboxEnvelope.Data[0].RootKey != "notes" {
		t.Fatalf("unexpected backup outbox %#v", outboxEnvelope.Data)
	}

	var artifactsOut bytes.Buffer
	opts.out = &artifactsOut
	artifactsCmd := newWatchedRootsCommand(&opts)
	artifactsCmd.SetArgs([]string{"backups", "artifacts", "notes"})
	if err := artifactsCmd.Execute(); err != nil {
		t.Fatalf("watched-roots backups artifacts failed: %v", err)
	}
	var artifactsEnvelope response.Envelope[[]LocalWatchedRootBackupArtifact]
	if err := json.Unmarshal(artifactsOut.Bytes(), &artifactsEnvelope); err != nil {
		t.Fatalf("decode backup artifacts: %v\n%s", err, artifactsOut.String())
	}
	if len(artifactsEnvelope.Data) != 1 || artifactsEnvelope.Data[0].RootKey != "notes" {
		t.Fatalf("unexpected backup artifacts %#v", artifactsEnvelope.Data)
	}
}

func TestWatchedRootsRunFlushesObjectsAndDeletionRequests(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatalf("mkdir vault: %v", err)
	}
	projectPath := filepath.Join(vault, "Project.md")
	if err := os.WriteFile(projectPath, []byte("# Project\n\nflush token one\n"), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	var uploads []loomsync.SyncedObjectInput
	var deletions []loomsync.DeletionRequestInput
	var reports []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC()
		switch r.URL.Path {
		case "/v1/node-agent/sync/object-upload":
			var input loomsync.SyncedObjectInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode object upload: %v", err)
			}
			uploads = append(uploads, input)
			versionID := "version_first"
			if len(uploads) > 1 {
				versionID = "version_second"
			}
			response.WriteJSON(w, http.StatusCreated, response.Success("corr_test", loomsync.SyncedObjectResult{
				Batch: loomsync.SyncBatch{
					SyncBatchID:   "sync_batch_object",
					OriginNodeID:  "node_test",
					BatchKind:     loomsync.BatchKindObjectBlobs,
					Status:        loomsync.BatchStatusAccepted,
					ItemCount:     1,
					AcceptedCount: 1,
					ReceivedAt:    now,
					CompletedAt:   &now,
					Metadata:      json.RawMessage(`{}`),
				},
				Item: loomsync.SyncItemResult{
					SyncBatchItemID: "sync_batch_item_object",
					LocalRef:        input.LocalObjectRef,
					ItemKind:        loomsync.ItemKindObjectBlob,
					StreamName:      loomsync.StreamObjectBlobs,
					LocalSequence:   input.LocalSequence,
					Status:          loomsync.ItemStatusAccepted,
					GlobalRef:       "object_main",
					PayloadHash:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					Metadata: objectJSON(map[string]any{
						"local_outbox_id":   metadataString(input.Metadata, "local_outbox_id"),
						"local_version_ref": input.LocalVersionRef,
						"object_id":         "object_main",
						"object_version_id": versionID,
						"blob_id":           "blob_main",
						"hash_uri":          input.HashURI,
						"index_status":      "queued",
					}),
				},
				Cursor: loomsync.SyncCursor{
					SyncCursorID:         "sync_cursor_object",
					NodeID:               "node_test",
					StreamName:           loomsync.StreamObjectBlobs,
					LastAcceptedSequence: input.LocalSequence,
					UpdatedAt:            now,
					Metadata:             json.RawMessage(`{}`),
				},
				ObjectID:    "object_main",
				VersionID:   versionID,
				BlobID:      "blob_main",
				HashURI:     input.HashURI,
				IndexStatus: "queued",
			}))
		case "/v1/node-agent/sync/deletion-request":
			var input loomsync.DeletionRequestInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode deletion request: %v", err)
			}
			deletions = append(deletions, input)
			response.WriteJSON(w, http.StatusCreated, response.Success("corr_test", loomsync.DeletionRequestResult{
				Request: loomsync.DeletionRequest{
					DeletionRequestID: "deletion_request_main",
					OriginNodeID:      "node_test",
					TargetKind:        input.TargetKind,
					TargetRef:         input.TargetRef,
					RequestedAction:   input.RequestedAction,
					Status:            "recorded",
					Reason:            input.Reason,
					RequestedAt:       now,
					Metadata:          input.Metadata,
				},
			}))
		case "/v1/node-agent/watched-roots/report":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode watched-root report: %v", err)
			}
			reports = append(reports, input)
			response.WriteJSON(w, http.StatusCreated, response.Success("corr_test", map[string]any{
				"root": map[string]any{
					"watched_root_id": "watched_root_main",
					"node_id":         "node_test",
					"root_key":        input["root_key"],
					"worker_key":      input["worker_key"],
					"status":          input["status"],
				},
				"findings": []any{},
			}))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	store := Store{
		ConfigPath: filepath.Join(dir, "config.json"),
		StatePath:  filepath.Join(dir, "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	config := Config{
		MainURL:     server.URL,
		NodeKey:     "workspace-test",
		DisplayName: "Workspace Test",
		Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
			filesystemconnector.DefaultSafeRoot("slice10", vault),
		}},
	}
	if err := store.SaveConfig(config); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	state := State{NodeID: "node_test", CredentialToken: "node_cred_test"}
	if err := store.SaveState(state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	opts := rootOptions{
		configPath: store.ConfigPath,
		statePath:  store.StatePath,
		dataDir:    store.DataDir,
		jsonOutput: true,
		out:        io.Discard,
		errOut:     io.Discard,
	}
	addCmd := newWatchedRootsCommand(&opts)
	addCmd.SetArgs([]string{
		"add", "notes",
		"--safe-root", "slice10",
		"--path", ".",
		"--include", "**/*.md",
		"--sync-mode", watchedroots.SyncModeSelectedFiles,
		"--project", "project_test",
		"--index-mode", watchedroots.IndexModeMarkdownText,
		"--delete-mode", watchedroots.DeleteModeTombstone,
	})
	if err := addCmd.Execute(); err != nil {
		t.Fatalf("watched-roots add failed: %v", err)
	}

	var runOut bytes.Buffer
	opts.out = &runOut
	runCmd := newWatchedRootsCommand(&opts)
	runCmd.SetArgs([]string{"run", "notes", "--once", "--mode", "full", "--stability-window", "0s", "--flush"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("watched-roots flush run failed: %v", err)
	}
	var runEnvelope response.Envelope[watchedroots.ScanResult]
	if err := json.Unmarshal(runOut.Bytes(), &runEnvelope); err != nil {
		t.Fatalf("decode flush run: %v\n%s", err, runOut.String())
	}
	if len(uploads) != 1 || runEnvelope.Data.OutputFlush == nil || runEnvelope.Data.OutputFlush.Status != watchedroots.OutputStatusRecorded {
		t.Fatalf("expected one flushed upload, uploads=%d flush=%#v", len(uploads), runEnvelope.Data.OutputFlush)
	}
	if runEnvelope.Data.MainReport == nil || runEnvelope.Data.MainReport.Status != watchedroots.OutputStatusRecorded || len(reports) != 1 {
		t.Fatalf("expected recorded main report, reports=%d report=%#v", len(reports), runEnvelope.Data.MainReport)
	}
	pathState, err := watchedroots.NewStore(store.DataDir).LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState failed: %v", err)
	}
	if pathState.MainObjectID != "object_main" ||
		pathState.MainVersionID != "version_first" ||
		pathState.LastSyncedHashURI != pathState.ContentHashURI ||
		pathState.IndexStatus != "queued" {
		t.Fatalf("expected accepted watched-root sync state, got %#v", pathState)
	}
	firstLocalObjectID := uploads[0].LocalObjectRef
	firstVersionID := uploads[0].LocalVersionRef

	if err := os.WriteFile(projectPath, []byte("# Project\n\nflush token two\n"), 0o600); err != nil {
		t.Fatalf("modify project: %v", err)
	}
	runOut.Reset()
	opts.out = &runOut
	runCmd = newWatchedRootsCommand(&opts)
	runCmd.SetArgs([]string{"run", "notes", "--once", "--mode", "full", "--stability-window", "0s", "--flush"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("watched-roots second flush run failed: %v", err)
	}
	if len(uploads) != 2 ||
		uploads[1].LocalObjectRef != firstLocalObjectID ||
		uploads[1].LocalVersionRef == firstVersionID {
		t.Fatalf("expected changed flush to reuse local object with new version, uploads=%#v", uploads)
	}

	if err := os.Remove(projectPath); err != nil {
		t.Fatalf("remove project: %v", err)
	}
	runOut.Reset()
	opts.out = &runOut
	runCmd = newWatchedRootsCommand(&opts)
	runCmd.SetArgs([]string{"run", "notes", "--once", "--mode", "full", "--stability-window", "0s", "--flush"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("watched-roots deletion flush run failed: %v", err)
	}
	if len(deletions) != 1 {
		t.Fatalf("expected one deletion request, got %#v", deletions)
	}
	if deletions[0].TargetRef != "object_main" ||
		!strings.HasPrefix(deletions[0].IdempotencyKey, "node-agent.watched-root.delete.node_test.notes.") {
		t.Fatalf("unexpected deletion request input %#v", deletions[0])
	}
	pathState, err = watchedroots.NewStore(store.DataDir).LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState deleted failed: %v", err)
	}
	if pathState.DeletionStatus != watchedroots.OutputStatusRecorded ||
		pathState.DeletionRequestID != "deletion_request_main" {
		t.Fatalf("expected recorded deletion status, got %#v", pathState)
	}
	status, err := store.LocalSyncStatus(config, state)
	if err != nil {
		t.Fatalf("LocalSyncStatus failed: %v", err)
	}
	if status.Counts.Pending != 0 || status.Counts.Accepted != 3 {
		t.Fatalf("expected all flushed items accepted, got %#v", status.Counts)
	}
	runOut.Reset()
	opts.out = &runOut
	runCmd = newWatchedRootsCommand(&opts)
	runCmd.SetArgs([]string{"run", "notes", "--once", "--mode", "full", "--stability-window", "0s", "--flush"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("watched-roots repeated deletion flush run failed: %v", err)
	}
	if len(deletions) != 1 {
		t.Fatalf("recorded deletion should not be queued again, got %#v", deletions)
	}
}
