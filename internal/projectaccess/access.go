package projectaccess

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"loom.local/loom/internal/fsaccess"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/scripts"
)

type Status string

const (
	StatusOK      Status = "ok"
	StatusWarning Status = "warning"
	StatusBlocked Status = "blocked"
	StatusSkipped Status = "skipped"
)

type Check struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Status      Status   `json:"status"`
	Summary     string   `json:"summary"`
	Detail      string   `json:"detail,omitempty"`
	Path        string   `json:"path,omitempty"`
	Facet       string   `json:"facet,omitempty"`
	SubjectRef  string   `json:"subject_ref,omitempty"`
	Remediation string   `json:"remediation,omitempty"`
	Required    []string `json:"required,omitempty"`
}

func Analyze(analysis projectcontracts.Analysis) []Check {
	root := strings.TrimSpace(analysis.Report.ProjectRoot)
	contractPath := strings.TrimSpace(analysis.Report.ContractPath)
	if analysis.Loaded != nil {
		root = strings.TrimSpace(analysis.Loaded.RootPath)
		contractPath = strings.TrimSpace(analysis.Loaded.ContractPath)
	}
	if root == "" {
		return []Check{{
			Key:         "runtime_access.project_root.skipped",
			Title:       "Project Runtime Access",
			Status:      StatusSkipped,
			Summary:     "Project root is unavailable.",
			Remediation: "Load a local project folder before running runtime access checks.",
		}}
	}
	checks := []Check{
		pathCheck("runtime_access.project_root", "Project Root Access", root, "", "project", fsaccess.Read, fsaccess.Execute),
	}
	if contractPath != "" {
		checks = append(checks, pathCheck("runtime_access.project_contract", "Project Contract Access", contractPath, "", filepath.ToSlash(contractPath), fsaccess.Read))
	}
	for _, item := range analysis.Plan.Scripts {
		checks = append(checks, scriptChecks(root, item)...)
	}
	for _, item := range analysis.Plan.Workflows {
		checks = append(checks, workflowChecks(root, item)...)
	}
	for _, item := range analysis.Plan.WatchedRoots {
		if strings.TrimSpace(item.RootRelativePath) == "" || item.ActivationStatus == "disabled" {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(item.RootRelativePath))
		checks = append(checks, pathCheck("runtime_access.watched_root."+item.BackendRootKey, "Watched Root Runtime Access", path, "watched_roots", item.BackendRootKey, fsaccess.Read, fsaccess.Execute))
	}
	sort.SliceStable(checks, func(i, j int) bool {
		if statusRank(checks[i].Status) != statusRank(checks[j].Status) {
			return statusRank(checks[i].Status) < statusRank(checks[j].Status)
		}
		return checks[i].Key < checks[j].Key
	})
	return checks
}

func Blocking(checks []Check) []Check {
	out := []Check{}
	for _, check := range checks {
		if check.Status == StatusBlocked {
			out = append(out, check)
		}
	}
	return out
}

func BlockingSummary(checks []Check) string {
	blocked := Blocking(checks)
	if len(blocked) == 0 {
		return ""
	}
	parts := make([]string, 0, min(len(blocked), 3))
	for i, check := range blocked {
		if i >= 3 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s: %s", check.Key, firstNonEmpty(check.Path, check.Summary)))
	}
	if len(blocked) > len(parts) {
		parts = append(parts, fmt.Sprintf("%d more", len(blocked)-len(parts)))
	}
	return strings.Join(parts, "; ")
}

func scriptChecks(root string, item projectcontracts.ScriptFacetItem) []Check {
	if item.ActivationStatus == "disabled" {
		return nil
	}
	packageRoot := filepath.Join(root, filepath.FromSlash(item.Folder))
	checks := []Check{
		pathCheck("runtime_access.scripts."+item.Key+".package", "Script Package Access", packageRoot, "scripts", item.Key, packageRequirements(item.Manifest.Execution)...),
	}
	checks = append(checks, entrypointChecks("runtime_access.scripts."+item.Key, "Script Entrypoint Access", packageRoot, item.Manifest.Entrypoint, "scripts", item.Key)...)
	return checks
}

func workflowChecks(root string, item projectcontracts.WorkflowFacetItem) []Check {
	if !item.Executable || item.ContractStatus == "disabled" {
		return nil
	}
	packageRoot := filepath.Join(root, filepath.FromSlash(firstNonEmpty(item.PackageRoot, item.Folder)))
	checks := []Check{
		pathCheck("runtime_access.workflows."+item.Key+".package", "Workflow Package Access", packageRoot, "workflows", item.Key, packageRequirements(item.Execution)...),
	}
	checks = append(checks, entrypointChecks("runtime_access.workflows."+item.Key, "Workflow Entrypoint Access", packageRoot, item.Entrypoint, "workflows", item.Key)...)
	return checks
}

func packageRequirements(execution scripts.Execution) []fsaccess.Requirement {
	requirements := []fsaccess.Requirement{fsaccess.Read, fsaccess.Execute}
	mode := strings.ToLower(strings.TrimSpace(fmt.Sprint(execution.Filesystem["mode"])))
	if strings.Contains(mode, "write") && !strings.Contains(mode, "read_only") {
		requirements = append(requirements, fsaccess.Write)
	}
	return requirements
}

func entrypointChecks(keyPrefix, title, packageRoot string, entrypoint scripts.Entrypoint, facet, subject string) []Check {
	command := entrypoint.Command
	if len(command) == 0 {
		return []Check{{
			Key:         keyPrefix + ".entrypoint_missing",
			Title:       title,
			Status:      StatusBlocked,
			Summary:     "Entrypoint command is missing.",
			Facet:       facet,
			SubjectRef:  subject,
			Remediation: "Add entrypoint.command before activating this runtime facet.",
		}}
	}
	first := strings.TrimSpace(command[0])
	if path, ok := resolvePackageCommandPath(packageRoot, first); ok {
		return []Check{pathCheck(keyPrefix+".entrypoint", title, path, facet, subject, fsaccess.Read, fsaccess.Execute)}
	}
	if isInterpreter(first) {
		checks := []Check{}
		for index, arg := range command[1:] {
			path, ok := resolvePackageCommandPath(packageRoot, arg)
			if !ok {
				continue
			}
			checks = append(checks, pathCheck(fmt.Sprintf("%s.entrypoint_arg_%d", keyPrefix, index+1), title, path, facet, subject, fsaccess.Read))
		}
		if len(checks) > 0 {
			return checks
		}
	}
	if _, err := exec.LookPath(first); err != nil {
		return []Check{{
			Key:         keyPrefix + ".entrypoint_command",
			Title:       title,
			Status:      StatusWarning,
			Summary:     "Entrypoint command was not found on the current PATH.",
			Detail:      err.Error(),
			Facet:       facet,
			SubjectRef:  subject,
			Remediation: "Ensure the loomd service environment can resolve the command or use a package-relative entrypoint.",
		}}
	}
	return []Check{{
		Key:        keyPrefix + ".entrypoint_command",
		Title:      title,
		Status:     StatusOK,
		Summary:    "Entrypoint command resolves from PATH.",
		Facet:      facet,
		SubjectRef: subject,
	}}
}

func pathCheck(key, title, path, facet, subject string, requirements ...fsaccess.Requirement) Check {
	result := fsaccess.Check(path, requirements...)
	check := Check{
		Key:        key,
		Title:      title,
		Path:       path,
		Facet:      facet,
		SubjectRef: subject,
		Required:   requirementsAsStrings(result.Required),
	}
	if result.OK {
		check.Status = StatusOK
		check.Summary = "Required runtime access is available."
		return check
	}
	check.Status = StatusBlocked
	check.Summary = "Required runtime access is not available."
	check.Detail = firstNonEmpty(result.Error, "missing access: "+strings.Join(requirementsAsStrings(result.MissingModes), ", "))
	check.Remediation = "Grant the LOOM runtime user the listed access, then rerun project doctor or activation."
	return check
}

func resolvePackageCommandPath(packageRoot, raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || strings.HasPrefix(value, "-") || strings.Contains(value, "://") {
		return "", false
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), true
	}
	if strings.HasPrefix(value, ".") || strings.Contains(value, "/") {
		return filepath.Clean(filepath.Join(packageRoot, filepath.FromSlash(value))), true
	}
	return "", false
}

func isInterpreter(command string) bool {
	switch filepath.Base(strings.TrimSpace(command)) {
	case "bash", "sh", "zsh", "python", "python3", "node", "npm", "npx", "deno", "bun", "ruby", "perl", "php", "go":
		return true
	default:
		return false
	}
}

func requirementsAsStrings(requirements []fsaccess.Requirement) []string {
	out := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		out = append(out, string(requirement))
	}
	return out
}

func statusRank(status Status) int {
	switch status {
	case StatusBlocked:
		return 0
	case StatusWarning:
		return 1
	case StatusOK:
		return 2
	case StatusSkipped:
		return 3
	default:
		return 4
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
