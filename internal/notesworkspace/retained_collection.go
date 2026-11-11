package notesworkspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
)

const CollectionRetentionSchema = "notes_workspace.collection_retention.v1"

type CollectionRetentionFile struct {
	File       File             `json:"file"`
	SourcePath string           `json:"source_path"`
	Location   RetainedLocation `json:"location"`
}

type CollectionRetentionJournal struct {
	OperationID string           `json:"operation_id"`
	State       string           `json:"state"`
	Journal     string           `json:"journal"`
	Collection  string           `json:"collection"`
	Path        string           `json:"path"`
	Previous    *JournalLocation `json:"previous,omitempty"`
	Location    JournalLocation  `json:"location"`
}

// The caller persists this sealed metadata in its reviewed storage intent.
// Source inventory, physical movement and the enclosing project's state stay
// with that owner; this plan neither moves bytes nor enrolls a Notes source.
type CollectionRetentionPlan struct {
	Schema   string                       `json:"schema"`
	ID       string                       `json:"operation_id"`
	From     string                       `json:"from"`
	To       string                       `json:"to"`
	Scope    SourceScope                  `json:"retained_scope"`
	Files    []CollectionRetentionFile    `json:"files"`
	Journals []CollectionRetentionJournal `json:"journals"`
	Digest   string                       `json:"digest"`
}

func collectionRetentionDigest(p CollectionRetentionPlan) string {
	p.Digest = ""
	return "sha256:" + identity(p)
}

func retentionAbsolute(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && p != string(filepath.Separator) && !strings.ContainsRune(p, 0)
}

// EnsureRetainedScope is called only with the configured Storage root by its
// trusted local owner, never with a client-selected root. One stable scope per
// node/root preserves the generation; replacing its inode requires recovery.
func (s SQLStore) EnsureRetainedScope(ctx context.Context, scope SourceScope) error {
	if !strings.HasPrefix(scope.ID, "filesystem_retained_") || scope.ID == "filesystem_retained_" || scope.NodeKey == "" || !retentionAbsolute(scope.Path) || scope.RootIdentity == "" {
		return ErrMembership
	}
	if s.DB == nil && s.query == nil {
		return ErrMembership
	}
	if err := checkRetentionScope(scope); err != nil {
		return err
	}
	if _, err := s.q().ExecContext(ctx, `INSERT INTO notes_workspace.source_scopes(scope_id,kind,source_path,node_key,root_identity)
 VALUES($1,'retained',$2,$3,$4) ON CONFLICT(scope_id) DO NOTHING`, scope.ID, scope.Path, scope.NodeKey, scope.RootIdentity); err != nil {
		return err
	}
	var saved SourceScope
	var kind string
	saved.ID = scope.ID
	if err := s.q().QueryRowContext(ctx, `SELECT kind,source_path,node_key,root_identity FROM notes_workspace.source_scopes WHERE scope_id=$1`, scope.ID).Scan(&kind, &saved.Path, &saved.NodeKey, &saved.RootIdentity); err != nil {
		return err
	}
	if kind != "retained" || saved != scope {
		return ErrMembership
	}
	return nil
}

func checkRetentionScope(scope SourceScope) error {
	parent, _, err := openSourceParent(scope.Path, "retained-root-check")
	if err != nil {
		return err
	}
	defer parent.Close()
	var st unix.Stat_t
	if err = unix.Fstat(parent.fd(), &st); err != nil {
		return err
	}
	if scope.RootIdentity != FilesystemGeneration(scope.ID, scope.NodeKey, scope.Path, uint64(st.Dev), st.Ino) {
		return ErrMembership
	}
	return parent.check()
}

func (s SQLStore) collectionRetainedScope(ctx context.Context, to string) (scope SourceScope, err error) {
	q, ok := s.q().(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	if !ok {
		return scope, ErrMembership
	}
	rows, err := q.QueryContext(ctx, `SELECT scope_id,source_path,node_key,root_identity FROM notes_workspace.source_scopes WHERE kind='retained' ORDER BY scope_id LIMIT 65`)
	if err != nil {
		return scope, err
	}
	count, matches := 0, 0
	for rows.Next() {
		var v SourceScope
		if err = rows.Scan(&v.ID, &v.Path, &v.NodeKey, &v.RootIdentity); err != nil {
			break
		}
		count++
		if retentionAbsolute(v.Path) && within(to, v.Path) {
			scope, matches = v, matches+1
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return scope, err
	}
	if count > 64 || matches != 1 || scope.NodeKey == "" || !strings.HasPrefix(scope.ID, "filesystem_retained_") {
		return scope, ErrMembership
	}
	relative, err := filepath.Rel(scope.Path, to)
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if err != nil || validateDirectoryPath(filepath.ToSlash(relative)) != nil || len(parts) < 2 || (parts[0] != "Projects" && parts[0] != "Library" && parts[0] != "Records") {
		return scope, ErrUnsafePath
	}
	return scope, checkRetentionScope(scope)
}

// PlanCollectionRetention runs on s.query when supplied (including a held conn
// or caller transaction). The caller holds the exclusive filesystem/project/
// custody barriers and separately proves physical inventory and dependencies.
func (s SQLStore) PlanCollectionRetention(ctx context.Context, fromAbsolute, toAbsolute, id string) (p CollectionRetentionPlan, err error) {
	if !s.ExclusiveSource || s.DB == nil && s.query == nil || id == "" || len(id) > 160 || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\x00\r\n") || !retentionAbsolute(fromAbsolute) || !retentionAbsolute(toAbsolute) || retentionIntersects(fromAbsolute, toAbsolute) {
		return p, ErrUnsafePath
	}
	p = CollectionRetentionPlan{Schema: CollectionRetentionSchema, ID: id, From: fromAbsolute, To: toAbsolute}
	p.Scope, err = s.collectionRetainedScope(ctx, toAbsolute)
	if err != nil {
		return p, err
	}
	if retentionIntersects(fromAbsolute, p.Scope.Path) {
		return p, ErrUnsafePath
	}
	if err = s.guardCollectionRetention(ctx, p); err != nil {
		return p, err
	}
	bindings, journals, err := s.treeBindingsAt(ctx, Source{CollectionID: p.Scope.ID, Path: p.Scope.Path}, p.From, p.To)
	if err != nil {
		return p, err
	}
	for _, binding := range bindings {
		physical := filepath.Join(binding.SourcePath, filepath.FromSlash(binding.File.RelativePath))
		if !within(physical, p.From) {
			return p, fmt.Errorf("%w: destination has an existing file binding", ErrPathBusy)
		}
		scope, e := s.scopeLocation(ctx, binding.File.CollectionID)
		if e != nil {
			return p, e
		}
		if scope.Path != binding.SourcePath || scope.NodeKey != p.Scope.NodeKey || binding.File.ID == "" || validateDirectoryPath(binding.File.RelativePath) != nil || binding.File.PathKey != pathKey(binding.File.RelativePath) || binding.File.Deleted || binding.File.Retained != nil || binding.File.PathVersion >= uint64(1<<63-1) {
			return p, ErrMembership
		}
		destination := p.To + strings.TrimPrefix(physical, p.From)
		relative, e := filepath.Rel(p.Scope.Path, destination)
		if e != nil || validateDirectoryPath(filepath.ToSlash(relative)) != nil {
			return p, ErrUnsafePath
		}
		p.Files = append(p.Files, CollectionRetentionFile{File: binding.File, SourcePath: binding.SourcePath, Location: RetainedLocation{OperationID: p.ID, ScopeID: p.Scope.ID, Root: p.Scope.Path, Path: filepath.ToSlash(relative)}})
	}
	for _, journal := range journals {
		v, e := s.collectionRetentionJournal(ctx, journal.OperationID)
		if e != nil {
			return p, e
		}
		anchor := JournalLocation{ScopeID: v.Collection, RelativePath: v.Path}
		if v.Previous != nil {
			anchor = *v.Previous
		}
		scope, e := s.scopeLocation(ctx, anchor.ScopeID)
		if e != nil {
			return p, e
		}
		if scope.NodeKey != p.Scope.NodeKey || validateDirectoryPath(v.Path) != nil || v.State != Accepted && v.State != Conflict || v.Journal != ".loom-notes-"+identity(v.OperationID) {
			return p, ErrMembership
		}
		v.Location = journal.Location
		p.Journals = append(p.Journals, v)
	}
	p.Digest = collectionRetentionDigest(p)
	return p, nil
}

func retentionIntersects(a, b string) bool {
	return within(pathKey(a), pathKey(b)) || within(pathKey(b), pathKey(a))
}

func (s SQLStore) guardCollectionRetention(ctx context.Context, p CollectionRetentionPlan) error {
	q := s.q().(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	rows, err := q.QueryContext(ctx, `SELECT notes_source_root_id,source_path FROM knowledge.notes_source_roots
 WHERE source_path<>'' AND (COALESCE(node_key,'')='' OR node_key=$1) ORDER BY notes_source_root_id LIMIT 50001`, p.Scope.NodeKey)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var id, root string
		if err = rows.Scan(&id, &root); err != nil {
			break
		}
		count++
		if treeContainsSourceRoot(filepath.Dir(p.From), filepath.Base(p.From), root) || retentionIntersects(root, p.To) {
			err = fmt.Errorf("%w: registered Notes root %s needs its location successor", ErrMembership, id)
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if count > maxTreeEntries {
		return fmt.Errorf("too many Notes roots for a bounded retention plan")
	}
	// These are metadata only. Include plain edits without path_stage and uploads
	// that have not yet produced an operation or a bound destination file.
	rows, err = q.QueryContext(ctx, `WITH scopes AS (
 SELECT s.scope_id,CASE WHEN s.kind='notes' THEN COALESCE(NULLIF(r.source_path,''),s.source_path) ELSE s.source_path END AS root,
 CASE WHEN s.kind='notes' THEN COALESCE(r.node_key,s.node_key,'') ELSE s.node_key END AS node
 FROM notes_workspace.source_scopes s LEFT JOIN knowledge.notes_source_roots r ON s.kind='notes' AND r.notes_source_root_id=s.scope_id
 ) SELECT * FROM (
 SELECT 'operation' AS kind,o.operation_id AS id,COALESCE(s.root,'') AS root,COALESCE(s.node,'') AS node,
 COALESCE(o.record#>>'{file,relative_path}','') AS source,COALESCE(o.record#>>'{request,destination_path}','') AS destination
 FROM notes_workspace.operations o LEFT JOIN scopes s ON s.scope_id=o.record#>>'{file,collection_id}' WHERE o.state IN ('pending','held')
 UNION ALL
 SELECT 'upload',u.import_id,COALESCE(s.root,''),COALESCE(s.node,''),COALESCE(u.record->>'path',''),''
 FROM notes_workspace.desktop_imports u LEFT JOIN scopes s ON s.scope_id=u.record->>'collection'
 WHERE u.record->>'state' IN ('receiving','ready') OR u.record->>'staging_held'='true'
 UNION ALL
 SELECT 'tree',t.move_id,COALESCE(t.record->>'source_root',''),COALESCE(s.node,''),COALESCE(t.record->>'from',''),COALESCE(t.record->>'to','')
 FROM notes_workspace.tree_moves t LEFT JOIN scopes s ON s.scope_id=t.scope_id WHERE t.state IN ('preview','prepared','held')
 ) busy ORDER BY kind,id LIMIT 50001`)
	if err != nil {
		return err
	}
	defer rows.Close()
	count = 0
	for rows.Next() {
		var kind, id, root, node, source, destination string
		if err := rows.Scan(&kind, &id, &root, &node, &source, &destination); err != nil {
			return err
		}
		count++
		if count > maxTreeEntries {
			return fmt.Errorf("too many unfinished operations for a bounded retention plan")
		}
		if id == p.ID {
			continue
		}
		if node != "" && node != p.Scope.NodeKey {
			continue
		}
		for index, relative := range []string{source, destination} {
			if index == 1 && relative == "" {
				continue
			}
			physical, known := scopePathKey(root, relative)
			if !known || retentionIntersects(physical, p.From) || retentionIntersects(physical, p.To) {
				return fmt.Errorf("%w: unfinished %s %s intersects retained custody or has unknown location", ErrPathBusy, kind, id)
			}
		}
	}
	return rows.Err()
}

func (s SQLStore) collectionRetentionJournal(ctx context.Context, id string) (v CollectionRetentionJournal, err error) {
	v.OperationID = id
	var previous []byte
	err = s.q().QueryRowContext(ctx, `SELECT state,COALESCE(record->>'journal',''),COALESCE(record#>>'{file,collection_id}',''),
 COALESCE(record#>>'{file,relative_path}',''),COALESCE(record->'journal_location','null'::jsonb)
 FROM notes_workspace.operations WHERE operation_id=$1`, id).Scan(&v.State, &v.Journal, &v.Collection, &v.Path, &previous)
	if err == nil {
		err = json.Unmarshal(previous, &v.Previous)
	}
	return
}

func collectionRetentionResult(p CollectionRetentionPlan, item CollectionRetentionFile) (File, Operation, PathChange) {
	file := item.File
	file.Deleted, file.PathVersion, file.Retained = true, file.PathVersion+1, &item.Location
	id := "collection_retention_" + identity([]string{p.ID, file.ID})
	op := Operation{Request: Request{OperationID: id, FileID: file.ID, Kind: "collection_retention"}, File: item.File,
		State: Accepted, Reason: "retained by Main collection operation " + p.ID, ResultFile: &file}
	change := PathChange{FileID: file.ID, OperationID: id, Version: file.PathVersion, FromPath: file.RelativePath, Retained: &item.Location}
	return file, op, change
}

// CommitCollectionRetentionTx joins the storage owner's final transaction. It
// never begins/commits/rolls back a transaction or touches payload bytes. On any
// error the caller must roll back its transaction and retain the prepared intent.
func (s SQLStore) CommitCollectionRetentionTx(ctx context.Context, tx *sql.Tx, p CollectionRetentionPlan) error {
	if tx == nil || !s.ExclusiveSource || p.Schema != CollectionRetentionSchema || p.Digest == "" || collectionRetentionDigest(p) != p.Digest {
		return ErrIntentMismatch
	}
	store := SQLStore{DB: s.DB, query: tx, ExclusiveSource: true}
	current, err := store.PlanCollectionRetention(ctx, p.From, p.To, p.ID)
	if err != nil {
		return err
	}
	if current.Digest != p.Digest {
		if len(current.Files) != 0 || len(current.Journals) != 0 || current.Scope != p.Scope {
			return ErrStalePath
		}
		return store.verifyCollectionRetention(ctx, p)
	}
	for _, item := range p.Files {
		file, op, change := collectionRetentionResult(p, item)
		before, _ := json.Marshal(item.File)
		after, _ := json.Marshal(file)
		result, err := tx.ExecContext(ctx, `UPDATE notes_workspace.files SET deleted=true,record=$2
 WHERE file_id=$1 AND NOT deleted AND record=$3::jsonb`, file.ID, after, before)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("%w: retained file binding changed: %v", ErrStalePath, err)
		}
		raw, _ := json.Marshal(op)
		if _, err = tx.ExecContext(ctx, `INSERT INTO notes_workspace.operations(operation_id,file_id,state,record) VALUES($1,$2,'accepted',$3)`, op.Request.OperationID, file.ID, raw); err != nil {
			return err
		}
		raw, _ = json.Marshal(change)
		if _, err = tx.ExecContext(ctx, `INSERT INTO notes_workspace.path_history(file_id,path_version,operation_id,record) VALUES($1,$2,$3,$4)`, file.ID, file.PathVersion, op.Request.OperationID, raw); err != nil {
			return err
		}
	}
	for _, journal := range p.Journals {
		location, _ := json.Marshal(journal.Location)
		previous, _ := json.Marshal(journal.Previous)
		result, err := tx.ExecContext(ctx, `UPDATE notes_workspace.operations SET record=jsonb_set(record,'{journal_location}',$2::jsonb),updated_at=now()
 WHERE operation_id=$1 AND state=$3 AND record->>'journal'=$4 AND record#>>'{file,collection_id}'=$5
 AND record#>>'{file,relative_path}'=$6 AND COALESCE(record->'journal_location','null'::jsonb)=$7::jsonb`, journal.OperationID, location, journal.State, journal.Journal, journal.Collection, journal.Path, previous)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("%w: retained journal location changed: %v", ErrStalePath, err)
		}
	}
	return nil
}

func (s SQLStore) verifyCollectionRetention(ctx context.Context, p CollectionRetentionPlan) error {
	for _, item := range p.Files {
		file, op, change := collectionRetentionResult(p, item)
		f, _ := json.Marshal(file)
		o, _ := json.Marshal(op)
		h, _ := json.Marshal(change)
		var complete bool
		err := s.q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notes_workspace.files WHERE file_id=$1 AND deleted AND record=$2::jsonb)
 AND EXISTS(SELECT 1 FROM notes_workspace.operations WHERE operation_id=$3 AND state='accepted' AND record=$4::jsonb)
 AND EXISTS(SELECT 1 FROM notes_workspace.path_history WHERE file_id=$1 AND path_version=$5 AND operation_id=$3 AND record=$6::jsonb)`, file.ID, f, op.Request.OperationID, o, file.PathVersion, h).Scan(&complete)
		if err != nil {
			return err
		}
		if !complete {
			return ErrStalePath
		}
	}
	for _, expected := range p.Journals {
		actual, err := s.collectionRetentionJournal(ctx, expected.OperationID)
		if err != nil {
			return err
		}
		if actual.Previous == nil || *actual.Previous != expected.Location {
			return ErrStalePath
		}
		actual.Location, actual.Previous = expected.Location, expected.Previous
		if !reflect.DeepEqual(actual, expected) {
			return ErrStalePath
		}
	}
	return nil
}
