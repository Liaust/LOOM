package portal

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/lane"
)

func renderBox(builder *strings.Builder, state ScreenState) {
	data := state.Data.Box
	status := data.Status
	records := ScreenRecordItems(state)
	row := renderTopAvailableActions(builder, state, records)
	renderOperationalActionGroups(builder, state, records, &row)

	renderPrimarySection(builder, "Box Status")
	metrics := []string{
		"status=" + renderStatus(firstNonEmpty(status.State, "unknown")),
		"profile=" + firstNonEmpty(status.Profile, "-"),
		"contract=" + renderStatus(firstNonEmpty(status.ContractState, "-")),
		"watch=" + boxWatchPolicySummary(data),
	}
	if portalHasWorkspaceLane(status) {
		metrics = append(metrics, "workspace_lane="+renderStatus(firstNonEmpty(status.LaneState, "-")))
	} else if status.Profile == box.ProfileMain {
		metrics = append(metrics, "intake=Storage Imports")
	}
	renderMetricLine(builder, metrics...)
	if state.RawDetails {
		renderKeyValue(builder, "path", firstNonEmpty(status.RootPath, "-"))
		renderKeyValue(builder, "owner", firstNonEmpty(status.OwnerNode, "-"))
		renderKeyValue(builder, "default project path", firstNonEmpty(status.DefaultProjectPath, "-"))
	}
	fmt.Fprintf(builder, "  %s %s  %s\n",
		renderSelectedMarker(row, state.SelectedIndex),
		"LOOM Box",
		portalRenderContext().Styles.Muted.Render(firstNonEmpty(status.RootPath, "resolved local Box")),
	)
	row++

	if portalHasWorkspaceLane(status) {
		renderBoxLane(builder, state, &row)
	}
	renderBoxFoldersAndPolicies(builder, state, &row)

	renderBoxWatch(builder, state, &row)
	renderBoxDiagnostics(builder, status, state.RawDetails)
}

func renderBoxFoldersAndPolicies(builder *strings.Builder, state ScreenState, row *int) {
	status := state.Data.Box.Status
	areas := boxVisibleAreas(status, state.RawDetails)
	policies := boxVisiblePolicies(status, state.RawDetails)
	renderSummarySection(builder, "Folders And Policies")
	if len(areas) == 0 && len(policies) == 0 {
		renderEmpty(builder, "No user-facing folders or watch policies are active.")
		return
	}
	for _, area := range areas {
		fmt.Fprintf(builder, "  %s folder %-10s %s  %s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			boxAreaLabel(area.Key),
			renderStatus(area.Status),
			portalRenderContext().Styles.Muted.Render(area.RelativePath),
		)
		if state.RawDetails {
			fmt.Fprintf(builder, "      path=%s enabled=%t\n", area.Path, area.Enabled)
		}
		*row = *row + 1
	}
	for _, policy := range policies {
		fmt.Fprintf(builder, "  %s policy %-10s %s  %s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			boxAreaLabel(policy.Key),
			renderStatus(policy.Status),
			portalRenderContext().Styles.Muted.Render(boxPolicyDisplayPath(policy, state.RawDetails)),
		)
		if state.RawDetails {
			fmt.Fprintf(builder, "      path=%s\n", policy.Path)
		}
		*row = *row + 1
	}
}

func boxPolicyDisplayPath(policy box.PathStatus, rawDetails bool) string {
	if rawDetails {
		return policy.RelativePath
	}
	switch strings.ToLower(strings.TrimSpace(policy.Key)) {
	case box.AreaNotes:
		return "sync and index markdown notes"
	case box.AreaDocuments, box.AreaLaunchpad:
		return "backup general documents"
	default:
		if strings.HasPrefix(policy.RelativePath, ".loom/") {
			return "managed watch policy"
		}
		return policy.RelativePath
	}
}

func renderBoxLane(builder *strings.Builder, state ScreenState, row *int) {
	boxStatus := state.Data.Box.Status
	if !portalHasWorkspaceLane(boxStatus) {
		return
	}
	status := boxStatus.Lane
	renderPrimarySection(builder, "LOOM Lane")
	renderMetricLine(builder,
		"state="+firstNonEmpty(status.State, "-"),
		"mode="+firstNonEmpty(string(status.Transport.RecommendedMode), "file_tree"),
		"profile="+firstNonEmpty(string(status.Profile), "faithful"),
		fmt.Sprintf("pending=%d", status.PendingItems),
		fmt.Sprintf("protected=%d", status.PendingFiles),
		fmt.Sprintf("ignored=%d", status.IgnoredFileCount),
		fmt.Sprintf("active=%d", status.ActivePendingItems),
		fmt.Sprintf("ack=%d", status.AcknowledgedPendingItems),
		"size="+storageFormatBytes(status.PendingBytes),
		"recovery="+storageFormatBytes(status.RecoveryStorage.RetainedBytes),
		"recovery_protected="+storageFormatBytes(status.RecoveryStorage.ProtectedEvidenceBytes),
		"preflight="+firstNonEmpty(status.Preflight.Status, "-"),
	)
	if state.RawDetails {
		fmt.Fprintf(builder, "  transport=%s temporary=%s reason=%s\n",
			firstNonEmpty(string(status.Transport.RecommendedMode), "file_tree"),
			storageFormatBytes(status.Transport.EstimatedTemporaryBytes),
			firstNonEmpty(status.Transport.Reason, "-"),
		)
		fmt.Fprintf(builder, "  recovery retained=%s grace=%s protected=%s cleanup=%s safety=%s untracked=%s next_expiry=%s\n",
			storageFormatBytes(status.RecoveryStorage.RetainedBytes),
			storageFormatBytes(status.RecoveryStorage.SuccessfulGraceBytes),
			storageFormatBytes(status.RecoveryStorage.ProtectedEvidenceBytes),
			storageFormatBytes(status.RecoveryStorage.CleanupQuarantineBytes),
			storageFormatBytes(status.RecoveryStorage.TransportSafetyBytes),
			storageFormatBytes(status.RecoveryStorage.UntrackedBytes),
			portalTimePtr(status.RecoveryStorage.NextSuccessfulQuarantineExpiresAt),
		)
	}
	if status.PendingItems == 0 {
		renderEmpty(builder, "No pending LOOM Lane items.")
	} else {
		for _, item := range status.Items {
			fmt.Fprintf(builder, "  %s %-10s %-28s %8s %s\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				renderStatus(item.Kind),
				trimForWidth(item.RelativePath, 28),
				renderStatus(firstNonEmpty(item.AttentionStatus, "-")),
				portalRenderContext().Styles.Muted.Render(storageFormatBytes(item.Bytes)),
			)
			if state.RawDetails {
				fmt.Fprintf(builder, "      files=%d dirs=%d age=%s modified=%s\n", item.FileCount, item.DirCount, formatPortalLaneAge(item.AgeSeconds), item.ModifiedAt.Format("2006-01-02T15:04:05Z07:00"))
				if item.AttentionReason != "" {
					fmt.Fprintf(builder, "      next=%s\n", item.AttentionReason)
				}
			}
			*row = *row + 1
		}
	}
	if status.LastTransfer != nil {
		fmt.Fprintf(builder, "  %s %-10s %-28s %8s %s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			renderStatus("transfer"),
			trimForWidth(firstNonEmpty(status.LastTransfer.BatchID, "Lane transfer"), 28),
			renderStatus(firstNonEmpty(status.LastTransfer.Status, "-")),
			portalRenderContext().Styles.Muted.Render(storageFormatBytes(status.LastTransfer.TotalBytes)),
		)
		if state.RawDetails {
			fmt.Fprintf(builder, "      attention=%s visible=%s\n", firstNonEmpty(status.LastTransfer.AttentionStatus, "-"), firstNonEmpty(status.LastTransfer.VisibleStoragePath, "-"))
			if status.LastTransfer.ErrorMessage != "" {
				fmt.Fprintf(builder, "      error=%s\n", trimForWidth(status.LastTransfer.ErrorMessage, 92))
			}
		}
		*row = *row + 1
	}
	if state.RawDetails {
		fmt.Fprintf(builder, "      lane=%s state=%s main=%s rsync=%s ssh=%s\n",
			firstNonEmpty(status.LanePath, "-"),
			firstNonEmpty(status.StatePath, "-"),
			firstNonEmpty(status.Preflight.MainHost, "-"),
			firstNonEmpty(status.Preflight.RsyncPath, "-"),
			firstNonEmpty(status.Preflight.SSHPath, "-"),
		)
		renderBoxLaneDiagnostics(builder, status.Diagnostics)
		renderBoxLaneDiagnostics(builder, status.Preflight.Diagnostics)
	}
}

func portalTimePtr(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func formatPortalLaneAge(seconds int64) string {
	if seconds <= 0 {
		return "-"
	}
	return (time.Duration(seconds) * time.Second).Round(time.Second).String()
}

func renderBoxLaneDiagnostics(builder *strings.Builder, diagnostics []lane.Diagnostic) {
	for _, diagnostic := range diagnostics {
		fmt.Fprintf(builder, "      %s %s: %s\n", renderStatus(diagnostic.Severity), diagnostic.Code, diagnostic.Message)
	}
}

func renderBoxWatch(builder *strings.Builder, state ScreenState, row *int) {
	data := state.Data.Box
	renderDetailsSection(builder, "Watch Policy")
	if data.Status.State != "ok" {
		renderEmpty(builder, "Initialize or repair the Box before compiling Notes and Documents watch policy.")
		return
	}
	plan := data.WatchPlan
	if len(plan.WatchedRoots) == 0 && data.WatchStatusAvailable {
		plan = data.WatchStatus.Plan
	}
	if len(plan.WatchedRoots) == 0 {
		renderEmpty(builder, "No watched roots compiled for this Box.")
	} else {
		for _, root := range plan.WatchedRoots {
			fmt.Fprintf(builder, "  %s %-10s %s  sync=%s index=%s backup=%s\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				root.Key,
				portalRenderContext().Styles.Muted.Render(root.BackendRootKey),
				root.SyncMode,
				root.IndexMode,
				root.BackupMode,
			)
			*row = *row + 1
		}
	}
	if len(plan.Excluded) > 0 {
		if state.RawDetails {
			fmt.Fprintf(builder, "  %s\n", portalRenderContext().Styles.Muted.Render("Excluded: projects own project policies; transfer intake is not a watched root."))
		}
	}

	renderDetailsSection(builder, "Watch Status")
	if !data.WatchStatusAvailable {
		renderEmpty(builder, "No backend watch status is available yet.")
		return
	}
	renderMetricLine(builder,
		fmt.Sprintf("registered=%d", len(data.WatchStatus.Registrations)),
		fmt.Sprintf("reported=%d", len(data.WatchStatus.Statuses)),
	)
	for _, registration := range data.WatchStatus.Registrations {
		if !state.RawDetails && boxAreaIsInternal(registration.AreaKey) {
			continue
		}
		fmt.Fprintf(builder, "  %s %-10s %s  node=%s root=%s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			boxAreaLabel(registration.AreaKey),
			renderStatus(registration.ActivationStatus),
			firstNonEmpty(registration.OwnerNodeKey, registration.NodeID, "-"),
			firstNonEmpty(registration.BackendRootKey, "-"),
		)
		*row = *row + 1
	}
	for _, status := range data.WatchStatus.Statuses {
		if !state.RawDetails && boxAreaIsInternal(status.Root.RootKey) {
			continue
		}
		protected, ignored, countsAvailable := watchedRootProtectionCounts(status.Root.SummaryJSON)
		protection := "protected=- ignored=-"
		if countsAvailable {
			protection = fmt.Sprintf("protected=%d ignored=%d", protected, ignored)
		}
		fmt.Fprintf(builder, "  %s report %-10s %s  worker=%s %s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			boxAreaLabel(status.Root.RootKey),
			renderStatus(status.Root.Status),
			firstNonEmpty(status.Root.WorkerKey, "-"),
			protection,
		)
		*row = *row + 1
	}
}

func watchedRootProtectionCounts(summaryJSON json.RawMessage) (protected int, ignored int, ok bool) {
	if len(summaryJSON) == 0 {
		return 0, 0, false
	}
	var summary struct {
		Included *int `json:"included"`
		Excluded *int `json:"excluded"`
	}
	if err := json.Unmarshal(summaryJSON, &summary); err != nil {
		return 0, 0, false
	}
	if summary.Included == nil && summary.Excluded == nil {
		return 0, 0, false
	}
	if summary.Included != nil {
		protected = *summary.Included
	}
	if summary.Excluded != nil {
		ignored = *summary.Excluded
	}
	return protected, ignored, true
}

func renderBoxDiagnostics(builder *strings.Builder, status box.Status, rawDetails bool) {
	if len(status.Diagnostics) == 0 {
		if rawDetails {
			renderDiagnosticsSection(builder, "Diagnostics")
			renderEmpty(builder, "No Box diagnostics.")
		}
		return
	}
	renderDiagnosticsSection(builder, "Diagnostics")
	for _, diagnostic := range status.Diagnostics {
		fmt.Fprintf(builder, "  %s %s: %s\n", renderStatus(diagnostic.Severity), diagnostic.Code, diagnostic.Message)
		if diagnostic.Suggestion != "" {
			fmt.Fprintf(builder, "      %s\n", diagnostic.Suggestion)
		}
	}
}

func boxVisibleAreas(status box.Status, rawDetails bool) []box.PathStatus {
	areas := make([]box.PathStatus, 0, len(status.Areas))
	for _, area := range status.Areas {
		key := strings.ToLower(strings.TrimSpace(area.Key))
		if key == box.AreaDropzone || (status.Profile == box.ProfileMain && key == box.AreaLane) {
			continue
		}
		if !rawDetails && (boxAreaIsInternal(area.Key) || !boxAreaIsUserFacing(area.Key)) {
			continue
		}
		areas = append(areas, area)
	}
	return areas
}

func boxVisiblePolicies(status box.Status, rawDetails bool) []box.PathStatus {
	policies := make([]box.PathStatus, 0, len(status.Policies))
	for _, policy := range status.Policies {
		key := strings.ToLower(strings.TrimSpace(policy.Key))
		if key == box.AreaDropzone || (status.Profile == box.ProfileMain && key == box.AreaLane) {
			continue
		}
		if !rawDetails && (boxAreaIsInternal(policy.Key) || !boxPolicyIsUserFacing(policy.Key)) {
			continue
		}
		policies = append(policies, policy)
	}
	return policies
}

func boxAreaIsInternal(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	switch key {
	case "", ".loom", "metadata", "policies", "state", "contract", "lane_state", "lane_batches", "lane_sent", "lane_logs", "dropzone_state", box.AreaDropzone:
		return true
	default:
		return strings.HasPrefix(key, ".loom") || strings.Contains(key, "acceptance")
	}
}

func boxAreaIsUserFacing(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case box.AreaProjects, box.AreaNotes, box.AreaDocuments, box.AreaLaunchpad, box.AreaLane:
		return true
	default:
		return false
	}
}

func boxPolicyIsUserFacing(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case box.AreaNotes, box.AreaDocuments, box.AreaLaunchpad:
		return true
	default:
		return false
	}
}

func boxAreaLabel(key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case box.AreaProjects:
		return "Projects"
	case box.AreaNotes:
		return "Notes"
	case box.AreaDocuments, box.AreaLaunchpad:
		return "Documents"
	case box.AreaLane:
		return "Lane"
	default:
		return titleFromToken(key)
	}
}

func boxWatchPolicySummary(data BoxData) string {
	desired := len(data.WatchPlan.WatchedRoots)
	if desired == 0 && data.WatchStatusAvailable {
		desired = len(data.WatchStatus.Plan.WatchedRoots)
	}
	registered := 0
	reported := 0
	if data.WatchStatusAvailable {
		registered = len(data.WatchStatus.Registrations)
		reported = len(data.WatchStatus.Statuses)
	}
	if desired == 0 && registered == 0 && reported == 0 {
		return "none"
	}
	return fmt.Sprintf("%d desired/%d registered/%d reported", desired, registered, reported)
}
