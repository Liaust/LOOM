package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActivationLeaseExcludesApplyAndRollbackAndReleases(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(filepath.Join(root, "target"), 0755); err != nil {
		t.Fatal(err)
	}
	first, err := acquireActivationLease(state)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := acquireActivationLease(state); err == nil {
		second.Close()
		t.Fatal("overlapping lease admitted")
	}
	result, err := Apply(context.Background(), ApplyInput{Yes: true, AllowNonProduction: true, Spec: UpdateSpec{StateDir: state, ReleasePath: filepath.Join(root, "target")}})
	if err != nil || !result.Refused || !strings.Contains(result.Refusal, "another Main update") {
		t.Fatal("apply did not refuse competing owner", result, err)
	}
	rollback, err := Rollback(context.Background(), RollbackInput{Yes: true, AllowNonProduction: true, StateDir: state})
	if err != nil || !rollback.Refused {
		t.Fatal("rollback did not refuse competing owner", rollback, err)
	}
	if _, err = os.Lstat(state); !os.IsNotExist(err) {
		t.Fatal("refusal created manifest state")
	}
	first.Close()
	again, err := acquireActivationLease(state)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
}

func TestActivationLeaseRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "user-file")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "state.activation.lock")); err != nil {
		t.Fatal(err)
	}
	if lease, err := acquireActivationLease(filepath.Join(root, "state")); err == nil {
		lease.Close()
		t.Fatal("followed lock link")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "keep" {
		t.Fatal("changed user file")
	}
}

func TestActivationLeaseReusesProvisionedInodeInReadOnlyParent(t *testing.T) {
	root := t.TempDir()
	lock := filepath.Join(root, "state.activation.lock")
	if err := os.WriteFile(lock, []byte("preserved"), 0440); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(root, 0700)
	lease, err := acquireActivationLease(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	after, err := os.Stat(lock)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("replaced provisioned lease inode", err)
	}
	content, err := os.ReadFile(lock)
	if err != nil || string(content) != "preserved" || after.Mode().Perm() != 0440 {
		t.Fatal("changed provisioned lease", err)
	}
}
