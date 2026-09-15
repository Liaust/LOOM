package projectcontracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/ids"
)

const layoutMigrationRecordSchema = "project.layout_migration.v1"

type LayoutMigrationOptions struct {
	ProjectRef  string `json:"project_ref,omitempty"`
	ProjectRoot string `json:"project_root"`
	Apply       bool   `json:"apply,omitempty"`
	Yes         bool   `json:"yes,omitempty"`
}

type LayoutMigrationPlan struct {
	ProjectRoot string                     `json:"project_root"`
	Layout      ProjectLayout              `json:"layout"`
	Archived    bool                       `json:"archived,omitempty"`
	Actions     []LayoutMigrationAction    `json:"actions"`
	Collisions  []LayoutMigrationCollision `json:"collisions"`
	Skips       []LayoutMigrationSkip      `json:"skips"`
	NextActions []string                   `json:"next_actions"`
	planned     []plannedLayoutAction
}

type LayoutMigrationResult struct {
	OK           bool                       `json:"ok"`
	DryRun       bool                       `json:"dry_run"`
	Applied      bool                       `json:"applied"`
	ProjectRoot  string                     `json:"project_root"`
	BeforeLayout ProjectLayout              `json:"before_layout"`
	AfterLayout  ProjectLayout              `json:"after_layout,omitempty"`
	Actions      []LayoutMigrationAction    `json:"actions"`
	Collisions   []LayoutMigrationCollision `json:"collisions"`
	Skips        []LayoutMigrationSkip      `json:"skips"`
	NextActions  []string                   `json:"next_actions"`
	RecordPath   string                     `json:"record_path,omitempty"`
}

type LayoutMigrationAction struct {
	Order       int    `json:"order"`
	Operation   string `json:"operation"`
	Kind        string `json:"kind"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination,omitempty"`
	BeforeHash  string `json:"before_hash,omitempty"`
	AfterHash   string `json:"after_hash,omitempty"`
	Status      string `json:"status"`
}

type LayoutMigrationCollision struct {
	Kind            string `json:"kind"`
	Source          string `json:"source,omitempty"`
	Destination     string `json:"destination"`
	SourceHash      string `json:"source_hash,omitempty"`
	DestinationHash string `json:"destination_hash,omitempty"`
	Reason          string `json:"reason"`
}

type LayoutMigrationSkip struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

type plannedLayoutAction struct {
	Public     LayoutMigrationAction
	Content    []byte
	Mode       fs.FileMode
	SourceHash string
	TargetHash string
}

type layoutMigrationRecord struct {
	SchemaVersion string                     `json:"schema_version"`
	RecordedAt    time.Time                  `json:"recorded_at"`
	ProjectRoot   string                     `json:"project_root"`
	BeforeLayout  ProjectLayout              `json:"before_layout"`
	AfterLayout   ProjectLayout              `json:"after_layout,omitempty"`
	OK            bool                       `json:"ok"`
	Actions       []LayoutMigrationAction    `json:"actions"`
	Collisions    []LayoutMigrationCollision `json:"collisions,omitempty"`
	Error         string                     `json:"error,omitempty"`
}

type ProjectContractV04MigrationOptions struct {
	ProjectRoot       string           `json:"project_root"`
	ProjectID         string           `json:"project_id"`
	RepositoryMembers []RepoMemberSpec `json:"repository_members,omitempty"`
}

type ProjectContractV04MigrationPlan struct {
	ProjectRoot       string          `json:"project_root"`
	ProjectPath       string          `json:"project_path"`
	ReposPath         string          `json:"repos_path,omitempty"`
	Project           ProjectContract `json:"project"`
	Repos             ReposContract   `json:"repos,omitempty"`
	projectRaw        []byte
	reposRaw          []byte
	reposPresent      bool
	projectTargetHash string
	reposTargetHash   string
}

type ProjectContractV04MigrationResult struct {
	OK          bool            `json:"ok"`
	Applied     bool            `json:"applied"`
	ProjectRoot string          `json:"project_root"`
	Project     ProjectContract `json:"project"`
	Repos       ReposContract   `json:"repos,omitempty"`
}

func PlanProjectContractV04Migration(options ProjectContractV04MigrationOptions) (ProjectContractV04MigrationPlan, error) {
	loaded, err := LoadProject(options.ProjectRoot)
	if err != nil {
		return ProjectContractV04MigrationPlan{}, err
	}
	project := NormalizeContract(loaded.Contract)
	if project.SchemaVersion != ProjectSchemaV03 && project.SchemaVersion != ProjectSchemaV04 {
		return ProjectContractV04MigrationPlan{}, fmt.Errorf("project contract schema %q cannot migrate to %s", project.SchemaVersion, ProjectSchemaV04)
	}
	if project.SchemaVersion == ProjectSchemaV03 {
		report := Validate(loaded)
		if !report.OK {
			return ProjectContractV04MigrationPlan{}, fmt.Errorf("v0.3 project must validate before contract migration: %s", firstDiagnosticSummary(report.Diagnostics))
		}
	}
	projectID := strings.TrimSpace(options.ProjectID)
	if project.SchemaVersion == ProjectSchemaV04 {
		if projectID == "" {
			projectID = project.Project.ID
		}
		if project.Project.ID != "" && project.Project.ID != projectID {
			return ProjectContractV04MigrationPlan{}, fmt.Errorf("supplied project ID does not match existing portable project identity")
		}
	}
	if projectID == "" {
		return ProjectContractV04MigrationPlan{}, fmt.Errorf("existing typed project ID is required for project.contract.v0.4 migration")
	}
	if err := ids.Validate(ids.ProjectPrefix, projectID); err != nil {
		return ProjectContractV04MigrationPlan{}, fmt.Errorf("project ID is invalid: %w", err)
	}
	project.SchemaVersion = ProjectSchemaV04
	project.Project.ID = projectID
	projectRelative, err := filepath.Rel(loaded.RootPath, loaded.ContractPath)
	if err != nil {
		return ProjectContractV04MigrationPlan{}, err
	}
	plan := ProjectContractV04MigrationPlan{
		ProjectRoot: loaded.RootPath,
		ProjectPath: filepath.ToSlash(projectRelative),
		Project:     project,
		projectRaw:  append([]byte{}, loaded.Raw...),
	}

	if !project.Facets["repos"] && options.RepositoryMembers == nil {
		if err := sealProjectContractV04MigrationPlan(&plan); err != nil {
			return ProjectContractV04MigrationPlan{}, err
		}
		return plan, nil
	}
	resolution, err := ResolveSingletonContract(loaded, ProjectContractRepos, "")
	if err != nil {
		return ProjectContractV04MigrationPlan{}, err
	}
	plan.ReposPath = resolution.RelativePath
	repos := ReposContract{
		Kind:          ReposContractKind,
		SchemaVersion: ReposSchemaV04,
		Repos: ReposPolicySpec{
			Status:     ProjectStatusDraft,
			WatchRoots: []RepoRootPolicySpec{},
			Members:    []RepoMemberSpec{},
		},
		Metadata: map[string]any{},
	}
	if resolution.Present {
		loadedRepos, raw, loadErr := LoadReposContract(resolution.Path)
		if loadErr != nil {
			return ProjectContractV04MigrationPlan{}, loadErr
		}
		loadedRepos = normalizeReposContract(loadedRepos)
		if loadedRepos.SchemaVersion != ReposSchemaV03 && loadedRepos.SchemaVersion != ReposSchemaV04 {
			return ProjectContractV04MigrationPlan{}, fmt.Errorf("repos contract schema %q cannot migrate to %s", loadedRepos.SchemaVersion, ReposSchemaV04)
		}
		repos.Kind = loadedRepos.Kind
		repos.Repos.Status = loadedRepos.Repos.Status
		repos.Repos.Defaults = loadedRepos.Repos.Defaults
		repos.Repos.WatchRoots = append([]RepoRootPolicySpec{}, repoWatchRoots(loadedRepos)...)
		repos.Metadata = loadedRepos.Metadata
		if loadedRepos.SchemaVersion == ReposSchemaV04 {
			repos.Repos.Members = append([]RepoMemberSpec{}, loadedRepos.Repos.Members...)
		}
		plan.reposRaw = append([]byte{}, raw...)
		plan.reposPresent = true
	}
	if options.RepositoryMembers != nil {
		members, memberErr := normalizeExplicitRepositoryMembers(options.RepositoryMembers, true)
		if memberErr != nil {
			return ProjectContractV04MigrationPlan{}, memberErr
		}
		repos.Repos.Members = members
	}
	plan.Repos = repos
	if err := sealProjectContractV04MigrationPlan(&plan); err != nil {
		return ProjectContractV04MigrationPlan{}, err
	}
	return plan, nil
}

func ApplyProjectContractV04Migration(plan ProjectContractV04MigrationPlan, yes bool) (ProjectContractV04MigrationResult, error) {
	result := ProjectContractV04MigrationResult{ProjectRoot: plan.ProjectRoot, Project: plan.Project, Repos: plan.Repos}
	if !yes {
		return result, fmt.Errorf("project contract v0.4 migration apply requires explicit confirmation")
	}
	if strings.TrimSpace(plan.ProjectRoot) == "" || strings.TrimSpace(plan.ProjectPath) == "" {
		return result, fmt.Errorf("project contract v0.4 migration plan is incomplete")
	}
	if err := verifyProjectContractV04MigrationPlan(plan); err != nil {
		return result, err
	}
	currentProject, present, err := readMigrationFile(plan.ProjectRoot, plan.ProjectPath)
	if err != nil {
		return result, err
	}
	if !present || !bytes.Equal(currentProject, plan.projectRaw) {
		return result, fmt.Errorf("project contract changed after planning")
	}
	if plan.ReposPath != "" {
		currentRepos, reposPresent, readErr := readMigrationFile(plan.ProjectRoot, plan.ReposPath)
		if readErr != nil {
			return result, readErr
		}
		if reposPresent != plan.reposPresent || !bytes.Equal(currentRepos, plan.reposRaw) {
			return result, fmt.Errorf("repos contract changed after planning")
		}
	}

	members := append([]RepoMemberSpec{}, plan.Repos.Repos.Members...)
	for index := range members {
		if strings.TrimSpace(members[index].ID) == "" {
			members[index].ID = NewRepositoryID()
		}
	}
	if len(members) > 0 {
		members, err = normalizeExplicitRepositoryMembers(members, false)
		if err != nil {
			return result, err
		}
		if err := validateMigrationMemberPaths(plan.ProjectRoot, members); err != nil {
			return result, err
		}
	}
	result.Repos.Repos.Members = members

	projectRaw, err := yaml.Marshal(result.Project)
	if err != nil {
		return result, err
	}
	var reposRaw []byte
	if plan.ReposPath != "" {
		reposRaw, err = marshalReposContractV04(result.Repos)
		if err != nil {
			return result, err
		}
	}
	if err := atomicWriteMigrationFile(plan.ProjectRoot, plan.ProjectPath, ensureTrailingNewline(projectRaw), 0o644); err != nil {
		return result, err
	}
	if plan.ReposPath != "" {
		if err := atomicWriteMigrationFile(plan.ProjectRoot, plan.ReposPath, ensureTrailingNewline(reposRaw), 0o644); err != nil {
			rollbackErr := atomicWriteMigrationFile(plan.ProjectRoot, plan.ProjectPath, plan.projectRaw, 0o644)
			if rollbackErr != nil {
				return result, fmt.Errorf("write repos contract: %w (project contract rollback failed: %v)", err, rollbackErr)
			}
			return result, err
		}
	}
	result.OK = true
	result.Applied = true
	return result, nil
}

func validateMigrationMemberPaths(projectRoot string, members []RepoMemberSpec) error {
	reposRoot := filepath.Join(projectRoot, "repos")
	for _, member := range members {
		memberPath := filepath.Join(reposRoot, filepath.FromSlash(member.Path))
		if escapes, err := existingPathEscapesRoot(projectRoot, memberPath); err != nil || escapes {
			if err != nil {
				return fmt.Errorf("repository member %s path symlink check failed: %w", member.Key, err)
			}
			return fmt.Errorf("repository member %s path escapes repos/ through a symbolic link", member.Key)
		}
		if member.StateRoot == "" {
			continue
		}
		statePath := filepath.Join(memberPath, filepath.FromSlash(member.StateRoot))
		if escapes, err := existingPathEscapesRoot(projectRoot, statePath); err != nil || escapes {
			if err != nil {
				return fmt.Errorf("repository member %s state_root symlink check failed: %w", member.Key, err)
			}
			return fmt.Errorf("repository member %s state_root escapes repos/ through a symbolic link", member.Key)
		}
	}
	return nil
}

func marshalReposContractV04(contract ReposContract) ([]byte, error) {
	type reposPolicyV04 struct {
		Status     string               `yaml:"status,omitempty"`
		Defaults   RepoRootPolicySpec   `yaml:"defaults,omitempty"`
		WatchRoots []RepoRootPolicySpec `yaml:"watch_roots"`
		Members    []RepoMemberSpec     `yaml:"members"`
	}
	type reposContractV04 struct {
		Kind          string         `yaml:"kind"`
		SchemaVersion string         `yaml:"schema_version"`
		Repos         reposPolicyV04 `yaml:"repos"`
		Metadata      map[string]any `yaml:"metadata,omitempty"`
	}
	return yaml.Marshal(reposContractV04{
		Kind:          contract.Kind,
		SchemaVersion: contract.SchemaVersion,
		Repos: reposPolicyV04{
			Status:     contract.Repos.Status,
			Defaults:   contract.Repos.Defaults,
			WatchRoots: append([]RepoRootPolicySpec{}, contract.Repos.WatchRoots...),
			Members:    append([]RepoMemberSpec{}, contract.Repos.Members...),
		},
		Metadata: contract.Metadata,
	})
}

func sealProjectContractV04MigrationPlan(plan *ProjectContractV04MigrationPlan) error {
	projectRaw, err := yaml.Marshal(plan.Project)
	if err != nil {
		return err
	}
	plan.projectTargetHash = hashBytesURI(projectRaw)
	if plan.ReposPath == "" {
		return nil
	}
	reposRaw, err := marshalReposContractV04(plan.Repos)
	if err != nil {
		return err
	}
	plan.reposTargetHash = hashBytesURI(reposRaw)
	return nil
}

func verifyProjectContractV04MigrationPlan(plan ProjectContractV04MigrationPlan) error {
	projectRaw, err := yaml.Marshal(plan.Project)
	if err != nil {
		return err
	}
	if plan.projectTargetHash == "" || hashBytesURI(projectRaw) != plan.projectTargetHash {
		return fmt.Errorf("project contract migration target changed after planning")
	}
	if plan.ReposPath == "" {
		return nil
	}
	reposRaw, err := marshalReposContractV04(plan.Repos)
	if err != nil {
		return err
	}
	if plan.reposTargetHash == "" || hashBytesURI(reposRaw) != plan.reposTargetHash {
		return fmt.Errorf("repos contract migration target changed after planning")
	}
	return nil
}

func MigrateProjectLayout(options LayoutMigrationOptions) (LayoutMigrationResult, error) {
	plan, err := PlanProjectLayoutMigration(options.ProjectRoot)
	if err != nil {
		return LayoutMigrationResult{}, err
	}
	result := layoutMigrationResultFromPlan(plan, !options.Apply)
	if len(plan.Collisions) > 0 {
		result.OK = false
		if !options.Apply {
			return result, nil
		}
		return result, fmt.Errorf("project layout migration blocked by %d collision(s)", len(plan.Collisions))
	}
	if !options.Apply {
		result.OK = true
		return result, nil
	}
	if plan.Archived {
		result.OK = false
		return result, fmt.Errorf("archived project layout is immutable")
	}
	if !options.Yes {
		result.OK = false
		return result, fmt.Errorf("project layout migration apply requires --apply --yes")
	}
	return ApplyProjectLayoutMigration(plan, true)
}

func PlanProjectLayoutMigration(root string) (LayoutMigrationPlan, error) {
	loaded, err := LoadProject(root)
	if err != nil {
		return LayoutMigrationPlan{}, err
	}
	contract := NormalizeContract(loaded.Contract)
	plan := LayoutMigrationPlan{
		ProjectRoot: loaded.RootPath,
		Layout:      loaded.Layout,
		Archived:    contract.Project.Status == ProjectStatusArchived,
		Actions:     []LayoutMigrationAction{},
		Collisions:  []LayoutMigrationCollision{},
		Skips:       []LayoutMigrationSkip{},
		NextActions: layoutMigrationNextActions(loaded.RootPath),
		planned:     []plannedLayoutAction{},
	}

	data, err := scaffoldDataFromContract(contract, enabledContractFacets(contract.Facets))
	if err != nil {
		return plan, err
	}
	if err := planRootContractMigration(&plan, loaded, contract); err != nil {
		return plan, err
	}
	for _, kind := range []string{ProjectContractNotes, ProjectContractRepos, ProjectContractSync, ProjectContractBackup, ProjectContractWorkers, ProjectContractCredentials} {
		if err := planSingletonContractMigration(&plan, loaded, contract, kind); err != nil {
			return plan, err
		}
	}
	if err := planPortableControlFiles(&plan, data); err != nil {
		return plan, err
	}
	if err := planLegacyGeneratedCleanup(&plan, data, contract); err != nil {
		return plan, err
	}
	if err := planLegacyDirectoryCleanup(&plan, contract); err != nil {
		return plan, err
	}

	sort.SliceStable(plan.Collisions, func(i, j int) bool {
		if plan.Collisions[i].Destination != plan.Collisions[j].Destination {
			return plan.Collisions[i].Destination < plan.Collisions[j].Destination
		}
		return plan.Collisions[i].Source < plan.Collisions[j].Source
	})
	sort.SliceStable(plan.Skips, func(i, j int) bool { return plan.Skips[i].Path < plan.Skips[j].Path })
	for index := range plan.planned {
		plan.planned[index].Public.Order = index + 1
		plan.Actions = append(plan.Actions, plan.planned[index].Public)
	}
	return plan, nil
}

func ApplyProjectLayoutMigration(plan LayoutMigrationPlan, yes bool) (LayoutMigrationResult, error) {
	result := layoutMigrationResultFromPlan(plan, false)
	if !yes {
		result.OK = false
		return result, fmt.Errorf("project layout migration apply requires explicit confirmation")
	}
	if plan.Archived {
		result.OK = false
		return result, fmt.Errorf("archived project layout is immutable")
	}
	if len(plan.Collisions) > 0 {
		result.OK = false
		return result, fmt.Errorf("project layout migration blocked by %d collision(s)", len(plan.Collisions))
	}
	if err := preflightLayoutMigrationPlan(plan); err != nil {
		result.OK = false
		return result, err
	}
	for index := range plan.planned {
		action := plan.planned[index]
		if err := applyLayoutMigrationAction(plan.ProjectRoot, action); err != nil {
			result.Actions[index].Status = "failed"
			for pending := index + 1; pending < len(result.Actions); pending++ {
				result.Actions[pending].Status = "not_applied"
			}
			result.OK = false
			recordPath, recordErr := writeLayoutMigrationRecord(result, err)
			result.RecordPath = recordPath
			if recordErr != nil {
				return result, fmt.Errorf("apply layout migration: %w (record failure: %v)", err, recordErr)
			}
			return result, err
		}
		result.Actions[index].Status = "applied"
	}
	result.Applied = true
	result.OK = true
	if loaded, err := LoadProject(plan.ProjectRoot); err == nil {
		result.AfterLayout = loaded.Layout
	}
	recordPath, err := writeLayoutMigrationRecord(result, nil)
	result.RecordPath = recordPath
	if err != nil {
		result.OK = false
		return result, err
	}
	return result, nil
}

func planRootContractMigration(plan *LayoutMigrationPlan, loaded LoadedProject, contract ProjectContract) error {
	desiredContract := contract
	rewriteKnownLegacyPolicyRefs(&desiredContract)
	desiredRaw := loaded.Raw
	if !projectContractsSemanticallyEqual(contract, desiredContract) {
		var err error
		desiredRaw, err = yaml.Marshal(desiredContract)
		if err != nil {
			return err
		}
	}
	canonicalRelative := CanonicalRootContractPath
	legacyRelative := LegacyRootContractPath
	canonicalRaw, canonicalPresent, err := readMigrationFile(plan.ProjectRoot, canonicalRelative)
	if err != nil {
		return err
	}
	legacyRaw, legacyPresent, err := readMigrationFile(plan.ProjectRoot, legacyRelative)
	if err != nil {
		return err
	}
	if canonicalPresent {
		if !bytes.Equal(bytes.TrimSpace(canonicalRaw), bytes.TrimSpace(desiredRaw)) {
			addLayoutWriteAction(plan, "rewrite_contract", "root_contract", "", canonicalRelative, canonicalRaw, desiredRaw, 0o644)
		}
		if legacyPresent {
			addLayoutRemoveAction(plan, "remove_redundant", "root_contract", legacyRelative, legacyRaw)
		}
		return nil
	}
	if !legacyPresent {
		return fmt.Errorf("legacy root contract disappeared while planning migration")
	}
	if bytes.Equal(legacyRaw, desiredRaw) {
		addLayoutMoveAction(plan, "root_contract", legacyRelative, canonicalRelative, legacyRaw)
	} else {
		addLayoutWriteRemoveAction(plan, "relocate_contract", "root_contract", legacyRelative, canonicalRelative, legacyRaw, desiredRaw, 0o644)
	}
	return nil
}

func rewriteKnownLegacyPolicyRefs(contract *ProjectContract) {
	if strings.TrimSpace(contract.Policies.Sync) == singletonContractDefinitions[ProjectContractSync].Legacy {
		contract.Policies.Sync = singletonContractDefinitions[ProjectContractSync].Canonical
	}
	if strings.TrimSpace(contract.Policies.Backup) == singletonContractDefinitions[ProjectContractBackup].Legacy {
		contract.Policies.Backup = singletonContractDefinitions[ProjectContractBackup].Canonical
	}
	if strings.TrimSpace(contract.Policies.Workers) == singletonContractDefinitions[ProjectContractWorkers].Legacy {
		contract.Policies.Workers = singletonContractDefinitions[ProjectContractWorkers].Canonical
	}
	if strings.TrimSpace(contract.Policies.Credentials) == singletonContractDefinitions[ProjectContractCredentials].Legacy {
		contract.Policies.Credentials = singletonContractDefinitions[ProjectContractCredentials].Canonical
	}
}

func planSingletonContractMigration(plan *LayoutMigrationPlan, loaded LoadedProject, contract ProjectContract, kind string) error {
	definition := singletonContractDefinitions[kind]
	explicit := policyReferenceForKind(contract, kind)
	if explicit != "" && explicit != definition.Legacy && explicit != definition.Canonical {
		return nil
	}
	legacyRaw, legacyPresent, err := readMigrationFile(plan.ProjectRoot, definition.Legacy)
	if err != nil || !legacyPresent {
		return err
	}
	canonicalRaw, canonicalPresent, err := readMigrationFile(plan.ProjectRoot, definition.Canonical)
	if err != nil {
		return err
	}
	if canonicalPresent {
		equal, compareErr := yamlDocumentsSemanticallyEqual(canonicalRaw, legacyRaw)
		if compareErr != nil {
			return compareErr
		}
		if !equal {
			plan.Collisions = append(plan.Collisions, LayoutMigrationCollision{
				Kind:            kind,
				Source:          definition.Legacy,
				Destination:     definition.Canonical,
				SourceHash:      hashBytesURI(legacyRaw),
				DestinationHash: hashBytesURI(canonicalRaw),
				Reason:          "canonical destination contains a different contract",
			})
			return nil
		}
		addLayoutRemoveAction(plan, "remove_redundant", kind+"_contract", definition.Legacy, legacyRaw)
		return nil
	}
	addLayoutMoveAction(plan, kind+"_contract", definition.Legacy, definition.Canonical, legacyRaw)
	return nil
}

func policyReferenceForKind(contract ProjectContract, kind string) string {
	switch kind {
	case ProjectContractSync:
		return strings.TrimSpace(contract.Policies.Sync)
	case ProjectContractBackup:
		return strings.TrimSpace(contract.Policies.Backup)
	case ProjectContractWorkers:
		return strings.TrimSpace(contract.Policies.Workers)
	case ProjectContractCredentials:
		return strings.TrimSpace(contract.Policies.Credentials)
	default:
		return ""
	}
}

func planPortableControlFiles(plan *LayoutMigrationPlan, data scaffoldData) error {
	desired := []scaffoldFile{
		{RelativePath: ".loom/.gitignore", Kind: "loom_gitignore", Content: ensureTrailingNewline([]byte(loomGitignoreTemplate)), Mode: 0o644},
		{RelativePath: ".loom/tools/validate-project.sh", Kind: "validation_script", Content: ensureTrailingNewline([]byte(validateProjectScriptTemplate)), Mode: 0o755},
	}
	for _, item := range []struct {
		path string
		kind string
		tmpl string
	}{
		{path: "README.md", kind: "root_readme", tmpl: conciseRootReadmeTemplate},
		{path: "AGENTS.md", kind: "root_agents", tmpl: conciseRootAgentsTemplate},
		{path: ".loom/agents/project.md", kind: "project_agents", tmpl: projectAgentsTemplate},
	} {
		content, err := renderScaffoldTemplate(item.path, item.tmpl, data)
		if err != nil {
			return err
		}
		desired = append(desired, scaffoldFile{RelativePath: item.path, Kind: item.kind, Content: []byte(content), Mode: 0o644})
	}
	for _, facet := range data.Facets {
		file, err := surfaceScaffoldFile(data, facet)
		if err != nil {
			return err
		}
		desired = append(desired, file)
	}
	sort.SliceStable(desired, func(i, j int) bool { return desired[i].RelativePath < desired[j].RelativePath })

	legacyRootFiles, err := renderedLegacyRootFiles(data)
	if err != nil {
		return err
	}
	for _, file := range desired {
		existing, present, err := readMigrationFile(plan.ProjectRoot, file.RelativePath)
		if err != nil {
			return err
		}
		if !present {
			addLayoutWriteAction(plan, "create", file.Kind, "", file.RelativePath, nil, file.Content, file.Mode)
			continue
		}
		if bytes.Equal(existing, file.Content) {
			continue
		}
		legacy, legacyKnown := legacyRootFiles[file.RelativePath]
		if (file.RelativePath == "README.md" || file.RelativePath == "AGENTS.md") && legacyKnown && bytes.Equal(existing, legacy) {
			addLayoutWriteAction(plan, "replace_generated", file.Kind, "", file.RelativePath, existing, file.Content, file.Mode)
			continue
		}
		if file.RelativePath == "README.md" || file.RelativePath == "AGENTS.md" {
			plan.Skips = append(plan.Skips, LayoutMigrationSkip{Path: file.RelativePath, Kind: file.Kind, Reason: "preserved user-authored or modified root file"})
			continue
		}
		plan.Collisions = append(plan.Collisions, LayoutMigrationCollision{
			Kind:            file.Kind,
			Destination:     file.RelativePath,
			DestinationHash: hashBytesURI(existing),
			SourceHash:      hashBytesURI(file.Content),
			Reason:          "portable LOOM destination already contains different content",
		})
	}
	return nil
}

func renderedLegacyRootFiles(data scaffoldData) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, item := range []struct {
		path string
		tmpl string
	}{
		{path: "README.md", tmpl: rootReadmeTemplate},
		{path: "AGENTS.md", tmpl: rootAgentsTemplate},
	} {
		content, err := renderScaffoldTemplate(item.path, item.tmpl, data)
		if err != nil {
			return nil, err
		}
		out[item.path] = []byte(content)
	}
	return out, nil
}

func planLegacyGeneratedCleanup(plan *LayoutMigrationPlan, data scaffoldData, contract ProjectContract) error {
	legacyFiles, err := renderedLegacyGeneratedFiles(data, contract)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(legacyFiles))
	for path := range legacyFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		existing, present, err := readMigrationFile(plan.ProjectRoot, path)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		if bytes.Equal(existing, legacyFiles[path]) {
			addLayoutRemoveAction(plan, "remove_generated", "legacy_scaffold_file", path, existing)
			continue
		}
		plan.Skips = append(plan.Skips, LayoutMigrationSkip{Path: path, Kind: "legacy_scaffold_file", Reason: "preserved user-authored or modified file"})
	}
	return nil
}

func renderedLegacyGeneratedFiles(data scaffoldData, contract ProjectContract) (map[string][]byte, error) {
	files := map[string][]byte{}
	addRendered := func(path, tmpl string, renderData any) error {
		content, err := renderScaffoldTemplate(path, tmpl, renderData)
		if err != nil {
			return err
		}
		files[path] = []byte(content)
		return nil
	}
	facetTemplates := map[string][2]string{
		"notes":         {notesReadmeTemplate, notesAgentsTemplate},
		"scripts":       {scriptsReadmeTemplate, scriptsAgentsTemplate},
		"workflows":     {workflowsReadmeTemplate, workflowsAgentsTemplate},
		"connectors":    {connectorsReadmeTemplate, connectorsAgentsTemplate},
		"schedules":     {schedulesReadmeTemplate, schedulesAgentsTemplate},
		"direct_events": {directEventsReadmeTemplate, directEventsAgentsTemplate},
		"modules":       {modulesReadmeTemplate, modulesAgentsTemplate},
		"repos":         {reposReadmeTemplate, reposAgentsTemplate},
		"datasets":      {datasetsReadmeTemplate, datasetsAgentsTemplate},
		"docs":          {docsReadmeTemplate, docsAgentsTemplate},
		"secrets":       {secretsReadmeTemplate, secretsAgentsTemplate},
	}
	for facet, templates := range facetTemplates {
		if !contract.Facets[facet] {
			continue
		}
		if err := addRendered(facet+"/README.md", templates[0], data); err != nil {
			return nil, err
		}
		if err := addRendered(facet+"/AGENTS.md", templates[1], data); err != nil {
			return nil, err
		}
	}
	if contract.Facets["docs"] {
		if err := addRendered("docs/contracts.md", contractsDocTemplate, data); err != nil {
			return nil, err
		}
	}
	if contract.Facets["connectors"] {
		if err := addRendered("connectors/example_connector/AGENTS.md", connectorAgentsTemplate, data); err != nil {
			return nil, err
		}
	}
	if contract.Facets["sync_policy"] || contract.Facets["backup_policy"] || contract.Facets["worker_policy"] || contract.Facets["secrets"] {
		if err := addRendered("policies/README.md", policiesReadmeTemplate, data); err != nil {
			return nil, err
		}
		if err := addRendered("policies/AGENTS.md", policiesAgentsTemplate, data); err != nil {
			return nil, err
		}
	}
	if err := addRendered("tests/README.md", testsReadmeTemplate, data); err != nil {
		return nil, err
	}
	files["tests/AGENTS.md"] = ensureTrailingNewline([]byte(testsAgentsTemplate))
	files["tests/validate_project.sh"] = ensureTrailingNewline([]byte(legacyValidateProjectScriptTemplate))
	return files, nil
}

func planLegacyDirectoryCleanup(plan *LayoutMigrationPlan, contract ProjectContract) error {
	for _, relative := range []string{"policies", "tests"} {
		if relative == "tests" && contract.Facets["tests"] {
			continue
		}
		entries, present, err := migrationDirectoryManifest(plan.ProjectRoot, relative)
		if err != nil || !present {
			if err != nil {
				return err
			}
			continue
		}
		removals := map[string]bool{}
		for _, action := range plan.planned {
			if action.Public.Source != "" && action.Public.Operation != "rewrite_contract" {
				removals[action.Public.Source] = true
			}
		}
		allRemoved := true
		for _, entry := range entries {
			if strings.HasSuffix(entry, "/") {
				allRemoved = false
				break
			}
			if removals[entry] {
				continue
			}
			allRemoved = false
			break
		}
		if !allRemoved {
			continue
		}
		manifestRaw := []byte(strings.Join(entries, "\n"))
		action := LayoutMigrationAction{Operation: "remove_directory", Kind: "legacy_scaffold_directory", Source: relative, BeforeHash: hashBytesURI(manifestRaw), Status: "planned"}
		plan.planned = append(plan.planned, plannedLayoutAction{Public: action, SourceHash: action.BeforeHash})
	}
	return nil
}

func addLayoutWriteAction(plan *LayoutMigrationPlan, operation, kind, source, destination string, before, after []byte, mode fs.FileMode) {
	action := LayoutMigrationAction{Operation: operation, Kind: kind, Source: source, Destination: destination, BeforeHash: hashOptionalBytes(before), AfterHash: hashBytesURI(after), Status: "planned"}
	plan.planned = append(plan.planned, plannedLayoutAction{Public: action, Content: append([]byte{}, after...), Mode: mode, SourceHash: hashOptionalBytes(before), TargetHash: hashOptionalBytes(before)})
}

func addLayoutWriteRemoveAction(plan *LayoutMigrationPlan, operation, kind, source, destination string, before, after []byte, mode fs.FileMode) {
	action := LayoutMigrationAction{Operation: operation, Kind: kind, Source: source, Destination: destination, BeforeHash: hashBytesURI(before), AfterHash: hashBytesURI(after), Status: "planned"}
	plan.planned = append(plan.planned, plannedLayoutAction{Public: action, Content: append([]byte{}, after...), Mode: mode, SourceHash: hashBytesURI(before)})
}

func addLayoutRemoveAction(plan *LayoutMigrationPlan, operation, kind, source string, before []byte) {
	action := LayoutMigrationAction{Operation: operation, Kind: kind, Source: source, BeforeHash: hashBytesURI(before), Status: "planned"}
	plan.planned = append(plan.planned, plannedLayoutAction{Public: action, SourceHash: hashBytesURI(before)})
}

func addLayoutMoveAction(plan *LayoutMigrationPlan, kind, source, destination string, before []byte) {
	action := LayoutMigrationAction{Operation: "move_contract", Kind: kind, Source: source, Destination: destination, BeforeHash: hashBytesURI(before), AfterHash: hashBytesURI(before), Status: "planned"}
	plan.planned = append(plan.planned, plannedLayoutAction{Public: action, SourceHash: action.BeforeHash})
}

func preflightLayoutMigrationPlan(plan LayoutMigrationPlan) error {
	for _, action := range plan.planned {
		if action.Public.Operation == "remove_directory" {
			entries, present, err := migrationDirectoryManifest(plan.ProjectRoot, action.Public.Source)
			if err != nil {
				return err
			}
			if !present || hashBytesURI([]byte(strings.Join(entries, "\n"))) != action.SourceHash {
				return fmt.Errorf("migration directory changed after planning: %s", action.Public.Source)
			}
			continue
		}
		if action.Public.Source != "" {
			raw, present, err := readMigrationFile(plan.ProjectRoot, action.Public.Source)
			if err != nil {
				return err
			}
			if !present || hashBytesURI(raw) != action.SourceHash {
				return fmt.Errorf("migration source changed after planning: %s", action.Public.Source)
			}
		}
		if action.Public.Destination != "" {
			raw, present, err := readMigrationFile(plan.ProjectRoot, action.Public.Destination)
			if err != nil {
				return err
			}
			if action.TargetHash == "" && present {
				return fmt.Errorf("migration destination appeared after planning: %s", action.Public.Destination)
			}
			if action.TargetHash != "" && (!present || hashBytesURI(raw) != action.TargetHash) {
				return fmt.Errorf("migration destination changed after planning: %s", action.Public.Destination)
			}
		}
	}
	return nil
}

func applyLayoutMigrationAction(root string, action plannedLayoutAction) error {
	switch action.Public.Operation {
	case "create", "replace_generated", "rewrite_contract":
		return atomicWriteMigrationFile(root, action.Public.Destination, action.Content, action.Mode)
	case "relocate_contract":
		if err := atomicWriteMigrationFile(root, action.Public.Destination, action.Content, action.Mode); err != nil {
			return err
		}
		return removeMigrationFile(root, action.Public.Source)
	case "move_contract":
		source, err := safeMigrationPath(root, action.Public.Source)
		if err != nil {
			return err
		}
		destination, err := safeMigrationPath(root, action.Public.Destination)
		if err != nil {
			return err
		}
		if err := ensureScaffoldDirectory(root, filepath.Dir(destination)); err != nil {
			return err
		}
		return os.Rename(source, destination)
	case "remove_generated", "remove_redundant":
		return removeMigrationFile(root, action.Public.Source)
	case "remove_directory":
		target, err := safeMigrationPath(root, action.Public.Source)
		if err != nil {
			return err
		}
		return os.Remove(target)
	default:
		return fmt.Errorf("unsupported layout migration operation: %s", action.Public.Operation)
	}
}

func migrationDirectoryManifest(root, relative string) ([]string, bool, error) {
	target, err := safeMigrationPath(root, relative)
	if err != nil {
		return nil, false, err
	}
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("migration directory target is not a directory: %s", relative)
	}
	entries := []string{}
	err = filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == target {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("migration directory contains a symbolic link: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			rel += "/"
		}
		entries = append(entries, rel)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	sort.Strings(entries)
	return entries, true, nil
}

func atomicWriteMigrationFile(root, relative string, content []byte, mode fs.FileMode) error {
	target, err := safeMigrationPath(root, relative)
	if err != nil {
		return err
	}
	if err := ensureScaffoldDirectory(root, filepath.Dir(target)); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".loom-layout-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(scaffoldFileMode(mode)); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, target)
}

func removeMigrationFile(root, relative string) error {
	target, err := safeMigrationPath(root, relative)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("migration removal target is not a regular file: %s", relative)
	}
	return os.Remove(target)
}

func readMigrationFile(root, relative string) ([]byte, bool, error) {
	path, err := safeMigrationPath(root, relative)
	if err != nil {
		return nil, false, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("project layout path is not a regular file: %s", relative)
	}
	raw, err := os.ReadFile(path)
	return raw, err == nil, err
}

func safeMigrationPath(root, relative string) (string, error) {
	target, err := scaffoldTargetPath(root, relative)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(root)
	rel, err := filepath.Rel(current, target)
	if err != nil {
		return "", err
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	for index, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("project layout path contains a symbolic link: %s", filepath.Join(parts[:index+1]...))
		}
	}
	return target, nil
}

func writeLayoutMigrationRecord(result LayoutMigrationResult, applyErr error) (string, error) {
	record := layoutMigrationRecord{
		SchemaVersion: layoutMigrationRecordSchema,
		RecordedAt:    nowUTC(),
		ProjectRoot:   result.ProjectRoot,
		BeforeLayout:  result.BeforeLayout,
		AfterLayout:   result.AfterLayout,
		OK:            result.OK,
		Actions:       result.Actions,
		Collisions:    result.Collisions,
	}
	if applyErr != nil {
		record.Error = applyErr.Error()
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	name := record.RecordedAt.Format("20060102T150405.000000000Z") + ".json"
	relative := filepath.ToSlash(filepath.Join(ProjectMetadataDir, "state", "layout-migrations", name))
	if err := atomicWriteMigrationFile(result.ProjectRoot, relative, raw, 0o600); err != nil {
		return "", err
	}
	return relative, nil
}

func layoutMigrationResultFromPlan(plan LayoutMigrationPlan, dryRun bool) LayoutMigrationResult {
	return LayoutMigrationResult{
		OK:           len(plan.Collisions) == 0,
		DryRun:       dryRun,
		ProjectRoot:  plan.ProjectRoot,
		BeforeLayout: plan.Layout,
		Actions:      append([]LayoutMigrationAction{}, plan.Actions...),
		Collisions:   append([]LayoutMigrationCollision{}, plan.Collisions...),
		Skips:        append([]LayoutMigrationSkip{}, plan.Skips...),
		NextActions:  append([]string{}, plan.NextActions...),
	}
}

func layoutMigrationNextActions(root string) []string {
	return []string{
		"loom project validate " + root,
		"loom project diff " + root,
		"loom project register " + root,
	}
}

func hashOptionalBytes(raw []byte) string {
	if raw == nil {
		return ""
	}
	return hashBytesURI(raw)
}

func ensureTrailingNewline(raw []byte) []byte {
	if bytes.HasSuffix(raw, []byte("\n")) {
		return append([]byte{}, raw...)
	}
	return append(append([]byte{}, raw...), '\n')
}
