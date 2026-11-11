package notesworkspacesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"loom.local/loom/internal/notesworkspace"
)

// WorkspaceLookup is read-only navigation metadata. A device must confirm the
// installed binding with its own journal; this does not admit a source edit.
type WorkspaceLookup struct {
	Collection string `json:"collection"`
	Relative   string `json:"relative"`
}

func LookupKey(collection, relative string) string { return collection + "\x00" + relative }

func (r *Runtime) LookupWorkspaceFiles(ctx context.Context, inputs []WorkspaceLookup) (map[string]Binding, error) {
	out := map[string]Binding{}
	if len(inputs) > 100 {
		return nil, fmt.Errorf("workspace lookup exceeds 100 files")
	}
	if r.DB == nil || r.Config.Validate() != nil || len(inputs) == 0 {
		return out, nil
	}
	type request struct {
		Collection string `json:"collection"`
		Relative   string `json:"relative"`
		PathKey    string `json:"path_key"`
	}
	selected := map[string]Scope{}
	requests := []request{}
	for _, input := range inputs {
		for _, selection := range r.Config.Scopes {
			if selection.SourceCollection == input.Collection && !selection.Archived && selection.contains(input.Relative) {
				k := LookupKey(input.Collection, input.Relative)
				selected[k] = selection.Scope
				requests = append(requests, request{input.Collection, input.Relative, cases.Fold().String(norm.NFC.String(input.Relative))})
				break
			}
		}
	}
	if len(requests) == 0 {
		return out, nil
	}
	tx, err := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	raw, _ := json.Marshal(requests)
	rows, err := tx.QueryContext(ctx, `SELECT i.collection,i.relative,f.record
 FROM jsonb_to_recordset($1::jsonb) AS i(collection text,relative text,path_key text)
 JOIN notes_workspace.files f ON f.collection_id=i.collection AND f.path_key=i.path_key AND NOT f.deleted`, raw)
	if err != nil {
		return nil, err
	}
	type target struct {
		key, locator string
		file         notesworkspace.File
		scope        Scope
	}
	targets := []target{}
	for rows.Next() {
		var collection, relative string
		var record []byte
		var file notesworkspace.File
		if err = rows.Scan(&collection, &relative, &record); err != nil {
			rows.Close()
			return nil, err
		}
		if json.Unmarshal(record, &file) != nil || file.Deleted || file.RelativePath != relative || file.CollectionID != collection {
			continue
		}
		k := LookupKey(collection, relative)
		scope := selected[k]
		targets = append(targets, target{k, mappingLocator(scope, file.ID), file, scope})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	locators := []string{}
	for _, target := range targets {
		locators = append(locators, target.locator)
	}
	if len(locators) == 0 {
		return out, nil
	}
	// Exact retained reverse locators are maintained by the existing exporter.
	// Do not scan JSON records or start WithReplica on a UI read path.
	rows, err = tx.QueryContext(ctx, `SELECT p.record_id,f.record,b.record
 FROM notes_workspace.sync_records p
 JOIN notes_workspace.sync_records f ON f.replica_id=p.replica_id AND f.kind='file' AND f.record_id=(p.record #>> '{}')
 JOIN notes_workspace.sync_records b ON b.replica_id=f.replica_id AND b.kind='binding' AND b.record_id=f.record->'Latest'->>'id'
 WHERE p.replica_id=$1 AND p.kind='source_file' AND p.record_id=ANY($2)`, r.Config.Replica, locators)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byLocator := map[string]target{}
	for _, target := range targets {
		byLocator[target.locator] = target
	}
	for rows.Next() {
		var locator string
		var mappingRaw, recordRaw []byte
		var mapping FileMapping
		var record BindingRecord
		if err = rows.Scan(&locator, &mappingRaw, &recordRaw); err != nil {
			return nil, err
		}
		if json.Unmarshal(mappingRaw, &mapping) != nil || json.Unmarshal(recordRaw, &record) != nil {
			continue
		}
		target, ok := byLocator[locator]
		if !ok || mapping.Latest == nil {
			continue
		}
		scope := target.scope
		scope.Generation = mapping.Scope.Generation
		if !validExportMapping(mapping, scope, target.file.ID, mapping.ClientFileID) || mapping.Latest.Deleted || record.Binding != *mapping.Latest || record.SourceFileID != target.file.ID || mapping.Latest.Path != scope.Root+"/"+target.file.RelativePath {
			continue
		}
		out[target.key] = *mapping.Latest
	}
	return out, rows.Err()
}
