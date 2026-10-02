package notesworkspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Small in-memory store models committed records, with copy isolation and one
// optional failure at an ordinary persistence boundary. PostgreSQL stays separate.
type memoryStore struct {
	mu               sync.Mutex
	files            map[string]File
	bases            map[string]Base
	ops              map[string]Operation
	beforeSave       func(Operation) error
	recoveries       map[string]Recovery
	beforeRecovery   func(Recovery) error
	beforeCommitPath func(Operation) error
	afterCommitPath  func(Operation) error
	history          []PathChange
}

func newMemoryStore() *memoryStore {
	return &memoryStore{files: map[string]File{}, bases: map[string]Base{}, ops: map[string]Operation{}, recoveries: map[string]Recovery{}}
}
func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }
func (m *memoryStore) WithLock(_ context.Context, _ string, fn func(Store) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(m)
}
func (m *memoryStore) Bind(_ context.Context, f File) (File, error) {
	for _, old := range m.files {
		if !old.Deleted && old.CollectionID == f.CollectionID && old.PathKey == f.PathKey {
			return old, nil
		}
	}
	m.files[f.ID] = f
	return f, nil
}
func (m *memoryStore) File(_ context.Context, id string) (File, error) {
	v, ok := m.files[id]
	if !ok {
		return v, sql.ErrNoRows
	}
	return v, nil
}
func (m *memoryStore) SaveBase(_ context.Context, b Base) error { m.bases[b.ID] = clone(b); return nil }
func (m *memoryStore) Base(_ context.Context, id string) (Base, error) {
	v, ok := m.bases[id]
	if !ok {
		return v, sql.ErrNoRows
	}
	return clone(v), nil
}
func (m *memoryStore) Receive(_ context.Context, op Operation) (Operation, error) {
	if old, ok := m.ops[op.Request.OperationID]; ok {
		if !sameIntent(old, op) {
			return Operation{}, ErrIntentMismatch
		}
		return clone(old), nil
	}
	m.ops[op.Request.OperationID] = clone(op)
	return clone(op), nil
}
func (m *memoryStore) Operation(_ context.Context, id string) (Operation, error) {
	v, ok := m.ops[id]
	if !ok {
		return v, sql.ErrNoRows
	}
	return clone(v), nil
}
func (m *memoryStore) SaveOperation(_ context.Context, op Operation) error {
	if m.beforeSave != nil {
		if err := m.beforeSave(op); err != nil {
			return err
		}
	}
	m.ops[op.Request.OperationID] = clone(op)
	return nil
}

func (m *memoryStore) SaveRecovery(_ context.Context, r Recovery) error {
	if m.beforeRecovery != nil {
		if err := m.beforeRecovery(r); err != nil {
			return err
		}
	}
	m.recoveries[r.ID] = clone(r)
	return nil
}

func (m *memoryStore) FileAt(_ context.Context, collection, key string) (File, error) {
	for _, f := range m.files {
		if !f.Deleted && f.CollectionID == collection && f.PathKey == key {
			return f, nil
		}
	}
	return File{}, sql.ErrNoRows
}
func (m *memoryStore) PathBusy(_ context.Context, collection, key, except string) (bool, error) {
	for _, op := range m.ops {
		if op.File.CollectionID == collection && op.Request.OperationID != except && (op.State == Pending || op.State == Held) && op.PathStage != "" && (op.File.PathKey == key || op.DestinationKey == key) {
			return true, nil
		}
	}
	return false, nil
}
func (m *memoryStore) CommitPath(_ context.Context, op Operation, file File, base Base) error {
	if m.beforeCommitPath != nil {
		if err := m.beforeCommitPath(op); err != nil {
			return err
		}
	}
	old, ok := m.files[file.ID]
	if !ok || old.Deleted || old.PathVersion != op.File.PathVersion {
		return ErrStalePath
	}
	for _, other := range m.files {
		if !file.Deleted && !other.Deleted && other.ID != file.ID && other.CollectionID == file.CollectionID && other.PathKey == file.PathKey {
			return ErrPathBusy
		}
	}
	m.files[file.ID] = clone(file)
	m.bases[base.ID] = clone(base)
	m.ops[op.Request.OperationID] = clone(op)
	change := PathChange{FileID: file.ID, OperationID: op.Request.OperationID, Version: file.PathVersion, FromPath: op.File.RelativePath, Deleted: file.Deleted}
	if !file.Deleted {
		change.ToPath = file.RelativePath
	}
	m.history = append(m.history, change)
	if m.afterCommitPath != nil {
		return m.afterCommitPath(op)
	}
	return nil
}

type testResolver struct {
	root       string
	active     bool
	generation string
}

func (r *testResolver) WithSource(_ context.Context, id, relative string, fn func(Source) error) error {
	if !r.active || id != "collection" {
		return ErrMembership
	}
	return fn(Source{CollectionID: id, Generation: r.generation, Path: r.root})
}
func (r *testResolver) WithPaths(ctx context.Context, id string, paths []string, fn func(Source) error) error {
	return r.WithSource(ctx, id, paths[0], fn)
}
func fixture(t *testing.T) (Service, *memoryStore, *testResolver) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := newMemoryStore()
	r := &testResolver{root: root, active: true, generation: "generation-1"}
	return Service{Store: m, Sources: r}, m, r
}
func readBase(t *testing.T, s Service, name string) ReadResult {
	t.Helper()
	out, err := s.Read(t.Context(), "collection", name)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReferenceSnapshotCannotBecomeWriteIntent(t *testing.T) {
	s, _, r := fixture(t)
	content := []byte{137, 80, 78, 71, 0, 255}
	name := filepath.Join(r.root, "image.png")
	if err := os.WriteFile(name, content, 0600); err != nil {
		t.Fatal(err)
	}
	b := readBase(t, s, "image.png")
	if string(b.Base.Content) != string(content) || !b.Base.Exists {
		t.Fatal("binary snapshot changed")
	}
	_, err := s.Receive(t.Context(), Request{OperationID: "reference-write", FileID: b.File.ID, Generation: b.Base.Generation, BaseID: b.Base.ID, Content: []byte("replacement")})
	if !errors.Is(err, ErrReferenceOnly) {
		t.Fatalf("reference mutation: %v", err)
	}
	if _, err := s.Read(t.Context(), "collection", "absent.pdf"); !errors.Is(err, ErrReferenceOnly) {
		t.Fatalf("absent reference: %v", err)
	}
	if err := os.Symlink(name, filepath.Join(r.root, "link.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(t.Context(), "collection", "link.png"); err == nil {
		t.Fatal("followed reference symlink")
	}
}

func TestReferenceSizeDoesNotWidenTextReads(t *testing.T) {
	s, _, r := fixture(t)
	content := strings.Repeat("x", MaxContentBytes+1)
	for _, name := range []string{"large.pdf", "large.md"} {
		if err := os.WriteFile(filepath.Join(r.root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if b := readBase(t, s, "large.pdf"); len(b.Base.Content) != len(content) {
		t.Fatal("large reference changed")
	}
	if _, err := s.Read(t.Context(), "collection", "large.md"); err == nil {
		t.Fatal("reference allowance widened text reads")
	}
	f, err := os.Create(filepath.Join(r.root, "oversized.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(MaxReferenceBytes + 1)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(t.Context(), "collection", "oversized.pdf"); err == nil {
		t.Fatal("oversized reference accepted")
	}
}
func receive(t *testing.T, s Service, id string, base ReadResult, body string) Operation {
	t.Helper()
	op, err := s.Receive(t.Context(), Request{OperationID: id, FileID: base.File.ID, Generation: base.Base.Generation, BaseID: base.Base.ID, Content: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func apply(t *testing.T, s Service, id, state string) Operation {
	t.Helper()
	op, err := s.Apply(t.Context(), id)
	if err != nil || op.State != state {
		t.Fatalf("apply: %+v %v, want %s", op, err, state)
	}
	return op
}
func write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func contents(t *testing.T, name, want string) {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil || string(b) != want {
		t.Fatalf("%s: %q %v, want %q", name, b, err, want)
	}
}

func TestCreateEditAndExactReplay(t *testing.T) {
	s, m, r := fixture(t)
	absent := readBase(t, s, "note.md")
	if absent.Base.Exists {
		t.Fatal("create base not absent")
	}
	op := receive(t, s, "create", absent, "A")
	if op.State != Pending || string(m.ops["create"].Request.Content) != "A" {
		t.Fatal("intent not committed")
	}
	if _, err := os.Stat(filepath.Join(r.root, "note.md")); !os.IsNotExist(err) {
		t.Fatal("receive mutated source")
	}
	created := apply(t, s, "create", Accepted)
	contents(t, filepath.Join(r.root, "note.md"), "A")
	base := readBase(t, s, "note.md")
	if base.File.ID != absent.File.ID || base.Base.ID != created.ResultBaseID {
		t.Fatal("identity/base changed")
	}
	receive(t, s, "edit", base, "B")
	edited := apply(t, s, "edit", Accepted)
	contents(t, filepath.Join(r.root, "note.md"), "B")
	contents(t, filepath.Join(r.root, edited.Journal, "displaced"), "A")
	// Late replay cannot overwrite newer independent source content.
	write(t, filepath.Join(r.root, "note.md"), "C")
	replay := apply(t, Service{Store: m, Sources: r}, "edit", Accepted)
	if replay.ResultBaseID != edited.ResultBaseID {
		t.Fatal("acknowledgement changed")
	}
	contents(t, filepath.Join(r.root, "note.md"), "C")
	request := op.Request
	request.Content = []byte("different")
	if _, err := s.Receive(t.Context(), request); !errors.Is(err, ErrIntentMismatch) {
		t.Fatalf("changed replay: %v", err)
	}
}

func TestStaleBaseAndMembershipHold(t *testing.T) {
	s, m, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	base := readBase(t, s, "note.md")
	receive(t, s, "stale", base, "C")
	write(t, name, "B")
	op := apply(t, s, "stale", Conflict)
	if string(op.Observed) != "B" || string(op.Base.Content) != "A" || string(op.Request.Content) != "C" {
		t.Fatal("conflict variants lost")
	}
	contents(t, name, "B")
	base = readBase(t, s, "note.md")
	receive(t, s, "withdrawn", base, "D")
	r.active = false
	apply(t, s, "withdrawn", Held)
	contents(t, name, "B")
	r.active = true
	r.generation = "generation-2"
	apply(t, Service{Store: m, Sources: r}, "withdrawn", Held)
	contents(t, name, "B")
	if string(m.ops["withdrawn"].Request.Content) != "D" {
		t.Fatal("withdrawal discarded pending bytes")
	}
}

func TestRacingReplacementPreservesDisplacedBytes(t *testing.T) {
	s, m, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	base := readBase(t, s, "note.md")
	receive(t, s, "race", base, "C")
	raced := false
	m.beforeSave = func(op Operation) error {
		if !raced && op.Journal != "" {
			raced = true
			replacement := filepath.Join(r.root, "agent.md")
			write(t, replacement, "B")
			if err := os.Rename(replacement, name); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	op := apply(t, s, "race", Conflict)
	contents(t, name, "B")
	if string(op.Observed) != "B" || string(op.Request.Content) != "C" || string(op.Base.Content) != "A" {
		t.Fatal("racing variants lost")
	}
}

func TestConcurrentCreateAfterDisplacementDoesNotGetOverwritten(t *testing.T) {
	s, m, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	base := readBase(t, s, "note.md")
	receive(t, s, "race", base, "C")
	raced := false
	m.beforeSave = func(op Operation) error {
		if !raced && op.ObservedExists {
			raced = true
			write(t, name, "B")
		}
		return nil
	}
	op := apply(t, s, "race", Conflict)
	contents(t, name, "B")
	contents(t, filepath.Join(r.root, op.Journal, "displaced"), "A")
	if string(op.Observed) != "B" {
		t.Fatal("replacement not exposed")
	}
}

func TestRestartAfterDisplacementAndAfterPublication(t *testing.T) {
	for _, point := range []string{"displaced", "published"} {
		t.Run(point, func(t *testing.T) {
			s, m, r := fixture(t)
			name := filepath.Join(r.root, "note.md")
			write(t, name, "A")
			base := readBase(t, s, "note.md")
			receive(t, s, "save", base, "B")
			failed := false
			m.beforeSave = func(op Operation) error {
				if !failed && ((point == "displaced" && op.ObservedExists) || (point == "published" && op.State == Accepted)) {
					failed = true
					return errors.New("interrupted persistence")
				}
				return nil
			}
			held := apply(t, s, "save", Held)
			if point == "displaced" {
				contents(t, name, "A")
				contents(t, filepath.Join(r.root, held.Journal, "proposed"), "B")
			} else {
				contents(t, filepath.Join(r.root, held.Journal, "displaced"), "A")
			}
			m.beforeSave = nil
			op := apply(t, Service{Store: m, Sources: r}, "save", Accepted)
			contents(t, name, "B")
			if op.ResultBaseID == "" || string(op.Base.Content) != "A" {
				t.Fatal("recovery lost exact base")
			}
		})
	}
}

func TestReferenceAndPathGuards(t *testing.T) {
	s, _, r := fixture(t)
	for _, name := range []string{"../escape.md", "/escape.md", ".env/note.md", "credentials/note.md", "report.pdf", "a/../../escape.md", "cafe\u0301.md"} {
		if _, err := s.Read(t.Context(), "collection", name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	write(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(r.root, "link.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(t.Context(), "collection", "link.md"); err == nil {
		t.Fatal("followed symlink")
	}
	write(t, filepath.Join(r.root, "Note.md"), "upper")
	if _, err := s.Read(t.Context(), "collection", "note.md"); err == nil {
		t.Fatal("case alias accepted")
	}
	contents(t, outside, "outside")
}

func TestRestartDuringConflictRestoration(t *testing.T) {
	s, m, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	base := readBase(t, s, "note.md")
	receive(t, s, "race", base, "C")
	raced, failed := false, false
	m.beforeSave = func(op Operation) error {
		if !raced && op.Journal != "" {
			raced = true
			replacement := filepath.Join(r.root, "agent.md")
			write(t, replacement, "B")
			if err := os.Rename(replacement, name); err != nil {
				t.Fatal(err)
			}
		}
		if !failed && op.Reason == "source diverged from exact base" {
			failed = true
			return errors.New("interrupted conflict custody")
		}
		return nil
	}
	held := apply(t, s, "race", Held)
	contents(t, name, "B")
	contents(t, filepath.Join(r.root, held.Journal, "proposed"), "C")
	m.beforeSave = nil
	op := apply(t, Service{Store: m, Sources: r}, "race", Conflict)
	contents(t, name, "B")
	if string(op.Observed) != "B" {
		t.Fatal("recovery lost conflict variant")
	}
}

func TestRetainsDisplacedInodeForAlreadyOpenWriter(t *testing.T) {
	s, _, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	base := readBase(t, s, "note.md")
	writer, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	receive(t, s, "edit", base, "C")
	op := apply(t, s, "edit", Accepted)
	if _, err := writer.WriteAt([]byte("B"), 0); err != nil {
		t.Fatal(err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatal(err)
	}
	contents(t, name, "C")
	contents(t, filepath.Join(r.root, op.Journal, "displaced"), "B")
}

func TestFailedPublicationRestoresBeforeMembershipWithdrawal(t *testing.T) {
	for _, point := range []string{"save-error", "canceled", "replacement", "unsafe-parent", "unsafe-parent-canceled"} {
		t.Run(point, func(t *testing.T) {
			s, m, r := fixture(t)
			name := filepath.Join(r.root, "note.md")
			write(t, name, "A")
			base := readBase(t, s, "note.md")
			receive(t, s, "save", base, "B")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("save failed after displacement")
			failed := false
			journalRoot := r.root
			m.beforeSave = func(op Operation) error {
				if failed && point == "unsafe-parent-canceled" {
					return ctx.Err()
				}
				if !failed && op.ObservedExists {
					failed = true
					if _, err := os.Stat(name); !os.IsNotExist(err) {
						t.Fatalf("source not displaced: %v", err)
					}
					switch point {
					case "canceled":
						cancel()
						return ctx.Err()
					case "replacement":
						write(t, name, "C")
					case "unsafe-parent", "unsafe-parent-canceled":
						journalRoot = r.root + "-moved"
						if err := os.Rename(r.root, journalRoot); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							if err := os.Rename(journalRoot, r.root); err != nil {
								t.Error(err)
							}
						})
					}
					if point == "unsafe-parent-canceled" {
						cancel()
					}
					return failure
				}
				return nil
			}
			held, err := s.Apply(ctx, "save")
			if point == "unsafe-parent-canceled" {
				if !errors.Is(err, context.Canceled) || !errors.Is(err, failure) || !strings.Contains(err.Error(), "restore displaced source") {
					t.Fatalf("lost original/cleanup/hold error: %v", err)
				}
				held = m.ops["save"]
				contents(t, filepath.Join(journalRoot, held.Journal, "displaced"), "A")
			} else if err != nil || held.State != Held || !failed {
				t.Fatalf("apply: %+v %v", held, err)
			}
			if point == "unsafe-parent" {
				if !strings.Contains(held.Reason, "restore displaced source") {
					t.Fatalf("missing restoration failure: %s", held.Reason)
				}
				contents(t, filepath.Join(journalRoot, held.Journal, "displaced"), "A")
			} else if point == "replacement" {
				contents(t, name, "C")
				contents(t, filepath.Join(journalRoot, held.Journal, "displaced"), "A")
			} else if point != "unsafe-parent-canceled" {
				contents(t, name, "A")
			}
			contents(t, filepath.Join(journalRoot, held.Journal, "proposed"), "B")
			if string(m.ops["save"].Request.Content) != "B" || held.Journal == "" {
				t.Fatal("recovery intent lost")
			}
			m.beforeSave = nil
			r.active = false
			apply(t, s, "save", Held)
			if point == "save-error" || point == "canceled" {
				contents(t, name, "A")
			}
		})
	}
}
