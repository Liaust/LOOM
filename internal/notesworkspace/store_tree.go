package notesworkspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

func (s SQLStore) TreeMove(ctx context.Context, id string) (move TreeMove, err error) {
	var raw []byte
	err = s.q().QueryRowContext(ctx, `SELECT record FROM notes_workspace.tree_moves WHERE move_id=$1`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return move, fs.ErrNotExist
	}
	if err == nil {
		err = json.Unmarshal(raw, &move)
	}
	return
}
func (s SQLStore) saveTreeMove(ctx context.Context, move TreeMove, create bool) error {
	raw, err := json.Marshal(move)
	if err != nil {
		return err
	}
	if create {
		_, err = s.q().ExecContext(ctx, `INSERT INTO notes_workspace.tree_moves(move_id,scope_id,state,record) VALUES($1,$2,$3,$4)`, move.ID, move.ScopeID, move.State, raw)
	} else {
		_, err = s.q().ExecContext(ctx, `UPDATE notes_workspace.tree_moves SET state=$2,record=$3,updated_at=now() WHERE move_id=$1`, move.ID, move.State, raw)
	}
	return err
}

func (s SQLStore) treeBindings(ctx context.Context, src Source, from, to string) (bindings []TreeBinding, journals []TreeJournal, err error) {
	return s.treeBindingsAt(ctx, src, filepath.Join(src.Path, filepath.FromSlash(from)), filepath.Join(src.Path, filepath.FromSlash(to)))
}

// journalScope is custody for finalized journals, never a writable resolver.
func (s SQLStore) treeBindingsAt(ctx context.Context, journalScope Source, fromAbsolute, toAbsolute string) (bindings []TreeBinding, journals []TreeJournal, err error) {
	query, ok := s.q().(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	if !ok {
		return nil, nil, ErrMembership
	}
	rows, err := query.QueryContext(ctx, `SELECT f.record,CASE WHEN s.kind='notes' THEN COALESCE(NULLIF(r.source_path,''),s.source_path) ELSE s.source_path END
 FROM notes_workspace.files f JOIN notes_workspace.source_scopes s ON f.collection_id=s.scope_id
 LEFT JOIN knowledge.notes_source_roots r ON s.kind='notes' AND r.notes_source_root_id=s.scope_id
 WHERE NOT f.deleted AND s.source_path<>'' ORDER BY f.collection_id,f.path_key`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var raw []byte
		var root string
		if err = rows.Scan(&raw, &root); err != nil {
			break
		}
		var file File
		if err = json.Unmarshal(raw, &file); err != nil {
			break
		}
		physical := filepath.Join(root, filepath.FromSlash(file.RelativePath))
		if within(physical, toAbsolute) {
			if len(bindings) >= maxTreeEntries {
				err = fmt.Errorf("move has too many bound files")
				break
			}
			bindings = append(bindings, TreeBinding{File: file, SourcePath: root, Retire: true, RetireReason: "previous binding at the absent move destination; history retained"})
			continue
		}
		if !within(physical, fromAbsolute) {
			continue
		}
		if len(bindings) >= maxTreeEntries {
			err = fmt.Errorf("move has too many bound files")
			break
		}
		destination := toAbsolute + strings.TrimPrefix(physical, fromAbsolute)
		binding := TreeBinding{File: file, SourcePath: root}
		if within(destination, root) {
			binding.Destination = filepath.ToSlash(strings.TrimPrefix(destination, root+string(filepath.Separator)))
		} else {
			binding.Retire = true
			binding.RetireReason = "moved outside prior scope; bytes retained at " + destination
		}
		bindings = append(bindings, binding)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	sort.SliceStable(bindings, func(i, j int) bool { return bindings[i].Retire && !bindings[j].Retire })
	// Read journal metadata only, never retained PDF/text payloads.
	rows, err = query.QueryContext(ctx, `SELECT o.operation_id,o.state,
 CASE WHEN s.kind='notes' THEN COALESCE(NULLIF(r.source_path,''),s.source_path) ELSE s.source_path END,
 COALESCE(o.record#>>'{journal_location,relative_path}',o.record#>>'{file,relative_path}')
 FROM notes_workspace.operations o JOIN notes_workspace.source_scopes s
 ON s.scope_id=COALESCE(o.record#>>'{journal_location,scope_id}',o.record#>>'{file,collection_id}')
 LEFT JOIN knowledge.notes_source_roots r ON s.kind='notes' AND r.notes_source_root_id=s.scope_id
 WHERE COALESCE(o.record->>'journal','')<>'' ORDER BY o.operation_id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, state, root, anchor string
		if err = rows.Scan(&id, &state, &root, &anchor); err != nil {
			return nil, nil, err
		}
		if root == "" {
			continue
		}
		physical := filepath.Join(root, filepath.FromSlash(anchor))
		if !within(filepath.Dir(physical), fromAbsolute) {
			continue
		}
		if state == Pending || state == Held {
			return nil, nil, fmt.Errorf("%w: unfinished source journal %s inside move", ErrPathBusy, id)
		}
		if len(journals) >= maxTreeEntries {
			return nil, nil, fmt.Errorf("move contains too many retained journals")
		}
		destination := toAbsolute + strings.TrimPrefix(physical, fromAbsolute)
		relative, e := filepath.Rel(journalScope.Path, destination)
		if e != nil || strings.HasPrefix(relative, "..") {
			return nil, nil, ErrUnsafePath
		}
		journals = append(journals, TreeJournal{OperationID: id, Location: JournalLocation{ScopeID: journalScope.CollectionID, RelativePath: filepath.ToSlash(relative)}})
	}
	return bindings, journals, rows.Err()
}

func (s SQLStore) commitTree(ctx context.Context, move TreeMove) error {
	beginner, ok := s.q().(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return ErrMembership
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = commitTreeBindings(ctx, tx, move); err != nil {
		return err
	}
	store := SQLStore{DB: s.DB, query: tx, ExclusiveSource: true}
	move.State, move.Reason = Accepted, "moved original directory inode on Main; descendant identities and journals retained"
	if err = store.saveTreeMove(ctx, move, false); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(ErrPathCommitUncertain, err)
	}
	return nil
}

func commitTreeBindings(ctx context.Context, tx *sql.Tx, move TreeMove) error {
	store := SQLStore{query: tx, ExclusiveSource: true}
	for _, binding := range move.Bindings {
		file := binding.File
		if file.PathVersion >= uint64(1<<63-1) {
			return fmt.Errorf("file path version exhausted")
		}
		file.PathVersion++
		file.Deleted = binding.Retire
		if !binding.Retire {
			if other, e := store.FileAt(ctx, file.CollectionID, pathKey(binding.Destination)); e == nil && other.ID != file.ID {
				return fmt.Errorf("%w: destination binding exists", ErrPathBusy)
			} else if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			file.RelativePath = binding.Destination
			file.PathKey = pathKey(file.RelativePath)
		}
		raw, e := json.Marshal(file)
		if e != nil {
			return e
		}
		result, e := tx.ExecContext(ctx, `UPDATE notes_workspace.files SET path_key=$2,deleted=$3,record=$4 WHERE file_id=$1 AND NOT deleted AND COALESCE((record->>'path_version')::bigint,0)=$5`, file.ID, file.PathKey, file.Deleted, raw, binding.File.PathVersion)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrStalePath
		}
		id := move.ID
		if file.ID != move.File.ID {
			id = "tree_child_" + identity([]string{move.ID, file.ID})
		}
		op := Operation{Request: Request{OperationID: id, FileID: file.ID, Kind: "tree_move", Generation: move.Generation, DestinationPath: binding.Destination}, File: binding.File, State: Accepted, Reason: "moved by Main directory operation " + move.ID, ResultFile: &file}
		if binding.Retire {
			op.Reason = binding.RetireReason
		}
		raw, e = json.Marshal(op)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO notes_workspace.operations(operation_id,file_id,state,record) VALUES($1,$2,$3,$4)`, id, file.ID, Accepted, raw); e != nil {
			return e
		}
		change := PathChange{FileID: file.ID, OperationID: id, Version: file.PathVersion, FromPath: binding.File.RelativePath, Deleted: binding.Retire}
		if !binding.Retire {
			change.ToPath = file.RelativePath
		}
		raw, e = json.Marshal(change)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO notes_workspace.path_history(file_id,path_version,operation_id,record) VALUES($1,$2,$3,$4)`, file.ID, file.PathVersion, id, raw); e != nil {
			return e
		}
	}
	for _, journal := range move.Journals {
		raw, e := json.Marshal(journal.Location)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE notes_workspace.operations SET record=jsonb_set(record,'{journal_location}',$2::jsonb),updated_at=now() WHERE operation_id=$1 AND state IN ('accepted','conflict')`, journal.OperationID, raw); e != nil {
			return e
		}
	}
	return nil
}

// WithJournalLocation is a read-only retained-custody fence after a Main tree
// move. The original intent/generation is not rewritten into new write authority.
func (s SQLStore) WithJournalLocation(ctx context.Context, location JournalLocation, fn func(Source) error) error {
	if fn == nil || validateDirectoryPath(location.RelativePath) != nil {
		return ErrUnsafePath
	}
	if s.DB == nil && s.query == nil {
		return ErrMembership
	}
	var root, node, generation string
	if err := s.q().QueryRowContext(ctx, `SELECT source_path,node_key,root_identity FROM notes_workspace.source_scopes WHERE scope_id=$1 AND kind IN ('box','retained')`, location.ScopeID).Scan(&root, &node, &generation); err != nil {
		return err
	}
	p, _, err := openSourceParent(root, "journal-root-check")
	if err != nil {
		return err
	}
	defer p.Close()
	var stat unix.Stat_t
	if err = unix.Fstat(p.fd(), &stat); err != nil {
		return err
	}
	if FilesystemGeneration(location.ScopeID, node, root, uint64(stat.Dev), uint64(stat.Ino)) != generation {
		return ErrMembership
	}
	if err = fn(Source{CollectionID: location.ScopeID, Path: root, Generation: generation}); err != nil {
		return err
	}
	return p.check()
}
