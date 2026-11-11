package cloudstorage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBorgIdentityIsStableAndOverridesAmbientRandomIdentity(t *testing.T) {
	t.Setenv("BORG_HOST_ID", "ambient-identity-must-not-win")
	cfg := testBorgStatusConfig(t)
	t.Setenv("PATH", "")
	first, err := borgHostID(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := borgHostID(cfg)
	if err != nil || first != second || !validBorgHostID(first) || !strings.HasPrefix(first, "loom-") {
		t.Fatalf("unstable identity: %q %q %v", first, second, err)
	}
	for _, stream := range []bool{false, true} {
		var got []string
		runner := BorgCommandRunner{Config: cfg, DisableRemoteLock: true,
			Exec: func(_ context.Context, _ string, _ BorgCommand, env []string) ([]byte, error) {
				got = env
				return nil, nil
			},
			StreamExec: func(_ context.Context, _ string, _ BorgCommand, env []string, _ io.Writer) error {
				got = env
				return nil
			},
		}
		if stream {
			if err := runner.RunStream(context.Background(), BorgCommand{Args: []string{"list"}}, io.Discard); err != nil {
				t.Fatal(err)
			}
		} else if _, err := runner.Run(context.Background(), BorgCommand{Args: []string{"list"}}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(got, "\n"), "BORG_HOST_ID="+first) {
			t.Fatal("identity not passed to child", got)
		}
	}
	cfg.Snapshots.Borg.HostID = "explicit-main-machine"
	id, err := borgHostID(cfg)
	if err != nil || id != cfg.Snapshots.Borg.HostID {
		t.Fatal(id, err)
	}
	for _, bad := range []string{"../other", "bad/name", "space name", "a\nBORG_REPO=evil", strings.Repeat("a", 129)} {
		cfg.Snapshots.Borg.HostID = bad
		if _, err := NormalizeConfig(cfg); err == nil {
			t.Fatalf("unsafe host identity accepted: %q", bad)
		}
	}
}

func TestBorgProgressIsBoundedFragmentSafeAndNumericOnly(t *testing.T) {
	var events []BorgProgress
	w := borgProgressWriter{notify: func(p BorgProgress) { events = append(events, p) }}
	raw := `{"type":"archive_progress","original_size":12,"compressed_size":8,"deduplicated_size":4,"nfiles":2,"path":"private-path-must-not-escape"}` + "\n"
	for _, part := range []string{raw[:25], raw[25:], strings.Repeat("x", 100<<10) + "\n", `{"type":"archive_progress","nfiles":-1}` + "\n", "not-json\n", raw} {
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if len(events) != 2 || events[0].Files != 2 || events[0].OriginalBytes != 12 {
		t.Fatal(events)
	}
	_, _ = w.Write([]byte(`{"type":"archive_progress","finished":true}` + "\n"))
	if len(events) != 3 || !events[2].Finished || events[2].OriginalBytes != 12 || events[2].Files != 2 {
		t.Fatal("finished event erased progress", events)
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "private-path") || strings.Contains(string(w.Bytes()), "private-path") || len(w.Bytes()) > borgFailureInspectLimit+128 || len(w.line) > 64<<10 {
		t.Fatal("unbounded or private progress", len(w.Bytes()))
	}
}

func TestBorgCacheLockFailureDoesNotRecommendInitialization(t *testing.T) {
	cfg := testBorgStatusConfig(t)
	report, err := SnapshotBackendStatus(context.Background(), SnapshotBackendStatusInput{Config: cfg, Live: true, Runner: BorgCommandRunner{Exec: func(context.Context, string, BorgCommand, []string) ([]byte, error) {
		return []byte("Failed to create/acquire the lock /private/cache/lock.exclusive (timeout)."), errors.New("exit status 2")
	}}})
	if err != nil || report.Status != "lock_blocked" || strings.Contains(report.RepairHint, "backend init") || !strings.Contains(report.RepairHint, "Do not initialize") {
		t.Fatal(report, err)
	}
}

func TestBorgInterruptedExitCodePreservesResumeMarker(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	request := directArchiveV2Request(fixture)
	fake := newFakeDirectBorg(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := BorgCommandRunner{Config: fixture.cfg, Exec: func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
		if command.Name() == "create" {
			cancel()
			return nil, errors.Join(context.Canceled, &exec.ExitError{})
		}
		return fake.exec(ctx, binary, command, env)
	}}
	backend := BorgSnapshotBackend{Runner: runner}
	result, err := backend.ArchiveCanonicalRoots(ctx, DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
	if !errors.Is(err, ErrDirectArchiveRetryable) || !result.Retryable || result.Committed || result.Code != DirectArchiveCodeInterrupted || result.PendingArchive == "" {
		t.Fatalf("interrupted create lost recovery: %+v %v", result, err)
	}
	prepared := prepareDirectArchiveV2ForTest(t, fixture.cfg, request)
	if _, err := os.Stat(directArchiveResumeMarkerPath(fixture.cfg, prepared)); err != nil {
		t.Fatal("resume evidence lost", err)
	}
	for name, count := range result.BorgCommandCounts {
		want := int64(0)
		if name == "list" || name == "create" {
			want = 1
		}
		if count != want {
			t.Fatal("cancelled operation attempted more cloud work", result.BorgCommandCounts)
		}
	}
}
