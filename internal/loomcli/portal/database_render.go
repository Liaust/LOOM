package portal

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/loomcli/ui"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/search"
	loomsync "loom.local/loom/internal/sync"
)

func renderDatabase(builder *strings.Builder, state ScreenState) {
	data := state.Data.Database
	records := ScreenRecordItems(state)
	row := renderTopAvailableActions(builder, state, records)
	renderOperationalActionGroups(builder, state, records, &row)
	userObjects := databaseMeaningfulObjects(data.Objects)
	indexGroups := databaseIndexGroupRows(data)
	indexCounts := databaseIndexCountsForData(data)
	latestIndexed := databaseLatestStatus(data.IndexStatuses, func(status search.IndexStatus) bool {
		return databaseIndexStatusIsIndexed(status.Status)
	})
	latestFailure := databaseLatestStatus(data.IndexFailures, func(status search.IndexStatus) bool {
		return true
	})

	renderDatabaseAttention(builder, state, &row)

	renderSummarySection(builder, "Diagnostics Summary")
	renderMetricLine(builder,
		fmt.Sprintf("objects=%d", len(data.Objects)),
		fmt.Sprintf("user_objects=%d", len(userObjects)),
		fmt.Sprintf("indexed=%d", indexCounts.Indexed),
		fmt.Sprintf("queued=%d", indexCounts.Queued),
		fmt.Sprintf("failed=%d", indexCounts.Failed),
		fmt.Sprintf("open_conflicts=%d", len(data.SyncConflicts)),
	)
	renderMetricLine(builder,
		"latest indexed: "+databaseIndexStatusSummary(latestIndexed),
		"latest failure: "+databaseIndexStatusSummary(latestFailure),
	)
	renderEmpty(builder, "Diagnostics for object metadata, text-index state, and sync records. Press s to query indexed object content.")

	renderPrimarySection(builder, "Recent Object Changes")
	if len(userObjects) == 0 {
		renderEmpty(builder, "No objects returned by the backend.")
		if len(data.Objects) > 0 {
			renderEmpty(builder, fmt.Sprintf("%d dev or acceptance object(s) hidden by default.", len(data.Objects)))
		}
	} else {
		objectsToRender := userObjects
		if len(objectsToRender) > 8 {
			objectsToRender = objectsToRender[:8]
		}
		groupByObject := databaseIndexGroupMap(indexGroups)
		for _, object := range objectsToRender {
			renderObjectRow(builder, row, state.SelectedIndex, object, groupByObject[object.ObjectID], state.RawDetails)
			row++
		}
		if len(userObjects) > len(objectsToRender) {
			renderEmpty(builder, fmt.Sprintf("%d more object(s) hidden. Use #database or diagnostics search to narrow.", len(userObjects)-len(objectsToRender)))
		}
	}

	if strings.TrimSpace(data.Search.Query) != "" || data.Search.Status == ScreenLoadLoading || data.Search.Status == ScreenLoadFailed {
		renderDatabaseSearchResults(builder, state, &row)
	}

	renderSummarySection(builder, "Indexed Object Summary")
	if len(indexGroups) == 0 {
		renderEmpty(builder, "No index status returned by the backend.")
	} else {
		limit := len(indexGroups)
		if limit > 6 {
			limit = 6
		}
		for _, group := range indexGroups[:limit] {
			renderIndexGroupRow(builder, group)
		}
		if len(indexGroups) > limit {
			renderEmpty(builder, fmt.Sprintf("%d more indexed object(s) hidden.", len(indexGroups)-limit))
		}
	}

	renderDatabaseDiagnostics(builder, state, &row)
}

func renderDatabaseAttention(builder *strings.Builder, state ScreenState, row *int) {
	data := state.Data.Database
	renderAttentionSection(builder, "Diagnostics Attention")
	wrote := false
	for _, failure := range data.IndexFailures {
		renderIndexStatusRow(builder, *row, state.SelectedIndex, failure, state.RawDetails)
		(*row)++
		wrote = true
	}
	for _, conflict := range data.SyncConflicts {
		fmt.Fprintf(builder, "  %s %s  %s  node=%s  %s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			firstNonEmpty(conflict.SyncConflictID, "-"),
			renderStatus(firstNonEmpty(conflict.Status, "-")),
			firstNonEmpty(conflict.OriginNodeID, "-"),
			trimForWidth(firstNonEmpty(conflict.Summary, conflict.ConflictType, "-"), usableWidth(portalRenderContext().Width, 32)),
		)
		(*row)++
		wrote = true
	}
	for _, request := range data.DeletionRequests {
		if !nodeDeletionRequestIsActionable(request) {
			continue
		}
		fmt.Fprintf(builder, "  %s %s  %s  %s:%s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			firstNonEmpty(request.DeletionRequestID, "-"),
			renderStatus(firstNonEmpty(request.Status, "-")),
			firstNonEmpty(request.TargetKind, "-"),
			firstNonEmpty(request.TargetRef, "-"),
		)
		(*row)++
		wrote = true
	}
	if !wrote {
		renderEmpty(builder, "No failed index work, sync conflicts, or deletion requests need attention.")
	}
}

func renderDatabaseDiagnostics(builder *strings.Builder, state ScreenState, row *int) {
	if !state.RawDetails {
		return
	}
	data := state.Data.Database
	renderDiagnosticsSection(builder, "Diagnostics")
	renderDetailsSection(builder, "Sync Status")
	renderMetricLine(builder,
		fmt.Sprintf("cursors=%d", data.SyncStatus.Summary.CursorCount),
		fmt.Sprintf("recent_batches=%d", data.SyncStatus.Summary.RecentBatchCount),
		fmt.Sprintf("open_conflicts=%d", data.SyncStatus.Summary.OpenConflictCount),
		fmt.Sprintf("replicas=%d", data.SyncStatus.Summary.ReplicaCount),
	)

	renderSection(builder, "Raw Objects")
	if len(data.Objects) == 0 {
		renderEmpty(builder, "No objects returned by the backend.")
	} else {
		for _, object := range data.Objects {
			renderObjectRow(builder, *row, state.SelectedIndex, object, nil, true)
			(*row)++
		}
	}

	renderSection(builder, "Raw Index Statuses")
	if len(data.IndexStatuses) == 0 {
		renderEmpty(builder, "No recent index status rows returned by the backend.")
	} else {
		for _, status := range data.IndexStatuses {
			renderIndexStatusRow(builder, *row, state.SelectedIndex, status, true)
			(*row)++
		}
	}

	renderSection(builder, "Raw Index Queue")
	if len(data.IndexQueue) == 0 {
		renderEmpty(builder, "No index queue items returned by the backend.")
	} else {
		for _, item := range data.IndexQueue {
			renderIndexStatusRow(builder, *row, state.SelectedIndex, item, true)
			(*row)++
		}
	}

	renderSection(builder, "Raw Index Failures")
	if len(data.IndexFailures) == 0 {
		renderEmpty(builder, "No index failures returned by the backend.")
	} else {
		for _, failure := range data.IndexFailures {
			renderIndexStatusRow(builder, *row, state.SelectedIndex, failure, true)
			(*row)++
		}
	}

	renderSyncBatchRows(builder, state, row, data.SyncBatches)
	renderSyncConflictRows(builder, state, row, data.SyncConflicts)
	renderSyncReplicaRows(builder, state, row, data.SyncReplicas)
	renderPrivateBackupRows(builder, state, row, data.PrivateBackups)
	renderDeletionRequestRows(builder, state, row, data.DeletionRequests)
}

func renderObjectRow(builder *strings.Builder, row int, selected int, object objects.Object, indexGroup *databaseIndexGroupRow, raw bool) {
	label := firstNonEmpty(object.Name, objectSlug(object), object.ObjectID)
	fmt.Fprintf(builder, "  %s %s  %s  %s  updated=%s\n",
		renderSelectedMarker(row, selected),
		trimForWidth(label, usableWidth(portalRenderContext().Width, 42)),
		renderStatus(firstNonEmpty(object.Status, "-")),
		firstNonEmpty(object.ObjectType, "-"),
		timeOrDash(object.UpdatedAt),
	)
	if indexGroup != nil {
		fmt.Fprintf(builder, "      %s\n", portalRenderContext().Styles.Muted.Render("index "+indexGroup.Summary()))
	}
	if raw {
		fmt.Fprintf(builder, "      object_id=%s scope=%s state=%s owner=%s\n",
			firstNonEmpty(object.ObjectID, "-"),
			stringPtrOrDash(object.HomeScopeID),
			firstNonEmpty(object.StateClass, "-"),
			stringPtrOrDash(object.OwnerActorID),
		)
	}
}

func renderDatabaseSearchResults(builder *strings.Builder, state ScreenState, row *int) {
	searchData := state.Data.Database.Search
	renderSection(builder, "Search Results")
	if searchData.Status == ScreenLoadLoading {
		renderLoading(builder, "Object Diagnostics Search")
		return
	}
	if searchData.Status == ScreenLoadFailed {
		fmt.Fprintf(builder, "  %s %s\n", renderStatus("failed"), firstNonEmpty(searchData.Error, "Search failed."))
		return
	}
	if strings.TrimSpace(searchData.Query) == "" {
		renderEmpty(builder, "No object diagnostics search has been run.")
		return
	}
	fmt.Fprintf(builder, "  query=%q results=%d\n", searchData.Query, searchData.ResultSet.ResultCount)
	if len(searchData.ResultSet.Results) == 0 {
		renderEmpty(builder, "No indexed objects matched the search.")
		return
	}
	for _, result := range searchData.ResultSet.Results {
		renderSearchResultRow(builder, *row, state.SelectedIndex, result, state.RawDetails)
		(*row)++
	}
}

func renderSearchResultRow(builder *strings.Builder, row int, selected int, result search.SearchResult, raw bool) {
	title := firstNonEmpty(result.Title, result.Citation.Label, result.ObjectID, result.SearchDocumentID)
	snippet := compactPortalText(result.Snippet, usableWidth(portalRenderContext().Width, 12))
	fmt.Fprintf(builder, "  %s %s  score=%.4f  object=%s\n",
		renderSelectedMarker(row, selected),
		trimForWidth(title, usableWidth(portalRenderContext().Width, 36)),
		result.RankScore,
		firstNonEmpty(result.ObjectID, "-"),
	)
	if snippet != "" {
		fmt.Fprintf(builder, "      %s\n", portalRenderContext().Styles.Muted.Render(snippet))
	}
	sourceLine := strings.TrimSpace(fmt.Sprintf("source=%s  scope=%s  freshness=%s",
		firstNonEmpty(result.SourceNodeID, "-"),
		firstNonEmpty(result.ScopeID, "-"),
		firstNonEmpty(result.FreshnessState, "-"),
	))
	if sourceLine != "" {
		fmt.Fprintf(builder, "      %s\n", portalRenderContext().Styles.Muted.Render(sourceLine))
	}
	if raw {
		fmt.Fprintf(builder, "      document=%s version=%s chunk=%s indexed=%s\n",
			firstNonEmpty(result.SearchDocumentID, "-"),
			firstNonEmpty(result.ObjectVersionID, "-"),
			firstNonEmpty(result.DocumentChunkID, "-"),
			timeOrDash(result.IndexedAt),
		)
	}
}

type databaseIndexCounts struct {
	Indexed int
	Queued  int
	Failed  int
}

type databaseIndexGroupRow struct {
	ObjectID      string
	Label         string
	Status        string
	Indexed       int
	Queued        int
	Failed        int
	IndexTypes    []string
	LatestAt      time.Time
	LatestFailure string
}

func (row databaseIndexGroupRow) Summary() string {
	parts := []string{
		renderStatus(firstNonEmpty(row.Status, "-")),
		fmt.Sprintf("indexed=%d", row.Indexed),
		fmt.Sprintf("queued=%d", row.Queued),
		fmt.Sprintf("failed=%d", row.Failed),
	}
	if len(row.IndexTypes) > 0 {
		parts = append(parts, "types="+strings.Join(row.IndexTypes, ","))
	}
	if !row.LatestAt.IsZero() {
		parts = append(parts, "updated="+timeOrDash(row.LatestAt))
	}
	if row.LatestFailure != "" {
		parts = append(parts, "latest failure="+row.LatestFailure)
	}
	return strings.Join(parts, "  ")
}

func renderIndexGroupRow(builder *strings.Builder, group databaseIndexGroupRow) {
	fmt.Fprintf(builder, "  %s  %s\n",
		trimForWidth(group.Label, usableWidth(portalRenderContext().Width, 42)),
		group.Summary(),
	)
}

func databaseMeaningfulObjects(values []objects.Object) []objects.Object {
	result := make([]objects.Object, 0, len(values))
	for _, object := range values {
		if databaseObjectIsDevAcceptance(object) {
			continue
		}
		result = append(result, object)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result
}

func databaseObjectIsDevAcceptance(object objects.Object) bool {
	return databaseRecordLooksDevAcceptance(
		object.ObjectID,
		object.Name,
		objectSlug(object),
		string(object.Metadata),
	)
}

func databaseRecordLooksDevAcceptance(fields ...string) bool {
	joined := strings.ToLower(strings.Join(fields, " "))
	return strings.Contains(joined, ".loom-acceptance") ||
		strings.Contains(joined, "loom-acceptance") ||
		strings.Contains(joined, "loom acceptance")
}

func databaseIndexCountsForData(data DatabaseData) databaseIndexCounts {
	var counts databaseIndexCounts
	for _, status := range data.IndexStatuses {
		if databaseIndexStatusIsIndexed(status.Status) {
			counts.Indexed++
		}
	}
	counts.Queued = len(data.IndexQueue)
	counts.Failed = len(data.IndexFailures)
	return counts
}

func databaseIndexGroupRows(data DatabaseData) []databaseIndexGroupRow {
	objectLabels := map[string]string{}
	hiddenObjects := map[string]bool{}
	for _, object := range data.Objects {
		objectLabels[object.ObjectID] = firstNonEmpty(object.Name, objectSlug(object), object.ObjectID)
		hiddenObjects[object.ObjectID] = databaseObjectIsDevAcceptance(object)
	}
	groups := map[string]*databaseIndexGroupRow{}
	add := func(status search.IndexStatus) {
		key := firstNonEmpty(status.ObjectID, status.SourceID, status.IndexStatusID)
		if key == "" || hiddenObjects[key] || databaseRecordLooksDevAcceptance(status.ObjectID, status.SourceID, status.IndexStatusID, string(status.Metadata)) {
			return
		}
		group := groups[key]
		if group == nil {
			group = &databaseIndexGroupRow{
				ObjectID: status.ObjectID,
				Label:    firstNonEmpty(objectLabels[status.ObjectID], status.ObjectID, status.SourceID, status.IndexStatusID),
				Status:   "unknown",
			}
			groups[key] = group
		}
		if status.ObjectID != "" {
			group.ObjectID = status.ObjectID
		}
		if status.IndexType != "" && !stringSliceContains(group.IndexTypes, status.IndexType) {
			group.IndexTypes = append(group.IndexTypes, status.IndexType)
			sort.Strings(group.IndexTypes)
		}
		switch {
		case databaseIndexStatusIsFailed(status.Status):
			group.Failed++
			group.Status = "failed"
			group.LatestFailure = firstNonEmpty(status.LastErrorCode, status.LastErrorMessage, "failed")
		case databaseIndexStatusIsQueued(status.Status):
			group.Queued++
			if group.Status != "failed" {
				group.Status = "queued"
			}
		case databaseIndexStatusIsIndexed(status.Status):
			group.Indexed++
			if group.Status == "unknown" {
				group.Status = "indexed"
			}
		default:
			if group.Status == "unknown" {
				group.Status = firstNonEmpty(status.Status, "unknown")
			}
		}
		if at := databaseIndexStatusUpdatedAt(status); at.After(group.LatestAt) {
			group.LatestAt = at
		}
	}
	for _, status := range data.IndexStatuses {
		add(status)
	}
	for _, status := range data.IndexQueue {
		add(status)
	}
	for _, status := range data.IndexFailures {
		add(status)
	}
	rows := make([]databaseIndexGroupRow, 0, len(groups))
	for _, group := range groups {
		rows = append(rows, *group)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		leftRank := databaseIndexStatusRank(rows[i].Status)
		rightRank := databaseIndexStatusRank(rows[j].Status)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if !rows[i].LatestAt.Equal(rows[j].LatestAt) {
			return rows[i].LatestAt.After(rows[j].LatestAt)
		}
		return rows[i].Label < rows[j].Label
	})
	return rows
}

func databaseIndexGroupMap(rows []databaseIndexGroupRow) map[string]*databaseIndexGroupRow {
	result := map[string]*databaseIndexGroupRow{}
	for idx := range rows {
		if rows[idx].ObjectID == "" {
			continue
		}
		result[rows[idx].ObjectID] = &rows[idx]
	}
	return result
}

func databaseLatestStatus(statuses []search.IndexStatus, accept func(search.IndexStatus) bool) search.IndexStatus {
	var latest search.IndexStatus
	var latestAt time.Time
	for _, status := range statuses {
		if accept != nil && !accept(status) {
			continue
		}
		at := databaseIndexStatusUpdatedAt(status)
		if at.After(latestAt) {
			latestAt = at
			latest = status
		}
	}
	return latest
}

func databaseIndexStatusSummary(status search.IndexStatus) string {
	if status.IndexStatusID == "" && status.ObjectID == "" && status.Status == "" {
		return "-"
	}
	ref := firstNonEmpty(status.ObjectID, status.SourceID, status.IndexStatusID)
	return fmt.Sprintf("%s %s %s", firstNonEmpty(ref, "-"), renderStatus(firstNonEmpty(status.Status, "-")), databaseIndexStatusUpdatedAt(status).UTC().Format("2006-01-02 15:04"))
}

func databaseIndexStatusUpdatedAt(status search.IndexStatus) time.Time {
	for _, value := range []*time.Time{status.CompletedAt, status.FailedAt, status.StartedAt, status.QueuedAt, status.LastAttemptAt} {
		if value != nil && !value.IsZero() {
			return *value
		}
	}
	if !status.UpdatedAt.IsZero() {
		return status.UpdatedAt
	}
	return status.CreatedAt
}

func databaseIndexStatusRank(status string) int {
	switch {
	case databaseIndexStatusIsFailed(status):
		return 0
	case databaseIndexStatusIsQueued(status):
		return 1
	case databaseIndexStatusIsIndexed(status):
		return 2
	default:
		return 3
	}
}

func databaseIndexStatusIsIndexed(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	return status == "indexed" || status == "completed" || status == "succeeded" || status == "success"
}

func databaseIndexStatusIsQueued(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	return status == "queued" || status == "pending" || status == "claimed" || status == "running" || status == "processing"
}

func databaseIndexStatusIsFailed(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	return status == "failed" || status == "error" || status == "manual_action_required"
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func renderSyncBatchRows(builder *strings.Builder, state ScreenState, row *int, batches []loomsync.SyncBatch) {
	renderSection(builder, "Sync Batches")
	if len(batches) == 0 {
		renderEmpty(builder, "No sync batches returned by the backend.")
		return
	}
	for _, batch := range batches {
		fmt.Fprintf(builder, "  %s %s  %s  node=%s  items=%d conflicts=%d failed=%d\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			firstNonEmpty(batch.SyncBatchID, "-"),
			renderStatus(firstNonEmpty(batch.Status, "-")),
			firstNonEmpty(batch.OriginNodeID, "-"),
			batch.ItemCount,
			batch.ConflictCount,
			batch.FailedCount,
		)
		if state.RawDetails {
			fmt.Fprintf(builder, "      kind=%s received=%s completed=%s idempotency=%s\n",
				firstNonEmpty(batch.BatchKind, "-"),
				timeOrDash(batch.ReceivedAt),
				timePtrOrDash(batch.CompletedAt),
				stringPtrOrDash(batch.IdempotencyKey),
			)
		}
		(*row)++
	}
}

func renderSyncConflictRows(builder *strings.Builder, state ScreenState, row *int, conflicts []loomsync.SyncConflict) {
	renderSection(builder, "Open Sync Conflicts")
	if len(conflicts) == 0 {
		renderEmpty(builder, "No open sync conflicts returned by the backend.")
		return
	}
	for _, conflict := range conflicts {
		fmt.Fprintf(builder, "  %s %s  %s  node=%s  %s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			firstNonEmpty(conflict.SyncConflictID, "-"),
			renderStatus(firstNonEmpty(conflict.Status, "-")),
			firstNonEmpty(conflict.OriginNodeID, "-"),
			trimForWidth(firstNonEmpty(conflict.Summary, conflict.ConflictType, "-"), usableWidth(portalRenderContext().Width, 32)),
		)
		if state.RawDetails {
			fmt.Fprintf(builder, "      local_ref=%s batch=%s created=%s resolved=%s\n",
				firstNonEmpty(conflict.LocalRef, "-"),
				stringPtrOrDash(conflict.SyncBatchID),
				timeOrDash(conflict.CreatedAt),
				timePtrOrDash(conflict.ResolvedAt),
			)
		}
		(*row)++
	}
}

func renderSyncReplicaRows(builder *strings.Builder, state ScreenState, row *int, replicas []loomsync.SyncReplica) {
	renderSection(builder, "Sync Replicas")
	if len(replicas) == 0 {
		renderEmpty(builder, "No sync replicas returned by the backend.")
		return
	}
	for _, replica := range replicas {
		fmt.Fprintf(builder, "  %s %s  %s  source=%s replica=%s kind=%s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			firstNonEmpty(replica.ReplicaID, "-"),
			renderStatus(firstNonEmpty(replica.FreshnessState, "-")),
			firstNonEmpty(replica.SourceNodeID, "-"),
			firstNonEmpty(replica.ReplicaNodeID, "-"),
			firstNonEmpty(replica.ReplicatedKind, "-"),
		)
		if state.RawDetails {
			fmt.Fprintf(builder, "      replicated_id=%s mode=%s storage=%s verified=%s\n",
				firstNonEmpty(replica.ReplicatedID, "-"),
				firstNonEmpty(replica.ReplicaMode, "-"),
				firstNonEmpty(replica.StorageRef, "-"),
				timePtrOrDash(replica.LastVerifiedAt),
			)
		}
		(*row)++
	}
}

func renderPrivateBackupRows(builder *strings.Builder, state ScreenState, row *int, backups []loomsync.PrivateBackupOperation) {
	renderSection(builder, "Private Backups")
	if len(backups) == 0 {
		renderEmpty(builder, "No private backup operations returned by the backend.")
		return
	}
	for _, backup := range backups {
		fmt.Fprintf(builder, "  %s %s  %s  node=%s  bytes=%d\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			firstNonEmpty(backup.PrivateBackupOperationID, "-"),
			renderStatus(firstNonEmpty(backup.Status, "-")),
			firstNonEmpty(backup.OriginNodeID, "-"),
			backup.CoarseSizeBytes,
		)
		if state.RawDetails {
			fmt.Fprintf(builder, "      storage=%s started=%s completed=%s error=%s\n",
				firstNonEmpty(backup.StorageRef, "-"),
				timeOrDash(backup.StartedAt),
				timePtrOrDash(backup.CompletedAt),
				firstNonEmpty(backup.ErrorCode, backup.ErrorMessage, "-"),
			)
		}
		(*row)++
	}
}

func renderDeletionRequestRows(builder *strings.Builder, state ScreenState, row *int, requests []loomsync.DeletionRequest) {
	renderSection(builder, "Deletion Requests")
	if len(requests) == 0 {
		renderEmpty(builder, "No deletion requests returned by the backend.")
		return
	}
	for _, request := range requests {
		fmt.Fprintf(builder, "  %s %s  %s  %s:%s\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			firstNonEmpty(request.DeletionRequestID, "-"),
			renderStatus(firstNonEmpty(request.Status, "-")),
			firstNonEmpty(request.TargetKind, "-"),
			firstNonEmpty(request.TargetRef, "-"),
		)
		if state.RawDetails {
			fmt.Fprintf(builder, "      node=%s action=%s requested=%s reviewed=%s reason=%s\n",
				firstNonEmpty(request.OriginNodeID, "-"),
				firstNonEmpty(request.RequestedAction, "-"),
				timeOrDash(request.RequestedAt),
				timePtrOrDash(request.ReviewedAt),
				firstNonEmpty(request.Reason, "-"),
			)
		}
		(*row)++
	}
}

func RenderDatabaseSearchWithSelection(mode ui.Mode, query string, data DatabaseSearchData, selected int, width int, height int) string {
	ctx := renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: width, Height: height, Logo: SelectPortalLogo(width, nil)}
	restore := setActiveRenderContext(ctx)
	defer restore()

	var builder strings.Builder
	renderTitle(&builder, mode, "Object Diagnostics Search")
	renderDatabaseSearchBox(&builder, query, width)
	renderSection(&builder, "Search Results")
	switch data.Status {
	case ScreenLoadLoading:
		renderLoading(&builder, "Object Diagnostics Search")
	case ScreenLoadFailed:
		fmt.Fprintf(&builder, "  %s %s\n", renderStatus("failed"), firstNonEmpty(data.Error, "Search failed."))
	default:
		if strings.TrimSpace(data.Query) == "" {
			renderEmpty(&builder, "Type a query and press enter.")
		} else if len(data.ResultSet.Results) == 0 {
			fmt.Fprintf(&builder, "  query=%q results=0\n", data.Query)
			renderEmpty(&builder, "No indexed objects matched the search.")
		} else {
			fmt.Fprintf(&builder, "  query=%q results=%d\n", data.Query, data.ResultSet.ResultCount)
			for idx, result := range data.ResultSet.Results {
				renderSearchResultRow(&builder, idx, selected, result, false)
			}
		}
	}
	renderFooterFor(&builder, footerDBSearch)
	return builder.String()
}

func renderDatabaseSearchBox(builder *strings.Builder, query string, width int) {
	if width <= 0 {
		width = 72
	}
	boxWidth := width
	if boxWidth > 80 {
		boxWidth = 80
	}
	if boxWidth < 32 {
		boxWidth = 32
	}
	innerWidth := boxWidth - 4
	label := " Object Diagnostics Search "
	topFill := innerWidth - len(label)
	if topFill < 0 {
		topFill = 0
	}
	top := "+" + label + strings.Repeat("-", topFill) + "+"
	searchValue := strings.TrimSpace(query)
	if searchValue == "" {
		searchValue = portalRenderContext().Styles.Muted.Render("query indexed object content")
	}
	line := " Query  " + searchValue
	line = trimForWidth(line, innerWidth)
	padding := innerWidth - len(stripANSI(line))
	if padding < 0 {
		padding = 0
	}
	bottom := "+" + strings.Repeat("-", innerWidth) + "+"
	border := portalRenderContext().Styles.BorderFocus
	builder.WriteString(border.Render(top))
	builder.WriteByte('\n')
	builder.WriteString(border.Render("| "))
	builder.WriteString(line)
	builder.WriteString(strings.Repeat(" ", padding))
	builder.WriteString(border.Render(" |"))
	builder.WriteByte('\n')
	builder.WriteString(border.Render(bottom))
	builder.WriteString("\n\n")
}

func compactPortalText(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func int64PtrOrDash(value *int64) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *value)
}

func objectSlug(object objects.Object) string {
	if object.Slug == nil {
		return ""
	}
	return *object.Slug
}
