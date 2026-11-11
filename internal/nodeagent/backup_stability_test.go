package nodeagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupStabilityDoesNotSleepForOldSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.md")
	if err := os.WriteFile(path, []byte("retained source"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := stableFileInfo(path, "1s"); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) >= 500*time.Millisecond {
		t.Fatal("repeated the entire quiet window for an old source")
	}
	// Even with an old mtime, a hash mismatch cannot become a valid backup.
	config := Config{BackupTransport: BackupTransportConfig{StabilityWindow: "1s"}}
	action := backupMetadataAction("notes", "old.md")
	action.ContentHashURI = "sha256:not-the-expected-bytes"
	action.BackupMaxFileBytes = 1 << 20
	if _, _, err := (Store{DataDir: t.TempDir()}).stageWatchedRootBackupArtifact(config, nil, action, path, time.Now()); err == nil {
		t.Fatal("old mtime bypassed expected hash verification")
	}
}

func TestBackupStabilityStillRejectsChangedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "changing.md")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		changed <- os.WriteFile(path, []byte("after, changed"), 0600)
	}()
	_, err := stableFileInfo(path, "200ms")
	if writeErr := <-changed; writeErr != nil {
		t.Fatal(writeErr)
	}
	if err == nil {
		t.Fatal("changed source passed stability check")
	}
}
