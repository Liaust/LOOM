package projectdoctor

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/projectaccess"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/projectwatch"
)

type Status string

const (
	StatusOK      Status = "ok"
	StatusWarning Status = "warning"
	StatusError   Status = "error"
	StatusBlocked Status = "blocked"
	StatusUnknown Status = "unknown"
	StatusSkipped Status = "skipped"
)

type Check struct {
	Key         string          `json:"key"`
	Title       string          `json:"title"`
	Status      Status          `json:"status"`
	Summary     string          `json:"summary"`
	Detail      string          `json:"detail,omitempty"`
	Facet       string          `json:"facet,omitempty"`
	SubjectRef  string          `json:"subject_ref,omitempty"`
	Remediation string          `json:"remediation,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type Summary struct {
	OK       int `json:"ok"`
	Warnings int `json:"warnings"`
	Errors   int `json:"errors"`
	Blocked  int `json:"blocked"`
	Unknown  int `json:"unknown"`
	Skipped  int `json:"skipped"`
}

type Report struct {
	ProjectRef string    `json:"project_ref,omitempty"`
	Source     string    `json:"source"`
	Summary    Summary   `json:"summary"`
	Checks     []Check   `json:"checks"`
	CreatedAt  time.Time `json:"created_at"`
}

type BackendDriftStatus string

const (
	BackendDriftCurrent      BackendDriftStatus = "current"
	BackendDriftStale        BackendDriftStatus = "stale"
	BackendDriftUnregistered BackendDriftStatus = "unregistered"
	BackendDriftInvalid      BackendDriftStatus = "invalid"
	BackendDriftUnavailable  BackendDriftStatus = "unavailable"
)

type BackendAnalysisResult struct {
	ProjectRef             string                              `json:"project_ref,omitempty"`
	ProjectRoot            string                              `json:"project_root,omitempty"`
	Analysis               projectcontracts.Analysis           `json:"analysis"`
	Detail                 *projects.ProjectRegistrationDetail `json:"detail,omitempty"`
	Diff                   DiffReport                          `json:"diff"`
	Report                 Report                              `json:"doctor"`
	DriftStatus            BackendDriftStatus                  `json:"drift_status"`
	Current                bool                                `json:"current"`
	CurrentContractHash    string                              `json:"current_contract_hash,omitempty"`
	RegisteredContractHash string                              `json:"registered_contract_hash,omitempty"`
	NextAction             string                              `json:"next_action,omitempty"`
}

type ReportInput struct {
	ProjectRef      string
	Source          string
	Local           *projectcontracts.Analysis
	Detail          *projects.ProjectRegistrationDetail
	Sync            *projectwatch.ProjectSyncStatus
	Backup          *projectwatch.ProjectBackupStatus
	RepositoryState *projectstate.ProjectProjection
	CreatedAt       time.Time
}

func BuildBackendAnalysisResult(analysis projectcontracts.Analysis, detail *projects.ProjectRegistrationDetail) BackendAnalysisResult {
	projectRef := firstNonEmpty(localProjectRef(&analysis), detailProjectRef(detail))
	currentHash := ""
	if analysis.Loaded != nil {
		currentHash = contractHash(analysis.Loaded.Raw)
	}
	registeredHash := ""
	if detail != nil && detail.Registration != nil {
		registeredHash = strings.TrimSpace(detail.Registration.ContractHash)
	}
	diff := BuildDiff(analysis, detail)
	report := BuildReport(ReportInput{
		ProjectRef: projectRef,
		Source:     "project.backend_analysis",
		Local:      &analysis,
		Detail:     detail,
	})
	result := BackendAnalysisResult{
		ProjectRef:             projectRef,
		ProjectRoot:            analysis.Report.ProjectRoot,
		Analysis:               analysis,
		Detail:                 detail,
		Diff:                   diff,
		Report:                 report,
		DriftStatus:            BackendDriftCurrent,
		Current:                true,
		CurrentContractHash:    currentHash,
		RegisteredContractHash: registeredHash,
		NextAction:             "no registration needed",
	}
	switch {
	case analysis.Loaded == nil || !analysis.Report.OK || analysis.Report.Summary.Errors > 0:
		result.DriftStatus = BackendDriftInvalid
		result.Current = false
		result.NextAction = "fix backend project contract"
	case detail == nil || detail.Registration == nil:
		result.DriftStatus = BackendDriftUnregistered
		result.Current = false
		result.NextAction = "register contract from backend"
	case currentHash == "" || registeredHash == "":
		result.DriftStatus = BackendDriftUnavailable
		result.Current = false
		result.NextAction = "inspect registration and backend contract"
	case currentHash != registeredHash || diff.Summary.HasChanges():
		result.DriftStatus = BackendDriftStale
		result.Current = false
		result.NextAction = "re-register contract from backend"
	}
	return result
}

func BuildReport(input ReportInput) Report {
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = "project.doctor"
	}
	projectRef := firstNonEmpty(input.ProjectRef, localProjectRef(input.Local), detailProjectRef(input.Detail))
	checks := []Check{}
	add := func(check Check) {
		check.Key = strings.TrimSpace(check.Key)
		check.Title = strings.TrimSpace(check.Title)
		check.Summary = strings.TrimSpace(check.Summary)
		if check.Metadata == nil {
			check.Metadata = json.RawMessage(`{}`)
		}
		checks = append(checks, check)
	}

	if input.Local == nil {
		add(Check{
			Key:         "local_contract.skipped",
			Title:       "Local Contract",
			Status:      StatusSkipped,
			Summary:     "No local project folder was analyzed.",
			Remediation: "Pass a project folder or --project-root when local drift checks are required.",
		})
	} else if input.Local.Loaded == nil {
		add(Check{
			Key:         "local_contract.load_failed",
			Title:       "Local Contract",
			Status:      StatusError,
			Summary:     "Project contract could not be loaded.",
			Detail:      diagnosticsSummary(input.Local.Report.Diagnostics),
			Remediation: "Fix the local project folder and run loom project validate.",
		})
	} else if input.Local.Report.Summary.Errors > 0 {
		add(Check{
			Key:         "local_contract.invalid",
			Title:       "Local Contract",
			Status:      StatusError,
			Summary:     fmt.Sprintf("Project contract has %d validation error(s).", input.Local.Report.Summary.Errors),
			Detail:      diagnosticsSummary(input.Local.Report.Diagnostics),
			Remediation: "Run loom project validate and fix the reported errors before registering or activating.",
		})
	} else if input.Local.Report.Summary.Warnings > 0 {
		add(Check{
			Key:     "local_contract.warning",
			Title:   "Local Contract",
			Status:  StatusWarning,
			Summary: fmt.Sprintf("Project contract is valid with %d warning(s).", input.Local.Report.Summary.Warnings),
			Detail:  diagnosticsSummary(input.Local.Report.Diagnostics),
		})
	} else {
		add(Check{
			Key:        "local_contract.ok",
			Title:      "Local Contract",
			Status:     StatusOK,
			Summary:    "Project contract loads and validates.",
			SubjectRef: localProjectRef(input.Local),
		})
	}

	if input.Detail == nil || input.Detail.Project.Project.ProjectID == "" {
		add(Check{
			Key:         "registration.missing",
			Title:       "Registration",
			Status:      StatusError,
			Summary:     "Project registration was not loaded from the backend.",
			Remediation: "Run loom project register <project-folder> or check the project ref.",
		})
	} else if input.Detail.Registration == nil {
		add(Check{
			Key:         "registration.absent",
			Title:       "Registration",
			Status:      StatusError,
			Summary:     "Project exists but has no registered contract.",
			SubjectRef:  detailProjectRef(input.Detail),
			Remediation: "Run loom project register <project-folder>.",
		})
	} else {
		add(Check{
			Key:        "registration.present",
			Title:      "Registration",
			Status:     StatusOK,
			Summary:    "Project has a registered contract.",
			SubjectRef: input.Detail.Registration.ProjectContractRegistrationID,
		})
		if input.Local != nil && input.Local.Loaded != nil {
			localHash := contractHash(input.Local.Loaded.Raw)
			if localHash != input.Detail.Registration.ContractHash {
				add(Check{
					Key:         "registration.stale",
					Title:       "Contract Drift",
					Status:      StatusBlocked,
					Summary:     "Local contract hash does not match the registered contract hash.",
					Detail:      fmt.Sprintf("local=%s registered=%s", localHash, input.Detail.Registration.ContractHash),
					Remediation: "Run loom project diff, then loom project register <project-folder> if the local changes should become active.",
				})
			} else {
				add(Check{
					Key:     "registration.current",
					Title:   "Contract Drift",
					Status:  StatusOK,
					Summary: "Local contract hash matches the registered contract hash.",
				})
			}
		}
	}

	if input.Detail != nil && input.Detail.Registration != nil {
		if input.Detail.Registration.ActivationStatus == projects.ProjectActivationStatusBaseActive {
			add(Check{Key: "activation.base_active", Title: "Base Activation", Status: StatusOK, Summary: "Project base registration is active."})
		} else {
			add(Check{
				Key:         "activation.base_inactive",
				Title:       "Base Activation",
				Status:      StatusWarning,
				Summary:     "Project base registration is not active.",
				Detail:      input.Detail.Registration.ActivationStatus,
				Remediation: "Run loom project activate <project-ref>.",
			})
		}
		for _, facet := range input.Detail.Facets {
			add(facetCheck(facet))
		}
		checks = append(checks, scriptExposureChecks(input.Detail.ScriptExposures)...)
		checks = append(checks, scheduleChecks(input.Detail.ScheduleRegistrations)...)
		checks = append(checks, directEventChecks(input.Detail.DirectEventRegistrations)...)
		checks = append(checks, connectorChecks(input.Detail.ConnectorRegistrations)...)
		checks = append(checks, moduleChecks(input.Detail.ModuleRegistrations)...)
		checks = append(checks, watchedRootChecks(input.Detail.WatchedRootRegistrations)...)
		checks = append(checks, workflowChecks(input.Detail)...)
	}

	if input.Local != nil && input.Local.Loaded != nil {
		for _, runtimeCheck := range runtimeAccessChecks(*input.Local) {
			add(runtimeCheck)
		}
	}

	if input.Sync != nil {
		add(Check{
			Key:        "sync.visible",
			Title:      "Sync Status",
			Status:     statusFromBlockedCounts(input.Sync.Blocked, input.Sync.Stale, input.Sync.PendingAgentApply),
			Summary:    fmt.Sprintf("%d sync root(s), %d pending agent apply, %d stale, %d blocked.", input.Sync.SyncRoots, input.Sync.PendingAgentApply, input.Sync.Stale, input.Sync.Blocked),
			SubjectRef: input.Sync.ProjectRef,
		})
	}
	if input.Backup != nil {
		add(Check{
			Key:        "backup.visible",
			Title:      "Backup Status",
			Status:     statusFromBlockedCounts(input.Backup.Blocked, input.Backup.Stale, input.Backup.PendingAgentApply),
			Summary:    fmt.Sprintf("%d backup root(s), %d pending agent apply, %d stale, %d blocked.", input.Backup.BackupRoots, input.Backup.PendingAgentApply, input.Backup.Stale, input.Backup.Blocked),
			SubjectRef: input.Backup.ProjectRef,
		})
	}
	if input.RepositoryState != nil {
		for _, check := range repositoryStateChecks(*input.RepositoryState) {
			add(check)
		}
	}

	sort.SliceStable(checks, func(i, j int) bool {
		if statusRank(checks[i].Status) != statusRank(checks[j].Status) {
			return statusRank(checks[i].Status) < statusRank(checks[j].Status)
		}
		if checks[i].Facet != checks[j].Facet {
			return checks[i].Facet < checks[j].Facet
		}
		return checks[i].Key < checks[j].Key
	})
	return Report{
		ProjectRef: projectRef,
		Source:     source,
		Summary:    Summarize(checks),
		Checks:     checks,
		CreatedAt:  createdAt,
	}
}

func repositoryStateChecks(state projectstate.ProjectProjection) []Check {
	checks := []Check{}
	summaryStatus := StatusOK
	summaryText := fmt.Sprintf("%d repository member(s): %d observed, %d not observed, %d remote unavailable.", state.Observation.MemberCount, state.Observation.Observed, state.Observation.NotObserved, state.Observation.RemoteUnavailable)
	switch state.Observation.Posture {
	case projects.ProjectRepositoryObservationRemoteUnavailable:
		summaryStatus = StatusUnknown
	case projects.ProjectRepositoryObservationNotObserved:
		if state.Observation.MemberCount > 0 {
			summaryStatus = StatusWarning
		}
	}
	checks = append(checks, Check{
		Key:        "repositories.summary",
		Title:      "Repository State",
		Status:     summaryStatus,
		Summary:    summaryText,
		SubjectRef: state.Project.ProjectID,
		Metadata: metadata(map[string]any{
			"source_posture":      state.Source.Posture,
			"source_revision":     state.Source.SourceRevision,
			"semantic_digest":     state.Source.SemanticDigest,
			"location_digest":     state.Source.LocationDigest,
			"observation_posture": state.Observation.Posture,
			"member_count":        state.Observation.MemberCount,
			"observed":            state.Observation.Observed,
			"not_observed":        state.Observation.NotObserved,
			"remote_unavailable":  state.Observation.RemoteUnavailable,
		}),
	})
	for _, member := range state.Members {
		status := StatusOK
		summary := "Registered repository member was observed locally."
		switch member.ObservationPosture {
		case projects.ProjectRepositoryObservationRemoteUnavailable:
			status = StatusUnknown
			summary = "Registered repository member is owned by an unavailable remote node."
		case projects.ProjectRepositoryObservationNotObserved:
			status = StatusWarning
			summary = "Registered repository member was not observed."
		}
		for _, problem := range member.Problems {
			if problem.Severity == projectstate.ProblemSeverityError {
				status = StatusBlocked
				summary = problem.Summary
				break
			}
		}
		if member.DevelopmentState.Posture == projectstate.DevelopmentStateInvalid || member.DevelopmentState.Posture == projectstate.DevelopmentStateMismatch {
			status = StatusBlocked
			summary = "Repository development-state backlink is invalid or mismatched."
		}
		if status == StatusOK && member.Git != nil && member.Git.Dirty.Dirty {
			status = StatusWarning
			summary = "Registered repository member was observed with local Git changes."
		}
		metadataValue := map[string]any{
			"repository_id":                 member.RepositoryID,
			"repository_owner_project_id":   member.RepositoryOwnerProjectID,
			"role":                          member.Role,
			"relative_source":               member.RelativeSource,
			"source_binding_digest":         member.SourceBindingDigest,
			"observation_posture":           member.ObservationPosture,
			"reason_code":                   member.ReasonCode,
			"development_state_posture":     member.DevelopmentState.Posture,
			"development_state_reason_code": member.DevelopmentState.ReasonCode,
		}
		if member.Git != nil {
			metadataValue["git_head"] = member.Git.Head
			metadataValue["git_current_branch"] = member.Git.CurrentBranch
			metadataValue["git_default_branch"] = member.Git.DefaultBranch
			metadataValue["git_worktree"] = member.Git.Worktree
			metadataValue["git_worktree_canonical_project_state"] = false
			metadataValue["git_dirty"] = member.Git.Dirty.Dirty
			metadataValue["git_tracked_changes"] = member.Git.Dirty.TrackedChanges
			metadataValue["git_untracked_changes"] = member.Git.Dirty.UntrackedChanges
			metadataValue["git_submodule_changes"] = member.Git.Dirty.SubmoduleChanges
			metadataValue["git_ahead"] = member.Git.Ahead
			metadataValue["git_behind"] = member.Git.Behind
		}
		checks = append(checks, Check{
			Key:        "repositories." + member.RepositoryID,
			Title:      "Repository " + firstNonEmpty(member.Key, member.RepositoryID),
			Status:     status,
			Summary:    summary,
			Facet:      "repos",
			SubjectRef: member.RepositoryID,
			Metadata:   metadata(metadataValue),
		})
	}
	return checks
}

func runtimeAccessChecks(analysis projectcontracts.Analysis) []Check {
	raw := projectaccess.Analyze(analysis)
	out := make([]Check, 0, len(raw))
	for _, check := range raw {
		out = append(out, Check{
			Key:         check.Key,
			Title:       check.Title,
			Status:      runtimeAccessStatus(check.Status),
			Summary:     check.Summary,
			Detail:      check.Detail,
			Facet:       check.Facet,
			SubjectRef:  check.SubjectRef,
			Remediation: check.Remediation,
			Metadata:    metadata(map[string]any{"path": check.Path, "required": check.Required}),
		})
	}
	return out
}

func runtimeAccessStatus(status projectaccess.Status) Status {
	switch status {
	case projectaccess.StatusOK:
		return StatusOK
	case projectaccess.StatusWarning:
		return StatusWarning
	case projectaccess.StatusBlocked:
		return StatusBlocked
	case projectaccess.StatusSkipped:
		return StatusSkipped
	default:
		return StatusUnknown
	}
}

func metadata(value map[string]any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func Summarize(checks []Check) Summary {
	var summary Summary
	for _, check := range checks {
		switch check.Status {
		case StatusOK:
			summary.OK++
		case StatusWarning:
			summary.Warnings++
		case StatusError:
			summary.Errors++
		case StatusBlocked:
			summary.Blocked++
		case StatusUnknown:
			summary.Unknown++
		case StatusSkipped:
			summary.Skipped++
		}
	}
	return summary
}

func HasFailures(report Report) bool {
	return report.Summary.Errors > 0 || report.Summary.Blocked > 0
}

func HasWarnings(report Report) bool {
	return report.Summary.Warnings > 0 || report.Summary.Unknown > 0
}

func facetCheck(facet projects.ProjectContractFacet) Check {
	status := StatusWarning
	summary := "Project facet is declared but not activated."
	switch facet.FacetStatus {
	case projects.ProjectFacetStatusActivated:
		status = StatusOK
		summary = "Project facet is activated."
	case projects.ProjectFacetStatusDisabled:
		status = StatusSkipped
		summary = "Project facet is disabled."
	case projects.ProjectFacetStatusUnsupported, projects.ProjectFacetStatusPlaceholder:
		status = StatusBlocked
		summary = "Project facet is placeholder or unsupported."
	case projects.ProjectFacetStatusMissing:
		status = StatusWarning
		summary = "Project facet is enabled but missing locally."
	}
	return Check{
		Key:        "facet." + facet.FacetKey,
		Title:      "Facet " + facet.FacetKey,
		Status:     status,
		Summary:    summary,
		Facet:      facet.FacetKey,
		SubjectRef: facet.FacetKey,
		Metadata:   json.RawMessage(`{}`),
	}
}

func scriptExposureChecks(exposures []projects.ProjectScriptExposure) []Check {
	out := make([]Check, 0, len(exposures))
	for _, exposure := range exposures {
		status := StatusOK
		summary := "Project script capability is active."
		if exposure.ActivationStatus == projects.ProjectScriptExposureStatusDisabled {
			status = StatusSkipped
			summary = "Project script capability is disabled."
		} else if exposure.ActivationStatus == projects.ProjectScriptExposureStatusStale {
			status = StatusWarning
			summary = "Project script capability is stale."
		} else if exposure.ActivationStatus == projects.ProjectScriptExposureStatusBlocked {
			status = StatusBlocked
			summary = "Project script capability is blocked."
		} else if exposure.ActivationStatus != projects.ProjectScriptExposureStatusActive {
			status = StatusWarning
			summary = "Project script capability is registered but not active."
		}
		if exposure.ActivationStatus == projects.ProjectScriptExposureStatusActive && (exposure.CapabilityEndpointID == nil || exposure.RuntimeBindingID == nil) {
			status = StatusBlocked
			summary = "Active project script capability is missing endpoint or runtime binding ids."
		}
		out = append(out, Check{
			Key:        "scripts." + exposure.ScriptKey,
			Title:      "Script " + exposure.ScriptKey,
			Status:     status,
			Summary:    summary,
			Facet:      "scripts",
			SubjectRef: firstNonEmpty(exposure.CapabilityAddress, exposure.ScriptKey),
			Metadata:   json.RawMessage(`{}`),
		})
	}
	return out
}

func scheduleChecks(registrations []projects.ProjectScheduleRegistration) []Check {
	out := make([]Check, 0, len(registrations))
	for _, registration := range registrations {
		status := StatusOK
		summary := "Project schedule is registered."
		switch registration.ActivationStatus {
		case projects.ProjectScheduleRegistrationStatusDisabled:
			status = StatusSkipped
			summary = "Project schedule is disabled."
		case projects.ProjectScheduleRegistrationStatusBlocked:
			status = StatusBlocked
			summary = "Project schedule is blocked."
		case projects.ProjectScheduleRegistrationStatusStale:
			status = StatusWarning
			summary = "Project schedule is stale."
		case projects.ProjectScheduleRegistrationStatusRegistered, projects.ProjectScheduleRegistrationStatusPaused:
			status = StatusWarning
			summary = "Project schedule exists but is not active."
		}
		if strings.TrimSpace(registration.TargetCapability) == "" {
			status = StatusBlocked
			summary = "Project schedule is missing a target capability."
		}
		out = append(out, Check{
			Key:        "schedules." + registration.ScheduleKey,
			Title:      "Schedule " + registration.ScheduleKey,
			Status:     status,
			Summary:    summary,
			Facet:      "schedules",
			SubjectRef: firstNonEmpty(registration.BackendScheduleKey, registration.ScheduleKey),
			Metadata:   json.RawMessage(`{}`),
		})
	}
	return out
}

func directEventChecks(registrations []projects.ProjectDirectEventRegistration) []Check {
	out := make([]Check, 0, len(registrations))
	for _, registration := range registrations {
		status := StatusOK
		summary := "Project direct event endpoint is registered."
		switch registration.ActivationStatus {
		case projects.ProjectDirectEventRegistrationStatusDisabled:
			status = StatusSkipped
			summary = "Project direct event endpoint is disabled."
		case projects.ProjectDirectEventRegistrationStatusBlocked:
			status = StatusBlocked
			summary = "Project direct event endpoint is blocked."
		case projects.ProjectDirectEventRegistrationStatusStale:
			status = StatusWarning
			summary = "Project direct event endpoint is stale."
		case projects.ProjectDirectEventRegistrationStatusRegistered, projects.ProjectDirectEventRegistrationStatusPaused:
			status = StatusWarning
			summary = "Project direct event endpoint exists but is not active."
		}
		if strings.TrimSpace(registration.TargetCapability) == "" {
			status = StatusBlocked
			summary = "Project direct event endpoint is missing a target capability."
		}
		out = append(out, Check{
			Key:        "direct_events." + registration.EventKey,
			Title:      "Direct Event " + registration.EventKey,
			Status:     status,
			Summary:    summary,
			Facet:      "direct_events",
			SubjectRef: firstNonEmpty(registration.BackendEndpointSlug, registration.EventKey),
			Metadata:   json.RawMessage(`{}`),
		})
	}
	return out
}

func connectorChecks(registrations []projects.ProjectConnectorRegistration) []Check {
	out := make([]Check, 0, len(registrations))
	for _, registration := range registrations {
		status := StatusOK
		summary := "Project connector is active."
		switch registration.ActivationStatus {
		case projects.ProjectConnectorRegistrationStatusDisabled:
			status = StatusSkipped
			summary = "Project connector is disabled."
		case projects.ProjectConnectorRegistrationStatusBlocked:
			status = StatusBlocked
			summary = "Project connector is blocked."
		case projects.ProjectConnectorRegistrationStatusStale:
			status = StatusWarning
			summary = "Project connector is stale."
		case projects.ProjectConnectorRegistrationStatusRegistered:
			status = StatusWarning
			summary = "Project connector is registered but not active."
		}
		if registration.ActivationStatus == projects.ProjectConnectorRegistrationStatusActive && registration.ActiveCapabilityCount < registration.CapabilityCount {
			status = StatusWarning
			summary = "Project connector has inactive capability endpoints."
		}
		out = append(out, Check{
			Key:        "connectors." + registration.ConnectorKey,
			Title:      "Connector " + registration.ConnectorKey,
			Status:     status,
			Summary:    summary,
			Facet:      "connectors",
			SubjectRef: firstNonEmpty(registration.ProviderAddress, registration.ConnectorKey),
			Metadata:   json.RawMessage(`{}`),
		})
	}
	return out
}

func moduleChecks(registrations []projects.ProjectModuleRegistration) []Check {
	out := make([]Check, 0, len(registrations))
	for _, registration := range registrations {
		status := StatusOK
		summary := "Project module registration is present."
		switch registration.ActivationStatus {
		case projects.ProjectModuleRegistrationStatusDisabled:
			status = StatusSkipped
			summary = "Project module registration is disabled."
		case projects.ProjectModuleRegistrationStatusBlocked:
			status = StatusBlocked
			summary = "Project module registration is blocked."
		case projects.ProjectModuleRegistrationStatusStale:
			status = StatusWarning
			summary = "Project module registration is stale."
		}
		out = append(out, Check{
			Key:        "modules." + registration.ModuleKey,
			Title:      "Module " + registration.ModuleKey,
			Status:     status,
			Summary:    summary,
			Facet:      "modules",
			SubjectRef: firstNonEmpty(registration.ModuleID, registration.ModuleKey),
			Metadata:   json.RawMessage(`{}`),
		})
	}
	return out
}

func watchedRootChecks(registrations []projects.ProjectWatchedRootRegistration) []Check {
	out := make([]Check, 0, len(registrations))
	for _, registration := range registrations {
		status := StatusOK
		summary := "Project watched root is applied or reported."
		switch registration.ActivationStatus {
		case projects.ProjectWatchedRootRegistrationStatusDisabled:
			status = StatusSkipped
			summary = "Project watched root is disabled in LOOM."
		case projects.ProjectWatchedRootRegistrationStatusBlocked:
			status = StatusBlocked
			summary = "Project watched root is blocked."
		case projects.ProjectWatchedRootRegistrationStatusStale:
			status = StatusWarning
			summary = "Project watched root is stale."
		case projects.ProjectWatchedRootRegistrationStatusRegistered, projects.ProjectWatchedRootRegistrationStatusPendingAgentApply:
			status = StatusWarning
			summary = "Project watched root is waiting for node-agent apply/report."
		}
		out = append(out, Check{
			Key:        "watched_roots." + registration.LocalRootKey,
			Title:      "Watched Root " + registration.LocalRootKey,
			Status:     status,
			Summary:    summary,
			Facet:      "watched_roots",
			SubjectRef: firstNonEmpty(registration.BackendRootKey, registration.LocalRootKey),
			Metadata:   json.RawMessage(`{}`),
		})
	}
	return out
}

func workflowChecks(detail *projects.ProjectRegistrationDetail) []Check {
	if detail == nil || detail.Registration == nil || len(detail.Registration.RegistrationPlan) == 0 {
		return nil
	}
	var plan projectcontracts.ProjectPlan
	if err := json.Unmarshal(detail.Registration.RegistrationPlan, &plan); err != nil {
		return []Check{{
			Key:         "workflows.plan_decode_failed",
			Title:       "Workflows",
			Status:      StatusWarning,
			Summary:     "Registered plan could not be decoded for workflow checks.",
			Facet:       "workflows",
			Remediation: "Re-register the project contract.",
			Metadata:    json.RawMessage(`{}`),
		}}
	}
	registered := map[string]projects.ProjectWorkflowRegistration{}
	for _, registration := range detail.WorkflowRegistrations {
		key := strings.TrimSpace(registration.WorkflowKey)
		if key == "" {
			key = ptrStringValue(registration.WorkflowID)
		}
		if key != "" {
			registered[key] = registration
		}
	}
	out := []Check{}
	seen := map[string]bool{}
	for _, workflow := range plan.Workflows {
		if strings.TrimSpace(workflow.ManifestPath) == "" {
			continue
		}
		key := firstNonEmpty(workflow.Key, workflow.WorkflowID)
		seen[key] = true
		status := StatusWarning
		summary := "Workflow contract is design-only and has no callable endpoint."
		subjectRef := firstNonEmpty(workflow.CapabilityAddress, workflow.ScriptCapabilityAddress, workflow.WorkflowID, workflow.Key)
		if workflow.ContractStatus == "disabled" {
			status = StatusSkipped
			summary = "Workflow contract is disabled."
		} else if workflow.ImplementationKind == projectcontracts.WorkflowImplementationPlaceholder {
			status = StatusWarning
			summary = "Workflow contract is design-only and will not register a callable endpoint."
		} else if registration, ok := registered[key]; ok {
			status, summary = workflowRegistrationCheckStatus(workflow, registration)
			subjectRef = firstNonEmpty(registration.CapabilityAddress, subjectRef)
		} else if workflow.ExposeEnabled && strings.TrimSpace(workflow.CapabilityAddress) != "" {
			status = StatusBlocked
			summary = "Workflow is exposed in the contract but has no project workflow registration row."
		} else {
			status = StatusWarning
			summary = "Workflow is not exposed as a callable capability."
		}
		out = append(out, Check{
			Key:        "workflows." + firstNonEmpty(workflow.WorkflowID, workflow.Key),
			Title:      "Workflow " + firstNonEmpty(workflow.Name, workflow.WorkflowID, workflow.Key),
			Status:     status,
			Summary:    summary,
			Facet:      "workflows",
			SubjectRef: subjectRef,
			Metadata:   json.RawMessage(`{}`),
		})
	}
	for key, registration := range registered {
		if seen[key] {
			continue
		}
		status, summary := workflowRegistrationOnlyCheckStatus(registration)
		out = append(out, Check{
			Key:        "workflows." + key,
			Title:      "Workflow " + key,
			Status:     status,
			Summary:    summary,
			Facet:      "workflows",
			SubjectRef: firstNonEmpty(registration.CapabilityAddress, ptrStringValue(registration.WorkflowID), key),
			Metadata:   json.RawMessage(`{}`),
		})
	}
	return out
}

func workflowRegistrationCheckStatus(workflow projectcontracts.WorkflowFacetItem, registration projects.ProjectWorkflowRegistration) (Status, string) {
	switch registration.ActivationStatus {
	case projects.ProjectWorkflowRegistrationStatusDisabled:
		return StatusSkipped, "Workflow registration is disabled."
	case projects.ProjectWorkflowRegistrationStatusStale:
		return StatusWarning, "Workflow registration is stale and should be reactivated."
	case projects.ProjectWorkflowRegistrationStatusBlocked:
		return StatusBlocked, "Workflow registration is blocked."
	case projects.ProjectWorkflowRegistrationStatusRegistered:
		return StatusWarning, "Workflow registration exists but is not active yet."
	}
	if registration.ActivationStatus != projects.ProjectWorkflowRegistrationStatusActive {
		return StatusUnknown, "Workflow registration has an unknown activation status."
	}
	expectedRuntime := capabilities.RuntimeKindWorkflow
	if workflow.ImplementationKind == projectcontracts.WorkflowImplementationScript {
		expectedRuntime = capabilities.RuntimeKindScript
	}
	if registration.RuntimeKind != expectedRuntime {
		return StatusBlocked, fmt.Sprintf("Workflow runtime binding kind is %s, expected %s.", firstNonEmpty(registration.RuntimeKind, "-"), expectedRuntime)
	}
	if strings.TrimSpace(registration.CapabilityAddress) == "" || ptrStringValue(registration.CapabilityEndpointID) == "" || ptrStringValue(registration.CapabilityEndpointVersionID) == "" || ptrStringValue(registration.RuntimeBindingID) == "" {
		return StatusBlocked, "Workflow registration is active but missing capability endpoint, endpoint version, or runtime binding rows."
	}
	if workflow.ImplementationKind == projectcontracts.WorkflowImplementationWorkflow && (ptrStringValue(registration.WorkflowID) == "" || ptrStringValue(registration.WorkflowVersionID) == "") {
		return StatusBlocked, "Executable workflow registration is active but missing workflow package/version rows."
	}
	if workflow.ManifestHash != "" && registration.WorkflowManifestHash != "" && workflow.ManifestHash != registration.WorkflowManifestHash {
		return StatusWarning, "Workflow contract hash differs from the active workflow registration."
	}
	if workflow.ImplementationKind == projectcontracts.WorkflowImplementationScript {
		return StatusOK, "Script-backed workflow shim has an active workflow capability endpoint."
	}
	return StatusOK, "Executable workflow has active package, capability endpoint, endpoint version, and runtime binding rows."
}

func workflowRegistrationOnlyCheckStatus(registration projects.ProjectWorkflowRegistration) (Status, string) {
	switch registration.ActivationStatus {
	case projects.ProjectWorkflowRegistrationStatusActive:
		return StatusWarning, "Workflow registration is active but no longer appears in the registered plan."
	case projects.ProjectWorkflowRegistrationStatusStale:
		return StatusWarning, "Workflow registration is stale and no longer appears in the registered plan."
	case projects.ProjectWorkflowRegistrationStatusDisabled:
		return StatusSkipped, "Workflow registration is disabled and no longer appears in the registered plan."
	case projects.ProjectWorkflowRegistrationStatusBlocked:
		return StatusBlocked, "Workflow registration is blocked and no longer appears in the registered plan."
	default:
		return StatusUnknown, "Workflow registration exists outside the registered plan."
	}
}

func ptrStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func statusFromBlockedCounts(blocked, stale, pending int) Status {
	if blocked > 0 {
		return StatusBlocked
	}
	if stale > 0 || pending > 0 {
		return StatusWarning
	}
	return StatusOK
}

func contractHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func diagnosticsSummary(diagnostics []projectcontracts.Diagnostic) string {
	parts := []string{}
	for _, diag := range diagnostics {
		if diag.Severity == projectcontracts.SeverityInfo {
			continue
		}
		parts = append(parts, diag.Code+": "+diag.Message)
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, "; ")
}

func localProjectRef(analysis *projectcontracts.Analysis) string {
	if analysis == nil {
		return ""
	}
	return firstNonEmpty(analysis.Plan.Project.Slug, analysis.Report.Project.Slug)
}

func detailProjectRef(detail *projects.ProjectRegistrationDetail) string {
	if detail == nil {
		return ""
	}
	return firstNonEmpty(detail.Project.Project.Slug, detail.Project.Project.ProjectID)
}

func statusRank(status Status) int {
	switch status {
	case StatusError:
		return 0
	case StatusBlocked:
		return 1
	case StatusWarning:
		return 2
	case StatusUnknown:
		return 3
	case StatusSkipped:
		return 4
	case StatusOK:
		return 5
	default:
		return 6
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
