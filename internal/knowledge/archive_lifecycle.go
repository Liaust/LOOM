package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
)

// NotesArchiveProjector is an internal custody consumer, not an access or
// filesystem authority. Construction requires the local trusted archive owner.
type NotesArchiveProjector struct {
	DB        *sql.DB
	Workspace storagearchive.WorkspaceMoveService
	NodeKey   string
}

type NotesArchiveProjectionResult struct {
	OperationID string `json:"operation_id"`
	EventID     string `json:"event_id"`
	Projected   int    `json:"projected"`
	Replayed    int    `json:"replayed"`
}

const maxNotesCustodyObjects = 250000

func (p NotesArchiveProjector) ProjectOperation(ctx context.Context, operationID string) (NotesArchiveProjectionResult, error) {
	result := NotesArchiveProjectionResult{OperationID: operationID}
	if p.DB == nil || strings.TrimSpace(p.NodeKey) == "" || !notesCustodyOperationID.MatchString(operationID) {
		return result, fmt.Errorf("%w: trusted local custody projector is not configured", ErrInvalid)
	}
	tx, err := p.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err := storagecatalog.LockWorkspaceCustodyWriterTx(ctx, tx); err != nil {
		return result, err
	}
	var nodeID string
	if err := tx.QueryRowContext(ctx, `SELECT node_id FROM nodes.nodes WHERE node_key=$1 AND node_role='main' FOR SHARE`, p.NodeKey).Scan(&nodeID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return result, err
		}
		return result, fmt.Errorf("%w: trusted local Notes custody node unavailable", ErrInvalid)
	}
	if err := lockNotesUpstream(ctx, tx, operationID); err != nil {
		return result, notesCustodyFailure(NotesCustodyUpstreamUnavailable, err)
	}
	// Authenticate on the lock-holding connection, including under pool pressure.
	owner := p.Workspace
	owner.Journal = notesCustodyJournal{tx: tx}
	evidence, err := owner.ReadLifecycleEvidence(ctx, operationID)
	if err != nil {
		return result, notesCustodyFailure(NotesCustodyUpstreamUnavailable, err)
	}
	result.EventID = evidence.Event.EventID
	projectEvent, err := notesProjectCompletion(ctx, tx, evidence)
	if err != nil {
		return result, notesCustodyFailure(NotesCustodyProjectUnavailable, err)
	}
	previousEvent, err := notesCustodyArchivePredecessor(ctx, tx, nodeID, evidence)
	if err != nil {
		return result, err
	}
	candidates, err := p.notesCustodyCandidates(ctx, tx, evidence)
	if err != nil {
		return result, err
	}
	inventory := make(map[string]storagearchive.NoFollowInventoryEntry, len(evidence.ArchivePlan.Inventory.Entries))
	for _, entry := range evidence.ArchivePlan.Inventory.Entries {
		inventory[entry.RelativePath] = entry
	}
	for _, candidate := range candidates {
		replayed, err := p.projectNotesObject(ctx, tx, evidence, projectEvent, candidate, inventory)
		if err != nil {
			return NotesArchiveProjectionResult{OperationID: operationID, EventID: result.EventID}, notesCustodyFailure(NotesCustodySourceConflict, err)
		}
		if replayed {
			result.Replayed++
		} else {
			result.Projected++
		}
	}
	if err := recordNotesCustodyReceipt(ctx, tx, nodeID, result.EventID, notesCustodyReceipt{
		ManifestDigest: evidence.ManifestDigest, ProjectEventID: projectEvent, PreviousEventID: previousEvent,
	}, result.Projected+result.Replayed); err != nil {
		return NotesArchiveProjectionResult{OperationID: operationID, EventID: result.EventID}, err
	}
	if err := tx.Commit(); err != nil {
		return NotesArchiveProjectionResult{OperationID: operationID, EventID: result.EventID}, err
	}
	return result, nil
}

type notesCustodyJournal struct {
	storagecatalog.Service // Unconfigured write methods refuse; only reads are used.
	tx                     *sql.Tx
}

func (j notesCustodyJournal) LoadWorkspaceArchiveJournal(ctx context.Context, operationID string) (storagecatalog.WorkspaceArchiveJournalRecord, bool, error) {
	return storagecatalog.LoadWorkspaceArchiveJournalTx(ctx, j.tx, operationID)
}

func lockNotesUpstream(ctx context.Context, tx *sql.Tx, operationID string) error {
	var archiveID, restoreID string
	if err := tx.QueryRowContext(ctx, `SELECT workspace_archive_operation_id, COALESCE(restore_operation_id,'')
	 FROM storage.workspace_archive_manifests
	 WHERE workspace_archive_operation_id=$1 OR restore_operation_id=$1 FOR SHARE`, operationID).Scan(&archiveID, &restoreID); err != nil {
		return fmt.Errorf("Notes custody requires a completed upstream manifest: %w", err)
	}
	for _, table := range []string{"workspace_archive_operations", "workspace_lifecycle_events"} {
		rows, err := tx.QueryContext(ctx, `SELECT workspace_archive_operation_id FROM storage.`+table+`
		 WHERE workspace_archive_operation_id IN ($1,$2) ORDER BY workspace_archive_operation_id FOR SHARE`, archiveID, restoreID)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			count++
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		if count != 1 && restoreID == "" || count != 2 && restoreID != "" {
			return fmt.Errorf("Notes custody upstream operation/event chain is incomplete")
		}
	}
	return nil
}

type notesCustodyCandidate struct {
	Object KnowledgeObject
	Root   SourceRoot
	Bound  bool
}

func (p NotesArchiveProjector) notesCustodyCandidates(ctx context.Context, tx *sql.Tx, evidence storagearchive.WorkspaceLifecycleEvidence) ([]notesCustodyCandidate, error) {
	// Prefixes only find candidates. Every new transition additionally requires
	// exact root, native/catalog identity and reviewed inventory membership.
	bindings := []string{}
	for _, binding := range evidence.ArchivePlan.CatalogRebinds {
		bindings = append(bindings, binding.StorageEntryID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT to_jsonb(o), to_jsonb(r), COALESCE((
	 o.source_node_id=r.node_id AND o.source_node_key=r.node_key
	 AND o.project_id IS NOT DISTINCT FROM r.project_id
	 AND o.metadata->'source_root'->>'notes_source_root_id'=r.notes_source_root_id
	 AND o.metadata->'source_root'->>'node_key'=r.node_key
	 AND o.metadata->'source_root'->>'backend_root_key'=r.backend_root_key
	 AND o.metadata->'source_root'->>'source_path'=r.source_path
	 AND o.metadata->'source_root'->>'root_relative_path'=r.root_relative_path
	 AND COALESCE(o.metadata->'source_root'->'knowledge_source','null'::jsonb)
	     = COALESCE(`+notesEffectiveDeclarationSQL("r", "o", "$7::text")+`,'null'::jsonb)
	 AND ((o.storage_entry_id IS NULL AND EXISTS (
	 SELECT 1 FROM files.file_metadata f JOIN objects.objects obj USING(object_id)
	 JOIN objects.object_versions v ON v.object_version_id=f.latest_version_id AND v.object_id=obj.object_id
	 JOIN sync.replicas rep ON rep.replica_id=o.metadata->'synced_object'->>'replica_id'
	 JOIN objects.object_scope_links link ON link.object_id=obj.object_id
	 JOIN scopes.scopes scope ON scope.scope_id=link.scope_id
	 WHERE obj.object_id=o.metadata->'synced_object'->>'object_id'
	 AND v.object_version_id=o.metadata->'synced_object'->>'object_version_id'
	 AND obj.object_type='file' AND obj.status='active' AND v.status='active'
	 AND v.content_hash=o.source_hash AND v.size_bytes=o.size_bytes
	 AND f.source_node_id=r.node_id AND v.source_node_id=r.node_id AND rep.source_node_id=r.node_id
	 AND rep.replicated_kind='object_version' AND rep.replicated_id=v.object_version_id AND rep.freshness_state='fresh'
	 AND v.source_path='watched-root://' || r.backend_root_key || '/' || o.relative_path
	 AND f.source_path=v.source_path AND link.relevance_status='active'
	 AND scope.scope_key=o.metadata->'synced_object'->>'scope_key'
	 AND ((r.project_id IS NOT NULL AND EXISTS (SELECT 1 FROM projects.projects project
	      WHERE project.project_id=r.project_id AND project.project_scope_id=scope.scope_id))
	 OR (r.project_id IS NULL AND scope.scope_type='box_area' AND scope.home_node_id=r.node_id
	     AND scope.scope_key='loom_box:' || (r.metadata->>'box_id') || ':' || substring(r.root_kind from 5)
	     AND scope.metadata->>'owner_node_id'=r.node_id AND scope.metadata->>'owner_node_key'=r.node_key
	     AND scope.metadata->>'box_id'=r.metadata->>'box_id' AND scope.metadata->>'box_area'=substring(r.root_kind from 5)))
	 AND f.metadata=COALESCE(o.metadata->'file_metadata','{}'::jsonb)
	 AND obj.metadata=COALESCE(o.metadata->'object_metadata','{}'::jsonb)
	 AND v.metadata=COALESCE(o.metadata->'version_metadata','{}'::jsonb)
	 )) OR (o.storage_entry_id IS NOT NULL AND EXISTS (
	 SELECT 1 FROM storage.storage_entries entry WHERE entry.storage_entry_id=o.storage_entry_id
	 AND entry.origin_node_key=r.node_key AND entry.project_id IS NOT DISTINCT FROM r.project_id
	 AND entry.watched_root_key=r.backend_root_key AND entry.deleted_at IS NULL
	 AND entry.checksum_algorithm='sha256' AND 'sha256:' || entry.checksum_hex=o.source_hash
	 AND entry.size_bytes=o.size_bytes AND entry.metadata=COALESCE(o.metadata->'storage_metadata','{}'::jsonb)
	 )))),false)
	 FROM knowledge.knowledge_objects o JOIN knowledge.notes_source_roots r USING(notes_source_root_id)
	 JOIN nodes.nodes node ON node.node_id=r.node_id AND node.node_key=r.node_key
	 WHERE r.node_key=$1 AND node.node_role='main'
	 AND (((starts_with(o.source_path,$2 || '/') OR starts_with(r.source_path || '/' || o.relative_path,$2 || '/')
	 OR o.storage_entry_id=ANY($6::text[])) AND o.created_at <= $3) OR EXISTS (
	 SELECT 1 FROM knowledge.notes_custody_transitions t WHERE t.knowledge_object_id=o.knowledge_object_id AND t.archive_operation_id=$4))
	 ORDER BY o.knowledge_object_id LIMIT $5 FOR UPDATE OF o FOR SHARE OF r`, p.NodeKey,
		evidence.ArchivePlan.Source.Path.AbsolutePath, evidence.Manifest.ArchivedAt, evidence.ArchivePlan.OperationID, maxNotesCustodyObjects+1, bindings, evidence.ArchivePlan.OperationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []notesCustodyCandidate
	for rows.Next() {
		var candidate notesCustodyCandidate
		var objectJSON, rootJSON []byte
		if err := rows.Scan(&objectJSON, &rootJSON, &candidate.Bound); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(objectJSON, &candidate.Object); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(rootJSON, &candidate.Root); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) > maxNotesCustodyObjects {
		return nil, fmt.Errorf("Notes custody candidate bound exceeded")
	}
	return candidates, rows.Err()
}

func (p NotesArchiveProjector) projectNotesObject(ctx context.Context, tx *sql.Tx, e storagearchive.WorkspaceLifecycleEvidence, projectEvent string, c notesCustodyCandidate, inventory map[string]storagearchive.NoFollowInventoryEntry) (bool, error) {
	object, root := c.Object, c.Root
	fail := func() (bool, error) {
		return false, fmt.Errorf("Notes custody evidence is missing, changed or out of order for %s", object.KnowledgeObjectID)
	}
	rel, err := filepath.Rel(e.ArchivePlan.Source.Path.AbsolutePath, object.SourcePath)
	if err != nil {
		return fail()
	}
	rel = filepath.ToSlash(rel)
	transition := NotesCustodyTransition{KnowledgeObjectID: object.KnowledgeObjectID,
		WorkspaceLifecycleEventID: e.Event.EventID, ArchiveOperationID: e.ArchivePlan.OperationID,
		ProjectEventID: projectEvent, OriginalPath: object.SourcePath,
		CanonicalPath:         filepath.Join(e.Plan.Destination.Path.AbsolutePath, filepath.FromSlash(rel)),
		WorkspaceRelativePath: rel, ManifestDigest: e.ManifestDigest}
	existing, found, err := loadNotesTransition(ctx, tx, object.KnowledgeObjectID, e.Event.EventID)
	if err != nil {
		return false, err
	}
	if found {
		transition.PreviousEventID = existing.PreviousEventID
		transition.TransitionDigest = NotesCustodyTransitionDigest(transition)
		if ValidateNotesCustodyTransition(existing) != nil || !reflect.DeepEqual(existing, transition) {
			return fail()
		}
		var currentChain bool
		if err := tx.QueryRowContext(ctx, `WITH RECURSIVE chain AS (
		 SELECT t.workspace_lifecycle_event_id,t.previous_event_id,1 AS depth
		 FROM knowledge.notes_current_custody c JOIN knowledge.notes_custody_transitions t
		 ON t.knowledge_object_id=c.knowledge_object_id AND t.workspace_lifecycle_event_id=c.workspace_lifecycle_event_id
		 WHERE c.knowledge_object_id=$1 AND NOT EXISTS (SELECT 1 FROM knowledge.notes_custody_transitions child
		 WHERE child.knowledge_object_id=$1 AND child.previous_event_id=c.workspace_lifecycle_event_id)
		 UNION ALL SELECT t.workspace_lifecycle_event_id,t.previous_event_id,chain.depth+1
		 FROM chain JOIN knowledge.notes_custody_transitions t ON t.knowledge_object_id=$1
		 AND t.workspace_lifecycle_event_id=chain.previous_event_id WHERE chain.depth<1000
		 ) SELECT EXISTS(SELECT 1 FROM chain WHERE workspace_lifecycle_event_id=$2)`, object.KnowledgeObjectID, e.Event.EventID).Scan(&currentChain); err != nil {
			return false, err
		}
		if !currentChain {
			return fail()
		}
		// A historical replay must not replace a later current pointer.
		return true, nil
	}
	entry, covered := inventory[rel]
	if !c.Bound || !covered || entry.Kind != storagearchive.InventoryEntryFile ||
		entry.ContentDigest != object.SourceHash || object.SizeBytes == nil || entry.SizeBytes != *object.SizeBytes ||
		object.SourcePath != filepath.Join(root.SourcePath, filepath.FromSlash(object.RelativePath)) ||
		!notesCustodyRelativePath(object.RelativePath) || object.DeletedAt != nil {
		return fail()
	}
	switch e.Plan.Kind {
	case storagearchive.WorkspaceKindProject:
		if valueOrEmpty(root.ProjectID) != e.Plan.ObjectID || (root.RootKind != RootKindProjectNotes && root.RootKind != RootKindProjectMaterial) ||
			root.SourcePath != filepath.Join(e.ArchivePlan.Source.Path.AbsolutePath, filepath.FromSlash(root.RootRelativePath)) {
			return fail()
		}
	case storagearchive.WorkspaceKindTopic, storagearchive.WorkspaceKindLibraryItem:
		want := RootKindBoxTopics
		if e.Plan.Kind == storagearchive.WorkspaceKindLibraryItem {
			want = RootKindBoxLibrary
		}
		if root.RootKind != want || root.ProjectID != nil || root.SourcePath != filepath.Join(p.Workspace.Roots.BoxRoot, filepath.FromSlash(root.RootRelativePath)) {
			return fail()
		}
	default:
		return fail()
	}
	if object.StorageEntryID != nil {
		matched := false
		for _, binding := range e.ArchivePlan.CatalogRebinds {
			if binding.StorageEntryID != *object.StorageEntryID {
				continue
			}
			for _, path := range []string{binding.ExpectedOriginalPath, binding.ExpectedCurrentViewPath, binding.ExpectedURI} {
				if path == object.SourcePath || path == filepath.ToSlash(filepath.Join(e.ArchivePlan.Source.Path.RelativePath, rel)) ||
					path == (&url.URL{Scheme: "file", Path: object.SourcePath}).String() {
					matched = true
				}
			}
		}
		if !matched {
			return fail()
		}
	}
	var currentID, lifecycle, currentArchive, canonical string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(workspace_lifecycle_event_id,''),source_lifecycle,
	 COALESCE(archive_operation_id,''),canonical_path FROM knowledge.notes_object_custody WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&currentID, &lifecycle, &currentArchive, &canonical)
	if err != nil {
		return false, err
	}
	if canonical != filepath.Join(e.Plan.Source.Path.AbsolutePath, filepath.FromSlash(rel)) ||
		(e.Plan.OperationKind == storagearchive.WorkspaceOperationArchive && lifecycle != "active") ||
		(e.Plan.OperationKind == storagearchive.WorkspaceOperationRestore && (lifecycle != "archived" || currentArchive != e.ArchivePlan.OperationID || currentID == "")) {
		return fail()
	}
	transition.PreviousEventID = currentID
	transition.TransitionDigest = NotesCustodyTransitionDigest(transition)
	if err := ValidateNotesCustodyTransition(transition); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO knowledge.notes_custody_transitions
	 (knowledge_object_id,workspace_lifecycle_event_id,archive_operation_id,project_event_id,previous_event_id,
	 original_path,canonical_path,workspace_relative_path,manifest_digest,transition_digest)
	 VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8,$9,$10)`, transition.KnowledgeObjectID,
		transition.WorkspaceLifecycleEventID, transition.ArchiveOperationID, transition.ProjectEventID, transition.PreviousEventID,
		transition.OriginalPath, transition.CanonicalPath, transition.WorkspaceRelativePath, transition.ManifestDigest, transition.TransitionDigest)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO knowledge.notes_current_custody(knowledge_object_id,workspace_lifecycle_event_id)
	 VALUES ($1,$2) ON CONFLICT(knowledge_object_id) DO UPDATE SET workspace_lifecycle_event_id=EXCLUDED.workspace_lifecycle_event_id`, object.KnowledgeObjectID, e.Event.EventID)
	return false, err
}

func notesCustodyRelativePath(path string) bool {
	return notesCustodyText(path, 4096) && !filepath.IsAbs(path) && filepath.ToSlash(filepath.Clean(path)) == path &&
		path != "." && path != ".." && !strings.HasPrefix(path, "../") && !strings.Contains(path, "\\")
}

func loadNotesTransition(ctx context.Context, tx *sql.Tx, objectID, eventID string) (NotesCustodyTransition, bool, error) {
	var transition NotesCustodyTransition
	err := tx.QueryRowContext(ctx, `SELECT knowledge_object_id,workspace_lifecycle_event_id,archive_operation_id,
	 COALESCE(project_event_id,''),COALESCE(previous_event_id,''),original_path,canonical_path,workspace_relative_path,manifest_digest,transition_digest
	 FROM knowledge.notes_custody_transitions WHERE knowledge_object_id=$1 AND workspace_lifecycle_event_id=$2`, objectID, eventID).Scan(
		&transition.KnowledgeObjectID, &transition.WorkspaceLifecycleEventID, &transition.ArchiveOperationID,
		&transition.ProjectEventID, &transition.PreviousEventID, &transition.OriginalPath, &transition.CanonicalPath,
		&transition.WorkspaceRelativePath, &transition.ManifestDigest, &transition.TransitionDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return transition, false, nil
	}
	return transition, err == nil, err
}

func notesProjectCompletion(ctx context.Context, tx *sql.Tx, e storagearchive.WorkspaceLifecycleEvidence) (string, error) {
	if e.Plan.Kind != storagearchive.WorkspaceKindProject {
		return "", nil
	}
	var raw []byte
	var scopeID string
	if err := tx.QueryRowContext(ctx, `SELECT archive_state,project_scope_id FROM projects.projects WHERE project_id=$1 FOR SHARE`, e.Plan.ObjectID).Scan(&raw, &scopeID); err != nil {
		return "", fmt.Errorf("Notes custody requires project completion: %w", err)
	}
	state, valid := projects.ParseProjectPhysicalArchiveState(raw)
	if !valid || state.Phase != projects.ProjectArchivePhaseComplete || state.ProjectID != e.Plan.ObjectID ||
		state.ProjectSlug != e.Plan.Slug || state.OperationID != e.ArchivePlan.OperationID ||
		state.WorkspacePlanDigest != e.ArchivePlan.PlanDigest || state.ActivePath != e.ArchivePlan.Source.Path.AbsolutePath ||
		state.ArchivePath != e.ArchivePlan.Destination.Path.AbsolutePath || state.ActorID != e.ArchivePlan.ActorID || state.Reason != e.ArchivePlan.Reason {
		return "", fmt.Errorf("Notes custody project completion is missing or inconsistent")
	}
	payload := map[string]any{"project_id": state.ProjectID, "project_slug": state.ProjectSlug,
		"archive_operation_id": state.OperationID, "plan_digest": state.PlanDigest, "workspace_plan_digest": state.WorkspacePlanDigest,
		"archive_path": state.ArchivePath, "archive_manifest_digest": state.ArchiveManifestDigest, "source": "project.physical_archive"}
	eventType, status, actor, expectedID := "project.archived", "archived", state.ActorID, ""
	originNode, correlation := "", ""
	if e.Plan.OperationKind == storagearchive.WorkspaceOperationRestore {
		r := state.Restore
		if r == nil || r.Phase != projects.ProjectRestorePhaseComplete || r.OperationID != e.Plan.OperationID ||
			r.WorkspacePlanDigest != e.Plan.PlanDigest || r.ActiveManifestDigest != e.ManifestDigest || state.ArchiveManifestDigest != e.Plan.ArchiveManifestDigest || r.Request.ActorID != e.Plan.ActorID {
			return "", fmt.Errorf("Notes custody project restore completion is missing or inconsistent")
		}
		payload = map[string]any{"project_id": state.ProjectID, "project_slug": state.ProjectSlug,
			"archive_operation_id": state.OperationID, "restore_operation_id": r.OperationID,
			"plan_digest": r.PlanDigest, "workspace_plan_digest": r.WorkspacePlanDigest,
			"archive_manifest_digest": state.ArchiveManifestDigest, "active_manifest_digest": r.ActiveManifestDigest,
			"active_path": state.ActivePath, "activation_state": r.ActivationState, "restored_at": r.RestoredAt,
			"source": "project.physical_restore"}
		eventType, status, actor, expectedID = "project.restored", "restored", r.Request.ActorID, r.EventID
		originNode, correlation = r.Request.OriginNodeID, r.Request.CorrelationID
	} else if state.ArchiveManifestDigest != e.ManifestDigest {
		return "", fmt.Errorf("Notes custody project archive manifest differs")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_id FROM events.events WHERE event_type=$1 AND status=$2
	 AND target_kind='project' AND target_id=$3 AND scope_id=$4 AND actor_id=$5
	 AND result='ok' AND event_level='audit' AND visibility_class='internal' AND payload=$6::jsonb
	 AND ($7::text='' OR event_id=$7) AND ($8::text='' OR origin_node_id=$8)
	 AND ($9::text='' OR correlation_id=$9) ORDER BY event_id LIMIT 2 FOR SHARE`, eventType, status, state.ProjectID, scopeID, actor, data, expectedID, originNode, correlation)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var matches []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		matches = append(matches, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("Notes custody requires one exact completed project event")
	}
	return matches[0], nil
}
