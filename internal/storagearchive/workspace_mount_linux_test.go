package storagearchive

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWorkspaceMountBoundaries(t *testing.T) {
	if os.Getenv("LOOM_WORKSPACE_MOUNT_CHILD") != "1" {
		if os.Geteuid() != 0 {
			t.Skip("requires a disposable Linux root-capable runner")
		}
		cmd := exec.Command(os.Args[0], "-test.v", "-test.count=1", "-test.run=^TestWorkspaceMountBoundaries$")
		cmd.Env = append(os.Environ(), "LOOM_WORKSPACE_MOUNT_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNS}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("private mount namespace: %v\n%s", err, out)
		}
		t.Log(string(out))
		return
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, which := range []string{"source_parent", "source_root", "destination_parent"} {
		t.Run(which, func(t *testing.T) {
			f := newWorkspaceMoveFixture(t)
			plan := f.plan(t)
			before := snapshotWorkspacePayload(t, f.paths.Active.AbsolutePath)
			target := filepath.Dir(f.paths.Active.AbsolutePath)
			if which == "source_root" {
				target = f.paths.Active.AbsolutePath
			} else if which == "destination_parent" {
				target = filepath.Dir(f.paths.ArchiveContainer.AbsolutePath)
			}
			unmount := workspaceTestBindMount(t, target)
			if _, err := f.service.PlanArchive(ctx, f.planInput()); err == nil || !strings.Contains(err.Error(), "mount boundary") {
				t.Fatalf("bound plan must reject same-device bind mount: %v", err)
			}
			if _, err := f.service.ApplyArchive(ctx, plan, plan.PlanDigest); err == nil || f.journal.exists {
				t.Fatalf("apply crossed mount or committed intent: %v", err)
			}
			if got := snapshotWorkspacePayload(t, f.paths.Active.AbsolutePath); !reflect.DeepEqual(got, before) {
				t.Fatal("mount refusal changed payload")
			}
			unmount()
			if _, err := f.service.ApplyArchive(ctx, plan, plan.PlanDigest); err != nil {
				t.Fatal(err)
			}
			if got := snapshotWorkspacePayload(t, f.paths.ArchivePayload.AbsolutePath); !reflect.DeepEqual(got, before) {
				t.Fatal("corrected mount changed payload")
			}
		})
	}
	t.Run("pending_recovery", func(t *testing.T) {
		f := newWorkspaceMoveFixture(t)
		plan := f.plan(t)
		before := snapshotWorkspacePayload(t, f.paths.Active.AbsolutePath)
		f.service.FailureHook = func(b WorkspaceMoveBoundary) error {
			if b == BoundaryAfterIntent {
				return errors.New("isolated interruption")
			}
			return nil
		}
		if _, err := f.service.ApplyArchive(ctx, plan, plan.PlanDigest); err == nil || !f.journal.exists {
			t.Fatal("missing interrupted intent")
		}
		f.service.FailureHook = nil
		unmount := workspaceTestBindMount(t, filepath.Dir(f.paths.ArchiveContainer.AbsolutePath))
		if _, err := f.service.RecoverArchive(ctx, plan.OperationID); err == nil {
			t.Fatal("pending recovery accepted bind mount")
		}
		if got := snapshotWorkspacePayload(t, f.paths.Active.AbsolutePath); !reflect.DeepEqual(got, before) {
			t.Fatal("failed recovery changed source")
		}
		unmount()
		if _, err := f.service.RecoverArchive(ctx, plan.OperationID); err != nil {
			t.Fatal(err)
		}
		restore := planRestore(t, f, plan.OperationID)
		if _, err := f.service.ApplyRestore(ctx, restore, restore.PlanDigest); err != nil {
			t.Fatal(err)
		}
		if got := snapshotWorkspacePayload(t, f.paths.Active.AbsolutePath); !reflect.DeepEqual(got, before) {
			t.Fatal("recovered round trip changed source")
		}
	})
	t.Run("restore_before_intent", func(t *testing.T) {
		f, archived, before := archivedRestoreFixture(t)
		plan := planRestore(t, f, archived.OperationID)
		unmount := workspaceTestBindMount(t, filepath.Dir(f.paths.Active.AbsolutePath))
		if _, err := f.service.PlanRestore(ctx, WorkspaceRestorePlanInput{OperationID: restoreTestOperationID, ArchiveOperationID: archived.OperationID, ActorID: "actor_archive_test", Reason: "mount test"}); err == nil {
			t.Fatal("restore plan accepted bind mount")
		}
		if _, err := f.service.ApplyRestore(ctx, plan, plan.PlanDigest); err == nil || f.journal.restoreIntentCommits != 0 {
			t.Fatalf("restore did not refuse before intent: %v", err)
		}
		unmount()
		if _, err := f.service.ApplyRestore(ctx, plan, plan.PlanDigest); err != nil {
			t.Fatal(err)
		}
		if got := snapshotWorkspacePayload(t, f.paths.Active.AbsolutePath); !reflect.DeepEqual(got, before) {
			t.Fatal("restore changed payload")
		}
	})
}

func workspaceTestBindMount(t *testing.T, target string) func() {
	t.Helper()
	if err := unix.Mount(target, target, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	active := true
	unmount := func() {
		if active {
			if err := unix.Unmount(target, 0); err != nil {
				t.Fatal(err)
			}
			active = false
		}
	}
	t.Cleanup(unmount)
	return unmount
}

func TestWorkspaceMountIdentityRejectsInvalidDescriptor(t *testing.T) {
	if err := sameWorkspaceMounts(-1); err == nil {
		t.Fatal("unobservable mount identity was accepted")
	}
}
