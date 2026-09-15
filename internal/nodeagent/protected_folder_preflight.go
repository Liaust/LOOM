package nodeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
)

var errProtectedFolderPreflightTruncated = errors.New("protected-folder preflight budget exhausted")

type preflightProvenance struct {
	IgnoredBySource map[filepolicy.RuleCategory]int `json:"ignored_by_source"`
	IgnoredSamples  []filepolicy.Decision           `json:"ignored_samples,omitempty"`
	SymlinkCount    int                             `json:"symlink_count"`
}

func buildProtectedFolderPreflightAck(config Config, state State, store Store, message communication.Message) communication.AckInput {
	ack := buildAckInput(state, message)
	ack.AckStatus = communication.AckStatusCompleted
	ack.ResultJSON = objectJSON(map[string]any{})
	ack.ErrorJSON = objectJSON(map[string]any{})
	payload, err := backupcontracts.DecodePreflightPayload(message.PayloadJSON)
	if err != nil {
		return protectedFolderPreflightFailure(ack, communication.AckStatusFailedPermanent, "preflight.invalid_request", "The protected-folder preflight request is invalid.")
	}
	if message.NodeID != state.NodeID || payload.TargetNode != state.NodeID {
		return protectedFolderPreflightFailure(ack, communication.AckStatusRejected, "preflight.wrong_node", "The preflight request targets a different owner node.")
	}
	if !payload.ExpiresAt.After(time.Now().UTC()) {
		return protectedFolderPreflightFailure(ack, communication.AckStatusFailedPermanent, "preflight.expired", "The protected-folder preflight request expired before execution.")
	}
	result := inspectProtectedFolder(config, store, payload)
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > backupcontracts.MaxPreflightResultBytes {
		return protectedFolderPreflightFailure(ack, communication.AckStatusFailedRetryable, "preflight.result_failed", "The node could not produce a bounded preflight result.")
	}
	ack.ResultJSON = encoded
	return ack
}

func protectedFolderPreflightFailure(ack communication.AckInput, status, code, summary string) communication.AckInput {
	ack.AckStatus = status
	ack.ResultJSON = objectJSON(map[string]any{})
	ack.ErrorJSON = objectJSON(map[string]any{"code": code, "summary": summary})
	return ack
}

func inspectProtectedFolder(config Config, store Store, payload backupcontracts.ProtectedFolderPreflightPayload) backupcontracts.PreflightResult {
	result := backupcontracts.PreflightResult{
		SchemaVersion: backupcontracts.ProtectedFolderPreflightResultVersion,
		RequestedPath: payload.RequestedPath,
		Budget:        payload.Budget,
		Policy:        backupcontracts.PreflightPolicyEvidence{Profile: payload.Ignore.Profile},
	}
	addFinding := func(code, severity, message string, blocking bool) {
		if len(result.Findings) >= 200 {
			return
		}
		result.Findings = append(result.Findings, backupcontracts.PreflightFinding{Code: code, Severity: severity, Message: message, Blocking: blocking})
	}
	requested := filepath.Clean(strings.TrimSpace(payload.RequestedPath))
	info, err := os.Lstat(requested)
	if err != nil {
		if os.IsNotExist(err) {
			addFinding("preflight.path_missing", "error", "The selected folder does not exist on the owner node.", true)
		} else if os.IsPermission(err) {
			addFinding("preflight.path_unreadable", "error", "The selected folder cannot be inspected with the node-agent permissions.", true)
		} else {
			addFinding("preflight.path_inspection_failed", "error", "The selected folder could not be inspected.", true)
		}
		return result
	}
	result.Exists = true
	if info.Mode()&os.ModeSymlink != 0 {
		canonical, canonicalErr := filepath.EvalSymlinks(requested)
		if canonicalErr == nil {
			result.CanonicalPath = filepath.Clean(canonical)
		}
		addFinding("preflight.symlink_root", "error", "A protected-folder root must not itself be a symbolic link.", true)
		return result
	}
	if !info.IsDir() {
		addFinding("preflight.not_directory", "error", "The selected path is not a directory.", true)
		return result
	}
	result.Directory = true
	canonical, err := filepath.EvalSymlinks(requested)
	if err != nil {
		addFinding("preflight.canonicalize_failed", "error", "The selected folder could not be safely canonicalized.", true)
		return result
	}
	canonical = filepath.Clean(canonical)
	result.CanonicalPath = canonical
	if reason := forbiddenProtectedFolderReason(canonical, store.DataDir); reason != "" {
		addFinding("preflight.forbidden_root", "error", reason, true)
		return result
	}
	if overlap := configuredRootOverlap(config, store.DataDir, canonical, payload.Recheck); overlap != "" {
		addFinding("preflight.root_overlap", "error", overlap, true)
		return result
	}
	directory, err := os.Open(canonical)
	if err != nil {
		addFinding("preflight.path_unreadable", "error", "The selected folder cannot be read with the node-agent permissions.", true)
		return result
	}
	_, readErr := directory.Readdirnames(1)
	_ = directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		addFinding("preflight.path_unreadable", "error", "The selected folder cannot be enumerated with the node-agent permissions.", true)
		return result
	}
	result.Readable = true
	result.MountPoint = portableMountPoint(canonical)
	profile, err := filepolicy.ParseProfile(payload.Ignore.Profile)
	if err != nil {
		addFinding("preflight.policy_invalid", "error", "The selected ignore-policy profile is invalid.", true)
		return result
	}
	deadline := time.Now().Add(time.Duration(payload.Budget.MaxDurationMillis) * time.Millisecond)
	resolver, err := filepolicy.NewResolver(canonical, profile, filepolicy.ResolverOptions{
		DiscoverUserRules:   payload.Ignore.DiscoverUserRules,
		ContractExcludes:    append([]string{}, payload.Exclude...),
		MaxDiscoveryEntries: payload.Budget.MaxEntries,
		MaxPolicyBytes:      16 * 1024 * 1024,
		DiscoveryDeadline:   deadline,
	})
	if err != nil {
		if strings.Contains(err.Error(), "budget exhausted") {
			result.Truncated = true
			addFinding("preflight.scan_truncated", "warning", "The folder estimate reached its configured scan budget and is partial.", false)
			return result
		}
		addFinding("preflight.policy_invalid", "error", "A discovered .loomignore policy is malformed or unreadable.", true)
		return result
	}
	result.Policy.Fingerprint = resolver.Fingerprint()
	for _, policyFile := range resolver.PolicyFiles() {
		result.Policy.PolicyFiles = append(result.Policy.PolicyFiles, policyFile.RelativePath)
	}
	sort.Strings(result.Policy.PolicyFiles)
	provenance := preflightProvenance{IgnoredBySource: map[filepolicy.RuleCategory]int{}}
	entries := 0
	walkErr := filepath.WalkDir(canonical, func(pathValue string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if pathValue == canonical {
			return nil
		}
		entries++
		if entries > payload.Budget.MaxEntries || time.Now().After(deadline) {
			result.Truncated = true
			return errProtectedFolderPreflightTruncated
		}
		relative, err := filepath.Rel(canonical, pathValue)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			provenance.SymlinkCount++
			return nil
		}
		resolution, err := resolver.Resolve(relative, entry.IsDir())
		if err != nil {
			return err
		}
		if entry.IsDir() {
			result.DirectoryCount++
			if !resolution.Decision.Included && !resolver.MayIncludeDescendant(relative) {
				return fs.SkipDir
			}
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if !entryInfo.Mode().IsRegular() {
			return nil
		}
		result.FileCount++
		size := entryInfo.Size()
		result.ApparentBytes += size
		if result.ApparentBytes > payload.Budget.MaxApparentBytes {
			result.Truncated = true
			return errProtectedFolderPreflightTruncated
		}
		included := resolution.Decision.Included
		decision := resolution.Decision
		if included && len(payload.Include) > 0 {
			matched, pattern := agentwatchedroots.MatchAny(payload.Include, relative)
			if !matched {
				included = false
				decision = filepolicy.Decision{Path: relative, Included: false, RuleCategory: filepolicy.RuleCategoryContract, Pattern: pattern}
			}
		}
		if included {
			result.Policy.IncludedCount++
			result.Policy.IncludedBytes += size
		} else {
			result.Policy.IgnoredCount++
			result.Policy.IgnoredBytes += size
			provenance.IgnoredBySource[decision.RuleCategory]++
			if len(provenance.IgnoredSamples) < 20 {
				if decision.SourceFile != "" {
					if rel, relErr := filepath.Rel(canonical, decision.SourceFile); relErr == nil {
						decision.SourceFile = filepath.ToSlash(rel)
					}
				}
				provenance.IgnoredSamples = append(provenance.IgnoredSamples, decision)
			}
		}
		return nil
	})
	if errors.Is(walkErr, errProtectedFolderPreflightTruncated) {
		addFinding("preflight.scan_truncated", "warning", "The folder estimate reached its configured scan budget and is partial.", false)
	} else if walkErr != nil {
		addFinding("preflight.scan_failed", "error", "The folder contains a path the node-agent could not inspect.", true)
	}
	if provenance.SymlinkCount > 0 {
		addFinding("preflight.symlinks_observed", "warning", "Symbolic links were observed and were not followed.", false)
	}
	result.Policy.RuleProvenance, _ = json.Marshal(provenance)
	return result
}

func forbiddenProtectedFolderReason(pathValue, dataDir string) string {
	clean := filepath.Clean(pathValue)
	if clean == string(filepath.Separator) {
		return "The filesystem root cannot be protected as a folder."
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == clean {
		return "The whole user home directory is too broad; choose a specific folder."
	}
	if strings.TrimSpace(dataDir) != "" {
		canonicalDataDir := filepath.Clean(dataDir)
		if resolved, err := filepath.EvalSymlinks(dataDir); err == nil {
			canonicalDataDir = filepath.Clean(resolved)
		}
		if pathsOverlap(clean, canonicalDataDir) {
			return "The selected folder overlaps LOOM node-agent runtime state."
		}
	}
	slashed := "/" + strings.Trim(filepath.ToSlash(clean), "/") + "/"
	for _, segment := range []string{"/loom-storage/", "/loom-main-box/"} {
		if strings.Contains(slashed, segment) {
			return "The selected folder is a generated LOOM storage view."
		}
	}
	for _, prefix := range []string{"/srv/loom/archive", "/srv/loom/storage", "/srv/loom/views", "/Volumes/loom-storage"} {
		if clean == prefix || strings.HasPrefix(clean, prefix+string(filepath.Separator)) {
			return "The selected folder is LOOM archive or generated storage state."
		}
	}
	return ""
}

func configuredRootOverlap(config Config, dataDir, target string, recheck *backupcontracts.ProtectedFolderRecheckIdentity) string {
	allowSelf := validManagedRecheckSelf(config, dataDir, target, recheck)
	for _, root := range config.Filesystem.SafeRoots {
		canonical, err := filesystemconnector.CanonicalSafeRootPath(root)
		if allowSelf && filesystemconnector.NormalizeRootKey(root.RootKey) == recheck.SafeRootKey && err == nil && filepath.Clean(canonical) == filepath.Clean(target) {
			continue
		}
		if err == nil && pathsOverlap(target, canonical) {
			return fmt.Sprintf("The selected folder overlaps configured filesystem root %q.", root.RootKey)
		}
	}
	runtimeStore := noderuntime.NewStore(dataDir)
	instances, err := runtimeStore.LoadInstances()
	if err != nil {
		return ""
	}
	for _, instance := range instances {
		if instance.Kind != noderuntime.KindWatchedRoot {
			continue
		}
		rootConfig, err := watchedRootConfigFromInstance(instance)
		if err != nil {
			continue
		}
		safeRoot, ok := filesystemconnector.FindSafeRoot(config.Filesystem, rootConfig.SafeRootKey)
		if !ok {
			continue
		}
		base, err := filesystemconnector.CanonicalSafeRootPath(safeRoot)
		if err != nil {
			continue
		}
		watchedPath := filepath.Clean(filepath.Join(base, filepath.FromSlash(rootConfig.RootRelativePath)))
		if allowSelf && instance.WorkerKey == recheck.WorkerKey && rootConfig.RootKey == recheck.RootKey && rootConfig.SafeRootKey == recheck.SafeRootKey && watchedPath == filepath.Clean(target) {
			continue
		}
		if pathsOverlap(target, watchedPath) {
			return fmt.Sprintf("The selected folder overlaps watched root %q.", rootConfig.RootKey)
		}
	}
	return ""
}

func validManagedRecheckSelf(config Config, dataDir, target string, recheck *backupcontracts.ProtectedFolderRecheckIdentity) bool {
	if recheck == nil || recheck.Validate() != nil {
		return false
	}
	safeRoot, ok := filesystemconnector.FindSafeRoot(config.Filesystem, recheck.SafeRootKey)
	if !ok || !isManagedProtectedSafeRoot(safeRoot) || managedSafeRootContractKey(safeRoot) != recheck.ContractKey {
		return false
	}
	base, err := filesystemconnector.CanonicalSafeRootPath(safeRoot)
	if err != nil || filepath.Clean(base) != filepath.Clean(target) {
		return false
	}
	instance, err := noderuntime.NewStore(dataDir).LoadInstance(recheck.WorkerKey)
	if err != nil || instance.Kind != noderuntime.KindWatchedRoot {
		return false
	}
	rootConfig, err := watchedRootConfigFromInstance(instance)
	if err != nil || rootConfig.RootKey != recheck.RootKey || rootConfig.SafeRootKey != recheck.SafeRootKey {
		return false
	}
	return filepath.Clean(filepath.Join(base, filepath.FromSlash(rootConfig.RootRelativePath))) == filepath.Clean(target)
}

func pathsOverlap(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	return left == right || filesystemconnector.IsWithin(left, right) || filesystemconnector.IsWithin(right, left)
}

func portableMountPoint(pathValue string) string {
	current := filepath.Clean(pathValue)
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return current
		}
		currentInfo, currentErr := os.Stat(current)
		parentInfo, parentErr := os.Stat(parent)
		if currentErr != nil || parentErr != nil {
			return ""
		}
		currentDevice, currentOK := portableDeviceID(currentInfo)
		parentDevice, parentOK := portableDeviceID(parentInfo)
		if currentOK && parentOK && currentDevice != parentDevice {
			return current
		}
		current = parent
	}
}

func portableDeviceID(info os.FileInfo) (uint64, bool) {
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() {
		return 0, false
	}
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, false
	}
	field := value.FieldByName("Dev")
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return field.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(field.Int()), true
	default:
		return 0, false
	}
}
