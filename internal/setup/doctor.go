package setup

import (
	"fmt"
	"strings"
	"time"
)

func Doctor(input DoctorInput) (DoctorReport, error) {
	status, err := Status(input.StatusInput)
	if err != nil {
		return DoctorReport{}, err
	}
	findings := []DoctorFinding{}
	if !status.Manifest.Exists {
		findings = append(findings, DoctorFinding{
			Code:       "setup.manifest.missing",
			Severity:   DiagnosticError,
			Message:    "No install manifest found.",
			Evidence:   map[string]any{"path": status.Manifest.Path},
			RepairID:   RepairRewriteRedactedManifest,
			RepairHint: "loom setup repair --fix rewrite-redacted-manifest --yes",
		})
	} else if status.Manifest.State == "invalid" {
		findings = append(findings, DoctorFinding{
			Code:       "setup.manifest.invalid",
			Severity:   DiagnosticBlocking,
			Message:    "Install manifest could not be parsed.",
			Evidence:   map[string]any{"path": status.Manifest.Path, "error": status.Manifest.Error},
			RepairHint: "Inspect the manifest before repair.",
		})
	} else if status.Manifest.SchemaVersion != ManifestSchemaVersion {
		findings = append(findings, DoctorFinding{
			Code:     "setup.manifest.unsupported_schema",
			Severity: DiagnosticWarning,
			Message:  "Install manifest schema differs from this setup implementation.",
			Evidence: map[string]any{"path": status.Manifest.Path, "schema_version": status.Manifest.SchemaVersion},
		})
	}
	if status.Manifest.Exists {
		if keys := ManifestSensitiveKeys(status.Manifest.Path); len(keys) > 0 {
			findings = append(findings, DoctorFinding{
				Code:       "setup.manifest.raw_secret",
				Severity:   DiagnosticBlocking,
				Message:    "Install manifest contains unredacted sensitive keys.",
				Evidence:   map[string]any{"path": status.Manifest.Path, "keys": keys},
				RepairID:   RepairRewriteRedactedManifest,
				RepairHint: "loom setup repair --fix rewrite-redacted-manifest --yes",
			})
		}
	}
	if status.Profiles.Error != "" {
		findings = append(findings, DoctorFinding{
			Code:     "setup.profile.invalid",
			Severity: DiagnosticBlocking,
			Message:  "Node kind, role, and runtime class are incompatible.",
			Evidence: map[string]any{"error": status.Profiles.Error, "node": status.Node},
		})
	}
	for _, diagnostic := range status.Diagnostics {
		if diagnostic.Severity == DiagnosticError || diagnostic.Severity == DiagnosticWarning {
			findings = append(findings, DoctorFinding{
				Code:       diagnostic.Code,
				Severity:   diagnostic.Severity,
				Message:    diagnostic.Message,
				Evidence:   map[string]any{"field": diagnostic.Field, "path": diagnostic.Path},
				RepairHint: diagnostic.RepairHint,
			})
		}
	}
	if !setupStatusIsIntentionallyInactive(status.Summary.Status) {
		for _, pathStatus := range status.Paths {
			if !pathStatus.Required || pathStatus.Status == "present" || pathStatus.Status == "not_configured" {
				continue
			}
			repairID := repairIDForPath(pathStatus.Key)
			findings = append(findings, DoctorFinding{
				Code:       "setup.path." + pathStatus.Key + "." + pathStatus.Status,
				Severity:   pathSeverity(pathStatus),
				Message:    fmt.Sprintf("Expected setup path is %s: %s", pathStatus.Status, pathStatus.Path),
				Evidence:   map[string]any{"key": pathStatus.Key, "path": pathStatus.Path, "status": pathStatus.Status},
				RepairID:   repairID,
				RepairHint: repairHintForID(repairID),
			})
		}
		for _, pathStatus := range status.Paths {
			if !pathStatus.Required || pathStatus.Status != "present" {
				continue
			}
			if !pathStatus.IsDir {
				if setupPathRequiresRead(status, pathStatus.Key) && !pathStatus.Readable {
					findings = append(findings, pathAccessFinding(pathStatus, []string{"read"}, "Expected setup file is not readable."))
				}
				continue
			}
			missing := []string{}
			if setupPathRequiresRead(status, pathStatus.Key) && !pathStatus.Readable {
				missing = append(missing, "read")
			}
			if setupPathRequiresExecute(status, pathStatus.Key) && !pathStatus.Executable {
				missing = append(missing, "execute")
			}
			if setupPathRequiresWrite(status, pathStatus.Key) && !pathStatus.Writable {
				missing = append(missing, "write")
			}
			if len(missing) > 0 {
				findings = append(findings, pathAccessFinding(pathStatus, missing, "Expected setup directory does not grant required runtime access."))
			}
		}
	}
	if status.Manifest.Exists && status.Box.Enabled && !setupStatusIsIntentionallyInactive(status.Summary.Status) {
		if status.Box.MigrationState == "ready" {
			findings = append(findings, DoctorFinding{
				Code:     "setup.box.legacy_state_migration_ready",
				Severity: DiagnosticWarning,
				Message:  "Legacy Box runtime state remains readable but requires an explicit migration before canonical-only reads.",
				Evidence: map[string]any{"source_root": status.Box.LegacyStateRoot, "target_root": status.Box.RuntimeStateRoot},
			})
		}
		if status.Box.MigrationState == "conflict" {
			findings = append(findings, DoctorFinding{
				Code:     "setup.box.runtime_state_conflict",
				Severity: DiagnosticBlocking,
				Message:  "Legacy and canonical Box runtime state diverge; automatic reconciliation is forbidden.",
				Evidence: map[string]any{"source_root": status.Box.LegacyStateRoot, "target_root": status.Box.RuntimeStateRoot},
			})
		}
		if status.Box.Profile == "main" && status.Node.NodeKind != "main" {
			findings = append(findings, DoctorFinding{
				Code:     "setup.box.profile_mismatch",
				Severity: DiagnosticWarning,
				Message:  "Non-main node is configured with the main Box profile.",
				Evidence: map[string]any{"profile": status.Box.Profile, "node_kind": status.Node.NodeKind},
			})
		}
		if status.Node.NodeKind == "main" && status.Box.Profile != "main" {
			findings = append(findings, DoctorFinding{
				Code:     "setup.box.main_profile_required",
				Severity: DiagnosticError,
				Message:  "Main node Box profile should be main.",
				Evidence: map[string]any{"profile": status.Box.Profile},
			})
		}
	}
	if !setupStatusIsIntentionallyInactive(status.Summary.Status) {
		for _, humanLink := range status.HumanLinks {
			if !humanLinkNeedsRepair(humanLink) {
				continue
			}
			findings = append(findings, DoctorFinding{
				Code:       "setup.human_link." + sanitizeID(humanLink.Key) + "." + humanLink.Status,
				Severity:   humanLinkSeverity(humanLink),
				Message:    fmt.Sprintf("Expected human-facing %s link is %s: %s", humanLink.Kind, humanLink.Status, humanLink.LinkPath),
				Evidence:   map[string]any{"key": humanLink.Key, "kind": humanLink.Kind, "intent": humanLink.Intent, "link_path": humanLink.LinkPath, "target_path": humanLink.TargetPath, "status": humanLink.Status, "message": humanLink.Message},
				RepairID:   RepairMainHumanLinks,
				RepairHint: repairHintForID(RepairMainHumanLinks),
			})
		}
	}
	if status.NodeAgent.Enabled && !setupStatusIsIntentionallyInactive(status.Summary.Status) {
		if !status.NodeAgent.ConfigExists {
			findings = append(findings, DoctorFinding{
				Code:     "setup.node_agent.config_missing",
				Severity: DiagnosticWarning,
				Message:  "Node-agent config file is missing.",
				Evidence: map[string]any{"path": status.NodeAgent.ConfigPath},
			})
		}
		if status.Enrollment.Status == "failed" {
			code := firstNonEmpty(status.Enrollment.FailureCode, "setup.enrollment.failed")
			if code == "credential_token_unavailable" {
				code = "setup.enrollment.credential_import_unrecoverable"
			}
			findings = append(findings, DoctorFinding{
				Code:     code,
				Severity: DiagnosticError,
				Message:  firstNonEmpty(status.Enrollment.FailureMessage, "Node enrollment failed."),
				Evidence: map[string]any{
					"enrollment_status":     status.Enrollment.Status,
					"enrollment_request_id": status.Enrollment.EnrollmentRequestID,
				},
				RepairHint: "Re-run enrollment or rotate the node credential.",
			})
		}
		if !status.NodeAgent.CredentialConfigured && status.Manifest.Exists {
			severity := DiagnosticError
			if status.Enrollment.Status == "skipped" {
				severity = DiagnosticWarning
			}
			findings = append(findings, DoctorFinding{
				Code:     "setup.enrollment.credential_missing",
				Severity: severity,
				Message:  "Node credential is not configured in the install manifest.",
				Evidence: map[string]any{"enrollment_status": status.Enrollment.Status},
			})
		}
	}
	if status.MainConnectivity.Required && strings.TrimSpace(status.MainConnectivity.MainURL) == "" {
		findings = append(findings, DoctorFinding{
			Code:     "setup.main_url.missing",
			Severity: DiagnosticError,
			Message:  "Non-main node setup requires a main URL for enrollment and heartbeat.",
			Evidence: map[string]any{"node_kind": status.Node.NodeKind},
		})
	}
	for _, binary := range status.Binaries {
		if binary.Required && !binary.Found {
			repairID := ""
			repairHint := ""
			if workspaceBinaryLinksRepairable(status) {
				repairID = RepairRefreshWorkspaceBinaryLinks
				repairHint = repairHintForID(repairID)
			}
			findings = append(findings, DoctorFinding{
				Code:       "setup.binary." + binary.Name + ".missing",
				Severity:   DiagnosticWarning,
				Message:    fmt.Sprintf("Required binary is not on PATH: %s", binary.Name),
				Evidence:   map[string]any{"binary": binary.Name},
				RepairID:   repairID,
				RepairHint: repairHint,
			})
		}
	}
	if !setupStatusIsIntentionallyInactive(status.Summary.Status) {
		for _, service := range status.Services {
			if !service.Expected {
				continue
			}
			switch service.Status {
			case "missing":
				findings = append(findings, DoctorFinding{
					Code:       "setup.service." + sanitizeID(firstNonEmpty(service.Name, service.Label)) + ".missing",
					Severity:   DiagnosticError,
					Message:    "Expected LaunchAgent plist is missing.",
					Evidence:   map[string]any{"manager": service.Manager, "label": service.Label, "path": service.Path, "status": service.Status},
					RepairID:   RepairInstallLaunchAgent,
					RepairHint: repairHintForID(RepairInstallLaunchAgent),
				})
			case "not_loaded":
				findings = append(findings, DoctorFinding{
					Code:       "setup.service." + sanitizeID(firstNonEmpty(service.Name, service.Label)) + ".not_loaded",
					Severity:   DiagnosticError,
					Message:    "Expected LaunchAgent is not loaded.",
					Evidence:   map[string]any{"manager": service.Manager, "label": service.Label, "path": service.Path, "status": service.Status, "message": service.Message},
					RepairID:   RepairInstallLaunchAgent,
					RepairHint: repairHintForID(RepairInstallLaunchAgent),
				})
			}
		}
	}

	report := DoctorReport{
		CheckedAt: status.CheckedAt,
		Summary:   summarizeDoctor(findings),
		Findings:  findings,
		Status:    status,
	}
	report.Repairs = repairActionsForFindings(findings, status)
	if input.Strict && report.Summary == SummaryConfigured {
		report.Summary = SummaryHealthy
	}
	return report, nil
}

func setupPathRequiresWrite(status SetupStatus, key string) bool {
	if setupStatusUsesSystemdService(status) {
		return setupSystemdPathRequiresCurrentActorWrite(key)
	}
	return setupUserPathRequiresCurrentActorWrite(key)
}

func setupPathRequiresRead(status SetupStatus, key string) bool {
	if setupStatusUsesSystemdService(status) {
		return setupSystemdPathRequiresCurrentActorRead(key)
	}
	return true
}

func setupPathRequiresExecute(status SetupStatus, key string) bool {
	if setupStatusUsesSystemdService(status) {
		return setupSystemdPathRequiresCurrentActorExecute(key)
	}
	return true
}

func setupStatusUsesSystemdService(status SetupStatus) bool {
	if status.Plan == nil {
		return false
	}
	spec := status.Plan.Spec
	return spec.InstallMode == InstallModeService && spec.ServiceManager == ServiceManagerSystemd
}

func setupSystemdPathRequiresCurrentActorRead(key string) bool {
	switch key {
	case "box_root", "box_loom_dir", "config_dir":
		return true
	default:
		return false
	}
}

func setupSystemdPathRequiresCurrentActorExecute(key string) bool {
	switch key {
	case "box_root", "box_loom_dir", "config_dir":
		return true
	default:
		return false
	}
}

func setupSystemdPathRequiresCurrentActorWrite(key string) bool {
	switch key {
	case "box_root", "box_loom_dir":
		return true
	default:
		return false
	}
}

func setupUserPathRequiresCurrentActorWrite(key string) bool {
	switch key {
	case "config_dir", "data_dir", "state_dir", "log_dir", "box_loom_dir", "box_state_root", "object_store", "service_root", "storage_root", "imports_root", "user_backups_root", "archive_root", "generated_root", "socket_parent", "cloud_state_dir", "cloud_borg_cache_dir", "cloud_borg_security_dir", "node_agent_data_dir":
		return true
	default:
		return false
	}
}

func pathAccessFinding(pathStatus SetupPathStatus, missing []string, message string) DoctorFinding {
	repairID := ""
	if pathStatus.Key == "box_root" || pathStatus.Key == "box_loom_dir" {
		repairID = RepairMainBoxServiceACL
	}
	return DoctorFinding{
		Code:       "setup.path." + pathStatus.Key + ".access_denied",
		Severity:   DiagnosticError,
		Message:    message,
		Evidence:   map[string]any{"key": pathStatus.Key, "path": pathStatus.Path, "missing_access": missing, "error": pathStatus.AccessError},
		RepairID:   repairID,
		RepairHint: repairHintForID(repairID),
	}
}

func setupStatusIsIntentionallyInactive(status string) bool {
	switch strings.TrimSpace(status) {
	case "disabled", "decommissioned", "decommission_pending", "uninstalled_preserve_data", "purged":
		return true
	default:
		return false
	}
}

func summarizeDoctor(findings []DoctorFinding) string {
	if len(findings) == 0 {
		return SummaryConfigured
	}
	hasBlocking := false
	hasError := false
	for _, finding := range findings {
		switch finding.Severity {
		case DiagnosticBlocking:
			hasBlocking = true
		case DiagnosticError:
			hasError = true
		}
	}
	if hasBlocking {
		return SummaryBlocked
	}
	if hasError {
		return SummaryDegraded
	}
	return SummaryPartial
}

func repairIDForPath(key string) string {
	switch key {
	case "config_dir":
		return RepairCreateMissingConfigDir
	case "data_dir":
		return RepairCreateMissingDataDir
	case "state_dir":
		return RepairCreateMissingStateDir
	case "log_dir":
		return RepairCreateMissingLogDir
	case "box_root":
		return RepairCreateMissingBoxRoot
	case "box_loom_dir":
		return RepairCreateMissingBoxLoomDir
	case "box_state_root":
		return RepairCreateMissingBoxStateRoot
	case "service_root":
		return RepairCreateMissingServiceRoot
	case "storage_root":
		return RepairCreateMissingStorageRoot
	case "imports_root":
		return RepairCreateMissingImportsRoot
	case "user_backups_root":
		return RepairCreateMissingUserBackupsRoot
	case "archive_root":
		return RepairCreateMissingArchiveRoot
	case "generated_root":
		return RepairCreateMissingGeneratedRoot
	case "main_documents":
		return RepairCreateMissingMainDocuments
	case "storage_export_root":
		return ""
	case "node_agent_data_dir":
		return RepairCreateMissingNodeAgentDataDir
	default:
		return ""
	}
}

func workspaceBinaryLinksRepairable(status SetupStatus) bool {
	return len(workspaceBinaryLinkTargets(status)) > 0
}

func repairHintForID(id string) string {
	if id == "" {
		return ""
	}
	return "loom setup repair --fix " + id + " --yes"
}

func pathSeverity(pathStatus SetupPathStatus) string {
	if pathStatus.Status == "wrong_type" || pathStatus.Status == "error" {
		return DiagnosticError
	}
	return DiagnosticWarning
}

func repairActionsForFindings(findings []DoctorFinding, status SetupStatus) []RepairAction {
	seen := map[string]bool{}
	actions := []RepairAction{}
	for _, finding := range findings {
		if finding.RepairID == "" || seen[finding.RepairID] {
			continue
		}
		action, ok := repairActionForID(finding.RepairID, status)
		if !ok {
			continue
		}
		action.FindingCode = finding.Code
		actions = append(actions, action)
		seen[action.ID] = true
	}
	return actions
}

func nowPtr(t time.Time) *time.Time {
	return &t
}
