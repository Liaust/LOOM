package knowledge

import (
	"context"
	"database/sql"
	"fmt"

	"loom.local/loom/internal/storagecatalog"
)

// AdmissionCursor is independent of extraction claims. Full inventories rest
// at EOF until both finish; priority deliveries wrap on their own.
type AdmissionCursor struct {
	StorageAfter  string    `json:"storage_after,omitempty"`
	SyncedAfter   [3]string `json:"synced_after"`
	StorageDone   bool      `json:"storage_done,omitempty"`
	SyncedDone    bool      `json:"synced_done,omitempty"`
	PriorityAfter [3]string `json:"priority_after,omitempty"`
}

type AdmissionResult struct {
	Cursor                 AdmissionCursor `json:"cursor"`
	CatalogObserved        int             `json:"catalog_observed"`
	SyncedObserved         int             `json:"synced_observed"`
	PrioritySyncedObserved int             `json:"priority_synced_observed"`
	Applied                int             `json:"applied"`
	Skipped                int             `json:"skipped"`
	MoreWork               bool            `json:"more_work"`
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
	var entries []storagecatalog.Entry
	// New deliveries must not wait for the full custody inventory. Advancing
	// this independent cursor also prevents excluded files starving later ones.
	priority, err := s.store.listSyncedNotesPageFiltered(ctx, "", cursor.PriorityAfter, limit+1, true)
	if err != nil {
		return AdmissionResult{}, err
	}
	morePriority := len(priority) > limit
	if morePriority {
		priority = priority[:limit]
	}
	var urgent KnowledgeObjectReconcileResult
	if len(priority) != 0 {
		// Commit urgent admission before a slow catalog query can exhaust the
		// shared budget. Missing pipeline publication remains priority work.
		urgent, err = s.ReconcileKnowledgeObjects(ctx, KnowledgeObjectReconcileInput{
			SourceRoots: roots, SyncedObjects: priority,
		})
		if err != nil {
			return AdmissionResult{}, err
		}
	}
	var moreCatalog bool
	if !cursor.StorageDone {
		entries, moreCatalog, err = s.store.notesCatalogPage(ctx, cursor.StorageAfter, limit)
		if err != nil {
			return AdmissionResult{}, err
		}
	}
	var synced []SyncedObjectEntry
	if !cursor.SyncedDone {
		synced, err = s.store.listSyncedNotesPage(ctx, "", cursor.SyncedAfter, limit+1)
		if err != nil {
			return AdmissionResult{}, err
		}
	}
	moreSynced := len(synced) > limit
	if moreSynced {
		synced = synced[:limit]
	}
	result := AdmissionResult{CatalogObserved: len(entries), SyncedObserved: len(synced), MoreWork: moreCatalog || moreSynced}
	result.Cursor.StorageDone = !moreCatalog
	result.Cursor.SyncedDone = !moreSynced
	if moreCatalog {
		result.Cursor.StorageAfter = entries[len(entries)-1].StorageEntryID
	}
	if moreSynced {
		last := synced[len(synced)-1]
		result.Cursor.SyncedAfter = [3]string{last.NotesSourceRootID, last.ObjectVersionID, last.ReplicaID}
	}
	if !result.MoreWork {
		result.Cursor = AdmissionCursor{}
	}
	result.PrioritySyncedObserved = len(priority)
	result.MoreWork = result.MoreWork || morePriority
	if morePriority {
		last := priority[len(priority)-1]
		result.Cursor.PriorityAfter = [3]string{last.NotesSourceRootID, last.ObjectVersionID, last.ReplicaID}
	}
	seen := make(map[[3]string]bool, len(priority))
	for _, entry := range priority {
		seen[[3]string{entry.NotesSourceRootID, entry.ObjectVersionID, entry.ReplicaID}] = true
	}
	combined := make([]SyncedObjectEntry, 0, len(synced))
	for _, entry := range synced {
		key := [3]string{entry.NotesSourceRootID, entry.ObjectVersionID, entry.ReplicaID}
		if !seen[key] {
			combined = append(combined, entry)
			seen[key] = true
		}
	}
	reconciled, err := s.ReconcileKnowledgeObjects(ctx, KnowledgeObjectReconcileInput{
		SourceRoots: roots, StorageEntries: entries, SyncedObjects: combined,
	})
	result.Applied, result.Skipped = urgent.Applied+reconciled.Applied, len(urgent.Skipped)+len(reconciled.Skipped)
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

// notesSourceHasSyncedOwner is a bounded identity check for one catalog source,
// independent of either admission cursor. It is not an eligibility/read grant:
// a private, disabled or temporarily unavailable current Objects source still
// owns this path and cannot be replaced with older retained catalog bytes.
// The normal synced query continues to enforce eligibility before admission.
func notesSourceHasSyncedOwner(ctx context.Context, tx *sql.Tx, object KnowledgeObject) (bool, error) {
	var owned bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
 SELECT 1 FROM knowledge.notes_source_roots nsr
 JOIN scopes.scopes ss ON `+notesSyncedScopeSQL("nsr", "ss")+`
 JOIN objects.object_scope_links osl ON osl.scope_id=ss.scope_id AND osl.relevance_status='active'
 JOIN objects.objects o ON o.object_id=osl.object_id AND o.object_type='file'
 JOIN files.file_metadata f ON f.object_id=o.object_id
 JOIN objects.object_versions ov ON ov.object_version_id=f.latest_version_id AND ov.object_id=o.object_id
 WHERE nsr.notes_source_root_id=$1 AND nsr.status='active'
 AND COALESCE(ov.source_node_id,f.source_node_id) IS NOT DISTINCT FROM $3::text
 AND (nsr.node_id IS NULL OR nsr.node_id=COALESCE(ov.source_node_id,f.source_node_id))
 AND (COALESCE(nsr.node_key,'')='' OR nsr.node_key=$4)
 AND (
   btrim(COALESCE(ov.source_path,f.source_path,o.metadata->>'source_path','')) = 'watched-root://' || nsr.backend_root_key || '/' || $2
   OR (nsr.root_kind IN ('project_notes','project_material')
     AND NOT starts_with(btrim(COALESCE(ov.source_path,f.source_path,o.metadata->>'source_path','')), 'watched-root://' || nsr.backend_root_key || '/')
     AND o.metadata->'metadata'->>'watched_root'=nsr.backend_root_key
     AND btrim(COALESCE(f.logical_name,o.name,''))=$2)
 )
 AND `+notesSyncedCurrentSourceSQL("ss", "o", "ov", "f")+`
 )`, object.NotesSourceRootID, object.RelativePath, object.SourceNodeID, object.SourceNodeKey).Scan(&owned)
	return owned, err
}
