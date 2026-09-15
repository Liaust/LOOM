package projectcontracts

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ScaffoldCleanupOptions struct {
	ProjectRoot string
	DryRun      bool
}

type ScaffoldCleanupResult struct {
	OK           bool                      `json:"ok"`
	DryRun       bool                      `json:"dry_run,omitempty"`
	ProjectRoot  string                    `json:"project_root"`
	ContractPath string                    `json:"contract_path"`
	Slug         string                    `json:"slug"`
	Name         string                    `json:"name"`
	OwnerNode    string                    `json:"owner_node"`
	Packages     []ScaffoldCleanupPackage  `json:"packages"`
	Removed      []string                  `json:"removed,omitempty"`
	Skipped      []ScaffoldCleanupSkip     `json:"skipped,omitempty"`
	Validation   ScaffoldValidationSummary `json:"validation"`
}

type ScaffoldCleanupPackage struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

type ScaffoldCleanupSkip struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

type cleanupExamplePackage struct {
	Path  string
	Kind  string
	Files []scaffoldFile
}

var cleanupExamplePackagePaths = map[string]string{
	"scripts/hello_world":         "script_example_package",
	"workflows/example_workflow":  "workflow_example_package",
	"schedules/example_schedule":  "schedule_example_package",
	"direct_events/example_event": "direct_event_example_package",
}

func CleanupScaffoldExamples(options ScaffoldCleanupOptions) (ScaffoldCleanupResult, error) {
	loaded, err := LoadProject(options.ProjectRoot)
	if err != nil {
		return ScaffoldCleanupResult{}, err
	}
	contract := NormalizeContract(loaded.Contract)
	result := ScaffoldCleanupResult{
		OK:           true,
		DryRun:       options.DryRun,
		ProjectRoot:  loaded.RootPath,
		ContractPath: loaded.ContractPath,
		Slug:         contract.Project.Slug,
		Name:         contract.Project.Name,
		OwnerNode:    contract.Project.OwnerNode,
		Packages:     []ScaffoldCleanupPackage{},
		Removed:      []string{},
		Skipped:      []ScaffoldCleanupSkip{},
	}
	packages, err := scaffoldCleanupPackages(contract)
	if err != nil {
		return result, err
	}
	for _, pkg := range packages {
		pkgResult, err := cleanupScaffoldPackage(loaded.RootPath, pkg, options.DryRun)
		if err != nil {
			return result, err
		}
		result.Packages = append(result.Packages, pkgResult)
		switch pkgResult.Action {
		case "removed", "planned_remove":
			result.Removed = append(result.Removed, pkg.Path)
		case "skipped":
			result.Skipped = append(result.Skipped, ScaffoldCleanupSkip{Path: pkg.Path, Kind: pkg.Kind, Reason: pkgResult.Reason})
		}
	}
	if options.DryRun {
		result.Validation = ScaffoldValidationSummary{State: ScaffoldValidationPlannedOnly}
		return result, nil
	}
	analysis := Analyze(loaded.RootPath)
	result.Validation = scaffoldValidationFromReport(analysis.Report)
	if !analysis.Report.OK {
		result.OK = false
		return result, fmt.Errorf("project failed validation after scaffold cleanup with %d error(s): %s", analysis.Report.Summary.Errors, firstDiagnosticSummary(analysis.Report.Diagnostics))
	}
	return result, nil
}

func scaffoldCleanupPackages(contract ProjectContract) ([]cleanupExamplePackage, error) {
	facets := enabledContractFacets(contract.Facets)
	for _, facet := range []string{"scripts", "workflows", "schedules", "direct_events"} {
		if !containsString(facets, facet) {
			facets = append(facets, facet)
		}
	}
	data, err := scaffoldDataFromContract(contract, facets)
	if err != nil {
		return nil, err
	}
	files := []scaffoldFile{}
	for _, facet := range []string{"scripts", "workflows", "schedules", "direct_events"} {
		if err := appendFacetFiles(&files, data, facet); err != nil {
			return nil, err
		}
	}
	byPath := map[string]*cleanupExamplePackage{}
	for path, kind := range cleanupExamplePackagePaths {
		byPath[path] = &cleanupExamplePackage{Path: path, Kind: kind, Files: []scaffoldFile{}}
	}
	for _, file := range files {
		for path := range cleanupExamplePackagePaths {
			if file.RelativePath == path || strings.HasPrefix(file.RelativePath, path+"/") {
				byPath[path].Files = append(byPath[path].Files, file)
			}
		}
	}
	packages := make([]cleanupExamplePackage, 0, len(byPath))
	for path, pkg := range byPath {
		if len(pkg.Files) == 0 {
			return nil, fmt.Errorf("scaffold cleanup package %s has no expected files", path)
		}
		sort.SliceStable(pkg.Files, func(i, j int) bool {
			return pkg.Files[i].RelativePath < pkg.Files[j].RelativePath
		})
		packages = append(packages, *pkg)
	}
	sort.SliceStable(packages, func(i, j int) bool {
		return packages[i].Path < packages[j].Path
	})
	return packages, nil
}

func cleanupScaffoldPackage(root string, pkg cleanupExamplePackage, dryRun bool) (ScaffoldCleanupPackage, error) {
	target, err := scaffoldTargetPath(root, pkg.Path)
	if err != nil {
		return ScaffoldCleanupPackage{}, err
	}
	result := ScaffoldCleanupPackage{Path: pkg.Path, Kind: pkg.Kind}
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		result.Action = "absent"
		return result, nil
	} else if err != nil {
		return result, err
	}
	ok, reason, err := scaffoldPackageUntouched(root, pkg)
	if err != nil {
		return result, err
	}
	if !ok {
		result.Action = "skipped"
		result.Reason = reason
		return result, nil
	}
	if dryRun {
		result.Action = "planned_remove"
		return result, nil
	}
	if err := os.RemoveAll(target); err != nil {
		return result, err
	}
	result.Action = "removed"
	return result, nil
}

func scaffoldPackageUntouched(root string, pkg cleanupExamplePackage) (bool, string, error) {
	expectedFiles := map[string]scaffoldFile{}
	expectedDirs := map[string]bool{filepath.ToSlash(pkg.Path): true}
	for _, file := range pkg.Files {
		rel := filepath.ToSlash(file.RelativePath)
		expectedFiles[rel] = file
		dir := filepath.ToSlash(filepath.Dir(rel))
		for dir != "." && dir != "/" && strings.HasPrefix(dir, filepath.ToSlash(pkg.Path)) {
			expectedDirs[dir] = true
			next := filepath.ToSlash(filepath.Dir(dir))
			if next == dir {
				break
			}
			dir = next
		}
	}
	for rel, expected := range expectedFiles {
		target, err := scaffoldTargetPath(root, rel)
		if err != nil {
			return false, "", err
		}
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			return false, "missing expected file " + rel, nil
		}
		if err != nil {
			return false, "", err
		}
		if !info.Mode().IsRegular() {
			return false, "expected file is not a regular file: " + rel, nil
		}
		if executableBit(info.Mode()) != executableBit(expected.Mode) {
			return false, "file mode changed: " + rel, nil
		}
		raw, err := os.ReadFile(target)
		if err != nil {
			return false, "", err
		}
		if !bytes.Equal(raw, expected.Content) {
			return false, "file content changed: " + rel, nil
		}
	}
	target, err := scaffoldTargetPath(root, pkg.Path)
	if err != nil {
		return false, "", err
	}
	err = filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			if !expectedDirs[rel] {
				return fmt.Errorf("extra path %s", rel)
			}
			return nil
		}
		if _, ok := expectedFiles[rel]; !ok {
			return fmt.Errorf("extra path %s", rel)
		}
		return nil
	})
	if err != nil {
		if strings.HasPrefix(err.Error(), "extra path ") {
			return false, err.Error(), nil
		}
		return false, "", err
	}
	return true, "", nil
}

func executableBit(mode fs.FileMode) fs.FileMode {
	if mode&0o111 != 0 {
		return 0o111
	}
	return 0
}
