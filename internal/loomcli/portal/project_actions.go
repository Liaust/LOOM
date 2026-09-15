package portal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectdoctor"
	"loom.local/loom/internal/projectexport"
	"loom.local/loom/internal/projects"
)

func NewProjectCreateHintAction(sourceScreen string) PortalAction {
	payload := map[string]string{
		"recommended_surface": "LOOM Box",
		"command":             "loom project scaffold <project-name>",
		"portal_command":      "$ create project <project-name>",
		"status":              "Box status was not available on this screen load; open Box or use command mode.",
	}
	action := NewPortalRecordInspectAction("projects", sourceScreen, "project_create_hint", "create_project", "Create Project In Box", payload)
	action.ID = "project.create.hint"
	action.Label = "Create Project"
	action.Description = "Create a project scaffold in the Box Projects folder. Open Box or use $ create project <name> if Box status is unavailable here."
	action.TargetKind = "project_create"
	action.TargetRef = "create_project"
	action.TargetLabel = "Box Projects"
	action.RawCommand = []string{"loom", "project", "scaffold", "<project-name>"}
	action.RefreshScreen = ScreenProjects
	return action
}

func NewProjectScaffoldOnBackendAction(sourceScreen string, status box.Status) PortalAction {
	targetOptions := []string{"main"}
	localOwner := strings.TrimSpace(status.OwnerNode)
	if status.State == "ok" && localOwner != "" && localOwner != "main" {
		targetOptions = append(targetOptions, localOwner)
	}
	payload := map[string]string{
		"default_target_node": "main",
		"target":              "node_box",
		"scope":               "node_project",
		"project_parent":      "selected node loom-box/Projects",
		"local_owner_node":    localOwner,
		"local_box_state":     status.State,
		"local_project_parent": firstNonEmpty(
			status.DefaultProjectPath,
			filepath.Join(status.RootPath, "Projects"),
		),
		"local_box_root":          status.RootPath,
		"local_box_profile":       status.Profile,
		"local_box_state_root":    status.RuntimeStateRoot,
		"local_box_contract_path": status.ContractPath,
	}
	action := PortalAction{
		ID:           "project.scaffold.backend",
		Label:        "Create Project",
		Description:  "Create a project scaffold under the selected node's LOOM Box Projects folder.",
		Domain:       "projects",
		SourceScreen: NormalizeScreen(sourceScreen),
		TargetKind:   "project_create",
		TargetRef:    "node_box",
		TargetLabel:  "select node",
		Risk:         ActionRiskSafeRun,
		State:        ActionAvailable,
		InputFields: []PortalActionField{
			{Name: "project_name", Label: "Project Name", Kind: ActionFieldText, Required: true, Placeholder: "New Project"},
			{Name: "target_node", Label: "Target Node", Kind: ActionFieldSelect, Required: true, Value: "main", Options: targetOptions, Help: "The project folder is created in this node's LOOM Box. Provider URLs use the same prefix, for example main@project-slug."},
			{Name: "preset", Label: "Preset", Kind: ActionFieldText, Value: projectcontracts.PresetMinimal, Help: "Use minimal, automation, software, notes, or another supported project preset."},
			{Name: "facets", Label: "Facets", Kind: ActionFieldText, Help: "Optional comma-separated facet override. Leave blank to use the selected preset."},
			{Name: "dry_run", Label: "Dry Run", Kind: ActionFieldBoolean, Value: "false", Help: "Set true to preview without writing files."},
			{Name: "register_after_create", Label: "Register After Create", Kind: ActionFieldBoolean, Value: "true", Help: "Register the generated contract immediately after creating a main-owned project."},
		},
		InputValues: map[string]string{
			"target_node":           "main",
			"preset":                projectcontracts.PresetMinimal,
			"dry_run":               "false",
			"register_after_create": "true",
		},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectScaffoldBackend, Target: "backend_default_box", Payload: payload},
		RawCommand:    []string{"loom", "project", "scaffold", "<project-name>", "--backend", "--register", "--owner-node", "<target-node>", "--preset", projectcontracts.PresetMinimal},
		RawDetails:    payload,
		RefreshScreen: ScreenProjects,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectAddFacetAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	root := ""
	if detail.Registration != nil {
		root = detail.Registration.ProjectRoot
	}
	payload := map[string]string{"project_ref": ref, "project_root": root}
	action := PortalAction{
		ID:           fmt.Sprintf("project.%s.facet.add", safeActionID(ref)),
		Label:        "Add Facet",
		Description:  "Add one or more project facets and generate missing contract files from the backend.",
		Domain:       "projects",
		SourceScreen: NormalizeScreen(sourceScreen),
		TargetKind:   "project",
		TargetRef:    ref,
		TargetLabel:  projectLabel(project),
		Risk:         ActionRiskSafeRun,
		State:        ActionAvailable,
		InputFields: []PortalActionField{
			{Name: "facets", Label: "Facets", Kind: ActionFieldText, Required: true, Placeholder: "notes, scripts", Help: "Comma-separated facet keys to add."},
			{Name: "dry_run", Label: "Dry Run", Kind: ActionFieldBoolean, Value: "true", Help: "Preview generated files before writing to the project folder."},
			{Name: "force", Label: "Force", Kind: ActionFieldBoolean, Value: "false", Help: "Overwrite existing generated facet files when writing."},
			{Name: "register_after_apply", Label: "Register After Apply", Kind: ActionFieldBoolean, Value: "true", Help: "When Dry Run is false, register the updated backend project contract after writing facet files."},
		},
		InputValues: map[string]string{
			"dry_run":              "true",
			"force":                "false",
			"register_after_apply": "true",
		},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectAddFacet, Target: ref, Payload: payload},
		RawCommand:    []string{"loom", "project", "facet", "add", ref, "<facets>", "--backend", "--dry-run"},
		RawDetails:    payload,
		RefreshScreen: ScreenProjects,
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This project row does not include a project ref."
	} else if projectRuntimeQuarantined(project) {
		action.State = ActionDisabled
		action.DisabledReason = projectRuntimeQuarantineReason(project)
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectMigrateLayoutAction(detail projects.ProjectRegistrationDetail, analysis *projectdoctor.BackendAnalysisResult, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	layout, root, contractPath := projectResolvedLayout(detail, analysis)
	payload := map[string]string{
		"project_ref":   ref,
		"project_root":  root,
		"contract_path": contractPath,
		"layout":        string(layout),
	}
	action := PortalAction{
		ID:           fmt.Sprintf("project.%s.layout.migrate", safeActionID(ref)),
		Label:        "Migrate Project Layout",
		Description:  "Plan or apply migration from the compatible root layout to the canonical project-local .loom layout.",
		Domain:       "projects",
		SourceScreen: NormalizeScreen(sourceScreen),
		TargetKind:   "project",
		TargetRef:    ref,
		TargetLabel:  projectLabel(project),
		Risk:         ActionRiskSensitive,
		State:        ActionAvailable,
		InputFields: []PortalActionField{{
			Name:  "dry_run",
			Label: "Dry Run",
			Kind:  ActionFieldBoolean,
			Value: "true",
			Help:  "Preview the complete collision-checked migration. Set false only after reviewing every action and skip.",
		}},
		InputValues:   map[string]string{"dry_run": "true"},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectMigrateLayout, Target: ref, Payload: payload},
		RawCommand:    []string{"loom", "project", "migrate-layout", ref, "--backend", "--dry-run"},
		RawDetails:    payload,
		RefreshScreen: ScreenProjects,
	}
	switch {
	case ref == "":
		action.State = ActionDisabled
		action.DisabledReason = "This project row does not include a project ref."
	case projectRuntimeQuarantined(project):
		action.State = ActionDisabled
		action.DisabledReason = projectRuntimeQuarantineReason(project)
	case layout != projectcontracts.ProjectLayoutLegacy && layout != projectcontracts.ProjectLayoutCanonicalWithLegacy:
		action.State = ActionDisabled
		action.DisabledReason = "This project already uses the canonical layout or its backend layout is unavailable."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func projectResolvedLayout(detail projects.ProjectRegistrationDetail, analysis *projectdoctor.BackendAnalysisResult) (projectcontracts.ProjectLayout, string, string) {
	if analysis != nil && analysis.Analysis.Loaded != nil {
		loaded := analysis.Analysis.Loaded
		return loaded.Layout, loaded.RootPath, loaded.ContractPath
	}
	if detail.Registration == nil {
		return "", "", ""
	}
	registration := detail.Registration
	return projectLayoutFromPortalPath(registration.ProjectRoot, registration.ContractPath), registration.ProjectRoot, registration.ContractPath
}

func projectLayoutFromPortalPath(root, contractPath string) projectcontracts.ProjectLayout {
	root = strings.TrimSpace(root)
	contractPath = strings.TrimSpace(contractPath)
	if root == "" || contractPath == "" {
		return ""
	}
	relative, err := filepath.Rel(root, contractPath)
	if err != nil {
		return ""
	}
	switch filepath.ToSlash(relative) {
	case projectcontracts.CanonicalRootContractPath:
		return projectcontracts.ProjectLayoutCanonical
	case projectcontracts.LegacyRootContractPath:
		return projectcontracts.ProjectLayoutLegacy
	default:
		return ""
	}
}

func NewProjectRegenerateMissingContractsAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	return newProjectDisabledPlaceholderAction(
		detail,
		sourceScreen,
		"contracts.regenerate_missing",
		"Regenerate Missing Contracts",
		"Regenerate missing project contracts without overwriting user-edited files.",
		ActionRiskSafeRun,
		"Safe contract regeneration is not implemented yet. Use project doctor/plan output to repair contracts manually for now.",
	)
}

func NewProjectRegisterRuntimeAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	return newProjectDisabledPlaceholderAction(
		detail,
		sourceScreen,
		"runtime.register",
		"Register Runtime",
		"Register project runtime bindings for scripts, workflows, connectors, and modules.",
		ActionRiskSensitive,
		"Runtime registration is currently handled through project activation and capability registration. A dedicated runtime registration flow is not implemented yet.",
	)
}

func NewProjectDisableRuntimeAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	return newProjectDisabledPlaceholderAction(
		detail,
		sourceScreen,
		"runtime.disable",
		"Disable Runtime",
		"Disable project-owned runtime bindings without deleting project files.",
		ActionRiskSensitive,
		"Dedicated runtime disable is not implemented yet. Use project facet deactivation for supported facets.",
	)
}

func NewProjectValidateRuntimeAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	return newProjectDisabledPlaceholderAction(
		detail,
		sourceScreen,
		"runtime.validate",
		"Validate Runtime",
		"Validate project-owned runtime bindings, credentials, and callable capabilities.",
		ActionRiskInspect,
		"Dedicated runtime validation is not implemented yet. Use project automation health and project doctor for now.",
	)
}

func NewProjectInspectArchiveAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	payload := map[string]string{"project_ref": ref}
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.archive.inspect", safeActionID(ref)),
		Label:         "Inspect Archive",
		Description:   "Inspect archived project retention, disabled runtime, and retained storage.",
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project",
		TargetRef:     ref,
		TargetLabel:   projectLabel(project),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectArchiveInspect, Target: ref, Payload: payload},
		RawCommand:    []string{"loom", "project", "archive", "inspect", ref},
		RawDetails:    payload,
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This project row does not include a project ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectRestoreReactivateAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	if state, physical := projects.ParseProjectPhysicalArchiveState(detail.Project.Project.ArchiveState); physical {
		action := newProjectPhysicalMoveAction(detail, sourceScreen, true)
		if projects.ValidateProjectPhysicalArchiveState(state) != nil || state.Phase != projects.ProjectArchivePhaseComplete || state.Restore != nil {
			action.State = ActionDisabled
			action.DisabledReason = "Inspect and recover the existing operation; restored runtime remains inactive."
		}
		return action
	}
	project := detail.Project.Project
	ref := projectRef(project)
	payload := map[string]string{"project_ref": ref}
	action := PortalAction{
		ID:           fmt.Sprintf("project.%s.archive.restore", safeActionID(ref)),
		Label:        "Plan Restore",
		Description:  "Plan a dry-run restore from project archive state without applying file or runtime changes.",
		Domain:       "projects",
		SourceScreen: NormalizeScreen(sourceScreen),
		TargetKind:   "project",
		TargetRef:    ref,
		TargetLabel:  projectLabel(project),
		Risk:         ActionRiskInspect,
		State:        ActionAvailable,
		InputFields: []PortalActionField{
			{Name: "to_node", Label: "To Node", Kind: ActionFieldText, Help: "Node that would receive restored project files."},
			{Name: "dry_run", Label: "Dry Run", Kind: ActionFieldBoolean, Value: "true", Help: "Restore planning is dry-run only in this slice."},
		},
		InputValues:   map[string]string{"dry_run": "true"},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectArchiveRestore, Target: ref, Payload: payload},
		RawCommand:    []string{"loom", "project", "archive", "restore", ref, "--dry-run"},
		RawDetails:    payload,
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This project row does not include a project ref."
	} else if _, physical := projects.ParseProjectPhysicalArchiveState(project.ArchiveState); physical {
		action.State = ActionDisabled
		action.DisabledReason = "Physical restore requires the preserved reviewed plan; this legacy dry-run action does not apply it or activate runtime."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func newProjectDisabledPlaceholderAction(detail projects.ProjectRegistrationDetail, sourceScreen, suffix, label, description string, risk PortalActionRisk, reason string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	payload := map[string]string{
		"project_ref": ref,
		"status":      "not_implemented",
		"reason":      reason,
	}
	action := PortalAction{
		ID:             fmt.Sprintf("project.%s.%s", safeActionID(ref), safeActionID(suffix)),
		Label:          label,
		Description:    description,
		Domain:         "projects",
		SourceScreen:   NormalizeScreen(sourceScreen),
		TargetKind:     "project",
		TargetRef:      ref,
		TargetLabel:    projectLabel(project),
		Risk:           risk,
		State:          ActionDisabled,
		DisabledReason: reason,
		InputValues:    map[string]string{},
		Executor:       PortalActionExecutor{Kind: PortalExecutorUnsupported, Target: ref, Payload: payload},
		RawDetails:     payload,
		RefreshScreen:  NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.DisabledReason = "No selected project is available for this action."
		action.RawDetails["reason"] = action.DisabledReason
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func localProjectRootReadable(root string) bool {
	root = strings.TrimSpace(root)
	if root == "" {
		return false
	}
	info, err := os.Stat(root)
	return err == nil && info.IsDir()
}

func NewProjectInspectAction(project projects.Project, sourceScreen string) PortalAction {
	ref := projectRef(project)
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.inspect", safeActionID(ref)),
		Label:         "Inspect Project",
		Description:   "Inspect project identity, registration, facets, and ownership.",
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project",
		TargetRef:     ref,
		TargetLabel:   projectLabel(project),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectInspect, Target: ref, Payload: projectPayload(project)},
		RawCommand:    []string{"loom", "project", "inspect", ref},
		RawDetails:    projectPayload(project),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This project row does not include a project ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectRepositoryStatusAction(status localclient.ProjectRepositoryStatusResult, sourceScreen string) PortalAction {
	ref := firstNonEmpty(status.Project.Slug, status.Project.ProjectID)
	payload := map[string]string{
		"project_id":               status.Project.ProjectID,
		"lifecycle":                status.Project.Lifecycle,
		"source_posture":           string(status.Source.Posture),
		"project_contract_version": status.Source.ProjectContractSchemaVersion,
		"repos_contract_version":   status.Source.ReposContractSchemaVersion,
		"source_revision":          fmt.Sprintf("%d", status.Source.SourceRevision),
		"freshness":                string(status.Observation.Posture),
		"member_count":             fmt.Sprintf("%d", status.Observation.MemberCount),
		"observed":                 fmt.Sprintf("%d", status.Observation.Observed),
		"not_observed":             fmt.Sprintf("%d", status.Observation.NotObserved),
		"remote_unavailable":       fmt.Sprintf("%d", status.Observation.RemoteUnavailable),
	}
	if !status.ObservedAt.IsZero() {
		payload["observed_at"] = status.ObservedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	action := NewPortalRecordInspectAction("projects", sourceScreen, "repository_status", ref, "Repository Status", payload)
	action.ID = fmt.Sprintf("project.%s.repositories.status", safeActionID(ref))
	action.Label = "Repository Status"
	action.Description = "Inspect explicit repository membership freshness, Git posture, and .repo posture."
	action.TargetKind = "project_repository_status"
	action.TargetRef = ref
	action.RawCommand = []string{"loom", "project", "repos", "status", ref}
	if ref == "" || !statusHasProjectRepositoryIdentity(status) {
		action.State = ActionDisabled
		action.DisabledReason = "Authorized repository state is unavailable for this project load."
	}
	return action
}

func NewProjectRepositoryInspectAction(projectRef string, source localclient.ProjectRepositorySource, repository localclient.ProjectRepositoryItem, sourceScreen string) PortalAction {
	ref := firstNonEmpty(repository.RepositoryID, repository.Key)
	payload := map[string]string{
		"repository_id":            repository.RepositoryID,
		"owner_project_id":         repository.RepositoryOwnerProjectID,
		"key":                      repository.Key,
		"role":                     repository.Role,
		"relative_path":            repository.RelativePath,
		"state_root":               repository.StateRoot,
		"membership_lifecycle":     repository.MembershipLifecycle,
		"repository_lifecycle":     repository.RepositoryLifecycle,
		"freshness":                repository.ObservationPosture,
		"reason_code":              repository.ReasonCode,
		"repo_posture":             string(repository.DevelopmentState.Posture),
		"repo_path":                repository.DevelopmentState.RelativePath,
		"project_contract_version": source.ProjectContractSchemaVersion,
		"repos_contract_version":   source.ReposContractSchemaVersion,
	}
	if repository.ObservedAt != nil {
		payload["observed_at"] = repository.ObservedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if repository.Git != nil {
		payload["git_branch"] = firstNonEmpty(repository.Git.CurrentBranch, "detached")
		payload["git_head"] = repository.Git.Head
		payload["git_default_branch"] = repository.Git.DefaultBranch
		payload["git_upstream"] = repository.Git.Upstream
		payload["git_ahead"] = fmt.Sprintf("%d", repository.Git.Ahead)
		payload["git_behind"] = fmt.Sprintf("%d", repository.Git.Behind)
		payload["git_dirty"] = fmt.Sprintf("%t", repository.Git.Dirty.Dirty)
		payload["git_worktree"] = fmt.Sprintf("%t", repository.Git.Worktree)
		payload["git_worktree_canonical_project_state"] = "false"
	}
	action := NewPortalRecordInspectAction("projects", sourceScreen, "repository", ref, firstNonEmpty(repository.Key, repository.RepositoryID), payload)
	action.ID = fmt.Sprintf("project.%s.repository.%s.inspect", safeActionID(projectRef), safeActionID(ref))
	action.Label = "Inspect Repository"
	action.Description = "Inspect this explicit repository member's bounded source, Git, freshness, and .repo posture."
	action.TargetKind = "project_repository"
	action.TargetRef = ref
	action.RawCommand = []string{"loom", "project", "repos", "inspect", projectRef, ref}
	if projectRef == "" || ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "Project and repository references are required."
	}
	return action
}

func statusHasProjectRepositoryIdentity(status localclient.ProjectRepositoryStatusResult) bool {
	return strings.TrimSpace(status.Project.ProjectID) != "" || strings.TrimSpace(status.Project.Slug) != ""
}

func NewProjectValidateLocalAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	root := ""
	if detail.Registration != nil {
		root = detail.Registration.ProjectRoot
	}
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.validate_local", safeActionID(ref)),
		Label:         "Validate Local Contract",
		Description:   "Validate the registered project folder from this machine when it is readable locally.",
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project",
		TargetRef:     ref,
		TargetLabel:   projectLabel(project),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectValidateLocal, Target: ref, Payload: map[string]string{"project_ref": ref, "project_root": root}},
		RawCommand:    []string{"loom", "project", "validate", root},
		RawDetails:    map[string]string{"project_ref": ref, "project_root": root},
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if root == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This project has no registered local project root."
	} else if !localProjectRootReadable(root) {
		action.State = ActionDisabled
		action.DisabledReason = "The registered project root is not readable from this machine; use backend validation."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectValidateBackendAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	root := ""
	if detail.Registration != nil {
		root = detail.Registration.ProjectRoot
	}
	payload := map[string]string{"project_ref": ref, "project_root": root}
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.validate_backend", safeActionID(ref)),
		Label:         "Validate Contract",
		Description:   "Validate the project contract on the backend owner node.",
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project",
		TargetRef:     ref,
		TargetLabel:   projectLabel(project),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectValidateBackend, Target: ref, Payload: payload},
		RawCommand:    []string{"loom", "project", "validate", ref, "--backend"},
		RawDetails:    payload,
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This project row does not include a project ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectExportActions(detail projects.ProjectRegistrationDetail, sourceScreen string) []PortalAction {
	actions := make([]PortalAction, 0, 6)
	for _, mode := range []projectexport.Mode{projectexport.ModeHuman, projectexport.ModePortable, projectexport.ModeArchival} {
		actions = append(actions,
			newProjectExportAction(detail, sourceScreen, mode, false),
			newProjectExportAction(detail, sourceScreen, mode, true),
		)
	}
	return actions
}

func newProjectExportAction(detail projects.ProjectRegistrationDetail, sourceScreen string, mode projectexport.Mode, backend bool) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	root := ""
	if detail.Registration != nil {
		root = strings.TrimSpace(detail.Registration.ProjectRoot)
	}
	location := "local"
	labelLocation := "Local"
	executor := PortalExecutorProjectExportLocal
	target := root
	if backend {
		location = "backend"
		labelLocation = "From Main"
		executor = PortalExecutorProjectExportBackend
		target = ref
	}
	sourceDescription := "from this readable project folder"
	if backend {
		sourceDescription = "on main, then download its bytes"
	}
	payload := map[string]string{
		"project_ref":  ref,
		"project_root": root,
		"mode":         string(mode),
		"backend":      fmt.Sprintf("%t", backend),
	}
	label := fmt.Sprintf("Export %s (%s)", titleFromToken(string(mode)), labelLocation)
	description := fmt.Sprintf("Create the %s project tar %s and write the archive to a caller-local output path.", mode, sourceDescription)
	rawTarget := root
	if backend {
		rawTarget = ref
	}
	raw := []string{"loom", "project", "export", rawTarget, "--mode", string(mode), "--out", "<archive.tar>"}
	if backend {
		raw = append(raw, "--backend")
	}
	action := PortalAction{
		ID:           fmt.Sprintf("project.%s.export.%s.%s", safeActionID(ref), mode, location),
		Label:        label,
		Description:  description,
		Domain:       "projects",
		SourceScreen: NormalizeScreen(sourceScreen),
		TargetKind:   "project_export",
		TargetRef:    target,
		TargetLabel:  projectLabel(project),
		Risk:         ActionRiskSafeRun,
		State:        ActionAvailable,
		InputFields: []PortalActionField{
			{
				Name:        "output_path",
				Label:       "Output Tar",
				Kind:        ActionFieldPath,
				Required:    true,
				Placeholder: filepath.Join("path", "to", firstNonEmpty(project.Slug, "project")+"-"+string(mode)+".tar"),
				Help:        "The archive is written on the machine running the Portal. Existing files are preserved unless Overwrite is enabled.",
			},
			{
				Name:  "overwrite",
				Label: "Overwrite",
				Kind:  ActionFieldBoolean,
				Value: "false",
				Help:  "Enable only after reviewing the exact existing output path.",
			},
		},
		InputValues:   map[string]string{"overwrite": "false"},
		Executor:      PortalActionExecutor{Kind: executor, Target: target, Payload: payload},
		RawCommand:    raw,
		RawDetails:    payload,
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if backend {
		if ref == "" {
			action.State = ActionDisabled
			action.DisabledReason = "This project row does not include a backend project ref."
		}
	} else if root == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This project has no registered local project root."
	} else if !localProjectRootReadable(root) {
		action.State = ActionDisabled
		action.DisabledReason = "The registered project root is not readable from this machine; use the matching From Main export."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectRegisterBackendAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	root := ""
	label := "Register Contract"
	if detail.Registration != nil {
		root = detail.Registration.ProjectRoot
		label = "Re-register Contract"
	}
	payload := map[string]string{"project_ref": ref, "project_root": root}
	action := PortalAction{
		ID:           fmt.Sprintf("project.%s.register_backend", safeActionID(ref)),
		Label:        label,
		Description:  "Analyze and register the project contract from the backend node filesystem.",
		Domain:       "projects",
		SourceScreen: NormalizeScreen(sourceScreen),
		TargetKind:   "project",
		TargetRef:    ref,
		TargetLabel:  projectLabel(project),
		Risk:         ActionRiskSafeRun,
		State:        ActionAvailable,
		InputFields: []PortalActionField{{
			Name:  "strict",
			Label: "Strict",
			Kind:  ActionFieldBoolean,
			Value: "false",
			Help:  "Fail registration when validation produces warnings.",
		}},
		InputValues:   map[string]string{"strict": "false"},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectRegisterBackend, Target: ref, Payload: payload},
		RawCommand:    []string{"loom", "project", "register", ref, "--backend"},
		RawDetails:    payload,
		RefreshScreen: ScreenProjects,
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This project row does not include a project ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectRegistrationPlanAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.registration_plan", safeActionID(ref)),
		Label:         "View Registration Plan",
		Description:   "Inspect the registered project plan, facets, diagnostics, and workflow runtime surfaces.",
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project",
		TargetRef:     ref,
		TargetLabel:   projectLabel(project),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectRegistrationPlan, Target: ref, Payload: map[string]string{"project_ref": ref}},
		RawCommand:    []string{"loom", "project", "status", ref},
		RawDetails:    map[string]string{"project_ref": ref},
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if detail.Registration == nil {
		action.State = ActionDisabled
		action.DisabledReason = "This project has no registered project contract."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectArchiveAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	return newProjectPhysicalMoveAction(detail, sourceScreen, false)
}

func newLegacyProjectArchiveAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	payload := map[string]string{"project_ref": ref}
	action := PortalAction{
		ID:           fmt.Sprintf("project.%s.archive", safeActionID(ref)),
		Label:        "Archive Project",
		Description:  "Archive project storage and disable project-owned runtime surfaces from the backend.",
		Domain:       "projects",
		SourceScreen: NormalizeScreen(sourceScreen),
		TargetKind:   "project",
		TargetRef:    ref,
		TargetLabel:  projectLabel(project),
		Risk:         ActionRiskSensitive,
		State:        ActionAvailable,
		InputFields: []PortalActionField{
			{Name: "dry_run", Label: "Dry Run", Kind: ActionFieldBoolean, Value: "true", Help: "Preview archive effects without changing backend state."},
			{Name: "skip_storage_archive", Label: "Skip Storage Archive", Kind: ActionFieldBoolean, Value: "false", Help: "Only write runtime archive state and deactivate runtime surfaces."},
			{Name: "source_ref", Label: "Source Ref", Kind: ActionFieldText},
			{Name: "target_path", Label: "Target Path", Kind: ActionFieldText, Placeholder: "main/Archive/Projects/<project>"},
			{Name: "reason", Label: "Reason", Kind: ActionFieldText},
		},
		InputValues: map[string]string{
			"dry_run":              "true",
			"skip_storage_archive": "false",
		},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectArchive, Target: ref, Payload: payload},
		RawCommand:    []string{"loom", "project", "archive", ref, "--dry-run"},
		RawDetails:    payload,
		RefreshScreen: ScreenProjects,
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This project row does not include a project ref."
	} else if detail.Registration == nil {
		action.State = ActionDisabled
		action.DisabledReason = "This project has no registered project contract."
	} else if projectRuntimeQuarantined(project) {
		action.State = ActionDisabled
		action.DisabledReason = projectRuntimeQuarantineReason(project)
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectDoctorAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	root := ""
	if detail.Registration != nil {
		root = detail.Registration.ProjectRoot
	}
	raw := []string{"loom", "project", "doctor", ref, "--use-registered-snapshot"}
	payloadRoot := ""
	if root != "" && localProjectRootReadable(root) {
		raw = []string{"loom", "project", "doctor", ref, "--project-root", root}
		payloadRoot = root
	}
	payload := map[string]string{"project_ref": ref, "project_root": payloadRoot, "registered_project_root": root}
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.doctor", safeActionID(ref)),
		Label:         "Run Project Doctor",
		Description:   "Diagnose registration, activation, drift, and project-owned runtime surfaces.",
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project",
		TargetRef:     ref,
		TargetLabel:   projectLabel(project),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectDoctor, Target: ref, Payload: payload},
		RawCommand:    raw,
		RawDetails:    payload,
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if detail.Registration == nil {
		action.State = ActionDisabled
		action.DisabledReason = "This project has no registered project contract."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectDiffAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	root := ""
	if detail.Registration != nil {
		root = detail.Registration.ProjectRoot
	}
	payloadRoot := ""
	raw := []string{"loom", "project", "diff", ref, "--backend"}
	if localProjectRootReadable(root) {
		payloadRoot = root
		raw = []string{"loom", "project", "diff", root, "--project", ref}
	}
	payload := map[string]string{"project_ref": ref, "project_root": payloadRoot, "registered_project_root": root}
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.diff", safeActionID(ref)),
		Label:         "Show Project Diff",
		Description:   "Compare the backend project contract against the registered snapshot.",
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project",
		TargetRef:     ref,
		TargetLabel:   projectLabel(project),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectDiff, Target: ref, Payload: payload},
		RawCommand:    raw,
		RawDetails:    payload,
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if detail.Registration == nil {
		action.State = ActionDisabled
		action.DisabledReason = "This project has no registered project contract."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectActivateAction(detail projects.ProjectRegistrationDetail, facet string, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	root := ""
	if detail.Registration != nil {
		root = detail.Registration.ProjectRoot
	}
	label := "Activate Project"
	if facet != "" {
		label = "Activate " + titleFromToken(facet)
	}
	rawFacet := strings.ReplaceAll(strings.TrimSpace(facet), "_", "-")
	raw := []string{"loom", "project", "activate", ref}
	if facet != "" {
		raw = append(raw, "--facet", rawFacet)
	}
	if root != "" {
		raw = append(raw, "--project-root", root)
	}
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.activate.%s", safeActionID(ref), safeActionID(firstNonEmpty(facet, "base"))),
		Label:         label,
		Description:   "Activate the project base registration or one supported project facet.",
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project",
		TargetRef:     ref,
		TargetLabel:   projectLabel(project),
		Risk:          ActionRiskSensitive,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectActivate, Target: ref, Payload: map[string]string{"project_ref": ref, "project_root": root, "facet": facet}},
		RawCommand:    raw,
		RawDetails:    map[string]string{"project_ref": ref, "project_root": root, "facet": facet},
		RefreshScreen: ScreenProjects,
	}
	if detail.Registration == nil {
		action.State = ActionDisabled
		action.DisabledReason = "This project has no registered project contract."
	} else if projectRuntimeQuarantined(project) {
		action.State = ActionDisabled
		action.DisabledReason = projectRuntimeQuarantineReason(project)
	} else if reason := projectActivationDependencyDisabledReason(detail, facet); reason != "" {
		action.State = ActionDisabled
		action.DisabledReason = reason
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectDeactivateAction(detail projects.ProjectRegistrationDetail, facet string, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	normalizedFacet := strings.ReplaceAll(strings.TrimSpace(facet), "-", "_")
	label := "Deactivate " + titleFromToken(normalizedFacet)
	rawFacet := normalizedFacet
	if rawFacet == "direct_events" {
		rawFacet = "direct-events"
	}
	if rawFacet == "watched_roots" {
		rawFacet = "watched-roots"
	}
	raw := []string{"loom", "project", "deactivate", ref, "--facet", rawFacet}
	payload := map[string]string{"project_ref": ref, "facet": normalizedFacet}
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.deactivate.%s", safeActionID(ref), safeActionID(firstNonEmpty(normalizedFacet, "facet"))),
		Label:         label,
		Description:   "Deactivate this project-owned facet without deleting history.",
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project",
		TargetRef:     ref,
		TargetLabel:   projectLabel(project),
		Risk:          ActionRiskSensitive,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectDeactivate, Target: ref, Payload: payload},
		RawCommand:    raw,
		RawDetails:    payload,
		RefreshScreen: ScreenProjects,
	}
	if detail.Registration == nil {
		action.State = ActionDisabled
		action.DisabledReason = "This project has no registered project contract."
	} else if normalizedFacet == "" {
		action.State = ActionDisabled
		action.DisabledReason = "A project facet is required."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectAutomationHealthAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.automation_health", safeActionID(ref)),
		Label:         "View Automation Health",
		Description:   "Inspect project contract, activation, capabilities, schedules, events, jobs, runtime bindings, and data policy in one composed view.",
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project",
		TargetRef:     ref,
		TargetLabel:   projectLabel(project),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectAutomationHealth, Target: ref, Payload: map[string]string{"project_ref": ref}},
		RawCommand:    []string{"loom", "project", "status", ref},
		RawDetails:    map[string]string{"project_ref": ref, "surface": "automation_health"},
		RefreshScreen: ScreenProjects,
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This project row does not include a project ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectScaffoldCleanupAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	root := ""
	if detail.Registration != nil {
		root = detail.Registration.ProjectRoot
	}
	payload := map[string]string{"project_ref": ref, "project_root": root}
	action := PortalAction{
		ID:           fmt.Sprintf("project.%s.scaffold_cleanup", safeActionID(ref)),
		Label:        "Clean Scaffold Examples",
		Description:  "Remove untouched generated example packages while preserving edited examples.",
		Domain:       "projects",
		SourceScreen: NormalizeScreen(sourceScreen),
		TargetKind:   "project",
		TargetRef:    ref,
		TargetLabel:  projectLabel(project),
		Risk:         ActionRiskSensitive,
		State:        ActionAvailable,
		InputFields: []PortalActionField{{
			Name:  "dry_run",
			Label: "Dry Run",
			Kind:  ActionFieldBoolean,
			Value: "true",
			Help:  "Preview cleanup first. Set false to remove untouched scaffold examples.",
		}},
		InputValues:   map[string]string{"dry_run": "true"},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectScaffoldCleanup, Target: ref, Payload: payload},
		RawCommand:    []string{"loom", "project", "scaffold", "cleanup", root, "--dry-run"},
		RawDetails:    payload,
		RefreshScreen: ScreenProjects,
	}
	if root == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This project has no registered local project root."
	} else if !localProjectRootReadable(root) {
		action.State = ActionDisabled
		action.DisabledReason = "The registered project root is not readable from this machine."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectFacetInspectAction(detail projects.ProjectRegistrationDetail, facet projects.ProjectContractFacet, sourceScreen string) PortalAction {
	ref := projectRef(detail.Project.Project)
	payload := map[string]string{
		"project_ref": ref,
		"facet":       facet.FacetKey,
		"status":      facet.FacetStatus,
		"folder":      facet.Folder,
		"enabled":     fmt.Sprintf("%t", facet.Enabled),
		"present":     fmt.Sprintf("%t", facet.Present),
		"placeholder": fmt.Sprintf("%t", facet.Placeholder),
	}
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.facet.%s.inspect", safeActionID(ref), safeActionID(facet.FacetKey)),
		Label:         "Inspect " + titleFromToken(facet.FacetKey),
		Description:   "Inspect this project facet registration state.",
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project_facet",
		TargetRef:     facet.FacetKey,
		TargetLabel:   facet.FacetKey,
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProjectFacetInspect, Target: facet.FacetKey, Payload: payload},
		RawDetails:    payload,
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectWorkflowInspectAction(detail projects.ProjectRegistrationDetail, workflow ProjectWorkflowIntent, sourceScreen string) PortalAction {
	projectRef := projectRef(detail.Project.Project)
	workflowRef := firstNonEmpty(workflow.WorkflowID, workflow.Name)
	payload := projectWorkflowPayload(projectRef, workflow)
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.workflow.%s.inspect", safeActionID(projectRef), safeActionID(workflowRef)),
		Label:         "Inspect Workflow",
		Description:   "Inspect this project workflow contract and registered runtime surface.",
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project_workflow",
		TargetRef:     workflowRef,
		TargetLabel:   firstNonEmpty(workflow.Name, workflow.WorkflowID, workflowRef),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorRecordInspect, Target: workflowRef, Payload: payload},
		RawCommand:    []string{"loom", "project", "workflows", "inspect", projectRef, workflowRef},
		RawDetails:    payload,
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if projectRef == "" || workflowRef == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This workflow row does not include a project and workflow ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectWorkflowCapabilityInspectAction(detail projects.ProjectRegistrationDetail, workflow ProjectWorkflowIntent, sourceScreen string) PortalAction {
	projectRef := projectRef(detail.Project.Project)
	capabilityRef := strings.TrimSpace(workflow.CapabilityAddress)
	payload := projectWorkflowPayload(projectRef, workflow)
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.workflow.%s.capability.inspect", safeActionID(projectRef), safeActionID(firstNonEmpty(capabilityRef, workflow.WorkflowID))),
		Label:         "Inspect Workflow Capability",
		Description:   "Inspect the capability endpoint registered for this workflow.",
		Domain:        "capabilities",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "capability",
		TargetRef:     capabilityRef,
		TargetLabel:   firstNonEmpty(capabilityRef, workflow.Name, workflow.WorkflowID),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorCapabilityInspect, Target: capabilityRef, Payload: payload},
		RawCommand:    []string{"loom", "capability", "inspect", capabilityRef},
		RawDetails:    payload,
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if capabilityRef == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This workflow has no registered capability URL."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectWorkflowCapabilityCallAction(detail projects.ProjectRegistrationDetail, workflow ProjectWorkflowIntent, sourceScreen string) PortalAction {
	projectRef := projectRef(detail.Project.Project)
	capabilityRef := strings.TrimSpace(workflow.CapabilityAddress)
	payload := projectWorkflowPayload(projectRef, workflow)
	action := PortalAction{
		ID:           fmt.Sprintf("project.%s.workflow.%s.capability.call", safeActionID(projectRef), safeActionID(firstNonEmpty(capabilityRef, workflow.WorkflowID))),
		Label:        "Call Workflow Capability",
		Description:  "Call this workflow through the normal capability router with JSON input.",
		Domain:       "capabilities",
		SourceScreen: NormalizeScreen(sourceScreen),
		TargetKind:   "capability",
		TargetRef:    capabilityRef,
		TargetLabel:  firstNonEmpty(capabilityRef, workflow.Name, workflow.WorkflowID),
		Risk:         ActionRiskSensitive,
		State:        ActionAvailable,
		InputFields: []PortalActionField{{
			Name:        "input_json",
			Label:       "Input JSON",
			Kind:        ActionFieldTextareaJSON,
			Required:    false,
			Value:       "{}",
			Placeholder: "{}",
			Help:        "Minimal JSON input sent to the workflow capability.",
		}},
		InputValues:   map[string]string{"input_json": "{}"},
		Executor:      PortalActionExecutor{Kind: PortalExecutorCapabilityCall, Target: capabilityRef, Payload: payload},
		RawCommand:    []string{"loom", "capability", "call", capabilityRef, "--input", "{}"},
		RawDetails:    payload,
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if capabilityRef == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This workflow has no registered capability URL."
	} else if workflow.RuntimeKind == "" || workflow.RuntimeBindingID == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This workflow has no active runtime binding yet."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectWorkflowJobsAction(detail projects.ProjectRegistrationDetail, workflow ProjectWorkflowIntent, sourceScreen string) PortalAction {
	projectRef := projectRef(detail.Project.Project)
	payload := projectWorkflowPayload(projectRef, workflow)
	raw := []string{"loom", "jobs", "list", "--project", projectRef, "--type", "workflow_run"}
	if workflow.WorkflowPackageID != "" {
		raw = append(raw, "--workflow", workflow.WorkflowPackageID)
	}
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.workflow.%s.jobs", safeActionID(projectRef), safeActionID(firstNonEmpty(workflow.WorkflowID, workflow.Name, "workflow"))),
		Label:         "Open Workflow Jobs",
		Description:   "Open recent jobs and use the filtered raw command for this workflow.",
		Domain:        "jobs",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "job",
		TargetRef:     projectRef,
		TargetLabel:   firstNonEmpty(workflow.Name, workflow.WorkflowID, projectRef),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorNavigate, Target: ScreenJobs, Payload: payload},
		RawCommand:    raw,
		RawDetails:    payload,
		RefreshScreen: ScreenJobs,
	}
	if projectRef == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This workflow row does not include a project ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProjectWatchPlanAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	return newProjectStatusAction(detail, sourceScreen, "watch_plan", "View Watch Plan", "Inspect desired watched roots from the registered project contract.", PortalExecutorProjectWatchPlan, []string{"loom", "project", "watch-plan", projectRef(detail.Project.Project), "--use-registered-snapshot"})
}

func NewProjectSyncStatusAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	return newProjectStatusAction(detail, sourceScreen, "sync_status", "View Sync Status", "Inspect project sync status across watched roots.", PortalExecutorProjectSyncStatus, []string{"loom", "project", "sync-status", projectRef(detail.Project.Project)})
}

func NewProjectBackupStatusAction(detail projects.ProjectRegistrationDetail, sourceScreen string) PortalAction {
	return newProjectStatusAction(detail, sourceScreen, "backup_status", "View Backup Status", "Inspect project backup status across watched roots.", PortalExecutorProjectBackupStatus, []string{"loom", "project", "backup-status", projectRef(detail.Project.Project)})
}

func newProjectStatusAction(detail projects.ProjectRegistrationDetail, sourceScreen, suffix, label, description, executor string, raw []string) PortalAction {
	project := detail.Project.Project
	ref := projectRef(project)
	action := PortalAction{
		ID:            fmt.Sprintf("project.%s.%s", safeActionID(ref), suffix),
		Label:         label,
		Description:   description,
		Domain:        "projects",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "project",
		TargetRef:     ref,
		TargetLabel:   projectLabel(project),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: executor, Target: ref, Payload: map[string]string{"project_ref": ref}},
		RawCommand:    raw,
		RawDetails:    map[string]string{"project_ref": ref},
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if detail.Registration == nil {
		action.State = ActionDisabled
		action.DisabledReason = "This project has no registered project contract."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewRuntimeBindingInspectAction(binding capabilities.RuntimeBindingInspection, sourceScreen string) PortalAction {
	ref := binding.Binding.RuntimeBindingID
	payload := map[string]string{
		"runtime_binding_id": ref,
		"runtime_kind":       binding.Binding.RuntimeKind,
		"status":             binding.Binding.Status,
		"capability":         binding.Endpoint.CompactAddress,
		"provider":           binding.Provider.CompactAddress,
	}
	action := PortalAction{
		ID:            fmt.Sprintf("capability.runtime_binding.%s.inspect", safeActionID(ref)),
		Label:         "Inspect Runtime Binding",
		Description:   "Inspect runtime binding status and implementation kind.",
		Domain:        "capabilities",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "runtime_binding",
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(binding.Endpoint.CompactAddress, ref),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorRuntimeBindingInspect, Target: ref, Payload: payload},
		RawCommand:    []string{"loom", "capability", "runtime-binding", "inspect", ref},
		RawDetails:    payload,
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This runtime binding row does not include a binding ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func projectPayload(project projects.Project) map[string]string {
	return map[string]string{
		"project_id":        project.ProjectID,
		"project_scope_id":  project.ProjectScopeID,
		"project_scope_key": project.ProjectScopeKey,
		"slug":              project.Slug,
		"name":              project.Name,
		"home_node_id":      ptrOrDash(project.HomeNodeID),
		"status":            project.Status,
		"project_type":      project.ProjectType,
	}
}

func projectWorkflowPayload(projectRef string, workflow ProjectWorkflowIntent) map[string]string {
	return map[string]string{
		"project_ref":         projectRef,
		"workflow_id":         workflow.WorkflowID,
		"name":                workflow.Name,
		"status":              workflow.Status,
		"implementation_kind": workflow.ImplementationKind,
		"activation_status":   workflow.ActivationStatus,
		"capability_address":  workflow.CapabilityAddress,
		"runtime_kind":        workflow.RuntimeKind,
		"runtime_binding_id":  workflow.RuntimeBindingID,
		"workflow_package_id": workflow.WorkflowPackageID,
		"workflow_version_id": workflow.WorkflowVersionID,
		"executable":          fmt.Sprintf("%t", workflow.Executable),
		"placeholder":         fmt.Sprintf("%t", workflow.Placeholder),
		"source":              workflow.Source,
	}
}

func projectActivationDependencyDisabledReason(detail projects.ProjectRegistrationDetail, facet string) string {
	facet = strings.ReplaceAll(strings.TrimSpace(facet), "-", "_")
	if facet != "schedules" && facet != "direct_events" {
		return ""
	}
	if detail.Registration == nil || len(detail.Registration.RegistrationPlan) == 0 {
		return ""
	}
	var plan projectcontracts.ProjectPlan
	if err := json.Unmarshal(detail.Registration.RegistrationPlan, &plan); err != nil {
		return ""
	}
	report := projectcontracts.ValidationReport{
		DerivedProviders: plan.DerivedProviders,
		Scripts:          plan.Scripts,
		Workflows:        plan.Workflows,
		Connectors:       plan.Connectors,
		Modules:          plan.Modules,
		Schedules:        plan.Schedules,
		DirectEvents:     plan.DirectEvents,
	}
	if facet == "schedules" {
		for _, schedule := range report.Schedules {
			if strings.TrimSpace(schedule.ManifestPath) == "" || strings.TrimSpace(schedule.ContractStatus) == "disabled" {
				continue
			}
			if reason := projectTargetDependencyReason(detail, report, "schedule", schedule.Key, schedule.TargetCapability); reason != "" {
				return reason
			}
		}
		return ""
	}
	for _, event := range report.DirectEvents {
		if strings.TrimSpace(event.ManifestPath) == "" || strings.TrimSpace(event.ContractStatus) == "disabled" {
			continue
		}
		if reason := projectTargetDependencyReason(detail, report, "direct event", event.Key, event.TargetCapability); reason != "" {
			return reason
		}
	}
	return ""
}

func projectTargetDependencyReason(detail projects.ProjectRegistrationDetail, report projectcontracts.ValidationReport, kind, key, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Sprintf("%s %s has no target capability.", kind, key)
	}
	address, err := capabilities.ParseAddress(target)
	if err != nil {
		return fmt.Sprintf("%s %s target capability is invalid.", kind, key)
	}
	normalized := address.CompactAddress
	if projectActiveCapabilityURL(detail, normalized) {
		return ""
	}
	localCapabilities := projectcontracts.LocalCapabilitySources(&report)
	if source := localCapabilities[normalized]; source.Facet != "" {
		if projectLocalCapabilitySourceActive(detail, source) {
			return ""
		}
		return fmt.Sprintf("%s %s needs %s from the %s facet. Activate %s first or use Activate All.", titleFromToken(kind), key, normalized, source.Facet, source.Facet)
	}
	localProviders := projectcontracts.LocalProviderSources(&report)
	providerAddress := address.ScopePath + "@" + address.ProviderKey
	if localProviders[providerAddress] != "" {
		return fmt.Sprintf("%s %s points at project-local provider %s, but that capability is not declared or active.", titleFromToken(kind), key, providerAddress)
	}
	return ""
}

func projectActiveCapabilityURL(detail projects.ProjectRegistrationDetail, target string) bool {
	target, err := capabilities.NormalizeAddress(target)
	if err != nil {
		return false
	}
	for _, exposure := range detail.ScriptExposures {
		if exposure.ActivationStatus != projects.ProjectScriptExposureStatusActive {
			continue
		}
		if normalized, err := capabilities.NormalizeAddress(exposure.CapabilityAddress); err == nil && normalized == target {
			return true
		}
	}
	for _, workflow := range detail.WorkflowRegistrations {
		if workflow.ActivationStatus != projects.ProjectWorkflowRegistrationStatusActive {
			continue
		}
		if normalized, err := capabilities.NormalizeAddress(workflow.CapabilityAddress); err == nil && normalized == target {
			return true
		}
	}
	return false
}

func projectLocalCapabilitySourceActive(detail projects.ProjectRegistrationDetail, source projectcontracts.LocalCapabilitySource) bool {
	switch source.Facet {
	case "scripts":
		for _, exposure := range detail.ScriptExposures {
			if exposure.ScriptKey == source.Key && exposure.ActivationStatus == projects.ProjectScriptExposureStatusActive {
				return true
			}
		}
	case "workflows":
		for _, workflow := range detail.WorkflowRegistrations {
			if workflow.WorkflowKey == source.Key && workflow.ActivationStatus == projects.ProjectWorkflowRegistrationStatusActive {
				return true
			}
		}
	case "connectors":
		for _, connector := range detail.ConnectorRegistrations {
			if connector.ConnectorKey == source.Key && connector.ActivationStatus == projects.ProjectConnectorRegistrationStatusActive {
				return true
			}
		}
	case "modules":
		for _, module := range detail.ModuleRegistrations {
			if module.ModuleKey == source.Key && module.ActivationStatus == projects.ProjectModuleRegistrationStatusRegistered {
				return true
			}
		}
	}
	return false
}
