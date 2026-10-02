package notesworkspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func receivePath(t *testing.T, s Service, id, kind, destination string, base ReadResult) Operation {
	t.Helper()
	op, err := s.Receive(t.Context(), Request{OperationID: id, Kind: kind, DestinationPath: destination, FileID: base.File.ID, BaseID: base.Base.ID, Generation: base.Base.Generation})
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func TestRenameKeepsIdentityAndRejectsOldPathVersions(t *testing.T) {
	s, m, r := fixture(t)
	old := filepath.Join(r.root, "note.md")
	write(t, old, "A")
	if err := os.Mkdir(filepath.Join(r.root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setxattr(old, "user.notes_test", []byte("metadata"), 0); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(old)
	if err != nil {
		t.Fatal(err)
	}
	base := readBase(t, s, "note.md")
	receive(t, s, "stale-edit", base, "stale")
	receivePath(t, s, "rename", KindRename, "folder/new.md", base)
	renamed := apply(t, s, "rename", Accepted)
	if renamed.ResultFile == nil || renamed.ResultFile.ID != base.File.ID || renamed.ResultFile.RelativePath != "folder/new.md" || renamed.ResultFile.PathVersion != 1 {
		t.Fatalf("rename identity: %+v", renamed)
	}
	target := filepath.Join(r.root, "folder/new.md")
	after, err := os.Stat(target)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("original inode not moved: %v", err)
	}
	attr := make([]byte, 32)
	n, err := unix.Getxattr(target, "user.notes_test", attr)
	if err != nil || string(attr[:n]) != "metadata" {
		t.Fatalf("metadata lost: %v", err)
	}
	result, err := m.Base(t.Context(), renamed.ResultBaseID)
	if err != nil || result.PathVersion != 1 || !result.Exists || string(result.Content) != "A" {
		t.Fatalf("exact result base: %+v %v", result, err)
	}
	current := readBase(t, s, "folder/new.md")
	if current.File.ID != base.File.ID || current.Base.ID != result.ID {
		t.Fatal("read did not retain renamed identity/base")
	}
	oldBinding := readBase(t, s, "note.md")
	if oldBinding.File.ID == base.File.ID || oldBinding.Base.Exists {
		t.Fatal("old path inherited moved identity")
	}
	write(t, old, "new occupant")
	apply(t, s, "stale-edit", Conflict)
	contents(t, old, "new occupant")
	contents(t, target, "A")
	receive(t, s, "edit-renamed", current, "C")
	apply(t, s, "edit-renamed", Accepted)
	contents(t, target, "C")
	if len(m.history) != 1 || m.history[0].FromPath != "note.md" || m.history[0].ToPath != "folder/new.md" {
		t.Fatalf("path history: %+v", m.history)
	}
	r.active = false
	if replay := apply(t, s, "rename", Accepted); !reflect.DeepEqual(replay, renamed) {
		t.Fatal("rename replay changed")
	}
	if replay, err := s.Receive(t.Context(), renamed.Request); err != nil || !reflect.DeepEqual(replay, renamed) {
		t.Fatalf("renamed receive replay: %+v %v", replay, err)
	}
}
func TestDeleteRetiresIdentityAndRetainsLateWriter(t *testing.T) {
	s, m, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	base := readBase(t, s, "note.md")
	writer, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	receivePath(t, s, "delete", KindDelete, "", base)
	deleted := apply(t, s, "delete", Accepted)
	if deleted.ResultFile == nil || !deleted.ResultFile.Deleted || deleted.ResultFile.ID != base.File.ID || deleted.ResultFile.PathVersion != 1 {
		t.Fatalf("deleted identity: %+v", deleted)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("canonical path still exists: %v", err)
	}
	result, err := m.Base(t.Context(), deleted.ResultBaseID)
	if err != nil || result.Exists || result.PathVersion != 1 {
		t.Fatalf("delete result base: %+v %v", result, err)
	}
	retained := filepath.Join(r.root, deleted.Journal, "displaced")
	contents(t, retained, "A")
	write(t, name, "new file")
	fresh := readBase(t, s, "note.md")
	if fresh.File.ID == base.File.ID {
		t.Fatal("recreated path inherited deleted identity")
	}
	if _, err := writer.WriteAt([]byte("B"), 0); err != nil {
		t.Fatal(err)
	}
	observation, err := s.Reconcile(t.Context(), "delete")
	if err != nil || observation.State != Conflict || string(observation.Retained) != "B" || string(observation.Current) != "new file" {
		t.Fatalf("deleted late write: %+v %v", observation, err)
	}
	replay := apply(t, s, "delete", Accepted)
	if !reflect.DeepEqual(replay, deleted) {
		t.Fatal("delete replay changed")
	}
	contents(t, name, "new file")
	contents(t, retained, "B")
	receive(t, s, "stale", base, "bad")
	apply(t, s, "stale", Conflict)
	contents(t, name, "new file")
	if len(m.history) != 1 || !m.history[0].Deleted {
		t.Fatalf("delete history: %+v", m.history)
	}
}
func TestPathOperationStaleBaseAndCollision(t *testing.T) {
	for _, kind := range []string{KindRename, KindDelete} {
		t.Run(kind, func(t *testing.T) {
			s, _, r := fixture(t)
			name := filepath.Join(r.root, "note.md")
			write(t, name, "A")
			base := readBase(t, s, "note.md")
			destination := ""
			if kind == KindRename {
				destination = "new.md"
			}
			receivePath(t, s, "op", kind, destination, base)
			write(t, name, "B")
			apply(t, s, "op", Conflict)
			contents(t, name, "B")
		})
	}
	for _, bindingOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "bytes", true: "reserved-identity"}[bindingOnly], func(t *testing.T) {
			s, _, r := fixture(t)
			write(t, filepath.Join(r.root, "note.md"), "A")
			if bindingOnly {
				readBase(t, s, "new.md")
			} else {
				write(t, filepath.Join(r.root, "new.md"), "occupant")
			}
			receivePath(t, s, "rename", KindRename, "new.md", readBase(t, s, "note.md"))
			apply(t, s, "rename", Conflict)
			contents(t, filepath.Join(r.root, "note.md"), "A")
			if !bindingOnly {
				contents(t, filepath.Join(r.root, "new.md"), "occupant")
			}
		})
	}
}
func TestPathFinalizationRecoveryAndReservations(t *testing.T) {
	for _, kind := range []string{KindRename, KindDelete} {
		t.Run(kind, func(t *testing.T) {
			s, m, r := fixture(t)
			old := filepath.Join(r.root, "note.md")
			write(t, old, "A")
			base := readBase(t, s, "note.md")
			destination := ""
			if kind == KindRename {
				destination = "new.md"
			}
			receivePath(t, s, "op", kind, destination, base)
			m.beforeCommitPath = func(Operation) error { return errors.New("commit unavailable") }
			held := apply(t, s, "op", Held)
			if held.PathStage != "detached" || held.MoveIdentity == nil {
				t.Fatalf("missing durable phase: %+v", held)
			}
			if kind == KindRename {
				contents(t, filepath.Join(r.root, "new.md"), "A")
			} else {
				contents(t, old, "A")
			}
			for _, path := range []string{"note.md", destination} {
				if path != "" {
					if _, err := s.Read(t.Context(), "collection", path); !errors.Is(err, ErrPathBusy) {
						t.Fatalf("unfinalized path allowed binding: %s %v", path, err)
					}
				}
			}
			receive(t, s, "other", base, "B")
			if _, err := s.Apply(t.Context(), "other"); !errors.Is(err, ErrPathBusy) {
				t.Fatalf("other operation passed reservation: %v", err)
			}
			m.beforeCommitPath = nil
			accepted := apply(t, Service{Store: m, Sources: r}, "op", Accepted)
			if accepted.ResultFile == nil || len(m.history) != 1 {
				t.Fatal("recovery did not commit identity/history")
			}
			apply(t, s, "op", Accepted)
			if len(m.history) != 1 {
				t.Fatal("duplicate path history")
			}
			apply(t, s, "other", Conflict)
		})
	}
}
func TestPathDisplacementFailureAndRacingDestination(t *testing.T) {
	for _, scenario := range []string{"store-error", "cancellation", "destination-race", "source-race"} {
		t.Run(scenario, func(t *testing.T) {
			s, m, r := fixture(t)
			old := filepath.Join(r.root, "note.md")
			newPath := filepath.Join(r.root, "new.md")
			write(t, old, "A")
			receivePath(t, s, "rename", KindRename, "new.md", readBase(t, s, "note.md"))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			acted := false
			m.beforeSave = func(op Operation) error {
				if acted {
					return nil
				}
				if scenario == "source-race" && op.PathStage == "prepared" {
					acted = true
					write(t, old, "B")
					return nil
				}
				if op.PathStage == "detached" {
					acted = true
					switch scenario {
					case "store-error":
						return errors.New("save failed")
					case "cancellation":
						cancel()
						return ctx.Err()
					case "destination-race":
						write(t, newPath, "C")
					}
				}
				return nil
			}
			op, err := s.Apply(ctx, "rename")
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "source-race" {
				if op.State != Conflict {
					t.Fatalf("source race: %+v", op)
				}
				contents(t, old, "B")
			} else {
				contents(t, old, "A")
			}
			if scenario == "destination-race" {
				if op.State != Conflict {
					t.Fatalf("destination race: %+v", op)
				}
				contents(t, newPath, "C")
			}
			if scenario == "store-error" || scenario == "cancellation" {
				if op.State != Held {
					t.Fatalf("failed publication: %+v", op)
				}
				m.beforeSave = nil
				apply(t, s, "rename", Accepted)
				contents(t, newPath, "A")
			}
		})
	}
}
func TestRenamedDestinationReplacedBeforeRecoveryHolds(t *testing.T) {
	s, m, r := fixture(t)
	old := filepath.Join(r.root, "note.md")
	newPath := filepath.Join(r.root, "new.md")
	write(t, old, "A")
	receivePath(t, s, "rename", KindRename, "new.md", readBase(t, s, "note.md"))
	m.beforeCommitPath = func(Operation) error { return errors.New("commit unavailable") }
	held := apply(t, s, "rename", Held)
	replacement := filepath.Join(r.root, "agent.md")
	write(t, replacement, "B")
	if err := os.Rename(replacement, newPath); err != nil {
		t.Fatal(err)
	}
	m.beforeCommitPath = nil
	held = apply(t, s, "rename", Held)
	if !strings.Contains(held.Reason, "recovery required") {
		t.Fatalf("lost inode not surfaced: %+v", held)
	}
	contents(t, newPath, "B")
	contents(t, filepath.Join(r.root, held.Journal, "original"), "A")
}

func TestCommittedPathReceiptSurvivesAmbiguousCommitError(t *testing.T) {
	for _, kind := range []string{KindRename, KindDelete} {
		t.Run(kind, func(t *testing.T) {
			s, m, r := fixture(t)
			old := filepath.Join(r.root, "note.md")
			write(t, old, "A")
			dest := ""
			if kind == KindRename {
				dest = "new.md"
			}
			receivePath(t, s, "op", kind, dest, readBase(t, s, "note.md"))
			m.afterCommitPath = func(Operation) error { return ErrPathCommitUncertain }
			out, err := s.Apply(t.Context(), "op")
			if !errors.Is(err, ErrPathCommitUncertain) || out.State != Accepted || m.ops["op"].State != Accepted {
				t.Fatalf("committed receipt demoted: %+v %v", out, err)
			}
			if _, err := os.Stat(old); !os.IsNotExist(err) {
				t.Fatalf("committed move was undone: %v", err)
			}
			m.afterCommitPath = nil
			replay := apply(t, s, "op", Accepted)
			if !reflect.DeepEqual(out, replay) {
				t.Fatal("ambiguous commit replay changed")
			}
		})
	}
}

type deniedDestinationResolver struct{ *testResolver }

func (r deniedDestinationResolver) WithPaths(ctx context.Context, id string, paths []string, fn func(Source) error) error {
	if len(paths) != 2 || paths[1] == "blocked.md" {
		return ErrMembership
	}
	return r.testResolver.WithPaths(ctx, id, paths, fn)
}
func TestRenameDestinationAdmissionAndRequestGuards(t *testing.T) {
	s, _, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	base := readBase(t, s, "note.md")
	s.Sources = deniedDestinationResolver{r}
	receivePath(t, s, "blocked", KindRename, "blocked.md", base)
	apply(t, s, "blocked", Held)
	contents(t, name, "A")
	for _, req := range []Request{
		{OperationID: "traverse", Kind: KindRename, DestinationPath: "../escape.md"},
		{OperationID: "payload", Kind: KindRename, DestinationPath: "new.md", Content: []byte("B")},
		{OperationID: "delete-target", Kind: KindDelete, DestinationPath: "new.md"},
		{OperationID: "unknown", Kind: "folder"},
	} {
		req.FileID, req.BaseID, req.Generation = base.File.ID, base.Base.ID, base.Base.Generation
		if _, err := s.Receive(t.Context(), req); err == nil {
			t.Fatalf("invalid request accepted: %+v", req)
		}
	}
}

func TestChangedRenameResultCannotBecomeFreshBaseForChildEdit(t *testing.T) {
	s, m, r := fixture(t)
	old := filepath.Join(r.root, "note.md")
	target := filepath.Join(r.root, "new.md")
	write(t, old, "A")
	base := readBase(t, s, "note.md")
	receivePath(t, s, "rename", KindRename, "new.md", base)
	m.beforeCommitPath = func(Operation) error { return errors.New("commit unavailable") }
	apply(t, s, "rename", Held)
	write(t, target, "concurrent bytes")
	m.beforeCommitPath = nil
	held := apply(t, s, "rename", Held)
	if held.ResultBaseID != "" || !strings.Contains(held.Reason, "content changed before acknowledgement") {
		t.Fatalf("fresh incoming base guessed: %+v", held)
	}
	recovery, err := s.Reconcile(t.Context(), "rename")
	if err != nil || recovery.State != Conflict || string(recovery.Retained) != "A" || string(recovery.Current) != "concurrent bytes" {
		t.Fatalf("rename drift not visible: %+v %v", recovery, err)
	}
	contents(t, target, "concurrent bytes")
}
