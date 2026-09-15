package nodeagent

import (
	"context"
	"encoding/json"
	"strings"

	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func reportWatchedRootRun(ctx context.Context, store Store, config Config, state State, instance noderuntime.WorkerInstance, root watchedroots.ValidatedRoot, result watchedroots.ScanResult, correlationID string) watchedroots.MainReport {
	report := watchedroots.MainReport{
		Attempted: true,
		Status:    watchedroots.OutputStatusPending,
	}
	if strings.TrimSpace(state.NodeID) == "" || strings.TrimSpace(state.CredentialToken) == "" {
		report.Attempted = false
		report.Status = "skipped_missing_credentials"
		return report
	}
	client, err := NewClient(config.MainURL)
	if err != nil {
		report.Status = watchedroots.OutputStatusFailed
		report.Error = err.Error()
		return report
	}
	watchedStore := watchedroots.NewStore(store.DataDir)
	findings, err := watchedStore.ListFindings(root.Config.RootKey, 100)
	if err != nil {
		report.Status = watchedroots.OutputStatusFailed
		report.Error = err.Error()
		return report
	}
	reportFindings := reportableWatchedRootFindings(findings)
	localSyncStatus, _ := store.LocalSyncStatus(config, state)
	localBackupStatus, _ := store.LocalWatchedRootBackupStatus(config, state, root.Config.RootKey)
	configJSON, err := json.Marshal(root.Config)
	if err != nil {
		report.Status = watchedroots.OutputStatusFailed
		report.Error = err.Error()
		return report
	}
	summaryJSON, err := json.Marshal(map[string]any{
		"schema_version":      "watched_root.main_report_summary.v0.2",
		"summary":             result.Summary,
		"scan_counts":         result.Counts,
		"changed_paths":       result.ChangedPaths,
		"output_plan":         result.OutputPlan,
		"output_flush":        result.OutputFlush,
		"backup_output_flush": result.BackupFlush,
		"local_sync_status":   localSyncStatus,
		"local_backup_status": localBackupStatus,
		"runtime_worker_key":  instance.WorkerKey,
	})
	if err != nil {
		report.Status = watchedroots.OutputStatusFailed
		report.Error = err.Error()
		return report
	}
	input := mainwatchedroots.ReportInput{
		NodeRef:                state.NodeID,
		CredentialToken:        state.CredentialToken,
		RootKey:                root.Config.RootKey,
		WorkerKey:              instance.WorkerKey,
		DisplayName:            root.Config.DisplayName,
		SafeRootKey:            root.Config.SafeRootKey,
		Status:                 result.Status,
		ConfigHash:             root.ConfigHash,
		ConfigJSON:             configJSON,
		SummaryJSON:            summaryJSON,
		Findings:               watchedRootFindingReportInputs(reportFindings),
		ResolveMissingFindings: watchedRootReportCanResolveMissing(root, result),
		Metadata: objectJSON(map[string]any{
			"source":             "loom-node-agent",
			"runtime_worker":     instance.WorkerKey,
			"root_reachable":     root.RootReachable,
			"safe_root_key":      root.Config.SafeRootKey,
			"root_relative_path": root.Config.RootRelativePath,
		}),
	}
	envelope, err := client.ReportWatchedRoot(ctx, correlationID, "node-agent.watched-root.report."+state.NodeID+"."+root.Config.RootKey, input)
	if err != nil {
		report.Status = watchedroots.OutputStatusFailed
		report.Error = err.Error()
		return report
	}
	report.Status = watchedroots.OutputStatusRecorded
	report.WatchedRootID = envelope.Data.Root.WatchedRootID
	report.FindingsReported = len(envelope.Data.Findings)
	report.FindingsResolved = envelope.Data.ResolvedCount
	return report
}

func reportableWatchedRootFindings(findings []watchedroots.Finding) []watchedroots.Finding {
	out := make([]watchedroots.Finding, 0, len(findings))
	for _, finding := range findings {
		if watchedroots.FindingIsReportable(finding) {
			out = append(out, finding)
		}
	}
	return out
}

func watchedRootReportCanResolveMissing(root watchedroots.ValidatedRoot, result watchedroots.ScanResult) bool {
	return root.RootReachable &&
		result.Mode == watchedroots.ScanModeFull &&
		result.Status != watchedroots.RunStatusBlocked &&
		result.Counts.BudgetExhausted == 0
}

func watchedRootFindingReportInputs(findings []watchedroots.Finding) []mainwatchedroots.FindingInput {
	out := make([]mainwatchedroots.FindingInput, 0, len(findings))
	for _, finding := range findings {
		firstSeen := finding.FirstSeenAt
		lastSeen := finding.LastSeenAt
		out = append(out, mainwatchedroots.FindingInput{
			FindingKey:   finding.FindingID,
			Severity:     watchedRootFindingSeverity(finding.Severity),
			Status:       watchedRootFindingStatus(finding.Status),
			Kind:         finding.Kind,
			RelativePath: finding.RelativePath,
			Summary:      finding.Summary,
			DetailsJSON:  finding.Details,
			FirstSeenAt:  &firstSeen,
			LastSeenAt:   &lastSeen,
		})
	}
	return out
}

func watchedRootFindingSeverity(severity string) string {
	switch strings.TrimSpace(severity) {
	case mainwatchedroots.FindingSeverityInfo,
		mainwatchedroots.FindingSeverityWarning,
		mainwatchedroots.FindingSeverityCritical:
		return strings.TrimSpace(severity)
	default:
		return mainwatchedroots.FindingSeverityWarning
	}
}

func watchedRootFindingStatus(status string) string {
	switch strings.TrimSpace(status) {
	case mainwatchedroots.FindingStatusResolved,
		mainwatchedroots.FindingStatusIgnored:
		return strings.TrimSpace(status)
	default:
		return mainwatchedroots.FindingStatusOpen
	}
}
