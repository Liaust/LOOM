package notesworkspace

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"loom.local/loom/internal/workspaceactions"
)

func treeContainsSourceRoot(root, from, registered string) bool {
	moving, ok := scopePathKey(root, from)
	return ok && filepath.IsAbs(registered) && within(pathKey(filepath.Clean(registered)), moving)
}

// Moving a declared root needs its registration owner, not binding retirement.
// Ordinary children of an enclosing root do not change that root's declaration.
func (s SQLStore) guardTreeSourceRoots(ctx context.Context, src Source, from string, destinations ...string) error {
	scope, err := s.scopeLocation(ctx, src.CollectionID)
	if err != nil {
		return err
	}
	query, ok := s.q().(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	})
	if !ok {
		return ErrMembership
	}
	to := ""
	if len(destinations) > 0 && destinations[0] != "" {
		to = filepath.Join(src.Path, destinations[0])
	}
	if err := workspaceactions.GuardOrdinaryMove(ctx, query, filepath.Join(src.Path, from), to); err != nil {
		return fmt.Errorf("%w: %v", ErrMembership, err)
	}
	rows, err := query.QueryContext(ctx, `SELECT notes_source_root_id,source_path FROM knowledge.notes_source_roots
 WHERE source_path<>'' AND ($1='' OR COALESCE(node_key,'')='' OR node_key=$1) ORDER BY notes_source_root_id`, scope.NodeKey)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			return err
		}
		if treeContainsSourceRoot(src.Path, from, path) || to != "" && workspaceactions.Within(path, to) {
			return fmt.Errorf("%w: moving registered Notes source root %s requires its relocation owner", ErrMembership, id)
		}
	}
	return rows.Err()
}
