package notesworkspace

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
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Metadata-only SQL responses exercise conn/tx composition without a database.
type retentionSQL struct {
	query              func(string, []driver.NamedValue) (driver.Rows, error)
	exec               func(string, []driver.NamedValue) (driver.Result, error)
	commits, rollbacks int
}
type retentionConnector struct{ state *retentionSQL }
type retentionDriver struct{}
type retentionConn struct{ state *retentionSQL }
type retentionTx struct{ state *retentionSQL }
type retentionRows struct {
	columns []string
	values  [][]driver.Value
}

func (retentionDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }
func (retentionConnector) Driver() driver.Driver         { return retentionDriver{} }
func (c retentionConnector) Connect(context.Context) (driver.Conn, error) {
	return retentionConn{c.state}, nil
}
func (retentionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (retentionConn) Close() error                { return nil }
func (c retentionConn) Begin() (driver.Tx, error) { return retentionTx{c.state}, nil }
func (c retentionConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.state.query(q, args)
}
func (c retentionConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if c.state.exec == nil {
		return nil, fmt.Errorf("unexpected mutation: %s", q)
	}
	return c.state.exec(q, args)
}
func (t retentionTx) Commit() error        { t.state.commits++; return nil }
func (t retentionTx) Rollback() error      { t.state.rollbacks++; return nil }
func (r *retentionRows) Columns() []string { return r.columns }
func (*retentionRows) Close() error        { return nil }
func (r *retentionRows) Next(dst []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dst, r.values[0])
	r.values = r.values[1:]
	return nil
}
func retentionResultRows(columns int, values ...[]driver.Value) driver.Rows {
	names := make([]string, columns)
	for i := range names {
		names[i] = fmt.Sprint(i)
	}
	return &retentionRows{names, values}
}

type retentionFixture struct {
	store    SQLStore
	db       *sql.DB
	sql      *retentionSQL
	scope    SourceScope
	from, to string
	file     File
	journal  CollectionRetentionJournal
	roots    [][]driver.Value
	busy     [][]driver.Value
	bindings [][]driver.Value
	journals [][]driver.Value
	queries  int
	verified bool
}

func newRetentionFixture(t *testing.T) *retentionFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storage := filepath.Join(root, "Storage")
	if err := os.Mkdir(storage, 0700); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(storage, &st); err != nil {
		t.Fatal(err)
	}
	f := &retentionFixture{from: filepath.Join(root, "Box", "Projects", "subject", "data"), to: filepath.Join(storage, "Records", "subject-data")}
	f.scope = SourceScope{ID: "filesystem_retained_main", Path: storage, NodeKey: "main"}
	f.scope.RootIdentity = FilesystemGeneration(f.scope.ID, f.scope.NodeKey, storage, uint64(st.Dev), st.Ino)
	f.file = File{ID: "file-one", CollectionID: "collection", RelativePath: "data/note.md", PathKey: "data/note.md", PathVersion: 3}
	raw, _ := json.Marshal(f.file)
	f.bindings = [][]driver.Value{{raw, filepath.Dir(f.from)}}
	f.journal = CollectionRetentionJournal{OperationID: "original-edit", State: Accepted, Collection: "collection", Path: "data/note.md", Journal: ".loom-notes-" + identity("original-edit")}
	f.journals = [][]driver.Value{{f.journal.OperationID, Accepted, filepath.Dir(f.from), f.journal.Path}}
	f.sql = &retentionSQL{query: f.query}
	f.db = sql.OpenDB(retentionConnector{f.sql})
	f.db.SetMaxOpenConns(1)
	t.Cleanup(func() { f.db.Close() })
	f.store = SQLStore{DB: f.db, ExclusiveSource: true}
	return f
}

func (f *retentionFixture) query(q string, args []driver.NamedValue) (driver.Rows, error) {
	f.queries++
	switch {
	case strings.Contains(q, "WHERE kind='retained'"):
		return retentionResultRows(4, []driver.Value{f.scope.ID, f.scope.Path, f.scope.NodeKey, f.scope.RootIdentity}), nil
	case strings.Contains(q, "SELECT notes_source_root_id,source_path"):
		return retentionResultRows(2, f.roots...), nil
	case strings.Contains(q, "WITH scopes AS"):
		return retentionResultRows(6, f.busy...), nil
	case strings.Contains(q, "SELECT f.record,CASE"):
		return retentionResultRows(2, f.bindings...), nil
	case strings.Contains(q, "SELECT o.operation_id,o.state"):
		return retentionResultRows(4, f.journals...), nil
	case strings.Contains(q, "SELECT CASE WHEN s.kind='notes'"):
		return retentionResultRows(2, []driver.Value{filepath.Dir(f.from), "main"}), nil
	case strings.Contains(q, "SELECT state,COALESCE(record->>'journal'"):
		previous, _ := json.Marshal(f.journal.Previous)
		return retentionResultRows(5, []driver.Value{f.journal.State, f.journal.Journal, f.journal.Collection, f.journal.Path, previous}), nil
	case strings.Contains(q, "SELECT source_path,node_key,root_identity"):
		if !strings.Contains(q, "kind IN ('box','retained')") || args[0].Value != f.scope.ID {
			return nil, fmt.Errorf("journal read did not use the explicit retained scope")
		}
		return retentionResultRows(3, []driver.Value{f.scope.Path, f.scope.NodeKey, f.scope.RootIdentity}), nil
	case strings.Contains(q, "SELECT EXISTS(SELECT 1 FROM notes_workspace.files"):
		return retentionResultRows(1, []driver.Value{f.verified}), nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", q)
	}
}

func (f *retentionFixture) plan(t *testing.T) CollectionRetentionPlan {
	t.Helper()
	p, err := f.store.PlanCollectionRetention(t.Context(), f.from, f.to, "retain-one")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCollectionRetentionPlanAndConnComposition(t *testing.T) {
	f := newRetentionFixture(t)
	conn, err := f.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// DB is deliberately nil: no secondary connection or transaction may be opened.
	p, err := (SQLStore{query: conn, ExclusiveSource: true}).PlanCollectionRetention(t.Context(), f.from, f.to, "retain-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 1 || p.Files[0].File != f.file || p.Files[0].Location.Path != "Records/subject-data/note.md" || p.Files[0].Location.Root != f.scope.Path || p.Files[0].Location.OperationID != p.ID || p.Files[0].Location.ScopeID != f.scope.ID {
		t.Fatalf("retained file plan: %+v", p)
	}
	if len(p.Journals) != 1 || p.Journals[0].Location != (JournalLocation{ScopeID: f.scope.ID, RelativePath: "Records/subject-data/note.md"}) || p.Journals[0].Previous != nil {
		t.Fatalf("retained journal plan: %+v", p.Journals)
	}
	var replay CollectionRetentionPlan
	raw, _ := json.Marshal(p)
	if json.Unmarshal(raw, &replay) != nil || collectionRetentionDigest(replay) != p.Digest {
		t.Fatal("plan digest did not survive persistence")
	}
	replay.Files[0].Location.Path = "Records/elsewhere.md"
	if collectionRetentionDigest(replay) == p.Digest {
		t.Fatal("location was not bound into the digest")
	}
}

func TestCollectionRetentionRefusesUnfinishedAndNestedSources(t *testing.T) {
	for _, kind := range []string{"operation", "upload", "tree"} {
		for _, target := range []string{"source", "destination", "ancestor", "unknown"} {
			t.Run(kind+"/"+target, func(t *testing.T) {
				f := newRetentionFixture(t)
				root, relative := filepath.Dir(f.from), "data/new.md"
				switch target {
				case "destination":
					root, relative = f.scope.Path, "Records/subject-data/new.md"
				case "ancestor":
					root, relative = filepath.Dir(root), filepath.Base(root)
				case "unknown":
					root = ""
				}
				f.busy = [][]driver.Value{{kind, "unfinished-one", root, "main", relative, ""}}
				if _, err := f.store.PlanCollectionRetention(t.Context(), f.from, f.to, "retain-one"); !errors.Is(err, ErrPathBusy) {
					t.Fatalf("unfinished work accepted: %v", err)
				}
			})
		}
	}
	t.Run("nested Notes root", func(t *testing.T) {
		f := newRetentionFixture(t)
		f.roots = [][]driver.Value{{"nested-notes", filepath.Join(f.from, "nested")}}
		if _, err := f.store.PlanCollectionRetention(t.Context(), f.from, f.to, "retain-one"); !errors.Is(err, ErrMembership) {
			t.Fatalf("nested source accepted: %v", err)
		}
	})
	t.Run("enclosing Notes root and unrelated work", func(t *testing.T) {
		f := newRetentionFixture(t)
		f.roots = [][]driver.Value{{"enclosing-notes", filepath.Dir(f.from)}}
		f.busy = [][]driver.Value{{"operation", "other", filepath.Dir(f.from), "main", "data-other/note.md", ""}}
		f.plan(t)
	})
	t.Run("same operation recovery only", func(t *testing.T) {
		f := newRetentionFixture(t)
		f.busy = [][]driver.Value{{"tree", "retain-one", filepath.Dir(f.from), "main", "data", ""}}
		f.plan(t)
		f.busy = append(f.busy, []driver.Value{"operation", "unrelated-edit", filepath.Dir(f.from), "main", "data/note.md", ""})
		if _, err := f.store.PlanCollectionRetention(t.Context(), f.from, f.to, "retain-one"); !errors.Is(err, ErrPathBusy) {
			t.Fatalf("recovery ignored another pending edit: %v", err)
		}
	})
	t.Run("destination binding", func(t *testing.T) {
		f := newRetentionFixture(t)
		file := f.file
		file.RelativePath = "Records/subject-data/note.md"
		raw, _ := json.Marshal(file)
		f.bindings = [][]driver.Value{{raw, f.scope.Path}}
		if _, err := f.store.PlanCollectionRetention(t.Context(), f.from, f.to, "retain-one"); !errors.Is(err, ErrPathBusy) {
			t.Fatalf("destination binding accepted: %v", err)
		}
	})
}

func TestCollectionRetentionCommitPreservesRequestsAndBases(t *testing.T) {
	f := newRetentionFixture(t)
	p := f.plan(t)
	var queries []string
	var retired File
	var receipt Operation
	var change PathChange
	f.sql.exec = func(q string, args []driver.NamedValue) (driver.Result, error) {
		queries = append(queries, q)
		switch {
		case strings.HasPrefix(q, "UPDATE notes_workspace.files"):
			var before File
			if json.Unmarshal(args[1].Value.([]byte), &retired) != nil || json.Unmarshal(args[2].Value.([]byte), &before) != nil || before != f.file || !strings.Contains(q, "record=$3::jsonb") {
				t.Fatal("file preimage was not checked")
			}
		case strings.HasPrefix(q, "INSERT INTO notes_workspace.operations"):
			if err := json.Unmarshal(args[2].Value.([]byte), &receipt); err != nil {
				t.Fatal(err)
			}
		case strings.HasPrefix(q, "INSERT INTO notes_workspace.path_history"):
			if err := json.Unmarshal(args[3].Value.([]byte), &change); err != nil {
				t.Fatal(err)
			}
		case strings.HasPrefix(q, "UPDATE notes_workspace.operations"):
			if args[0].Value != f.journal.OperationID || !strings.Contains(q, "jsonb_set(record,'{journal_location}'") || !strings.Contains(q, "COALESCE(record->'journal_location','null'::jsonb)=$7::jsonb") {
				t.Fatal("original operation was not location-only CAS")
			}
		default:
			t.Fatalf("unexpected write: %s", q)
		}
		return driver.RowsAffected(1), nil
	}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = (SQLStore{ExclusiveSource: true}).CommitCollectionRetentionTx(t.Context(), tx, p); err != nil {
		t.Fatal(err)
	}
	if f.sql.commits != 0 || f.sql.rollbacks != 0 || len(queries) != 4 {
		t.Fatal("component took transaction ownership or issued unexpected writes")
	}
	if !retired.Deleted || retired.Retained == nil || retired.ID != f.file.ID || retired.CollectionID != f.file.CollectionID || retired.RelativePath != f.file.RelativePath || retired.PathVersion != f.file.PathVersion+1 {
		t.Fatalf("retired binding: %+v", retired)
	}
	if change.Deleted || change.Retained == nil || change.ToPath != "" || !reflect.DeepEqual(change.Retained, retired.Retained) || receipt.Request.Kind != "collection_retention" || receipt.File != f.file {
		t.Fatalf("retention was reported as user deletion: %+v %+v", change, receipt)
	}
	if err = tx.Rollback(); err != nil || f.sql.rollbacks != 1 {
		t.Fatal("caller could not roll back its transaction")
	}
}

func TestCollectionRetentionCommitRejectsDriftAndAllowsExactReplay(t *testing.T) {
	for _, mode := range []string{"digest", "binding", "CAS", "replay", "incomplete replay"} {
		t.Run(mode, func(t *testing.T) {
			f := newRetentionFixture(t)
			p := f.plan(t)
			writes := 0
			f.sql.exec = func(string, []driver.NamedValue) (driver.Result, error) {
				writes++
				return driver.RowsAffected(0), nil
			}
			switch mode {
			case "digest":
				p.Files[0].Location.Path += "changed"
			case "binding":
				file := f.file
				file.PathVersion++
				raw, _ := json.Marshal(file)
				f.bindings[0][0] = raw
			case "replay", "incomplete replay":
				f.bindings, f.journals = nil, nil
				f.journal.Previous = &p.Journals[0].Location
				f.verified = mode == "replay"
			}
			tx, err := f.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = f.store.CommitCollectionRetentionTx(t.Context(), tx, p)
			if mode == "replay" && err != nil || mode != "replay" && err == nil {
				t.Fatalf("commit result: %v", err)
			}
			if mode != "CAS" && writes != 0 || mode == "CAS" && writes != 1 || f.sql.commits != 0 || f.sql.rollbacks != 0 {
				t.Fatalf("unexpected transaction effects: writes=%d", writes)
			}
		})
	}
}

func TestCollectionRetentionOldIDCannotWriteRecreatedPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("new source"), 0600); err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	file := File{ID: "old-file", CollectionID: "collection", RelativePath: "note.md", PathKey: "note.md"}
	base := makeBase(file, "generation", true, []byte("old source"))
	request := Request{OperationID: "pending-old", FileID: file.ID, BaseID: base.ID, Generation: base.Generation, Content: []byte("stale proposal")}
	store.bases[base.ID] = base
	store.ops[request.OperationID] = Operation{Request: request, File: file, Base: base, State: Pending}
	file.PathVersion++
	// Explicit custody guards also refuse an inconsistent record with Deleted=false.
	file.Retained = &RetainedLocation{OperationID: "retain-one", ScopeID: "filesystem_retained_main", Root: "/storage", Path: "Records/subject/note.md"}
	store.files[file.ID] = file
	svc := Service{Store: store, Sources: &testResolver{root: root, active: true, generation: base.Generation}}
	if _, err := svc.Apply(t.Context(), request.OperationID); !errors.Is(err, ErrReferenceOnly) {
		t.Fatalf("stale apply accepted: %v", err)
	}
	request.OperationID = "new-old-id"
	if _, err := svc.Receive(t.Context(), request); !errors.Is(err, ErrReferenceOnly) {
		t.Fatalf("stale receive accepted: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "note.md")); err != nil || string(raw) != "new source" {
		t.Fatal("recreated source was changed")
	}
	final := store.ops["pending-old"]
	final.State = Accepted
	store.ops[final.Request.OperationID] = final
	if replay, err := svc.Receive(t.Context(), final.Request); err != nil || !reflect.DeepEqual(replay, final) {
		t.Fatalf("original accepted receipt changed: %v", err)
	}
}

func TestCollectionRetentionJournalReadOnlyScopeAndGeneration(t *testing.T) {
	f := newRetentionFixture(t)
	location := JournalLocation{ScopeID: f.scope.ID, RelativePath: "Records/subject-data/note.md"}
	called := false
	if err := f.store.WithJournalLocation(t.Context(), location, func(src Source) error {
		called = true
		if src.Path != f.scope.Path || src.CollectionID != f.scope.ID || src.Generation != f.scope.RootIdentity {
			t.Fatalf("journal was admitted through current membership: %+v", src)
		}
		return nil
	}); err != nil || !called {
		t.Fatalf("retained journal admission: %v", err)
	}
	f.scope.RootIdentity = "stale generation"
	if err := f.store.WithJournalLocation(t.Context(), location, func(Source) error { t.Fatal("stale scope admitted"); return nil }); !errors.Is(err, ErrMembership) {
		t.Fatalf("stale generation accepted: %v", err)
	}
	if err := f.store.EnsureBoxScope(t.Context(), f.scope); err == nil {
		t.Fatal("retained scope was accepted as a writable Box scope")
	}
}

func TestCollectionRetentionRequiresNamedScopeAndNoEffectsForInvalidPlans(t *testing.T) {
	f := newRetentionFixture(t)
	for _, to := range []string{f.scope.Path, filepath.Join(f.scope.Path, "Records"), filepath.Join(f.scope.Path, "backups", "data"), f.to + "/../other"} {
		if _, err := f.store.PlanCollectionRetention(t.Context(), f.from, to, "retain-one"); err == nil {
			t.Fatalf("unsafe destination accepted: %s", to)
		}
	}
	if _, err := (SQLStore{DB: f.db}).PlanCollectionRetention(t.Context(), f.from, f.to, "retain-one"); err == nil {
		t.Fatal("plan admitted without exclusive owner contract")
	}
	if err := f.store.CommitCollectionRetentionTx(t.Context(), nil, CollectionRetentionPlan{}); !errors.Is(err, ErrIntentMismatch) {
		t.Fatalf("nil transaction accepted: %v", err)
	}
}
