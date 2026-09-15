package portal

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagefidelity"
	"loom.local/loom/internal/storageview"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func renderStorage(builder *strings.Builder, state ScreenState) {
	data := state.Data.Storage
	renderStorageAttention(builder, data, state.RawDetails)
	renderStorageOverview(builder, data)
	records := ScreenRecordItems(state)
	actionRows := renderTopAvailableActions(builder, state, records)
	renderOperationalActionGroups(builder, state, records, &actionRows)
	visibleItems := contentSelectableItems(records)

	renderSection(builder, "User Storage")
	if len(visibleItems) == 0 {
		renderEmpty(builder, "No user-facing storage paths returned by the backend.")
		renderEmpty(builder, "Use #storage to search storage paths or enable details for diagnostics.")
	} else {
		selectedRecord := selectedContentIndex(visibleItems, state.SelectedIndex)
		start, end := visibleCapabilityExplorerWindow(len(visibleItems), selectedRecord, storageVisibleRowLimit(portalRenderContext().Height))
		if start > 0 || end < len(visibleItems) {
			renderEmpty(builder, fmt.Sprintf("Showing %d-%d of %d top-level path(s). Use #storage to search deeper.", start+1, end, len(visibleItems)))
		}
		if start > 0 {
			renderEmpty(builder, fmt.Sprintf("%d rows above", start))
		}
		for idx := start; idx < end; idx++ {
			renderStorageSelectableItem(builder, visibleItems[idx], visibleItems[idx].RowIndex, state.SelectedIndex, state.RawDetails)
		}
		if end < len(visibleItems) {
			renderEmpty(builder, fmt.Sprintf("%d rows below", len(visibleItems)-end))
		}
	}

	renderStorageProtection(builder, data)
	renderStorageStatusDetails(builder, data, state.RawDetails)
}

func renderStorageOverview(builder *strings.Builder, data StorageData) {
	renderSummarySection(builder, "Storage Safety")
	if data.Tree.ViewKey != "" {
		renderKeyValue(builder, "view", data.Tree.ViewKey)
	}
	counts := data.Tree.Counts
	renderMetricLine(builder,
		fmt.Sprintf("entries: %d", counts.Entries),
		fmt.Sprintf("files: %d", counts.Files),
		fmt.Sprintf("directories: %d", counts.Directories),
		fmt.Sprintf("writable: %d", counts.Writable),
		fmt.Sprintf("read-only: %d", counts.ReadOnly),
	)
	if data.FilesystemStatusAvailable {
		filesystem := data.FilesystemStatus
		renderMetricLine(builder,
			"physical roots: "+renderStatus(filesystem.Status),
			fmt.Sprintf("catalog returned: %d", filesystem.Catalog.Returned),
			fmt.Sprintf("pending: %d", filesystem.Catalog.Pending),
			fmt.Sprintf("failed: %d", filesystem.Catalog.Failed),
		)
		for _, root := range filesystem.Roots {
			renderMetricLine(builder,
				root.Key+": "+renderStatus(root.Status),
				root.Role,
			)
		}
	} else {
		renderEmpty(builder, "Canonical filesystem status unavailable.")
	}
	if data.RetentionStatusAvailable {
		retention := data.RetentionStatus
		renderMetricLine(builder,
			fmt.Sprintf("safe candidates: %d", retention.SafeCandidates),
			fmt.Sprintf("unsafe candidates: %d", retention.UnsafeCandidates),
			fmt.Sprintf("tombstoned: %d", retention.Tombstoned),
		)
	}
	if len(data.Entries) > 0 {
		counts := storageCatalogSafetyCounts(data.Entries)
		renderMetricLine(builder,
			fmt.Sprintf("backup pending: %d", counts.PendingBackup),
			"pending bytes: "+storageFormatBytes(counts.PendingBackupBytes),
			fmt.Sprintf("backup failed: %d", counts.FailedBackup),
			fmt.Sprintf("skipped too large: %d", counts.SkippedTooLarge),
			fmt.Sprintf("retained/snapshot: %d", counts.RetainedOrSnapshot),
		)
	}
	if data.DBStatusAvailable {
		worst := storageWorstDatabasePressure(data.DBStatus.Tables)
		if worst != "ok" || len(data.DBStatus.Warnings) > 0 {
			renderMetricLine(builder,
				"database: "+renderStatus(firstNonEmpty(data.DBStatus.Status, "-")),
				"pressure: "+renderStatus(worst),
				fmt.Sprintf("warnings: %d", len(data.DBStatus.Warnings)),
			)
		}
	}
}

func renderStorageProtection(builder *strings.Builder, data StorageData) {
	renderSummarySection(builder, "Backup And Cloud Protection")
	if data.MainDocumentsStatusAvailable {
		documents := data.MainDocumentsStatus
		renderMetricLine(builder,
			"main/Documents: "+renderStatus(boolStatus(documents.Exists)),
			fmt.Sprintf("accepted: %d", documents.FilesAccepted),
			fmt.Sprintf("delayed: %d", documents.FilesDelayed),
			fmt.Sprintf("failed: %d", documents.FilesFailed),
			fmt.Sprintf("tombstoned: %d", documents.FilesTombstoned),
		)
	}
	if data.TransferStatusesAvailable {
		counts := storageTransferCounts(data.TransferStatuses)
		renderMetricLine(builder,
			fmt.Sprintf("transfers: %d", len(data.TransferStatuses)),
			fmt.Sprintf("active: %d", counts["active"]),
			fmt.Sprintf("failed: %d", counts["failed"]),
			fmt.Sprintf("accepted: %d", counts["accepted"]),
		)
	}
	if data.LaneStatus != nil {
		laneStatus := data.LaneStatus
		parts := []string{
			"lane: " + renderStatus(firstNonEmpty(laneStatus.State, "-")),
			fmt.Sprintf("pending: %d", laneStatus.PendingItems),
			"size: " + storageFormatBytes(laneStatus.PendingBytes),
			"preflight: " + renderStatus(firstNonEmpty(laneStatus.Preflight.Status, "-")),
		}
		if laneStatus.LastTransfer != nil {
			parts = append(parts,
				"last: "+renderStatus(firstNonEmpty(laneStatus.LastTransfer.Status, "-")),
				"last bytes: "+storageFormatBytes(laneStatus.LastTransfer.TotalBytes),
			)
			if laneStatus.LastTransfer.Progress.Observed {
				parts = append(parts, fmt.Sprintf("progress: %.1f%%", laneStatus.LastTransfer.Progress.Percent))
			}
		}
		renderMetricLine(builder, parts...)
	}
	if data.CloudStatusAvailable {
		cloud := data.CloudStatus
		parts := []string{
			"cloud: " + renderStatus(firstNonEmpty(cloud.Status, "-")),
			"mode: " + firstNonEmpty(cloud.Mode, "cached"),
			"backend: " + firstNonEmpty(cloud.Config.SnapshotBackend, "-"),
		}
		if cloud.SnapshotStore != nil {
			parts = append(parts,
				"snapshot store: "+renderStatus(firstNonEmpty(cloud.SnapshotStore.Status, "-")),
				fmt.Sprintf("archives: %d", cloud.SnapshotStore.ArchiveCount),
			)
		}
		renderMetricLine(builder, parts...)
		if cloud.RemoteState != nil {
			stateParts := []string{
				"remote state: " + renderStatus(firstNonEmpty(cloud.RemoteState.State, "-")),
				"last success: " + timePtrOrDash(cloud.RemoteState.LastSuccessAt),
				"last failure: " + timePtrOrDash(cloud.RemoteState.LastFailureAt),
				"next live check: " + timePtrOrDash(cloud.RemoteState.NextLiveCheckAfter),
			}
			if cloud.RemoteState.LastErrorClass != "" {
				stateParts = append(stateParts, "last error: "+cloud.RemoteState.LastErrorClass)
			}
			renderMetricLine(builder, stateParts...)
		}
	}
	if data.BackupStatusAvailable {
		latest := "-"
		if data.BackupStatus.LatestSuccessful != nil {
			latest = data.BackupStatus.LatestSuccessful.Operation.StartedAt.UTC().Format("2006-01-02 15:04")
		}
		renderMetricLine(builder,
			"main backup: "+renderStatus(firstNonEmpty(data.BackupStatus.Status, "-")),
			"latest successful: "+latest,
			fmt.Sprintf("open findings: %d", data.BackupStatus.OpenFindings.Open),
		)
	}
	if !data.MainDocumentsStatusAvailable && !data.TransferStatusesAvailable && data.LaneStatus == nil && !data.CloudStatusAvailable && !data.BackupStatusAvailable {
		renderEmpty(builder, "No protection status returned by the backend.")
	}
	renderEmpty(builder, "MacBook mount status uses SMB by default via scripts/loom-macbook main-storage-status; physical-root status above is backend state.")
}

func renderStorageSelectableItem(builder *strings.Builder, item SelectableItem, index int, selected int, rawDetails bool) {
	marker := renderSelectedMarker(index, selected)
	width := portalRenderContext().Width
	label := firstNonEmpty(item.RecordLabel, item.Label, item.RecordRef)
	description := trimForWidth(item.Description, usableWidth(width, len(label)+12))
	status := storageRecordStatus(item)
	hint := ""
	if len(item.RelatedActions) > 0 {
		hint = portalRenderContext().Styles.Muted.Render("space actions")
	}
	fmt.Fprintf(builder, "  %s %s  %s  %s  %s\n",
		marker,
		trimForWidth(label, usableWidth(width, 56)),
		portalRenderContext().Styles.Muted.Render(description),
		renderStatus(status),
		hint,
	)
	if rawDetails {
		fmt.Fprintf(builder, "      kind=%s ref=%s\n", firstNonEmpty(item.RecordKind, "-"), firstNonEmpty(item.RecordRef, "-"))
		if item.PrimaryAction != nil {
			for key, value := range item.PrimaryAction.Executor.Payload {
				if strings.TrimSpace(value) != "" {
					fmt.Fprintf(builder, "      %s=%s\n", key, value)
				}
			}
		}
	}
}

func renderStorageStatusDetails(builder *strings.Builder, data StorageData, rawDetails bool) {
	if !rawDetails {
		return
	}
	renderSection(builder, "Diagnostics")
	if len(data.Tree.Entries) > 0 {
		report := storagefidelity.ReportForViewEntries("", "", data.Tree.Entries, time.Now().UTC())
		renderMetricLine(builder,
			fmt.Sprintf("fidelity errors: %d", report.Summary.Errors),
			fmt.Sprintf("fidelity warnings: %d", report.Summary.Warnings),
			fmt.Sprintf("not safe: %d", report.Summary.NotSafe),
			fmt.Sprintf("partial: %d", report.Summary.PartiallySafe),
		)
	}
	if data.WatchedRootBackupItemsAvailable {
		bytes := watchedRootBackupByteCounts(data.WatchedRootBackupItems)
		renderMetricLine(builder,
			"watched roots accepted: "+storageFormatBytes(bytes.Accepted),
			"duplicate: "+storageFormatBytes(bytes.Duplicate),
			"failed: "+storageFormatBytes(bytes.Failed),
			"skipped: "+storageFormatBytes(bytes.Skipped),
		)
	}
	if data.MainDocumentsStatusAvailable && mainDocumentsPortalMetricsAny(data.MainDocumentsStatus.Metrics) {
		metrics := data.MainDocumentsStatus.Metrics
		renderMetricLine(builder,
			"documents total: "+portalDurationMS(metrics.TotalDurationMS),
			"discover: "+portalDurationMS(metrics.DiscoverDurationMS),
			"hash: "+portalDurationMS(metrics.HashDurationMS),
			"retain: "+portalDurationMS(metrics.RetentionCopyDurationMS),
			"register: "+portalDurationMS(metrics.RegisterDurationMS),
			"reconcile: "+portalDurationMS(metrics.ReconcileDurationMS),
		)
	}
	renderMetricLine(builder, "snapshot inventory: explicit live action only")

	if len(data.Tree.Entries) > 0 {
		renderSection(builder, "Detailed Storage Paths")
		limit := len(data.Tree.Entries)
		if limit > 12 {
			limit = 12
		}
		for idx, entry := range data.Tree.Entries[:limit] {
			fmt.Fprintf(builder, "  %s  %s  %s\n",
				firstNonEmpty(entry.ViewPath, entry.DisplayName, "-"),
				renderStatus(firstNonEmpty(entry.Permissions, "-")),
				storageViewEntryDescription(entry),
			)
			if idx == limit-1 && len(data.Tree.Entries) > limit {
				renderEmpty(builder, fmt.Sprintf("%d more detailed path(s) hidden.", len(data.Tree.Entries)-limit))
			}
		}
	}

	if rawDetails && data.MainDocumentsStatusAvailable && len(data.MainDocumentsStatus.Imports) > 0 {
		renderSection(builder, "Recent Main Documents Imports")
		for idx, item := range data.MainDocumentsStatus.Imports {
			if idx >= 5 {
				renderEmpty(builder, fmt.Sprintf("%d more import(s) hidden.", len(data.MainDocumentsStatus.Imports)-idx))
				break
			}
			fmt.Fprintf(builder, "  %s  %s  %s\n", renderStatus(item.State), firstNonEmpty(item.RelativePath, "-"), storageFormatBytes(item.SizeBytes))
		}
	}
	if rawDetails && data.TransferStatusesAvailable && len(data.TransferStatuses) > 0 {
		renderSection(builder, "Recent File Transfers")
		for idx, transfer := range data.TransferStatuses {
			if idx >= 5 {
				renderEmpty(builder, fmt.Sprintf("%d more transfer(s) hidden.", len(data.TransferStatuses)-idx))
				break
			}
			fmt.Fprintf(builder, "  %s  %s  %s  %s\n",
				renderStatus(transfer.Manifest.Status),
				firstNonEmpty(transfer.Manifest.TransferID, "-"),
				firstNonEmpty(transfer.Manifest.TransferKind, "-"),
				firstNonEmpty(transfer.Manifest.SourceRelativePath, transfer.Manifest.DestinationLogicalPath, "-"),
			)
		}
	}
	if rawDetails && data.DBStatusAvailable && len(data.DBStatus.Tables) > 0 {
		renderSection(builder, "Database Pressure")
		for idx, table := range data.DBStatus.Tables {
			if idx >= 8 {
				renderEmpty(builder, fmt.Sprintf("%d more table(s) hidden.", len(data.DBStatus.Tables)-idx))
				break
			}
			fmt.Fprintf(builder, "  %s  %s  rows=%d size=%s role=%s\n",
				renderStatus(table.Pressure),
				table.QualifiedName,
				table.Rows,
				storageFormatBytes(table.TotalBytes),
				table.RetentionRole,
			)
		}
	}
}

type storageCollapsedRow struct {
	Path        string
	RecordKind  string
	Description string
	Status      string
	Files       int
	Directories int
	Writable    bool
}

func storageCollapsedSelectableItems(state ScreenState) []SelectableItem {
	rows := storageCollapsedRows(state.Data.Storage)
	items := make([]SelectableItem, 0, len(rows))
	for idx, row := range rows {
		action := NewStorageCollapsedPathInspectAction(row)
		items = append(items, SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         row.Path,
			Description:   row.Description,
			Screen:        ScreenStorage,
			RowIndex:      idx,
			RecordKind:    row.RecordKind,
			RecordRef:     row.Path,
			RecordLabel:   row.Path,
			PrimaryAction: actionPtr(action),
		})
	}
	return items
}

func storageCollapsedRows(data StorageData) []storageCollapsedRow {
	if !storageHasUserStorageSignal(data) {
		return nil
	}
	rowsByPath := map[string]*storageCollapsedRow{}
	order := map[string]int{
		"main/Documents": 0,
		"main/Archive":   1,
	}
	add := func(pathValue string, entryKind string, permissions string, writable bool) {
		pathValue = storageCleanViewPath(pathValue)
		if pathValue == "" || storagePathHiddenByDefault(pathValue) {
			return
		}
		key := storageCollapsedKey(pathValue)
		if key == "" {
			return
		}
		row := rowsByPath[key]
		if row == nil {
			row = &storageCollapsedRow{
				Path:       key,
				RecordKind: storageCollapsedRecordKind(key),
				Status:     firstNonEmpty(storageCollapsedPermissionForPath(key), permissions, storageview.PermissionReadOnly),
			}
			rowsByPath[key] = row
		}
		if writable {
			row.Writable = true
			row.Status = storageview.PermissionWritable
		}
		if permissions != "" && row.Status == "" {
			row.Status = permissions
		}
		switch entryKind {
		case storageview.EntryKindFile:
			row.Files++
		case storageview.EntryKindDirectory:
			row.Directories++
		}
	}

	add("main/Documents", storageview.EntryKindDirectory, storageview.PermissionWritable, true)
	add("main/Archive", storageview.EntryKindDirectory, storageview.PermissionControlled, false)
	for _, entry := range data.Tree.Entries {
		add(entry.ViewPath, entry.EntryKind, entry.Permissions, entry.Writable)
	}
	if len(data.Tree.Entries) == 0 {
		var walk func(storageview.Node)
		walk = func(node storageview.Node) {
			add(firstNonEmpty(node.Path, node.Name), node.EntryKind, node.Permissions, node.Writable)
			for _, child := range node.Children {
				walk(child)
			}
		}
		walk(data.Tree.Root)
	}

	rows := make([]storageCollapsedRow, 0, len(rowsByPath))
	for _, row := range rowsByPath {
		row.Description = storageCollapsedDescription(*row)
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		leftOrder, leftKnown := order[rows[i].Path]
		rightOrder, rightKnown := order[rows[j].Path]
		if leftKnown || rightKnown {
			if leftKnown && rightKnown {
				return leftOrder < rightOrder
			}
			return leftKnown
		}
		return rows[i].Path < rows[j].Path
	})
	return rows
}

func storageHasUserStorageSignal(data StorageData) bool {
	return data.Tree.ViewKey != "" ||
		data.Tree.Root.Path != "" ||
		data.Tree.Root.Name != "" ||
		len(data.Tree.Root.Children) > 0 ||
		len(data.Tree.Entries) > 0 ||
		data.FilesystemStatusAvailable ||
		data.MainDocumentsStatusAvailable ||
		data.RetentionStatusAvailable ||
		data.TransferStatusesAvailable ||
		data.CloudStatusAvailable ||
		data.BackupStatusAvailable ||
		data.DBStatusAvailable
}

func storageCollapsedDescription(row storageCollapsedRow) string {
	parts := []string{}
	if row.Files > 0 {
		parts = append(parts, fmt.Sprintf("%d files", row.Files))
	}
	if row.Directories > 0 {
		parts = append(parts, fmt.Sprintf("%d folders", row.Directories))
	}
	if len(parts) == 0 {
		parts = append(parts, "top-level path")
	}
	switch row.Path {
	case "main/Documents":
		parts = append(parts, "writable main storage")
	case "main/Archive":
		parts = append(parts, "controlled archive")
	default:
		if strings.HasPrefix(strings.ToLower(row.Path), "backups/") || strings.Contains(row.Path, "/Backups") {
			parts = append(parts, "folder watcher backup")
		} else if strings.Contains(row.Path, "/Dropzone") {
			parts = append(parts, "custody transfer")
		} else if strings.Contains(row.Path, "/Lane") {
			parts = append(parts, "lane transfer")
		}
	}
	return strings.Join(parts, "  ")
}

func storageCollapsedKey(pathValue string) string {
	pathValue = storageCleanViewPath(pathValue)
	if pathValue == "" || storagePathHiddenByDefault(pathValue) {
		return ""
	}
	segments := strings.Split(pathValue, "/")
	if len(segments) == 0 {
		return ""
	}
	if strings.EqualFold(segments[0], "main") {
		if len(segments) < 2 {
			return ""
		}
		switch strings.ToLower(segments[1]) {
		case "documents":
			return "main/Documents"
		case "archive":
			return "main/Archive"
		default:
			return ""
		}
	}
	if strings.EqualFold(segments[0], "backups") {
		if len(segments) < 2 {
			return ""
		}
		return "backups/" + segments[1]
	}
	if len(segments) < 2 {
		return ""
	}
	switch strings.ToLower(segments[1]) {
	case "backups":
		return segments[0] + "/Backups"
	case "dropzone":
		return segments[0] + "/Dropzone"
	case "lane":
		return segments[0] + "/Lane"
	default:
		return ""
	}
}

func storageCollapsedRecordKind(pathValue string) string {
	switch {
	case pathValue == "main/Archive":
		return "storage_archive"
	case strings.Contains(pathValue, "/Dropzone") || strings.Contains(pathValue, "/Lane"):
		return "storage_transfer"
	default:
		return "storage_node"
	}
}

func storageCollapsedPermissionForPath(pathValue string) string {
	permissions, _, _ := storageview.PermissionsForPath(pathValue)
	return permissions
}

func storageCleanViewPath(pathValue string) string {
	return strings.Trim(strings.ReplaceAll(strings.TrimSpace(pathValue), "\\", "/"), "/")
}

func storagePathHiddenByDefault(pathValue string) bool {
	pathValue = storageCleanViewPath(pathValue)
	if pathValue == "" {
		return true
	}
	segments := strings.Split(pathValue, "/")
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		if strings.HasPrefix(segment, ".") || strings.HasPrefix(segment, "_") {
			return true
		}
	}
	return false
}

func storageVisibleRowLimit(height int) int {
	if height <= 0 {
		return 0
	}
	limit := height - 18
	if limit < 5 {
		return 5
	}
	if limit > 24 {
		return 24
	}
	return limit
}

func storageRecordStatus(item SelectableItem) string {
	if item.PrimaryAction == nil {
		return ""
	}
	payload := item.PrimaryAction.Executor.Payload
	if severity := payload["fidelity_severity"]; severity == storagefidelity.SeverityError || severity == storagefidelity.SeverityWarning {
		return severity
	}
	switch item.RecordKind {
	case "storage_node":
		if payload["writable"] == "true" {
			return "writable"
		}
		return firstNonEmpty(payload["permissions"], storageview.PermissionReadOnly)
	case "storage_archive":
		return "archive"
	case "storage_transfer":
		return firstNonEmpty(payload["availability_state"], "transfer")
	default:
		return firstNonEmpty(payload["availability_state"], payload["entry_kind"], item.RecordKind)
	}
}

func boolStatus(value bool) string {
	if value {
		return "ok"
	}
	return "missing"
}

func mainDocumentsPortalMetricsAny(metrics mainstorage.Metrics) bool {
	return metrics.TotalDurationMS > 0 ||
		metrics.DiscoverDurationMS > 0 ||
		metrics.HashDurationMS > 0 ||
		metrics.RetentionCopyDurationMS > 0 ||
		metrics.RegisterDurationMS > 0 ||
		metrics.ReconcileDurationMS > 0
}

func portalDurationMS(value int64) string {
	if value <= 0 {
		return "0ms"
	}
	return fmt.Sprintf("%dms", value)
}

func renderStorageAttention(builder *strings.Builder, data StorageData, rawDetails bool) {
	var lines []string
	if data.MainDocumentsStatusAvailable {
		for _, item := range data.MainDocumentsStatus.Imports {
			if routineIgnoredMainDocumentAttention(item) {
				continue
			}
			if item.State == mainstorage.StateFailedImport || item.State == mainstorage.StateIgnored ||
				item.State == mainstorage.StateMissingDeferred || item.State == mainstorage.StateTombstoned {
				lines = append(lines, fmt.Sprintf("main/Documents %s: %s %s", item.State, firstNonEmpty(item.RelativePath, "-"), firstNonEmpty(item.Error, item.DelayReason, item.IgnoredReason)))
			}
		}
	}
	if data.TransferStatusesAvailable {
		for _, transfer := range data.TransferStatuses {
			if transfer.Manifest.Status == "failed" || transfer.Manifest.LastErrorMessage != "" {
				lines = append(lines, fmt.Sprintf("transfer %s: %s", firstNonEmpty(transfer.Manifest.TransferID, "-"), firstNonEmpty(transfer.Manifest.LastErrorMessage, transfer.Manifest.Status)))
			}
		}
	}
	if data.FilesystemStatusAvailable {
		for _, root := range data.FilesystemStatus.Roots {
			if root.Status == "warning" || root.Status == "error" {
				lines = append(lines, fmt.Sprintf("physical root %s: %s", root.Key, firstNonEmpty(root.Message, root.Status)))
			}
		}
	}
	if len(data.Tree.Entries) > 0 {
		report := storagefidelity.ReportForViewEntries("", "", data.Tree.Entries, time.Now().UTC())
		for _, item := range report.Items {
			for _, finding := range item.Findings {
				if finding.Blocking || finding.Severity == storagefidelity.SeverityWarning {
					lines = append(lines, fmt.Sprintf("fidelity %s: %s %s", finding.Severity, firstNonEmpty(item.ViewPath, item.StorageEntryID), finding.Summary))
					break
				}
			}
		}
	}
	if data.CloudStatusAvailable && data.CloudStatus.Config.Enabled {
		switch data.CloudStatus.Status {
		case "cooling_down":
			lines = append(lines, "cloud backup live checks are cooling down")
		case "unreachable", "lock_busy":
			lines = append(lines, "cloud backup is "+data.CloudStatus.Status)
		}
	}
	if data.DBStatusAvailable {
		for _, warning := range data.DBStatus.Warnings {
			lines = append(lines, "database: "+warning)
		}
	}
	if len(lines) == 0 {
		return
	}
	renderAttentionSection(builder, "Attention")
	limit := len(lines)
	if limit > 5 {
		limit = 5
	}
	renderEmpty(builder, fmt.Sprintf("Storage reports %d advisory or attention item(s). Inspect typed storage and fidelity diagnostics before repair.", len(lines)))
	if rawDetails {
		for _, line := range lines[:limit] {
			renderEmpty(builder, line)
		}
	}
	if len(lines) > limit {
		renderEmpty(builder, fmt.Sprintf("%d more storage advisory or attention item(s) hidden.", len(lines)-limit))
	} else if !rawDetails {
		renderEmpty(builder, "Press tab for local storage diagnostics on this surface.")
	}
}

func storageWorstDatabasePressure(tables []maintenance.DatabaseTable) string {
	worst := "ok"
	for _, table := range tables {
		if table.Pressure == "critical" {
			return "critical"
		}
		if table.Pressure == "warning" {
			worst = "warning"
		}
	}
	return worst
}

func storageTimeOrDash(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format("2006-01-02")
}

func routineIgnoredMainDocumentAttention(item mainstorage.FileStatus) bool {
	if item.State != mainstorage.StateIgnored {
		return false
	}
	reason := strings.ToLower(strings.TrimSpace(item.IgnoredReason))
	if strings.Contains(reason, "appledouble") ||
		strings.Contains(reason, "finder metadata") ||
		strings.Contains(reason, "platform metadata") ||
		strings.Contains(reason, "office lock") ||
		strings.Contains(reason, "temporary") ||
		strings.Contains(reason, "placeholder sidecar") {
		return true
	}
	relativePath := strings.TrimSpace(item.RelativePath)
	if slash := strings.LastIndex(relativePath, "/"); slash >= 0 {
		relativePath = relativePath[slash+1:]
	}
	lower := strings.ToLower(relativePath)
	return relativePath == ".DS_Store" ||
		strings.HasPrefix(relativePath, "._") ||
		strings.HasPrefix(relativePath, "~$") ||
		strings.HasPrefix(relativePath, ".~lock.") ||
		strings.HasSuffix(lower, ".part") ||
		strings.HasSuffix(lower, ".tmp") ||
		strings.HasSuffix(lower, ".download") ||
		strings.HasSuffix(lower, ".crdownload") ||
		strings.HasSuffix(lower, ".icloud") ||
		strings.HasPrefix(lower, ".nfs") ||
		strings.HasPrefix(lower, ".smbdelete")
}

func storageTransferCounts(transfers []filetransfer.Status) map[string]int {
	counts := map[string]int{"active": 0, "failed": 0, "accepted": 0}
	for _, transfer := range transfers {
		switch transfer.Manifest.Status {
		case "accepted":
			counts["accepted"]++
		case "failed", "aborted":
			counts["failed"]++
		case "pending", "uploading", "paused", "completing":
			counts["active"]++
		}
	}
	return counts
}

type storageSafetyCounts struct {
	PendingBackup      int
	PendingBackupBytes int64
	FailedBackup       int
	FailedBackupBytes  int64
	SkippedTooLarge    int
	RetainedOrSnapshot int
}

func storageCatalogSafetyCounts(entries []storagecatalog.Entry) storageSafetyCounts {
	var counts storageSafetyCounts
	for _, entry := range entries {
		if entry.RetentionState == storagecatalog.RetentionStateRetained || entry.RetentionState == storagecatalog.RetentionStateSnapshot {
			counts.RetainedOrSnapshot++
		}
		if entry.StorageClass != storagecatalog.StorageClassPrivateBackup {
			continue
		}
		if entry.AvailabilityState == storagecatalog.AvailabilityStatePending || entry.AvailabilityState == storagecatalog.AvailabilityStateDiscovered {
			counts.PendingBackup++
			if entry.SizeBytes != nil {
				counts.PendingBackupBytes += *entry.SizeBytes
			}
		}
		if entry.AvailabilityState == storagecatalog.AvailabilityStateFailed || entry.ProcessingState == storagecatalog.ProcessingStateFailed {
			counts.FailedBackup++
			if entry.SizeBytes != nil {
				counts.FailedBackupBytes += *entry.SizeBytes
			}
		}
		if entry.ProcessingState == storagecatalog.ProcessingStateExcluded && strings.Contains(string(entry.Metadata), "backup_file_too_large") {
			counts.SkippedTooLarge++
		}
	}
	return counts
}

type watchedRootBackupBytes struct {
	Accepted  int64
	Duplicate int64
	Failed    int64
	Skipped   int64
}

func watchedRootBackupByteCounts(items []mainwatchedroots.BackupItem) watchedRootBackupBytes {
	var counts watchedRootBackupBytes
	for _, item := range items {
		if item.SizeBytes <= 0 {
			continue
		}
		switch item.Status {
		case mainwatchedroots.BackupItemStatusAccepted:
			counts.Accepted += item.SizeBytes
		case mainwatchedroots.BackupItemStatusDuplicate:
			counts.Duplicate += item.SizeBytes
		case mainwatchedroots.BackupItemStatusFailed:
			counts.Failed += item.SizeBytes
		case mainwatchedroots.BackupItemStatusSkipped:
			counts.Skipped += item.SizeBytes
		}
	}
	return counts
}
