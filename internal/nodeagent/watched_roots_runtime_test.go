package nodeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	loomsync "loom.local/loom/internal/sync"
)

func TestWatchedRootReadinessDoesNotDiscoverSourcePolicies(t *testing.T) {
	store, config, state, dir := localSyncObjectTestStore(t)
	source := filepath.Join(dir, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	safe := filesystemconnector.DefaultSafeRoot("notes", source)
	safe.MaxFileBytes = 1024 * 1024
	config.Filesystem.SafeRoots = []filesystemconnector.SafeRoot{safe}
	root, err := watchedroots.ValidateRootConfigForRun(watchedroots.RootConfig{
		RootKey: "notes", SafeRootKey: "notes",
		IgnorePolicy: watchedroots.IgnorePolicy{Profile: "managed", DiscoverUserRules: true},
		Watch:        watchedroots.WatchConfig{Enabled: true, Backend: watchedroots.WatchBackendAuto},
		Scan:         watchedroots.ScanConfig{FullRescanInterval: "6h", StabilityWindow: "2s", MaxHashFileBytes: 2 * 1024 * 1024},
	}, config.Filesystem)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(state); err != nil {
		t.Fatal(err)
	}
	// A recursive resolver would refuse this policy entry. Readiness must not
	// traverse it; actual execution and watch registration must still refuse it.
	if err := os.Mkdir(filepath.Join(source, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(source, "nested", ".loomignore")); err != nil {
		t.Fatal(err)
	}
	rawConfig := root.Config
	rawConfig.Scan.MaxHashFileBytes = 2 * 1024 * 1024
	raw, err := json.Marshal(rawConfig)
	if err != nil {
		t.Fatal(err)
	}
	instance := noderuntime.WorkerInstance{WorkerKey: noderuntime.WatchedRootWorkerKey("notes"), Kind: noderuntime.KindWatchedRoot, Enabled: true, IntervalSeconds: 900, ConfigJSON: raw}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	ws := watchedroots.NewStore(store.DataDir)
	env := noderuntime.RuntimeEnv(store.ConfigPath, store.StatePath, store.DataDir, state.NodeID, config.NodeKey, config.MainURL, true)
	now := time.Now().UTC()
	last, full := now.Add(-10*time.Second), now.Add(-time.Minute)
	base := watchedroots.RootCheckpoint{ConfigHash: root.ConfigHash, RootReachable: true, LastFullRescanAt: &full, OutputsCurrent: true}
	for _, tc := range []struct {
		name     string
		mutate   func(*watchedroots.RootCheckpoint)
		hint     *watchedroots.DirtyHint
		pending  bool
		watching bool
		want     time.Time
	}{
		{name: "idle", watching: true, want: full.Add(6 * time.Hour)},
		{name: "fallback", want: last.Add(15 * time.Minute)},
		{name: "dirty", watching: true, hint: &watchedroots.DirtyHint{RelativePath: "note.md", LastSeenAt: now}, want: now.Add(2 * time.Second)},
		{name: "hash", watching: true, mutate: func(cp *watchedroots.RootCheckpoint) {
			ready := now.Add(3 * time.Second)
			cp.PendingRescan, cp.DeferredHashPaths, cp.NextHashReadyAt = true, []string{"note.md"}, &ready
		}, want: now.Add(3 * time.Second)},
		{name: "planning", watching: true, mutate: func(cp *watchedroots.RootCheckpoint) { cp.OutputsCurrent = false }, want: last.Add(time.Minute)},
		{name: "transport", watching: true, pending: true, want: last.Add(time.Minute)},
		{name: "fallback transport", pending: true, want: last.Add(time.Minute)},
		{name: "fallback output continuation", mutate: func(cp *watchedroots.RootCheckpoint) { cp.OutputContinuationReady = true }, want: last.Add(time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := base
			if tc.mutate != nil {
				tc.mutate(&cp)
			}
			if err := ws.SaveCheckpoint("notes", cp); err != nil {
				t.Fatal(err)
			}
			if err := ws.ClearDirtyHints("notes", nil); err != nil {
				t.Fatal(err)
			}
			if tc.hint != nil {
				if err := ws.AddDirtyHint("notes", *tc.hint); err != nil {
					t.Fatal(err)
				}
			}
			if tc.pending {
				if err := store.SaveSyncOutbox([]LocalSyncOutboxItem{{LocalOutboxID: "pending", ItemKind: loomsync.ItemKindDeletionRequest, Status: localSyncStatusPending, PayloadJSON: json.RawMessage(`{"root_key":"notes"}`)}}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := (watchedRootRuntime{}).NextRunAt(runtimeStore, env, instance, now, last, tc.watching)
			if err != nil || !got.Equal(tc.want) {
				t.Fatalf("deadline=%v want=%v err=%v", got, tc.want, err)
			}
		})
	}
	if watch, err := (watchedRootRuntime{}).Watch(t.Context(), runtimeStore, env, instance, nil); err == nil {
		if watch != nil {
			_ = watch.Close()
		}
		t.Fatal("watch registration bypassed source policy validation")
	}
	result, err := (watchedRootRuntime{}).RunOnce(t.Context(), runtimeStore, env, instance)
	if err != nil || result.HealthStatus != noderuntime.WorkerStatusBlocked || !strings.Contains(result.Message, "must not be a symlink") {
		t.Fatalf("execution bypassed source policy validation: %+v, %v", result, err)
	}
}
