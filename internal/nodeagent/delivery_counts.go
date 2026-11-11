package nodeagent

import "context"

// Scheduling consumes detached counts for every root from one queue generation,
// not a separate retained-history decode for each enabled root.
func (s Store) syncCountsByRoot() (map[string]LocalSyncCounts, error) {
	if !s.syncLocked && s.recordsExist() {
		var counts map[string]LocalSyncCounts
		err := s.withRecordAccess(func(locked Store) error { var err error; counts, err = locked.syncCountsByRoot(); return err })
		return counts, err
	}
	if s.queueDB != nil {
		return s.recordSyncCounts()
	}
	load := func() (map[string]LocalSyncCounts, error) {
		items, err := s.LoadSyncOutbox()
		if err != nil {
			return nil, err
		}
		objects, err := s.LoadSyncObjects()
		if err != nil {
			return nil, err
		}
		refs := make(map[string]LocalSyncObject, len(objects))
		versions := make(map[string]LocalSyncObject, len(objects))
		for _, object := range objects {
			refs[object.LocalObjectID] = object
			versions[localObjectVersionKey(object.LocalObjectID, object.LocalVersionID)] = object
		}
		counts := map[string]LocalSyncCounts{}
		for _, item := range items {
			root := syncOutboxRoot(item, refs, versions)
			c := counts[root]
			switch item.Status {
			case localSyncStatusAccepted:
				c.Accepted++
			case localSyncStatusDuplicate:
				c.Duplicates++
			case localSyncStatusFailed:
				c.Failed++
			case localSyncStatusConflicted:
				c.Conflicted++
			case localSyncStatusRetainedInternal:
				c.RetainedInternal++
			case localSyncStatusRetainedSuperseded:
				c.RetainedSuperseded++
			default:
				c.Pending++
			}
			counts[root] = c
		}
		return counts, nil
	}
	if s.syncBatch != nil {
		return load()
	}
	return cachedLocalStatus("sync-roots", []string{s.syncOutboxPath(), s.syncObjectsPath()}, load)
}

type backupDeliveryCounts struct {
	Pending   int
	Retryable int
}

func (s Store) backupDeliveryCountsByRoot() (map[string]backupDeliveryCounts, error) {
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return nil, err
	}
	defer unlock()
	if s.queueDB != nil && s.syncBatch == nil {
		return s.recordBackupDeliveryCounts()
	}
	load := func() (map[string]backupDeliveryCounts, error) {
		items, err := s.LoadWatchedRootBackupOutbox()
		if err != nil {
			return nil, err
		}
		counts := map[string]backupDeliveryCounts{}
		for _, item := range items {
			switch item.Status {
			case localBackupStatusAccepted, localBackupStatusDuplicate, localBackupStatusFailed, localBackupStatusConflicted:
				continue
			}
			c := counts[item.RootKey]
			c.Pending++
			if watchedRootBackupHasError(item.LastErrorCode, item.LastErrorMessage) {
				c.Retryable++
			}
			counts[item.RootKey] = c
		}
		return counts, nil
	}
	if s.syncBatch != nil {
		return load()
	}
	return cachedLocalStatus("backup-delivery-roots", []string{s.watchedRootBackupOutboxPath()}, load)
}
