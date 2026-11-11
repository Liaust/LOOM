package notesworkspacesync

import (
	"context"
	"fmt"
)

var errMappingIndexPending = fmt.Errorf("%w: retained mapping index pending", ErrHeld)

type mappingIndexCursor struct {
	After    string
	Complete bool
}

func mappingLocator(scope Scope, fileID string) string {
	return "retained_" + key(scope.Workspace, scope.Collection, scope.SourceCollection, scope.Root, fileID)
}

// The locator is navigation only: canonical WithExport and the exact native leaf
// still authorize publication. Historical generation mappings stay untouched.
func indexMapping(ctx context.Context, r Records, mapping FileMapping) error {
	if mapping.Latest == nil {
		return nil
	}
	if !validExportMapping(mapping, mapping.Scope, mapping.SourceFileID, mapping.ClientFileID) {
		return ErrHeld
	}
	current, err := load[string](ctx, r, "source_file", sourceKey(mapping.Scope, mapping.SourceFileID))
	if err != nil && !missing(err) {
		return err
	}
	if err == nil && current != mapping.ClientFileID {
		return nil
	}
	id := mappingLocator(mapping.Scope, mapping.SourceFileID)
	pointer := mapKey(Intent{Workspace: mapping.Scope.Workspace, Collection: mapping.Scope.Collection, Generation: mapping.Scope.Generation, FileID: mapping.ClientFileID})
	previous, err := load[string](ctx, r, "source_file", id)
	if err != nil && !missing(err) {
		return err
	}
	if err == nil {
		old, err := load[FileMapping](ctx, r, "file", previous)
		if err != nil {
			return err
		}
		if mappingLocator(old.Scope, old.SourceFileID) != id || old.Latest == nil {
			return ErrHeld
		}
		if old.ClientFileID != mapping.ClientFileID {
			if current != mapping.ClientFileID || mapping.SuccessorOf != old.ClientFileID || mapping.CreateBaseID == "" || !retiredSourceMapping(ctx, r, old) {
				return ErrHeld
			}
			return r.Put(ctx, "source_file", id, pointer)
		}
		if old.Latest.SourceSequence > mapping.Latest.SourceSequence {
			return nil
		}
		if old.Latest.SourceSequence == mapping.Latest.SourceSequence && *old.Latest != *mapping.Latest {
			return ErrHeld
		}
	}
	return r.Put(ctx, "source_file", id, pointer)
}

// Legacy mappings are indexed once in bounded pages, not scanned per source
// read. Future saveMapping calls keep the locator current under the replica lock.
func (s *Service) indexRetainedMappings(ctx context.Context, limit int) error {
	s.MappingIndexPending = false
	if limit < 1 || limit > 128 {
		return ErrHeld
	}
	return s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		cursor, err := load[mappingIndexCursor](ctx, r, "cursor", "mapping-index-v1")
		if err != nil && !missing(err) {
			return err
		}
		if cursor.Complete {
			return nil
		}
		ids, err := r.List(ctx, "file", cursor.After, limit)
		if err != nil {
			return err
		}
		for _, id := range ids {
			mapping, err := load[FileMapping](ctx, r, "file", id)
			if err != nil {
				return err
			}
			if err := indexMapping(ctx, r, mapping); err != nil {
				return err
			}
			cursor.After = id
		}
		cursor.Complete = len(ids) < limit
		if err := r.Put(ctx, "cursor", "mapping-index-v1", cursor); err != nil {
			return err
		}
		s.MappingIndexPending = !cursor.Complete
		return nil
	})
}

func retainedMapping(ctx context.Context, r Records, scope Scope, fileID string) (FileMapping, error) {
	pointer, err := load[string](ctx, r, "source_file", mappingLocator(scope, fileID))
	if err != nil {
		if missing(err) {
			cursor, e := load[mappingIndexCursor](ctx, r, "cursor", "mapping-index-v1")
			if e != nil && !missing(e) {
				return FileMapping{}, e
			}
			if !cursor.Complete {
				return FileMapping{}, errMappingIndexPending
			}
		}
		return FileMapping{}, err
	}
	mapping, err := load[FileMapping](ctx, r, "file", pointer)
	if err != nil {
		return FileMapping{}, err
	}
	if pointer != mapKey(Intent{Workspace: mapping.Scope.Workspace, Collection: mapping.Scope.Collection, Generation: mapping.Scope.Generation, FileID: mapping.ClientFileID}) {
		return FileMapping{}, ErrHeld
	}
	mapping, err = recoverRetainedLayoutMapping(ctx, r, mapping)
	if err != nil {
		return FileMapping{}, err
	}
	lookup := scope
	lookup.Generation = mapping.Scope.Generation
	if mapping.Latest == nil || !validExportMapping(mapping, lookup, fileID, mapping.ClientFileID) {
		return FileMapping{}, ErrHeld
	}
	return mapping, nil
}

func validExportMapping(mapping FileMapping, scope Scope, fileID, clientID string) bool {
	if !sameExportScope(mapping.Scope, scope) || mapping.SourceFileID != fileID || fileID == "" || mapping.ClientFileID != clientID || !token(clientID) {
		return false
	}
	latest := mapping.Latest
	return latest == nil || latest.valid() && latest.Generation == scope.Generation && latest.Workspace == scope.Workspace && latest.Collection == scope.Collection && latest.FileID == clientID && latest.CollectionRoot == scope.Root
}
