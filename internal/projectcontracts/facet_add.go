package projectcontracts

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type AddProjectFacetsOptions struct {
	ProjectRef            string   `json:"project_ref,omitempty"`
	ProjectRoot           string   `json:"project_root,omitempty"`
	Facets                []string `json:"facets"`
	Force                 bool     `json:"force,omitempty"`
	DryRun                bool     `json:"dry_run,omitempty"`
	BootstrapMissing      bool     `json:"-"`
	BootstrapName         string   `json:"-"`
	BootstrapSlug         string   `json:"-"`
	BootstrapOwnerNodeKey string   `json:"-"`
}

type AddProjectFacetsResult struct {
	OK              bool                      `json:"ok"`
	DryRun          bool                      `json:"dry_run,omitempty"`
	ProjectRoot     string                    `json:"project_root"`
	ContractPath    string                    `json:"contract_path"`
	Slug            string                    `json:"slug"`
	Name            string                    `json:"name"`
	OwnerNode       string                    `json:"owner_node"`
	RequestedFacets []string                  `json:"requested_facets"`
	AddedFacets     []string                  `json:"added_facets,omitempty"`
	ExistingFacets  []string                  `json:"existing_facets,omitempty"`
	Facets          []string                  `json:"facets"`
	Directories     []ScaffoldDirectoryResult `json:"directories,omitempty"`
	Files           []ScaffoldFileResult      `json:"files"`
	Validation      ScaffoldValidationSummary `json:"validation"`
}

func AddProjectFacets(options AddProjectFacetsOptions) (AddProjectFacetsResult, error) {
	return addProjectFacets(options, scaffoldCandidateHooks{})
}

func addProjectFacets(options AddProjectFacetsOptions, hooks scaffoldCandidateHooks) (result AddProjectFacetsResult, returnedErr error) {
	defer func() {
		if returnedErr != nil {
			result.OK = false
		}
	}()
	requested, err := NormalizeFacetList(options.Facets)
	if err != nil {
		return AddProjectFacetsResult{}, err
	}
	if len(requested) == 0 {
		return AddProjectFacetsResult{}, fmt.Errorf("at least one facet is required")
	}
	loaded, err := LoadProject(options.ProjectRoot)
	if err != nil {
		if options.BootstrapMissing && (errors.Is(err, os.ErrNotExist) || projectFacetDeclarationsMissing(options.ProjectRoot)) {
			return bootstrapProjectFacetRoot(options, requested, hooks)
		}
		return AddProjectFacetsResult{}, err
	}
	contract := NormalizeContract(loaded.Contract)
	facets := make(map[string]bool, len(contract.Facets))
	for key, enabled := range contract.Facets {
		facets[key] = enabled
	}
	contract.Facets = facets
	currentEnabled := enabledContractFacets(contract.Facets)
	merged, err := NormalizeFacetList(append(currentEnabled, requested...))
	if err != nil {
		return AddProjectFacetsResult{}, err
	}
	result = AddProjectFacetsResult{
		OK:              true,
		DryRun:          options.DryRun,
		ProjectRoot:     loaded.RootPath,
		ContractPath:    loaded.ContractPath,
		Slug:            contract.Project.Slug,
		Name:            contract.Project.Name,
		OwnerNode:       contract.Project.OwnerNode,
		RequestedFacets: requested,
		Facets:          merged,
		Directories:     []ScaffoldDirectoryResult{},
		Files:           []ScaffoldFileResult{},
	}

	alreadyEnabled := map[string]bool{}
	for _, facet := range currentEnabled {
		alreadyEnabled[facet] = true
	}
	for _, facet := range requested {
		if alreadyEnabled[facet] {
			result.ExistingFacets = append(result.ExistingFacets, facet)
		} else {
			result.AddedFacets = append(result.AddedFacets, facet)
		}
		contract.Facets[facet] = true
	}

	data, err := scaffoldDataFromContract(contract, merged)
	if err != nil {
		return result, err
	}
	files, err := facetAddFiles(data, requested)
	if err != nil {
		return result, err
	}
	if err := appendPolicyFiles(&files, data); err != nil {
		return result, err
	}
	for _, entry := range []struct{ path, kind, template string }{
		{"README.md", "root_readme", conciseRootReadmeTemplate},
		{"AGENTS.md", "root_agents", conciseRootAgentsTemplate},
		{".loom/agents/project.md", "project_agents", projectAgentsTemplate},
	} {
		content, err := renderScaffoldTemplate(entry.path, entry.template, data)
		if err != nil {
			return result, err
		}
		files = append(files, scaffoldFile{RelativePath: entry.path, Kind: entry.kind, Content: []byte(content), Mode: 0o644})
	}
	files, err = resolveFacetAddContractPaths(loaded, contract, files)
	if err != nil {
		return result, err
	}

	contractRelativePath, err := filepath.Rel(loaded.RootPath, loaded.ContractPath)
	if err != nil {
		return result, err
	}
	contractFile, _, err := projectContractScaffoldFile(contract, loaded.Raw, filepath.ToSlash(contractRelativePath))
	if err != nil {
		return result, err
	}
	files = append(files, contractFile)
	if loaded.Layout == ProjectLayoutCanonicalWithLegacy {
		legacy := contractFile
		legacy.RelativePath = LegacyRootContractPath
		files = append(files, legacy)
	}
	sort.SliceStable(files, func(i, j int) bool {
		if (files[i].Kind == "root_contract") != (files[j].Kind == "root_contract") {
			return files[j].Kind == "root_contract"
		}
		return files[i].RelativePath < files[j].RelativePath
	})
	candidate, err := prepareScaffoldCandidate(loaded, data, files, requested)
	if err != nil {
		return result, err
	}
	defer func() {
		if err := os.RemoveAll(candidate.root); err != nil && returnedErr == nil {
			returnedErr = fmt.Errorf("remove owned candidate directory: %w", err)
		}
	}()
	if err := executeScaffoldCandidate(candidate, &result, hooks); err != nil {
		return result, err
	}

	return result, nil
}

func executeScaffoldCandidate(candidate *scaffoldCandidate, result *AddProjectFacetsResult, hooks scaffoldCandidateHooks) error {
	result.Files = append(result.Files, candidate.actions...)
	result.Directories = candidate.directories
	if hooks.beforeValidation != nil {
		if err := hooks.beforeValidation(candidate.root); err != nil {
			return err
		}
	}
	// Reload after preparation so the same ordinary parser sees exactly the
	// candidate source that is validated in both preview and apply.
	var err error
	candidate.loaded, err = LoadProject(candidate.root)
	if err != nil {
		return err
	}
	candidate.loaded.candidateSources = &scaffoldCandidateSources{root: candidate.source, packages: candidate.packages, published: map[string]bool{}}
	candidate.report = candidate.validate()
	result.Validation = scaffoldValidationFromReport(candidate.report)
	if !candidate.report.OK {
		return fmt.Errorf("project candidate failed validation before writes; existing customized policy/package content was preserved: %s", strings.ReplaceAll(firstDiagnosticSummary(candidate.report.Diagnostics), candidate.root, candidate.source))
	}
	if result.DryRun {
		return nil
	}
	if err := candidate.publish(result, hooks); err != nil {
		return err
	}
	analysis := Analyze(candidate.source)
	result.Validation = scaffoldValidationFromReport(analysis.Report)
	if !analysis.Report.OK {
		return fmt.Errorf("published project changed or failed final validation: %s", firstDiagnosticSummary(analysis.Report.Diagnostics))
	}

	return nil
}

func projectFacetDeclarationsMissing(root string) bool {
	for _, relative := range []string{CanonicalRootContractPath, LegacyRootContractPath} {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative))); !os.IsNotExist(err) {
			return false
		}
	}
	return true
}

func bootstrapProjectFacetRoot(options AddProjectFacetsOptions, requested []string, hooks scaffoldCandidateHooks) (result AddProjectFacetsResult, returnedErr error) {
	defer func() {
		if returnedErr != nil {
			result.OK = false
		}
	}()
	root := filepath.Clean(strings.TrimSpace(options.ProjectRoot))
	if root == "" || root == "." {
		return AddProjectFacetsResult{}, fmt.Errorf("project root is required")
	}
	slug := strings.TrimSpace(options.BootstrapSlug)
	if slug == "" {
		slug = strings.TrimSpace(options.ProjectRef)
	}
	if slug == "" {
		slug = filepath.Base(root)
	}
	name := strings.TrimSpace(options.BootstrapName)
	if name == "" {
		name = slug
	}
	ownerNode := strings.TrimSpace(options.BootstrapOwnerNodeKey)
	if ownerNode == "" {
		ownerNode = "main"
	}
	scaffoldOptions, data, err := normalizeScaffoldOptions(ScaffoldOptions{Name: name, Slug: slug, OwnerNode: ownerNode, Directory: filepath.Dir(root), Facets: requested})
	if err != nil {
		return result, err
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return result, err
	}
	expectedRoot, err := scaffoldRoot(scaffoldOptions.Directory, scaffoldOptions.Slug)
	if err != nil {
		return result, err
	}
	if absoluteRoot != expectedRoot {
		return result, fmt.Errorf("bootstrap project root mismatch: got %s want %s", expectedRoot, absoluteRoot)
	}
	// Preview gets an ephemeral identity solely for full schema validation; it is
	// never persisted. Apply prepares its own identity in the same candidate path.
	scaffoldOptions, err = assignScaffoldIdentities(absoluteRoot, scaffoldOptions)
	if err != nil {
		return result, err
	}
	data.ProjectID = scaffoldOptions.ProjectID
	files, err := scaffoldFiles(data)
	if err != nil {
		return result, err
	}
	sort.SliceStable(files, func(i, j int) bool {
		if (files[i].Kind == "root_contract") != (files[j].Kind == "root_contract") {
			return files[j].Kind == "root_contract"
		}
		return files[i].RelativePath < files[j].RelativePath
	})
	var contract ProjectContract
	for _, file := range files {
		if file.Kind == "root_contract" {
			contract, err = parseProjectContract(file.Content, "", file.RelativePath)
			if err != nil {
				return result, err
			}
		}
	}
	candidate, err := prepareScaffoldCandidate(LoadedProject{RootPath: absoluteRoot, Contract: contract}, data, files, requested)
	if err != nil {
		return result, err
	}
	defer func() {
		if err := os.RemoveAll(candidate.root); err != nil && returnedErr == nil {
			returnedErr = fmt.Errorf("remove owned bootstrap candidate: %w", err)
		}
	}()
	result = AddProjectFacetsResult{OK: true, DryRun: options.DryRun, ProjectRoot: absoluteRoot, ContractPath: filepath.Join(absoluteRoot, CanonicalRootContractPath), Slug: data.Slug, Name: data.Name, OwnerNode: data.OwnerNode, RequestedFacets: requested, AddedFacets: requested, Facets: data.Facets}
	if err := executeScaffoldCandidate(candidate, &result, hooks); err != nil {
		return result, err
	}
	return result, nil
}

func enabledContractFacets(facets map[string]bool) []string {
	values := []string{}
	for key, enabled := range facets {
		if !enabled {
			continue
		}
		values = append(values, key)
	}
	normalized, err := NormalizeFacetList(values)
	if err != nil {
		sort.Strings(values)
		return values
	}
	return normalized
}

func scaffoldDataFromContract(contract ProjectContract, facets []string) (scaffoldData, error) {
	_, data, err := normalizeScaffoldOptions(ScaffoldOptions{
		Name:      contract.Project.Name,
		Slug:      contract.Project.Slug,
		OwnerNode: contract.Project.OwnerNode,
		ProjectID: contract.Project.ID,
		Preset:    PresetMinimal,
		Directory: ".",
		Facets:    facets,
	})
	return data, err
}

func facetAddFiles(data scaffoldData, facets []string) ([]scaffoldFile, error) {
	files := []scaffoldFile{}
	for _, facet := range facets {
		surface, err := surfaceScaffoldFile(data, facet)
		if err != nil {
			return nil, err
		}
		files = append(files, surface)
		if err := appendFacetFiles(&files, data, facet); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].RelativePath < files[j].RelativePath
	})
	return files, nil
}

func resolveFacetAddContractPaths(loaded LoadedProject, contract ProjectContract, files []scaffoldFile) ([]scaffoldFile, error) {
	out := append([]scaffoldFile{}, files...)
	for index := range out {
		kind := ""
		explicit := ""
		switch out[index].Kind {
		case "notes_contract":
			kind = ProjectContractNotes
		case "repos_contract":
			kind = ProjectContractRepos
		case "sync_policy":
			kind, explicit = ProjectContractSync, contract.Policies.Sync
		case "backup_policy":
			kind, explicit = ProjectContractBackup, contract.Policies.Backup
		case "worker_policy":
			kind, explicit = ProjectContractWorkers, contract.Policies.Workers
		case "credentials_policy":
			kind, explicit = ProjectContractCredentials, contract.Policies.Credentials
		}
		if kind == "" {
			continue
		}
		resolution, err := ResolveSingletonContract(loaded, kind, explicit)
		if err != nil {
			return nil, err
		}
		out[index].RelativePath = resolution.RelativePath
	}
	return out, nil
}

func projectContractScaffoldFile(contract ProjectContract, previous []byte, relativePath string) (scaffoldFile, bool, error) {
	relativePath, err := safeScaffoldRelativePath(relativePath)
	if err != nil {
		return scaffoldFile{}, false, err
	}
	raw, err := yaml.Marshal(contract)
	if err != nil {
		return scaffoldFile{}, false, err
	}
	if !bytes.HasSuffix(raw, []byte("\n")) {
		raw = append(raw, '\n')
	}
	changed := !bytes.Equal(bytes.TrimSpace(previous), bytes.TrimSpace(raw))
	return scaffoldFile{RelativePath: relativePath, Kind: "root_contract", Content: raw, Mode: 0o644}, changed, nil
}

func applyScaffoldDirectory(root string, directory scaffoldDirectory, dryRun bool) (ScaffoldDirectoryResult, error) {
	result := ScaffoldDirectoryResult{Path: directory.RelativePath}
	target, err := scaffoldTargetPath(root, directory.RelativePath)
	if err != nil {
		return result, err
	}
	info, err := os.Stat(target)
	if err == nil {
		if !info.IsDir() {
			return result, fmt.Errorf("scaffold directory path is not a directory: %s", target)
		}
		result.Action = "existing"
		return result, nil
	}
	if !os.IsNotExist(err) {
		return result, err
	}
	if dryRun {
		result.Action = "planned"
		return result, nil
	}
	action, err := writeScaffoldDirectory(root, directory)
	result.Action = action
	return result, err
}

func hasFacetAddChanges(files []ScaffoldFileResult) bool {
	for _, file := range files {
		switch strings.TrimSpace(file.Action) {
		case "created", "updated", "overwritten", "planned", "planned_update", "planned_overwrite":
			return true
		}
	}
	return false
}
