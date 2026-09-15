package projectcontracts

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/oklog/ulid/v2"

	"loom.local/loom/internal/fsaccess"
	"loom.local/loom/internal/ids"
)

type ScaffoldOptions struct {
	Mode              string           `json:"mode,omitempty"`
	Name              string           `json:"name"`
	Slug              string           `json:"slug,omitempty"`
	OwnerNode         string           `json:"owner_node,omitempty"`
	Preset            string           `json:"preset,omitempty"`
	Facets            []string         `json:"facets,omitempty"`
	Directory         string           `json:"directory,omitempty"`
	DirectorySource   string           `json:"directory_source,omitempty"`
	BoxDefaultUsed    bool             `json:"box_default_used,omitempty"`
	BoxRoot           string           `json:"box_root,omitempty"`
	BoxProfile        string           `json:"box_profile,omitempty"`
	BoxContractPath   string           `json:"box_contract_path,omitempty"`
	Force             bool             `json:"force,omitempty"`
	DryRun            bool             `json:"dry_run,omitempty"`
	ProjectID         string           `json:"project_id,omitempty"`
	RepositoryMembers []RepoMemberSpec `json:"repository_members,omitempty"`
}

type ScaffoldResult struct {
	ExecutionLocation string                    `json:"execution_location,omitempty"`
	Mode              string                    `json:"mode,omitempty"`
	SourceState       string                    `json:"source_state,omitempty"`
	ContextState      string                    `json:"context_state,omitempty"`
	Registration      *DeclarationResult        `json:"registration,omitempty"`
	OK                bool                      `json:"ok"`
	ProjectRoot       string                    `json:"project_root"`
	ParentDir         string                    `json:"parent_dir"`
	DirSource         string                    `json:"directory_source"`
	BoxDefault        bool                      `json:"box_default_used"`
	BoxRoot           string                    `json:"box_root,omitempty"`
	BoxProfile        string                    `json:"box_profile,omitempty"`
	BoxContract       string                    `json:"box_contract_path,omitempty"`
	ContractPath      string                    `json:"contract_path"`
	Name              string                    `json:"name"`
	Slug              string                    `json:"slug"`
	OwnerNode         string                    `json:"owner_node"`
	ProjectID         string                    `json:"project_id,omitempty"`
	Preset            string                    `json:"preset"`
	Facets            []string                  `json:"facets"`
	RepositoryMembers []RepoMemberSpec          `json:"repository_members,omitempty"`
	Directories       []ScaffoldDirectoryResult `json:"directories,omitempty"`
	Files             []ScaffoldFileResult      `json:"files"`
	Validation        ScaffoldValidationSummary `json:"validation"`
}

type ScaffoldDirectoryResult struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

type ScaffoldFileResult struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Action string `json:"action"`
}

type ScaffoldValidationSummary struct {
	State    string `json:"state"`
	OK       bool   `json:"ok"`
	Errors   int    `json:"errors"`
	Warnings int    `json:"warnings"`
}

const (
	ScaffoldDirectoryCurrent  = "current_directory"
	ScaffoldDirectoryExplicit = "explicit_directory"
	ScaffoldDirectoryBox      = "box_default"

	ScaffoldValidationNotRun      = "not_run"
	ScaffoldValidationPlannedOnly = "planned_only"
	ScaffoldValidationPassed      = "passed"
	ScaffoldValidationFailed      = "failed"
)

func (summary ScaffoldValidationSummary) MarshalJSON() ([]byte, error) {
	state := scaffoldValidationState(summary)
	type scaffoldValidationJSON struct {
		State    string `json:"state"`
		OK       *bool  `json:"ok,omitempty"`
		Errors   int    `json:"errors"`
		Warnings int    `json:"warnings"`
	}
	out := scaffoldValidationJSON{
		State:    state,
		Errors:   summary.Errors,
		Warnings: summary.Warnings,
	}
	if state == ScaffoldValidationPassed || state == ScaffoldValidationFailed {
		ok := summary.OK
		out.OK = &ok
	}
	return json.Marshal(out)
}

type scaffoldFile struct {
	RelativePath string
	Kind         string
	Content      []byte
	Mode         fs.FileMode
}

type scaffoldDirectory struct {
	RelativePath string
}

type scaffoldData struct {
	Name              string
	Slug              string
	OwnerNode         string
	ProjectID         string
	RepositoryMembers []RepoMemberSpec
	Preset            string
	Facets            []string
	FacetList         string
	ProjectProvider   string
	ExampleCapability string
	HasScripts        bool
	HasConnectors     bool
	HasSchedules      bool
	HasDirectEvents   bool
	HasModules        bool
	HasServices       bool
	HasNotes          bool
	HasRepos          bool
	HasSyncPolicy     bool
	HasBackupPolicy   bool
	HasWorkerPolicy   bool
	HasSecrets        bool
}

var nonSlugRunes = regexp.MustCompile(`[^a-z0-9]+`)

func ScaffoldProject(options ScaffoldOptions) (ScaffoldResult, error) {
	if options.Mode == ScaffoldModeDeclaration {
		return scaffoldDeclarationProject(options)
	}
	if options.Mode != "" {
		return ScaffoldResult{}, fmt.Errorf("unsupported scaffold mode: %s", options.Mode)
	}
	options, data, err := normalizeScaffoldOptions(options)
	if err != nil {
		return ScaffoldResult{}, err
	}
	root, err := scaffoldRoot(options.Directory, options.Slug)
	if err != nil {
		return ScaffoldResult{}, err
	}
	if !options.DryRun {
		options, err = assignScaffoldIdentities(root, options)
		if err != nil {
			return ScaffoldResult{}, err
		}
		data.ProjectID = options.ProjectID
		data.RepositoryMembers = append([]RepoMemberSpec{}, options.RepositoryMembers...)
	}
	files, err := scaffoldFiles(data)
	if err != nil {
		return ScaffoldResult{}, err
	}
	result := ScaffoldResult{
		OK:                true,
		ProjectRoot:       root,
		ParentDir:         options.Directory,
		DirSource:         options.DirectorySource,
		BoxDefault:        options.BoxDefaultUsed,
		BoxRoot:           options.BoxRoot,
		BoxProfile:        options.BoxProfile,
		BoxContract:       options.BoxContractPath,
		ContractPath:      filepath.Join(root, filepath.FromSlash(CanonicalRootContractPath)),
		Name:              options.Name,
		Slug:              options.Slug,
		OwnerNode:         options.OwnerNode,
		ProjectID:         options.ProjectID,
		Preset:            options.Preset,
		Facets:            append([]string{}, data.Facets...),
		RepositoryMembers: append([]RepoMemberSpec{}, options.RepositoryMembers...),
		Directories:       []ScaffoldDirectoryResult{},
		Files:             make([]ScaffoldFileResult, 0, len(files)),
	}
	directories := scaffoldDirectories(data.Facets)

	if options.DryRun {
		for _, directory := range directories {
			result.Directories = append(result.Directories, ScaffoldDirectoryResult{Path: directory.RelativePath, Action: "planned"})
		}
		for _, file := range files {
			result.Files = append(result.Files, ScaffoldFileResult{Path: file.RelativePath, Kind: file.Kind, Action: "planned"})
		}
		result.Validation = ScaffoldValidationSummary{State: ScaffoldValidationPlannedOnly}
		if _, err := os.Stat(result.ContractPath); err == nil {
			analysis := Analyze(root)
			result.Validation = scaffoldValidationFromReport(analysis.Report)
			result.OK = analysis.Report.OK
			if !analysis.Report.OK {
				return result, fmt.Errorf("existing project failed validation with %d error(s)", analysis.Report.Summary.Errors)
			}
		} else if err != nil && !os.IsNotExist(err) {
			return result, err
		}
		return result, nil
	}

	if err := preflightScaffoldWrite(root, files, options.Force); err != nil {
		return result, err
	}
	if err := preflightScaffoldDirectories(root, directories); err != nil {
		return result, err
	}
	for _, directory := range directories {
		action, err := writeScaffoldDirectory(root, directory)
		if err != nil {
			return result, err
		}
		result.Directories = append(result.Directories, ScaffoldDirectoryResult{Path: directory.RelativePath, Action: action})
	}
	for _, file := range files {
		action, err := writeScaffoldFile(root, file, options.Force)
		if err != nil {
			return result, err
		}
		result.Files = append(result.Files, ScaffoldFileResult{Path: file.RelativePath, Kind: file.Kind, Action: action})
	}

	analysis := Analyze(root)
	result.Validation = scaffoldValidationFromReport(analysis.Report)
	if !analysis.Report.OK {
		result.OK = false
		return result, fmt.Errorf("generated project failed validation with %d error(s): %s", analysis.Report.Summary.Errors, firstDiagnosticSummary(analysis.Report.Diagnostics))
	}
	return result, nil
}

func scaffoldValidationFromReport(report ValidationReport) ScaffoldValidationSummary {
	state := ScaffoldValidationPassed
	if !report.OK {
		state = ScaffoldValidationFailed
	}
	return ScaffoldValidationSummary{
		State:    state,
		OK:       report.OK,
		Errors:   report.Summary.Errors,
		Warnings: report.Summary.Warnings,
	}
}

func scaffoldValidationState(summary ScaffoldValidationSummary) string {
	switch summary.State {
	case ScaffoldValidationNotRun, ScaffoldValidationPlannedOnly, ScaffoldValidationPassed, ScaffoldValidationFailed:
		return summary.State
	}
	if summary.OK {
		return ScaffoldValidationPassed
	}
	if summary.Errors > 0 || summary.Warnings > 0 {
		return ScaffoldValidationFailed
	}
	return ScaffoldValidationNotRun
}

func firstDiagnosticSummary(diagnostics []Diagnostic) string {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != SeverityError {
			continue
		}
		return diagnostic.Code + " " + diagnostic.File + " " + diagnostic.Field + ": " + diagnostic.Message
	}
	if len(diagnostics) == 0 {
		return "no diagnostics returned"
	}
	diagnostic := diagnostics[0]
	return diagnostic.Code + " " + diagnostic.File + " " + diagnostic.Field + ": " + diagnostic.Message
}

func DeriveProjectSlug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = nonSlugRunes.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	if len(name) > 64 {
		name = strings.TrimRight(name[:64], "-")
	}
	return name
}

func normalizeScaffoldOptions(options ScaffoldOptions) (ScaffoldOptions, scaffoldData, error) {
	options.Name = strings.TrimSpace(options.Name)
	options.OwnerNode = strings.TrimSpace(options.OwnerNode)
	options.Preset = strings.ToLower(strings.TrimSpace(options.Preset))
	options.Slug = strings.TrimSpace(options.Slug)
	options.Directory = strings.TrimSpace(options.Directory)
	options.DirectorySource = strings.TrimSpace(options.DirectorySource)
	options.BoxRoot = strings.TrimSpace(options.BoxRoot)
	options.BoxProfile = strings.TrimSpace(options.BoxProfile)
	options.BoxContractPath = strings.TrimSpace(options.BoxContractPath)
	options.ProjectID = strings.TrimSpace(options.ProjectID)
	rawDirectory := options.Directory
	if options.Name == "" {
		return options, scaffoldData{}, fmt.Errorf("project name is required")
	}
	if options.Slug == "" {
		options.Slug = DeriveProjectSlug(options.Name)
	}
	if !projectSlugPattern.MatchString(options.Slug) {
		return options, scaffoldData{}, fmt.Errorf("project slug must be lowercase URL-safe and 3-64 characters")
	}
	if options.OwnerNode == "" {
		return options, scaffoldData{}, fmt.Errorf("owner node is required")
	}
	if !nodeKeyPattern.MatchString(options.OwnerNode) {
		return options, scaffoldData{}, fmt.Errorf("owner node must be a lowercase node key")
	}
	if options.ProjectID != "" {
		if err := ids.Validate(ids.ProjectPrefix, options.ProjectID); err != nil {
			return options, scaffoldData{}, fmt.Errorf("project ID is invalid: %w", err)
		}
	}
	members, err := normalizeExplicitRepositoryMembers(options.RepositoryMembers, true)
	if err != nil {
		return options, scaffoldData{}, err
	}
	options.RepositoryMembers = members
	if options.Directory == "" {
		options.Directory = "."
	}
	if options.DirectorySource == "" {
		if rawDirectory == "" {
			options.DirectorySource = ScaffoldDirectoryCurrent
		} else {
			options.DirectorySource = ScaffoldDirectoryExplicit
		}
	}
	preset, ok := PresetByName(options.Preset)
	if !ok {
		return options, scaffoldData{}, fmt.Errorf("unsupported project scaffold preset: %s", options.Preset)
	}
	options.Preset = preset.Name
	facets := append([]string{}, preset.Facets...)
	if len(options.Facets) > 0 {
		facets = append([]string{}, options.Facets...)
	}
	normalizedFacets, err := NormalizeFacetList(facets)
	if err != nil {
		return options, scaffoldData{}, err
	}
	if len(options.RepositoryMembers) > 0 && !containsString(normalizedFacets, "repos") {
		return options, scaffoldData{}, fmt.Errorf("explicit repository members require the repos facet")
	}
	data := scaffoldData{
		Name:              options.Name,
		Slug:              options.Slug,
		OwnerNode:         options.OwnerNode,
		ProjectID:         options.ProjectID,
		RepositoryMembers: append([]RepoMemberSpec{}, options.RepositoryMembers...),
		Preset:            options.Preset,
		Facets:            normalizedFacets,
		FacetList:         strings.Join(normalizedFacets, ", "),
		ProjectProvider:   options.OwnerNode + "@" + options.Slug,
		ExampleCapability: options.OwnerNode + "@" + options.Slug + ".hello_world",
		HasScripts:        containsString(normalizedFacets, "scripts"),
		HasConnectors:     containsString(normalizedFacets, "connectors"),
		HasSchedules:      containsString(normalizedFacets, "schedules"),
		HasDirectEvents:   containsString(normalizedFacets, "direct_events"),
		HasModules:        containsString(normalizedFacets, "modules"),
		HasServices:       containsString(normalizedFacets, "services"),
		HasNotes:          containsString(normalizedFacets, "notes"),
		HasRepos:          containsString(normalizedFacets, "repos"),
		HasSyncPolicy:     containsString(normalizedFacets, "sync_policy"),
		HasBackupPolicy:   containsString(normalizedFacets, "backup_policy"),
		HasWorkerPolicy:   containsString(normalizedFacets, "worker_policy"),
		HasSecrets:        containsString(normalizedFacets, "secrets"),
	}
	options.Facets = normalizedFacets
	return options, data, nil
}

func NewRepositoryID() string {
	return RepositoryIDPrefix + "_" + ulid.MustNew(ulid.Timestamp(time.Now().UTC()), rand.Reader).String()
}

func assignScaffoldIdentities(root string, options ScaffoldOptions) (ScaffoldOptions, error) {
	var existingMembers []RepoMemberSpec
	if options.Force {
		hasContract := pathExists(filepath.Join(root, filepath.FromSlash(CanonicalRootContractPath))) || pathExists(filepath.Join(root, filepath.FromSlash(LegacyRootContractPath)))
		if hasContract {
			loaded, loadErr := LoadProject(root)
			if loadErr != nil {
				return options, fmt.Errorf("load existing project identity before forced scaffold: %w", loadErr)
			}
			existing := NormalizeContract(loaded.Contract)
			switch existing.SchemaVersion {
			case ProjectSchemaV04:
				if err := ids.Validate(ids.ProjectPrefix, existing.Project.ID); err != nil {
					return options, fmt.Errorf("forced scaffold cannot replace invalid existing project identity: %w", err)
				}
				if options.ProjectID == "" {
					options.ProjectID = existing.Project.ID
				} else if existing.Project.ID != options.ProjectID {
					return options, fmt.Errorf("forced scaffold cannot replace existing project identity")
				}
			case ProjectSchemaV03:
				return options, fmt.Errorf("forced scaffold cannot reinterpret a v0.3 project; use explicit contract migration to preserve watched-root policy")
			}
			if resolution, resolveErr := ResolveSingletonContract(loaded, ProjectContractRepos, ""); resolveErr == nil && resolution.Present {
				repos, _, reposErr := LoadReposContract(resolution.Path)
				if reposErr != nil {
					return options, fmt.Errorf("load existing repository identities before forced scaffold: %w", reposErr)
				}
				if repos.SchemaVersion == ReposSchemaV04 {
					existingMembers = normalizeReposContract(repos).Repos.Members
				}
			}
		}
	}
	if options.ProjectID == "" {
		options.ProjectID = ids.NewProjectID()
	}
	if options.RepositoryMembers == nil && len(existingMembers) > 0 {
		options.RepositoryMembers = append([]RepoMemberSpec{}, existingMembers...)
	}
	for index := range options.RepositoryMembers {
		if options.RepositoryMembers[index].ID != "" {
			continue
		}
		for _, existing := range existingMembers {
			if existing.Key == options.RepositoryMembers[index].Key || existing.Path == options.RepositoryMembers[index].Path {
				options.RepositoryMembers[index].ID = existing.ID
				break
			}
		}
		if options.RepositoryMembers[index].ID == "" {
			options.RepositoryMembers[index].ID = NewRepositoryID()
		}
	}
	members, err := normalizeExplicitRepositoryMembers(options.RepositoryMembers, false)
	if err != nil {
		return options, err
	}
	options.RepositoryMembers = members
	return options, nil
}

func normalizeExplicitRepositoryMembers(input []RepoMemberSpec, allowMissingIDs bool) ([]RepoMemberSpec, error) {
	if input == nil {
		return nil, nil
	}
	members := append([]RepoMemberSpec{}, input...)
	seenIDs := map[string]bool{}
	seenKeys := map[string]bool{}
	seenPaths := map[string]bool{}
	for index := range members {
		member := &members[index]
		member.ID = strings.TrimSpace(member.ID)
		member.Key = strings.TrimSpace(member.Key)
		member.Role = strings.ToLower(strings.TrimSpace(member.Role))
		member.StateRoot = strings.TrimSpace(member.StateRoot)
		if member.ID == "" {
			if member.Role == RepositoryRoleReference {
				return nil, fmt.Errorf("repository member %d is a non-owning reference and must supply an existing repository ID", index)
			}
			if !allowMissingIDs {
				return nil, fmt.Errorf("repository member %d ID is required", index)
			}
		} else {
			if err := ids.Validate(RepositoryIDPrefix, member.ID); err != nil {
				return nil, fmt.Errorf("repository member %d ID is invalid: %w", index, err)
			}
			if seenIDs[member.ID] {
				return nil, fmt.Errorf("repository member %d duplicates ID %s", index, member.ID)
			}
			seenIDs[member.ID] = true
		}
		if !providerKeyPattern.MatchString(member.Key) {
			return nil, fmt.Errorf("repository member %d key must be lowercase and URL-safe", index)
		}
		if seenKeys[member.Key] {
			return nil, fmt.Errorf("repository member %d duplicates key %s", index, member.Key)
		}
		seenKeys[member.Key] = true
		path, err := normalizeRepositoryMemberPath(member.Path)
		if err != nil {
			return nil, fmt.Errorf("repository member %d path must be safely relative to repos/", index)
		}
		if hasReservedRepositoryPath(path, false) {
			return nil, fmt.Errorf("repository member %d path uses a reserved control name", index)
		}
		member.Path = path
		if seenPaths[path] {
			return nil, fmt.Errorf("repository member %d duplicates path %s", index, path)
		}
		seenPaths[path] = true
		switch member.Role {
		case RepositoryRolePrimary, RepositoryRoleComponent, RepositoryRoleReference:
		default:
			return nil, fmt.Errorf("repository member %d role must be primary, component, or reference", index)
		}
		if member.StateRoot != "" {
			stateRoot, err := normalizeRepositoryStateRoot(member.StateRoot)
			if err != nil {
				return nil, fmt.Errorf("repository member %d state_root must be safely relative to the repository", index)
			}
			if hasReservedRepositoryPath(stateRoot, true) {
				return nil, fmt.Errorf("repository member %d state_root uses a reserved control name", index)
			}
			member.StateRoot = stateRoot
		}
	}
	return members, nil
}

func scaffoldRoot(directory, slug string) (string, error) {
	parent, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(parent, slug)), nil
}

func scaffoldFiles(data scaffoldData) ([]scaffoldFile, error) {
	files := []scaffoldFile{}
	add := func(path, kind, content string, mode fs.FileMode) error {
		path, err := safeScaffoldRelativePath(path)
		if err != nil {
			return err
		}
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		files = append(files, scaffoldFile{RelativePath: path, Kind: kind, Content: []byte(content), Mode: mode})
		return nil
	}
	addRendered := func(path, kind, tmpl string, mode fs.FileMode) error {
		content, err := renderScaffoldTemplate(path, tmpl, data)
		if err != nil {
			return err
		}
		return add(path, kind, content, mode)
	}

	if err := addRendered(CanonicalRootContractPath, "root_contract", rootContractTemplate, 0o644); err != nil {
		return nil, err
	}
	if err := add(".loom/.gitignore", "loom_gitignore", loomGitignoreTemplate, 0o644); err != nil {
		return nil, err
	}
	if err := addRendered("README.md", "root_readme", conciseRootReadmeTemplate, 0o644); err != nil {
		return nil, err
	}
	if err := addRendered("AGENTS.md", "root_agents", conciseRootAgentsTemplate, 0o644); err != nil {
		return nil, err
	}
	if err := addRendered(".loom/agents/project.md", "project_agents", projectAgentsTemplate, 0o644); err != nil {
		return nil, err
	}
	if err := add(".loom/tools/validate-project.sh", "validation_script", validateProjectScriptTemplate, 0o755); err != nil {
		return nil, err
	}
	for _, item := range []struct {
		path    string
		kind    string
		content string
	}{
		{path: ".loom/templates/note.md", kind: "content_template", content: noteContentTemplate},
		{path: ".loom/templates/dated-file.yaml", kind: "content_template", content: datedFileManifestTemplate},
		{path: ".loom/templates/dataset.yaml", kind: "content_template", content: datasetManifestTemplate},
	} {
		if err := add(item.path, item.kind, item.content, 0o644); err != nil {
			return nil, err
		}
	}
	if err := appendRepositoryDevelopmentPack(&files, data); err != nil {
		return nil, err
	}

	for _, facet := range data.Facets {
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

const (
	repositoryDevelopmentPackSourceRoot = "repo_development_pack"
	repositoryDevelopmentPackTargetRoot = ".loom/agent-packs/repo-development"
)

func appendRepositoryDevelopmentPack(files *[]scaffoldFile, data scaffoldData) error {
	return fs.WalkDir(repositoryDevelopmentPackTemplates, repositoryDevelopmentPackSourceRoot, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(sourcePath, repositoryDevelopmentPackSourceRoot+"/")
		if relative == sourcePath || relative == "" {
			return fmt.Errorf("invalid repository development pack source path: %s", sourcePath)
		}
		targetPath, err := safeScaffoldRelativePath(repositoryDevelopmentPackTargetRoot + "/" + relative)
		if err != nil {
			return err
		}
		raw, err := fs.ReadFile(repositoryDevelopmentPackTemplates, sourcePath)
		if err != nil {
			return err
		}
		content, err := renderScaffoldTemplate(targetPath, string(raw), data)
		if err != nil {
			return err
		}
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		*files = append(*files, scaffoldFile{
			RelativePath: targetPath,
			Kind:         "repository_development_pack",
			Content:      []byte(content),
			Mode:         0o644,
		})
		return nil
	})
}

func surfaceScaffoldFile(data scaffoldData, facet string) (scaffoldFile, error) {
	path := ".loom/agents/surfaces/" + facet + ".md"
	content, err := renderScaffoldTemplate(path, surfaceAgentsTemplate, map[string]any{"Facet": facet, "Project": data.Name})
	if err != nil {
		return scaffoldFile{}, err
	}
	return scaffoldFile{RelativePath: path, Kind: "surface_agents", Content: []byte(content), Mode: 0o644}, nil
}

func appendFacetFiles(files *[]scaffoldFile, data scaffoldData, facet string) error {
	add := func(path, kind, content string, mode fs.FileMode) error {
		path, err := safeScaffoldRelativePath(path)
		if err != nil {
			return err
		}
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		*files = append(*files, scaffoldFile{RelativePath: path, Kind: kind, Content: []byte(content), Mode: mode})
		return nil
	}
	addRendered := func(path, kind, tmpl string, mode fs.FileMode) error {
		content, err := renderScaffoldTemplate(path, tmpl, data)
		if err != nil {
			return err
		}
		return add(path, kind, content, mode)
	}

	switch facet {
	case "notes":
		return addRendered(".loom/contracts/notes.yaml", "notes_contract", notesContractTemplate, 0o644)
	case "scripts":
		if err := addRendered("scripts/hello_world/README.md", "script_readme", scriptPackageReadmeTemplate, 0o644); err != nil {
			return err
		}
		if err := add("scripts/hello_world/examples/input.example.json", "script_example", "{\n  \"message\": \"hello\"\n}", 0o644); err != nil {
			return err
		}
		if err := add("scripts/hello_world/run.sh", "script_entrypoint", scriptRunTemplate, 0o755); err != nil {
			return err
		}
		if err := add("scripts/hello_world/loom.script.yaml", "script_manifest", scriptManifestTemplate, 0o644); err != nil {
			return err
		}
		return add("scripts/hello_world/loom.exposure.yaml", "script_exposure", scriptExposureTemplate, 0o644)
	case "workflows":
		if err := add("workflows/example_workflow/examples/input.example.json", "workflow_example", "{\n  \"message\": \"hello\"\n}", 0o644); err != nil {
			return err
		}
		if err := addRendered("workflows/example_workflow/README.md", "workflow_readme", workflowReadmeTemplate, 0o644); err != nil {
			return err
		}
		if err := add("workflows/example_workflow/run.sh", "workflow_entrypoint", workflowRunTemplate, 0o755); err != nil {
			return err
		}
		return addRendered("workflows/example_workflow/loom.workflow.yaml", "workflow_contract", workflowContractTemplate, 0o644)
	case "connectors":
		if err := addRendered("connectors/example_connector/README.md", "connector_readme", connectorReadmeTemplate, 0o644); err != nil {
			return err
		}
		if err := add("connectors/example_connector/capabilities/ping.yaml", "connector_capability", connectorCapabilityTemplate, 0o644); err != nil {
			return err
		}
		if err := addRendered("connectors/example_connector/scripts/ping/README.md", "connector_script_readme", connectorScriptReadmeTemplate, 0o644); err != nil {
			return err
		}
		if err := add("connectors/example_connector/scripts/ping/loom.script.yaml", "connector_script_manifest", connectorScriptManifestTemplate, 0o644); err != nil {
			return err
		}
		if err := add("connectors/example_connector/scripts/ping/run.sh", "connector_script", connectorPingTemplate, 0o755); err != nil {
			return err
		}
		if err := add("connectors/example_connector/examples/input.example.json", "connector_example", "{\n  \"message\": \"ping\"\n}", 0o644); err != nil {
			return err
		}
		return add("connectors/example_connector/loom.connector.yaml", "connector_contract", connectorContractTemplate, 0o644)
	case "schedules":
		if err := add("schedules/example_schedule/input.example.json", "schedule_input", "{\n  \"message\": \"scheduled run\"\n}", 0o644); err != nil {
			return err
		}
		return addRendered("schedules/example_schedule/loom.schedule.yaml", "schedule_contract", scheduleContractTemplate, 0o644)
	case "direct_events":
		if err := add("direct_events/example_event/examples/payload.json", "direct_event_example", "{\n  \"id\": \"example-1\",\n  \"message\": \"hello\"\n}", 0o644); err != nil {
			return err
		}
		if err := add("direct_events/example_event/examples/expected_mapped_input.json", "direct_event_example", "{\n  \"message\": \"hello\"\n}", 0o644); err != nil {
			return err
		}
		return addRendered("direct_events/example_event/loom.direct_event.yaml", "direct_event_contract", directEventContractTemplate, 0o644)
	case "modules":
		if err := addRendered("modules/example_module/README.md", "module_readme", moduleReadmeTemplate, 0o644); err != nil {
			return err
		}
		if err := addRendered("modules/example_module/module.json", "module_manifest", moduleManifestTemplate, 0o644); err != nil {
			return err
		}
		return addRendered("modules/example_module/loom.module_project.yaml", "module_project_contract", moduleProjectContractTemplate, 0o644)
	case "services":
		return addRendered(".loom/contracts/services/example.yaml", "service_contract", serviceRegistrationContractTemplate, 0o644)
	case "sync_policy", "backup_policy", "worker_policy":
		return appendPolicyFiles(files, data)
	case "repos":
		return addRendered(".loom/contracts/repos.yaml", "repos_contract", reposContractTemplate, 0o644)
	case "datasets", "docs", "tests", "portal":
		return nil
	case "secrets":
		return appendPolicyFiles(files, data)
	default:
		return nil
	}
}

func appendSimpleFacet(files *[]scaffoldFile, data scaffoldData, folder, title string) error {
	return appendSimpleFacetWithAgents(files, data, folder, title, simpleFacetAgentsTemplate)
}

func appendSimpleFacetWithAgents(files *[]scaffoldFile, data scaffoldData, folder, title, agentsTemplate string) error {
	return appendSimpleFacetWithTemplates(files, data, folder, title, simpleFacetReadmeTemplate, agentsTemplate)
}

func appendSimpleFacetWithTemplates(files *[]scaffoldFile, data scaffoldData, folder, title, readmeTemplate, agentsTemplate string) error {
	readme, err := renderScaffoldTemplate(folder+"/README.md", readmeTemplate, map[string]any{"Title": title, "Folder": folder, "Project": data.Name, "Slug": data.Slug})
	if err != nil {
		return err
	}
	agents, err := renderScaffoldTemplate(folder+"/AGENTS.md", agentsTemplate, data)
	if err != nil {
		return err
	}
	for _, file := range []scaffoldFile{
		{RelativePath: folder + "/README.md", Kind: "facet_readme", Content: []byte(readme + "\n"), Mode: 0o644},
		{RelativePath: folder + "/AGENTS.md", Kind: "facet_agents", Content: []byte(agents + "\n"), Mode: 0o644},
	} {
		*files = append(*files, file)
	}
	return nil
}

func appendPolicyFiles(files *[]scaffoldFile, data scaffoldData) error {
	add := func(path, kind, content string) {
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		*files = append(*files, scaffoldFile{RelativePath: path, Kind: kind, Content: []byte(content), Mode: 0o644})
	}
	addRendered := func(path, kind, tmpl string) error {
		content, err := renderScaffoldTemplate(path, tmpl, data)
		if err != nil {
			return err
		}
		add(path, kind, content)
		return nil
	}
	if data.HasSyncPolicy && !hasScaffoldFile(*files, ".loom/contracts/sync.yaml") {
		add(".loom/contracts/sync.yaml", "sync_policy", syncPolicyTemplate)
	}
	if data.HasBackupPolicy && !hasScaffoldFile(*files, ".loom/contracts/backup.yaml") {
		if err := addRendered(".loom/contracts/backup.yaml", "backup_policy", backupPolicyTemplate); err != nil {
			return err
		}
	}
	if data.HasWorkerPolicy && !hasScaffoldFile(*files, ".loom/contracts/workers.yaml") {
		add(".loom/contracts/workers.yaml", "worker_policy", workerPolicyTemplate)
	}
	if data.HasSecrets && !hasScaffoldFile(*files, ".loom/contracts/credentials.yaml") {
		add(".loom/contracts/credentials.yaml", "credentials_policy", credentialsPolicyTemplate)
	}
	return nil
}

func scaffoldDirectories(facets []string) []scaffoldDirectory {
	directories := []scaffoldDirectory{}
	for _, facet := range facets {
		switch facet {
		case "notes", "repos", "scripts", "workflows", "connectors", "schedules", "direct_events", "modules", "datasets", "docs", "tests", "secrets":
			directories = append(directories, scaffoldDirectory{RelativePath: facet})
		case "services":
			directories = append(directories, scaffoldDirectory{RelativePath: ".loom/contracts/services"})
		}
	}
	sort.SliceStable(directories, func(i, j int) bool { return directories[i].RelativePath < directories[j].RelativePath })
	return directories
}

func writeScaffoldDirectory(root string, directory scaffoldDirectory) (string, error) {
	target, err := scaffoldTargetPath(root, directory.RelativePath)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(target); err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("scaffold directory path is not a directory: %s", target)
		}
		return "existing", ensureScaffoldDirectory(root, target)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := ensureScaffoldDirectory(root, target); err != nil {
		return "", err
	}
	return "created", nil
}

func preflightScaffoldWrite(root string, files []scaffoldFile, force bool) error {
	for _, file := range files {
		target, err := scaffoldTargetPath(root, file.RelativePath)
		if err != nil {
			return err
		}
		if _, err := os.Stat(target); err == nil && !force {
			return fmt.Errorf("refusing to overwrite existing file without --force: %s", target)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func preflightScaffoldDirectories(root string, directories []scaffoldDirectory) error {
	for _, directory := range directories {
		target, err := scaffoldTargetPath(root, directory.RelativePath)
		if err != nil {
			return err
		}
		if info, err := os.Stat(target); err == nil && !info.IsDir() {
			return fmt.Errorf("scaffold directory path is not a directory: %s", target)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func hasScaffoldFile(files []scaffoldFile, path string) bool {
	for _, file := range files {
		if file.RelativePath == path {
			return true
		}
	}
	return false
}

func renderScaffoldTemplate(name, source string, data any) (string, error) {
	tmpl, err := template.New(name).Funcs(template.FuncMap{
		"join": strings.Join,
	}).Parse(source)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return "", err
	}
	return strings.TrimRight(out.String(), "\n") + "\n", nil
}

func safeScaffoldRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("scaffold path is required")
	}
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("scaffold path must be relative: %s", value)
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("scaffold path escapes project root: %s", value)
	}
	return clean, nil
}

func writeScaffoldFile(root string, file scaffoldFile, force bool) (string, error) {
	cleanTarget, err := scaffoldTargetPath(root, file.RelativePath)
	if err != nil {
		return "", err
	}
	if err := ensureScaffoldDirectory(root, filepath.Dir(cleanTarget)); err != nil {
		return "", err
	}
	action := "created"
	if _, err := os.Stat(cleanTarget); err == nil {
		if !force {
			return "", fmt.Errorf("refusing to overwrite existing file without --force: %s", cleanTarget)
		}
		action = "overwritten"
	} else if !os.IsNotExist(err) {
		return "", err
	}
	mode := scaffoldFileMode(file.Mode)
	if err := os.WriteFile(cleanTarget, file.Content, mode); err != nil {
		return "", err
	}
	if err := os.Chmod(cleanTarget, mode); err != nil {
		return "", err
	}
	return action, nil
}

func ensureScaffoldDirectory(root, dir string) error {
	return ensureScaffoldDirectoryWithChmod(root, dir, os.Chmod)
}

func ensureScaffoldDirectoryWithChmod(root, dir string, chmod func(string, fs.FileMode) error) error {
	cleanRoot := filepath.Clean(root)
	cleanDir := filepath.Clean(dir)
	if cleanDir != cleanRoot && !strings.HasPrefix(cleanDir, cleanRoot+string(os.PathSeparator)) {
		return fmt.Errorf("scaffold directory escapes project root: %s", dir)
	}
	if err := os.MkdirAll(cleanDir, scaffoldDirectoryMode()); err != nil {
		return err
	}
	rel, err := filepath.Rel(cleanRoot, cleanDir)
	if err != nil {
		return err
	}
	if rel == "." {
		return ensureScaffoldDirectoryMode(cleanRoot, chmod)
	}
	current := cleanRoot
	if err := ensureScaffoldDirectoryMode(current, chmod); err != nil {
		return err
	}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		if err := ensureScaffoldDirectoryMode(current, chmod); err != nil {
			return err
		}
	}
	return nil
}

func ensureScaffoldDirectoryMode(path string, chmod func(string, fs.FileMode) error) error {
	err := chmod(path, scaffoldDirectoryMode())
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	access := fsaccess.Check(path, fsaccess.Write, fsaccess.Execute)
	if access.OK {
		return nil
	}
	return err
}

func scaffoldDirectoryMode() fs.FileMode {
	return 0o2775
}

func scaffoldFileMode(mode fs.FileMode) fs.FileMode {
	normalized := mode
	if normalized&0o200 != 0 && normalized&0o040 != 0 {
		normalized |= 0o020
	}
	if normalized&0o100 != 0 && normalized&0o040 != 0 {
		normalized |= 0o010
	}
	return normalized
}

func scaffoldTargetPath(root, relativePath string) (string, error) {
	relative, err := safeScaffoldRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	cleanRoot := filepath.Clean(root)
	cleanTarget := filepath.Clean(filepath.Join(cleanRoot, filepath.FromSlash(relative)))
	if cleanTarget != cleanRoot && !strings.HasPrefix(cleanTarget, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("scaffold path escapes project root: %s", relativePath)
	}
	return cleanTarget, nil
}
