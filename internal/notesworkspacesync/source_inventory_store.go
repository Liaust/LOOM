package notesworkspacesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type sqlInventoryRecords struct {
	tx    *sql.Tx
	scope InventoryScope
	state InventoryState
}

func (s SQLStore) WithInventory(ctx context.Context, scope InventoryScope, fn func(InventoryRecords) error) error {
	if s.DB == nil || scope.Collection == "" || scope.Generation == "" {
		return ErrHeld
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "notes-inventory:"+key(scope.Collection, scope.Generation)); err != nil {
		return err
	}
	r := &sqlInventoryRecords{tx: tx, scope: scope}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT record FROM notes_workspace.source_inventory_generations WHERE collection_id=$1 AND generation=$2`, scope.Collection, scope.Generation).Scan(&raw)
	if err != nil && !missing(err) {
		return err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &r.state); err != nil {
			return err
		}
	} else {
		r.state.CountersReady = true
		r.state.LaneExclusionsReady = true
		if _, err = tx.ExecContext(ctx, `INSERT INTO notes_workspace.source_inventory_generations(collection_id,generation,record) VALUES($1,$2,'{}')`, scope.Collection, scope.Generation); err != nil {
			return err
		}
	}
	// Upgrade once at the existing writer boundary, never during UI reads.
	if r.state.CountersReady && !r.state.LaneExclusionsReady {
		if err = tx.QueryRowContext(ctx, `SELECT
 COUNT(*) FILTER (WHERE entry_kind IN ('text','directory')),COUNT(*) FILTER (WHERE entry_kind='asset')
 FROM notes_workspace.source_inventory WHERE collection_id=$1 AND generation=$2
 AND present AND sequence>0 AND reason='source_excluded'`, scope.Collection, scope.Generation).
			Scan(&r.state.Counts.TextExcluded, &r.state.Counts.AssetExcluded); err != nil {
			return err
		}
		r.state.LaneExclusionsReady = true
	}
	if err = fn(r); err != nil {
		return err
	}
	updated, err := json.Marshal(r.state)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE notes_workspace.source_inventory_generations SET record=$3,updated_at=now() WHERE collection_id=$1 AND generation=$2 AND record IS DISTINCT FROM $3::jsonb`, scope.Collection, scope.Generation, updated); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *sqlInventoryRecords) State() *InventoryState { return &r.state }

const inventoryColumns = `relative_path,entry_kind,present,fingerprint,size_bytes,sequence,delivered_sequence,seen_scan,priority,waiting_since,retry_at,attempts,reason`

func scanInventoryEntry(row interface{ Scan(...any) error }) (e InventoryEntry, err error) {
	err = row.Scan(&e.Path, &e.Kind, &e.Present, &e.Fingerprint, &e.Size, &e.Sequence, &e.Delivered, &e.SeenScan, &e.Priority, &e.WaitingSince, &e.RetryAt, &e.Attempts, &e.Reason)
	return
}
func (r *sqlInventoryRecords) Get(ctx context.Context, path string) (InventoryEntry, error) {
	return scanInventoryEntry(r.tx.QueryRowContext(ctx, `SELECT `+inventoryColumns+` FROM notes_workspace.source_inventory WHERE collection_id=$1 AND generation=$2 AND relative_path=$3`, r.scope.Collection, r.scope.Generation, path))
}
func (r *sqlInventoryRecords) MarkSeen(ctx context.Context, path string, scan int64) error {
	_, err := r.tx.ExecContext(ctx, `UPDATE notes_workspace.source_inventory SET seen_scan=$4
 WHERE collection_id=$1 AND generation=$2 AND relative_path=$3 AND seen_scan<$4`, r.scope.Collection, r.scope.Generation, path, scan)
	return err
}
func (r *sqlInventoryRecords) Put(ctx context.Context, e InventoryEntry) error {
	old, err := r.Get(ctx, e.Path)
	if err != nil && !missing(err) {
		return err
	}
	if r.state.CountersReady {
		inventoryTrack(&r.state.Counts, old, e)
	}
	_, err = r.tx.ExecContext(ctx, `INSERT INTO notes_workspace.source_inventory(collection_id,generation,`+inventoryColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
 ON CONFLICT(collection_id,generation,relative_path) DO UPDATE SET
 entry_kind=EXCLUDED.entry_kind,present=EXCLUDED.present,fingerprint=EXCLUDED.fingerprint,size_bytes=EXCLUDED.size_bytes,
 sequence=EXCLUDED.sequence,delivered_sequence=EXCLUDED.delivered_sequence,seen_scan=EXCLUDED.seen_scan,priority=EXCLUDED.priority,
 waiting_since=EXCLUDED.waiting_since,retry_at=EXCLUDED.retry_at,attempts=EXCLUDED.attempts,reason=EXCLUDED.reason`,
		r.scope.Collection, r.scope.Generation, e.Path, e.Kind, e.Present, e.Fingerprint, e.Size, e.Sequence, e.Delivered, e.SeenScan, e.Priority, e.WaitingSince, e.RetryAt, e.Attempts, e.Reason)
	return err
}
func (r *sqlInventoryRecords) Pending(ctx context.Context, kind string, now time.Time, limit int) ([]InventoryEntry, error) {
	// Withdrawals are retained evidence, not authority to delete a native revision.
	// Client delete/rename intents still run first through Service.Step.
	entries, err := r.query(ctx, `SELECT `+inventoryColumns+` FROM notes_workspace.source_inventory WHERE collection_id=$1 AND generation=$2 AND (present OR entry_kind='directory') AND reason<>'source_excluded' AND sequence>delivered_sequence AND ($3='' OR entry_kind=$3 OR $3='text' AND entry_kind='directory') AND retry_at<=$4 ORDER BY priority DESC,waiting_since,relative_path LIMIT $5`, r.scope.Collection, r.scope.Generation, kind, now, limit)
	if err != nil {
		return nil, err
	}
	oldest, err := r.query(ctx, `SELECT `+inventoryColumns+` FROM notes_workspace.source_inventory WHERE collection_id=$1 AND generation=$2 AND (present OR entry_kind='directory') AND reason<>'source_excluded' AND sequence>delivered_sequence AND ($3='' OR entry_kind=$3 OR $3='text' AND entry_kind='directory') AND retry_at<=$4 ORDER BY waiting_since,relative_path LIMIT 1`, r.scope.Collection, r.scope.Generation, kind, now)
	if err != nil {
		return nil, err
	}
	return inventoryAgeGuard(entries, oldest, now, limit), nil
}
func (r *sqlInventoryRecords) Unseen(ctx context.Context, prefix, after string, sequence int64, limit int) ([]InventoryEntry, error) {
	return r.query(ctx, `SELECT `+inventoryColumns+` FROM notes_workspace.source_inventory WHERE collection_id=$1 AND generation=$2 AND present AND reason<>'source_excluded' AND relative_path COLLATE "C">$3 COLLATE "C" AND seen_scan<$4 AND sequence<=$4 AND ($5='' OR starts_with(relative_path,$5||'/')) ORDER BY relative_path LIMIT $6`, r.scope.Collection, r.scope.Generation, after, sequence, prefix, limit)
}
func (r *sqlInventoryRecords) query(ctx context.Context, query string, args ...any) ([]InventoryEntry, error) {
	rows, err := r.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InventoryEntry
	for rows.Next() {
		e, err := scanInventoryEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *sqlInventoryRecords) Waiting(ctx context.Context, kind string) (*InventoryEntry, *time.Time, error) {
	oldest, err := r.query(ctx, `SELECT `+inventoryColumns+` FROM notes_workspace.source_inventory WHERE collection_id=$1 AND generation=$2 AND (entry_kind=$3 OR $3='text' AND entry_kind='directory') AND (present OR entry_kind='directory') AND reason<>'source_excluded' AND sequence>delivered_sequence ORDER BY waiting_since,relative_path LIMIT 1`, r.scope.Collection, r.scope.Generation, kind)
	if err != nil {
		return nil, nil, err
	}
	var retry sql.NullTime
	err = r.tx.QueryRowContext(ctx, `SELECT retry_at FROM notes_workspace.source_inventory WHERE collection_id=$1 AND generation=$2 AND (entry_kind=$3 OR $3='text' AND entry_kind='directory') AND (present OR entry_kind='directory') AND reason<>'source_excluded' AND sequence>delivered_sequence ORDER BY retry_at LIMIT 1`, r.scope.Collection, r.scope.Generation, kind).Scan(&retry)
	if err != nil && !missing(err) {
		return nil, nil, err
	}
	var entry *InventoryEntry
	var at *time.Time
	if len(oldest) > 0 {
		entry = &oldest[0]
	}
	if retry.Valid {
		at = &retry.Time
	}
	return entry, at, nil
}
