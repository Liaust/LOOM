package projectcontracts

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed all:project_development_pack/templates
var projectDevelopmentTemplates embed.FS

func projectDevelopmentFiles(data scaffoldData) ([]scaffoldFile, error) {
	const root = "project_development_pack/templates"
	files := []scaffoldFile{}
	err := fs.WalkDir(projectDevelopmentTemplates, root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := projectDevelopmentTemplates.ReadFile(path)
		if err != nil {
			return err
		}
		content, err := renderScaffoldTemplate(path, string(raw), data)
		if err != nil {
			return err
		}
		files = append(files, scaffoldFile{RelativePath: strings.TrimPrefix(path, root+"/"), Kind: "project_context", Content: []byte(content), Mode: 0664})
		return nil
	})
	return files, err
}

func (candidate *scaffoldCandidate) prepareProjectDevelopment(data scaffoldData, result *ScaffoldResult) error {
	state, err := candidate.observe(".project", false)
	if err != nil {
		return err
	}
	if state.info != nil && !state.info.IsDir() {
		return fmt.Errorf("create directory conflicts with existing path: .project")
	}
	files, err := projectDevelopmentFiles(data)
	if err != nil {
		return err
	}
	for _, file := range files {
		// A mature project tree is user-owned, not a partially installed pack.
		if state.info != nil && strings.HasPrefix(file.RelativePath, ".project/") {
			continue
		}
		observed, err := candidate.observe(file.RelativePath, false)
		if err != nil {
			return err
		}
		action := "planned"
		if observed.info != nil {
			if !observed.info.Mode().IsRegular() {
				return fmt.Errorf("project entrypoint conflicts with existing path: %s", file.RelativePath)
			}
			action = "existing"
		} else {
			candidate.files = append(candidate.files, file)
		}
		result.Files = append(result.Files, ScaffoldFileResult{Path: file.RelativePath, Kind: file.Kind, Action: action})
	}
	if state.info != nil {
		result.Directories = append(result.Directories, ScaffoldDirectoryResult{Path: ".project", Action: "existing"})
	}
	return nil
}
