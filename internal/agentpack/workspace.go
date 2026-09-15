package agentpack

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type WorkspaceActionKind string

const (
	WorkspaceCreateDirectory    WorkspaceActionKind = "create-directory"
	WorkspaceUnchangedDirectory WorkspaceActionKind = "unchanged-directory"
	WorkspaceCreateFile         WorkspaceActionKind = "create-file"
	WorkspaceUnchangedFile      WorkspaceActionKind = "unchanged-file"
	WorkspacePreserveUserFile   WorkspaceActionKind = "preserve-user-file"
	WorkspaceConflict           WorkspaceActionKind = "conflict-non-directory"
)

type WorkspaceOptions struct {
	Template string
	Path     string
}

type WorkspaceAction struct {
	Kind            WorkspaceActionKind `json:"kind"`
	RelativePath    string              `json:"relative_path"`
	SourcePath      string              `json:"source_path,omitempty"`
	DestinationPath string              `json:"destination_path"`
}

type WorkspacePlan struct {
	PackName    string            `json:"pack_name"`
	PackVersion string            `json:"pack_version"`
	Template    string            `json:"template"`
	Path        string            `json:"path"`
	Actions     []WorkspaceAction `json:"actions"`
	Conflicts   int               `json:"conflicts"`

	pack     *Pack
	payloads map[string][]byte
}

type WorkspaceResult struct {
	PackName string            `json:"pack_name"`
	Template string            `json:"template"`
	Path     string            `json:"path"`
	Applied  bool              `json:"applied"`
	Actions  []WorkspaceAction `json:"actions"`
}

func PlanWorkspace(pack *Pack, options WorkspaceOptions) (WorkspacePlan, error) {
	if pack == nil {
		return WorkspacePlan{}, fmt.Errorf("pack is required")
	}
	templateName := strings.TrimSpace(options.Template)
	if templateName == "" {
		templateName = "morathustra"
	}
	destinationValue := strings.TrimSpace(options.Path)
	if destinationValue == "" {
		return WorkspacePlan{}, fmt.Errorf("workspace destination requires explicit --path")
	}
	destination, err := filepath.Abs(filepath.Clean(destinationValue))
	if err != nil {
		return WorkspacePlan{}, fmt.Errorf("resolve workspace destination: %w", err)
	}
	template, err := findWorkspaceTemplate(pack, templateName)
	if err != nil {
		return WorkspacePlan{}, err
	}
	templateRoot, err := secureJoin(pack.Root.Path, template.Path)
	if err != nil {
		return WorkspacePlan{}, fmt.Errorf("resolve workspace template: %w", err)
	}
	payloads, err := inspectWorkspaceTemplate(pack, template)
	if err != nil {
		return WorkspacePlan{}, err
	}
	parent, err := openWorkspaceDirectory(filepath.Dir(destination))
	if err != nil {
		return WorkspacePlan{}, fmt.Errorf("inspect workspace parent: %w", err)
	}
	parent.Close()
	if isNamedPersonaTemplate(templateName) {
		if err := inspectMorathustraDestination(destination); err != nil {
			return WorkspacePlan{}, err
		}
	}

	files := make([]string, 0, len(template.Files))
	directorySet := map[string]bool{".": true}
	seen := map[string]bool{}
	for _, declared := range template.Files {
		relative, err := normalizeWorkspaceRelativePath(declared)
		if err != nil {
			return WorkspacePlan{}, fmt.Errorf("template %q file %q: %w", templateName, declared, err)
		}
		if seen[relative] {
			return WorkspacePlan{}, fmt.Errorf("template %q declares duplicate file %q", templateName, relative)
		}
		seen[relative] = true
		files = append(files, relative)
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative))); parent != "."; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			directorySet[parent] = true
		}
	}

	directories := make([]string, 0, len(directorySet))
	for relative := range directorySet {
		directories = append(directories, relative)
	}
	sort.Slice(directories, func(i, j int) bool {
		leftDepth := strings.Count(directories[i], "/")
		rightDepth := strings.Count(directories[j], "/")
		if leftDepth == rightDepth {
			return directories[i] < directories[j]
		}
		return leftDepth < rightDepth
	})
	sort.Strings(files)

	plan := WorkspacePlan{PackName: pack.Manifest.Name, PackVersion: pack.Manifest.Version, Template: templateName, Path: destination, pack: pack, payloads: payloads}
	blockedDirectories := map[string]bool{}
	for _, relative := range directories {
		path := destination
		if relative != "." {
			path, err = secureJoin(destination, relative)
			if err != nil {
				return WorkspacePlan{}, err
			}
		}
		action := WorkspaceAction{Kind: WorkspaceCreateDirectory, RelativePath: relative, DestinationPath: path}
		if workspaceAncestorBlocked(relative, blockedDirectories) {
			action.Kind = WorkspaceConflict
			plan.Conflicts++
			blockedDirectories[relative] = true
			plan.Actions = append(plan.Actions, action)
			continue
		}
		info, statErr := os.Lstat(path)
		switch {
		case statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0:
			action.Kind = WorkspaceUnchangedDirectory
		case statErr == nil:
			action.Kind = WorkspaceConflict
			plan.Conflicts++
			blockedDirectories[relative] = true
		case os.IsNotExist(statErr):
			if workspaceAncestorBlocked(relative, blockedDirectories) {
				action.Kind = WorkspaceConflict
				plan.Conflicts++
				blockedDirectories[relative] = true
			}
		default:
			return WorkspacePlan{}, fmt.Errorf("inspect workspace directory %q: %w", path, statErr)
		}
		plan.Actions = append(plan.Actions, action)
	}

	for _, relative := range files {
		source, _ := secureJoin(templateRoot, relative)
		destinationPath, _ := secureJoin(destination, relative)
		action := WorkspaceAction{Kind: WorkspaceCreateFile, RelativePath: relative, SourcePath: source, DestinationPath: destinationPath}
		if workspaceAncestorBlocked(filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative))), blockedDirectories) {
			action.Kind = WorkspaceConflict
			plan.Conflicts++
			plan.Actions = append(plan.Actions, action)
			continue
		}
		info, statErr := os.Lstat(destinationPath)
		switch {
		case statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0:
			sourcePayload := payloads[relative]
			currentPayload, readErr := readWorkspaceFile(destinationPath)
			if readErr != nil {
				return WorkspacePlan{}, readErr
			}
			if isNamedPersonaTemplate(templateName) && relative == ".hermes/SOUL.md" && !substantiveSoul(currentPayload) {
				return WorkspacePlan{}, fmt.Errorf("existing .hermes/SOUL.md is not substantive; operator review required")
			}
			if bytes.Equal(sourcePayload, currentPayload) {
				action.Kind = WorkspaceUnchangedFile
			} else {
				action.Kind = WorkspacePreserveUserFile
			}
		case statErr == nil:
			action.Kind = WorkspaceConflict
			plan.Conflicts++
		case os.IsNotExist(statErr):
		default:
			return WorkspacePlan{}, fmt.Errorf("inspect workspace file %q: %w", destinationPath, statErr)
		}
		plan.Actions = append(plan.Actions, action)
	}
	return plan, nil
}

func ApplyWorkspace(plan WorkspacePlan, confirmed bool) (WorkspaceResult, error) {
	result := WorkspaceResult{PackName: plan.PackName, Template: plan.Template, Path: plan.Path, Actions: append([]WorkspaceAction(nil), plan.Actions...)}
	if !confirmed {
		return result, fmt.Errorf("workspace scaffolding requires explicit confirmation")
	}
	if strings.TrimSpace(plan.Path) == "" {
		return result, fmt.Errorf("workspace destination is required")
	}
	if plan.Conflicts > 0 {
		return result, fmt.Errorf("workspace plan has %d path conflict(s)", plan.Conflicts)
	}
	if plan.pack == nil {
		return result, fmt.Errorf("workspace plan must be prepared in this process")
	}
	fresh, err := PlanWorkspace(plan.pack, WorkspaceOptions{Template: plan.Template, Path: plan.Path})
	if err != nil {
		return result, fmt.Errorf("revalidate workspace plan: %w", err)
	}
	if plan.PackName != fresh.PackName || plan.PackVersion != fresh.PackVersion || !reflect.DeepEqual(plan.Actions, fresh.Actions) || !reflect.DeepEqual(plan.payloads, fresh.payloads) {
		return result, fmt.Errorf("workspace source or destination changed; prepare and review a new plan")
	}
	var sealDirectories []string
	for _, action := range plan.Actions {
		switch action.Kind {
		case WorkspaceCreateDirectory:
			mode := os.FileMode(0o755)
			if isNamedPersonaTemplate(plan.Template) && (action.RelativePath == "." || action.RelativePath == ".hermes") {
				mode = 0o700
			}
			if err := createWorkspaceDirectory(action.DestinationPath, mode); err != nil {
				return result, err
			}
			if isNamedPersonaTemplate(plan.Template) && (action.RelativePath == "skills" || action.RelativePath == "skills/installed" || strings.HasPrefix(action.RelativePath, "skills/installed/")) {
				sealDirectories = append(sealDirectories, action.DestinationPath)
			}
		case WorkspaceCreateFile:
			parent, err := openWorkspaceDirectory(filepath.Dir(action.DestinationPath))
			if err != nil {
				return result, err
			}
			mode := os.FileMode(0o644)
			if isNamedPersonaTemplate(plan.Template) {
				if action.RelativePath == ".hermes/SOUL.md" {
					mode = 0o600
				} else if strings.HasPrefix(action.RelativePath, "skills/installed/") {
					mode = 0o444
				}
			}
			file, err := parent.OpenFile(filepath.Base(action.DestinationPath), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			parent.Close()
			if err != nil {
				return result, fmt.Errorf("create workspace file %q: %w", action.DestinationPath, err)
			}
			if _, err := file.Write(plan.payloads[action.RelativePath]); err != nil {
				file.Close()
				return result, fmt.Errorf("write workspace file %q: %w", action.DestinationPath, err)
			}
			if err := file.Close(); err != nil {
				return result, err
			}
		case WorkspaceUnchangedDirectory, WorkspaceUnchangedFile, WorkspacePreserveUserFile:
			// Existing directories, modes and user edits remain untouched.
		default:
			return result, fmt.Errorf("unsupported workspace action %q", action.Kind)
		}
	}
	// Seal only directories created by this apply, after writing their files.
	// Existing ownership/modes are never silently repaired by a scaffold.
	for index := len(sealDirectories) - 1; index >= 0; index-- {
		directory, err := openWorkspaceDirectory(sealDirectories[index])
		if err != nil {
			return result, err
		}
		err = directory.Chmod(".", 0o555)
		directory.Close()
		if err != nil {
			return result, err
		}
	}
	result.Applied = true
	return result, nil
}

func findWorkspaceTemplate(pack *Pack, name string) (ManifestTemplate, error) {
	var selected *ManifestTemplate
	for _, template := range pack.Manifest.Templates {
		if template.Name == name {
			if selected != nil {
				return ManifestTemplate{}, fmt.Errorf("duplicate workspace template %q", name)
			}
			selected = &template
		}
	}
	if selected != nil {
		return *selected, nil
	}
	return ManifestTemplate{}, fmt.Errorf("unknown workspace template %q", name)
}

func normalizeWorkspaceRelativePath(value string) (string, error) {
	if value != strings.TrimSpace(value) {
		return "", fmt.Errorf("path must not contain surrounding whitespace")
	}
	if value == "" || filepath.IsAbs(value) || strings.ContainsAny(value, "\\:\x00") {
		return "", fmt.Errorf("path must be non-empty and relative")
	}
	normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") || normalized != value {
		return "", fmt.Errorf("path must be normalized and remain within the template")
	}
	return normalized, nil
}

func workspaceAncestorBlocked(relative string, blocked map[string]bool) bool {
	for current := relative; current != "." && current != ""; current = filepath.ToSlash(filepath.Dir(filepath.FromSlash(current))) {
		if blocked[current] {
			return true
		}
	}
	return blocked["."]
}

func createWorkspaceDirectory(path string, mode os.FileMode) error {
	parent, err := openWorkspaceDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := parent.Mkdir(filepath.Base(path), mode); err != nil {
		return fmt.Errorf("create workspace directory %q: %w", path, err)
	}
	return nil
}
