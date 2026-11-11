package notesworkspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func (s Service) ApplyMetadataAction(ctx context.Context, id string) (out Operation, err error) {
	if s.Store == nil || s.Sources == nil {
		return out, ErrMembership
	}
	initial, err := s.Store.Operation(ctx, id)
	if err != nil {
		return out, err
	}
	if initial.Base.MetadataAction == nil {
		return out, ErrIntentMismatch
	}
	err = s.Store.WithLock(ctx, "collection:"+initial.File.CollectionID, func(store Store) error {
		op, e := store.Operation(ctx, id)
		if e != nil {
			return e
		}
		out = op
		if op.State == Accepted || op.State == Conflict {
			return nil
		}
		if op.Base.MetadataAction == nil {
			return ErrIntentMismatch
		}
		if e = validateMetadataActionRequest(op.Request); e != nil {
			return e
		}
		current, e := store.File(ctx, op.File.ID)
		if e != nil {
			return e
		}
		if current.Deleted || current.PathVersion != op.Base.PathVersion || current.RelativePath != op.File.RelativePath {
			out, e = pathConflict(ctx, store, op, ErrStalePath.Error(), nil, false)
			return e
		}
		paths := []string{op.File.RelativePath}
		if op.Request.Kind == KindRename {
			paths = append(paths, op.Request.DestinationPath)
		}
		for _, path := range paths {
			if e = validateDirectoryPath(path); e != nil {
				return e
			}
			if busy, e := store.PathBusy(ctx, op.File.CollectionID, pathKey(path), id); e != nil {
				return e
			} else if busy {
				return ErrPathBusy
			}
		}
		resolver, ok := s.Sources.(PathResolver)
		if !ok {
			return ErrMembership
		}
		e = resolver.WithPaths(ctx, op.File.CollectionID, paths, func(src Source) error {
			if src.CollectionID != op.File.CollectionID || src.Generation != op.Request.Generation {
				return ErrMembership
			}
			var applyErr error
			out, applyErr = applyMetadataAction(ctx, store, src, op)
			return applyErr
		})
		if e == nil {
			return nil
		}
		latest, lookupErr := store.Operation(ctx, id)
		if lookupErr != nil {
			return errors.Join(e, lookupErr)
		}
		if latest.State == Accepted || latest.State == Conflict {
			out = latest
			return e
		}
		latest.State, latest.Reason = Held, e.Error()
		out = latest
		return store.SaveOperation(ctx, latest)
	})
	return
}

func applyMetadataAction(ctx context.Context, store Store, src Source, op Operation) (Operation, error) {
	parent, name, err := openSourceParent(src.Path, op.File.RelativePath)
	if err != nil {
		return op, err
	}
	defer parent.Close()
	var target *sourceDirectory
	var targetName string
	if op.Request.Kind == KindRename {
		target, targetName, err = openSourceParent(src.Path, op.Request.DestinationPath)
		if err != nil {
			return op, err
		}
		defer target.Close()
		if other, e := store.FileAt(ctx, op.File.CollectionID, pathKey(op.Request.DestinationPath)); e == nil && other.ID != op.File.ID {
			if op.PathStage != "" {
				return op, ErrPathBusy
			}
			return pathConflict(ctx, store, op, "destination has an active file binding", nil, false)
		} else if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return op, e
		}
	}
	expected := *op.Base.MetadataAction
	var sourceParentStat unix.Stat_t
	if err := unix.Fstat(parent.fd(), &sourceParentStat); err != nil {
		return op, err
	}
	if uint64(sourceParentStat.Dev) != expected.Device {
		return op, fmt.Errorf("mounted or cross-filesystem metadata action is not supported")
	}
	if op.PathStage == "" {
		observed, e := metadataActionAt(parent.fd(), name)
		if e != nil {
			return op, e
		}
		if observed == nil || *observed != expected {
			return pathConflict(ctx, store, op, "source metadata revision changed", nil, observed != nil)
		}
		if target != nil {
			var stat unix.Stat_t
			if e = unix.Fstat(target.fd(), &stat); e != nil {
				return op, e
			}
			if uint64(stat.Dev) != expected.Device {
				return op, fmt.Errorf("cross-filesystem metadata rename is not supported")
			}
			if e = unix.Fstatat(target.fd(), targetName, &stat, unix.AT_SYMLINK_NOFOLLOW); e == nil {
				return pathConflict(ctx, store, op, "destination exists", nil, true)
			} else if !errors.Is(e, unix.ENOENT) {
				return op, e
			}
		}
		op.PathStage = "prepared"
		op.Journal = ".loom-notes-" + identity(op.Request.OperationID)
		physical := expected.inode()
		op.MoveIdentity = &physical
		if target != nil {
			op.DestinationKey = pathKey(op.Request.DestinationPath)
		}
		if e = store.SaveOperation(ctx, op); e != nil {
			return op, e
		}
	}
	journal, err := openJournal(parent, op)
	if err != nil {
		return op, err
	}
	defer journal.Close()
	retained, err := metadataActionAt(int(journal.Fd()), "displaced")
	if err != nil {
		return op, err
	}
	if retained == nil && target != nil {
		landed, e := metadataActionAt(target.fd(), targetName)
		if e != nil {
			return op, e
		}
		if landed != nil && landed.inode() == expected.inode() {
			source, e := metadataActionAt(parent.fd(), name)
			if e != nil {
				return op, e
			}
			if source != nil {
				return op, fmt.Errorf("rename outcome is ambiguous; source and destination both exist")
			}
			if !expected.matchesMoved(landed) {
				return op, fmt.Errorf("moved inode revision changed; retained intent requires review")
			}
			if e = errors.Join(parent.check(), target.check(), parent.current().Sync(), target.current().Sync(), journal.Sync()); e != nil {
				return op, e
			}
			return commitMetadataAction(ctx, store, op, *landed)
		}
	}
	if retained == nil {
		if op.PathStage == "detached" {
			return op, fmt.Errorf("captured inode is missing from journal and expected destination; recovery required")
		}
		observed, e := metadataActionAt(parent.fd(), name)
		if e != nil {
			return op, e
		}
		if observed == nil || observed.inode() != expected.inode() {
			return op, fmt.Errorf("prepared original inode is missing or replaced; recovery required")
		}
		if *observed != expected {
			return pathConflict(ctx, store, op, "source metadata revision changed before capture", nil, observed != nil)
		}
		if e = ctx.Err(); e != nil {
			return op, e
		}
		if e = parent.check(); e != nil {
			return op, e
		}
		if e = renameNoReplace(parent.fd(), name, int(journal.Fd()), "displaced"); e != nil {
			return op, e
		}
		if e = errors.Join(parent.current().Sync(), journal.Sync()); e != nil {
			return op, e
		}
		retained, err = metadataActionAt(int(journal.Fd()), "displaced")
		if err != nil {
			return op, err
		}
	}
	if !expected.matchesMoved(retained) {
		if err := restoreDisplaced(parent, name, journal); err != nil {
			return op, err
		}
		return pathConflict(ctx, store, op, "captured inode differs from metadata base; all versions retained", nil, retained != nil)
	}
	op.PathStage = "detached"
	if err := store.SaveOperation(ctx, op); err != nil {
		return op, err
	}
	if err := ctx.Err(); err != nil {
		return op, err
	}
	if err := parent.check(); err != nil {
		return op, err
	}
	if target != nil {
		if err := target.check(); err != nil {
			return op, err
		}
		if err := checkNameAlias(target.current(), targetName); err != nil {
			return op, err
		}
		if err := renameNoReplace(int(journal.Fd()), "displaced", target.fd(), targetName); errors.Is(err, unix.EEXIST) {
			if err := restoreDisplaced(parent, name, journal); err != nil {
				return op, err
			}
			return pathConflict(ctx, store, op, "destination appeared during rename; source retained", nil, true)
		} else if err != nil {
			return op, err
		}
		if err := errors.Join(target.current().Sync(), journal.Sync()); err != nil {
			return op, err
		}
		retained, err = metadataActionAt(target.fd(), targetName)
		if err != nil {
			return op, err
		}
	}
	if !expected.matchesMoved(retained) {
		return op, fmt.Errorf("moved or retained inode revision changed; recovery required")
	}
	return commitMetadataAction(ctx, store, op, *retained)
}

func commitMetadataAction(ctx context.Context, store Store, op Operation, observed MetadataActionIdentity) (Operation, error) {
	file := op.File
	if file.PathVersion >= uint64(1<<63-1) {
		return op, fmt.Errorf("file path version exhausted")
	}
	file.PathVersion++
	if op.Request.Kind == KindRename {
		file.RelativePath = op.Request.DestinationPath
		file.PathKey = pathKey(file.RelativePath)
	} else {
		file.Deleted = true
	}
	base := makeMetadataActionBase(file, op.Request.Generation, !file.Deleted, observed)
	op.State, op.Reason = Accepted, "renamed original inode without reading payload"
	if file.Deleted {
		op.Reason = "removed original inode into retained journal without reading payload"
	}
	op.ResultFile, op.ResultBaseID = &file, base.ID
	return op, store.CommitPath(ctx, op, file, base)
}
