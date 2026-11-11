package watchedroots

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SubtreeScan struct {
	Generation string    `json:"generation"`
	ID         string    `json:"id"`
	After      string    `json:"after,omitempty"`
	RetryAt    time.Time `json:"retry_at,omitempty"`
}

// Directory events retain prefix/generation progress. A newer event replaces
// that generation rather than letting an old walk acknowledge unseen changes.
func reconcileSubtree(ctx context.Context, store Store, root ValidatedRoot, result *ScanResult, hint DirtyHint, started time.Time) (bool, error) {
	prefix := hint.RelativePath
	cursor := result.subtreeScans[prefix]
	if cursor.Generation != hint.Generation || cursor.ID == "" {
		cursor = SubtreeScan{Generation: hint.Generation, ID: "subtree_" + rand.Text()}
	}
	if time.Now().Before(cursor.RetryAt) {
		return false, nil
	}
	previous, err := store.ListPathStates(root.Config.RootKey, 0)
	if err != nil {
		return false, err
	}
	byPath := map[string]PathState{}
	observed := map[string]struct{}{}
	for _, state := range previous {
		byPath[state.RelativePath] = state
		if !dirtySubtreeContains(prefix, state.RelativePath) || state.FullScanID == cursor.ID {
			observed[state.RelativePath] = struct{}{}
		}
	}
	limits := scanLimits(root, started, result)
	beforeErrors, beforeBudget := result.Counts.Errors, result.Counts.BudgetExhausted
	rootObservation := ObservePath(root, prefix)
	if !rootObservation.Safe || rootObservation.ErrorCode != "" || rootObservation.Symlink {
		result.Counts.Errors++
	} else if rootObservation.Exists {
		err = filepath.WalkDir(filepath.Join(root.RootPath, filepath.FromSlash(prefix)), func(path string, entry os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			relative := relativeFromPath(root.RootPath, path)
			if walkErr != nil {
				result.Counts.Errors++
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if cursor.After != "" && !walkPathAfter(relative, cursor.After) {
				if entry.IsDir() && !strings.HasPrefix(cursor.After, relative+"/") {
					return filepath.SkipDir
				}
				return nil
			}
			if limits.exhausted() {
				result.Counts.BudgetExhausted++
				return filepath.SkipAll
			}
			observation := ObservePath(root, relative)
			observed[relative] = struct{}{}
			if observation.Kind == PathKindDirectory {
				result.Counts.Directories++
			} else {
				result.Counts.Files++
			}
			result.Counts.Visited++
			state, change, err := reconcileObservation(store, root, byPath[relative], observation, result)
			if err != nil {
				return err
			}
			if change != nil {
				result.ChangedPaths = append(result.ChangedPaths, *change)
			}
			cursor.After = relative
			if entry.IsDir() && (!observation.Safe || observation.Symlink ||
				(!state.Classification.Included && !mayMatchDescendant(root.Config.Include, relative)) ||
				state.Classification.ReasonCode == ReasonExcludedByPattern || state.Classification.ReasonCode == ReasonExcludedHidden ||
				state.Classification.ReasonCode == ReasonExcludedPackageBoundary ||
				state.Classification.ReasonCode == ReasonExcludedFilePolicy && (root.PolicyResolver == nil || !root.PolicyResolver.MayIncludeDescendant(relative))) {
				return filepath.SkipDir
			}
			return nil
		})
		if errors.Is(err, filepath.SkipAll) {
			err = nil
		}
		if err != nil {
			return false, err
		}
	}
	if result.Counts.Errors > beforeErrors {
		// Unreadability is not absence; retain work with a bounded retry.
		cursor.After, cursor.RetryAt = "", time.Now().Add(time.Minute)
		result.subtreeScans[prefix] = cursor
		return false, nil
	}
	if result.Counts.BudgetExhausted > beforeBudget {
		for relative := range observed {
			if !dirtySubtreeContains(prefix, relative) {
				continue
			}
			state, err := store.LoadPathState(root.Config.RootKey, relative)
			if err != nil {
				return false, err
			}
			if state.FullScanID != cursor.ID {
				state.FullScanID = cursor.ID
				if err := store.SavePathState(state); err != nil {
					return false, err
				}
			}
		}
		result.subtreeScans[prefix] = cursor
		return false, nil
	}
	var scoped []PathState
	for _, state := range previous {
		if dirtySubtreeContains(prefix, state.RelativePath) {
			scoped = append(scoped, state)
		}
	}
	if err := markExcludedByCurrentPolicy(store, root, scoped, observed, started, result); err != nil {
		return false, err
	}
	if err := reconcileDeletedPaths(store, root, previous, observed, result); err != nil {
		return false, err
	}
	delete(result.subtreeScans, prefix)
	return true, nil
}
