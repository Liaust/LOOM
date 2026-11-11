package loomdapp

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/notesworkspace"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/workspacefiles"
)

// The adapter only composes existing SQL owners; no PostgreSQL runtime is used.
type pathNotesSQL struct {
	scopes             map[string]notesworkspace.SourceScope
	kinds              map[string]string
	file               notesworkspace.File
	box                string
	busy               [][]driver.Value
	prepared           string
	excepts            []string
	locks, writes      []string
	commits, rollbacks int
}
type pathNotesConnector struct{ state *pathNotesSQL }
type pathNotesDriver struct{}
type pathNotesConn struct{ state *pathNotesSQL }
type pathNotesTx struct{ state *pathNotesSQL }
type pathNotesRows struct {
	names []string
	data  [][]driver.Value
}

func (pathNotesDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }
func (pathNotesConnector) Driver() driver.Driver         { return pathNotesDriver{} }
func (c pathNotesConnector) Connect(context.Context) (driver.Conn, error) {
	return pathNotesConn{c.state}, nil
}
func (pathNotesConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (pathNotesConn) Close() error                { return nil }
func (c pathNotesConn) Begin() (driver.Tx, error) { return pathNotesTx{c.state}, nil }
func (t pathNotesTx) Commit() error               { t.state.commits++; return nil }
func (t pathNotesTx) Rollback() error             { t.state.rollbacks++; return nil }
func (r *pathNotesRows) Columns() []string        { return r.names }
func (*pathNotesRows) Close() error               { return nil }
func (r *pathNotesRows) Next(dst []driver.Value) error {
	if len(r.data) == 0 {
		return io.EOF
	}
	copy(dst, r.data[0])
	r.data = r.data[1:]
	return nil
}
func pathNotesResult(columns int, data ...[]driver.Value) driver.Rows {
	names := make([]string, columns)
	for i := range names {
		names[i] = fmt.Sprint(i)
	}
	return &pathNotesRows{names, data}
}

func (c pathNotesConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	s := c.state
	if strings.Contains(q, "pg_advisory_lock") {
		s.locks = append(s.locks, q+":"+args[0].Value.(string))
		return driver.RowsAffected(1), nil
	}
	s.writes = append(s.writes, q)
	if strings.HasPrefix(q, "INSERT INTO notes_workspace.source_scopes") {
		id := args[0].Value.(string)
		if _, exists := s.scopes[id]; !exists {
			s.scopes[id] = notesworkspace.SourceScope{ID: id, Path: args[1].Value.(string), NodeKey: args[2].Value.(string), RootIdentity: args[3].Value.(string)}
			s.kinds[id] = "box"
			if strings.Contains(q, "'retained'") {
				s.kinds[id] = "retained"
			}
		}
		return driver.RowsAffected(1), nil
	}
	for _, prefix := range []string{"UPDATE notes_workspace.files", "INSERT INTO notes_workspace.operations", "INSERT INTO notes_workspace.path_history", "UPDATE notes_workspace.operations"} {
		if strings.HasPrefix(q, prefix) {
			return driver.RowsAffected(1), nil
		}
	}
	return nil, fmt.Errorf("unexpected write: %s", q)
}

func (c pathNotesConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	s := c.state
	switch {
	case strings.Contains(q, "pg_advisory_unlock"):
		s.locks = append(s.locks, q+":"+args[0].Value.(string))
		return pathNotesResult(1, []driver.Value{true}), nil
	case strings.Contains(q, "SELECT kind,source_path,node_key,root_identity"):
		id := args[0].Value.(string)
		v := s.scopes[id]
		return pathNotesResult(4, []driver.Value{s.kinds[id], v.Path, v.NodeKey, v.RootIdentity}), nil
	case strings.Contains(q, "WHERE kind='retained'"):
		var rows [][]driver.Value
		for id, scope := range s.scopes {
			if s.kinds[id] == "retained" {
				rows = append(rows, []driver.Value{id, scope.Path, scope.NodeKey, scope.RootIdentity})
			}
		}
		return pathNotesResult(4, rows...), nil
	case strings.Contains(q, "SELECT f.record,CASE"):
		raw, _ := json.Marshal(s.file)
		return pathNotesResult(2, []driver.Value{raw, s.box}), nil
	case strings.Contains(q, "SELECT o.operation_id,o.state"):
		return pathNotesResult(4), nil
	case strings.Contains(q, "SELECT CASE WHEN s.kind='notes'"):
		v := s.scopes[args[0].Value.(string)]
		return pathNotesResult(2, []driver.Value{v.Path, v.NodeKey}), nil
	case strings.Contains(q, "SELECT EXISTS(SELECT 1 FROM notes_workspace.operations"):
		except := args[2].Value.(string)
		s.excepts = append(s.excepts, except)
		return pathNotesResult(1, []driver.Value{s.prepared != "" && s.prepared != except}), nil
	case strings.Contains(q, "FROM notes_workspace.operations o JOIN notes_workspace.files"):
		return pathNotesResult(4), nil
	case strings.Contains(q, "SELECT record FROM notes_workspace.files"):
		return pathNotesResult(1), nil
	case strings.Contains(q, "SELECT notes_source_root_id,source_path"):
		return pathNotesResult(2), nil
	case strings.Contains(q, "WITH scopes AS"):
		return pathNotesResult(6, s.busy...), nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", q)
	}
}

type pathNotesFixture struct {
	db    *sql.DB
	files *workspacefiles.Service
	roots storagearchive.TrustedWorkspaceRoots
	state *pathNotesSQL
}

func newPathNotesFixture(t *testing.T) pathNotesFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	roots := storagearchive.TrustedWorkspaceRoots{BoxRoot: filepath.Join(root, "Box"), StorageRoot: filepath.Join(root, "Storage")}
	for _, directory := range []string{filepath.Join(roots.BoxRoot, "Projects", "example", "data"), roots.StorageRoot} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(roots.BoxRoot, "Projects", "example", "data", "note.md"), []byte("pending edits stay separate"), 0600); err != nil {
		t.Fatal(err)
	}
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{{Ref: "box", SourcePath: roots.BoxRoot, Node: "main"}, {Ref: "storage", SourcePath: roots.StorageRoot, Node: "main", ReadOnly: true}}}
	t.Cleanup(func() { files.Close() })
	box, err := files.MutationSource(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	state := &pathNotesSQL{scopes: map[string]notesworkspace.SourceScope{}, kinds: map[string]string{}, box: roots.BoxRoot,
		file: notesworkspace.File{ID: "existing-file", CollectionID: box.CollectionID, RelativePath: "Projects/example/data/note.md", PathKey: "projects/example/data/note.md"}}
	db := sql.OpenDB(pathNotesConnector{state})
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { db.Close() })
	return pathNotesFixture{db: db, files: files, roots: roots, state: state}
}

func (f pathNotesFixture) plan(kind string) storagearchive.WorkspacePathPlan {
	from := "Projects/example"
	to := "Projects/renamed"
	destRoot := storagearchive.WorkspaceRootBox
	destBase := f.roots.BoxRoot
	if kind == "retain_child" {
		from, to, destRoot, destBase = "Projects/example/data", "Records/example-data", storagearchive.WorkspaceRootStorage, f.roots.StorageRoot
	}
	return storagearchive.WorkspacePathPlan{Schema: storagearchive.WorkspacePathSchema, Kind: kind, ProjectID: "project-one", Move: storagearchive.RetainedRelocationPlan{Schema: storagearchive.RetainedRelocationSchema, ID: "workspace_path_operation_one",
		Source:      storagearchive.WorkspacePathRef{Root: storagearchive.WorkspaceRootBox, RelativePath: from, AbsolutePath: filepath.Join(f.roots.BoxRoot, from)},
		Destination: storagearchive.WorkspacePathRef{Root: destRoot, RelativePath: to, AbsolutePath: filepath.Join(destBase, to)}}}
}

func TestWorkspacePathNotesPlansAndCommitsUnderCallerLocks(t *testing.T) {
	for _, kind := range []string{"relocate_project", "retain_child"} {
		t.Run(kind, func(t *testing.T) {
			f := newPathNotesFixture(t)
			withLock, commit := workspacePathNotesRuntime(f.db, f.files, f.roots, "main")
			p := f.plan(kind)
			var escaped storagearchive.WorkspacePathNotesOwner
			err := withLock(t.Context(), func(owner storagearchive.WorkspacePathNotesOwner) error {
				escaped = owner
				var err error
				p.Notes, err = owner.Plan(t.Context(), p)
				if err != nil {
					return err
				}
				notes, err := decodeWorkspacePathNotes(p.Notes)
				if err != nil || notes.Kind != kind {
					t.Fatalf("Notes wrapper: %+v %v", notes, err)
				}
				if kind == "relocate_project" {
					if notes.Location == nil || len(notes.Location.Bindings) != 1 || notes.Location.From != p.Move.Source.RelativePath || notes.Location.SourcePath != f.roots.BoxRoot || notes.Location.Bindings[0].Retire {
						t.Fatalf("location plan: %+v", notes.Location)
					}
				} else if notes.Retained == nil || len(notes.Retained.Files) != 1 || notes.Retained.From != p.Move.Source.AbsolutePath || notes.Retained.Scope.Path != f.roots.StorageRoot {
					t.Fatalf("retained plan: %+v", notes.Retained)
				}
				f.state.writes = nil
				tx, err := f.db.BeginTx(t.Context(), nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				if err := commit(t.Context(), tx, p); err != nil {
					return err
				}
				if f.state.commits != 0 || f.state.rollbacks != 0 || len(f.state.writes) != 3 {
					t.Fatal("commit hook took transaction ownership or wrote extra state")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(f.state.locks) != 4 || !strings.Contains(f.state.locks[0], "pg_advisory_lock(") || !strings.HasSuffix(f.state.locks[0], ":notesworkspace:filesystem") {
				t.Fatalf("exclusive lock lifecycle: %v", f.state.locks)
			}
			if _, err := escaped.Plan(t.Context(), p); !errors.Is(err, notesworkspace.ErrMembership) {
				t.Fatalf("lock owner escaped its lifetime: %v", err)
			}
			if b, err := os.ReadFile(filepath.Join(f.roots.BoxRoot, "Projects/example/data/note.md")); err != nil || string(b) != "pending edits stay separate" {
				t.Fatal("adapter changed source bytes")
			}
			if _, err := os.Lstat(p.Move.Destination.AbsolutePath); !os.IsNotExist(err) {
				t.Fatal("adapter created a physical destination")
			}
		})
	}
}

func TestWorkspacePathNotesRecoveryExceptsOnlySameOperation(t *testing.T) {
	for _, kind := range []string{"relocate_project", "retain_child"} {
		t.Run(kind, func(t *testing.T) {
			f := newPathNotesFixture(t)
			withLock, _ := workspacePathNotesRuntime(f.db, f.files, f.roots, "main")
			p := f.plan(kind)
			f.state.prepared = p.Move.ID
			if kind == "retain_child" {
				f.state.busy = [][]driver.Value{{"tree", p.Move.ID, f.roots.BoxRoot, "main", p.Move.Source.RelativePath, ""}}
			}
			err := withLock(t.Context(), func(owner storagearchive.WorkspacePathNotesOwner) error {
				before, err := owner.Plan(t.Context(), p)
				if err != nil {
					return err
				}
				// An interrupted physical rename leaves Notes metadata at its old
				// paths until the storage owner's final transaction commits it.
				if err := os.MkdirAll(filepath.Dir(p.Move.Destination.AbsolutePath), 0700); err != nil {
					return err
				}
				if err := os.Rename(p.Move.Source.AbsolutePath, p.Move.Destination.AbsolutePath); err != nil {
					return err
				}
				after, err := owner.Plan(t.Context(), p)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("same-operation recovery changed the Notes review: %v", err)
				}
				f.state.prepared = "different-operation"
				f.state.busy = append(f.state.busy, []driver.Value{"operation", "pending-edit", f.roots.BoxRoot, "main", "Projects/example/data/note.md", ""})
				if _, err := owner.Plan(t.Context(), p); !errors.Is(err, notesworkspace.ErrPathBusy) {
					t.Fatalf("pending work was not held: %v", err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, except := range f.state.excepts {
				if except != p.Move.ID {
					t.Fatal("recovery did not use the exact operation ID")
				}
			}
		})
	}
}

func TestWorkspacePathNotesRetainedScopeDoesNotRebind(t *testing.T) {
	f := newPathNotesFixture(t)
	withLock, _ := workspacePathNotesRuntime(f.db, f.files, f.roots, "main")
	p := f.plan("retain_child")
	err := withLock(t.Context(), func(owner storagearchive.WorkspacePathNotesOwner) error {
		raw, err := owner.Plan(t.Context(), p)
		if err != nil {
			return err
		}
		notes, err := decodeWorkspacePathNotes(raw)
		if err != nil {
			return err
		}
		id := notes.Retained.Scope.ID
		previous := f.state.scopes[id]
		previous.RootIdentity = "previous generation must survive"
		f.state.scopes[id] = previous
		if _, err := owner.Plan(t.Context(), p); !errors.Is(err, notesworkspace.ErrMembership) {
			t.Fatalf("retained scope silently rebound: %v", err)
		}
		if f.state.scopes[id] != previous || len(f.state.scopes) != 2 {
			t.Fatal("retained scope was replaced or duplicated")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspacePathNotesStrictDecodeAndCommitBinding(t *testing.T) {
	for _, kind := range []string{"relocate_project", "retain_child"} {
		t.Run(kind, func(t *testing.T) {
			f := newPathNotesFixture(t)
			withLock, commit := workspacePathNotesRuntime(f.db, f.files, f.roots, "main")
			p := f.plan(kind)
			if err := withLock(t.Context(), func(owner storagearchive.WorkspacePathNotesOwner) error {
				var err error
				p.Notes, err = owner.Plan(t.Context(), p)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for _, variant := range []string{"unknown field", "trailing JSON", "kind", "ID", "root", "payload"} {
				t.Run(variant, func(t *testing.T) {
					v := p
					notes, _ := decodeWorkspacePathNotes(p.Notes)
					switch variant {
					case "unknown field":
						v.Notes = append([]byte(`{"unexpected":true,`), p.Notes[1:]...)
					case "trailing JSON":
						v.Notes = append(append([]byte(nil), p.Notes...), []byte(` {}`)...)
					case "kind":
						notes.Kind = "unknown"
					case "ID":
						v.Move.ID += "-other"
					case "root":
						v.Move.Source.AbsolutePath = filepath.Join(f.roots.StorageRoot, "Projects/example")
					case "payload":
						if notes.Location != nil {
							notes.Location.Generation += "-changed"
						} else {
							notes.Retained.Scope.RootIdentity += "-changed"
						}
					}
					if variant != "unknown field" && variant != "trailing JSON" {
						v.Notes, _ = json.Marshal(notes)
					}
					f.state.writes = nil
					tx, err := f.db.BeginTx(t.Context(), nil)
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback()
					if err := commit(t.Context(), tx, v); err == nil || len(f.state.writes) != 0 {
						t.Fatalf("invalid payload mutated Notes: %v", err)
					}
				})
			}
		})
	}
	for _, raw := range []string{`null`, `{}`, `{"schema":"notes_workspace.path_operation.v1","kind":"retain_child","location":{}}`, `{"schema":"notes_workspace.path_operation.v1","kind":"retain_child","retained":{},"location":{}}`} {
		if _, err := decodeWorkspacePathNotes(json.RawMessage(raw)); err == nil {
			t.Fatalf("ambiguous Notes shape accepted: %s", raw)
		}
	}
}

func TestWorkspacePathNotesFailsClosedAndReleasesLock(t *testing.T) {
	f := newPathNotesFixture(t)
	for _, node := range []string{"", "different-node"} {
		withLock, _ := workspacePathNotesRuntime(f.db, f.files, f.roots, node)
		if err := withLock(t.Context(), func(storagearchive.WorkspacePathNotesOwner) error { t.Fatal("invalid owner admitted"); return nil }); err == nil {
			t.Fatal("invalid node accepted")
		}
	}
	withLock, _ := workspacePathNotesRuntime(nil, f.files, f.roots, "main")
	if err := withLock(t.Context(), func(storagearchive.WorkspacePathNotesOwner) error { t.Fatal("nil database admitted"); return nil }); err == nil {
		t.Fatal("unconfigured adapter accepted")
	}
	withLock, _ = workspacePathNotesRuntime(f.db, f.files, f.roots, "main")
	stop := errors.New("callback stopped")
	f.state.locks = nil
	if err := withLock(t.Context(), func(storagearchive.WorkspacePathNotesOwner) error { return stop }); !errors.Is(err, stop) || len(f.state.locks) != 4 {
		t.Fatalf("callback error lost locks or cause: %v %v", err, f.state.locks)
	}
}
