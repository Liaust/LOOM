package notesworkspace

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type sqlQuery interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// SQLStore requires the feature-local migration to be numbered/installed by the
// integrator. No schema creation or alternative database is performed here.
type SQLStore struct {
	DB              *sql.DB
	ExclusiveSource bool
	query           sqlQuery
}

func (s SQLStore) q() sqlQuery {
	if s.query != nil {
		return s.query
	}
	return s.DB
}
func (s SQLStore) WithLock(ctx context.Context, key string, fn func(Store) error) (result error) {
	if s.DB == nil {
		return fmt.Errorf("notes workspace database required")
	}
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	filesystemLock := `SELECT pg_advisory_lock_shared(hashtextextended($1,0))`
	filesystemUnlock := `SELECT pg_advisory_unlock_shared(hashtextextended($1,0))`
	if s.ExclusiveSource {
		filesystemLock = `SELECT pg_advisory_lock(hashtextextended($1,0))`
		filesystemUnlock = `SELECT pg_advisory_unlock(hashtextextended($1,0))`
	}
	locks := []struct{ key, acquire, release string }{
		{"notesworkspace:filesystem", filesystemLock, filesystemUnlock},
		{"notesworkspace:" + key, `SELECT pg_advisory_lock(hashtextextended($1,0))`, `SELECT pg_advisory_unlock(hashtextextended($1,0))`},
	}
	held, discarded := 0, false
	defer func() {
		if discarded {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for i := held - 1; i >= 0; i-- {
			var unlocked bool
			err := conn.QueryRowContext(cleanup, locks[i].release, locks[i].key).Scan(&unlocked)
			if err == nil && !unlocked {
				err = fmt.Errorf("notes workspace lock not held: %s", locks[i].key)
			}
			if err != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
				result = errors.Join(result, err)
				return
			}
		}
	}()
	for _, lock := range locks {
		if _, err := conn.ExecContext(ctx, lock.acquire, lock.key); err != nil {
			// Cancellation can obscure which session locks PostgreSQL acquired.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			discarded = true
			return err
		}
		held++
	}
	return fn(SQLStore{DB: s.DB, ExclusiveSource: s.ExclusiveSource, query: conn})
}
func (s SQLStore) Bind(ctx context.Context, f File) (File, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return File{}, err
	}
	_, err = s.q().ExecContext(ctx, `INSERT INTO notes_workspace.source_scopes(scope_id,kind,source_path,node_key,created_at)
 SELECT notes_source_root_id,'notes',source_path,COALESCE(node_key,''),created_at FROM knowledge.notes_source_roots
 WHERE notes_source_root_id=$1 AND NOT EXISTS(SELECT 1 FROM notes_workspace.source_scopes WHERE scope_id=$1)
 ON CONFLICT(scope_id) DO NOTHING`, f.CollectionID)
	if err != nil {
		return File{}, err
	}
	_, err = s.q().ExecContext(ctx, `INSERT INTO notes_workspace.files(file_id,collection_id,path_key,record) VALUES($1,$2,$3,$4) ON CONFLICT(collection_id,path_key) WHERE NOT deleted DO NOTHING`, f.ID, f.CollectionID, f.PathKey, raw)
	if err != nil {
		return File{}, err
	}
	err = s.q().QueryRowContext(ctx, `SELECT record FROM notes_workspace.files WHERE collection_id=$1 AND path_key=$2 AND NOT deleted`, f.CollectionID, f.PathKey).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &f)
	}
	return f, err
}
func (s SQLStore) File(ctx context.Context, id string) (f File, err error) {
	var raw []byte
	err = s.q().QueryRowContext(ctx, `SELECT record FROM notes_workspace.files WHERE file_id=$1`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &f)
	}
	return
}
func (s SQLStore) SaveBase(ctx context.Context, b Base) error {
	// Streamed references already have no inline bytes. Direct PDF reads still
	// need storage-level sharing so enrollment changes cannot reintroduce copies.
	var shared bool
	if len(b.Content) > 0 {
		var relative string
		if err := s.q().QueryRowContext(ctx, `SELECT record->>'relative_path' FROM notes_workspace.files WHERE file_id=$1`, b.FileID).Scan(&relative); err != nil {
			return err
		}
		shared = isPDF(relative)
	}
	var payload any
	if shared {
		if err := ensureReferencePayload(ctx, s.q(), b.Hash, b.Content); err != nil {
			return err
		}
		payload = b.Hash
		b.Content = nil
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = s.q().ExecContext(ctx, `INSERT INTO notes_workspace.bases(base_id,file_id,record,reference_payload_hash) VALUES($1,$2,$3,$4) ON CONFLICT(base_id) DO NOTHING`, b.ID, b.FileID, raw, payload)
	return err
}
func (s SQLStore) Base(ctx context.Context, id string) (b Base, err error) {
	return readSQLBase(ctx, s.q(), id)
}
func (s SQLStore) Receive(ctx context.Context, op Operation) (Operation, error) {
	raw, err := json.Marshal(op)
	if err != nil {
		return Operation{}, err
	}
	_, err = s.q().ExecContext(ctx, `INSERT INTO notes_workspace.operations(operation_id,file_id,state,record) VALUES($1,$2,$3,$4) ON CONFLICT(operation_id) DO NOTHING`, op.Request.OperationID, op.File.ID, op.State, raw)
	if err != nil {
		return Operation{}, err
	}
	saved, err := s.Operation(ctx, op.Request.OperationID)
	if err != nil {
		return Operation{}, err
	}
	if !sameIntent(saved, op) {
		return Operation{}, ErrIntentMismatch
	}
	return saved, nil
}
func (s SQLStore) Operation(ctx context.Context, id string) (op Operation, err error) {
	var raw []byte
	err = s.q().QueryRowContext(ctx, `SELECT record FROM notes_workspace.operations WHERE operation_id=$1`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &op)
	}
	return
}
func (s SQLStore) SaveOperation(ctx context.Context, op Operation) error {
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	result, err := s.q().ExecContext(ctx, `UPDATE notes_workspace.operations SET state=$2,record=$3,updated_at=now() WHERE operation_id=$1`, op.Request.OperationID, op.State, raw)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return sql.ErrNoRows
	}
	return err
}

// PendingOperationIDs advances through pending AND held records without allowing
// an unchanged held page to monopolize discovery. The caller persists the cursor.
func (s SQLStore) PendingOperationIDs(ctx context.Context, after string, limit int) (DiscoveryPage, error) {
	return s.discover(ctx, after, limit, "state IN ('pending','held')")
}

// RetainedOperationIDs includes final receipts: late open-inode writes can occur
// after acknowledgement. Run separate repeated sweeps; do not prune on acceptance.
func (s SQLStore) RetainedOperationIDs(ctx context.Context, after string, limit int) (DiscoveryPage, error) {
	return s.discover(ctx, after, limit, "COALESCE(record->>'journal','') <> '' AND COALESCE(record#>>'{file,entry_kind}','') <> 'directory'")
}
func (s SQLStore) discover(ctx context.Context, after string, limit int, predicate string) (DiscoveryPage, error) {
	if s.DB == nil {
		return DiscoveryPage{}, fmt.Errorf("notes workspace database required")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT operation_id FROM notes_workspace.operations WHERE `+predicate+` AND operation_id COLLATE "C" > $1 COLLATE "C" ORDER BY operation_id COLLATE "C" LIMIT $2`, after, limit)
	if err != nil {
		return DiscoveryPage{}, err
	}
	defer rows.Close()
	var page DiscoveryPage
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return DiscoveryPage{}, err
		}
		page.IDs = append(page.IDs, id)
		page.Next = id
	}
	if err := rows.Err(); err != nil {
		return DiscoveryPage{}, err
	}
	return page, nil
}

func (s SQLStore) SaveRecovery(ctx context.Context, r Recovery) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.q().ExecContext(ctx, `INSERT INTO notes_workspace.recoveries(recovery_id,operation_id,state,record) VALUES($1,$2,$3,$4) ON CONFLICT(recovery_id) DO NOTHING`, r.ID, r.OperationID, r.State, raw)
	return err
}

// Recoveries exposes durable recovery outcomes separately from stable save receipts.
// Keyset paging also bounds a repeatedly changing retained inode's history.
func (s SQLStore) Recoveries(ctx context.Context, operationID, after string, limit int) ([]Recovery, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT record FROM notes_workspace.recoveries WHERE operation_id=$1 AND recovery_id COLLATE "C" > $2 COLLATE "C" ORDER BY recovery_id COLLATE "C" LIMIT $3`, operationID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recovery
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r Recovery
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s SQLStore) FileAt(ctx context.Context, collection, key string) (f File, err error) {
	var raw []byte
	err = s.q().QueryRowContext(ctx, `SELECT record FROM notes_workspace.files WHERE collection_id=$1 AND path_key=$2 AND NOT deleted`, collection, key).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &f)
	}
	return
}

// Reservations are prepared journal entries, not another queue. Callers hold the
// filesystem and collection locks; aliases must see the same physical reservation.
func (s SQLStore) PathBusy(ctx context.Context, collection, key, except string) (bool, error) {
	var busy bool
	scope, err := s.scopeLocation(ctx, collection)
	if err != nil {
		return false, err
	}
	physical, resolved := scopePathKey(scope.Path, key)
	err = s.q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notes_workspace.operations o JOIN notes_workspace.files f USING(file_id)
 WHERE f.collection_id=$1 AND o.operation_id<>$3 AND o.state IN ('pending','held') AND COALESCE(o.record->>'path_stage','')<>''
 AND (o.record->'file'->>'path_key'=$2 OR o.record->>'destination_key'=$2)) OR EXISTS(
 SELECT 1 FROM notes_workspace.tree_moves t
 CROSS JOIN LATERAL (SELECT t.record->>'source_key' AS source,t.record->>'destination_key' AS destination) m
 WHERE t.state IN ('prepared','held') AND t.move_id<>$3
 AND ($4=m.source OR left($4,length(m.source)+1)=m.source||'/' OR left(m.source,length($4)+1)=$4||'/'
 OR $4=m.destination OR left($4,length(m.destination)+1)=m.destination||'/' OR left(m.destination,length($4)+1)=$4||'/')) OR EXISTS(
 SELECT 1 FROM storage.retained_relocations r WHERE r.state='prepared' AND r.relocation_id<>$3 AND r.plan_json->>'schema'='storage.workspace_path_plan.v1'
 AND ($4=r.source_prefix OR left($4,length(r.source_prefix)+1)=r.source_prefix||'/' OR left(r.source_prefix,length($4)+1)=$4||'/'
 OR $4=r.destination_prefix OR left($4,length(r.destination_prefix)+1)=r.destination_prefix||'/' OR left(r.destination_prefix,length($4)+1)=$4||'/'))`, collection, key, except, physical).Scan(&busy)
	if err != nil || busy {
		return busy, err
	}
	if !resolved {
		return false, ErrMembership
	}
	query, ok := s.q().(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	if !ok {
		return false, ErrMembership
	}
	// Read prepared path metadata only, not retained proposal/base payloads.
	rows, err := query.QueryContext(ctx, `SELECT
 CASE WHEN s.kind='notes' OR s.scope_id IS NULL THEN COALESCE(NULLIF(r.source_path,''),s.source_path,'') ELSE s.source_path END,
 CASE WHEN s.kind='notes' OR s.scope_id IS NULL THEN COALESCE(r.node_key,s.node_key,'') ELSE s.node_key END,
 COALESCE(NULLIF(o.record#>>'{file,path_key}',''),f.path_key),
 COALESCE(NULLIF(o.record->>'destination_key',''),o.record#>>'{request,destination_path}','')
 FROM notes_workspace.operations o JOIN notes_workspace.files f USING(file_id)
 LEFT JOIN notes_workspace.source_scopes s ON s.scope_id=f.collection_id
 LEFT JOIN knowledge.notes_source_roots r ON r.notes_source_root_id=f.collection_id AND (s.kind='notes' OR s.scope_id IS NULL)
 WHERE o.operation_id<>$1 AND o.state IN ('pending','held') AND COALESCE(o.record->>'path_stage','')<>''`, except)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var root, node, source, destination string
		if err := rows.Scan(&root, &node, &source, &destination); err != nil {
			return false, err
		}
		if scope.NodeKey != "" && node != "" && scope.NodeKey != node {
			continue
		}
		for _, relative := range []string{source, destination} {
			reserved, known := scopePathKey(root, relative)
			if known && (within(physical, reserved) || within(reserved, physical)) {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

// CommitPath atomically advances binding/history/base/receipt after physical
// publication. A failed commit leaves the prepared operation's paths reserved.
func (s SQLStore) CommitPath(ctx context.Context, op Operation, file File, base Base) error {
	beginner, ok := s.q().(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return fmt.Errorf("path finalization requires database connection")
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	locked := SQLStore{DB: s.DB, query: tx}
	raw, err := json.Marshal(file)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE notes_workspace.files SET path_key=$2,deleted=$3,record=$4 WHERE file_id=$1 AND NOT deleted AND COALESCE((record->>'path_version')::bigint,0)=$5`, file.ID, file.PathKey, file.Deleted, raw, op.File.PathVersion)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStalePath
	}
	change := PathChange{FileID: file.ID, OperationID: op.Request.OperationID, Version: file.PathVersion, FromPath: op.File.RelativePath, Deleted: file.Deleted}
	if !file.Deleted {
		change.ToPath = file.RelativePath
	}
	raw, err = json.Marshal(change)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notes_workspace.path_history(file_id,path_version,operation_id,record) VALUES($1,$2,$3,$4)`, file.ID, file.PathVersion, op.Request.OperationID, raw); err != nil {
		return err
	}
	if err := locked.SaveBase(ctx, base); err != nil {
		return err
	}
	if err := locked.SaveOperation(ctx, op); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(ErrPathCommitUncertain, err)
	}
	return nil
}

func (s SQLStore) PathHistory(ctx context.Context, fileID string, afterVersion uint64, limit int) ([]PathChange, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT record FROM notes_workspace.path_history WHERE file_id=$1 AND path_version>$2 ORDER BY path_version LIMIT $3`, fileID, afterVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PathChange
	for rows.Next() {
		var raw []byte
		var change PathChange
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &change); err != nil {
			return nil, err
		}
		out = append(out, change)
	}
	return out, rows.Err()
}
