package httpapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/notesworkspace"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workspacefiles"
)

const companyFileRef = "box:box_company"

func boxMutationFixture(t *testing.T) (Server, string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	personal, company := filepath.Join(base, "personal"), filepath.Join(base, "company")
	for _, root := range []string{personal, company} {
		if err := os.MkdirAll(filepath.Join(root, "Notes", "old"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "Notes", "note.md"), []byte(root), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{
		{Ref: "box", BoxID: "box_personal", SourceKind: "box", SourcePath: personal, Node: "main"},
		{Ref: companyFileRef, BoxID: "box_company", SourceKind: "box", SourcePath: company, Node: "main"},
		{Ref: "box:box_unavailable", BoxID: "box_unavailable", SourceKind: "box", SourcePath: filepath.Join(base, "unavailable"), Node: "main"},
	}}
	t.Cleanup(func() { _ = files.Close() })
	catalog := &boxregistry.Catalog{DefaultBox: "personal", Boxes: []boxregistry.Box{
		{ID: "box_personal", Key: "personal", RootRef: "box", RootPath: personal, Primary: true, Available: true},
		{ID: "box_company", Key: "company", RootRef: companyFileRef, RootPath: company, Available: true},
		{ID: "box_unavailable", Key: "unavailable", RootRef: "box:box_unavailable", RootPath: filepath.Join(base, "unavailable"), Reason: "root_missing"},
	}}
	s := NewServer(Services{Files: files, Boxes: catalog, RuntimeConfig: config.Config{NodeID: "main"}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return s, personal, company
}

func mutationRequest(t *testing.T, handler http.Handler, method, url string, body any, defaultBox string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, url, strings.NewReader(string(raw)))
	r = r.WithContext(context.WithValue(r.Context(), localFileTransportKey{}, true))
	if defaultBox != "" {
		r.Header.Set("X-Loom-Box", defaultBox)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestMultiBoxDesktopSourceUsesRequestedRoot(t *testing.T) {
	s, personal, company := boxMutationFixture(t)
	selection, err := s.services.Boxes.Select(boxregistry.Selection{}, "personal")
	if err != nil {
		t.Fatal(err)
	}
	ctx := boxregistry.WithSelection(t.Context(), selection)
	ctx, source, relative, err := s.desktopFilesystemSource(ctx, companyFileRef, "Notes/note.md")
	if err != nil || source.Path != company || relative != "Notes/note.md" {
		t.Fatal(source, relative, err)
	}
	if selected, _ := boxregistry.FromContext(ctx); !selected.Contains("box_company") || selected.Contains("box_personal") {
		t.Fatal("mutable default replaced request binding")
	}
	_, personalSource, _, err := s.desktopFilesystemSource(t.Context(), "box", "Notes/note.md")
	if err != nil || personalSource.Path != personal || personalSource.CollectionID == source.CollectionID || personalSource.Generation == source.Generation {
		t.Fatal("Box source identities mixed", err)
	}
	explicit, _ := s.services.Boxes.Select(boxregistry.Selection{BoxIDs: []string{"box_personal"}}, "")
	if _, _, _, err := s.desktopFilesystemSource(boxregistry.WithSelection(t.Context(), explicit), companyFileRef, "Notes/note.md"); err == nil {
		t.Fatal("explicit selection mismatch admitted")
	}
	for _, ref := range []string{"box:box_unknown", "box:box_unavailable", "storage"} {
		if _, _, _, err := s.desktopFilesystemSource(t.Context(), ref, "Notes/note.md"); err == nil {
			t.Fatal("unknown/unavailable/non-Box root admitted", ref)
		}
	}
	if _, _, _, err := s.legacyDesktopSource(t.Context(), companyFileRef, "Notes/note.md"); !errors.Is(err, notesworkspace.ErrMembership) {
		t.Fatal("Company fell back to Personal legacy resolver", err)
	}
}

func TestMultiBoxFileMutationHTTPAdmission(t *testing.T) {
	s, _, _ := boxMutationFixture(t)
	action := fileActionInput{Root: companyFileRef, Path: "Notes/note.md", OperationID: "desktop_action_company", FileID: "file", BaseID: "base", Generation: "filesystem:base", Kind: "delete"}
	note := desktopMutation{Root: companyFileRef, Path: "Notes/note.md", OperationID: "desktop_note_company", FileID: "file", BaseID: "base", Generation: "filesystem:base", Kind: "edit", Text: "company edit"}
	tree := fileTreeMoveInput{Root: companyFileRef, OperationID: "desktop_tree_company", From: "Notes/old", To: "Notes/new"}
	for _, endpoint := range []struct {
		path  string
		input any
	}{{"actions", action}, {"note", note}, {"tree-move", tree}} {
		for _, tc := range []struct {
			query  string
			status int
		}{{"", 409}, {"?box=personal", 403}, {"?box=company", 409}, {"?root=box", 403}} {
			w := mutationRequest(t, s.Handler(), "POST", "/v1/files/"+endpoint.path+tc.query, endpoint.input, "personal")
			if w.Code != tc.status {
				t.Fatalf("%s%s: %d %s", endpoint.path, tc.query, w.Code, w.Body.String())
			}
		}
	}
	for _, endpoint := range []string{"actions", "note"} {
		for _, tc := range []struct {
			ref    string
			status int
		}{{companyFileRef, 409}, {"box:box_unknown", 404}, {"box:box_unavailable", 503}} {
			w := mutationRequest(t, s.Handler(), "GET", "/v1/files/"+endpoint+"?root="+tc.ref+"&path=Notes/note.md", nil, "personal")
			if w.Code != tc.status {
				t.Fatalf("%s root=%s: %d %s", endpoint, tc.ref, w.Code, w.Body.String())
			}
		}
	}
	legacyTree := tree
	legacyTree.Root = ""
	if w := mutationRequest(t, s.Handler(), "POST", "/v1/files/tree-move", legacyTree, "company"); w.Code != 409 {
		t.Fatalf("legacy Personal shape rejected: %d %s", w.Code, w.Body.String())
	}
	note.BoxID = "box_personal"
	if w := mutationRequest(t, s.Handler(), "POST", "/v1/files/note", note, ""); w.Code != 409 {
		t.Fatal("mismatched body BoxID admitted", w.Code)
	}
	s.services.Boxes = nil
	if _, _, _, err := s.desktopSource(t.Context(), companyFileRef, "Notes/note.md"); !errors.Is(err, boxregistry.ErrUnavailable) {
		t.Fatal("Company admitted without catalog", err)
	}
}

// This read-only connector exercises terminal HTTP replay against saved SQL
// records. No database server, writer implementation or transaction is needed.
type boxReceiptConnector struct {
	operations map[string]notesworkspace.Operation
	trees      map[string]notesworkspace.TreeMove
	uploads    map[string]notesworkspace.Upload
	scopes     map[string]string
	writes     int
}
type boxReceiptConn struct{ data *boxReceiptConnector }
type boxReceiptDriver struct{}

func (boxReceiptDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use fixture connector")
}
func (c *boxReceiptConnector) Driver() driver.Driver { return boxReceiptDriver{} }
func (c *boxReceiptConnector) Connect(context.Context) (driver.Conn, error) {
	return &boxReceiptConn{c}, nil
}
func (c *boxReceiptConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("receipt fixture forbids prepare")
}
func (c *boxReceiptConn) Close() error { return nil }
func (c *boxReceiptConn) Begin() (driver.Tx, error) {
	c.data.writes++
	return nil, errors.New("receipt fixture forbids transactions")
}
func (c *boxReceiptConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.data.writes++
	return nil, errors.New("receipt fixture forbids writes and locks")
}
func (c *boxReceiptConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("unexpected receipt query args")
	}
	id, _ := args[0].Value.(string)
	var record any
	var exists bool
	switch {
	case strings.Contains(query, "FROM notes_workspace.operations"):
		record, exists = c.data.operations[id]
	case strings.Contains(query, "FROM notes_workspace.tree_moves"):
		record, exists = c.data.trees[id]
	case strings.Contains(query, "FROM notes_workspace.desktop_imports"):
		record, exists = c.data.uploads[id]
	case strings.Contains(query, "FROM notes_workspace.source_scopes"):
		root, ok := c.data.scopes[id]
		rows := &httpStorageFakeRows{columns: []string{"source_path", "node_key"}}
		if ok {
			rows.rows = [][]driver.Value{{root, "main"}}
		}
		return rows, nil
	default:
		return nil, fmt.Errorf("unexpected receipt query")
	}
	rows := &httpStorageFakeRows{columns: []string{"record"}}
	if exists {
		raw, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		rows.rows = [][]driver.Value{{raw}}
	}
	return rows, nil
}

func installBoxReceiptStore(t *testing.T, s *Server) *boxReceiptConnector {
	t.Helper()
	c := &boxReceiptConnector{operations: map[string]notesworkspace.Operation{}, trees: map[string]notesworkspace.TreeMove{}, uploads: map[string]notesworkspace.Upload{}, scopes: map[string]string{}}
	s.services.DB = sql.OpenDB(c)
	t.Cleanup(func() { _ = s.services.DB.Close() })
	return c
}

func TestMultiBoxTerminalFileReceiptsKeepOriginalRoot(t *testing.T) {
	s, personal, company := boxMutationFixture(t)
	ctx, source, _, err := s.desktopFilesystemSource(t.Context(), companyFileRef, "Notes/note.md")
	if err != nil {
		t.Fatal(err)
	}
	db := installBoxReceiptStore(t, &s)
	db.scopes[source.CollectionID] = company
	action := fileActionInput{Root: companyFileRef, BoxID: "box_company", Path: "Notes/old/asset.bin", OperationID: "desktop_action_saved_company", FileID: "file", BaseID: "base", Generation: source.Generation, Kind: "rename", Target: "Notes/new.bin"}
	op := notesworkspace.Operation{State: notesworkspace.Accepted, File: notesworkspace.File{ID: action.FileID, CollectionID: source.CollectionID, RelativePath: action.Path}, Base: notesworkspace.Base{MetadataAction: &notesworkspace.MetadataActionIdentity{}}, Request: notesworkspace.Request{BoxID: "box_company", OperationID: action.OperationID, FileID: action.FileID, BaseID: action.BaseID, Generation: action.Generation, Kind: action.Kind, DestinationPath: action.Target}}
	db.operations[action.OperationID] = op
	note := desktopMutation{Root: companyFileRef, Path: "Notes/old/note.md", OperationID: "desktop_note_saved_company", FileID: "note", BaseID: "note_base", Generation: source.Generation, Kind: "edit", Text: "saved company text"}
	db.operations[note.OperationID] = notesworkspace.Operation{State: notesworkspace.Conflict, File: notesworkspace.File{ID: note.FileID, CollectionID: source.CollectionID, RelativePath: note.Path}, Request: notesworkspace.Request{BoxID: "box_company", OperationID: note.OperationID, FileID: note.FileID, BaseID: note.BaseID, Generation: note.Generation, Kind: note.Kind, Content: []byte(note.Text)}}
	tree := fileTreeMoveInput{Root: companyFileRef, OperationID: "desktop_tree_saved_company", From: "Notes/old", To: "Notes/new", PlanHash: strings.Repeat("a", 64), Confirm: true}
	db.trees[tree.OperationID] = notesworkspace.TreeMove{BoxID: "box_company", ID: tree.OperationID, ScopeID: source.CollectionID, Generation: source.Generation, SourcePath: company, From: tree.From, To: tree.To, Hash: tree.PlanHash, State: notesworkspace.Conflict}
	if err := os.Remove(filepath.Join(company, "Notes", "old")); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []struct {
		path  string
		input any
	}{{"actions", action}, {"note", note}, {"tree-move", tree}} {
		w := mutationRequest(t, s.Handler(), "POST", "/v1/files/"+endpoint.path, endpoint.input, "personal")
		var envelope response.Envelope[map[string]any]
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || w.Code != 200 || envelope.Data["root"] != companyFileRef || envelope.Data["box_id"] != "box_company" {
			t.Fatalf("%s receipt: %d %s %v", endpoint.path, w.Code, w.Body.String(), err)
		}
		w = mutationRequest(t, s.Handler(), "POST", "/v1/files/"+endpoint.path+"?box=personal", endpoint.input, "")
		if w.Code != 403 {
			t.Fatalf("foreign receipt selection accepted: %d %s", w.Code, w.Body.String())
		}
	}
	action.Root, action.BoxID = "box", ""
	if w := mutationRequest(t, s.Handler(), "POST", "/v1/files/actions", action, ""); w.Code != 409 {
		t.Fatal("Company receipt replayed as Personal", w.Code)
	}
	if !s.desktopReceiptSourceMatches(ctx, companyFileRef, company, source.CollectionID, source.Generation) || s.desktopReceiptSourceMatches(ctx, companyFileRef, personal, source.CollectionID, source.Generation) {
		t.Fatal("receipt source binding changed")
	}
	if db.writes != 0 {
		t.Fatal("terminal replay attempted writes", db.writes)
	}
}

func TestMultiBoxPersonalHistoricalReceiptsStillReplay(t *testing.T) {
	s, personal, _ := boxMutationFixture(t)
	db := installBoxReceiptStore(t, &s)
	db.scopes["legacy_notes"] = filepath.Join(personal, "Notes")
	note := desktopMutation{Root: "box", Path: "Notes/old/note.md", OperationID: "desktop_note_old_personal", FileID: "note", BaseID: "base", Generation: "historical-selection-generation", Kind: "edit", Text: "old Personal edit"}
	db.operations[note.OperationID] = notesworkspace.Operation{State: notesworkspace.Conflict, File: notesworkspace.File{ID: note.FileID, CollectionID: "legacy_notes", RelativePath: "old/note.md"}, Request: notesworkspace.Request{OperationID: note.OperationID, FileID: note.FileID, BaseID: note.BaseID, Generation: note.Generation, Kind: note.Kind, Content: []byte(note.Text)}}
	action := fileActionInput{Root: "box", Path: "Notes/old/file.bin", OperationID: "desktop_action_old_personal", FileID: "asset", BaseID: "base", Generation: note.Generation, Kind: "delete"}
	db.operations[action.OperationID] = notesworkspace.Operation{State: notesworkspace.Conflict, File: notesworkspace.File{ID: action.FileID, CollectionID: "legacy_notes", RelativePath: "old/file.bin"}, Base: notesworkspace.Base{MetadataAction: &notesworkspace.MetadataActionIdentity{}}, Request: notesworkspace.Request{OperationID: action.OperationID, FileID: action.FileID, BaseID: action.BaseID, Generation: action.Generation, Kind: action.Kind}}
	tree := fileTreeMoveInput{OperationID: "desktop_tree_old_personal", From: "Notes/old", To: "Notes/new", PlanHash: strings.Repeat("b", 64), Confirm: true}
	db.trees[tree.OperationID] = notesworkspace.TreeMove{ID: tree.OperationID, ScopeID: "legacy_notes", Generation: note.Generation, SourcePath: personal, From: tree.From, To: tree.To, Hash: tree.PlanHash, State: notesworkspace.Conflict}
	if err := os.Remove(filepath.Join(personal, "Notes", "old")); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []struct {
		path  string
		input any
	}{{"note", note}, {"actions", action}, {"tree-move", tree}} {
		w := mutationRequest(t, s.Handler(), "POST", "/v1/files/"+endpoint.path, endpoint.input, "company")
		var envelope response.Envelope[map[string]any]
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || w.Code != 200 || envelope.Data["root"] != "box" {
			t.Fatalf("old Personal %s replay: %d %s %v", endpoint.path, w.Code, w.Body.String(), err)
		}
		if _, exists := envelope.Data["box_id"]; exists {
			t.Fatal("old Personal receipt was restamped")
		}
	}
	if db.writes != 0 {
		t.Fatal("old Personal replay attempted metadata write", db.writes)
	}
}
