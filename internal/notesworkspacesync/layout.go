package notesworkspacesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"loom.local/loom/internal/notesworkspace"
)

// Layout changes transport paths, never the canonical source identity or bytes.
type Layout struct {
	Header
	Previous   Binding `json:"previous"`
	Binding    Binding `json:"binding"`
	SourceMove bool    `json:"sourceMove,omitempty"`
}

type layoutCursor struct {
	After      string
	Complete   bool
	Retry      bool
	RetryAt    time.Time
	RetryKnown bool         `json:",omitempty"`
	Holds      []layoutHold `json:",omitempty"`
}

type layoutHold struct {
	MappingID, Reason, Dependency string
	Dependencies                  []string `json:",omitempty"`
	RetryAt                       time.Time
	Attempts                      int
}

const layoutEvidenceRetry = 6 * time.Hour

type layoutRoots struct{ Roots []string }

func layoutRootsKey(scope Scope) string {
	return "layout_roots_" + key(scope.Workspace, scope.Collection, scope.Generation)
}

func layoutBindingID(ctx context.Context, r Records, scope Scope, id string) (string, error) {
	_, err := load[layoutRoots](ctx, r, "cursor", layoutRootsKey(scope))
	if missing(err) {
		return id, nil
	}
	if err != nil {
		return "", err
	}
	return "layout_" + id, nil
}

func layoutAliasKey(scope Scope) string {
	return "layout_alias_" + key(scope.Workspace, scope.Collection, scope.Generation, scope.Root)
}

func sameSourceScope(a, b Scope) bool {
	a.Root, b.Root, a.PreviousGeneration, b.PreviousGeneration = "", "", "", ""
	return a == b
}

func layoutScopeAllowed(ctx context.Context, r Records, old, current Scope) bool {
	compare := old
	if current.ReadOnly || old.ReadOnly {
		compare.Generation, compare.ReadOnly = current.Generation, current.ReadOnly
	}
	if !sameSourceScope(compare, current) {
		return false
	}
	if old.Root == current.Root {
		return true
	}
	alias, err := load[Scope](ctx, r, "export", layoutAliasKey(old))
	return err == nil && sameExportScope(alias, old)
}

// Reference and directory display metadata can cross a source revision after a
// current admitted read. This grants no old-generation write authority.
func observedLayoutScopeAllowed(ctx context.Context, r Records, mapping FileMapping, current Scope) bool {
	if mapping.Latest == nil || mapping.Latest.EntryKind != "directory" && (mapping.Latest.Writable || !referencePath(mapping.Latest.Path)) {
		return false
	}
	old := mapping.Scope
	old.Generation = current.Generation
	if layoutScopeAllowed(ctx, r, old, current) {
		return true
	}
	if !sameSourceScope(old, current) {
		return false
	}
	alias, err := load[Scope](ctx, r, "export", layoutAliasKey(mapping.Scope))
	return err == nil && sameExportScope(alias, mapping.Scope)
}

func (s *Service) prepareLayouts(ctx context.Context, selections []RuntimeSelection) error {
	if !s.LayoutPublication {
		return nil
	}
	return s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		for _, selection := range selections {
			if selection.LayoutFrom == "" || selection.LayoutFrom == selection.Root {
				continue
			}
			for _, scope := range s.Scopes {
				if scope.Collection != selection.Collection {
					continue
				}
				old := scope
				old.Root = selection.LayoutFrom
				if selection.LayoutGeneration != "" {
					old.Generation, old.ReadOnly = selection.LayoutGeneration, selection.LayoutReadOnly
				}
				if !textPath(old.Root + "/probe.md") {
					return ErrHeld
				}
				if err := r.Put(ctx, "export", layoutAliasKey(old), old); err != nil {
					return err
				}
				if err := r.Put(ctx, "export", layoutAliasKey(scope), scope); err != nil {
					return err
				}
				roots, err := load[layoutRoots](ctx, r, "cursor", layoutRootsKey(scope))
				if err != nil && !missing(err) {
					return err
				}
				for _, root := range []string{old.Root, scope.Root} {
					found := false
					for _, retained := range roots.Roots {
						found = found || retained == root
					}
					if !found {
						roots.Roots = append(roots.Roots, root)
					}
				}
				if err := r.Put(ctx, "cursor", layoutRootsKey(scope), roots); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// An old-path intent still needs its exact retained binding (or registered scope
// for a create), current source policy and normal base/conflict validation.
func (s *Service) intentScope(ctx context.Context, r Records, i Intent) (Scope, bool) {
	current, ok := s.scope(i)
	if !ok {
		for _, scope := range s.Scopes {
			if scope.ReadOnly && scope.Workspace == i.Workspace && scope.Collection == i.Collection {
				return scope, true // refusal only; no old generation becomes writable
			}
		}
		return Scope{}, false
	}
	if current.ReadOnly {
		return current, true
	}
	if strings.HasPrefix(i.Path, current.Root+"/") {
		return current, true
	}
	root := ""
	if i.Base != nil {
		root = i.Base.CollectionRoot
	}
	if root == "" {
		roots, err := load[layoutRoots](ctx, r, "cursor", layoutRootsKey(current))
		if err != nil {
			return Scope{}, false
		}
		for _, candidate := range roots.Roots {
			if strings.HasPrefix(i.Path, candidate+"/") {
				if root != "" {
					return Scope{}, false
				}
				root = candidate
			}
		}
		if root == "" {
			return Scope{}, false
		}
	}
	old := current
	old.Root = root
	return old, layoutScopeAllowed(ctx, r, old, current)
}

func (s *Service) relocate(ctx context.Context, r Records, scope Scope, mapping FileMapping, file notesworkspace.File, base notesworkspace.Base) (Binding, error) {
	old := mapping.Latest
	if !s.LayoutPublication || old == nil || !(layoutScopeAllowed(ctx, r, mapping.Scope, scope) || observedLayoutScopeAllowed(ctx, r, mapping, scope)) ||
		!old.valid() || old.Deleted || old.Generation != scope.Generation && !scope.ReadOnly && old.Writable && file.EntryKind != "directory" || base.Generation != scope.Generation ||
		file.CollectionID != scope.SourceCollection || old.Path != mapping.Scope.Root+"/"+file.RelativePath ||
		old.SourceBase != "base_"+key(s.Replica, base.ID) && !scope.ReadOnly && old.Writable && file.EntryKind != "directory" || old.SHA256 != base.Hash || !base.Exists {
		return Binding{}, ErrHeld
	}
	// A later generation/base is a different publication, even when it uses
	// the same retained predecessor and destination. Never relabel an old receipt.
	id := layoutPublicationID(s.Replica, old.ID, scope, "base_"+key(s.Replica, base.ID))
	layout, err := loadLayoutPublication(ctx, r, s.Replica, *old, scope, "base_"+key(s.Replica, base.ID))
	if err != nil && !missing(err) {
		return Binding{}, err
	}
	if missing(err) {
		next := *old
		next.ID = "layout_binding_" + key(id)
		if old.EntryKind == "directory" {
			next.ID = "layout_dir_" + key(id)
		}
		if strings.HasSuffix(strings.ToLower(file.RelativePath), ".canvas") {
			next.ID = "layout_canvas_" + key(id)
		}
		next.CollectionRoot, next.Path = scope.Root, scope.Root+"/"+file.RelativePath
		next.Generation, next.SourceBase = scope.Generation, "base_"+key(s.Replica, base.ID)
		next.Writable = !referencePath(file.RelativePath) && !scope.ReadOnly
		if next.EntryKind == "directory" {
			// Cross-generation directory relocation is an observation, not an
			// implicit renewal of write permission. Normal export readmits it.
			next.Writable = old.Writable && !scope.ReadOnly && old.Generation == scope.Generation
		}
		next.PreviousPath = ""
		next.SourceSequence++
		if next.EntryKind == "directory" {
			var e error
			next.SourceSequence, e = directorySequence(ctx, r, next.ID)
			if e != nil {
				return Binding{}, e
			}
			if next.SourceSequence <= old.SourceSequence {
				return Binding{}, ErrHeld
			}
		}
		if next.EntryKind != "directory" {
			var result Evidence
			if err := s.call(ctx, "layout.publish", map[string]any{"operationId": id, "previous": old, "path": next.Path}, &result); err != nil {
				return Binding{}, err
			}
			if result.Status != "published" || "sha256:"+result.SHA256 != old.SHA256 || !revRE.MatchString(result.Revision) {
				if !old.Writable && referencePath(old.Path) {
					return Binding{}, referenceLayoutHold(result.Reason)
				}
				return Binding{}, ErrHeld
			}
			next.NativeRevision = result.Revision
		}
		if !next.valid() {
			return Binding{}, ErrHeld
		}
		layout = Layout{Header: Header{1, "layout", id}, Previous: *old, Binding: next}
		if err := r.Put(ctx, "export", id, layout); err != nil {
			return Binding{}, err
		}
	}
	if layout.Previous != *old || layout.Binding.Path != scope.Root+"/"+file.RelativePath {
		return Binding{}, ErrHeld
	}
	raw, err := marshalControl(layout)
	if err != nil {
		return Binding{}, err
	}
	var result Evidence
	if err := s.call(ctx, "control.put", map[string]any{"contentBase64": raw}, &result); err != nil {
		return Binding{}, err
	}
	if result.Status != "published" {
		return Binding{}, ErrHeld
	}
	next := layout.Binding
	if err := r.Put(ctx, "binding", next.ID, BindingRecord{Binding: next, SourceFileID: file.ID, SourceBaseID: base.ID}); err != nil {
		return Binding{}, err
	}
	mapping.Scope, mapping.Latest = scope, &next
	if err := s.saveMapping(ctx, r, Intent{Workspace: scope.Workspace, Collection: scope.Collection, Generation: scope.Generation, FileID: next.FileID}, mapping); err != nil {
		return Binding{}, err
	}
	if err := s.publishExportBinding(ctx, r, next); err != nil {
		return Binding{}, err
	}
	if next.EntryKind != "directory" {
		// A newly arrived old-path edit can refuse retirement. Its exact old base
		// and intent remain valid; no native branch is discarded to force a move.
		if err := s.call(ctx, "layout.retire", map[string]any{"layout": layout}, &result); err != nil {
			return Binding{}, err
		}
	}
	return next, nil
}

func layoutPublicationID(replica, previous string, scope Scope, sourceBase string) string {
	return "layout_" + key(replica, previous, scope.Root, scope.Generation, sourceBase)
}

func loadLayoutPublication(ctx context.Context, r Records, replica string, previous Binding, scope Scope, sourceBase string) (Layout, error) {
	id := layoutPublicationID(replica, previous.ID, scope, sourceBase)
	layout, err := load[Layout](ctx, r, "export", id)
	if !missing(err) {
		return layout, err
	}
	legacy, err := load[Layout](ctx, r, "export", "layout_"+key(replica, previous.ID, scope.Root))
	if err != nil {
		return Layout{}, err
	}
	if legacy.Previous != previous || legacy.Binding.Generation != scope.Generation || legacy.Binding.SourceBase != sourceBase || legacy.Binding.CollectionRoot != scope.Root {
		return Layout{}, sql.ErrNoRows
	}
	return legacy, nil
}

// Reference layouts reuse the exact persisted export base. WithExport retains
// current source authority; native layout publication reuses existing chunks.
func (s *Service) relocateReference(ctx context.Context, scope Scope, mapping FileMapping) error {
	if err := s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		_, record, err := s.referenceRelocationReceipt(ctx, r, mapping)
		if err != nil {
			return err
		}
		base, err := s.Source.Base(ctx, record.SourceBaseID)
		if err != nil {
			return err
		}
		if base.FileID != record.SourceFileID || !base.Exists || base.Hash != record.Binding.SHA256 ||
			base.ReferenceSize < 0 || base.ReferenceSize > notesworkspace.MaxStreamReferenceBytes {
			return referenceHold("reference_base_evidence_held")
		}
		return nil
	}); err != nil {
		return err
	}
	baseID := ""
	if mapping.Scope.Generation != scope.Generation {
		file, err := s.Source.File(ctx, mapping.SourceFileID)
		if err != nil {
			return err
		}
		if mapping.Latest == nil || file.CollectionID != scope.SourceCollection ||
			mapping.Latest.Path != mapping.Scope.Root+"/"+file.RelativePath || file.Deleted {
			return referenceHold("reference_source_path_held")
		}
		observation, err := s.fileObservation(ctx, file)
		if err != nil {
			return err
		}
		input := referenceAttemptInput{Fingerprint: observation.Fingerprint, Size: observation.Size, Layout: &mapping}
		read, snapshot, err := s.readExportReference(ctx, scope, file.RelativePath, input)
		if snapshot != "" {
			defer os.Remove(snapshot)
		}
		if err != nil {
			return err
		}
		if read.File.ID != mapping.SourceFileID || mapping.Latest == nil || read.Base.Hash != mapping.Latest.SHA256 {
			return referenceHold("reference_content_changed")
		}
		baseID = read.Base.ID
		attempt := referenceLayoutAttempt{Scope: scope, Fingerprint: observation.Fingerprint, Previous: *mapping.Latest,
			BaseID: baseID, OperationID: layoutPublicationID(s.Replica, mapping.Latest.ID, scope, "base_"+key(s.Replica, baseID))}
		if err := s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
			return r.Put(ctx, "cursor", referenceLayoutAttemptKey(scope, mapping), attempt)
		}); err != nil {
			return err
		}
	}
	return s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		current, record, err := s.referenceRelocationReceipt(ctx, r, mapping)
		if err != nil {
			return err
		}
		if baseID == "" {
			baseID = record.SourceBaseID
		}
		return s.Source.WithExport(ctx, record.SourceFileID, baseID, func(file notesworkspace.File, base notesworkspace.Base) error {
			if file.ID != record.SourceFileID || file.Deleted || file.EntryKind != "" || !referencePath(file.RelativePath) ||
				base.ID != baseID || base.FileID != file.ID || base.PathVersion != file.PathVersion || base.Generation != scope.Generation ||
				!base.Exists || base.Hash != record.Binding.SHA256 || !hashRE.MatchString(base.Hash) ||
				base.ReferenceSize < 0 || base.ReferenceSize > notesworkspace.MaxStreamReferenceBytes {
				return referenceHold("reference_base_evidence_held")
			}
			if len(base.Content) > 0 || base.ReferenceSize == 0 {
				if len(base.Content) > MaxReferenceBytes || digest(base.Content) != base.Hash {
					return ErrHeld
				}
			}
			_, err := s.relocate(ctx, r, scope, current, file, base)
			return err
		})
	})
}

func layoutCursorID(scope Scope) string {
	return "layout_scan_v3_" + key(scope.Workspace, scope.Collection, scope.Generation, scope.Root)
}

func layoutHasPredecessor(ctx context.Context, r Records, scope Scope) (bool, error) {
	roots, err := load[layoutRoots](ctx, r, "cursor", layoutRootsKey(scope))
	if missing(err) {
		return false, nil
	}
	current, previous := false, false
	for _, root := range roots.Roots {
		current = current || root == scope.Root
		previous = previous || root != "" && root != scope.Root
	}
	return current && previous, err
}

// These are navigation fingerprints, never publication authority. Only exact
// held identities are read; no source bytes or corpus-wide revision is needed.
func (s *Service) layoutDependency(ctx context.Context, r Records, scope Scope, id string) (FileMapping, string, error) {
	cache := s.dependencyCache()
	version := cache.version()
	var keys []string
	retained := map[string]json.RawMessage{}
	get := func(kind, id string, out any) error {
		keys = append(keys, "record:"+kind+":"+id)
		if raw, ok := retained[kind+":"+id]; ok {
			if raw != nil && out != nil {
				return json.Unmarshal(raw, out)
			}
			return nil
		}
		var raw json.RawMessage
		err := r.Get(ctx, kind, id, &raw)
		if err != nil && !missing(err) {
			return err
		}
		retained[kind+":"+id] = raw
		if err == nil && out != nil {
			return json.Unmarshal(raw, out)
		}
		return nil
	}
	var mapping FileMapping
	if err := get("file", id, &mapping); err != nil {
		return mapping, "", err
	}
	keys = append(keys, "source:"+mapping.SourceFileID)
	file, fileErr := s.Source.File(ctx, mapping.SourceFileID)
	var observation InventoryEntry
	var observationErr error
	if fileErr == nil && file.CollectionID != "" && !file.Deleted {
		observation, observationErr = s.fileObservation(ctx, file)
	}
	var probe layoutProbe
	if fileErr == nil && observationErr == nil && mapping.Latest != nil && !mapping.Latest.Writable &&
		referencePath(file.RelativePath) && mapping.Latest.Path == mapping.Scope.Root+"/"+file.RelativePath && mapping.Scope.Root != scope.Root {
		var e error
		probe, e = s.referenceLayoutProbe(ctx, r, scope, mapping, observation.Fingerprint)
		if e != nil {
			return mapping, "", e
		}
	}
	if file.CollectionID != "" && file.RelativePath != "" {
		keys = append(keys, layoutPathKey(file.CollectionID, file.RelativePath))
	}
	mappings := []FileMapping{mapping}
	for _, lookup := range []Scope{scope, mapping.Scope} {
		var client, pointer string
		if err := get("source_file", sourceKey(lookup, mapping.SourceFileID), &client); err != nil {
			return mapping, "", err
		}
		if client != "" {
			var head FileMapping
			if err := get("file", mapKey(Intent{Workspace: lookup.Workspace, Collection: lookup.Collection, Generation: lookup.Generation, FileID: client}), &head); err != nil {
				return mapping, "", err
			}
			mappings = append(mappings, head)
		}
		if err := get("source_file", mappingLocator(lookup, mapping.SourceFileID), &pointer); err != nil {
			return mapping, "", err
		}
		if pointer != "" {
			var head FileMapping
			if err := get("file", pointer, &head); err != nil {
				return mapping, "", err
			}
			mappings = append(mappings, head)
		}
	}
	for _, current := range mappings {
		if current.Latest == nil {
			continue
		}
		if err := get("binding", current.Latest.ID, nil); err != nil {
			return mapping, "", err
		}
		if err := get("export", "published_"+key(current.Latest.ID), nil); err != nil {
			return mapping, "", err
		}
		if current.Scope.Generation != current.Latest.Generation {
			if err := get("file", mapKey(Intent{Workspace: current.Scope.Workspace, Collection: current.Scope.Collection, Generation: current.Latest.Generation, FileID: current.ClientFileID}), nil); err != nil {
				return mapping, "", err
			}
		}
	}
	raw, err := json.Marshal(struct {
		Scope         Scope
		AdoptExisting bool
		File          notesworkspace.File
		FileState     string
		Observation   InventoryEntry
		ObservedState string
		Native        layoutProbe
		Retained      map[string]json.RawMessage
	}{scope, s.AdoptExisting, file, layoutFailureReason("source", fileErr), observation, layoutFailureReason("source", observationErr), probe, retained})
	fingerprint := digest(raw)
	if err == nil {
		cache.record(layoutDependencyKey(scope, id), keys, fingerprint, version)
	}
	return mapping, fingerprint, err
}

func layoutFailureReason(stage string, err error) string {
	var reference referenceDeliveryHold
	if errors.As(err, &reference) {
		return reference.reason
	}
	switch {
	case err == nil:
		return ""
	case missing(err), errors.Is(err, os.ErrNotExist):
		return stage + "_missing"
	case errors.Is(err, notesworkspace.ErrMembership):
		return stage + "_membership_held"
	case errors.Is(err, notesworkspace.ErrStalePath):
		return stage + "_path_held"
	case errors.Is(err, notesworkspace.ErrUnsafePath), errors.Is(err, os.ErrPermission):
		return stage + "_access_held"
	case errors.Is(err, notesworkspace.ErrReferenceOnly):
		return stage + "_read_only_held"
	case errors.Is(err, notesworkspace.ErrReferenceLimit):
		return stage + "_reference_limit_held"
	case errors.Is(err, ErrHeld):
		return stage + "_evidence_held"
	default:
		return stage + "_unavailable"
	}
}

func (c *layoutCursor) summarize() {
	legacy := !c.RetryKnown && c.Retry
	if !legacy {
		c.RetryAt = time.Time{}
	}
	for _, hold := range c.Holds {
		if c.RetryAt.IsZero() || hold.RetryAt.Before(c.RetryAt) {
			c.RetryAt = hold.RetryAt
		}
	}
	c.Retry = legacy || len(c.Holds) > 0
}

// Integrator readiness contract: true means discovery or a targeted retry is
// runnable; next is only a future deadline. This does not mutate checkpoints.
func (s *Service) layoutReadiness(ctx context.Context, now time.Time) (ready bool, next time.Time, err error) {
	if !s.LayoutPublication {
		return
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		for _, scope := range s.Scopes {
			active, e := layoutHasPredecessor(ctx, r, scope)
			if e != nil {
				return e
			}
			if !active {
				continue
			}
			cursor, e := load[layoutCursor](ctx, r, "cursor", layoutCursorID(scope))
			if e != nil && !missing(e) {
				return e
			}
			if !cursor.Complete || cursor.Retry && !cursor.RetryKnown && !now.Before(cursor.RetryAt) {
				ready = true
			}
			for _, hold := range cursor.Holds {
				needed, _ := s.dependencyCache().needs(layoutDependencyKey(scope, hold.MappingID), hold.Dependency, now, hold.RetryAt)
				ready = ready || needed
			}
			if cursor.Retry && now.Before(cursor.RetryAt) && (next.IsZero() || cursor.RetryAt.Before(next)) {
				next = cursor.RetryAt
			}
		}
		return nil
	})
	return
}

func (s *Service) layoutCandidate(ctx context.Context, r Records, scope Scope, mapping FileMapping) (bool, error) {
	if mapping.Latest == nil || mapping.Scope.Root == scope.Root || !(layoutScopeAllowed(ctx, r, mapping.Scope, scope) || observedLayoutScopeAllowed(ctx, r, mapping, scope)) {
		return false, nil
	}
	// Retained predecessors are not new work once their exact successor is done.
	head, err := exportMapping(ctx, r, scope, mapping.SourceFileID)
	if err != nil && !missing(err) && !errors.Is(err, ErrHeld) {
		return false, err
	}
	if err != nil || head.Latest == nil || head.Scope.Root != scope.Root || head.ClientFileID != mapping.ClientFileID || head.Latest.SourceSequence < mapping.Latest.SourceSequence {
		return true, nil
	}
	if mapping.Latest.EntryKind != "directory" {
		return false, nil
	}
	layout, err := loadLayoutPublication(ctx, r, s.Replica, *mapping.Latest, scope, head.Latest.SourceBase)
	if missing(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	published, err := load[exportPublication](ctx, r, "export", "published_"+key(layout.Binding.ID))
	if err != nil && !missing(err) {
		return false, err
	}
	return err != nil || published != (exportPublication{layout.Binding.ID, layout.Binding.NativeRevision, layout.Binding.SHA256}), nil
}

// Scan discovery once, then retain only failed identities as retry navigation.
// Checkpoint navigation before publishing so crashes cannot lose a held move.
func (s *Service) advanceLayouts(ctx context.Context, limit int) (more bool, moved, held int, err error) {
	s.LayoutRetryAt, s.LayoutInspected = time.Time{}, 0
	s.LayoutMetadataProbes, s.LayoutPressure = 0, ""
	s.LayoutHoldReasons = map[string]int{}
	if !s.LayoutPublication {
		return
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	s.layoutBudget = &referenceBudget{started: now}
	defer func() { s.layoutBudget = nil }()
	remaining := limit
	for _, scope := range s.Scopes {
		cache := s.dependencyCache()
		due, cached, version := cache.beginLayout(scope, s.Native, s.Epoch, s.AdoptExisting, now)
		if !due {
			if next := cached.next(); s.LayoutRetryAt.IsZero() || next.Before(s.LayoutRetryAt) {
				s.LayoutRetryAt = next
			}
			for reason, count := range cached.holdReasons {
				s.LayoutHoldReasons[reason] += count
			}
			continue
		}
		var cursor layoutCursor
		active := false
		checkpoint := false
		selected := map[string]FileMapping{}
		dependencies := map[string]string{}
		err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
			var e error
			active, e = layoutHasPredecessor(ctx, r, scope)
			if e != nil || !active {
				return e
			}
			cursor, e = load[layoutCursor](ctx, r, "cursor", layoutCursorID(scope))
			if e != nil && !missing(e) {
				return e
			}
			if !cursor.RetryKnown {
				if !cursor.Retry {
					cursor.RetryKnown = true
					checkpoint = true
				} else if cursor.Complete && !now.Before(cursor.RetryAt) {
					// Old v3 held only a boolean: one resumable catch-up discovers IDs.
					cursor.After, cursor.Complete, cursor.RetryKnown = "", false, true
					checkpoint = true
				}
			}
			if !cursor.Complete && remaining > 0 {
				checkpoint = true
				var ids []string
				if scoped, ok := r.(collectionFileRecords); ok {
					ids, e = scoped.ListCollectionFiles(ctx, scope.Collection, cursor.After, remaining)
				} else {
					ids, e = r.List(ctx, "file", cursor.After, remaining)
				}
				if e != nil {
					return e
				}
				cursor.Complete = len(ids) < remaining
				remaining -= len(ids)
				for _, id := range ids {
					mapping, e := load[FileMapping](ctx, r, "file", id)
					if e != nil {
						return e
					}
					s.LayoutInspected++
					candidate, e := s.layoutCandidate(ctx, r, scope, mapping)
					if e != nil {
						return e
					}
					if candidate {
						selected[id] = mapping
						found := false
						for _, hold := range cursor.Holds {
							found = found || hold.MappingID == id
						}
						if !found {
							cursor.Holds = append(cursor.Holds, layoutHold{MappingID: id, Reason: "layout_pending", RetryAt: now})
						}
					}
					cursor.After = id
				}
			}
			more = more || !cursor.Complete
			for index, hold := range cursor.Holds {
				needed, _ := s.dependencyCache().needs(layoutDependencyKey(scope, hold.MappingID), hold.Dependency, now, hold.RetryAt)
				_, already := selected[hold.MappingID]
				if !needed && !already {
					continue
				}
				if !already && remaining <= 0 {
					more = true
					continue
				}
				if !already {
					remaining--
				}
				mapping, dependency, e := s.layoutDependency(ctx, r, scope, hold.MappingID)
				if e != nil {
					return e
				}
				s.LayoutMetadataProbes++
				if len(hold.Dependencies) == 0 {
					cursor.Holds[index].Dependencies = s.dependencyCache().keys(layoutDependencyKey(scope, hold.MappingID))
					checkpoint = true
				}
				if already {
					dependencies[hold.MappingID] = dependency
					continue
				}
				s.LayoutInspected++
				if dependency == hold.Dependency && !strings.HasSuffix(hold.Reason, "_unavailable") {
					// Expiry/cache loss permits an evidence probe, not a fresh read
					// of an unchanged rejected payload. Keep the attempt history.
					if !now.Before(hold.RetryAt) {
						cursor.Holds[index].RetryAt = now.Add(layoutEvidenceRetry)
						checkpoint = true
					}
					s.dependencyCache().settled(layoutDependencyKey(scope, hold.MappingID))
					continue
				}
				selected[hold.MappingID], dependencies[hold.MappingID] = mapping, dependency
			}
			cursor.summarize()
			if !checkpoint && len(selected) == 0 {
				return nil
			}
			return r.Put(ctx, "cursor", layoutCursorID(scope), cursor)
		})
		if err != nil {
			return
		}
		var actionErr error
		for i := 0; i < len(cursor.Holds); {
			hold := &cursor.Holds[i]
			mapping, ok := selected[hold.MappingID]
			if !ok {
				i++
				continue
			}
			started, readBytes, readAttempts := time.Now(), s.ReferenceReadBytes, s.ReferenceReadAttempts
			reason, e := s.advanceLayout(ctx, scope, mapping)
			if len(s.LayoutAttempts) < 8 {
				s.LayoutAttempts = append(s.LayoutAttempts, LayoutAttemptDetail{Operation: "layout",
					EvidenceKey: key(scope.Collection, hold.MappingID, dependencies[hold.MappingID]), Reason: reason,
					ReadBytes: s.ReferenceReadBytes - readBytes, ReadAttempts: s.ReferenceReadAttempts - readAttempts,
					ElapsedMS: time.Since(started).Milliseconds()})
			}
			if errors.Is(e, errLayoutBudget) {
				more = true
				i++
				continue // Still pending; no failed attempt or sliding deadline.
			}
			if e == nil {
				if reason != "layout_already_current" {
					moved++
				}
				s.dependencyCache().forget(layoutDependencyKey(scope, hold.MappingID))
				cursor.Holds = append(cursor.Holds[:i], cursor.Holds[i+1:]...)
			} else {
				held++
				if hold.Reason != reason || hold.Dependency != dependencies[hold.MappingID] {
					hold.Attempts = 0
				}
				hold.Reason, hold.Dependency = reason, dependencies[hold.MappingID]
				hold.Attempts++
				delay := layoutEvidenceRetry
				if strings.HasSuffix(reason, "_unavailable") {
					delay = time.Minute << min(hold.Attempts-1, 2)
				}
				hold.RetryAt = now.Add(delay)
				s.dependencyCache().settled(layoutDependencyKey(scope, hold.MappingID))
				i++
				if reason == "layout_unavailable" || reason == "mapping_unavailable" {
					actionErr = e
					break
				}
			}
		}
		cursor.summarize()
		if len(selected) > 0 {
			err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error { return r.Put(ctx, "cursor", layoutCursorID(scope), cursor) })
			if err != nil {
				return
			}
		}
		for _, hold := range cursor.Holds {
			s.LayoutHoldReasons[hold.Reason]++
		}
		if cursor.Retry && (s.LayoutRetryAt.IsZero() || cursor.RetryAt.Before(s.LayoutRetryAt)) {
			s.LayoutRetryAt = cursor.RetryAt
		}
		if actionErr != nil {
			err = actionErr
			return
		}
		if active {
			cache.finishLayout(scope, s.Native, s.Epoch, s.AdoptExisting, cursor, now, version)
			if repairAt := now.Add(layoutEvidenceRetry); s.LayoutRetryAt.IsZero() || repairAt.Before(s.LayoutRetryAt) {
				s.LayoutRetryAt = repairAt
			}
		}
	}
	return
}

func (s *Service) advanceLayout(ctx context.Context, scope Scope, mapping FileMapping) (string, error) {
	if mapping.Latest == nil {
		return "mapping_missing", ErrHeld
	}
	allowed := false
	alreadyCurrent := false
	reason := "mapping_unavailable"
	if err := s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		if _, err := recoverRetainedLayoutMapping(ctx, r, mapping); err != nil {
			if errors.Is(err, ErrHeld) {
				reason = "mapping_origin_unverified"
				originID := mapKey(Intent{Workspace: mapping.Scope.Workspace, Collection: mapping.Scope.Collection, Generation: mapping.Latest.Generation, FileID: mapping.ClientFileID})
				if _, e := load[FileMapping](ctx, r, "file", originID); missing(e) {
					reason = "mapping_origin_missing"
				} else if _, e := load[BindingRecord](ctx, r, "binding", mapping.Latest.ID); missing(e) {
					reason = "mapping_origin_receipt_missing"
				}
			}
			return err
		}
		if !mapping.Latest.Writable && referencePath(mapping.Latest.Path) {
			head, err := s.outgoingExportMapping(ctx, r, scope, mapping.SourceFileID)
			if err != nil && !missing(err) && !errors.Is(err, errMappingIndexPending) {
				return err
			}
			if err == nil && head.Latest != nil && head.ClientFileID == mapping.ClientFileID &&
				head.Latest.SourceSequence >= mapping.Latest.SourceSequence {
				// Content catch-up may have advanced this predecessor. Move that
				// exact current binding, never re-compare new bytes with the old one.
				mapping = head
				alreadyCurrent = head.Scope.Root == scope.Root
			}
		}
		allowed = layoutScopeAllowed(ctx, r, mapping.Scope, scope) || observedLayoutScopeAllowed(ctx, r, mapping, scope)
		return nil
	}); err != nil {
		return reason, err
	}
	if alreadyCurrent {
		return "layout_already_current", nil
	}
	if !allowed {
		return "mapping_scope_held", ErrHeld
	}
	file, err := s.Source.File(ctx, mapping.SourceFileID)
	if err != nil {
		return layoutFailureReason("source", err), err
	}
	if file.Deleted {
		return "source_deleted", ErrHeld
	}
	if file.EntryKind == "" && referencePath(file.RelativePath) && !mapping.Latest.Writable {
		err := s.relocateReference(ctx, scope, mapping)
		return layoutFailureReason("reference", err), err
	}
	var read notesworkspace.ReadResult
	if file.EntryKind == "directory" {
		directory, ok := s.Source.(directorySource)
		if !ok {
			return "directory_source_held", ErrHeld
		}
		read, err = directory.ReadDirectory(ctx, file.CollectionID, file.RelativePath)
	} else {
		read, err = s.Source.Read(ctx, file.CollectionID, file.RelativePath)
	}
	if err != nil {
		return layoutFailureReason("source_read", err), err
	}
	if file.EntryKind == "directory" {
		err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
			current, err := load[FileMapping](ctx, r, "file", mapKey(Intent{Workspace: mapping.Scope.Workspace, Collection: mapping.Scope.Collection, Generation: mapping.Scope.Generation, FileID: mapping.ClientFileID}))
			if err != nil {
				return err
			}
			if current.Latest == nil || *current.Latest != *mapping.Latest || current.SourceFileID != read.File.ID {
				return ErrHeld
			}
			return s.Source.WithExport(ctx, read.File.ID, read.Base.ID, func(file notesworkspace.File, base notesworkspace.Base) error {
				_, err := s.relocate(ctx, r, scope, current, file, base)
				return err
			})
		})
	} else {
		var binding Binding
		binding, err = s.Export(ctx, scope.Collection, read.File.ID, read.Base.ID)
		if err == nil && binding.CollectionRoot != scope.Root {
			_, err = s.Export(ctx, scope.Collection, read.File.ID, read.Base.ID)
		}
	}
	return layoutFailureReason("layout", err), err
}
