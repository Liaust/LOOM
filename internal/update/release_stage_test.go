package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStageReleaseCopiesTreeAndWritesTargetSafeManifest(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	stage := filepath.Join(root, "stage")
	target := "/srv/loom/releases/v-test"
	writeReleaseStageFixture(t, source)
	if err := os.WriteFile(filepath.Join(source, ".DS_Store"), []byte("ignore"), 0o644); err != nil {
		t.Fatalf("write .DS_Store: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, ".git", "config"), []byte("ignore"), 0o644); err != nil {
		t.Fatalf("write .git/config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(source, "tmp"), 0o755); err != nil {
		t.Fatalf("mkdir root tmp: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "tmp", "scratch.txt"), []byte("ignore"), 0o644); err != nil {
		t.Fatalf("write root tmp fixture: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(source, "templates", "morathustra", "tmp"), 0o755); err != nil {
		t.Fatalf("mkdir nested template tmp: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "templates", "morathustra", "tmp", "README.md"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write nested template tmp fixture: %v", err)
	}

	result, err := StageRelease(context.Background(), ReleaseStageInput{
		SourcePath:  source,
		StagePath:   stage,
		TargetPath:  target,
		ReleaseID:   "release_test",
		Version:     "0.9.7-test",
		Commit:      "abc123",
		FlakeOutput: ".#hardware-main",
		BackupScope: &ReleaseBackupScope{
			SchemaVersion: ReleaseBackupScopeSchemaVersion,
			Class:         ReleaseBackupScopeOperationalOnly,
		},
		Now: func() time.Time {
			return time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("StageRelease returned error: %v", err)
	}
	if result.Status != ReleaseStageStatusStaged {
		t.Fatalf("status = %q, want %q", result.Status, ReleaseStageStatusStaged)
	}
	if _, err := os.Stat(filepath.Join(stage, "go.mod")); err != nil {
		t.Fatalf("expected staged go.mod: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, ".git")); !os.IsNotExist(err) {
		t.Fatalf("expected .git to be excluded, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, ".DS_Store")); !os.IsNotExist(err) {
		t.Fatalf("expected .DS_Store to be excluded, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "tmp")); !os.IsNotExist(err) {
		t.Fatalf("expected root tmp to be excluded, err=%v", err)
	}
	if payload, err := os.ReadFile(filepath.Join(stage, "templates", "morathustra", "tmp", "README.md")); err != nil || string(payload) != "keep" {
		t.Fatalf("nested template tmp was not preserved: payload=%q err=%v", payload, err)
	}
	manifest, err := ReadReleaseManifest(filepath.Join(stage, DefaultReleaseManifestFileYAML))
	if err != nil {
		t.Fatalf("ReadReleaseManifest returned error: %v", err)
	}
	if manifest.SourcePath != target {
		t.Fatalf("manifest source_path = %q, want %q", manifest.SourcePath, target)
	}
	if manifest.MigrationsDir != filepath.Join(target, "migrations") {
		t.Fatalf("manifest migrations_dir = %q", manifest.MigrationsDir)
	}
	if manifest.FlakeOutput != ".#hardware-main" || manifest.ReleaseID != "release_test" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if manifest.BackupScope == nil || manifest.BackupScope.SchemaVersion != ReleaseBackupScopeSchemaVersion || manifest.BackupScope.Class != ReleaseBackupScopeOperationalOnly {
		t.Fatalf("manifest backup scope = %#v", manifest.BackupScope)
	}
	assertExactReleaseRootMode(t, stage, 0o755)
}

func TestStageReleaseNormalizesExistingEmptyMode0700Root(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	stage := filepath.Join(root, "stage")
	writeReleaseStageFixture(t, source)
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatalf("mkdir mode-0700 stage: %v", err)
	}

	_, err := StageRelease(context.Background(), ReleaseStageInput{
		SourcePath: source,
		StagePath:  stage,
		TargetPath: "/srv/loom/releases/v-normalized",
	})
	if err != nil {
		t.Fatalf("StageRelease returned error: %v", err)
	}
	assertExactReleaseRootMode(t, stage, 0o755)
	if _, err := os.Stat(filepath.Join(stage, DefaultReleaseManifestFileYAML)); err != nil {
		t.Fatalf("staged manifest missing: %v", err)
	}
}

func TestStageReleaseRefusesSymlinkStageRoot(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	realStage := filepath.Join(root, "real-stage")
	stage := filepath.Join(root, "stage")
	writeReleaseStageFixture(t, source)
	if err := os.Mkdir(realStage, 0o755); err != nil {
		t.Fatalf("mkdir real stage: %v", err)
	}
	if err := os.Symlink(realStage, stage); err != nil {
		t.Fatalf("symlink stage: %v", err)
	}

	_, err := StageRelease(context.Background(), ReleaseStageInput{
		SourcePath: source,
		StagePath:  stage,
		TargetPath: "/srv/loom/releases/v-symlink",
		Overwrite:  true,
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
	entries, readErr := os.ReadDir(realStage)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("symlink target changed: entries=%d err=%v", len(entries), readErr)
	}
}

func TestStageReleaseDryRunDoesNotCreateStageDirectory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	stage := filepath.Join(root, "stage")
	writeReleaseStageFixture(t, source)

	result, err := StageRelease(context.Background(), ReleaseStageInput{
		SourcePath: source,
		StagePath:  stage,
		TargetPath: "/srv/loom/releases/v-dry-run",
		DryRun:     true,
	})
	if err != nil {
		t.Fatalf("StageRelease dry run returned error: %v", err)
	}
	if result.Status != UpdateStatusDryRun {
		t.Fatalf("status = %q, want %q", result.Status, UpdateStatusDryRun)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create stage directory, err=%v", err)
	}
}

func TestStageReleaseRefusesNonEmptyStageWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	stage := filepath.Join(root, "stage")
	writeReleaseStageFixture(t, source)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatalf("mkdir stage: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "existing.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write existing stage file: %v", err)
	}

	_, err := StageRelease(context.Background(), ReleaseStageInput{
		SourcePath: source,
		StagePath:  stage,
		TargetPath: "/srv/loom/releases/v-refuse",
	})
	if err == nil || !strings.Contains(err.Error(), "--overwrite") {
		t.Fatalf("expected overwrite refusal, got %v", err)
	}
}

func writeReleaseStageFixture(t *testing.T, source string) {
	t.Helper()
	files := map[string]string{
		"go.mod":                    "module example.local/release\n",
		"flake.nix":                 "{ outputs = { self }: {}; }\n",
		"cmd/loom/main.go":          "package main\nfunc main() {}\n",
		"migrations/00001_init.sql": "-- +goose Up\nSELECT 1;\n",
		"internal/example.txt":      "release payload\n",
	}
	for rel, content := range files {
		path := filepath.Join(source, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func assertExactReleaseRootMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(%s): %v", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("release root mode = %v, want real directory", info.Mode())
	}
	if got := info.Mode().Perm(); got != want.Perm() {
		t.Fatalf("release root mode = %04o, want %04o", got, want.Perm())
	}
}
