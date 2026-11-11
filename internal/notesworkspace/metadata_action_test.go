package notesworkspace

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func metadataRead(t *testing.T, s Service, path string) ReadResult {
	t.Helper()
	read, err := s.ReadMetadataAction(t.Context(), "collection", path)
	if err != nil {
		t.Fatal(err)
	}
	return read
}
func metadataReceive(t *testing.T, s Service, id, kind, target string, read ReadResult) Operation {
	t.Helper()
	op, err := s.ReceiveMetadataAction(t.Context(), Request{OperationID: id, Kind: kind, DestinationPath: target, FileID: read.File.ID, BaseID: read.Base.ID, Generation: read.Base.Generation})
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func metadataApply(t *testing.T, s Service, id, state string) Operation {
	t.Helper()
	op, err := s.ApplyMetadataAction(t.Context(), id)
	if err != nil || op.State != state {
		t.Fatalf("apply %s: %+v %v", id, op, err)
	}
	return op
}

func TestMetadataActionStableBaseRenameDeleteAndLateWriter(t *testing.T) {
	s, store, resolver := fixture(t)
	source := filepath.Join(resolver.root, "opaque.unlisted")
	if err := os.WriteFile(source, []byte{0, 255, 2, 128}, 0640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	read := metadataRead(t, s, "opaque.unlisted")
	if again := metadataRead(t, s, "opaque.unlisted"); !reflect.DeepEqual(again, read) {
		t.Fatal("metadata base or binding changed without a filesystem change")
	}
	if read.Base.MetadataAction == nil || len(read.Base.Content) != 0 || read.Base.Hash != "" || read.ReferenceReadBytes != 0 {
		t.Fatal("metadata observation contains payload state")
	}
	metadataReceive(t, s, "rename", KindRename, "renamed.other", read)
	renamed := metadataApply(t, s, "rename", Accepted)
	if renamed.ResultFile.ID != read.File.ID || renamed.ResultFile.PathVersion != 1 || len(store.history) != 1 {
		t.Fatal("rename lost identity or history")
	}
	after, err := os.Stat(filepath.Join(resolver.root, "renamed.other"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("rename copied the original inode: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(resolver.root, renamed.Journal)); err != nil || len(entries) != 1 || entries[0].Name() != "intent" {
		t.Fatalf("rename duplicated payload: %v %v", entries, err)
	}
	base, err := store.Base(t.Context(), renamed.ResultBaseID)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(filepath.Join(resolver.root, "renamed.other"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	metadataReceive(t, s, "delete", KindDelete, "", ReadResult{File: *renamed.ResultFile, Base: base})
	deleted := metadataApply(t, s, "delete", Accepted)
	retained := filepath.Join(resolver.root, deleted.Journal, "displaced")
	retainedInfo, err := os.Stat(retained)
	if err != nil || !os.SameFile(before, retainedInfo) {
		t.Fatalf("delete did not retain original inode: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(resolver.root, "renamed.other")); !os.IsNotExist(err) {
		t.Fatal("deleted path remains", err)
	}
	if _, err := writer.WriteAt([]byte{123}, 0); err != nil {
		t.Fatal(err)
	}
	observation, err := s.Reconcile(t.Context(), "delete")
	if err != nil || observation.State != Conflict || !observation.RetainedExists || len(observation.Retained) != 0 {
		t.Fatalf("late-writer metadata observation: %+v %v", observation, err)
	}
	if got := metadataApply(t, s, "delete", Accepted); !reflect.DeepEqual(got, deleted) {
		t.Fatal("late write changed terminal receipt")
	}
	resolver.active = false
	if got := metadataApply(t, s, "rename", Accepted); !reflect.DeepEqual(got, renamed) {
		t.Fatal("exact replay required current source")
	}
	if got, err := s.ReceiveMetadataAction(t.Context(), deleted.Request); err != nil || !reflect.DeepEqual(got, deleted) {
		t.Fatal("receive replay changed", err)
	}
}

func TestMetadataActionLargeUnreadableFileWithoutSpool(t *testing.T) {
	s, _, resolver := fixture(t)
	file, err := os.Create(filepath.Join(resolver.root, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxStreamReferenceBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Chmod(0); err != nil {
		t.Fatal(err)
	}
	file.Close()
	read := metadataRead(t, s, "large.bin")
	metadataReceive(t, s, "large-rename", KindRename, "large.unknown", read)
	metadataApply(t, s, "large-rename", Accepted)
	read = metadataRead(t, s, "large.unknown")
	metadataReceive(t, s, "large-delete", KindDelete, "", read)
	deleted := metadataApply(t, s, "large-delete", Accepted)
	info, err := os.Stat(filepath.Join(resolver.root, deleted.Journal, "displaced"))
	if err != nil || info.Size() != MaxStreamReferenceBytes+1 || info.Mode().Perm() != 0 {
		t.Fatal("large inode or metadata changed", err)
	}
	entries, err := os.ReadDir(filepath.Join(resolver.root, deleted.Journal))
	if err != nil || len(entries) != 2 {
		t.Fatal("unexpected payload snapshot/spool", err)
	}
}

func TestMetadataActionStaleRevisionAndCollision(t *testing.T) {
	for _, scenario := range []string{"changed", "replaced", "collision", "late-collision", "capture-race"} {
		t.Run(scenario, func(t *testing.T) {
			s, store, resolver := fixture(t)
			source := filepath.Join(resolver.root, "source.bin")
			write(t, source, "first")
			read := metadataRead(t, s, "source.bin")
			metadataReceive(t, s, "op", KindRename, "target.bin", read)
			switch scenario {
			case "changed":
				write(t, source, "changed size")
			case "replaced":
				if err := os.Rename(source, source+"-retained"); err != nil {
					t.Fatal(err)
				}
				write(t, source, "first")
			case "collision":
				write(t, filepath.Join(resolver.root, "target.bin"), "unrelated")
			case "late-collision", "capture-race":
				store.beforeSave = func(op Operation) error {
					if scenario == "late-collision" && op.PathStage == "detached" {
						write(t, filepath.Join(resolver.root, "target.bin"), "unrelated")
					}
					if scenario == "capture-race" && op.PathStage == "prepared" {
						write(t, source, "racing source")
					}
					return nil
				}
			}
			metadataApply(t, s, "op", Conflict)
			if _, err := os.Stat(source); err != nil {
				t.Fatal("source was lost", err)
			}
			if scenario == "collision" || scenario == "late-collision" {
				contents(t, filepath.Join(resolver.root, "target.bin"), "unrelated")
			}
		})
	}
}

func TestMetadataActionRestartAndUncertainCommit(t *testing.T) {
	for _, scenario := range []string{"capture", "rename-commit", "delete-commit", "uncertain-commit"} {
		t.Run(scenario, func(t *testing.T) {
			s, store, resolver := fixture(t)
			write(t, filepath.Join(resolver.root, "source.bin"), "original")
			read := metadataRead(t, s, "source.bin")
			kind, target := KindRename, "target.bin"
			if scenario == "delete-commit" {
				kind, target = KindDelete, ""
			}
			metadataReceive(t, s, "op", kind, target, read)
			if scenario == "capture" {
				store.beforeSave = func(op Operation) error {
					if op.PathStage == "detached" {
						return errors.New("interrupted after capture")
					}
					return nil
				}
			} else if scenario == "uncertain-commit" {
				store.afterCommitPath = func(Operation) error { return ErrPathCommitUncertain }
			} else {
				store.beforeCommitPath = func(Operation) error { return errors.New("commit unavailable") }
			}
			first, err := s.ApplyMetadataAction(t.Context(), "op")
			if scenario == "uncertain-commit" {
				if !errors.Is(err, ErrPathCommitUncertain) || first.State != Accepted {
					t.Fatalf("terminal outcome demoted: %+v %v", first, err)
				}
			} else if err != nil || first.State != Held {
				t.Fatalf("interruption: %+v %v", first, err)
			}
			if first.State == Held {
				if busy, err := store.PathBusy(t.Context(), "collection", pathKey("source.bin"), "other"); err != nil || !busy {
					t.Fatal("prepared source reservation lost", err)
				}
			}
			store.beforeSave, store.beforeCommitPath, store.afterCommitPath = nil, nil, nil
			accepted := metadataApply(t, Service{Store: store, Sources: resolver}, "op", Accepted)
			if len(store.history) != 1 || accepted.ResultFile.ID != read.File.ID {
				t.Fatal("recovery duplicated history or file identity")
			}
			if got := metadataApply(t, s, "op", Accepted); !reflect.DeepEqual(got, accepted) {
				t.Fatal("replay changed receipt")
			}
		})
	}
}

func TestMetadataActionGuards(t *testing.T) {
	s, _, resolver := fixture(t)
	write(t, filepath.Join(resolver.root, "source.bin"), "original")
	read := metadataRead(t, s, "source.bin")
	request := Request{OperationID: "op", Kind: KindRename, DestinationPath: "target.bin", FileID: read.File.ID, BaseID: read.Base.ID, Generation: read.Base.Generation}
	for _, change := range []func(*Request){func(r *Request) { r.Kind = KindEdit }, func(r *Request) { r.Content = []byte("binary write") }, func(r *Request) { r.DestinationPath = ".hidden" }, func(r *Request) { r.DestinationPath = "../outside" }, func(r *Request) { r.DirectoryTargets = []string{"child"} }, func(r *Request) { r.Generation = "changed" }} {
		bad := request
		change(&bad)
		if _, err := s.ReceiveMetadataAction(t.Context(), bad); err == nil {
			t.Fatal("invalid metadata action admitted", bad)
		}
	}
	metadataReceive(t, s, "op", KindRename, "target.bin", read)
	request.DestinationPath = "different.bin"
	if _, err := s.ReceiveMetadataAction(t.Context(), request); !errors.Is(err, ErrIntentMismatch) {
		t.Fatal("operation ID reused", err)
	}
	if err := os.Symlink("source.bin", filepath.Join(resolver.root, "link.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(resolver.root, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"link.bin", "directory", ".hidden", "../outside", "missing.bin"} {
		if _, err := s.ReadMetadataAction(t.Context(), "collection", path); err == nil {
			t.Fatal("unsafe metadata base admitted", path)
		}
	}
}

func TestMetadataActionHeldWhenPublishedInodeIsReplaced(t *testing.T) {
	s, store, resolver := fixture(t)
	write(t, filepath.Join(resolver.root, "source.bin"), "original")
	read := metadataRead(t, s, "source.bin")
	metadataReceive(t, s, "op", KindRename, "target.bin", read)
	store.beforeCommitPath = func(Operation) error { return errors.New("interrupted after publication") }
	metadataApply(t, s, "op", Held)
	target := filepath.Join(resolver.root, "target.bin")
	if err := os.Rename(target, target+"-retained"); err != nil {
		t.Fatal(err)
	}
	write(t, target, "unrelated replacement")
	store.beforeCommitPath = nil
	metadataApply(t, s, "op", Held)
	contents(t, target, "unrelated replacement")
	contents(t, target+"-retained", "original")
	if len(store.history) != 0 {
		t.Fatal("unproven publication was accepted")
	}
}

func TestMetadataActionHardlinkDoesNotBecomePayloadWrite(t *testing.T) {
	s, _, resolver := fixture(t)
	write(t, filepath.Join(resolver.root, "source.bin"), "shared inode")
	if err := os.Link(filepath.Join(resolver.root, "source.bin"), filepath.Join(resolver.root, "other.bin")); err != nil {
		t.Fatal(err)
	}
	read := metadataRead(t, s, "source.bin")
	metadataReceive(t, s, "op", KindDelete, "", read)
	deleted := metadataApply(t, s, "op", Accepted)
	other, err := os.Stat(filepath.Join(resolver.root, "other.bin"))
	if err != nil {
		t.Fatal(err)
	}
	retained, err := os.Stat(filepath.Join(resolver.root, deleted.Journal, "displaced"))
	if err != nil || !os.SameFile(other, retained) {
		t.Fatal("hardlinked regular file was copied or changed", err)
	}
}

func TestMetadataActionBaseCannotAuthorizeTextEdit(t *testing.T) {
	s, _, resolver := fixture(t)
	write(t, filepath.Join(resolver.root, "empty.md"), "")
	read := metadataRead(t, s, "empty.md")
	if _, err := s.Receive(t.Context(), Request{OperationID: "wrong-owner", Kind: KindEdit, FileID: read.File.ID, BaseID: read.Base.ID, Generation: read.Base.Generation, Content: []byte("not authorized by metadata")}); !errors.Is(err, ErrReferenceOnly) {
		t.Fatal("metadata-only base authorized a text write", err)
	}
}

func TestMetadataActionGenericApplyUsesMetadataOwner(t *testing.T) {
	s, store, resolver := fixture(t)
	write(t, filepath.Join(resolver.root, "source.bin"), "original")
	read := metadataRead(t, s, "source.bin")
	metadataReceive(t, s, "op", KindRename, "target.bin", read)
	op, err := s.Apply(t.Context(), "op")
	if err != nil || op.State != Accepted || op.ResultFile.RelativePath != "target.bin" {
		t.Fatalf("generic apply did not dispatch metadata action: %+v %v", op, err)
	}
	base, err := store.Base(t.Context(), op.ResultBaseID)
	if err != nil || base.MetadataAction == nil || len(base.Content) != 0 {
		t.Fatal("generic apply invented a payload base", err)
	}
	if _, err := s.Receive(t.Context(), op.Request); !errors.Is(err, ErrReferenceOnly) {
		t.Fatal("generic text receiver replayed a metadata action", err)
	}
}
