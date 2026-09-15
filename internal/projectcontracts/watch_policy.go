package projectcontracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/ids"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

const (
	projectWatchedRootStatusDisabled          = "disabled"
	projectWatchedRootStatusPendingAgentApply = "pending_agent_apply"
	defaultProjectSafeRootKey                 = "project"
	maxProjectWatchedRootKeyLength            = 63
)

var (
	defaultNotesIncludes     = []string{"**/*"}
	defaultNotesTextIncludes = []string{"**/*.md", "**/*.markdown", "**/*.mdown", "**/*.txt", "**/*.text", "**/*.rst", "**/*.adoc"}
	defaultNotesExcludes     = []string{}
	defaultRepoIncludes      = []string{"**/*"}
	defaultRepoExcludes      = []string{}
)

func (value *PolicyModeValue) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		*value = ""
		return nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!bool":
			var boolValue bool
			if err := node.Decode(&boolValue); err != nil {
				return err
			}
			if boolValue {
				*value = "true"
			} else {
				*value = "false"
			}
			return nil
		default:
			*value = PolicyModeValue(strings.TrimSpace(node.Value))
			return nil
		}
	default:
		return fmt.Errorf("mode value must be a string or boolean")
	}
}

func validateNotesFacet(loaded LoadedProject, contract ProjectContract, add func(Diagnostic)) []NotesFacetItem {
	if !contract.Facets["notes"] {
		return nil
	}
	resolution, ok := resolveSingletonForValidation(loaded, ProjectContractNotes, "", "facets.notes", add)
	if !ok {
		return nil
	}
	contractPath := resolution.Path
	if !resolution.Present {
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "notes.contract_missing",
			Message:    "notes facet is enabled but no notes contract was found",
			File:       contractPath,
			Field:      "facets.notes",
			Suggestion: "create " + singletonContractDefinitions[ProjectContractNotes].Canonical + " or disable the notes facet",
		})
		return nil
	}
	notes, raw, err := LoadNotesContract(contractPath)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "notes.contract_invalid", Message: "notes contract is invalid: " + err.Error(), File: contractPath, Field: "notes"})
		return nil
	}
	return notesFacetItems(notes, raw, contractPath, func(relative string) bool {
		return pathExists(filepath.Join(loaded.RootPath, filepath.FromSlash(relative)))
	}, add)
}

// notesFacetItems is shared by file validation and captured-source compilation.
// A nil presence callback performs no filesystem reads.
func notesFacetItems(notes NotesContract, raw []byte, contractPath string, exists func(string) bool, add func(Diagnostic)) []NotesFacetItem {
	normalized := normalizeNotesContract(notes)
	validateNotesContractBasics(normalized, contractPath, add)
	if !validProjectFacetStatus(normalized.Notes.Status) {
		add(Diagnostic{Severity: SeverityError, Code: "notes.status_invalid", Message: "notes.status is not supported", File: contractPath, Field: "notes.status", Suggestion: "use draft, active, paused, or disabled"})
	}
	projectPath, err := facetProjectPath("notes", normalized.Notes.Path)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "notes.path_unsafe", Message: "notes.path is unsafe: " + normalized.Notes.Path, File: contractPath, Field: "notes.path", Suggestion: "use a path relative to notes/"})
		return nil
	}
	if err := watchedroots.ValidatePatterns(normalized.Notes.Include); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "notes.include_invalid", Message: "notes include pattern is invalid: " + err.Error(), File: contractPath, Field: "notes.include"})
	}
	if err := watchedroots.ValidatePatterns(normalized.Notes.Exclude); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "notes.exclude_invalid", Message: "notes exclude pattern is invalid: " + err.Error(), File: contractPath, Field: "notes.exclude"})
	}
	if exists != nil && !exists(projectPath) {
		add(Diagnostic{Severity: SeverityWarning, Code: "notes.path_missing", Message: "notes path does not exist: " + projectPath, File: contractPath, Field: "notes.path"})
	}
	item := NotesFacetItem{
		RootKey:          normalized.Notes.RootKey,
		Path:             normalized.Notes.Path,
		ProjectPath:      projectPath,
		ContractPath:     filepath.ToSlash(contractPath),
		ContractHash:     hashBytesURI(raw),
		Status:           normalized.Notes.Status,
		Sync:             boolValue(normalized.Notes.Sync, true),
		Index:            boolValue(normalized.Notes.Index, true),
		Backup:           boolValue(normalized.Notes.Backup, false),
		Include:          append([]string{}, normalized.Notes.Include...),
		Exclude:          append([]string{}, normalized.Notes.Exclude...),
		ActivationStatus: projectWatchedRootStatusPendingAgentApply,
		Metadata:         normalized.Metadata,
	}
	if item.Status == "disabled" {
		item.ActivationStatus = projectWatchedRootStatusDisabled
	}
	items := []NotesFacetItem{item}
	seenKeys := map[string]bool{item.RootKey: true}
	paths := []string{item.ProjectPath}
	for index, material := range normalized.Material {
		field := fmt.Sprintf("material[%d]", index)
		category := strings.TrimSpace(material.Category)
		key := strings.TrimSpace(material.Key)
		materialPath, pathErr := normalizeProjectRootRelativePath(material.Path)
		validCategory := category == "notes" || category == "docs" || category == "research"
		if !validCategory || key == "" || key != filesystemconnector.NormalizeRootKey(key) || seenKeys[key] || pathErr != nil || materialPath == "." || !(materialPath == category || strings.HasPrefix(materialPath, category+"/")) {
			add(Diagnostic{Severity: SeverityError, Code: "notes.material_invalid", Message: "material needs a unique root key and a declared notes/docs/research path inside the project", File: contractPath, Field: field})
			continue
		}
		seenKeys[key] = true
		overlap := false
		for _, existing := range paths {
			if materialPath == existing || strings.HasPrefix(materialPath, existing+"/") || strings.HasPrefix(existing, materialPath+"/") {
				overlap = true
			}
		}
		if overlap {
			add(Diagnostic{Severity: SeverityError, Code: "notes.material_overlap", Message: "material roots must not overlap existing Notes or another declaration", File: contractPath, Field: field})
			continue
		}
		paths = append(paths, materialPath)
		include := normalizePolicyPatterns(firstNonEmptyStrings(material.Include, defaultNotesIncludes))
		exclude := normalizePolicyPatterns(material.Exclude)
		if err := watchedroots.ValidatePatterns(include); err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "notes.material_include_invalid", Message: err.Error(), File: contractPath, Field: field})
			continue
		}
		if err := watchedroots.ValidatePatterns(exclude); err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "notes.material_exclude_invalid", Message: err.Error(), File: contractPath, Field: field})
			continue
		}
		status, activation := "disabled", projectWatchedRootStatusDisabled
		if material.Enabled {
			status, activation = "active", projectWatchedRootStatusPendingAgentApply
		}
		items = append(items, NotesFacetItem{RootKey: key, Path: material.Path, ProjectPath: materialPath,
			ContractPath: filepath.ToSlash(contractPath), ContractHash: hashBytesURI(raw),
			Status: status, Sync: material.Enabled, Index: material.Enabled, Include: include, Exclude: exclude,
			ActivationStatus: activation, MaterialCategory: category})
	}
	return items
}

func validateReposFacet(loaded LoadedProject, contract ProjectContract, add func(Diagnostic)) ([]RepoFacetItem, []RepoMemberSpec, *RepositorySourceSnapshot) {
	if !contract.Facets["repos"] {
		return nil, nil, nil
	}
	reposRoot := filepath.Join(loaded.RootPath, "repos")
	resolution, ok := resolveSingletonForValidation(loaded, ProjectContractRepos, "", "facets.repos", add)
	if !ok {
		return nil, nil, nil
	}
	contractPath := resolution.Path
	if !resolution.Present {
		if pathExists(reposRoot) {
			add(Diagnostic{
				Severity:   SeverityWarning,
				Code:       "repos.contract_missing",
				Message:    "repos facet is enabled but no repos contract was found",
				File:       contractPath,
				Field:      "facets.repos",
				Suggestion: "create " + singletonContractDefinitions[ProjectContractRepos].Canonical + " or disable the repos facet",
			})
		}
		return nil, nil, nil
	}
	repos, raw, err := LoadReposContract(contractPath)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "repos.contract_invalid", Message: "repos contract is invalid: " + err.Error(), File: contractPath, Field: "repos"})
		return nil, nil, nil
	}
	source := &RepositorySourceSnapshot{
		ContractPath:          filepath.ToSlash(contractPath),
		ContractHash:          hashBytesURI(raw),
		ContractSchemaVersion: strings.TrimSpace(repos.SchemaVersion),
	}
	items := repoFacetItems(repos, raw, contractPath, func(relative string) bool {
		return pathExists(filepath.Join(loaded.RootPath, filepath.FromSlash(relative)))
	}, add)
	members := validateRepoMembers(loaded, normalizeReposContract(repos), contractPath, add)
	return items, members, source
}

func repoFacetItems(repos ReposContract, raw []byte, contractPath string, exists func(string) bool, add func(Diagnostic)) []RepoFacetItem {
	normalized := normalizeReposContract(repos)
	validateReposContractBasics(normalized, contractPath, add)
	if !validProjectFacetStatus(normalized.Repos.Status) {
		add(Diagnostic{Severity: SeverityError, Code: "repos.status_invalid", Message: "repos.status is not supported", File: contractPath, Field: "repos.status", Suggestion: "use draft, active, paused, or disabled"})
	}
	items := []RepoFacetItem{}
	for index, root := range repoWatchRoots(normalized) {
		fieldPrefix := "repos.roots"
		if normalized.SchemaVersion == ReposSchemaV04 {
			fieldPrefix = "repos.watch_roots"
		}
		projectPath, err := facetProjectPath("repos", root.Path)
		if err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "repos.path_unsafe", Message: "repo root path is unsafe: " + root.Path, File: contractPath, Field: fmt.Sprintf("%s[%d].path", fieldPrefix, index), Suggestion: "use a path relative to repos/"})
			continue
		}
		if err := watchedroots.ValidatePatterns(root.Include); err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "repos.include_invalid", Message: "repo include pattern is invalid: " + err.Error(), File: contractPath, Field: fmt.Sprintf("%s[%d].include", fieldPrefix, index)})
		}
		if err := watchedroots.ValidatePatterns(root.Exclude); err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "repos.exclude_invalid", Message: "repo exclude pattern is invalid: " + err.Error(), File: contractPath, Field: fmt.Sprintf("%s[%d].exclude", fieldPrefix, index)})
		}
		if exists != nil && !exists(projectPath) {
			add(Diagnostic{Severity: SeverityWarning, Code: "repos.path_missing", Message: "repo root path does not exist: " + projectPath, File: contractPath, Field: fmt.Sprintf("%s[%d].path", fieldPrefix, index)})
		}
		status := normalized.Repos.Status
		activation := projectWatchedRootStatusPendingAgentApply
		if status == "disabled" {
			activation = projectWatchedRootStatusDisabled
		}
		items = append(items, RepoFacetItem{
			Key:              root.Key,
			Path:             root.Path,
			ProjectPath:      projectPath,
			DisplayName:      root.DisplayName,
			ContractPath:     filepath.ToSlash(contractPath),
			ContractHash:     hashBytesURI(raw),
			Status:           status,
			Sync:             boolValue(root.Sync, false),
			Index:            boolValue(root.Index, false),
			Backup:           boolValue(root.Backup, false),
			Include:          append([]string{}, root.Include...),
			Exclude:          append([]string{}, root.Exclude...),
			ActivationStatus: activation,
			Metadata:         normalized.Metadata,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Key < items[j].Key
	})
	return items
}

func validateProjectWatchPolicies(loaded LoadedProject, contract ProjectContract, notes []NotesFacetItem, repos []RepoFacetItem, add func(Diagnostic)) []ProjectWatchedRootItem {
	if contract.Project.Slug == "" || contract.Project.OwnerNode == "" {
		return nil
	}
	builder := newWatchPolicyBuilder(loaded, contract, add)
	for _, item := range notes {
		builder.addNotes(item)
	}
	for _, item := range repos {
		builder.addRepo(item)
	}
	if path, ok := projectSyncPolicyPath(loaded, contract); ok {
		builder.addSyncPolicy(path)
	}
	if path, ok := projectBackupPolicyPath(loaded, contract); ok {
		builder.addBackupPolicy(path)
	}
	return builder.items()
}

func LoadNotesContract(path string) (NotesContract, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return NotesContract{}, nil, err
	}
	var contract NotesContract
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&contract); err != nil {
		return NotesContract{}, raw, err
	}
	return contract, raw, nil
}

func LoadReposContract(path string) (ReposContract, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ReposContract{}, nil, err
	}
	var contract ReposContract
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&contract); err != nil {
		return ReposContract{}, raw, err
	}
	return contract, raw, nil
}

func LoadSyncPolicyContract(path string) (ProjectSyncPolicyContract, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ProjectSyncPolicyContract{}, nil, err
	}
	var contract ProjectSyncPolicyContract
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&contract); err != nil {
		return ProjectSyncPolicyContract{}, raw, err
	}
	return contract, raw, nil
}

func LoadBackupPolicyContract(path string) (ProjectBackupPolicyContract, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ProjectBackupPolicyContract{}, nil, err
	}
	return parseBackupPolicyContract(raw)
}

func parseBackupPolicyContract(raw []byte) (ProjectBackupPolicyContract, []byte, error) {
	var contract ProjectBackupPolicyContract
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&contract); err != nil {
		return ProjectBackupPolicyContract{}, raw, err
	}
	return contract, raw, nil
}

func ProjectWatchedRootKey(projectSlug, localRootKey string) string {
	projectSlug = strings.TrimSpace(projectSlug)
	localRootKey = strings.TrimSpace(localRootKey)
	base := strings.ReplaceAll(projectSlug, "-", "_") + "__" + localRootKey
	if base == "__" {
		base = "project_root"
	}
	base = filesystemconnector.NormalizeRootKey(base)
	if base == "" {
		base = "project_root"
	}
	if len(base) <= maxProjectWatchedRootKeyLength {
		return base
	}
	sum := sha256.Sum256([]byte(projectSlug + "\x00" + localRootKey))
	suffix := "_" + hex.EncodeToString(sum[:])[:12]
	return strings.TrimRight(base[:maxProjectWatchedRootKeyLength-len(suffix)], "_-") + suffix
}

type watchPolicyBuilder struct {
	sourceOnly bool
	loaded     LoadedProject
	contract   ProjectContract
	add        func(Diagnostic)
	roots      map[string]*watchRootAccumulator
}

type watchRootAccumulator struct {
	key                 string
	safeRoot            string
	path                string
	displayName         string
	sourceKinds         []string
	include             []string
	exclude             []string
	syncMode            string
	indexMode           string
	deleteMode          string
	backupMode          string
	projectRef          string
	scopeRef            string
	maxSyncFileBytes    int64
	maxIndexTextBytes   int64
	logicalNameStrategy string
	maxBackupFileBytes  int64
	maxBackupBatchBytes int64
	maxPendingItems     int
	maxPendingBytes     int64
	includeDeletions    *bool
	backupOnLimit       string
	metadata            map[string]any
}

func newWatchPolicyBuilder(loaded LoadedProject, contract ProjectContract, add func(Diagnostic)) *watchPolicyBuilder {
	return &watchPolicyBuilder{loaded: loaded, contract: contract, add: add, roots: map[string]*watchRootAccumulator{}}
}

func (builder *watchPolicyBuilder) addNotes(item NotesFacetItem) {
	if item.Status == "disabled" {
		return
	}
	root := watchRootAccumulator{
		key:                 item.RootKey,
		safeRoot:            defaultProjectSafeRootKey,
		path:                item.ProjectPath,
		displayName:         "Project Notes",
		sourceKinds:         []string{"notes_contract"},
		include:             item.Include,
		exclude:             item.Exclude,
		syncMode:            modeIf(item.Sync, watchedroots.SyncModeSelectedFiles, watchedroots.SyncModeNone),
		indexMode:           modeIf(item.Index, watchedroots.IndexModeMarkdownText, watchedroots.IndexModeNone),
		deleteMode:          watchedroots.DeleteModeTombstone,
		backupMode:          modeIf(item.Backup, watchedroots.BackupModeIncrementalRaw, watchedroots.BackupModeNone),
		projectRef:          builder.contract.Project.Slug,
		logicalNameStrategy: watchedroots.SyncLogicalNameRelativePath,
		metadata:            map[string]any{"project_slug": builder.contract.Project.Slug, "facet": "notes"},
	}
	if !item.Sync {
		root.deleteMode = watchedroots.DeleteModeLocalStateOnly
	}
	if item.MaterialCategory != "" {
		root.displayName = "Project " + item.MaterialCategory
		root.sourceKinds = []string{"project_material"}
		root.metadata["knowledge_source"] = map[string]any{
			"root_kind": "project_material", "category": "projects", "declaration": item.MaterialCategory,
			"enabled": item.Sync && item.Index, "root_relative_path": item.ProjectPath,
			"include": item.Include, "exclude": item.Exclude,
		}
	}
	builder.merge(root, item.ContractPath, "notes")
}

func (builder *watchPolicyBuilder) addRepo(item RepoFacetItem) {
	if item.Status == "disabled" {
		return
	}
	root := watchRootAccumulator{
		key:                 item.Key,
		safeRoot:            defaultProjectSafeRootKey,
		path:                item.ProjectPath,
		displayName:         firstNonEmptyString(item.DisplayName, "Project Repositories"),
		sourceKinds:         []string{"repos_contract"},
		include:             item.Include,
		exclude:             item.Exclude,
		syncMode:            modeIf(item.Sync, watchedroots.SyncModeSelectedFiles, watchedroots.SyncModeNone),
		indexMode:           modeIf(item.Index, watchedroots.IndexModeMarkdownText, watchedroots.IndexModeNone),
		deleteMode:          watchedroots.DeleteModeLocalStateOnly,
		backupMode:          modeIf(item.Backup, watchedroots.BackupModeIncrementalRaw, watchedroots.BackupModeNone),
		projectRef:          builder.contract.Project.Slug,
		logicalNameStrategy: watchedroots.SyncLogicalNameRelativePath,
		metadata:            map[string]any{"project_slug": builder.contract.Project.Slug, "facet": "repos"},
	}
	if item.Sync {
		root.deleteMode = watchedroots.DeleteModeTombstone
	}
	builder.merge(root, item.ContractPath, "repos")
}

func (builder *watchPolicyBuilder) addSyncPolicy(path string) {
	policy, _, err := LoadSyncPolicyContract(path)
	if err != nil {
		builder.add(Diagnostic{Severity: SeverityError, Code: "sync_policy.contract_invalid", Message: "sync policy is invalid: " + err.Error(), File: path, Field: "sync"})
		return
	}
	normalized := normalizeSyncPolicy(policy)
	if normalized.Kind != SyncPolicyKind {
		builder.add(Diagnostic{Severity: SeverityError, Code: "sync_policy.kind_invalid", Message: "sync policy kind must be " + SyncPolicyKind, File: path, Field: "kind", Suggestion: "set kind: loom.project_sync_policy"})
	}
	if normalized.SchemaVersion != SyncPolicySchemaV03 {
		builder.add(Diagnostic{Severity: SeverityError, Code: "sync_policy.schema_version_invalid", Message: "sync policy schema_version must be " + SyncPolicySchemaV03, File: path, Field: "schema_version", Suggestion: "set schema_version: sync.policy.v0.3"})
	}
	if !boolValue(normalized.Sync.Enabled, true) {
		return
	}
	for index, rootSpec := range normalized.Sync.Roots {
		field := fmt.Sprintf("sync.roots[%d]", index)
		projectPath, err := normalizeProjectRootRelativePath(rootSpec.Path)
		if err != nil {
			builder.add(Diagnostic{Severity: SeverityError, Code: "sync_policy.path_unsafe", Message: "sync policy root path is unsafe: " + rootSpec.Path, File: path, Field: field + ".path"})
			continue
		}
		if err := watchedroots.ValidatePatterns(rootSpec.Include); err != nil {
			builder.add(Diagnostic{Severity: SeverityError, Code: "sync_policy.include_invalid", Message: "sync policy include pattern is invalid: " + err.Error(), File: path, Field: field + ".include"})
		}
		if err := watchedroots.ValidatePatterns(rootSpec.Exclude); err != nil {
			builder.add(Diagnostic{Severity: SeverityError, Code: "sync_policy.exclude_invalid", Message: "sync policy exclude pattern is invalid: " + err.Error(), File: path, Field: field + ".exclude"})
		}
		mode, include := normalizeSyncMode(rootSpec.Mode, rootSpec.Include)
		indexMode := normalizeIndexMode(rootSpec.Index, mode)
		deleteMode := firstNonEmptyString(rootSpec.Delete, watchedroots.DeleteModeTombstone)
		if mode == watchedroots.SyncModeNone {
			deleteMode = firstNonEmptyString(rootSpec.Delete, watchedroots.DeleteModeLocalStateOnly)
		}
		key := rootSpec.Key
		if key == "" {
			key = deriveLocalRootKey(projectPath)
		}
		root := watchRootAccumulator{
			key:                 key,
			safeRoot:            firstNonEmptyString(rootSpec.SafeRoot, defaultProjectSafeRootKey),
			path:                projectPath,
			displayName:         rootSpec.DisplayName,
			sourceKinds:         []string{"sync_policy"},
			include:             include,
			exclude:             rootSpec.Exclude,
			syncMode:            mode,
			indexMode:           indexMode,
			deleteMode:          deleteMode,
			backupMode:          watchedroots.BackupModeNone,
			projectRef:          firstNonEmptyString(rootSpec.ProjectRef, builder.contract.Project.Slug),
			scopeRef:            rootSpec.ScopeRef,
			maxSyncFileBytes:    rootSpec.MaxFileBytes,
			maxIndexTextBytes:   rootSpec.MaxTextBytes,
			logicalNameStrategy: firstNonEmptyString(rootSpec.LogicalNameStrategy, watchedroots.SyncLogicalNameRelativePath),
			metadata:            map[string]any{"project_slug": builder.contract.Project.Slug, "policy": "sync"},
		}
		if root.scopeRef != "" && rootSpec.ProjectRef == "" {
			root.projectRef = ""
		}
		if !pathExists(filepath.Join(builder.loaded.RootPath, filepath.FromSlash(projectPath))) {
			builder.add(Diagnostic{Severity: SeverityWarning, Code: "sync_policy.path_missing", Message: "sync policy root path does not exist: " + projectPath, File: path, Field: field + ".path"})
		}
		builder.merge(root, path, field)
	}
}

func (builder *watchPolicyBuilder) addBackupPolicy(path string) {
	policy, _, err := LoadBackupPolicyContract(path)
	if err != nil {
		builder.add(Diagnostic{Severity: SeverityError, Code: "backup_policy.contract_invalid", Message: "backup policy is invalid: " + err.Error(), File: path, Field: "backup"})
		return
	}
	builder.addBackupPolicySource(policy, path)
}

func (builder *watchPolicyBuilder) addBackupPolicySource(policy ProjectBackupPolicyContract, path string) {
	normalized := normalizeBackupPolicy(policy)
	if normalized.Kind != BackupPolicyKind {
		builder.add(Diagnostic{Severity: SeverityError, Code: "backup_policy.kind_invalid", Message: "backup policy kind must be " + BackupPolicyKind, File: path, Field: "kind", Suggestion: "set kind: loom.project_backup_policy"})
	}
	if normalized.SchemaVersion != BackupPolicySchemaV03 {
		builder.add(Diagnostic{Severity: SeverityError, Code: "backup_policy.schema_version_invalid", Message: "backup policy schema_version must be " + BackupPolicySchemaV03, File: path, Field: "schema_version", Suggestion: "set schema_version: backup.policy.v0.3"})
	}
	if !boolValue(normalized.Backup.Enabled, true) {
		return
	}
	for index, rootSpec := range normalized.Backup.Roots {
		field := fmt.Sprintf("backup.roots[%d]", index)
		projectPath, err := normalizeProjectRootRelativePath(rootSpec.Path)
		if err != nil {
			builder.add(Diagnostic{Severity: SeverityError, Code: "backup_policy.path_unsafe", Message: "backup policy root path is unsafe: " + rootSpec.Path, File: path, Field: field + ".path"})
			continue
		}
		if err := watchedroots.ValidatePatterns(rootSpec.Include); err != nil {
			builder.add(Diagnostic{Severity: SeverityError, Code: "backup_policy.include_invalid", Message: "backup policy include pattern is invalid: " + err.Error(), File: path, Field: field + ".include"})
		}
		if err := watchedroots.ValidatePatterns(rootSpec.Exclude); err != nil {
			builder.add(Diagnostic{Severity: SeverityError, Code: "backup_policy.exclude_invalid", Message: "backup policy exclude pattern is invalid: " + err.Error(), File: path, Field: field + ".exclude"})
		}
		key := rootSpec.Key
		if key == "" {
			key = deriveLocalRootKey(projectPath)
		}
		root := watchRootAccumulator{
			key:                 key,
			safeRoot:            firstNonEmptyString(rootSpec.SafeRoot, defaultProjectSafeRootKey),
			path:                projectPath,
			displayName:         rootSpec.DisplayName,
			sourceKinds:         []string{"backup_policy"},
			include:             rootSpec.Include,
			exclude:             rootSpec.Exclude,
			syncMode:            watchedroots.SyncModeNone,
			indexMode:           watchedroots.IndexModeNone,
			deleteMode:          watchedroots.DeleteModeLocalStateOnly,
			backupMode:          firstNonEmptyString(rootSpec.Mode, watchedroots.BackupModeIncrementalRaw),
			maxBackupFileBytes:  rootSpec.MaxFileBytes,
			maxBackupBatchBytes: rootSpec.MaxBatchBytes,
			maxPendingItems:     rootSpec.MaxPendingItems,
			maxPendingBytes:     rootSpec.MaxPendingBytes,
			includeDeletions:    rootSpec.IncludeDeletionMarkers,
			backupOnLimit:       rootSpec.OnLimit,
			metadata:            map[string]any{"project_slug": builder.contract.Project.Slug, "policy": "backup"},
		}
		if !builder.sourceOnly && !pathExists(filepath.Join(builder.loaded.RootPath, filepath.FromSlash(projectPath))) {
			builder.add(Diagnostic{Severity: SeverityWarning, Code: "backup_policy.path_missing", Message: "backup policy root path does not exist: " + projectPath, File: path, Field: field + ".path"})
		}
		builder.merge(root, path, field)
	}
}

func (builder *watchPolicyBuilder) merge(root watchRootAccumulator, file, field string) {
	root.key = filesystemconnector.NormalizeRootKey(root.key)
	root.safeRoot = filesystemconnector.NormalizeRootKey(root.safeRoot)
	if root.safeRoot == "" {
		root.safeRoot = defaultProjectSafeRootKey
	}
	if root.key == "" {
		builder.add(Diagnostic{Severity: SeverityError, Code: "watched_root.key_required", Message: "watched-root root key is required", File: file, Field: field + ".key"})
		return
	}
	root.path = firstNonEmptyString(root.path, ".")
	existing := builder.roots[root.key]
	if existing == nil {
		builder.roots[root.key] = &root
		return
	}
	if existing.safeRoot != root.safeRoot || existing.path != root.path {
		builder.add(Diagnostic{
			Severity: SeverityError,
			Code:     "watched_root.duplicate_conflict",
			Message:  "watched-root key " + root.key + " is declared with incompatible safe root or path",
			File:     file,
			Field:    field,
		})
		return
	}
	if !compatiblePatternSet(existing.include, root.include) || !compatiblePatternSet(existing.exclude, root.exclude) {
		builder.add(Diagnostic{
			Severity: SeverityError,
			Code:     "watched_root.pattern_conflict",
			Message:  "watched-root key " + root.key + " has incompatible include or exclude declarations",
			File:     file,
			Field:    field,
		})
		return
	}
	if len(existing.include) == 0 {
		existing.include = append([]string{}, root.include...)
	}
	if len(existing.exclude) == 0 {
		existing.exclude = append([]string{}, root.exclude...)
	}
	existing.sourceKinds = appendUnique(existing.sourceKinds, root.sourceKinds...)
	if existing.displayName == "" {
		existing.displayName = root.displayName
	}
	mergeMode(&existing.syncMode, root.syncMode, watchedroots.SyncModeNone, builder.add, file, field, "sync_mode")
	mergeMode(&existing.indexMode, root.indexMode, watchedroots.IndexModeNone, builder.add, file, field, "index_mode")
	mergeMode(&existing.backupMode, root.backupMode, watchedroots.BackupModeNone, builder.add, file, field, "backup_mode")
	mergeDeleteMode(&existing.deleteMode, root.deleteMode)
	if root.projectRef != "" {
		existing.projectRef = root.projectRef
	}
	if root.scopeRef != "" {
		existing.scopeRef = root.scopeRef
		existing.projectRef = ""
	}
	if existing.maxSyncFileBytes == 0 {
		existing.maxSyncFileBytes = root.maxSyncFileBytes
	}
	if existing.maxIndexTextBytes == 0 {
		existing.maxIndexTextBytes = root.maxIndexTextBytes
	}
	if existing.logicalNameStrategy == "" {
		existing.logicalNameStrategy = root.logicalNameStrategy
	}
	if existing.maxBackupFileBytes == 0 {
		existing.maxBackupFileBytes = root.maxBackupFileBytes
	}
	if existing.maxBackupBatchBytes == 0 {
		existing.maxBackupBatchBytes = root.maxBackupBatchBytes
	}
	if existing.maxPendingItems == 0 {
		existing.maxPendingItems = root.maxPendingItems
	}
	if existing.maxPendingBytes == 0 {
		existing.maxPendingBytes = root.maxPendingBytes
	}
	if existing.includeDeletions == nil {
		existing.includeDeletions = root.includeDeletions
	}
	if existing.backupOnLimit == "" {
		existing.backupOnLimit = root.backupOnLimit
	}
}

func (builder *watchPolicyBuilder) items() []ProjectWatchedRootItem {
	items := []ProjectWatchedRootItem{}
	for _, root := range builder.roots {
		config := watchedroots.RootConfig{
			RootKey:          ProjectWatchedRootKey(builder.contract.Project.Slug, root.key),
			DisplayName:      firstNonEmptyString(root.displayName, strings.ReplaceAll(root.key, "_", " ")),
			SafeRootKey:      root.safeRoot,
			RootRelativePath: root.path,
			Include:          root.include,
			Exclude:          root.exclude,
			IgnorePolicy: watchedroots.IgnorePolicy{
				Profile:                string(filepolicy.ProfileManaged),
				DiscoverUserRules:      true,
				PolicyRootRelativePath: ".",
			},
			Scan: watchedroots.ScanConfig{HiddenPolicy: watchedroots.HiddenPolicyPolicyControlled},
			BackupPolicy: watchedroots.BackupPolicy{
				Mode:                   firstNonEmptyString(root.backupMode, watchedroots.BackupModeNone),
				MaxFileBytes:           root.maxBackupFileBytes,
				MaxBatchBytes:          root.maxBackupBatchBytes,
				MaxPendingItems:        root.maxPendingItems,
				MaxPendingBytes:        root.maxPendingBytes,
				IncludeDeletionMarkers: root.includeDeletions,
				OnLimit:                root.backupOnLimit,
			},
			SyncPolicy: watchedroots.SyncPolicy{
				Mode:                firstNonEmptyString(root.syncMode, watchedroots.SyncModeNone),
				ProjectRef:          root.projectRef,
				ScopeRef:            root.scopeRef,
				MaxFileBytes:        root.maxSyncFileBytes,
				LogicalNameStrategy: root.logicalNameStrategy,
			},
			IndexPolicy: watchedroots.IndexPolicy{
				Mode:         firstNonEmptyString(root.indexMode, watchedroots.IndexModeNone),
				MaxTextBytes: root.maxIndexTextBytes,
			},
			DeletePolicy: watchedroots.DeletePolicy{Mode: firstNonEmptyString(root.deleteMode, watchedroots.DeleteModeLocalStateOnly)},
		}
		if config.SyncPolicy.Mode == watchedroots.SyncModeNone {
			config.SyncPolicy.ProjectRef = ""
			config.SyncPolicy.ScopeRef = ""
		}
		config, err := watchedroots.ValidatePortableRootConfig(config)
		if err == nil {
			// Preserve normalized scan defaults while admitting every file allowed
			// by an enabled operation. Node safe-root limits still apply at admission.
			if config.BackupPolicy.Mode != watchedroots.BackupModeNone {
				config.Scan.MaxHashFileBytes = max(config.Scan.MaxHashFileBytes, config.BackupPolicy.MaxFileBytes)
			}
			if config.SyncPolicy.Mode != watchedroots.SyncModeNone {
				config.Scan.MaxHashFileBytes = max(config.Scan.MaxHashFileBytes, config.SyncPolicy.MaxFileBytes)
			}
			config, err = watchedroots.ValidatePortableRootConfig(config)
		}
		if err != nil {
			builder.add(Diagnostic{Severity: SeverityError, Code: "watched_root.config_invalid", Message: "compiled watched-root config is invalid: " + err.Error(), Field: "watched_roots." + root.key})
			continue
		}
		configJSON, err := json.Marshal(config)
		if err != nil {
			builder.add(Diagnostic{Severity: SeverityError, Code: "watched_root.config_invalid", Message: "compiled watched-root config could not be encoded: " + err.Error(), Field: "watched_roots." + root.key})
			continue
		}
		backendKey := config.RootKey
		item := ProjectWatchedRootItem{
			Key:              root.key,
			BackendRootKey:   backendKey,
			WorkerKey:        noderuntime.WatchedRootWorkerKey(backendKey),
			SourceKinds:      sortedStrings(root.sourceKinds),
			OwnerNode:        builder.contract.Project.OwnerNode,
			SafeRootKey:      config.SafeRootKey,
			RootRelativePath: config.RootRelativePath,
			DisplayName:      config.DisplayName,
			Include:          append([]string{}, config.Include...),
			Exclude:          append([]string{}, config.Exclude...),
			SyncMode:         config.SyncPolicy.Mode,
			BackupMode:       config.BackupPolicy.Mode,
			IndexMode:        config.IndexPolicy.Mode,
			DeleteMode:       config.DeletePolicy.Mode,
			ConfigHash:       watchedroots.ConfigHash(config),
			ConfigJSON:       json.RawMessage(configJSON),
			ActivationStatus: projectWatchedRootStatusPendingAgentApply,
			Metadata:         root.metadata,
		}
		item.AgentCommands = watchedRootCommands(builder.loaded.RootPath, config)
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].BackendRootKey < items[j].BackendRootKey
	})
	return items
}

func normalizeNotesContract(contract NotesContract) NotesContract {
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.Notes.Status = strings.ToLower(strings.TrimSpace(contract.Notes.Status))
	if contract.Notes.Status == "" {
		contract.Notes.Status = ProjectStatusDraft
	}
	contract.Notes.RootKey = filesystemconnector.NormalizeRootKey(contract.Notes.RootKey)
	if contract.Notes.RootKey == "" {
		contract.Notes.RootKey = "notes"
	}
	contract.Notes.Path = strings.TrimSpace(contract.Notes.Path)
	if contract.Notes.Path == "" {
		contract.Notes.Path = "."
	}
	contract.Notes.Include = normalizePolicyPatterns(firstNonEmptyStrings(contract.Notes.Include, defaultNotesIncludes))
	contract.Notes.Exclude = normalizePolicyPatterns(firstNonEmptyStrings(contract.Notes.Exclude, defaultNotesExcludes))
	if contract.Metadata == nil {
		contract.Metadata = map[string]any{}
	}
	return contract
}

func normalizeReposContract(contract ReposContract) ReposContract {
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.Repos.Status = strings.ToLower(strings.TrimSpace(contract.Repos.Status))
	if contract.Repos.Status == "" {
		contract.Repos.Status = ProjectStatusDraft
	}
	defaults := contract.Repos.Defaults
	if defaults.Sync == nil {
		defaults.Sync = boolPtr(false)
	}
	if defaults.Backup == nil {
		defaults.Backup = boolPtr(false)
	}
	if defaults.Index == nil {
		defaults.Index = boolPtr(false)
	}
	defaults.Include = normalizePolicyPatterns(firstNonEmptyStrings(defaults.Include, defaultRepoIncludes))
	defaults.Exclude = normalizePolicyPatterns(firstNonEmptyStrings(defaults.Exclude, defaultRepoExcludes))
	if contract.SchemaVersion == ReposSchemaV03 && len(contract.Repos.Roots) == 0 {
		contract.Repos.Roots = []RepoRootPolicySpec{{Key: "repos", Path: ".", DisplayName: "Project Repositories"}}
	}
	roots := contract.Repos.Roots
	if contract.SchemaVersion == ReposSchemaV04 {
		roots = contract.Repos.WatchRoots
	}
	for index := range roots {
		root := &roots[index]
		root.Key = filesystemconnector.NormalizeRootKey(firstNonEmptyString(root.Key, deriveLocalRootKey(root.Path)))
		if root.Key == "" {
			root.Key = "repos"
		}
		root.Path = firstNonEmptyString(strings.TrimSpace(root.Path), ".")
		root.DisplayName = strings.TrimSpace(root.DisplayName)
		root.Sync = inheritBool(root.Sync, defaults.Sync, false)
		root.Backup = inheritBool(root.Backup, defaults.Backup, false)
		root.Index = inheritBool(root.Index, defaults.Index, false)
		root.Include = normalizePolicyPatterns(firstNonEmptyStrings(root.Include, defaults.Include))
		root.Exclude = normalizePolicyPatterns(firstNonEmptyStrings(root.Exclude, defaults.Exclude))
	}
	if contract.SchemaVersion == ReposSchemaV04 {
		contract.Repos.WatchRoots = roots
	} else {
		contract.Repos.Roots = roots
	}
	for index := range contract.Repos.Members {
		member := &contract.Repos.Members[index]
		member.ID = strings.TrimSpace(member.ID)
		member.Key = strings.TrimSpace(member.Key)
		member.Path = strings.TrimSpace(member.Path)
		member.Role = strings.ToLower(strings.TrimSpace(member.Role))
		member.StateRoot = strings.TrimSpace(member.StateRoot)
	}
	if contract.Metadata == nil {
		contract.Metadata = map[string]any{}
	}
	return contract
}

func repoWatchRoots(contract ReposContract) []RepoRootPolicySpec {
	if contract.SchemaVersion == ReposSchemaV04 {
		return contract.Repos.WatchRoots
	}
	return contract.Repos.Roots
}

func RepositoryRoleOwns(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case RepositoryRolePrimary, RepositoryRoleComponent:
		return true
	default:
		return false
	}
}

func validateRepoMembers(loaded LoadedProject, contract ReposContract, contractPath string, add func(Diagnostic)) []RepoMemberSpec {
	if contract.SchemaVersion != ReposSchemaV04 {
		return nil
	}
	members := append([]RepoMemberSpec{}, contract.Repos.Members...)
	seenIDs := map[string]int{}
	seenKeys := map[string]int{}
	seenPaths := map[string]int{}
	reposRoot := filepath.Join(loaded.RootPath, "repos")
	for index := range members {
		member := &members[index]
		field := fmt.Sprintf("repos.members[%d]", index)
		if member.ID == "" {
			add(Diagnostic{Severity: SeverityError, Code: "repos.member_id_required", Message: "repository member id is required", File: contractPath, Field: field + ".id"})
		} else if err := ids.Validate(RepositoryIDPrefix, member.ID); err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "repos.member_id_invalid", Message: "repository member id must be a valid typed LOOM repository ID: " + err.Error(), File: contractPath, Field: field + ".id"})
		}
		if previous, exists := seenIDs[member.ID]; member.ID != "" && exists {
			add(Diagnostic{Severity: SeverityError, Code: "repos.member_id_duplicate", Message: fmt.Sprintf("repository member id duplicates repos.members[%d]", previous), File: contractPath, Field: field + ".id"})
		} else if member.ID != "" {
			seenIDs[member.ID] = index
		}

		if member.Key == "" {
			add(Diagnostic{Severity: SeverityError, Code: "repos.member_key_required", Message: "repository member key is required", File: contractPath, Field: field + ".key"})
		} else if !providerKeyPattern.MatchString(member.Key) {
			add(Diagnostic{Severity: SeverityError, Code: "repos.member_key_invalid", Message: "repository member key must be lowercase and URL-safe", File: contractPath, Field: field + ".key"})
		}
		if previous, exists := seenKeys[member.Key]; member.Key != "" && exists {
			add(Diagnostic{Severity: SeverityError, Code: "repos.member_key_duplicate", Message: fmt.Sprintf("repository member key duplicates repos.members[%d]", previous), File: contractPath, Field: field + ".key"})
		} else if member.Key != "" {
			seenKeys[member.Key] = index
		}

		path, err := normalizeRepositoryMemberPath(member.Path)
		if err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "repos.member_path_unsafe", Message: "repository member path must be safely relative to repos/", File: contractPath, Field: field + ".path"})
		} else {
			member.Path = path
			if hasReservedRepositoryPath(path, false) {
				add(Diagnostic{Severity: SeverityError, Code: "repos.member_path_reserved", Message: "repository member path uses a reserved LOOM or repository control name", File: contractPath, Field: field + ".path"})
			}
			if previous, exists := seenPaths[path]; exists {
				add(Diagnostic{Severity: SeverityError, Code: "repos.member_path_duplicate", Message: fmt.Sprintf("repository member path duplicates repos.members[%d]", previous), File: contractPath, Field: field + ".path"})
			} else {
				seenPaths[path] = index
			}
			target := filepath.Join(reposRoot, filepath.FromSlash(path))
			if escapes, checkErr := existingPathEscapesRoot(loaded.RootPath, target); checkErr != nil || escapes {
				message := "repository member path escapes repos/ through a symbolic link"
				if checkErr != nil {
					message += ": " + checkErr.Error()
				}
				add(Diagnostic{Severity: SeverityError, Code: "repos.member_path_symlink_escape", Message: message, File: contractPath, Field: field + ".path"})
			}
		}

		switch member.Role {
		case RepositoryRolePrimary, RepositoryRoleComponent, RepositoryRoleReference:
		default:
			add(Diagnostic{Severity: SeverityError, Code: "repos.member_role_invalid", Message: "repository member role must be primary, component, or reference", File: contractPath, Field: field + ".role"})
		}

		if member.StateRoot != "" {
			stateRoot, stateErr := normalizeRepositoryStateRoot(member.StateRoot)
			if stateErr != nil {
				add(Diagnostic{Severity: SeverityError, Code: "repos.member_state_root_unsafe", Message: "repository member state_root must be safely relative to the repository", File: contractPath, Field: field + ".state_root"})
			} else {
				member.StateRoot = stateRoot
				if hasReservedRepositoryPath(stateRoot, true) {
					add(Diagnostic{Severity: SeverityError, Code: "repos.member_state_root_reserved", Message: "repository member state_root uses a reserved control name", File: contractPath, Field: field + ".state_root"})
				}
				if member.Path != "" {
					target := filepath.Join(reposRoot, filepath.FromSlash(member.Path), filepath.FromSlash(stateRoot))
					if escapes, checkErr := existingPathEscapesRoot(loaded.RootPath, target); checkErr != nil || escapes {
						message := "repository member state_root escapes repos/ through a symbolic link"
						if checkErr != nil {
							message += ": " + checkErr.Error()
						}
						add(Diagnostic{Severity: SeverityError, Code: "repos.member_state_root_symlink_escape", Message: message, File: contractPath, Field: field + ".state_root"})
					}
				}
			}
		}
	}
	sort.SliceStable(members, func(i, j int) bool {
		if members[i].Key != members[j].Key {
			return members[i].Key < members[j].Key
		}
		return members[i].ID < members[j].ID
	})
	return members
}

func normalizeRepositoryMemberPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if unsafePortableRepositoryPath(value) {
		return "", os.ErrInvalid
	}
	value = filepath.ToSlash(value)
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", os.ErrInvalid
	}
	return clean, nil
}

func normalizeRepositoryStateRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if unsafePortableRepositoryPath(value) {
		return "", os.ErrInvalid
	}
	value = filepath.ToSlash(value)
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", os.ErrInvalid
	}
	return clean, nil
}

func unsafePortableRepositoryPath(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") {
		return true
	}
	if len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' {
		return true
	}
	return strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

func hasReservedRepositoryPath(value string, stateRoot bool) bool {
	reserved := map[string]bool{".git": true, ".loom": true, ".project": true}
	if !stateRoot {
		reserved[".repo"] = true
	}
	for _, component := range strings.Split(filepath.ToSlash(value), "/") {
		if reserved[strings.ToLower(component)] {
			return true
		}
	}
	return false
}

func existingPathEscapesRoot(root, target string) (bool, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	candidate := filepath.Clean(target)
	for {
		_, err = os.Lstat(candidate)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return false, err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return false, err
		}
		candidate = parent
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil {
		return false, err
	}
	return relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)), nil
}

func normalizeSyncPolicy(contract ProjectSyncPolicyContract) ProjectSyncPolicyContract {
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	defaults := contract.Sync.Defaults
	defaults.SafeRoot = firstNonEmptyString(defaults.SafeRoot, defaultProjectSafeRootKey)
	defaults.Mode = firstNonEmptyPolicyMode(defaults.Mode, "markdown")
	defaults.Index = firstNonEmptyPolicyMode(defaults.Index, "markdown_text")
	defaults.Delete = firstNonEmptyString(defaults.Delete, watchedroots.DeleteModeTombstone)
	defaults.LogicalNameStrategy = firstNonEmptyString(defaults.LogicalNameStrategy, watchedroots.SyncLogicalNameRelativePath)
	for index := range contract.Sync.Roots {
		root := &contract.Sync.Roots[index]
		root.Key = filesystemconnector.NormalizeRootKey(root.Key)
		root.Path = firstNonEmptyString(strings.TrimSpace(root.Path), ".")
		root.DisplayName = strings.TrimSpace(root.DisplayName)
		root.SafeRoot = filesystemconnector.NormalizeRootKey(firstNonEmptyString(root.SafeRoot, defaults.SafeRoot))
		root.Mode = firstNonEmptyPolicyMode(root.Mode, string(defaults.Mode))
		root.Index = firstNonEmptyPolicyMode(root.Index, string(defaults.Index))
		root.Delete = firstNonEmptyString(root.Delete, defaults.Delete)
		root.ProjectRef = strings.TrimSpace(firstNonEmptyString(root.ProjectRef, defaults.ProjectRef))
		root.ScopeRef = strings.TrimSpace(firstNonEmptyString(root.ScopeRef, defaults.ScopeRef))
		root.MaxFileBytes = firstPositiveInt64(root.MaxFileBytes, defaults.MaxFileBytes)
		root.MaxTextBytes = firstPositiveInt64(root.MaxTextBytes, defaults.MaxTextBytes)
		root.LogicalNameStrategy = firstNonEmptyString(root.LogicalNameStrategy, defaults.LogicalNameStrategy)
		root.Include = normalizePolicyPatterns(firstNonEmptyStrings(root.Include, defaults.Include))
		root.Exclude = normalizePolicyPatterns(firstNonEmptyStrings(root.Exclude, defaults.Exclude))
	}
	if contract.Metadata == nil {
		contract.Metadata = map[string]any{}
	}
	return contract
}

func normalizeBackupPolicy(contract ProjectBackupPolicyContract) ProjectBackupPolicyContract {
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	defaults := contract.Backup.Defaults
	defaults.SafeRoot = firstNonEmptyString(defaults.SafeRoot, defaultProjectSafeRootKey)
	defaults.Mode = firstNonEmptyString(defaults.Mode, watchedroots.BackupModeIncrementalRaw)
	defaults.OnLimit = firstNonEmptyString(defaults.OnLimit, watchedroots.BackupOnLimitDegradeAndRequireManualAction)
	if defaults.IncludeDeletionMarkers == nil {
		defaults.IncludeDeletionMarkers = boolPtr(true)
	}
	for index := range contract.Backup.Roots {
		root := &contract.Backup.Roots[index]
		root.Key = filesystemconnector.NormalizeRootKey(root.Key)
		root.Path = firstNonEmptyString(strings.TrimSpace(root.Path), ".")
		root.DisplayName = strings.TrimSpace(root.DisplayName)
		root.SafeRoot = filesystemconnector.NormalizeRootKey(firstNonEmptyString(root.SafeRoot, defaults.SafeRoot))
		root.Mode = firstNonEmptyString(root.Mode, defaults.Mode)
		root.MaxFileBytes = firstPositiveInt64(root.MaxFileBytes, defaults.MaxFileBytes)
		root.MaxBatchBytes = firstPositiveInt64(root.MaxBatchBytes, defaults.MaxBatchBytes)
		root.MaxPendingItems = firstPositiveInt(root.MaxPendingItems, defaults.MaxPendingItems)
		root.MaxPendingBytes = firstPositiveInt64(root.MaxPendingBytes, defaults.MaxPendingBytes)
		root.IncludeDeletionMarkers = inheritBool(root.IncludeDeletionMarkers, defaults.IncludeDeletionMarkers, true)
		root.OnLimit = firstNonEmptyString(root.OnLimit, defaults.OnLimit)
		root.Include = normalizePolicyPatterns(firstNonEmptyStrings(root.Include, defaults.Include))
		root.Exclude = normalizePolicyPatterns(firstNonEmptyStrings(root.Exclude, defaults.Exclude))
	}
	if contract.Metadata == nil {
		contract.Metadata = map[string]any{}
	}
	return contract
}

func validateNotesContractBasics(contract NotesContract, path string, add func(Diagnostic)) {
	if contract.Kind != NotesContractKind {
		add(Diagnostic{Severity: SeverityError, Code: "notes.kind_invalid", Message: "notes contract kind must be " + NotesContractKind, File: path, Field: "kind", Suggestion: "set kind: loom.notes"})
	}
	if contract.SchemaVersion != NotesSchemaV03 {
		add(Diagnostic{Severity: SeverityError, Code: "notes.schema_version_invalid", Message: "notes contract schema_version must be " + NotesSchemaV03, File: path, Field: "schema_version", Suggestion: "set schema_version: notes.contract.v0.3"})
	}
}

func validateReposContractBasics(contract ReposContract, path string, add func(Diagnostic)) {
	if contract.Kind != ReposContractKind {
		add(Diagnostic{Severity: SeverityError, Code: "repos.kind_invalid", Message: "repos contract kind must be " + ReposContractKind, File: path, Field: "kind", Suggestion: "set kind: loom.repos"})
	}
	if contract.SchemaVersion != ReposSchemaV03 && contract.SchemaVersion != ReposSchemaV04 {
		add(Diagnostic{Severity: SeverityError, Code: "repos.schema_version_invalid", Message: "repos contract schema_version must be " + ReposSchemaV03 + " or " + ReposSchemaV04, File: path, Field: "schema_version", Suggestion: "set schema_version to a supported repos contract version"})
	}
	if contract.SchemaVersion == ReposSchemaV03 && (len(contract.Repos.WatchRoots) > 0 || len(contract.Repos.Members) > 0) {
		add(Diagnostic{Severity: SeverityError, Code: "repos.v04_fields_not_supported", Message: "watch_roots and members require " + ReposSchemaV04, File: path, Field: "repos"})
	}
	if contract.SchemaVersion == ReposSchemaV04 && len(contract.Repos.Roots) > 0 {
		add(Diagnostic{Severity: SeverityError, Code: "repos.roots_legacy", Message: "repos.contract.v0.4 uses watch_roots; roots remains v0.3 watched-root policy only", File: path, Field: "repos.roots", Suggestion: "rename roots to watch_roots without creating members"})
	}
}

func projectSyncPolicyPath(loaded LoadedProject, contract ProjectContract) (string, bool) {
	if !contract.Facets["sync_policy"] && strings.TrimSpace(contract.Policies.Sync) == "" {
		return "", false
	}
	resolution, err := ResolveSingletonContract(loaded, ProjectContractSync, contract.Policies.Sync)
	return resolution.Path, err == nil && resolution.Present
}

func projectBackupPolicyPath(loaded LoadedProject, contract ProjectContract) (string, bool) {
	if !contract.Facets["backup_policy"] && strings.TrimSpace(contract.Policies.Backup) == "" {
		return "", false
	}
	resolution, err := ResolveSingletonContract(loaded, ProjectContractBackup, contract.Policies.Backup)
	return resolution.Path, err == nil && resolution.Present
}

func facetProjectPath(facet, value string) (string, error) {
	relative, err := normalizeProjectRootRelativePath(value)
	if err != nil {
		return "", err
	}
	if relative == "" || relative == "." {
		return facet, nil
	}
	return filepath.ToSlash(filepath.Join(facet, filepath.FromSlash(relative))), nil
}

func normalizeProjectRootRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return ".", nil
	}
	if filepath.IsAbs(value) {
		return "", os.ErrInvalid
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." {
		return ".", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", os.ErrInvalid
	}
	return clean, nil
}

func normalizeSyncMode(value PolicyModeValue, include []string) (string, []string) {
	mode := strings.ToLower(strings.TrimSpace(string(value)))
	switch mode {
	case "", "false", "none":
		return watchedroots.SyncModeNone, include
	case "true", "selected_files":
		return watchedroots.SyncModeSelectedFiles, firstNonEmptyStrings(include, []string{"**/*"})
	case "markdown":
		return watchedroots.SyncModeSelectedFiles, firstNonEmptyStrings(include, defaultNotesTextIncludes)
	default:
		return mode, include
	}
}

func normalizeIndexMode(value PolicyModeValue, syncMode string) string {
	mode := strings.ToLower(strings.TrimSpace(string(value)))
	switch mode {
	case "", "false", "none":
		return watchedroots.IndexModeNone
	case "true":
		return watchedroots.IndexModeMarkdownText
	default:
		return mode
	}
}

func modeIf(enabled bool, yes, no string) string {
	if enabled {
		return yes
	}
	return no
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func boolPtr(value bool) *bool {
	return &value
}

func inheritBool(value, fallback *bool, defaultValue bool) *bool {
	if value != nil {
		return value
	}
	if fallback != nil {
		return boolPtr(*fallback)
	}
	return boolPtr(defaultValue)
}

func validProjectFacetStatus(status string) bool {
	switch status {
	case ProjectStatusDraft, ProjectStatusActive, ProjectStatusPaused, "disabled":
		return true
	default:
		return false
	}
}

func deriveLocalRootKey(path string) string {
	path = strings.TrimSpace(filepath.ToSlash(path))
	if path == "" || path == "." {
		return "project"
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 {
		return "project"
	}
	return filesystemconnector.NormalizeRootKey(parts[len(parts)-1])
}

func compatiblePatternSet(existing, incoming []string) bool {
	if len(existing) == 0 || len(incoming) == 0 {
		return true
	}
	return stringSlicesEqual(existing, incoming)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mergeMode(target *string, incoming, none string, add func(Diagnostic), file, field, name string) {
	if incoming == "" || incoming == none {
		return
	}
	if *target == "" || *target == none {
		*target = incoming
		return
	}
	if *target != incoming {
		add(Diagnostic{Severity: SeverityError, Code: "watched_root.mode_conflict", Message: "watched-root has incompatible " + name + " declarations", File: file, Field: field})
	}
}

func mergeDeleteMode(target *string, incoming string) {
	if incoming == "" || incoming == watchedroots.DeleteModeLocalStateOnly {
		return
	}
	*target = incoming
}

func appendUnique(values []string, additions ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	for _, value := range additions {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func sortedStrings(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

func normalizePolicyPatterns(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(filepath.ToSlash(value))
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func firstNonEmptyStrings(value, fallback []string) []string {
	if len(value) > 0 {
		return append([]string{}, value...)
	}
	return append([]string{}, fallback...)
}

func firstNonEmptyPolicyMode(value PolicyModeValue, fallback string) PolicyModeValue {
	if strings.TrimSpace(string(value)) != "" {
		return value
	}
	return PolicyModeValue(fallback)
}

func firstPositiveInt64(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

func firstPositiveInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func watchedRootCommands(projectRoot string, config watchedroots.RootConfig) []ProjectWatchedRootCommand {
	applyCommand := []string{"loom-node-agent", "watched-roots", "apply-plan", "/tmp/loom-project-watch-plan.json"}
	if strings.TrimSpace(projectRoot) != "" {
		applyCommand = append(applyCommand, "--project-root", projectRoot)
	}
	applyCommand = append(applyCommand, "--root", config.RootKey)
	return []ProjectWatchedRootCommand{
		{
			Description: "Apply the exact compiled watched-root config from the project watch-plan on the owner node.",
			Command:     applyCommand,
			Shell:       shellJoin(applyCommand),
		},
	}
}

func watchedRootSummaries(roots []ProjectWatchedRootItem) []string {
	out := []string{}
	for _, root := range roots {
		out = append(out, fmt.Sprintf(
			"%s %s %s:%s sync=%s index=%s backup=%s",
			root.BackendRootKey,
			root.OwnerNode,
			root.SafeRootKey,
			root.RootRelativePath,
			root.SyncMode,
			root.IndexMode,
			root.BackupMode,
		))
	}
	sort.Strings(out)
	return out
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" {
			quoted = append(quoted, "''")
			continue
		}
		if strings.IndexFunc(arg, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@", r))
		}) == -1 {
			quoted = append(quoted, arg)
			continue
		}
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", "'\"'\"'")+"'")
	}
	return strings.Join(quoted, " ")
}
