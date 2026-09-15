package cloudstorage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClassifyRemoteError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "connection refused", err: errors.New("dial tcp 91.98.240.163:23: connect: connection refused"), want: RemoteErrorConnectionRefused},
		{name: "network unreachable", err: errors.New("ssh: connect to host example port 23: Network is unreachable"), want: RemoteErrorNetworkUnreachable},
		{name: "timeout", err: errors.New("dial tcp: i/o timeout"), want: RemoteErrorTimeout},
		{name: "local key permission", err: errors.New("failed to read private key file: open /var/lib/loom/cloud/keys/key: permission denied"), want: RemoteErrorPermissionLocal},
		{name: "auth failed", err: errors.New("session setup failed: NT_STATUS_LOGON_FAILURE"), want: RemoteErrorAuthFailed},
		{name: "remote missing", err: errors.New("directory not found"), want: RemoteErrorRemoteMissing},
		{name: "unknown", err: errors.New("unexpected failure"), want: RemoteErrorUnknown},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ClassifyRemoteError(tc.err); got != tc.want {
				t.Fatalf("ClassifyRemoteError() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRecordRemoteFailureCreatesCooldownForTransportErrors(t *testing.T) {
	t.Parallel()

	cfg := testRemoteStateConfig(t)
	now := time.Date(2026, 6, 19, 23, 0, 0, 0, time.UTC)
	err := errors.New("dial tcp 91.98.240.163:23: connect: connection refused")

	if recErr := RecordRemoteFailureAt(cfg, err, now, "test"); recErr != nil {
		t.Fatalf("RecordRemoteFailureAt returned error: %v", recErr)
	}
	state, loadErr := LoadRemoteState(cfg)
	if loadErr != nil {
		t.Fatalf("LoadRemoteState returned error: %v", loadErr)
	}
	if state.State != RemoteStateCoolingDown {
		t.Fatalf("state = %q, want cooling_down", state.State)
	}
	if state.LastErrorClass != RemoteErrorConnectionRefused {
		t.Fatalf("error class = %q", state.LastErrorClass)
	}
	if state.FailureCount != 1 {
		t.Fatalf("failure count = %d", state.FailureCount)
	}
	if state.NextLiveCheckAfter == nil || !state.NextLiveCheckAfter.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("next live check = %v", state.NextLiveCheckAfter)
	}
	if CanLiveProbe(state, now.Add(29*time.Minute)) {
		t.Fatal("live probe should be blocked during cooldown")
	}
	if !CanLiveProbe(state, now.Add(31*time.Minute)) {
		t.Fatal("live probe should be allowed after cooldown")
	}
}

func TestRecordRemoteFailureExtendsCooldown(t *testing.T) {
	t.Parallel()

	cfg := testRemoteStateConfig(t)
	first := time.Date(2026, 6, 19, 23, 0, 0, 0, time.UTC)
	second := first.Add(5 * time.Minute)
	err := errors.New("connect: connection refused")

	if recErr := RecordRemoteFailureAt(cfg, err, first, "test"); recErr != nil {
		t.Fatal(recErr)
	}
	if recErr := RecordRemoteFailureAt(cfg, err, second, "test"); recErr != nil {
		t.Fatal(recErr)
	}
	state, loadErr := LoadRemoteState(cfg)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.FailureCount != 2 {
		t.Fatalf("failure count = %d, want 2", state.FailureCount)
	}
	if state.NextLiveCheckAfter == nil || !state.NextLiveCheckAfter.Equal(second.Add(time.Hour)) {
		t.Fatalf("next live check = %v, want %v", state.NextLiveCheckAfter, second.Add(time.Hour))
	}
}

func TestRecordRemoteSuccessClearsCooldown(t *testing.T) {
	t.Parallel()

	cfg := testRemoteStateConfig(t)
	now := time.Date(2026, 6, 19, 23, 0, 0, 0, time.UTC)
	if err := RecordRemoteFailureAt(cfg, errors.New("connection refused"), now, "test"); err != nil {
		t.Fatal(err)
	}
	successAt := now.Add(time.Hour)
	probe := RemoteProbeResult{
		Status:  RemoteStatus{Reachable: true, RemoteURI: cfg.RemoteURI(""), CheckedAt: successAt, Entries: 2},
		Entries: []RemoteEntry{{Path: DefaultMainSnapshots, IsDir: true}, {Path: DefaultCloudFolder, IsDir: true}},
	}
	if err := RecordRemoteSuccessAt(cfg, probe, successAt, "test"); err != nil {
		t.Fatal(err)
	}
	state, err := LoadRemoteState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != RemoteStateHealthy {
		t.Fatalf("state = %q", state.State)
	}
	if state.FailureCount != 0 || state.NextLiveCheckAfter != nil || state.LastErrorClass != "" {
		t.Fatalf("cooldown not cleared: %#v", state)
	}
	if len(state.LastRoots) != 3 {
		t.Fatalf("roots = %#v", state.LastRoots)
	}
}

func TestLocalPermissionFailureIsDegradedNotCooldown(t *testing.T) {
	t.Parallel()

	cfg := testRemoteStateConfig(t)
	now := time.Date(2026, 6, 19, 23, 0, 0, 0, time.UTC)
	err := errors.New("failed to read private key file: open /var/lib/loom/cloud/key: permission denied")
	if recErr := RecordRemoteFailureAt(cfg, err, now, "test"); recErr != nil {
		t.Fatal(recErr)
	}
	state, loadErr := LoadRemoteState(cfg)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.State != RemoteStateDegraded {
		t.Fatalf("state = %q, want degraded", state.State)
	}
	if state.NextLiveCheckAfter != nil {
		t.Fatalf("local permission failure should not set cooldown: %#v", state.NextLiveCheckAfter)
	}
	if !CanLiveProbe(state, now) {
		t.Fatal("degraded config state should not block live probe by cooldown")
	}
}

func TestLoadRemoteStateMissingReturnsUnknown(t *testing.T) {
	t.Parallel()

	cfg := testRemoteStateConfig(t)
	state, err := LoadRemoteState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != RemoteStateUnknown {
		t.Fatalf("state = %q", state.State)
	}
	if state.RemoteURI != cfg.RemoteURI("") {
		t.Fatalf("remote uri = %q", state.RemoteURI)
	}
	if _, err := os.Stat(RemoteStatePath(cfg)); !os.IsNotExist(err) {
		t.Fatalf("missing load should not create state file, stat err=%v", err)
	}
}

func testRemoteStateConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := NormalizeConfig(Config{
		SchemaVersion: ConfigSchemaVersion,
		Enabled:       true,
		StateDir:      filepath.Join(t.TempDir(), "cloud"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
