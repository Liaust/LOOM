package cloudstorage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusCachedDoesNotCallDriver(t *testing.T) {
	t.Parallel()

	_, configPath := writeTestConfig(t, 0o600)
	driver := &recordingCloudStatusDriver{}
	report, err := Status(context.Background(), StatusInput{
		ConfigPath: configPath,
		Driver:     driver,
		Now:        func() time.Time { return time.Unix(100, 0) },
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if report.Mode != string(StatusModeCached) {
		t.Fatalf("mode = %q, want cached", report.Mode)
	}
	if report.Status != RemoteStateUnknown {
		t.Fatalf("status = %q, want unknown", report.Status)
	}
	if driver.statusCalls != 0 || driver.listCalls != 0 {
		t.Fatalf("cached status should not call remote driver: status=%d list=%d", driver.statusCalls, driver.listCalls)
	}
}

func TestStatusLiveCallsDriverAndRecordsState(t *testing.T) {
	t.Parallel()

	cfg, configPath := writeTestConfig(t, 0o600)
	now := time.Unix(200, 0).UTC()
	driver := &recordingCloudStatusDriver{
		status: RemoteStatus{Reachable: true, RemoteURI: cfg.RemoteURI(""), CheckedAt: now, Entries: 3},
		entries: []RemoteEntry{
			{Path: DefaultMainSnapshots, IsDir: true},
			{Path: DefaultFullOffload, IsDir: true},
			{Path: DefaultCloudFolder, IsDir: true},
		},
	}
	report, err := Status(context.Background(), StatusInput{
		ConfigPath: configPath,
		Driver:     driver,
		Now:        func() time.Time { return now },
		Mode:       StatusModeLive,
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if report.Status != "reachable" {
		t.Fatalf("status = %q", report.Status)
	}
	if driver.statusCalls == 0 || driver.listCalls == 0 {
		t.Fatalf("live status should call remote driver: status=%d list=%d", driver.statusCalls, driver.listCalls)
	}
	state, err := LoadRemoteState(cfg)
	if err != nil {
		t.Fatalf("LoadRemoteState returned error: %v", err)
	}
	if state.State != RemoteStateHealthy || state.LastSuccessAt == nil {
		t.Fatalf("state not marked healthy: %#v", state)
	}
	if len(state.LastRoots) != 3 {
		t.Fatalf("last roots = %#v", state.LastRoots)
	}
}

func TestStatusLiveWithRcloneDriverUsesOneProbeCommand(t *testing.T) {
	t.Parallel()

	cfg, configPath := writeTestConfig(t, 0o600)
	driver := NewRcloneDriver(cfg)
	driver.Now = func() time.Time { return time.Unix(250, 0) }
	var calls int
	driver.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		return []byte("main-snapshots/\nfull-offload/\ncloud-folder/\n"), nil
	}
	report, err := Status(context.Background(), StatusInput{
		ConfigPath: configPath,
		Driver:     driver,
		Now:        func() time.Time { return time.Unix(250, 0) },
		Mode:       StatusModeLive,
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if report.Status != "reachable" {
		t.Fatalf("status = %q", report.Status)
	}
	if calls != 1 {
		t.Fatalf("rclone calls = %d, want 1", calls)
	}
}

func TestStatusLiveRespectsCooldown(t *testing.T) {
	t.Parallel()

	cfg, configPath := writeTestConfig(t, 0o600)
	now := time.Unix(300, 0).UTC()
	if err := RecordRemoteFailureAt(cfg, errors.New("dial tcp: connection refused"), now, "test"); err != nil {
		t.Fatalf("RecordRemoteFailureAt returned error: %v", err)
	}
	driver := &recordingCloudStatusDriver{}
	report, err := Status(context.Background(), StatusInput{
		ConfigPath: configPath,
		Driver:     driver,
		Now:        func() time.Time { return now.Add(time.Minute) },
		Mode:       StatusModeLive,
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if report.Status != RemoteStateCoolingDown {
		t.Fatalf("status = %q, want cooling_down", report.Status)
	}
	if driver.statusCalls != 0 || driver.listCalls != 0 {
		t.Fatalf("cooldown should suppress remote driver: status=%d list=%d", driver.statusCalls, driver.listCalls)
	}
	if report.RemoteState == nil || report.RemoteState.NextLiveCheckAfter == nil {
		t.Fatalf("expected cooldown remote state, got %#v", report.RemoteState)
	}
}

func TestStatusLiveReportsLockBusyWithoutCallingDriver(t *testing.T) {
	t.Parallel()

	cfg, configPath := writeTestConfig(t, 0o600)
	release, waitHeld := holdRemoteLockForTest(t, cfg)
	defer release()
	waitHeld()

	driver := &recordingCloudStatusDriver{}
	report, err := Status(context.Background(), StatusInput{
		ConfigPath: configPath,
		Driver:     driver,
		Now:        func() time.Time { return time.Unix(400, 0) },
		Mode:       StatusModeLive,
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if report.Status != "lock_busy" {
		t.Fatalf("status = %q, want lock_busy", report.Status)
	}
	if driver.statusCalls != 0 || driver.listCalls != 0 {
		t.Fatalf("lock-busy status should not call remote driver: status=%d list=%d", driver.statusCalls, driver.listCalls)
	}
	if report.LockPath != cfg.RemoteLockPath {
		t.Fatalf("lock path = %q, want %q", report.LockPath, cfg.RemoteLockPath)
	}
}

func TestStatusCachedIgnoresHeldRemoteLock(t *testing.T) {
	t.Parallel()

	cfg, configPath := writeTestConfig(t, 0o600)
	release, waitHeld := holdRemoteLockForTest(t, cfg)
	defer release()
	waitHeld()

	driver := &recordingCloudStatusDriver{}
	report, err := Status(context.Background(), StatusInput{
		ConfigPath: configPath,
		Driver:     driver,
		Now:        func() time.Time { return time.Unix(500, 0) },
		Mode:       StatusModeCached,
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if report.Status != RemoteStateUnknown {
		t.Fatalf("status = %q, want unknown", report.Status)
	}
	if driver.statusCalls != 0 || driver.listCalls != 0 {
		t.Fatalf("cached status should not call remote driver: status=%d list=%d", driver.statusCalls, driver.listCalls)
	}
}

func TestStatusDisabledReportsDisabledState(t *testing.T) {
	t.Parallel()

	report, err := Status(context.Background(), StatusInput{
		ConfigPath: filepath.Join(t.TempDir(), "missing.json"),
		Now:        func() time.Time { return time.Unix(100, 0) },
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if report.Status != "disabled" {
		t.Fatalf("status = %q", report.Status)
	}
	if report.RemoteState == nil || report.RemoteState.State != RemoteStateDisabled {
		t.Fatalf("remote state = %#v", report.RemoteState)
	}
}

type recordingCloudStatusDriver struct {
	status      RemoteStatus
	statusErr   error
	entries     []RemoteEntry
	listErr     error
	statusCalls int
	listCalls   int
}

func (d *recordingCloudStatusDriver) Status(context.Context) (RemoteStatus, error) {
	d.statusCalls++
	if d.status.RemoteURI == "" {
		d.status = RemoteStatus{Reachable: true, RemoteURI: "loom-cloud:loom", CheckedAt: time.Unix(100, 0), Entries: len(d.entries)}
	}
	return d.status, d.statusErr
}

func (d *recordingCloudStatusDriver) List(context.Context, string) ([]RemoteEntry, error) {
	d.listCalls++
	return d.entries, d.listErr
}

func (d *recordingCloudStatusDriver) CopyToRemote(context.Context, string, string, CopyOptions) (CopyResult, error) {
	return CopyResult{}, nil
}

func (d *recordingCloudStatusDriver) CopyFromRemote(context.Context, string, string, CopyOptions) (CopyResult, error) {
	return CopyResult{}, nil
}

func (d *recordingCloudStatusDriver) Check(context.Context, string, string) (CheckResult, error) {
	return CheckResult{}, nil
}
