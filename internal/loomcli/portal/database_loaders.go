package portal

import (
	"context"
	"sync"
	"time"

	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/search"
	loomsync "loom.local/loom/internal/sync"
	"loom.local/loom/internal/workers"
)

func loadDatabaseScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot) ScreenLoadResult {
	builder := newScreenLoadBuilder(ScreenDatabase, fallback)
	data := DatabaseData{}

	// These eleven bounded reads share only the correlation/scope; none uses
	// another panel's result. Capture independently, then merge in panel order.
	reads := []databasePanelRead{
		{source: "database_objects", load: func(ctx context.Context) func() {
			envelope, err := client.ListObjects(ctx, correlationID, objects.ListFilter{Limit: 25})
			return func() {
				if err != nil {
					builder.addPartial("database_objects", err)
				} else {
					data.Objects = envelope.Data
				}
			}
		}},
		{source: "database_index_status", load: func(ctx context.Context) func() {
			envelope, err := client.ListIndexStatus(ctx, correlationID, search.StatusFilter{Limit: 20})
			return func() {
				if err != nil {
					builder.addPartial("database_index_status", err)
				} else {
					data.IndexStatuses = envelope.Data
				}
			}
		}},
		{source: "database_index_queue", load: func(ctx context.Context) func() {
			envelope, err := client.ListIndexQueue(ctx, correlationID, search.IndexQueueFilter{Limit: 20, IncludeActive: true})
			return func() {
				if err != nil {
					builder.addPartial("database_index_queue", err)
				} else {
					data.IndexQueue = envelope.Data
				}
			}
		}},
		{source: "database_index_failures", load: func(ctx context.Context) func() {
			envelope, err := client.ListIndexFailures(ctx, correlationID, search.StatusFilter{Limit: 20})
			return func() {
				if err != nil {
					builder.addPartial("database_index_failures", err)
				} else {
					data.IndexFailures = envelope.Data
				}
			}
		}},
		{source: "database_sync_status", load: func(ctx context.Context) func() {
			envelope, err := client.GetSyncStatus(ctx, correlationID, portalMainSyncStatusFilter())
			return func() {
				if err != nil {
					builder.addPartial("database_sync_status", err)
				} else {
					data.SyncStatus = envelope.Data
				}
			}
		}},
		{source: "database_sync_batches", load: func(ctx context.Context) func() {
			envelope, err := client.ListSyncBatches(ctx, correlationID, loomsync.ListFilter{Limit: 20})
			return func() {
				if err != nil {
					builder.addPartial("database_sync_batches", err)
				} else {
					data.SyncBatches = envelope.Data
				}
			}
		}},
		{source: "database_sync_conflicts", load: func(ctx context.Context) func() {
			envelope, err := client.ListSyncConflicts(ctx, correlationID, loomsync.ListFilter{Status: "open", Limit: 20})
			return func() {
				if err != nil {
					builder.addPartial("database_sync_conflicts", err)
				} else {
					data.SyncConflicts = envelope.Data
				}
			}
		}},
		{source: "database_sync_replicas", load: func(ctx context.Context) func() {
			envelope, err := client.ListSyncReplicas(ctx, correlationID, loomsync.ListFilter{Limit: 20})
			return func() {
				if err != nil {
					builder.addPartial("database_sync_replicas", err)
				} else {
					data.SyncReplicas = envelope.Data
				}
			}
		}},
		{source: "database_private_backups", load: func(ctx context.Context) func() {
			envelope, err := client.ListPrivateBackups(ctx, correlationID, loomsync.ListFilter{Limit: 20})
			return func() {
				if err != nil {
					builder.addPartial("database_private_backups", err)
				} else {
					data.PrivateBackups = envelope.Data
				}
			}
		}},
		{source: "database_deletion_requests", load: func(ctx context.Context) func() {
			envelope, err := client.ListDeletionRequests(ctx, correlationID, loomsync.ListFilter{ActiveOnly: true, Limit: 20})
			return func() {
				if err != nil {
					builder.addPartial("database_deletion_requests", err)
				} else {
					data.DeletionRequests = envelope.Data
				}
			}
		}},
		{source: "database_workers", load: func(ctx context.Context) func() {
			envelope, err := client.ListWorkers(ctx, correlationID, workers.WorkerFilter{Limit: 100})
			return func() {
				if err != nil {
					builder.addPartial("database_workers", err)
				} else {
					data.Workers = envelope.Data
				}
			}
		}},
	}
	collectDatabasePanels(ctx, &builder, reads)

	builder.data.Database = data
	return builder.result()
}

func portalMainSyncStatusFilter() loomsync.ListFilter {
	return loomsync.ListFilter{NodeRef: "main", Limit: 20}
}

const databaseReadConcurrency = 4
const databaseReadTimeout = 5 * time.Second

type databasePanelRead struct {
	source string
	load   func(context.Context) func()
}

// Workers own separate result slots. Only the caller merges panel data and
// errors, preserving source order even when the reads finish out of order.
func collectDatabasePanels(ctx context.Context, builder *screenLoadBuilder, reads []databasePanelRead) {
	merges := make([]func(), len(reads))
	jobs := make(chan int, len(reads))
	for i := range reads {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for i := 0; i < databaseReadConcurrency && i < len(reads); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				read := reads[index]
				readCtx, cancel := context.WithTimeout(ctx, databaseReadTimeout)
				if err := readCtx.Err(); err != nil {
					merges[index] = func() { builder.addPartial(read.source, err) }
				} else {
					merges[index] = read.load(readCtx)
				}
				cancel()
			}
		}()
	}
	workers.Wait()
	for _, merge := range merges {
		if merge != nil {
			merge()
		}
	}
}
