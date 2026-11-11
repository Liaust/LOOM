package knowledge

import (
	"context"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

// Pending catalog identities are navigation, not permission to admit stale bytes.
func (s *Service) admitPendingCatalog(ctx context.Context, roots []SourceRoot, limit int, result *AdmissionResult) (bool, error) {
	started := time.Now()
	rows, err := s.store.db.QueryContext(ctx, `SELECT storage_entry_id,revision
 FROM knowledge.pending_catalog_admissions WHERE next_attempt_at<=now()
 ORDER BY next_attempt_at,received_at,storage_entry_id LIMIT $1`, limit)
	if err != nil {
		return false, err
	}
	var ids []string
	var revisions []int64
	for rows.Next() {
		var id string
		var revision int64
		if err := rows.Scan(&id, &revision); err != nil {
			rows.Close()
			return false, err
		}
		ids, revisions = append(ids, id), append(revisions, revision)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) == 0 {
		return false, err
	}
	current, err := s.store.currentCatalogAdmissionIDs(ctx, ids)
	if err != nil {
		return true, err
	}
	details, err := storagecatalog.NewService(s.store.db).ListEntryDetails(ctx, current)
	if err != nil {
		return true, err
	}
	entries := make([]storagecatalog.Entry, 0, len(current))
	for _, id := range current {
		if entry, ok := details[id]; ok {
			entries = append(entries, entry.Entry)
		}
	}
	result.ElapsedMS["pending_catalog_selection"] = time.Since(started).Milliseconds()
	started = time.Now()
	applied, err := s.ReconcileKnowledgeObjects(ctx, KnowledgeObjectReconcileInput{SourceRoots: roots, StorageEntries: entries})
	if err != nil {
		return true, err
	}
	result.ElapsedMS["pending_catalog_reconciliation"] = time.Since(started).Milliseconds()
	result.CatalogObserved, result.Applied, result.Skipped = len(entries), applied.Applied, len(applied.Skipped)
	result.Changed, result.Unchanged = applied.Changed, applied.Unchanged
	paused := false
	for _, skip := range applied.Skipped {
		paused = paused || skip.Reason == "source_custody_paused"
	}
	if err := s.acknowledgePendingCatalog(ctx, ids, revisions, paused); err != nil {
		return true, err
	}
	result.InventoryDeferred, result.MoreWork = true, true
	return true, nil
}

func (s *Service) acknowledgePendingCatalog(ctx context.Context, ids []string, revisions []int64, paused bool) error {
	query := `DELETE FROM knowledge.pending_catalog_admissions p USING unnest($1::text[],$2::bigint[]) seen(id,revision)
 WHERE p.storage_entry_id=seen.id AND p.revision=seen.revision`
	if paused {
		query = `UPDATE knowledge.pending_catalog_admissions p SET next_attempt_at=now()+interval '30 seconds'
 FROM unnest($1::text[],$2::bigint[]) seen(id,revision) WHERE p.storage_entry_id=seen.id AND p.revision=seen.revision`
	}
	_, err := s.store.db.ExecContext(ctx, query, ids, revisions)
	return err
}

func (s Store) currentCatalogAdmissionIDs(ctx context.Context, ids []string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.storage_entry_id FROM storage.storage_entries p
 CROSS JOIN LATERAL (`+preferredCatalogEntrySQL+`) preferred
 WHERE p.storage_entry_id=ANY($1::text[]) AND p.storage_entry_id=preferred.storage_entry_id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var current []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		current = append(current, id)
	}
	return current, rows.Err()
}
