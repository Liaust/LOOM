package notesworkspacesync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryLockSerializesReplicaAndRejectsLinks(t *testing.T) {
	root := t.TempDir()
	replica := filepath.Join(root, "replica")
	f, ok, err := lockReplica(replica)
	if err != nil || !ok {
		t.Fatalf("first lock: %v %v", ok, err)
	}
	defer f.Close()
	if second, ok, err := lockReplica(replica); err != nil || ok || second != nil {
		t.Fatalf("quiet interval was not respected: %v %v", ok, err)
	}
	f.Close()
	third, ok, err := lockReplica(replica)
	if err != nil || !ok {
		t.Fatalf("resume: %v %v", ok, err)
	}
	third.Close()
	path := filepath.Join(root, "recovery.lock")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "elsewhere"), path); err != nil {
		t.Fatal(err)
	}
	if f, ok, err := lockReplica(replica); err == nil || ok || f != nil {
		t.Fatal("symlink accepted")
	}
}
