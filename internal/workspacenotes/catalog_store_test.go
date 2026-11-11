package workspacenotes

import (
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

	"loom.local/loom/internal/workspacefiles"
)

func TestCatalogReadOnlyPaginationSkipsPrivateCandidatesWithoutLeakingPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{{Ref: "box", SourcePath: root}}}
	t.Cleanup(func() { _ = files.Close() })
	calls := 0
	query := func(q string, args []driver.NamedValue) (driver.Rows, error) {
		calls++
		if strings.HasPrefix(q, "SELECT path") {
			if args[0].Value != "box" || args[1].Value != "workspace_note_hidden" {
				t.Fatal(args)
			}
			return &catalogRows{columns: []string{"path"}, values: [][]driver.Value{{"escape/private.md"}}}, nil
		}
		if !strings.HasPrefix(q, "WITH page AS MATERIALIZED") {
			t.Fatalf("unexpected query (observation or per-item query): %s", q)
		}
		rows := [][]driver.Value{catalogRow("workspace_note_note", "note.md", nil)}
		if calls == 1 {
			rows = append([][]driver.Value{catalogRow("workspace_note_internal", ".loom/state/private.md", nil), catalogRow("workspace_note_hidden", "escape/private.md", nil)}, rows...)
		}
		return &catalogRows{columns: strings.Split("id,root,path,revision,aliases,tags,properties,values,version,observed,forward", ","), values: rows}, nil
	}
	db := sql.OpenDB(catalogConnector{t, query})
	t.Cleanup(func() { _ = db.Close() })
	s := Service{DB: db, Files: files}
	page, err := s.Catalog(t.Context(), CatalogInput{Root: "box", Limit: 2})
	if err != nil || len(page.Items) != 0 || !page.Truncated || page.Next != "workspace_note_hidden" || calls != 1 {
		t.Fatal(page, err, calls)
	}
	raw, err := json.Marshal(page)
	if err != nil || strings.Contains(string(raw), "private.md") || strings.Contains(string(raw), "escape") {
		t.Fatal("private cursor path leaked", string(raw), err)
	}
	page, err = s.Catalog(t.Context(), CatalogInput{Root: "box", Limit: 2, After: page.Next})
	if err != nil || len(page.Items) != 1 || page.Items[0].Path != "note.md" || page.Truncated || page.Next != "" || calls != 3 {
		t.Fatal(page, err, calls)
	}
	if page.Items[0].Values == nil || page.Items[0].Forward == nil || page.Items[0].MetadataVersion != 0 || page.Coverage == "" {
		t.Fatal("unknown metadata or empty collections lost", page)
	}
	if err := os.Mkdir(filepath.Join(root, "scope"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = s.Catalog(t.Context(), CatalogInput{Root: "box", Path: "scope", After: "workspace_note_hidden"})
	if !errors.Is(err, workspacefiles.ErrInvalidCursor) || calls != 4 {
		t.Fatal("cursor crossed scope", err, calls)
	}
}

func TestCatalogBoundsForwardReferencesWithoutInventingAnotherPage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	forward := []Reference{}
	for i := range 501 {
		forward = append(forward, Reference{Kind: "wikilink", Target: fmt.Sprint(i)})
	}
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{{Ref: "box", SourcePath: root}}}
	t.Cleanup(func() { _ = files.Close() })
	db := sql.OpenDB(catalogConnector{t, func(string, []driver.NamedValue) (driver.Rows, error) {
		return &catalogRows{columns: strings.Split("id,root,path,revision,aliases,tags,properties,values,version,observed,forward", ","), values: [][]driver.Value{catalogRow("workspace_note_note", "note.md", forward)}}, nil
	}})
	t.Cleanup(func() { _ = db.Close() })
	page, err := (Service{DB: db, Files: files}).Catalog(t.Context(), CatalogInput{Root: "box"})
	if err != nil || len(page.Items) != 1 || len(page.Items[0].Forward) != 500 || !page.Items[0].ForwardTruncated || !page.Truncated || page.Next != "" {
		t.Fatal(page, err)
	}
}

func catalogRow(id, path string, forward []Reference) []driver.Value {
	if forward == nil {
		forward = []Reference{}
	}
	raw, _ := json.Marshal(forward)
	return []driver.Value{id, "box", path, "", []byte("[]"), []byte("[]"), []byte("[]"), []byte("{}"), int64(0), nil, raw}
}

type catalogConnector struct {
	t     *testing.T
	query func(string, []driver.NamedValue) (driver.Rows, error)
}

func (c catalogConnector) Connect(context.Context) (driver.Conn, error) { return catalogConn{c}, nil }
func (c catalogConnector) Driver() driver.Driver                        { return catalogDriver{} }

type catalogDriver struct{}

func (catalogDriver) Open(string) (driver.Conn, error) { return nil, fmt.Errorf("use sql.OpenDB") }

type catalogConn struct{ catalogConnector }

func (c catalogConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("unexpected prepare")
}
func (c catalogConn) Close() error              { return nil }
func (c catalogConn) Begin() (driver.Tx, error) { return nil, fmt.Errorf("use BeginTx") }
func (c catalogConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if !opts.ReadOnly {
		c.t.Fatal("catalog transaction is not read-only")
	}
	return catalogTx{}, nil
}
func (c catalogConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	if q != "SET LOCAL jit=off" && q != "SET LOCAL statement_timeout='3s'" {
		c.t.Fatalf("unexpected catalog write: %s", q)
	}
	return driver.RowsAffected(0), nil
}
func (c catalogConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(q, args)
}

type catalogTx struct{}

func (catalogTx) Commit() error   { return nil }
func (catalogTx) Rollback() error { return nil }

type catalogRows struct {
	columns []string
	values  [][]driver.Value
}

func (r *catalogRows) Columns() []string { return r.columns }
func (r *catalogRows) Close() error      { return nil }
func (r *catalogRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}
