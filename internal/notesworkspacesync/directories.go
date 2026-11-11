package notesworkspacesync

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"

	"loom.local/loom/internal/notesworkspace"
)

type directorySource interface {
	ReadDirectory(context.Context, string, string) (notesworkspace.ReadResult, error)
	SaveDirectory(context.Context, SourceRequest) (Outcome, error)
	DirectoryWritable(context.Context, string) bool
}

type collectionControl struct {
	Header
	Workspace      string `json:"workspace"`
	Collection     string `json:"collection"`
	Generation     string `json:"generation"`
	CollectionRoot string `json:"collectionRoot"`
	SourceSequence uint64 `json:"sourceSequence"`
	Writable       bool   `json:"writable"`
}

// Collection admission is independent of having a first materialized file.
func (s *Service) publishScopes(ctx context.Context) error {
	source, ok := s.Source.(directorySource)
	if !ok {
		return nil
	}
	return s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		for _, scope := range s.Scopes {
			id := "scope_" + key(scope.Workspace, scope.Collection)
			old, err := load[collectionControl](ctx, r, "cursor", id)
			if err != nil && !missing(err) {
				return err
			}
			writable := source.DirectoryWritable(ctx, scope.SourceCollection)
			next := old
			if old.Generation != scope.Generation || old.CollectionRoot != scope.Root || old.Writable != writable || old.SourceSequence == 0 {
				next = collectionControl{Header: Header{1, "scope", id + "_" + key(scope.Generation, scope.Root, strconv.FormatUint(old.SourceSequence+1, 10))}, Workspace: scope.Workspace,
					Collection: scope.Collection, Generation: scope.Generation, CollectionRoot: scope.Root, SourceSequence: old.SourceSequence + 1, Writable: writable}
				if err := r.Put(ctx, "cursor", id, next); err != nil {
					return err
				}
			}
			raw, _ := json.Marshal(next)
			published, e := load[string](ctx, r, "cursor", "scope_published_"+id)
			if e != nil && !missing(e) {
				return e
			}
			if published == next.ID {
				continue
			}
			var result Evidence
			if err := s.call(ctx, "control.put", map[string]any{"contentBase64": raw}, &result); err != nil {
				return err
			}
			if result.Status != "published" {
				return ErrHeld
			}
			if err := r.Put(ctx, "cursor", "scope_published_"+id, next.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) directoryReserved(ctx context.Context, scope Scope, relative string) (reserved bool, err error) {
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		id, e := load[string](ctx, r, "cursor", "directory_reservation_"+key(scope.Workspace, scope.Collection, scope.Root+"/"+relative))
		if missing(e) {
			return nil
		}
		if e != nil {
			return e
		}
		j, e := load[Join](ctx, r, "operation", id)
		if e != nil {
			return e
		}
		reserved = j.Status != "applied" && j.Status != "conflict"
		if reserved && j.Status == "held" && j.AckPublished && j.Ack != nil &&
			j.Ack.Status == "held" && j.Ack.Reason == "directory_identity_collision" &&
			j.SourceFileID == "" && j.SourceBaseID == "" && j.ResultBaseID == "" && j.ResultFile.ID == "" {
			i, e := parseIntent([]byte(j.IntentRaw))
			if e == nil && i.EntryKind == "directory" && i.Operation == "create" && i.Base == nil &&
				i.Predecessor == "" && i.Target == "" && len(i.Dependencies) == 0 &&
				i.Workspace == scope.Workspace && i.Collection == scope.Collection && i.Path == scope.Root+"/"+relative {
				// A terminal, effect-free duplicate ensure must not prevent the
				// authoritative directory's current metadata from reaching clients.
				reserved = false
			}
		}
		return nil
	})
	return
}

func (scope Scope) directoryRelative(p string) (string, bool) {
	if !directoryPath(p) || !strings.HasPrefix(p, scope.Root+"/") {
		return "", false
	}
	rel := strings.TrimPrefix(p, scope.Root+"/")
	return rel, directoryPath(rel)
}

// A path can be recreated with a new directory identity. Order structural
// receipts across identities as well as within one logical directory.
func directorySequence(ctx context.Context, r Records, id string) (uint64, error) {
	old, err := load[BindingRecord](ctx, r, "binding", id)
	if err == nil {
		return old.Binding.SourceSequence, nil
	}
	if !missing(err) {
		return 0, err
	}
	sequence, err := load[struct{ Sequence uint64 }](ctx, r, "cursor", "directory_sequence")
	if err != nil && !missing(err) {
		return 0, err
	}
	if sequence.Sequence >= 9007199254740991 {
		return 0, ErrHeld
	}
	sequence.Sequence++
	return sequence.Sequence, r.Put(ctx, "cursor", "directory_sequence", sequence)
}

func (s *Service) processDirectory(ctx context.Context, r Records, id string, j *Join, i Intent, p Publication, scope Scope) error {
	source, ok := s.Source.(directorySource)
	if !ok {
		holdJoin(j, "directory_source_unavailable")
		return nil
	}
	relative, ok := scope.directoryRelative(i.Path)
	if !ok {
		holdJoin(j, "outside_collection")
		return nil
	}
	target := ""
	if i.Target != "" {
		target, ok = scope.directoryRelative(i.Target)
		if !ok || strings.HasPrefix(i.Target, i.Path+"/") {
			holdJoin(j, "outside_collection")
			return nil
		}
	}
	var targets []string
	seenTargets := map[string]bool{}
	for _, dependency := range i.Dependencies {
		previous, err := load[Join](ctx, r, "operation", dependency)
		if err != nil && !missing(err) {
			return err
		}
		if missing(err) || previous.Status != "applied" {
			holdJoin(j, "waiting_for_directory_children")
			return nil
		}
		child, err := parseIntent([]byte(previous.IntentRaw))
		if err != nil || child.Workspace != i.Workspace || child.Collection != i.Collection || child.Generation != i.Generation || child.Operation != i.Operation || !strings.HasPrefix(child.Path, i.Path+"/") {
			holdJoin(j, "directory_child_mismatch")
			return nil
		}
		if i.Operation == "rename" {
			if child.Target != i.Target+strings.TrimPrefix(child.Path, i.Path) {
				holdJoin(j, "directory_child_mismatch")
				return nil
			}
			childPath, _ := scope.directoryRelative(child.Target)
			for p := childPath; p != target && p != "."; p = path.Dir(p) {
				if !seenTargets[p] {
					targets = append(targets, p)
					seenTargets[p] = true
				}
			}
		}
	}
	sort.Strings(targets)
	var payload Evidence
	if err := s.call(ctx, "payload.read", map[string]any{"intentId": i.ID}, &payload); err != nil {
		return err
	}
	if payload.Status != "available" || payload.Path != Prefix+"payload/"+i.ID+".md" || len(payload.Content) != 0 ||
		payload.Revision != p.Revisions[0].Revision || "sha256:"+payload.SHA256 != i.SHA256 {
		holdJoin(j, "directory_publication_pending")
		return nil
	}
	if i.Predecessor != "" {
		previous, err := load[Join](ctx, r, "operation", i.Predecessor)
		if err != nil && !missing(err) {
			return err
		}
		if missing(err) || previous.Status != "applied" {
			holdJoin(j, "waiting_for_predecessor")
			return nil
		}
		old, err := parseIntent([]byte(previous.IntentRaw))
		expected := old.Path
		if old.Operation == "rename" {
			expected = old.Target
		}
		if err != nil || old.EntryKind != "directory" || old.Operation == "delete" || mapKey(old) != mapKey(i) || expected != i.Path || !sameBase(i.Base, old.Base) {
			holdJoin(j, "predecessor_mismatch")
			return nil
		}
		j.SourceFileID, j.SourceBaseID = previous.SourceFileID, previous.ResultBaseID
	} else if i.Operation != "create" {
		if i.Base == nil {
			holdJoin(j, "missing_source_binding")
			return nil
		}
		binding, err := load[BindingRecord](ctx, r, "binding", i.Base.ID)
		if err != nil && !missing(err) {
			return err
		}
		if missing(err) || binding.Binding != *i.Base || i.Base.Path != i.Path {
			holdJoin(j, "binding_mismatch")
			return nil
		}
		j.SourceFileID, j.SourceBaseID = binding.SourceFileID, binding.SourceBaseID
	}
	mapping, err := load[FileMapping](ctx, r, "file", mapKey(i))
	if err != nil && !missing(err) {
		return err
	}
	if i.Operation == "create" && missing(err) {
		read, err := source.ReadDirectory(ctx, scope.SourceCollection, relative)
		if err != nil {
			holdJoin(j, "directory_create_unavailable")
			return nil
		}
		if read.Base.Generation != i.Generation || read.File.EntryKind != "directory" || read.File.CollectionID != scope.SourceCollection || read.File.RelativePath != relative {
			holdJoin(j, "directory_source_mismatch")
			return nil
		}
		mapping = FileMapping{Scope: scope, ClientFileID: i.FileID, SourceFileID: read.File.ID, CreateBaseID: read.Base.ID}
		if err := s.saveMapping(ctx, r, i, mapping); err != nil {
			if errors.Is(err, ErrHeld) {
				j.Ack = &Ack{Header: Header{1, "ack", "dir_ack_" + key(s.Replica, i.ID)}, IntentID: i.ID,
					IntentDigest: p.IntentDigest, PublicationID: p.ID, Status: "held", Reason: "directory_identity_collision"}
				j.Status = "held"
				if err := r.Put(ctx, "operation", id, *j); err != nil {
					return err
				}
				return s.publishAck(ctx, j)
			}
			return err
		}
	}
	if mapping.ClientFileID == "" || !layoutScopeAllowed(ctx, r, mapping.Scope, scope) {
		holdJoin(j, "unknown_file_mapping")
		return nil
	}
	if i.Operation == "create" {
		j.SourceFileID, j.SourceBaseID = mapping.SourceFileID, mapping.CreateBaseID
	}
	if mapping.SourceFileID != j.SourceFileID {
		holdJoin(j, "file_mapping_mismatch")
		return nil
	}
	base, err := s.Source.Base(ctx, j.SourceBaseID)
	if err != nil {
		holdJoin(j, "source_base_unavailable")
		return nil
	}
	if base.FileID != j.SourceFileID || base.Generation != i.Generation {
		holdJoin(j, "source_base_mismatch")
		return nil
	}
	j.Status, j.Reason = "pending", ""
	if err := r.Put(ctx, "operation", id, *j); err != nil {
		return err
	}
	outcome, err := source.SaveDirectory(ctx, SourceRequest{OperationID: "sync_" + key(s.Replica, i.ID), FileID: j.SourceFileID,
		Generation: i.Generation, BaseID: j.SourceBaseID, Kind: i.Operation, DestinationPath: target, DirectoryTargets: targets})
	if err != nil {
		holdJoin(j, "directory_operation_unavailable")
		return nil
	}
	if outcome.State != notesworkspace.Accepted && outcome.State != notesworkspace.Conflict {
		holdJoin(j, "directory_outcome_held")
		return nil
	}
	ack := Ack{Header: Header{1, "ack", "dir_ack_" + key(s.Replica, i.ID)}, IntentID: i.ID, IntentDigest: p.IntentDigest,
		PublicationID: p.ID, Status: "conflict", Reason: "source_conflict"}
	j.ResultBaseID, j.ResultFile = outcome.ResultBaseID, outcome.ResultFile
	if outcome.State == notesworkspace.Accepted {
		result, err := s.Source.Base(ctx, outcome.ResultBaseID)
		if err != nil {
			return err
		}
		if result.FileID != j.SourceFileID || result.Generation != i.Generation || result.Exists != (i.Operation != "delete") {
			return ErrHeld
		}
		path := i.Path
		if i.Operation == "rename" {
			path = i.Target
		}
		b := Binding{Header: Header{1, "binding", "dir_binding_" + key(s.Replica, i.ID)}, Workspace: i.Workspace, Collection: i.Collection,
			Generation: i.Generation, FileID: i.FileID, CollectionRoot: scope.Root, Path: path, SourceBase: "base_" + key(s.Replica, result.ID),
			NativeRevision: payload.Revision, SHA256: digest(nil), Writable: true, EntryKind: "directory", Deleted: i.Operation == "delete", SourceSequence: 1}
		if i.Operation == "rename" {
			b.PreviousPath = i.Path
		}
		b.ID, err = layoutBindingID(ctx, r, scope, b.ID)
		if err != nil {
			return err
		}
		b.SourceSequence, err = directorySequence(ctx, r, b.ID)
		if err != nil {
			return err
		}
		if !b.valid() {
			return ErrHeld
		}
		if err := r.Put(ctx, "binding", b.ID, BindingRecord{Binding: b, SourceFileID: j.SourceFileID, SourceBaseID: result.ID}); err != nil {
			return err
		}
		mapping.Latest = &b
		mapping.Scope = scope
		if err := r.Put(ctx, "file", mapKey(i), mapping); err != nil {
			return err
		}
		ack.Status, ack.Reason, ack.Binding = "applied", "source_applied", &b
	}
	j.Ack = &ack
	j.Status = ack.Status
	j.Reason = ""
	if err := r.Put(ctx, "operation", id, *j); err != nil {
		return err
	}
	return s.publishAck(ctx, j)
}

// Directory state uses encrypted controls only, never visible placeholder notes.
func (s *Service) ExportDirectory(ctx context.Context, collection, fileID, baseID string) (out Binding, err error) {
	var scope Scope
	for _, candidate := range s.Scopes {
		if candidate.Collection == collection {
			scope = candidate
		}
	}
	if scope.Collection == "" {
		return out, ErrHeld
	}
	writable := !scope.ReadOnly
	if source, ok := s.Source.(directorySource); ok {
		writable = writable && source.DirectoryWritable(ctx, scope.SourceCollection)
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		return s.Source.WithExport(ctx, fileID, baseID, func(file notesworkspace.File, base notesworkspace.Base) error {
			if file.EntryKind != "directory" || base.Generation != scope.Generation || file.CollectionID != scope.SourceCollection {
				return ErrHeld
			}
			mapping, e := s.outgoingExportMapping(ctx, r, scope, fileID)
			if e != nil && !missing(e) {
				return e
			}
			if mapping.Latest != nil && mapping.Scope.Root != scope.Root && base.Exists {
				out, e = s.relocate(ctx, r, scope, mapping, file, base)
				return e
			}
			path := scope.Root + "/" + file.RelativePath
			sourceBase := "base_" + key(s.Replica, baseID)
			if mapping.Latest != nil && mapping.Latest.SourceBase == sourceBase && mapping.Latest.Path == path && mapping.Latest.Writable == writable {
				out = *mapping.Latest
				return s.publishExportBinding(ctx, r, out)
			}
			if !base.Exists && mapping.Latest == nil {
				return nil
			}
			id := "directory_export_" + key(scope.Workspace, scope.Collection, scope.Generation, fileID, baseID, strconv.FormatBool(writable))
			if mapping.Latest != nil && mapping.Latest.Writable != writable {
				id += "_" + key(mapping.Latest.ID)
			}
			var payload Evidence
			if e := s.call(ctx, "payload.put", map[string]any{"intentId": id, "contentBase64": ""}, &payload); e != nil {
				return e
			}
			if payload.Status != "published" || !revRE.MatchString(payload.Revision) {
				return ErrHeld
			}
			clientID := mapping.ClientFileID
			if clientID == "" {
				clientID = "directory_" + key(scope.Workspace, scope.Collection, fileID)
			}
			b := Binding{Header: Header{1, "binding", "dir_binding_" + key(s.Replica, id)}, Workspace: scope.Workspace, Collection: scope.Collection,
				Generation: scope.Generation, FileID: clientID, CollectionRoot: scope.Root, Path: path, SourceBase: sourceBase,
				NativeRevision: payload.Revision, SHA256: digest(nil), Writable: writable, SourceSequence: 1, EntryKind: "directory", Deleted: !base.Exists}
			b.ID, e = layoutBindingID(ctx, r, scope, b.ID)
			if e != nil {
				return e
			}
			b.SourceSequence, e = directorySequence(ctx, r, b.ID)
			if e != nil {
				return e
			}
			if !b.valid() {
				return ErrHeld
			}
			if e := r.Put(ctx, "binding", b.ID, BindingRecord{Binding: b, SourceFileID: fileID, SourceBaseID: baseID}); e != nil {
				return e
			}
			mapping = FileMapping{Scope: scope, ClientFileID: clientID, SourceFileID: fileID, Latest: &b}
			if e := s.saveMapping(ctx, r, Intent{Workspace: scope.Workspace, Collection: scope.Collection, Generation: scope.Generation, FileID: clientID}, mapping); e != nil {
				return e
			}
			out = b
			return s.publishExportBinding(ctx, r, b)
		})
	})
	return
}
