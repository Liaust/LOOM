package notesworkspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"golang.org/x/sys/unix"
)

// MetadataActionIdentity is a path-action observation, never a content snapshot
// or a base for editing. Revision is metadata-derived, not a content checksum.
type MetadataActionIdentity struct {
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified_ns"`
	Changed  int64  `json:"changed_ns"`
	Mode     uint32 `json:"mode"`
	Links    uint64 `json:"links"`
}

func (m MetadataActionIdentity) Revision() string     { return "stat:" + identity(m) }
func (m MetadataActionIdentity) inode() inodeIdentity { return inodeIdentity{m.Device, m.Inode} }
func (m MetadataActionIdentity) matchesMoved(other *MetadataActionIdentity) bool {
	// A rename changes ctime; it must not change identity, bytes' observable
	// revision, permissions or link count. This is not proof of byte equality.
	return other != nil && m.inode() == other.inode() && m.Size == other.Size && m.Modified == other.Modified && m.Mode == other.Mode && m.Links == other.Links
}

func metadataActionAt(fd int, name string) (*MetadataActionIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, ErrUnsafePath
	}
	return &MetadataActionIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size,
		Modified: stat.Mtim.Nano(), Changed: stat.Ctim.Nano(), Mode: uint32(stat.Mode), Links: uint64(stat.Nlink)}, nil
}

func makeMetadataActionBase(file File, generation string, exists bool, observed MetadataActionIdentity) Base {
	base := Base{FileID: file.ID, Generation: generation, PathVersion: file.PathVersion, Exists: exists, MetadataAction: &observed}
	base.ID = "notes_metadata_base_" + identity(base)
	return base
}

// ReadMetadataAction binds any admitted visible regular file without opening its
// payload, hashing its bytes, creating a spool or requesting enrichment.
func (s Service) ReadMetadataAction(ctx context.Context, collection, relative string) (out ReadResult, err error) {
	if s.Store == nil || s.Sources == nil {
		return out, ErrMembership
	}
	if err = validateDirectoryPath(relative); err != nil {
		return
	}
	err = s.Store.WithLock(ctx, "collection:"+collection, func(store Store) error {
		if busy, e := store.PathBusy(ctx, collection, pathKey(relative), ""); e != nil {
			return e
		} else if busy {
			return ErrPathBusy
		}
		return s.Sources.WithSource(ctx, collection, relative, func(src Source) error {
			if src.CollectionID != collection || src.Generation == "" {
				return ErrMembership
			}
			parent, name, e := openSourceParent(src.Path, relative)
			if e != nil {
				return e
			}
			defer parent.Close()
			observed, e := metadataActionAt(parent.fd(), name)
			if e != nil {
				return e
			}
			if observed == nil {
				return fs.ErrNotExist
			}
			if e = parent.check(); e != nil {
				return e
			}
			id, e := newFileID()
			if e != nil {
				return e
			}
			file, e := store.Bind(ctx, File{ID: id, CollectionID: collection, RelativePath: relative, PathKey: pathKey(relative)})
			if e != nil {
				return e
			}
			if file.RelativePath != relative || file.EntryKind != "" || file.Deleted {
				return ErrStalePath
			}
			base := makeMetadataActionBase(file, src.Generation, true, *observed)
			if e = store.SaveBase(ctx, base); e != nil {
				return e
			}
			out = ReadResult{File: file, Base: base}
			return nil
		})
	})
	return
}

func validateMetadataActionRequest(request Request) error {
	if request.OperationID == "" || len(request.OperationID) > 160 || len(request.Content) != 0 || len(request.DirectoryTargets) != 0 {
		return ErrIntentMismatch
	}
	switch request.Kind {
	case KindRename:
		return validateDirectoryPath(request.DestinationPath)
	case KindDelete:
		if request.DestinationPath == "" {
			return nil
		}
	}
	return fmt.Errorf("metadata actions support only rename and recovery delete without content")
}

func (s Service) ReceiveMetadataAction(ctx context.Context, request Request) (Operation, error) {
	if s.Store == nil {
		return Operation{}, ErrMembership
	}
	if err := validateMetadataActionRequest(request); err != nil {
		return Operation{}, err
	}
	if old, err := s.Store.Operation(ctx, request.OperationID); err == nil {
		if old.Base.MetadataAction == nil || !sameIntent(old, Operation{Request: request}) {
			return Operation{}, ErrIntentMismatch
		}
		return old, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	file, err := s.Store.File(ctx, request.FileID)
	if err != nil {
		return Operation{}, err
	}
	if file.EntryKind != "" {
		return Operation{}, ErrUnsafePath
	}
	if err = validateDirectoryPath(file.RelativePath); err != nil {
		return Operation{}, err
	}
	if request.Kind == KindRename && pathKey(request.DestinationPath) == file.PathKey {
		return Operation{}, ErrUnsafePath
	}
	base, err := s.Store.Base(ctx, request.BaseID)
	if err != nil {
		return Operation{}, err
	}
	if base.MetadataAction == nil || !base.Exists || len(base.Content) != 0 || base.DirectoryIdentity != nil || base.FileID != file.ID || base.Generation != request.Generation {
		return Operation{}, ErrMembership
	}
	return s.Store.Receive(ctx, Operation{Request: request, File: file, Base: base, State: Pending})
}
