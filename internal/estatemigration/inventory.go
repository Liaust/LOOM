package estatemigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemmeta"
)

const (
	defaultMaxRoots          = 32
	defaultMaxEntries        = 250_000
	defaultMaxDepth          = 128
	defaultMaxRepositories   = 2_048
	defaultMaxGitRefs        = 100_000
	defaultMaxGitUntracked   = 100_000
	defaultMaxGitSubmodules  = 4_096
	defaultMaxGitWorktrees   = 4_096
	defaultMaxGitRemotes     = 128
	defaultMaxGitOutputBytes = 16 * 1024 * 1024
	defaultMaxPolicyBytes    = 1024 * 1024

	absoluteMaxRoots          = 256
	absoluteMaxEntries        = 2_000_000
	absoluteMaxDepth          = 1_024
	absoluteMaxRepositories   = 20_000
	absoluteMaxGitRefs        = 1_000_000
	absoluteMaxGitUntracked   = 1_000_000
	absoluteMaxGitSubmodules  = 100_000
	absoluteMaxGitWorktrees   = 100_000
	absoluteMaxGitRemotes     = 4_096
	absoluteMaxGitOutputBytes = 64 * 1024 * 1024
	absoluteMaxPolicyBytes    = 16 * 1024 * 1024
)

var rootIDPattern = func(value string) bool {
	if value == "" || len(value) > 63 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, char := range value[1:] {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

type Service struct {
	Filesystem Filesystem
	Git        GitAdapter
	Policies   PolicyFactory
}

func DefaultBounds() Bounds {
	return Bounds{
		MaxRoots:          defaultMaxRoots,
		MaxEntries:        defaultMaxEntries,
		MaxDepth:          defaultMaxDepth,
		MaxRepositories:   defaultMaxRepositories,
		MaxGitRefs:        defaultMaxGitRefs,
		MaxGitUntracked:   defaultMaxGitUntracked,
		MaxGitSubmodules:  defaultMaxGitSubmodules,
		MaxGitWorktrees:   defaultMaxGitWorktrees,
		MaxGitRemotes:     defaultMaxGitRemotes,
		MaxGitOutputBytes: defaultMaxGitOutputBytes,
		MaxPolicyBytes:    defaultMaxPolicyBytes,
	}
}

// Inventory performs metadata-only, no-follow traversal of the configured
// roots. Absolute paths exist only at adapter boundaries and are replaced by
// stable root locators before the report or its digest is constructed.
func (service Service) Inventory(ctx context.Context, config Config) (Inventory, error) {
	if err := ctx.Err(); err != nil {
		return Inventory{}, err
	}
	bounds, err := normalizeBounds(config.Bounds)
	if err != nil {
		return Inventory{}, err
	}
	specs, err := validateRootSpecs(config.Roots, bounds)
	if err != nil {
		return Inventory{}, err
	}

	filesystem := service.Filesystem
	if filesystem == nil {
		filesystem = OSFilesystem{}
	}
	git := service.Git
	if git == nil {
		git = OSGitAdapter{}
	}
	policies := service.Policies
	if policies == nil {
		policies = FilePolicyFactory{}
	}

	prepared := make([]preparedRoot, 0, len(specs))
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			closePrepared(prepared)
			return Inventory{}, err
		}
		root, openErr := filesystem.OpenRoot(spec.Path)
		if openErr != nil {
			closePrepared(prepared)
			return Inventory{}, redactedRootError(spec.ID, "open configured root", openErr)
		}
		prepared = append(prepared, preparedRoot{spec: spec, root: root, canonical: root.CanonicalPath()})
	}
	defer closePrepared(prepared)

	redactor := newPathRedactor(prepared)
	for index := range prepared {
		if err := ctx.Err(); err != nil {
			return Inventory{}, err
		}
		policy, policyErr := policies.New(ctx, prepared[index].canonical, prepared[index].spec.Ignore, bounds)
		if policyErr != nil {
			if err := ctx.Err(); err != nil {
				return Inventory{}, err
			}
			return Inventory{}, redactedRootError(prepared[index].spec.ID, "load ignore policy", policyErr)
		}
		prepared[index].policy = policy
	}

	report := Inventory{
		SchemaVersion:    InventorySchemaVersion,
		Bounds:           bounds,
		Roots:            []RootInventory{},
		Repositories:     []RepositoryObservation{},
		Overlaps:         findRootOverlaps(prepared),
		DuplicateObjects: []DuplicateObject{},
	}
	state := scanState{bounds: bounds, redactor: redactor, git: git}
	for index := range prepared {
		rootReport, scanErr := state.scanRoot(ctx, &prepared[index])
		if scanErr != nil {
			return Inventory{}, scanErr
		}
		report.Roots = append(report.Roots, rootReport)
	}
	report.Repositories = state.repositories
	sort.Slice(report.Repositories, func(left, right int) bool {
		return report.Repositories[left].Locator < report.Repositories[right].Locator
	})

	allEntries := make([]Entry, 0, state.entryCount)
	for index := range report.Roots {
		summary, summaryErr := summarizeRoot(report.Roots[index].Entries)
		if summaryErr != nil {
			return Inventory{}, summaryErr
		}
		report.Roots[index].Summary = summary
		report.Roots[index].Summary.RepositoryCount = len(state.repositoryRootCounts[report.Roots[index].ID])
		allEntries = append(allEntries, report.Roots[index].Entries...)
	}
	report.DuplicateObjects, err = duplicateObjects(allEntries)
	if err != nil {
		return Inventory{}, err
	}
	report.Totals, err = calculateAccounting(allEntries)
	if err != nil {
		return Inventory{}, err
	}

	digest, err := inventoryDigest(report)
	if err != nil {
		return Inventory{}, err
	}
	report.Digest = digest
	return report, nil
}

type preparedRoot struct {
	spec      RootSpec
	root      FilesystemRoot
	canonical string
	policy    Policy
}

func closePrepared(roots []preparedRoot) {
	for index := range roots {
		_ = roots[index].root.Close()
	}
}

type scanState struct {
	bounds               Bounds
	redactor             pathRedactor
	git                  GitAdapter
	entryCount           int
	repositories         []RepositoryObservation
	seenRepos            map[string]struct{}
	repositoryRootCounts map[string]map[string]struct{}
}

func (state *scanState) scanRoot(ctx context.Context, prepared *preparedRoot) (RootInventory, error) {
	rootReport := RootInventory{
		ID:                prepared.spec.ID,
		Locator:           rootLocator(prepared.spec.ID, "."),
		Domain:            prepared.spec.Domain,
		PolicyFingerprint: redactedFingerprint(prepared.policy.Fingerprint()),
		Entries:           []Entry{},
	}
	if state.seenRepos == nil {
		state.seenRepos = map[string]struct{}{}
		state.repositoryRootCounts = map[string]map[string]struct{}{}
	}

	rootEntry, rootInfo, err := state.inspectEntry(ctx, prepared, ".")
	if err != nil {
		return RootInventory{}, err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return RootInventory{}, redactedRootError(prepared.spec.ID, "configured root is not a real directory", ErrInvalidConfig)
	}
	rootReport.Entries = append(rootReport.Entries, rootEntry)
	if err := state.scanDirectory(ctx, prepared, ".", 0, &rootReport); err != nil {
		return RootInventory{}, err
	}
	sort.Slice(rootReport.Entries, func(left, right int) bool {
		return rootReport.Entries[left].Locator < rootReport.Entries[right].Locator
	})
	return rootReport, nil
}

func (state *scanState) scanDirectory(ctx context.Context, prepared *preparedRoot, relative string, depth int, report *RootInventory) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	before, err := prepared.root.Lstat(relative)
	if err != nil {
		return redactedRootError(prepared.spec.ID, "inspect directory", err)
	}
	children, err := prepared.root.ReadDir(relative)
	if err != nil {
		return redactedRootError(prepared.spec.ID, "read directory", err)
	}
	sort.Slice(children, func(left, right int) bool { return children[left].Name() < children[right].Name() })
	if !isGitMetadataPath(relative) && bareRepositoryShape(children) && (relative == "." && prepared.spec.Domain == DomainRepositories || strings.HasSuffix(path.Base(relative), ".git")) {
		if err := state.observeRepository(ctx, prepared, relative); err != nil {
			return err
		}
	}
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return err
		}
		childRelative := child.Name()
		if relative != "." {
			childRelative = path.Join(relative, child.Name())
		}
		childDepth := depth + 1
		if childDepth > state.bounds.MaxDepth {
			return budgetError("depth", state.bounds.MaxDepth)
		}
		entry, info, inspectErr := state.inspectEntry(ctx, prepared, childRelative)
		if inspectErr != nil {
			return inspectErr
		}
		report.Entries = append(report.Entries, entry)

		if child.Name() == ".git" {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
				return redactedGitError(entry.Locator, gitObservationError("unsafe_git_marker", nil))
			}
			repositoryRelative := path.Dir(childRelative)
			if repositoryRelative == "." {
				repositoryRelative = "."
			}
			if err := state.observeRepository(ctx, prepared, repositoryRelative); err != nil {
				return err
			}
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := state.scanDirectory(ctx, prepared, childRelative, childDepth, report); err != nil {
			return err
		}
	}
	after, err := prepared.root.Lstat(relative)
	if err != nil || !sameFileSnapshot(before, after) {
		return redactedRootError(prepared.spec.ID, "directory identity changed during inventory", ErrSourceChanged)
	}
	return nil
}

func bareRepositoryShape(entries []fs.DirEntry) bool {
	wanted := map[string]bool{"HEAD": false, "config": false, "objects": false, "refs": false}
	for _, entry := range entries {
		if _, present := wanted[entry.Name()]; present {
			wanted[entry.Name()] = true
		}
	}
	return wanted["HEAD"] && wanted["config"] && wanted["objects"] && wanted["refs"]
}

func (state *scanState) inspectEntry(ctx context.Context, prepared *preparedRoot, relative string) (Entry, fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, nil, err
	}
	state.entryCount++
	if state.entryCount > state.bounds.MaxEntries {
		return Entry{}, nil, budgetError("entries", state.bounds.MaxEntries)
	}
	before, err := prepared.root.Lstat(relative)
	if err != nil {
		return Entry{}, nil, redactedRootError(prepared.spec.ID, "inspect entry", err)
	}
	metadata, err := prepared.root.Metadata(relative)
	if err != nil {
		return Entry{}, nil, redactedRootError(prepared.spec.ID, "inspect entry metadata", err)
	}
	if err := validateFilesystemMetadata(before, metadata); err != nil {
		return Entry{}, nil, redactedRootError(prepared.spec.ID, "validate entry metadata", err)
	}
	after, err := prepared.root.Lstat(relative)
	if err != nil || !sameFileSnapshot(before, after) {
		return Entry{}, nil, redactedRootError(prepared.spec.ID, "entry identity changed during inventory", ErrSourceChanged)
	}

	resolution, err := prepared.policy.Resolve(policyPath(relative), before.IsDir())
	if err != nil {
		return Entry{}, nil, redactedRootError(prepared.spec.ID, "resolve ignore policy", err)
	}
	decision := resolution.Decision
	if isGitMetadataPath(relative) {
		decision = filepolicy.Decision{
			Path:          policyPath(relative),
			Included:      true,
			Profile:       decision.Profile,
			RuleCategory:  filepolicy.RuleCategoryContract,
			Pattern:       "**/.git/**",
			PolicyVersion: decision.PolicyVersion,
			Negated:       true,
		}
	}
	if !validPolicyDecisionText(decision) {
		return Entry{}, nil, redactedRootError(prepared.spec.ID, "validate ignore policy output", ErrSourceChanged)
	}
	entry := Entry{
		Locator:      rootLocator(prepared.spec.ID, relative),
		Domain:       prepared.spec.Domain,
		Kind:         metadata.Kind,
		LogicalBytes: nonnegativeUint64(metadata.LogicalSizeBytes),
		HardLink:     metadata.IsHardLink,
		Metadata:     metadataPosture(metadata),
		Ignore:       ignoreDecision(decision, state.redactor),
	}
	if metadata.AllocatedBytes != nil && *metadata.AllocatedBytes >= 0 {
		allocated := uint64(*metadata.AllocatedBytes)
		entry.AllocatedBytes = &allocated
	}
	if metadata.LinkCount != nil && *metadata.LinkCount >= 0 {
		links := uint64(*metadata.LinkCount)
		entry.LinkCount = &links
	}
	if metadata.DeviceID != nil && metadata.Inode != nil {
		entry.ObjectIdentity = objectIdentity(*metadata.DeviceID, *metadata.Inode)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		target, readErr := prepared.root.Readlink(relative)
		if readErr != nil {
			return Entry{}, nil, redactedRootError(prepared.spec.ID, "read symlink target", readErr)
		}
		entry.SymlinkTarget = state.redactor.redactSymlink(prepared.canonical, relative, target)
	}
	return entry, after, nil
}

func validateFilesystemMetadata(info fs.FileInfo, metadata filesystemmeta.Observation) error {
	if info == nil || !filesystemmeta.ValidObjectKind(metadata.Kind) {
		return ErrSourceChanged
	}
	expectedLogical := int64(0)
	if info.Size() > 0 {
		expectedLogical = info.Size()
	}
	if metadata.LogicalSizeBytes != expectedLogical || metadata.SourceMode != uint32(info.Mode().Perm()) {
		return ErrSourceChanged
	}
	kindMatches := false
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		kindMatches = metadata.Kind == filesystemmeta.ObjectKindSymlink
	case info.IsDir():
		kindMatches = metadata.Kind == filesystemmeta.ObjectKindDirectory || metadata.Kind == filesystemmeta.ObjectKindPackage
	case info.Mode().IsRegular():
		kindMatches = metadata.Kind == filesystemmeta.ObjectKindRegularFile
	default:
		kindMatches = metadata.Kind == filesystemmeta.ObjectKindSpecial || metadata.Kind == filesystemmeta.ObjectKindUnknown
	}
	if !kindMatches || metadata.DeviceID == nil != (metadata.Inode == nil) || metadata.AllocatedBytes != nil && *metadata.AllocatedBytes < 0 || metadata.LinkCount != nil && *metadata.LinkCount < 0 {
		return ErrSourceChanged
	}
	for _, risk := range metadata.Risks {
		if !filesystemmeta.ValidFidelityRisk(risk) {
			return ErrSourceChanged
		}
	}
	if !utf8.ValidString(metadata.PackageKind) {
		return ErrSourceChanged
	}
	for _, name := range metadata.XattrNames {
		if !utf8.ValidString(name) {
			return ErrSourceChanged
		}
	}
	return nil
}

func validPolicyDecisionText(decision filepolicy.Decision) bool {
	return utf8.ValidString(string(decision.Profile)) &&
		utf8.ValidString(string(decision.RuleCategory)) &&
		utf8.ValidString(decision.Pattern) &&
		utf8.ValidString(decision.PolicyVersion) &&
		utf8.ValidString(decision.SourceFile)
}

func (state *scanState) observeRepository(ctx context.Context, prepared *preparedRoot, relative string) error {
	before, err := prepared.root.Lstat(relative)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return redactedRootError(prepared.spec.ID, "repository root changed before Git observation", ErrSourceChanged)
	}
	absolute := prepared.canonical
	if relative != "." {
		absolute = filepath.Join(absolute, filepath.FromSlash(relative))
	}
	locator := state.redactor.redactAbsolute(absolute)
	repoKey := filepath.Clean(absolute)
	rootRepos := state.repositoryRootCounts[prepared.spec.ID]
	if rootRepos == nil {
		rootRepos = map[string]struct{}{}
		state.repositoryRootCounts[prepared.spec.ID] = rootRepos
	}
	rootRepos[repoKey] = struct{}{}
	if _, present := state.seenRepos[repoKey]; present {
		return nil
	}
	state.seenRepos[repoKey] = struct{}{}
	if len(state.repositories) >= state.bounds.MaxRepositories {
		return budgetError("repositories", state.bounds.MaxRepositories)
	}
	raw, err := state.git.Observe(ctx, absolute, state.bounds)
	if err != nil {
		return redactedGitError(locator, err)
	}
	after, statErr := prepared.root.Lstat(relative)
	if statErr != nil || !sameFileSnapshot(before, after) {
		return redactedRootError(prepared.spec.ID, "repository root changed during Git observation", ErrSourceChanged)
	}
	observation, err := redactGitObservation(raw, absolute, locator, state.redactor, state.bounds)
	if err != nil {
		return redactedGitError(locator, err)
	}
	state.repositories = append(state.repositories, observation)
	return nil
}

func validateRootSpecs(inputs []RootSpec, bounds Bounds) ([]RootSpec, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("%w: at least one source root is required", ErrInvalidConfig)
	}
	if len(inputs) > bounds.MaxRoots {
		return nil, budgetError("roots", bounds.MaxRoots)
	}
	seen := map[string]struct{}{}
	result := make([]RootSpec, len(inputs))
	copy(result, inputs)
	for index := range result {
		root := &result[index]
		root.ID = strings.TrimSpace(root.ID)
		if !rootIDPattern(root.ID) {
			return nil, fmt.Errorf("%w: root id %q is invalid", ErrInvalidConfig, root.ID)
		}
		if _, duplicate := seen[root.ID]; duplicate {
			return nil, fmt.Errorf("%w: root id %q is duplicated", ErrInvalidConfig, root.ID)
		}
		seen[root.ID] = struct{}{}
		if !root.Domain.valid() {
			return nil, fmt.Errorf("%w: root %q has an unsupported domain", ErrInvalidConfig, root.ID)
		}
		root.Path = filepath.Clean(strings.TrimSpace(root.Path))
		if root.Path == "." || !filepath.IsAbs(root.Path) {
			return nil, fmt.Errorf("%w: root %q must use an absolute path", ErrInvalidConfig, root.ID)
		}
		profile := root.Ignore.Profile
		if profile == "" {
			profile = filepolicy.ProfileManaged
		}
		parsed, err := filepolicy.ParseProfile(string(profile))
		if err != nil {
			return nil, fmt.Errorf("%w: root %q has an invalid ignore profile", ErrInvalidConfig, root.ID)
		}
		root.Ignore.Profile = parsed
		root.Ignore.ContractIncludes = sortedCopy(root.Ignore.ContractIncludes)
		root.Ignore.ContractExcludes = sortedCopy(root.Ignore.ContractExcludes)
		patterns := append(append([]string{}, root.Ignore.ContractIncludes...), root.Ignore.ContractExcludes...)
		for _, pattern := range patterns {
			cleaned := filepath.Clean(strings.TrimSpace(strings.TrimSuffix(pattern, "/")))
			if pattern == "" || filepath.IsAbs(pattern) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("%w: root %q has an unsafe contract ignore pattern", ErrInvalidConfig, root.ID)
			}
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func normalizeBounds(input Bounds) (Bounds, error) {
	defaults := DefaultBounds()
	values := []struct {
		name     string
		value    *int
		fallback int
		maximum  int
	}{
		{"max_roots", &input.MaxRoots, defaults.MaxRoots, absoluteMaxRoots},
		{"max_entries", &input.MaxEntries, defaults.MaxEntries, absoluteMaxEntries},
		{"max_depth", &input.MaxDepth, defaults.MaxDepth, absoluteMaxDepth},
		{"max_repositories", &input.MaxRepositories, defaults.MaxRepositories, absoluteMaxRepositories},
		{"max_git_refs", &input.MaxGitRefs, defaults.MaxGitRefs, absoluteMaxGitRefs},
		{"max_git_untracked", &input.MaxGitUntracked, defaults.MaxGitUntracked, absoluteMaxGitUntracked},
		{"max_git_submodules", &input.MaxGitSubmodules, defaults.MaxGitSubmodules, absoluteMaxGitSubmodules},
		{"max_git_worktrees", &input.MaxGitWorktrees, defaults.MaxGitWorktrees, absoluteMaxGitWorktrees},
		{"max_git_remotes", &input.MaxGitRemotes, defaults.MaxGitRemotes, absoluteMaxGitRemotes},
		{"max_git_output_bytes", &input.MaxGitOutputBytes, defaults.MaxGitOutputBytes, absoluteMaxGitOutputBytes},
	}
	for _, item := range values {
		if *item.value < 0 || *item.value > item.maximum {
			return Bounds{}, fmt.Errorf("%w: %s must be between 1 and %d", ErrInvalidConfig, item.name, item.maximum)
		}
		if *item.value == 0 {
			*item.value = item.fallback
		}
	}
	if input.MaxPolicyBytes < 0 || input.MaxPolicyBytes > absoluteMaxPolicyBytes {
		return Bounds{}, fmt.Errorf("%w: max_policy_bytes must be between 1 and %d", ErrInvalidConfig, absoluteMaxPolicyBytes)
	}
	if input.MaxPolicyBytes == 0 {
		input.MaxPolicyBytes = defaults.MaxPolicyBytes
	}
	return input, nil
}

func findRootOverlaps(roots []preparedRoot) []RootOverlap {
	result := []RootOverlap{}
	for left := range roots {
		for right := left + 1; right < len(roots); right++ {
			leftWithinRight := pathWithin(roots[left].canonical, roots[right].canonical)
			rightWithinLeft := pathWithin(roots[right].canonical, roots[left].canonical)
			switch {
			case filepath.Clean(roots[left].canonical) == filepath.Clean(roots[right].canonical):
				result = append(result, RootOverlap{AncestorRootID: roots[left].spec.ID, DescendantRootID: roots[right].spec.ID})
			case leftWithinRight:
				result = append(result, RootOverlap{AncestorRootID: roots[right].spec.ID, DescendantRootID: roots[left].spec.ID})
			case rightWithinLeft:
				result = append(result, RootOverlap{AncestorRootID: roots[left].spec.ID, DescendantRootID: roots[right].spec.ID})
			}
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].AncestorRootID != result[right].AncestorRootID {
			return result[left].AncestorRootID < result[right].AncestorRootID
		}
		return result[left].DescendantRootID < result[right].DescendantRootID
	})
	return result
}

func pathWithin(candidate, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type pathRedactor struct {
	roots []redactionRoot
}

type redactionRoot struct {
	id   string
	path string
}

func newPathRedactor(roots []preparedRoot) pathRedactor {
	result := pathRedactor{roots: make([]redactionRoot, 0, len(roots)*2)}
	seen := map[string]struct{}{}
	for _, root := range roots {
		for _, value := range []string{root.canonical, root.spec.Path} {
			value = filepath.Clean(value)
			key := root.spec.ID + "\x00" + value
			if _, present := seen[key]; present {
				continue
			}
			seen[key] = struct{}{}
			result.roots = append(result.roots, redactionRoot{id: root.spec.ID, path: value})
		}
	}
	sort.Slice(result.roots, func(left, right int) bool {
		if len(result.roots[left].path) != len(result.roots[right].path) {
			return len(result.roots[left].path) > len(result.roots[right].path)
		}
		return result.roots[left].id < result.roots[right].id
	})
	return result
}

func (redactor pathRedactor) redactAbsolute(value string) string {
	cleaned := filepath.Clean(value)
	for _, root := range redactor.roots {
		if !pathWithin(cleaned, root.path) {
			continue
		}
		relative, err := filepath.Rel(root.path, cleaned)
		if err == nil {
			return rootLocator(root.id, filepath.ToSlash(relative))
		}
	}
	return opaqueExternalLocator(cleaned)
}

func (redactor pathRedactor) redactSymlink(rootPath, relative, target string) string {
	if target == "" {
		return ""
	}
	resolved := target
	if !filepath.IsAbs(resolved) {
		parent := rootPath
		if relative != "." {
			parent = filepath.Dir(filepath.Join(rootPath, filepath.FromSlash(relative)))
		}
		resolved = filepath.Join(parent, resolved)
	}
	return redactor.redactAbsolute(resolved)
}

func rootLocator(rootID, relative string) string {
	if relative == "" || relative == "." {
		return "root://" + rootID
	}
	parts := strings.Split(strings.TrimPrefix(filepath.ToSlash(relative), "/"), "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return "root://" + rootID + "/" + strings.Join(parts, "/")
}

func opaqueExternalLocator(value string) string {
	digest := sha256.Sum256([]byte("external-path-v1\x00" + value))
	return "external://sha256:" + hex.EncodeToString(digest[:])
}

func metadataPosture(input filesystemmeta.Observation) MetadataPosture {
	xattrs := sortedCopy(input.XattrNames)
	risks := sortedCopy(input.Risks)
	return MetadataPosture{
		Known:            !input.PermissionDenied && input.Kind != filesystemmeta.ObjectKindUnknown,
		Mode:             input.SourceMode,
		Executable:       input.Executable,
		Sparse:           input.IsSparse,
		HasXattrs:        input.HasXattrs,
		XattrNames:       xattrs,
		HasACL:           input.HasACL,
		HasResourceFork:  input.HasResourceFork,
		HasFinderTags:    input.HasFinderTags,
		HasQuarantine:    input.HasQuarantine,
		Package:          input.IsPackage,
		PackageKind:      input.PackageKind,
		PermissionDenied: input.PermissionDenied,
		Risks:            risks,
	}
}

func ignoreDecision(input filepolicy.Decision, redactor pathRedactor) IgnoreDecision {
	source := input.SourceFile
	if filepath.IsAbs(source) {
		source = redactor.redactAbsolute(source)
	}
	return IgnoreDecision{
		Included:      input.Included,
		Profile:       input.Profile,
		RuleCategory:  input.RuleCategory,
		Pattern:       input.Pattern,
		PolicyVersion: input.PolicyVersion,
		Source:        source,
		SourceLine:    input.SourceLine,
		Negated:       input.Negated,
	}
}

func summarizeRoot(entries []Entry) (RootSummary, error) {
	accounting, err := calculateAccounting(entries)
	if err != nil {
		return RootSummary{}, err
	}
	summary := RootSummary{EntryCount: len(entries), Accounting: accounting}
	for _, entry := range entries {
		if entry.Ignore.Included {
			summary.IncludedCount++
		} else {
			summary.IgnoredCount++
		}
		switch entry.Kind {
		case filesystemmeta.ObjectKindRegularFile:
			summary.RegularFileCount++
		case filesystemmeta.ObjectKindDirectory, filesystemmeta.ObjectKindPackage:
			summary.DirectoryCount++
		case filesystemmeta.ObjectKindSymlink:
			summary.SymlinkCount++
		case filesystemmeta.ObjectKindSpecial, filesystemmeta.ObjectKindUnknown:
			summary.SpecialFileCount++
		}
		if entry.HardLink {
			summary.HardLinkCount++
		}
		if len(entry.Metadata.Risks) > 0 || entry.Metadata.HasXattrs || entry.Metadata.HasACL {
			summary.MetadataRiskCount++
		}
		if !entry.Metadata.Known || entry.AllocatedBytes == nil || entry.ObjectIdentity == "" {
			summary.MetadataUnknownCount++
		}
	}
	return summary, nil
}

func calculateAccounting(entries []Entry) (Accounting, error) {
	var result Accounting
	allSeen := map[string]uint64{}
	includedSeen := map[string]uint64{}
	var knownAllocated uint64
	var includedKnownAllocated uint64
	for _, entry := range entries {
		if err := checkedAdd(&result.LogicalBytes, entry.LogicalBytes); err != nil {
			return Accounting{}, err
		}
		if entry.Ignore.Included {
			if err := checkedAdd(&result.IncludedLogicalBytes, entry.LogicalBytes); err != nil {
				return Accounting{}, err
			}
		}
		if entry.AllocatedBytes == nil {
			result.UnknownAllocationCount++
			continue
		}
		if err := checkedAdd(&result.AllocatedBytes, *entry.AllocatedBytes); err != nil {
			return Accounting{}, err
		}
		if entry.Ignore.Included {
			if err := checkedAdd(&result.IncludedAllocatedBytes, *entry.AllocatedBytes); err != nil {
				return Accounting{}, err
			}
		}
		if entry.ObjectIdentity == "" {
			result.UnknownAllocationCount++
			continue
		}
		if err := checkedAdd(&knownAllocated, *entry.AllocatedBytes); err != nil {
			return Accounting{}, err
		}
		if existing, exists := allSeen[entry.ObjectIdentity]; exists && existing != *entry.AllocatedBytes {
			return Accounting{}, ErrSourceChanged
		} else if !exists {
			allSeen[entry.ObjectIdentity] = *entry.AllocatedBytes
			if err := checkedAdd(&result.UniqueAllocatedBytes, *entry.AllocatedBytes); err != nil {
				return Accounting{}, err
			}
		}
		if entry.Ignore.Included {
			if err := checkedAdd(&includedKnownAllocated, *entry.AllocatedBytes); err != nil {
				return Accounting{}, err
			}
			if existing, exists := includedSeen[entry.ObjectIdentity]; exists && existing != *entry.AllocatedBytes {
				return Accounting{}, ErrSourceChanged
			} else if !exists {
				includedSeen[entry.ObjectIdentity] = *entry.AllocatedBytes
				if err := checkedAdd(&result.IncludedUniqueAllocatedBytes, *entry.AllocatedBytes); err != nil {
					return Accounting{}, err
				}
			}
		}
	}
	if knownAllocated >= result.UniqueAllocatedBytes {
		result.DuplicateAllocatedBytes = knownAllocated - result.UniqueAllocatedBytes
	}
	if includedKnownAllocated >= result.IncludedUniqueAllocatedBytes {
		result.IncludedDuplicateAllocatedBytes = includedKnownAllocated - result.IncludedUniqueAllocatedBytes
	}
	return result, nil
}

func duplicateObjects(entries []Entry) ([]DuplicateObject, error) {
	type group struct {
		kind     string
		locators []string
	}
	groups := map[string]*group{}
	for _, entry := range entries {
		if entry.ObjectIdentity == "" {
			continue
		}
		item := groups[entry.ObjectIdentity]
		if item == nil {
			item = &group{kind: entry.Kind}
			groups[entry.ObjectIdentity] = item
		} else if item.kind != entry.Kind {
			return nil, ErrSourceChanged
		}
		item.locators = append(item.locators, entry.Locator)
	}
	result := []DuplicateObject{}
	for identity, item := range groups {
		if len(item.locators) < 2 {
			continue
		}
		sort.Strings(item.locators)
		result = append(result, DuplicateObject{ObjectIdentity: identity, Kind: item.kind, Locators: item.locators})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ObjectIdentity < result[right].ObjectIdentity })
	return result, nil
}

func inventoryDigest(report Inventory) (string, error) {
	report.Digest = ""
	payload, err := json.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("marshal redacted estate inventory: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func objectIdentity(device, inode int64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("filesystem-object-v1\x00%d\x00%d", device, inode)))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func policyPath(relative string) string {
	if relative == "." {
		return ""
	}
	return filepath.ToSlash(relative)
}

func isGitMetadataPath(relative string) bool {
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		if part == ".git" {
			return true
		}
	}
	return false
}

func sameFileSnapshot(left, right fs.FileInfo) bool {
	if left == nil || right == nil {
		return false
	}
	if os.SameFile(left, right) {
		return left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
	}
	return left.Name() == right.Name() && left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func nonnegativeUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func checkedAdd(target *uint64, value uint64) error {
	if ^uint64(0)-*target < value {
		return ErrMetadataOverflow
	}
	*target += value
	return nil
}

func redactedFingerprint(value string) string {
	if validDigest(value) {
		return value
	}
	digest := sha256.Sum256([]byte("policy-fingerprint-v1\x00" + value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func sortedCopy(values []string) []string {
	result := append([]string{}, values...)
	sort.Strings(result)
	if result == nil {
		result = []string{}
	}
	return result
}

func budgetError(kind string, limit int) error {
	return fmt.Errorf("%w: %s limit %d", ErrBudgetExhausted, kind, limit)
}

func redactedRootError(rootID, operation string, err error) error {
	return &inventoryOperationError{prefix: fmt.Sprintf("inventory root %q: %s", rootID, operation), cause: err}
}

func redactedGitError(locator string, err error) error {
	return &inventoryOperationError{prefix: fmt.Sprintf("observe repository %q", locator), cause: errors.Join(ErrGitUnavailable, err)}
}

type inventoryOperationError struct {
	prefix string
	cause  error
}

func (err *inventoryOperationError) Error() string { return err.prefix }
func (err *inventoryOperationError) Unwrap() error { return err.cause }

// OSFilesystem is the production read-only filesystem adapter.
type OSFilesystem struct{}

func (OSFilesystem) OpenRoot(value string) (FilesystemRoot, error) {
	absolute, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return nil, err
	}
	before, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidConfig
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	after, err := root.Lstat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = root.Close()
		return nil, ErrSourceChanged
	}
	return &osFilesystemRoot{root: root, canonical: filepath.Clean(canonical)}, nil
}

type osFilesystemRoot struct {
	root      *os.Root
	canonical string
}

func (root *osFilesystemRoot) CanonicalPath() string { return root.canonical }
func (root *osFilesystemRoot) Close() error          { return root.root.Close() }

func (root *osFilesystemRoot) Lstat(relative string) (fs.FileInfo, error) {
	name, err := confinedName(relative)
	if err != nil {
		return nil, err
	}
	return root.root.Lstat(name)
}

func (root *osFilesystemRoot) ReadDir(relative string) ([]fs.DirEntry, error) {
	name, err := confinedName(relative)
	if err != nil {
		return nil, err
	}
	return fs.ReadDir(root.root.FS(), name)
}

func (root *osFilesystemRoot) Readlink(relative string) (string, error) {
	name, err := confinedName(relative)
	if err != nil {
		return "", err
	}
	return root.root.Readlink(name)
}

func (root *osFilesystemRoot) Metadata(relative string) (filesystemmeta.Observation, error) {
	name, err := confinedName(relative)
	if err != nil {
		return filesystemmeta.Observation{}, err
	}
	absolute := root.canonical
	if name != "." {
		absolute = filepath.Join(root.canonical, name)
	}
	return filesystemmeta.DetectPath(absolute, filesystemmeta.DetectOptions{RootPath: root.canonical, IncludeXattrNames: true})
}

func confinedName(relative string) (string, error) {
	if relative == "" || relative == "." {
		return ".", nil
	}
	if !fs.ValidPath(relative) || filepath.IsAbs(relative) {
		return "", ErrInvalidConfig
	}
	return filepath.FromSlash(relative), nil
}

// FilePolicyFactory binds the existing generalized LOOM policy engine to one
// configured root. The mandatory .git override is enforced by Inventory after
// resolution, so neither user nor contract excludes can hide Git state.
type FilePolicyFactory struct{}

func (FilePolicyFactory) New(ctx context.Context, root string, config IgnoreConfig, bounds Bounds) (Policy, error) {
	includes := append([]string{}, config.ContractIncludes...)
	includes = append(includes, ".git", ".git/**", "**/.git", "**/.git/**")
	deadline, _ := ctx.Deadline()
	resolver, err := filepolicy.NewResolver(root, config.Profile, filepolicy.ResolverOptions{
		DiscoverUserRules:   config.DiscoverUserRules,
		PolicyRoot:          root,
		ContractIncludes:    includes,
		ContractExcludes:    append([]string{}, config.ContractExcludes...),
		MaxDiscoveryEntries: bounds.MaxEntries,
		MaxPolicyBytes:      bounds.MaxPolicyBytes,
		DiscoveryDeadline:   deadline,
	})
	if err != nil && strings.Contains(err.Error(), "budget exhausted") {
		return nil, errors.Join(ErrBudgetExhausted, err)
	}
	return resolver, err
}

// GitCommandRunner is an injectable, output-bounded command boundary.
type GitCommandRunner interface {
	Run(context.Context, string, int, ...string) ([]byte, error)
}

type OSGitCommandRunner struct{}

func (OSGitCommandRunner) Run(ctx context.Context, directory string, limit int, args ...string) ([]byte, error) {
	commandArgs := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.preloadindex=false", "-c", "maintenance.auto=false", "-C", directory}
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = readOnlyGitEnvironment(os.Environ())
	stdout := boundedBuffer{limit: limit}
	stderr := boundedBuffer{limit: 4096}
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, err
	}
	if stdout.truncated {
		return nil, budgetError("Git output bytes", limit)
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func readOnlyGitEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+4)
	for _, value := range environment {
		key := value
		if index := strings.IndexByte(value, '='); index >= 0 {
			key = value[:index]
		}
		if strings.HasPrefix(key, "GIT_") || key == "GCM_INTERACTIVE" || key == "LC_ALL" {
			continue
		}
		result = append(result, value)
	}
	return append(result, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GCM_INTERACTIVE=never", "LC_ALL=C")
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *boundedBuffer) Write(payload []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		keep := len(payload)
		if keep > remaining {
			keep = remaining
		}
		_, _ = buffer.buffer.Write(payload[:keep])
	}
	if len(payload) > remaining {
		buffer.truncated = true
	}
	return len(payload), nil
}

func (buffer *boundedBuffer) Bytes() []byte { return buffer.buffer.Bytes() }

type OSGitAdapter struct {
	Runner GitCommandRunner
}

func (adapter OSGitAdapter) Observe(ctx context.Context, repositoryRoot string, bounds Bounds) (RawGitObservation, error) {
	repositoryRoot = filepath.Clean(repositoryRoot)
	repositoryInfo, err := os.Lstat(repositoryRoot)
	if err != nil || !repositoryInfo.IsDir() || repositoryInfo.Mode()&os.ModeSymlink != 0 {
		return RawGitObservation{}, gitObservationError("unsafe_repository_root", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil || filepath.Clean(resolvedRoot) != repositoryRoot {
		return RawGitObservation{}, gitObservationError("repository_root_alias", err)
	}
	runner := adapter.Runner
	if runner == nil {
		runner = OSGitCommandRunner{}
	}
	remainingOutputBytes := bounds.MaxGitOutputBytes
	run := func(args ...string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		output, err := runner.Run(ctx, repositoryRoot, remainingOutputBytes, args...)
		if err != nil {
			return nil, err
		}
		if len(output) > remainingOutputBytes {
			return nil, budgetError("Git output bytes", bounds.MaxGitOutputBytes)
		}
		remainingOutputBytes -= len(output)
		return output, nil
	}

	bareOutput, err := run("rev-parse", "--is-bare-repository")
	if err != nil {
		return RawGitObservation{}, gitObservationError("repository_kind_unavailable", err)
	}
	bareValue := strings.TrimSpace(string(bareOutput))
	if bareValue != "true" && bareValue != "false" {
		return RawGitObservation{}, gitObservationError("repository_kind_malformed", nil)
	}
	bare := bareValue == "true"
	gitDirectory, err := run("rev-parse", "--absolute-git-dir")
	if err != nil {
		return RawGitObservation{}, gitObservationError("git_directory_unavailable", err)
	}
	commonDirectory, err := run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return RawGitObservation{}, gitObservationError("common_directory_unavailable", err)
	}
	gitDir := filepath.Clean(strings.TrimSpace(string(gitDirectory)))
	commonDir := filepath.Clean(strings.TrimSpace(string(commonDirectory)))
	if bare {
		if gitDir != repositoryRoot {
			return RawGitObservation{}, gitObservationError("bare_root_mismatch", nil)
		}
	} else {
		top, topErr := run("rev-parse", "--path-format=absolute", "--show-toplevel")
		if topErr != nil || filepath.Clean(strings.TrimSpace(string(top))) != repositoryRoot {
			return RawGitObservation{}, gitObservationError("root_mismatch", topErr)
		}
	}

	branchOutput, branchErr := run("symbolic-ref", "--quiet", "--short", "HEAD")
	headOutput, headErr := run("rev-parse", "--verify", "HEAD")
	if branchErr != nil && headErr != nil {
		return RawGitObservation{}, gitObservationError("head_unavailable", errors.Join(branchErr, headErr))
	}
	head := GitHead{}
	if branchErr == nil {
		head.Branch = strings.TrimSpace(string(branchOutput))
	}
	if headErr == nil {
		head.Commit = strings.TrimSpace(string(headOutput))
		if !validObjectID(head.Commit) {
			return RawGitObservation{}, gitObservationError("head_malformed", nil)
		}
	} else {
		head.Unborn = true
	}
	if head.Branch == "" && head.Commit != "" {
		head.Detached = true
	}

	refsOutput, err := run("for-each-ref", "--format=%(refname)%00%(objectname)")
	if err != nil {
		return RawGitObservation{}, gitObservationError("refs_unavailable", err)
	}
	refs, err := parseGitRefs(refsOutput, bounds.MaxGitRefs)
	if err != nil {
		return RawGitObservation{}, err
	}
	worktreeDiff := []byte{}
	indexDiff := []byte{}
	untracked := []string{}
	submodules := []RawGitSubmodule{}
	if !bare {
		worktreeDiff, indexDiff, untracked, submodules, err = observeWorkingTreeState(run, repositoryRoot, head, bounds)
		if err != nil {
			return RawGitObservation{}, err
		}
	}
	worktreeOutput, err := run("worktree", "list", "--porcelain", "-z")
	if err != nil {
		return RawGitObservation{}, gitObservationError("worktrees_unavailable", err)
	}
	worktrees, err := parseGitWorktrees(worktreeOutput, bounds.MaxGitWorktrees)
	if err != nil {
		return RawGitObservation{}, err
	}
	remotes, err := observeRemoteDigests(run, bounds.MaxGitRemotes)
	if err != nil {
		return RawGitObservation{}, err
	}
	repositoryAfter, afterErr := os.Lstat(repositoryRoot)
	if afterErr != nil || !sameFileSnapshot(repositoryInfo, repositoryAfter) {
		return RawGitObservation{}, gitObservationError("repository_root_changed", ErrSourceChanged)
	}
	return RawGitObservation{
		CommonDirectory: commonDir,
		RemoteDigests:   remotes,
		Bare:            bare,
		Head:            head,
		Refs:            refs,
		Dirty:           len(bytes.Trim(worktreeDiff, "\x00\n")) > 0 || len(bytes.Trim(indexDiff, "\x00\n")) > 0 || len(untracked) > 0,
		UntrackedPaths:  untracked,
		Submodules:      submodules,
		LinkedWorktree:  gitDir != commonDir,
		Worktrees:       worktrees,
	}, nil
}

type gitRun func(...string) ([]byte, error)

func observeWorkingTreeState(run gitRun, repositoryRoot string, head GitHead, bounds Bounds) ([]byte, []byte, []string, []RawGitSubmodule, error) {
	worktreeDiff, err := run("diff-files", "--raw", "-z", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none")
	if err != nil {
		return nil, nil, nil, nil, gitObservationError("worktree_diff_unavailable", err)
	}
	indexDiff := []byte{}
	if !head.Unborn {
		indexDiff, err = run("diff-index", "--cached", "--raw", "-z", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", "HEAD")
		if err != nil {
			return nil, nil, nil, nil, gitObservationError("index_diff_unavailable", err)
		}
	}
	untrackedOutput, err := run("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, nil, nil, nil, gitObservationError("untracked_unavailable", err)
	}
	untracked, err := parseNULTerminatedGitPaths(untrackedOutput, bounds.MaxGitUntracked)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	gitlinkOutput, err := run("ls-files", "--stage", "-z")
	if err != nil {
		return nil, nil, nil, nil, gitObservationError("gitlinks_unavailable", err)
	}
	submodules, err := parseGitlinks(gitlinkOutput, bounds.MaxGitSubmodules)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for index := range submodules {
		if submodules[index].State == "conflicted" {
			continue
		}
		moduleRoot := filepath.Join(repositoryRoot, filepath.FromSlash(submodules[index].Path))
		moduleInfo, statErr := os.Lstat(moduleRoot)
		if errors.Is(statErr, os.ErrNotExist) {
			submodules[index].State = "uninitialized"
			continue
		}
		if statErr != nil || !moduleInfo.IsDir() || moduleInfo.Mode()&os.ModeSymlink != 0 {
			return nil, nil, nil, nil, gitObservationError("unsafe_submodule_root", statErr)
		}
		moduleMarker, markerErr := os.Lstat(filepath.Join(moduleRoot, ".git"))
		if errors.Is(markerErr, os.ErrNotExist) {
			submodules[index].State = "uninitialized"
			continue
		}
		if markerErr != nil || moduleMarker.Mode()&os.ModeSymlink != 0 || !moduleMarker.IsDir() && !moduleMarker.Mode().IsRegular() {
			return nil, nil, nil, nil, gitObservationError("unsafe_submodule_marker", markerErr)
		}
		resolvedModule, resolveErr := filepath.EvalSymlinks(moduleRoot)
		if resolveErr != nil || filepath.Clean(resolvedModule) != moduleRoot {
			return nil, nil, nil, nil, gitObservationError("submodule_root_alias", resolveErr)
		}
		moduleTop, topErr := run("-C", filepath.FromSlash(submodules[index].Path), "rev-parse", "--path-format=absolute", "--show-toplevel")
		if topErr != nil || filepath.Clean(strings.TrimSpace(string(moduleTop))) != moduleRoot {
			return nil, nil, nil, nil, gitObservationError("submodule_root_mismatch", topErr)
		}
		checkedOut, observeErr := run("-C", filepath.FromSlash(submodules[index].Path), "rev-parse", "--verify", "HEAD")
		if observeErr != nil {
			submodules[index].State = "uninitialized"
			continue
		}
		checkedOutCommit := strings.TrimSpace(string(checkedOut))
		if !validObjectID(checkedOutCommit) {
			return nil, nil, nil, nil, gitObservationError("submodule_head_malformed", nil)
		}
		if checkedOutCommit != submodules[index].Commit {
			submodules[index].State = "different_commit"
			continue
		}
		moduleDiff, diffErr := run("diff-files", "--raw", "-z", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", "--", filepath.FromSlash(submodules[index].Path))
		if diffErr != nil {
			return nil, nil, nil, nil, gitObservationError("submodule_diff_unavailable", diffErr)
		}
		if len(bytes.Trim(moduleDiff, "\x00\n")) > 0 {
			submodules[index].State = "dirty"
		}
		moduleAfter, afterErr := os.Lstat(moduleRoot)
		if afterErr != nil || !sameFileSnapshot(moduleInfo, moduleAfter) {
			return nil, nil, nil, nil, gitObservationError("submodule_root_changed", ErrSourceChanged)
		}
	}
	return worktreeDiff, indexDiff, untracked, submodules, nil
}

type gitObservationFailure struct {
	code  string
	cause error
}

func (failure *gitObservationFailure) Error() string {
	return "Git observation failed: " + failure.code
}
func (failure *gitObservationFailure) Unwrap() error {
	return errors.Join(ErrGitUnavailable, failure.cause)
}

func gitObservationError(code string, cause error) error {
	return &gitObservationFailure{code: code, cause: cause}
}

func parseGitRefs(payload []byte, limit int) ([]GitRef, error) {
	result := []GitRef{}
	for _, line := range bytes.Split(payload, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.Split(line, []byte{0})
		if len(parts) != 2 || !strings.HasPrefix(string(parts[0]), "refs/") || !validObjectID(string(parts[1])) {
			return nil, gitObservationError("refs_malformed", nil)
		}
		result = append(result, GitRef{Name: string(parts[0]), Object: string(parts[1])})
		if len(result) > limit {
			return nil, budgetError("Git refs", limit)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result, nil
}

func parseNULTerminatedGitPaths(payload []byte, limit int) ([]string, error) {
	result := []string{}
	for _, record := range bytes.Split(payload, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		value := string(record)
		if !validGitRelativePath(value) {
			return nil, gitObservationError("path_invalid", nil)
		}
		result = append(result, filepath.ToSlash(value))
		if len(result) > limit {
			return nil, budgetError("Git paths", limit)
		}
	}
	sort.Strings(result)
	return result, nil
}

func parseGitlinks(payload []byte, limit int) ([]RawGitSubmodule, error) {
	byPath := map[string]RawGitSubmodule{}
	for _, record := range bytes.Split(payload, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			return nil, gitObservationError("gitlinks_malformed", nil)
		}
		fields := strings.Fields(string(record[:tab]))
		if len(fields) != 3 {
			return nil, gitObservationError("gitlinks_malformed", nil)
		}
		if fields[0] != "160000" {
			continue
		}
		modulePath := string(record[tab+1:])
		if !validObjectID(fields[1]) || !validGitRelativePath(modulePath) {
			return nil, gitObservationError("gitlink_malformed", nil)
		}
		modulePath = filepath.ToSlash(modulePath)
		state := "clean"
		if fields[2] != "0" {
			state = "conflicted"
		}
		candidate := RawGitSubmodule{Path: modulePath, Commit: fields[1], State: state}
		if existing, present := byPath[modulePath]; !present || candidate.Commit < existing.Commit {
			byPath[modulePath] = candidate
		}
		if len(byPath) > limit {
			return nil, budgetError("Git submodules", limit)
		}
	}
	result := make([]RawGitSubmodule, 0, len(byPath))
	for _, module := range byPath {
		result = append(result, module)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Path < result[right].Path })
	return result, nil
}

func parseGitWorktrees(payload []byte, limit int) ([]RawGitWorktree, error) {
	result := []RawGitWorktree{}
	current := RawGitWorktree{}
	flush := func() error {
		if current.Path == "" {
			return nil
		}
		if !filepath.IsAbs(current.Path) {
			return gitObservationError("worktree_path_invalid", nil)
		}
		result = append(result, current)
		if len(result) > limit {
			return budgetError("Git worktrees", limit)
		}
		current = RawGitWorktree{}
		return nil
	}
	for _, field := range bytes.Split(payload, []byte{0}) {
		if len(field) == 0 {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		line := string(field)
		switch {
		case strings.HasPrefix(line, "worktree "):
			if current.Path != "" {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			current.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			current.Head = strings.TrimPrefix(line, "HEAD ")
			if !validObjectID(current.Head) {
				return nil, gitObservationError("worktree_head_malformed", nil)
			}
		case strings.HasPrefix(line, "branch "):
			current.Branch = strings.TrimPrefix(line, "branch ")
		case line == "detached":
			current.Detached = true
		case line == "bare":
			current.Bare = true
		case strings.HasPrefix(line, "prunable"):
			current.Prunable = true
		case strings.HasPrefix(line, "locked"):
		default:
			return nil, gitObservationError("worktrees_malformed", nil)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Path < result[right].Path })
	return result, nil
}

func observeRemoteDigests(run func(...string) ([]byte, error), limit int) ([]string, error) {
	output, err := run("remote")
	if err != nil {
		return nil, gitObservationError("remotes_unavailable", err)
	}
	names := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		names = append(names, line)
		if len(names) > limit {
			return nil, budgetError("Git remotes", limit)
		}
	}
	sort.Strings(names)
	digests := []string{}
	for _, name := range names {
		urls, err := run("remote", "get-url", "--all", name)
		if err != nil {
			return nil, gitObservationError("remote_url_unavailable", err)
		}
		for _, rawURL := range strings.Split(strings.TrimSpace(string(urls)), "\n") {
			if rawURL == "" {
				continue
			}
			digest := sha256.Sum256([]byte("git-remote-v1\x00" + sanitizeRemoteURL(rawURL)))
			digests = append(digests, "sha256:"+hex.EncodeToString(digest[:]))
			if len(digests) > limit {
				return nil, budgetError("Git remote URLs", limit)
			}
		}
	}
	sort.Strings(digests)
	return digests, nil
}

func sanitizeRemoteURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		if parsed.Scheme == "file" || parsed.Scheme == "bundle" {
			parsed.Path = opaqueExternalLocator(parsed.Path)
		}
		return parsed.String()
	}
	if filepath.IsAbs(trimmed) {
		return opaqueExternalLocator(filepath.Clean(trimmed))
	}
	if colon := strings.IndexByte(trimmed, ':'); colon > 0 && !strings.Contains(trimmed[:colon], "/") {
		host := trimmed[:colon]
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
		return host + trimmed[colon:]
	}
	return trimmed
}

func redactGitObservation(raw RawGitObservation, repositoryRoot, locator string, redactor pathRedactor, bounds Bounds) (RepositoryObservation, error) {
	if len(raw.Refs) > bounds.MaxGitRefs || len(raw.UntrackedPaths) > bounds.MaxGitUntracked || len(raw.Submodules) > bounds.MaxGitSubmodules || len(raw.Worktrees) > bounds.MaxGitWorktrees || len(raw.RemoteDigests) > bounds.MaxGitRemotes {
		return RepositoryObservation{}, ErrBudgetExhausted
	}
	refs := append([]GitRef{}, raw.Refs...)
	seenRefs := map[string]struct{}{}
	for _, ref := range refs {
		if !validRefName(ref.Name) || !validObjectID(ref.Object) {
			return RepositoryObservation{}, gitObservationError("ref_malformed", nil)
		}
		if _, duplicate := seenRefs[ref.Name]; duplicate {
			return RepositoryObservation{}, gitObservationError("ref_duplicated", nil)
		}
		seenRefs[ref.Name] = struct{}{}
	}
	sort.Slice(refs, func(left, right int) bool { return refs[left].Name < refs[right].Name })
	remoteDigests := sortedCopy(raw.RemoteDigests)
	for _, digest := range remoteDigests {
		if !validDigest(digest) {
			return RepositoryObservation{}, gitObservationError("remote_digest_malformed", nil)
		}
	}
	if raw.CommonDirectory == "" || !filepath.IsAbs(raw.CommonDirectory) {
		return RepositoryObservation{}, gitObservationError("common_directory_malformed", nil)
	}
	if raw.Head.Commit != "" && !validObjectID(raw.Head.Commit) || raw.Head.Unborn && raw.Head.Commit != "" || raw.Head.Detached && raw.Head.Branch != "" || raw.Head.Branch != "" && !validShortBranchName(raw.Head.Branch) {
		return RepositoryObservation{}, gitObservationError("head_malformed", nil)
	}
	untracked := make([]string, 0, len(raw.UntrackedPaths))
	for _, value := range raw.UntrackedPaths {
		if !validGitRelativePath(value) {
			return RepositoryObservation{}, gitObservationError("untracked_path_invalid", nil)
		}
		untracked = append(untracked, redactor.redactAbsolute(filepath.Join(repositoryRoot, filepath.FromSlash(value))))
	}
	sort.Strings(untracked)
	submodules := make([]GitSubmodule, 0, len(raw.Submodules))
	for _, module := range raw.Submodules {
		if !validGitRelativePath(module.Path) || !validObjectID(module.Commit) || !validSubmoduleState(module.State) {
			return RepositoryObservation{}, gitObservationError("submodule_malformed", nil)
		}
		submodules = append(submodules, GitSubmodule{
			Path:   redactor.redactAbsolute(filepath.Join(repositoryRoot, filepath.FromSlash(module.Path))),
			Commit: module.Commit,
			State:  module.State,
		})
	}
	sort.Slice(submodules, func(left, right int) bool { return submodules[left].Path < submodules[right].Path })
	worktrees := make([]GitWorktree, 0, len(raw.Worktrees))
	for _, worktree := range raw.Worktrees {
		if !filepath.IsAbs(worktree.Path) || worktree.Head != "" && !validObjectID(worktree.Head) || worktree.Branch != "" && !validRefName(worktree.Branch) {
			return RepositoryObservation{}, gitObservationError("worktree_path_invalid", nil)
		}
		worktrees = append(worktrees, GitWorktree{
			Locator:  redactor.redactAbsolute(worktree.Path),
			Head:     worktree.Head,
			Branch:   worktree.Branch,
			Detached: worktree.Detached,
			Bare:     worktree.Bare,
			Prunable: worktree.Prunable,
		})
	}
	sort.Slice(worktrees, func(left, right int) bool { return worktrees[left].Locator < worktrees[right].Locator })
	identityParts := remoteDigests
	if len(identityParts) == 0 {
		commonDigest := sha256.Sum256([]byte("git-common-directory-v1\x00" + filepath.Clean(raw.CommonDirectory)))
		identityParts = []string{"sha256:" + hex.EncodeToString(commonDigest[:])}
	}
	identityDigest := sha256.Sum256([]byte("repository-identity-v1\x00" + strings.Join(identityParts, "\x00")))
	return RepositoryObservation{
		Locator:        locator,
		IdentityDigest: "sha256:" + hex.EncodeToString(identityDigest[:]),
		Bare:           raw.Bare,
		RemoteDigests:  remoteDigests,
		Head:           raw.Head,
		Refs:           refs,
		Dirty:          raw.Dirty,
		Untracked:      untracked,
		Submodules:     submodules,
		LinkedWorktree: raw.LinkedWorktree,
		Worktrees:      worktrees,
	}, nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validRefName(value string) bool {
	if !strings.HasPrefix(value, "refs/") {
		return false
	}
	return validRefPath(strings.TrimPrefix(value, "refs/"))
}

func validShortBranchName(value string) bool {
	return validRefPath(value)
}

func validRefPath(value string) bool {
	if !utf8.ValidString(value) || value == "" || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.Contains(value, "\\") {
		return false
	}
	for _, character := range value {
		if character <= ' ' || character == 0x7f || strings.ContainsRune("~^:?*[", character) {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validSubmoduleState(value string) bool {
	switch value {
	case "clean", "dirty", "uninitialized", "different_commit", "conflicted":
		return true
	default:
		return false
	}
}

func validGitRelativePath(value string) bool {
	if value == "" || strings.ContainsRune(value, 0) || filepath.IsAbs(value) {
		return false
	}
	cleaned := filepath.Clean(value)
	return cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}
