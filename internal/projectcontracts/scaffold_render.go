package projectcontracts

import (
	"fmt"
	"io"
	"strings"
	"time"
)

type ProjectContentTemplateInput struct {
	Title      string
	Path       string
	DatasetKey string
}

type projectContentTemplateData struct {
	ProjectContentTemplateInput
	CreatedAt string
	UpdatedAt string
}

// RenderProjectContentTemplate instantiates a reusable project-local content
// template. It captures the clock once so initial creation and update times are
// identical and deterministic under an injected clock.
func RenderProjectContentTemplate(name, source string, input ProjectContentTemplateInput, clock func() time.Time) (string, error) {
	if clock == nil {
		clock = time.Now
	}
	instant := clock()
	if instant.IsZero() {
		return "", fmt.Errorf("content template clock returned a zero timestamp")
	}
	timestamp := instant.UTC().Format(time.RFC3339)
	return renderScaffoldTemplate(name, source, projectContentTemplateData{
		ProjectContentTemplateInput: input,
		CreatedAt:                   timestamp,
		UpdatedAt:                   timestamp,
	})
}

func RenderScaffold(w io.Writer, result ScaffoldResult, dryRun bool) {
	status := "created"
	if dryRun {
		status = "planned"
	}
	if !result.OK {
		status = "failed"
	}
	created, overwritten, planned := scaffoldActionCounts(result.Files)
	fmt.Fprintf(w, "Project scaffold: %s\n", status)
	fmt.Fprintf(w, "Project: %s\n", result.Slug)
	fmt.Fprintf(w, "Path: %s\n", result.ProjectRoot)
	if result.BoxDefault {
		fmt.Fprintf(w, "Box: %s\n", result.BoxRoot)
	}
	fmt.Fprintf(w, "Directory source: %s\n", result.DirSource)
	if result.ExecutionLocation != "" {
		fmt.Fprintf(w, "Execution filesystem: %s\n", result.ExecutionLocation)
	}
	fmt.Fprintf(w, "Owner node: %s\n", result.OwnerNode)
	if result.Mode != ScaffoldModeDeclaration {
		fmt.Fprintf(w, "Preset: %s\n", result.Preset)
	}
	if len(result.Facets) > 0 {
		fmt.Fprintf(w, "Facets: %s\n", strings.Join(result.Facets, ", "))
	}
	if dryRun {
		fmt.Fprintf(w, "Directories: %d planned, %d existing\n", directoryActionCount(result.Directories, "planned"), directoryActionCount(result.Directories, "existing"))
		fmt.Fprintf(w, "Files: %d planned, 0 skipped\n", planned)
		fmt.Fprintf(w, "Validation: %s\n", scaffoldValidationRender(result.Validation))
		return
	}
	fmt.Fprintf(w, "Directories: %d created, %d existing\n", directoryActionCount(result.Directories, "created"), directoryActionCount(result.Directories, "existing"))
	fmt.Fprintf(w, "Files: %d created, %d overwritten, 0 skipped\n", created, overwritten)
	fmt.Fprintf(w, "Validation: %d errors, %d warnings (%s)\n", result.Validation.Errors, result.Validation.Warnings, scaffoldValidationState(result.Validation))
	if result.Mode == ScaffoldModeDeclaration {
		fmt.Fprintf(w, "Context: %s\n", result.ContextState)
		fmt.Fprintln(w, "Start with AGENTS.md and .project/STATE.md. Edit ordinary folders freely; declare only resources that LOOM should manage.")
		return
	}
	fmt.Fprintln(w, "Next:")
	fmt.Fprintf(w, "  loom project validate %s\n", result.ProjectRoot)
	fmt.Fprintf(w, "  loom project plan %s\n", result.ProjectRoot)
}

func scaffoldValidationRender(summary ScaffoldValidationSummary) string {
	switch scaffoldValidationState(summary) {
	case ScaffoldValidationPlannedOnly:
		return "planned_only (project files were not written; validation did not run)"
	case ScaffoldValidationNotRun:
		return "not_run"
	default:
		return fmt.Sprintf("%d errors, %d warnings (%s)", summary.Errors, summary.Warnings, scaffoldValidationState(summary))
	}
}

func scaffoldActionCounts(files []ScaffoldFileResult) (created, overwritten, planned int) {
	for _, file := range files {
		switch file.Action {
		case "created":
			created++
		case "overwritten":
			overwritten++
		case "planned":
			planned++
		}
	}
	return created, overwritten, planned
}

func RenderAddProjectFacets(w io.Writer, result AddProjectFacetsResult) {
	status := "updated"
	if result.DryRun {
		status = "planned"
	}
	if !result.OK {
		status = "failed"
	}
	created, updated, overwritten, skipped, planned, plannedUpdate, plannedOverwrite := facetAddActionCounts(result.Files)
	fmt.Fprintf(w, "Project facets: %s\n", status)
	fmt.Fprintf(w, "Project: %s\n", result.Slug)
	fmt.Fprintf(w, "Path: %s\n", result.ProjectRoot)
	if len(result.RequestedFacets) > 0 {
		fmt.Fprintf(w, "Requested facets: %s\n", strings.Join(result.RequestedFacets, ", "))
	}
	if len(result.AddedFacets) > 0 {
		fmt.Fprintf(w, "Added facets: %s\n", strings.Join(result.AddedFacets, ", "))
	}
	if len(result.ExistingFacets) > 0 {
		fmt.Fprintf(w, "Already enabled: %s\n", strings.Join(result.ExistingFacets, ", "))
	}
	if result.DryRun {
		fmt.Fprintf(w, "Directories: %d planned, %d existing\n", directoryActionCount(result.Directories, "planned"), directoryActionCount(result.Directories, "existing"))
		fmt.Fprintf(w, "Files: %d planned, %d planned update, %d planned overwrite, %d skipped\n", planned, plannedUpdate, plannedOverwrite, skipped)
		fmt.Fprintf(w, "Validation: %s\n", scaffoldValidationRender(result.Validation))
	} else {
		fmt.Fprintf(w, "Directories: %d created, %d existing\n", directoryActionCount(result.Directories, "created"), directoryActionCount(result.Directories, "existing"))
		fmt.Fprintf(w, "Files: %d created, %d updated, %d overwritten, %d skipped\n", created, updated, overwritten, skipped)
		fmt.Fprintf(w, "Validation: %d errors, %d warnings (%s)\n", result.Validation.Errors, result.Validation.Warnings, scaffoldValidationState(result.Validation))
	}
	fmt.Fprintln(w, "Next:")
	fmt.Fprintf(w, "  loom project validate %s\n", result.ProjectRoot)
	fmt.Fprintf(w, "  loom project plan %s\n", result.ProjectRoot)
	if len(result.AddedFacets) > 0 {
		for _, facet := range result.AddedFacets {
			switch facet {
			case "scripts", "workflows", "connectors", "modules", "schedules", "direct_events":
				fmt.Fprintf(w, "  loom project activate %s --facet %s --project-root %s\n", result.Slug, facet, result.ProjectRoot)
			}
		}
	}
}

func directoryActionCount(directories []ScaffoldDirectoryResult, action string) int {
	count := 0
	for _, directory := range directories {
		if directory.Action == action {
			count++
		}
	}
	return count
}

func RenderLayoutMigration(w io.Writer, result LayoutMigrationResult) {
	status := "planned"
	if result.Applied {
		status = "applied"
	}
	if !result.OK {
		status = "blocked"
	}
	fmt.Fprintf(w, "Project layout migration: %s\n", status)
	fmt.Fprintf(w, "Path: %s\n", result.ProjectRoot)
	fmt.Fprintf(w, "Layout: %s", result.BeforeLayout)
	if result.AfterLayout != "" {
		fmt.Fprintf(w, " -> %s", result.AfterLayout)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Actions: %d\n", len(result.Actions))
	for _, action := range result.Actions {
		target := action.Destination
		if target == "" {
			target = action.Source
		}
		fmt.Fprintf(w, "  %d. %s %s (%s)\n", action.Order, action.Operation, target, action.Status)
	}
	for _, collision := range result.Collisions {
		fmt.Fprintf(w, "Collision: %s (%s)\n", collision.Destination, collision.Reason)
	}
	for _, skip := range result.Skips {
		fmt.Fprintf(w, "Skipped: %s (%s)\n", skip.Path, skip.Reason)
	}
	if result.RecordPath != "" {
		fmt.Fprintf(w, "Record: %s\n", result.RecordPath)
	}
	if len(result.NextActions) > 0 {
		fmt.Fprintln(w, "Next:")
		for _, next := range result.NextActions {
			fmt.Fprintf(w, "  %s\n", next)
		}
	}
}

func RenderScaffoldCleanup(w io.Writer, result ScaffoldCleanupResult) {
	status := "cleaned"
	if result.DryRun {
		status = "planned"
	}
	if !result.OK {
		status = "failed"
	}
	removed, skipped, absent, planned := scaffoldCleanupActionCounts(result.Packages)
	fmt.Fprintf(w, "Project scaffold cleanup: %s\n", status)
	fmt.Fprintf(w, "Project: %s\n", result.Slug)
	fmt.Fprintf(w, "Path: %s\n", result.ProjectRoot)
	if result.DryRun {
		fmt.Fprintf(w, "Packages: %d planned remove, %d skipped, %d absent\n", planned, skipped, absent)
		fmt.Fprintf(w, "Validation: %s\n", scaffoldValidationRender(result.Validation))
	} else {
		fmt.Fprintf(w, "Packages: %d removed, %d skipped, %d absent\n", removed, skipped, absent)
		fmt.Fprintf(w, "Validation: %d errors, %d warnings (%s)\n", result.Validation.Errors, result.Validation.Warnings, scaffoldValidationState(result.Validation))
	}
	for _, pkg := range result.Packages {
		if pkg.Action == "skipped" {
			fmt.Fprintf(w, "Skipped: %s (%s)\n", pkg.Path, pkg.Reason)
		}
	}
	fmt.Fprintln(w, "Next:")
	fmt.Fprintf(w, "  loom project validate %s\n", result.ProjectRoot)
	fmt.Fprintf(w, "  loom project plan %s\n", result.ProjectRoot)
}

func scaffoldCleanupActionCounts(packages []ScaffoldCleanupPackage) (removed, skipped, absent, planned int) {
	for _, pkg := range packages {
		switch pkg.Action {
		case "removed":
			removed++
		case "skipped":
			skipped++
		case "absent":
			absent++
		case "planned_remove":
			planned++
		}
	}
	return removed, skipped, absent, planned
}

func facetAddActionCounts(files []ScaffoldFileResult) (created, updated, overwritten, skipped, planned, plannedUpdate, plannedOverwrite int) {
	for _, file := range files {
		switch file.Action {
		case "created":
			created++
		case "updated":
			updated++
		case "overwritten":
			overwritten++
		case "skipped":
			skipped++
		case "planned":
			planned++
		case "planned_update":
			plannedUpdate++
		case "planned_overwrite":
			plannedOverwrite++
		}
	}
	return created, updated, overwritten, skipped, planned, plannedUpdate, plannedOverwrite
}
