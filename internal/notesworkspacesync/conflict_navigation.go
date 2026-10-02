package notesworkspacesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"loom.local/loom/internal/notesworkspace"
)

var ErrConflictReview = errors.New("conflict changed or source binding is not current; refresh the comparison")

type ConflictItem struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	Status       string `json:"status"`
	ResolutionID string `json:"resolution_id,omitempty"`
}
type ConflictPage struct {
	Items []ConflictItem `json:"items"`
	Next  string         `json:"next,omitempty"`
}
type ConflictView struct {
	ConflictItem
	Review  string  `json:"review"`
	Binding Binding `json:"binding"`
	Base    string  `json:"base"`
	Device  string  `json:"device"`
	Source  string  `json:"source"`
}
type ResolveConflictInput struct {
	ID      string `json:"id"`
	Review  string `json:"review"`
	Choice  string `json:"choice"`
	Text    string `json:"text,omitempty"`
	Confirm bool   `json:"confirm"`
}
type ResolutionResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Ack    *Ack   `json:"ack,omitempty"`
}

func (v ResolveConflictInput) Validate() error {
	if !token(v.ID) || !hashRE.MatchString(v.Review) || !v.Confirm || !utf8Text([]byte(v.Text)) ||
		(v.Choice != "source" && v.Choice != "device" && v.Choice != "merge") || v.Choice != "merge" && v.Text != "" {
		return ErrHeld
	}
	return nil
}

type stagedSource interface {
	Stage(context.Context, SourceRequest) error
	Operation(context.Context, string) (notesworkspace.Operation, error)
}

func (s *Service) conflictView(ctx context.Context, r Records, id string) (out ConflictView, err error) {
	j, err := load[Join](ctx, r, "operation", id)
	if err != nil {
		return out, err
	}
	i, err := parseIntent([]byte(j.IntentRaw))
	if err != nil || i.Operation != "edit" || j.Ack == nil || j.Ack.Status != "conflict" || j.Ack.Reason != "source_conflict" {
		return out, ErrHeld
	}
	scope, ok := s.scope(i)
	if !ok {
		return out, ErrHeld
	}
	relative, ok := scope.relative(i.Path)
	if !ok {
		return out, ErrHeld
	}
	current, err := s.Source.Read(ctx, scope.SourceCollection, relative)
	if err != nil {
		return out, err
	}
	if !current.Base.Exists || current.File.ID != j.SourceFileID || current.Base.Generation != i.Generation {
		return out, ErrHeld
	}
	out.ConflictItem = ConflictItem{ID: id, Path: i.Path, Status: "waiting_for_source_binding", ResolutionID: j.ResolutionID}
	mapping, err := load[FileMapping](ctx, r, "file", mapKey(i))
	if err != nil {
		return out, err
	}
	if mapping.Latest == nil || mapping.Latest.SHA256 != current.Base.Hash ||
		mapping.Latest.SourceBase != "base_"+key(s.Replica, current.Base.ID) {
		return out, ErrConflictReview
	}
	source, ok := s.Source.(stagedSource)
	if !ok {
		return out, ErrHeld
	}
	original, err := source.Operation(ctx, "sync_"+key(s.Replica, id))
	if err != nil {
		return out, err
	}
	if original.State != notesworkspace.Conflict || original.Request.FileID != current.File.ID ||
		digest(original.Request.Content) != i.SHA256 {
		return out, ErrHeld
	}
	status := "conflict"
	if j.ResolutionID != "" {
		attempt, err := load[Join](ctx, r, "operation", j.ResolutionID)
		if err != nil {
			return out, err
		}
		status = "resolution_pending"
		if attempt.Ack != nil {
			status = attempt.Ack.Status
		}
	}
	out = ConflictView{ConflictItem: ConflictItem{ID: id, Path: i.Path, Status: status, ResolutionID: j.ResolutionID},
		Binding: *mapping.Latest, Base: string(original.Base.Content), Device: string(original.Request.Content), Source: string(current.Base.Content)}
	raw, _ := json.Marshal(out)
	out.Review = digest(raw)
	return out, nil
}
func (s *Service) ShowConflict(ctx context.Context, id string) (out ConflictView, err error) {
	if !token(id) {
		return out, ErrHeld
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		out, err = s.conflictView(ctx, r, id)
		return err
	})
	return
}
func (s *Service) ListConflicts(ctx context.Context, after string) (out ConflictPage, err error) {
	out.Items = []ConflictItem{}
	if after != "" && !token(after) {
		return out, ErrHeld
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		ids, err := r.List(ctx, "operation", after, 128)
		if err != nil {
			return err
		}
		for _, id := range ids {
			view, e := s.conflictView(ctx, r, id)
			if e != nil && !errors.Is(e, ErrHeld) && !missing(e) && !errors.Is(e, ErrConflictReview) {
				return e
			}
			if (e == nil || errors.Is(e, ErrConflictReview)) && view.Status != "applied" {
				out.Items = append(out.Items, view.ConflictItem)
			}
		}
		if len(ids) == 128 {
			out.Next = ids[len(ids)-1]
		}
		return nil
	})
	return
}
func (s *Service) ResolveConflict(ctx context.Context, input ResolveConflictInput) (out ResolutionResult, err error) {
	if err = input.Validate(); err != nil {
		return
	}
	id := "resolve_" + key(input.ID, input.Review, input.Choice, input.Text)
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		if prior, e := load[Join](ctx, r, "operation", id); e == nil {
			if prior.OperatorReview != input.Review {
				return ErrHeld
			}
			// Recheck current enrollment before exposing a historical receipt.
			i, e := parseIntent([]byte(prior.IntentRaw))
			if e != nil {
				return e
			}
			scope, ok := s.scope(i)
			if !ok {
				return ErrHeld
			}
			relative, ok := scope.relative(i.Path)
			if !ok {
				return ErrHeld
			}
			if _, e = s.Source.Read(ctx, scope.SourceCollection, relative); e != nil {
				return e
			}
			out = ResolutionResult{ID: id, Status: prior.Status, Reason: prior.Reason, Ack: prior.Ack}
			return nil
		} else if !missing(e) {
			return e
		}
		view, e := s.conflictView(ctx, r, input.ID)
		if e != nil {
			return e
		}
		if view.Review != input.Review || view.Status == "applied" || view.Status == "resolution_pending" {
			return ErrConflictReview
		}
		text := input.Text
		if input.Choice == "source" {
			text = view.Source
		}
		if input.Choice == "device" {
			text = view.Device
		}
		old, e := load[Join](ctx, r, "operation", input.ID)
		if e != nil {
			return e
		}
		record, e := load[BindingRecord](ctx, r, "binding", view.Binding.ID)
		if e != nil {
			return e
		}
		i := Intent{Header: Header{1, "intent", id}, Device: "loom-operator", Operation: "edit", Workspace: view.Binding.Workspace,
			Collection: view.Binding.Collection, Generation: view.Binding.Generation, FileID: view.Binding.FileID,
			Base: &view.Binding, Path: view.Path, SHA256: digest([]byte(text)), Length: len([]byte(text)), Resolves: []string{input.ID}}
		raw, e := json.Marshal(i)
		if e != nil {
			return e
		}
		j := Join{IntentRaw: string(raw), Status: "pending", SourceFileID: old.SourceFileID, SourceBaseID: record.SourceBaseID, OperatorReview: input.Review}
		source, ok := s.Source.(stagedSource)
		if !ok {
			return ErrHeld
		}
		if e := source.Stage(ctx, SourceRequest{OperationID: "sync_" + key(s.Replica, id), FileID: j.SourceFileID,
			Generation: i.Generation, BaseID: j.SourceBaseID, Kind: "edit", Content: []byte(text)}); e != nil {
			return e
		}
		if reason, e := s.claimResolutions(ctx, r, i, j); e != nil {
			return e
		} else if reason != "" {
			return fmt.Errorf("%w: %s", ErrConflictReview, reason)
		}
		if e := r.Put(ctx, "operation", id, j); e != nil {
			return e
		}
		out = ResolutionResult{ID: id, Status: "pending", Reason: "waiting_for_workspace_worker"}
		return nil
	})
	return
}

func (s *Service) publishOperatorResolution(ctx context.Context, r Records, id string, j *Join) error {
	i, err := parseIntent([]byte(j.IntentRaw))
	if err != nil || i.ID != id {
		return ErrHeld
	}
	scope, ok := s.scope(i)
	if !ok {
		return ErrHeld
	}
	relative, ok := scope.relative(i.Path)
	if !ok {
		return ErrHeld
	}
	live, err := s.Source.Read(ctx, scope.SourceCollection, relative)
	if err != nil || live.File.ID != j.SourceFileID || live.Base.Generation != i.Generation {
		return ErrHeld
	}
	source, ok := s.Source.(stagedSource)
	if !ok {
		return ErrHeld
	}
	op, err := source.Operation(ctx, "sync_"+key(s.Replica, id))
	if err != nil {
		return err
	}
	if digest(op.Request.Content) != i.SHA256 || op.Request.BaseID != j.SourceBaseID ||
		op.Request.FileID != j.SourceFileID || op.Request.Generation != i.Generation ||
		op.Request.Kind != "edit" || len(op.Request.Content) != i.Length {
		return ErrHeld
	}
	raw, _ := marshalControl(i)
	var result Evidence
	if err := s.call(ctx, "control.put", map[string]any{"contentBase64": raw}, &result); err != nil || result.Status != "published" {
		return ErrHeld
	}
	if err := s.call(ctx, "payload.put", map[string]any{"intentId": id, "contentBase64": op.Request.Content}, &result); err != nil || result.Status != "published" {
		return ErrHeld
	}
	if err := s.call(ctx, "publish", map[string]any{"operationId": id, "path": i.Path, "baseRevision": i.Base.NativeRevision, "contentBase64": op.Request.Content}, &result); err != nil || result.Status != "published" {
		return ErrHeld
	}
	p := Publication{Header: Header{1, "publication", "pub_" + id}, IntentID: id, IntentDigest: digest([]byte(j.IntentRaw)), Revisions: []Revision{{Path: i.Path, Revision: result.Revision, Role: "content"}}}
	bytes, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err := parsePublication(bytes); err != nil {
		return err
	}
	raw, _ = marshalControl(p)
	if err := s.call(ctx, "control.put", map[string]any{"contentBase64": raw}, &result); err != nil || result.Status != "published" {
		return ErrHeld
	}
	j.PublicationRaw = string(bytes)
	return r.Put(ctx, "operation", id, *j)
}

// HTTP/CLI requests stage work in existing source/replica journals. They do not
// open the native DB or compete with the long-running replication worker.
func (r *Runtime) conflictService(ctx context.Context) (*Service, error) {
	if r.Config.Validate() != nil || r.DB == nil || r.Knowledge == nil || r.NodeKey == "" {
		return nil, ErrHeld
	}
	resolver := notesworkspace.KnowledgeResolver{Knowledge: r.Knowledge, NodeKey: r.NodeKey,
		WithSelection: func(_ context.Context, id string, fn func(string) error) error {
			for _, scope := range r.Config.Scopes {
				if scope.SourceCollection == id {
					generation := scope.Generation
					if scope.PathPrefix != "" {
						generation = key(generation, scope.PathPrefix)
					}
					return fn(generation)
				}
			}
			return notesworkspace.ErrMembership
		}}
	source := notesworkspace.Service{Store: notesworkspace.SQLStore{DB: r.DB}, Sources: selectedSources{PathResolver: resolver, Selections: r.Config.Scopes}}
	s := &Service{Replica: r.Config.Replica, Store: SQLStore{DB: r.DB}, Source: NotesSource{Service: source}}
	if err := r.DB.QueryRowContext(ctx, `SELECT native_epoch FROM notes_workspace.sync_replicas WHERE replica_id=$1`, s.Replica).Scan(&s.Epoch); err != nil {
		return nil, err
	}
	for _, selection := range r.Config.Scopes {
		scope := selection.Scope
		if err := source.Sources.WithSource(ctx, scope.SourceCollection, selection.AdmissionPath, func(src notesworkspace.Source) error { scope.Generation = src.Generation; return nil }); err == nil {
			s.Scopes = append(s.Scopes, scope)
		}
	}
	return s, nil
}
func (r *Runtime) ListConflicts(ctx context.Context, after string) (ConflictPage, error) {
	s, e := r.conflictService(ctx)
	if e != nil {
		return ConflictPage{}, e
	}
	return s.ListConflicts(ctx, after)
}
func (r *Runtime) ShowConflict(ctx context.Context, id string) (ConflictView, error) {
	s, e := r.conflictService(ctx)
	if e != nil {
		return ConflictView{}, e
	}
	return s.ShowConflict(ctx, id)
}
func (r *Runtime) ResolveConflict(ctx context.Context, input ResolveConflictInput) (ResolutionResult, error) {
	s, e := r.conflictService(ctx)
	if e != nil {
		return ResolutionResult{}, e
	}
	return s.ResolveConflict(ctx, input)
}
