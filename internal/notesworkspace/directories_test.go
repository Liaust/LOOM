package notesworkspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The fixture uses the same admission callback for files and directories, but
// production must implement the separate directory admission interface.
func (r *testResolver) WithDirectoryPaths(ctx context.Context, id string, paths []string, fn func(Source) error) error {
	return r.WithSource(ctx, id, "", fn)
}

type readOnlyDirectoryResolver struct{ *testResolver }

func (r readOnlyDirectoryResolver) WithDirectoryPaths(context.Context, string, []string, func(Source) error) error {
	return ErrReferenceOnly
}

func (r readOnlyDirectoryResolver) WithReadDirectoryPaths(ctx context.Context, id string, paths []string, fn func(Source) error) error {
	return r.WithSource(ctx, id, "", fn)
}

func TestReferenceDirectoryCanBeReadWithoutWriteAdmission(t *testing.T) {
	s, _, r := fixture(t)
	mkdir(t, r.root, "notebooks")
	s.Sources = readOnlyDirectoryResolver{r}
	b := readDirectory(t, s, "notebooks")
	if !b.Base.Exists {
		t.Fatal("reference directory was not observed")
	}
	if _, err := s.ReceiveDirectory(t.Context(), Request{OperationID: "ref-delete", Kind: KindDelete, FileID: b.File.ID, BaseID: b.Base.ID, Generation: b.Base.Generation}); err != nil {
		t.Fatal(err)
	}
	op, err := s.ApplyDirectory(t.Context(), "ref-delete")
	if err != nil || op.State != Held || op.Reason != ErrReferenceOnly.Error() {
		t.Fatalf("read-only directory gained mutation permission: %+v %v", op, err)
	}
	if _, err := os.Stat(filepath.Join(r.root, "notebooks")); err != nil {
		t.Fatalf("read-only directory was modified: %v", err)
	}
}

func readDirectory(t *testing.T, s Service, relative string) ReadResult {
	t.Helper()
	b, err := s.ReadDirectory(t.Context(), "collection", relative)
	if err != nil {
		t.Fatal(err)
	}
	if b.File.EntryKind != directoryEntry || b.Base.Content != nil || b.Base.Hash != digest(nil) || b.Base.Exists != (b.Base.DirectoryIdentity != nil) {
		t.Fatalf("not an exact directory base: %+v", b)
	}
	return b
}

func receiveDirectory(t *testing.T, s Service, id, kind, destination string, base ReadResult, targets ...string) Operation {
	t.Helper()
	op, err := s.ReceiveDirectory(t.Context(), Request{OperationID: id, Kind: kind, DestinationPath: destination, FileID: base.File.ID, BaseID: base.Base.ID, Generation: base.Base.Generation, DirectoryTargets: targets})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func mkdir(t *testing.T, root, relative string) string {
	t.Helper()
	full := filepath.Join(root, relative)
	if err := os.MkdirAll(full, 0775); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestDirectoryCreateBeforeAnyChildAndReceiptReplay(t *testing.T) {
	s, m, r := fixture(t)
	b := readDirectory(t, s, "new/nested/empty")
	if b.Base.Exists {
		t.Fatal("absent directory exists")
	}
	if _, err := os.Stat(filepath.Join(r.root, "new")); !os.IsNotExist(err) {
		t.Fatal("read created parents", err)
	}
	receiveDirectory(t, s, "create", KindEdit, "", b)
	op := apply(t, s, "create", Accepted) // generic recovery dispatches directories
	current := readDirectory(t, s, "new/nested/empty")
	if !current.Base.Exists || current.File.ID != b.File.ID || current.File.PathVersion != 1 || current.Base.ID != op.ResultBaseID || len(m.history) != 1 {
		t.Fatalf("create not atomically committed: %+v %+v", op, current)
	}
	entries, err := os.ReadDir(filepath.Join(r.root, "new/nested/empty"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("directory has a synthetic file: %v %v", entries, err)
	}
	r.active = false
	if got := apply(t, s, "create", Accepted); !reflect.DeepEqual(got, op) {
		t.Fatal("receipt changed on apply replay")
	}
	if got, err := s.ReceiveDirectory(t.Context(), op.Request); err != nil || !reflect.DeepEqual(got, op) {
		t.Fatal("receipt changed on receive replay", err)
	}
	if len(m.history) != 1 {
		t.Fatal("duplicate create history")
	}
}

func TestDirectoryEnsureAdoptsParentCreatedByChild(t *testing.T) {
	s, _, r := fixture(t)
	b := readDirectory(t, s, "folder")
	receiveDirectory(t, s, "ensure", KindEdit, "", b)
	file := readBase(t, s, "folder/child.canvas")
	receive(t, s, "child", file, "{\n  \"nodes\": []\n}\n")
	apply(t, s, "child", Accepted)
	before, err := os.Stat(filepath.Join(r.root, "folder"))
	if err != nil {
		t.Fatal(err)
	}
	apply(t, s, "ensure", Accepted)
	after, err := os.Stat(filepath.Join(r.root, "folder"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("existing directory was replaced", err)
	}
	contents(t, filepath.Join(r.root, "folder/child.canvas"), "{\n  \"nodes\": []\n}\n")
}

func TestDirectoryEmptyRenameDeleteAndRetainedJournals(t *testing.T) {
	s, m, r := fixture(t)
	mkdir(t, r.root, "old")
	journalName := ".loom-notes-" + strings.Repeat("a", 64)
	mkdir(t, r.root, "old/"+journalName)
	write(t, filepath.Join(r.root, "old", journalName, "original"), "retained child revision")
	b := readDirectory(t, s, "old")
	receiveDirectory(t, s, "rename", KindRename, "nested/new", b)
	renamed := apply(t, s, "rename", Accepted)
	current := readDirectory(t, s, "nested/new")
	if current.File.ID != b.File.ID || !sameDirectoryIdentity(current.Base.DirectoryIdentity, b.Base.DirectoryIdentity) || current.File.PathVersion != 1 {
		t.Fatal("rename lost exact directory identity")
	}
	if _, err := os.Stat(filepath.Join(r.root, "old")); !os.IsNotExist(err) {
		t.Fatal("old directory remains", err)
	}
	receiveDirectory(t, s, "delete", KindDelete, "", current)
	deleted := apply(t, s, "delete", Accepted)
	result, err := m.Base(t.Context(), deleted.ResultBaseID)
	if err != nil || result.Content != nil || result.Exists || result.DirectoryIdentity != nil || result.PathVersion != 2 || !deleted.ResultFile.Deleted {
		t.Fatalf("delete base: %+v %v", result, err)
	}
	contents(t, filepath.Join(r.root, "nested", deleted.Journal, "displaced", journalName, "original"), "retained child revision")
	if _, err := os.Stat(filepath.Join(r.root, "nested/new")); !os.IsNotExist(err) {
		t.Fatal("deleted canonical directory still exists", err)
	}
	if len(m.history) != 2 || renamed.ResultFile.PathVersion != 1 {
		t.Fatal("path history lost")
	}
	recovery, err := s.Reconcile(t.Context(), "delete")
	if err != nil || recovery.State != Held || !strings.Contains(recovery.Reason, "file-byte reconciliation is not applicable") {
		t.Fatalf("directory read as file: %+v %v", recovery, err)
	}
}

func TestDirectoryCreateAndMoveReplayAfterCommitFailure(t *testing.T) {
	for _, kind := range []string{KindEdit, KindRename, KindDelete, "merge"} {
		t.Run(kind, func(t *testing.T) {
			s, m, r := fixture(t)
			if kind != KindEdit {
				mkdir(t, r.root, "old")
			}
			b := readDirectory(t, s, "old")
			destination, requestKind := "", kind
			var targets []string
			if kind == KindRename || kind == "merge" {
				destination, requestKind = "new", KindRename
			}
			if kind == "merge" {
				mkdir(t, r.root, "new")
				write(t, filepath.Join(r.root, "new/child.md"), "acknowledged child")
				targets = []string{"new/child.md"}
			}
			receiveDirectory(t, s, "operation", requestKind, destination, b, targets...)
			m.beforeCommitPath = func(Operation) error { return errors.New("commit unavailable") }
			held := apply(t, s, "operation", Held)
			if held.MoveIdentity == nil || held.PathStage == "" {
				t.Fatalf("missing durable inode: %+v", held)
			}
			if _, err := s.ReadDirectory(t.Context(), "collection", "old"); !errors.Is(err, ErrPathBusy) {
				t.Fatal("pending path could be rebound", err)
			}
			m.beforeCommitPath = nil
			op := apply(t, Service{Store: m, Sources: r}, "operation", Accepted)
			if len(m.history) != 1 || op.ResultFile.PathVersion != 1 {
				t.Fatal("replay did not commit exactly once")
			}
			if kind == "merge" {
				contents(t, filepath.Join(r.root, "new/child.md"), "acknowledged child")
			}
		})
	}
}

func TestDirectoryRefusesUnknownChildrenAndChangedInodes(t *testing.T) {
	for _, scenario := range []string{"existing-child", "prepared-child", "detached-child", "journal-lookalike", "replaced-inode"} {
		for _, kind := range []string{KindRename, KindDelete} {
			t.Run(scenario+"/"+kind, func(t *testing.T) {
				s, m, r := fixture(t)
				mkdir(t, r.root, "old")
				b := readDirectory(t, s, "old")
				destination := ""
				if kind == KindRename {
					destination = "new"
				}
				receiveDirectory(t, s, "op", kind, destination, b)
				if scenario == "existing-child" {
					write(t, filepath.Join(r.root, "old/unknown.md"), "preserve")
				}
				if scenario == "journal-lookalike" {
					mkdir(t, r.root, "old/.loom-notes-not-a-real-journal")
				}
				if scenario == "replaced-inode" {
					if err := os.Rename(filepath.Join(r.root, "old"), filepath.Join(r.root, "original")); err != nil {
						t.Fatal(err)
					}
					mkdir(t, r.root, "old")
				}
				added := false
				m.beforeSave = func(op Operation) error {
					if !added && (scenario == "prepared-child" && op.PathStage == "prepared" || scenario == "detached-child" && op.PathStage == "detached") {
						added = true
						parent := filepath.Join(r.root, "old")
						if scenario == "detached-child" {
							parent = filepath.Join(r.root, op.Journal, "displaced")
							if op.MoveIdentity == nil {
								t.Fatal("moved inode not durable")
							}
						}
						write(t, filepath.Join(parent, "unknown.md"), "preserve")
					}
					return nil
				}
				apply(t, s, "op", Conflict)
				if scenario == "existing-child" || added {
					contents(t, filepath.Join(r.root, "old/unknown.md"), "preserve")
				}
				if _, err := os.Stat(filepath.Join(r.root, "old")); err != nil {
					t.Fatal("source not preserved", err)
				}
			})
		}
	}
}

func TestDirectoryTargetMergeRequiresExactListedDescendants(t *testing.T) {
	for _, scenario := range []string{"allowed", "no-list", "missing-parent", "unknown-child", "late-child", "symlink", "empty-target"} {
		t.Run(scenario, func(t *testing.T) {
			s, m, r := fixture(t)
			mkdir(t, r.root, "old")
			journal := ".loom-notes-" + strings.Repeat("b", 64)
			mkdir(t, r.root, "old/"+journal)
			write(t, filepath.Join(r.root, "old", journal, "original"), "retained")
			b := readDirectory(t, s, "old")
			mkdir(t, r.root, "new/nested")
			write(t, filepath.Join(r.root, "new/nested/child.canvas"), "{\"nodes\":[]}")
			targets := []string{"new/nested", "new/nested/child.canvas"}
			switch scenario {
			case "no-list":
				targets = nil
			case "missing-parent":
				targets = targets[1:]
			case "unknown-child":
				write(t, filepath.Join(r.root, "new/unknown.md"), "preserve")
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(r.root, "new/link")); err != nil {
					t.Fatal(err)
				}
				targets = append(targets, "new/link")
			case "empty-target":
				mkdir(t, r.root, "empty")
			}
			destination := "new"
			if scenario == "empty-target" {
				destination, targets = "empty", nil
			}
			receiveDirectory(t, s, "rename", KindRename, destination, b, targets...)
			added := false
			m.beforeSave = func(op Operation) error {
				if scenario == "late-child" && !added && op.PathStage == "detached" {
					added = true
					write(t, filepath.Join(r.root, "new/unknown.md"), "preserve")
				}
				return nil
			}
			state := Conflict
			if scenario == "allowed" {
				state = Accepted
			}
			op := apply(t, s, "rename", state)
			contents(t, filepath.Join(r.root, "new/nested/child.canvas"), "{\"nodes\":[]}")
			if scenario == "allowed" {
				contents(t, filepath.Join(r.root, op.Journal, "displaced", journal, "original"), "retained")
				if sameDirectoryIdentity(readDirectory(t, s, "new").Base.DirectoryIdentity, b.Base.DirectoryIdentity) {
					t.Fatal("merged destination did not retain its own inode")
				}
			} else {
				contents(t, filepath.Join(r.root, "old", journal, "original"), "retained")
			}
			if scenario == "unknown-child" || scenario == "late-child" {
				contents(t, filepath.Join(r.root, "new/unknown.md"), "preserve")
			}
		})
	}
}

func TestDirectoryPathAndSymlinkGuards(t *testing.T) {
	s, _, r := fixture(t)
	for _, relative := range []string{"", ".", "../escape", "/absolute", ".hidden", "credentials", "private_no_index", "a/../b", "a\\b"} {
		if _, err := s.ReadDirectory(t.Context(), "collection", relative); err == nil {
			t.Fatalf("unsafe directory path admitted: %q", relative)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(r.root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadDirectory(t.Context(), "collection", "link"); err == nil {
		t.Fatal("read followed symlink")
	}
	if _, err := s.ReadDirectory(t.Context(), "collection", "link/child"); err == nil {
		t.Fatal("read followed parent symlink")
	}
	b := readDirectory(t, s, "collision")
	receiveDirectory(t, s, "create", KindEdit, "", b)
	if err := os.Symlink(outside, filepath.Join(r.root, "collision")); err != nil {
		t.Fatal(err)
	}
	apply(t, s, "create", Conflict)
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("symlink target changed", err)
	}
	readBase(t, s, "identity.md")
	if _, err := s.ReadDirectory(t.Context(), "collection", "identity.md"); err == nil {
		t.Fatal("file binding adopted as directory")
	}
}

func TestDirectoryInterruptedPreparationAndAmbiguousCommit(t *testing.T) {
	for _, scenario := range []string{"prepared", "created", "detached", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			s, m, r := fixture(t)
			kind := KindEdit
			if scenario == "detached" {
				kind = KindDelete
				mkdir(t, r.root, "old")
			}
			b := readDirectory(t, s, "old")
			receiveDirectory(t, s, "op", kind, "", b)
			failed := false
			if scenario == "ambiguous" {
				m.afterCommitPath = func(Operation) error { return ErrPathCommitUncertain }
				op, err := s.Apply(t.Context(), "op")
				if !errors.Is(err, ErrPathCommitUncertain) || op.State != Accepted || m.ops["op"].State != Accepted {
					t.Fatalf("committed receipt demoted: %+v %v", op, err)
				}
			} else {
				m.beforeSave = func(op Operation) error {
					if !failed && op.PathStage == scenario {
						failed = true
						return errors.New("interrupted durable save")
					}
					return nil
				}
				apply(t, s, "op", Held)
				if !failed {
					t.Fatal("interruption not exercised")
				}
			}
			m.afterCommitPath, m.beforeSave = nil, nil
			apply(t, s, "op", Accepted)
			if len(m.history) != 1 {
				t.Fatal("replay duplicated/lost path history")
			}
		})
	}
}

func TestDirectoryUncommittedUncertainMoveRefusesLateChildren(t *testing.T) {
	for _, kind := range []string{KindRename, KindDelete, "merge"} {
		t.Run(kind, func(t *testing.T) {
			s, m, r := fixture(t)
			mkdir(t, r.root, "old")
			b := readDirectory(t, s, "old")
			requestKind, target := kind, ""
			var targets []string
			if kind == KindRename || kind == "merge" {
				requestKind, target = KindRename, "new"
			}
			if kind == "merge" {
				mkdir(t, r.root, "new")
				write(t, filepath.Join(r.root, "new/child.md"), "acknowledged")
				targets = []string{"new/child.md"}
			}
			receiveDirectory(t, s, "op", requestKind, target, b, targets...)
			// Model a transaction that did not commit, but whose failure cannot
			// establish that outcome until replay reads its durable receipt.
			m.beforeCommitPath = func(Operation) error { return ErrPathCommitUncertain }
			held := apply(t, s, "op", Held)
			lateParent := filepath.Join(r.root, held.Journal, "displaced")
			if kind == KindRename || kind == "merge" {
				lateParent = filepath.Join(r.root, "new")
			}
			write(t, filepath.Join(lateParent, "late.md"), "preserve late child")
			m.beforeCommitPath = nil
			conflict := apply(t, Service{Store: m, Sources: r}, "op", Conflict)
			if len(m.history) != 0 || conflict.ResultBaseID != "" {
				t.Fatal("unacknowledged children received a successful path receipt")
			}
			if kind == KindDelete {
				lateParent = filepath.Join(r.root, "old")
			}
			contents(t, filepath.Join(lateParent, "late.md"), "preserve late child")
		})
	}
}

func TestDirectoryRequestIntentAndStalePath(t *testing.T) {
	s, _, r := fixture(t)
	mkdir(t, r.root, "old")
	b := readDirectory(t, s, "old")
	received := receiveDirectory(t, s, "rename", KindRename, "new", b, "new/child.md")
	changed := received.Request
	changed.DirectoryTargets = []string{"new/other.md"}
	if _, err := s.ReceiveDirectory(t.Context(), changed); !errors.Is(err, ErrIntentMismatch) {
		t.Fatal("target allowlist not part of intent", err)
	}
	for _, target := range []string{"new/child", "old", "old/nested", ".", "../escape"} {
		req := received.Request
		req.OperationID, req.DestinationPath, req.DirectoryTargets = "invalid-"+target, target, nil
		if target == "new/child" {
			req.DirectoryTargets = []string{"unrelated/child"}
		}
		if _, err := s.ReceiveDirectory(t.Context(), req); err == nil {
			t.Fatalf("invalid directory rename accepted: %q", target)
		}
	}
	apply(t, s, "rename", Accepted)
	receiveDirectory(t, s, "stale", KindDelete, "", b)
	apply(t, s, "stale", Conflict)
}

func TestCanvasSourceBytesAndExactBaseConflict(t *testing.T) {
	s, _, r := fixture(t)
	if IsReferencePath("board.canvas") {
		t.Fatal("Canvas remains reference-only")
	}
	b := readBase(t, s, "board.canvas")
	content := "{\r\n  \"nodes\": [{\"id\":\"unchanged\", \"text\":\"A\"}],\r\n  \"edges\": []\r\n}\r\n"
	receive(t, s, "create", b, content)
	apply(t, s, "create", Accepted)
	contents(t, filepath.Join(r.root, "board.canvas"), content)
	b = readBase(t, s, "board.canvas")
	receive(t, s, "competing", b, "{\"nodes\":[],\"edges\":[]}")
	write(t, filepath.Join(r.root, "board.canvas"), "{\"nodes\":[{\"id\":\"other\"}]}")
	apply(t, s, "competing", Conflict)
	contents(t, filepath.Join(r.root, "board.canvas"), "{\"nodes\":[{\"id\":\"other\"}]}")
	b = readBase(t, s, "board.canvas")
	receivePath(t, s, "move-canvas", KindRename, "moved.canvas", b)
	apply(t, s, "move-canvas", Accepted)
	contents(t, filepath.Join(r.root, "moved.canvas"), "{\"nodes\":[{\"id\":\"other\"}]}")
}
