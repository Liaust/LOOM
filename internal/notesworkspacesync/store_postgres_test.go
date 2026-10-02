package notesworkspacesync

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/notesworkspace"
)

func TestBridgeSQLSourceRestartAndConflictPostgres(t *testing.T) {
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires local PostgreSQL administrator; creates only a disposable database")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, socket := u.Hostname(), u.Query().Get("host")
	localSocket := strings.HasPrefix(socket, "/tmp/") || socket == "/run/postgresql" || socket == "/var/run/postgresql"
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && !(host == "" && localSocket) {
		t.Fatal("requires local disposable database endpoint")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := "notes_sync_" + strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), "project_"))
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
	}()
	u.Path = "/" + name
	if _, err := migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	const collection = "notes_source_root_sync_test"
	if _, err := db.ExecContext(t.Context(), `INSERT INTO knowledge.notes_source_roots(notes_source_root_id,root_kind,backend_root_key,source_path) VALUES($1,'box_notes','notes-sync-test','/test')`, collection); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "a.md")
	if err := os.WriteFile(path, []byte("A"), 0600); err != nil {
		t.Fatal(err)
	}
	source := notesworkspace.Service{Store: notesworkspace.SQLStore{DB: db}, Sources: resolver{root}}
	read, err := source.Read(t.Context(), collection, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	native := &fakeNative{docs: map[string]Evidence{}, epoch: "epoch"}
	s := &Service{Replica: "replica", Epoch: "epoch", Store: SQLStore{DB: db}, Source: NotesSource{Service: source}, Native: native,
		Scopes: []Scope{{Workspace: "w", Collection: "c", SourceCollection: collection, Generation: "generation", Root: "Notes"}}}
	binding, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID)
	if err != nil {
		t.Fatal(err)
	}
	i, p := operation(binding, "edit", "B", "2-edit")
	native.content(binding.Path, "2-edit", binding.NativeRevision, "B")
	native.add(i)
	native.add(p)
	step(t, s)
	if len(native.acks) != 1 || native.acks[0].Status != "applied" {
		t.Fatalf("missing applied acknowledgement: %+v", native.acks)
	}
	// Recompose from persisted stores, without copying any in-memory bridge state.
	s.Store = SQLStore{DB: db}
	s.Source = NotesSource{Service: notesworkspace.Service{Store: notesworkspace.SQLStore{DB: db}, Sources: resolver{root}}}
	step(t, s)
	if len(native.acks) != 1 {
		t.Fatal("restart duplicated acknowledgement")
	}
	stale, stalePublication := operation(binding, "stale", "C", "2-stale")
	native.content(binding.Path, "2-stale", binding.NativeRevision, "C")
	native.add(stalePublication)
	native.add(stale)
	step(t, s)
	if len(native.acks) != 2 || native.acks[1].Status != "conflict" {
		t.Fatalf("stale base was not conflicted: %+v", native.acks)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "B" {
		t.Fatalf("newer source changed: %q %v", content, err)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM notes_workspace.operations`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("source receipts duplicated or lost: %d %v", count, err)
	}
	if err := s.Store.WithReplica(t.Context(), s.Replica, "replacement-epoch", func(Records) error { t.Fatal("epoch replacement admitted"); return nil }); !errors.Is(err, ErrHeld) {
		t.Fatalf("epoch mismatch: %v", err)
	}
}
