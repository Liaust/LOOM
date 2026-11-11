package notesworkspacesync

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspace"
)

type archiveSelectionCache struct {
	Signature string
	Until     time.Time
	Source    knowledge.WorkspaceArchiveSource
}

// This cache is navigation only. Every original-byte read and export still
// reauthenticates current custody/privacy through ArchiveReader.WithFile.
// Avoid reparsing retained repository inventories on each live keystroke.
func (r *Runtime) withArchiveSelection(ctx context.Context, id string, fn func(knowledge.WorkspaceArchiveSource) error) error {
	var metadata, custody json.RawMessage
	if err := r.DB.QueryRowContext(ctx, `SELECT root.metadata,project.archive_state
 FROM knowledge.notes_source_roots root JOIN projects.projects project USING(project_id)
 WHERE root.notes_source_root_id=$1 AND project.status='archived'`, id).Scan(&metadata, &custody); err != nil {
		return err
	}
	signature := key(string(metadata), string(custody))
	if cached, ok := r.archiveSelections[id]; ok && cached.Signature == signature && time.Now().Before(cached.Until) {
		return fn(cached.Source)
	}
	return r.Archives.WithRoot(ctx, id, func(source knowledge.WorkspaceArchiveSource) error {
		if r.archiveSelections == nil {
			r.archiveSelections = map[string]archiveSelectionCache{}
		}
		r.archiveSelections[id] = archiveSelectionCache{Signature: signature, Until: time.Now().Add(15 * time.Minute), Source: source}
		return fn(source)
	})
}

type runtimeSources struct {
	NotesSource
	Archive    *notesworkspace.ArchiveReader
	Archived   map[string]knowledge.WorkspaceArchiveSource
	Selections []RuntimeSelection
}

func (s runtimeSources) selected(collection, relative string) bool {
	for _, selection := range s.Selections {
		if selection.SourceCollection == collection {
			return selection.contains(relative)
		}
	}
	return false
}

func (s runtimeSources) Read(ctx context.Context, collection, relative string) (notesworkspace.ReadResult, error) {
	if source, ok := s.Archived[collection]; ok {
		if !s.selected(collection, relative) {
			return notesworkspace.ReadResult{}, notesworkspace.ErrMembership
		}
		return s.Archive.Read(ctx, collection, relative, source.Generation)
	}
	return s.NotesSource.Read(ctx, collection, relative)
}

func (s runtimeSources) ReadReference(ctx context.Context, collection, relative, spool string) (notesworkspace.ReadResult, string, error) {
	if source, ok := s.Archived[collection]; ok {
		if !s.selected(collection, relative) {
			return notesworkspace.ReadResult{}, "", notesworkspace.ErrMembership
		}
		return s.Archive.ReadReference(ctx, collection, relative, source.Generation, spool)
	}
	return s.NotesSource.ReadReference(ctx, collection, relative, spool)
}

func (s runtimeSources) FileObservation(ctx context.Context, file notesworkspace.File) (InventoryEntry, error) {
	if archive, ok := s.Archived[file.CollectionID]; ok {
		if !s.selected(file.CollectionID, file.RelativePath) {
			return InventoryEntry{}, notesworkspace.ErrMembership
		}
		for _, retained := range archive.Files {
			if retained.RelativePath == file.RelativePath {
				return InventoryEntry{Path: file.RelativePath, Kind: "asset", Present: true, Size: retained.SizeBytes,
					Fingerprint: key(archive.Generation, archive.ManifestDigest, retained.RelativePath, retained.SourceHash)}, nil
			}
		}
		return InventoryEntry{}, notesworkspace.ErrMembership
	}
	return s.NotesSource.FileObservation(ctx, file)
}

func (s runtimeSources) WithExport(ctx context.Context, fileID, baseID string, fn func(notesworkspace.File, notesworkspace.Base) error) error {
	file, err := s.File(ctx, fileID)
	if err != nil {
		return err
	}
	if source, ok := s.Archived[file.CollectionID]; ok {
		if !s.selected(file.CollectionID, file.RelativePath) {
			return notesworkspace.ErrMembership
		}
		return s.Archive.WithExport(ctx, fileID, baseID, source.Generation, fn)
	}
	return s.NotesSource.WithExport(ctx, fileID, baseID, fn)
}

func (s runtimeSources) DirectoryWritable(ctx context.Context, id string) bool {
	if _, ok := s.Archived[id]; ok {
		return false
	}
	return s.NotesSource.DirectoryWritable(ctx, id)
}

type archiveCursor struct {
	After       string
	Complete    bool
	Files, Held int
	Bytes       int64
	Excluded    int
	RetryAt     time.Time
}

func (s *Service) advanceArchives(ctx context.Context, sources map[string]knowledge.WorkspaceArchiveSource, limit int) (more bool, moved, held int, err error) {
	s.ArchiveRetryAt = time.Time{}
	until, bytes, attemptedTotal := time.Now().Add(100*time.Millisecond), int64(0), 0
	for _, scope := range s.Scopes {
		if !scope.ReadOnly {
			continue
		}
		archive := sources[scope.SourceCollection]
		id := "archive_delivery_" + key(scope.Workspace, scope.Collection, scope.Generation, scope.Root)
		var cursor archiveCursor
		err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
			var e error
			cursor, e = load[archiveCursor](ctx, r, "cursor", id)
			if missing(e) {
				return nil
			}
			return e
		})
		if err != nil {
			return
		}
		if cursor.Complete {
			continue
		}
		if time.Now().Before(cursor.RetryAt) {
			if s.ArchiveRetryAt.IsZero() || cursor.RetryAt.Before(s.ArchiveRetryAt) {
				s.ArchiveRetryAt = cursor.RetryAt
			}
			continue
		}
		cursor.Held = 0
		attempted := 0
		budgetStopped := false
		for _, file := range archive.Files {
			if file.RelativePath <= cursor.After {
				continue
			}
			if attempted == limit {
				more = true
				break
			}
			if attemptedTotal >= limit || attemptedTotal > 0 && (time.Now().After(until) || bytes+file.SizeBytes > 8<<20) {
				more = true
				budgetStopped = true
				break
			}
			attempted++
			attemptedTotal++
			limit := int64(MaxBytes)
			if referencePath(file.RelativePath) {
				limit = MaxReferenceBytes
			}
			if !workspacePath(file.RelativePath, true) || file.SizeBytes > limit {
				cursor.After = file.RelativePath
				cursor.Excluded++
				continue
			}
			read, snapshot, e := readExportSource(ctx, s.Source, scope.SourceCollection, file.RelativePath, s.ReferenceSpool)
			if e == nil {
				_, e = s.export(ctx, scope.Collection, read.File.ID, read.Base.ID, snapshot)
			}
			if snapshot != "" {
				os.Remove(snapshot)
			}
			if e != nil {
				// Preserve exact failed entry for a later bounded retry rather than
				// falsely advancing archive completion or spinning during idle.
				held++
				cursor.Held++
				cursor.RetryAt = time.Now().Add(5 * time.Minute)
				if s.ArchiveRetryAt.IsZero() || cursor.RetryAt.Before(s.ArchiveRetryAt) {
					s.ArchiveRetryAt = cursor.RetryAt
				}
				break
			}
			cursor.After = file.RelativePath
			cursor.Files++
			cursor.Bytes += file.SizeBytes
			bytes += file.SizeBytes
			moved++
		}
		if attempted < limit && cursor.Held == 0 && !budgetStopped {
			cursor.Complete = true
		}
		err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error { return r.Put(ctx, "cursor", id, cursor) })
		if err != nil {
			return
		}
		if budgetStopped {
			return
		}
	}
	return
}
