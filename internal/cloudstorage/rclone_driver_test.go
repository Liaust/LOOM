package cloudstorage

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRcloneDriverListUsesConfiguredRemote(t *testing.T) {
	t.Parallel()

	var gotName string
	var gotArgs []string
	driver := NewRcloneDriver(testCloudConfig(t))
	driver.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = append([]string{}, args...)
		return []byte("main-snapshots/\nfull-offload/\ncloud-folder/\n"), nil
	}
	entries, err := driver.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if gotName != "rclone" {
		t.Fatalf("binary = %q", gotName)
	}
	wantArgs := append([]string{"--config", driver.Config.RcloneConfigPath}, rcloneBudgetFlags(rcloneOperationStatusList)...)
	wantArgs = append(wantArgs, "lsf", "loom-cloud:loom")
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if len(entries) != 3 || !entries[0].IsDir || entries[0].Path != "main-snapshots" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestRcloneDriverProbeUsesSingleQuietListCommand(t *testing.T) {
	t.Parallel()

	var calls int
	var gotArgs []string
	driver := NewRcloneDriver(testCloudConfig(t))
	driver.DisableRemoteLock = true
	driver.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		gotArgs = append([]string{}, args...)
		return []byte("main-snapshots/\nfull-offload/\ncloud-folder/\n"), nil
	}
	probe, err := driver.Probe(context.Background(), "")
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if probe.Status.Entries != 3 || len(probe.Entries) != 3 {
		t.Fatalf("probe = %#v", probe)
	}
	for _, want := range []string{"--retries", "1", "--low-level-retries", "1", "--checkers", "1", "--transfers", "1", "--sftp-connections", "2", "--stats", "0", "lsf", "loom-cloud:loom"} {
		if !containsString(gotArgs, want) {
			t.Fatalf("args %#v missing %q", gotArgs, want)
		}
	}
}

func TestRcloneDriverCopyToRemoteSupportsDryRunChecksum(t *testing.T) {
	t.Parallel()

	driver := NewRcloneDriver(testCloudConfig(t))
	driver.DisableRemoteLock = true
	driver.Now = func() time.Time { return time.Unix(100, 0) }
	var commandLine string
	driver.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		commandLine = name + " " + strings.Join(args, " ")
		return []byte("ok\n"), nil
	}
	result, err := driver.CopyToRemote(context.Background(), "/tmp/local", "main-snapshots/test", CopyOptions{DryRun: true, Checksum: true})
	if err != nil {
		t.Fatalf("CopyToRemote returned error: %v", err)
	}
	for _, want := range []string{"rclone --config", "--retries 2", "--low-level-retries 2", "--checkers 1", "--transfers 1", "--sftp-connections 3", "--stats 30s", "copy --dry-run --checksum --create-empty-src-dirs", "/tmp/local", "loom-cloud:loom/main-snapshots/test"} {
		if !strings.Contains(commandLine, want) {
			t.Fatalf("command %q does not contain %q", commandLine, want)
		}
	}
	if result.Dest != "loom-cloud:loom/main-snapshots/test" || !result.DryRun {
		t.Fatalf("result = %#v", result)
	}
}

func TestRcloneDriverPreservesLinksAndMetadataForSnapshotRoundTripAndCheck(t *testing.T) {
	t.Parallel()

	driver := NewRcloneDriver(testCloudConfig(t))
	driver.DisableRemoteLock = true
	var commandLines []string
	driver.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		commandLines = append(commandLines, name+" "+strings.Join(args, " "))
		return []byte("ok\n"), nil
	}
	if _, err := driver.CopyToRemote(context.Background(), "/tmp/local", "main-snapshots/test", CopyOptions{Checksum: true, PreserveLinks: true, PreserveMetadata: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.CheckWithOptions(context.Background(), "/tmp/local", "main-snapshots/test", CheckOptions{PreserveLinks: true, PreserveMetadata: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.CopyFromRemote(context.Background(), "main-snapshots/test", "/tmp/restored", CopyOptions{PreserveLinks: true, PreserveMetadata: true}); err != nil {
		t.Fatal(err)
	}
	if len(commandLines) != 3 {
		t.Fatalf("commands = %#v", commandLines)
	}
	for _, commandLine := range commandLines {
		if !strings.Contains(commandLine, " --links ") {
			t.Fatalf("preserve-links command missing --links: %q", commandLine)
		}
		if !strings.Contains(commandLine, " --metadata ") {
			t.Fatalf("preserve-metadata command missing --metadata: %q", commandLine)
		}
	}
}

func TestRcloneDriverLockBusyPreventsCommandExecution(t *testing.T) {
	t.Parallel()

	cfg := testCloudConfig(t)
	release, waitHeld := holdRemoteLockForTest(t, cfg)
	defer release()
	waitHeld()

	var calls int
	driver := NewRcloneDriver(cfg)
	driver.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		return []byte("unexpected\n"), nil
	}
	_, err := driver.List(context.Background(), "")
	if !errors.Is(err, ErrRemoteLockBusy) {
		t.Fatalf("err = %v, want ErrRemoteLockBusy", err)
	}
	if calls != 0 {
		t.Fatalf("exec calls = %d, want 0", calls)
	}
}

func testCloudConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := NormalizeConfig(Config{SchemaVersion: ConfigSchemaVersion, Enabled: true, StateDir: filepath.Join(t.TempDir(), "cloud")})
	if err != nil {
		t.Fatal(err)
	}
	cfg.RcloneConfigPath = "/tmp/rclone.conf"
	return cfg
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
