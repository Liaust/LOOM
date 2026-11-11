package notesworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// ReconcileMetadataAction observes retained inode custody, not payload equality.
// An accepted receipt is immutable even when an already-open writer advances it.
func (s Service) ReconcileMetadataAction(ctx context.Context, id string) (out Recovery, err error) {
	if s.Store == nil || s.Sources == nil {
		return out, ErrMembership
	}
	op, err := s.Store.Operation(ctx, id)
	if err != nil {
		return out, err
	}
	if op.Base.MetadataAction == nil {
		return out, ErrIntentMismatch
	}
	err = s.Store.WithLock(ctx, "collection:"+op.File.CollectionID, func(store Store) error {
		op, e := store.Operation(ctx, id)
		if e != nil {
			return e
		}
		out = Recovery{OperationID: id, State: "clean", Reason: "metadata-only observation; payload equality is not verified"}
		if op.Journal == "" {
			return nil
		}
		guard := func(fn func(Source) error) error {
			return s.Sources.WithSource(ctx, op.File.CollectionID, op.File.RelativePath, fn)
		}
		if op.JournalLocation != nil {
			reader, ok := store.(interface {
				WithJournalLocation(context.Context, JournalLocation, func(Source) error) error
			})
			if !ok {
				return ErrMembership
			}
			guard = func(fn func(Source) error) error { return reader.WithJournalLocation(ctx, *op.JournalLocation, fn) }
		} else if retained, ok := s.Sources.(RetainedResolver); ok && (op.State == Accepted || op.State == Conflict) {
			guard = func(fn func(Source) error) error {
				return retained.WithRetainedSource(ctx, op.File.CollectionID, op.File.RelativePath, op.Request.Generation, fn)
			}
		}
		e = guard(func(src Source) error {
			relative := op.File.RelativePath
			if op.JournalLocation != nil {
				if src.CollectionID != op.JournalLocation.ScopeID {
					return ErrMembership
				}
				relative = op.JournalLocation.RelativePath
			} else if src.CollectionID != op.File.CollectionID || src.Generation != op.Request.Generation {
				return ErrMembership
			}
			if e := validateDirectoryPath(relative); e != nil {
				return e
			}
			parent, _, e := openSourceParent(src.Path, relative)
			if e != nil {
				return e
			}
			defer parent.Close()
			if e = parent.check(); e != nil {
				return e
			}
			if op.Journal != ".loom-notes-"+identity(op.Request.OperationID) {
				return ErrIntentMismatch
			}
			fd, e := unix.Openat(parent.fd(), op.Journal, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if e != nil {
				return e
			}
			journal := os.NewFile(uintptr(fd), op.Journal)
			defer journal.Close()
			info, e := journal.Stat()
			if e != nil {
				return e
			}
			if info.Mode().Perm() != 0700 {
				return ErrUnsafePath
			}
			intent, exists, e := readAt(journal, "intent")
			if e != nil {
				return e
			}
			if !exists || string(intent) != identity(op.Request) {
				return ErrIntentMismatch
			}
			retained, e := metadataActionAt(fd, "displaced")
			if e != nil {
				return e
			}
			if retained == nil {
				if op.State == Accepted && op.Request.Kind == KindDelete {
					return fmt.Errorf("retained deletion inode missing")
				}
				if op.State != Accepted && op.State != Conflict {
					return fmt.Errorf("unfinished metadata action requires exact operation replay")
				}
				return parent.check()
			}
			out.RetainedExists = true
			if retained.inode() != op.Base.MetadataAction.inode() {
				return fmt.Errorf("retained metadata-action inode replaced")
			}
			if op.State == Accepted && op.ResultBaseID != "" {
				base, e := store.Base(ctx, op.ResultBaseID)
				if e != nil {
					return e
				}
				if base.MetadataAction == nil || *retained != *base.MetadataAction {
					out.State, out.Reason = Conflict, "retained inode metadata changed to "+retained.Revision()+"; bytes remain in the original journal inode"
				}
			}
			return parent.check()
		})
		if e != nil {
			out.State, out.Reason = Held, e.Error()
		}
		if out.State == "clean" {
			return nil
		}
		out.ID = "notes_recovery_" + identity(out)
		return errors.Join(e, store.SaveRecovery(ctx, out))
	})
	return
}
