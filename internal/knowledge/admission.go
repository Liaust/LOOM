package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

// AdmissionCursor is independent of extraction claims. Full inventories rest
// at EOF until both finish; priority deliveries wrap on their own.
type AdmissionCursor struct {
	StorageAfter       string                         `json:"storage_after,omitempty"`
	SyncedAfter        [3]string                      `json:"synced_after"`
	StorageDone        bool                           `json:"storage_done,omitempty"`
	SyncedDone         bool                           `json:"synced_done,omitempty"`
	PriorityAfter      [3]string                      `json:"priority_after,omitempty"`
	Incremental        bool                           `json:"incremental,omitempty"`
	RootCursors        map[string]AdmissionRootCursor `json:"root_cursors,omitempty"`
	InventoryRootAfter string                         `json:"inventory_root_after,omitempty"`
	NextRepairAt       time.Time                      `json:"next_repair_at,omitempty"`
	BaselineGeneration string                         `json:"baseline_generation,omitempty"`
	RootConfigRevision string                         `json:"root_config_revision,omitempty"`
	// Historical checkpoint clock; periodic repair now owns a worker deadline.
	NextRootReconcileAt time.Time `json:"next_root_reconcile_at,omitempty"`
}

type AdmissionResult struct {
	PreferredPipelineRunID string                 `json:"preferred_pipeline_run_id,omitempty"`
	Cursor                 AdmissionCursor        `json:"cursor"`
	CatalogObserved        int                    `json:"catalog_observed"`
	SyncedObserved         int                    `json:"synced_observed"`
	PrioritySyncedObserved int                    `json:"priority_synced_observed"`
	PriorityApplied        int                    `json:"priority_applied"`
	Applied                int                    `json:"applied"`
	Changed                int                    `json:"changed"`
	Unchanged              int                    `json:"unchanged"`
	Skipped                int                    `json:"skipped"`
	MoreWork               bool                   `json:"more_work"`
	InventoryMoreWork      bool                   `json:"inventory_more_work"`
	InventoryDeferred      bool                   `json:"inventory_deferred,omitempty"`
	PriorityMoreWork       bool                   `json:"priority_more_work"`
	SourceGeneration       string                 `json:"source_generation"`
	Warning                *AdmissionPhaseWarning `json:"warning,omitempty"`
	ElapsedMS              map[string]int64       `json:"elapsed_ms,omitempty"`
}

// Read-only event readiness: unchanged catalog pages are safety reconciliation,
// not imports. A new accepted source/configuration must still wake admission.
func (s *Service) NotesAdmissionChanged(ctx context.Context, generation, revision string) (bool, error) {
	if s == nil || s.store.db == nil {
		return false, fmt.Errorf("Notes admission unavailable")
	}
	roots, err := s.store.ListSourceRoots(ctx, SourceRootFilter{})
	if err != nil {
		return false, err
	}
	current, err := admissionSourceGeneration(roots)
	if err != nil || current != generation {
		return current != generation, err
	}
	currentRevision, err := s.NotesAdmissionRevision(ctx)
	return currentRevision != revision, err
}

// Snapshot before admission, not after it: arrivals during a turn remain new
// work. Permanently excluded identities do not keep an unchanged worker awake.
func (s *Service) NotesAdmissionRevision(ctx context.Context) (string, error) {
	var revision string
	err := s.store.db.QueryRowContext(ctx, `SELECT jsonb_build_array(
	 NULL, NULL, NULL,
	 (SELECT md5(coalesce(string_agg(jsonb_build_array(r.project_watched_root_registration_id,
	  r.config_hash,r.activation_status,r.metadata->'knowledge_source',a.contract_hash,a.registration_status,
	  p.status,w.config_hash,w.status)::text,'|' ORDER BY r.project_watched_root_registration_id),''))
	  FROM projects.project_watched_root_registrations r
	  JOIN projects.project_contract_registrations a USING(project_contract_registration_id)
	  JOIN projects.projects p ON p.project_id=r.project_id
	  LEFT JOIN watched_roots.roots w ON w.watched_root_id=r.watched_root_id),
	 (SELECT md5(coalesce(string_agg(jsonb_build_array(d.project_id,d.node_id,d.desired_revision,
	  m.status,md5(m.result_json::text),o.state,
	  (SELECT jsonb_agg(jsonb_build_array(i.action_id,i.stage,i.final_intent,a.state) ORDER BY i.action_id)
	   FROM projects.declaration_watch_intents i
	   LEFT JOIN projects.declaration_action_receipts a USING(operation_id,action_id)
	   WHERE i.operation_id=d.operation_id))::text,'|' ORDER BY d.project_id,d.node_id),''))
	  FROM (SELECT DISTINCT ON (project_id,node_id) project_id,node_id,desired_revision,operation_id,message_id
	   FROM projects.declaration_watch_deliveries ORDER BY project_id,node_id,desired_revision DESC) d
	  JOIN communication.messages m ON m.communication_message_id=d.message_id
	  JOIN projects.declaration_operations o USING(operation_id)),
	 (SELECT md5(COALESCE(string_agg(jsonb_build_array(r.box_watch_root_registration_id,r.box_id,r.box_root_path,
	 r.node_id,r.owner_node_key,r.area_key,r.local_root_key,r.backend_root_key,r.source_kinds_json,
	 r.root_relative_path,r.activation_status,r.metadata)::text,'|' ORDER BY r.box_watch_root_registration_id),''))
	 FROM box.watch_root_registrations r))::text`).Scan(&revision)
	return revision, err
}

// AdmitRegisteredNotesOnce reads bounded metadata pages, never source trees.
// Reconciliation and pipeline identity remain owned by the existing services.
func (s *Service) AdmitRegisteredNotesOnce(ctx context.Context, cursor AdmissionCursor, limit int) (AdmissionResult, error) {
	return s.admitRegisteredNotesOnce(ctx, cursor, limit, false)
}

func (s *Service) AdmitRegisteredNotesInteractiveOnce(ctx context.Context, cursor AdmissionCursor, limit int) (AdmissionResult, error) {
	return s.admitRegisteredNotesOnce(ctx, cursor, limit, true)
}

func (s *Service) admitRegisteredNotesOnce(ctx context.Context, cursor AdmissionCursor, limit int, interactive bool) (AdmissionResult, error) {
	if s == nil || s.store.db == nil {
		return AdmissionResult{}, fmt.Errorf("knowledge store is not configured")
	}
	if limit <= 0 || limit > 200 {
		return AdmissionResult{}, fmt.Errorf("%w: admission limit must be 1..200", ErrInvalid)
	}
	result := AdmissionResult{Cursor: cursor, ElapsedMS: map[string]int64{}}
	started := time.Now()
	reconcileRoots := true
	if interactive {
		revision, err := s.NotesAdmissionRevision(ctx)
		if err != nil {
			return result, err
		}
		var parts []json.RawMessage
		if err := json.Unmarshal([]byte(revision), &parts); err != nil || len(parts) < 4 {
			return result, fmt.Errorf("invalid Notes source revision")
		}
		config, _ := json.Marshal(parts[3:])
		// Periodic repair has its own background worker. Only changed authority
		// belongs before fresh admission; an expired repair timer is not a gate.
		reconcileRoots = cursor.RootConfigRevision != string(config)
		if reconcileRoots {
			cursor.RootConfigRevision = string(config)
			cursor.NextRootReconcileAt = time.Now().Add(15 * time.Minute)
		}
	}
	finishPhase := admissionPhase(ctx, "source_root_reconciliation")
	if reconcileRoots {
		if _, err := s.ReconcileSourceRoots(ctx, SourceRootReconcileInput{DiscoverFromStore: true}); err != nil {
			return result, finishPhase(err)
		}
	}
	result.ElapsedMS["root_reconciliation"] = time.Since(started).Milliseconds()
	result.Cursor = cursor
	finishPhase = admissionPhase(ctx, "source_root_listing")
	roots, err := s.store.ListSourceRoots(ctx, SourceRootFilter{})
	if err != nil {
		return result, finishPhase(err)
	}
	finishPhase = admissionPhase(ctx, "source_generation")
	generation, err := admissionSourceGeneration(roots)
	if err != nil {
		return result, finishPhase(err)
	}
	result.SourceGeneration = generation
	if interactive {
		if cursor.Incremental {
			cursor, _, err = prepareAdmissionScopes(roots, cursor, generation, time.Now().UTC())
			if err != nil {
				return result, err
			}
			result.Cursor = cursor
		}
		if observed, err := s.admitPendingSources(ctx, roots, min(limit, 8), &result); err != nil || observed {
			return result, err
		}
		if observed, err := s.admitPendingCatalog(ctx, roots, min(limit, 8), &result); err != nil || observed {
			return result, err
		}
		next, err := s.NextPipelineWorkAt(ctx, PipelineExecutionCoordinator, time.Now().UTC())
		if err != nil {
			return result, err
		}
		if next != nil && !next.After(time.Now()) {
			result.InventoryDeferred, result.MoreWork = true, true
			return result, nil
		}
		// Exact delivery now owns ordinary edits. Historical discovery runs only
		// for an unfinished inventory, a configuration change or sparse repair.
		if !reconcileRoots && cursor.Incremental && !cursor.NextRepairAt.IsZero() && cursor.NextRepairAt.After(time.Now()) {
			complete := true
			for _, state := range cursor.RootCursors {
				complete = complete && state.StorageDone && state.SyncedDone
			}
			if complete {
				return result, nil
			}
		}
	}
	var entries []storagecatalog.Entry
	var recent KnowledgeObjectReconcileResult
	recentObserved := 0
	if interactive && reconcileRoots {
		// A fresh arrival must not wait for a cursor through old excluded rows.
		// Keep the fair cursor untouched; scoped inventory resumes next turn.
		finishPhase = admissionPhase(ctx, "priority_selection")
		phaseStarted := time.Now()
		head, err := s.store.listRecentSyncedNotesPage(ctx, min(limit, 8))
		result.ElapsedMS["recent_priority_selection"] = time.Since(phaseStarted).Milliseconds()
		if err != nil {
			return result, finishPhase(err)
		}
		recentObserved = len(head)
		if len(head) != 0 {
			finishPhase = admissionPhase(ctx, "priority_reconciliation")
			phaseStarted = time.Now()
			recent, err = s.ReconcileKnowledgeObjects(ctx, KnowledgeObjectReconcileInput{SourceRoots: roots, SyncedObjects: head})
			result.ElapsedMS["recent_priority_reconciliation"] = time.Since(phaseStarted).Milliseconds()
			result.PrioritySyncedObserved = recentObserved
			result.PriorityApplied, result.Applied, result.Skipped = recent.Applied, recent.Applied, len(recent.Skipped)
			result.Changed, result.Unchanged = recent.Changed, recent.Unchanged
			if err != nil {
				return result, finishPhase(err)
			}
		}
		now := time.Now().UTC()
		next, err := s.NextPipelineWorkAt(ctx, PipelineExecutionCoordinator, now)
		if err != nil {
			return result, err
		}
		if next != nil && !next.After(now) {
			result.InventoryDeferred, result.MoreWork = true, true
			return result, nil
		}
		// Publish the completed authority/head phase before another selection
		// spends the shared budget. This does not acknowledge inventory coverage.
		result.InventoryMoreWork = !cursor.StorageDone || !cursor.SyncedDone
		if cursor.Incremental {
			result.InventoryMoreWork = false
			for _, state := range cursor.RootCursors {
				result.InventoryMoreWork = result.InventoryMoreWork || !state.StorageDone || !state.SyncedDone
			}
		}
		result.MoreWork = result.InventoryMoreWork
		return result, nil
	}
	// New deliveries must not wait for the full custody inventory. Advancing
	// this independent cursor also prevents excluded files starving later ones.
	finishPhase = admissionPhase(ctx, "priority_selection")
	var priority []SyncedObjectEntry
	if !interactive {
		phaseStarted := time.Now()
		priority, err = s.store.listSyncedNotesPageFiltered(ctx, "", cursor.PriorityAfter, limit+1, true)
		result.ElapsedMS["priority_selection"] = time.Since(phaseStarted).Milliseconds()
		if err != nil {
			return result, finishPhase(err)
		}
	}
	morePriority := len(priority) > limit
	if morePriority {
		priority = priority[:limit]
	}
	result.PrioritySyncedObserved, result.PriorityMoreWork = recentObserved+len(priority), morePriority
	var urgent KnowledgeObjectReconcileResult
	if len(priority) != 0 {
		// Commit urgent admission before a slow catalog query can exhaust the
		// shared budget. Missing pipeline publication remains priority work.
		finishPhase = admissionPhase(ctx, "priority_reconciliation")
		urgent, err = s.ReconcileKnowledgeObjects(ctx, KnowledgeObjectReconcileInput{
			SourceRoots: roots, SyncedObjects: priority,
		})
		result.PriorityApplied, result.Applied, result.Skipped = recent.Applied+urgent.Applied, recent.Applied+urgent.Applied, len(recent.Skipped)+len(urgent.Skipped)
		result.Changed, result.Unchanged = recent.Changed+urgent.Changed, recent.Unchanged+urgent.Unchanged
		if err != nil {
			return result, finishPhase(err)
		}
	}
	var moreCatalog bool
	if interactive {
		now := time.Now().UTC()
		next, err := s.NextPipelineWorkAt(ctx, PipelineExecutionCoordinator, now)
		if err != nil {
			return result, err
		}
		if next != nil && !next.After(now) {
			result.Cursor = cursor
			result.Cursor.PriorityAfter = [3]string{}
			if morePriority {
				last := priority[len(priority)-1]
				result.Cursor.PriorityAfter = [3]string{last.NotesSourceRootID, last.ObjectVersionID, last.ReplicaID}
			}
			result.InventoryDeferred, result.MoreWork = true, true
			return result, nil
		}
	}
	var synced []SyncedObjectEntry
	if cursor.Incremental {
		// The coordinator already adapts this bounded limit to its time budget.
		// Its eight-entry urgent probe must not throttle historical inventory too.
		page, err := s.admitScopedInventory(ctx, roots, cursor, generation, limit)
		if err != nil {
			return result, err
		}
		entries, synced = page.entries, page.synced
		result.Cursor = page.cursor
		result.InventoryMoreWork = page.more
		result.CatalogObserved, result.SyncedObserved = len(entries), len(synced)
	} else {
		if !cursor.StorageDone {
			entries, moreCatalog, err = s.store.notesCatalogPage(ctx, cursor.StorageAfter, limit)
			if err != nil {
				return result, err
			}
		}
		result.CatalogObserved = len(entries)
		if !cursor.SyncedDone {
			finishPhase = admissionPhase(ctx, "inventory_selection")
			synced, err = s.store.listSyncedNotesPage(ctx, "", cursor.SyncedAfter, limit+1)
			if err != nil {
				return result, finishPhase(err)
			}
		}
		moreSynced := len(synced) > limit
		if moreSynced {
			synced = synced[:limit]
		}
		result.SyncedObserved = len(synced)
		result.MoreWork, result.InventoryMoreWork = moreCatalog || moreSynced, moreCatalog || moreSynced
		result.Cursor = AdmissionCursor{}
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
	}
	result.Cursor.RootConfigRevision = cursor.RootConfigRevision
	result.Cursor.NextRootReconcileAt = cursor.NextRootReconcileAt
	result.MoreWork = result.InventoryMoreWork
	result.Cursor.PriorityAfter = [3]string{}
	result.PrioritySyncedObserved = recentObserved + len(priority)
	result.PriorityApplied = recent.Applied + urgent.Applied
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
	finishPhase = admissionPhase(ctx, "candidate_reconciliation")
	reconciled, err := s.ReconcileKnowledgeObjects(ctx, KnowledgeObjectReconcileInput{
		SourceRoots: roots, StorageEntries: entries, SyncedObjects: combined, AuditMetadata: true,
	})
	result.Applied, result.Skipped = recent.Applied+urgent.Applied+reconciled.Applied, len(recent.Skipped)+len(urgent.Skipped)+len(reconciled.Skipped)
	result.Changed, result.Unchanged = recent.Changed+urgent.Changed+reconciled.Changed, recent.Unchanged+urgent.Unchanged+reconciled.Unchanged
	return result, finishPhase(err)
}

// Configuration changes need one fast full sweep; observation timestamps and
// changing report metadata must not turn every ordinary sweep into an import.
func admissionSourceGeneration(roots []SourceRoot) (string, error) {
	identities := make([]string, 0, len(roots))
	for _, root := range roots {
		status := root.Status
		if root.RootKind == RootKindProjectMaterial && status == SourceRootStatusBlocked {
			// Waiting for the same owner's ACK does not change declared membership.
			status = SourceRootStatusActive
		}
		raw, err := json.Marshal(struct {
			ID           string
			Kind         string
			NodeID       *string
			NodeKey      string
			ProjectID    *string
			Backend      string
			SourcePath   string
			RelativePath string
			Status       string
			Declaration  any
		}{root.NotesSourceRootID, root.RootKind, root.NodeID, root.NodeKey,
			root.ProjectID, root.BackendRootKey, root.SourcePath, root.RootRelativePath,
			status, admissionSourceDeclaration(root)})
		if err != nil {
			return "", fmt.Errorf("Notes admission source generation unavailable")
		}
		identities = append(identities, string(raw))
	}
	sort.Strings(identities)
	raw, err := json.Marshal(identities)
	if err != nil {
		return "", fmt.Errorf("Notes admission source generation unavailable")
	}
	return hashArtifactValue(string(raw)), nil
}

func (s Store) notesCatalogPage(ctx context.Context, after string, limit int) ([]storagecatalog.Entry, bool, error) {
	finishPhase := admissionPhase(ctx, "catalog_selection")
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
		return nil, false, finishPhase(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, false, finishPhase(err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, false, finishPhase(err)
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	finishPhase = admissionPhase(ctx, "catalog_hydration")
	details, err := storagecatalog.NewService(s.db).ListEntryDetails(ctx, ids)
	if err != nil {
		return nil, false, finishPhase(err)
	}
	entries := make([]storagecatalog.Entry, 0, len(ids))
	for _, id := range ids {
		detail, exists := details[id]
		if !exists {
			return nil, false, finishPhase(fmt.Errorf("catalog changed during Notes admission; retry page"))
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
	err := tx.QueryRowContext(ctx, `WITH requested AS MATERIALIZED (
 SELECT nsr.*, 'watched-root://' || nsr.backend_root_key || '/' || $2 AS exact_path
 FROM knowledge.notes_source_roots nsr WHERE nsr.notes_source_root_id=$1 AND nsr.status='active'
 ), candidate_owners AS MATERIALIZED (
 SELECT ov.object_id FROM requested r JOIN objects.object_versions ov
 ON md5(btrim(ov.source_path))=md5(r.exact_path)
 UNION
 SELECT f.object_id FROM requested r JOIN files.file_metadata f
 ON md5(btrim(f.source_path))=md5(r.exact_path)
 UNION
 SELECT o.object_id FROM requested r JOIN objects.objects o
 ON md5(btrim(o.metadata->>'source_path'))=md5(r.exact_path)
 UNION
 SELECT o.object_id FROM requested r JOIN objects.objects o
 ON o.metadata->'metadata'->>'watched_root'=r.backend_root_key
 WHERE r.root_kind IN ('project_notes','project_material')
 ) SELECT EXISTS (
 SELECT 1 FROM requested nsr
 JOIN scopes.scopes ss ON `+notesSyncedScopeSQL("nsr", "ss")+`
 JOIN objects.object_scope_links osl ON osl.scope_id=ss.scope_id AND osl.relevance_status='active'
 JOIN candidate_owners owner_candidate ON owner_candidate.object_id=osl.object_id
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
