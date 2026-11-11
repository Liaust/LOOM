package notesworkspace

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

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
)

func TestWorkspaceMutationScopesPostgres(t *testing.T) {
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires local disposable PostgreSQL administrator LOOM_TEST_DB_URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, socket := u.Hostname(), u.Query().Get("host")
	localSocket := strings.HasPrefix(socket, "/tmp/") || socket == "/run/postgresql" || socket == "/var/run/postgresql"
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && !(host == "" && localSocket) {
		t.Fatal("requires a local disposable database endpoint")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	id, err := newFileID()
	if err != nil {
		t.Fatal(err)
	}
	name := "scopes_" + strings.TrimPrefix(id, "notes_file_")
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
	}()
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "migrations")
	ctx := t.Context()
	if err := goose.UpToContext(ctx, db, dir, 101); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Older installations can retain a nullable Notes node key. Mutation scope
	// metadata must preserve that uncertainty rather than failing the backfill.
	exec(`ALTER TABLE knowledge.notes_source_roots ALTER COLUMN node_key DROP NOT NULL`)
	exec(`INSERT INTO knowledge.notes_source_roots(notes_source_root_id,root_kind,backend_root_key,source_path,node_key,status)
 VALUES('notes_source_root_retained','box_notes','retained','/notes','main','disabled'),
 ('notes_source_root_empty','box_notes','empty','',NULL,'deleted')`)
	exec(`INSERT INTO notes_workspace.files(file_id,collection_id,path_key,record)
 VALUES('retained-file','notes_source_root_retained','old.md','{}')`)
	if err := goose.UpToContext(ctx, db, dir, 102); err != nil {
		t.Fatal(err)
	}
	var historical, historicalNode string
	if err := db.QueryRowContext(ctx, `SELECT source_path,node_key FROM notes_workspace.source_scopes WHERE scope_id='notes_source_root_empty'`).Scan(&historical, &historicalNode); err != nil || historical != "" || historicalNode != "" {
		t.Fatalf("unknown historical scope was dropped or invented: path=%q node=%q err=%v", historical, historicalNode, err)
	}
	store := SQLStore{DB: db}
	var kind, node string
	if err := db.QueryRowContext(ctx, `SELECT kind,node_key FROM notes_workspace.source_scopes WHERE scope_id='notes_source_root_retained'`).Scan(&kind, &node); err != nil || kind != "notes" || node != "main" {
		t.Fatalf("existing disabled root not retained: %q %q %v", kind, node, err)
	}
	scope := SourceScope{ID: "filesystem_box_test", Path: "/box", NodeKey: "main", RootIdentity: "device:inode"}
	for range 2 {
		if err := store.EnsureBoxScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []func(*SourceScope){
		func(s *SourceScope) { s.Path = "/other" },
		func(s *SourceScope) { s.NodeKey = "other" },
		func(s *SourceScope) { s.RootIdentity = "other" },
	} {
		changed := scope
		change(&changed)
		if err := store.EnsureBoxScope(ctx, changed); err == nil {
			t.Fatal("existing Box scope rebound")
		}
	}
	exec(`INSERT INTO notes_workspace.source_scopes(scope_id,kind,source_path,node_key,root_identity)
 VALUES('filesystem_box_wrong_kind','notes','/box','main','device:inode')`)
	wrongKind := scope
	wrongKind.ID = "filesystem_box_wrong_kind"
	if err := store.EnsureBoxScope(ctx, wrongKind); err == nil {
		t.Fatal("Notes scope accepted as Box")
	}
	file := File{ID: "box-file", CollectionID: scope.ID, RelativePath: "unindexed.txt", PathKey: "unindexed.txt"}
	if _, err := store.Bind(ctx, file); err != nil {
		t.Fatal(err)
	}
	var roots, objects, inventory int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM knowledge.notes_source_roots),
 (SELECT count(*) FROM knowledge.knowledge_objects),(SELECT count(*) FROM notes_workspace.source_inventory_generations)`).Scan(&roots, &objects, &inventory); err != nil || roots != 2 || objects != 0 || inventory != 0 {
		t.Fatalf("Box mutation enrolled Notes work: roots=%d objects=%d inventory=%d err=%v", roots, objects, inventory, err)
	}
	file.ID, file.CollectionID = "unknown-file", "filesystem_box_unknown"
	_, err = store.Bind(ctx, file)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("unknown scope must FK-fail: %v", err)
	}
	exec(`INSERT INTO knowledge.notes_source_roots(notes_source_root_id,root_kind,backend_root_key,source_path,node_key)
 VALUES('notes_source_root_late','box_notes','late','/late',NULL)`)
	if path, err := store.ScopePath(ctx, "notes_source_root_late"); err != nil || path != "/late" {
		t.Fatalf("late unscoped root lookup: path=%q err=%v", path, err)
	}
	file.ID, file.CollectionID = "late-file", "notes_source_root_late"
	if _, err := store.Bind(ctx, file); err != nil {
		t.Fatalf("late existing Notes root not scoped by Bind: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT node_key FROM notes_workspace.source_scopes WHERE scope_id='notes_source_root_late'`).Scan(&node); err != nil || node != "" {
		t.Fatalf("nullable node key not retained by Bind: %q %v", node, err)
	}
	assertPath := func(id, want string) {
		t.Helper()
		if got, err := store.ScopePath(ctx, id); err != nil || got != want {
			t.Fatalf("scope %s path=%q want=%q err=%v", id, got, want, err)
		}
	}
	assertPath(scope.ID, "/box")
	exec(`UPDATE knowledge.notes_source_roots SET source_path='/notes-moved' WHERE notes_source_root_id='notes_source_root_retained'`)
	assertPath("notes_source_root_retained", "/notes-moved")
	exec(`UPDATE knowledge.notes_source_roots SET source_path='' WHERE notes_source_root_id='notes_source_root_retained'`)
	assertPath("notes_source_root_retained", "/notes")
	file.ID, file.CollectionID = "retained-path-file", "notes_source_root_retained"
	if _, err := store.Bind(ctx, file); err != nil {
		t.Fatalf("existing scope lost its retained path: %v", err)
	}
	exec(`DELETE FROM knowledge.notes_source_roots WHERE notes_source_root_id='notes_source_root_retained'`)
	assertPath("notes_source_root_retained", "/notes")
	if _, err := store.ScopePath(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown scope lookup: %v", err)
	}
	for _, state := range []string{"preview", "prepared", "accepted", "conflict", "held"} {
		exec(`INSERT INTO notes_workspace.tree_moves(move_id,scope_id,state,record) VALUES($1,$2,$1,'{}')`, state, scope.ID)
	}
	for _, query := range []string{
		`INSERT INTO notes_workspace.tree_moves(move_id,scope_id,state,record) VALUES('bad-state','filesystem_box_test','pending','{}')`,
		`INSERT INTO notes_workspace.tree_moves(move_id,scope_id,state,record) VALUES('bad-record','filesystem_box_test','preview','[]')`,
		`INSERT INTO notes_workspace.tree_moves(move_id,scope_id,state,record) VALUES('bad-scope','missing','preview','{}')`,
	} {
		if _, err := db.ExecContext(ctx, query); err == nil {
			t.Fatalf("invalid tree move accepted: %s", query)
		}
	}
	if err := goose.DownToContext(ctx, db, dir, 101); err == nil {
		t.Fatal("custody downgrade did not fail closed")
	}
	var moves int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM notes_workspace.tree_moves`).Scan(&moves); err != nil || moves != 5 {
		t.Fatalf("downgrade lost move history: %d %v", moves, err)
	}
	assertPath(scope.ID, "/box")
	t.Run("filesystem locking", func(t *testing.T) { testScopeFilesystemLocks(t, db) })
	t.Run("physical legacy reservations", func(t *testing.T) { testScopePhysicalReservations(t, db) })
	t.Run("registered source roots", func(t *testing.T) { testTreeSourceRootGuard(t, db) })
}

func testScopePhysicalReservations(t *testing.T, db *sql.DB) {
	ctx := t.Context()
	store := SQLStore{DB: db}
	box := SourceScope{ID: "filesystem_box_physical", Path: "/physical-box", NodeKey: "main", RootIdentity: "physical-test"}
	if err := store.EnsureBoxScope(ctx, box); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowledge.notes_source_roots(notes_source_root_id,root_kind,backend_root_key,source_path,node_key)
 VALUES('notes_source_root_physical','box_notes','physical','/physical-box/Notes','main')`); err != nil {
		t.Fatal(err)
	}
	file, err := store.Bind(ctx, File{ID: "legacy-physical-file", CollectionID: "notes_source_root_physical", RelativePath: "drafts/note.md", PathKey: pathKey("drafts/note.md")})
	if err != nil {
		t.Fatal(err)
	}
	// No journal directory is required to reserve the immutable prepared intent.
	op := Operation{Request: Request{OperationID: "legacy-physical-move", FileID: file.ID, Kind: KindRename, DestinationPath: "results/moved.md"},
		File: file, State: Pending, PathStage: "prepared", DestinationKey: pathKey("results/moved.md")}
	if _, err := store.Receive(ctx, op); err != nil {
		t.Fatal(err)
	}
	assertBusy := func(collection, relative, except string, want bool) {
		t.Helper()
		if got, err := store.PathBusy(ctx, collection, pathKey(relative), except); err != nil || got != want {
			t.Fatalf("reservation %s:%s except=%q got=%t want=%t err=%v", collection, relative, except, got, want, err)
		}
	}
	for _, relative := range []string{"Notes/drafts/note.md", "Notes/drafts", "Notes/results/moved.md", "Notes/results"} {
		assertBusy(box.ID, relative, "", true)
		assertBusy(box.ID, relative, op.Request.OperationID, false)
	}
	for _, relative := range []string{"Notes/drafts-other", "Notes/results-other", "Elsewhere/note.md"} {
		assertBusy(box.ID, relative, "", false)
	}
	op.State = Held
	if err := store.SaveOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	assertBusy(box.ID, "Notes/drafts/note.md", "", true)
	other := box
	other.ID, other.NodeKey = "filesystem_box_physical_other_node", "other"
	if err := store.EnsureBoxScope(ctx, other); err != nil {
		t.Fatal(err)
	}
	assertBusy(other.ID, "Notes/drafts/note.md", "", false)
	if _, err := db.ExecContext(ctx, `UPDATE knowledge.notes_source_roots SET node_key=NULL WHERE notes_source_root_id=$1`, file.CollectionID); err != nil {
		t.Fatal(err)
	}
	assertBusy(box.ID, "Notes/results/moved.md", "", true)
	assertBusy(other.ID, "Notes/drafts/note.md", "", false) // Retained scope still identifies Main.
	for _, state := range []string{Accepted, Conflict} {
		op.State = state
		if err := store.SaveOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		assertBusy(box.ID, "Notes/drafts/note.md", "", false)
	}
	op.State, op.PathStage = Pending, ""
	if err := store.SaveOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	assertBusy(box.ID, "Notes/drafts/note.md", "", false)
	unknown, err := store.Bind(ctx, File{ID: "unresolved-file", CollectionID: "notes_source_root_empty", RelativePath: "old.md", PathKey: "old.md"})
	if err != nil {
		t.Fatal(err)
	}
	unresolved := Operation{Request: Request{OperationID: "unresolved-history", FileID: unknown.ID}, File: unknown, State: Held, PathStage: "prepared"}
	if _, err := store.Receive(ctx, unresolved); err != nil {
		t.Fatal(err)
	}
	assertBusy(unknown.CollectionID, "old.md", "", true)
	if _, err := store.PathBusy(ctx, unknown.CollectionID, "other.md", ""); !errors.Is(err, ErrMembership) {
		t.Fatalf("unresolved history became physical write authority: %v", err)
	}
	assertBusy(box.ID, "old.md", "", false)
}

func testTreeSourceRootGuard(t *testing.T, db *sql.DB) {
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"Notes/child/empty", "incoming/nested", "pending/nested", "prepared/nested", "other-node/nested"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0750); err != nil {
			t.Fatal(err)
		}
	}
	store := SQLStore{DB: db, ExclusiveSource: true}
	scope := SourceScope{ID: "filesystem_box_root_guard", Path: root, NodeKey: "main", RootIdentity: "root-guard-test"}
	if err := store.EnsureBoxScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	service := Service{Store: store, Sources: treeTestResolver{Source{CollectionID: scope.ID, Path: root, Generation: scope.RootIdentity}}}
	register := func(id, relative, node string) {
		t.Helper()
		var nodeKey any = node
		if node == "" {
			nodeKey = nil
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO knowledge.notes_source_roots(notes_source_root_id,root_kind,backend_root_key,source_path,node_key)
 VALUES($1,'box_notes',$1,$2,$3)`, "notes_source_root_guard_"+id, filepath.Join(root, relative), nodeKey); err != nil {
			t.Fatal(err)
		}
	}
	register("parent", "Notes", "main")
	register("nested", "incoming/nested", "main")
	for _, from := range []string{"Notes", "incoming"} {
		if _, err := service.PreviewTree(ctx, scope.ID, "guard-"+from, from, from+"-moved"); !errors.Is(err, ErrMembership) {
			t.Fatalf("registered root allowed in preview of %s: %v", from, err)
		}
	}
	plan, err := service.PreviewTree(ctx, scope.ID, "guard-child", "Notes/child", "Notes/moved")
	if err != nil {
		t.Fatalf("ordinary child blocked by enclosing Notes root: %v", err)
	}
	if result, err := service.ApplyTree(ctx, plan.ID, plan.Hash); err != nil || result.State != Accepted {
		t.Fatalf("ordinary child apply: state=%s err=%v", result.State, err)
	}
	register("other_node", "other-node/nested", "other")
	if _, err := service.PreviewTree(ctx, scope.ID, "guard-other-node", "other-node", "other-node-moved"); err != nil {
		t.Fatalf("unrelated node's registration blocked Main: %v", err)
	}
	for _, prepared := range []bool{false, true} {
		from := "pending"
		if prepared {
			from = "prepared"
		}
		plan, err := service.PreviewTree(ctx, scope.ID, "guard-late-"+from, from, from+"-moved")
		if err != nil {
			t.Fatal(err)
		}
		wantState := "preview"
		if prepared {
			plan.State, wantState = "prepared", "prepared"
			if err := store.saveTreeMove(ctx, plan, false); err != nil {
				t.Fatal(err)
			}
		}
		node := "main"
		if prepared {
			node = "" // An unknown node must not bypass the registered-root guard.
		}
		register(from, from+"/nested", node)
		if _, err := service.PreviewTree(ctx, scope.ID, plan.ID, plan.From, plan.To); !errors.Is(err, ErrMembership) {
			t.Fatalf("replayed preview ignored new source registration: %v", err)
		}
		if _, err := service.ApplyTree(ctx, plan.ID, plan.Hash); !errors.Is(err, ErrMembership) {
			t.Fatalf("apply ignored new source registration: %v", err)
		}
		if saved, err := store.TreeMove(ctx, plan.ID); err != nil || saved.State != wantState || saved.Hash != plan.Hash {
			t.Fatalf("refusal rewrote retained plan: state=%s err=%v", saved.State, err)
		}
		if _, err := os.Stat(filepath.Join(root, from, "nested")); err != nil {
			t.Fatalf("refused move changed source: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, from+"-moved")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused move placed destination: %v", err)
		}
	}
}

func testScopeFilesystemLocks(t *testing.T, db *sql.DB) {
	store := SQLStore{DB: db}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := store.WithLock(ctx, "collection:first", func(locked Store) error {
		if locked.(SQLStore).ExclusiveSource {
			t.Fatal("normal callback became exclusive")
		}
		// Different collections may coexist under the shared filesystem lock.
		if err := store.WithLock(ctx, "collection:second", func(Store) error { return nil }); err != nil {
			return err
		}
		// A separate session cannot acquire global exclusivity while normal work runs.
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		var acquired bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended('notesworkspace:filesystem',0))`).Scan(&acquired); err != nil {
			return err
		}
		if acquired {
			_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtextextended('notesworkspace:filesystem',0))`)
			t.Error("shared owner failed to block exclusive work")
		}
		cancel() // Unlocking must not inherit this cancelled callback context.
		return ctx.Err()
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled callback: %v", err)
	}
	exclusive := SQLStore{DB: db, ExclusiveSource: true}
	lockCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	if err := exclusive.WithLock(lockCtx, "collection:tree", func(locked Store) error {
		if !locked.(SQLStore).ExclusiveSource {
			t.Error("exclusive callback lost its lock mode")
		}
		blockedCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		called := false
		err := store.WithLock(blockedCtx, "collection:other", func(Store) error { called = true; return nil })
		if err == nil || called {
			t.Errorf("exclusive filesystem owner did not block another collection: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("exclusive acquisition or detached cleanup: %v", err)
	}
	if err := store.WithLock(lockCtx, "collection:other", func(Store) error { return nil }); err != nil {
		t.Fatalf("cancelled lock acquisition leaked a lock: %v", err)
	}
}
