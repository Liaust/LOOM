package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func Repair(input RepairInput) (RepairResult, error) {
	if len(input.FixIDs) == 0 {
		input.FixIDs = []string{RepairAllSafe}
	}
	statusInput := input.StatusInput
	if statusInput.LaunchdRunner == nil {
		statusInput.LaunchdRunner = input.LaunchdRunner
	}
	report, err := Doctor(DoctorInput{StatusInput: statusInput})
	if err != nil {
		return RepairResult{}, err
	}
	requested := normalizeRepairIDs(input.FixIDs)
	actions := selectRepairActions(requested, report)
	result := RepairResult{
		DryRun:    input.DryRun,
		Requested: requested,
		Actions:   actions,
		Doctor:    report,
	}
	if len(actions) == 0 {
		return result, nil
	}
	if !input.DryRun && !input.Yes {
		result.Refused = true
		if input.NoInteractive {
			result.Refusal = "setup repair is non-interactive; pass --yes to perform safe repairs"
		} else {
			result.Refusal = "setup repair requires --yes before mutating local paths"
		}
		return result, nil
	}
	for _, action := range actions {
		changes, skipped, err := runRepairAction(action, report.Status, input.DryRun, input.LaunchdRunner)
		result.Changed = append(result.Changed, changes...)
		result.Skipped = append(result.Skipped, skipped...)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func normalizeRepairIDs(ids []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		for _, part := range strings.Split(id, ",") {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			out = append(out, part)
			seen[part] = true
		}
	}
	return out
}

func selectRepairActions(requested []string, report DoctorReport) []RepairAction {
	byID := map[string]RepairAction{}
	for _, action := range report.Repairs {
		byID[action.ID] = action
	}
	status := report.Status
	out := []RepairAction{}
	seen := map[string]bool{}
	for _, id := range requested {
		if id == RepairAllSafe {
			for _, action := range report.Repairs {
				if action.Safe && !action.Destructive && !seen[action.ID] {
					out = append(out, action)
					seen[action.ID] = true
				}
			}
			continue
		}
		action, ok := byID[id]
		if !ok {
			action, ok = repairActionForID(id, status)
		}
		if ok && action.Safe && !action.Destructive && !seen[action.ID] {
			out = append(out, action)
			seen[action.ID] = true
		}
	}
	return out
}

func repairActionForID(id string, status SetupStatus) (RepairAction, bool) {
	if id == RepairInstallLaunchAgent {
		action := RepairAction{
			ID:          id,
			Title:       "Install and load LaunchAgent",
			Description: launchAgentRepairPath(status),
			Safe:        true,
			Destructive: false,
			RequiresYes: true,
			Steps:       []SetupStep{repairStep(id, "service", "Install and load LaunchAgent", launchAgentRepairPath(status))},
		}
		return action, true
	}
	if id == RepairRefreshWorkspaceBinaryLinks {
		targets := workspaceBinaryLinkTargets(status)
		if len(targets) == 0 {
			return RepairAction{}, false
		}
		action := RepairAction{
			ID:          id,
			Title:       "Refresh workspace binary symlinks",
			Description: "Relink ~/.local/bin workspace commands to the active LOOM workspace release.",
			Safe:        true,
			Destructive: false,
			RequiresYes: true,
		}
		for _, target := range targets {
			action.Steps = append(action.Steps, repairStep(id, "binaries", "Refresh "+target.Name+" symlink", target.LinkPath))
			action.Steps[len(action.Steps)-1].Metadata["target"] = target.TargetPath
		}
		return action, true
	}
	if id == RepairMainHumanLinks {
		links := mainHumanLinkRepairTargets(status)
		if len(links) == 0 {
			return RepairAction{}, false
		}
		action := RepairAction{
			ID:          id,
			Title:       "Repair main human storage links",
			Description: "Create canonical main human links and remove known legacy link names when safe.",
			Safe:        true,
			Destructive: false,
			RequiresYes: true,
		}
		for _, link := range links {
			stepTitle := "Repair " + link.Label
			if link.Intent == humanLinkIntentSymlink {
				stepTitle = "Link " + link.Label
			}
			action.Steps = append(action.Steps, repairStep(id, "human_links", stepTitle, link.LinkPath))
			step := &action.Steps[len(action.Steps)-1]
			step.Metadata["intent"] = link.Intent
			step.Metadata["target"] = link.TargetPath
			step.Metadata["key"] = link.Key
		}
		return action, true
	}
	if id == RepairMainBoxServiceACL {
		targets := mainBoxServiceACLTargets(status)
		if len(targets) == 0 {
			return RepairAction{}, false
		}
		action := RepairAction{
			ID:          id,
			Title:       "Repair main Box service ACLs",
			Description: "Grant the LOOM service user minimal traverse/read access to the configured home-based Box path.",
			Safe:        true,
			Destructive: false,
			RequiresYes: true,
		}
		for _, target := range targets {
			action.Steps = append(action.Steps, repairStep(id, "acl", "Set service ACL", target.Path))
			step := &action.Steps[len(action.Steps)-1]
			step.Metadata["access"] = target.Access
			step.Metadata["service_user"] = target.User
		}
		return action, true
	}
	path, title := repairPathAndTitle(id, status)
	if path == "" && id != RepairRewriteRedactedManifest {
		return RepairAction{}, false
	}
	action := RepairAction{
		ID:          id,
		Title:       title,
		Safe:        true,
		Destructive: false,
		RequiresYes: true,
	}
	if id == RepairRewriteRedactedManifest {
		action.Title = "Rewrite redacted setup manifest"
		action.Description = status.Manifest.Path
		action.Steps = []SetupStep{repairStep(id, "manifest", "Rewrite install manifest without raw secrets", status.Manifest.Path)}
		return action, true
	}
	action.Description = path
	action.Steps = []SetupStep{repairStep(id, "paths", title, path)}
	return action, true
}

func repairPathAndTitle(id string, status SetupStatus) (string, string) {
	paths := map[string]string{}
	for _, pathStatus := range status.Paths {
		paths[pathStatus.Key] = pathStatus.Path
	}
	switch id {
	case RepairCreateMissingConfigDir:
		return paths["config_dir"], "Create missing config directory"
	case RepairCreateMissingDataDir:
		return paths["data_dir"], "Create missing data directory"
	case RepairCreateMissingStateDir:
		return paths["state_dir"], "Create missing state directory"
	case RepairCreateMissingLogDir:
		return paths["log_dir"], "Create missing log directory"
	case RepairCreateMissingBoxRoot:
		return status.Box.Path, "Create missing LOOM Box root"
	case RepairCreateMissingBoxLoomDir:
		return status.Box.LoomDir, "Create missing LOOM Box metadata directory"
	case RepairCreateMissingBoxStateRoot:
		return paths["box_state_root"], "Create missing external Box runtime-state directory"
	case RepairCreateMissingServiceRoot:
		return paths["service_root"], "Create missing canonical LOOM service root"
	case RepairCreateMissingStorageRoot:
		return paths["storage_root"], "Create missing canonical physical Storage root"
	case RepairCreateMissingImportsRoot:
		return paths["imports_root"], "Create missing canonical imports root"
	case RepairCreateMissingUserBackupsRoot:
		return paths["user_backups_root"], "Create missing canonical user-backups root"
	case RepairCreateMissingArchiveRoot:
		return paths["archive_root"], "Create missing canonical archive root"
	case RepairCreateMissingGeneratedRoot:
		return paths["generated_root"], "Create missing generated-artifact root"
	case RepairCreateMissingMainDocuments:
		return paths["main_documents"], "Create missing main Documents import directory"
	case RepairCreateMissingStorageExport:
		return "", ""
	case RepairCreateMissingNodeAgentDataDir:
		return status.NodeAgent.DataDir, "Create missing node-agent data directory"
	default:
		return "", ""
	}
}

func launchAgentRepairPath(status SetupStatus) string {
	for _, service := range status.Services {
		if service.Manager == ServiceManagerLaunchd && strings.TrimSpace(service.Path) != "" {
			return service.Path
		}
	}
	if status.Plan != nil {
		return status.Plan.Paths.LaunchAgentPlistPath
	}
	return ""
}

func repairStep(id, category, title, path string) SetupStep {
	return SetupStep{
		ID:       id,
		Category: category,
		Title:    title,
		Required: true,
		Mutating: true,
		Status:   StepStatusWouldChange,
		Action:   "safe local repair",
		Metadata: map[string]any{"path": path},
	}
}

func runRepairAction(action RepairAction, status SetupStatus, dryRun bool, runner LaunchdRunner) ([]RepairChange, []RepairChange, error) {
	if action.ID == RepairRewriteRedactedManifest {
		return runManifestRepair(action, status, dryRun)
	}
	if action.ID == RepairInstallLaunchAgent {
		return runLaunchAgentRepair(action, status, dryRun, runner)
	}
	if action.ID == RepairRefreshWorkspaceBinaryLinks {
		return runWorkspaceBinaryLinkRepair(action, status, dryRun)
	}
	if action.ID == RepairMainHumanLinks {
		return runMainHumanLinksRepair(action, status, dryRun)
	}
	if action.ID == RepairMainBoxServiceACL {
		return runMainBoxServiceACLRepair(action, status, dryRun)
	}
	path := ""
	if len(action.Steps) > 0 {
		if raw, ok := action.Steps[0].Metadata["path"].(string); ok {
			path = raw
		}
	}
	if strings.TrimSpace(path) == "" {
		return nil, []RepairChange{{RepairID: action.ID, Status: "skipped", Message: "repair has no path"}}, nil
	}
	if dryRun {
		return []RepairChange{{RepairID: action.ID, Status: "would_create", Path: path}}, nil, nil
	}
	mode := repairDirectoryMode(action.ID)
	if err := os.MkdirAll(path, mode); err != nil {
		return nil, nil, fmt.Errorf("repair %s: create %s: %w", action.ID, path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return nil, nil, fmt.Errorf("repair %s: chmod %s: %w", action.ID, path, err)
	}
	return []RepairChange{{RepairID: action.ID, Status: "created", Path: path}}, nil, nil
}

func repairDirectoryMode(repairID string) os.FileMode {
	if repairID == RepairCreateMissingConfigDir || repairID == RepairCreateMissingBoxStateRoot || repairID == RepairCreateMissingGeneratedRoot {
		return 0o750
	}
	if repairID == RepairCreateMissingStorageRoot || repairID == RepairCreateMissingArchiveRoot {
		return 0o2750
	}
	if repairID == RepairCreateMissingImportsRoot || repairID == RepairCreateMissingUserBackupsRoot {
		return 0o2770
	}
	if repairID == RepairCreateMissingServiceRoot {
		return 0o755
	}
	return 0o700
}

type serviceACLTarget struct {
	Path   string
	Access string
	User   string
}

func mainBoxServiceACLTargets(status SetupStatus) []serviceACLTarget {
	if status.Box.Path == "" {
		return nil
	}
	serviceUser := "loom"
	home := ""
	if status.Plan != nil {
		home = strings.TrimSpace(status.Plan.Spec.HomeDir)
	}
	if home == "" {
		home = filepath.Dir(status.Box.Path)
	}
	targets := []serviceACLTarget{}
	if home != "" && home != "." && home != "/" {
		targets = append(targets, serviceACLTarget{Path: filepath.Clean(home), Access: "--x", User: serviceUser})
	}
	targets = append(targets, serviceACLTarget{Path: filepath.Clean(status.Box.Path), Access: "r-x", User: serviceUser})
	if status.Box.LoomDir != "" {
		targets = append(targets, serviceACLTarget{Path: filepath.Clean(status.Box.LoomDir), Access: "rwx", User: serviceUser})
	}
	return targets
}

func runMainBoxServiceACLRepair(action RepairAction, status SetupStatus, dryRun bool) ([]RepairChange, []RepairChange, error) {
	targets := mainBoxServiceACLTargets(status)
	if len(targets) == 0 {
		return nil, []RepairChange{{RepairID: action.ID, Status: "skipped", Message: "no Box path is configured"}}, nil
	}
	changed := []RepairChange{}
	for _, target := range targets {
		if strings.TrimSpace(target.Path) == "" {
			continue
		}
		if dryRun {
			changed = append(changed, RepairChange{RepairID: action.ID, Status: "would_set_acl", Path: target.Path, Message: aclSpec(target)})
			continue
		}
		if _, err := exec.LookPath("setfacl"); err != nil {
			return changed, []RepairChange{{RepairID: action.ID, Status: "skipped", Message: "setfacl is not available on this system"}}, nil
		}
		arg := aclSpec(target)
		if output, err := exec.Command("setfacl", "-m", arg, target.Path).CombinedOutput(); err != nil {
			return changed, nil, fmt.Errorf("repair %s: setfacl %s on %s: %w: %s", action.ID, arg, target.Path, err, strings.TrimSpace(string(output)))
		}
		changed = append(changed, RepairChange{RepairID: action.ID, Status: "acl_set", Path: target.Path, Message: arg})
	}
	return changed, nil, nil
}

func aclSpec(target serviceACLTarget) string {
	return "u:" + target.User + ":" + target.Access + ",m::" + target.Access
}

func mainHumanLinkRepairTargets(status SetupStatus) []HumanLinkPlan {
	if status.Plan == nil {
		return nil
	}
	statuses := map[string]HumanLinkStatus{}
	for _, item := range status.HumanLinks {
		statuses[item.Key] = item
	}
	out := []HumanLinkPlan{}
	for _, link := range status.Plan.Paths.HumanLinks {
		if current, ok := statuses[link.Key]; ok && !humanLinkNeedsRepair(current) {
			continue
		}
		out = append(out, link)
	}
	return out
}

func runMainHumanLinksRepair(action RepairAction, status SetupStatus, dryRun bool) ([]RepairChange, []RepairChange, error) {
	if status.Plan == nil {
		return nil, []RepairChange{{RepairID: action.ID, Status: "skipped", Message: "no setup plan available"}}, nil
	}
	links := mainHumanLinkRepairTargets(status)
	if len(links) == 0 {
		return nil, []RepairChange{{RepairID: action.ID, Status: "already_satisfied", Message: "main human links are already repaired"}}, nil
	}
	plan := *status.Plan
	plan.Paths.HumanLinks = links
	changes, err := applyHumanLinks(plan, dryRun)
	if err != nil {
		return nil, nil, err
	}
	changed := []RepairChange{}
	skipped := []RepairChange{}
	convert := func(change ApplyChange) RepairChange {
		statusValue := change.Status
		switch statusValue {
		case ApplyStatusWouldChange:
			statusValue = "would_change"
		case ApplyStatusChanged:
			statusValue = "changed"
		case ApplyStatusAlreadySatisfied:
			statusValue = "already_satisfied"
		case ApplyStatusSkipped:
			statusValue = "skipped"
		case ApplyStatusBlocked:
			statusValue = "blocked"
		}
		return RepairChange{RepairID: action.ID, Status: statusValue, Path: change.Path, Message: change.Message}
	}
	for _, change := range changes.changed {
		changed = append(changed, convert(change))
	}
	for _, change := range changes.blocked {
		changed = append(changed, convert(change))
	}
	for _, change := range changes.skipped {
		skipped = append(skipped, convert(change))
	}
	return changed, skipped, nil
}

type workspaceBinaryLinkTarget struct {
	Name       string
	LinkPath   string
	TargetPath string
}

func workspaceBinaryLinkTargets(status SetupStatus) []workspaceBinaryLinkTarget {
	manifest := status.Manifest.Manifest
	if manifest == nil || manifest.NodeKind != "workspace" {
		return nil
	}
	home := strings.TrimSpace(manifest.HomeDir)
	dataDir := strings.TrimSpace(manifest.DataDir)
	if status.Plan != nil {
		home = firstNonEmpty(home, status.Plan.Spec.HomeDir)
		dataDir = firstNonEmpty(dataDir, status.Plan.Paths.DataDir)
	}
	if home == "" || dataDir == "" {
		return nil
	}
	currentPath := filepath.Join(filepath.Clean(dataDir), "current")
	localBinDir := filepath.Join(filepath.Clean(home), ".local", "bin")
	names := []string{"loom"}
	if manifest.NodeKind != "main" {
		names = append(names, "loom-node-agent")
	}
	targets := make([]workspaceBinaryLinkTarget, 0, len(names))
	for _, name := range names {
		targets = append(targets, workspaceBinaryLinkTarget{
			Name:       name,
			LinkPath:   filepath.Join(localBinDir, name),
			TargetPath: filepath.Join(currentPath, "bin", name),
		})
	}
	return targets
}

func runWorkspaceBinaryLinkRepair(action RepairAction, status SetupStatus, dryRun bool) ([]RepairChange, []RepairChange, error) {
	targets := workspaceBinaryLinkTargets(status)
	if len(targets) == 0 {
		return nil, []RepairChange{{RepairID: action.ID, Status: "skipped", Message: "no workspace binary link targets available"}}, nil
	}
	changed := []RepairChange{}
	skipped := []RepairChange{}
	for _, target := range targets {
		if dryRun {
			changed = append(changed, RepairChange{RepairID: action.ID, Status: "would_link", Path: target.LinkPath, Message: target.TargetPath})
			continue
		}
		info, err := os.Stat(target.TargetPath)
		if err != nil {
			skipped = append(skipped, RepairChange{RepairID: action.ID, Status: "skipped", Path: target.LinkPath, Message: "target binary missing: " + target.TargetPath})
			continue
		}
		if info.IsDir() {
			skipped = append(skipped, RepairChange{RepairID: action.ID, Status: "skipped", Path: target.LinkPath, Message: "target binary is a directory: " + target.TargetPath})
			continue
		}
		change, err := replaceSymlinkChange("repair_link_"+target.Name, target.LinkPath, target.TargetPath)
		if err != nil {
			return changed, skipped, err
		}
		switch change.Status {
		case ApplyStatusAlreadySatisfied, ApplyStatusSkipped:
			skipped = append(skipped, RepairChange{RepairID: action.ID, Status: change.Status, Path: change.Path, Message: change.Message})
		case ApplyStatusBlocked:
			return changed, skipped, fmt.Errorf("repair %s: %s", action.ID, change.Message)
		default:
			changed = append(changed, RepairChange{RepairID: action.ID, Status: change.Status, Path: change.Path, Message: change.Message})
		}
	}
	return changed, skipped, nil
}

func runLaunchAgentRepair(action RepairAction, status SetupStatus, dryRun bool, runner LaunchdRunner) ([]RepairChange, []RepairChange, error) {
	if status.Plan == nil {
		return nil, []RepairChange{{RepairID: action.ID, Status: "skipped", Message: "no setup plan available"}}, nil
	}
	changes, err := applyLaunchAgent(*status.Plan, dryRun, runner)
	if err != nil {
		return nil, nil, err
	}
	changed := []RepairChange{}
	skipped := []RepairChange{}
	convert := func(change ApplyChange) RepairChange {
		status := change.Status
		switch status {
		case ApplyStatusWouldChange:
			status = "would_write"
		case ApplyStatusChanged:
			status = "changed"
		case ApplyStatusAlreadySatisfied:
			status = "already_satisfied"
		case ApplyStatusSkipped:
			status = "skipped"
		case ApplyStatusBlocked:
			status = "blocked"
		}
		return RepairChange{RepairID: action.ID, Status: status, Path: change.Path, Message: firstNonEmpty(change.Message, change.ID)}
	}
	for _, change := range changes.changed {
		changed = append(changed, convert(change))
	}
	for _, change := range changes.blocked {
		changed = append(changed, convert(change))
	}
	for _, change := range changes.skipped {
		skipped = append(skipped, convert(change))
	}
	return changed, skipped, nil
}

func runManifestRepair(action RepairAction, status SetupStatus, dryRun bool) ([]RepairChange, []RepairChange, error) {
	path := status.Manifest.Path
	if strings.TrimSpace(path) == "" {
		return nil, []RepairChange{{RepairID: action.ID, Status: "skipped", Message: "manifest path is not known"}}, nil
	}
	if dryRun {
		return []RepairChange{{RepairID: action.ID, Status: "would_write", Path: path}}, nil, nil
	}
	var manifest InstallManifest
	if status.Manifest.Manifest != nil {
		manifest = *status.Manifest.Manifest
	} else if status.Plan != nil {
		manifest = ManifestFromPlan(*status.Plan)
	} else {
		return nil, []RepairChange{{RepairID: action.ID, Status: "skipped", Path: path, Message: "no manifest or plan available"}}, nil
	}
	if err := WriteManifest(path, manifest); err != nil {
		return nil, nil, fmt.Errorf("repair %s: write %s: %w", action.ID, path, err)
	}
	return []RepairChange{{RepairID: action.ID, Status: "written", Path: path}}, nil, nil
}
