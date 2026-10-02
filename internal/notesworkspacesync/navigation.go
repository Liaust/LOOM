package notesworkspacesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspace"
)

// Navigation never authorizes an edit or asserts that a device has received a
// binding. Clients must match the binding identity/revision to their local file.
type Navigation struct {
	SchemaVersion string                      `json:"schema_version"`
	Citation      knowledge.NotesPassageInput `json:"citation"`
	Status        string                      `json:"status"`
	Reason        string                      `json:"reason"`
	Binding       *Binding                    `json:"binding,omitempty"`
	LocalSearch   string                      `json:"local_search"`
	Sync          string                      `json:"sync"`
	LexicalIndex  string                      `json:"lexical_index"`
	SemanticIndex string                      `json:"semantic_index"`
}

func navigationResult(input knowledge.NotesPassageInput) Navigation {
	return Navigation{SchemaVersion: "loom.notes.workspace_navigation.v1", Citation: input,
		Status: "unavailable", Reason: "workspace_not_configured", LocalSearch: "device_local_not_observed",
		Sync: "not_observed", LexicalIndex: "citation_validated", SemanticIndex: "not_observed"}
}

// LocatePassage checks the exact retained tuple using the existing Notes access
// surface before reading any workspace binding. A historical citation remains
// readable via passage get, but never supplies a guessed writable destination.
func (r *Runtime) LocatePassage(ctx context.Context, input knowledge.NotesPassageInput) (Navigation, error) {
	out := navigationResult(input)
	if r.Knowledge == nil {
		return out, errors.New("Notes knowledge service unavailable")
	}
	passage, err := r.Knowledge.GetNotesPassage(ctx, input)
	if err != nil {
		return out, err
	}
	if !navigationCurrentPassage(&out, passage) {
		return out, nil
	}
	if r.Config.Validate() != nil || r.DB == nil || r.NodeKey == "" {
		return out, nil
	}
	object, err := r.Knowledge.Store().GetKnowledgeObjectWithLifecycle(ctx, input.KnowledgeObjectID, input.SourceLifecycle)
	if err != nil {
		return out, err
	}
	if object.KnowledgeObjectID != input.KnowledgeObjectID {
		return out, sql.ErrNoRows
	}
	if object.SourceHash != input.SourceHash {
		out.Status, out.Reason, out.LexicalIndex = "stale_citation", "source_changed_since_indexing", "lagging"
		return out, nil
	}
	var selection *RuntimeSelection
	for i := range r.Config.Scopes {
		scope := &r.Config.Scopes[i]
		if scope.SourceCollection == object.NotesSourceRootID && scope.contains(object.RelativePath) {
			selection = scope
			break
		}
	}
	if selection == nil {
		out.Status, out.Reason = "unbound", "source_not_enrolled"
		return out, nil
	}
	resolver := notesworkspace.KnowledgeResolver{Knowledge: r.Knowledge, NodeKey: r.NodeKey,
		WithSelection: func(_ context.Context, id string, fn func(string) error) error {
			if id != selection.SourceCollection {
				return notesworkspace.ErrMembership
			}
			generation := selection.Generation
			if selection.PathPrefix != "" {
				generation = key(generation, selection.PathPrefix)
			}
			return fn(generation)
		},
	}
	source := notesworkspace.Service{Store: notesworkspace.SQLStore{DB: r.DB}, Sources: selectedSources{PathResolver: resolver, Selections: r.Config.Scopes}}
	out.Status, out.Reason = "unbound", "current_file_has_no_verified_binding"
	err = source.WithNavigation(ctx, object.NotesSourceRootID, object.RelativePath, input.SourceHash, func(file notesworkspace.File, base notesworkspace.Base) error {
		scope := selection.Scope
		scope.Generation = base.Generation
		mapping, record, err := r.navigationBinding(ctx, scope, file.ID)
		if err != nil {
			return err
		}
		binding, ok := verifiedNavigationBinding(scope, file, base, mapping, record)
		if !ok || binding.SourceBase != "base_"+key(r.Config.Replica, base.ID) {
			return nil
		}
		out.Status, out.Reason, out.Binding = "available", "exact_current_source_binding", &binding
		out.Sync, out.LexicalIndex = "binding_recorded_device_ack_unknown", "citation_matches_current_source"
		return nil
	})
	if err != nil {
		// Privacy/lifecycle/selection/path holds never expose a stale target or raw
		// source paths. The retained passage endpoint retains its own error behavior.
		out.Binding = nil
		out.Status, out.Reason = "unavailable", "current_source_or_binding_unavailable"
		if errors.Is(err, sql.ErrNoRows) {
			out.Status, out.Reason = "unbound", "current_file_has_no_verified_binding"
		}
		if errors.Is(err, notesworkspace.ErrStalePath) {
			out.Status, out.Reason, out.LexicalIndex = "stale_citation", "source_changed_since_indexing", "lagging"
		}
	}
	return out, nil
}

// One read-only statement sees the persisted reverse mapping, latest binding,
// and original binding receipt together. It never opens/starts a native replica,
// registers an epoch, or acquires locks in reverse runtime order.
func (r *Runtime) navigationBinding(ctx context.Context, scope Scope, fileID string) (FileMapping, BindingRecord, error) {
	var mapping FileMapping
	var record BindingRecord
	var mappingJSON, bindingJSON []byte
	err := r.DB.QueryRowContext(ctx, `SELECT f.record,b.record
 FROM notes_workspace.sync_records reverse
 JOIN notes_workspace.sync_records f ON f.replica_id=reverse.replica_id AND f.kind='file'
 AND f.record->>'ClientFileID'=(reverse.record #>> '{}')
 AND f.record->'Scope'->>'Workspace'=$3 AND f.record->'Scope'->>'Collection'=$4
 AND f.record->'Scope'->>'Generation'=$5
 JOIN notes_workspace.sync_records b ON b.replica_id=f.replica_id AND b.kind='binding'
 AND b.record_id=f.record->'Latest'->>'id'
 WHERE reverse.replica_id=$1 AND reverse.kind='source_file' AND reverse.record_id=$2`,
		r.Config.Replica, sourceKey(scope, fileID), scope.Workspace, scope.Collection, scope.Generation).Scan(&mappingJSON, &bindingJSON)
	if err != nil {
		return mapping, record, err
	}
	if err = json.Unmarshal(mappingJSON, &mapping); err != nil {
		return mapping, record, err
	}
	err = json.Unmarshal(bindingJSON, &record)
	return mapping, record, err
}

func verifiedNavigationBinding(scope Scope, file notesworkspace.File, base notesworkspace.Base, mapping FileMapping, record BindingRecord) (Binding, bool) {
	if mapping.Latest == nil {
		return Binding{}, false
	}
	b := *mapping.Latest
	valid := b.valid() && mapping.Scope == scope && mapping.SourceFileID == file.ID && mapping.ClientFileID == b.FileID &&
		file.CollectionID == scope.SourceCollection && !file.Deleted && base.FileID == file.ID && base.Exists &&
		base.Generation == scope.Generation && base.PathVersion == file.PathVersion &&
		b.Workspace == scope.Workspace && b.Collection == scope.Collection && b.Generation == scope.Generation &&
		b.CollectionRoot == scope.Root && b.Path == scope.Root+"/"+file.RelativePath && b.SHA256 == base.Hash &&
		record.Binding == b && record.SourceFileID == file.ID && record.SourceBaseID == base.ID &&
		b.Writable == !notesworkspace.IsReferencePath(file.RelativePath)
	return b, valid
}

func navigationCurrentPassage(out *Navigation, passage knowledge.NotesPassage) bool {
	if passage.Custody.SourceLifecycle == knowledge.SourceLifecycleArchived {
		out.Status, out.Reason = "archived", "archived_source_has_no_live_workspace_target"
		return false
	}
	if passage.Historical {
		out.Status, out.Reason, out.LexicalIndex = "stale_citation", "request_a_current_search_citation", "historical"
		return false
	}
	return true
}
