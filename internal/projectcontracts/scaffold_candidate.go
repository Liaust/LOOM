package projectcontracts

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// scaffoldCandidateSources is only used by facet preflight. Existing packages
// stay at their real source roots: their validators retain normal discovery,
// containment, ancillary-file and hashing behavior. Only new generated packages
// live in the temporary candidate. This is not a runtime filesystem overlay.
type scaffoldCandidateSources struct {
	root          string
	candidateRoot string
	packages      map[string]bool
	published     map[string]bool
}

func projectFacetEntries(loaded LoadedProject, facet string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(filepath.Join(loaded.RootPath, facet))
	if loaded.candidateSources == nil {
		return entries, err
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	original, sourceErr := os.ReadDir(filepath.Join(loaded.candidateSources.root, facet))
	if sourceErr != nil && !os.IsNotExist(sourceErr) {
		return nil, sourceErr
	}
	byName := make(map[string]os.DirEntry, len(entries)+len(original))
	for _, entry := range original {
		byName[entry.Name()] = entry
	}
	for _, entry := range entries {
		key := filepath.ToSlash(filepath.Join(facet, entry.Name()))
		if _, exists := byName[entry.Name()]; exists && loaded.candidateSources.packages[key] && !loaded.candidateSources.published[key] {
			return nil, fmt.Errorf("candidate package conflicts with existing source path: %s", key)
		}
		if _, exists := byName[entry.Name()]; !exists {
			byName[entry.Name()] = entry
		}
	}
	out := make([]os.DirEntry, 0, len(byName))
	for _, entry := range byName {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

func projectFacetPackage(loaded LoadedProject, facet, name string) (LoadedProject, string) {
	relative := filepath.Join(facet, name)
	if loaded.candidateSources != nil && loaded.candidateSources.candidateRoot == "" {
		sources := *loaded.candidateSources
		sources.candidateRoot = loaded.RootPath
		loaded.candidateSources = &sources
	}
	if loaded.candidateSources != nil && !loaded.candidateSources.packages[filepath.ToSlash(relative)] {
		loaded.RootPath = loaded.candidateSources.root
	}
	return loaded, filepath.Join(loaded.RootPath, relative)
}

// Every observed path is metadata or an explicit directory fact. Package
// payloads are read only by the existing package analyzers above.
type scaffoldSourceState struct {
	info fs.FileInfo
	raw  []byte
}

type scaffoldCandidate struct {
	root         string
	source       string
	loaded       LoadedProject
	files        []scaffoldFile
	actions      []ScaffoldFileResult
	observations map[string]scaffoldSourceState
	packages     map[string]bool
	directories  []ScaffoldDirectoryResult
	report       ValidationReport
}

type scaffoldCandidateHooks struct {
	beforeValidation  func(string) error
	beforePublication func() error
	beforeWrite       func(string) error
}

func (candidate *scaffoldCandidate) observe(relative string, read bool) (scaffoldSourceState, error) {
	if state, ok := candidate.observations[relative]; ok {
		return state, nil
	}
	if relative != "." {
		if _, err := candidate.observe(filepath.ToSlash(filepath.Dir(relative)), false); err != nil {
			return scaffoldSourceState{}, err
		}
	}
	target := candidate.source
	var err error
	if relative != "." {
		target, err = safeMigrationPath(candidate.source, relative)
		if err != nil {
			return scaffoldSourceState{}, err
		}
	}
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		state := scaffoldSourceState{}
		candidate.observations[relative] = state
		return state, nil
	}
	if err != nil {
		return scaffoldSourceState{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return scaffoldSourceState{}, fmt.Errorf("candidate source path contains a symbolic link: %s", relative)
	}
	state := scaffoldSourceState{info: info}
	if read {
		if !info.Mode().IsRegular() {
			return state, fmt.Errorf("candidate metadata is not a regular file: %s", relative)
		}
		state.raw, err = os.ReadFile(target)
		if err != nil {
			return state, err
		}
	}
	candidate.observations[relative] = state
	return state, nil
}

func (candidate *scaffoldCandidate) copyMetadata(relative string) error {
	if strings.TrimSpace(relative) == "" {
		return nil
	}
	state, err := candidate.observe(relative, true)
	if err != nil {
		return err
	}
	if state.info == nil {
		return nil
	}
	return candidate.writeTemporary(scaffoldFile{RelativePath: relative, Content: state.raw, Mode: state.info.Mode().Perm()})
}

func (candidate *scaffoldCandidate) writeTemporary(file scaffoldFile) error {
	target, err := scaffoldTargetPath(candidate.root, file.RelativePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return os.WriteFile(target, file.Content, file.Mode.Perm())
}

func scaffoldPackagePath(relative string) string {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) < 3 {
		return ""
	}
	switch parts[0] {
	case "scripts", "workflows", "connectors", "modules", "schedules", "direct_events":
		return strings.Join(parts[:2], "/")
	}
	return ""
}

func prepareScaffoldCandidate(loaded LoadedProject, data scaffoldData, files []scaffoldFile, requested []string) (*scaffoldCandidate, error) {
	temporary, err := os.MkdirTemp("", "loom-scaffold-candidate-")
	if err != nil {
		return nil, err
	}
	candidate := &scaffoldCandidate{root: temporary, source: loaded.RootPath, observations: map[string]scaffoldSourceState{}, packages: map[string]bool{}}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(temporary)
		}
	}()
	// Copy only the exact singleton declarations consulted by the existing loader,
	// including alternate layouts so conflicts cannot disappear in the candidate.
	metadata := []string{CanonicalRootContractPath, LegacyRootContractPath, loaded.Contract.Policies.Sync, loaded.Contract.Policies.Backup, loaded.Contract.Policies.Workers, loaded.Contract.Policies.Credentials}
	for kind, definition := range singletonContractDefinitions {
		explicit := ""
		switch kind {
		case ProjectContractSync:
			explicit = loaded.Contract.Policies.Sync
		case ProjectContractBackup:
			explicit = loaded.Contract.Policies.Backup
		case ProjectContractWorkers:
			explicit = loaded.Contract.Policies.Workers
		case ProjectContractCredentials:
			explicit = loaded.Contract.Policies.Credentials
		}
		if containsString(data.Facets, definition.Facet) || explicit != "" {
			metadata = append(metadata, definition.Canonical, definition.Legacy)
		}
	}
	for _, relative := range metadata {
		if err := candidate.copyMetadata(relative); err != nil {
			return nil, err
		}
	}
	if loaded.ContractPath != "" {
		relative, err := filepath.Rel(loaded.RootPath, loaded.ContractPath)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(candidate.observations[filepath.ToSlash(relative)].raw, loaded.Raw) {
			return nil, fmt.Errorf("root contract changed during candidate preparation; replan before retry")
		}
	}
	for _, facet := range facetOrder {
		relative := facetFolders[facet]
		_, declared := loaded.Contract.Facets[facet]
		if !declared && !containsString(data.Facets, facet) {
			continue
		}
		if relative == "" {
			continue
		}
		state, err := candidate.observe(relative, false)
		if err != nil {
			return nil, err
		}
		planned := containsString(requested, facet)
		if state.info != nil && !state.info.IsDir() {
			if containsString(data.Facets, facet) {
				return nil, fmt.Errorf("scaffold directory path is not a directory: %s", relative)
			}
			if err := candidate.writeTemporary(scaffoldFile{RelativePath: relative, Mode: 0o600}); err != nil {
				return nil, err
			}
			continue
		}
		if state.info != nil || planned {
			if err := os.MkdirAll(filepath.Join(temporary, filepath.FromSlash(relative)), 0o700); err != nil {
				return nil, err
			}
		}
	}
	for _, directory := range scaffoldDirectories(requested) {
		state := candidate.observations[directory.RelativePath]
		action := "planned"
		if state.info != nil {
			action = "existing"
		}
		candidate.directories = append(candidate.directories, ScaffoldDirectoryResult{Path: directory.RelativePath, Action: action})
	}
	// Service contracts are declaration metadata, not package or project payload.
	serviceDir := ".loom/contracts/services"
	var entries []os.DirEntry
	if containsString(data.Facets, "services") {
		entries, err = os.ReadDir(filepath.Join(loaded.RootPath, serviceDir))
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		relative := serviceDir + "/" + entry.Name()
		if filepath.Ext(entry.Name()) != ".yaml" {
			if _, err := candidate.observe(relative, false); err != nil {
				return nil, err
			}
			if err := candidate.writeTemporary(scaffoldFile{RelativePath: relative, Mode: 0o600}); err != nil {
				return nil, err
			}
		} else if err := candidate.copyMetadata(relative); err != nil {
			return nil, err
		}
	}
	previous, err := scaffoldDataFromContract(loaded.Contract, enabledContractFacets(loaded.Contract.Facets))
	if err != nil {
		return nil, err
	}
	previousPolicies := []scaffoldFile{}
	if err := appendPolicyFiles(&previousPolicies, previous); err != nil {
		return nil, err
	}
	previousPolicies, err = resolveFacetAddContractPaths(loaded, loaded.Contract, previousPolicies)
	if err != nil {
		return nil, err
	}
	generatedBefore := map[string][]byte{}
	for _, file := range previousPolicies {
		generatedBefore[file.RelativePath] = file.Content
	}
	seen := map[string]bool{}
	for _, file := range files {
		if seen[file.RelativePath] {
			continue
		}
		seen[file.RelativePath] = true
		packagePath := scaffoldPackagePath(file.RelativePath)
		if packagePath != "" {
			state, err := candidate.observe(packagePath, false)
			if err != nil {
				return nil, err
			}
			if state.info != nil {
				if !state.info.IsDir() {
					return nil, fmt.Errorf("generated package conflicts with existing path: %s", packagePath)
				}
				// Existing packages, including partially authored examples and ancillary
				// files, are owned by their source. Never repair them by overwriting or by
				// synthesizing a partial copy that omits the rest of the package.
				candidate.actions = append(candidate.actions, ScaffoldFileResult{Path: file.RelativePath, Kind: file.Kind, Action: "skipped"})
				continue
			}
			candidate.packages[packagePath] = true
		}
		state, err := candidate.observe(file.RelativePath, true)
		if err != nil {
			return nil, err
		}
		action := "planned"
		if state.info != nil {
			action = "skipped"
			switch {
			case bytes.Equal(state.raw, file.Content):
			case file.Kind == "root_contract":
				action = "planned_update"
			case file.Kind == "backup_policy" && (bytes.Equal(state.raw, generatedBefore[file.RelativePath]) || generatedBackupPolicyMatches(state.raw, data)):
				action = "planned_update"
			case file.Kind == "root_readme" || file.Kind == "root_agents" || file.Kind == "project_agents":
				if replacement, ok := generatedGuidanceReplacement(file, state.raw, previous, data); ok {
					file = replacement
					action = "planned_update"
				}
			}
			if action == "skipped" {
				if err := candidate.writeTemporary(scaffoldFile{RelativePath: file.RelativePath, Content: state.raw, Mode: state.info.Mode().Perm()}); err != nil {
					return nil, err
				}
			} else {
				file.Mode = state.info.Mode().Perm()
			}
		}
		candidate.actions = append(candidate.actions, ScaffoldFileResult{Path: file.RelativePath, Kind: file.Kind, Action: action})
		if action == "skipped" {
			continue
		}
		if state.info == nil {
			file.Mode = scaffoldFileMode(file.Mode)
		}
		candidate.files = append(candidate.files, file)
		if err := candidate.writeTemporary(file); err != nil {
			return nil, err
		}
	}
	candidate.loaded, err = LoadProject(temporary)
	if err != nil {
		return nil, err
	}
	candidate.loaded.candidateSources = &scaffoldCandidateSources{root: loaded.RootPath, packages: candidate.packages, published: map[string]bool{}}
	// Record only explicitly declared path facts. Never walk notes/repos payloads.
	if err := candidate.copyDeclaredPathFacts(); err != nil {
		return nil, err
	}
	success = true
	return candidate, nil
}

// A previous failed facet addition may have published the root before its
// generated dependency. Recognize only exact default templates for subsets of
// the proposed Notes/Repos coverage; never infer ownership from a marker alone.
func generatedBackupPolicyMatches(raw []byte, data scaffoldData) bool {
	for _, notes := range []bool{false, true} {
		for _, repos := range []bool{false, true} {
			if (notes && !data.HasNotes) || (repos && !data.HasRepos) {
				continue
			}
			previous := data
			previous.HasNotes = notes
			previous.HasRepos = repos
			expected, err := renderScaffoldTemplate("backup", backupPolicyTemplate, previous)
			if err == nil && bytes.Equal(raw, []byte(expected)) {
				return true
			}
		}
	}
	return false
}

func generatedGuidanceReplacement(file scaffoldFile, raw []byte, previous, next scaffoldData) (scaffoldFile, bool) {
	template := ""
	switch file.Kind {
	case "root_readme":
		template = conciseRootReadmeTemplate
	case "root_agents":
		template = conciseRootAgentsTemplate
	case "project_agents":
		template = projectAgentsTemplate
	}
	for _, preset := range SupportedPresets() {
		previous.Preset = preset.Name
		expected, err := renderScaffoldTemplate(file.RelativePath, template, previous)
		if err == nil && bytes.Equal(raw, []byte(expected)) {
			next.Preset = preset.Name
			replacement, err := renderScaffoldTemplate(file.RelativePath, template, next)
			if err == nil {
				file.Content = []byte(replacement)
				return file, true
			}
		}
	}
	return file, false
}

func (candidate *scaffoldCandidate) copyDeclaredPathFacts() error {
	report := Validate(candidate.loaded)
	paths := []string{}
	for _, item := range report.Notes {
		paths = append(paths, item.ProjectPath)
	}
	for _, item := range report.Repos {
		paths = append(paths, item.ProjectPath)
	}
	for _, item := range report.WatchedRoots {
		paths = append(paths, item.RootRelativePath)
	}
	for _, member := range report.RepositoryMembers {
		path, err := normalizeRepositoryMemberPath(member.Path)
		if err != nil {
			continue
		}
		paths = append(paths, "repos/"+path)
		if member.StateRoot != "" {
			state, err := normalizeRepositoryStateRoot(member.StateRoot)
			if err == nil {
				paths = append(paths, "repos/"+path+"/"+state)
			}
		}
	}
	for _, relative := range paths {
		relative = filepath.ToSlash(filepath.Clean(relative))
		if relative == "." {
			continue
		}
		state, err := candidate.observe(relative, false)
		if err != nil {
			return err
		}
		if state.info == nil {
			continue
		}
		target, err := scaffoldTargetPath(candidate.root, relative)
		if err != nil {
			return err
		}
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if state.info.IsDir() {
			err = os.MkdirAll(target, 0o700)
		} else {
			err = os.MkdirAll(filepath.Dir(target), 0o700)
			if err == nil {
				err = os.WriteFile(target, nil, 0o600)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (candidate *scaffoldCandidate) validate() ValidationReport {
	report := Validate(candidate.loaded)
	report.GeneratedAt = time.Time{}
	return report
}

func (candidate *scaffoldCandidate) recheck() error {
	keys := make([]string, 0, len(candidate.observations))
	for key := range candidate.observations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, relative := range keys {
		expected := candidate.observations[relative]
		target := candidate.source
		var err error
		if relative != "." {
			target, err = safeMigrationPath(candidate.source, relative)
			if err != nil {
				return err
			}
		}
		actual, err := os.Lstat(target)
		if expected.info == nil && os.IsNotExist(err) {
			continue
		}
		if err != nil || expected.info == nil || !os.SameFile(expected.info, actual) || expected.info.Mode() != actual.Mode() || (actual.IsDir() && !expected.info.ModTime().Equal(actual.ModTime())) {
			return fmt.Errorf("source changed after candidate validation: %s; replan before retry", relative)
		}
		if expected.raw != nil {
			raw, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(raw, expected.raw) {
				return fmt.Errorf("source changed after candidate validation: %s; replan before retry", relative)
			}
		}
	}
	if !reflect.DeepEqual(candidate.report, candidate.validate()) {
		return fmt.Errorf("package or declaration discovery changed after candidate validation; replan before retry")
	}
	return nil
}

func (candidate *scaffoldCandidate) publish(result *AddProjectFacetsResult, hooks scaffoldCandidateHooks) error {
	if hooks.beforePublication != nil {
		if err := hooks.beforePublication(); err != nil {
			return err
		}
	}
	if err := candidate.recheck(); err != nil {
		return err
	}
	publishedPackages := map[string]bool{}
	for _, file := range candidate.files {
		packagePath := scaffoldPackagePath(file.RelativePath)
		path := file.RelativePath
		if packagePath != "" {
			if publishedPackages[packagePath] {
				continue
			}
			path = packagePath
		}
		if hooks.beforeWrite != nil {
			if err := hooks.beforeWrite(path); err != nil {
				candidate.failed(result, path)
				return fmt.Errorf("publish %s: %w", path, err)
			}
		}
		if err := candidate.recheck(); err != nil {
			candidate.failed(result, path)
			return err
		}
		if err := candidate.makeDirectories(filepath.ToSlash(filepath.Dir(path)), result); err != nil {
			candidate.failed(result, path)
			return err
		}
		var err error
		if packagePath != "" {
			err = candidate.publishPackage(packagePath)
		} else {
			err = candidate.publishFile(file)
		}
		if err != nil {
			candidate.failed(result, path)
			return fmt.Errorf("publish %s: %w", path, err)
		}
		if packagePath != "" {
			publishedPackages[packagePath] = true
			candidate.loaded.candidateSources.published[packagePath] = true
		}
		for i := range result.Files {
			entry := &result.Files[i]
			if entry.Path != path && (packagePath == "" || scaffoldPackagePath(entry.Path) != packagePath) {
				continue
			}
			if entry.Action == "planned_update" {
				entry.Action = "updated"
			} else if entry.Action == "planned" {
				entry.Action = "created"
			}
		}
		// Rebind only our own published identities, retaining the expectations for
		// all other files so subsequent writes cannot absorb concurrent edits.
		for _, published := range candidate.files {
			if published.RelativePath == path || (packagePath != "" && scaffoldPackagePath(published.RelativePath) == packagePath) {
				delete(candidate.observations, published.RelativePath)
				if _, err := candidate.observe(published.RelativePath, true); err != nil {
					return err
				}
			}
		}
		if packagePath != "" {
			candidate.refreshDirectory(packagePath)
			for key := range candidate.observations {
				if strings.HasPrefix(key, packagePath+"/") {
					if info, err := os.Lstat(filepath.Join(candidate.source, filepath.FromSlash(key))); err == nil && info.IsDir() {
						candidate.refreshDirectory(key)
					}
				}
			}
		}
		candidate.refreshDirectory(filepath.ToSlash(filepath.Dir(path)))
	}
	for _, directory := range candidate.directories {
		if err := candidate.makeDirectories(directory.Path, result); err != nil {
			return err
		}
	}
	return candidate.recheck()
}

func (candidate *scaffoldCandidate) failed(result *AddProjectFacetsResult, path string) {
	for i := range result.Files {
		entry := &result.Files[i]
		if entry.Path == path || scaffoldPackagePath(entry.Path) == path {
			entry.Action = "failed"
		}
	}
}

func (candidate *scaffoldCandidate) refreshDirectory(relative string) {
	info, err := os.Lstat(filepath.Join(candidate.source, filepath.FromSlash(relative)))
	if err == nil {
		candidate.observations[relative] = scaffoldSourceState{info: info}
	}
}

// Existing directory permissions are user data. Only create missing parents;
// unlike a fresh scaffold, facet addition must not chmod an existing tree.
func (candidate *scaffoldCandidate) makeDirectories(relative string, result *AddProjectFacetsResult) error {
	if relative == "." {
		if info, err := os.Lstat(candidate.source); err == nil {
			if !info.IsDir() {
				return fmt.Errorf("project root is not a directory")
			}
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Mkdir(candidate.source, 0o775); err != nil {
			return err
		}
		result.Directories = append(result.Directories, ScaffoldDirectoryResult{Path: ".", Action: "created"})
		candidate.refreshDirectory(".")
		return nil
	}
	if err := candidate.makeDirectories(filepath.ToSlash(filepath.Dir(relative)), result); err != nil {
		return err
	}
	target, err := safeMigrationPath(candidate.source, relative)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("not a directory: %s", relative)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := os.Mkdir(target, 0o775); err != nil {
		return err
	}
	found := false
	for i := range result.Directories {
		if result.Directories[i].Path == relative {
			result.Directories[i].Action = "created"
			found = true
		}
	}
	if !found {
		result.Directories = append(result.Directories, ScaffoldDirectoryResult{Path: relative, Action: "created"})
	}
	candidate.refreshDirectory(relative)
	candidate.refreshDirectory(filepath.ToSlash(filepath.Dir(relative)))
	return nil
}

func (candidate *scaffoldCandidate) publishFile(file scaffoldFile) error {
	target, err := safeMigrationPath(candidate.source, file.RelativePath)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".loom-scaffold-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(file.Content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(file.Mode.Perm()); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	expected := candidate.observations[file.RelativePath]
	if expected.info == nil {
		// Link publishes a complete new file without replacing a file that appeared
		// after the final check. The temporary name is removed by the defer.
		return os.Link(temporary.Name(), target)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if !os.SameFile(expected.info, info) || info.Mode() != expected.info.Mode() || !bytes.Equal(raw, expected.raw) {
		return fmt.Errorf("source changed before replacement: %s", file.RelativePath)
	}
	return os.Rename(temporary.Name(), target)
}

// Newly generated packages publish as complete directories, so a later failure
// cannot leave a half-written example that a retry would mistake for user data.
func (candidate *scaffoldCandidate) publishPackage(relative string) error {
	target, err := safeMigrationPath(candidate.source, relative)
	if err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(target), ".loom-scaffold-package-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for _, file := range candidate.files {
		if scaffoldPackagePath(file.RelativePath) != relative {
			continue
		}
		suffix := strings.TrimPrefix(file.RelativePath, relative+"/")
		path := filepath.Join(staging, filepath.FromSlash(suffix))
		if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
			return err
		}
		if err := os.WriteFile(path, file.Content, file.Mode.Perm()); err != nil {
			return err
		}
	}
	if err := os.Chmod(staging, 0o775); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("package appeared before publication: %s", relative)
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(staging, target)
}

// First resolve the logical reference inside the workflow's real project root.
// Then select its one declared package source, never arbitrary alternate paths.
func candidateWorkflowScriptPath(loaded LoadedProject, relative, original string) (string, bool) {
	if loaded.candidateSources == nil {
		return original, true
	}
	root := loaded.candidateSources.root
	packagePath := scaffoldPackagePath(relative)
	if loaded.candidateSources.packages[packagePath] {
		root = loaded.candidateSources.candidateRoot
	}
	if root == "" {
		return "", false
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	escapes, err := existingPathEscapesRoot(root, target)
	if err != nil || escapes {
		return "", false
	}
	return target, true
}
