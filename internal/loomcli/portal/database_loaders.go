package portal

import (
	"context"

	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/search"
	loomsync "loom.local/loom/internal/sync"
	"loom.local/loom/internal/workers"
)

func loadDatabaseScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot) ScreenLoadResult {
	builder := newScreenLoadBuilder(ScreenDatabase, fallback)
	data := DatabaseData{}

	if envelope, err := client.ListObjects(ctx, correlationID, objects.ListFilter{Limit: 25}); err != nil {
		builder.addPartial("database_objects", err)
	} else {
		data.Objects = envelope.Data
	}
	if envelope, err := client.ListIndexStatus(ctx, correlationID, search.StatusFilter{Limit: 20}); err != nil {
		builder.addPartial("database_index_status", err)
	} else {
		data.IndexStatuses = envelope.Data
	}
	if envelope, err := client.ListIndexQueue(ctx, correlationID, search.IndexQueueFilter{Limit: 20, IncludeActive: true}); err != nil {
		builder.addPartial("database_index_queue", err)
	} else {
		data.IndexQueue = envelope.Data
	}
	if envelope, err := client.ListIndexFailures(ctx, correlationID, search.StatusFilter{Limit: 20}); err != nil {
		builder.addPartial("database_index_failures", err)
	} else {
		data.IndexFailures = envelope.Data
	}
	if envelope, err := client.GetSyncStatus(ctx, correlationID, portalMainSyncStatusFilter()); err != nil {
		builder.addPartial("database_sync_status", err)
	} else {
		data.SyncStatus = envelope.Data
	}
	if envelope, err := client.ListSyncBatches(ctx, correlationID, loomsync.ListFilter{Limit: 20}); err != nil {
		builder.addPartial("database_sync_batches", err)
	} else {
		data.SyncBatches = envelope.Data
	}
	if envelope, err := client.ListSyncConflicts(ctx, correlationID, loomsync.ListFilter{Status: "open", Limit: 20}); err != nil {
		builder.addPartial("database_sync_conflicts", err)
	} else {
		data.SyncConflicts = envelope.Data
	}
	if envelope, err := client.ListSyncReplicas(ctx, correlationID, loomsync.ListFilter{Limit: 20}); err != nil {
		builder.addPartial("database_sync_replicas", err)
	} else {
		data.SyncReplicas = envelope.Data
	}
	if envelope, err := client.ListPrivateBackups(ctx, correlationID, loomsync.ListFilter{Limit: 20}); err != nil {
		builder.addPartial("database_private_backups", err)
	} else {
		data.PrivateBackups = envelope.Data
	}
	if envelope, err := client.ListDeletionRequests(ctx, correlationID, loomsync.ListFilter{ActiveOnly: true, Limit: 20}); err != nil {
		builder.addPartial("database_deletion_requests", err)
	} else {
		data.DeletionRequests = envelope.Data
	}
	if envelope, err := client.ListWorkers(ctx, correlationID, workers.WorkerFilter{Limit: 100}); err != nil {
		builder.addPartial("database_workers", err)
	} else {
		data.Workers = envelope.Data
	}

	builder.data.Database = data
	return builder.result()
}

func portalMainSyncStatusFilter() loomsync.ListFilter {
	return loomsync.ListFilter{NodeRef: "main", Limit: 20}
}
