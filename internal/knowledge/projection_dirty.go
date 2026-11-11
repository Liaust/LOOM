package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/workers"
)

// ProjectionDirtyInput names authoritative identities, never payloads. Mark in
// the mutation's transaction; a rollback must roll back both mutation and mark.
// RootID expands only objects belonging to that root, including withdrawn rows.
type ProjectionDirtyInput struct {
	ObjectIDs     []string
	RootID        string
	FullReconcile bool
}

func MarkProjectionDirtyTx(ctx context.Context, tx *sql.Tx, input ProjectionDirtyInput) error {
	if tx == nil {
		return fmt.Errorf("projection marking requires the owner transaction")
	}
	ids := make([]string, 0, len(input.ObjectIDs))
	for _, id := range input.ObjectIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("empty projection object identity")
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 && input.RootID == "" && !input.FullReconcile {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO knowledge.notes_projection_dirty AS d (dirty_key,knowledge_object_id)
 SELECT dirty_key,knowledge_object_id FROM (
 SELECT 'object:' || id AS dirty_key,id AS knowledge_object_id FROM (
   SELECT unnest($1::text[]) AS id UNION
   SELECT knowledge_object_id FROM knowledge.knowledge_objects WHERE notes_source_root_id=$2
 ) objects
 UNION ALL SELECT 'full',NULL WHERE $3
 ) marks ORDER BY dirty_key
 ON CONFLICT (dirty_key) DO UPDATE SET sequence=d.sequence+1,
 waiting_since=CASE WHEN d.sequence=d.acknowledged_sequence THEN now() ELSE d.waiting_since END,
 retry_at=now(),updated_at=now()`, ids, input.RootID, input.FullReconcile)
	if err != nil {
		return err
	}
	if input.RootID != "" && !input.FullReconcile {
		// Real root-owner changes can repair a pending structural refusal. Do
		// not turn every root update into a new full scan when none is pending.
		if _, err := tx.ExecContext(ctx, `UPDATE knowledge.notes_projection_dirty SET sequence=sequence+1,retry_at=now(),updated_at=now()
		 WHERE dirty_key='full' AND sequence>acknowledged_sequence`); err != nil {
			return err
		}
	}
	return workers.NotifyQueueTx(ctx, tx, notesprojection.WorkerKind)
}

func (s *Service) PendingProjectionDirty(ctx context.Context, now time.Time, limit int) ([]notesprojection.DirtyChange, error) {
	if s == nil || s.store.db == nil {
		return nil, fmt.Errorf("projection dirty store is not configured")
	}
	if limit <= 0 || limit > notesprojection.MaxDeltaIdentities {
		limit = notesprojection.MaxDeltaIdentities
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT dirty_key,COALESCE(knowledge_object_id,''),sequence
 FROM knowledge.notes_projection_dirty WHERE sequence>acknowledged_sequence AND retry_at<=$1
	ORDER BY (dirty_key='full') DESC,waiting_since,dirty_key LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var changes []notesprojection.DirtyChange
	for rows.Next() {
		var c notesprojection.DirtyChange
		if err := rows.Scan(&c.Key, &c.ObjectID, &c.Sequence); err != nil {
			return nil, err
		}
		changes = append(changes, c)
	}
	return changes, rows.Err()
}

// An empty probe is read-only. Notifications are hints; this deadline also
// recovers missed wakes and work left pending by a killed consumer.
func (s *Service) NextProjectionDirtyAt(ctx context.Context) (*time.Time, error) {
	if s == nil || s.store.db == nil {
		return nil, fmt.Errorf("projection dirty store is not configured")
	}
	var next sql.NullTime
	err := s.store.db.QueryRowContext(ctx, `SELECT min(retry_at) FROM knowledge.notes_projection_dirty WHERE sequence>acknowledged_sequence`).Scan(&next)
	if err != nil || !next.Valid {
		return nil, err
	}
	return &next.Time, nil
}

// Only the exact selected sequence is acknowledged/deferred. A new mark during
// payload work resets retry_at and remains pending, including after restart.
func (s *Service) FinishProjectionDirty(ctx context.Context, change notesprojection.DirtyChange, retryAt *time.Time) error {
	return s.FinishProjectionDirtyBatch(ctx, []notesprojection.DirtyCompletion{{Change: change, RetryAt: retryAt}})
}

func (s *Service) FinishProjectionDirtyBatch(ctx context.Context, completions []notesprojection.DirtyCompletion) error {
	if s == nil || s.store.db == nil {
		return fmt.Errorf("projection dirty store is not configured")
	}
	if len(completions) > notesprojection.MaxDeltaIdentities {
		return fmt.Errorf("invalid projection dirty checkpoint")
	}
	if len(completions) == 0 {
		return nil
	}
	type completion struct {
		Key      string     `json:"dirty_key"`
		Sequence int64      `json:"sequence"`
		RetryAt  *time.Time `json:"retry_at"`
	}
	rows := make([]completion, 0, len(completions))
	seen := make(map[string]bool, len(completions))
	for _, item := range completions {
		if item.Change.Key == "" || item.Change.Sequence <= 0 || seen[item.Change.Key] {
			return fmt.Errorf("invalid projection dirty checkpoint")
		}
		seen[item.Change.Key] = true
		rows = append(rows, completion{Key: item.Change.Key, Sequence: item.Change.Sequence, RetryAt: item.RetryAt})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	// One commit, ordered like producer marks, with exact sequence fencing on
	// both lock acquisition and update. No stale batch may defer a newer edit.
	_, err = s.store.db.ExecContext(ctx, `WITH selected AS MATERIALIZED (
	 SELECT d.dirty_key,c.sequence,c.retry_at FROM knowledge.notes_projection_dirty d
	 JOIN jsonb_to_recordset($1::jsonb) AS c(dirty_key text,sequence bigint,retry_at timestamptz)
	 ON c.dirty_key=d.dirty_key
	 WHERE d.sequence=c.sequence AND d.sequence>d.acknowledged_sequence
	 ORDER BY d.dirty_key FOR UPDATE OF d
	) UPDATE knowledge.notes_projection_dirty d SET
	 acknowledged_sequence=CASE WHEN c.retry_at IS NULL THEN c.sequence ELSE d.acknowledged_sequence END,
	 retry_at=COALESCE(c.retry_at,d.retry_at)
	 FROM selected c WHERE d.dirty_key=c.dirty_key AND d.sequence=c.sequence
	 AND d.sequence>d.acknowledged_sequence`, raw)
	return err
}

func (s *Service) RequireProjectionReconcile(ctx context.Context) error {
	if s == nil || s.store.db == nil {
		return fmt.Errorf("projection dirty store is not configured")
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Ensure an outstanding repair without incrementing/debouncing one already
	// pending. Repeated uncertainty cannot bypass its failure retry deadline.
	result, err := tx.ExecContext(ctx, `INSERT INTO knowledge.notes_projection_dirty AS d (dirty_key) VALUES ('full')
	 ON CONFLICT (dirty_key) DO UPDATE SET sequence=d.sequence+1,waiting_since=now(),retry_at=now(),updated_at=now()
	 WHERE d.sequence=d.acknowledged_sequence`)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		if err := workers.NotifyQueueTx(ctx, tx, notesprojection.WorkerKind); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Only a verified changed dependency may expedite this exact retained failure.
// A concurrent newer full mark keeps its own immediate eligibility/sequence.
func (s *Service) WakeProjectionReconcile(ctx context.Context, sequence int64) error {
	if s == nil || s.store.db == nil || sequence <= 0 {
		return fmt.Errorf("invalid projection repair wake")
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE knowledge.notes_projection_dirty SET retry_at=now(),updated_at=now()
	 WHERE dirty_key='full' AND sequence=$1 AND sequence>acknowledged_sequence AND retry_at>now()`, sequence)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		if err := workers.NotifyQueueTx(ctx, tx, notesprojection.WorkerKind); err != nil {
			return err
		}
	}
	return tx.Commit()
}
