package notesworkspacesync

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Store persists protocol evidence and mappings in the existing PostgreSQL owner.
// It is not a payload queue. Source.Receive owns content before source mutation.
// WithReplica excludes competing import/export calls across processes. Each Put
// must commit before returning; a crash must retain earlier successful Puts.
type Store interface {
	WithReplica(context.Context, string, string, func(Records) error) error
}
type Records interface {
	Get(context.Context, string, string, any) error
	Put(context.Context, string, string, any) error
	List(context.Context, string, string, int) ([]string, error)
	ListPending(context.Context, string, string, int) ([]string, error)
}
type collectionFileRecords interface {
	ListCollectionFiles(context.Context, string, string, int) ([]string, error)
}
type enrollmentFileRecords interface {
	ListEnrollmentFiles(context.Context, string, int, []Scope) ([]string, error)
}

func (s sqlRecords) ListEnrollmentFiles(ctx context.Context, after string, limit int, scopes []Scope) ([]string, error) {
	if limit < 1 || limit > 128 {
		return nil, ErrHeld
	}
	raw, err := json.Marshal(scopes)
	if err != nil {
		return nil, err
	}
	// This narrows metadata discovery only; Go still validates every mapping and
	// its exact publication receipt before returning a catch-up candidate.
	rows, err := s.conn.QueryContext(ctx, `SELECT record_id FROM notes_workspace.sync_records f
	 WHERE replica_id=$1 AND kind='file' AND record_id COLLATE "C">$2 COLLATE "C"
	 AND EXISTS (SELECT 1 FROM jsonb_array_elements($4::jsonb) s
	 WHERE s->>'Workspace'=f.record#>>'{Scope,Workspace}' AND s->>'Collection'=f.record#>>'{Scope,Collection}'
	 AND s->>'Root'=f.record#>>'{Scope,Root}' AND s->>'SourceCollection'=f.record#>>'{Scope,SourceCollection}'
	 AND ((NULLIF(s->>'previous_generation','') IS NOT NULL AND s->>'previous_generation'<>s->>'Generation'
	 AND s->>'previous_generation'=f.record#>>'{Scope,Generation}')
	 OR (s->>'Generation'=f.record#>>'{Scope,Generation}' AND COALESCE(s->>'read_only','false')<>'true'
	 AND f.record#>>'{Latest,Writable}'='false' AND lower(f.record#>>'{Latest,Path}') LIKE '%.canvas')))
	 ORDER BY record_id COLLATE "C" LIMIT $3`, s.replica, after, limit, raw)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s sqlRecords) ListCollectionFiles(ctx context.Context, collection, after string, limit int) ([]string, error) {
	if limit < 1 || limit > 128 || collection == "" {
		return nil, ErrHeld
	}
	rows, err := s.conn.QueryContext(ctx, `SELECT record_id FROM notes_workspace.sync_records
	 WHERE replica_id=$1 AND kind='file' AND record#>>'{Scope,Collection}'=$2
	 AND record_id COLLATE "C">$3 COLLATE "C" ORDER BY record_id COLLATE "C" LIMIT $4`, s.replica, collection, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type SQLStore struct{ DB *sql.DB }
type sqlRecords struct {
	conn    *sql.Conn
	replica string
	known   map[string][]byte
}

func (s SQLStore) WithReplica(ctx context.Context, id, epoch string, fn func(Records) error) (err error) {
	if s.DB == nil || !token(id) || epoch == "" {
		return ErrHeld
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, "notes-sync:"+id); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, e := conn.ExecContext(c, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "notes-sync:"+id); e != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, e)
		}
	}()
	_, err = conn.ExecContext(ctx, `INSERT INTO notes_workspace.sync_replicas(replica_id,native_epoch) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, epoch)
	if err != nil {
		return err
	}
	var old string
	if err = conn.QueryRowContext(ctx, `SELECT native_epoch FROM notes_workspace.sync_replicas WHERE replica_id=$1`, id).Scan(&old); err != nil {
		return err
	}
	if old != epoch {
		return fmt.Errorf("%w: replica_epoch_mismatch", ErrHeld)
	}
	return fn(sqlRecords{conn: conn, replica: id, known: map[string][]byte{}})
}
func (s sqlRecords) Get(ctx context.Context, kind, id string, out any) error {
	var raw []byte
	if err := s.conn.QueryRowContext(ctx, `SELECT record FROM notes_workspace.sync_records WHERE replica_id=$1 AND kind=$2 AND record_id=$3`, s.replica, kind, id).Scan(&raw); err != nil {
		return err
	}
	if s.known != nil {
		canonical, err := canonicalRecord(raw)
		if err != nil {
			return err
		}
		s.known[kind+":"+id] = canonical
	}
	return json.Unmarshal(raw, out)
}
func (s sqlRecords) Put(ctx context.Context, kind, id string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	canonical, err := canonicalRecord(raw)
	if err != nil {
		return err
	}
	key := kind + ":" + id
	old, known := s.known[key]
	if !known {
		var stored []byte
		err := s.conn.QueryRowContext(ctx, `SELECT record FROM notes_workspace.sync_records WHERE replica_id=$1 AND kind=$2 AND record_id=$3`, s.replica, kind, id).Scan(&stored)
		if err == nil {
			old, err = canonicalRecord(stored)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if bytes.Equal(old, canonical) {
		return nil
	}
	_, err = s.conn.ExecContext(ctx, `INSERT INTO notes_workspace.sync_records(replica_id,kind,record_id,record) VALUES($1,$2,$3,$4) ON CONFLICT(replica_id,kind,record_id) DO UPDATE SET record=EXCLUDED.record,updated_at=now() WHERE notes_workspace.sync_records.record IS DISTINCT FROM EXCLUDED.record`, s.replica, kind, id, raw)
	if err == nil && s.known != nil {
		s.known[key] = canonical
	}
	return err
}

func canonicalRecord(raw []byte) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// Dependency readiness is checked against retained evidence, never a timer or
// inferred native winner. A failed relation becomes predecessor_mismatch.
const predecessorReadySQL = `o.record->>'Reason' IN ('predecessor_not_applied','waiting_for_predecessor') AND EXISTS (
 SELECT 1 FROM notes_workspace.sync_records p
 WHERE p.replica_id=o.replica_id AND p.kind='operation'
 AND p.record_id=(NULLIF(o.record->>'IntentRaw','')::jsonb)->>'predecessor'
 AND p.record->>'Status'='applied')`

func (s sqlRecords) List(ctx context.Context, kind, after string, limit int) ([]string, error) {
	return s.list(ctx, kind, after, limit, false)
}

func (s sqlRecords) PendingSourceMoves(ctx context.Context, limit int) ([]string, *time.Time, error) {
	if limit < 1 || limit > 128 {
		return nil, nil, ErrHeld
	}
	var nextMS sql.NullInt64
	if err := s.conn.QueryRowContext(ctx, `SELECT min((record->>'RetryAtMS')::bigint) FROM notes_workspace.sync_records
 WHERE replica_id=$1 AND kind='export' AND record_id COLLATE "C">=$2 COLLATE "C" AND record_id COLLATE "C"<($2||'~') COLLATE "C"
 AND COALESCE(record->>'Published','false')<>'true' AND COALESCE(record->>'Terminal','false')<>'true' AND COALESCE((record->>'RetryAtMS')::bigint,0)>$3`, s.replica, sourceMovePrefix, time.Now().UnixMilli()).Scan(&nextMS); err != nil {
		return nil, nil, err
	}
	var next *time.Time
	if nextMS.Valid {
		at := time.UnixMilli(nextMS.Int64)
		next = &at
	}
	rows, err := s.conn.QueryContext(ctx, `SELECT record_id FROM notes_workspace.sync_records
 WHERE replica_id=$1 AND kind='export' AND record_id COLLATE "C">=$2 COLLATE "C"
 AND record_id COLLATE "C"<($2||'~') COLLATE "C"
 AND COALESCE(record->>'Published','false')<>'true' AND COALESCE(record->>'Terminal','false')<>'true'
 AND COALESCE((record->>'RetryAtMS')::bigint,0)<=$4
 ORDER BY record_id COLLATE "C" LIMIT $3`, s.replica, sourceMovePrefix, limit, time.Now().UnixMilli())
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	return ids, next, rows.Err()
}

func (s sqlRecords) ListPending(ctx context.Context, kind, after string, limit int) ([]string, error) {
	if kind != "operation" && kind != "observation" {
		return nil, ErrHeld
	}
	return s.list(ctx, kind, after, limit, true)
}

func (s sqlRecords) ListReadyOperations(ctx context.Context, after string, limit int, scopeContext string, nowMS int64) ([]string, error) {
	if limit < 1 || limit > 128 {
		return nil, ErrHeld
	}
	rows, err := s.conn.QueryContext(ctx, `SELECT record_id FROM notes_workspace.sync_records o
		WHERE replica_id=$1 AND kind='operation' AND record_id COLLATE "C">$2 COLLATE "C"
		AND COALESCE(record->>'AckPublished','false')<>'true'
		AND (COALESCE(record->>'RetryContext','')<>$4 OR COALESCE((record->>'RetryAfterMS')::bigint,0)<=$5 OR (`+predecessorReadySQL+`))
		ORDER BY record_id COLLATE "C" LIMIT $3`, s.replica, after, limit, scopeContext, nowMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s sqlRecords) list(ctx context.Context, kind, after string, limit int, pending bool) ([]string, error) {
	if limit < 1 || limit > 128 {
		return nil, ErrHeld
	}
	predicate := ""
	if pending {
		switch kind {
		case "observation":
			predicate = " AND kind='observation' AND record->>'Reason'='control_pending'"
		case "operation":
			predicate = " AND kind='operation' AND COALESCE(record->>'AckPublished','false')<>'true'"
		default:
			return nil, ErrHeld
		}
	}
	rows, err := s.conn.QueryContext(ctx, `SELECT record_id FROM notes_workspace.sync_records
		WHERE replica_id=$1 AND kind=$2 AND record_id COLLATE "C">$3 COLLATE "C"
		`+predicate+` ORDER BY record_id COLLATE "C" LIMIT $4`, s.replica, kind, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
