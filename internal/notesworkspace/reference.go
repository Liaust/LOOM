package notesworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ReferenceMetadata is refusal-only navigation. It neither reads bytes nor
// admits a source; ReadReference and WithExport still own every actual export.
func (s Service) ReferenceMetadata(ctx context.Context, collection, relative string) (File, error) {
	if s.Store == nil || !IsReferencePath(relative) || validateSourcePath(relative, true) != nil {
		return File{}, ErrReferenceOnly
	}
	file, err := s.Store.FileAt(ctx, collection, pathKey(relative))
	if err == nil && (file.Deleted || file.CollectionID != collection || file.RelativePath != relative || file.EntryKind != "") {
		return File{}, ErrStalePath
	}
	return file, err
}

// ReadReference keeps large immutable reference bytes on disk, not in the
// source journal's JSON. The caller owns removal of the returned snapshot.
func (s Service) ReadReference(ctx context.Context, collection, relative, spool string) (out ReadResult, snapshot string, err error) {
	if s.Store == nil || s.Sources == nil || !IsReferencePath(relative) || validateSourcePath(relative, true) != nil {
		return out, "", ErrReferenceOnly
	}
	if !filepath.IsAbs(spool) || filepath.Clean(spool) != spool || spool == "/" {
		return out, "", ErrUnsafePath
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
			absent := func() error {
				id, e := newFileID()
				if e != nil {
					return e
				}
				f, e := store.Bind(ctx, File{ID: id, CollectionID: collection, RelativePath: relative, PathKey: pathKey(relative)})
				if e != nil {
					return e
				}
				if f.RelativePath != relative || f.EntryKind != "" {
					return ErrUnsafePath
				}
				b := makeBase(f, src.Generation, false, nil)
				if e := store.SaveBase(ctx, b); e != nil {
					return e
				}
				out = ReadResult{File: f, Base: b}
				return nil
			}
			parent, name, e := openSourceParent(src.Path, relative)
			if errors.Is(e, errMissingSourceDirectory) {
				return absent()
			}
			if e != nil {
				return e
			}
			defer parent.Close()
			fd, e := unix.Openat(parent.fd(), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if errors.Is(e, unix.ENOENT) {
				if e := parent.check(); e != nil {
					return e
				}
				return absent()
			}
			if e != nil {
				return e
			}
			file := os.NewFile(uintptr(fd), name)
			defer file.Close()
			before, e := file.Stat()
			if e != nil {
				return e
			}
			var stat unix.Stat_t
			if e := unix.Fstat(fd, &stat); e != nil {
				return e
			}
			if !before.Mode().IsRegular() || stat.Nlink != 1 {
				return ErrUnsafePath
			}
			if before.Size() > MaxStreamReferenceBytes {
				return ErrReferenceLimit
			}
			tmp, e := newReferenceSnapshot(spool)
			if e != nil {
				return e
			}
			snapshot = tmp.Name()
			defer tmp.Close()
			h := sha256.New()
			n, e := io.CopyBuffer(io.MultiWriter(tmp, h), contextReader{ctx, io.LimitReader(file, MaxStreamReferenceBytes+1)}, make([]byte, 256<<10))
			out.ReferenceReadBytes = n
			if e != nil {
				return e
			}
			after, e := file.Stat()
			if e != nil || n != before.Size() || n > MaxStreamReferenceBytes || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || parent.check() != nil {
				return ErrUnsafePath
			}
			namedFD, e := unix.Openat(parent.fd(), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if e != nil {
				return ErrUnsafePath
			}
			named := os.NewFile(uintptr(namedFD), name)
			namedInfo, e := named.Stat()
			named.Close()
			if e != nil || !os.SameFile(before, namedInfo) {
				return ErrUnsafePath
			}
			if e := tmp.Close(); e != nil {
				return e
			}
			id, e := newFileID()
			if e != nil {
				return e
			}
			f, e := store.Bind(ctx, File{ID: id, CollectionID: collection, RelativePath: relative, PathKey: pathKey(relative)})
			if e != nil {
				return e
			}
			if f.RelativePath != relative {
				return ErrUnsafePath
			}
			b := Base{FileID: f.ID, Generation: src.Generation, PathVersion: f.PathVersion, Exists: true, Hash: "sha256:" + hex.EncodeToString(h.Sum(nil)), ReferenceSize: n}
			b.ID = "notes_base_" + identity(b)
			if e := store.SaveBase(ctx, b); e != nil {
				return e
			}
			out.File, out.Base = f, b
			return nil
		})
	})
	if err != nil && snapshot != "" {
		os.Remove(snapshot)
		snapshot = ""
	}
	return
}

func newReferenceSnapshot(spool string) (*os.File, error) {
	if !filepath.IsAbs(spool) || filepath.Clean(spool) != spool || spool == "/" {
		return nil, ErrUnsafePath
	}
	if err := os.MkdirAll(spool, 0700); err != nil {
		return nil, err
	}
	fd, err := unix.Open(spool, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), spool)
	defer directory.Close()
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil || int(info.Uid) != os.Geteuid() {
		return nil, ErrUnsafePath
	}
	// Host ACL reconciliation can widen this service-owned temporary directory.
	// Restore the native publisher's private-spool contract without following links.
	if err := directory.Chmod(0700); err != nil {
		return nil, err
	}
	return os.CreateTemp(spool, "reference-*")
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
