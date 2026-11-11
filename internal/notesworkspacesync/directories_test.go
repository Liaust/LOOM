package notesworkspacesync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryControlCreateRenameDeleteAndReplay(t *testing.T) {
	s, n, _, root, _ := fixture(t)
	run := func(i Intent) Binding {
		t.Helper()
		raw, _ := json.Marshal(i)
		path := i.Path
		role := "content"
		if i.Operation == "rename" {
			path = i.Target
		}
		if i.Operation == "delete" {
			role = "deletion"
		}
		p := Publication{Header: Header{1, "publication", "pub_" + i.ID}, EntryKind: "directory", IntentID: i.ID, IntentDigest: digest(raw), Revisions: []Revision{{Path: path, Revision: "1-control", Role: role}}}
		payload := Prefix + "payload/" + i.ID + ".md"
		n.docs[payload+"@1-control"] = Evidence{Path: payload, Revision: "1-control", Status: "available", SHA256: digest(nil)[7:]}
		n.add(i)
		n.add(p)
		if _, err := s.Step(t.Context(), 128); err != nil {
			t.Fatal(err)
		}
		ack := n.acks[len(n.acks)-1]
		if ack.Status != "applied" || ack.Binding == nil {
			t.Fatalf("directory not applied: %+v", ack)
		}
		return *ack.Binding
	}
	create := Intent{Header: Header{1, "intent", "empty_create"}, Device: "device", Operation: "create", Workspace: "w", Collection: "c", Generation: "generation", FileID: "directory_client", EntryKind: "directory", Path: "Notes/Empty", SHA256: digest(nil)}
	b := run(create)
	entries, err := os.ReadDir(filepath.Join(root, "Empty"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("directory must exist empty before any file: %v %v", entries, err)
	}
	n.add(create)
	if _, err := s.Step(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	rename := create
	rename.ID = "empty_rename"
	rename.Operation = "rename"
	rename.Base = &b
	rename.Target = "Notes/Renamed"
	b = run(rename)
	if _, err := os.Stat(filepath.Join(root, "Empty")); !os.IsNotExist(err) {
		t.Fatalf("old empty directory remains: %v", err)
	}
	remove := create
	remove.ID = "empty_delete"
	remove.Operation = "delete"
	remove.Base = &b
	remove.Path = "Notes/Renamed"
	b = run(remove)
	if !b.Deleted {
		t.Fatal("directory tombstone not published")
	}
	if _, err := os.Stat(filepath.Join(root, "Renamed")); !os.IsNotExist(err) {
		t.Fatalf("deleted directory remains: %v", err)
	}
}

func TestDirectoryExportDoesNotPublishAVisibleFile(t *testing.T) {
	s, n, _, root, _ := fixture(t)
	if err := os.Mkdir(filepath.Join(root, "FromMain"), 0700); err != nil {
		t.Fatal(err)
	}
	source := s.Source.(NotesSource)
	read, err := source.ReadDirectory(t.Context(), "source", "FromMain")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ExportDirectory(t.Context(), "c", read.File.ID, read.Base.ID)
	if err != nil || b.EntryKind != "directory" || b.Path != "Notes/FromMain" {
		t.Fatalf("%+v %v", b, err)
	}
	if !b.valid() {
		t.Fatal("invalid directory binding")
	}
	for path := range n.docs {
		if path == "Notes/FromMain@1-control" {
			t.Fatal("visible placeholder document")
		}
	}
	if err := s.publishScopes(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestExistingExportedDirectoryCreateReturnsIdentityRefusal(t *testing.T) {
	s, n, _, root, _ := fixture(t)
	if err := os.Mkdir(filepath.Join(root, "Existing"), 0700); err != nil {
		t.Fatal(err)
	}
	source := s.Source.(NotesSource)
	read, err := source.ReadDirectory(t.Context(), "source", "Existing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExportDirectory(t.Context(), "c", read.File.ID, read.Base.ID); err != nil {
		t.Fatal(err)
	}
	i := Intent{Header: Header{1, "intent", "dir_duplicate"}, Device: "device", Operation: "create", Workspace: "w", Collection: "c", Generation: "generation", FileID: "another_identity", EntryKind: "directory", Path: "Notes/Existing", SHA256: digest(nil)}
	raw, _ := json.Marshal(i)
	p := Publication{Header: Header{1, "publication", "dir_pub_duplicate"}, EntryKind: "directory", IntentID: i.ID, IntentDigest: digest(raw), Revisions: []Revision{{Path: i.Path, Revision: "1-control", Role: "content"}}}
	n.docs[Prefix+"payload/"+i.ID+".md@1-control"] = Evidence{Path: Prefix + "payload/" + i.ID + ".md", Revision: "1-control", Status: "available", SHA256: digest(nil)[7:]}
	n.add(i)
	n.add(p)
	if _, err = s.Step(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	if len(n.acks) != 1 || n.acks[0].Status != "held" || n.acks[0].Reason != "directory_identity_collision" {
		t.Fatalf("expected terminal identity refusal, got %+v", n.acks)
	}
	if busy, err := s.directoryReserved(t.Context(), s.Scopes[0], "Existing"); err != nil || busy {
		t.Fatalf("refused duplicate ensure blocks source metadata: %v %v", busy, err)
	}
	if err := s.Store.WithReplica(t.Context(), s.Replica, s.Epoch, func(r Records) error {
		j, err := load[Join](t.Context(), r, "operation", i.ID)
		if err != nil {
			return err
		}
		j.ResultBaseID = "unfinished_effect"
		return r.Put(t.Context(), "operation", i.ID, j)
	}); err != nil {
		t.Fatal(err)
	}
	if busy, err := s.directoryReserved(t.Context(), s.Scopes[0], "Existing"); err != nil || !busy {
		t.Fatalf("an effectful operation lost its reservation: %v %v", busy, err)
	}
}
