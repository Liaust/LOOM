package cloudstorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// ACL writes here prepare or perturb synthetic fixtures. Product sealing uses
// only the reviewed descriptor-bound fchmod transition inside quarantine.
func cleanupLinuxFixtureACL(t *testing.T, path, name string, raw []byte) {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Fsetxattr(fd, name, raw, 0); errors.Is(err, unix.ENOTSUP) {
		t.Skip("fixture filesystem has no POSIX ACL support")
	} else if err != nil {
		t.Fatal(err)
	}
}

func cleanupLinuxFixtureGrant(owner, named, group, mask, other uint16) []byte {
	uid := uint32(1001)
	if uid == uint32(os.Geteuid()) {
		uid = 1002
	}
	return cleanupACLTestBytes(cleanupACLTestEntry{1, owner, 0xffffffff}, cleanupACLTestEntry{2, named, uid}, cleanupACLTestEntry{4, group, 0xffffffff}, cleanupACLTestEntry{16, mask, 0xffffffff}, cleanupACLTestEntry{32, other, 0xffffffff})
}

func TestRestoreCleanupLinuxFixtureAuthority(t *testing.T) {
	cfg, _, _ := cleanupFixture(t, true)
	for path := cfg.StateDir; ; path = filepath.Dir(path) {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, authorityErr := cleanupDirectory(fd, false)
		if authorityErr != nil {
			var st unix.Stat_t
			_ = unix.Fstat(fd, &st)
			access, _, accessErr := cleanupLinuxReadACL(fd, "system.posix_acl_access")
			defaults, _, defaultErr := cleanupLinuxReadACL(fd, "system.posix_acl_default")
			_ = unix.Close(fd)
			t.Fatalf("fixture ancestor %s uid=%d mode=%o: %v; access=%x (%v), default=%x (%v)", path, st.Uid, st.Mode, authorityErr, access, accessErr, defaults, defaultErr)
		}
		_ = unix.Close(fd)
		if path == filepath.Dir(path) {
			break
		}
	}
}

func TestRestoreCleanupLinuxRetainedACLs(t *testing.T) {
	cfg, attempt, root := cleanupFixture(t, true)
	readDir := cleanupLinuxFixtureGrant(7, 5, 5, 5, 0)
	for _, path := range []string{root, filepath.Join(root, "backup"), filepath.Join(root, "backup/sub"), filepath.Join(root, "backup/.loom-direct-archive")} {
		cleanupLinuxFixtureACL(t, path, "system.posix_acl_access", readDir)
		cleanupLinuxFixtureACL(t, path, "system.posix_acl_default", readDir)
	}
	manifest := filepath.Join(root, "backup/.loom-direct-archive/manifest.json")
	payload := filepath.Join(root, "backup/a")
	cleanupLinuxFixtureACL(t, manifest, "system.posix_acl_access", cleanupLinuxFixtureGrant(6, 5, 5, 5, 0))
	cleanupLinuxFixtureACL(t, payload, "system.posix_acl_access", cleanupLinuxFixtureGrant(6, 7, 7, 7, 4))
	var fds []int
	var before []unix.Stat_t
	var acls [][]byte
	for n, path := range []string{manifest, payload} {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			t.Fatal(err)
		}
		fds = append(fds, fd)
		defer unix.Close(fd)
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			t.Fatal(err)
		}
		before = append(before, st)
		want := uint32(0650)
		if n == 1 {
			want = 0674
		}
		if uint32(st.Mode)&07777 != want {
			t.Fatalf("fixture mode got %o", st.Mode)
		}
		raw, present, err := cleanupLinuxReadACL(fd, "system.posix_acl_access")
		if err != nil || !present {
			t.Fatal("ACL missing", err)
		}
		acls = append(acls, raw)
	}
	plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
	if err != nil {
		t.Fatal(err)
	}
	in := RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, DryRun: true}
	if _, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{}); err != nil {
		t.Fatal(err)
	}
	in.DryRun = false
	in.Yes = true
	result, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{})
	if err != nil || result.Status != "completed" {
		t.Fatalf("retained ACL apply: %+v %v", result, err)
	}
	for n, fd := range fds {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			t.Fatal(err)
		}
		raw, _, err := cleanupLinuxReadACL(fd, "system.posix_acl_access")
		if err != nil || !bytes.Equal(raw, acls[n]) || st.Mode != before[n].Mode || st.Uid != before[n].Uid || st.Gid != before[n].Gid || st.Size != before[n].Size {
			t.Fatal("cleanup rewrote retained metadata")
		}
	}
	for _, name := range []string{RestoreCleanupPlanFile, RestoreCleanupReceiptFile} {
		fd, err := unix.Open(filepath.Join(root, name), unix.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			t.Fatal(err)
		}
		raw, present, err := cleanupLinuxReadACL(fd, "system.posix_acl_access")
		if err != nil || !present {
			t.Fatal("inherited control ACL missing", err)
		}
		authority, err := cleanupParsePOSIXACL(raw, st.Uid, false)
		if err != nil || authority.nonOwnerWrite || st.Mode&07777 != 0600 || authority.mode != 0600 {
			t.Fatal("unsafe inherited control", err)
		}
	}
}
func TestRestoreCleanupLinuxACLAuthority(t *testing.T) {
	for _, kind := range []string{"read_access_and_default", "masked_raw_write", "mask_without_grant", "access_write", "default_write", "other_write", "manifest_write", "manifest_masked_write", "directory_mutation", "manifest_mutation", "foreign_owner"} {
		t.Run(kind, func(t *testing.T) {
			cfg, attempt, root := cleanupFixture(t, false)
			dir := filepath.Join(root, "backup")
			manifest := filepath.Join(dir, ".loom-direct-archive/manifest.json")
			safe := cleanupLinuxFixtureGrant(7, 5, 5, 5, 0)
			cleanupLinuxFixtureACL(t, dir, "system.posix_acl_access", safe)
			cleanupLinuxFixtureACL(t, dir, "system.posix_acl_default", safe)
			mutate := func() {
				switch kind {
				case "masked_raw_write":
					cleanupLinuxFixtureACL(t, dir, "system.posix_acl_access", cleanupLinuxFixtureGrant(7, 7, 7, 5, 0))
				case "mask_without_grant":
					cleanupLinuxFixtureACL(t, dir, "system.posix_acl_access", cleanupLinuxFixtureGrant(7, 5, 5, 7, 0))
				case "access_write", "directory_mutation":
					cleanupLinuxFixtureACL(t, dir, "system.posix_acl_access", cleanupLinuxFixtureGrant(7, 7, 5, 7, 0))
				case "default_write":
					cleanupLinuxFixtureACL(t, dir, "system.posix_acl_default", cleanupLinuxFixtureGrant(7, 7, 5, 7, 0))
				case "other_write":
					cleanupLinuxFixtureACL(t, dir, "system.posix_acl_access", cleanupLinuxFixtureGrant(7, 0, 0, 0, 2))
				case "manifest_write", "manifest_mutation":
					cleanupLinuxFixtureACL(t, manifest, "system.posix_acl_access", cleanupLinuxFixtureGrant(6, 7, 5, 7, 0))
				case "manifest_masked_write":
					cleanupLinuxFixtureACL(t, manifest, "system.posix_acl_access", cleanupLinuxFixtureGrant(6, 7, 7, 5, 0))
				case "foreign_owner":
					if os.Geteuid() != 0 {
						t.Skip("root-owned disposable fixture required for chown refusal")
					}
					if err := os.Chown(manifest, 1002, -1); err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind == "directory_mutation" || kind == "manifest_mutation" {
				plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
				if err != nil {
					t.Fatal(err)
				}
				fired := false
				_, err = applyRestoreCleanup(context.Background(), cfg, RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, Yes: true}, cleanupHooks{beforeMutation: func(point string, _ int) error {
					if point == "unlink" && !fired {
						fired = true
						dir = filepath.Join(root, cleanupQuarantineName, "backup")
						manifest = filepath.Join(dir, ".loom-direct-archive/manifest.json")
						mutate()
					}
					return nil
				}})
				if !fired || err == nil {
					t.Fatal("mutation accepted", err)
				}
				if _, err := os.Stat(manifest); err != nil {
					t.Fatal("mutation refusal removed manifest")
				}
				return
			}
			mutate()
			_, err := planRestoreCleanup(context.Background(), cfg, attempt)
			allowed := kind == "read_access_and_default" || kind == "masked_raw_write" || kind == "mask_without_grant" || kind == "manifest_masked_write"
			if (err == nil) != allowed {
				t.Fatalf("authority acceptance=%t error=%v", allowed, err)
			}
		})
	}
}
func TestRestoreCleanupLinuxForeignWriterHelper(t *testing.T) {
	if os.Getenv("LOOM_TEST_CLEANUP_FOREIGN_WRITER") != "1" {
		t.Skip("owned subprocess helper")
	}
	fd, err := unix.Openat(3, "a", unix.O_WRONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if _, err := unix.Write(fd, []byte("external content")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Unlinkat(3, "a", 0); !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EPERM) {
		t.Fatalf("non-owner changed namespace: %v", err)
	}
}
func TestRestoreCleanupLinuxForeignContentWriter(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root-capable isolated fixture")
	}
	cfg, attempt, root := cleanupFixture(t, false)
	dir := filepath.Join(root, "backup")
	cleanupLinuxFixtureACL(t, dir, "system.posix_acl_access", cleanupLinuxFixtureGrant(7, 5, 5, 5, 0))
	cleanupLinuxFixtureACL(t, filepath.Join(dir, "a"), "system.posix_acl_access", cleanupLinuxFixtureGrant(6, 7, 7, 7, 4))
	plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestRestoreCleanupLinuxForeignWriterHelper$", "-test.v")
	cmd.Env = []string{"LOOM_TEST_CLEANUP_FOREIGN_WRITER=1"}
	cmd.ExtraFiles = []*os.File{f}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1001, Gid: 1001, NoSetGroups: true}}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("foreign fixture writer: %v %s", err, out)
	}
	if _, err := applyRestoreCleanup(context.Background(), cfg, RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, Yes: true}, cleanupHooks{}); err == nil {
		t.Fatal("content metadata drift accepted")
	}
	fresh, err := planRestoreCleanup(context.Background(), cfg, attempt)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyRestoreCleanup(context.Background(), cfg, RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: fresh.PlanDigest, Yes: true}, cleanupHooks{})
	if err != nil || result.Status != "completed" {
		t.Fatal(fmt.Sprint(result), err)
	}
}
func TestRestoreCleanupLinuxMountIdentity(t *testing.T) {
	var ids []string
	for _, path := range []string{"/", "/proc"} {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			t.Fatal(err)
		}
		id, err := cleanupMountID(fd)
		unix.Close(fd)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if ids[0] == ids[1] {
		t.Fatal("distinct fixture mounts not distinguished")
	}
}

func TestRestoreCleanupLinuxNestedACLSeal(t *testing.T) {
	cfg, attempt, root := cleanupFixture(t, true)
	sub := filepath.Join(root, "backup/sub")
	installed := filepath.Join(sub, "installed")
	if err := os.Mkdir(installed, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "fixture"), []byte("nested synthetic bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, base := range []string{filepath.Join(root, "backup"), filepath.Join(root, cleanupQuarantineName, "backup")} {
			_ = os.Chmod(filepath.Join(base, "sub"), 0700)
			_ = os.Chmod(filepath.Join(base, "sub/installed"), 0700)
		}
	})
	uid := uint32(os.Geteuid())
	entries := func(owner, other uint16) []byte {
		return cleanupACLTestBytes(cleanupACLTestEntry{1, owner, 0xffffffff}, cleanupACLTestEntry{2, 5, uid}, cleanupACLTestEntry{2, 5, 1003}, cleanupACLTestEntry{4, 7, 0xffffffff}, cleanupACLTestEntry{16, 7, 0xffffffff}, cleanupACLTestEntry{32, other, 0xffffffff})
	}
	type held struct {
		fd     int
		before cleanupIdentity
		acl    cleanupACLState
	}
	var heldDirs []held
	for n, path := range []string{sub, installed} {
		if os.Geteuid() == 0 {
			if err := os.Chown(path, -1, 1001); err != nil {
				t.Fatal(err)
			}
		}
		owner := uint16(5)
		if n == 1 {
			owner = 7
		}
		cleanupLinuxFixtureACL(t, path, "system.posix_acl_access", entries(owner, 5))
		cleanupLinuxFixtureACL(t, path, "system.posix_acl_default", entries(7, 0))
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		before, acl, err := cleanupPayloadDirectory(fd, false)
		if err != nil {
			t.Fatal(err)
		}
		heldDirs = append(heldDirs, held{fd, before, acl})
	}
	plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyRestoreCleanup(context.Background(), cfg, RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, Yes: true}, cleanupHooks{})
	if err != nil || result.Status != "completed" {
		t.Fatal("nested ACL seal", result, err)
	}
	for _, h := range heldDirs {
		after, err := cleanupStat(h.fd)
		if err != nil {
			t.Fatal(err)
		}
		acl, err := cleanupReadPayloadDirACL(h.fd)
		if err != nil {
			t.Fatal(err)
		}
		expected := cleanupSealedACL(h.acl, 0755)
		if after.Mode&07777 != 0755 || after.Inode != h.before.Inode || after.UID != h.before.UID || after.GID != h.before.GID || !bytes.Equal(acl.Access, expected.Access) || !bytes.Equal(acl.Default, h.acl.Default) {
			t.Fatal("unexpected sealed ACL identity")
		}
		authority, err := cleanupParsePOSIXACL(acl.Access, after.UID, false)
		if err != nil || authority.nonOwnerWrite {
			t.Fatal("seal retained external write")
		}
	}
}
func TestRestoreCleanupLinuxUnprivilegedSealHelper(t *testing.T) {
	if os.Getenv("LOOM_TEST_CLEANUP_SEAL_HELPER") != "1" {
		t.Skip("owned subprocess helper")
	}
	if os.Geteuid() != 1001 || os.Getegid() != 1001 {
		t.Fatal("wrong fixture daemon identity")
	}
	TestRestoreCleanupQuarantineSeals(t)
	TestRestoreCleanupLinuxNestedACLSeal(t)
	// Exercise actual nlink/ctime changes as the non-root daemon too.
	t.Run("HardlinkComplete", TestRestoreCleanupHardlinkComplete)
	t.Run("HardlinkMutationRefusals", TestRestoreCleanupHardlinkMutationRefusals)
	t.Run("HardlinkInterruptedAndTamperedJournal", TestRestoreCleanupHardlinkInterruptedAndTamperedJournal)
	t.Run("HardlinkFinalAcknowledgementMutation", TestRestoreCleanupHardlinkFinalAcknowledgementMutation)
}
func TestRestoreCleanupLinuxUnprivilegedSeal(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("LOOM_TEST_CLEANUP_ISOLATED") != "1" {
		t.Skip("requires explicitly isolated root fixture")
	}
	// Only the explicitly isolated chroot runner enables this root-owned fixture.
	// A fresh daemon-owned cwd avoids changing any existing fixture permissions.
	work, err := os.MkdirTemp("/", ".loom-acceptance-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)
	if err := os.Chown(work, 1001, 1001); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestRestoreCleanupLinuxUnprivilegedSealHelper$", "-test.v")
	cmd.Dir = work
	cmd.Env = []string{"LOOM_TEST_CLEANUP_SEAL_HELPER=1"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1001, Gid: 1001}}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unprivileged daemon seal: %v %s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestRestoreCleanupLinuxBindMountRefusal(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("LOOM_TEST_CLEANUP_ISOLATED") != "1" {
		t.Skip("requires explicitly isolated mount namespace")
	}
	cfg, attempt, root := cleanupFixture(t, false)
	source := filepath.Join(root, "bind-source")
	target := filepath.Join(root, "backup/mounted")
	for _, path := range []string{source, target, filepath.Join(root, "backup/source-dir")} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Unmount(target, 0); err != nil {
			t.Error(err)
		}
	}()
	if _, err := planRestoreCleanup(context.Background(), cfg, attempt); err == nil {
		t.Fatal("same-device bind mount accepted")
	}
	for _, name := range []string{RestoreCleanupPlanFile, RestoreCleanupReceiptFile, cleanupQuarantineName} {
		if err := cleanupTestAbsent(root, name); err != nil {
			t.Fatal(err)
		}
	}
	from, err := unix.Open(filepath.Join(root, "backup"), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(from)
	to, err := unix.Open(target, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(to)
	if err := cleanupRenameNoReplace(from, "source-dir", to, "destination"); !errors.Is(err, unix.EXDEV) {
		t.Fatalf("cross-mount rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "backup/source-dir")); err != nil {
		t.Fatal("cross-mount source changed")
	}
}
