package cloudstorage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotBackendStatusDefaultMissingCacheAvoidsBorg(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	called := false
	report, err := SnapshotBackendStatus(context.Background(), SnapshotBackendStatusInput{
		Config: cfg,
		Runner: BorgCommandRunner{Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
			called = true
			return nil, nil
		}},
		Now: func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("SnapshotBackendStatus returned error: %v", err)
	}
	if called {
		t.Fatal("cached/default backend status should not run Borg")
	}
	if report.Status != RemoteStateUnknown {
		t.Fatalf("status = %q, want unknown", report.Status)
	}
	if report.Checks["borg_backend_cache"] != "missing" {
		t.Fatalf("checks = %#v", report.Checks)
	}
}

func TestSnapshotBackendStatusDefaultReadsCacheAvoidsBorg(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	now := time.Unix(200, 0).UTC()
	liveCalls := 0
	liveReport, err := SnapshotBackendStatus(context.Background(), SnapshotBackendStatusInput{
		Config: cfg,
		Runner: BorgCommandRunner{Exec: func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
			liveCalls++
			if command.Name() != "list" {
				t.Fatalf("command = %#v", command.Args)
			}
			return []byte(`{"archives":[{"name":"a","time":"2026-06-01T08:00:00Z"},{"name":"b","time":"2026-06-02T08:00:00Z"}]}`), nil
		}},
		Now:  func() time.Time { return now },
		Live: true,
	})
	if err != nil {
		t.Fatalf("live SnapshotBackendStatus returned error: %v", err)
	}
	if liveReport.ArchiveCount != 2 || liveCalls != 1 {
		t.Fatalf("live report/calls = %#v calls=%d", liveReport, liveCalls)
	}

	defaultCalls := 0
	cachedReport, err := SnapshotBackendStatus(context.Background(), SnapshotBackendStatusInput{
		Config: cfg,
		Runner: BorgCommandRunner{Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
			defaultCalls++
			return nil, errors.New("should not run")
		}},
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatalf("cached SnapshotBackendStatus returned error: %v", err)
	}
	if defaultCalls != 0 {
		t.Fatalf("cached/default backend status ran Borg %d times", defaultCalls)
	}
	if !cachedReport.Cached {
		t.Fatalf("cached flag = false: %#v", cachedReport)
	}
	if cachedReport.ArchiveCount != 2 {
		t.Fatalf("archive count = %d, want 2", cachedReport.ArchiveCount)
	}
	if cachedReport.CacheAgeSeconds != 60 {
		t.Fatalf("cache age = %d, want 60", cachedReport.CacheAgeSeconds)
	}
}

func TestSnapshotBackendStatusLiveRunsBorgOnce(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	calls := 0
	report, err := SnapshotBackendStatus(context.Background(), SnapshotBackendStatusInput{
		Config: cfg,
		Runner: BorgCommandRunner{Exec: func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
			calls++
			if command.Name() != "list" {
				t.Fatalf("command = %#v", command.Args)
			}
			return []byte(`{"archives":[{"name":"a","time":"2026-06-01T08:00:00Z"}]}`), nil
		}},
		Now:  func() time.Time { return time.Unix(300, 0).UTC() },
		Live: true,
	})
	if err != nil {
		t.Fatalf("SnapshotBackendStatus returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("borg calls = %d, want 1", calls)
	}
	if !report.Initialized || report.ArchiveCount != 1 {
		t.Fatalf("report = %#v", report)
	}
}

func TestSnapshotBackendStatusTransportFailureRecordsCooldown(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	now := time.Unix(400, 0).UTC()
	report, err := SnapshotBackendStatus(context.Background(), SnapshotBackendStatusInput{
		Config: cfg,
		Runner: BorgCommandRunner{Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
			return nil, errors.New("dial tcp 91.98.240.163:23: connect: connection refused")
		}},
		Now:  func() time.Time { return now },
		Live: true,
	})
	if err != nil {
		t.Fatalf("SnapshotBackendStatus returned error: %v", err)
	}
	if report.Status != "not_ready" {
		t.Fatalf("status = %q", report.Status)
	}
	state, err := LoadRemoteState(cfg)
	if err != nil {
		t.Fatalf("LoadRemoteState returned error: %v", err)
	}
	if state.State != RemoteStateCoolingDown || state.LastErrorClass != RemoteErrorConnectionRefused {
		t.Fatalf("remote state = %#v", state)
	}
}

func TestSnapshotBackendStatusLocalPermissionFailureRecordsDegraded(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	now := time.Unix(500, 0).UTC()
	_, err := SnapshotBackendStatus(context.Background(), SnapshotBackendStatusInput{
		Config: cfg,
		Runner: BorgCommandRunner{Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
			return nil, errors.New("failed to read private key file: open /var/lib/loom/cloud/key: permission denied")
		}},
		Now:  func() time.Time { return now },
		Live: true,
	})
	if err != nil {
		t.Fatalf("SnapshotBackendStatus returned error: %v", err)
	}
	state, err := LoadRemoteState(cfg)
	if err != nil {
		t.Fatalf("LoadRemoteState returned error: %v", err)
	}
	if state.State != RemoteStateDegraded || state.LastErrorClass != RemoteErrorPermissionLocal {
		t.Fatalf("remote state = %#v", state)
	}
	if state.NextLiveCheckAfter != nil {
		t.Fatalf("local permission failure should not set cooldown: %#v", state.NextLiveCheckAfter)
	}
}

func TestSnapshotBackendStatusLiveRespectsCooldown(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	now := time.Unix(600, 0).UTC()
	if err := RecordRemoteFailureAt(cfg, errors.New("connect: connection refused"), now, "test"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	report, err := SnapshotBackendStatus(context.Background(), SnapshotBackendStatusInput{
		Config: cfg,
		Runner: BorgCommandRunner{Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
			calls++
			return nil, nil
		}},
		Now:  func() time.Time { return now.Add(time.Minute) },
		Live: true,
	})
	if err != nil {
		t.Fatalf("SnapshotBackendStatus returned error: %v", err)
	}
	if report.Status != RemoteStateCoolingDown {
		t.Fatalf("status = %q, want cooling_down", report.Status)
	}
	if calls != 0 {
		t.Fatalf("cooldown backend status ran Borg %d times", calls)
	}
}

func TestSnapshotBackendStatusLockBusyPreventsBorgExecution(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	release, waitHeld := holdRemoteLockForTest(t, cfg)
	defer release()
	waitHeld()

	calls := 0
	report, err := SnapshotBackendStatus(context.Background(), SnapshotBackendStatusInput{
		Config: cfg,
		Runner: BorgCommandRunner{Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
			calls++
			return nil, nil
		}},
		Now:  func() time.Time { return time.Unix(700, 0).UTC() },
		Live: true,
	})
	if err != nil {
		t.Fatalf("SnapshotBackendStatus returned error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("lock-busy backend status ran Borg %d times", calls)
	}
	if report.Status != "lock_busy" {
		t.Fatalf("status = %q, want lock_busy", report.Status)
	}
}

func TestSnapshotBackendStatusPreservesDisableRemoteLockWithDefaultExec(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	borgPath := filepath.Join(t.TempDir(), "borg")
	if err := os.WriteFile(borgPath, []byte("#!/bin/sh\nprintf '%s\\n' '{\"archives\":[{\"name\":\"a\",\"time\":\"2026-06-01T08:00:00Z\"}]}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Snapshots.Borg.Binary = borgPath
	release, waitHeld := holdRemoteLockForTest(t, cfg)
	defer release()
	waitHeld()

	report, err := SnapshotBackendStatus(context.Background(), SnapshotBackendStatusInput{
		Config: cfg,
		Runner: BorgCommandRunner{
			DisableRemoteLock: true,
		},
		Now:  func() time.Time { return time.Unix(750, 0).UTC() },
		Live: true,
	})
	if err != nil {
		t.Fatalf("SnapshotBackendStatus returned error: %v", err)
	}
	if !report.Initialized || report.ArchiveCount != 1 {
		t.Fatalf("report = %#v, want initialized backend with one archive", report)
	}
}

func TestSnapshotBackendStatusSuccessPreservesRemoteRootInventory(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	now := time.Unix(800, 0).UTC()
	if err := RecordRemoteSuccessAt(cfg, RemoteProbeResult{
		Status: RemoteStatus{Reachable: true, RemoteURI: cfg.RemoteURI(""), CheckedAt: now, Entries: 3},
		Entries: []RemoteEntry{
			{Path: DefaultMainSnapshots, IsDir: true},
			{Path: DefaultFullOffload, IsDir: true},
			{Path: DefaultCloudFolder, IsDir: true},
		},
	}, now, "cloud.status.live"); err != nil {
		t.Fatalf("RecordRemoteSuccessAt returned error: %v", err)
	}

	report, err := SnapshotBackendStatus(context.Background(), SnapshotBackendStatusInput{
		Config: cfg,
		Runner: BorgCommandRunner{Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
			return []byte(`{"archives":[{"name":"a","time":"2026-06-01T08:00:00Z"},{"name":"b","time":"2026-06-02T08:00:00Z"}]}`), nil
		}},
		Now:  func() time.Time { return now.Add(time.Minute) },
		Live: true,
	})
	if err != nil {
		t.Fatalf("SnapshotBackendStatus returned error: %v", err)
	}
	if !report.Initialized || report.ArchiveCount != 2 {
		t.Fatalf("report = %#v", report)
	}
	state, err := LoadRemoteState(cfg)
	if err != nil {
		t.Fatalf("LoadRemoteState returned error: %v", err)
	}
	if state.State != RemoteStateHealthy || state.UpdatedBy != "cloud.borg_backend.status" {
		t.Fatalf("state = %#v", state)
	}
	if state.LastRemoteEntries != 3 {
		t.Fatalf("last remote entries = %d, want preserved value 3", state.LastRemoteEntries)
	}
	if len(state.LastRoots) != 3 {
		t.Fatalf("last roots = %#v, want preserved root inventory", state.LastRoots)
	}
	for _, root := range state.LastRoots {
		if !root.Exists || root.Status != "present" {
			t.Fatalf("root inventory was overwritten by Borg status: %#v", state.LastRoots)
		}
	}
}

func testBorgStatusConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	passphrasePath := filepath.Join(dir, "borg.passphrase")
	if err := os.WriteFile(passphrasePath, []byte("test-passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := NormalizeConfig(Config{
		SchemaVersion: ConfigSchemaVersion,
		Enabled:       true,
		StateDir:      filepath.Join(dir, "state"),
		Snapshots: SnapshotsConfig{
			Backend: SnapshotBackendBorg,
			Borg: BorgConfig{
				Binary:         "borg",
				Repository:     filepath.Join(dir, "repo"),
				PassphraseFile: passphrasePath,
				CacheDir:       filepath.Join(dir, "cache"),
				SecurityDir:    filepath.Join(dir, "security"),
				Encryption:     DefaultBorgEncryption,
				Compression:    DefaultBorgCompression,
				CheckMode:      DefaultBorgCheckMode,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
