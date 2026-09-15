package loomcli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const acceptanceFolderName = ".loom-acceptance"

type acceptanceCleanupInput struct {
	Root        string
	ArchiveRoot string
	Yes         bool
	DeleteNow   bool
	Now         time.Time
}

type acceptanceCleanupResult struct {
	Status      string                   `json:"status"`
	Root        string                   `json:"root"`
	ArchiveRoot string                   `json:"archive_root,omitempty"`
	DryRun      bool                     `json:"dry_run"`
	DeleteNow   bool                     `json:"delete_now"`
	Summary     acceptanceCleanupSummary `json:"summary"`
	Items       []acceptanceCleanupItem  `json:"items,omitempty"`
}

type acceptanceCleanupSummary struct {
	Scanned      int `json:"scanned"`
	Candidates   int `json:"candidates"`
	Archived     int `json:"archived"`
	Deleted      int `json:"deleted"`
	WouldArchive int `json:"would_archive"`
	WouldDelete  int `json:"would_delete"`
	Skipped      int `json:"skipped"`
}

type acceptanceCleanupItem struct {
	Path         string `json:"path"`
	RelativePath string `json:"relative_path"`
	Action       string `json:"action"`
	Status       string `json:"status"`
	ArchivePath  string `json:"archive_path,omitempty"`
	Error        string `json:"error,omitempty"`
}

func runAcceptanceCleanup(input acceptanceCleanupInput) (acceptanceCleanupResult, error) {
	root, err := filepath.Abs(strings.TrimSpace(input.Root))
	if err != nil {
		return acceptanceCleanupResult{}, err
	}
	root = filepath.Clean(root)
	if root == "." || root == "" {
		return acceptanceCleanupResult{}, fmt.Errorf("root is required")
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	dryRun := !input.Yes
	result := acceptanceCleanupResult{
		Status:    "planned",
		Root:      root,
		DryRun:    dryRun,
		DeleteNow: input.DeleteNow,
	}
	candidates, err := acceptanceCleanupCandidates(root)
	if err != nil {
		return acceptanceCleanupResult{}, err
	}
	result.Summary.Scanned = len(candidates)
	result.Summary.Candidates = len(candidates)
	if !input.DeleteNow {
		archiveRoot := strings.TrimSpace(input.ArchiveRoot)
		if archiveRoot == "" {
			archiveRoot = defaultAcceptanceArchiveRoot(root)
		}
		archiveRoot, err = filepath.Abs(archiveRoot)
		if err != nil {
			return acceptanceCleanupResult{}, err
		}
		result.ArchiveRoot = filepath.Clean(archiveRoot)
	}
	for _, candidate := range candidates {
		item := acceptanceCleanupItem{
			Path:         candidate,
			RelativePath: relativePath(root, candidate),
			Action:       "archive",
			Status:       "planned",
		}
		if input.DeleteNow {
			item.Action = "delete"
		}
		if dryRun {
			if input.DeleteNow {
				item.Status = "would_delete"
				result.Summary.WouldDelete++
			} else {
				item.Status = "would_archive"
				item.ArchivePath = filepath.Join(result.ArchiveRoot, now.Format("20060102T150405Z"), item.RelativePath)
				result.Summary.WouldArchive++
			}
			result.Items = append(result.Items, item)
			continue
		}
		if input.DeleteNow {
			if err := os.RemoveAll(candidate); err != nil {
				item.Status = "skipped"
				item.Error = err.Error()
				result.Summary.Skipped++
			} else {
				item.Status = "deleted"
				result.Summary.Deleted++
			}
			result.Items = append(result.Items, item)
			continue
		}
		target := filepath.Join(result.ArchiveRoot, now.Format("20060102T150405Z"), item.RelativePath)
		item.ArchivePath = target
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			item.Status = "skipped"
			item.Error = err.Error()
			result.Summary.Skipped++
			result.Items = append(result.Items, item)
			continue
		}
		if err := os.Rename(candidate, target); err != nil {
			item.Status = "skipped"
			item.Error = err.Error()
			result.Summary.Skipped++
		} else {
			item.Status = "archived"
			result.Summary.Archived++
		}
		result.Items = append(result.Items, item)
	}
	if dryRun {
		result.Status = "dry_run"
	} else if result.Summary.Skipped > 0 {
		result.Status = "partial"
	} else {
		result.Status = "cleaned"
	}
	return result, nil
}

func acceptanceCleanupCandidates(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root is not a directory: %s", root)
	}
	acceptanceRoots := []string{}
	if filepath.Base(root) == acceptanceFolderName {
		acceptanceRoots = append(acceptanceRoots, root)
	} else {
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() {
				return nil
			}
			if entry.Name() != acceptanceFolderName {
				return nil
			}
			acceptanceRoots = append(acceptanceRoots, path)
			return filepath.SkipDir
		}); err != nil {
			return nil, err
		}
	}
	candidates := []string{}
	for _, acceptanceRoot := range acceptanceRoots {
		entries, err := os.ReadDir(acceptanceRoot)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			candidates = append(candidates, filepath.Join(acceptanceRoot, entry.Name()))
		}
	}
	sort.Strings(candidates)
	return candidates, nil
}

func defaultAcceptanceArchiveRoot(root string) string {
	if filepath.Base(root) == acceptanceFolderName {
		return filepath.Join(filepath.Dir(root), ".loom-acceptance-archive")
	}
	return filepath.Join(root, ".loom-acceptance-archive")
}

func relativePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Base(path)
	}
	return rel
}
