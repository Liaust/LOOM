package notesworkspacesync

import (
	"context"

	"loom.local/loom/internal/notesworkspace"
)

// Source owns all pending bytes, membership checks, filesystem writes and durable outcomes.
type Source interface {
	WithExport(context.Context, string, string, func(notesworkspace.File, notesworkspace.Base) error) error
	Read(context.Context, string, string) (notesworkspace.ReadResult, error)
	File(context.Context, string) (notesworkspace.File, error)
	Base(context.Context, string) (notesworkspace.Base, error)
	Save(context.Context, SourceRequest) (Outcome, error)
}
type SourceRequest struct {
	OperationID, FileID, Generation, BaseID, Kind, DestinationPath string
	Content                                                        []byte
}
type Outcome struct {
	State        string
	ResultBaseID string
	ResultFile   notesworkspace.File
}

// NotesSource delegates every mutation to the canonical source journal.
type NotesSource struct {
	Service notesworkspace.Service
}

func (s NotesSource) Stage(ctx context.Context, r SourceRequest) error {
	_, err := s.Service.Receive(ctx, notesworkspace.Request{OperationID: r.OperationID, FileID: r.FileID,
		Generation: r.Generation, BaseID: r.BaseID, Content: r.Content, Kind: r.Kind})
	return err
}
func (s NotesSource) Operation(ctx context.Context, id string) (notesworkspace.Operation, error) {
	return s.Service.Store.Operation(ctx, id)
}

func (s NotesSource) Read(c context.Context, id, p string) (notesworkspace.ReadResult, error) {
	return s.Service.Read(c, id, p)
}
func (s NotesSource) File(c context.Context, id string) (notesworkspace.File, error) {
	return s.Service.Store.File(c, id)
}
func (s NotesSource) Base(c context.Context, id string) (notesworkspace.Base, error) {
	return s.Service.Store.Base(c, id)
}
func (s NotesSource) Save(c context.Context, r SourceRequest) (Outcome, error) {
	kind := r.Kind
	switch kind {
	case "create":
		kind = notesworkspace.KindEdit // creation requires the exact absent base
	case notesworkspace.KindEdit, notesworkspace.KindRename, notesworkspace.KindDelete:
	default:
		return Outcome{}, ErrHeld
	}
	_, err := s.Service.Receive(c, notesworkspace.Request{OperationID: r.OperationID, FileID: r.FileID, Generation: r.Generation, BaseID: r.BaseID, Content: r.Content, Kind: kind, DestinationPath: r.DestinationPath})
	if err != nil {
		return Outcome{}, err
	}
	op, err := s.Service.Apply(c, r.OperationID)
	file := op.File
	if op.ResultFile != nil {
		file = *op.ResultFile
	}
	return Outcome{State: op.State, ResultBaseID: op.ResultBaseID, ResultFile: file}, err
}

// WithExport retains the source selection/lifecycle fence through native export.
// It authorizes existing persisted bytes; it never refreshes an incoming base.
func (s NotesSource) WithExport(ctx context.Context, fileID, baseID string, fn func(notesworkspace.File, notesworkspace.Base) error) error {
	file, err := s.Service.Store.File(ctx, fileID)
	if err != nil {
		return err
	}
	return s.Service.Store.WithLock(ctx, "collection:"+file.CollectionID, func(store notesworkspace.Store) error {
		file, err := store.File(ctx, fileID)
		if err != nil {
			return err
		}
		base, err := store.Base(ctx, baseID)
		if err != nil {
			return err
		}
		if base.FileID != file.ID || !base.Exists || file.Deleted || file.PathVersion != base.PathVersion {
			return ErrHeld
		}
		if busy, err := store.PathBusy(ctx, file.CollectionID, file.PathKey, ""); err != nil {
			return err
		} else if busy {
			return notesworkspace.ErrPathBusy
		}
		return s.Service.Sources.WithSource(ctx, file.CollectionID, file.RelativePath, func(src notesworkspace.Source) error {
			if src.CollectionID != file.CollectionID || src.Generation != base.Generation {
				return notesworkspace.ErrMembership
			}
			return fn(file, base)
		})
	})
}
