package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/version"
)

const ReleaseStageStatusStaged = "staged"

const releaseRootMode fs.FileMode = 0o755

var defaultReleaseStageExcludeNames = []string{
	".DS_Store",
	".direnv",
	".git",
	".loom-acceptance",
	"coverage.out",
	"dist",
	"node_modules",
	"tmp",
}

type ReleaseStageInput struct {
	SourcePath   string
	StagePath    string
	TargetPath   string
	ReleaseID    string
	Version      string
	Commit       string
	FlakeOutput  string
	BackupScope  *ReleaseBackupScope
	ExcludeNames []string
	Overwrite    bool
	DryRun       bool
	Now          func() time.Time
}

type ReleaseStageResult struct {
	Status        string             `json:"status"`
	DryRun        bool               `json:"dry_run"`
	SourcePath    string             `json:"source_path"`
	StagePath     string             `json:"stage_path"`
	TargetPath    string             `json:"target_path"`
	ManifestPath  string             `json:"manifest_path"`
	ReleaseID     string             `json:"release_id"`
	Version       string             `json:"version,omitempty"`
	Commit        string             `json:"commit,omitempty"`
	FlakeOutput   string             `json:"flake_output,omitempty"`
	MigrationsDir string             `json:"migrations_dir"`
	Excluded      []string           `json:"excluded,omitempty"`
	Changed       []UpdateChange     `json:"changed"`
	Diagnostics   []UpdateDiagnostic `json:"diagnostics,omitempty"`
}

func StageRelease(ctx context.Context, input ReleaseStageInput) (ReleaseStageResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	source, err := normalizeExistingDir(input.SourcePath, "source path")
	if err != nil {
		return ReleaseStageResult{}, err
	}
	stage, err := normalizePath(input.StagePath, "stage path")
	if err != nil {
		return ReleaseStageResult{}, err
	}
	target := filepath.Clean(strings.TrimSpace(input.TargetPath))
	if target == "." || target == "" {
		return ReleaseStageResult{}, fmt.Errorf("target path is required")
	}
	if filepath.IsAbs(stage) {
		if inside, err := pathInside(stage, source); err == nil && inside {
			return ReleaseStageResult{}, fmt.Errorf("stage path must not be inside source path: %s", stage)
		}
	}

	excluded := releaseStageExcludeNames(input.ExcludeNames)
	manifestPath := filepath.Join(stage, DefaultReleaseManifestFileYAML)
	releaseID := firstNonEmpty(input.ReleaseID, filepath.Base(target))
	releaseVersion := firstNonEmpty(input.Version, version.Current().Version)
	commit := firstNonEmpty(input.Commit, gitCommit(source))
	flakeOutput := firstNonEmpty(input.FlakeOutput, DefaultProductionFlakeOutput)
	migrationsDir := filepath.Join(target, "migrations")
	result := ReleaseStageResult{
		Status:        ReleaseStageStatusStaged,
		DryRun:        input.DryRun,
		SourcePath:    source,
		StagePath:     stage,
		TargetPath:    target,
		ManifestPath:  manifestPath,
		ReleaseID:     releaseID,
		Version:       releaseVersion,
		Commit:        commit,
		FlakeOutput:   flakeOutput,
		MigrationsDir: migrationsDir,
		Excluded:      excluded,
	}
	result.Changed = append(result.Changed, UpdateChange{
		Step:    "validate",
		Status:  "ready",
		Message: "release stage inputs validated",
		Path:    source,
	})

	if input.DryRun {
		result.Status = UpdateStatusDryRun
		result.Changed = append(result.Changed, UpdateChange{
			Step:    "copy_release",
			Status:  "would_change",
			Message: "release tree would be copied",
			Path:    stage,
		}, UpdateChange{
			Step:    "release_manifest",
			Status:  "would_write",
			Message: "release manifest would point at target path",
			Path:    manifestPath,
		})
		return result, nil
	}

	if err := prepareStageDir(stage, input.Overwrite); err != nil {
		return ReleaseStageResult{}, err
	}
	if err := copyReleaseTree(ctx, source, stage, excluded); err != nil {
		return ReleaseStageResult{}, err
	}
	result.Changed = append(result.Changed, UpdateChange{
		Step:   "copy_release",
		Status: "changed",
		Path:   stage,
	})
	if err := WriteReleaseManifest(manifestPath, ReleaseManifest{
		ReleaseID:     releaseID,
		Version:       releaseVersion,
		Commit:        commit,
		SourcePath:    target,
		MigrationsDir: migrationsDir,
		FlakeOutput:   flakeOutput,
		BackupScope:   cloneReleaseBackupScope(input.BackupScope),
		CreatedAt:     currentTime(input.Now),
		Metadata: map[string]any{
			"staged_from": source,
			"stage_path":  stage,
		},
	}); err != nil {
		return ReleaseStageResult{}, err
	}
	if err := validateExactDirectoryMode(stage, "stage path", releaseRootMode); err != nil {
		return ReleaseStageResult{}, fmt.Errorf("staged release root contract failed after publication: %w", err)
	}
	result.Changed = append(result.Changed, UpdateChange{
		Step:   "release_manifest",
		Status: "written",
		Path:   manifestPath,
	})
	return result, nil
}

func normalizeExistingDir(path, label string) (string, error) {
	clean, err := normalizePath(path, label)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("%s is not readable: %w", label, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory: %s", label, clean)
	}
	return clean, nil
}

func normalizePath(path, label string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func pathInside(path, parent string) (bool, error) {
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false, err
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

func releaseStageExcludeNames(extra []string) []string {
	seen := map[string]struct{}{}
	var names []string
	for _, name := range append(defaultReleaseStageExcludeNames, extra...) {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func prepareStageDir(stage string, overwrite bool) error {
	info, err := os.Lstat(stage)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("stage path must not be a symbolic link: %s", stage)
		}
		if !info.IsDir() {
			return fmt.Errorf("stage path exists and is not a directory: %s", stage)
		}
		empty, err := directoryEmpty(stage)
		if err != nil {
			return err
		}
		if !empty && !overwrite {
			return fmt.Errorf("stage path is not empty; pass --overwrite to replace it: %s", stage)
		}
		if overwrite {
			if err := os.RemoveAll(stage); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(stage, releaseRootMode); err != nil {
		return err
	}
	if err := os.Chmod(stage, releaseRootMode); err != nil {
		return err
	}
	return validateExactDirectoryMode(stage, "stage path", releaseRootMode)
}

func validateExactDirectoryMode(path, label string, want fs.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%s is not readable: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must not be a symbolic link: %s", label, path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory: %s", label, path)
	}
	if special := info.Mode() & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky); special != 0 {
		return fmt.Errorf("%s has special mode bits %v, want exact mode %04o: %s", label, special, want.Perm(), path)
	}
	if got := info.Mode().Perm(); got != want.Perm() {
		return fmt.Errorf("%s mode is %04o, want %04o: %s", label, got, want.Perm(), path)
	}
	return nil
}

func directoryEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func copyReleaseTree(ctx context.Context, source, stage string, excludeNames []string) error {
	exclude := map[string]struct{}{}
	for _, name := range excludeNames {
		exclude[name] = struct{}{}
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if _, skip := exclude[filepath.ToSlash(rel)]; skip {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dst := filepath.Join(stage, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			return os.MkdirAll(dst, mode.Perm())
		case mode.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case mode.IsRegular():
			return copyRegularFile(path, dst, mode.Perm())
		default:
			return nil
		}
	})
}

func copyRegularFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}
