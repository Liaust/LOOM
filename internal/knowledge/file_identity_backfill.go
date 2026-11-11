package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/requestctx"
)

type FileIdentityReconcileInput struct {
	After   string `json:"after,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}
type FileIdentityMapping struct {
	RootID   string `json:"root_id"`
	Path     string `json:"path"`
	FileID   string `json:"file_id,omitempty"`
	Presence string `json:"presence"`
	Reason   string `json:"reason,omitempty"`
}
type FileIdentityReconcileResult struct {
	DryRun    bool                  `json:"dry_run"`
	Mappings  []FileIdentityMapping `json:"mappings"`
	NextAfter string                `json:"next_after,omitempty"`
	Applied   int                   `json:"applied"`
}

// This bounded metadata transition consumes existing source inventory/Notes
// paths and verifies the actual filesystem. Inventory absence is not proof of
// deletion, and no bytes, enrichment jobs or producer counters are created.
func (s *Service) ReconcileFileIdentities(ctx context.Context, input FileIdentityReconcileInput) (FileIdentityReconcileResult, error) {
	observer, ok := s.fileSourceReader.(FileSourceObserver)
	if !ok {
		return FileIdentityReconcileResult{}, ErrFileSourceUnavailable
	}
	if input.Limit == 0 {
		input.Limit = 100
	}
	if input.Limit < 1 || input.Limit > 200 {
		return FileIdentityReconcileResult{}, fmt.Errorf("%w: limit must be 1..200", ErrInvalid)
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT r.notes_source_root_id,r.node_key,c.relative_path FROM (
	 SELECT notes_source_root_id,relative_path FROM knowledge.knowledge_objects WHERE deleted_at IS NULL
	 UNION SELECT collection_id,relative_path FROM notes_workspace.source_inventory) c
	 JOIN knowledge.notes_source_roots r USING(notes_source_root_id)
	 WHERE r.status='active' AND (r.notes_source_root_id||'/'||c.relative_path) COLLATE "C">$1 COLLATE "C"
	 ORDER BY r.notes_source_root_id COLLATE "C",c.relative_path COLLATE "C" LIMIT $2`, input.After, input.Limit+1)
	if err != nil {
		return FileIdentityReconcileResult{}, err
	}
	type candidate struct{ root, node, relative string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.root, &c.node, &c.relative); err != nil {
			rows.Close()
			return FileIdentityReconcileResult{}, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return FileIdentityReconcileResult{}, err
	}
	out := FileIdentityReconcileResult{DryRun: !input.Confirm, Mappings: []FileIdentityMapping{}}
	if len(candidates) > input.Limit {
		candidates = candidates[:input.Limit]
		last := candidates[len(candidates)-1]
		out.NextAfter = last.root + "/" + last.relative
	}
	for _, c := range candidates {
		mapping := FileIdentityMapping{RootID: c.root, Presence: "unavailable"}
		err = s.WithNotesWorkspaceRoot(ctx, c.root, c.node, func(root SourceRoot, generation string) error {
			mapping.Path = filepath.Join(root.SourcePath, filepath.FromSlash(c.relative))
			if c.relative == "" || filepath.Clean(c.relative) != c.relative || filepath.IsAbs(c.relative) || c.relative == ".." || strings.HasPrefix(c.relative, "../") || strings.ContainsAny(c.relative, "\\\x00") || filepolicy.IsIndexingExcludedPath(c.relative) || ignoredNotesKnowledgeRelativePath(c.relative) || expandedSourceExcluded(root, c.relative, nil) {
				mapping.Reason = "source_excluded"
				return nil
			}
			for _, part := range strings.Split(c.relative, "/") {
				if strings.HasPrefix(part, ".") {
					mapping.Reason = "source_excluded"
					return nil
				}
			}
			observation, e := observer.ObserveNotesFile(ctx, c.node, root.SourcePath, c.relative)
			if e != nil {
				return e
			}
			mapping.Presence = observation.Presence
			if !input.Confirm {
				return nil
			}
			tx, e := s.store.db.BeginTx(ctx, nil)
			if e != nil {
				return e
			}
			defer tx.Rollback()
			var scope string
			if e = tx.QueryRowContext(ctx, `SELECT scope_id FROM scopes.scopes sc JOIN knowledge.notes_source_roots r ON r.notes_source_root_id=$1 WHERE sc.status='active' AND `+notesSyncedScopeSQL("r", "sc")+` LIMIT 1`, c.root).Scan(&scope); e != nil {
				return e
			}
			id, e := objects.AdoptLegacyFileTx(ctx, tx, requestctx.Context{}, objects.FileObservation{NodeID: derefString(root.NodeID), RootKey: root.BackendRootKey, RelativePath: c.relative, Generation: generation, SourcePath: "watched-root://" + root.BackendRootKey + "/" + c.relative, ManagedPath: mapping.Path, ScopeID: scope, Presence: observation.Presence, ObservedAt: s.currentTime(), ModifiedAt: observation.ModifiedAt, SizeBytes: observation.Size, InitialIndexPolicy: "metadata_only", InitialRawBackupPolicy: "normal"})
			if errors.Is(e, objects.ErrFileObservationConflict) {
				mapping.Reason = "already_observed"
				// Keep the producer's newer observation, but attach legacy
				// membership/output to its exact existing physical identity.
				e = tx.QueryRowContext(ctx, `SELECT object_id FROM files.file_metadata WHERE source_node_id=$1 AND managed_source_path=$2`, derefString(root.NodeID), mapping.Path).Scan(&id)
			}
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE knowledge.knowledge_objects SET canonical_file_id=$3 WHERE notes_source_root_id=$1 AND relative_path=$2 AND deleted_at IS NULL AND canonical_file_id IS DISTINCT FROM $3`, c.root, c.relative, id); e != nil {
				return e
			}
			if e = attachCurrentFileDocumentsTx(ctx, tx, id); e != nil {
				return e
			}
			if e = tx.Commit(); e != nil {
				return e
			}
			mapping.FileID = id
			out.Applied++
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			if errors.Is(err, ErrFileSourceNotRegular) {
				mapping.Reason = "not_regular_file"
			} else if errors.Is(err, ErrNotesCustodyPaused) || errors.Is(err, ErrFileSourceUnavailable) {
				mapping.Reason = "source_admission_unavailable"
			} else {
				return out, err
			}
		}
		out.Mappings = append(out.Mappings, mapping)
	}
	return out, nil
}

func attachCurrentFileDocumentsTx(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, id string) error {
	// Resolve this file's source IDs first so attachment uses the existing
	// (source_kind, source_id) index instead of scanning every search document.
	_, err := tx.ExecContext(ctx, `WITH owned_sources AS MATERIALIZED (
	 SELECT 'knowledge_object_metadata'::text source_kind,ko.knowledge_object_id source_id
	 FROM knowledge.knowledge_objects ko WHERE ko.canonical_file_id=$1
	 UNION ALL SELECT 'knowledge_chunk',c.knowledge_chunk_id
	 FROM knowledge.knowledge_objects ko JOIN knowledge.knowledge_chunks c USING(knowledge_object_id)
	 WHERE ko.canonical_file_id=$1)
	 UPDATE search.search_documents sd SET object_id=$1,scope_id=o.home_scope_id
	 FROM owned_sources src,objects.objects o
	 WHERE o.object_id=$1 AND sd.source_kind=src.source_kind AND sd.source_id=src.source_id
	 AND (sd.object_id IS DISTINCT FROM $1 OR sd.scope_id IS DISTINCT FROM o.home_scope_id)`, id)
	return err
}
