package notesworkspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/objectstore"
)

// ImportParent pins the destination directory during a multi-request upload.
type ImportParent struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

var ErrImportContentChanged = errors.New("import source bytes changed; uploaded original retained")

func (s Service) ImportDestination(ctx context.Context, collection, relative string) (generation string, pin ImportParent, err error) {
	if err = validateDirectoryPath(relative); err != nil {
		return
	}
	err = s.Sources.WithSource(ctx, collection, relative, func(src Source) error {
		parent, name, e := openSourceParent(src.Path, relative)
		if e != nil {
			return e
		}
		defer parent.Close()
		if e = checkNameAlias(parent.current(), name); e != nil {
			return e
		}
		entry, e := metadataActionAt(parent.fd(), name)
		if e != nil {
			return e
		}
		if entry != nil {
			return os.ErrExist
		}
		var st unix.Stat_t
		if e = unix.Fstat(parent.fd(), &st); e != nil {
			return e
		}
		generation, pin = src.Generation, ImportParent{uint64(st.Dev), st.Ino}
		return parent.check()
	})
	return
}

// CreateImport accepts only a server-verified retained blob. No binary bytes
// enter a Notes JSON snapshot, and final acknowledgement survives later moves.
func (s Service) CreateImport(ctx context.Context, collection, relative, id, generation string, pin ImportParent, blob objectstore.StoredBlob) (out Operation, err error) {
	if validateDirectoryPath(relative) != nil || id == "" || len(id) > 160 || blob.HashURI != "sha256:"+blob.HashHex || len(blob.HashHex) != 64 || blob.SizeBytes < 0 {
		return out, ErrUnsafePath
	}
	err = s.Store.WithLock(ctx, "collection:"+collection, func(store Store) error {
		if old, e := store.Operation(ctx, id); e == nil {
			if old.Request.Kind != KindImport || old.File.CollectionID != collection || old.File.RelativePath != relative || old.Request.Generation != generation || old.Request.ImportHash != blob.HashURI || old.Request.ImportSize != blob.SizeBytes || old.ImportParent == nil || *old.ImportParent != (inodeIdentity{pin.Device, pin.Inode}) {
				return ErrIntentMismatch
			}
			out = old
			return nil
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if busy, e := store.PathBusy(ctx, collection, pathKey(relative), id); e != nil {
			return e
		} else if busy {
			return ErrPathBusy
		}
		return s.Sources.WithSource(ctx, collection, relative, func(src Source) error {
			if src.Generation != generation {
				return ErrMembership
			}
			parent, name, e := openSourceParent(src.Path, relative)
			if e != nil {
				return e
			}
			defer parent.Close()
			if e = checkImportParent(parent, pin); e != nil {
				return e
			}
			entry, e := metadataActionAt(parent.fd(), name)
			if e != nil {
				return e
			}
			if entry != nil {
				return os.ErrExist
			}
			fileID, e := newFileID()
			if e != nil {
				return e
			}
			file, e := store.Bind(ctx, File{ID: fileID, CollectionID: collection, RelativePath: relative, PathKey: pathKey(relative)})
			if e != nil {
				return e
			}
			if file.RelativePath != relative || file.EntryKind != "" || file.Deleted {
				return ErrStalePath
			}
			base := makeBase(file, generation, false, nil)
			if e = store.SaveBase(ctx, base); e != nil {
				return e
			}
			boxID := ""
			if selection, scoped := boxregistry.FromContext(ctx); scoped && len(selection.BoxIDs) == 1 {
				boxID = selection.BoxIDs[0]
			}
			out, e = store.Receive(ctx, Operation{Request: Request{BoxID: boxID, OperationID: id, FileID: file.ID, Generation: generation, BaseID: base.ID, Kind: KindImport, ImportHash: blob.HashURI, ImportSize: blob.SizeBytes}, File: file, Base: base, State: Pending, PathStage: "import_ready", Journal: ".loom-notes-" + identity(id), ImportBlob: &blob, ImportParent: &inodeIdentity{pin.Device, pin.Inode}})
			return e
		})
	})
	if err != nil {
		return out, err
	}
	return s.applyImport(ctx, out)
}

func checkImportParent(parent *sourceDirectory, pin ImportParent) error {
	var st unix.Stat_t
	if err := unix.Fstat(parent.fd(), &st); err != nil {
		return err
	}
	if uint64(st.Dev) != pin.Device || st.Ino != pin.Inode {
		return ErrStalePath
	}
	return parent.check()
}

func (s Service) applyImport(ctx context.Context, initial Operation) (out Operation, err error) {
	err = s.Store.WithLock(ctx, "collection:"+initial.File.CollectionID, func(store Store) error {
		op, e := store.Operation(ctx, initial.Request.OperationID)
		if e != nil {
			return e
		}
		out = op
		conflict := func(cause error) error {
			op.State, op.Reason = Conflict, cause.Error()
			if saveErr := store.SaveOperation(ctx, op); saveErr != nil {
				return saveErr
			}
			out = op
			return cause
		}
		if op.State == Accepted || op.State == Conflict {
			return nil
		}
		if op.ImportBlob == nil || op.ImportParent == nil {
			return ErrIntentMismatch
		}
		current, e := store.File(ctx, op.File.ID)
		if e != nil {
			return e
		}
		if current.Deleted || current.PathVersion != op.File.PathVersion || current.RelativePath != op.File.RelativePath {
			return ErrStalePath
		}
		if busy, e := store.PathBusy(ctx, op.File.CollectionID, op.File.PathKey, op.Request.OperationID); e != nil {
			return e
		} else if busy {
			return ErrPathBusy
		}
		return s.Sources.WithSource(ctx, op.File.CollectionID, op.File.RelativePath, func(src Source) error {
			if src.Generation != op.Request.Generation {
				return ErrMembership
			}
			parent, name, e := openSourceParent(src.Path, op.File.RelativePath)
			if e != nil {
				return e
			}
			defer parent.Close()
			if e = checkImportParent(parent, ImportParent{op.ImportParent.Device, op.ImportParent.Inode}); e != nil {
				return e
			}
			journal, e := openJournal(parent, op)
			if e != nil {
				return e
			}
			defer journal.Close()
			entry, e := metadataActionAt(parent.fd(), name)
			if e != nil {
				return e
			}
			if entry != nil && (op.MoveIdentity == nil || entry.inode() != *op.MoveIdentity) {
				return conflict(os.ErrExist)
			}
			if entry == nil {
				staged, e := metadataActionAt(int(journal.Fd()), "imported")
				if e != nil {
					return e
				}
				if staged == nil {
					if e = s.Blobs.MaterializeVerifiedAt(ctx, *op.ImportBlob, journal, "imported"); e != nil {
						return e
					}
					staged, e = metadataActionAt(int(journal.Fd()), "imported")
					if e != nil {
						return e
					}
				}
				if staged == nil || staged.Size != op.Request.ImportSize {
					return ErrIntentMismatch
				}
				inode := staged.inode()
				if op.MoveIdentity != nil && *op.MoveIdentity != inode {
					return ErrIntentMismatch
				}
				// Verify orphaned staging too: publication may have crashed before its
				// prepared-inode receipt was durably stored.
				if e = verifyImportFile(ctx, journal, "imported", op.Request.ImportHash, op.Request.ImportSize); e != nil {
					if errors.Is(e, ErrImportContentChanged) {
						return conflict(e)
					}
					return e
				}
				if staged.Links != 1 {
					if e = recoverImportLinks(journal, *staged); e != nil {
						return e
					}
				}
				op.MoveIdentity, op.PathStage = &inode, "import_prepared"
				if e = store.SaveOperation(ctx, op); e != nil {
					return e
				}
				if e = parent.check(); e != nil {
					return e
				}
				if e = checkNameAlias(parent.current(), name); e != nil {
					return e
				}
				if e = renameNoReplace(int(journal.Fd()), "imported", parent.fd(), name); e != nil {
					if errors.Is(e, os.ErrExist) {
						return conflict(e)
					}
					return e
				}
			} else if e = verifyImportFile(ctx, parent.current(), name, op.Request.ImportHash, op.Request.ImportSize); e != nil {
				if errors.Is(e, ErrImportContentChanged) {
					return conflict(e)
				}
				return e
			}
			if e = errors.Join(parent.current().Sync(), journal.Sync(), parent.check()); e != nil {
				return e
			}
			entry, e = metadataActionAt(parent.fd(), name)
			if e != nil {
				return e
			}
			if entry == nil || entry.inode() != *op.MoveIdentity {
				return ErrStalePath
			}
			if e = verifyImportFile(ctx, parent.current(), name, op.Request.ImportHash, op.Request.ImportSize); e != nil {
				if errors.Is(e, ErrImportContentChanged) {
					return conflict(e)
				}
				return e
			}
			after, e := metadataActionAt(parent.fd(), name)
			if e != nil {
				return e
			}
			if after == nil || *after != *entry {
				return ErrStalePath
			}
			if e = parent.check(); e != nil {
				return e
			}
			file := op.File
			file.PathVersion++
			base := makeMetadataActionBase(file, src.Generation, true, *entry)
			op.State, op.Reason, op.ResultBaseID, op.ResultFile = Accepted, "import verified and source created", base.ID, &file
			if e = store.CommitPath(ctx, op, file, base); e != nil {
				return e
			}
			out = op
			return nil
		})
	})
	return
}

func recoverImportLinks(journal *os.File, staged MetadataActionIdentity) error {
	entries, e := journal.ReadDir(64)
	if e != nil && e != io.EOF {
		return e
	}
	if len(entries) >= 64 {
		return ErrUnsafePath
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".object-") {
			continue
		}
		extra, e := metadataActionAt(int(journal.Fd()), entry.Name())
		if e != nil {
			return e
		}
		if extra != nil && extra.inode() == staged.inode() {
			if e = unix.Unlinkat(int(journal.Fd()), entry.Name(), 0); e != nil {
				return e
			}
		}
	}
	after, e := metadataActionAt(int(journal.Fd()), "imported")
	if e != nil {
		return e
	}
	if after == nil || after.Links != 1 {
		return ErrUnsafePath
	}
	return journal.Sync()
}

func verifyImportFile(ctx context.Context, directory *os.File, name, hash string, size int64) error {
	fd, e := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return e
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, e := file.Stat()
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return ErrIntentMismatch
	}
	before, e := metadataActionAt(int(directory.Fd()), name)
	if e != nil {
		return e
	}
	if before == nil {
		return ErrStalePath
	}
	h := sha256.New()
	buf := make([]byte, 128<<10)
	var count int64
	for {
		if e = ctx.Err(); e != nil {
			return e
		}
		n, readErr := file.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			count += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if count != size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != hash {
		return ErrImportContentChanged
	}
	after, e := metadataActionAt(int(directory.Fd()), name)
	if e != nil {
		return e
	}
	if after == nil || *after != *before {
		return ErrStalePath
	}
	return nil
}
