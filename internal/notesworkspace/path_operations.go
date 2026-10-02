package notesworkspace

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type inodeIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func inodeAt(parent *os.File, name string) (inodeIdentity, bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return inodeIdentity{}, false, nil
	}
	if err != nil {
		return inodeIdentity{}, false, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return inodeIdentity{}, true, ErrUnsafePath
	}
	return inodeIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, true, nil
}

// applyPath uses the source-parent journal for both operations. Rename transfers
// the original inode (and thus its metadata); delete retains it as displaced.
// Prepared operation rows reserve both paths until a terminal receipt commits.
func applyPath(ctx context.Context, store Store, src Source, op Operation) (out Operation, resultErr error) {
	parent, name, err := openSourceParent(src.Path, op.File.RelativePath)
	if err != nil {
		return op, err
	}
	defer parent.Close()
	var destination *sourceDirectory
	var destinationName string
	if requestKind(op.Request) == KindRename {
		if err := validatePath(op.Request.DestinationPath); err != nil {
			return op, err
		}
		if pathKey(op.Request.DestinationPath) == op.File.PathKey {
			return op, fmt.Errorf("rename requires a distinct non-alias destination")
		}
		destination, destinationName, err = openSourceParent(src.Path, op.Request.DestinationPath)
		if err != nil {
			return op, err
		}
		defer destination.Close()
		key := pathKey(op.Request.DestinationPath)
		if busy, err := store.PathBusy(ctx, op.File.CollectionID, key, op.Request.OperationID); err != nil {
			return op, err
		} else if busy {
			return op, ErrPathBusy
		}
		if _, err := store.FileAt(ctx, op.File.CollectionID, key); err == nil {
			return pathConflict(ctx, store, op, "destination already has an active file identity", nil, false)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return op, err
		}
		op.DestinationKey = key
	}
	if op.PathStage == "" {
		content, exists, err := readSource(parent, name)
		if err != nil {
			return op, err
		}
		if !exists || !bytes.Equal(content, op.Base.Content) {
			return pathConflict(ctx, store, op, "source diverged from exact path-operation base", content, exists)
		}
		if destination != nil {
			content, exists, err := readSource(destination, destinationName)
			if err != nil {
				return op, err
			}
			if exists {
				return pathConflict(ctx, store, op, "rename destination exists", content, true)
			}
		}
		op.PathStage = "prepared"
		op.Journal = ".loom-notes-" + identity(op.Request.OperationID)
		if err := store.SaveOperation(ctx, op); err != nil {
			return op, err
		}
	}
	journal, err := openJournal(parent, op)
	if err != nil {
		return op, err
	}
	defer journal.Close()
	committed := false
	defer func() {
		if !committed && resultErr != nil && !errors.Is(resultErr, ErrPathCommitUncertain) {
			if err := restoreDisplaced(parent, name, journal); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("restore path-operation source: %w", err))
			}
		}
	}()
	displaced, detached, err := readAt(journal, "displaced")
	if err != nil {
		return op, err
	}
	if !detached && op.MoveIdentity != nil {
		if destination != nil {
			actual, exists, err := inodeAt(destination.current(), destinationName)
			if err != nil {
				return op, err
			}
			if exists && actual == *op.MoveIdentity {
				if err := destination.check(); err != nil {
					return op, err
				}
				if err := verifyRenameResult(destination, destinationName, op); err != nil {
					return op, err
				}
				// An interrupted finalization must not mint a fresh identity at destination.
				out, err = commitPathResult(ctx, store, op)
				committed = err == nil
				return out, err
			}
		}
		actual, exists, err := inodeAt(parent.current(), name)
		if err != nil {
			return op, err
		}
		if !exists || actual != *op.MoveIdentity {
			return op, fmt.Errorf("original inode no longer at source, journal or rename destination; recovery required")
		}
	}
	if !detached {
		current, exists, err := readSource(parent, name)
		if err != nil {
			return op, err
		}
		if !exists || !bytes.Equal(current, op.Base.Content) {
			return pathConflict(ctx, store, op, "source diverged before displacement", current, exists)
		}
		if err := ctx.Err(); err != nil {
			return op, err
		}
		if err := parent.check(); err != nil {
			return op, err
		}
		if err := renameNoReplace(parent.fd(), name, int(journal.Fd()), "displaced"); err != nil {
			return op, err
		}
		if err := errors.Join(parent.current().Sync(), journal.Sync()); err != nil {
			return op, err
		}
		displaced, detached, err = readAt(journal, "displaced")
		if err != nil {
			return op, err
		}
	}
	if !detached {
		return op, fmt.Errorf("path-operation displacement missing")
	}
	actual, exists, err := inodeAt(journal, "displaced")
	if err != nil {
		return op, err
	}
	if !exists {
		return op, fmt.Errorf("path-operation inode missing")
	}
	if op.MoveIdentity != nil && actual != *op.MoveIdentity {
		return op, ErrIntentMismatch
	}
	op.MoveIdentity = &actual
	op.RetainedHash = digest(displaced)
	if !bytes.Equal(displaced, op.Base.Content) {
		if err := restoreDisplaced(parent, name, journal); err != nil {
			return op, err
		}
		return pathConflict(ctx, store, op, "displaced inode diverged from exact base", displaced, true)
	}
	// The operation's base is already durable; a journal snapshot also survives
	// rename transferring the retained inode to its new canonical destination.
	if err := writeAt(journal, "original", op.Base.Content); err != nil {
		return op, err
	}
	op.PathStage = "detached"
	if err := store.SaveOperation(ctx, op); err != nil {
		return op, err
	}
	if err := journal.Sync(); err != nil {
		return op, err
	}
	if err := ctx.Err(); err != nil {
		return op, err
	}
	if err := parent.check(); err != nil {
		return op, err
	}
	if destination != nil {
		if err := destination.check(); err != nil {
			return op, err
		}
		if err := checkNameAlias(destination.current(), destinationName); err != nil {
			return op, err
		}
		err := renameNoReplace(int(journal.Fd()), "displaced", destination.fd(), destinationName)
		if errors.Is(err, unix.EEXIST) {
			current, exists, readErr := readSource(destination, destinationName)
			if readErr != nil {
				return op, readErr
			}
			if err := restoreDisplaced(parent, name, journal); err != nil {
				return op, err
			}
			return pathConflict(ctx, store, op, "rename destination appeared during publication", current, exists)
		}
		if err != nil {
			return op, err
		}
		if err := errors.Join(destination.current().Sync(), journal.Sync()); err != nil {
			return op, err
		}
		published, exists, err := inodeAt(destination.current(), destinationName)
		if err != nil {
			return op, err
		}
		if !exists || published != actual {
			return op, fmt.Errorf("rename destination replaced after publication; recovery required")
		}
		if err := verifyRenameResult(destination, destinationName, op); err != nil {
			return op, err
		}
	}
	out, err = commitPathResult(ctx, store, op)
	committed = err == nil
	return out, err
}

func pathConflict(ctx context.Context, store Store, op Operation, reason string, observed []byte, exists bool) (Operation, error) {
	op.State, op.Reason, op.Observed, op.ObservedExists = Conflict, reason, observed, exists
	return op, store.SaveOperation(ctx, op)
}
func commitPathResult(ctx context.Context, store Store, op Operation) (Operation, error) {
	file := op.File
	if file.PathVersion >= uint64(1<<63-1) {
		return op, fmt.Errorf("file path version exhausted")
	}
	file.PathVersion++
	content := op.Base.Content
	exists := requestKind(op.Request) == KindRename
	if exists {
		file.RelativePath = op.Request.DestinationPath
		file.PathKey = pathKey(file.RelativePath)
	} else {
		file.Deleted = true
		content = nil
	}
	base := makeBase(file, op.Request.Generation, exists, content)
	op.State = Accepted
	if exists {
		op.Reason = "renamed original inode"
	} else {
		op.Reason = "deleted into retained journal"
	}
	op.ResultBaseID, op.ResultFile = base.ID, &file
	return op, store.CommitPath(ctx, op, file, base)
}

// A rename never adopts newly observed bytes as the incoming intent's base. Late
// writes after movement hold finalization, so a bridge child edit cannot erase them.
func verifyRenameResult(parent *sourceDirectory, name string, op Operation) error {
	content, exists, err := readSource(parent, name)
	if err != nil {
		return err
	}
	if !exists || !bytes.Equal(content, op.Base.Content) {
		return fmt.Errorf("renamed inode content changed before acknowledgement; recovery required")
	}
	actual, present, err := inodeAt(parent.current(), name)
	if err != nil {
		return err
	}
	if !present || op.MoveIdentity == nil || actual != *op.MoveIdentity {
		return fmt.Errorf("rename destination identity changed before acknowledgement; recovery required")
	}
	return nil
}
