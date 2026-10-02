package notesworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Reconcile observes a retained inode under the existing source/collection fences.
// Invoke in repeated bounded retained-operation sweeps, including after receipt.
// This is observation timing, not atomic protection against arbitrary writers.
func (s Service) Reconcile(ctx context.Context, id string) (out Recovery, err error) {
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
		out = Recovery{OperationID: id, State: "clean"}
		if op.Journal == "" {
			return nil
		}
		guard := func(fn func(Source) error) error {
			if retained, ok := s.Sources.(RetainedResolver); ok && (op.State == Accepted || op.State == Conflict) {
				return retained.WithRetainedSource(ctx, op.File.CollectionID, op.File.RelativePath, op.Request.Generation, fn)
			}
			return s.Sources.WithSource(ctx, op.File.CollectionID, op.File.RelativePath, fn)
		}
		if requestKind(op.Request) == KindRename && op.State != Accepted && op.State != Conflict {
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
			parent, name, err := openSourceParent(src.Path, op.File.RelativePath)
			if err != nil {
				return err
			}
			defer parent.Close()
			if err := parent.check(); err != nil {
				return err
			}
			// Reconciliation must not recreate a lost/replaced journal or its intent.
			if op.Journal != ".loom-notes-"+identity(op.Request.OperationID) {
				return ErrIntentMismatch
			}
			fd, err := unix.Openat(parent.fd(), op.Journal, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return fmt.Errorf("retained journal unavailable: %w", err)
			}
			journal := os.NewFile(uintptr(fd), op.Journal)
			defer journal.Close()
			info, err := journal.Stat()
			if err != nil {
				return err
			}
			if info.Mode().Perm() != 0700 {
				return ErrUnsafePath
			}
			intent, exists, err := readAt(journal, "intent")
			if err != nil {
				return err
			}
			if !exists || !bytes.Equal(intent, []byte(identity(op.Request))) {
				return ErrIntentMismatch
			}
			out.Retained, out.RetainedExists, err = readAt(journal, "displaced")
			if err != nil {
				return fmt.Errorf("retained inode unreadable; journal retained: %w", err)
			}
			if !out.RetainedExists {
				if requestKind(op.Request) == KindRename && op.MoveIdentity != nil && op.State != Accepted && op.State != Conflict {
					destination, destinationName, err := openSourceParent(src.Path, op.Request.DestinationPath)
					if err != nil {
						return err
					}
					defer destination.Close()
					actual, present, err := inodeAt(destination.current(), destinationName)
					if err != nil {
						return err
					}
					if present && actual == *op.MoveIdentity {
						out.Current, out.CurrentExists, err = readSource(destination, destinationName)
						if err != nil {
							return err
						}
						if !bytes.Equal(out.Current, op.Base.Content) {
							out.Retained, out.RetainedExists, err = readAt(journal, "original")
							if err != nil {
								return err
							}
							if !out.RetainedExists || !bytes.Equal(out.Retained, op.Base.Content) {
								return fmt.Errorf("rename base snapshot unavailable")
							}
							out.State, out.Reason = Conflict, "renamed inode changed before acknowledgement; original base retained in journal"
						}
						return nil
					}
					original, atSource, err := inodeAt(parent.current(), name)
					if err != nil {
						return err
					}
					if !atSource || original != *op.MoveIdentity {
						return fmt.Errorf("pending rename original inode missing; base snapshot retained")
					}
				}
				// Creates and failed/conflicting edits can legitimately have no displacement.
				if op.State == Accepted && op.Base.Exists && requestKind(op.Request) != KindRename && op.Reason != "already equals proposal" {
					return fmt.Errorf("acknowledged edit's retained inode missing")
				}
				return nil
			}
			baseline := op.RetainedHash
			if baseline == "" && op.State == Accepted {
				baseline = op.Base.Hash
			} // W3a receipt
			if baseline == "" {
				return fmt.Errorf("retained inode has no recorded comparison baseline")
			}
			if digest(out.Retained) == baseline {
				return parent.check()
			}
			out.Current, out.CurrentExists, err = readSource(parent, name)
			if err != nil {
				return fmt.Errorf("live source unreadable; retained variant preserved: %w", err)
			}
			out.State, out.Reason = Conflict, "retained inode changed after displacement"
			return nil
		})
		if guardErr != nil {
			out.State, out.Reason = Held, guardErr.Error()
		}
		if out.State == "clean" {
			return nil
		}
		out.ID = "notes_recovery_" + identity(out)
		if err := store.SaveRecovery(ctx, out); err != nil {
			return errors.Join(guardErr, err)
		}
		return nil
	})
	return
}
