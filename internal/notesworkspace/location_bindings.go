package notesworkspace

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
)

// The project location owner supplies the already admitted fixed Box scope and
// holds the exclusive filesystem barrier. This grants no new source authority.
func (s SQLStore) PlanLocationBindings(ctx context.Context, src Source, from, to, id string) (TreeMove, error) {
	move := TreeMove{ID: id, ScopeID: src.CollectionID, Generation: src.Generation, SourcePath: src.Path, From: from, To: to, State: "preview"}
	if !s.ExclusiveSource || validateDirectoryPath(from) != nil || validateDirectoryPath(to) != nil || within(from, to) || within(to, from) {
		return move, ErrUnsafePath
	}
	var err error
	move.Bindings, move.Journals, err = s.treeBindings(ctx, src, from, to)
	if err != nil {
		return move, err
	}
	for _, binding := range move.Bindings {
		if binding.Retire {
			return move, fmt.Errorf("location move would leave writable source %s; its location successor is required", binding.File.CollectionID)
		}
	}
	for _, relative := range []string{from, to} {
		busy, e := s.PathBusy(ctx, src.CollectionID, pathKey(relative), id)
		if e != nil {
			return move, e
		}
		if busy {
			return move, ErrPathBusy
		}
	}
	move.SourceKey = pathKey(filepath.Join(src.Path, from))
	move.DestinationKey = pathKey(filepath.Join(src.Path, to))
	move.Hash = treeDigest(move)
	return move, nil
}

func CommitLocationBindingsTx(ctx context.Context, tx *sql.Tx, move TreeMove) error {
	if tx == nil || move.Hash != treeDigest(move) {
		return ErrIntentMismatch
	}
	return commitTreeBindings(ctx, tx, move)
}
