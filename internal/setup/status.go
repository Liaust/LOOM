package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/fsaccess"
	"loom.local/loom/internal/nodeprofiles"
)

func Status(input StatusInput) (SetupStatus, error) {
	facts := input.Facts
	if input.CollectFacts || facts.OS == "" {
		facts = CollectLocalFacts()
	}
	now := time.Now
	if input.Now != nil {
		now = input.Now
	}
	checkedAt := now().UTC()
	specForPath := input.Spec
	if specForPath.InstallMode == "" {
		specForPath.InstallMode = InstallModeUser
	}
	manifestPath, _, err := ResolveManifestPath(ManifestPathInput{
		ExplicitPath: input.ManifestPath,
		Spec:         specForPath,
		HomeDir:      facts.HomeDir,
	})
	if err != nil {
		return SetupStatus{}, err
	}

	manifest, manifestErr := ReadManifest(manifestPath)
	manifestStatus := ManifestStatus{Path: manifestPath, State: "missing"}
	var effectiveSpec SetupSpec
	var plan *SetupPlan
	diagnostics := []Diagnostic{}
	if manifestErr == nil {
		manifestStatus.Exists = true
		manifestStatus.State = "loaded"
		manifestStatus.SchemaVersion = manifest.SchemaVersion
		manifestStatus.Manifest = &manifest
		effectiveSpec = specFromManifest(manifest)
	} else if errors.Is(manifestErr, fs.ErrNotExist) {
		effectiveSpec = input.Spec
	} else {
		manifestStatus.Exists = pathExists(manifestPath)
		manifestStatus.State = "invalid"
		manifestStatus.Error = manifestErr.Error()
		diagnostics = append(diagnostics, Diagnostic{
			Severity:   DiagnosticError,
			Code:       "setup.manifest.invalid",
			Message:    "Install manifest exists but could not be read.",
			Path:       manifestPath,
			RepairHint: "Inspect or replace the manifest.",
		})
		effectiveSpec = input.Spec
	}

	if effectiveSpec.NodeKind == "" {
		effectiveSpec.NodeKind = "workspace"
	}
	if effectiveSpec.HomeDir == "" {
		effectiveSpec.HomeDir = facts.HomeDir
	}
	derivedPlan, planErr := Plan(PlannerInput{
		Spec:         effectiveSpec,
		Facts:        facts,
		Now:          now,
		ManifestPath: manifestPath,
	})
	if planErr == nil {
		plan = &derivedPlan
		diagnostics = append(diagnostics, derivedPlan.Diagnostics...)
		effectiveSpec = derivedPlan.Spec
	} else {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Code:     "setup.plan.failed",
			Message:  planErr.Error(),
		})
	}

	status := SetupStatus{
		CheckedAt:   checkedAt,
		Manifest:    manifestStatus,
		Node:        nodeStatusFromSpec(effectiveSpec, manifest),
		Profiles:    profileStatusFromSpec(effectiveSpec),
		Binaries:    binaryStatuses(effectiveSpec, facts),
		Enrollment:  enrollmentStatusFromManifest(manifest, manifestStatus.Exists),
		Summary:     SetupStatusSummary{LastCheckedAt: &checkedAt},
		Diagnostics: diagnostics,
		Plan:        plan,
	}
	if plan != nil {
		status.Paths = pathStatuses(effectiveSpec, plan.Paths, plan.BoxStateMigration)
		status.Services = serviceStatuses(effectiveSpec, plan.Paths, input.LaunchdRunner)
		status.Box = boxStatus(effectiveSpec, plan.Paths, plan.BoxStateMigration)
		status.NodeAgent = nodeAgentStatus(effectiveSpec, plan.Paths, status.Enrollment)
		status.HumanLinks = humanLinkStatuses(plan.Paths.HumanLinks)
		status.MainConnectivity = connectivityStatus(effectiveSpec)
	} else {
		status.Services = serviceStatuses(effectiveSpec, PathPlan{}, input.LaunchdRunner)
		status.MainConnectivity = connectivityStatus(effectiveSpec)
	}
	status.Summary.Status = summarizeStatus(status)
	if manifest.LastStatus.LastHeartbeatAt != nil {
		status.Summary.LastHeartbeatAt = manifest.LastStatus.LastHeartbeatAt
	}
	return status, nil
}

func specFromManifest(manifest InstallManifest) SetupSpec {
	enableProviders := manifest.ProviderMode != "disabled"
	if manifest.ProviderMode == "" {
		enableProviders = manifest.NodeKind == "main" || manifest.NodeKind == "workspace" || manifest.NodeKind == "hardware"
	}
	return SetupSpec{
		SchemaVersion:               SchemaVersion,
		NodeKey:                     manifest.NodeKey,
		DisplayName:                 manifest.DisplayName,
		NodeKind:                    manifest.NodeKind,
		NodeRole:                    manifest.NodeRole,
		RuntimeClass:                manifest.RuntimeClass,
		MainURL:                     manifest.MainURL,
		InstallMode:                 manifest.InstallMode,
		ServiceManager:              manifest.ServiceManager,
		PackageMode:                 manifest.PackageMode,
		UserName:                    manifest.UserName,
		HomeDir:                     manifest.HomeDir,
		ConfigDir:                   manifest.ConfigDir,
		DataDir:                     manifest.DataDir,
		StateDir:                    manifest.StateDir,
		LogDir:                      manifest.LogDir,
		ServiceRoot:                 manifest.ServiceRoot,
		StorageRoot:                 manifest.StorageRoot,
		ImportsRoot:                 manifest.ImportsRoot,
		UserBackupsRoot:             manifest.UserBackupsRoot,
		ArchiveRoot:                 manifest.ArchiveRoot,
		GeneratedRoot:               manifest.GeneratedRoot,
		BoxStateRoot:                manifest.BoxStateRoot,
		BoxPath:                     manifest.BoxPath,
		BoxProfile:                  manifest.BoxProfile,
		NodeAgentConfigPath:         manifest.NodeAgentConfigPath,
		NodeAgentStatePath:          manifest.NodeAgentStatePath,
		NodeAgentDataDir:            manifest.NodeAgentDataDir,
		ObjectStorePath:             manifest.ObjectStorePath,
		MainDocumentsPath:           manifest.MainDocumentsPath,
		StorageExportRoot:           manifest.StorageExportRoot,
		SocketPath:                  manifest.SocketPath,
		HTTPListenAddr:              manifest.HTTPListenAddr,
		EnableCloud:                 manifest.EnableCloud,
		CloudConfigPath:             manifest.CloudConfigPath,
		CloudStateDir:               manifest.CloudStateDir,
		CloudRcloneConfigPath:       manifest.CloudRcloneConfigPath,
		CloudRemoteName:             manifest.CloudRemoteName,
		CloudRemoteRoot:             manifest.CloudRemoteRoot,
		CloudSnapshotBackend:        manifest.CloudSnapshotBackend,
		CloudBorgRepository:         manifest.CloudBorgRepository,
		CloudBorgPassphraseFile:     manifest.CloudBorgPassphraseFile,
		CloudBorgCacheDir:           manifest.CloudBorgCacheDir,
		CloudBorgSecurityDir:        manifest.CloudBorgSecurityDir,
		BootstrapMode:               firstNonEmpty(manifest.BootstrapMode, inferredBootstrapMode(manifest.NodeKind)),
		AuthorityProfile:            manifest.AuthorityProfile,
		RuntimeProfile:              manifest.RuntimeProfile,
		ProviderMode:                manifest.ProviderMode,
		SafeRoots:                   append([]SafeRootSpec{}, manifest.SafeRoots...),
		EnableBox:                   manifest.BoxPath != "",
		EnableNodeAgent:             manifest.NodeKind != "main",
		EnableLoomd:                 manifest.NodeKind == "main",
		EnableDropzone:              false,
		EnableWatchedRoots:          manifest.NodeKind == "workspace",
		EnableProviders:             enableProviders,
		EnableStorageCatalog:        manifest.NodeKind == "main",
		EnableStorageExportWorker:   false,
		EnableMainDocumentsWorker:   manifest.NodeKind == "main",
		EnableStorageRetention:      manifest.NodeKind == "main",
		EnableProjectArchiveRuntime: manifest.NodeKind == "main",
		AutoMigrate:                 manifest.NodeKind == "main",
		ProductionBootstrap:         manifest.NodeKind == "main",
		SkipEnroll:                  manifest.Enrollment.Status == "skipped",
		SourcePath:                  manifest.SourcePath,
		SourceCommit:                manifest.SourceCommit,
		MigrationsDir:               manifest.MigrationsDir,
	}
}

func inferredBootstrapMode(nodeKind string) string {
	if nodeKind == "main" {
		return "production"
	}
	return "none"
}

func nodeStatusFromSpec(spec SetupSpec, manifest InstallManifest) NodeSetupStatus {
	return NodeSetupStatus{
		NodeKey:      spec.NodeKey,
		NodeID:       manifest.NodeID,
		DisplayName:  spec.DisplayName,
		NodeKind:     spec.NodeKind,
		NodeRole:     spec.NodeRole,
		RuntimeClass: spec.RuntimeClass,
	}
}

func profileStatusFromSpec(spec SetupSpec) ProfileStatus {
	assignment, err := nodeprofiles.Resolve(nodeprofiles.ResolveInput{
		NodeKind:     spec.NodeKind,
		NodeRole:     spec.NodeRole,
		RuntimeClass: spec.RuntimeClass,
	})
	if err != nil {
		return ProfileStatus{AuthorityProfile: spec.AuthorityProfile, RuntimeProfile: spec.RuntimeProfile, Error: err.Error()}
	}
	return ProfileStatus{
		AuthorityProfile: firstNonEmpty(spec.AuthorityProfile, assignment.AuthorityProfileKey),
		RuntimeProfile:   firstNonEmpty(spec.RuntimeProfile, assignment.RuntimeProfileKey),
		Valid:            true,
	}
}

func pathStatuses(spec SetupSpec, paths PathPlan, migration *BoxStateMigrationPlan) []SetupPathStatus {
	out := []SetupPathStatus{
		checkPathStatus("config_dir", paths.ConfigDir, true, true),
		checkPathStatus("data_dir", paths.DataDir, true, true),
		checkPathStatus("state_dir", paths.StateDir, true, true),
		checkPathStatus("log_dir", paths.LogDir, true, true),
	}
	if spec.EnableBox {
		canonicalStateRequired := migration == nil || migration.State != "ready"
		out = append(out,
			checkPathStatus("box_root", paths.BoxPath, true, true),
			checkPathStatus("box_loom_dir", paths.BoxLoomDir, true, true),
			checkPathStatus("box_state_root", paths.BoxStateRoot, true, canonicalStateRequired),
		)
	}
	if spec.EnableLoomd && paths.ObjectStorePath != "" {
		out = append(out, checkPathStatus("object_store", paths.ObjectStorePath, true, true))
	}
	if spec.EnableLoomd {
		out = append(out,
			checkPathStatus("service_root", paths.ServiceRoot, true, true),
			checkPathStatus("storage_root", paths.StorageRoot, true, true),
			checkPathStatus("imports_root", paths.ImportsRoot, true, true),
			checkPathStatus("user_backups_root", paths.UserBackupsRoot, true, true),
			checkPathStatus("archive_root", paths.ArchiveRoot, true, true),
			checkPathStatus("generated_root", paths.GeneratedRoot, true, true),
		)
		if paths.MainDocumentsPath != "" {
			out = append(out, checkPathStatus("legacy_main_documents", paths.MainDocumentsPath, true, false))
		}
	}
	if spec.EnableLoomd && paths.SocketPath != "" {
		out = append(out, checkPathStatus("socket_parent", filepath.Dir(paths.SocketPath), true, true))
	}
	if spec.EnableCloud {
		borgRequired := spec.CloudSnapshotBackend == "borg"
		out = append(out,
			checkPathStatus("cloud_config_dir", paths.CloudConfigDir, true, true),
			checkPathStatus("cloud_state_dir", paths.CloudStateDir, true, true),
			checkPathStatus("cloud_rclone_config", paths.CloudRcloneConfigPath, false, false),
			checkPathStatus("cloud_borg_cache_dir", paths.CloudBorgCacheDir, true, true),
			checkPathStatus("cloud_borg_security_dir", paths.CloudBorgSecurityDir, true, true),
			checkPathStatus("cloud_borg_passphrase_file", paths.CloudBorgPassphraseFile, false, borgRequired),
		)
	}
	if spec.EnableNodeAgent {
		out = append(out,
			checkPathStatus("node_agent_config", paths.NodeAgentConfigPath, false, true),
			checkPathStatus("node_agent_state", paths.NodeAgentStatePath, false, true),
			checkPathStatus("node_agent_data_dir", paths.NodeAgentDataDir, true, true),
		)
	}
	return out
}

func checkPathStatus(key, path string, wantDir bool, required bool) SetupPathStatus {
	status := SetupPathStatus{Key: key, Path: path, Required: required, Status: "missing"}
	if strings.TrimSpace(path) == "" {
		status.Status = "not_configured"
		return status
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return status
		}
		status.Status = "error"
		return status
	}
	status.Exists = true
	status.IsDir = info.IsDir()
	access := fsaccess.Check(path, fsaccess.Read, fsaccess.Write, fsaccess.Execute)
	status.Readable = access.Readable
	status.Writable = access.Writable
	status.Executable = access.Executable
	status.AccessError = access.Error
	status.MissingMode = accessRequirementsAsStrings(access.MissingModes)
	switch {
	case wantDir && !status.IsDir:
		status.Status = "wrong_type"
	case !wantDir && status.IsDir:
		status.Status = "wrong_type"
	default:
		status.Status = "present"
	}
	return status
}

func accessRequirementsAsStrings(requirements []fsaccess.Requirement) []string {
	out := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		out = append(out, string(requirement))
	}
	return out
}

func binaryStatuses(spec SetupSpec, facts TargetFacts) []BinaryStatus {
	requiredLoomd := spec.EnableLoomd
	requiredAgent := spec.EnableNodeAgent
	statuses := []BinaryStatus{
		binaryStatus("loom", facts.ExistingLoom, true),
		binaryStatus("loomd", facts.ExistingLoomd, requiredLoomd),
		binaryStatus("loom-node-agent", facts.ExistingNodeAgent, requiredAgent),
	}
	if spec.EnableCloud && spec.CloudSnapshotBackend == "borg" {
		statuses = append(statuses, binaryStatus("borg", binaryFact("borg", facts.HomeDir), true))
	}
	return statuses
}

func binaryStatus(name string, fact BinaryFact, required bool) BinaryStatus {
	if strings.TrimSpace(fact.Name) == "" {
		fact.Name = name
	}
	status := "optional_missing"
	if required {
		status = "missing"
	}
	if fact.Found {
		status = "present"
	}
	return BinaryStatus{Name: fact.Name, Path: fact.Path, Found: fact.Found, Required: required, Status: status}
}

func serviceStatuses(spec SetupSpec, paths PathPlan, runner LaunchdRunner) []ServiceStatus {
	if spec.InstallMode != InstallModeService || spec.ServiceManager == ServiceManagerNone {
		return nil
	}
	if spec.ServiceManager == ServiceManagerLaunchd {
		return []ServiceStatus{launchdServiceStatus(spec, paths, runner)}
	}
	name := "loomd"
	if spec.EnableNodeAgent && !spec.EnableLoomd {
		name = "loom-node-agent"
	}
	return []ServiceStatus{{Name: name, Manager: spec.ServiceManager, Expected: true, Status: "unknown"}}
}

func boxStatus(spec SetupSpec, paths PathPlan, migration *BoxStateMigrationPlan) BoxSetupStatus {
	status := BoxSetupStatus{
		Enabled:           spec.EnableBox,
		Profile:           spec.BoxProfile,
		Path:              paths.BoxPath,
		LoomDir:           paths.BoxLoomDir,
		Status:            "disabled",
		RuntimeStateRoot:  paths.BoxStateRoot,
		LegacyStateRoot:   paths.LegacyBoxStateRoot,
		LegacyStateExists: pathExists(paths.LegacyBoxStateRoot),
	}
	if migration != nil {
		status.MigrationState = migration.State
	}
	if !spec.EnableBox {
		return status
	}
	status.Exists = pathExists(paths.BoxPath)
	status.LoomDirExists = pathExists(paths.BoxLoomDir)
	switch {
	case status.Exists && status.LoomDirExists:
		status.Status = "configured"
	case status.Exists:
		status.Status = "partial"
	default:
		status.Status = "missing"
	}
	return status
}

func nodeAgentStatus(spec SetupSpec, paths PathPlan, enrollment EnrollmentStatus) NodeAgentSetupStatus {
	status := NodeAgentSetupStatus{
		Enabled:              spec.EnableNodeAgent,
		ConfigPath:           paths.NodeAgentConfigPath,
		StatePath:            paths.NodeAgentStatePath,
		DataDir:              paths.NodeAgentDataDir,
		CredentialConfigured: enrollment.CredentialConfigured,
		Status:               "disabled",
	}
	if !spec.EnableNodeAgent {
		return status
	}
	status.ConfigExists = pathExists(paths.NodeAgentConfigPath)
	status.StateExists = pathExists(paths.NodeAgentStatePath)
	status.DataDirExists = pathExists(paths.NodeAgentDataDir)
	switch {
	case status.ConfigExists && status.DataDirExists && status.CredentialConfigured:
		status.Status = "configured"
	case status.ConfigExists || status.StateExists || status.DataDirExists:
		status.Status = "partial"
	default:
		status.Status = "not_initialized"
	}
	return status
}

func enrollmentStatusFromManifest(manifest InstallManifest, manifestExists bool) EnrollmentStatus {
	if !manifestExists {
		return EnrollmentStatus{Status: "not_started"}
	}
	status := strings.TrimSpace(manifest.Enrollment.Status)
	if status == "" {
		status = "not_started"
	}
	if manifest.Credential.Configured {
		status = "credential_configured"
	}
	return EnrollmentStatus{
		Status:               status,
		EnrollmentRequestID:  manifest.Enrollment.EnrollmentRequestID,
		CredentialConfigured: manifest.Credential.Configured,
		NodeCredentialID:     manifest.Credential.NodeCredentialID,
		CredentialHint:       manifest.Credential.CredentialHint,
		FailureCode:          manifest.Enrollment.FailureCode,
		FailureMessage:       manifest.Enrollment.FailureMessage,
		PresenceState:        manifest.Enrollment.PresenceState,
		VerifiedOnMain:       manifest.Enrollment.VerifiedOnMain,
	}
}

func connectivityStatus(spec SetupSpec) ConnectivityStatus {
	required := spec.NodeKind != "" && spec.NodeKind != "main"
	status := ConnectivityStatus{MainURL: spec.MainURL, Required: required, Status: "not_required"}
	if required {
		if strings.TrimSpace(spec.MainURL) == "" {
			status.Status = "missing_url"
		} else {
			status.Status = "not_checked"
		}
	}
	return status
}

func summarizeStatus(status SetupStatus) string {
	if !status.Manifest.Exists {
		return SummaryNotInstalled
	}
	if status.Manifest.Manifest != nil {
		last := strings.TrimSpace(status.Manifest.Manifest.LastStatus.Status)
		switch last {
		case "disabled", "decommissioned", "decommission_pending", "uninstalled_preserve_data", "purged":
			return last
		}
	}
	if status.Manifest.State == "invalid" || status.Profiles.Error != "" || hasErrorDiagnostic(status.Diagnostics) {
		return SummaryBlocked
	}
	if hasMissingRequiredPath(status.Paths) {
		return SummaryPartial
	}
	if status.NodeAgent.Enabled && !status.NodeAgent.CredentialConfigured {
		return SummaryPartial
	}
	if strings.TrimSpace(status.Manifest.Manifest.LastStatus.Status) == SummaryHealthy {
		return SummaryHealthy
	}
	return SummaryConfigured
}

func hasMissingRequiredPath(paths []SetupPathStatus) bool {
	for _, path := range paths {
		if path.Required && path.Status != "present" {
			return true
		}
	}
	return false
}
