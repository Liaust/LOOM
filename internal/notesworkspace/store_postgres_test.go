package notesworkspace

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"loom.local/loom/internal/migrations"
)

// Same optional disposable local administrator convention as knowledge tests.
// Never migrate the provided database; create one owned database only if configured.
func TestWorkspaceSQLCustodyAndReplayPostgres(t *testing.T) {
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires local disposable PostgreSQL administrator LOOM_TEST_DB_URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	socket := u.Query().Get("host")
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
	name := "nw_" + strings.TrimPrefix(id, "notes_file_")
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); err != nil {
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
	if _, err := db.Exec(`INSERT INTO knowledge.notes_source_roots(notes_source_root_id,root_kind,backend_root_key,source_path) VALUES('notes_source_root_test','box_notes','test','/test')`); err != nil {
		t.Fatal(err)
	}
	store := SQLStore{DB: db}
	var file File
	err = store.WithLock(t.Context(), "collection:notes_source_root_test", func(locked Store) error {
		var err error
		file, err = locked.Bind(t.Context(), File{ID: id, CollectionID: "notes_source_root_test", RelativePath: "note.md", PathKey: "note.md"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	base := makeBase(file, "generation", true, []byte("A"))
	if err := store.SaveBase(t.Context(), base); err != nil {
		t.Fatal(err)
	}
	request := Request{OperationID: "epoch/document/revision", FileID: file.ID, Generation: base.Generation, BaseID: base.ID, Content: []byte("B")}
	op, err := (Service{Store: store}).Receive(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingOperationIDs(t.Context(), "", 1)
	if err != nil || len(pending.IDs) != 1 || pending.IDs[0] != request.OperationID {
		t.Fatalf("pending discovery: %v %v", pending, err)
	}
	// Recreate the store/service to prove records, rather than process memory,
	// retain the exact base/proposal and deduplicate a native revision replay.
	restarted := SQLStore{DB: db}
	saved, err := restarted.Operation(context.Background(), request.OperationID)
	if err != nil || !sameIntent(op, saved) || string(saved.Base.Content) != "A" {
		t.Fatalf("durable custody lost: %+v %v", saved, err)
	}
	if _, err := (Service{Store: restarted}).Receive(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.Content = []byte("different")
	if _, err := (Service{Store: restarted}).Receive(t.Context(), request); err != ErrIntentMismatch {
		t.Fatalf("replay identity mismatch: %v", err)
	}
	saved.State = Conflict
	saved.Observed = []byte("C")
	saved.ObservedExists = true
	if err := restarted.WithLock(t.Context(), "collection:"+file.CollectionID, func(locked Store) error { return locked.SaveOperation(t.Context(), saved) }); err != nil {
		t.Fatal(err)
	}
	got, err := store.Operation(t.Context(), saved.Request.OperationID)
	if err != nil || got.State != Conflict || string(got.Observed) != "C" {
		t.Fatalf("conflict not durable: %+v %v", got, err)
	}
	// A full held page must not hide newer work or loop on the same IDs.
	for i := 0; i < 101; i++ {
		item := op
		item.Request.OperationID = fmt.Sprintf("fair-%03d", i)
		item.State = Held
		if i == 100 {
			item.State = Pending
		}
		if _, err := store.Receive(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.PendingOperationIDs(t.Context(), "", 100)
	if err != nil || len(page.IDs) != 100 || page.Next != "fair-099" {
		t.Fatalf("held page: %+v %v", page, err)
	}
	// Mutating held rows does not move the cursor order backwards.
	held, err := store.Operation(t.Context(), "fair-000")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOperation(t.Context(), held); err != nil {
		t.Fatal(err)
	}
	page, err = store.PendingOperationIDs(t.Context(), page.Next, 100)
	if err != nil || len(page.IDs) != 1 || page.IDs[0] != "fair-100" {
		t.Fatalf("pending behind held page: %+v %v", page, err)
	}
	page, err = store.PendingOperationIDs(t.Context(), page.Next, 100)
	if err != nil || len(page.IDs) != 0 || page.Next != "" {
		t.Fatalf("end of sweep: %+v %v", page, err)
	}
	saved.Journal = ".loom-notes-" + identity(saved.Request.OperationID)
	if err := store.SaveOperation(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	retained, err := store.RetainedOperationIDs(t.Context(), "", 100)
	if err != nil || len(retained.IDs) != 1 || retained.IDs[0] != saved.Request.OperationID {
		t.Fatalf("final receipt not discoverable: %+v %v", retained, err)
	}
	recovery := Recovery{ID: "recovery-test", OperationID: saved.Request.OperationID, State: Conflict, Retained: []byte("late"), RetainedExists: true, Current: []byte("current"), CurrentExists: true}
	for i := 0; i < 2; i++ {
		if err := store.SaveRecovery(t.Context(), recovery); err != nil {
			t.Fatal(err)
		}
	}
	events, err := restarted.Recoveries(t.Context(), recovery.OperationID, "", 100)
	if err != nil || len(events) != 1 || string(events[0].Retained) != "late" || string(events[0].Current) != "current" {
		t.Fatalf("durable recovery: %+v %v", events, err)
	}

	// Commit path binding, result base, receipt and history as one transaction.
	rename := Operation{Request: Request{OperationID: "rename-sql", Kind: KindRename, FileID: file.ID, BaseID: base.ID, Generation: base.Generation, DestinationPath: "new.md"}, File: file, Base: base, State: Pending, PathStage: "prepared", DestinationKey: pathKey("new.md")}
	if _, err := store.Receive(t.Context(), rename); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{file.PathKey, pathKey("new.md")} {
		if busy, err := store.PathBusy(t.Context(), file.CollectionID, key, ""); err != nil || !busy {
			t.Fatalf("durable path reservation: %s %v %v", key, busy, err)
		}
	}
	moved := file
	moved.RelativePath = "new.md"
	moved.PathKey = pathKey(moved.RelativePath)
	moved.PathVersion = 1
	movedBase := makeBase(moved, base.Generation, true, base.Content)
	rename.State, rename.ResultFile, rename.ResultBaseID = Accepted, &moved, movedBase.ID
	if err := store.WithLock(t.Context(), "collection:"+file.CollectionID, func(locked Store) error { return locked.CommitPath(t.Context(), rename, moved, movedBase) }); err != nil {
		t.Fatal(err)
	}
	actual, err := store.FileAt(t.Context(), file.CollectionID, moved.PathKey)
	if err != nil || actual.ID != file.ID || actual.PathVersion != 1 {
		t.Fatalf("renamed binding: %+v %v", actual, err)
	}
	if busy, err := store.PathBusy(t.Context(), file.CollectionID, moved.PathKey, ""); err != nil || busy {
		t.Fatalf("committed reservation not released: %v %v", busy, err)
	}
	recreated, err := store.Bind(t.Context(), File{ID: id + "_old", CollectionID: file.CollectionID, RelativePath: file.RelativePath, PathKey: file.PathKey})
	if err != nil || recreated.ID == file.ID {
		t.Fatalf("old path inherited moved identity: %+v %v", recreated, err)
	}
	deletion := Operation{Request: Request{OperationID: "delete-sql", Kind: KindDelete, FileID: file.ID, BaseID: movedBase.ID, Generation: base.Generation}, File: moved, Base: movedBase, State: Pending, PathStage: "prepared"}
	if _, err := store.Receive(t.Context(), deletion); err != nil {
		t.Fatal(err)
	}
	retired := moved
	retired.Deleted = true
	retired.PathVersion = 2
	absent := makeBase(retired, base.Generation, false, nil)
	deletion.State, deletion.ResultFile, deletion.ResultBaseID = Accepted, &retired, absent.ID
	if err := store.WithLock(t.Context(), "collection:"+file.CollectionID, func(locked Store) error { return locked.CommitPath(t.Context(), deletion, retired, absent) }); err != nil {
		t.Fatal(err)
	}
	recreated, err = store.Bind(t.Context(), File{ID: id + "_new", CollectionID: file.CollectionID, RelativePath: moved.RelativePath, PathKey: moved.PathKey})
	if err != nil || recreated.ID == file.ID {
		t.Fatalf("deleted path inherited identity: %+v %v", recreated, err)
	}
	history, err := restarted.PathHistory(t.Context(), file.ID, 0, 100)
	if err != nil || len(history) != 2 || history[0].Version != 1 || !history[1].Deleted {
		t.Fatalf("durable path history: %+v %v", history, err)
	}
	persisted, err := restarted.Operation(t.Context(), deletion.Request.OperationID)
	if err != nil || persisted.State != Accepted || persisted.ResultFile == nil || !persisted.ResultFile.Deleted {
		t.Fatalf("durable path receipt: %+v %v", persisted, err)
	}

}
