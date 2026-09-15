package cloudstorage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	loomconfig "loom.local/loom/internal/config"
)

func TestDoctorDisabledWhenConfigMissing(t *testing.T) {
	t.Parallel()

	report, err := Doctor(context.Background(), DoctorInput{
		ConfigPath:    filepath.Join(t.TempDir(), "missing.json"),
		RuntimeConfig: testRuntimeConfig(),
	})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	if report.Status != "disabled" {
		t.Fatalf("status = %q", report.Status)
	}
	if len(report.Findings) == 0 {
		t.Fatal("expected disabled findings")
	}
}

func TestDoctorEnabledOKWithFakeRclone(t *testing.T) {
	t.Parallel()

	cfg, configPath := writeTestConfig(t, 0o600)
	driver := NewRcloneDriver(cfg)
	driver.Now = func() time.Time { return time.Unix(100, 0) }
	driver.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("main-snapshots/\nfull-offload/\ncloud-folder/\n"), nil
	}
	report, err := Doctor(context.Background(), DoctorInput{
		ConfigPath:    configPath,
		RuntimeConfig: testRuntimeConfig(),
		Driver:        driver,
		LookPath:      func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Now:           func() time.Time { return time.Unix(100, 0) },
		Live:          true,
	})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	if report.Status != "ok" {
		t.Fatalf("status = %q findings=%#v", report.Status, report.Findings)
	}
	if len(report.Roots) != 3 {
		t.Fatalf("roots = %#v", report.Roots)
	}
}

func TestDoctorDefaultDoesNotProbeRemote(t *testing.T) {
	t.Parallel()

	_, configPath := writeTestConfig(t, 0o600)
	driver := &recordingCloudStatusDriver{}
	report, err := Doctor(context.Background(), DoctorInput{
		ConfigPath:    configPath,
		RuntimeConfig: testRuntimeConfig(),
		Driver:        driver,
		LookPath:      func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Now:           func() time.Time { return time.Unix(100, 0) },
	})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	if report.Status != "ok" {
		t.Fatalf("status = %q findings=%#v", report.Status, report.Findings)
	}
	if driver.statusCalls != 0 || driver.listCalls != 0 {
		t.Fatalf("default doctor should not call remote driver: status=%d list=%d", driver.statusCalls, driver.listCalls)
	}
	if len(report.Roots) != 0 {
		t.Fatalf("default doctor should not report live roots: %#v", report.Roots)
	}
}

func TestDoctorFailsWorldReadableRcloneConfig(t *testing.T) {
	t.Parallel()

	cfg, configPath := writeTestConfig(t, 0o666)
	driver := NewRcloneDriver(cfg)
	driver.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("main-snapshots/\nfull-offload/\ncloud-folder/\n"), nil
	}
	report, err := Doctor(context.Background(), DoctorInput{
		ConfigPath:    configPath,
		RuntimeConfig: testRuntimeConfig(),
		Driver:        driver,
		LookPath:      func(name string) (string, error) { return "/usr/bin/" + name, nil },
	})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	if report.Status != "failed" {
		t.Fatalf("status = %q findings=%#v", report.Status, report.Findings)
	}
}

func TestDoctorLiveReportsLockBusyWithoutCallingDriver(t *testing.T) {
	t.Parallel()

	cfg, configPath := writeTestConfig(t, 0o600)
	release, waitHeld := holdRemoteLockForTest(t, cfg)
	defer release()
	waitHeld()

	driver := &recordingCloudStatusDriver{}
	report, err := Doctor(context.Background(), DoctorInput{
		ConfigPath:    configPath,
		RuntimeConfig: testRuntimeConfig(),
		Driver:        driver,
		LookPath:      func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Now:           func() time.Time { return time.Unix(600, 0) },
		Live:          true,
	})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	if report.Status != "warning" {
		t.Fatalf("status = %q findings=%#v", report.Status, report.Findings)
	}
	if driver.statusCalls != 0 || driver.listCalls != 0 {
		t.Fatalf("lock-busy doctor should not call remote driver: status=%d list=%d", driver.statusCalls, driver.listCalls)
	}
	if !hasDoctorFinding(report.Findings, "cloud.remote.lock_busy") {
		t.Fatalf("missing lock busy finding: %#v", report.Findings)
	}
}

func writeTestConfig(t *testing.T, rcloneMode os.FileMode) (Config, string) {
	t.Helper()
	dir := t.TempDir()
	rclonePath := filepath.Join(dir, "rclone.conf")
	if err := os.WriteFile(rclonePath, []byte("[loom-cloud]\n"), rcloneMode); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, err := NormalizeConfig(Config{
		SchemaVersion:    ConfigSchemaVersion,
		Enabled:          true,
		RcloneConfigPath: rclonePath,
		StateDir:         stateDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "cloud.json")
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg, configPath
}

func hasDoctorFinding(findings []DoctorFinding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func testRuntimeConfig() loomconfig.Config {
	return loomconfig.Config{
		Env:           "production",
		NodeID:        "loom-main",
		NodeKind:      "main",
		NodeRole:      "main",
		RuntimeClass:  "main_full",
		DataDir:       "/var/lib/loom",
		ObjectStore:   "/var/lib/loom/object-store",
		StorageExport: "/var/lib/loom/storage-views/main-export",
		MainDocuments: "/var/lib/loom/main-documents",
		SocketPath:    "/run/loom/loomd.sock",
		MigrationsDir: "migrations",
	}
}
