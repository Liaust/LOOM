package notesworkspacesync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"loom.local/loom/internal/notesworkspace"
	"strings"
)

type exportRecord struct {
	Scope                                      Scope
	FileID, BaseID, Path, ClientFileID, Parent string
	Binding                                    *Binding
}

// Export publishes an already persisted source Read result. Call source.Read for
// an authorized export, not while admitting an incoming edit. Retries name the
// same file/base and retain the originally selected native parent.
func (s *Service) Export(ctx context.Context, collection, fileID, baseID string) (out Binding, err error) {
	if err = s.valid(); err != nil {
		return
	}
	if err = s.ready(ctx); err != nil {
		return
	}
	var scope Scope
	found := false
	for _, v := range s.Scopes {
		if v.Collection == collection {
			if found {
				return out, ErrHeld
			}
			scope = v
			found = true
		}
	}
	if !found {
		return out, ErrHeld
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		return s.Source.WithExport(ctx, fileID, baseID, func(file notesworkspace.File, base notesworkspace.Base) error {
			id := "export_" + key(scope.Workspace, scope.Collection, scope.Generation, fileID, baseID)
			ex, e := load[exportRecord](ctx, r, "export", id)
			if e != nil && !missing(e) {
				return e
			}
			reference := referencePath(file.RelativePath)
			if !base.Exists || base.FileID != fileID || base.Generation != scope.Generation || (!reference && !utf8Text(base.Content)) || len(base.Content) > MaxReferenceBytes || digest(base.Content) != base.Hash {
				return ErrHeld
			}
			mapping, e := exportMapping(ctx, r, scope, fileID)
			if e != nil && !missing(e) {
				return e
			}
			if mapping.ClientFileID != "" {
				if mapping.Latest != nil {
					latest := *mapping.Latest
					if latest.Path != scope.Root+"/"+file.RelativePath {
						return ErrHeld
					}
					if latest.Generation == scope.Generation && latest.SourceBase == "base_"+key(s.Replica, baseID) && latest.SHA256 == base.Hash {
						if latest.SourceSequence > 0 {
							out = latest
							return s.publishBinding(ctx, latest)
						}
						id = "export_" + key(scope.Workspace, scope.Collection, scope.Generation, fileID, baseID, "source-sequence-v1", latest.ID)
						ex, e = load[exportRecord](ctx, r, "export", id)
						if e != nil && !missing(e) {
							return e
						}
					}
					// Content-addressed bases recur on A-B-A source changes. A
					// completed old export is not the current native revision.
					// Retain pending legacy receipts, but give a reversion a new
					// identity bound to its actual current parent.
					if latest.Generation == scope.Generation && ex.Binding != nil && ex.Binding.ID != latest.ID {
						id = "export_" + key(scope.Workspace, scope.Collection, scope.Generation, fileID, baseID, latest.NativeRevision)
						ex, e = load[exportRecord](ctx, r, "export", id)
						if e != nil && !missing(e) {
							return e
						}
					}
				}
			}
			if ex.BaseID == "" {
				if file.CollectionID != scope.SourceCollection || !workspacePath(file.RelativePath, true) {
					return ErrHeld
				}
				ex = exportRecord{Scope: scope, FileID: fileID, BaseID: baseID, Path: scope.Root + "/" + file.RelativePath, ClientFileID: "file_" + key(scope.Workspace, scope.Collection, fileID)}
				if mapping.ClientFileID != "" {
					ex.ClientFileID = mapping.ClientFileID
				}
				if mapping.Latest != nil {
					if mapping.Latest.Path != ex.Path {
						return ErrHeld
					}
					ex.Parent = mapping.Latest.NativeRevision
				}
				if e = r.Put(ctx, "export", id, ex); e != nil {
					return e
				}
			}
			if !sameExportScope(ex.Scope, scope) || ex.FileID != fileID || ex.BaseID != baseID {
				return ErrHeld
			}
			if ex.Binding == nil {
				if mapping.Latest != nil && mapping.Latest.Generation != scope.Generation {
					// Check the actual current leaf, not merely the retained base:
					// outstanding client edits must survive a selection expansion.
					var exact Evidence
					if e := s.call(ctx, "read.path", map[string]any{"path": ex.Path, "revision": ex.Parent}, &exact); e != nil {
						return e
					}
					var leaves struct {
						Leaves []Evidence `json:"leaves"`
					}
					readable := exact.Status == "available" && "sha256:"+exact.SHA256 == mapping.Latest.SHA256
					// Binary reads expose exact immutable revision metadata rather
					// than returning the attachment bytes over the text-only reader.
					referenceMetadata := reference && exact.Status == "held" && (exact.Reason == "reference_binary" || exact.Reason == "content_limit")
					if exact.ID == "" || exact.Path != ex.Path || exact.Revision != ex.Parent || (!readable && !referenceMetadata) || exact.LogicalDeleted || exact.BranchDeleted {
						return ErrHeld
					}
					if e := s.call(ctx, "leaves", map[string]any{"id": exact.ID}, &leaves); e != nil {
						return e
					}
					if len(leaves.Leaves) != 1 || leaves.Leaves[0].Revision != ex.Parent || leaves.Leaves[0].LogicalDeleted || leaves.Leaves[0].BranchDeleted {
						return ErrHeld
					}
				}
				args := map[string]any{"operationId": id, "path": ex.Path, "contentBase64": base64.StdEncoding.EncodeToString(base.Content)}
				if ex.Parent == "" {
					args["create"] = true
				} else {
					args["baseRevision"] = ex.Parent
				}
				var result Evidence
				method := "publish"
				if reference {
					method = "publish.reference"
				}
				if e = s.call(ctx, method, args, &result); e != nil {
					return e
				}
				if result.Status != "published" || !revRE.MatchString(result.Revision) || "sha256:"+result.SHA256 != base.Hash {
					return ErrHeld
				}
				b := Binding{Header: Header{1, "binding", "binding_" + key(s.Replica, id)}, Workspace: scope.Workspace, Collection: scope.Collection, Generation: scope.Generation, FileID: ex.ClientFileID, CollectionRoot: scope.Root, Path: ex.Path, SourceBase: "base_" + key(s.Replica, baseID), NativeRevision: result.Revision, SHA256: base.Hash, Writable: !reference}
				b.SourceSequence = 1
				if mapping.Latest != nil {
					b.SourceSequence = mapping.Latest.SourceSequence + 1
				}
				if !b.valid() {
					return ErrHeld
				}
				ex.Binding = &b
				if e = r.Put(ctx, "export", id, ex); e != nil {
					return e
				}
			}
			b := *ex.Binding
			record := BindingRecord{Binding: b, SourceFileID: fileID, SourceBaseID: baseID}
			old, e := load[BindingRecord](ctx, r, "binding", b.ID)
			if e != nil && !missing(e) {
				return e
			}
			if e == nil && old != record {
				return ErrHeld
			}
			if e = r.Put(ctx, "binding", b.ID, record); e != nil {
				return e
			}
			i := Intent{Workspace: scope.Workspace, Collection: scope.Collection, Generation: scope.Generation, FileID: b.FileID}
			if mapping.ClientFileID == "" {
				mapping = FileMapping{Scope: scope, ClientFileID: b.FileID, SourceFileID: fileID}
			}
			mapping.Scope = scope
			// Do not roll the latest parent backwards when an old export is replayed.
			if mapping.Latest == nil || mapping.Latest.NativeRevision == ex.Parent || mapping.Latest.ID == b.ID {
				mapping.Latest = &b
			}
			if e = s.saveMapping(ctx, r, i, mapping); e != nil {
				return e
			}
			out = b
			return s.publishBinding(ctx, b)
		})
	})
	return
}

func sameExportScope(a, b Scope) bool {
	a.PreviousGeneration, b.PreviousGeneration = "", ""
	return a == b
}

func (s *Service) enrollmentExports(ctx context.Context, after string, limit int) (out []FileMapping, next string, err error) {
	if limit < 1 || limit > 128 {
		return nil, "", ErrHeld
	}
	needed := false
	for _, scope := range s.Scopes {
		needed = needed || scope.PreviousGeneration != "" && scope.PreviousGeneration != scope.Generation
	}
	if !needed {
		return nil, "", nil
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		ids, err := r.List(ctx, "file", after, limit)
		if err != nil {
			return err
		}
		for _, id := range ids {
			mapping, err := load[FileMapping](ctx, r, "file", id)
			if err != nil {
				return err
			}
			for _, scope := range s.Scopes {
				previous := scope
				previous.Generation = scope.PreviousGeneration
				if scope.PreviousGeneration == "" || previous.Generation == scope.Generation || !sameExportScope(mapping.Scope, previous) {
					continue
				}
				if mapping.Latest == nil || !mapping.Latest.valid() || mapping.SourceFileID == "" {
					return ErrHeld
				}
				out = append(out, mapping)
			}
		}
		if len(ids) == limit {
			next = ids[len(ids)-1]
		}
		return nil
	})
	return
}

func exportMapping(ctx context.Context, r Records, scope Scope, fileID string) (FileMapping, error) {
	lookup := scope
	clientID, err := load[string](ctx, r, "source_file", sourceKey(lookup, fileID))
	if missing(err) && scope.PreviousGeneration != "" && scope.PreviousGeneration != scope.Generation {
		lookup.Generation = scope.PreviousGeneration
		clientID, err = load[string](ctx, r, "source_file", sourceKey(lookup, fileID))
	}
	if err != nil {
		return FileMapping{}, err
	}
	mapping, err := load[FileMapping](ctx, r, "file", mapKey(Intent{Workspace: lookup.Workspace, Collection: lookup.Collection, Generation: lookup.Generation, FileID: clientID}))
	if err != nil {
		return FileMapping{}, err
	}
	if !sameExportScope(mapping.Scope, lookup) || mapping.SourceFileID != fileID || mapping.ClientFileID != clientID {
		return FileMapping{}, ErrHeld
	}
	if mapping.Latest == nil {
		if lookup.Generation != scope.Generation {
			return FileMapping{}, ErrHeld
		}
	} else if !mapping.Latest.valid() || mapping.Latest.Generation != lookup.Generation || mapping.Latest.Workspace != lookup.Workspace || mapping.Latest.Collection != lookup.Collection || mapping.Latest.FileID != clientID || mapping.Latest.CollectionRoot != lookup.Root {
		return FileMapping{}, ErrHeld
	}
	return mapping, nil
}

func (s *Service) publishBinding(ctx context.Context, b Binding) error {
	raw, err := marshalControl(b)
	if err != nil {
		return err
	}
	var result Evidence
	if err := s.call(ctx, "control.put", map[string]any{"contentBase64": raw}, &result); err != nil {
		return err
	}
	if result.Status != "published" {
		return errors.New("binding publication pending")
	}
	return nil
}
func marshalControl(v any) (string, error) {
	raw, e := json.Marshal(v)
	return base64.StdEncoding.EncodeToString(raw), e
}

// IsSourcePath excludes protocol controls before any Notes-ingestion integration.
// Runtime must additionally enforce its existing admitted source selection.
func IsSourcePath(p string) bool { return !strings.HasPrefix(p, Prefix) && workspacePath(p, true) }
