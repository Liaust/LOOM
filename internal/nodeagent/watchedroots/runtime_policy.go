package watchedroots

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loom.local/loom/internal/filesystemconnector"
)

type runtimePolicyEntry struct {
	root ValidatedRoot
	info os.FileInfo
}

var runtimePolicies = struct {
	sync.Mutex
	entries map[string]runtimePolicyEntry
}{entries: map[string]runtimePolicyEntry{}}

func InvalidateRuntimePolicy(store Store, rootKey string) {
	runtimePolicies.Lock()
	delete(runtimePolicies.entries, filepath.Join(store.DataDir, normalizeRootKey(rootKey)))
	runtimePolicies.Unlock()
}

// Recheck source custody every turn, but discover the complete policy tree only
// at a scan boundary or when exact policy inputs changed. This cache is disposable.
func ValidateRuntimeRoot(store Store, config RootConfig, fsConfig filesystemconnector.Config, now time.Time) (ValidatedRoot, error) {
	root, err := validateRootConfig(config, fsConfig, false, false)
	if err != nil || !root.RootReachable {
		return root, err
	}
	info, err := os.Stat(root.RootPath)
	if err != nil {
		return root, err
	}
	cp, err := store.LoadCheckpoint(root.Config.RootKey)
	if err != nil && !IsNotExist(err) {
		return root, err
	}
	hints, err := store.ListDirtyHints(root.Config.RootKey)
	if err != nil {
		return root, err
	}
	paths := make([]string, 0, len(hints))
	force := cp.FullScanID == "" && (autoScanMode(root, cp, hints, now) == ScanModeFull || hasRescanHint(hints))
	for _, hint := range hints {
		if hint.RelativePath != "__rescan__" {
			paths = append(paths, hint.RelativePath)
		}
		subtree, continuing := cp.SubtreeScans[hint.RelativePath]
		if filepath.Base(hint.RelativePath) == ".loomignore" || hint.HintKind == DirtyHintSubtree && (!continuing || subtree.Generation != hint.Generation) {
			force = true
		}
	}
	key := filepath.Join(store.DataDir, root.Config.RootKey)
	runtimePolicies.Lock()
	cached, ok := runtimePolicies.entries[key]
	runtimePolicies.Unlock()
	if ok && (cached.root.RootPath != root.RootPath || !os.SameFile(cached.info, info)) {
		if err := store.MarkRescanRequired(root.Config.RootKey, "source_identity_changed"); err != nil {
			return root, err
		}
		force = true
	}
	if ok && !force && cached.root.ConfigHash == root.ConfigHash && cached.root.RootPath == root.RootPath && os.SameFile(cached.info, info) {
		current := true
		if cached.root.PolicyResolver != nil {
			current, err = cached.root.PolicyResolver.CurrentForPaths(paths)
		}
		if err != nil {
			return root, err
		}
		if current {
			root.PolicyResolver = cached.root.PolicyResolver
			return root, nil
		}
		// A changed effective policy invalidates classifications, not only the cache.
		if err := store.MarkRescanRequired(root.Config.RootKey, "policy_inputs_changed"); err != nil {
			return root, err
		}
	}
	root, err = ValidateRootConfigForRun(config, fsConfig)
	if err != nil {
		return root, err
	}
	if root.PolicyResolver != nil {
		previous, readErr := store.LoadLatestSummary(root.Config.RootKey)
		if readErr != nil && !IsNotExist(readErr) {
			return root, readErr
		}
		if previous.PolicyFingerprint != "" && previous.PolicyFingerprint != root.PolicyResolver.Fingerprint() {
			if err := store.MarkRescanRequired(root.Config.RootKey, "policy_snapshot_changed"); err != nil {
				return root, err
			}
		}
	}
	runtimePolicies.Lock()
	if len(runtimePolicies.entries) >= 128 {
		for old := range runtimePolicies.entries {
			if strings.HasPrefix(old, store.DataDir+string(filepath.Separator)) && old != key {
				delete(runtimePolicies.entries, old)
				break
			}
		}
		if len(runtimePolicies.entries) >= 128 {
			for old := range runtimePolicies.entries {
				delete(runtimePolicies.entries, old)
				break
			}
		}
	}
	runtimePolicies.entries[key] = runtimePolicyEntry{root: root, info: info}
	runtimePolicies.Unlock()
	return root, nil
}
