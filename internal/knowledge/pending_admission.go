package knowledge

import (
	"context"
	"fmt"
	"time"
)

// PendingSourceAdmissionAt is also a restart-safe readiness signal. Notifications
// only shorten the wait; they are not the work record or an authorization grant.
func (s *Service) PendingSourceAdmissionAt(ctx context.Context) (*time.Time, error) {
	var at *time.Time
	err := s.store.db.QueryRowContext(ctx, `SELECT min(next_attempt_at) FROM (
 SELECT next_attempt_at FROM knowledge.pending_source_admissions
 UNION ALL SELECT next_attempt_at FROM knowledge.pending_catalog_admissions) pending`).Scan(&at)
	return at, err
}

func (s *Service) admitPendingSources(ctx context.Context, roots []SourceRoot, limit int, result *AdmissionResult) (bool, error) {
	started := time.Now()
	rows, err := s.store.db.QueryContext(ctx, `SELECT object_version_id,revision FROM knowledge.pending_source_admissions
  WHERE next_attempt_at<=now() ORDER BY next_attempt_at,received_at,object_version_id LIMIT $1`, limit)
	if err != nil {
		return false, err
	}
	versions := []string{}
	revisions := []int64{}
	for rows.Next() {
		var id string
		var revision int64
		if err := rows.Scan(&id, &revision); err != nil {
			rows.Close()
			return false, err
		}
		versions = append(versions, id)
		revisions = append(revisions, revision)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(versions) == 0 {
		return false, err
	}
	entries, err := s.store.listSyncedNotesVersions(ctx, "", [3]string{}, 0, false, false, versions)
	result.ElapsedMS["pending_selection"] = time.Since(started).Milliseconds()
	if err != nil {
		return true, err
	}
	started = time.Now()
	applied, err := s.ReconcileKnowledgeObjects(ctx, KnowledgeObjectReconcileInput{SourceRoots: roots, SyncedObjects: entries})
	result.ElapsedMS["pending_reconciliation"] = time.Since(started).Milliseconds()
	if err != nil {
		return true, err
	}
	result.PrioritySyncedObserved = len(entries)
	result.PriorityApplied, result.Applied, result.Skipped = applied.Applied, applied.Applied, len(applied.Skipped)
	result.Changed, result.Unchanged = applied.Changed, applied.Unchanged
	for _, run := range applied.PipelineRuns {
		if run.CurrentExecutionClass == PipelineExecutionCoordinator {
			result.PreferredPipelineRunID = run.KnowledgePipelineRunID
			break
		}
	}
	paused := false
	for _, skip := range applied.Skipped {
		paused = paused || skip.Reason == "source_custody_paused"
	}
	err = s.acknowledgePendingSources(ctx, versions, revisions, paused)
	if err != nil {
		return true, fmt.Errorf("acknowledge Notes source admission: %w", err)
	}
	next, err := s.PendingSourceAdmissionAt(ctx)
	result.PriorityMoreWork = next != nil && !next.After(time.Now())
	result.InventoryDeferred, result.MoreWork = true, true
	return true, err
}

func (s *Service) acknowledgePendingSources(ctx context.Context, versions []string, revisions []int64, paused bool) error {
	query := `DELETE FROM knowledge.pending_source_admissions p USING unnest($1::text[],$2::bigint[]) AS seen(version_id,revision) WHERE p.object_version_id=seen.version_id AND p.revision=seen.revision`
	if paused {
		query = `UPDATE knowledge.pending_source_admissions p SET next_attempt_at=now()+interval '30 seconds' FROM unnest($1::text[],$2::bigint[]) AS seen(version_id,revision) WHERE p.object_version_id=seen.version_id AND p.revision=seen.revision`
	}
	_, err := s.store.db.ExecContext(ctx, query, versions, revisions)
	return err
}
