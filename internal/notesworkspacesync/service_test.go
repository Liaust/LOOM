package notesworkspacesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"loom.local/loom/internal/notesworkspace"
)

func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }

type memoryRecords struct {
	data    map[string]json.RawMessage
	epoch   string
	failAck bool
}

func (m *memoryRecords) WithReplica(c context.Context, _ string, epoch string, fn func(Records) error) error {
	if m.epoch != "" && m.epoch != epoch {
		return ErrHeld
	}
	m.epoch = epoch
	return fn(m)
}
func (m *memoryRecords) Get(_ context.Context, k, id string, out any) error {
	raw, ok := m.data[k+":"+id]
	if !ok {
		return sql.ErrNoRows
	}
	return json.Unmarshal(raw, out)
}
func (m *memoryRecords) Put(_ context.Context, k, id string, value any) error {
	raw, e := json.Marshal(value)
	if e == nil {
		m.data[k+":"+id] = raw
	}
	return e
}
func (m *memoryRecords) List(_ context.Context, k, after string, limit int) ([]string, error) {
	var out []string
	prefix := k + ":"
	for v := range m.data {
		if len(v) > len(prefix) && v[:len(prefix)] == prefix && v[len(prefix):] > after {
			out = append(out, v[len(prefix):])
		}
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memoryRecords) ListPending(ctx context.Context, k, after string, limit int) ([]string, error) {
	all, err := m.List(ctx, k, after, len(m.data))
	var ids []string
	for _, id := range all {
		pending := false
		if k == "operation" {
			j, _ := load[Join](ctx, m, k, id)
			pending = !j.AckPublished
		} else {
			o, _ := load[Observation](ctx, m, k, id)
			pending = o.Reason == "control_pending"
		}
		if pending && len(ids) < limit {
			ids = append(ids, id)
		}
	}
	return ids, err
}

// Real source Service/filesystem; only its repository persistence is an in-memory
// unit fixture.
type sourceStore struct {
	files           map[string]notesworkspace.File
	bases           map[string]notesworkspace.Base
	ops             map[string]notesworkspace.Operation
	reads           int
	afterCommitPath func() error
}

func (m *sourceStore) WithLock(c context.Context, _ string, f func(notesworkspace.Store) error) error {
	return f(m)
}
func (m *sourceStore) Bind(_ context.Context, f notesworkspace.File) (notesworkspace.File, error) {
	for _, old := range m.files {
		if !old.Deleted && old.CollectionID == f.CollectionID && old.PathKey == f.PathKey {
			return old, nil
		}
	}
	m.files[f.ID] = f
	return f, nil
}
func (m *sourceStore) File(_ context.Context, id string) (notesworkspace.File, error) {
	v, ok := m.files[id]
	if !ok {
		return v, sql.ErrNoRows
	}
	return v, nil
}
func (m *sourceStore) Base(_ context.Context, id string) (notesworkspace.Base, error) {
	v, ok := m.bases[id]
	if !ok {
		return v, sql.ErrNoRows
	}
	return clone(v), nil
}
func (m *sourceStore) SaveBase(_ context.Context, v notesworkspace.Base) error {
	m.bases[v.ID] = clone(v)
	return nil
}
func (m *sourceStore) Receive(_ context.Context, v notesworkspace.Operation) (notesworkspace.Operation, error) {
	if old, ok := m.ops[v.Request.OperationID]; ok {
		a, _ := json.Marshal(old.Request)
		b, _ := json.Marshal(v.Request)
		if string(a) != string(b) {
			return old, notesworkspace.ErrIntentMismatch
		}
		return clone(old), nil
	}
	m.ops[v.Request.OperationID] = clone(v)
	return v, nil
}
func (m *sourceStore) Operation(_ context.Context, id string) (notesworkspace.Operation, error) {
	v, ok := m.ops[id]
	if !ok {
		return v, sql.ErrNoRows
	}
	return clone(v), nil
}
func (m *sourceStore) SaveOperation(_ context.Context, v notesworkspace.Operation) error {
	m.ops[v.Request.OperationID] = clone(v)
	return nil
}
func (m *sourceStore) SaveRecovery(context.Context, notesworkspace.Recovery) error { return nil }

func (m *sourceStore) FileAt(_ context.Context, collection, key string) (notesworkspace.File, error) {
	for _, f := range m.files {
		if !f.Deleted && f.CollectionID == collection && f.PathKey == key {
			return f, nil
		}
	}
	return notesworkspace.File{}, sql.ErrNoRows
}
func (m *sourceStore) PathBusy(_ context.Context, collection, key, except string) (bool, error) {
	for _, op := range m.ops {
		if op.File.CollectionID == collection && op.Request.OperationID != except && (op.State == notesworkspace.Pending || op.State == notesworkspace.Held) && op.PathStage != "" && (op.File.PathKey == key || op.DestinationKey == key) {
			return true, nil
		}
	}
	return false, nil
}
func (m *sourceStore) CommitPath(_ context.Context, op notesworkspace.Operation, file notesworkspace.File, base notesworkspace.Base) error {
	old, ok := m.files[file.ID]
	if !ok || old.Deleted || old.PathVersion != op.File.PathVersion {
		return notesworkspace.ErrStalePath
	}
	for _, other := range m.files {
		if !file.Deleted && !other.Deleted && other.ID != file.ID && other.CollectionID == file.CollectionID && other.PathKey == file.PathKey {
			return notesworkspace.ErrPathBusy
		}
	}
	m.files[file.ID], m.bases[base.ID], m.ops[op.Request.OperationID] = clone(file), clone(base), clone(op)
	if m.afterCommitPath != nil {
		return m.afterCommitPath()
	}
	return nil
}

type resolver struct{ root string }

func (r resolver) WithSource(_ context.Context, id, _ string, fn func(notesworkspace.Source) error) error {
	return fn(notesworkspace.Source{CollectionID: id, Generation: "generation", Path: r.root})
}
func (r resolver) WithPaths(ctx context.Context, id string, paths []string, fn func(notesworkspace.Source) error) error {
	return r.WithSource(ctx, id, paths[0], fn)
}

type fakeNative struct {
	docs       map[string]Evidence
	records    []Evidence
	acks       []Ack
	counter    int
	ackFailure bool
	epoch      string
	bindings   []Binding
}

func (n *fakeNative) Call(_ context.Context, req map[string]any, out any) error {
	var result any
	switch req["method"] {
	case "status":
		result = NativeStatus{Status: "ready", Epoch: n.epoch, EncryptionEnabled: true, ClientIntentVersion: 1}
	case "changes":
		raw, _ := json.Marshal(req["since"])
		var start int
		_ = json.Unmarshal(raw, &start)
		limit := req["limit"].(int)
		end := start + limit
		if end > len(n.records) {
			end = len(n.records)
		}
		var page NativeChanges
		page.Status = "available"
		page.Next, _ = json.Marshal(end)
		for _, v := range n.records[start:end] {
			page.Changes = append(page.Changes, struct {
				ID     string     `json:"id"`
				Leaves []Evidence `json:"leaves"`
			}{v.ID, []Evidence{v}})
		}
		result = page
	case "read.path":
		result = n.docs[req["path"].(string)+"@"+req["revision"].(string)]
	case "payload.read":
		result = n.docs[Prefix+"payload/"+req["intentId"].(string)+".md@1-control"]
	case "control.read":
		result = n.docs[req["id"].(string)+"@"+req["revision"].(string)]
	case "publish", "publish.reference":
		p := req["path"].(string)
		var b []byte
		raw, _ := json.Marshal(req["contentBase64"])
		_ = json.Unmarshal(raw, &b)
		n.counter++
		rev := "1-root"
		var parents []string
		if parent, ok := req["baseRevision"].(string); ok {
			var generation int
			_, _ = fmt.Sscanf(parent, "%d-", &generation)
			rev = fmt.Sprintf("%d-next", generation+1)
			parents = []string{parent}
		}
		evidence := Evidence{ID: p, Path: p, Revision: rev, Kind: "file", Status: "available", Content: b, SHA256: digest(b)[7:], AncestryAvailable: true, AncestryCompleteToRoot: true, Ancestors: parents}
		n.docs[p+"@"+rev] = evidence
		evidence.Status = "published"
		result = evidence
	case "control.put":
		if n.ackFailure {
			return errors.New("temporarily unavailable")
		}
		raw, _ := json.Marshal(req["contentBase64"])
		var bytes []byte
		_ = json.Unmarshal(raw, &bytes)
		var h Header
		_ = json.Unmarshal(bytes, &h)
		if h.Kind == "ack" {
			var a Ack
			_ = json.Unmarshal(bytes, &a)
			n.acks = append(n.acks, a)
		} else if h.Kind == "binding" {
			var b Binding
			_ = json.Unmarshal(bytes, &b)
			n.bindings = append(n.bindings, b)
		}
		result = Evidence{Status: "published", Revision: "1-control"}
	default:
		return errors.New("unexpected method")
	}
	raw, e := json.Marshal(result)
	if e != nil {
		return e
	}
	return json.Unmarshal(raw, out)
}
func (n *fakeNative) add(v any) {
	raw, _ := json.Marshal(v)
	var h Header
	_ = json.Unmarshal(raw, &h)
	p := controlPath(h)
	e := Evidence{ID: p, Path: p, Revision: "1-control", Kind: "control", Status: "available", Content: raw, SHA256: digest(raw)[7:], AncestryAvailable: true, AncestryCompleteToRoot: true}
	n.docs[p+"@1-control"] = e
	n.records = append(n.records, e)
}
func (n *fakeNative) content(path, rev, parent, text string) {
	n.docs[path+"@"+rev] = Evidence{ID: path, Path: path, Revision: rev, Kind: "file", Status: "available", Content: []byte(text), SHA256: digest([]byte(text))[7:], AncestryAvailable: true, AncestryCompleteToRoot: true, Ancestors: []string{parent}}
}
func fixture(t *testing.T) (*Service, *fakeNative, *sourceStore, string, Binding) {
	t.Helper()
	root, resolveErr := filepath.EvalSymlinks(t.TempDir())
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if e := os.WriteFile(filepath.Join(root, "a.md"), []byte("A"), 0600); e != nil {
		t.Fatal(e)
	}
	source := &sourceStore{files: map[string]notesworkspace.File{}, bases: map[string]notesworkspace.Base{}, ops: map[string]notesworkspace.Operation{}}
	service := notesworkspace.Service{Sources: resolver{root}, Store: source}
	read, e := service.Read(context.Background(), "source", "a.md")
	if e != nil {
		t.Fatal(e)
	}
	native := &fakeNative{docs: map[string]Evidence{}, epoch: "epoch"}
	s := &Service{Replica: "replica", Epoch: "epoch", Scopes: []Scope{{Workspace: "w", Collection: "c", SourceCollection: "source", Generation: "generation", Root: "Notes"}}, Source: NotesSource{Service: service}, Native: native, Store: &memoryRecords{data: map[string]json.RawMessage{}}}
	b, e := s.Export(context.Background(), "c", read.File.ID, read.Base.ID)
	if e != nil {
		t.Fatal(e)
	}
	return s, native, source, root, b
}
func operation(b Binding, id, text, rev string) (Intent, Publication) {
	i := Intent{Header: Header{1, "intent", id}, Device: "device", Operation: "edit", Workspace: b.Workspace, Collection: b.Collection, Generation: b.Generation, FileID: b.FileID, Base: &b, Path: b.Path, SHA256: digest([]byte(text)), Length: len(text)}
	raw, _ := json.Marshal(i)
	return i, Publication{Header: Header{1, "publication", "pub_" + id}, IntentID: id, IntentDigest: digest(raw), Revisions: []Revision{{Path: b.Path, Revision: rev, Role: "content"}}}
}

func TestReferenceExportRetainsBytesAndRejectsWritableBinding(t *testing.T) {
	s, native, _, root, _ := fixture(t)
	content := []byte{137, 80, 78, 71, 0, 255}
	if err := os.WriteFile(filepath.Join(root, "image.png"), content, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := s.Source.Read(t.Context(), "source", "image.png")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Export(t.Context(), "c", r.File.ID, r.Base.ID)
	if err != nil || b.Writable || !b.valid() {
		t.Fatalf("reference export: %+v %v", b, err)
	}
	if got := native.docs[b.Path+"@"+b.NativeRevision]; string(got.Content) != string(content) {
		t.Fatal("binary bytes changed")
	}
	b.Writable = true
	if b.valid() {
		t.Fatal("binary binding allowed writes")
	}
	if !IsSourcePath("attachments/image.png") || IsSourcePath(".private/image.png") || IsSourcePath("run.exe") {
		t.Fatal("reference selection")
	}
}

func TestMigrationReferencesRemainReadOnly(t *testing.T) {
	for _, extension := range []string{"canvas", "wav", "svg", "csv", "xlsx", "dat", "json"} {
		t.Run(extension, func(t *testing.T) {
			s, native, _, root, _ := fixture(t)
			name := "attachment." + extension
			content := []byte{0, 255, 42}
			if err := os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
				t.Fatal(err)
			}
			r, err := s.Source.Read(t.Context(), "source", name)
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.Export(t.Context(), "c", r.File.ID, r.Base.ID)
			if err != nil || b.Writable || !b.valid() {
				t.Fatalf("binding: %+v %v", b, err)
			}
			if got := native.docs[b.Path+"@"+b.NativeRevision]; string(got.Content) != string(content) {
				t.Fatal("reference bytes changed")
			}
			b.Writable = true
			if b.valid() {
				t.Fatal("reference accepted a writable binding")
			}
		})
	}
}

func TestLargeMarkdownExportAndControlBound(t *testing.T) {
	s, native, _, root, _ := fixture(t)
	content := make([]byte, 1200000)
	for i := range content {
		content[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(root, "large.md"), content, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := s.Source.Read(t.Context(), "source", "large.md")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Export(t.Context(), "c", r.File.ID, r.Base.ID)
	if err != nil || !b.Writable {
		t.Fatalf("export: %+v %v", b, err)
	}
	if got := native.docs[b.Path+"@"+b.NativeRevision]; string(got.Content) != string(content) {
		t.Fatal("large Markdown truncated")
	}
	i, _ := operation(b, "large_edit", string(content), "2-large")
	raw, _ := json.Marshal(i)
	if _, err := parseIntent(raw); err != nil {
		t.Fatal("large Markdown intent refused")
	}
	i.Length = MaxBytes + 1
	raw, _ = json.Marshal(i)
	if _, err := parseIntent(raw); err == nil {
		t.Fatal("oversized text intent admitted")
	}
	if MaxBytes != notesworkspace.MaxContentBytes || MaxControlBytes != 1<<20 {
		t.Fatal("content/control bounds diverged")
	}
}

type generationResolver struct {
	root, generation string
}

func (r generationResolver) WithSource(_ context.Context, id, _ string, fn func(notesworkspace.Source) error) error {
	return fn(notesworkspace.Source{CollectionID: id, Generation: r.generation, Path: r.root})
}

func (r generationResolver) WithPaths(ctx context.Context, id string, paths []string, fn func(notesworkspace.Source) error) error {
	return r.WithSource(ctx, id, paths[0], fn)
}

type transitionNative struct {
	*fakeNative
	heads []Evidence
}

func (n transitionNative) Call(ctx context.Context, req map[string]any, out any) error {
	if req["method"] == "leaves" {
		raw, _ := json.Marshal(struct {
			Leaves []Evidence `json:"leaves"`
		}{n.heads})
		return json.Unmarshal(raw, out)
	}
	return n.fakeNative.Call(ctx, req, out)
}

func TestEnrollmentExportDiscoveryIsPagedAndScopeBound(t *testing.T) {
	s, _, _, _, old := fixture(t)
	records := s.Store.(*memoryRecords)
	if err := records.Put(t.Context(), "file", "000-unrelated", FileMapping{Scope: Scope{Workspace: "other"}}); err != nil {
		t.Fatal(err)
	}
	s.Scopes[0].Generation = "expanded"
	s.Scopes[0].PreviousGeneration = old.Generation
	page, next, err := s.enrollmentExports(t.Context(), "", 1)
	if err != nil || len(page) != 0 || next != "000-unrelated" {
		t.Fatalf("first page: %v %q %v", page, next, err)
	}
	page, next, err = s.enrollmentExports(t.Context(), next, 32)
	if err != nil || len(page) != 1 || next != "" || page[0].ClientFileID != old.FileID {
		t.Fatalf("previous enrollment discovery: %v %q %v", page, next, err)
	}
	s.Scopes[0].Root = "Elsewhere"
	page, _, err = s.enrollmentExports(t.Context(), "", 32)
	if err != nil || len(page) != 0 {
		t.Fatal("different root discovered as a predecessor")
	}
	s.Scopes[0].Root = "Notes"
	s.Scopes[0].PreviousGeneration = "unapproved"
	page, _, err = s.enrollmentExports(t.Context(), "", 32)
	if err != nil || len(page) != 0 {
		t.Fatal("unapproved generation discovered")
	}
	if _, _, err := s.enrollmentExports(t.Context(), "", 0); !errors.Is(err, ErrHeld) {
		t.Fatal("unbounded discovery accepted")
	}
}

func TestExportSelectionTransitionPreservesHistory(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending_edit_%v", blocked), func(t *testing.T) {
			s, native, source, root, old := fixture(t)
			records := s.Store.(*memoryRecords)
			oldKey := "file:" + mapKey(Intent{Workspace: old.Workspace, Collection: old.Collection, Generation: old.Generation, FileID: old.FileID})
			oldRecord := string(records.data[oldKey])
			sourceService := notesworkspace.Service{Sources: generationResolver{root, "expanded"}, Store: source}
			s.Source = NotesSource{Service: sourceService}
			s.Scopes[0].Generation = "expanded"
			s.Scopes[0].PreviousGeneration = old.Generation
			head := native.docs[old.Path+"@"+old.NativeRevision]
			s.Native = transitionNative{fakeNative: native, heads: []Evidence{head}}
			read, err := s.Source.Read(t.Context(), "source", "a.md")
			if err != nil {
				t.Fatal(err)
			}
			if blocked {
				newer := head
				newer.Revision = "2-offline"
				s.Native = transitionNative{fakeNative: native, heads: []Evidence{newer}}
				if _, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID); !errors.Is(err, ErrHeld) {
					t.Fatalf("pending edit not retained: %v", err)
				}
				if native.counter != 1 || string(records.data[oldKey]) != oldRecord {
					t.Fatal("held transition changed content/history")
				}
				s.Native = transitionNative{fakeNative: native, heads: []Evidence{head}}
			}
			next, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID)
			if err != nil {
				t.Fatal(err)
			}
			if next.FileID != old.FileID || next.Generation != "expanded" || next.SourceSequence != old.SourceSequence+1 || next.NativeRevision == old.NativeRevision {
				t.Fatalf("transition identity: %+v", next)
			}
			if string(records.data[oldKey]) != oldRecord {
				t.Fatal("old mapping rewritten")
			}
			if oldIntent, _ := operation(old, "old_edit", "pending", "2-old"); func() bool { _, ok := s.scope(oldIntent); return ok }() {
				t.Fatal("withdrawn generation authorizes an incoming edit")
			}
			s.Scopes[0].PreviousGeneration = ""
			replayed, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID)
			if err != nil || replayed != next || native.counter != 2 {
				t.Fatalf("transition replay: %+v %v", replayed, err)
			}
		})
	}
}

func TestExportSourceReversionAdvancesAndThenReplays(t *testing.T) {
	for _, name := range []string{"a.md", "image.png"} {
		t.Run(name, func(t *testing.T) {
			s, native, _, root, _ := fixture(t)
			var bindings []Binding
			for _, content := range []string{"A", "B", "A", "A", "B", "B"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				r, err := s.Source.Read(t.Context(), "source", name)
				if err != nil {
					t.Fatal(err)
				}
				b, err := s.Export(t.Context(), "c", r.File.ID, r.Base.ID)
				if err != nil {
					t.Fatal(err)
				}
				bindings = append(bindings, b)
				if string(native.docs[b.Path+"@"+b.NativeRevision].Content) != content {
					t.Fatal("stale native bytes")
				}
			}
			if bindings[0].NativeRevision == bindings[2].NativeRevision || bindings[1].NativeRevision == bindings[4].NativeRevision || bindings[2] != bindings[3] || bindings[4] != bindings[5] {
				t.Fatalf("reversion/replay identities: %+v", bindings)
			}
			for i, want := range []uint64{1, 2, 3, 3, 4, 4} {
				if bindings[i].SourceSequence != want {
					t.Fatalf("source ordering at %d: %d, want %d", i, bindings[i].SourceSequence, want)
				}
			}
		})
	}
}
func TestExportLegacySourceRefreshesOnce(t *testing.T) {
	s, _, _, _, b := fixture(t)
	b.SourceSequence = 0
	err := s.Store.WithReplica(t.Context(), s.Replica, s.Epoch, func(r Records) error {
		k := mapKey(Intent{Workspace: b.Workspace, Collection: b.Collection, Generation: b.Generation, FileID: b.FileID})
		m, err := load[FileMapping](t.Context(), r, "file", k)
		if err != nil {
			return err
		}
		m.Latest = &b
		return r.Put(t.Context(), "file", k, m)
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Source.Read(t.Context(), "source", "a.md")
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.Export(t.Context(), "c", r.File.ID, r.Base.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.SourceSequence != 1 || next.ID == b.ID || next.NativeRevision == b.NativeRevision || next.SHA256 != b.SHA256 {
		t.Fatalf("legacy refresh: %+v", next)
	}
	again, err := s.Export(t.Context(), "c", r.File.ID, r.Base.ID)
	if err != nil || again != next {
		t.Fatalf("refresh replay: %+v %v", again, err)
	}
}
func step(t *testing.T, s *Service) {
	t.Helper()
	if _, e := s.Step(context.Background(), 128); e != nil {
		t.Fatal(e)
	}
}
func TestJoinOutOfOrderRestartAndExactStaleBase(t *testing.T) {
	s, n, source, root, b := fixture(t)
	i, p := operation(b, "edit", "C", "2-offline")
	n.content(b.Path, "2-offline", b.NativeRevision, "C")
	n.add(p)
	step(t, s)
	if len(source.ops) != 0 {
		t.Fatal("publication alone mutated source")
	}
	// An independent source writer advanced to B. An incoming edit must still use A.
	if e := os.WriteFile(filepath.Join(root, "a.md"), []byte("B"), 0600); e != nil {
		t.Fatal(e)
	}
	n.add(i)
	step(t, s)
	if len(n.acks) != 1 || n.acks[0].Status != "conflict" {
		t.Fatalf("acks: %+v", n.acks)
	}
	bytes, _ := os.ReadFile(filepath.Join(root, "a.md"))
	if string(bytes) != "B" {
		t.Fatal("stale source overwritten")
	}
	for _, op := range source.ops {
		if string(op.Base.Content) != "A" || string(op.Request.Content) != "C" {
			t.Fatal("base refreshed or bytes lost")
		}
	}
	restarted := *s
	step(t, &restarted)
	if len(source.ops) != 1 || len(n.acks) != 1 {
		t.Fatal("replay duplicated operation/ack")
	}
}
func TestAppliedChainAckRetryAndStockHold(t *testing.T) {
	s, n, source, root, b := fixture(t)
	first, p1 := operation(b, "first", "B", "2-first")
	next, p2 := operation(b, "next", "C", "3-next")
	next.Predecessor = first.ID
	raw, _ := json.Marshal(next)
	p2.IntentDigest = digest(raw)
	n.content(b.Path, "2-first", b.NativeRevision, "B")
	n.content(b.Path, "3-next", "2-first", "C")
	n.add(next)
	n.add(p2)
	step(t, s)
	if len(source.ops) != 0 {
		t.Fatal("successor passed missing predecessor")
	}
	n.add(first)
	n.add(p1)
	n.ackFailure = true
	if _, e := s.Step(context.Background(), 128); e == nil {
		t.Fatal("expected publication failure")
	}
	n.ackFailure = false
	restart := *s
	step(t, &restart)
	step(t, &restart)
	bytes, _ := os.ReadFile(filepath.Join(root, "a.md"))
	if string(bytes) != "C" || len(source.ops) != 2 {
		t.Fatalf("chain failed: %s / %d", bytes, len(source.ops))
	}
	if len(n.acks) != 2 || n.acks[1].Binding.SourceBase == b.SourceBase {
		t.Fatal("missing exact result binding")
	}
	n.records = append(n.records, Evidence{ID: "Notes/stock.md", Path: "Notes/stock.md", Revision: "1-stock", Kind: "file"})
	step(t, &restart)
	if len(source.ops) != 2 {
		t.Fatal("stock write admitted")
	}
}

func TestReverseOrderedChainPublishesBindingsWithoutExportScan(t *testing.T) {
	s, n, source, root, b := fixture(t)
	first, p1 := operation(b, "z_parent", "B", "2-first")
	next, p2 := operation(b, "a_child", "C", "3-next")
	next.Predecessor = first.ID
	raw, _ := json.Marshal(next)
	p2.IntentDigest = digest(raw)
	n.content(b.Path, "2-first", b.NativeRevision, "B")
	n.content(b.Path, "3-next", "2-first", "C")
	n.add(next)
	n.add(p2)
	n.add(first)
	n.add(p1)
	before := len(n.bindings)
	step(t, s)
	content, _ := os.ReadFile(filepath.Join(root, "a.md"))
	if string(content) != "C" || len(source.ops) != 2 || len(n.acks) != 2 || len(n.bindings)-before != 2 {
		t.Fatalf("chain required another tick/export: %q ops=%d acks=%d bindings=%d", content, len(source.ops), len(n.acks), len(n.bindings)-before)
	}
	p, err := s.Step(t.Context(), 128)
	if err != nil || p.Processed != 0 || p.Imported != 0 {
		t.Fatalf("completed history revisited: %+v %v", p, err)
	}
}

func TestReplicatedHistoryRequiresRetainedPayloadAndExactParent(t *testing.T) {
	for _, scenario := range []string{"valid", "missing", "wrong_hash", "wrong_parent", "collision", "stale_source"} {
		t.Run(scenario, func(t *testing.T) {
			s, n, source, root, b := fixture(t)
			i, p := operation(b, "offline", "B", "2-offline")
			n.docs[b.Path+"@2-offline"] = Evidence{Path: b.Path, Revision: "2-offline", Kind: "file", Status: "history", AncestryAvailable: true, AncestryCompleteToRoot: true, Ancestors: []string{b.NativeRevision}}
			path := Prefix + "payload/" + i.ID + ".md"
			evidence := Evidence{Path: path, Status: "available", Content: []byte("B"), SHA256: digest([]byte("B"))[7:]}
			switch scenario {
			case "missing":
				evidence = Evidence{}
			case "wrong_hash":
				evidence.Content = []byte("C")
			case "collision":
				evidence = Evidence{Status: "held", Reason: "payload_collision"}
			case "wrong_parent":
				x := n.docs[b.Path+"@2-offline"]
				x.Ancestors = []string{"1-other"}
				n.docs[b.Path+"@2-offline"] = x
			case "stale_source":
				if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("C"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			n.docs[path+"@1-control"] = evidence
			n.add(i)
			n.add(p)
			step(t, s)
			body, _ := os.ReadFile(filepath.Join(root, "a.md"))
			switch scenario {
			case "valid":
				if string(body) != "B" || len(source.ops) != 1 || len(n.acks) != 1 || n.acks[0].Status != "applied" {
					t.Fatalf("not applied: %s / %+v", body, n.acks)
				}
			case "stale_source":
				if string(body) != "C" || len(n.acks) != 1 || n.acks[0].Status != "conflict" {
					t.Fatal("stale source was not preserved")
				}
			default:
				if string(body) != "A" || len(source.ops) != 0 {
					t.Fatal("invalid historical evidence mutated source")
				}
			}
		})
	}
}
func TestRejectForgedBindingWrongParentAndReservedSource(t *testing.T) {
	for _, kind := range []string{"binding", "parent", "digest"} {
		t.Run(kind, func(t *testing.T) {
			s, n, source, _, b := fixture(t)
			i, p := operation(b, "bad", "C", "2-bad")
			parent := b.NativeRevision
			if kind == "binding" {
				i.Base.SourceBase = "invented"
				raw, _ := json.Marshal(i)
				p.IntentDigest = digest(raw)
			}
			if kind == "parent" {
				parent = "1-other"
			}
			if kind == "digest" {
				p.IntentDigest = digest([]byte("other"))
			}
			n.content(b.Path, "2-bad", parent, "C")
			n.add(i)
			n.add(p)
			step(t, s)
			if len(source.ops) != 0 {
				t.Fatal("invalid evidence admitted")
			}
		})
	}
	if IsSourcePath(Prefix + "intent/x.md") {
		t.Fatal("reserved controls admitted as notes")
	}
}
func TestExplicitCreateAndEpochFence(t *testing.T) {
	s, n, source, root, b := fixture(t)
	i, p := operation(b, "create", "new", "1-new")
	i.Operation = "create"
	i.Base = nil
	i.FileID = "new_client_file"
	i.Path = "Notes/new.md"
	raw, _ := json.Marshal(i)
	p.IntentDigest = digest(raw)
	p.Revisions[0].Path = i.Path
	n.content(i.Path, "1-new", "", "new")
	e := n.docs[i.Path+"@1-new"]
	e.Ancestors = nil
	n.docs[i.Path+"@1-new"] = e
	n.add(i)
	n.add(p)
	step(t, s)
	bytes, _ := os.ReadFile(filepath.Join(root, "new.md"))
	if string(bytes) != "new" || len(source.ops) != 1 {
		t.Fatalf("create failed: %s, %+v", bytes, n.acks)
	}
	n.epoch = "replaced"
	s.Epoch = "replaced"
	if _, e := s.Step(context.Background(), 128); !errors.Is(e, ErrHeld) {
		t.Fatal("epoch reused")
	}
}
func TestStrictWireRejectsDuplicatesAndUnknownFields(t *testing.T) {
	_, _, _, _, b := fixture(t)
	i, _ := operation(b, "id", "B", "2-b")
	raw, _ := json.Marshal(i)
	bad := append([]byte(`{"version":2,`), raw[1:]...)
	if _, e := parseIntent(bad); e == nil {
		t.Fatal("duplicate key accepted")
	}
	bad = append([]byte(`{"unknown":true,`), raw[1:]...)
	if _, e := parseIntent(bad); e == nil {
		t.Fatal("unknown field accepted")
	}
}

func pathOperation(n *fakeNative, b Binding, kind, text string) (Intent, Publication) {
	i, p := operation(b, "pathop", text, "1-target")
	i.Operation = kind
	n.docs[b.Path+"@2-delete"] = Evidence{ID: b.Path, Path: b.Path, Revision: "2-delete", Kind: "file", Status: "held", LogicalDeleted: true, AncestryAvailable: true, AncestryCompleteToRoot: true, Ancestors: []string{b.NativeRevision}}
	if kind == "rename" {
		i.Target = "Notes/renamed.md"
		n.content(i.Target, "1-target", "", text)
		target := n.docs[i.Target+"@1-target"]
		target.Ancestors = nil
		n.docs[i.Target+"@1-target"] = target
		p.Revisions = []Revision{{Path: i.Target, Revision: "1-target", Role: "content"}, {Path: i.Path, Revision: "2-delete", Role: "deletion"}}
	} else {
		i.Length = 0
		i.SHA256 = digest(nil)
		p.Revisions = []Revision{{Path: i.Path, Revision: "2-delete", Role: "deletion"}}
	}
	raw, _ := json.Marshal(i)
	p.IntentDigest = digest(raw)
	return i, p
}

func TestCanonicalPathOperationsAndReceiptReplay(t *testing.T) {
	for _, scenario := range []struct {
		name, kind, text string
		uncertain        bool
	}{
		{"rename", "rename", "A", false},
		{"rename_changed_content", "rename", "B", false},
		{"delete", "delete", "", false},
		{"uncertain_rename", "rename", "B", true},
		{"uncertain_delete", "delete", "", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s, n, source, root, b := fixture(t)
			ctx := context.Background()
			stored, e := load[BindingRecord](ctx, s.Store.(*memoryRecords), "binding", b.ID)
			if e != nil {
				t.Fatal(e)
			}
			i, p := pathOperation(n, b, scenario.kind, scenario.text)
			n.add(i)
			n.add(p)
			if scenario.uncertain {
				source.afterCommitPath = func() error { return notesworkspace.ErrPathCommitUncertain }
				step(t, s)
				if len(n.acks) != 0 || len(source.ops) != 1 {
					t.Fatal("uncertain commit acknowledged or changed ID")
				}
				source.afterCommitPath = nil
			}
			n.ackFailure = true
			if _, e = s.Step(ctx, 128); e == nil {
				t.Fatal("expected ack delivery failure")
			}
			n.ackFailure = false
			restart := *s
			step(t, &restart)
			step(t, &restart)
			expectedOps := 1
			if scenario.kind == "rename" && scenario.text != "A" {
				expectedOps = 2
			}
			if len(source.ops) != expectedOps || len(n.acks) != 1 || n.acks[0].Status != "applied" {
				t.Fatalf("receipt replay: ops=%+v ack=%+v", source.ops, n.acks)
			}
			file := source.files[stored.SourceFileID]
			if file.ID != stored.SourceFileID || file.PathVersion != 1 || file.Deleted != (scenario.kind == "delete") {
				t.Fatalf("wrong path identity: %+v", file)
			}
			if _, e := os.Stat(filepath.Join(root, "a.md")); !os.IsNotExist(e) {
				t.Fatalf("old path remains: %v", e)
			}
			joined, e := load[Join](ctx, s.Store.(*memoryRecords), "operation", i.ID)
			if e != nil || joined.ResultFile != file {
				t.Fatalf("wrong result file: %+v / %v", joined, e)
			}
			result := source.bases[joined.ResultBaseID]
			if result.FileID != file.ID || result.PathVersion != 1 || result.Exists != (scenario.kind == "rename") {
				t.Fatalf("wrong exact result: %+v", result)
			}
			if scenario.kind == "rename" {
				bytes, e := os.ReadFile(filepath.Join(root, "renamed.md"))
				if e != nil || string(bytes) != scenario.text || file.RelativePath != "renamed.md" {
					t.Fatalf("rename bytes: %q / %v / %+v", bytes, e, file)
				}
				if n.acks[0].Binding == nil || n.acks[0].Binding.Path != i.Target || n.acks[0].Binding.SHA256 != digest(bytes) {
					t.Fatal("missing renamed binding")
				}
				for _, op := range source.ops {
					if op.Request.Kind == notesworkspace.KindEdit && (op.Base.PathVersion != 1 || string(op.Base.Content) != "A") {
						t.Fatal("rename content edit refreshed its base")
					}
				}
			} else if n.acks[0].Binding != nil {
				t.Fatal("deleted identity received writable binding")
			}
			before := n.counter
			if _, e = s.Export(ctx, "c", stored.SourceFileID, stored.SourceBaseID); !errors.Is(e, ErrHeld) || n.counter != before {
				t.Fatalf("historical path base exported: %v", e)
			}
			// An independent old-path intent retains its stale base; it cannot follow the identity.
			stale, pub := operation(b, "stale", "C", "2-stale")
			n.content(b.Path, "2-stale", b.NativeRevision, "C")
			n.add(stale)
			n.add(pub)
			step(t, s)
			if len(n.acks) != 2 || n.acks[1].Status != "conflict" {
				t.Fatalf("stale path not conflicted: %+v", n.acks)
			}
		})
	}
}

func TestRenamePredecessorUsesExactNewPathBase(t *testing.T) {
	s, n, source, root, b := fixture(t)
	i, p := pathOperation(n, b, "rename", "A")
	next, pub := operation(b, "successor", "B", "2-next")
	next.Path, next.Predecessor = i.Target, i.ID
	pub.Revisions[0].Path = next.Path
	raw, _ := json.Marshal(next)
	pub.IntentDigest = digest(raw)
	n.content(next.Path, "2-next", "1-target", "B")
	n.add(next)
	n.add(pub)
	step(t, s)
	if len(source.ops) != 0 {
		t.Fatal("successor bypassed rename")
	}
	n.add(i)
	n.add(p)
	step(t, s)
	step(t, s)
	bytes, e := os.ReadFile(filepath.Join(root, "renamed.md"))
	if e != nil || string(bytes) != "B" || len(source.ops) != 2 || len(n.acks) != 2 {
		t.Fatalf("rename chain: %q / %v / %+v", bytes, e, n.acks)
	}
}

func TestMalformedPathEvidenceNeverReachesSource(t *testing.T) {
	for _, fault := range []string{"missing_pair", "missing_deletion", "branch_delete", "wrong_parent", "target_not_root"} {
		t.Run(fault, func(t *testing.T) {
			s, n, source, root, b := fixture(t)
			i, p := pathOperation(n, b, "rename", "A")
			deletion := n.docs[b.Path+"@2-delete"]
			switch fault {
			case "missing_pair":
				p.Revisions = p.Revisions[:1]
			case "missing_deletion":
				deletion.LogicalDeleted = false
			case "branch_delete":
				deletion.BranchDeleted = true
			case "wrong_parent":
				deletion.Ancestors = []string{"1-other"}
			case "target_not_root":
				target := n.docs[i.Target+"@1-target"]
				target.Ancestors = []string{"1-other"}
				n.docs[i.Target+"@1-target"] = target
			}
			n.docs[b.Path+"@2-delete"] = deletion
			n.add(i)
			n.add(p)
			step(t, s)
			bytes, _ := os.ReadFile(filepath.Join(root, "a.md"))
			if len(source.ops) != 0 || len(n.acks) != 0 || string(bytes) != "A" {
				t.Fatal("malformed path evidence mutated source")
			}
		})
	}
}

func TestExportRejectsReservedPath(t *testing.T) {
	s, n, source, _, b := fixture(t)
	ctx := context.Background()
	stored, e := load[BindingRecord](ctx, s.Store.(*memoryRecords), "binding", b.ID)
	if e != nil {
		t.Fatal(e)
	}
	file := source.files[stored.SourceFileID]
	source.ops["pending"] = notesworkspace.Operation{Request: notesworkspace.Request{OperationID: "pending"}, File: file, State: notesworkspace.Held, PathStage: "prepared", DestinationKey: "renamed.md"}
	before := n.counter
	if _, e := s.Export(ctx, "c", file.ID, stored.SourceBaseID); !errors.Is(e, notesworkspace.ErrPathBusy) || n.counter != before {
		t.Fatalf("reserved path exported: %v", e)
	}
}

type withdrawnResolver struct{}

func (withdrawnResolver) WithSource(context.Context, string, string, func(notesworkspace.Source) error) error {
	return notesworkspace.ErrMembership
}
func TestExportRechecksMembershipUnderSourceFence(t *testing.T) {
	s, n, _, _, b := fixture(t)
	stored, e := load[BindingRecord](context.Background(), s.Store.(*memoryRecords), "binding", b.ID)
	if e != nil {
		t.Fatal(e)
	}
	adapter := s.Source.(NotesSource)
	adapter.Service.Sources = withdrawnResolver{}
	s.Source = adapter
	before := n.counter
	if _, e = s.Export(context.Background(), "c", stored.SourceFileID, stored.SourceBaseID); !errors.Is(e, notesworkspace.ErrMembership) {
		t.Fatalf("withdrawn export: %v", e)
	}
	if n.counter != before {
		t.Fatal("exported across withdrawal")
	}
}
