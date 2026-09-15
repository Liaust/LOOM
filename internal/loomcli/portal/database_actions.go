package portal

import (
	"fmt"
	"strings"

	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/search"
	loomsync "loom.local/loom/internal/sync"
)

func databaseSelectableItems(state ScreenState) []SelectableItem {
	if state.RawDetails {
		return withOperationalActionGroups(state, databaseDetailedSelectableItems(state))
	}
	return withOperationalActionGroupsFromCandidates(state, databaseDefaultSelectableItems(state), databaseDetailedSelectableItems(state))
}

func databaseDefaultSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Database
	items := []SelectableItem{}
	row := 0

	addIndexStatusItems := func(kind string, statuses []search.IndexStatus) {
		for _, status := range statuses {
			if databaseRecordLooksDevAcceptance(status.ObjectID, status.SourceID, status.IndexStatusID, string(status.Metadata)) {
				continue
			}
			inspect := databaseScopedAction(NewIndexInspectAction(status, ScreenDatabase))
			items = append(items, SelectableItem{
				Kind:           SelectableKindRecord,
				Label:          firstNonEmpty(status.IndexStatusID, status.ObjectID),
				Screen:         ScreenDatabase,
				RowIndex:       row,
				RecordKind:     kind,
				RecordRef:      firstNonEmpty(status.IndexStatusID, status.ObjectID),
				RecordLabel:    firstNonEmpty(status.IndexStatusID, status.ObjectID),
				PrimaryAction:  actionPtr(inspect),
				RelatedActions: databaseScopedActions(indexRelatedActions(status)),
			})
			row++
		}
	}
	addIndexStatusItems("index_failure", data.IndexFailures)
	for _, conflict := range data.SyncConflicts {
		addDatabaseRecord(&items, &row, "sync_conflict", conflict.SyncConflictID, firstNonEmpty(conflict.Summary, conflict.SyncConflictID), syncConflictPayload(conflict))
	}
	for _, request := range data.DeletionRequests {
		if !nodeDeletionRequestIsActionable(request) {
			continue
		}
		addDatabaseRecordWithActions(&items, &row, "deletion_request", request.DeletionRequestID, firstNonEmpty(request.TargetRef, request.DeletionRequestID), deletionRequestPayload(request), databaseScopedActions(deletionRequestRelatedActions(request, ScreenDatabase)))
	}

	for _, object := range databaseMeaningfulObjects(data.Objects) {
		addDatabaseObjectSelectable(&items, &row, object)
	}

	if data.Search.Status == ScreenLoadLoaded && data.Search.Query != "" {
		for _, result := range data.Search.ResultSet.Results {
			if databaseRecordLooksDevAcceptance(result.Title, result.Citation.Label, result.ObjectID, result.SearchDocumentID) {
				continue
			}
			addDatabaseSearchResultSelectable(&items, &row, result)
		}
	}
	return items
}

func databaseDetailedSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Database
	items := []SelectableItem{}
	row := 0

	for _, object := range data.Objects {
		addDatabaseObjectSelectable(&items, &row, object)
	}

	if data.Search.Status == ScreenLoadLoaded && data.Search.Query != "" {
		for _, result := range data.Search.ResultSet.Results {
			addDatabaseSearchResultSelectable(&items, &row, result)
		}
	}

	addIndexStatusItems := func(kind string, statuses []search.IndexStatus) {
		for _, status := range statuses {
			inspect := databaseScopedAction(NewIndexInspectAction(status, ScreenDatabase))
			items = append(items, SelectableItem{
				Kind:           SelectableKindRecord,
				Label:          firstNonEmpty(status.IndexStatusID, status.ObjectID),
				Screen:         ScreenDatabase,
				RowIndex:       row,
				RecordKind:     kind,
				RecordRef:      firstNonEmpty(status.IndexStatusID, status.ObjectID),
				RecordLabel:    firstNonEmpty(status.IndexStatusID, status.ObjectID),
				PrimaryAction:  actionPtr(inspect),
				RelatedActions: databaseScopedActions(indexRelatedActions(status)),
			})
			row++
		}
	}
	addIndexStatusItems("index_status", data.IndexStatuses)
	addIndexStatusItems("index_queue", data.IndexQueue)
	addIndexStatusItems("index_failure", data.IndexFailures)

	for _, batch := range data.SyncBatches {
		addDatabaseRecord(&items, &row, "sync_batch", batch.SyncBatchID, batch.SyncBatchID, syncBatchPayload(batch))
	}
	for _, conflict := range data.SyncConflicts {
		addDatabaseRecord(&items, &row, "sync_conflict", conflict.SyncConflictID, firstNonEmpty(conflict.Summary, conflict.SyncConflictID), syncConflictPayload(conflict))
	}
	for _, replica := range data.SyncReplicas {
		addDatabaseRecord(&items, &row, "sync_replica", replica.ReplicaID, replica.ReplicaID, syncReplicaPayload(replica))
	}
	for _, backup := range data.PrivateBackups {
		addDatabaseRecord(&items, &row, "private_backup", backup.PrivateBackupOperationID, backup.PrivateBackupOperationID, privateBackupPayload(backup))
	}
	for _, request := range data.DeletionRequests {
		addDatabaseRecordWithActions(&items, &row, "deletion_request", request.DeletionRequestID, firstNonEmpty(request.TargetRef, request.DeletionRequestID), deletionRequestPayload(request), databaseScopedActions(deletionRequestRelatedActions(request, ScreenDatabase)))
	}
	return items
}

func databaseSearchSelectableItems(state ScreenState, filter string, query string) []SelectableItem {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if state.RawDetails {
		return databaseFilterSelectableItems(databaseDetailedSelectableItems(state), filter)
	}
	if filter == "indexes" {
		return databaseIndexSummarySelectableItems(state)
	}
	items := databaseDefaultSelectableItems(state)
	if strings.TrimSpace(query) == "" {
		return items
	}
	return databaseFilterSelectableItems(items, filter)
}

func databaseFilterSelectableItems(items []SelectableItem, filter string) []SelectableItem {
	if filter == "" {
		return items
	}
	result := make([]SelectableItem, 0, len(items))
	for _, item := range items {
		if selectableMatchesSearchFilter(item, filter) {
			result = append(result, item)
		}
	}
	return result
}

func NewDatabaseDiagnosticsInspectAction(data DatabaseData) PortalAction {
	indexCounts := databaseIndexCountsForData(data)
	payload := map[string]string{
		"objects":           fmt.Sprintf("%d", len(data.Objects)),
		"index_statuses":    fmt.Sprintf("%d", len(data.IndexStatuses)),
		"index_queued":      fmt.Sprintf("%d", indexCounts.Queued),
		"index_failed":      fmt.Sprintf("%d", indexCounts.Failed),
		"index_indexed":     fmt.Sprintf("%d", indexCounts.Indexed),
		"sync_batches":      fmt.Sprintf("%d", len(data.SyncBatches)),
		"sync_conflicts":    fmt.Sprintf("%d", len(data.SyncConflicts)),
		"sync_replicas":     fmt.Sprintf("%d", len(data.SyncReplicas)),
		"private_backups":   fmt.Sprintf("%d", len(data.PrivateBackups)),
		"deletion_requests": fmt.Sprintf("%d", len(data.DeletionRequests)),
		"workers":           fmt.Sprintf("%d", len(data.Workers)),
	}
	action := NewPortalRecordInspectAction("database", ScreenDatabase, "diagnostics_summary", "object_store_diagnostics", "Object Store Diagnostics", payload)
	action.ID = "database.diagnostics_summary.inspect"
	action.Label = "Inspect Diagnostics Summary"
	action.Description = "Inspect aggregate object-store diagnostics counts."
	action.TargetKind = "diagnostics_summary"
	action.RefreshScreen = ScreenDatabase
	return action
}

func databaseIndexSummarySelectableItems(state ScreenState) []SelectableItem {
	groups := databaseIndexGroupRows(state.Data.Database)
	items := make([]SelectableItem, 0, len(groups))
	for idx, group := range groups {
		action := NewDatabaseIndexSummaryInspectAction(group)
		item := SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         group.Label,
			Description:   group.Summary(),
			Screen:        ScreenDatabase,
			RowIndex:      idx,
			RecordKind:    "index_summary",
			RecordRef:     firstNonEmpty(group.ObjectID, group.Label),
			RecordLabel:   group.Label,
			PrimaryAction: actionPtr(action),
		}
		if group.ObjectID != "" {
			item.RelatedActions = []PortalAction{
				NewObjectExplainIndexAction(objects.Object{ObjectID: group.ObjectID, Name: group.Label}),
				NewObjectRebuildIndexAction(objects.Object{ObjectID: group.ObjectID, Name: group.Label}),
			}
		}
		items = append(items, item)
	}
	return items
}

func addDatabaseObjectSelectable(items *[]SelectableItem, row *int, object objects.Object) {
	inspect := NewObjectInspectAction(object)
	*items = append(*items, SelectableItem{
		Kind:           SelectableKindRecord,
		Label:          firstNonEmpty(object.Name, objectSlug(object), object.ObjectID),
		Screen:         ScreenDatabase,
		RowIndex:       *row,
		RecordKind:     "object",
		RecordRef:      object.ObjectID,
		RecordLabel:    firstNonEmpty(object.Name, object.ObjectID),
		PrimaryAction:  actionPtr(inspect),
		RelatedActions: []PortalAction{NewObjectExplainIndexAction(object), NewObjectRebuildIndexAction(object)},
	})
	(*row)++
}

func addDatabaseSearchResultSelectable(items *[]SelectableItem, row *int, result search.SearchResult) {
	inspect := NewSearchResultInspectAction(result)
	*items = append(*items, SelectableItem{
		Kind:           SelectableKindRecord,
		Label:          firstNonEmpty(result.Title, result.Citation.Label, result.ObjectID),
		Screen:         ScreenDatabase,
		RowIndex:       *row,
		RecordKind:     "search_result",
		RecordRef:      firstNonEmpty(result.ObjectID, result.SearchDocumentID),
		RecordLabel:    firstNonEmpty(result.Title, result.Citation.Label, result.ObjectID),
		PrimaryAction:  actionPtr(inspect),
		RelatedActions: []PortalAction{NewSearchResultExplainIndexAction(result), NewSearchResultRebuildIndexAction(result)},
	})
	(*row)++
}

func addDatabaseRecord(items *[]SelectableItem, row *int, kind, ref, label string, payload map[string]string) {
	addDatabaseRecordWithActions(items, row, kind, ref, label, payload, nil)
}

func addDatabaseRecordWithActions(items *[]SelectableItem, row *int, kind, ref, label string, payload map[string]string, related []PortalAction) {
	inspect := NewDatabaseRecordInspectAction(kind, ref, label, payload)
	*items = append(*items, SelectableItem{
		Kind:           SelectableKindRecord,
		Label:          label,
		Screen:         ScreenDatabase,
		RowIndex:       *row,
		RecordKind:     kind,
		RecordRef:      ref,
		RecordLabel:    label,
		PrimaryAction:  actionPtr(inspect),
		RelatedActions: related,
	})
	(*row)++
}

func NewObjectInspectAction(object objects.Object) PortalAction {
	ref := object.ObjectID
	label := firstNonEmpty(object.Name, objectSlug(object), ref)
	action := PortalAction{
		ID:            fmt.Sprintf("database.object.%s.inspect", safeActionID(firstNonEmpty(ref, label))),
		Label:         "Open Object",
		Description:   "Inspect object metadata, latest version, locations, and index state.",
		Domain:        "database",
		SourceScreen:  ScreenDatabase,
		TargetKind:    "object",
		TargetRef:     ref,
		TargetLabel:   label,
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorObjectInspect, Target: ref, Payload: objectPayload(object)},
		RawCommand:    []string{"loom", "objects", "inspect", ref},
		RawDetails:    objectPayload(object),
		RefreshScreen: ScreenDatabase,
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This object row does not include an object ID."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewObjectExplainIndexAction(object objects.Object) PortalAction {
	ref := object.ObjectID
	label := firstNonEmpty(object.Name, ref)
	action := PortalAction{
		ID:            fmt.Sprintf("database.object.%s.explain_index", safeActionID(firstNonEmpty(ref, label))),
		Label:         "Explain Object Index",
		Description:   "Inspect current index state for this object.",
		Domain:        "database",
		SourceScreen:  ScreenDatabase,
		TargetKind:    "object",
		TargetRef:     ref,
		TargetLabel:   label,
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorIndexExplainObject, Target: ref, Payload: map[string]string{"object_id": ref}},
		RawCommand:    []string{"loom", "indexes", "explain", "object", ref},
		RawDetails:    map[string]string{"object_id": ref},
		RefreshScreen: ScreenDatabase,
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This object row does not include an object ID."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewObjectRebuildIndexAction(object objects.Object) PortalAction {
	ref := object.ObjectID
	label := firstNonEmpty(object.Name, ref)
	action := PortalAction{
		ID:            fmt.Sprintf("database.object.%s.rebuild_index", safeActionID(firstNonEmpty(ref, label))),
		Label:         "Rebuild Object Index",
		Description:   "Queue fresh text index work for this object.",
		Domain:        "database",
		SourceScreen:  ScreenDatabase,
		TargetKind:    "object",
		TargetRef:     ref,
		TargetLabel:   label,
		Risk:          ActionRiskSafeRun,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorIndexRebuildObject, Target: ref, Payload: map[string]string{"object_id": ref}},
		RawCommand:    []string{"loom", "indexes", "rebuild", "object", ref},
		RawDetails:    map[string]string{"object_id": ref},
		RefreshScreen: ScreenDatabase,
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This object row does not include an object ID."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewSearchResultInspectAction(result search.SearchResult) PortalAction {
	ref := firstNonEmpty(result.ObjectID, result.SearchDocumentID)
	label := firstNonEmpty(result.Title, result.Citation.Label, ref)
	idRef := firstNonEmpty(result.SearchDocumentID, ref, label)
	action := PortalAction{
		ID:            fmt.Sprintf("database.search_result.%s.inspect", safeActionID(idRef)),
		Label:         "Open Search Result",
		Description:   "Inspect the object that produced this search result.",
		Domain:        "database",
		SourceScreen:  ScreenDatabase,
		TargetKind:    "object",
		TargetRef:     result.ObjectID,
		TargetLabel:   label,
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorObjectInspect, Target: result.ObjectID, Payload: searchResultPayload(result)},
		RawCommand:    []string{"loom", "objects", "inspect", result.ObjectID},
		RawDetails:    searchResultPayload(result),
		RefreshScreen: ScreenDatabase,
	}
	if result.ObjectID == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This search result does not include an object ID."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewSearchResultExplainIndexAction(result search.SearchResult) PortalAction {
	return NewObjectExplainIndexAction(objects.Object{ObjectID: result.ObjectID, Name: firstNonEmpty(result.Title, result.Citation.Label)})
}

func NewSearchResultRebuildIndexAction(result search.SearchResult) PortalAction {
	return NewObjectRebuildIndexAction(objects.Object{ObjectID: result.ObjectID, Name: firstNonEmpty(result.Title, result.Citation.Label)})
}

func NewDatabaseRecordInspectAction(kind, ref, label string, payload map[string]string) PortalAction {
	action := PortalAction{
		ID:            fmt.Sprintf("database.%s.%s.inspect", safeActionID(kind), safeActionID(firstNonEmpty(ref, label))),
		Label:         "Inspect " + titleFromToken(kind),
		Description:   "Inspect this database record.",
		Domain:        "database",
		SourceScreen:  ScreenDatabase,
		TargetKind:    kind,
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(label, ref),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorRecordInspect, Target: ref, Payload: payload},
		RawDetails:    payload,
		RefreshScreen: ScreenDatabase,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewDatabaseIndexSummaryInspectAction(group databaseIndexGroupRow) PortalAction {
	payload := map[string]string{
		"object_id":      firstNonEmpty(group.ObjectID, "-"),
		"label":          group.Label,
		"status":         group.Status,
		"indexed":        fmt.Sprintf("%d", group.Indexed),
		"queued":         fmt.Sprintf("%d", group.Queued),
		"failed":         fmt.Sprintf("%d", group.Failed),
		"index_types":    strings.Join(group.IndexTypes, ","),
		"latest_update":  timeOrDash(group.LatestAt),
		"latest_failure": group.LatestFailure,
	}
	action := NewPortalRecordInspectAction("database", ScreenDatabase, "index_summary", firstNonEmpty(group.ObjectID, group.Label), group.Label, payload)
	action.ID = "database.index_summary." + safeActionID(firstNonEmpty(group.ObjectID, group.Label)) + ".inspect"
	action.Label = "Inspect Index Summary"
	action.Description = "Inspect grouped index health for this object."
	action.TargetKind = "index_summary"
	action.RefreshScreen = ScreenDatabase
	return action
}

func databaseScopedActions(actions []PortalAction) []PortalAction {
	result := make([]PortalAction, 0, len(actions))
	for _, action := range actions {
		result = append(result, databaseScopedAction(action))
	}
	return result
}

func databaseScopedAction(action PortalAction) PortalAction {
	action.SourceScreen = ScreenDatabase
	action.RefreshScreen = ScreenDatabase
	return action
}

func objectPayload(object objects.Object) map[string]string {
	return map[string]string{
		"object_id":   object.ObjectID,
		"object_type": object.ObjectType,
		"name":        object.Name,
		"slug":        objectSlug(object),
		"status":      object.Status,
		"scope":       stringPtrOrDash(object.HomeScopeID),
		"state":       object.StateClass,
		"updated_at":  timeOrDash(object.UpdatedAt),
	}
}

func searchResultPayload(result search.SearchResult) map[string]string {
	return map[string]string{
		"search_document_id": result.SearchDocumentID,
		"object_id":          result.ObjectID,
		"object_version_id":  result.ObjectVersionID,
		"document_chunk_id":  result.DocumentChunkID,
		"title":              result.Title,
		"snippet":            result.Snippet,
		"score":              fmt.Sprintf("%.4f", result.RankScore),
		"indexed_at":         timeOrDash(result.IndexedAt),
	}
}

func syncBatchPayload(batch loomsync.SyncBatch) map[string]string {
	return map[string]string{
		"sync_batch_id":   batch.SyncBatchID,
		"origin_node":     batch.OriginNodeID,
		"kind":            batch.BatchKind,
		"status":          batch.Status,
		"items":           fmt.Sprintf("%d", batch.ItemCount),
		"accepted":        fmt.Sprintf("%d", batch.AcceptedCount),
		"conflicts":       fmt.Sprintf("%d", batch.ConflictCount),
		"failed":          fmt.Sprintf("%d", batch.FailedCount),
		"received_at":     timeOrDash(batch.ReceivedAt),
		"completed_at":    timePtrOrDash(batch.CompletedAt),
		"idempotency_key": stringPtrOrDash(batch.IdempotencyKey),
	}
}

func syncConflictPayload(conflict loomsync.SyncConflict) map[string]string {
	return map[string]string{
		"sync_conflict_id": conflict.SyncConflictID,
		"origin_node":      conflict.OriginNodeID,
		"batch":            stringPtrOrDash(conflict.SyncBatchID),
		"local_ref":        conflict.LocalRef,
		"type":             conflict.ConflictType,
		"status":           conflict.Status,
		"summary":          conflict.Summary,
		"created_at":       timeOrDash(conflict.CreatedAt),
		"resolved_at":      timePtrOrDash(conflict.ResolvedAt),
	}
}

func syncReplicaPayload(replica loomsync.SyncReplica) map[string]string {
	return map[string]string{
		"replica_id":        replica.ReplicaID,
		"kind":              replica.ReplicatedKind,
		"replicated_id":     replica.ReplicatedID,
		"source_node":       replica.SourceNodeID,
		"replica_node":      replica.ReplicaNodeID,
		"mode":              replica.ReplicaMode,
		"freshness":         replica.FreshnessState,
		"storage":           replica.StorageRef,
		"last_verified_at":  timePtrOrDash(replica.LastVerifiedAt),
		"source_cursor_ref": replica.SourceCursorRef,
	}
}

func privateBackupPayload(backup loomsync.PrivateBackupOperation) map[string]string {
	return map[string]string{
		"private_backup_operation_id": backup.PrivateBackupOperationID,
		"origin_node":                 backup.OriginNodeID,
		"status":                      backup.Status,
		"bytes":                       fmt.Sprintf("%d", backup.CoarseSizeBytes),
		"storage":                     backup.StorageRef,
		"started_at":                  timeOrDash(backup.StartedAt),
		"completed_at":                timePtrOrDash(backup.CompletedAt),
		"error":                       firstNonEmpty(backup.ErrorCode, backup.ErrorMessage, "-"),
	}
}

func deletionRequestPayload(request loomsync.DeletionRequest) map[string]string {
	return map[string]string{
		"deletion_request_id": request.DeletionRequestID,
		"origin_node":         request.OriginNodeID,
		"target_kind":         request.TargetKind,
		"target_ref":          request.TargetRef,
		"requested_action":    request.RequestedAction,
		"status":              request.Status,
		"reason":              request.Reason,
		"requested_at":        timeOrDash(request.RequestedAt),
		"reviewed_at":         timePtrOrDash(request.ReviewedAt),
		"reviewed_by":         stringPtrOrDash(request.ReviewedByActorID),
	}
}
