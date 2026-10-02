package notesworkspace

import (
	"context"
	"database/sql"
)

// WithNavigation observes an existing identity without binding a path or saving
// a base. The source owner holds the same enrollment/privacy/lifecycle fences as
// export. A citation must still describe the current original bytes.
func (s Service) WithNavigation(ctx context.Context, collection, relative, hash string, fn func(File, Base) error) error {
	if err := validateSourcePath(relative, true); err != nil {
		return err
	}
	return s.Store.WithLock(ctx, "collection:"+collection, func(store Store) error {
		file, err := store.FileAt(ctx, collection, pathKey(relative))
		if err != nil {
			return err
		}
		if file.Deleted || file.CollectionID != collection || file.RelativePath != relative {
			return ErrStalePath
		}
		busy, err := store.PathBusy(ctx, collection, file.PathKey, "")
		if err != nil {
			return err
		}
		if busy {
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
			if err != nil {
				return err
			}
			if !exists {
				return sql.ErrNoRows
			}
			if digest(content) != hash {
				return ErrStalePath
			}
			return fn(file, makeBase(file, src.Generation, true, content))
		})
	})
}
