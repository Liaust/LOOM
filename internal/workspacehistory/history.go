// Package workspacehistory reads existing source receipts. It creates neither
// snapshots nor events, and never infers complete history from filesystem mtime.
package workspacehistory

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/notesworkspace"
	"loom.local/loom/internal/workspacefiles"
)

type Service struct {
	DB    *sql.DB
	Files *workspacefiles.Service
}
type Input struct {
	Root, Path, After, Kind, Operation string
	From, To                           time.Time
	Limit                              int
}
type Event struct {
	Root           string    `json:"root"`
	BoxID          string    `json:"box_id,omitempty"`
	BoxAttribution string    `json:"box_attribution"`
	ID             string    `json:"id"`
	FileID         string    `json:"file_id"`
	At             time.Time `json:"at"`
	Kind           string    `json:"kind"`
	State          string    `json:"state"`
	Reason         string    `json:"reason,omitempty"`
	Path           string    `json:"path"`
	Target         string    `json:"target,omitempty"`
	CurrentPath    string    `json:"current_path"`
	BaseID         string    `json:"base_id,omitempty"`
	ResultBaseID   string    `json:"result_base_id,omitempty"`
	Directory      bool      `json:"directory"`
	Entries        int       `json:"entries,omitempty"`
}
type Page struct {
	Items    []Event  `json:"items"`
	Next     string   `json:"next,omitempty"`
	Coverage string   `json:"coverage"`
	Sources  []string `json:"sources"`
}
type cursor struct {
	Root string    `json:"root,omitempty"`
	At   time.Time `json:"at"`
	ID   string    `json:"id"`
}

const coverage = "Recorded source operations only. Dates are immutable receipt creation, not original authorship or final acceptance time. Git, unobserved filesystem changes, semantic milestones and unsent device edits are not included."

func validID(id string) bool {
	return id != "" && len(id) <= 200 && !strings.ContainsAny(id, "\x00\r\n/\\")
}
func normalize(in Input) (Input, cursor, error) {
	var c cursor
	if in.Root == "" || len(in.Path) > 4096 || in.Path != "" && (!fs.ValidPath(in.Path) || strings.ContainsAny(in.Path, "\\\x00")) {
		return in, c, workspacefiles.ErrInvalidPath
	}
	if in.Limit == 0 {
		in.Limit = 100
	}
	if in.Limit < 1 || in.Limit > 200 {
		return in, c, workspacefiles.ErrInvalidPath
	}
	switch in.Kind {
	case "", "edit", "create", "rename", "delete":
	default:
		return in, c, workspacefiles.ErrInvalidPath
	}
	if in.To.IsZero() {
		in.To = time.Now().UTC().Add(time.Second)
	}
	if in.From.IsZero() {
		in.From = in.To.AddDate(0, 0, -90)
	}
	if !in.From.Before(in.To) || in.To.Sub(in.From) > 10*366*24*time.Hour {
		return in, c, workspacefiles.ErrInvalidPath
	}
	if in.After != "" {
		raw, e := base64.RawURLEncoding.DecodeString(in.After)
		if e != nil || len(raw) > 1024 || json.Unmarshal(raw, &c) != nil || !validID(c.ID) || c.At.IsZero() {
			return in, c, workspacefiles.ErrInvalidCursor
		}
	}
	if in.Operation != "" && !validID(in.Operation) {
		return in, c, workspacefiles.ErrInvalidPath
	}
	return in, c, nil
}
func (s Service) scope(ctx context.Context, in Input) (string, string, error) {
	var registered workspacefiles.Root
	for _, candidate := range s.Files.Roots {
		if candidate.Ref == in.Root {
			registered = candidate
			break
		}
	}
	selection, scoped := boxregistry.FromContext(ctx)
	if !registered.IsWritableBox() || scoped && !selection.AllBoxes && (registered.BoxID == "" || !selection.Contains(registered.BoxID)) {
		return "", "", fs.ErrPermission
	}
	root, _, err := s.Files.SourcePath(ctx, in.Root, "")
	if err != nil {
		return "", "", err
	}
	if in.Path != "" {
		if _, err = s.Files.RetainedTarget(in.Root, in.Path); err != nil {
			return "", "", err
		}
	}
	return root, filepath.Join(root, filepath.FromSlash(in.Path)), nil
}
func escaped(value string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(value)
}
func inScope(scope, path string) bool {
	return scope == "" || path == scope || strings.HasPrefix(path, scope+"/")
}

// Scope identity includes earlier path-history rows. A move out of a folder
// does not erase its earlier receipts; hidden/rebuildable paths remain excluded.
const historyQuery = `WITH roots AS (
 SELECT s.scope_id,CASE WHEN s.kind='notes' THEN COALESCE(NULLIF(r.source_path,''),s.source_path) ELSE s.source_path END AS source_path
 FROM notes_workspace.source_scopes s LEFT JOIN knowledge.notes_source_roots r ON s.kind='notes' AND r.notes_source_root_id=s.scope_id
 UNION ALL SELECT r.notes_source_root_id,r.source_path FROM knowledge.notes_source_roots r WHERE NOT EXISTS(SELECT 1 FROM notes_workspace.source_scopes s WHERE s.scope_id=r.notes_source_root_id)
), scoped AS MATERIALIZED (
 SELECT f.file_id,r.source_path,f.record->>'relative_path' AS current_path FROM notes_workspace.files f JOIN roots r ON r.scope_id=f.collection_id
 WHERE (r.source_path=$1 OR r.source_path LIKE $2) AND (
 r.source_path||'/'||(f.record->>'relative_path')=$3 OR r.source_path||'/'||(f.record->>'relative_path') LIKE $4 OR EXISTS(
 SELECT 1 FROM notes_workspace.path_history h WHERE h.file_id=f.file_id AND (
 r.source_path||'/'||(h.record->>'from_path')=$3 OR r.source_path||'/'||(h.record->>'from_path') LIKE $4 OR
 r.source_path||'/'||(h.record->>'to_path')=$3 OR r.source_path||'/'||(h.record->>'to_path') LIKE $4)))
), events AS (
 SELECT o.operation_id,o.file_id,o.created_at,CASE WHEN o.record->'request'->>'kind'='import' THEN 'create' WHEN COALESCE(o.record->'request'->>'kind','') IN('','edit') THEN CASE WHEN COALESCE((o.record->'base'->>'exists')::boolean,false) THEN 'edit' ELSE 'create' END ELSE o.record->'request'->>'kind' END AS kind,
 o.state,COALESCE(o.record->>'reason','') AS reason,s.source_path,COALESCE(o.record->'file'->>'relative_path','') AS old_path,
 COALESCE(o.record->'result_file'->>'relative_path','') AS target_path,s.current_path,
 COALESCE(o.record->'request'->>'base_id','') AS base_id,COALESCE(o.record->>'result_base_id','') AS result_base_id,COALESCE(o.record->'file'->>'entry_kind','')='directory' AS directory,0 AS entries,COALESCE(o.record->'request'->>'box_id','') AS box_id
 FROM notes_workspace.operations o JOIN scoped s USING(file_id)
 WHERE COALESCE(o.record->'request'->>'kind','')<>'tree_move' AND o.created_at>=$5 AND o.created_at<$6 AND ($7::timestamptz IS NULL OR (o.created_at,o.operation_id)<($7::timestamptz,$8)) AND ($9='' OR o.operation_id=$9)
 UNION ALL
 SELECT m.move_id,COALESCE(m.record->'file'->>'file_id',''),m.created_at,'rename',m.state,COALESCE(m.record->>'reason',''),r.source_path,
 m.record->>'from',m.record->>'to',m.record->>'to','','',true,COALESCE((m.record->>'entries')::integer,0),COALESCE(m.record->>'box_id','')
 FROM notes_workspace.tree_moves m JOIN roots r ON r.scope_id=m.scope_id
 WHERE (r.source_path=$1 OR r.source_path LIKE $2) AND m.created_at>=$5 AND m.created_at<$6
 AND ($7::timestamptz IS NULL OR (m.created_at,m.move_id)<($7::timestamptz,$8)) AND ($9='' OR m.move_id=$9)
 AND (r.source_path||'/'||(m.record->>'from')=$3 OR r.source_path||'/'||(m.record->>'from') LIKE $4
 OR r.source_path||'/'||(m.record->>'to')=$3 OR r.source_path||'/'||(m.record->>'to') LIKE $4
 OR left($3,length(r.source_path||'/'||(m.record->>'from'))+1)=r.source_path||'/'||(m.record->>'from')||'/'
 OR left($3,length(r.source_path||'/'||(m.record->>'to'))+1)=r.source_path||'/'||(m.record->>'to')||'/')
) SELECT * FROM events WHERE ($10='' OR kind=$10) ORDER BY created_at DESC,operation_id DESC LIMIT $11`

func (s Service) List(ctx context.Context, input Input) (Page, error) {
	page := Page{Items: []Event{}, Coverage: coverage, Sources: []string{"Notes source receipts and recorded path changes"}}
	in, c, err := normalize(input)
	if err != nil {
		return page, err
	}
	root, scope, err := s.scope(ctx, in)
	if err != nil {
		return page, err
	}
	if in.After != "" && c.Root != in.Root && !(c.Root == "" && in.Root == "box") {
		return page, workspacefiles.ErrInvalidCursor
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout='3s'`); err != nil {
		return page, err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL jit=off`); err != nil {
		return page, err
	}
	var at any
	if !c.At.IsZero() {
		at = c.At
	}
	rows, err := tx.QueryContext(ctx, historyQuery, root, escaped(root)+"/%", scope, escaped(scope)+"/%", in.From, in.To, at, c.ID, in.Operation, in.Kind, in.Limit+1)
	if err != nil {
		return page, err
	}
	count := 0
	var last cursor
	for rows.Next() {
		var e Event
		var source, old, target, current string
		if err = rows.Scan(&e.ID, &e.FileID, &e.At, &e.Kind, &e.State, &e.Reason, &source, &old, &target, &current, &e.BaseID, &e.ResultBaseID, &e.Directory, &e.Entries, &e.BoxID); err != nil {
			break
		}
		if count == in.Limit {
			raw, _ := json.Marshal(last)
			page.Next = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		count++
		last = cursor{At: e.At, ID: e.ID, Root: in.Root}
		e.Root = in.Root
		e.BoxAttribution = "recorded"
		if e.BoxID == "" {
			e.BoxAttribution = "unattributed"
		}
		relative := func(p string) string {
			if p == "" {
				return ""
			}
			abs := filepath.Join(source, filepath.FromSlash(p))
			rel, er := filepath.Rel(root, abs)
			if er != nil {
				return ""
			}
			if _, er = s.Files.RetainedTarget(in.Root, filepath.ToSlash(rel)); er != nil {
				return ""
			}
			return filepath.ToSlash(rel)
		}
		e.Path, e.Target, e.CurrentPath = relative(old), relative(target), relative(current)
		if e.Kind != "rename" {
			e.Target = ""
		}
		if e.Path == "" || current != "" && e.CurrentPath == "" {
			continue
		}
		page.Items = append(page.Items, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return page, err
	}
	return page, tx.Commit()
}

type Comparison struct {
	Event     Event  `json:"event"`
	Available bool   `json:"available"`
	Before    string `json:"before"`
	After     string `json:"after"`
	Reason    string `json:"reason,omitempty"`
}

func (s Service) Compare(ctx context.Context, input Input) (Comparison, error) {
	var result Comparison
	if !validID(input.Operation) {
		return result, workspacefiles.ErrInvalidPath
	}
	input.To = time.Now().UTC().Add(time.Second)
	// The operation lookup uses the same scope filter as the timeline, not a raw
	// base-ID read supplied by the client. No retained bytes cross another scope.
	input.From = input.To.AddDate(-9, 0, 0)
	input.After = ""
	input.Limit = 1
	page, err := s.List(ctx, input)
	if err != nil {
		return result, err
	}
	if len(page.Items) != 1 {
		return result, sql.ErrNoRows
	}
	e := page.Items[0]
	result.Event = e
	if e.Directory || e.State != notesworkspace.Accepted || e.BaseID == "" || e.ResultBaseID == "" {
		result.Reason = "No complete accepted text pair is retained for this receipt."
		return result, nil
	}
	store := notesworkspace.SQLStore{DB: s.DB}
	texts := []string{}
	for _, id := range []string{e.BaseID, e.ResultBaseID} {
		var encoded int64
		var binary bool
		err = s.DB.QueryRowContext(ctx, `SELECT length(COALESCE(record->>'content','')),reference_payload_hash IS NOT NULL FROM notes_workspace.bases WHERE base_id=$1 AND file_id=$2`, id, e.FileID).Scan(&encoded, &binary)
		if err != nil {
			return result, err
		}
		if binary || encoded > 350000 {
			result.Reason = "Binary or large retained revisions are metadata-only (text comparison limit: 256 KiB per revision)."
			return result, nil
		}
		b, er := store.Base(ctx, id)
		if er != nil {
			return result, er
		}
		if len(b.Content) > 256<<10 || !utf8.Valid(b.Content) || strings.ContainsRune(string(b.Content), 0) {
			result.Reason = "This retained revision is not bounded UTF-8 text."
			return result, nil
		}
		texts = append(texts, string(b.Content))
	}
	result.Available = true
	result.Before = texts[0]
	result.After = texts[1]
	return result, nil
}

func ParseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return parsed, fmt.Errorf("invalid timeline date")
	}
	return parsed, nil
}
