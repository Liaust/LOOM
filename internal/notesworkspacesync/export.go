package notesworkspacesync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"loom.local/loom/internal/notesworkspace"
	"strings"
	"time"
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
	return s.export(ctx, collection, fileID, baseID, "")
}

func (s *Service) export(ctx context.Context, collection, fileID, baseID, referenceSnapshot string, inputs ...referenceAttemptInput) (out Binding, err error) {
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
	desiredScope := scope
	var input referenceAttemptInput
	if len(inputs) > 0 {
		input = inputs[0]
	}
	var failed referenceAttempt
	localEvidenceFailure := false
	retainFailure := func(r Records, failure error) error {
		var typed referenceDeliveryHold
		if !localEvidenceFailure || input.Fingerprint == "" || failed.FailureExport == "" || !errors.Is(failure, ErrHeld) || errors.As(failure, &typed) {
			return failure
		}
		proofErr := func() error {
			prior, e := load[referenceAttempt](ctx, r, "cursor", referenceAttemptKey(desiredScope, fileID))
			if e != nil && !missing(e) {
				return e
			}
			if e == nil && prior.Scope == failed.Scope && prior.Fingerprint == failed.Fingerprint && prior.ParentBinding == failed.ParentBinding {
				failed.Request = prior.Request
			}
			failed.FailureHash, e = s.referenceFailureHash(ctx, r, desiredScope, failed)
			if e != nil {
				return e
			}
			now := time.Now()
			if s.Now != nil {
				now = s.Now()
			}
			failed.FailureRetryAt = now.Add(layoutEvidenceRetry)
			s.dependencyCache().settled(layoutDependencyKey(desiredScope, "reference_"+fileID))
			return r.Put(ctx, "cursor", referenceAttemptKey(desiredScope, fileID), failed)
		}()
		return errors.Join(referenceHold("reference_export_evidence_held"), proofErr)
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) (failure error) {
		// Capture the failed dependency state before releasing its record owner;
		// a concurrent repair must never be certified as the failed state.
		defer func() { failure = retainFailure(r, failure) }()
		return s.Source.WithExport(ctx, fileID, baseID, func(file notesworkspace.File, base notesworkspace.Base) error {
			reference := referencePath(file.RelativePath)
			writable := !reference && !scope.ReadOnly
			streamed := reference && referenceSnapshot != "" && base.ReferenceSize >= 0 && base.ReferenceSize <= notesworkspace.MaxStreamReferenceBytes && len(base.Content) == 0
			if file.ID != fileID || file.CollectionID != scope.SourceCollection || base.ID != baseID || !base.Exists || base.FileID != fileID || base.Generation != scope.Generation || (!reference && !utf8Text(base.Content)) || len(base.Content) > MaxReferenceBytes || (!streamed && digest(base.Content) != base.Hash) {
				return ErrHeld
			}
			mapping, e := s.outgoingExportMapping(ctx, r, scope, fileID)
			if e != nil && !missing(e) {
				return e
			}
			if reference && input.Fingerprint != "" {
				candidate := mapping
				if candidate.ClientFileID == "" {
					candidate.Scope = scope
					candidate.ClientFileID = "file_" + key(scope.Workspace, scope.Collection, fileID)
				}
				failed = referenceAttempt{Scope: desiredScope, Fingerprint: input.Fingerprint, FileID: fileID, PathVersion: file.PathVersion,
					FailureMapping: mapKey(Intent{Workspace: candidate.Scope.Workspace, Collection: candidate.Scope.Collection, Generation: candidate.Scope.Generation, FileID: candidate.ClientFileID})}
				if mapping.Latest != nil {
					failed.ParentBinding = mapping.Latest.ID
				}
			}
			exportID := func(extra ...string) string {
				parts := []string{scope.Workspace, scope.Collection, scope.Generation, fileID, baseID}
				if mapping.SuccessorOf != "" {
					parts = append(parts, "successor-v1", mapping.ClientFileID)
				}
				id := "export_" + key(append(parts, extra...)...)
				if failed.FailureMapping != "" {
					failed.FailureExport = id
				}
				return id
			}
			id := exportID()
			ex, e := load[exportRecord](ctx, r, "export", id)
			if e != nil && !missing(e) {
				return e
			}
			if mapping.Latest != nil && mapping.Scope.Root != scope.Root {
				if mapping.Latest.Generation == scope.Generation && mapping.Latest.SourceBase == "base_"+key(s.Replica, baseID) && mapping.Latest.SHA256 == base.Hash {
					out, e = s.relocate(ctx, r, scope, mapping, file, base)
					return e
				}
				// Catch up source generation/base/bytes at the retained path first. A
				// subsequent bounded step moves that exact new binding; a layout
				// never relabels stale bytes as a current canonical base.
				if !layoutScopeAllowed(ctx, r, mapping.Scope, scope) && !s.outgoingLayoutScopeAllowed(ctx, r, mapping.Scope, scope) {
					return ErrHeld
				}
				scope.Root = mapping.Scope.Root
				if e := r.Put(ctx, "export", layoutAliasKey(scope), scope); e != nil {
					return e
				}
			}
			if writable && mapping.Latest != nil && !mapping.Latest.Writable {
				id = exportID("editable-v1", mapping.Latest.ID)
				ex, e = load[exportRecord](ctx, r, "export", id)
				if e != nil && !missing(e) {
					return e
				}
			}
			if mapping.ClientFileID != "" {
				if mapping.Latest != nil {
					latest := *mapping.Latest
					if latest.Path != scope.Root+"/"+file.RelativePath {
						return ErrHeld
					}
					if latest.Generation == scope.Generation && latest.SourceBase == "base_"+key(s.Replica, baseID) && latest.SHA256 == base.Hash && latest.Writable == writable {
						if latest.SourceSequence > 0 {
							out = latest
							return s.publishExportBinding(ctx, r, latest)
						}
						id = exportID("source-sequence-v1", latest.ID)
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
						id = exportID(latest.NativeRevision)
						ex, e = load[exportRecord](ctx, r, "export", id)
						if e != nil && !missing(e) {
							return e
						}
					}
				}
			}
			if s.AdoptExisting && mapping.Latest != nil && mapping.Latest.Generation != scope.Generation && ex.BaseID != "" && ex.Binding == nil && ex.Parent != mapping.Latest.NativeRevision {
				// Preserve the earlier failed create intent; recovery names the
				// retained parent rather than changing its original request.
				id = exportID("retained-parent-v1", mapping.Latest.NativeRevision)
				ex, e = load[exportRecord](ctx, r, "export", id)
				if e != nil && !missing(e) {
					return e
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
				localEvidenceFailure = true
				return ErrHeld
			}
			if ex.Binding == nil {
				soleParent := true
				replay := false
				if reference && mapping.Latest != nil && mapping.Latest.Generation != scope.Generation {
					request := map[string]any{"operationId": id, "path": ex.Path, "sha256": strings.TrimPrefix(base.Hash, "sha256:"), "size": base.ReferenceSize, "baseRevision": ex.Parent, "soleParent": true}
					if input.Fingerprint != "" {
						attempt := referenceAttempt{Scope: desiredScope, Fingerprint: input.Fingerprint, FileID: fileID, PathVersion: file.PathVersion, ParentBinding: mapping.Latest.ID, Request: request}
						if e := r.Put(ctx, "cursor", referenceAttemptKey(desiredScope, fileID), attempt); e != nil {
							return e
						}
					}
					var result struct{ Status, Outcome, Reason string }
					if e := s.call(ctx, "publication.preflight", request, &result); e != nil {
						return e
					}
					replay = result.Status == "ready" && result.Outcome == "published"
					if result.Status != "ready" {
						return referencePublicationHold(result.Reason)
					}
				}
				if mapping.Latest != nil && mapping.Latest.Generation != scope.Generation && !replay {
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
						if reference {
							return referencePublicationHold("base_unavailable")
						}
						return ErrHeld
					}
					if e := s.call(ctx, "leaves", map[string]any{"id": exact.ID}, &leaves); e != nil {
						return e
					}
					soleParent = len(leaves.Leaves) == 1 && leaves.Leaves[0].Revision == ex.Parent && !leaves.Leaves[0].LogicalDeleted && !leaves.Leaves[0].BranchDeleted
					if !soleParent && (!s.AdoptExisting || reference || len(leaves.Leaves) == 0) {
						if reference {
							return referencePublicationHold("base_diverged")
						}
						return ErrHeld
					}
					// Text publication can create a sibling from the retained exact
					// parent. Never replace/purge the outstanding client branches.
				}
				result := Evidence{}
				unchanged := !replay && soleParent && s.AdoptExisting && mapping.Latest != nil && mapping.Latest.Generation != scope.Generation && mapping.Latest.SHA256 == base.Hash
				if unchanged {
					// The exact sole leaf was checked above. Only its binding changes.
					result = Evidence{Status: "published", Revision: ex.Parent, SHA256: strings.TrimPrefix(base.Hash, "sha256:")}
				} else {
					args := map[string]any{"operationId": id, "path": ex.Path, "contentBase64": base64.StdEncoding.EncodeToString(base.Content)}
					if ex.Parent == "" {
						args["create"] = true
						if s.AdoptExisting && mapping.ClientFileID == "" && !reference {
							args["adoptIdentical"] = true
						}
					} else {
						args["baseRevision"] = ex.Parent
					}
					method := "publish"
					if reference {
						method = "publish.reference"
					}
					if streamed {
						method = "publish.reference.file"
						delete(args, "contentBase64")
						args["snapshot"] = referenceSnapshot
						args["size"] = base.ReferenceSize
						args["sha256"] = strings.TrimPrefix(base.Hash, "sha256:")
					}
					if reference && input.Fingerprint != "" {
						request := map[string]any{"operationId": id, "path": ex.Path, "sha256": strings.TrimPrefix(base.Hash, "sha256:"), "size": base.ReferenceSize}
						parent := ""
						if ex.Parent == "" {
							request["create"] = true
						} else {
							request["baseRevision"] = ex.Parent
						}
						if mapping.Latest != nil {
							parent = mapping.Latest.ID
							request["soleParent"] = mapping.Latest.Generation != desiredScope.Generation
						}
						attempt := referenceAttempt{Scope: desiredScope, Fingerprint: input.Fingerprint, FileID: fileID, PathVersion: file.PathVersion, ParentBinding: parent, Request: request}
						if err := r.Put(ctx, "cursor", referenceAttemptKey(desiredScope, fileID), attempt); err != nil {
							return err
						}
					}
					if e = s.call(ctx, method, args, &result); e != nil {
						return e
					}
				}
				if result.Status != "published" || !revRE.MatchString(result.Revision) || "sha256:"+result.SHA256 != base.Hash {
					if reference {
						return referencePublicationHold(result.Reason)
					}
					return ErrHeld
				}
				b := Binding{Header: Header{1, "binding", "binding_" + key(s.Replica, id)}, Workspace: scope.Workspace, Collection: scope.Collection, Generation: scope.Generation, FileID: ex.ClientFileID, CollectionRoot: scope.Root, Path: ex.Path, SourceBase: "base_" + key(s.Replica, baseID), NativeRevision: result.Revision, SHA256: base.Hash, Writable: writable}
				if strings.HasSuffix(strings.ToLower(ex.Path), ".canvas") {
					b.ID = "canvas_" + b.ID
				}
				b.ID, e = layoutBindingID(ctx, r, scope, b.ID)
				if e != nil {
					return e
				}
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
			failed.FailureBinding = b.ID
			record := BindingRecord{Binding: b, SourceFileID: fileID, SourceBaseID: baseID}
			old, e := load[BindingRecord](ctx, r, "binding", b.ID)
			if e != nil && !missing(e) {
				return e
			}
			if e == nil && old != record {
				localEvidenceFailure = true
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
			return s.publishExportBinding(ctx, r, b)
		})
	})
	if err == nil && out.ID != "" {
		err = s.observeMoveExport(ctx, out)
	}
	return
}

func sameExportScope(a, b Scope) bool {
	a.PreviousGeneration, b.PreviousGeneration = "", ""
	return a == b
}

// A completed metadata pass is not new work on every native retry. Keep its
// coverage across turns; changed scopes invalidate it and sparse repair remains.
func enrollmentScanDue(cursor *RuntimeCursor, scopes []Scope, now time.Time) bool {
	raw, _ := json.Marshal(scopes)
	revision := digest(raw)
	if cursor.EnrollmentContext != revision {
		cursor.EnrollmentContext, cursor.EnrollmentAfter = revision, ""
		cursor.EnrollmentComplete, cursor.EnrollmentRetryAt = false, time.Time{}
	}
	if cursor.EnrollmentComplete && cursor.EnrollmentRetryAt.After(now) {
		return false
	}
	if cursor.EnrollmentComplete {
		cursor.EnrollmentAfter, cursor.EnrollmentComplete = "", false
	}
	return true
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
		var ids []string
		var err error
		if scoped, ok := r.(enrollmentFileRecords); ok {
			ids, err = scoped.ListEnrollmentFiles(ctx, after, limit, s.Scopes)
		} else {
			ids, err = r.List(ctx, "file", after, limit)
		}
		if err != nil {
			return err
		}
		for _, id := range ids {
			mapping, err := load[FileMapping](ctx, r, "file", id)
			if err != nil {
				return err
			}
			for _, scope := range s.Scopes {
				// Legacy Canvas exports used binary/read-only bindings. During the
				// existing enrollment catch-up, refresh that metadata even if its
				// bytes and generation already match. No source edit is invented.
				if sameExportScope(mapping.Scope, scope) && !scope.ReadOnly && mapping.Latest != nil &&
					mapping.Latest.valid() && !mapping.Latest.Writable && strings.HasSuffix(strings.ToLower(mapping.Latest.Path), ".canvas") {
					out = append(out, mapping)
					continue
				}
				previous := scope
				previous.Generation = scope.PreviousGeneration
				if scope.PreviousGeneration == "" || previous.Generation == scope.Generation || !sameExportScope(mapping.Scope, previous) {
					continue
				}
				if mapping.Latest == nil || !mapping.Latest.valid() || mapping.SourceFileID == "" {
					return ErrHeld
				}
				// The predecessor is immutable history. Once its current successor
				// has a durable native publication receipt, it is not more catch-up.
				current, e := exportMapping(ctx, r, scope, mapping.SourceFileID)
				if e != nil && !missing(e) {
					return e
				}
				if e == nil && sameExportScope(current.Scope, scope) && current.Latest != nil && current.Latest.valid() && current.Latest.Generation == scope.Generation {
					published, e := load[exportPublication](ctx, r, "export", "published_"+key(current.Latest.ID))
					if e != nil && !missing(e) {
						return e
					}
					if e == nil && published == (exportPublication{current.Latest.ID, current.Latest.NativeRevision, current.Latest.SHA256}) {
						continue
					}
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
	if !sameExportScope(mapping.Scope, lookup) && !layoutScopeAllowed(ctx, r, mapping.Scope, lookup) {
		return FileMapping{}, ErrHeld
	}
	mapping, err = recoverRetainedLayoutMapping(ctx, r, mapping)
	if err != nil {
		return FileMapping{}, err
	}
	if mapping.Scope.Generation != lookup.Generation {
		lookup.Generation = mapping.Scope.Generation
	}
	if !sameExportScope(mapping.Scope, lookup) && !layoutScopeAllowed(ctx, r, mapping.Scope, lookup) || mapping.SourceFileID != fileID || mapping.ClientFileID != clientID {
		return FileMapping{}, ErrHeld
	}
	if mapping.Latest == nil {
		if lookup.Generation != scope.Generation {
			return FileMapping{}, ErrHeld
		}
	} else if !mapping.Latest.valid() || mapping.Latest.Generation != lookup.Generation || mapping.Latest.Workspace != lookup.Workspace || mapping.Latest.Collection != lookup.Collection || mapping.Latest.FileID != clientID || mapping.Latest.CollectionRoot != mapping.Scope.Root {
		return FileMapping{}, ErrHeld
	}
	return mapping, nil
}

// Older layout IDs omitted the generation/base and could save a newer scope
// beside an older binding. Recover navigation only from its exact intact origin;
// current source admission and the ordinary export guard still authorize writes.
func recoverRetainedLayoutMapping(ctx context.Context, r Records, mapping FileMapping) (FileMapping, error) {
	latest := mapping.Latest
	if latest == nil || latest.Generation == mapping.Scope.Generation {
		return mapping, nil
	}
	if !strings.HasPrefix(latest.ID, "layout_") || latest.EntryKind != "directory" || !latest.valid() {
		return FileMapping{}, ErrHeld
	}
	originScope := mapping.Scope
	originScope.Generation = latest.Generation
	origin, err := load[FileMapping](ctx, r, "file", mapKey(Intent{Workspace: originScope.Workspace, Collection: originScope.Collection, Generation: originScope.Generation, FileID: mapping.ClientFileID}))
	if err != nil {
		if missing(err) {
			return FileMapping{}, ErrHeld
		}
		return FileMapping{}, err
	}
	if !validExportMapping(origin, originScope, mapping.SourceFileID, mapping.ClientFileID) || origin.Latest == nil || *origin.Latest != *latest {
		return FileMapping{}, ErrHeld
	}
	receipt, err := load[BindingRecord](ctx, r, "binding", latest.ID)
	if err != nil {
		if missing(err) {
			return FileMapping{}, ErrHeld
		}
		return FileMapping{}, err
	}
	if receipt.Binding != *latest || receipt.SourceFileID != mapping.SourceFileID {
		return FileMapping{}, ErrHeld
	}
	return origin, nil
}

func (s *Service) exportMapping(ctx context.Context, r Records, scope Scope, fileID string) (FileMapping, error) {
	mapping, err := exportMapping(ctx, r, scope, fileID)
	if missing(err) && s.AdoptExisting {
		return retainedMapping(ctx, r, scope, fileID)
	}
	return mapping, err
}

func (s *Service) outgoingExportMapping(ctx context.Context, r Records, scope Scope, fileID string) (FileMapping, error) {
	mapping, err := s.exportMapping(ctx, r, scope, fileID)
	if s.AdoptExisting && s.LayoutPublication && (missing(err) || errors.Is(err, errMappingIndexPending)) {
		roots, e := load[layoutRoots](ctx, r, "cursor", layoutRootsKey(scope))
		if e != nil && !missing(e) {
			return FileMapping{}, e
		}
		found := false
		for _, root := range roots.Roots {
			if root == scope.Root {
				continue
			}
			candidate := scope
			candidate.Root = root
			retained, e := retainedMapping(ctx, r, candidate, fileID)
			if missing(e) || errors.Is(e, errMappingIndexPending) {
				continue
			}
			if e != nil {
				return FileMapping{}, e
			}
			if found || !s.outgoingLayoutScopeAllowed(ctx, r, retained.Scope, scope) {
				return FileMapping{}, ErrHeld
			}
			mapping, found = retained, true
		}
		if found {
			return mapping, nil
		}
	}
	return mapping, err
}

// Outgoing catch-up may rebind any retained generation, just as same-root
// adoption does. Only a registered display root is normalized; WithExport and
// the ordinary exact native-leaf checks still authorize the new binding.
func (s *Service) outgoingLayoutScopeAllowed(ctx context.Context, r Records, retained, current Scope) bool {
	if !s.AdoptExisting || !s.LayoutPublication || retained.Root == current.Root {
		return false
	}
	candidate := retained
	candidate.Generation = current.Generation
	if !sameSourceScope(candidate, current) {
		return false
	}
	roots, err := load[layoutRoots](ctx, r, "cursor", layoutRootsKey(current))
	if err != nil {
		return false
	}
	oldRoot, newRoot := false, false
	for _, root := range roots.Roots {
		oldRoot = oldRoot || root == retained.Root
		newRoot = newRoot || root == current.Root
	}
	return oldRoot && newRoot
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

// exportPublication is control durability evidence under the existing export
// record kind. Legacy mappings replay only their small binding once, never all
// file bytes, to establish this additive receipt.
type exportPublication struct{ BindingID, Revision, Hash string }

func (s *Service) publishExportBinding(ctx context.Context, r Records, b Binding) error {
	id := "published_" + key(b.ID)
	expected := exportPublication{b.ID, b.NativeRevision, b.SHA256}
	old, err := load[exportPublication](ctx, r, "export", id)
	if err == nil {
		if old != expected {
			return ErrHeld
		}
		return nil
	}
	if !missing(err) {
		return err
	}
	if err := s.publishBinding(ctx, b); err != nil {
		return err
	}
	return r.Put(ctx, "export", id, expected)
}

// ExportInventory adopts current mappings with their existing native parent and
// ID after canonical source validation. A missing publication receipt resumes
// ordinary Export; crashes never silently acknowledge an incomplete binding.
func (s *Service) ExportInventory(ctx context.Context, collection, fileID, baseID string) (out Binding, adopted bool, err error) {
	if err = s.valid(); err != nil {
		return
	}
	if err = s.ready(ctx); err != nil {
		return
	}
	var scope Scope
	for _, candidate := range s.Scopes {
		if candidate.Collection == collection {
			scope = candidate
			break
		}
	}
	if scope.Collection == "" {
		return out, false, ErrHeld
	}
	var retained *Binding
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		return s.Source.WithExport(ctx, fileID, baseID, func(file notesworkspace.File, base notesworkspace.Base) error {
			mapping, err := s.outgoingExportMapping(ctx, r, scope, fileID)
			if missing(err) {
				return nil
			}
			if err != nil {
				return err
			}
			latest := mapping.Latest
			retained = latest
			if latest == nil || latest.Generation != scope.Generation || latest.Path != scope.Root+"/"+file.RelativePath || latest.SourceBase != "base_"+key(s.Replica, baseID) || latest.SHA256 != base.Hash || latest.SourceSequence < 1 {
				return nil
			}
			receipt, err := load[exportPublication](ctx, r, "export", "published_"+key(latest.ID))
			if missing(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if receipt != (exportPublication{latest.ID, latest.NativeRevision, latest.SHA256}) {
				return ErrHeld
			}
			if base.Generation != scope.Generation || !base.Exists || base.FileID != file.ID || digest(base.Content) != base.Hash {
				return ErrHeld
			}
			out = *latest
			adopted = true
			return nil
		})
	})
	if err != nil || adopted {
		return
	}
	out, err = s.Export(ctx, collection, fileID, baseID)
	adopted = err == nil && retained != nil && retained.NativeRevision == out.NativeRevision && retained.SHA256 == out.SHA256 && retained.FileID == out.FileID
	return
}
