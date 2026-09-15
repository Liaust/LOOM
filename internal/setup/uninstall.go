package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const (
	UninstallSchemaVersion = "loom.setup.uninstall.v0.5.2"

	UninstallModeDisableOnly  = "disable-only"
	UninstallModePreserveData = "preserve-data"
	UninstallModePurge        = "purge"

	UninstallActionStopDisable = "stop_disable"
	UninstallActionRemove      = "remove"
	UninstallActionPreserve    = "preserve"
	UninstallActionPending     = "pending"

	UninstallStatusWouldChange = "would_change"
	UninstallStatusChanged     = "changed"
	UninstallStatusPreserved   = "preserved"
	UninstallStatusBlocked     = "blocked"
	UninstallStatusSkipped     = "skipped"
	UninstallStatusRefused     = "refused"
	UninstallStatusSucceeded   = "succeeded"
)

type UninstallInput struct {
	StatusInput
	Mode             string
	DryRun           bool
	Yes              bool
	NoInteractive    bool
	SkipMainRevoke   bool
	StrictMainRevoke bool

	RemoveCredentials bool
	RemoveBox         bool
	RemoveBackups     bool
	RemoveDB          bool
	RemoveObjectStore bool

	AllowProductionMain bool
	ConfirmNode         string
	BackupRef           string

	Now                func() time.Time
	ServiceRunner      UninstallServiceRunner
	DecommissionRunner UninstallDecommissionRunner
}

type UninstallServiceRunner interface {
	StopDisable(ctx context.Context, manager, service string, dryRun bool) (string, error)
}

type UninstallDecommissionRunner interface {
	DecommissionNode(ctx context.Context, nodeRef, reason string) (UninstallDecommissionResult, error)
}

type UninstallDecommissionResult struct {
	NodeRef            string `json:"node_ref"`
	Status             string `json:"status"`
	RevokedCredentials int    `json:"revoked_credentials,omitempty"`
	Message            string `json:"message,omitempty"`
}

type UninstallPlan struct {
	SchemaVersion string                      `json:"schema_version"`
	PlanID        string                      `json:"plan_id"`
	CreatedAt     time.Time                   `json:"created_at"`
	Mode          string                      `json:"mode"`
	ManifestPath  string                      `json:"manifest_path,omitempty"`
	ManifestState string                      `json:"manifest_state"`
	Node          NodeSetupStatus             `json:"node"`
	Production    bool                        `json:"production"`
	Services      []UninstallServiceAction    `json:"services,omitempty"`
	Decommission  UninstallDecommissionAction `json:"decommission,omitempty"`
	Paths         []UninstallPathAction       `json:"paths,omitempty"`
	Guardrails    []UninstallGuardrail        `json:"guardrails,omitempty"`
	PlanHash      string                      `json:"plan_hash"`
}

type UninstallServiceAction struct {
	ID      string `json:"id"`
	Manager string `json:"manager"`
	Service string `json:"service"`
	Label   string `json:"label,omitempty"`
	Path    string `json:"path,omitempty"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type UninstallDecommissionAction struct {
	Required bool   `json:"required"`
	Action   string `json:"action,omitempty"`
	Status   string `json:"status,omitempty"`
	NodeRef  string `json:"node_ref,omitempty"`
	MainURL  string `json:"main_url,omitempty"`
	Message  string `json:"message,omitempty"`
}

type UninstallPathAction struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Durable bool   `json:"durable"`
	Message string `json:"message,omitempty"`
}

type UninstallGuardrail struct {
	ID        string `json:"id"`
	Severity  string `json:"severity"`
	Required  bool   `json:"required"`
	Satisfied bool   `json:"satisfied"`
	Message   string `json:"message"`
}

type UninstallResult struct {
	DryRun       bool                         `json:"dry_run"`
	PlanID       string                       `json:"plan_id"`
	PlanHash     string                       `json:"plan_hash"`
	Mode         string                       `json:"mode"`
	Refused      bool                         `json:"refused"`
	Refusal      string                       `json:"refusal,omitempty"`
	Changed      []UninstallChange            `json:"changed,omitempty"`
	Skipped      []UninstallChange            `json:"skipped,omitempty"`
	Blocked      []UninstallChange            `json:"blocked,omitempty"`
	Preserved    []UninstallChange            `json:"preserved,omitempty"`
	RecordPath   string                       `json:"record_path,omitempty"`
	Manifest     InstallManifest              `json:"manifest,omitempty"`
	Plan         UninstallPlan                `json:"plan"`
	Decommission *UninstallDecommissionResult `json:"decommission,omitempty"`
}

type UninstallChange struct {
	ID       string         `json:"id"`
	Category string         `json:"category,omitempty"`
	Status   string         `json:"status"`
	Path     string         `json:"path,omitempty"`
	Message  string         `json:"message,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type UninstallRecord struct {
	SchemaVersion string            `json:"schema_version"`
	UninstallID   string            `json:"uninstall_id"`
	CreatedAt     time.Time         `json:"created_at"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty"`
	Mode          string            `json:"mode"`
	NodeKey       string            `json:"node_key,omitempty"`
	NodeID        string            `json:"node_id,omitempty"`
	ManifestPath  string            `json:"manifest_path,omitempty"`
	PlanHash      string            `json:"plan_hash"`
	Status        string            `json:"status"`
	BackupRef     string            `json:"backup_ref,omitempty"`
	Changed       []UninstallChange `json:"changed,omitempty"`
	Skipped       []UninstallChange `json:"skipped,omitempty"`
	Blocked       []UninstallChange `json:"blocked,omitempty"`
	Preserved     []UninstallChange `json:"preserved,omitempty"`
}

func PlanUninstall(input UninstallInput) (UninstallPlan, error) {
	input.Mode = normalizeUninstallMode(input.Mode)
	statusInput := input.StatusInput
	if input.Now != nil && statusInput.Now == nil {
		statusInput.Now = input.Now
	}
	status, err := Status(statusInput)
	if err != nil {
		return UninstallPlan{}, err
	}
	now := time.Now
	if input.Now != nil {
		now = input.Now
	}
	plan := UninstallPlan{
		SchemaVersion: UninstallSchemaVersion,
		CreatedAt:     now().UTC(),
		Mode:          input.Mode,
		ManifestPath:  status.Manifest.Path,
		ManifestState: status.Manifest.State,
		Node:          status.Node,
		Production:    isProductionMainStatus(status),
	}
	plan.Services = planUninstallServices(status, input)
	plan.Decommission = planUninstallDecommission(status, input)
	plan.Paths = planUninstallPaths(status, input)
	plan.Guardrails = planUninstallGuardrails(plan, status, input)
	plan.PlanHash = hashUninstallPlan(plan)
	plan.PlanID = "uninstall_plan_" + strings.TrimPrefix(plan.PlanHash, "sha256:")[:16]
	return plan, nil
}

func ApplyUninstall(input UninstallInput) (UninstallResult, error) {
	plan, err := PlanUninstall(input)
	if err != nil {
		return UninstallResult{}, err
	}
	result := UninstallResult{
		DryRun:   input.DryRun,
		PlanID:   plan.PlanID,
		PlanHash: plan.PlanHash,
		Mode:     plan.Mode,
		Plan:     plan,
	}
	if !input.DryRun && !input.Yes {
		result.Refused = true
		if input.NoInteractive {
			result.Refusal = "setup uninstall is non-interactive; pass --yes to mutate local setup state"
		} else {
			result.Refusal = "setup uninstall requires --yes before mutating local setup state"
		}
		return result, nil
	}
	for _, guard := range plan.Guardrails {
		if guard.Required && !guard.Satisfied {
			result.Blocked = append(result.Blocked, UninstallChange{
				ID:       guard.ID,
				Category: "guardrail",
				Status:   UninstallStatusBlocked,
				Message:  guard.Message,
			})
		}
	}
	if len(result.Blocked) > 0 {
		return result, nil
	}
	recordPath, recordErr := writeUninstallRecord(input, plan, result, "started")
	if recordErr == nil {
		result.RecordPath = recordPath
	}
	if recordErr != nil && !input.DryRun {
		result.Blocked = append(result.Blocked, UninstallChange{ID: "write_uninstall_record", Category: "record", Status: UninstallStatusBlocked, Message: recordErr.Error()})
		return result, nil
	}
	if dec := applyUninstallDecommission(input, plan); dec != nil {
		if dec.Status == UninstallStatusBlocked {
			result.Blocked = append(result.Blocked, UninstallChange{ID: "decommission_main", Category: "decommission", Status: UninstallStatusBlocked, Message: dec.Message})
			return result, nil
		}
		result.Decommission = dec
		result.Changed = append(result.Changed, UninstallChange{ID: "decommission_main", Category: "decommission", Status: dec.Status, Message: dec.Message})
	}
	for _, service := range plan.Services {
		change := applyUninstallService(input, service)
		switch change.Status {
		case UninstallStatusBlocked:
			result.Blocked = append(result.Blocked, change)
		case UninstallStatusSkipped:
			result.Skipped = append(result.Skipped, change)
		default:
			result.Changed = append(result.Changed, change)
		}
	}
	if len(result.Blocked) > 0 {
		return result, nil
	}
	for _, path := range plan.Paths {
		change := applyUninstallPath(input, path)
		switch change.Status {
		case UninstallStatusBlocked:
			result.Blocked = append(result.Blocked, change)
		case UninstallStatusPreserved:
			result.Preserved = append(result.Preserved, change)
		case UninstallStatusSkipped:
			result.Skipped = append(result.Skipped, change)
		default:
			result.Changed = append(result.Changed, change)
		}
	}
	if len(result.Blocked) > 0 {
		return result, nil
	}
	manifest, manifestErr := updateUninstallManifest(input, plan, result)
	if manifestErr == nil {
		result.Manifest = manifest
	} else if !input.DryRun && plan.Mode != UninstallModePurge {
		result.Blocked = append(result.Blocked, UninstallChange{ID: "write_manifest", Category: "manifest", Status: UninstallStatusBlocked, Message: manifestErr.Error()})
		return result, nil
	}
	if finalPath, finalErr := writeUninstallRecord(input, plan, result, "completed"); finalErr == nil && finalPath != "" {
		result.RecordPath = finalPath
	}
	return result, nil
}

func normalizeUninstallMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case UninstallModeDisableOnly, UninstallModePurge:
		return strings.TrimSpace(mode)
	default:
		return UninstallModePreserveData
	}
}

func planUninstallServices(status SetupStatus, input UninstallInput) []UninstallServiceAction {
	if status.Manifest.Manifest == nil || status.Manifest.Manifest.ServiceManager == ServiceManagerNone || status.Manifest.Manifest.ServiceManager == "" {
		return nil
	}
	services := append([]InstalledService{}, status.Manifest.Manifest.Services...)
	if len(services) == 0 {
		switch status.Manifest.Manifest.NodeKind {
		case "main":
			services = []InstalledService{{Name: "loomd", Manager: status.Manifest.Manifest.ServiceManager}}
		default:
			services = []InstalledService{{Name: "loom-node-agent", Manager: status.Manifest.Manifest.ServiceManager}}
		}
	}
	out := []UninstallServiceAction{}
	for _, service := range services {
		name := strings.TrimSpace(service.Name)
		if name == "" {
			continue
		}
		out = append(out, UninstallServiceAction{
			ID:      "service_" + sanitizeID(name),
			Manager: firstNonEmpty(service.Manager, status.Manifest.Manifest.ServiceManager),
			Service: name,
			Label:   service.Label,
			Path:    service.Path,
			Action:  UninstallActionStopDisable,
			Status:  UninstallStatusWouldChange,
		})
	}
	return out
}

func planUninstallDecommission(status SetupStatus, input UninstallInput) UninstallDecommissionAction {
	if input.Mode == UninstallModeDisableOnly {
		return UninstallDecommissionAction{}
	}
	if status.Manifest.Manifest == nil || status.Manifest.Manifest.NodeKind == "main" {
		return UninstallDecommissionAction{}
	}
	manifest := status.Manifest.Manifest
	if !manifest.Credential.Configured && strings.TrimSpace(manifest.NodeID) == "" {
		return UninstallDecommissionAction{}
	}
	nodeRef := firstNonEmpty(manifest.NodeID, manifest.NodeKey)
	action := UninstallDecommissionAction{
		Required: true,
		Action:   "revoke_node_on_main",
		Status:   UninstallStatusWouldChange,
		NodeRef:  nodeRef,
		MainURL:  manifest.MainURL,
		Message:  "Non-main node should be marked inactive on main before local removal.",
	}
	if input.SkipMainRevoke {
		action.Action = "write_pending_local_decommission"
		action.Status = UninstallActionPending
		action.Message = "Main-side revoke skipped; setup will write a pending local decommission record."
	}
	return action
}

func planUninstallPaths(status SetupStatus, input UninstallInput) []UninstallPathAction {
	if status.Manifest.Manifest == nil {
		return nil
	}
	manifest := status.Manifest.Manifest
	isMain := manifest.NodeKind == "main"
	out := []UninstallPathAction{}
	add := func(id, path string, durable bool, action string, message string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		status := UninstallStatusPreserved
		if action == UninstallActionRemove {
			status = UninstallStatusWouldChange
		}
		out = append(out, UninstallPathAction{ID: id, Path: path, Durable: durable, Action: action, Status: status, Message: message})
	}
	switch input.Mode {
	case UninstallModeDisableOnly:
		add("manifest", status.Manifest.Path, false, UninstallActionPreserve, "disable-only keeps the install manifest")
		add("config_dir", manifest.ConfigDir, false, UninstallActionPreserve, "disable-only keeps config")
		add("state_dir", manifest.StateDir, false, UninstallActionPreserve, "disable-only keeps state")
		add("data_dir", manifest.DataDir, true, UninstallActionPreserve, "disable-only keeps data")
		add("box", manifest.BoxPath, true, UninstallActionPreserve, "disable-only keeps Box")
		add("box_state", manifest.BoxStateRoot, true, UninstallActionPreserve, "disable-only keeps canonical Box runtime state")
		add("storage_root", manifest.StorageRoot, true, UninstallActionPreserve, "disable-only keeps canonical physical Storage")
		add("legacy_box_state", filepath.Join(manifest.BoxPath, ".loom", "state"), true, UninstallActionPreserve, "obsolete visible Box state is reported and never deleted automatically")
		add("object_store", manifest.ObjectStorePath, true, UninstallActionPreserve, "disable-only keeps object store")
		add("main_documents", manifest.MainDocumentsPath, true, UninstallActionPreserve, "disable-only keeps main Documents storage")
		add("legacy_storage_export_root", manifest.StorageExportRoot, false, UninstallActionPreserve, "retired export evidence is never deleted automatically")
		add("launch_agent_plist", launchAgentPathFromManifest(manifest), false, UninstallActionPreserve, "disable-only keeps LaunchAgent plist")
	case UninstallModePurge:
		add("manifest", status.Manifest.Path, false, UninstallActionRemove, "purge removes install manifest")
		add("loomd_env", loomEnvPath(manifest.ConfigDir), false, UninstallActionRemove, "purge removes generated loomd service environment")
		add("socket", manifest.SocketPath, false, UninstallActionRemove, "purge removes socket file")
		add("config_dir", manifest.ConfigDir, false, UninstallActionRemove, "purge removes generated config")
		add("state_dir", manifest.StateDir, false, UninstallActionRemove, "purge removes local state")
		add("log_dir", manifest.LogDir, false, UninstallActionRemove, "purge removes logs")
		if input.RemoveDB {
			add("data_dir", manifest.DataDir, true, UninstallActionRemove, "explicit --remove-db selected for runtime database/data after uninstall guardrails")
		} else {
			add("data_dir", manifest.DataDir, true, UninstallActionPreserve, "runtime database/data requires explicit --remove-db")
		}
		if input.RemoveObjectStore {
			add("object_store", manifest.ObjectStorePath, true, UninstallActionRemove, "explicit --remove-object-store selected")
		} else {
			add("object_store", manifest.ObjectStorePath, true, UninstallActionPreserve, "object store requires --remove-object-store")
		}
		add("main_documents", manifest.MainDocumentsPath, true, UninstallActionPreserve, "main Documents storage is durable user data")
		add("legacy_storage_export_root", manifest.StorageExportRoot, false, UninstallActionPreserve, "retired export evidence is never deleted automatically")
		if input.RemoveBox && !isMain {
			add("box", manifest.BoxPath, true, UninstallActionRemove, "explicit --remove-box selected")
		} else {
			add("box", manifest.BoxPath, true, UninstallActionPreserve, "canonical main Box is always preserved; non-main Box requires --remove-box")
		}
		if !pathIsWithin(manifest.BoxStateRoot, manifest.DataDir) {
			add("box_state", manifest.BoxStateRoot, true, UninstallActionPreserve, "Box runtime state outside data_dir is preserved without a separate removal choice")
		}
		add("storage_root", manifest.StorageRoot, true, UninstallActionPreserve, "canonical physical Storage is never deleted automatically")
		add("legacy_box_state", filepath.Join(manifest.BoxPath, ".loom", "state"), true, UninstallActionPreserve, "obsolete visible Box state is reported and never deleted automatically")
		add("node_agent_config", manifest.NodeAgentConfigPath, false, UninstallActionRemove, "purge removes node-agent config")
		add("node_agent_state", manifest.NodeAgentStatePath, false, UninstallActionRemove, "purge removes node-agent state")
		add("node_agent_data", manifest.NodeAgentDataDir, false, UninstallActionRemove, "purge removes node-agent data")
		add("launch_agent_plist", launchAgentPathFromManifest(manifest), false, UninstallActionRemove, "purge removes LaunchAgent plist")
	default:
		add("manifest", status.Manifest.Path, false, UninstallActionPreserve, "preserve-data keeps install manifest")
		add("loomd_env", loomEnvPath(manifest.ConfigDir), false, UninstallActionRemove, "preserve-data removes generated loomd service environment")
		add("socket", manifest.SocketPath, false, UninstallActionRemove, "preserve-data removes socket file")
		add("data_dir", manifest.DataDir, true, UninstallActionPreserve, "preserve-data keeps durable data")
		add("box", manifest.BoxPath, true, UninstallActionPreserve, "preserve-data keeps Box")
		add("box_state", manifest.BoxStateRoot, true, UninstallActionPreserve, "preserve-data keeps canonical Box runtime state")
		add("storage_root", manifest.StorageRoot, true, UninstallActionPreserve, "preserve-data keeps canonical physical Storage")
		add("legacy_box_state", filepath.Join(manifest.BoxPath, ".loom", "state"), true, UninstallActionPreserve, "obsolete visible Box state is reported and never deleted automatically")
		add("object_store", manifest.ObjectStorePath, true, UninstallActionPreserve, "preserve-data keeps object store")
		add("main_documents", manifest.MainDocumentsPath, true, UninstallActionPreserve, "preserve-data keeps main Documents storage")
		add("legacy_storage_export_root", manifest.StorageExportRoot, false, UninstallActionPreserve, "retired export evidence is never deleted automatically")
		add("node_agent_config", manifest.NodeAgentConfigPath, false, UninstallActionRemove, "preserve-data removes generated node-agent config")
		add("launch_agent_plist", launchAgentPathFromManifest(manifest), false, UninstallActionRemove, "preserve-data removes LaunchAgent plist")
		if shouldRemoveLocalCredentialState(manifest, input) {
			add("node_agent_state", manifest.NodeAgentStatePath, false, UninstallActionRemove, "preserve-data removes local credential state after decommission or explicit credential removal")
		} else {
			add("node_agent_state", manifest.NodeAgentStatePath, false, UninstallActionPreserve, "node-agent state may contain credentials; pass --remove-credentials or decommission through main before removal")
		}
	}
	return dedupeUninstallPathActions(out)
}

func pathIsWithin(path, root string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	root = filepath.Clean(strings.TrimSpace(root))
	if path == "." || root == "." || !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func launchAgentPathFromManifest(manifest *InstallManifest) string {
	if manifest == nil || manifest.ServiceManager != ServiceManagerLaunchd {
		return ""
	}
	for _, service := range manifest.Services {
		if service.Manager == ServiceManagerLaunchd && strings.TrimSpace(service.Path) != "" {
			return service.Path
		}
	}
	if strings.TrimSpace(manifest.HomeDir) == "" {
		return ""
	}
	return LaunchAgentPlistPath(manifest.HomeDir)
}

func loomEnvPath(configDir string) string {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return ""
	}
	return filepath.Join(configDir, "loom.env")
}

func shouldRemoveLocalCredentialState(manifest *InstallManifest, input UninstallInput) bool {
	if manifest == nil || manifest.NodeKind == "main" || !manifest.Credential.Configured {
		return false
	}
	if input.RemoveCredentials {
		return true
	}
	return !input.SkipMainRevoke
}

func planUninstallGuardrails(plan UninstallPlan, status SetupStatus, input UninstallInput) []UninstallGuardrail {
	guards := []UninstallGuardrail{}
	if !status.Manifest.Exists || status.Manifest.State != "loaded" || status.Manifest.Manifest == nil {
		guards = append(guards, UninstallGuardrail{
			ID:        "manifest_loaded",
			Severity:  DiagnosticBlocking,
			Required:  true,
			Satisfied: false,
			Message:   "Uninstall requires a loaded install manifest; pass --manifest or restore the manifest before mutating setup state.",
		})
	}
	if plan.Production {
		guards = append(guards, UninstallGuardrail{
			ID:        "allow_production_main",
			Severity:  DiagnosticBlocking,
			Required:  true,
			Satisfied: input.AllowProductionMain,
			Message:   "Production main uninstall requires --allow-production-main.",
		})
		if plan.Mode == UninstallModePurge {
			guards = append(guards, UninstallGuardrail{
				ID:        "production_backup_ref",
				Severity:  DiagnosticBlocking,
				Required:  true,
				Satisfied: strings.TrimSpace(input.BackupRef) != "",
				Message:   "Production main purge requires --backup-ref.",
			})
		}
	}
	if plan.Mode == UninstallModePurge {
		expected := strings.TrimSpace(plan.Node.NodeKey)
		guards = append(guards, UninstallGuardrail{
			ID:        "confirm_node",
			Severity:  DiagnosticBlocking,
			Required:  true,
			Satisfied: expected != "" && strings.TrimSpace(input.ConfirmNode) == expected,
			Message:   fmt.Sprintf("Purge requires --confirm-node %s.", expected),
		})
	}
	for _, path := range plan.Paths {
		if path.Action != UninstallActionRemove {
			continue
		}
		if bad, reason := suspiciousDeletePath(path.Path, status.Manifest.Manifest); bad {
			guards = append(guards, UninstallGuardrail{
				ID:        "safe_path_" + sanitizeID(path.ID),
				Severity:  DiagnosticBlocking,
				Required:  true,
				Satisfied: false,
				Message:   fmt.Sprintf("Refusing to remove suspicious path %s: %s", path.Path, reason),
			})
		}
	}
	return guards
}

func applyUninstallDecommission(input UninstallInput, plan UninstallPlan) *UninstallDecommissionResult {
	if !plan.Decommission.Required {
		return nil
	}
	if input.SkipMainRevoke {
		return &UninstallDecommissionResult{NodeRef: plan.Decommission.NodeRef, Status: UninstallActionPending, Message: "pending main-side decommission recorded locally"}
	}
	if input.DecommissionRunner == nil {
		if strings.TrimSpace(plan.Decommission.MainURL) == "" {
			return &UninstallDecommissionResult{NodeRef: plan.Decommission.NodeRef, Status: UninstallStatusBlocked, Message: "main-side decommission runner unavailable; pass --skip-main-revoke to write a pending local record"}
		}
		input.DecommissionRunner = HTTPUninstallDecommissionRunner{BaseURL: plan.Decommission.MainURL}
	}
	if input.DryRun {
		return &UninstallDecommissionResult{NodeRef: plan.Decommission.NodeRef, Status: UninstallStatusWouldChange, Message: "would decommission node on main"}
	}
	result, err := input.DecommissionRunner.DecommissionNode(context.Background(), plan.Decommission.NodeRef, "loom setup uninstall")
	if err != nil {
		return &UninstallDecommissionResult{NodeRef: plan.Decommission.NodeRef, Status: UninstallStatusBlocked, Message: err.Error()}
	}
	return &result
}

func applyUninstallService(input UninstallInput, action UninstallServiceAction) UninstallChange {
	change := UninstallChange{ID: action.ID, Category: "service", Status: UninstallStatusWouldChange, Message: fmt.Sprintf("%s %s", action.Action, action.Service)}
	if input.DryRun {
		return change
	}
	if input.ServiceRunner == nil {
		return UninstallChange{ID: action.ID, Category: "service", Status: UninstallStatusSkipped, Message: "service runner unavailable; service cleanup remains an operator action"}
	}
	serviceRef := action.Service
	if action.Manager == ServiceManagerLaunchd {
		serviceRef = firstNonEmpty(action.Label, action.Service)
	}
	message, err := input.ServiceRunner.StopDisable(context.Background(), action.Manager, serviceRef, false)
	if err != nil {
		return UninstallChange{ID: action.ID, Category: "service", Status: UninstallStatusBlocked, Message: err.Error()}
	}
	change.Status = UninstallStatusChanged
	change.Message = message
	return change
}

func applyUninstallPath(input UninstallInput, action UninstallPathAction) UninstallChange {
	change := UninstallChange{ID: action.ID, Category: "path", Path: action.Path, Status: action.Status, Message: action.Message}
	if action.Action == UninstallActionPreserve {
		change.Status = UninstallStatusPreserved
		return change
	}
	if input.DryRun {
		change.Status = UninstallStatusWouldChange
		return change
	}
	if err := removePathSafely(action.Path); err != nil {
		change.Status = UninstallStatusBlocked
		change.Message = err.Error()
		return change
	}
	change.Status = UninstallStatusChanged
	return change
}

func updateUninstallManifest(input UninstallInput, plan UninstallPlan, result UninstallResult) (InstallManifest, error) {
	if input.DryRun || plan.ManifestPath == "" {
		return InstallManifest{}, nil
	}
	manifest, err := ReadManifest(plan.ManifestPath)
	if err != nil {
		return InstallManifest{}, err
	}
	status := "uninstalled_preserve_data"
	switch plan.Mode {
	case UninstallModeDisableOnly:
		status = "disabled"
	case UninstallModePurge:
		status = "purged"
		if pathWasRemoved(result, plan.ManifestPath) {
			return manifest, nil
		}
	}
	if result.Decommission != nil && result.Decommission.Status == UninstallActionPending {
		status = "decommission_pending"
	} else if result.Decommission != nil && result.Decommission.Status != "" && result.Decommission.Status != UninstallStatusBlocked {
		status = "decommissioned"
	}
	if input.RemoveCredentials || status == "decommissioned" {
		manifest.Credential = CredentialManifest{}
		manifest.Enrollment.Status = "decommissioned"
	}
	manifest.LastStatus.Status = status
	if manifest.Metadata == nil {
		manifest.Metadata = map[string]any{}
	}
	manifest.Metadata["last_uninstall_plan_hash"] = plan.PlanHash
	manifest.Metadata["last_uninstall_mode"] = plan.Mode
	if result.RecordPath != "" {
		manifest.Metadata["last_uninstall_record"] = result.RecordPath
	}
	if err := WriteManifest(plan.ManifestPath, manifest); err != nil {
		return InstallManifest{}, err
	}
	return manifest, nil
}

func writeUninstallRecord(input UninstallInput, plan UninstallPlan, result UninstallResult, status string) (string, error) {
	if input.DryRun {
		return "", nil
	}
	dir := uninstallRecordDir(plan, input)
	if dir == "" {
		return "", fmt.Errorf("could not resolve uninstall record directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	now := time.Now
	if input.Now != nil {
		now = input.Now
	}
	t := now().UTC()
	record := UninstallRecord{
		SchemaVersion: UninstallSchemaVersion,
		UninstallID:   "uninstall_" + strings.TrimPrefix(plan.PlanHash, "sha256:")[:16],
		CreatedAt:     t,
		Mode:          plan.Mode,
		NodeKey:       plan.Node.NodeKey,
		NodeID:        plan.Node.NodeID,
		ManifestPath:  plan.ManifestPath,
		PlanHash:      plan.PlanHash,
		Status:        status,
		BackupRef:     strings.TrimSpace(input.BackupRef),
		Changed:       result.Changed,
		Skipped:       result.Skipped,
		Blocked:       result.Blocked,
		Preserved:     result.Preserved,
	}
	if status == "completed" {
		record.CompletedAt = &t
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, record.UninstallID+"-"+status+".json")
	return path, os.WriteFile(path, payload, 0o600)
}

func uninstallRecordDir(plan UninstallPlan, input UninstallInput) string {
	if input.StatusInput.Spec.StateDir != "" {
		return filepath.Join(input.StatusInput.Spec.StateDir, "setup", "uninstall-history")
	}
	if plan.ManifestPath != "" {
		return filepath.Join(filepath.Dir(plan.ManifestPath), "uninstall-history")
	}
	return ""
}

func RenderUninstallPlan(w io.Writer, plan UninstallPlan) {
	fmt.Fprintln(w, "LOOM setup uninstall plan")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Mode: %s\n", plan.Mode)
	fmt.Fprintf(w, "Node: %s %s/%s %s\n", plan.Node.NodeKey, plan.Node.NodeKind, plan.Node.NodeRole, plan.Node.RuntimeClass)
	fmt.Fprintf(w, "Manifest: %s %s\n", plan.ManifestState, firstNonEmpty(plan.ManifestPath, "-"))
	if plan.Production {
		fmt.Fprintln(w, "Production: yes")
	}
	if len(plan.Guardrails) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Guardrails")
		for _, guard := range plan.Guardrails {
			status := "ok"
			if guard.Required && !guard.Satisfied {
				status = "required"
			}
			fmt.Fprintf(w, "  %s %s: %s\n", status, guard.ID, guard.Message)
		}
	}
	if plan.Decommission.Required {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Decommission")
		fmt.Fprintf(w, "  %s %s %s\n", plan.Decommission.Status, plan.Decommission.NodeRef, plan.Decommission.Message)
	}
	if len(plan.Services) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Services")
		for _, service := range plan.Services {
			fmt.Fprintf(w, "  %s %s %s\n", service.Status, service.Manager, service.Service)
		}
	}
	if len(plan.Paths) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Paths")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, path := range plan.Paths {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", path.Status, path.ID, path.Path)
		}
		_ = tw.Flush()
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Plan hash: %s\n", plan.PlanHash)
}

func RenderUninstallResult(w io.Writer, result UninstallResult) {
	fmt.Fprintln(w, "LOOM setup uninstall")
	fmt.Fprintln(w)
	if result.Refused {
		fmt.Fprintf(w, "refused: %s\n", result.Refusal)
		return
	}
	if len(result.Blocked) > 0 {
		fmt.Fprintln(w, "Blocked")
		for _, change := range result.Blocked {
			fmt.Fprintf(w, "  %s %s\n", firstNonEmpty(change.Category, "uninstall"), change.ID)
			if change.Message != "" {
				fmt.Fprintf(w, "    %s\n", change.Message)
			}
		}
		return
	}
	fmt.Fprintf(w, "Mode: %s\n", result.Mode)
	if result.RecordPath != "" {
		fmt.Fprintf(w, "Record: %s\n", result.RecordPath)
	}
	if len(result.Changed) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Changed")
		for _, change := range result.Changed {
			fmt.Fprintf(w, "  %s %s %s\n", change.Status, firstNonEmpty(change.Path, change.ID), change.Message)
		}
	}
	if len(result.Preserved) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Preserved")
		for _, change := range result.Preserved {
			fmt.Fprintf(w, "  %s %s\n", change.ID, change.Path)
		}
	}
	if len(result.Skipped) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Skipped")
		for _, change := range result.Skipped {
			fmt.Fprintf(w, "  %s %s\n", change.ID, change.Message)
		}
	}
}

type DefaultUninstallServiceRunner struct{}

func (DefaultUninstallServiceRunner) StopDisable(ctx context.Context, manager, service string, dryRun bool) (string, error) {
	manager = strings.TrimSpace(manager)
	service = strings.TrimSpace(service)
	if dryRun {
		return "would stop and disable " + service, nil
	}
	switch manager {
	case ServiceManagerSystemd:
		if _, err := exec.LookPath("systemctl"); err != nil {
			return "", err
		}
		if err := exec.CommandContext(ctx, "systemctl", "stop", service).Run(); err != nil {
			return "", err
		}
		if err := exec.CommandContext(ctx, "systemctl", "disable", service).Run(); err != nil {
			return "", err
		}
		return "stopped and disabled " + service, nil
	case ServiceManagerLaunchd:
		label := service
		if label == "loom-node-agent" {
			label = LaunchAgentLabel
		}
		runner := DefaultLaunchdRunner{}
		_, _ = runner.Launchctl(ctx, "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+label)
		if _, err := runner.Launchctl(ctx, "disable", "gui/"+strconv.Itoa(os.Getuid())+"/"+label); err != nil {
			return "", err
		}
		return "unloaded and disabled " + label, nil
	default:
		return "", fmt.Errorf("service manager %q is not supported by automatic uninstall", manager)
	}
}

type HTTPUninstallDecommissionRunner struct {
	BaseURL string
	Client  *http.Client
}

func (r HTTPUninstallDecommissionRunner) DecommissionNode(ctx context.Context, nodeRef, reason string) (UninstallDecommissionResult, error) {
	base := strings.TrimRight(strings.TrimSpace(r.BaseURL), "/")
	if base == "" {
		return UninstallDecommissionResult{}, fmt.Errorf("main URL is required")
	}
	escaped := url.PathEscape(strings.TrimSpace(nodeRef))
	endpoint := base + "/v1/nodes/" + escaped + "/decommission"
	payload, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return UninstallDecommissionResult{}, err
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return UninstallDecommissionResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return UninstallDecommissionResult{}, err
	}
	defer resp.Body.Close()
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			RevokedCredentials int    `json:"revoked_credentials"`
			Reason             string `json:"reason"`
			Node               struct {
				NodeID  string `json:"node_id"`
				NodeKey string `json:"node_key"`
				Status  string `json:"status"`
			} `json:"node"`
		} `json:"data"`
		Error struct {
			Summary string `json:"summary"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return UninstallDecommissionResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.OK {
		return UninstallDecommissionResult{}, fmt.Errorf("main decommission failed: %s", firstNonEmpty(envelope.Error.Summary, envelope.Error.Code, resp.Status))
	}
	return UninstallDecommissionResult{
		NodeRef:            firstNonEmpty(envelope.Data.Node.NodeID, envelope.Data.Node.NodeKey, nodeRef),
		Status:             UninstallStatusChanged,
		RevokedCredentials: envelope.Data.RevokedCredentials,
		Message:            firstNonEmpty(envelope.Data.Reason, "node decommissioned on main"),
	}, nil
}

func hashUninstallPlan(plan UninstallPlan) string {
	clone := plan
	clone.PlanID = ""
	clone.PlanHash = ""
	payload, _ := json.Marshal(clone)
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func dedupeUninstallPathActions(actions []UninstallPathAction) []UninstallPathAction {
	seen := map[string]bool{}
	out := []UninstallPathAction{}
	for _, action := range actions {
		key := action.ID + "\x00" + action.Path
		if action.Path == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, action)
	}
	return out
}

func isProductionMainStatus(status SetupStatus) bool {
	if status.Manifest.Manifest == nil {
		return false
	}
	manifest := status.Manifest.Manifest
	return manifest.NodeKind == "main" && (manifest.BootstrapMode == "production" || manifest.RuntimeClass == "main_full")
}

func suspiciousDeletePath(path string, manifest *InstallManifest) (bool, string) {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" || clean == "." {
		return true, "empty or relative path"
	}
	for _, forbidden := range []string{"/", "/home", "/var", "/etc", "/usr", "/bin", "/sbin", "/tmp"} {
		if clean == forbidden {
			return true, "broad system directory"
		}
	}
	if manifest != nil && manifest.HomeDir != "" && clean == filepath.Clean(manifest.HomeDir) {
		return true, "home directory"
	}
	return false, ""
}

func removePathSafely(path string) error {
	bad, reason := suspiciousDeletePath(path, nil)
	if bad {
		return fmt.Errorf("refusing to remove suspicious path %s: %s", path, reason)
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to remove symlink %s", path)
	}
	return os.RemoveAll(path)
}

func pathWasRemoved(result UninstallResult, path string) bool {
	clean := filepath.Clean(path)
	for _, change := range result.Changed {
		if filepath.Clean(change.Path) == clean && change.Status == UninstallStatusChanged {
			return true
		}
	}
	return false
}

func sanitizeID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	out := strings.Builder{}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
		} else {
			out.WriteRune('_')
		}
	}
	return strings.Trim(out.String(), "_")
}
