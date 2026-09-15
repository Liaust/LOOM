package projectcontracts

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"gopkg.in/yaml.v3"
	"loom.local/loom/internal/ids"
)

const ScaffoldModeDeclaration = "declaration"

// The normal constructor uses the existing scaffold service and its checked
// publication primitives. No legacy preset or facet defaults are changed.
func scaffoldDeclarationProject(input ScaffoldOptions) (result ScaffoldResult, err error) {
	defer func() {
		if input.DryRun {
			return
		}
		for _, file := range result.Files {
			if file.Path == CanonicalRootContractPath && (file.Action == "created" || file.Action == "existing") && result.ProjectID != "" {
				result.SourceState, result.ContextState = "source_created", "source_only"
			}
		}
	}()
	// The existing backend normalizer supplies PresetMinimal when omitted.
	// Declaration mode overrides that compatibility default only.
	if (input.Preset != "" && input.Preset != PresetMinimal) || len(input.Facets) > 0 || len(input.RepositoryMembers) > 0 || input.Force || input.ProjectID != "" {
		return result, fmt.Errorf("normal create does not accept presets, facets, forced replacement or supplied identities")
	}
	options, _, err := normalizeScaffoldOptions(input)
	if err != nil {
		return result, err
	}
	root, err := scaffoldRoot(options.Directory, options.Slug)
	if err != nil {
		return result, err
	}
	result = ScaffoldResult{OK: false, Mode: ScaffoldModeDeclaration, ProjectRoot: root, ParentDir: options.Directory, DirSource: options.DirectorySource, BoxDefault: options.BoxDefaultUsed, BoxRoot: options.BoxRoot, BoxProfile: options.BoxProfile, BoxContract: options.BoxContractPath, ContractPath: filepath.Join(root, CanonicalRootContractPath), Name: options.Name, Slug: options.Slug, OwnerNode: options.OwnerNode, Facets: []string{}, Files: []ScaffoldFileResult{}, Directories: []ScaffoldDirectoryResult{}, Validation: ScaffoldValidationSummary{State: ScaffoldValidationNotRun}}
	// Resolve the caller/backend's explicitly selected parent, not owner_node.
	// A physical parent also avoids macOS /var -> /private/var aliases becoming
	// different project locations. Do not create missing parent directories.
	parent, err := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil {
		return result, err
	}
	info, err := os.Stat(parent)
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, fmt.Errorf("project parent is not a directory")
	}
	root = filepath.Join(parent, options.Slug)
	result.ProjectRoot = root
	result.ParentDir = parent
	result.ContractPath = filepath.Join(root, CanonicalRootContractPath)
	temporary, err := os.MkdirTemp("", "loom-project-create-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(temporary)
	candidate := &scaffoldCandidate{root: temporary, source: root, observations: map[string]scaffoldSourceState{}, packages: map[string]bool{}}
	for _, p := range []string{".", ".loom", "notes", "repos"} {
		state, err := candidate.observe(p, false)
		if err != nil {
			return result, err
		}
		if state.info != nil && !state.info.IsDir() {
			return result, fmt.Errorf("create directory conflicts with existing path: %s", p)
		}
	}
	canonical, err := candidate.observe(CanonicalRootContractPath, true)
	if err != nil {
		return result, err
	}
	legacy, err := candidate.observe(LegacyRootContractPath, true)
	if err != nil {
		return result, err
	}
	if legacy.info != nil {
		return result, fmt.Errorf("create conflicts with an existing legacy project source")
	}
	var raw []byte
	if canonical.info != nil {
		d, err := ParseProjectDeclaration(canonical.raw)
		if err != nil {
			return result, fmt.Errorf("create conflicts with existing project source; source preserved: %w", err)
		}
		if d.Project.Name != options.Name || d.Project.Slug != options.Slug || d.Project.OwnerNode != options.OwnerNode {
			return result, fmt.Errorf("create conflicts with existing project identity; source preserved")
		}
		// Creation is a replay of the constructor, not a declaration update.
		if len(d.Resources) > 0 || (d.Project.Status != "" && d.Project.Status != ProjectStatusDraft) {
			return result, fmt.Errorf("create conflicts with an evolved declaration; source preserved")
		}
		result.ProjectID = d.Project.ID
		raw = canonical.raw
		result.Files = append(result.Files, ScaffoldFileResult{Path: CanonicalRootContractPath, Kind: "root_contract", Action: "existing"})
	} else {
		if input.DryRun {
			// A dry-run has no persistent identity and must not claim source validation.
			result.Files = append(result.Files, ScaffoldFileResult{Path: CanonicalRootContractPath, Kind: "root_contract", Action: "planned"})
		} else {
			result.ProjectID = ids.NewProjectID()
			d := ProjectDeclaration{Kind: ProjectKind, SchemaVersion: ProjectSchemaV05, Project: ProjectSpec{ID: result.ProjectID, Name: options.Name, Slug: options.Slug, OwnerNode: options.OwnerNode}, Resources: map[ResourceKey]ResourceDeclaration{}}
			raw, err = yaml.Marshal(d)
			if err != nil {
				return result, err
			}
			if _, err := ParseProjectDeclaration(raw); err != nil {
				return result, err
			}
			candidate.files = []scaffoldFile{{RelativePath: CanonicalRootContractPath, Kind: "root_contract", Content: raw, Mode: 0664}}
			result.Files = append(result.Files, ScaffoldFileResult{Path: CanonicalRootContractPath, Kind: "root_contract", Action: "planned"})
		}
	}
	if err := candidate.prepareProjectDevelopment(scaffoldData{Name: options.Name, Slug: options.Slug, OwnerNode: options.OwnerNode, ProjectID: result.ProjectID}, &result); err != nil {
		return result, err
	}
	for _, p := range []string{"notes", "repos"} {
		action := "planned"
		if candidate.observations[p].info != nil {
			action = "existing"
		}
		result.Directories = append(result.Directories, ScaffoldDirectoryResult{Path: p, Action: action})
		candidate.directories = append(candidate.directories, ScaffoldDirectoryResult{Path: p, Action: action})
	}
	if input.DryRun {
		result.OK = true
		result.Validation = ScaffoldValidationSummary{State: ScaffoldValidationPlannedOnly}
		return result, nil
	}
	if err := candidate.writeTemporary(scaffoldFile{RelativePath: CanonicalRootContractPath, Content: raw, Mode: 0600}); err != nil {
		return result, err
	}
	candidate.loaded, err = LoadProject(temporary)
	if err != nil {
		return result, err
	}
	candidate.report = candidate.validate()
	if !candidate.report.OK {
		result.Validation = scaffoldValidationFromReport(candidate.report)
		return result, fmt.Errorf("create candidate failed validation")
	}
	published := AddProjectFacetsResult{Files: append([]ScaffoldFileResult{}, result.Files...), Directories: append([]ScaffoldDirectoryResult{}, result.Directories...)}
	err = candidate.publish(&published, scaffoldCandidateHooks{})
	result.Files = published.Files
	result.Directories = published.Directories
	if err != nil {
		return result, err
	}
	// Re-read the published bytes and identity, retaining truthful partial effects
	// if another writer interfered after publication.
	loaded, err := LoadProject(root)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(loaded.Declaration, candidate.loaded.Declaration) || string(loaded.Raw) != string(raw) {
		return result, fmt.Errorf("project source changed during publication; completed files are retained")
	}
	report := Validate(loaded)
	result.Validation = scaffoldValidationFromReport(report)
	result.OK = report.OK
	if !result.OK {
		return result, fmt.Errorf("published project failed validation")
	}
	return result, nil
}
