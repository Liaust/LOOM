package knowledge

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

type AdmissionRootCursor struct {
	Generation   string    `json:"generation"`
	StorageAfter string    `json:"storage_after,omitempty"`
	SyncedAfter  [3]string `json:"synced_after"`
	StorageDone  bool      `json:"storage_done,omitempty"`
	SyncedDone   bool      `json:"synced_done,omitempty"`
	NextRepairAt time.Time `json:"next_repair_at,omitempty"`
}

type scopedAdmissionPage struct {
	cursor  AdmissionCursor
	entries []storagecatalog.Entry
	synced  []SyncedObjectEntry
	more    bool
}

func prepareAdmissionScopes(roots []SourceRoot, cursor AdmissionCursor, generation string, now time.Time) (AdmissionCursor, []string, error) {
	// Copy before mutation: a timed-out page must not change its input checkpoint.
	states := make(map[string]AdmissionRootCursor, len(roots))
	baseline := cursor.RootCursors == nil && cursor.BaselineGeneration == generation
	ids := make([]string, 0, len(roots))
	for _, root := range roots {
		fingerprint, err := admissionSourceGeneration([]SourceRoot{root})
		if err != nil {
			return cursor, nil, err
		}
		state, exists := cursor.RootCursors[root.NotesSourceRootID]
		if !exists || state.Generation != fingerprint {
			state = AdmissionRootCursor{Generation: fingerprint, StorageDone: baseline, SyncedDone: baseline}
		}
		if state.StorageDone && state.SyncedDone {
			if state.NextRepairAt.IsZero() {
				state.NextRepairAt = nextAdmissionRepair(root.NotesSourceRootID, now)
			} else if !now.Before(state.NextRepairAt) {
				state = AdmissionRootCursor{Generation: fingerprint}
			}
		}
		states[root.NotesSourceRootID] = state
		ids = append(ids, root.NotesSourceRootID)
	}
	sort.Strings(ids)
	// One unfinished collection must not monopolize every inventory turn.
	start := sort.Search(len(ids), func(i int) bool { return ids[i] > cursor.InventoryRootAfter })
	ids = append(append(make([]string, 0, len(ids)), ids[start:]...), ids[:start]...)
	cursor.RootCursors, cursor.BaselineGeneration = states, ""
	cursor.NextRepairAt = admissionRepairDeadline(states)
	return cursor, ids, nil
}

func (s *Service) admitScopedInventory(ctx context.Context, roots []SourceRoot, cursor AdmissionCursor, generation string, limit int) (scopedAdmissionPage, error) {
	now := time.Now().UTC()
	cursor, ids, err := prepareAdmissionScopes(roots, cursor, generation, now)
	page := scopedAdmissionPage{cursor: cursor}
	if err != nil {
		return page, err
	}
	byID := make(map[string]SourceRoot, len(roots))
	for _, root := range roots {
		byID[root.NotesSourceRootID] = root
	}
	for _, id := range ids {
		state := cursor.RootCursors[id]
		if state.StorageDone && state.SyncedDone {
			continue
		}
		if !state.StorageDone {
			var more bool
			page.entries, state.StorageAfter, more, err = s.store.notesCatalogRootPage(ctx, byID[id], state.StorageAfter, limit)
			if err != nil {
				return page, err
			}
			state.StorageDone = !more
		}
		if !state.SyncedDone {
			page.synced, err = s.store.listSyncedNotesPage(ctx, id, state.SyncedAfter, limit+1)
			if err != nil {
				return page, err
			}
			state.SyncedDone = len(page.synced) <= limit
			if !state.SyncedDone {
				page.synced = page.synced[:limit]
				last := page.synced[len(page.synced)-1]
				state.SyncedAfter = [3]string{id, last.ObjectVersionID, last.ReplicaID}
			}
		}
		if state.StorageDone && state.SyncedDone {
			state.NextRepairAt = nextAdmissionRepair(id, now)
		}
		cursor.RootCursors[id] = state
		cursor.InventoryRootAfter = id
		break
	}
	for _, state := range cursor.RootCursors {
		page.more = page.more || !state.StorageDone || !state.SyncedDone
	}
	cursor.NextRepairAt = admissionRepairDeadline(cursor.RootCursors)
	page.cursor = cursor
	return page, nil
}

func nextAdmissionRepair(id string, now time.Time) time.Time {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return now.Add(6*time.Hour + time.Duration(h.Sum32()%600)*time.Second)
}

func admissionRepairDeadline(states map[string]AdmissionRootCursor) (next time.Time) {
	for _, state := range states {
		if state.StorageDone && state.SyncedDone && !state.NextRepairAt.IsZero() && (next.IsZero() || state.NextRepairAt.Before(next)) {
			next = state.NextRepairAt
		}
	}
	return
}

const preferredCatalogEntrySQL = `SELECT se.storage_entry_id FROM storage.storage_entries se
 WHERE se.source_area=p.source_area AND se.origin_node_key=p.origin_node_key
 AND COALESCE(se.origin_node_id,'')=COALESCE(p.origin_node_id,'')
 AND COALESCE(se.project_id,'')=COALESCE(p.project_id,'') AND se.watched_root_key=p.watched_root_key
 AND md5(COALESCE(NULLIF(se.original_source_path,''),se.logical_path))=md5(COALESCE(NULLIF(p.original_source_path,''),p.logical_path))
 AND COALESCE(NULLIF(se.original_source_path,''),se.logical_path)=COALESCE(NULLIF(p.original_source_path,''),p.logical_path)
 ORDER BY (se.availability_state IN ('archived','superseded')),se.updated_at DESC,
 CASE se.availability_state WHEN 'deleted' THEN 0 WHEN 'tombstoned' THEN 0
 WHEN 'available' THEN 1 WHEN 'failed' THEN 2 WHEN 'pending' THEN 3
 WHEN 'discovered' THEN 4 WHEN 'archived' THEN 5 WHEN 'superseded' THEN 5 ELSE 6 END,
 se.storage_entry_id DESC LIMIT 1`

// Page physical identities first, then resolve each page member's preferred
// representation. A retained historical ID must never replace newer live truth.
func (s Store) notesCatalogRootPage(ctx context.Context, root SourceRoot, after string, limit int) ([]storagecatalog.Entry, string, bool, error) {
	area := storagecatalog.SourceAreaProjects
	if root.RootKind == RootKindBoxNotes {
		area = storagecatalog.SourceAreaNotes
	} else if root.RootKind == RootKindBoxTopics || root.RootKind == RootKindBoxLibrary {
		area = storagecatalog.SourceAreaExternalWatchedRoot
	}
	rows, err := s.db.QueryContext(ctx, `WITH page AS MATERIALIZED (
	 SELECT se.* FROM storage.storage_entries se WHERE se.storage_entry_id>$1
	 AND se.source_area=$2 AND se.watched_root_key=$3
	 AND ($4::text IS NULL OR se.origin_node_id IS NULL OR se.origin_node_id=$4)
	 AND ($5::text='' OR se.origin_node_key='' OR se.origin_node_key=$5)
	 AND ($2='notes' OR se.project_id IS NOT DISTINCT FROM $6::text)
	 ORDER BY se.storage_entry_id LIMIT $7
	) SELECT p.storage_entry_id,preferred.storage_entry_id FROM page p
	 CROSS JOIN LATERAL (
	 `+preferredCatalogEntrySQL+`) preferred ORDER BY p.storage_entry_id`,
		after, area, root.BackendRootKey, root.NodeID, root.NodeKey, root.ProjectID, limit+1)
	if err != nil {
		return nil, after, false, err
	}
	var selected, physical []string
	for rows.Next() {
		var id, preferred string
		if err := rows.Scan(&id, &preferred); err != nil {
			rows.Close()
			return nil, after, false, err
		}
		physical, selected = append(physical, id), append(selected, preferred)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, after, false, err
	}
	more := len(selected) > limit
	if more {
		selected, physical = selected[:limit], physical[:limit]
	}
	current := make([]string, 0, len(selected))
	for index, id := range selected {
		if physical[index] == id {
			current = append(current, id)
		}
	}
	details, err := storagecatalog.NewService(s.db).ListEntryDetails(ctx, current)
	if err != nil {
		return nil, after, false, err
	}
	entries := make([]storagecatalog.Entry, 0, len(selected))
	seen := map[string]bool{}
	for index, id := range selected {
		// The preferred physical identity has its own position in this sweep.
		// Replaying it at every historical ID repeatedly rebuilds unchanged
		// metadata; only its own page may admit it. Concurrent replacement is
		// picked up at the successor's position or the next safety sweep.
		if physical[index] != id {
			continue
		}
		if !seen[id] {
			detail, exists := details[id]
			if !exists {
				return nil, after, false, fmt.Errorf("catalog changed during scoped admission; retry page")
			}
			entries = append(entries, detail.Entry)
			seen[id] = true
		}
	}
	if len(physical) > 0 {
		after = physical[len(physical)-1]
	}
	return entries, after, more, nil
}
