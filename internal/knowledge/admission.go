package knowledge

import (
	"context"
	"fmt"

	"loom.local/loom/internal/storagecatalog"
)

// AdmissionCursor is independent of extraction claims. EOF wraps on the next
// tick so changed metadata and temporarily unavailable sources are revisited.
type AdmissionCursor struct {
	StorageAfter string    `json:"storage_after,omitempty"`
	SyncedAfter  [3]string `json:"synced_after"`
}

type AdmissionResult struct {
	Cursor          AdmissionCursor `json:"cursor"`
	CatalogObserved int             `json:"catalog_observed"`
	SyncedObserved  int             `json:"synced_observed"`
	Applied         int             `json:"applied"`
	Skipped         int             `json:"skipped"`
	MoreWork        bool            `json:"more_work"`
}

// AdmitRegisteredNotesOnce reads bounded metadata pages, never source trees.
// Reconciliation and pipeline identity remain owned by the existing services.
func (s *Service) AdmitRegisteredNotesOnce(ctx context.Context, cursor AdmissionCursor, limit int) (AdmissionResult, error) {
	if s == nil || s.store.db == nil {
		return AdmissionResult{}, fmt.Errorf("knowledge store is not configured")
	}
	if limit <= 0 || limit > 200 {
		return AdmissionResult{}, fmt.Errorf("%w: admission limit must be 1..200", ErrInvalid)
	}
	if _, err := s.ReconcileSourceRoots(ctx, SourceRootReconcileInput{DiscoverFromStore: true}); err != nil {
		return AdmissionResult{}, err
	}
	roots, err := s.store.ListSourceRoots(ctx, SourceRootFilter{})
	if err != nil {
		return AdmissionResult{}, err
	}
	entries, moreCatalog, err := s.store.notesCatalogPage(ctx, cursor.StorageAfter, limit)
	if err != nil {
		return AdmissionResult{}, err
	}
	synced, err := s.store.listSyncedNotesPage(ctx, "", cursor.SyncedAfter, limit+1)
	if err != nil {
		return AdmissionResult{}, err
	}
	moreSynced := len(synced) > limit
	if moreSynced {
		synced = synced[:limit]
	}
	result := AdmissionResult{CatalogObserved: len(entries), SyncedObserved: len(synced), MoreWork: moreCatalog || moreSynced}
	if moreCatalog {
		result.Cursor.StorageAfter = entries[len(entries)-1].StorageEntryID
	}
	if moreSynced {
		last := synced[len(synced)-1]
		result.Cursor.SyncedAfter = [3]string{last.NotesSourceRootID, last.ObjectVersionID, last.ReplicaID}
	}
	reconciled, err := s.ReconcileKnowledgeObjects(ctx, KnowledgeObjectReconcileInput{
		SourceRoots: roots, StorageEntries: entries, SyncedObjects: synced,
	})
	result.Applied, result.Skipped = reconciled.Applied, len(reconciled.Skipped)
	return result, err
}

func (s Store) notesCatalogPage(ctx context.Context, after string, limit int) ([]storagecatalog.Entry, bool, error) {
	// Rank each physical representation before pagination, matching
	// preferStorageKnowledgeCandidate: historical copies cannot displace live
	// entries merely because their IDs fall on a later page.
	rows, err := s.db.QueryContext(ctx, `WITH preferred AS (
		SELECT DISTINCT ON (source_area, origin_node_key, origin_node_id, project_id,
			watched_root_key, COALESCE(NULLIF(original_source_path, ''), logical_path)) storage_entry_id
		FROM storage.storage_entries se
		WHERE (source_area IN ('notes', 'projects') OR
		 (source_area = 'external_watched_root' AND EXISTS (
		   SELECT 1 FROM knowledge.notes_source_roots nsr
		   WHERE nsr.root_kind IN ('box_topics', 'box_library') AND nsr.status = 'active'
		     AND nsr.backend_root_key = se.watched_root_key
		     AND nsr.node_key = se.origin_node_key AND nsr.node_id = se.origin_node_id
		     AND se.project_id IS NULL
		 ))) AND COALESCE(watched_root_key, '') <> ''
		ORDER BY source_area, origin_node_key, origin_node_id, project_id, watched_root_key,
			COALESCE(NULLIF(original_source_path, ''), logical_path),
			(availability_state IN ('archived', 'superseded')), updated_at DESC,
			CASE availability_state WHEN 'deleted' THEN 0 WHEN 'tombstoned' THEN 0
			 WHEN 'available' THEN 1 WHEN 'failed' THEN 2 WHEN 'pending' THEN 3
			 WHEN 'discovered' THEN 4 WHEN 'archived' THEN 5 WHEN 'superseded' THEN 5 ELSE 6 END,
			storage_entry_id DESC
	) SELECT storage_entry_id FROM preferred WHERE storage_entry_id > $1::text
	ORDER BY storage_entry_id LIMIT $2::integer`, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, false, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, false, err
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	details, err := storagecatalog.NewService(s.db).ListEntryDetails(ctx, ids)
	if err != nil {
		return nil, false, err
	}
	entries := make([]storagecatalog.Entry, 0, len(ids))
	for _, id := range ids {
		detail, exists := details[id]
		if !exists {
			return nil, false, fmt.Errorf("catalog changed during Notes admission; retry page")
		}
		entries = append(entries, detail.Entry)
	}
	return entries, more, nil
}
