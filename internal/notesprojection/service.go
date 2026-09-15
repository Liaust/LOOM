package notesprojection

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type Service struct {
	Sources        SourceProvider
	ProjectionRoot string
	Now            func() time.Time
}

func NewService(sources SourceProvider, projectionRoot string) Service {
	return Service{Sources: sources, ProjectionRoot: projectionRoot}
}

func (s Service) Status(ctx context.Context) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	now := s.now()
	root, err := normalizeProjectionRoot(s.ProjectionRoot)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		ProjectionRoot:     root,
		ManifestPath:       manifestPath(root),
		RawWritesSupported: false,
		GeneratedAt:        now,
	}
	info, statErr := os.Stat(root)
	switch {
	case statErr == nil:
		status.Exists = true
		status.ReadOnly = info.Mode().Perm()&0o222 == 0
	case os.IsNotExist(statErr):
		status.Exists = false
		status.ReadOnly = true
	default:
		return Status{}, statErr
	}
	manifest, ok, err := ReadManifest(root)
	if err != nil {
		status.Findings = append(status.Findings, Finding{
			Severity: SeverityWarning,
			Kind:     "manifest_unreadable",
			Path:     manifestPath(root),
			Summary:  err.Error(),
		})
		return status, nil
	}
	if ok {
		generatedAt := manifest.GeneratedAt
		status.LastRebuildAt = &generatedAt
		status.Counts = manifest.Counts
		status.Findings = append(status.Findings, manifest.Findings...)
		if !manifest.ReadOnly {
			status.ReadOnly = false
		}
	}
	if _, err := os.Lstat(filepath.Join(root, ".loom", "rebuild-pending")); err == nil {
		status.Findings = append(status.Findings, Finding{Severity: SeverityError, Kind: "rebuild_incomplete", Summary: "The last rebuild did not complete; the manifest is not proof of the current generated tree."})
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	return status, nil
}

func (s Service) Rebuild(ctx context.Context, input RebuildInput) (RebuildResult, error) {
	return s.rebuild(ctx, input, false)
}

// Refresh is the bounded automatic path. A complete source inventory is
// required before removing stale generated entries; explicit rebuild remains
// available for repairing copied content even when its source is unchanged.
func (s Service) Refresh(ctx context.Context) (bool, error) {
	result, err := s.rebuild(ctx, RebuildInput{MaxObjects: 5001}, true)
	return len(result.Changes) > 0, err
}

func (s Service) rebuild(ctx context.Context, input RebuildInput, refresh bool) (RebuildResult, error) {
	if err := ctx.Err(); err != nil {
		return RebuildResult{}, err
	}
	root := firstNonEmpty(input.ProjectionRoot, s.ProjectionRoot)
	root, err := normalizeProjectionRoot(root)
	if err != nil {
		return RebuildResult{}, err
	}
	if s.Sources == nil {
		return RebuildResult{}, fmt.Errorf("notes projection source provider is required")
	}
	if !input.DryRun {
		// Directory locks serialize explicit API rebuilds and automatic refreshes
		// without introducing a writable lock file into the read-only view.
		if err := os.MkdirAll(root, 0o755); err != nil {
			return RebuildResult{}, err
		}
		fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return RebuildResult{}, err
		}
		defer unix.Close(fd)
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			return RebuildResult{}, fmt.Errorf("Notes projection is busy: %w", err)
		}
		defer unix.Flock(fd, unix.LOCK_UN)
	}
	now := s.now()
	sources, err := s.Sources.ListProjectionSources(ctx, SourceListInput{Limit: input.MaxObjects})
	if err != nil {
		return RebuildResult{}, fmt.Errorf("list notes projection sources: %w", err)
	}
	if refresh && len(sources) >= 5001 {
		return RebuildResult{}, fmt.Errorf("automatic Notes projection exceeds 5000 sources; refusing incomplete inventory")
	}
	entries, buildFindings := BuildProjectionEntries(root, sources, now)
	if refresh && len(buildFindings) > 0 {
		return RebuildResult{}, fmt.Errorf("automatic Notes projection has invalid source paths; refusing partial inventory")
	}
	previous, found, manifestErr := ReadManifest(root)
	if refresh && manifestErr != nil {
		return RebuildResult{}, fmt.Errorf("read automatic Notes projection manifest: %w", manifestErr)
	}
	if refresh && found && projectionCurrent(root, previous, entries) {
		return RebuildResult{ProjectionRoot: root, Manifest: previous}, nil
	}
	if refresh {
		old := make(map[string]ProjectionEntry, len(previous.Entries))
		for _, entry := range previous.Entries {
			old[entry.ProjectedPath] = entry
		}
		var copyBytes int64
		for _, entry := range entries {
			if reusableProjectionEntry(root, old[entry.ProjectedPath], entry) {
				continue
			}
			if entry.SizeBytes == nil || *entry.SizeBytes < 0 || *entry.SizeBytes > 64*1024*1024-copyBytes {
				return RebuildResult{}, fmt.Errorf("automatic Notes projection copy budget is unknown or exceeds 64 MiB; explicit reviewed rebuild required")
			}
			copyBytes += *entry.SizeBytes
		}
	}
	materializer := materializer{
		root:     root,
		now:      now,
		dryRun:   input.DryRun,
		entries:  entries,
		findings: buildFindings,
		reuse:    refresh,
	}
	manifest, changes, materializeFindings, err := materializer.run(ctx)
	if err != nil {
		return RebuildResult{}, err
	}
	findings := append([]Finding{}, buildFindings...)
	findings = append(findings, materializeFindings...)
	manifest.Findings = append([]Finding{}, findings...)
	status := Status{
		ProjectionRoot:     root,
		Exists:             !input.DryRun,
		ManifestPath:       manifestPath(root),
		LastRebuildAt:      &manifest.GeneratedAt,
		Counts:             manifest.Counts,
		ReadOnly:           true,
		RawWritesSupported: false,
		Findings:           findings,
		GeneratedAt:        now,
	}
	if input.DryRun {
		if _, err := os.Stat(root); err == nil {
			status.Exists = true
		}
	}
	result := RebuildResult{
		ProjectionRoot: root,
		DryRun:         input.DryRun,
		Manifest:       manifest,
		Status:         status,
		Changes:        changes,
		Findings:       findings,
		GeneratedAt:    now,
	}
	return BoundRebuildResult(result, input), nil
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func normalizeProjectionRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("notes projection root is required")
	}
	if !filepath.IsAbs(root) {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", fmt.Errorf("resolve notes projection root: %w", err)
		}
		root = abs
	}
	return filepath.Clean(root), nil
}

func BoundRebuildResult(result RebuildResult, input RebuildInput) RebuildResult {
	if input.IncludeAll {
		return result
	}
	limit := input.MaxResults
	if limit <= 0 {
		limit = DefaultRebuildResultLimit
	}
	if limit > MaxRebuildResultLimit {
		limit = MaxRebuildResultLimit
	}
	if len(result.Changes) > limit {
		result.Changes = append([]Change(nil), result.Changes[:limit]...)
		result.ChangesTruncated = true
	}
	if len(result.Findings) > limit {
		result.Findings = append([]Finding(nil), result.Findings[:limit]...)
		result.FindingsTruncated = true
	}
	if len(result.Status.Findings) > limit {
		result.Status.Findings = append([]Finding(nil), result.Status.Findings[:limit]...)
	}
	if len(result.Manifest.Findings) > limit {
		result.Manifest.Findings = append([]Finding(nil), result.Manifest.Findings[:limit]...)
	}
	if len(result.Manifest.Entries) > limit {
		result.Manifest.Entries = nil
		result.ManifestEntriesTruncated = true
	}
	return result
}
