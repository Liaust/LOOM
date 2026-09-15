package portal

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/nodes"
	loomsync "loom.local/loom/internal/sync"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

type nodePortalSummary struct {
	Ref              string
	Label            string
	Kind             string
	Role             string
	Runtime          string
	Status           string
	Presence         string
	LastSeen         *time.Time
	LastHeartbeat    *time.Time
	WatchedRoots     int
	OpenFindings     int
	BackupStatus     string
	SyncConflicts    int
	DeletionRequests int
	PrivateBackups   int
}

type nodeFindingGroup struct {
	Ref       string
	NodeRef   string
	RootRef   string
	Severity  string
	Kind      string
	Summary   string
	Count     int
	Latest    time.Time
	Prototype mainwatchedroots.Finding
}

func renderNodes(builder *strings.Builder, state ScreenState) {
	data := state.Data.Nodes
	records := ScreenRecordItems(state)
	row := renderTopAvailableActions(builder, state, records)
	renderOperationalActionGroups(builder, state, records, &row)
	counts := nodeSurfaceCounts(data)
	renderMetricLine(builder,
		fmt.Sprintf("nodes=%d", len(data.Nodes)),
		fmt.Sprintf("online=%d", counts["online"]),
		fmt.Sprintf("attention=%d", counts["attention"]),
		fmt.Sprintf("protected_folders=%d", len(data.ProtectedFolders)),
		fmt.Sprintf("watched_roots=%d", len(data.WatchedRoots)),
		fmt.Sprintf("open_conflicts=%d", len(data.SyncConflicts)),
	)

	renderNodeAttention(builder, state, &row)

	renderPrimarySection(builder, "Node Cards")
	summaries := nodePortalSummaries(data)
	if len(summaries) == 0 {
		renderEmpty(builder, "No nodes returned by the backend.")
	} else {
		for _, summary := range summaries {
			health := nodeHealthLabel(summary)
			fmt.Fprintf(builder, "  %s %s  %s  %s/%s  runtime=%s\n",
				renderSelectedMarker(row, state.SelectedIndex),
				summary.Label,
				renderStatus(health),
				firstNonEmpty(summary.Kind, "node"),
				firstNonEmpty(summary.Role, "unknown"),
				firstNonEmpty(summary.Runtime, "-"),
			)
			fmt.Fprintf(builder, "      agent=%s  watchers=%d  findings=%d  sync_conflicts=%d  backup=%s\n",
				nodeAgentLabel(summary),
				summary.WatchedRoots,
				summary.OpenFindings,
				summary.SyncConflicts,
				firstNonEmpty(summary.BackupStatus, "not reported"),
			)
			row++
		}
	}

	renderProtectedFolders(builder, state, &row)

	renderPrimarySection(builder, "Active Watched Roots")
	if len(data.WatchedRoots) == 0 {
		renderEmpty(builder, "No watched roots are registered.")
	} else {
		for _, group := range watchedRootsByPurpose(data) {
			fmt.Fprintf(builder, "  %s\n", portalRenderContext().Styles.Muted.Render(group.Label))
			for _, root := range group.Roots {
				findingCount := watchedRootFindingCount(data.WatchedRootFindings, root.Root.NodeID, root.Root.RootKey)
				fmt.Fprintf(builder, "  %s %s  %s  findings=%d  reported=%s\n",
					renderSelectedMarker(row, state.SelectedIndex),
					watchedRootPurposefulLabel(data, root.Root),
					renderStatus(firstNonEmpty(root.Root.Status, "unknown")),
					findingCount,
					shortTimeOrDash(root.Root.LastReportedAt),
				)
				row++
			}
		}
	}

	if state.RawDetails {
		renderNodesDiagnostics(builder, state, &row)
	} else {
		renderDiagnosticsSection(builder, "Diagnostics")
		renderEmpty(builder, "Press tab for raw node IDs, watched-root keys, sync rows, backup batches, replicas, private backups, and deletion requests.")
	}
}

func renderProtectedFolders(builder *strings.Builder, state ScreenState, row *int) {
	data := state.Data.Nodes
	renderPrimarySection(builder, "Protected Folders")
	records := orderedProtectedFolders(data.ProtectedFolders)
	if len(records) == 0 {
		renderEmpty(builder, "No protected-folder contracts are registered.")
		return
	}
	group := ""
	for _, record := range records {
		nextGroup := protectedFolderGroupLabel(record.Lifecycle)
		if nextGroup != group {
			group = nextGroup
			fmt.Fprintf(builder, "  %s\n", portalRenderContext().Styles.Muted.Render(group))
		}
		label := firstNonEmpty(record.Contract.DisplayName, record.Key, "Protected folder")
		node := nodeDisplayName(data, firstNonEmpty(record.OwnerNodeKey, record.OwnerNodeID, record.Contract.OwnerNode))
		path := firstNonEmpty(record.Contract.Target.Path, protectedFolderPreflightPath(record), record.Path, "-")
		fmt.Fprintf(builder, "  %s %s  %s  node=%s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			trimForWidth(label, usableWidth(portalRenderContext().Width, 30)),
			renderStatus(firstNonEmpty(record.Lifecycle, record.Status, "unknown")),
			trimForWidth(node, usableWidth(portalRenderContext().Width, 48)),
		)
		evidence := protectedFolderRenderEvidence(record)
		fmt.Fprintf(builder, "      path=%s\n", trimForWidth(path, usableWidth(portalRenderContext().Width, 12)))
		fmt.Fprintf(builder, "      last_scan=%s  last_backup=%s\n", evidence.LastScan, evidence.LastBackup)
		if record.AttentionReason != "" {
			fmt.Fprintf(builder, "      attention=%s\n", trimForWidth(record.AttentionReason, usableWidth(portalRenderContext().Width, 16)))
		}
		(*row)++
	}
}

type protectedFolderEvidenceDetails struct {
	RootReportedAt     *time.Time `json:"root_reported_at"`
	LatestBackupAt     *time.Time `json:"latest_backup_at"`
	LatestBackupStatus string     `json:"latest_backup_status"`
}

type protectedFolderRenderEvidenceResult struct {
	LastScan   string
	LastBackup string
}

func protectedFolderRenderEvidence(record backupcontracts.ProtectedFolderRecord) protectedFolderRenderEvidenceResult {
	details := protectedFolderEvidenceDetails{}
	_ = json.Unmarshal(record.Details, &details)
	result := protectedFolderRenderEvidenceResult{LastScan: timePtrOrDash(details.RootReportedAt), LastBackup: "not accepted"}
	if record.LastBackupAcceptedAt != nil {
		result.LastBackup = timePtrOrDash(record.LastBackupAcceptedAt)
	} else if details.LatestBackupAt != nil {
		result.LastBackup = fmt.Sprintf("%s (%s, not accepted for current generation)", timePtrOrDash(details.LatestBackupAt), firstNonEmpty(details.LatestBackupStatus, "reported"))
	}
	return result
}

func protectedFolderPreflightPath(record backupcontracts.ProtectedFolderRecord) string {
	if record.Preflight == nil {
		return ""
	}
	return firstNonEmpty(record.Preflight.CanonicalPath, record.Preflight.RequestedPath)
}

func protectedFolderGroupLabel(lifecycle string) string {
	switch lifecycle {
	case backupcontracts.ProtectedFolderStatusProtected, backupcontracts.ProtectedFolderStatusActive:
		return "Active / Protected"
	case backupcontracts.ProtectedFolderStatusWaitingForNode, backupcontracts.ProtectedFolderStatusReady, backupcontracts.ProtectedFolderStatusActivating:
		return "Waiting"
	case backupcontracts.ProtectedFolderStatusAttention:
		return "Attention"
	case backupcontracts.ProtectedFolderStatusDisabled:
		return "Disabled"
	default:
		return "Other"
	}
}

func renderNodeAttention(builder *strings.Builder, state ScreenState, row *int) {
	data := state.Data.Nodes
	renderAttentionSection(builder, "Attention")
	groups := nodeFindingGroups(data.WatchedRootFindings)
	issueCount := 0
	for _, group := range groups {
		if nodeSeverityIsActionable(group.Severity) {
			issueCount += group.Count
		}
	}
	issueCount += len(data.SyncConflicts)
	for _, request := range data.DeletionRequests {
		if nodeDeletionRequestIsActionable(request) {
			issueCount++
		}
	}
	if issueCount == 0 {
		renderEmpty(builder, "No node-level findings, open conflicts, or deletion requests need attention.")
		return
	}
	renderEmpty(builder, fmt.Sprintf("Doctor owns %d node issue(s). Node cards and shared roots below stay focused on current topology.", issueCount))
	if !state.RawDetails {
		renderEmpty(builder, "Press tab for watched-root findings, sync conflicts, and deletion requests.")
		return
	}
	wrote := false
	for _, group := range groups {
		if !nodeSeverityIsActionable(group.Severity) {
			continue
		}
		fmt.Fprintf(builder, "  %s %s  %s  %s",
			renderSelectedMarker(*row, state.SelectedIndex),
			nodeDisplayName(data, group.NodeRef),
			renderStatus(group.Severity),
			firstNonEmpty(group.Summary, titleFromToken(group.Kind), "Watched-root finding"),
		)
		if group.Count > 1 {
			fmt.Fprintf(builder, "  x%d", group.Count)
		}
		builder.WriteString("\n")
		(*row)++
		wrote = true
	}
	for _, conflict := range data.SyncConflicts {
		fmt.Fprintf(builder, "  %s %s  %s  %s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			nodeDisplayName(data, conflict.OriginNodeID),
			renderStatus(firstNonEmpty(conflict.Status, "open")),
			firstNonEmpty(conflict.Summary, "Sync conflict needs review"),
		)
		(*row)++
		wrote = true
	}
	for _, request := range data.DeletionRequests {
		if !nodeDeletionRequestIsActionable(request) {
			continue
		}
		fmt.Fprintf(builder, "  %s %s  %s  deletion %s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			nodeDisplayName(data, request.OriginNodeID),
			renderStatus(firstNonEmpty(request.Status, "pending")),
			firstNonEmpty(request.TargetKind, "request"),
		)
		(*row)++
		wrote = true
	}
	if !wrote {
		renderEmpty(builder, "No node-level findings, open conflicts, or deletion requests need attention.")
	}
}

func renderNodesDiagnostics(builder *strings.Builder, state ScreenState, row *int) {
	data := state.Data.Nodes
	renderSection(builder, "Diagnostics")
	renderEmpty(builder, "Raw backend records are shown for debugging.")

	renderSection(builder, "Raw Nodes")
	if len(data.Nodes) == 0 {
		renderEmpty(builder, "No node records returned by the backend.")
	} else {
		for _, node := range data.Nodes {
			fmt.Fprintf(builder, "  %s %s  key=%s  %s  presence=%s\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				firstNonEmpty(node.NodeID, "-"),
				firstNonEmpty(node.NodeKey, "-"),
				renderStatus(firstNonEmpty(node.Status, "-")),
				firstNonEmpty(node.PresenceState, "-"),
			)
			(*row)++
		}
	}

	renderSection(builder, "Raw Watched Roots")
	if len(data.WatchedRoots) == 0 {
		renderEmpty(builder, "No watched roots returned by the backend.")
	} else {
		for _, root := range data.WatchedRoots {
			fmt.Fprintf(builder, "  %s %s/%s  %s  worker=%s  reported=%s\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				firstNonEmpty(root.Root.NodeID, "-"),
				firstNonEmpty(root.Root.RootKey, "-"),
				renderStatus(firstNonEmpty(root.Root.Status, "-")),
				firstNonEmpty(root.Root.WorkerKey, "-"),
				timeOrDash(root.Root.LastReportedAt),
			)
			(*row)++
		}
	}

	renderSection(builder, "Raw Watched-Root Findings")
	if len(data.WatchedRootFindings) == 0 {
		renderEmpty(builder, "No open watched-root findings returned by the backend.")
	} else {
		for _, finding := range data.WatchedRootFindings {
			fmt.Fprintf(builder, "  %s %s/%s  %s  %s\n", renderSelectedMarker(*row, state.SelectedIndex), finding.NodeID, finding.RootKey, renderStatus(finding.Severity), finding.Summary)
			(*row)++
		}
	}

	renderSection(builder, "Watched Root Backup Status")
	if data.BackupStatusSelectableCount() == 0 {
		renderEmpty(builder, "No watched-root backup status returned by the backend.")
	} else {
		fmt.Fprintf(builder, "  %s %s/%s  %s  batches=%d items=%d skipped=%d failed=%d findings=%d bytes=%s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			firstNonEmpty(data.BackupStatus.Root.NodeID, "-"),
			firstNonEmpty(data.BackupStatus.Root.RootKey, "-"),
			renderStatus(firstNonEmpty(data.BackupStatus.Status, "-")),
			data.BackupStatus.BatchCount,
			data.BackupStatus.ItemCount,
			data.BackupStatus.SkippedCount,
			data.BackupStatus.FailedCount,
			len(data.BackupStatus.LatestFindings),
			storageFormatBytes(data.BackupStatus.TotalBytes),
		)
		(*row)++
	}

	renderSection(builder, "Raw Backup Batches")
	if len(data.BackupBatches) == 0 {
		renderEmpty(builder, "No watched-root backup batches returned by the backend.")
	} else {
		for _, batch := range data.BackupBatches {
			fmt.Fprintf(builder, "  %s %s  %s/%s  %s  items=%d failed=%d received=%s\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				firstNonEmpty(batch.WatchedRootBackupBatchID, "-"),
				firstNonEmpty(batch.NodeID, "-"),
				firstNonEmpty(batch.RootKey, "-"),
				renderStatus(firstNonEmpty(batch.Status, "-")),
				batch.ItemCount,
				batch.FailedCount,
				timeOrDash(batch.ReceivedAt),
			)
			(*row)++
		}
	}

	renderSection(builder, "Sync Status")
	renderMetricLine(builder,
		fmt.Sprintf("cursors=%d", data.SyncStatus.Summary.CursorCount),
		fmt.Sprintf("recent_batches=%d", data.SyncStatus.Summary.RecentBatchCount),
		fmt.Sprintf("open_conflicts=%d", data.SyncStatus.Summary.OpenConflictCount),
		fmt.Sprintf("replicas=%d", data.SyncStatus.Summary.ReplicaCount),
	)

	renderNodesSyncDiagnostics(builder, state, row)
	builder.WriteString("Raw local node-agent details still come from loom-node-agent on the workspace node.\n")
}

func renderNodesSyncDiagnostics(builder *strings.Builder, state ScreenState, row *int) {
	data := state.Data.Nodes
	renderSection(builder, "Raw Sync Batches")
	if len(data.SyncBatches) == 0 {
		renderEmpty(builder, "No sync batches returned by the backend.")
	} else {
		for _, batch := range data.SyncBatches {
			fmt.Fprintf(builder, "  %s %s  %s  node=%s  items=%d conflicts=%d failed=%d\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				firstNonEmpty(batch.SyncBatchID, "-"),
				renderStatus(firstNonEmpty(batch.Status, "-")),
				firstNonEmpty(batch.OriginNodeID, "-"),
				batch.ItemCount,
				batch.ConflictCount,
				batch.FailedCount,
			)
			(*row)++
		}
	}

	renderSection(builder, "Raw Sync Conflicts")
	if len(data.SyncConflicts) == 0 {
		renderEmpty(builder, "No sync conflicts returned by the backend.")
	} else {
		for _, conflict := range data.SyncConflicts {
			fmt.Fprintf(builder, "  %s %s  %s  node=%s  %s\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				firstNonEmpty(conflict.SyncConflictID, "-"),
				renderStatus(firstNonEmpty(conflict.Status, "-")),
				firstNonEmpty(conflict.OriginNodeID, "-"),
				firstNonEmpty(conflict.Summary, "-"),
			)
			(*row)++
		}
	}

	renderSection(builder, "Raw Sync Replicas")
	if len(data.SyncReplicas) == 0 {
		renderEmpty(builder, "No sync replicas returned by the backend.")
	} else {
		for _, replica := range data.SyncReplicas {
			fmt.Fprintf(builder, "  %s %s  %s  source=%s replica=%s kind=%s\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				firstNonEmpty(replica.ReplicaID, "-"),
				renderStatus(firstNonEmpty(replica.FreshnessState, "-")),
				firstNonEmpty(replica.SourceNodeID, "-"),
				firstNonEmpty(replica.ReplicaNodeID, "-"),
				firstNonEmpty(replica.ReplicatedKind, "-"),
			)
			(*row)++
		}
	}

	renderSection(builder, "Raw Private Backups")
	if len(data.PrivateBackups) == 0 {
		renderEmpty(builder, "No private backups returned by the backend.")
	} else {
		for _, backup := range data.PrivateBackups {
			fmt.Fprintf(builder, "  %s %s  %s  node=%s  bytes=%s\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				firstNonEmpty(backup.PrivateBackupOperationID, "-"),
				renderStatus(firstNonEmpty(backup.Status, "-")),
				firstNonEmpty(backup.OriginNodeID, "-"),
				storageFormatBytes(backup.CoarseSizeBytes),
			)
			(*row)++
		}
	}

	renderSection(builder, "Raw Deletion Requests")
	if len(data.DeletionRequests) == 0 {
		renderEmpty(builder, "No deletion requests returned by the backend.")
	} else {
		for _, request := range data.DeletionRequests {
			fmt.Fprintf(builder, "  %s %s  %s  %s:%s  action=%s\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				firstNonEmpty(request.DeletionRequestID, "-"),
				renderStatus(firstNonEmpty(request.Status, "-")),
				firstNonEmpty(request.TargetKind, "-"),
				firstNonEmpty(request.TargetRef, "-"),
				firstNonEmpty(request.RequestedAction, "-"),
			)
			(*row)++
		}
	}
}

func nodeSurfaceCounts(data NodesData) map[string]int {
	counts := map[string]int{}
	for _, node := range data.Nodes {
		if nodeStatusOnline(node) {
			counts["online"]++
		}
		if nodeNeedsAttention(node) {
			counts["attention"]++
		}
	}
	for _, finding := range data.WatchedRootFindings {
		if nodeSeverityIsActionable(finding.Severity) {
			counts["attention"]++
		}
	}
	counts["attention"] += len(data.SyncConflicts)
	for _, request := range data.DeletionRequests {
		if nodeDeletionRequestIsActionable(request) {
			counts["attention"]++
		}
	}
	return counts
}

func nodePortalSummaries(data NodesData) []nodePortalSummary {
	nodeMap := map[string]*nodePortalSummary{}
	order := []string{}
	addRef := func(ref string) *nodePortalSummary {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			ref = "unknown"
		}
		if summary, ok := nodeMap[ref]; ok {
			return summary
		}
		summary := &nodePortalSummary{Ref: ref, Label: nodeFriendlyName(ref)}
		nodeMap[ref] = summary
		order = append(order, ref)
		return summary
	}
	for _, node := range data.Nodes {
		ref := firstNonEmpty(node.NodeID, node.NodeKey)
		summary := addRef(ref)
		summary.Label = nodeDisplayName(data, ref)
		summary.Kind = node.NodeKind
		summary.Role = node.NodeRole
		summary.Runtime = node.RuntimeClass
		summary.Status = node.Status
		summary.Presence = node.PresenceState
		summary.LastSeen = node.LastSeenAt
		summary.LastHeartbeat = node.LastHeartbeatAt
		if node.NodeKey != "" {
			nodeMap[node.NodeKey] = summary
		}
	}
	for _, root := range data.WatchedRoots {
		nodeMapRoot := addRef(root.Root.NodeID)
		nodeMapRoot.WatchedRoots++
		if nodeMapRoot.BackupStatus == "" && root.Root.Status != "" {
			nodeMapRoot.BackupStatus = root.Root.Status
		}
	}
	for _, finding := range data.WatchedRootFindings {
		if nodeSeverityIsActionable(finding.Severity) {
			addRef(finding.NodeID).OpenFindings++
		}
	}
	if data.BackupStatusSelectableCount() > 0 {
		summary := addRef(data.BackupStatus.Root.NodeID)
		summary.BackupStatus = firstNonEmpty(data.BackupStatus.Status, summary.BackupStatus)
		if data.BackupStatus.FailedCount > 0 || data.BackupStatus.SkippedCount > 0 {
			summary.OpenFindings += data.BackupStatus.FailedCount + data.BackupStatus.SkippedCount
		}
	}
	for _, conflict := range data.SyncConflicts {
		addRef(conflict.OriginNodeID).SyncConflicts++
	}
	for _, request := range data.DeletionRequests {
		if nodeDeletionRequestIsActionable(request) {
			addRef(request.OriginNodeID).DeletionRequests++
		}
	}
	for _, backup := range data.PrivateBackups {
		if !nodeStatusIsHealthy(backup.Status) {
			addRef(backup.OriginNodeID).PrivateBackups++
		}
	}
	result := make([]nodePortalSummary, 0, len(order))
	for _, ref := range order {
		result = append(result, *nodeMap[ref])
	}
	return result
}

func watchedRootsByNode(data NodesData) []struct {
	Label string
	Roots []mainwatchedroots.RootStatus
} {
	groups := []struct {
		Label string
		Roots []mainwatchedroots.RootStatus
	}{}
	index := map[string]int{}
	for _, root := range data.WatchedRoots {
		label := nodeDisplayName(data, root.Root.NodeID)
		if idx, ok := index[label]; ok {
			groups[idx].Roots = append(groups[idx].Roots, root)
			continue
		}
		index[label] = len(groups)
		groups = append(groups, struct {
			Label string
			Roots []mainwatchedroots.RootStatus
		}{Label: label, Roots: []mainwatchedroots.RootStatus{root}})
	}
	return groups
}

func watchedRootsByPurpose(data NodesData) []struct {
	Label string
	Roots []mainwatchedroots.RootStatus
} {
	type rootPurpose struct {
		key   string
		label string
	}
	order := []rootPurpose{
		{key: "box", label: "Box"},
		{key: "project_notes", label: "Project Notes"},
		{key: "project_storage", label: "Project Storage"},
		{key: "backups", label: "Backups"},
		{key: "diagnostics", label: "Diagnostics"},
	}
	groups := make([]struct {
		Label string
		Roots []mainwatchedroots.RootStatus
	}, 0, len(order))
	index := map[string]int{}
	for _, purpose := range order {
		index[purpose.key] = len(groups)
		groups = append(groups, struct {
			Label string
			Roots []mainwatchedroots.RootStatus
		}{Label: purpose.label})
	}
	for _, root := range data.WatchedRoots {
		key := watchedRootPurposeKey(root.Root)
		idx := index[key]
		groups[idx].Roots = append(groups[idx].Roots, root)
	}
	result := groups[:0]
	for _, group := range groups {
		if len(group.Roots) > 0 {
			result = append(result, group)
		}
	}
	return result
}

func watchedRootPurposeKey(root mainwatchedroots.WatchedRoot) string {
	value := strings.ToLower(strings.Join([]string{root.RootKey, root.SafeRootKey, root.DisplayName}, " "))
	switch {
	case strings.Contains(value, "loom_box") || strings.Contains(value, "box") || strings.Contains(value, "vault"):
		return "box"
	case strings.Contains(value, "notes"):
		return "project_notes"
	case strings.Contains(value, "project"):
		return "project_storage"
	case strings.Contains(value, "backup"):
		return "backups"
	default:
		return "diagnostics"
	}
}

func watchedRootPurposefulLabel(data NodesData, root mainwatchedroots.WatchedRoot) string {
	return firstNonEmpty(watchedRootFriendlyLabel(root), nodeDisplayName(data, root.NodeID)+" "+titleFromToken(root.RootKey), root.RootKey, "Shared Root")
}

func nodeFindingGroups(findings []mainwatchedroots.Finding) []nodeFindingGroup {
	groups := []nodeFindingGroup{}
	index := map[string]int{}
	for _, finding := range findings {
		key := strings.Join([]string{finding.NodeID, finding.RootKey, finding.Severity, finding.Kind, finding.Summary}, "\x00")
		if idx, ok := index[key]; ok {
			groups[idx].Count++
			if finding.LastSeenAt.After(groups[idx].Latest) {
				groups[idx].Latest = finding.LastSeenAt
				groups[idx].Prototype = finding
			}
			continue
		}
		index[key] = len(groups)
		groups = append(groups, nodeFindingGroup{
			Ref:       firstNonEmpty(finding.WatchedRootFindingID, key),
			NodeRef:   finding.NodeID,
			RootRef:   finding.RootKey,
			Severity:  finding.Severity,
			Kind:      finding.Kind,
			Summary:   finding.Summary,
			Count:     1,
			Latest:    finding.LastSeenAt,
			Prototype: finding,
		})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		left := attentionSeverityRank(groups[i].Severity)
		right := attentionSeverityRank(groups[j].Severity)
		if left != right {
			return left > right
		}
		if !groups[i].Latest.Equal(groups[j].Latest) {
			return groups[i].Latest.After(groups[j].Latest)
		}
		return groups[i].Ref < groups[j].Ref
	})
	return groups
}

func watchedRootFindingCount(findings []mainwatchedroots.Finding, nodeRef, rootKey string) int {
	count := 0
	for _, finding := range findings {
		if finding.NodeID == nodeRef && finding.RootKey == rootKey && nodeSeverityIsActionable(finding.Severity) {
			count++
		}
	}
	return count
}

func nodeDisplayName(data NodesData, ref string) string {
	ref = strings.TrimSpace(ref)
	for _, node := range data.Nodes {
		if ref != "" && (node.NodeID == ref || node.NodeKey == ref) {
			return firstNonEmpty(node.DisplayName, node.NodeKey, nodeFriendlyName(node.NodeID), node.NodeID)
		}
	}
	return nodeFriendlyName(ref)
}

func nodeFriendlyName(ref string) string {
	ref = strings.TrimSpace(ref)
	switch strings.ToLower(ref) {
	case "":
		return "Unknown Node"
	case "node_main", "main", "loom-main":
		return "main"
	case "node_workspace", "workspace", "macbook", "primary-workspace":
		return "macbook"
	case "vps", "loom-vps", "node_vps":
		return "VPS"
	case "cloud", "loom-cloud", "hetzner", "node_cloud":
		return "cloud"
	default:
		return humanizeNodeToken(ref)
	}
}

func humanizeNodeToken(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "node_")
	value = strings.TrimPrefix(value, "node-")
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.ReplaceAll(value, "-", " ")
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "Unknown Node"
	}
	return value
}

func watchedRootFriendlyLabel(root mainwatchedroots.WatchedRoot) string {
	return firstNonEmpty(root.DisplayName, titleFromToken(root.RootKey), root.RootKey, "Watched Root")
}

func nodeHealthLabel(summary nodePortalSummary) string {
	if !nodeStatusIsHealthy(summary.Status) {
		return firstNonEmpty(summary.Status, "unknown")
	}
	if summary.Presence != "" && !nodeStatusIsHealthy(summary.Presence) {
		return summary.Presence
	}
	if summary.OpenFindings > 0 || summary.SyncConflicts > 0 || summary.DeletionRequests > 0 || summary.PrivateBackups > 0 {
		return "attention"
	}
	return firstNonEmpty(summary.Presence, summary.Status, "unknown")
}

func nodeAgentLabel(summary nodePortalSummary) string {
	if summary.LastHeartbeat != nil && !summary.LastHeartbeat.IsZero() {
		return "heartbeat " + shortTimeOrDash(*summary.LastHeartbeat)
	}
	if summary.LastSeen != nil && !summary.LastSeen.IsZero() {
		return "seen " + shortTimeOrDash(*summary.LastSeen)
	}
	if nodeStatusOnline(nodes.Node{Status: summary.Status, PresenceState: summary.Presence}) {
		return "online"
	}
	return "not reported"
}

func nodeStatusOnline(node nodes.Node) bool {
	return nodeStatusIsHealthy(firstNonEmpty(node.PresenceState, node.Status))
}

func nodeNeedsAttention(node nodes.Node) bool {
	return !nodeStatusIsHealthy(firstNonEmpty(node.PresenceState, node.Status))
}

func nodeStatusIsHealthy(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "ok", "healthy", "online", "active", "accepted", "completed", "fresh", "resolved", "running":
		return true
	case "":
		return false
	default:
		return false
	}
}

func nodeSeverityIsActionable(severity string) bool {
	severity = strings.ToLower(strings.TrimSpace(severity))
	switch severity {
	case "error", "failed", "failure", "critical", "danger", "warning", "warn", "attention":
		return true
	default:
		return false
	}
}

func nodeDeletionRequestIsActionable(request loomsync.DeletionRequest) bool {
	return loomsync.DeletionRequestIsActive(request)
}

func shortTimeOrDash(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format("2006-01-02 15:04")
}
