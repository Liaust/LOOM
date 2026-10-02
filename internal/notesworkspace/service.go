package notesworkspace

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type Service struct {
	Sources Resolver
	Store   Store
}

// Read binds an existing or absent path to a stable ID, then persists exact bytes
// before returning a base. An absent base is the only create authorization.
func (s Service) Read(ctx context.Context, collection, relative string) (out ReadResult, err error) {
	if s.Store == nil || s.Sources == nil {
		return out, fmt.Errorf("notes workspace is not configured")
	}
	if err = validateSourcePath(relative, true); err != nil {
		return out, err
	}
	err = s.Store.WithLock(ctx, "collection:"+collection, func(store Store) error {
		if busy, err := store.PathBusy(ctx, collection, pathKey(relative), ""); err != nil {
			return err
		} else if busy {
			return ErrPathBusy
		}
		return s.Sources.WithSource(ctx, collection, relative, func(src Source) error {
			if src.CollectionID != collection || src.Generation == "" {
				return ErrMembership
			}
			parent, name, err := openSourceParent(src.Path, relative)
			if err != nil {
				return err
			}
			defer parent.Close()
			if err := parent.check(); err != nil {
				return err
			}
			limit := int64(MaxContentBytes)
			if IsReferencePath(relative) {
				limit = MaxReferenceBytes
			}
			content, exists, err := readBytesAt(parent.current(), name, limit)
			if err == nil && !IsReferencePath(relative) {
				err = validateContent(content)
			}
			if err == nil && IsReferencePath(relative) && !exists {
				err = ErrReferenceOnly
			}
			if err != nil {
				return err
			}
			id, err := newFileID()
			if err != nil {
				return err
			}
			f, err := store.Bind(ctx, File{ID: id, CollectionID: collection, RelativePath: relative, PathKey: pathKey(relative)})
			if err != nil {
				return err
			}
			if f.RelativePath != relative {
				return fmt.Errorf("source path case/Unicode collision")
			}
			base := makeBase(f, src.Generation, exists, content)
			if err := store.SaveBase(ctx, base); err != nil {
				return err
			}
			out = ReadResult{File: f, Base: base}
			return nil
		})
	})
	return
}

// Receive stores the exact request before any source mutation. Unknown bases and
// ambiguous transport operations must be held by the transport, never guessed.
func (s Service) Receive(ctx context.Context, request Request) (Operation, error) {
	if s.Store == nil {
		return Operation{}, fmt.Errorf("notes workspace is not configured")
	}
	if request.OperationID == "" || len(request.OperationID) > 1024 {
		return Operation{}, fmt.Errorf("operation identity required (at most 1024 bytes)")
	}
	if err := validateRequest(request); err != nil {
		return Operation{}, err
	}
	// An existing receipt replays against its original file snapshot after moves.
	if old, err := s.Store.Operation(ctx, request.OperationID); err == nil {
		if !sameIntent(old, Operation{Request: request}) {
			return Operation{}, ErrIntentMismatch
		}
		return old, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	if err := validateContent(request.Content); err != nil {
		return Operation{}, err
	}
	file, err := s.Store.File(ctx, request.FileID)
	if err != nil {
		return Operation{}, err
	}
	if err := validatePath(file.RelativePath); err != nil {
		return Operation{}, err
	}
	base, err := s.Store.Base(ctx, request.BaseID)
	if err != nil {
		return Operation{}, err
	}
	if requestKind(request) != KindEdit && !base.Exists {
		return Operation{}, fmt.Errorf("rename/delete requires exact existing base")
	}
	if base.FileID != file.ID || base.Generation != request.Generation {
		return Operation{}, ErrMembership
	}
	return s.Store.Receive(ctx, Operation{Request: request, File: file, Base: base, State: Pending})
}

// Apply also resumes saved pending/held operations after process restart. Final
// acknowledgements are replayed unchanged even if the live source later advances.
func (s Service) Apply(ctx context.Context, id string) (out Operation, err error) {
	if s.Store == nil || s.Sources == nil {
		return out, fmt.Errorf("notes workspace is not configured")
	}
	op, err := s.Store.Operation(ctx, id)
	if err != nil {
		return out, err
	}
	err = s.Store.WithLock(ctx, "collection:"+op.File.CollectionID, func(store Store) error {
		op, err = store.Operation(ctx, id)
		if err != nil {
			return err
		}
		if op.State == Accepted || op.State == Conflict {
			out = op
			return nil
		}
		current, e := store.File(ctx, op.File.ID)
		if e != nil {
			return e
		}
		if current.Deleted || current.PathVersion != op.Base.PathVersion || current.RelativePath != op.File.RelativePath {
			op.State, op.Reason = Conflict, ErrStalePath.Error()
			if err := store.SaveOperation(ctx, op); err != nil {
				return err
			}
			out = op
			return nil
		}
		if busy, e := store.PathBusy(ctx, op.File.CollectionID, op.File.PathKey, id); e != nil {
			return e
		} else if busy {
			return ErrPathBusy
		}
		guard := func(fn func(Source) error) error {
			return s.Sources.WithSource(ctx, op.File.CollectionID, op.File.RelativePath, fn)
		}
		if requestKind(op.Request) == KindRename {
			paths, ok := s.Sources.(PathResolver)
			if !ok {
				return fmt.Errorf("source resolver does not support fenced rename paths")
			}
			guard = func(fn func(Source) error) error {
				return paths.WithPaths(ctx, op.File.CollectionID, []string{op.File.RelativePath, op.Request.DestinationPath}, fn)
			}
		}
		guardErr := guard(func(src Source) error {
			if src.CollectionID != op.File.CollectionID || src.Generation != op.Request.Generation {
				return ErrMembership
			}
			if err := validatePath(op.File.RelativePath); err != nil {
				return err
			}
			if requestKind(op.Request) != KindEdit {
				result, err := applyPath(ctx, store, src, op)
				if err == nil {
					out = result
				}
				return err
			}
			parent, name, err := openSourceParent(src.Path, op.File.RelativePath)
			if err != nil {
				return err
			}
			defer parent.Close()
			result, err := publish(ctx, store, parent, name, op)
			if err != nil {
				return err
			}
			op = result
			if op.State == Accepted {
				base := makeBase(op.File, src.Generation, true, op.Request.Content)
				if err := store.SaveBase(ctx, base); err != nil {
					return err
				}
				op.ResultBaseID = base.ID
				resultFile := op.File
				op.ResultFile = &resultFile
			}
			if err := store.SaveOperation(ctx, op); err != nil {
				return err
			}
			out = op
			return nil
		})
		if guardErr != nil {
			// Preserve the exact pending/base bytes and recovery location. Never treat
			// membership withdrawal or I/O failure as source deletion or a new base.
			latest, e := store.Operation(ctx, id)
			if e != nil {
				return errors.Join(guardErr, e)
			}
			// A source-fence cleanup or ambiguous database commit may fail AFTER
			// the immutable receipt committed. Never demote that durable outcome.
			if latest.State == Accepted || latest.State == Conflict {
				out = latest
				return guardErr
			}
			latest.State, latest.Reason = Held, guardErr.Error()
			if e = store.SaveOperation(ctx, latest); e != nil {
				return errors.Join(guardErr, e)
			}
			out = latest
		}
		return nil
	})
	return
}
func sameIntent(a, b Operation) bool {
	return requestKind(a.Request) == requestKind(b.Request) && a.Request.DestinationPath == b.Request.DestinationPath && a.Request.OperationID == b.Request.OperationID && a.Request.FileID == b.Request.FileID && a.Request.Generation == b.Request.Generation && a.Request.BaseID == b.Request.BaseID && bytes.Equal(a.Request.Content, b.Request.Content)
}

func validateRequest(r Request) error {
	switch requestKind(r) {
	case KindEdit:
		if r.DestinationPath != "" {
			return fmt.Errorf("edit cannot include destination")
		}
	case KindDelete:
		if r.DestinationPath != "" || len(r.Content) != 0 {
			return fmt.Errorf("delete cannot include destination or content")
		}
	case KindRename:
		if len(r.Content) != 0 {
			return fmt.Errorf("rename content must be a separate edit")
		}
		if err := validatePath(r.DestinationPath); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported Notes operation kind")
	}
	return nil
}
