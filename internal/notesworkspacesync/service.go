package notesworkspacesync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"loom.local/loom/internal/notesworkspace"
)

// Scopes come from the runtime's existing enrollment owner, never a client control.
// Generation is the exact source resolver generation, not a client capability.
type Scope struct {
	Workspace, Collection, SourceCollection, Generation, Root string
	// Operator-selected exact predecessor. It authorizes export rebinding only,
	// never incoming writes under a withdrawn generation.
	PreviousGeneration string `json:"previous_generation,omitempty"`
}
type Service struct {
	Replica, Epoch string
	Scopes         []Scope
	Store          Store
	Source         Source
	Native         Native
}
type BindingRecord struct {
	Binding                    Binding
	SourceFileID, SourceBaseID string
}
type FileMapping struct {
	Scope                                    Scope
	ClientFileID, SourceFileID, CreateBaseID string
	Latest                                   *Binding
}
type Join struct {
	IntentRaw, PublicationRaw  string
	Status, Reason             string
	SourceFileID, SourceBaseID string
	ResultBaseID               string
	ResultFile                 notesworkspace.File
	Ack                        *Ack
	AckPublished               bool
	// ResolutionID links a reviewed attempt without changing the original Ack.
	ResolutionID   string `json:"ResolutionID,omitempty"`
	OperatorReview string `json:"OperatorReview,omitempty"`
}
type Observation struct {
	Evidence Evidence
	Reason   string
}
type Progress struct {
	Imported, Processed int
	NextSequence        json.RawMessage
	NextOperation       string
	Held                int
}
type Cursor struct {
	Sequence  json.RawMessage
	Operation string
	Retry     string
}

func (s *Service) scope(i Intent) (Scope, bool) {
	for _, v := range s.Scopes {
		if v.Workspace == i.Workspace && v.Collection == i.Collection && v.Generation == i.Generation {
			return v, true
		}
	}
	return Scope{}, false
}
func (v Scope) relative(p string) (string, bool) {
	if !textPath(v.Root+"/x.md") || !textPath(p) || !strings.HasPrefix(p, v.Root+"/") {
		return "", false
	}
	r := strings.TrimPrefix(p, v.Root+"/")
	return r, textPath(r)
}
func mapKey(i Intent) string              { return key(i.Workspace, i.Collection, i.Generation, i.FileID) }
func sourceKey(v Scope, id string) string { return key(v.Workspace, v.Collection, v.Generation, id) }
func (s *Service) valid() error {
	if !token(s.Replica) || s.Epoch == "" || s.Store == nil || s.Source == nil || s.Native == nil {
		return ErrHeld
	}
	seen := map[string]bool{}
	for _, v := range s.Scopes {
		k := key(v.Workspace, v.Collection, v.Generation)
		if seen[k] || !token(v.Workspace) || !token(v.Collection) || !token(v.Generation) || v.SourceCollection == "" || !textPath(v.Root+"/x.md") || v.PreviousGeneration != "" && !token(v.PreviousGeneration) {
			return ErrHeld
		}
		seen[k] = true
	}
	return nil
}
func load[T any](ctx context.Context, r Records, kind, id string) (v T, err error) {
	err = r.Get(ctx, kind, id, &v)
	return
}
func missing(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// Step imports one bounded native page, retries one bounded evidence page, then
// joins one bounded operation page. Cursors advance only after custody commits.
// Repeated sweeps revisit held/out-of-order records without starving later IDs.
func (s *Service) Step(ctx context.Context, limit int) (progress Progress, err error) {
	if err = s.valid(); err != nil {
		return
	}
	if limit < 1 || limit > 128 {
		return progress, ErrHeld
	}
	if err = s.ready(ctx); err != nil {
		return
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		cursor, e := load[Cursor](ctx, r, "cursor", "native")
		if e != nil && !missing(e) {
			return e
		}
		if len(cursor.Sequence) == 0 {
			cursor.Sequence = json.RawMessage("0")
		}
		var page NativeChanges
		if e = s.call(ctx, "changes", map[string]any{"since": cursor.Sequence, "limit": limit}, &page); e != nil {
			return e
		}
		if page.Status != "available" || !json.Valid(page.Next) || len(page.Changes) > limit {
			return ErrHeld
		}
		fresh := make([]string, 0, limit)
		for _, change := range page.Changes {
			if len(change.Leaves) > 64 {
				return ErrHeld
			}
			for _, leaf := range change.Leaves {
				id := key(leaf.ID, leaf.Revision)
				if leaf.Kind == "system" {
					continue
				}
				leaf.Content = nil // observations retain references, never a second payload queue
				observation := Observation{Evidence: leaf, Reason: "missing_client_intent"}
				if strings.HasPrefix(leaf.Path, Prefix) {
					observation.Reason = "control_pending"
				}
				if old, e := load[Observation](ctx, r, "observation", id); e == nil && old.Reason == "consumed" {
					continue
				} else if e != nil && !missing(e) {
					return e
				}
				if e = r.Put(ctx, "observation", id, observation); e != nil {
					return e
				}
				if observation.Reason == "control_pending" && len(fresh) < limit {
					fresh = append(fresh, id)
				}
			}
		}
		cursor.Sequence = page.Next
		ids, e := r.ListPending(ctx, "observation", cursor.Retry, limit)
		if e != nil {
			return e
		}
		// Arrivals need not wait for a historical retry cursor to wrap around.
		seen := map[string]bool{}
		for _, id := range append(fresh, ids...) {
			if seen[id] {
				continue
			}
			seen[id] = true
			ob, e := load[Observation](ctx, r, "observation", id)
			if e != nil {
				return e
			}
			if ob.Reason == "control_pending" {
				reason, e := s.importControl(ctx, r, ob.Evidence)
				if e != nil {
					return e
				}
				ob.Reason = reason
				if e = r.Put(ctx, "observation", id, ob); e != nil {
					return e
				}
				if reason == "consumed" {
					progress.Imported++
				}
			}
		}
		if len(ids) < limit {
			cursor.Retry = ""
		} else {
			cursor.Retry = ids[len(ids)-1]
		}
		ops, e := r.ListPending(ctx, "operation", cursor.Operation, limit)
		if e != nil {
			return e
		}
		visiting, done := map[string]bool{}, map[string]bool{}
		budget := 128
		var visit func(string) error
		visit = func(id string) error {
			if done[id] || visiting[id] || budget == 0 {
				return nil
			}
			budget--
			visiting[id] = true
			j, e := load[Join](ctx, r, "operation", id)
			if missing(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if i, e := parseIntent([]byte(j.IntentRaw)); e == nil && i.Predecessor != "" && j.Ack == nil {
				if e := visit(i.Predecessor); e != nil {
					return e
				}
			}
			if e = s.process(ctx, r, id, &j); e != nil {
				return e
			}
			if e = r.Put(ctx, "operation", id, j); e != nil {
				return e
			}
			progress.Processed++
			if j.Status == "held" {
				progress.Held++
			}
			done[id] = true
			delete(visiting, id)
			return nil
		}
		for _, id := range ops {
			if e := visit(id); e != nil {
				return e
			}
			cursor.Operation = id
		}
		if len(ops) < limit {
			cursor.Operation = ""
		}
		if e = r.Put(ctx, "cursor", "native", cursor); e != nil {
			return e
		}
		progress.NextSequence = cursor.Sequence
		progress.NextOperation = cursor.Operation
		return nil
	})
	return
}
func (s *Service) importControl(ctx context.Context, r Records, leaf Evidence) (string, error) {
	// Payloads are independently verified when paired with an explicit intent.
	if strings.HasPrefix(leaf.Path, Prefix+"payload/") {
		return "consumed", nil
	}
	var exact Evidence
	if e := s.call(ctx, "control.read", map[string]any{"id": leaf.ID, "revision": leaf.Revision}, &exact); e != nil {
		return "", e
	}
	if exact.Status != "available" {
		if exact.Reason == "control_invalid" || exact.Reason == "control_collision" {
			return exact.Reason, nil
		}
		return "control_pending", nil
	}
	if exact.ID != leaf.ID || exact.Revision != leaf.Revision || exact.Path != leaf.Path || len(exact.Content) > MaxControlBytes || digest(exact.Content) != "sha256:"+exact.SHA256 {
		return "control_invalid", nil
	}
	var h Header
	if json.Unmarshal(exact.Content, &h) != nil || h.Version != 1 || !token(h.ID) || controlPath(h) != leaf.Path {
		return "control_invalid", nil
	}
	var intentID string
	switch h.Kind {
	case "intent":
		i, e := parseIntent(exact.Content)
		if e != nil {
			return "control_invalid", nil
		}
		intentID = i.ID
	case "publication":
		p, e := parsePublication(exact.Content)
		if e != nil {
			return "control_invalid", nil
		}
		intentID = p.IntentID
	// Inbound bindings/acks are never authoritative for the server mapping.
	case "binding", "ack":
		return "consumed", nil
	default:
		return "control_invalid", nil
	}
	j, e := load[Join](ctx, r, "operation", intentID)
	if e != nil && !missing(e) {
		return "", e
	}
	old := j.IntentRaw
	if h.Kind == "publication" {
		old = j.PublicationRaw
	}
	if old != "" && old != string(exact.Content) {
		j.Status = "held"
		j.Reason = "control_collision"
	} else if h.Kind == "intent" {
		j.IntentRaw = string(exact.Content)
	} else {
		j.PublicationRaw = string(exact.Content)
	}
	if e = r.Put(ctx, "operation", intentID, j); e != nil {
		return "", e
	}
	return "consumed", nil
}
func holdJoin(j *Join, reason string) { j.Status = "held"; j.Reason = reason }
func (s *Service) process(ctx context.Context, r Records, id string, j *Join) error {
	if j.Reason == "control_collision" {
		return nil
	}
	if j.Ack != nil {
		return s.publishAck(ctx, j)
	}
	if j.OperatorReview != "" && j.PublicationRaw == "" {
		if err := s.publishOperatorResolution(ctx, r, id, j); err != nil {
			holdJoin(j, "operator_publication_pending")
			return nil
		}
	}
	if j.IntentRaw == "" || j.PublicationRaw == "" {
		holdJoin(j, "waiting_for_intent_or_publication")
		return nil
	}
	i, e := parseIntent([]byte(j.IntentRaw))
	if e != nil {
		holdJoin(j, "invalid_intent")
		return nil
	}
	p, e := parsePublication([]byte(j.PublicationRaw))
	if e != nil || !paired(i, p) || p.IntentDigest != digest([]byte(j.IntentRaw)) {
		holdJoin(j, "publication_pair_mismatch")
		return nil
	}
	scope, ok := s.scope(i)
	if !ok {
		holdJoin(j, "scope_unavailable")
		return nil
	}
	relative, ok := scope.relative(i.Path)
	if !ok {
		holdJoin(j, "outside_collection")
		return nil
	}
	if i.Target != "" {
		if _, ok = scope.relative(i.Target); !ok {
			holdJoin(j, "outside_collection")
			return nil
		}
	}
	var parent string
	var base notesworkspace.Base
	if i.Predecessor != "" {
		previous, e := load[Join](ctx, r, "operation", i.Predecessor)
		if missing(e) {
			holdJoin(j, "waiting_for_predecessor")
			return nil
		}
		if e != nil {
			return e
		}
		pi, e := parseIntent([]byte(previous.IntentRaw))
		pp, pe := parsePublication([]byte(previous.PublicationRaw))
		expectedPath := pi.Path
		if pi.Operation == "rename" {
			expectedPath = pi.Target
		}
		if e != nil || pe != nil || mapKey(pi) != mapKey(i) || pi.Operation == "delete" || !sameBase(i.Base, pi.Base) || expectedPath != i.Path || previous.Status != "applied" {
			holdJoin(j, "predecessor_not_applied")
			return nil
		}
		j.SourceFileID = previous.SourceFileID
		j.SourceBaseID = previous.ResultBaseID
		parent = findRevision(pp, i.Path, "content")
		// The predecessor's source result is exact durable evidence, not a refresh.
	} else if i.Operation != "create" {
		if i.Base == nil {
			holdJoin(j, "missing_source_binding")
			return nil
		}
		binding, e := load[BindingRecord](ctx, r, "binding", i.Base.ID)
		if missing(e) {
			holdJoin(j, "unknown_source_binding")
			return nil
		}
		if e != nil {
			return e
		}
		if binding.Binding != *i.Base || i.Base.Path != i.Path {
			holdJoin(j, "binding_mismatch")
			return nil
		}
		j.SourceFileID = binding.SourceFileID
		j.SourceBaseID = binding.SourceBaseID
		parent = i.Base.NativeRevision
	}
	// Verify exact native payload/tombstone and direct parent, even for a losing
	// native branch. No current native winner or source read supplies ancestry.
	var payload []byte
	var retained Evidence
	if e = s.call(ctx, "payload.read", map[string]any{"intentId": i.ID}, &retained); e != nil {
		return e
	}
	retainedOK := retained.Status == "available" && retained.Path == Prefix+"payload/"+i.ID+".md" &&
		len(retained.Content) == i.Length && digest(retained.Content) == i.SHA256 &&
		digest(retained.Content) == "sha256:"+retained.SHA256 && utf8Text(retained.Content)
	if retained.Status == "available" && !retainedOK || retained.Reason == "payload_collision" {
		holdJoin(j, "retained_payload_mismatch")
		return nil
	}
	for _, revision := range p.Revisions {
		var exact Evidence
		if e = s.call(ctx, "read.path", map[string]any{"path": revision.Path, "revision": revision.Revision}, &exact); e != nil {
			return e
		}
		if exact.Revision != revision.Revision || exact.Path != revision.Path || exact.Kind != "file" || exact.BranchDeleted || !exact.AncestryAvailable {
			holdJoin(j, "native_revision_unavailable")
			return nil
		}
		if exact.Status == "history" && retainedOK {
			payload = retained.Content
		} else if revision.Role == "deletion" {
			if !exact.LogicalDeleted {
				holdJoin(j, "native_delete_mismatch")
				return nil
			}
		} else {
			if exact.Status != "available" || exact.LogicalDeleted || len(exact.Content) != i.Length || digest(exact.Content) != i.SHA256 || digest(exact.Content) != "sha256:"+exact.SHA256 || !utf8Text(exact.Content) {
				holdJoin(j, "native_payload_mismatch")
				return nil
			}
			payload = exact.Content
		}
		root := revision.Role == "content" && (i.Operation == "create" || i.Operation == "rename")
		if root {
			if !exact.AncestryCompleteToRoot || len(exact.Ancestors) != 0 || !strings.HasPrefix(exact.Revision, "1-") {
				holdJoin(j, "native_root_required")
				return nil
			}
		} else if parent == "" || len(exact.Ancestors) == 0 || exact.Ancestors[0] != parent {
			holdJoin(j, "native_parent_mismatch")
			return nil
		}
	}
	if i.Operation == "create" {
		mapping, e := load[FileMapping](ctx, r, "file", mapKey(i))
		if e != nil && !missing(e) {
			return e
		}
		if missing(e) {
			read, e := s.Source.Read(ctx, scope.SourceCollection, relative)
			if e != nil {
				holdJoin(j, "source_create_unavailable")
				return nil
			}
			if read.Base.Exists || read.Base.Generation != i.Generation || read.File.CollectionID != scope.SourceCollection || read.File.RelativePath != relative {
				holdJoin(j, "source_create_requires_absence")
				return nil
			}
			mapping = FileMapping{Scope: scope, ClientFileID: i.FileID, SourceFileID: read.File.ID, CreateBaseID: read.Base.ID}
			if e = s.saveMapping(ctx, r, i, mapping); e != nil {
				if errors.Is(e, ErrHeld) {
					holdJoin(j, "source_identity_collision")
					return nil
				}
				return e
			}
		}
		j.SourceFileID = mapping.SourceFileID
		j.SourceBaseID = mapping.CreateBaseID
	}
	mapping, e := load[FileMapping](ctx, r, "file", mapKey(i))
	if missing(e) {
		holdJoin(j, "unknown_file_mapping")
		return nil
	}
	if e != nil {
		return e
	}
	if mapping.Scope != scope || mapping.SourceFileID != j.SourceFileID {
		holdJoin(j, "file_mapping_mismatch")
		return nil
	}
	base, e = s.Source.Base(ctx, j.SourceBaseID)
	if e != nil {
		holdJoin(j, "source_base_unavailable")
		return nil
	}
	if base.FileID != j.SourceFileID || base.Generation != i.Generation || i.Operation == "create" && base.Exists || i.Operation != "create" && !base.Exists {
		holdJoin(j, "source_base_mismatch")
		return nil
	}
	if reason, err := s.claimResolutions(ctx, r, i, *j); err != nil {
		return err
	} else if reason != "" {
		holdJoin(j, reason)
		return nil
	}
	// Persist the exact source request identity before any Source call. Source
	// Receive commits bytes, then Apply returns its durable outcome/replay.
	j.Status = "pending"
	j.Reason = ""
	if e = r.Put(ctx, "operation", id, *j); e != nil {
		return e
	}
	request := SourceRequest{OperationID: "sync_" + key(s.Replica, i.ID), FileID: j.SourceFileID, Generation: i.Generation, BaseID: j.SourceBaseID, Kind: i.Operation, Content: payload}
	if i.Operation == "delete" || i.Operation == "rename" {
		request.Content = nil
	}
	if i.Operation == "rename" {
		request.DestinationPath, _ = scope.relative(i.Target)
	}
	outcome, e := s.Source.Save(ctx, request)
	if e != nil {
		holdJoin(j, "source_operation_unavailable")
		return nil
	}
	if i.Operation == "rename" && outcome.State == notesworkspace.Accepted && i.SHA256 != base.Hash {
		// Rename+changed content is two durable source operations, not atomic.
		request.Kind = "edit"
		request.OperationID += "_content"
		request.DestinationPath = ""
		request.BaseID = outcome.ResultBaseID
		request.Content = payload
		outcome, e = s.Source.Save(ctx, request)
		if e != nil {
			holdJoin(j, "rename_content_pending")
			return nil
		}
	}
	if outcome.State != notesworkspace.Accepted && outcome.State != notesworkspace.Conflict {
		holdJoin(j, "source_outcome_held")
		return nil
	}
	ack := Ack{Header: Header{1, "ack", "ack_" + key(s.Replica, i.ID)}, IntentID: i.ID, IntentDigest: p.IntentDigest, PublicationID: p.ID, Status: "conflict", Reason: "source_conflict"}
	j.Status = "conflict"
	j.ResultBaseID = outcome.ResultBaseID
	j.ResultFile = outcome.ResultFile
	if outcome.State == notesworkspace.Accepted {
		result, e := s.Source.Base(ctx, outcome.ResultBaseID)
		if e != nil {
			return e
		}
		if result.FileID != j.SourceFileID || result.Generation != i.Generation || result.Exists != (i.Operation != "delete") || i.Operation != "delete" && result.Hash != i.SHA256 {
			return fmt.Errorf("%w: invalid_source_outcome", ErrHeld)
		}
		ack.Status = "applied"
		ack.Reason = "source_applied"
		ack.Resolves = append([]string(nil), i.Resolves...)
		j.Status = "applied"
		if i.Operation != "delete" {
			target := i.Path
			if i.Operation == "rename" {
				target = i.Target
			}
			binding := Binding{Header: Header{1, "binding", "binding_" + key(s.Replica, i.ID)}, Workspace: i.Workspace, Collection: i.Collection, Generation: i.Generation, FileID: i.FileID, CollectionRoot: scope.Root, Path: target, SourceBase: "base_" + key(s.Replica, result.ID), NativeRevision: findRevision(p, target, "content"), SHA256: i.SHA256, Writable: true}
			binding.SourceSequence = 1
			if mapping.Latest != nil {
				binding.SourceSequence = mapping.Latest.SourceSequence + 1
				if mapping.Latest.ID == binding.ID {
					binding.SourceSequence = mapping.Latest.SourceSequence
				}
			}
			if !binding.valid() {
				return ErrHeld
			}
			record := BindingRecord{Binding: binding, SourceFileID: j.SourceFileID, SourceBaseID: result.ID}
			if e = r.Put(ctx, "binding", binding.ID, record); e != nil {
				return e
			}
			mapping.Latest = &binding
			if e = r.Put(ctx, "file", mapKey(i), mapping); e != nil {
				return e
			}
			ack.Binding = &binding
		}
	}
	j.Ack = &ack
	j.Reason = ""
	if e = r.Put(ctx, "operation", id, *j); e != nil {
		return e
	}
	return s.publishAck(ctx, j)
}
func utf8Text(b []byte) bool { return len(b) <= MaxBytes && !bytes.ContainsRune(b, 0) && utf8.Valid(b) }
func sameBase(a, b *Binding) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func (s *Service) saveMapping(ctx context.Context, r Records, i Intent, m FileMapping) error {
	reverse := sourceKey(m.Scope, m.SourceFileID)
	old, e := load[string](ctx, r, "source_file", reverse)
	if e != nil && !missing(e) {
		return e
	}
	if e == nil && old != i.FileID {
		return ErrHeld
	}
	if e = r.Put(ctx, "source_file", reverse, i.FileID); e != nil {
		return e
	}
	return r.Put(ctx, "file", mapKey(i), m)
}
func (s *Service) publishAck(ctx context.Context, j *Join) error {
	if j.AckPublished {
		return nil
	}
	// Every enrolled client needs the accepted binding, not only the author
	// consuming its own ack. Publish now, not on a later source-export sweep.
	if j.Ack != nil && j.Ack.Binding != nil {
		if err := s.publishBinding(ctx, *j.Ack.Binding); err != nil {
			return err
		}
	}
	raw, e := json.Marshal(j.Ack)
	if e != nil {
		return e
	}
	var result Evidence
	if e = s.call(ctx, "control.put", map[string]any{"contentBase64": raw}, &result); e != nil {
		return e
	}
	if result.Status != "published" {
		holdJoin(j, "ack_publication_pending")
		return nil
	}
	j.AckPublished = true
	j.Status = j.Ack.Status
	j.Reason = ""
	return nil
}
