package notesworkspacesync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"loom.local/loom/internal/workers"
)

const sourceMovePrefix = "source_move_request_"

func movableSourceBinding(b Binding) bool {
	return !b.Deleted && b.EntryKind == "" &&
		(b.Writable && textPath(b.Path) || !b.Writable && referencePath(b.Path))
}

type moveHashIndex struct {
	Bindings  []BindingRecord
	Ambiguous bool
}

func rememberMoveBinding(ctx context.Context, records Records, current BindingRecord) ([]BindingRecord, error) {
	b := current.Binding
	if !movableSourceBinding(b) {
		return nil, nil
	}
	id := "move_hash_v2_" + key(b.Workspace, b.SHA256, strconv.FormatBool(referencePath(b.Path)))
	index, err := load[moveHashIndex](ctx, records, "source_file", id)
	if err != nil && !missing(err) {
		return nil, err
	}
	if index.Ambiguous {
		return nil, nil
	}
	candidates := append([]BindingRecord(nil), index.Bindings...)
	for n, old := range index.Bindings {
		if old.Binding.Collection == b.Collection && old.Binding.FileID == b.FileID && old.Binding.Path == b.Path {
			if old.Binding.SourceSequence > b.SourceSequence {
				return nil, nil
			}
			index.Bindings[n] = current
			return candidates, records.Put(ctx, "source_file", id, index)
		}
	}
	if len(index.Bindings) >= 8 {
		index.Ambiguous = true
		return nil, records.Put(ctx, "source_file", id, index)
	}
	index.Bindings = append(index.Bindings, current)
	return candidates, records.Put(ctx, "source_file", id, index)
}

// One bounded metadata backfill covers pre-upgrade bindings. Completion is
// retained; idle turns do not rescan files or historical mappings.
func (s *Service) indexMoveBindings(ctx context.Context, limit int) (more bool, err error) {
	if !s.LayoutPublication {
		return
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(records Records) error {
		cursor, err := load[mappingIndexCursor](ctx, records, "cursor", "source-move-binding-index-v2")
		if err != nil && !missing(err) {
			return err
		}
		if cursor.Complete {
			return nil
		}
		ids, err := records.List(ctx, "file", cursor.After, limit)
		if err != nil {
			return err
		}
		for _, id := range ids {
			mapping, err := load[FileMapping](ctx, records, "file", id)
			if err != nil {
				return err
			}
			if mapping.Latest != nil {
				record, err := load[BindingRecord](ctx, records, "binding", mapping.Latest.ID)
				if err != nil {
					return err
				}
				if _, err := rememberMoveBinding(ctx, records, record); err != nil {
					return err
				}
			}
			cursor.After = id
		}
		cursor.Complete = len(ids) < limit
		more = !cursor.Complete
		return records.Put(ctx, "cursor", "source-move-binding-index-v2", cursor)
	})
	return
}

// Index only bindings already exported by the normal source owner. Matching is
// bounded and never scans the corpus or treats an identical live copy as a move.
func (s *Service) observeMoveExport(ctx context.Context, binding Binding) error {
	if !s.LayoutPublication || !movableSourceBinding(binding) {
		return nil
	}
	var candidates []BindingRecord
	err := s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(records Records) error {
		current, err := load[BindingRecord](ctx, records, "binding", binding.ID)
		if err != nil {
			return err
		}
		candidates, err = rememberMoveBinding(ctx, records, current)
		return err
	})
	if err != nil {
		return err
	}
	var matching *sourceMoveRecord
	for _, old := range candidates {
		if old.Binding.Path == binding.Path {
			continue
		}
		var destination BindingRecord
		var retired Layout
		err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(records Records) error {
			var retirementErr error
			retired, retirementErr = load[Layout](ctx, records, "export", sourceMoveRetirement(old.Binding))
			if retirementErr != nil && !missing(retirementErr) {
				return retirementErr
			}
			var e error
			destination, e = load[BindingRecord](ctx, records, "binding", binding.ID)
			return e
		})
		if err != nil {
			return err
		}
		if retired.SourceMove && (old.Binding.SourceSequence <= retired.Previous.SourceSequence) {
			continue
		}
		view, err := s.sourceMoveView(ctx, old, destination)
		if err != nil {
			continue
		}
		if matching != nil {
			return nil
		} // Multiple absent predecessors require review.
		matching = &sourceMoveRecord{View: view, Previous: old, Destination: destination}
	}
	if matching == nil {
		return nil
	}
	return s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(records Records) error {
		id := sourceMovePrefix + key(matching.View.ID)
		if _, err := load[sourceMoveRecord](ctx, records, "export", id); err == nil {
			return nil
		} else if !missing(err) {
			return err
		}
		if err := records.Put(ctx, "export", id, *matching); err != nil {
			return err
		}
		s.SourceMovesQueued = true
		return nil
	})
}

type SourceMoveInput struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Review  string `json:"review,omitempty"`
	Confirm bool   `json:"confirm,omitempty"`
}

func (v SourceMoveInput) Validate() error {
	if !workspacePath(v.From, true) || !workspacePath(v.To, true) || referencePath(v.From) != referencePath(v.To) || v.From == v.To || v.Confirm && !hashRE.MatchString(v.Review) || !v.Confirm && v.Review != "" {
		return ErrHeld
	}
	return nil
}

type SourceMoveView struct {
	SchemaVersion    string  `json:"schema_version"`
	ID               string  `json:"id"`
	Review           string  `json:"review"`
	Status           string  `json:"status"`
	Reason           string  `json:"reason,omitempty"`
	Previous         Binding `json:"previous"`
	Binding          Binding `json:"binding"`
	SourceGeneration string  `json:"source_generation"`
	DestinationBase  string  `json:"destination_base"`
	ContentChanged   bool    `json:"content_changed"`
	Diagnostic       string  `json:"diagnostic,omitempty"`
}

type sourceMoveRecord struct {
	View        SourceMoveView
	Previous    BindingRecord
	Destination BindingRecord
	Published   bool
	Reason      string
	Detail      string
	Terminal    bool
	RetryAtMS   int64
}

// A successor owns only the freed pathname. The retired binding and its late
// request fence remain immutable navigation/recovery evidence.
func retiredSourceMapping(ctx context.Context, records Records, mapping FileMapping) bool {
	if mapping.Latest == nil {
		return false
	}
	layout, err := load[Layout](ctx, records, "export", sourceMoveRetirement(*mapping.Latest))
	if err != nil || !layout.SourceMove || layout.Previous != *mapping.Latest {
		return false
	}
	move, err := load[sourceMoveRecord](ctx, records, "export", sourceMovePrefix+key(layout.ID))
	return err == nil && move.Published && move.Previous.Binding == layout.Previous && move.Destination.Binding == layout.Binding
}

type sourceSuccessor struct {
	IntentID, ClientFileID, CreateBaseID string
}

func (s *Service) reserveSourceSuccessor(ctx context.Context, records Records, i Intent, m FileMapping, priorClient string) error {
	if i.Operation != "create" || i.Base != nil || i.Predecessor != "" || m.Latest != nil || m.CreateBaseID == "" {
		return ErrHeld
	}
	prior, err := load[FileMapping](ctx, records, "file", mapKey(Intent{Workspace: m.Scope.Workspace, Collection: m.Scope.Collection, Generation: m.Scope.Generation, FileID: priorClient}))
	if err != nil || prior.SourceFileID != m.SourceFileID || !sameExportScope(prior.Scope, m.Scope) || prior.Latest == nil || prior.Latest.Path != i.Path || !retiredSourceMapping(ctx, records, prior) {
		return ErrHeld
	}
	base, err := s.Source.Base(ctx, m.CreateBaseID)
	if err != nil || base.Exists || base.FileID != m.SourceFileID || base.Generation != i.Generation {
		return ErrHeld
	}
	id := "source_successor_" + key(sourceKey(m.Scope, m.SourceFileID), priorClient)
	want := sourceSuccessor{i.ID, i.FileID, m.CreateBaseID}
	old, err := load[sourceSuccessor](ctx, records, "source_file", id)
	if err == nil && old != want || err != nil && !missing(err) {
		return ErrHeld
	}
	return records.Put(ctx, "source_file", id, want)
}

func sourceMoveRetirement(b Binding) string {
	return "source_move_retired_" + key(b.Workspace, b.Collection, b.FileID, b.Path)
}

func (s *Service) sourceMoveView(ctx context.Context, previous, destination BindingRecord) (SourceMoveView, error) {
	a, b := previous.Binding, destination.Binding
	if !a.valid() || !b.valid() || a.Workspace != b.Workspace || a.Path == b.Path || a.Deleted || b.Deleted ||
		!movableSourceBinding(a) || !movableSourceBinding(b) || referencePath(a.Path) != referencePath(b.Path) ||
		referencePath(a.Path) && a.SHA256 != b.SHA256 || b.SourceSequence == 0 {
		return SourceMoveView{}, ErrHeld
	}
	var from, to *Scope
	for i := range s.Scopes {
		scope := &s.Scopes[i]
		if scope.Workspace == a.Workspace && scope.Collection == a.Collection && scope.Root == a.CollectionRoot {
			from = scope
		}
		if scope.Workspace == b.Workspace && scope.Collection == b.Collection && scope.Root == b.CollectionRoot {
			to = scope
		}
	}
	if from == nil || to == nil || from.ReadOnly || to.ReadOnly || to.Generation != b.Generation {
		return SourceMoveView{}, ErrHeld
	}
	oldRelative := strings.TrimPrefix(a.Path, from.Root+"/")
	if oldRelative == a.Path || !workspacePath(oldRelative, true) {
		return SourceMoveView{}, ErrHeld
	}
	newRelative := strings.TrimPrefix(b.Path, to.Root+"/")
	if newRelative == b.Path || !workspacePath(newRelative, true) {
		return SourceMoveView{}, ErrHeld
	}
	absent, oldSnapshot, err := readExportSource(ctx, s.Source, from.SourceCollection, oldRelative, s.ReferenceSpool)
	if oldSnapshot != "" {
		defer os.Remove(oldSnapshot)
	}
	if err != nil {
		return SourceMoveView{}, err
	}
	if absent.Base.Exists {
		return SourceMoveView{}, errors.New("source move still has an original file; retire it through its source journal first")
	}
	current, snapshot, err := readExportSource(ctx, s.Source, to.SourceCollection, newRelative, s.ReferenceSpool)
	if snapshot != "" {
		defer os.Remove(snapshot)
	}
	if err != nil {
		return SourceMoveView{}, err
	}
	if !current.Base.Exists || current.File.ID != destination.SourceFileID || current.Base.ID != destination.SourceBaseID ||
		current.Base.Generation != b.Generation || current.Base.Hash != b.SHA256 || b.SourceBase != "base_"+key(s.Replica, current.Base.ID) {
		return SourceMoveView{}, ErrHeld
	}
	view := SourceMoveView{SchemaVersion: "loom.notes.source_move.v1", Status: "review", Previous: a, Binding: b,
		SourceGeneration: absent.Base.Generation, DestinationBase: current.Base.ID, ContentChanged: a.SHA256 != b.SHA256}
	raw, _ := json.Marshal(view)
	view.Review = digest(raw)
	view.ID = "layout_source_move_" + key(view.Review)
	return view, nil
}

// Review and confirmation stage metadata in the existing export journal. Only
// the long-lived native owner publishes controls or retires native revisions.
func (r *Runtime) SourceMove(ctx context.Context, input SourceMoveInput) (SourceMoveView, error) {
	if input.Validate() != nil {
		return SourceMoveView{}, ErrHeld
	}
	s, err := r.conflictService(ctx)
	if err != nil {
		return SourceMoveView{}, err
	}
	s.AdoptExisting = true
	s.ReferenceSpool = filepath.Join(r.Config.ReplicaDir, "reference-spool")
	a, err := r.sourceMoveBinding(ctx, input.From, s)
	if err != nil {
		return SourceMoveView{}, err
	}
	b, err := r.sourceMoveBinding(ctx, input.To, s)
	if err != nil {
		return SourceMoveView{}, err
	}
	view, err := s.sourceMoveView(ctx, a, b)
	if err != nil {
		return SourceMoveView{}, err
	}
	if !input.Confirm {
		err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(records Records) error {
			old, err := load[sourceMoveRecord](ctx, records, "export", sourceMovePrefix+key(view.ID))
			if err != nil {
				if missing(err) {
					return nil
				}
				return err
			}
			if old.Published {
				view.Status = "published_device_ack_unknown"
			} else if old.Reason != "" {
				view.Status = "held"
				view.Reason = old.Reason
				view.Diagnostic = old.Detail
			} else {
				view.Status = "queued"
			}
			return nil
		})
		return view, err
	}
	if view.Review != input.Review {
		return SourceMoveView{}, ErrConflictReview
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(records Records) error {
		for _, expected := range []BindingRecord{a, b} {
			actual, err := load[BindingRecord](ctx, records, "binding", expected.Binding.ID)
			if err != nil || actual != expected {
				return ErrConflictReview
			}
		}
		retired, err := load[Layout](ctx, records, "export", sourceMoveRetirement(a.Binding))
		if err != nil && !missing(err) {
			return err
		}
		if err == nil && retired.SourceMove && a.Binding.SourceSequence <= retired.Previous.SourceSequence && retired.Binding != b.Binding {
			return ErrConflictReview
		}
		id := sourceMovePrefix + key(view.ID)
		old, err := load[sourceMoveRecord](ctx, records, "export", id)
		if err != nil && !missing(err) {
			return err
		}
		if old.Published {
			view.Status = "published_device_ack_unknown"
			return nil
		}
		view.Status = "queued"
		return records.Put(ctx, "export", id, sourceMoveRecord{View: view, Previous: a, Destination: b})
	})
	if err != nil {
		return SourceMoveView{}, err
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return view, err
	}
	defer tx.Rollback()
	if err = workers.NotifyQueueTx(ctx, tx, "notes_workspace_sync"); err != nil {
		return view, err
	}
	return view, tx.Commit()
}

func (r *Runtime) sourceMoveBinding(ctx context.Context, path string, service *Service) (BindingRecord, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT record FROM notes_workspace.sync_records
 WHERE replica_id=$1 AND kind='file' AND record->'Latest'->>'path'=$2 LIMIT 129`, r.Config.Replica, path)
	if err != nil {
		return BindingRecord{}, err
	}
	defer rows.Close()
	var candidates []FileMapping
	for rows.Next() {
		var raw []byte
		var record FileMapping
		if err := rows.Scan(&raw); err != nil {
			return BindingRecord{}, err
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return BindingRecord{}, err
		}
		candidates = append(candidates, record)
	}
	if err := rows.Err(); err != nil {
		return BindingRecord{}, err
	}
	if err := rows.Close(); err != nil {
		return BindingRecord{}, err
	}
	if len(candidates) > 128 {
		return BindingRecord{}, ErrHeld
	}
	var found []BindingRecord
	err = service.Store.WithReplica(ctx, service.Replica, service.Epoch, func(records Records) error {
		seen := map[string]bool{}
		for _, candidate := range candidates {
			for _, scope := range service.Scopes {
				lookup := candidate.Scope
				// Outgoing retirement may need a retained predecessor after a
				// processing-policy handover. Current membership/absence and the
				// live reverse pointer still gate it; old incoming writes gain nothing.
				if service.AdoptExisting {
					lookup.Generation = scope.Generation
				}
				if !sameSourceScope(lookup, scope) || candidate.Scope.Root != scope.Root {
					continue
				}
				identity := sourceKey(scope, candidate.SourceFileID)
				if seen[identity] {
					continue
				}
				seen[identity] = true
				current, err := liveMoveMapping(ctx, records, scope, candidate.SourceFileID)
				if err != nil || current.Latest == nil || current.Latest.Path != path {
					return ErrHeld
				}
				binding, err := load[BindingRecord](ctx, records, "binding", current.Latest.ID)
				if err != nil || binding.Binding != *current.Latest || binding.SourceFileID != candidate.SourceFileID {
					return ErrHeld
				}
				found = append(found, binding)
			}
		}
		return nil
	})
	if err != nil || len(found) != 1 {
		return BindingRecord{}, ErrHeld
	}
	return found[0], nil
}

func liveMoveMapping(ctx context.Context, records Records, scope Scope, sourceID string) (FileMapping, error) {
	_, err := load[string](ctx, records, "source_file", sourceKey(scope, sourceID))
	if missing(err) && scope.PreviousGeneration != "" && scope.PreviousGeneration != scope.Generation {
		prior := scope
		prior.Generation = scope.PreviousGeneration
		_, err = load[string](ctx, records, "source_file", sourceKey(prior, sourceID))
	}
	if missing(err) {
		return retainedMapping(ctx, records, scope, sourceID)
	}
	if err != nil {
		return FileMapping{}, err
	}
	// A present reverse pointer must resolve; a missing target is not absence
	// of ownership and cannot grant a retired locator authority again.
	return exportMapping(ctx, records, scope, sourceID)
}

func (s *Service) advanceSourceMoves(ctx context.Context, limit int) (more bool, published, held int, err error) {
	if !s.LayoutPublication {
		return
	}
	s.MoveRetryAt = time.Time{}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(records Records) error {
		var ids []string
		if query, ok := records.(interface {
			PendingSourceMoves(context.Context, int) ([]string, *time.Time, error)
		}); ok {
			var next *time.Time
			ids, next, err = query.PendingSourceMoves(ctx, limit)
			if next != nil {
				s.MoveRetryAt = *next
			}
		} else {
			ids, err = records.List(ctx, "export", sourceMovePrefix, limit)
		}
		if err != nil {
			return err
		}
		more = len(ids) == limit
		for _, id := range ids {
			if !strings.HasPrefix(id, sourceMovePrefix) {
				more = false
				break
			}
			move, err := load[sourceMoveRecord](ctx, records, "export", id)
			if err != nil {
				return err
			}
			if move.Published || move.Terminal {
				continue
			}
			if move.RetryAtMS > time.Now().UnixMilli() {
				at := time.UnixMilli(move.RetryAtMS)
				if s.MoveRetryAt.IsZero() || at.Before(s.MoveRetryAt) {
					s.MoveRetryAt = at
				}
				more = false
				continue
			}
			layout := Layout{Header: Header{1, "layout", move.View.ID}, Previous: move.Previous.Binding, Binding: move.Destination.Binding, SourceMove: true}
			var result Evidence
			if err := s.call(ctx, "source.move.status", map[string]any{"layout": layout}, &result); err != nil {
				return err
			}
			if result.Phase != "published" && result.Phase != "retired" {
				view, err := s.sourceMoveView(ctx, move.Previous, move.Destination)
				if err != nil || view.Review != move.View.Review {
					move.Reason, move.Terminal = "source_move_review_changed", true
					if err := records.Put(ctx, "export", id, move); err != nil {
						return err
					}
					held++
					continue
				}
			}
			if result.Phase != "retired" {
				if err := s.call(ctx, "source.move", map[string]any{"layout": layout}, &result); err != nil {
					return err
				}
			}
			if result.Phase == "published" || result.Phase == "retired" {
				if err := records.Put(ctx, "export", sourceMoveRetirement(layout.Previous), layout); err != nil {
					return err
				}
			}
			move.Reason, move.Detail, move.RetryAtMS = "", "", 0
			if result.Status == "published" && result.Phase == "retired" {
				move.Published = true
				published++
			} else {
				move.Reason = result.Reason
				move.Detail = result.Detail
				if move.Reason == "" {
					move.Reason = "source_move_native_unavailable"
				}
				switch result.Reason {
				case "layout_controls_pending", "control_unavailable", "control_pending_chunks":
					at := time.Now().Add(2 * time.Second)
					move.RetryAtMS = at.UnixMilli()
					if s.MoveRetryAt.IsZero() || at.Before(s.MoveRetryAt) {
						s.MoveRetryAt = at
					}
				default:
					move.Terminal = true
				}
			}
			if move.Reason != "" {
				held++
			}
			if err := records.Put(ctx, "export", id, move); err != nil {
				return err
			}
		}
		return nil
	})
	return
}
