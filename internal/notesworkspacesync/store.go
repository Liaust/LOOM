package notesworkspacesync

import (
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
type SQLStore struct{ DB *sql.DB }
type sqlRecords struct {
	conn    *sql.Conn
	replica string
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
	return fn(sqlRecords{conn: conn, replica: id})
}
func (s sqlRecords) Get(ctx context.Context, kind, id string, out any) error {
	var raw []byte
	if err := s.conn.QueryRowContext(ctx, `SELECT record FROM notes_workspace.sync_records WHERE replica_id=$1 AND kind=$2 AND record_id=$3`, s.replica, kind, id).Scan(&raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
func (s sqlRecords) Put(ctx context.Context, kind, id string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.conn.ExecContext(ctx, `INSERT INTO notes_workspace.sync_records(replica_id,kind,record_id,record) VALUES($1,$2,$3,$4) ON CONFLICT(replica_id,kind,record_id) DO UPDATE SET record=EXCLUDED.record,updated_at=now() WHERE notes_workspace.sync_records.record IS DISTINCT FROM EXCLUDED.record`, s.replica, kind, id, raw)
	return err
}
func (s sqlRecords) List(ctx context.Context, kind, after string, limit int) ([]string, error) {
	return s.list(ctx, kind, after, limit, false)
}

func (s sqlRecords) ListPending(ctx context.Context, kind, after string, limit int) ([]string, error) {
	if kind != "operation" && kind != "observation" {
		return nil, ErrHeld
	}
	return s.list(ctx, kind, after, limit, true)
}

func (s sqlRecords) list(ctx context.Context, kind, after string, limit int, pending bool) ([]string, error) {
	if limit < 1 || limit > 128 {
		return nil, ErrHeld
	}
	rows, err := s.conn.QueryContext(ctx, `SELECT record_id FROM notes_workspace.sync_records
		WHERE replica_id=$1 AND kind=$2 AND record_id COLLATE "C">$3 COLLATE "C"
		AND (NOT $5 OR (kind='observation' AND record->>'Reason'='control_pending')
		OR (kind='operation' AND COALESCE(record->>'AckPublished','false')<>'true'))
		ORDER BY record_id COLLATE "C" LIMIT $4`, s.replica, kind, after, limit, pending)
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
