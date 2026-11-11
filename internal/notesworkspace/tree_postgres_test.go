package notesworkspace

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/migrations"
)

type treeTestResolver struct{ source Source }

func (r treeTestResolver) WithSource(_ context.Context, id, relative string, fn func(Source) error) error {
	if id != r.source.CollectionID {
		return ErrMembership
	}
	return fn(r.source)
}
func (r treeTestResolver) WithPaths(ctx context.Context, id string, paths []string, fn func(Source) error) error {
	return r.WithSource(ctx, id, "", fn)
}
func (r treeTestResolver) WithDirectoryPaths(ctx context.Context, id string, paths []string, fn func(Source) error) error {
	return r.WithSource(ctx, id, "", fn)
}

func TestWorkspaceTreeMovePostgres(t *testing.T) {
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires local disposable PostgreSQL administrator LOOM_TEST_DB_URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && !(u.Hostname() == "" && strings.HasPrefix(u.Query().Get("host"), "/")) {
		t.Fatal("local test database required")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	id, _ := newFileID()
	name := "tree_" + strings.TrimPrefix(id, "notes_file_")
	if _, err = admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`) }()
	u.Path = "/" + name
	if _, err = migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Notes/src/empty", "Topics"} {
		if err = os.MkdirAll(filepath.Join(root, p), 0750); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(root, "Notes/src/note.md"), []byte("first"), 0640); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "Notes/src/asset.pdf"), []byte("opaque unindexed asset"), 0640); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err = unix.Stat(root, &st); err != nil {
		t.Fatal(err)
	}
	scope := SourceScope{ID: "filesystem_box_test", Path: root, NodeKey: "main", RootIdentity: FilesystemGeneration("filesystem_box_test", "main", root, uint64(st.Dev), uint64(st.Ino))}
	store := SQLStore{DB: db, ExclusiveSource: true}
	if err = store.EnsureBoxScope(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	src := Source{CollectionID: scope.ID, Path: root, Generation: scope.RootIdentity}
	service := Service{Store: store, Sources: treeTestResolver{src}}
	read, err := service.Read(t.Context(), scope.ID, "Notes/src/note.md")
	if err != nil {
		t.Fatal(err)
	}
	request := Request{OperationID: "tree-test-edit", FileID: read.File.ID, BaseID: read.Base.ID, Generation: src.Generation, Content: []byte("second")}
	if _, err = service.Receive(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	before, err := service.Apply(t.Context(), request.OperationID)
	if err != nil || before.State != Accepted {
		t.Fatalf("edit: %+v %v", before, err)
	}
	// A legacy Notes alias remains under the same physical owner and retains its
	// immutable original receipt even when a later move crosses its old scope.
	legacyRoot := filepath.Join(root, "Notes")
	if _, err = db.Exec(`INSERT INTO knowledge.notes_source_roots(notes_source_root_id,root_kind,backend_root_key,source_path,node_key,status) VALUES('notes_source_root_legacy','box_notes','legacy',$1,'main','active')`, legacyRoot); err != nil {
		t.Fatal(err)
	}
	legacy := Service{Store: SQLStore{DB: db}, Sources: treeTestResolver{Source{CollectionID: "notes_source_root_legacy", Path: legacyRoot, Generation: "legacy"}}}
	old, err := legacy.Read(t.Context(), "notes_source_root_legacy", "src/note.md")
	if err != nil {
		t.Fatal(err)
	}
	oldRequest := Request{OperationID: "tree-test-legacy", FileID: old.File.ID, BaseID: old.Base.ID, Generation: "legacy", Content: []byte("third")}
	if _, err = legacy.Receive(t.Context(), oldRequest); err != nil {
		t.Fatal(err)
	}
	oldReceipt, err := legacy.Apply(t.Context(), oldRequest.OperationID)
	if err != nil || oldReceipt.State != Accepted {
		t.Fatal(err)
	}
	plan, err := service.PreviewTree(t.Context(), scope.ID, "desktop_tree_owned", "Notes/src", "Notes/moved")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Journals) != 2 {
		t.Fatalf("retained journal inventory=%d", len(plan.Journals))
	}
	// Simulate interruption after the atomic filesystem step, before SQL commit.
	prepared := plan
	prepared.State = "prepared"
	if err = store.saveTreeMove(t.Context(), prepared, false); err != nil {
		t.Fatal(err)
	}
	if busy, e := store.PathBusy(t.Context(), scope.ID, pathKey("Notes/src/note.md"), ""); e != nil || !busy {
		t.Fatalf("prepared move lost reservation: %t %v", busy, e)
	}
	if err = os.Rename(filepath.Join(root, "Notes/src"), filepath.Join(root, "Notes/moved")); err != nil {
		t.Fatal(err)
	}
	result, err := service.ApplyTree(t.Context(), plan.ID, plan.Hash)
	if err != nil || result.State != Accepted {
		t.Fatalf("resume: %+v %v", result.Summary(), err)
	}
	again, err := service.ApplyTree(t.Context(), plan.ID, plan.Hash)
	if err != nil || again.State != Accepted {
		t.Fatal(err)
	}
	after, err := service.Read(t.Context(), scope.ID, "Notes/moved/note.md")
	if err != nil || after.File.ID != read.File.ID || string(after.Base.Content) != "third" {
		t.Fatalf("file identity/content changed: %+v %v", after, err)
	}
	if asset, e := os.ReadFile(filepath.Join(root, "Notes/moved/asset.pdf")); e != nil || string(asset) != "opaque unindexed asset" {
		t.Fatal("asset lost", e)
	}
	for _, entry := range []string{"Notes/moved/empty", "Notes/moved/.loom-notes-" + identity(request.OperationID)} {
		if _, e := os.Stat(filepath.Join(root, entry)); e != nil {
			t.Fatal(e)
		}
	}
	for _, op := range []string{request.OperationID, oldRequest.OperationID} {
		r, e := service.Reconcile(t.Context(), op)
		if e != nil || r.State != "clean" {
			t.Fatalf("moved retained journal: %+v %v", r, e)
		}
	}
	saved, err := store.Operation(t.Context(), oldRequest.OperationID)
	if err != nil || !sameIntent(saved, oldReceipt) || saved.ResultBaseID != oldReceipt.ResultBaseID {
		t.Fatal("legacy receipt rewritten", err)
	}
	second, err := service.PreviewTree(t.Context(), scope.ID, "desktop_tree_cross", "Notes/moved", "Topics/moved")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ApplyTree(t.Context(), second.ID, second.Hash); err != nil {
		t.Fatal(err)
	}
	alias, err := store.File(t.Context(), old.File.ID)
	if err != nil || !alias.Deleted {
		t.Fatal("old scope alias not retired", err)
	}
	if current, e := service.Read(t.Context(), scope.ID, "Topics/moved/note.md"); e != nil || current.File.ID != read.File.ID || string(current.Base.Content) != "third" {
		t.Fatal("cross-scope move changed canonical file", e)
	}
	if r, e := legacy.Reconcile(t.Context(), oldRequest.OperationID); e != nil || r.State != "clean" {
		t.Fatalf("cross-scope retained journal: %+v %v", r, e)
	}
	var objects int
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_objects`).Scan(&objects); err != nil || objects != 0 {
		t.Fatal("move enrolled/indexed content", objects, err)
	}
}
