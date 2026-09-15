package repostate

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const (
	RepositoryStatePlanSchema = "repo.state_change_plan.v1"
	generatedMarkerKind       = "loom.repository-state-generated.v1"
)

type ChangeKind string

const (
	ChangeInitialize ChangeKind = "initialize"
	ChangeMigrate    ChangeKind = "migrate_project_state"
)

type MigrationDisposition string

const (
	MigrationPreserved   MigrationDisposition = "preserved"
	MigrationRenamed     MigrationDisposition = "renamed"
	MigrationSplit       MigrationDisposition = "split"
	MigrationArchived    MigrationDisposition = "archived"
	MigrationSkipped     MigrationDisposition = "skipped"
	MigrationConflicting MigrationDisposition = "conflicting"
)

type MigrationStatus string

const (
	MigrationPending  MigrationStatus = "pending"
	MigrationSkip     MigrationStatus = "skipped"
	MigrationConflict MigrationStatus = "conflicting"
	MigrationApplied  MigrationStatus = "applied"
	MigrationNotRun   MigrationStatus = "not_applied"
)

type ChangePlanOptions struct {
	RepositoryRoot string             `json:"repository_root"`
	PackRoot       string             `json:"pack_root"`
	Manifest       RepositoryManifest `json:"manifest"`
	applyHooks     *repositoryStateApplyHooks
}

type repositoryStateApplyHooks struct {
	afterReplan             func()
	afterWrite              func(int, string)
	writeReplacementTemp    func(*os.File, []byte) error
	beforeReplacementVerify func(string)
}

type GitPosture struct {
	HeadCommit    string   `json:"head_commit,omitempty"`
	Clean         bool     `json:"clean"`
	Allowed       bool     `json:"allowed"`
	ChangedPaths  []string `json:"changed_paths"`
	BlockingPaths []string `json:"blocking_paths"`
}

type MigrationTarget struct {
	Path          string          `json:"path"`
	Digest        string          `json:"digest,omitempty"`
	Mode          string          `json:"mode,omitempty"`
	CurrentDigest string          `json:"current_digest,omitempty"`
	Status        MigrationStatus `json:"status"`
	Overwrite     bool            `json:"overwrite,omitempty"`
	Reason        string          `json:"reason,omitempty"`
}

type MigrationFile struct {
	Source       string               `json:"source"`
	SourceDigest string               `json:"source_digest,omitempty"`
	Disposition  MigrationDisposition `json:"disposition"`
	Status       MigrationStatus      `json:"status"`
	Targets      []MigrationTarget    `json:"targets"`
	Reason       string               `json:"reason,omitempty"`
}

type MigrationCounts struct {
	Preserved   int `json:"preserved"`
	Renamed     int `json:"renamed"`
	Split       int `json:"split"`
	Archived    int `json:"archived"`
	Skipped     int `json:"skipped"`
	Conflicting int `json:"conflicting"`
	Pending     int `json:"pending"`
}

type ChangePlan struct {
	SchemaVersion  string          `json:"schema_version"`
	Kind           ChangeKind      `json:"kind"`
	RepositoryRoot string          `json:"repository_root"`
	PackRoot       string          `json:"pack_root"`
	Digest         string          `json:"digest"`
	Git            GitPosture      `json:"git"`
	Files          []MigrationFile `json:"files"`
	Counts         MigrationCounts `json:"counts"`
	ApplyAllowed   bool            `json:"apply_allowed"`
	CommitCreated  bool            `json:"commit_created"`
	plannedWrites  []plannedRepositoryWrite
}

type ApplyResult struct {
	Plan          ChangePlan `json:"plan"`
	Applied       bool       `json:"applied"`
	Written       []string   `json:"written"`
	CommitCreated bool       `json:"commit_created"`
	Staged        bool       `json:"staged"`
}

type plannedRepositoryWrite struct {
	Path             string
	Content          []byte
	Mode             fs.FileMode
	CurrentDigest    string
	CurrentIdentity  confinedIdentity
	ReplaceGenerated bool
	ActionIndex      int
	TargetIndex      int
}

type desiredRepositoryFile struct {
	Content         []byte
	Mode            fs.FileMode
	CurrentIdentity confinedIdentity
	ActionIndex     int
	TargetIndex     int
}

type sourceTreeFile struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

type boundRepositoryRoot struct {
	path         string
	files        []*os.File
	bindings     []directoryBinding
	rootIdentity confinedIdentity
}

func (root *boundRepositoryRoot) current() *os.File {
	return root.files[len(root.files)-1]
}

func (root *boundRepositoryRoot) Close() {
	for index := len(root.files) - 1; index >= 0; index-- {
		_ = root.files[index].Close()
	}
}

func (root *boundRepositoryRoot) verify() error {
	if len(root.files) == 0 {
		return errors.New("repository root binding is closed")
	}
	var heldRoot unix.Stat_t
	if err := unix.Fstat(int(root.files[0].Fd()), &heldRoot); err != nil || !sameConfinedIdentity(root.rootIdentity, identityFromStat(&heldRoot)) {
		return errors.New("repository root ancestor binding changed")
	}
	for index, binding := range root.bindings {
		var current unix.Stat_t
		if err := unix.Fstatat(int(root.files[binding.parentIndex].Fd()), binding.name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameConfinedIdentity(binding.identity, identityFromStat(&current)) {
			return errors.New("repository root pathname or ancestor binding changed")
		}
		var held unix.Stat_t
		if err := unix.Fstat(int(root.files[index+1].Fd()), &held); err != nil || !sameConfinedIdentity(binding.identity, identityFromStat(&held)) {
			return errors.New("held repository root ancestor identity changed")
		}
	}
	return nil
}

func openBoundRepositoryRoot(value string) (*boundRepositoryRoot, error) {
	path, err := requireExplicitRealDirectory(value, "repository")
	if err != nil {
		return nil, err
	}
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	if len(components) == 0 || (len(components) == 1 && components[0] == "") {
		return nil, errors.New("repository path must not be the filesystem root")
	}
	rootFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	filesystemRoot := os.NewFile(uintptr(rootFD), string(filepath.Separator))
	var filesystemRootStat unix.Stat_t
	if err := unix.Fstat(rootFD, &filesystemRootStat); err != nil {
		_ = filesystemRoot.Close()
		return nil, err
	}
	bound := &boundRepositoryRoot{
		path:         path,
		files:        []*os.File{filesystemRoot},
		rootIdentity: identityFromStat(&filesystemRootStat),
	}
	for _, component := range components {
		if !validConfinedComponent(component) {
			bound.Close()
			return nil, errors.New("repository path contains an invalid component")
		}
		parent := bound.current()
		var inspected unix.Stat_t
		if err := unix.Fstatat(int(parent.Fd()), component, &inspected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			bound.Close()
			return nil, err
		}
		identity := identityFromStat(&inspected)
		if identity.kind != unix.S_IFDIR {
			bound.Close()
			return nil, errors.New("repository path component is not a real directory")
		}
		childFD, err := unix.Openat(int(parent.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			bound.Close()
			return nil, err
		}
		child := os.NewFile(uintptr(childFD), component)
		var opened unix.Stat_t
		if err := unix.Fstat(childFD, &opened); err != nil || !sameConfinedIdentity(identity, identityFromStat(&opened)) {
			_ = child.Close()
			bound.Close()
			return nil, errors.New("repository path component changed while opening")
		}
		bound.bindings = append(bound.bindings, directoryBinding{parentIndex: len(bound.files) - 1, name: component, identity: identity})
		bound.files = append(bound.files, child)
	}
	if err := bound.verify(); err != nil {
		bound.Close()
		return nil, err
	}
	return bound, nil
}

func PlanInitialization(ctx context.Context, options ChangePlanOptions) (ChangePlan, error) {
	return planRepositoryStateChange(ctx, ChangeInitialize, options)
}

func PlanProjectStateMigration(ctx context.Context, options ChangePlanOptions) (ChangePlan, error) {
	return planRepositoryStateChange(ctx, ChangeMigrate, options)
}

func ApplyRepositoryStateChange(ctx context.Context, kind ChangeKind, options ChangePlanOptions, reviewedDigest string, confirmed bool) (ApplyResult, error) {
	result := ApplyResult{Written: []string{}}
	if !confirmed {
		return result, errors.New("repository state apply requires explicit confirmation")
	}
	if strings.TrimSpace(options.RepositoryRoot) == "" {
		return result, errors.New("repository state apply requires an explicit repository path")
	}
	if strings.TrimSpace(reviewedDigest) == "" {
		return result, errors.New("repository state apply requires a reviewed plan digest")
	}
	repositoryRoot, err := openBoundRepositoryRoot(options.RepositoryRoot)
	if err != nil {
		return result, err
	}
	defer repositoryRoot.Close()
	options.RepositoryRoot = repositoryRoot.path
	if err := repositoryRoot.verify(); err != nil {
		return result, err
	}
	plan, err := planRepositoryStateChange(ctx, kind, options)
	result.Plan = plan
	if err != nil {
		return result, err
	}
	if reviewedDigest != plan.Digest {
		return result, fmt.Errorf("reviewed plan digest does not match the current plan")
	}
	if !plan.Git.Allowed {
		return result, fmt.Errorf("repository Git posture is not clean or limited to the planned .repo targets")
	}
	if !plan.ApplyAllowed {
		return result, fmt.Errorf("repository state plan has conflicts")
	}
	if options.applyHooks != nil && options.applyHooks.afterReplan != nil {
		options.applyHooks.afterReplan()
	}
	if err := repositoryRoot.verify(); err != nil {
		return result, err
	}

	for index := range result.Plan.Files {
		for targetIndex := range result.Plan.Files[index].Targets {
			target := &result.Plan.Files[index].Targets[targetIndex]
			if target.Status == MigrationPending {
				target.Status = MigrationNotRun
			}
		}
		result.Plan.Files[index].Status = aggregateMigrationStatus(result.Plan.Files[index].Targets)
	}
	result.Plan.Counts = countMigrationFiles(result.Plan.Files)
	for writeIndex, write := range plan.plannedWrites {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := repositoryRoot.verify(); err != nil {
			return result, err
		}
		published, writeErr := applyRepositoryWrite(ctx, repositoryRoot, write, options.applyHooks)
		if published {
			result.Written = append(result.Written, write.Path)
			target := &result.Plan.Files[write.ActionIndex].Targets[write.TargetIndex]
			target.Status = MigrationApplied
			result.Plan.Files[write.ActionIndex].Status = aggregateMigrationStatus(result.Plan.Files[write.ActionIndex].Targets)
			result.Plan.Counts = countMigrationFiles(result.Plan.Files)
		}
		if writeErr != nil {
			return result, writeErr
		}
		if options.applyHooks != nil && options.applyHooks.afterWrite != nil {
			options.applyHooks.afterWrite(writeIndex+1, write.Path)
		}
		if err := repositoryRoot.verify(); err != nil {
			return result, err
		}
	}
	if err := repositoryRoot.verify(); err != nil {
		return result, err
	}
	result.Applied = true
	result.Plan.CommitCreated = false
	result.CommitCreated = false
	result.Staged = false
	return result, nil
}

func planRepositoryStateChange(ctx context.Context, kind ChangeKind, options ChangePlanOptions) (ChangePlan, error) {
	plan := ChangePlan{
		SchemaVersion: RepositoryStatePlanSchema,
		Kind:          kind,
		Files:         []MigrationFile{},
		CommitCreated: false,
	}
	root, err := requireExplicitRealDirectory(options.RepositoryRoot, "repository")
	if err != nil {
		return plan, err
	}
	packRoot, err := requireExplicitRealDirectory(options.PackRoot, "repository development pack")
	if err != nil {
		return plan, err
	}
	plan.RepositoryRoot = root
	plan.PackRoot = packRoot
	if err := validateChangeManifest(options.Manifest); err != nil {
		return plan, err
	}

	packFiles, err := readBoundedSourceTree(filepath.Join(packRoot, "templates", StateRoot), StateRoot)
	if err != nil {
		return plan, fmt.Errorf("read repository development pack: %w", err)
	}
	packDesired, err := buildPackDesired(packFiles, options.Manifest)
	if err != nil {
		return plan, err
	}
	desired := map[string]desiredRepositoryFile{}

	switch kind {
	case ChangeInitialize:
		if err := appendPackActions(&plan, desired, packDesired, nil); err != nil {
			return plan, err
		}
	case ChangeMigrate:
		projectFiles, readErr := readBoundedSourceTree(filepath.Join(root, ".project"), ".project")
		if readErr != nil {
			return plan, fmt.Errorf("read .project migration source: %w", readErr)
		}
		claimed := map[string]struct{}{}
		targetOwners := map[string][2]int{}
		for _, source := range projectFiles {
			action, outputs := mapProjectSource(source, packDesired, options.Manifest)
			actionIndex := len(plan.Files)
			plan.Files = append(plan.Files, action)
			for targetIndex := range plan.Files[actionIndex].Targets {
				target := plan.Files[actionIndex].Targets[targetIndex].Path
				claimed[target] = struct{}{}
				if owner, exists := targetOwners[target]; exists {
					reason := "multiple migration sources claim the same target"
					plan.Files[actionIndex].Targets[targetIndex].Status = MigrationConflict
					plan.Files[actionIndex].Targets[targetIndex].Reason = reason
					plan.Files[owner[0]].Targets[owner[1]].Status = MigrationConflict
					plan.Files[owner[0]].Targets[owner[1]].Reason = reason
					delete(desired, target)
					continue
				}
				targetOwners[target] = [2]int{actionIndex, targetIndex}
				if output, ok := outputs[target]; ok {
					desired[target] = desiredRepositoryFile{Content: output.Content, Mode: output.Mode, ActionIndex: actionIndex, TargetIndex: targetIndex}
				}
			}
		}
		if err := appendPackActions(&plan, desired, packDesired, claimed); err != nil {
			return plan, err
		}
	default:
		return plan, fmt.Errorf("unsupported repository state change kind %q", kind)
	}

	if err := inspectDesiredTargets(&plan, desired); err != nil {
		return plan, err
	}
	allowedGitPaths := make(map[string]struct{}, len(desired))
	for target := range desired {
		allowedGitPaths[target] = struct{}{}
	}
	plan.Git, err = inspectApplyGitPosture(ctx, root, allowedGitPaths)
	if err != nil {
		return plan, err
	}
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].Source < plan.Files[j].Source })
	// Rebind private write indexes after the public report is sorted.
	plan.plannedWrites = nil
	for actionIndex := range plan.Files {
		for targetIndex := range plan.Files[actionIndex].Targets {
			target := plan.Files[actionIndex].Targets[targetIndex]
			if target.Status != MigrationPending {
				continue
			}
			entry, ok := desired[target.Path]
			if !ok {
				return plan, fmt.Errorf("planned target %s has no deterministic content", target.Path)
			}
			plan.plannedWrites = append(plan.plannedWrites, plannedRepositoryWrite{
				Path:             target.Path,
				Content:          append([]byte(nil), entry.Content...),
				Mode:             entry.Mode,
				CurrentDigest:    target.CurrentDigest,
				CurrentIdentity:  entry.CurrentIdentity,
				ReplaceGenerated: target.Overwrite,
				ActionIndex:      actionIndex,
				TargetIndex:      targetIndex,
			})
		}
	}
	sort.Slice(plan.plannedWrites, func(i, j int) bool { return plan.plannedWrites[i].Path < plan.plannedWrites[j].Path })
	plan.Counts = countMigrationFiles(plan.Files)
	plan.ApplyAllowed = plan.Git.Allowed && plan.Counts.Conflicting == 0
	plan.Digest, err = digestChangePlan(plan)
	if err != nil {
		return plan, err
	}
	return plan, nil
}

func validateChangeManifest(manifest RepositoryManifest) error {
	if issues := validateManifest(manifest); len(issues) > 0 {
		return fmt.Errorf("repository manifest input is invalid: %s", issues[0].Code)
	}
	return nil
}

func requireExplicitRealDirectory(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s path is required", label)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s path must be absolute", label)
	}
	clean := filepath.Clean(value)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("resolve %s path: %w", label, err)
	}
	clean = filepath.Clean(resolved)
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("path is not a directory")
		}
		return "", fmt.Errorf("inspect %s path: %w", label, err)
	}
	return clean, nil
}

func readBoundedSourceTree(root, portableRoot string) ([]sourceTreeFile, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%s path must be absolute", portableRoot)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(resolved) != root {
		return nil, fmt.Errorf("%s source root must not contain symlinked components", portableRoot)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if err == nil {
			err = errors.New("source root is not a real directory")
		}
		return nil, err
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	rootDirectory := os.NewFile(uintptr(rootFD), root)
	defer rootDirectory.Close()
	var rootStat unix.Stat_t
	if err := unix.Fstat(rootFD, &rootStat); err != nil {
		return nil, err
	}
	rootIdentity := identityFromStat(&rootStat)
	files := []sourceTreeFile{}
	totalBytes := 0
	nodes := 0
	err = walkBoundedSourceDirectory(rootDirectory, "", portableRoot, 0, &nodes, &totalBytes, &files)
	if err != nil {
		return nil, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(rootFD, &after); err != nil || !sameConfinedIdentity(rootIdentity, identityFromStat(&after)) {
		return nil, errors.New("source root changed while it was being read")
	}
	var rebound unix.Stat_t
	if err := unix.Lstat(root, &rebound); err != nil || !sameConfinedIdentity(rootIdentity, identityFromStat(&rebound)) {
		return nil, errors.New("source root path changed while it was being read")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func walkBoundedSourceDirectory(directory *os.File, relative, portableRoot string, depth int, nodes, totalBytes *int, files *[]sourceTreeFile) error {
	if depth > maxRepositoryStateDepth {
		return fmt.Errorf("source tree exceeds %d directory levels", maxRepositoryStateDepth)
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		*nodes++
		if *nodes > maxRepositoryTreeNodes {
			return fmt.Errorf("source tree exceeds %d entries", maxRepositoryTreeNodes)
		}
		name := entry.Name()
		if !validConfinedComponent(name) {
			return fmt.Errorf("source path contains an invalid component")
		}
		childRelative := name
		if relative != "" {
			childRelative = relative + "/" + name
		}
		var inspected unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), name, &inspected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		identity := identityFromStat(&inspected)
		switch identity.kind {
		case unix.S_IFDIR:
			childFD, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			child := os.NewFile(uintptr(childFD), childRelative)
			var opened unix.Stat_t
			if err := unix.Fstat(childFD, &opened); err != nil || !sameConfinedIdentity(identity, identityFromStat(&opened)) {
				_ = child.Close()
				return errors.New("source directory changed while opening")
			}
			err = walkBoundedSourceDirectory(child, childRelative, portableRoot, depth+1, nodes, totalBytes, files)
			_ = child.Close()
			if err != nil {
				return err
			}
			if err := unix.Fstatat(int(directory.Fd()), name, &opened, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameConfinedIdentity(identity, identityFromStat(&opened)) {
				return errors.New("source directory path changed while reading")
			}
		case unix.S_IFREG:
			if len(*files) >= maxRepositoryStateFiles {
				return fmt.Errorf("source tree exceeds %d files", maxRepositoryStateFiles)
			}
			payload, err := readBoundedSourceFileAt(directory, name, identity)
			if err != nil {
				return err
			}
			*totalBytes += len(payload)
			if *totalBytes > maxRepositoryStateBytes {
				return fmt.Errorf("source tree exceeds %d bytes", maxRepositoryStateBytes)
			}
			*files = append(*files, sourceTreeFile{
				Path:    portableRoot + "/" + childRelative,
				Content: payload,
				Mode:    fs.FileMode(inspected.Mode & 0o777),
			})
		default:
			return fmt.Errorf("source path %q is not a regular file", childRelative)
		}
	}
	return nil
}

func readBoundedSourceFileAt(parent *os.File, name string, expected confinedIdentity) ([]byte, error) {
	fileFD, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fileFD), name)
	defer file.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(fileFD, &opened); err != nil || !sameConfinedIdentity(expected, identityFromStat(&opened)) {
		return nil, errors.New("source file changed while opening")
	}
	payload, err := readBoundedReader(file, maxRepositoryStateFile)
	if err != nil {
		return nil, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fileFD, &after); err != nil || !sameConfinedIdentity(expected, identityFromStat(&after)) {
		return nil, errors.New("source file changed while reading")
	}
	if err := unix.Fstatat(int(parent.Fd()), name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameConfinedIdentity(expected, identityFromStat(&after)) {
		return nil, errors.New("source file path changed while reading")
	}
	return payload, nil
}

func buildPackDesired(packFiles []sourceTreeFile, manifest RepositoryManifest) (map[string]sourceTreeFile, error) {
	desired := map[string]sourceTreeFile{}
	manifest.Repository.Aliases = sortedUniqueStrings(manifest.Repository.Aliases)
	manifest.Repository.Topics = sortedUniqueStrings(manifest.Repository.Topics)
	manifestRaw, err := yaml.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	for _, source := range packFiles {
		target := source.Path
		payload := source.Content
		if target == RepositoryManifestPath {
			payload = manifestRaw
		}
		payload = markGenerated(target, ensureFinalNewline(payload))
		desired[target] = sourceTreeFile{Path: target, Content: payload, Mode: normalizedPortableMode(source.Mode)}
	}
	if _, ok := desired[RepositoryManifestPath]; !ok {
		return nil, errors.New("repository development pack is missing templates/.repo/repo.yaml")
	}
	return desired, nil
}

func appendPackActions(plan *ChangePlan, desired map[string]desiredRepositoryFile, pack map[string]sourceTreeFile, claimed map[string]struct{}) error {
	paths := make([]string, 0, len(pack))
	for path := range pack {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, target := range paths {
		if _, exists := claimed[target]; exists {
			continue
		}
		source := pack[target]
		actionIndex := len(plan.Files)
		action := MigrationFile{
			Source:       "pack:templates/" + target,
			SourceDigest: digestBytes(source.Content),
			Disposition:  MigrationPreserved,
			Status:       MigrationPending,
			Targets:      []MigrationTarget{{Path: target, Digest: digestBytes(source.Content), Status: MigrationPending}},
			Reason:       "initialize from the explicit project-local repository development pack",
		}
		plan.Files = append(plan.Files, action)
		desired[target] = desiredRepositoryFile{Content: source.Content, Mode: source.Mode, ActionIndex: actionIndex, TargetIndex: 0}
	}
	return nil
}

func mapProjectSource(source sourceTreeFile, pack map[string]sourceTreeFile, manifest RepositoryManifest) (MigrationFile, map[string]sourceTreeFile) {
	action := MigrationFile{
		Source:       source.Path,
		SourceDigest: digestBytes(source.Content),
		Disposition:  MigrationConflicting,
		Status:       MigrationConflict,
		Targets:      []MigrationTarget{},
	}
	outputs := map[string]sourceTreeFile{}
	addOutput := func(target string, payload []byte, mode fs.FileMode, generated bool) {
		if generated {
			payload = markGenerated(target, ensureFinalNewline(payload))
		}
		payload = ensureFinalNewline(payload)
		action.Targets = append(action.Targets, MigrationTarget{Path: target, Digest: digestBytes(payload), Status: MigrationPending})
		outputs[target] = sourceTreeFile{Path: target, Content: payload, Mode: normalizedPortableMode(mode)}
	}
	addPack := func(target string) bool {
		packed, ok := pack[target]
		if !ok {
			action.Reason = "required repository development pack target is missing"
			return false
		}
		action.Targets = append(action.Targets, MigrationTarget{Path: target, Digest: digestBytes(packed.Content), Status: MigrationPending})
		outputs[target] = packed
		return true
	}
	portableCopy := func(target string, disposition MigrationDisposition) {
		if unsafePortableMigrationSource(source.Path, source.Content) {
			action.Disposition = MigrationConflicting
			action.Reason = "source contains forbidden portable data or a sensitive path and requires manual reconciliation"
			action.Targets = append(action.Targets, MigrationTarget{Path: target, Status: MigrationConflict, Reason: action.Reason})
			return
		}
		action.Disposition = disposition
		action.Status = MigrationPending
		addOutput(target, source.Content, source.Mode, false)
	}

	relative := strings.TrimPrefix(source.Path, ".project/")
	switch relative {
	case "project.yaml":
		action.Disposition = MigrationSplit
		action.Status = MigrationPending
		if !addPack(RepositoryManifestPath) {
			action.Status = MigrationConflict
		}
		action.Reason = "repository identity is supplied explicitly; runtime worktree/path/authority fields are skipped"
	case "PROJECT.md":
		payload := rewriteRepositoryTerminology(source.Content)
		if unsafePortableMigrationSource(source.Path, payload) {
			action.Targets = append(action.Targets, MigrationTarget{Path: ".repo/REPOSITORY.md", Status: MigrationConflict, Reason: "rewritten repository guidance still contains forbidden host-local data"})
			action.Reason = "repository guidance requires manual portable-path reconciliation"
			break
		}
		action.Disposition = MigrationRenamed
		action.Status = MigrationPending
		addOutput(".repo/REPOSITORY.md", payload, source.Mode, true)
	case "README.md":
		payload := rewriteRepositoryTerminology(source.Content)
		action.Disposition = MigrationPreserved
		if unsafePortableMigrationSource(source.Path, payload) {
			action.Targets = append(action.Targets, MigrationTarget{Path: ".repo/README.md", Status: MigrationConflict, Reason: "repository index requires manual portable-path reconciliation"})
			action.Reason = "repository index requires manual portable-path reconciliation"
			break
		}
		action.Status = MigrationPending
		addOutput(".repo/README.md", payload, source.Mode, true)
	case "STATE.md", "ROADMAP.md":
		portableCopy(".repo/"+relative, MigrationPreserved)
	case "future/INDEX.md":
		portableCopy(".repo/future/README.md", MigrationRenamed)
	case "initiatives/README.md", "features/README.md", "decisions/README.md", "releases/README.md":
		portableCopy(".repo/"+relative, MigrationPreserved)
	case "integration/STATUS.md":
		portableCopy(".repo/integrations/STATUS.md", MigrationRenamed)
	case "protocols/AI_WORKTREE_WORKFLOW.md":
		action.Disposition = MigrationSplit
		action.Status = MigrationPending
		for _, target := range []string{".repo/protocols/WORKTREE_OWNERSHIP.md", ".repo/protocols/CODEX_WORKFLOW.md", ".repo/protocols/ORCA_WORKFLOW.md"} {
			if !addPack(target) {
				action.Status = MigrationConflict
			}
		}
		action.Reason = "replace the shared legacy harness workflow with accepted shared ownership plus distinct native-harness guidance"
	case "protocols/PROJECT_STATE_PROTOCOL.md":
		action.Disposition = MigrationRenamed
		action.Status = MigrationPending
		if !addPack(".repo/protocols/REPOSITORY_STATE_PROTOCOL.md") {
			action.Status = MigrationConflict
		}
	case "templates/decision.md":
		action.Disposition = MigrationSplit
		action.Status = MigrationPending
		for _, target := range []string{".repo/templates/decision/decision.md", ".repo/templates/decision/decision.yaml"} {
			if !addPack(target) {
				action.Status = MigrationConflict
			}
		}
	default:
		switch {
		case strings.HasPrefix(relative, "archive/"):
			portableCopy(".repo/archive/"+strings.TrimPrefix(relative, "archive/"), MigrationArchived)
		case strings.HasPrefix(relative, "architecture/"):
			portableCopy(".repo/architecture/"+strings.TrimPrefix(relative, "architecture/"), MigrationPreserved)
		case strings.HasPrefix(relative, "protocols/"):
			portableCopy(".repo/protocols/"+strings.TrimPrefix(relative, "protocols/"), MigrationPreserved)
		case strings.HasPrefix(relative, "templates/docs/"):
			portableCopy(".repo/templates/docs/"+strings.TrimPrefix(relative, "templates/docs/"), MigrationPreserved)
		case strings.HasPrefix(relative, "templates/"):
			target := ".repo/" + relative
			disposition := MigrationPreserved
			if strings.HasPrefix(relative, "templates/future/") || strings.HasPrefix(relative, "templates/initiative/") {
				disposition = MigrationSplit
			}
			if packed, ok := pack[target]; ok {
				action.Disposition = disposition
				action.Status = MigrationPending
				action.Targets = append(action.Targets, MigrationTarget{Path: target, Digest: digestBytes(packed.Content), Status: MigrationPending})
				outputs[target] = packed
				action.Reason = "use the version-matched repository lifecycle template"
			} else {
				portableCopy(target, disposition)
			}
		case strings.HasPrefix(relative, "features/"):
			mapFeatureSource(&action, outputs, source, relative, addOutput, portableCopy)
		case strings.HasPrefix(relative, "releases/"):
			mapReleaseSource(&action, outputs, source, relative, addOutput, portableCopy)
		case strings.HasPrefix(relative, "future/") && strings.HasSuffix(relative, "/outline.md"):
			portableCopy(".repo/"+relative, MigrationSplit)
			action.Targets = append(action.Targets, MigrationTarget{Path: strings.TrimSuffix(".repo/"+relative, "outline.md") + "future.yaml", Status: MigrationConflict, Reason: "future lifecycle metadata requires explicit review"})
			action.Status = MigrationConflict
			action.Reason = "narrative is preserved but a reviewed future manifest is required"
		case strings.HasPrefix(relative, "initiatives/"):
			parts := strings.Split(relative, "/")
			if len(parts) == 3 && parts[2] == "overview.md" {
				portableCopy(".repo/"+relative, MigrationSplit)
				action.Targets = append(action.Targets, MigrationTarget{Path: ".repo/initiatives/" + parts[1] + "/initiative.yaml", Status: MigrationConflict, Reason: "initiative lifecycle metadata requires explicit review"})
				action.Status = MigrationConflict
				action.Reason = "narrative is preserved but a reviewed initiative manifest is required"
			} else {
				portableCopy(".repo/"+relative, MigrationPreserved)
			}
		case strings.HasPrefix(relative, "decisions/ADR-") && strings.HasSuffix(relative, ".md"):
			base := strings.TrimSuffix(filepath.Base(relative), ".md")
			id := base
			if dash := strings.Index(strings.TrimPrefix(base, "ADR-"), "-"); dash >= 0 {
				id = "ADR-" + strings.TrimPrefix(base, "ADR-")[:dash]
			}
			targetRoot := ".repo/decisions/" + id
			portableCopy(targetRoot+"/decision.md", MigrationSplit)
			action.Targets = append(action.Targets, MigrationTarget{Path: targetRoot + "/decision.yaml", Status: MigrationConflict, Reason: "decision lifecycle metadata requires explicit review"})
			action.Status = MigrationConflict
			action.Reason = "decision narrative is preserved but a reviewed decision manifest is required"
		default:
			action.Disposition = MigrationConflicting
			action.Status = MigrationConflict
			action.Reason = "source path has no frozen v1 compatibility mapping"
		}
	}
	if len(action.Targets) == 0 && action.Status != MigrationConflict {
		action.Disposition = MigrationSkipped
		action.Status = MigrationSkip
		action.Reason = "source has no portable repository target"
	}
	return action, outputs
}

func mapFeatureSource(action *MigrationFile, outputs map[string]sourceTreeFile, source sourceTreeFile, relative string, addOutput func(string, []byte, fs.FileMode, bool), portableCopy func(string, MigrationDisposition)) {
	parts := strings.Split(relative, "/")
	if len(parts) < 3 {
		action.Disposition = MigrationConflicting
		action.Status = MigrationConflict
		action.Reason = "feature source path is incomplete"
		return
	}
	target := ".repo/" + relative
	if parts[2] == "worktree_progress.md" || parts[2] == "handoff.md" {
		action.Disposition = MigrationSplit
		if unsafePortableMigrationSource(source.Path, source.Content) {
			action.Status = MigrationConflict
			action.Reason = "runtime-only worktree or handoff data requires manual removal before migration"
			action.Targets = append(action.Targets, MigrationTarget{Path: target, Status: MigrationConflict, Reason: action.Reason})
			return
		}
		action.Status = MigrationPending
		addOutput(target, source.Content, source.Mode, false)
		return
	}
	if parts[2] != "feature.yaml" {
		portableCopy(target, MigrationPreserved)
		return
	}
	if unsafePortableMigrationSource(source.Path, source.Content) {
		action.Disposition = MigrationPreserved
		action.Status = MigrationConflict
		action.Reason = "feature manifest contains forbidden portable data"
		action.Targets = append(action.Targets, MigrationTarget{Path: target, Status: MigrationConflict, Reason: action.Reason})
		return
	}
	payload, err := transformLifecycleManifest(ObjectKindFeature, parts[1], source.Content)
	if err != nil {
		action.Disposition = MigrationConflicting
		action.Status = MigrationConflict
		action.Reason = "feature manifest requires manual reconciliation: " + boundedDiagnostic(err.Error())
		action.Targets = append(action.Targets, MigrationTarget{Path: target, Status: MigrationConflict, Reason: action.Reason})
		return
	}
	action.Disposition = MigrationPreserved
	action.Status = MigrationPending
	addOutput(target, payload, source.Mode, true)
}

func mapReleaseSource(action *MigrationFile, outputs map[string]sourceTreeFile, source sourceTreeFile, relative string, addOutput func(string, []byte, fs.FileMode, bool), portableCopy func(string, MigrationDisposition)) {
	parts := strings.Split(relative, "/")
	if len(parts) < 3 {
		action.Disposition = MigrationConflicting
		action.Status = MigrationConflict
		action.Reason = "release source path is incomplete"
		return
	}
	target := ".repo/" + relative
	if parts[2] != "release.yaml" {
		portableCopy(target, MigrationPreserved)
		return
	}
	if unsafePortableMigrationSource(source.Path, source.Content) {
		action.Disposition = MigrationPreserved
		action.Status = MigrationConflict
		action.Reason = "release manifest contains forbidden portable data"
		action.Targets = append(action.Targets, MigrationTarget{Path: target, Status: MigrationConflict, Reason: action.Reason})
		return
	}
	payload, err := transformLifecycleManifest(ObjectKindRelease, parts[1], source.Content)
	if err != nil {
		action.Disposition = MigrationConflicting
		action.Status = MigrationConflict
		action.Reason = "release manifest requires manual reconciliation: " + boundedDiagnostic(err.Error())
		action.Targets = append(action.Targets, MigrationTarget{Path: target, Status: MigrationConflict, Reason: action.Reason})
		return
	}
	action.Disposition = MigrationPreserved
	action.Status = MigrationPending
	addOutput(target, payload, source.Mode, true)
}

func transformLifecycleManifest(kind ObjectKind, directory string, payload []byte) ([]byte, error) {
	if _, err := decodeYAMLDocument(payload); err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if err := yaml.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	schema := map[ObjectKind]string{ObjectKindFeature: "repo.feature.v1", ObjectKindRelease: "repo.release.v1"}[kind]
	if schema == "" {
		return nil, fmt.Errorf("unsupported lifecycle kind %s", kind)
	}
	fields["kind"] = string(kind)
	fields["schema_version"] = schema
	fields["id"] = string(kind) + "/" + directory
	if kind == ObjectKindFeature {
		fields["slug"] = directory
	} else {
		fields["version"] = directory
	}
	return yaml.Marshal(fields)
}

func rewriteRepositoryTerminology(payload []byte) []byte {
	text := string(payload)
	replacements := []struct{ old, new string }{
		{"Project Definition", "Repository Definition"},
		{"Project Control Plane", "Repository Control Plane"},
		{"project-level", "repository-level"},
		{"Project-specific", "Repository-specific"},
		{"project-specific", "repository-specific"},
		{".project/", ".repo/"},
	}
	for _, replacement := range replacements {
		text = strings.ReplaceAll(text, replacement.old, replacement.new)
	}
	return []byte(text)
}

func unsafePortableMigrationSource(path string, payload []byte) bool {
	lower := strings.ToLower(path)
	base := strings.ToLower(filepath.Base(path))
	if base == ".env" || strings.HasPrefix(base, ".env.") || strings.Contains(base, "credential") || strings.Contains(base, "secret") || strings.Contains(lower, "/private-memory") {
		return true
	}
	return containsHostAbsolutePath(payload)
}

func inspectDesiredTargets(plan *ChangePlan, desired map[string]desiredRepositoryFile) error {
	for actionIndex := range plan.Files {
		action := &plan.Files[actionIndex]
		for targetIndex := range action.Targets {
			target := &action.Targets[targetIndex]
			if target.Status == MigrationConflict {
				continue
			}
			entry, ok := desired[target.Path]
			if !ok {
				target.Status = MigrationConflict
				target.Reason = "target has no deterministic generated content"
				continue
			}
			target.Mode = portableModeString(entry.Mode)
			payload, identity, present, err := readExistingRepositoryTarget(plan.RepositoryRoot, target.Path)
			if err != nil {
				target.Status = MigrationConflict
				target.Reason = boundedDiagnostic(err.Error())
				continue
			}
			if !present {
				target.Status = MigrationPending
				continue
			}
			target.CurrentDigest = digestBytes(payload)
			if bytes.Equal(payload, entry.Content) {
				target.Status = MigrationSkip
				target.Reason = "existing target is byte-identical"
				continue
			}
			if generatedPayloadValid(target.Path, payload) {
				target.Status = MigrationPending
				target.Overwrite = true
				target.Reason = "existing target has proven generated ownership and a matching embedded content digest"
				entry.CurrentIdentity = identity
				desired[target.Path] = entry
				continue
			}
			target.Status = MigrationConflict
			target.Reason = "existing target is repository-owned or its generated digest no longer matches"
		}
		action.Status = aggregateMigrationStatus(action.Targets)
		if action.Status == MigrationConflict && action.Reason == "" {
			action.Reason = "one or more targets conflict"
		}
	}
	return nil
}

func inspectApplyGitPosture(ctx context.Context, root string, allowedPaths map[string]struct{}) (GitPosture, error) {
	posture := GitPosture{ChangedPaths: []string{}, BlockingPaths: []string{}}
	runner := OSGitCommandRunner{}
	top, err := runner.Run(ctx, root, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return posture, fmt.Errorf("repository state apply requires a Git repository: %w", err)
	}
	if !samePath(root, strings.TrimSpace(string(top))) {
		return posture, errors.New("repository path must be the exact Git worktree root")
	}
	if head, headErr := runner.Run(ctx, root, "rev-parse", "--verify", "HEAD"); headErr == nil {
		posture.HeadCommit = strings.TrimSpace(string(head))
	}
	status, err := runner.Run(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return posture, fmt.Errorf("inspect repository Git posture: %w", err)
	}
	paths, err := parseApplyStatus(status)
	if err != nil {
		return posture, err
	}
	posture.ChangedPaths = paths
	posture.Clean = len(paths) == 0
	for _, changed := range paths {
		if _, allowed := allowedPaths[changed]; !allowed {
			posture.BlockingPaths = append(posture.BlockingPaths, changed)
		}
	}
	posture.Allowed = len(posture.BlockingPaths) == 0
	return posture, nil
}

func parseApplyStatus(payload []byte) ([]string, error) {
	records := bytes.Split(payload, []byte{0})
	paths := []string{}
	for index := 0; index < len(records); index++ {
		record := string(records[index])
		if record == "" {
			continue
		}
		if len(record) < 4 || record[2] != ' ' {
			return nil, errors.New("Git returned malformed status output")
		}
		status := record[:2]
		path := filepath.ToSlash(record[3:])
		if !validGitStatusPath(path) {
			return nil, errors.New("Git returned a non-portable status path")
		}
		paths = append(paths, path)
		if strings.ContainsAny(status, "RC") {
			index++
			if index >= len(records) || len(records[index]) == 0 {
				return nil, errors.New("Git rename status is missing its source path")
			}
			original := filepath.ToSlash(string(records[index]))
			if !validGitStatusPath(original) {
				return nil, errors.New("Git returned a non-portable rename source path")
			}
			paths = append(paths, original)
		}
	}
	return sortedUniqueStrings(paths), nil
}

func validGitStatusPath(value string) bool {
	if value == "" || strings.Contains(value, "\\") || strings.ContainsRune(value, 0) || filepath.IsAbs(value) {
		return false
	}
	return filepath.ToSlash(filepath.Clean(value)) == value && value != ".." && !strings.HasPrefix(value, "../")
}

func readExistingRepositoryTarget(root, target string) ([]byte, confinedIdentity, bool, error) {
	if !validRepoSourcePath(target) {
		return nil, confinedIdentity{}, false, fmt.Errorf("target %q is outside .repo", target)
	}
	state, present, err := openStateRoot(root, nil)
	if err != nil || !present {
		return nil, confinedIdentity{}, false, err
	}
	defer state.Close()
	payload, identity, err := state.readRegularFile(target, nil, maxRepositoryStateFile, nil)
	if confinedErrorHasKind(err, confinedMissing) {
		return nil, confinedIdentity{}, false, nil
	}
	if err != nil {
		return nil, confinedIdentity{}, false, err
	}
	return payload, identity, true, nil
}

func applyRepositoryWrite(ctx context.Context, repositoryRoot *boundRepositoryRoot, write plannedRepositoryWrite, hooks *repositoryStateApplyHooks) (bool, error) {
	components, err := confinedSourceComponents(write.Path)
	if err != nil {
		return false, err
	}
	if err := repositoryRoot.verify(); err != nil {
		return false, err
	}
	state, err := openOrCreateWritableStateRoot(repositoryRoot)
	if err != nil {
		return false, err
	}
	defer state.Close()
	parent, err := openOrCreateWriteDirectory(repositoryRoot, state, components[:len(components)-1])
	if err != nil {
		return false, err
	}
	defer parent.Close()
	if write.ReplaceGenerated {
		return replaceGeneratedTarget(ctx, repositoryRoot, parent, components[len(components)-1], write, hooks)
	}
	return createRepositoryTarget(ctx, repositoryRoot, parent, components[len(components)-1], write)
}

func openOrCreateWritableStateRoot(root *boundRepositoryRoot) (*stateRootHandle, error) {
	if err := root.verify(); err != nil {
		return nil, err
	}
	repositoryFD, err := unix.Dup(int(root.current().Fd()))
	if err != nil {
		return nil, err
	}
	repository := os.NewFile(uintptr(repositoryFD), root.path)
	var inspected unix.Stat_t
	if err := unix.Fstatat(repositoryFD, StateRoot, &inspected, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		if err := root.verify(); err != nil {
			_ = repository.Close()
			return nil, err
		}
		if err := unix.Mkdirat(repositoryFD, StateRoot, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
			_ = repository.Close()
			return nil, err
		}
		if err := syncDirectory(repository); err != nil {
			_ = repository.Close()
			return nil, err
		}
		if err := unix.Fstatat(repositoryFD, StateRoot, &inspected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			_ = repository.Close()
			return nil, err
		}
	} else if err != nil {
		_ = repository.Close()
		return nil, err
	}
	identity := identityFromStat(&inspected)
	if identity.kind != unix.S_IFDIR {
		_ = repository.Close()
		return nil, errors.New("repository state root is not a real directory")
	}
	stateFD, err := unix.Openat(repositoryFD, StateRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = repository.Close()
		return nil, err
	}
	state := os.NewFile(uintptr(stateFD), StateRoot)
	var stateStat unix.Stat_t
	if err := unix.Fstat(stateFD, &stateStat); err != nil {
		_ = state.Close()
		_ = repository.Close()
		return nil, err
	}
	if !sameConfinedIdentity(identity, identityFromStat(&stateStat)) {
		_ = state.Close()
		_ = repository.Close()
		return nil, errors.New("repository state root changed while opening")
	}
	handle := &stateRootHandle{
		repositoryDirectory: repository,
		stateDirectory:      state,
		identity:            identityFromStat(&stateStat),
	}
	if err := handle.verifyRootBinding(); err != nil {
		_ = handle.Close()
		return nil, err
	}
	if err := root.verify(); err != nil {
		_ = handle.Close()
		return nil, err
	}
	return handle, nil
}

func openOrCreateWriteDirectory(repositoryRoot *boundRepositoryRoot, root *stateRootHandle, components []string) (*boundDirectory, error) {
	if err := repositoryRoot.verify(); err != nil {
		return nil, err
	}
	rootFD, err := unix.Dup(int(root.stateDirectory.Fd()))
	if err != nil {
		return nil, err
	}
	directory := &boundDirectory{root: root, files: []*os.File{os.NewFile(uintptr(rootFD), StateRoot)}}
	for _, component := range components {
		if !validConfinedComponent(component) {
			directory.Close()
			return nil, errors.New("destination contains an invalid path component")
		}
		parent := directory.current()
		var inspected unix.Stat_t
		if err := unix.Fstatat(int(parent.Fd()), component, &inspected, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
			if err := repositoryRoot.verify(); err != nil {
				directory.Close()
				return nil, err
			}
			if err := unix.Mkdirat(int(parent.Fd()), component, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
				directory.Close()
				return nil, err
			}
			if err := syncDirectory(parent); err != nil {
				directory.Close()
				return nil, err
			}
			if err := unix.Fstatat(int(parent.Fd()), component, &inspected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				directory.Close()
				return nil, err
			}
		} else if err != nil {
			directory.Close()
			return nil, err
		}
		identity := identityFromStat(&inspected)
		if identity.kind != unix.S_IFDIR {
			directory.Close()
			return nil, errors.New("destination path component is not a real directory")
		}
		childFD, err := unix.Openat(int(parent.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			directory.Close()
			return nil, err
		}
		child := os.NewFile(uintptr(childFD), component)
		var opened unix.Stat_t
		if err := unix.Fstat(childFD, &opened); err != nil || !sameConfinedIdentity(identity, identityFromStat(&opened)) {
			_ = child.Close()
			directory.Close()
			return nil, errors.New("destination directory changed while opening")
		}
		directory.bindings = append(directory.bindings, directoryBinding{parentIndex: len(directory.files) - 1, name: component, identity: identity})
		directory.files = append(directory.files, child)
	}
	if err := directory.verify(); err != nil {
		directory.Close()
		return nil, err
	}
	if err := repositoryRoot.verify(); err != nil {
		directory.Close()
		return nil, err
	}
	return directory, nil
}

func replaceGeneratedTarget(ctx context.Context, repositoryRoot *boundRepositoryRoot, parent *boundDirectory, name string, write plannedRepositoryWrite, hooks *repositoryStateApplyHooks) (bool, error) {
	writer := writeAll
	if hooks != nil && hooks.writeReplacementTemp != nil {
		writer = hooks.writeReplacementTemp
	}
	temporaryName, temporary, temporaryIdentity, err := createTemporaryTarget(parent, write, writer)
	if err != nil {
		return false, err
	}
	defer removeTemporaryTarget(parent, temporaryName, temporaryIdentity)
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if hooks != nil && hooks.beforeReplacementVerify != nil {
		hooks.beforeReplacementVerify(write.Path)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := repositoryRoot.verify(); err != nil {
		return false, err
	}
	if err := parent.verify(); err != nil {
		return false, err
	}
	targetIdentity, err := validateGeneratedTarget(parent, name, write)
	if err != nil {
		return false, err
	}
	if err := repositoryRoot.verify(); err != nil {
		return false, err
	}
	if err := verifyBoundTarget(parent, name, targetIdentity); err != nil {
		return false, err
	}
	if err := unix.Renameat(int(parent.current().Fd()), temporaryName, int(parent.current().Fd()), name); err != nil {
		return false, fmt.Errorf("publish generated target %s atomically: %w", write.Path, err)
	}
	if err := verifyBoundTarget(parent, name, temporaryIdentity); err != nil {
		return true, err
	}
	if err := repositoryRoot.verify(); err != nil {
		return true, err
	}
	if err := syncDirectory(parent.current()); err != nil {
		return true, err
	}
	if err := repositoryRoot.verify(); err != nil {
		return true, err
	}
	return true, nil
}

func validateGeneratedTarget(parent *boundDirectory, name string, write plannedRepositoryWrite) (confinedIdentity, error) {
	var inspected unix.Stat_t
	if err := unix.Fstatat(int(parent.current().Fd()), name, &inspected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return confinedIdentity{}, fmt.Errorf("generated target %s changed after planning: %w", write.Path, err)
	}
	identity := identityFromStat(&inspected)
	if identity.kind != unix.S_IFREG {
		return confinedIdentity{}, fmt.Errorf("generated target %s changed after planning", write.Path)
	}
	if !sameConfinedIdentity(identity, write.CurrentIdentity) {
		return confinedIdentity{}, fmt.Errorf("generated target %s identity changed after planning", write.Path)
	}
	fileFD, err := unix.Openat(int(parent.current().Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return confinedIdentity{}, fmt.Errorf("open generated target %s: %w", write.Path, err)
	}
	file := os.NewFile(uintptr(fileFD), write.Path)
	defer file.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(fileFD, &opened); err != nil || !sameConfinedIdentity(identity, identityFromStat(&opened)) {
		return confinedIdentity{}, fmt.Errorf("generated target %s changed while opening", write.Path)
	}
	current, err := readBoundedReader(file, maxRepositoryStateFile)
	if err != nil || digestBytes(current) != write.CurrentDigest || !generatedPayloadValid(write.Path, current) {
		return confinedIdentity{}, fmt.Errorf("generated target %s changed after planning", write.Path)
	}
	if err := verifyBoundTarget(parent, name, identity); err != nil {
		return confinedIdentity{}, err
	}
	return identity, nil
}

func createRepositoryTarget(ctx context.Context, repositoryRoot *boundRepositoryRoot, parent *boundDirectory, name string, write plannedRepositoryWrite) (bool, error) {
	if err := repositoryRoot.verify(); err != nil {
		return false, err
	}
	var existing unix.Stat_t
	if err := unix.Fstatat(int(parent.current().Fd()), name, &existing, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return false, fmt.Errorf("target %s appeared after planning", write.Path)
	} else if !errors.Is(err, unix.ENOENT) {
		return false, err
	}
	temporaryName, temporary, identity, err := createTemporaryTarget(parent, write, writeAll)
	if err != nil {
		return false, err
	}
	defer removeTemporaryTarget(parent, temporaryName, identity)
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := repositoryRoot.verify(); err != nil {
		return false, err
	}
	if err := parent.verify(); err != nil {
		return false, err
	}
	if err := unix.Linkat(int(parent.current().Fd()), temporaryName, int(parent.current().Fd()), name, 0); err != nil {
		return false, fmt.Errorf("create target %s without overwrite: %w", write.Path, err)
	}
	if err := verifyBoundTarget(parent, name, identity); err != nil {
		return true, err
	}
	if err := repositoryRoot.verify(); err != nil {
		return true, err
	}
	if err := syncDirectory(parent.current()); err != nil {
		return true, err
	}
	if err := repositoryRoot.verify(); err != nil {
		return true, err
	}
	return true, nil
}

func createTemporaryTarget(parent *boundDirectory, write plannedRepositoryWrite, writer func(*os.File, []byte) error) (string, *os.File, confinedIdentity, error) {
	for attempt := 0; attempt < 32; attempt++ {
		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			return "", nil, confinedIdentity{}, err
		}
		name := ".loom-repostate-" + hex.EncodeToString(random)
		fileFD, err := unix.Openat(int(parent.current().Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(normalizedPortableMode(write.Mode).Perm()))
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, confinedIdentity{}, err
		}
		file := os.NewFile(uintptr(fileFD), name)
		if err := unix.Fchmod(fileFD, uint32(normalizedPortableMode(write.Mode).Perm())); err != nil {
			discardTemporaryTarget(parent, name, file)
			return "", nil, confinedIdentity{}, err
		}
		if err := writer(file, write.Content); err != nil {
			discardTemporaryTarget(parent, name, file)
			return "", nil, confinedIdentity{}, err
		}
		if err := file.Sync(); err != nil {
			discardTemporaryTarget(parent, name, file)
			return "", nil, confinedIdentity{}, err
		}
		var created unix.Stat_t
		if err := unix.Fstat(fileFD, &created); err != nil {
			discardTemporaryTarget(parent, name, file)
			return "", nil, confinedIdentity{}, err
		}
		return name, file, identityFromStat(&created), nil
	}
	return "", nil, confinedIdentity{}, errors.New("could not allocate a temporary repository state target")
}

func discardTemporaryTarget(parent *boundDirectory, name string, file *os.File) {
	var held unix.Stat_t
	statErr := unix.Fstat(int(file.Fd()), &held)
	_ = file.Close()
	if statErr == nil {
		removeTemporaryTarget(parent, name, identityFromStat(&held))
	}
}

func removeTemporaryTarget(parent *boundDirectory, name string, expected confinedIdentity) {
	var current unix.Stat_t
	if err := unix.Fstatat(int(parent.current().Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return
	}
	if sameConfinedIdentity(expected, identityFromStat(&current)) {
		_ = unix.Unlinkat(int(parent.current().Fd()), name, 0)
	}
}

func syncDirectory(directory *os.File) error {
	if err := unix.Fsync(int(directory.Fd())); err != nil {
		return fmt.Errorf("sync repository state directory: %w", err)
	}
	return nil
}

func verifyBoundTarget(parent *boundDirectory, name string, expected confinedIdentity) error {
	if err := parent.verify(); err != nil {
		return err
	}
	var current unix.Stat_t
	if err := unix.Fstatat(int(parent.current().Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameConfinedIdentity(expected, identityFromStat(&current)) {
		return errors.New("repository state target path changed during apply")
	}
	return parent.verify()
}

func writeAll(file *os.File, payload []byte) error {
	for len(payload) > 0 {
		written, err := file.Write(payload)
		if err != nil {
			return err
		}
		if written == 0 {
			return errors.New("repository state write made no progress")
		}
		payload = payload[written:]
	}
	return nil
}

func digestChangePlan(plan ChangePlan) (string, error) {
	payload := struct {
		SchemaVersion  string          `json:"schema_version"`
		Kind           ChangeKind      `json:"kind"`
		RepositoryRoot string          `json:"repository_root"`
		PackRoot       string          `json:"pack_root"`
		Git            GitPosture      `json:"git"`
		Files          []MigrationFile `json:"files"`
	}{plan.SchemaVersion, plan.Kind, plan.RepositoryRoot, plan.PackRoot, plan.Git, plan.Files}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return digestBytes(raw), nil
}

func countMigrationFiles(files []MigrationFile) MigrationCounts {
	counts := MigrationCounts{}
	for _, file := range files {
		switch file.Disposition {
		case MigrationPreserved:
			counts.Preserved++
		case MigrationRenamed:
			counts.Renamed++
		case MigrationSplit:
			counts.Split++
		case MigrationArchived:
			counts.Archived++
		}
		if file.Status == MigrationConflict {
			counts.Conflicting++
		}
		if file.Disposition == MigrationSkipped || file.Status == MigrationSkip {
			counts.Skipped++
		}
		if file.Status == MigrationPending {
			counts.Pending++
		}
	}
	return counts
}

func aggregateMigrationStatus(targets []MigrationTarget) MigrationStatus {
	if len(targets) == 0 {
		return MigrationSkip
	}
	allSkipped := true
	allAppliedOrSkipped := true
	for _, target := range targets {
		if target.Status == MigrationConflict {
			return MigrationConflict
		}
		if target.Status != MigrationSkip {
			allSkipped = false
		}
		if target.Status != MigrationSkip && target.Status != MigrationApplied {
			allAppliedOrSkipped = false
		}
	}
	if allSkipped {
		return MigrationSkip
	}
	if allAppliedOrSkipped {
		return MigrationApplied
	}
	for _, target := range targets {
		if target.Status == MigrationPending {
			return MigrationPending
		}
	}
	return MigrationNotRun
}

func markGenerated(path string, body []byte) []byte {
	body = ensureFinalNewline(body)
	prefix := "<!-- "
	suffix := " -->"
	if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
		prefix = "# "
		suffix = ""
	}
	marker := fmt.Sprintf("%s%s path=%s content_digest=%s%s\n", prefix, generatedMarkerKind, path, digestBytes(body), suffix)
	return append([]byte(marker), body...)
}

func generatedPayloadValid(path string, payload []byte) bool {
	lineEnd := bytes.IndexByte(payload, '\n')
	if lineEnd < 0 {
		return false
	}
	line := string(payload[:lineEnd])
	body := payload[lineEnd+1:]
	prefix := "# " + generatedMarkerKind + " path=" + path + " content_digest="
	suffix := ""
	if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
		prefix = "<!-- " + generatedMarkerKind + " path=" + path + " content_digest="
		suffix = " -->"
	}
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
		return false
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(line, prefix), suffix)
	return digest == digestBytes(body)
}

func digestBytes(payload []byte) string {
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func ensureFinalNewline(payload []byte) []byte {
	copyPayload := append([]byte(nil), payload...)
	if len(copyPayload) == 0 || copyPayload[len(copyPayload)-1] != '\n' {
		copyPayload = append(copyPayload, '\n')
	}
	return copyPayload
}

func normalizedPortableMode(mode fs.FileMode) fs.FileMode {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func portableModeString(mode fs.FileMode) string {
	return fmt.Sprintf("%04o", normalizedPortableMode(mode).Perm())
}
