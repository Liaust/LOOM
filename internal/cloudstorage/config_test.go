package cloudstorage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigMissingReturnsDisabledDefault(t *testing.T) {
	t.Parallel()

	load, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if load.Exists {
		t.Fatal("missing config should report Exists=false")
	}
	if load.Config.Enabled {
		t.Fatal("missing config should default to disabled")
	}
	if load.Config.RemoteURI(DefaultMainSnapshots) != "loom-cloud:loom/main-snapshots" {
		t.Fatalf("remote uri = %q", load.Config.RemoteURI(DefaultMainSnapshots))
	}
}

func TestLoadConfigNormalizesDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cloud.json")
	payload, err := json.Marshal(Config{SchemaVersion: ConfigSchemaVersion, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	load, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if !load.Exists || !load.Config.Enabled {
		t.Fatalf("load result = %#v", load)
	}
	if load.Config.RemoteName != DefaultRemoteName || load.Config.Roots.CloudFolder != DefaultCloudFolder {
		t.Fatalf("defaults not applied: %#v", load.Config)
	}
	if load.Config.RemoteLockPath != filepath.Join(load.Config.StateDir, "locks", "storagebox.lock") {
		t.Fatalf("remote lock path = %q", load.Config.RemoteLockPath)
	}
	if load.Config.RemoteLockWaitSeconds != DefaultRemoteLockWaitSeconds ||
		load.Config.RemoteLockWorkerWaitSeconds != DefaultRemoteLockWorkerWaitSeconds ||
		load.Config.RemoteLockEffectfulWaitSeconds != DefaultRemoteLockEffectfulWaitSeconds {
		t.Fatalf("remote lock waits not applied: %#v", load.Config)
	}
	if load.Config.SchemaVersion != ConfigSchemaVersion || load.Config.Snapshots.Backend != SnapshotBackendLegacyTree {
		t.Fatalf("snapshot defaults not applied: %#v", load.Config)
	}
}

func TestNormalizeConfigDerivesRemoteLockPathFromStateDir(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "cloud-state")
	cfg, err := NormalizeConfig(Config{
		SchemaVersion: ConfigSchemaVersion,
		Enabled:       true,
		StateDir:      stateDir,
	})
	if err != nil {
		t.Fatalf("NormalizeConfig returned error: %v", err)
	}
	if cfg.RemoteLockPath != filepath.Join(stateDir, "locks", "storagebox.lock") {
		t.Fatalf("remote lock path = %q", cfg.RemoteLockPath)
	}
}

func TestNormalizeConfigRejectsNegativeRemoteLockWaits(t *testing.T) {
	t.Parallel()

	for _, input := range []Config{
		{SchemaVersion: ConfigSchemaVersion, RemoteLockWaitSeconds: -1},
		{SchemaVersion: ConfigSchemaVersion, RemoteLockWorkerWaitSeconds: -1},
		{SchemaVersion: ConfigSchemaVersion, RemoteLockEffectfulWaitSeconds: -1},
	} {
		if _, err := NormalizeConfig(input); err == nil {
			t.Fatalf("expected negative wait to fail for %#v", input)
		}
	}
}

func TestLoadConfigAcceptsV063AsLegacyTreeSnapshotBackend(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cloud.json")
	payload, err := json.Marshal(Config{SchemaVersion: ConfigSchemaVersionV063, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	load, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if load.Config.SchemaVersion != ConfigSchemaVersion {
		t.Fatalf("schema = %q, want %q", load.Config.SchemaVersion, ConfigSchemaVersion)
	}
	if load.Config.Snapshots.Backend != SnapshotBackendLegacyTree {
		t.Fatalf("snapshot backend = %q, want legacy_tree", load.Config.Snapshots.Backend)
	}
}

func TestNormalizeConfigAppliesBorgDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := NormalizeConfig(Config{
		SchemaVersion: ConfigSchemaVersion,
		Enabled:       true,
		StateDir:      filepath.Join(t.TempDir(), "cloud"),
		Snapshots:     SnapshotsConfig{Backend: SnapshotBackendBorg},
	})
	if err != nil {
		t.Fatalf("NormalizeConfig returned error: %v", err)
	}
	if cfg.Snapshots.Borg.Binary != DefaultBorgBinary {
		t.Fatalf("borg binary = %q", cfg.Snapshots.Borg.Binary)
	}
	if cfg.Snapshots.Borg.Compression != DefaultBorgCompression || cfg.Snapshots.Borg.Encryption != DefaultBorgEncryption {
		t.Fatalf("borg defaults not applied: %#v", cfg.Snapshots.Borg)
	}
	if cfg.Snapshots.Borg.CacheDir == "" || cfg.Snapshots.Borg.SecurityDir == "" {
		t.Fatalf("borg state dirs not applied: %#v", cfg.Snapshots.Borg)
	}
	if cfg.Snapshots.Borg.CheckIntervalHours != DefaultBorgCheckIntervalHours {
		t.Fatalf("borg check interval = %d", cfg.Snapshots.Borg.CheckIntervalHours)
	}
	if cfg.Snapshots.Borg.LockWaitSeconds != DefaultBorgLockWaitSeconds {
		t.Fatalf("borg lock wait = %d", cfg.Snapshots.Borg.LockWaitSeconds)
	}
	if cfg.Snapshots.Borg.InventoryCacheTTLSeconds != DefaultSnapshotInventoryCacheTTLSeconds {
		t.Fatalf("borg inventory cache ttl = %d", cfg.Snapshots.Borg.InventoryCacheTTLSeconds)
	}
}

func TestNormalizeConfigRejectsNegativeBorgLockWait(t *testing.T) {
	t.Parallel()

	_, err := NormalizeConfig(Config{
		SchemaVersion: ConfigSchemaVersion,
		Snapshots: SnapshotsConfig{
			Backend: SnapshotBackendBorg,
			Borg:    BorgConfig{LockWaitSeconds: -1},
		},
	})
	if err == nil {
		t.Fatal("expected negative Borg lock wait to fail")
	}
}

func TestNormalizeConfigRejectsUnsafeRemoteRoot(t *testing.T) {
	t.Parallel()

	_, err := NormalizeConfig(Config{SchemaVersion: ConfigSchemaVersion, RemoteRoot: "/var/lib/loom"})
	if err == nil {
		t.Fatal("expected unsafe remote root to fail")
	}
}
