package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type PlannerInput struct {
	Spec          SetupSpec
	Facts         TargetFacts
	Existing      *InstallManifest
	Now           func() time.Time
	CollectFacts  bool
	ManifestPath  string
	SourceVersion string
}

func Plan(input PlannerInput) (SetupPlan, error) {
	facts := input.Facts
	if input.CollectFacts || facts.OS == "" {
		facts = CollectLocalFacts()
	}
	now := time.Now
	if input.Now != nil {
		now = input.Now
	}
	spec, diagnostics, err := NormalizeSpec(input.Spec, facts)
	if err != nil {
		return SetupPlan{}, err
	}
	paths, err := BuildPathPlan(spec)
	if err != nil {
		return SetupPlan{}, err
	}
	if strings.TrimSpace(input.ManifestPath) != "" {
		manifestPath, _, pathErr := ResolveManifestPath(ManifestPathInput{ExplicitPath: input.ManifestPath, Spec: spec})
		if pathErr != nil {
			return SetupPlan{}, pathErr
		}
		paths.ManifestPath = manifestPath
	}
	profile := ProfilePlan{
		AuthorityProfileKey: spec.AuthorityProfile,
		RuntimeProfileKey:   spec.RuntimeProfile,
	}
	steps := buildSteps(spec, facts, paths, diagnostics)
	plan := SetupPlan{
		SchemaVersion: SchemaVersion,
		CreatedAt:     now().UTC(),
		Spec:          spec,
		Facts:         facts,
		Profile:       profile,
		Paths:         paths,
		Steps:         steps,
		Diagnostics:   diagnostics,
	}
	if spec.NodeKind == "main" && spec.BoxProfile == "main" {
		cleanup, cleanupErr := PlanRetiredIntakeCleanup(plan, now().UTC())
		if cleanupErr != nil {
			return SetupPlan{}, cleanupErr
		}
		plan.RetiredIntakeCleanup = &cleanup
	}
	if spec.EnableBox {
		migration, migrationErr := PlanBoxStateMigration(paths.LegacyBoxStateRoot, paths.BoxStateRoot)
		if migrationErr != nil {
			return SetupPlan{}, migrationErr
		}
		plan.BoxStateMigration = &migration
	}
	plan.PlanHash = HashPlan(plan)
	plan.PlanID = "setup_plan_" + strings.TrimPrefix(plan.PlanHash, "sha256:")[:16]
	return plan, nil
}

func buildSteps(spec SetupSpec, facts TargetFacts, paths PathPlan, diagnostics []Diagnostic) []SetupStep {
	hasBlocking := hasErrorDiagnostic(diagnostics)
	steps := []SetupStep{
		{
			ID:          "detect_target",
			Category:    "preflight",
			Title:       "Detect local target facts",
			Description: fmt.Sprintf("Detected %s/%s on %s.", facts.OS, facts.Arch, firstNonEmpty(facts.Hostname, "unknown-host")),
			Required:    true,
			Mutating:    false,
			Status:      StepStatusAlreadySatisfied,
			Check:       "collect local OS, user, binary, and service-manager facts",
		},
		{
			ID:          "resolve_profiles",
			Category:    "profiles",
			Title:       "Resolve authority and runtime profiles",
			Description: fmt.Sprintf("%s + %s + %s -> %s / %s", spec.NodeKind, spec.NodeRole, spec.RuntimeClass, spec.AuthorityProfile, spec.RuntimeProfile),
			Required:    true,
			Mutating:    false,
			Status:      StepStatusAlreadySatisfied,
			Check:       "run node profile resolver",
		},
	}
	steps = append(steps, pathStep("ensure_config_dir", "paths", "Ensure config directory", paths.ConfigDir, true, spec.InstallMode == InstallModeService))
	steps = append(steps, pathStep("ensure_data_dir", "paths", "Ensure data directory", paths.DataDir, true, spec.InstallMode == InstallModeService))
	steps = append(steps, pathStep("ensure_state_dir", "paths", "Ensure state directory", paths.StateDir, true, spec.InstallMode == InstallModeService))
	if spec.EnableBox {
		steps = append(steps, pathStep("ensure_box_root", "box", "Ensure LOOM Box root", paths.BoxPath, true, false))
		steps = append(steps, pathStep("ensure_box_metadata_dir", "box", "Ensure LOOM Box metadata directory", paths.BoxLoomDir, true, false))
		steps = append(steps, pathStep("ensure_box_state_root", "box", "Ensure external Box runtime-state root", paths.BoxStateRoot, true, spec.InstallMode == InstallModeService))
	}
	if spec.EnableLoomd {
		steps = append(steps,
			pathStep("ensure_object_store", "database", "Ensure object store directory", paths.ObjectStorePath, true, spec.InstallMode == InstallModeService),
			pathStep("ensure_service_root", "storage", "Ensure canonical LOOM service root", paths.ServiceRoot, true, spec.InstallMode == InstallModeService),
			pathStep("ensure_storage_root", "storage", "Ensure canonical physical Storage root", paths.StorageRoot, true, spec.InstallMode == InstallModeService),
			pathStep("ensure_imports_root", "storage", "Ensure canonical imports root", paths.ImportsRoot, true, spec.InstallMode == InstallModeService),
			pathStep("ensure_user_backups_root", "storage", "Ensure canonical user-backups root", paths.UserBackupsRoot, true, spec.InstallMode == InstallModeService),
			pathStep("ensure_archive_root", "storage", "Ensure canonical archive root", paths.ArchiveRoot, true, spec.InstallMode == InstallModeService),
			pathStep("ensure_generated_root", "storage", "Ensure internal generated-artifact root", paths.GeneratedRoot, true, spec.InstallMode == InstallModeService),
			SetupStep{
				ID:          "write_loomd_config",
				Category:    "config",
				Title:       "Write loomd environment",
				Description: "Render LOOM service configuration from setup spec.",
				Required:    true,
				Mutating:    true,
				Privileged:  spec.InstallMode == InstallModeService,
				Status:      StepStatusWouldChange,
				Check:       "compare desired loomd environment with existing service config",
				Action:      "write LOOM config/env file during setup apply",
			},
			SetupStep{
				ID:         "run_migrations",
				Category:   "database",
				Title:      "Run database migrations",
				Required:   spec.AutoMigrate,
				Mutating:   true,
				Privileged: false,
				Status:     conditionalStatus(spec.AutoMigrate, StepStatusWouldChange, StepStatusSkipped),
				Check:      "inspect migration status",
				Action:     "run migrations during setup apply",
			},
			SetupStep{
				ID:         "production_bootstrap",
				Category:   "bootstrap",
				Title:      "Run production bootstrap",
				Required:   spec.ProductionBootstrap,
				Mutating:   true,
				Privileged: false,
				Status:     conditionalStatus(spec.ProductionBootstrap, StepStatusWouldChange, StepStatusSkipped),
				Check:      "inspect main bootstrap state",
				Action:     "create production bootstrap records during setup apply",
			},
		)
	}
	if spec.EnableCloud {
		steps = append(steps,
			pathStep("ensure_cloud_config_dir", "cloud", "Ensure cloud config directory", paths.CloudConfigDir, true, spec.InstallMode == InstallModeService),
			pathStep("ensure_cloud_state_dir", "cloud", "Ensure cloud state directory", paths.CloudStateDir, true, spec.InstallMode == InstallModeService),
			pathStep("ensure_cloud_borg_cache_dir", "cloud", "Ensure Borg cache directory", paths.CloudBorgCacheDir, true, spec.InstallMode == InstallModeService),
			pathStep("ensure_cloud_borg_security_dir", "cloud", "Ensure Borg security directory", paths.CloudBorgSecurityDir, true, spec.InstallMode == InstallModeService),
			SetupStep{
				ID:          "cloud_credentials_operator_managed",
				Category:    "cloud",
				Title:       "Confirm cloud credentials are operator-managed",
				Description: paths.CloudRcloneConfigPath,
				Required:    false,
				Mutating:    false,
				Privileged:  false,
				Status:      StepStatusSkipped,
				Check:       "run loom cloud doctor after installing rclone credentials",
				RepairHint:  "Create /etc/loom/cloud/rclone.conf with strict permissions; setup does not write cloud secrets.",
				Metadata: map[string]any{
					"remote_name":      spec.CloudRemoteName,
					"remote_root":      spec.CloudRemoteRoot,
					"snapshot_backend": spec.CloudSnapshotBackend,
				},
			},
			SetupStep{
				ID:          "cloud_borg_credentials_operator_managed",
				Category:    "cloud",
				Title:       "Confirm Borg snapshot credentials are operator-managed",
				Description: paths.CloudBorgPassphraseFile,
				Required:    spec.CloudSnapshotBackend == "borg",
				Mutating:    false,
				Privileged:  false,
				Status:      StepStatusSkipped,
				Check:       "run loom cloud snapshot backend doctor after installing Borg credentials",
				RepairHint:  "Create /etc/loom/cloud/borg.passphrase with strict permissions and configure snapshots.borg.repository; setup does not write Borg secrets.",
				Metadata: map[string]any{
					"backend":               spec.CloudSnapshotBackend,
					"repository_configured": strings.TrimSpace(spec.CloudBorgRepository) != "",
					"passphrase_file":       paths.CloudBorgPassphraseFile,
					"cache_dir":             paths.CloudBorgCacheDir,
					"security_dir":          paths.CloudBorgSecurityDir,
				},
			},
		)
	}
	if spec.EnableNodeAgent {
		steps = append(steps,
			pathStep("ensure_node_agent_config_dir", "node_agent", "Ensure node-agent config directory", filepath.Dir(paths.NodeAgentConfigPath), true, false),
			pathStep("ensure_node_agent_data_dir", "node_agent", "Ensure node-agent data directory", paths.NodeAgentDataDir, true, false),
			SetupStep{
				ID:          "write_node_agent_config",
				Category:    "node_agent",
				Title:       "Write node-agent config",
				Description: "Render node-agent config from setup spec.",
				Required:    true,
				Mutating:    true,
				Privileged:  false,
				Status:      StepStatusWouldChange,
				Check:       "compare desired node-agent config with existing config",
				Action:      "write node-agent config during setup apply",
			},
			SetupStep{
				ID:          "enroll_node",
				Category:    "enrollment",
				Title:       "Enroll node with main",
				Description: "Submit enrollment, import credentials, and verify heartbeat.",
				Required:    spec.RunEnrollment,
				Mutating:    true,
				Privileged:  false,
				Status:      enrollmentStepStatus(spec, hasBlocking),
				Check:       "check local node credential and last heartbeat",
				Action:      "run enrollment automation during setup apply",
				RepairHint:  "Provide --main-url before applying non-main setup.",
			},
		)
	}
	if spec.ServiceManager != ServiceManagerNone && spec.InstallMode == InstallModeService {
		title := "Install service unit"
		privileged := true
		metadata := map[string]any{}
		if spec.ServiceManager == ServiceManagerLaunchd {
			title = "Install LaunchAgent"
			privileged = false
			metadata["label"] = LaunchAgentLabel
			metadata["path"] = paths.LaunchAgentPlistPath
		}
		steps = append(steps, SetupStep{
			ID:          "install_service",
			Category:    "service",
			Title:       title,
			Description: fmt.Sprintf("Install LOOM service using %s.", spec.ServiceManager),
			Required:    true,
			Mutating:    true,
			Privileged:  privileged,
			Status:      StepStatusWouldChange,
			Check:       "inspect service manager state",
			Action:      "write and enable service during setup apply",
			Metadata:    metadata,
		})
	}
	steps = append(steps, SetupStep{
		ID:          "write_manifest",
		Category:    "manifest",
		Title:       "Write install manifest",
		Description: "Record non-sensitive local setup state.",
		Required:    true,
		Mutating:    true,
		Privileged:  spec.InstallMode == InstallModeService,
		Status:      StepStatusWouldChange,
		Check:       "inspect install manifest",
		Action:      "write redacted install manifest during setup apply",
		Metadata: map[string]any{
			"path": paths.ManifestPath,
		},
	})
	if hasBlocking {
		for i := range steps {
			if steps[i].ID == "write_manifest" || steps[i].Category == "enrollment" {
				steps[i].Status = StepStatusBlocked
			}
		}
	}
	return steps
}

func pathStep(id, category, title, path string, required bool, privileged bool) SetupStep {
	status := StepStatusSkipped
	if required {
		status = StepStatusWouldChange
		if pathExists(path) {
			status = StepStatusAlreadySatisfied
		}
	}
	return SetupStep{
		ID:          id,
		Category:    category,
		Title:       title,
		Description: path,
		Required:    required,
		Mutating:    required,
		Privileged:  privileged,
		Status:      status,
		Check:       "stat path",
		Action:      "create directory during setup apply",
		RepairHint:  "Run loom setup apply after reviewing the plan.",
		Metadata: map[string]any{
			"path": path,
		},
	}
}

func conditionalStatus(condition bool, trueStatus string, falseStatus string) string {
	if condition {
		return trueStatus
	}
	return falseStatus
}

func enrollmentStepStatus(spec SetupSpec, hasBlocking bool) string {
	if spec.NodeKind == "main" || spec.SkipEnroll || !spec.RunEnrollment {
		return StepStatusSkipped
	}
	if hasBlocking {
		return StepStatusBlocked
	}
	return StepStatusWouldChange
}

func hasErrorDiagnostic(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == DiagnosticError {
			return true
		}
	}
	return false
}

func HashPlan(plan SetupPlan) string {
	payload := struct {
		SchemaVersion        string                    `json:"schema_version"`
		Spec                 SetupSpec                 `json:"spec"`
		Facts                TargetFacts               `json:"facts"`
		Profile              ProfilePlan               `json:"profile"`
		Paths                PathPlan                  `json:"paths"`
		Steps                []SetupStep               `json:"steps"`
		Diagnostics          []Diagnostic              `json:"diagnostics,omitempty"`
		RetiredIntakeCleanup *RetiredIntakeCleanupPlan `json:"retired_intake_cleanup,omitempty"`
	}{
		SchemaVersion:        plan.SchemaVersion,
		Spec:                 plan.Spec,
		Facts:                plan.Facts,
		Profile:              plan.Profile,
		Paths:                plan.Paths,
		Steps:                plan.Steps,
		Diagnostics:          plan.Diagnostics,
		RetiredIntakeCleanup: plan.RetiredIntakeCleanup,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
