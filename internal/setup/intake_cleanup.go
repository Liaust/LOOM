package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/box"
)

const (
	RetiredIntakeCleanupSchemaVersion = "loom.setup.retired_intake_cleanup.v1"

	RetiredIntakeCleanupAbsent             = "absent"
	RetiredIntakeCleanupEligibleEmptyDir   = "eligible_empty_directory"
	RetiredIntakeCleanupEligibleKnownLink  = "eligible_known_link"
	RetiredIntakeCleanupSkippedNonEmpty    = "skipped_non_empty"
	RetiredIntakeCleanupSkippedUnknownType = "skipped_unknown_type"
	RetiredIntakeCleanupSkippedLinkTarget  = "skipped_unexpected_link_target"
	RetiredIntakeCleanupSkippedUnsafePath  = "skipped_unsafe_path"
	RetiredIntakeCleanupSkippedUnreadable  = "skipped_unreadable"
	RetiredIntakeCleanupSkippedChanged     = "skipped_changed_target"
)

type retiredIntakeCleanupCandidate struct {
	ID                  string
	Category            string
	Root                string
	RelativePath        string
	ExpectedLinkTargets []string
}

type retiredIntakeCleanupAnchor struct {
	file *os.File
	name string
	stat unix.Stat_t
}

type retiredIntakeCleanupObservation struct {
	item    RetiredIntakeCleanupItem
	anchors []retiredIntakeCleanupAnchor
	parent  *os.File
	object  *os.File
	name    string
	stat    unix.Stat_t
}

type retiredIntakeCleanupApplyHooks struct {
	beforeFinalRevalidation func(RetiredIntakeCleanupItem) error
}

func (o *retiredIntakeCleanupObservation) close() {
	if o == nil {
		return
	}
	if o.object != nil {
		_ = o.object.Close()
	}
	for index := len(o.anchors) - 1; index >= 0; index-- {
		_ = o.anchors[index].file.Close()
	}
}

func PlanRetiredIntakeCleanup(plan SetupPlan, generatedAt time.Time) (RetiredIntakeCleanupPlan, error) {
	result := RetiredIntakeCleanupPlan{
		SchemaVersion: RetiredIntakeCleanupSchemaVersion,
		GeneratedAt:   generatedAt.UTC(),
	}
	if plan.Spec.NodeKind != "main" || plan.Spec.BoxProfile != box.ProfileMain {
		result.PlanDigest = hashRetiredIntakeCleanupPlan(result)
		return result, nil
	}
	for _, candidate := range retiredIntakeCleanupCandidates(plan) {
		observation := observeRetiredIntakeCleanupCandidate(candidate, false)
		result.Items = append(result.Items, observation.item)
		observation.close()
	}
	sort.SliceStable(result.Items, func(i, j int) bool { return result.Items[i].ID < result.Items[j].ID })
	result.Summary = summarizeRetiredIntakeCleanup(result.Items)
	result.PlanDigest = hashRetiredIntakeCleanupPlan(result)
	return result, nil
}

func ApplyRetiredIntakeCleanup(input RetiredIntakeCleanupApplyInput) (RetiredIntakeCleanupResult, error) {
	return applyRetiredIntakeCleanup(input, retiredIntakeCleanupApplyHooks{})
}

func applyRetiredIntakeCleanup(input RetiredIntakeCleanupApplyInput, hooks retiredIntakeCleanupApplyHooks) (RetiredIntakeCleanupResult, error) {
	result := RetiredIntakeCleanupResult{DryRun: input.DryRun, Status: "skipped"}
	plan := input.Plan
	if plan.Spec.NodeKind != "main" || plan.Spec.BoxProfile != box.ProfileMain {
		return result, fmt.Errorf("retired intake cleanup is only valid for a main-profile setup plan")
	}
	computedSetupHash := HashPlan(plan)
	if strings.TrimSpace(plan.PlanHash) == "" || plan.PlanHash != computedSetupHash {
		return result, fmt.Errorf("setup plan hash mismatch: got %s, computed %s", plan.PlanHash, computedSetupHash)
	}
	if plan.RetiredIntakeCleanup == nil {
		return result, fmt.Errorf("setup plan has no retired intake cleanup inventory")
	}
	cleanup := *plan.RetiredIntakeCleanup
	result.PlanDigest = cleanup.PlanDigest
	if cleanup.SchemaVersion != RetiredIntakeCleanupSchemaVersion {
		return result, fmt.Errorf("unsupported retired intake cleanup schema_version %q", cleanup.SchemaVersion)
	}
	computedCleanupDigest := hashRetiredIntakeCleanupPlan(cleanup)
	if cleanup.PlanDigest == "" || cleanup.PlanDigest != computedCleanupDigest {
		return result, fmt.Errorf("retired intake cleanup digest mismatch: got %s, computed %s", cleanup.PlanDigest, computedCleanupDigest)
	}
	if strings.TrimSpace(input.ConfirmDigest) == "" || input.ConfirmDigest != cleanup.PlanDigest {
		return result, fmt.Errorf("--confirm-digest must match the reviewed retired intake cleanup digest %s", cleanup.PlanDigest)
	}
	if !input.DryRun && !input.Yes {
		return result, fmt.Errorf("retired intake cleanup apply requires --yes")
	}

	expected := map[string]retiredIntakeCleanupCandidate{}
	for _, candidate := range retiredIntakeCleanupCandidates(plan) {
		expected[candidate.ID] = candidate
	}
	for _, planned := range cleanup.Items {
		candidate, ok := expected[planned.ID]
		if !ok || !cleanupItemMatchesCandidate(planned, candidate) {
			result.Skipped = append(result.Skipped, cleanupChange(planned, RetiredIntakeCleanupSkippedUnsafePath, "item is outside the built-in retired intake cleanup boundary"))
			continue
		}
		if !planned.Eligible {
			result.Skipped = append(result.Skipped, cleanupChange(planned, planned.State, planned.Reason))
			continue
		}
		current := observeRetiredIntakeCleanupCandidate(candidate, true)
		if !current.item.Eligible || current.item.State != planned.State || current.item.AnchorDigest != planned.AnchorDigest || current.item.ObjectDigest != planned.ObjectDigest {
			message := "target changed since the reviewed plan"
			if current.item.Reason != "" {
				message += ": " + current.item.Reason
			}
			result.Skipped = append(result.Skipped, cleanupChange(planned, RetiredIntakeCleanupSkippedChanged, message))
			current.close()
			continue
		}
		if input.DryRun {
			result.Removed = append(result.Removed, cleanupChange(planned, "would_remove", "reviewed target remains exact and removable"))
			current.close()
			continue
		}
		if hooks.beforeFinalRevalidation != nil {
			if err := hooks.beforeFinalRevalidation(planned); err != nil {
				current.close()
				return result, fmt.Errorf("retired intake cleanup pre-revalidation hook: %w", err)
			}
		}
		if err := revalidateRetiredIntakeObservation(current, planned); err != nil {
			result.Skipped = append(result.Skipped, cleanupChange(planned, RetiredIntakeCleanupSkippedChanged, err.Error()))
			current.close()
			continue
		}
		flags := 0
		if planned.State == RetiredIntakeCleanupEligibleEmptyDir {
			flags = unix.AT_REMOVEDIR
		}
		if err := unix.Unlinkat(int(current.parent.Fd()), current.name, flags); err != nil {
			result.Skipped = append(result.Skipped, cleanupChange(planned, RetiredIntakeCleanupSkippedChanged, "exact non-recursive removal refused: "+err.Error()))
			current.close()
			continue
		}
		result.Removed = append(result.Removed, cleanupChange(planned, "removed", "removed exact reviewed target"))
		current.close()
	}
	if len(result.Removed) > 0 {
		if input.DryRun {
			result.Status = "planned"
		} else {
			result.Status = "applied"
		}
	} else {
		result.Status = "no_changes"
	}
	return result, nil
}

func retiredIntakeCleanupCandidates(plan SetupPlan) []retiredIntakeCleanupCandidate {
	candidates := []retiredIntakeCleanupCandidate{
		{ID: "main_box_lane", Category: "obsolete_main_name", Root: plan.Paths.BoxPath, RelativePath: box.DefaultLaneDirName},
		{ID: "main_box_legacy_lane", Category: "obsolete_main_name", Root: plan.Paths.BoxPath, RelativePath: box.LegacyLaneDirName},
		{ID: "main_box_dropzone", Category: "dropzone_owned_path", Root: plan.Paths.BoxPath, RelativePath: "Dropzone"},
		{ID: "main_box_lane_policy", Category: "obsolete_main_name", Root: plan.Paths.BoxPath, RelativePath: filepath.Join(".loom", "policies", "lane.transfer.yaml")},
		{ID: "main_box_dropzone_policy", Category: "dropzone_owned_path", Root: plan.Paths.BoxPath, RelativePath: filepath.Join(".loom", "policies", "dropzone.transfer.yaml")},
		{ID: "main_box_legacy_dropzone_state", Category: "dropzone_owned_path", Root: plan.Paths.BoxPath, RelativePath: filepath.Join(".loom", "state", "dropzone")},
		{ID: "main_box_external_dropzone_state", Category: "dropzone_owned_path", Root: plan.Paths.BoxStateRoot, RelativePath: "dropzone"},
	}
	for _, home := range mainHumanHomePaths(plan.Spec) {
		base := sanitizeID(filepath.Base(home))
		targets := []string{plan.Paths.BoxPath}
		candidates = append(candidates,
			retiredIntakeCleanupCandidate{ID: "obsolete_legacy_main_box_link_" + base, Category: "obsolete_main_link", Root: home, RelativePath: box.LegacyMainBoxLinkName, ExpectedLinkTargets: targets},
			retiredIntakeCleanupCandidate{ID: "obsolete_quoted_box_link_" + base, Category: "obsolete_main_link", Root: home, RelativePath: "'LOOM BOX'", ExpectedLinkTargets: targets},
		)
		if filepath.Base(home) == "loomdesk" {
			candidates = append(candidates, retiredIntakeCleanupCandidate{ID: "obsolete_loomdesk_box_link", Category: "obsolete_main_link", Root: home, RelativePath: "LOOM BOX", ExpectedLinkTargets: targets})
		}
	}
	unique := make([]retiredIntakeCleanupCandidate, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		key := filepath.Clean(candidate.Root) + "\x00" + filepath.Clean(candidate.RelativePath)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, candidate)
	}
	return unique
}

func observeRetiredIntakeCleanupCandidate(candidate retiredIntakeCleanupCandidate, keepHandles bool) *retiredIntakeCleanupObservation {
	item := RetiredIntakeCleanupItem{
		ID:                  candidate.ID,
		Category:            candidate.Category,
		Root:                filepath.Clean(strings.TrimSpace(candidate.Root)),
		RelativePath:        filepath.Clean(strings.TrimSpace(candidate.RelativePath)),
		ExpectedLinkTargets: cleanAbsolutePaths(candidate.ExpectedLinkTargets),
		State:               RetiredIntakeCleanupSkippedUnsafePath,
		Reason:              "candidate path is not confined",
	}
	observation := &retiredIntakeCleanupObservation{item: item}
	if !cleanupRootIsSafe(item.Root) || !cleanupRelativePathIsSafe(item.RelativePath) {
		return observation
	}
	item.Path = filepath.Join(item.Root, item.RelativePath)
	observation.item = item
	anchors, err := openCleanupRootNoFollow(item.Root)
	if errors.Is(err, unix.ENOENT) {
		observation.item.State = RetiredIntakeCleanupAbsent
		observation.item.Reason = "cleanup root is absent"
		return observation
	}
	if err != nil {
		observation.item.State = RetiredIntakeCleanupSkippedUnsafePath
		observation.item.Reason = "cleanup root is unavailable without following links: " + err.Error()
		return observation
	}
	observation.anchors = anchors
	parent := anchors[len(anchors)-1].file
	anchorStats := cleanupAnchorStats(anchors)
	components := strings.Split(item.RelativePath, string(filepath.Separator))
	for _, component := range components[:len(components)-1] {
		fd, openErr := unix.Openat(int(parent.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			observation.item.State = RetiredIntakeCleanupAbsent
			observation.item.Reason = "candidate parent is absent"
			observation.close()
			return observation
		}
		if openErr != nil {
			observation.item.State = RetiredIntakeCleanupSkippedUnsafePath
			observation.item.Reason = "candidate parent is not a confined real directory: " + openErr.Error()
			observation.close()
			return observation
		}
		next := os.NewFile(uintptr(fd), component)
		var parentStat unix.Stat_t
		if err := unix.Fstat(fd, &parentStat); err != nil {
			observation.item.State = RetiredIntakeCleanupSkippedUnreadable
			observation.item.Reason = err.Error()
			observation.anchors = append(observation.anchors, retiredIntakeCleanupAnchor{file: next, name: component})
			observation.close()
			return observation
		}
		observation.anchors = append(observation.anchors, retiredIntakeCleanupAnchor{file: next, name: component, stat: parentStat})
		parent = next
		anchorStats = append(anchorStats, parentStat)
	}
	observation.parent = parent
	observation.name = components[len(components)-1]
	observation.item.AnchorDigest = hashCleanupAnchor(anchorStats)
	if err := unix.Fstatat(int(parent.Fd()), observation.name, &observation.stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		observation.item.State = RetiredIntakeCleanupAbsent
		observation.item.Reason = "candidate is absent"
		if !keepHandles {
			observation.close()
		}
		return observation
	} else if err != nil {
		observation.item.State = RetiredIntakeCleanupSkippedUnreadable
		observation.item.Reason = err.Error()
		if !keepHandles {
			observation.close()
		}
		return observation
	}
	observation.item.ObjectDigest = hashCleanupObject(observation.stat, "")
	switch observation.stat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		observation.item.ObservedType = "directory"
		fd, openErr := unix.Openat(int(parent.Fd()), observation.name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			observation.item.State = RetiredIntakeCleanupSkippedUnsafePath
			observation.item.Reason = "candidate directory could not be opened without following links: " + openErr.Error()
			break
		}
		observation.object = os.NewFile(uintptr(fd), observation.name)
		var opened unix.Stat_t
		if err := unix.Fstat(fd, &opened); err != nil || !sameCleanupIdentity(observation.stat, opened) {
			observation.item.State = RetiredIntakeCleanupSkippedChanged
			observation.item.Reason = "candidate directory identity changed while planning"
			break
		}
		_, readErr := observation.object.Readdirnames(1)
		if readErr == io.EOF {
			observation.item.State = RetiredIntakeCleanupEligibleEmptyDir
			observation.item.Eligible = true
			observation.item.Reason = "exact directory is empty"
		} else if readErr == nil {
			observation.item.State = RetiredIntakeCleanupSkippedNonEmpty
			observation.item.Reason = "directory is non-empty; contents were not traversed"
		} else {
			observation.item.State = RetiredIntakeCleanupSkippedUnreadable
			observation.item.Reason = readErr.Error()
		}
	case unix.S_IFLNK:
		observation.item.ObservedType = "symlink"
		target, readErr := readCleanupLinkAt(int(parent.Fd()), observation.name)
		if readErr != nil {
			observation.item.State = RetiredIntakeCleanupSkippedUnreadable
			observation.item.Reason = readErr.Error()
			break
		}
		observation.item.LinkTarget = target
		observation.item.ObjectDigest = hashCleanupObject(observation.stat, target)
		if cleanupLinkTargetExpected(item.Path, target, item.ExpectedLinkTargets) {
			observation.item.State = RetiredIntakeCleanupEligibleKnownLink
			observation.item.Eligible = true
			observation.item.Reason = "link target matches an exact known obsolete target"
		} else {
			observation.item.State = RetiredIntakeCleanupSkippedLinkTarget
			observation.item.Reason = "link target is not an exact known obsolete target"
		}
	default:
		observation.item.ObservedType = cleanupFileType(uint32(observation.stat.Mode))
		observation.item.State = RetiredIntakeCleanupSkippedUnknownType
		observation.item.Reason = "only exact empty directories or exact known links may be removed"
	}
	if !keepHandles {
		observation.close()
	}
	return observation
}

func openCleanupRootNoFollow(root string) ([]retiredIntakeCleanupAnchor, error) {
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Open(string(filepath.Separator), flags, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), string(filepath.Separator))
	var rootStat unix.Stat_t
	if err := unix.Fstat(fd, &rootStat); err != nil {
		_ = current.Close()
		return nil, err
	}
	anchors := []retiredIntakeCleanupAnchor{{file: current, stat: rootStat}}
	components := strings.Split(strings.TrimPrefix(filepath.Clean(root), string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" {
			continue
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), component, flags, 0)
		if openErr != nil {
			closeCleanupAnchors(anchors)
			return nil, openErr
		}
		next := os.NewFile(uintptr(nextFD), component)
		var nextStat unix.Stat_t
		if err := unix.Fstat(nextFD, &nextStat); err != nil {
			_ = next.Close()
			closeCleanupAnchors(anchors)
			return nil, err
		}
		anchors = append(anchors, retiredIntakeCleanupAnchor{file: next, name: component, stat: nextStat})
		current = next
	}
	return anchors, nil
}

func closeCleanupAnchors(anchors []retiredIntakeCleanupAnchor) {
	for index := len(anchors) - 1; index >= 0; index-- {
		_ = anchors[index].file.Close()
	}
}

func cleanupAnchorStats(anchors []retiredIntakeCleanupAnchor) []unix.Stat_t {
	stats := make([]unix.Stat_t, 0, len(anchors))
	for _, anchor := range anchors {
		stats = append(stats, anchor.stat)
	}
	return stats
}

func revalidateRetiredIntakeObservation(current *retiredIntakeCleanupObservation, planned RetiredIntakeCleanupItem) error {
	if current == nil || current.parent == nil {
		return fmt.Errorf("target is not open under its reviewed parent")
	}
	if err := revalidateRetiredIntakeAnchors(current.anchors); err != nil {
		return err
	}
	var latest unix.Stat_t
	if err := unix.Fstatat(int(current.parent.Fd()), current.name, &latest, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("target changed before removal: %w", err)
	}
	linkTarget := ""
	if latest.Mode&unix.S_IFMT == unix.S_IFLNK {
		var err error
		linkTarget, err = readCleanupLinkAt(int(current.parent.Fd()), current.name)
		if err != nil {
			return fmt.Errorf("target link changed before removal: %w", err)
		}
	}
	if !sameCleanupIdentity(current.stat, latest) || hashCleanupObject(latest, linkTarget) != planned.ObjectDigest {
		return fmt.Errorf("target identity or digest changed before removal")
	}
	if planned.State == RetiredIntakeCleanupEligibleKnownLink && !cleanupLinkTargetExpected(planned.Path, linkTarget, planned.ExpectedLinkTargets) {
		return fmt.Errorf("target link no longer resolves to an exact known obsolete target")
	}
	if planned.State == RetiredIntakeCleanupEligibleEmptyDir {
		if current.object == nil {
			return fmt.Errorf("target directory is not held open")
		}
		if _, err := current.object.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("reset target directory check: %w", err)
		}
		if _, err := current.object.Readdirnames(1); err != io.EOF {
			if err == nil {
				return fmt.Errorf("target directory became non-empty")
			}
			return fmt.Errorf("recheck target directory: %w", err)
		}
	}
	return nil
}

func revalidateRetiredIntakeAnchors(anchors []retiredIntakeCleanupAnchor) error {
	if len(anchors) == 0 {
		return fmt.Errorf("cleanup ancestor chain is not held open")
	}
	for index, anchor := range anchors {
		var held unix.Stat_t
		if err := unix.Fstat(int(anchor.file.Fd()), &held); err != nil {
			return fmt.Errorf("cleanup ancestor descriptor %d changed before removal: %w", index, err)
		}
		if !sameCleanupIdentity(anchor.stat, held) {
			return fmt.Errorf("cleanup ancestor descriptor %d identity changed before removal", index)
		}
		if index == 0 {
			continue
		}
		parent := anchors[index-1]
		var named unix.Stat_t
		if err := unix.Fstatat(int(parent.file.Fd()), anchor.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("cleanup ancestor %q changed before removal: %w", anchor.name, err)
		}
		if named.Mode&unix.S_IFMT != unix.S_IFDIR || !sameCleanupIdentity(anchor.stat, named) {
			return fmt.Errorf("cleanup ancestor %q no longer names the held directory", anchor.name)
		}
	}
	return nil
}

func cleanupItemMatchesCandidate(item RetiredIntakeCleanupItem, candidate retiredIntakeCleanupCandidate) bool {
	return item.ID == candidate.ID && item.Category == candidate.Category &&
		filepath.Clean(item.Root) == filepath.Clean(candidate.Root) &&
		filepath.Clean(item.RelativePath) == filepath.Clean(candidate.RelativePath) &&
		filepath.Clean(item.Path) == filepath.Join(filepath.Clean(candidate.Root), filepath.Clean(candidate.RelativePath)) &&
		stringSlicesEqual(item.ExpectedLinkTargets, cleanAbsolutePaths(candidate.ExpectedLinkTargets))
}

func cleanupRootIsSafe(root string) bool {
	return root != "" && root != "." && filepath.IsAbs(root) && filepath.Clean(root) != string(filepath.Separator)
}

func cleanupRelativePathIsSafe(path string) bool {
	if path == "" || path == "." || filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func cleanAbsolutePaths(paths []string) []string {
	out := []string{}
	for _, path := range paths {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "" || path == "." || !filepath.IsAbs(path) || path == string(filepath.Separator) {
			continue
		}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func cleanupLinkTargetExpected(linkPath, target string, expected []string) bool {
	resolved := filepath.Clean(target)
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Clean(filepath.Join(filepath.Dir(linkPath), resolved))
	}
	for _, candidate := range expected {
		if resolved == filepath.Clean(candidate) {
			return true
		}
	}
	return false
}

func readCleanupLinkAt(parentFD int, name string) (string, error) {
	buffer := make([]byte, 4097)
	n, err := unix.Readlinkat(parentFD, name, buffer)
	if err != nil {
		return "", err
	}
	if n >= len(buffer)-1 {
		return "", fmt.Errorf("symlink target exceeds cleanup inspection limit")
	}
	return string(buffer[:n]), nil
}

func hashCleanupAnchor(stats []unix.Stat_t) string {
	type identity struct {
		Device uint64 `json:"device"`
		Inode  uint64 `json:"inode"`
		Mode   uint32 `json:"mode"`
	}
	values := make([]identity, 0, len(stats))
	for _, stat := range stats {
		values = append(values, identity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Mode: uint32(stat.Mode)})
	}
	return hashCleanupValue(values)
}

func hashCleanupObject(stat unix.Stat_t, linkTarget string) string {
	return hashCleanupValue(struct {
		Device     uint64 `json:"device"`
		Inode      uint64 `json:"inode"`
		Mode       uint32 `json:"mode"`
		Size       int64  `json:"size"`
		LinkTarget string `json:"link_target,omitempty"`
	}{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Mode: uint32(stat.Mode), Size: stat.Size, LinkTarget: linkTarget})
}

func hashCleanupValue(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hashRetiredIntakeCleanupPlan(plan RetiredIntakeCleanupPlan) string {
	clone := plan
	clone.PlanDigest = ""
	return hashCleanupValue(clone)
}

func summarizeRetiredIntakeCleanup(items []RetiredIntakeCleanupItem) RetiredIntakeCleanupSummary {
	summary := RetiredIntakeCleanupSummary{Total: len(items)}
	for _, item := range items {
		switch item.State {
		case RetiredIntakeCleanupAbsent:
			summary.Absent++
		case RetiredIntakeCleanupEligibleEmptyDir:
			summary.EligibleEmptyDirs++
		case RetiredIntakeCleanupEligibleKnownLink:
			summary.EligibleKnownLinks++
		default:
			summary.Skipped++
		}
	}
	return summary
}

func cleanupChange(item RetiredIntakeCleanupItem, status, message string) RetiredIntakeCleanupChange {
	return RetiredIntakeCleanupChange{ID: item.ID, Path: item.Path, Status: status, Message: message}
}

func sameCleanupIdentity(a, b unix.Stat_t) bool {
	return uint64(a.Dev) == uint64(b.Dev) && uint64(a.Ino) == uint64(b.Ino) && uint32(a.Mode) == uint32(b.Mode)
}

func cleanupFileType(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		return "regular_file"
	case unix.S_IFIFO:
		return "fifo"
	case unix.S_IFSOCK:
		return "socket"
	case unix.S_IFCHR:
		return "character_device"
	case unix.S_IFBLK:
		return "block_device"
	default:
		return "unknown"
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
