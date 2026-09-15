package portal

import (
	"context"

	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storageview"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func loadStorageScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot) ScreenLoadResult {
	builder := newScreenLoadBuilder(ScreenStorage, fallback)
	data := StorageData{}

	if envelope, err := client.ListStorageEntries(ctx, correlationID, storagecatalog.ListFilter{Limit: 250}); err != nil {
		builder.addPartial("storage_entries", err)
	} else {
		data.Entries = envelope.Data
		data.Tree.ViewKey = "catalog"
		for _, entry := range envelope.Data {
			viewEntry, pathErr := storageview.CatalogViewEntry(entry)
			if pathErr != nil {
				builder.addPartial("storage_catalog_path", pathErr)
				continue
			}
			data.Tree.Entries = append(data.Tree.Entries, viewEntry)
			data.Tree.Counts.Entries++
			if viewEntry.EntryKind == storageview.EntryKindDirectory {
				data.Tree.Counts.Directories++
			} else {
				data.Tree.Counts.Files++
			}
			switch viewEntry.Permissions {
			case storageview.PermissionWritable:
				data.Tree.Counts.Writable++
			case storageview.PermissionControlled:
				data.Tree.Counts.Controlled++
			default:
				data.Tree.Counts.ReadOnly++
			}
		}
	}
	if envelope, err := client.GetStorageFilesystemStatus(ctx, correlationID); err != nil {
		builder.addPartial("storage_filesystem_status", err)
	} else {
		data.FilesystemStatus = envelope.Data
		data.FilesystemStatusAvailable = true
	}
	if envelope, err := client.GetStorageRetentionStatus(ctx, correlationID); err != nil {
		builder.addPartial("storage_retention_status", err)
	} else {
		data.RetentionStatus = envelope.Data
		data.RetentionStatusAvailable = true
	}
	if envelope, err := client.ListFileTransfers(ctx, correlationID, filetransfer.ListFilter{Limit: 25}); err != nil {
		builder.addPartial("storage_file_transfers", err)
	} else {
		data.TransferStatuses = envelope.Data
		data.TransferStatusesAvailable = true
	}
	if envelope, err := client.GetMainDocumentsStatus(ctx, correlationID); err != nil {
		builder.addPartial("storage_main_documents_status", err)
	} else {
		data.MainDocumentsStatus = envelope.Data
		data.MainDocumentsStatusAvailable = true
	}
	if envelope, err := client.ListWatchedRootBackupItems(ctx, correlationID, mainwatchedroots.BackupItemFilter{Limit: 200}); err != nil {
		builder.addPartial("storage_watched_root_backup_items", err)
	} else {
		data.WatchedRootBackupItems = envelope.Data
		data.WatchedRootBackupItemsAvailable = true
	}
	if fallback.BoxStatus.Lane != nil {
		data.LaneStatus = fallback.BoxStatus.Lane
	}
	if envelope, err := client.MaintenanceDBStatus(ctx, correlationID); err != nil {
		builder.addPartial("storage_database_status", err)
	} else {
		data.DBStatus = envelope.Data
		data.DBStatusAvailable = true
	}
	if envelope, err := client.MaintenanceBackupStatus(ctx, correlationID); err != nil {
		builder.addPartial("storage_backup_status", err)
	} else {
		data.BackupStatus = envelope.Data
		data.BackupStatusAvailable = true
	}
	if status, err := loadMainCloudStatus(ctx, client, correlationID); err != nil {
		builder.addPartial("storage_cloud_status", err)
	} else {
		data.CloudStatus = status
		data.CloudStatusAvailable = true
	}

	builder.data.Storage = data
	return builder.result()
}
