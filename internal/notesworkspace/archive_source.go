package notesworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"loom.local/loom/internal/knowledge"
)

// ArchiveReader persists original-byte bases but is deliberately not a Resolver:
// receiving a reference never grants Service.Apply a path to write an archive.
type ArchiveReader struct {
	Reader interface {
		WithFile(context.Context, string, string, string, int64, func(knowledge.WorkspaceArchiveSource, knowledge.WorkspaceArchiveFile, io.Reader) error) error
	}
	Store Store
}

func ArchiveGeneration(raw string) string { return identity([]string{"archive-reference-v1", raw}) }

func (a ArchiveReader) Read(ctx context.Context, collection, relative, generation string) (out ReadResult, err error) {
	if a.Store == nil {
		return out, ErrMembership
	}
	limit := int64(MaxContentBytes)
	if IsReferencePath(relative) {
		limit = MaxReferenceBytes
	}
	err = a.Store.WithLock(ctx, "collection:"+collection, func(store Store) error {
		return a.Reader.WithFile(ctx, collection, relative, generation, limit, func(source knowledge.WorkspaceArchiveSource, file knowledge.WorkspaceArchiveFile, reader io.Reader) error {
			content, err := io.ReadAll(io.LimitReader(reader, limit+1))
			if err != nil {
				return err
			}
			if int64(len(content)) > limit || digest(content) != file.SourceHash {
				return ErrMembership
			}
			if !IsReferencePath(relative) {
				if err := validateContent(content); err != nil {
					return err
				}
			}
			id, err := newFileID()
			if err != nil {
				return err
			}
			f, err := store.Bind(ctx, File{ID: id, CollectionID: collection, RelativePath: relative, PathKey: pathKey(relative)})
			if err != nil {
				return err
			}
			if f.RelativePath != relative || f.EntryKind != "" {
				return ErrMembership
			}
			base := makeBase(f, ArchiveGeneration(source.Generation), true, content)
			if err := store.SaveBase(ctx, base); err != nil {
				return err
			}
			out = ReadResult{File: f, Base: base}
			return nil
		})
	})
	return
}

func (a ArchiveReader) WithExport(ctx context.Context, fileID, baseID, generation string, fn func(File, Base) error) error {
	file, err := a.Store.File(ctx, fileID)
	if err != nil {
		return err
	}
	base, err := a.Store.Base(ctx, baseID)
	if err != nil {
		return err
	}
	size := int64(len(base.Content))
	streamed := IsReferencePath(file.RelativePath) && len(base.Content) == 0 && base.ReferenceSize > 0
	if streamed {
		size = base.ReferenceSize
	}
	if base.FileID != file.ID || !base.Exists || base.Generation != ArchiveGeneration(generation) ||
		(!streamed && base.Hash != digest(base.Content)) || size < 0 || size > MaxReferenceBytes {
		return ErrMembership
	}
	return a.Reader.WithFile(ctx, file.CollectionID, file.RelativePath, generation, size, func(_ knowledge.WorkspaceArchiveSource, expected knowledge.WorkspaceArchiveFile, _ io.Reader) error {
		if expected.SourceHash != base.Hash || expected.SizeBytes != size {
			return fmt.Errorf("%w: archive reference changed", ErrMembership)
		}
		return fn(file, base)
	})
}

// ReadReference retains authenticated archive bytes in the export spool, not in
// a new JSON base for every enrollment. Archive custody remains read-only.
func (a ArchiveReader) ReadReference(ctx context.Context, collection, relative, generation, spool string) (out ReadResult, snapshot string, err error) {
	if a.Store == nil || a.Reader == nil || !IsReferencePath(relative) || validateSourcePath(relative, true) != nil {
		return out, "", ErrMembership
	}
	err = a.Store.WithLock(ctx, "collection:"+collection, func(store Store) error {
		return a.Reader.WithFile(ctx, collection, relative, generation, MaxReferenceBytes, func(source knowledge.WorkspaceArchiveSource, file knowledge.WorkspaceArchiveFile, reader io.Reader) error {
			tmp, err := newReferenceSnapshot(spool)
			if err != nil {
				return err
			}
			snapshot = tmp.Name()
			defer tmp.Close()
			h := sha256.New()
			n, err := io.CopyBuffer(io.MultiWriter(tmp, h), contextReader{ctx, io.LimitReader(reader, MaxReferenceBytes+1)}, make([]byte, 256<<10))
			out.ReferenceReadBytes = n
			hash := "sha256:" + hex.EncodeToString(h.Sum(nil))
			if err != nil {
				return err
			}
			if n != file.SizeBytes || n > MaxReferenceBytes || hash != file.SourceHash {
				return ErrMembership
			}
			if err := tmp.Close(); err != nil {
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
			if f.RelativePath != relative || f.EntryKind != "" {
				return ErrMembership
			}
			base := Base{FileID: f.ID, Generation: ArchiveGeneration(source.Generation), PathVersion: f.PathVersion, Exists: true, Hash: hash, ReferenceSize: n}
			base.ID = "notes_base_" + identity(base)
			if err := store.SaveBase(ctx, base); err != nil {
				return err
			}
			out.File, out.Base = f, base
			return nil
		})
	})
	if err != nil && snapshot != "" {
		os.Remove(snapshot)
		snapshot = ""
	}
	return
}
