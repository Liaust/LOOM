package cloudstorage

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// These are feasibility counterexamples, NOT cleanup acceptance tests. They
// deliberately execute the rejected check-then-unlink algorithm on tiny, newly
// created fixtures. Passing means that descriptor-relative unlink can still
// remove an unreviewed replacement after the final identity check. Do not copy
// this algorithm into a cleanup implementation or invert these assertions to
// imply that the frozen deletion contract has been implemented.
func TestRestoreCleanupUnlinkIdentityGap(t *testing.T) {
	for _, directory := range []bool{false, true} {
		name := "regular"
		if directory {
			name = "empty_directory"
		}
		t.Run(name, func(t *testing.T) {
			root := cleanupGapFixture(t)
			backup := filepath.Join(root, "backup")
			if err := os.Mkdir(backup, 0700); err != nil {
				t.Fatal(err)
			}
			for _, leaf := range []string{"reviewed", "replacement"} {
				var err error
				if directory {
					err = os.Mkdir(filepath.Join(backup, leaf), 0700)
				} else {
					err = os.WriteFile(filepath.Join(backup, leaf), []byte(leaf), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			anchors := cleanupGapHoldChain(t, backup)
			parent := anchors[len(anchors)-1].fd
			reviewed := cleanupGapOpen(t, parent, "reviewed", directory)
			replacement := cleanupGapOpen(t, parent, "replacement", directory)
			defer unix.Close(reviewed)
			defer unix.Close(replacement)
			original := cleanupGapStat(t, reviewed)
			other := cleanupGapStat(t, replacement)
			if original.Dev != other.Dev || original.Ino == other.Ino {
				t.Fatal("fixture needs distinct same-device inodes")
			}
			cleanupGapLock(t, anchors[len(anchors)-2].fd)
			cleanupGapValidateChain(t, anchors)
			var named unix.Stat_t
			if err := unix.Fstatat(parent, "reviewed", &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || named != original {
				t.Fatal("final exact no-follow identity check did not pass", err)
			}

			// Deterministic interleaving in the last-check-to-unlink interval.
			// No timing, process-name inference, symlinks or data rehash is used.
			// The second goroutine ignores the advisory lifecycle lock, just as
			// another namespace writer with the same permissions can do.
			done := make(chan error, 1)
			go func() {
				if err := unix.Renameat(parent, "reviewed", parent, "reviewed-saved"); err != nil {
					done <- err
					return
				}
				done <- unix.Renameat(parent, "replacement", parent, "reviewed")
			}()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			flags := 0
			if directory {
				flags = unix.AT_REMOVEDIR
			}
			if err := unix.Unlinkat(parent, "reviewed", flags); err != nil {
				t.Fatal("counterexample unlink unexpectedly refused", err)
			}
			if err := unix.Fstatat(parent, "reviewed", &named, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
				t.Fatal("replacement name was not removed", err)
			}
			if err := unix.Fstatat(parent, "reviewed-saved", &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || named.Dev != original.Dev || named.Ino != original.Ino {
				t.Fatal("reviewed object was not retained at its moved name", err)
			}
			removed := cleanupGapStat(t, replacement)
			retained := cleanupGapStat(t, reviewed)
			if !directory && (removed.Nlink != 0 || retained.Nlink != 1) {
				t.Fatalf("unexpected link truth: replacement=%d reviewed=%d", removed.Nlink, retained.Nlink)
			}
			t.Logf("counterexample confirmed: final check accepted dev=%d ino=%d; unlink removed replacement ino=%d; reviewed inode remains", original.Dev, original.Ino, other.Ino)
		})
	}
}

func TestRestoreCleanupAncestorCustodyGap(t *testing.T) {
	root := cleanupGapFixture(t)
	backup := filepath.Join(root, "backup")
	if err := os.Mkdir(backup, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "reviewed"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	anchors := cleanupGapHoldChain(t, backup)
	parent := anchors[len(anchors)-1].fd
	attempt := anchors[len(anchors)-2].fd
	cleanupGapLock(t, attempt)
	file := cleanupGapOpen(t, parent, "reviewed", false)
	defer unix.Close(file)
	original := cleanupGapStat(t, file)
	cleanupGapValidateChain(t, anchors)
	var named unix.Stat_t
	if err := unix.Fstatat(parent, "reviewed", &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || named != original {
		t.Fatal("final exact no-follow identity check did not pass", err)
	}
	// Holding the entire chain does not prevent a rename of a held directory.
	if err := unix.Renameat(attempt, "backup", attempt, "moved-backup"); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkdirat(attempt, "backup", 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Unlinkat(parent, "reviewed", 0); err != nil {
		t.Fatal("counterexample unlink unexpectedly refused", err)
	}
	if cleanupGapStat(t, file).Nlink != 0 {
		t.Fatal("expected removal in renamed custody")
	}
	if err := unix.Fstatat(attempt, "backup", &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || named.Ino == anchors[len(anchors)-1].stat.Ino {
		t.Fatal("expected replacement backup directory", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "moved-backup", "reviewed")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reviewed entry was not removed from moved custody", err)
	}
	t.Log("counterexample confirmed: full held-chain revalidation passed, but unlink proceeded inside backup after its custody name was replaced")
}

func cleanupGapFixture(t *testing.T) string {
	t.Helper()
	physical, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(physical, ".loom-acceptance", "cloud", "restore-drills", "synthetic-failed-attempt")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

type cleanupGapAnchor struct {
	fd   int
	name string
	stat unix.Stat_t
}

func cleanupGapHoldChain(t *testing.T, path string) []cleanupGapAnchor {
	t.Helper()
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	anchors := []cleanupGapAnchor{{fd: fd, name: "/", stat: cleanupGapStat(t, fd)}}
	t.Cleanup(func() {
		for i := len(anchors) - 1; i >= 0; i-- {
			_ = unix.Close(anchors[i].fd)
		}
	})
	for _, name := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next := cleanupGapOpen(t, fd, name, true)
		anchors = append(anchors, cleanupGapAnchor{fd: next, name: name, stat: cleanupGapStat(t, next)})
		fd = next
	}
	return anchors
}

func cleanupGapOpen(t *testing.T, parent int, name string, directory bool) int {
	t.Helper()
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(parent, name, flags, 0)
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

func cleanupGapStat(t *testing.T, fd int) unix.Stat_t {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		t.Fatal(err)
	}
	return stat
}

func cleanupGapValidateChain(t *testing.T, anchors []cleanupGapAnchor) {
	t.Helper()
	for i, anchor := range anchors {
		held := cleanupGapStat(t, anchor.fd)
		// Ancestor identity excludes timestamps/size: unrelated sibling work
		// changes them without changing the root/ancestor namespace binding.
		if held.Dev != anchor.stat.Dev || held.Ino != anchor.stat.Ino || held.Mode != anchor.stat.Mode || held.Uid != anchor.stat.Uid || held.Gid != anchor.stat.Gid {
			t.Fatal("held ancestor changed before the final check")
		}
		if i > 0 {
			var named unix.Stat_t
			if err := unix.Fstatat(anchors[i-1].fd, anchor.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || named.Dev != held.Dev || named.Ino != held.Ino || named.Mode != held.Mode || named.Uid != held.Uid || named.Gid != held.Gid {
				t.Fatal("named ancestor changed before the final check", err)
			}
		}
	}
}

func cleanupGapLock(t *testing.T, attempt int) {
	t.Helper()
	fd, err := unix.Openat(attempt, ".attempt-lifecycle.lock", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	second := cleanupGapOpen(t, attempt, ".attempt-lifecycle.lock", false)
	defer unix.Close(second)
	if err := unix.Flock(second, unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
		t.Fatal("fixture did not exclude a cooperating lock holder", err)
	}
}

func cleanupFixture(t *testing.T, legacy bool) (Config, string, string) {
	t.Helper()
	// The checkout may inherit shared CI workspace ACLs. Use private home
	// scratch, not that checkout or world-writable /tmp; custody checks stay on.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(home, ".loom-acceptance")
	if err = os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp(scratch, "restore-cleanup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Snapshots.Backend = SnapshotBackendBorg
	cfg.StateDir = filepath.Join(base, "cloud")
	attempt := "20260906T101535Z-history-cleanup"
	root := filepath.Join(cfg.StateDir, "restore-drills", attempt)
	for _, dir := range []string{"backup/sub", "backup/.loom-direct-archive"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range map[string]string{"backup/a": "reviewed payload", "backup/sub/b": "second payload", "sentinel": "outside backup", "backup/.loom-direct-archive/manifest.json": "{\"fixture\":true}\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../sentinel", filepath.Join(root, "backup", "link")); err != nil {
		t.Fatal(err)
	}
	manifest := cleanupDigest([]byte("{\"fixture\":true}\n"))
	operational, provenance := "loom_restore_drill_cleanup", "loom_provenance_restore_drill_cleanup"
	if legacy {
		record := RestoreCleanupLegacyEvidence{Schema: "loom.cloud_restore_legacy_failure.v1", Status: "failed", Diagnostic: "diagnostic_unavailable", Attempt: attempt, Ref: "history-cleanup", Archive: "__loom-direct-user-data-main-history-cleanup", ManifestSHA256: manifest, OperationalTarget: operational, ProvenanceTarget: provenance}
		if err := os.WriteFile(filepath.Join(root, RestoreCleanupLegacyFile), cleanupJSON(record), 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		var st unix.Stat_t
		if err := unix.Stat(root, &st); err != nil {
			t.Fatal(err)
		}
		unlock, err := startRestoreAttemptLifecycle(root, st)
		if err != nil {
			t.Fatal(err)
		}
		unlock.close()
		record, _ := restoreFetchFailureEvidence(SnapshotFetchResult{Status: SnapshotStatusFailed, Ref: "history-cleanup", Archive: "__loom-direct-user-data-main-history-cleanup", DirectArchiveManifestSHA256: manifest}, CloudRestoreDrillInput{Config: cfg, Ref: "history-cleanup", TargetDatabase: operational, ProvenanceTargetDatabase: provenance}, time.Now().UTC(), nil)
		if _, err := publishRestoreFailureReceipt(root, record); err != nil {
			t.Fatal(err)
		}
	}
	var st unix.Stat_t
	if err := unix.Stat(root, &st); err != nil {
		t.Fatal(err)
	}
	pre := RestoreCleanupPreflight{Schema: "loom.cloud_restore_cleanup_preflight.v1", Attempt: attempt, Device: uint64(st.Dev), Inode: uint64(st.Ino), OperationalTarget: operational, ProvenanceTarget: provenance,
		Inactive: RestoreCleanupObservation{"inactive", time.Now().UTC(), cleanupDigest([]byte("separate inactive fixture observation"))}, DatabaseAbsence: RestoreCleanupObservation{"both_absent", time.Now().UTC(), cleanupDigest([]byte("separate database absence fixture observation"))}, HealthQuiet: RestoreCleanupObservation{"healthy_no_restore_borg_backup", time.Now().UTC(), cleanupDigest([]byte("separate healthy quiet fixture observation"))}}
	if err := os.WriteFile(filepath.Join(root, RestoreCleanupPreflightFile), cleanupJSON(pre), 0600); err != nil {
		t.Fatal(err)
	}
	return cfg, attempt, root
}

func TestRestoreCleanupPlanApplyReplay(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			cfg, attempt, root := cleanupFixture(t, legacy)
			failure := RestoreFailureReceiptFile
			if legacy {
				failure = RestoreCleanupLegacyFile
			}
			raw, err := os.ReadFile(filepath.Join(root, failure))
			if err != nil {
				t.Fatal(err)
			}
			plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Status != "planned" || plan.Entries != 7 || plan.Legacy != legacy || plan.FailureSHA256 != cleanupDigest(raw) {
				t.Fatalf("plan %+v", plan)
			}
			public := string(cleanupJSON(plan))
			if strings.Contains(public, cfg.StateDir) || strings.Contains(public, "second payload") || strings.Contains(public, "backup/sub") {
				t.Fatal("private payload leaked")
			}
			for _, name := range []string{RestoreCleanupPlanFile, RestoreCleanupReceiptFile} {
				if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Fatal("plan wrote control files")
				}
			}
			in := RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, DryRun: true}
			dry, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{})
			if err != nil || dry.Status != "planned" || dry.ConfirmedRemoved != 0 {
				t.Fatalf("dry=%+v err=%v", dry, err)
			}
			if _, err := os.Stat(filepath.Join(root, "backup/sub/b")); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{RestoreCleanupPlanFile, RestoreCleanupReceiptFile} {
				if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Fatal("dry-run wrote control files")
				}
			}
			in.DryRun = false
			in.Yes = true
			got, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "completed" || got.ConfirmedRemoved != plan.Entries {
				t.Fatalf("apply %+v", got)
			}
			if _, err := os.Lstat(filepath.Join(root, "backup")); !os.IsNotExist(err) {
				t.Fatal("backup remains")
			}
			after, _ := os.ReadFile(filepath.Join(root, failure))
			if !bytes.Equal(raw, after) {
				t.Fatal("failure changed")
			}
			sentinel, _ := os.ReadFile(filepath.Join(root, "sentinel"))
			if string(sentinel) != "outside backup" {
				t.Fatal("symlink referent changed")
			}
			journal, _ := os.ReadFile(filepath.Join(root, RestoreCleanupReceiptFile))
			var first cleanupJournal
			if err := cleanupDecode(bytes.Split(journal, []byte{'\n'})[0], &first); err != nil || first.Failure == nil || !bytes.Equal(first.Failure.Bytes, raw) {
				t.Fatal("receipt not independently readable", err)
			}
			replay, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{})
			if err != nil || replay.Status != "completed" {
				t.Fatalf("replay %+v %v", replay, err)
			}
			preserved, _ := os.ReadFile(filepath.Join(root, RestoreCleanupReceiptFile))
			if !bytes.Equal(journal, preserved) {
				t.Fatal("replay rewrote receipt")
			}
			for _, name := range []string{RestoreCleanupPlanFile, RestoreCleanupReceiptFile} {
				info, err := os.Stat(filepath.Join(root, name))
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("control mode", err)
				}
			}
		})
	}
}

func TestRestoreCleanupRefusals(t *testing.T) {
	for _, kind := range []string{"missing_digest", "wrong_digest", "missing_yes", "active", "hardlink", "fifo", "writable_directory", "root_writable", "root_symlink", "stale_file", "stale_manifest", "modified_failure", "missing_preflight", "forged_legacy_success", "legacy_targets", "legacy_observation", "legacy_without_adoption", "oversize_file", "oversize_evidence", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			legacy := strings.HasPrefix(kind, "legacy") || kind == "forged_legacy_success"
			cfg, attempt, root := cleanupFixture(t, legacy)
			plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
			if err != nil {
				t.Fatal(err)
			}
			in := RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, Yes: true}
			ctx := context.Background()
			switch kind {
			case "missing_digest":
				in.ConfirmDigest = ""
			case "wrong_digest":
				in.ConfirmDigest = strings.Repeat("0", 64)
			case "missing_yes":
				in.Yes = false
			case "active":
				fd := cleanupGapOpen(t, unix.AT_FDCWD, filepath.Join(root, restoreAttemptLockFile), false)
				defer unix.Close(fd)
				if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(filepath.Join(root, "backup/a"), filepath.Join(root, "backup/extra")); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(filepath.Join(root, "backup/fifo"), 0600); err != nil {
					t.Fatal(err)
				}
			case "writable_directory":
				if err := os.Chmod(filepath.Join(root, "backup/sub"), 0770); err != nil {
					t.Fatal(err)
				}
			case "root_writable":
				if err := os.Chmod(cfg.StateDir, 0777); err != nil {
					t.Fatal(err)
				}
			case "root_symlink":
				if err := os.Rename(cfg.StateDir, cfg.StateDir+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(cfg.StateDir+"-saved", cfg.StateDir); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Remove(cfg.StateDir); _ = os.Rename(cfg.StateDir+"-saved", cfg.StateDir) }()
			case "stale_file":
				_ = os.WriteFile(filepath.Join(root, "backup/a"), []byte("different bytes"), 0600)
			case "stale_manifest":
				_ = os.WriteFile(filepath.Join(root, "backup/.loom-direct-archive/manifest.json"), []byte("{}\n"), 0600)
			case "modified_failure":
				_ = os.WriteFile(filepath.Join(root, RestoreFailureReceiptFile), []byte("{}\n"), 0600)
			case "missing_preflight":
				_ = os.Remove(filepath.Join(root, RestoreCleanupPreflightFile))
			case "forged_legacy_success", "legacy_targets", "legacy_observation":
				file := RestoreCleanupLegacyFile
				if kind == "legacy_observation" {
					file = RestoreCleanupPreflightFile
				}
				b, _ := os.ReadFile(filepath.Join(root, file))
				b = bytes.ReplaceAll(b, []byte("\"failed\""), []byte("\"succeeded\""))
				if kind == "legacy_targets" {
					b = bytes.ReplaceAll(b, []byte("loom_restore_drill_cleanup"), []byte("loom_restore_drill_other"))
				}
				if kind == "legacy_observation" {
					b = bytes.ReplaceAll(b, []byte("both_absent"), []byte("not_started"))
				}
				_ = os.WriteFile(filepath.Join(root, file), b, 0600)
			case "legacy_without_adoption":
				_ = os.Remove(filepath.Join(root, RestoreCleanupLegacyFile))
			case "oversize_file":
				if err := os.Truncate(filepath.Join(root, "backup/a"), cleanupMaxBytes+1); err != nil {
					t.Fatal(err)
				}
			case "oversize_evidence":
				_ = os.WriteFile(filepath.Join(root, RestoreCleanupPreflightFile), bytes.Repeat([]byte("x"), 17<<10), 0600)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := applyRestoreCleanup(ctx, cfg, in, cleanupHooks{}); err == nil {
				t.Fatal("unsafe apply accepted")
			}
			if _, err := os.Lstat(filepath.Join(root, RestoreCleanupReceiptFile)); !os.IsNotExist(err) {
				t.Fatal("refusal wrote receipt before validation")
			}
		})
	}
}

func TestRestoreCleanupSelectorsAndConfiguredRoot(t *testing.T) {
	cfg, attempt, _ := cleanupFixture(t, false)
	for _, bad := range []string{"", ".", "..", "../" + attempt, "backup/" + attempt, "/" + attempt, "*", "foo?", "foo[1]", "foo\\bar", " " + attempt, strings.Repeat("x", 256)} {
		if _, err := planRestoreCleanup(context.Background(), cfg, bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
	if err != nil {
		t.Fatal(err)
	}
	other, otherAttempt, _ := cleanupFixture(t, false)
	if _, err := applyRestoreCleanup(context.Background(), other, RestoreCleanupApplyInput{Attempt: otherAttempt, ConfirmDigest: plan.PlanDigest, Yes: true}, cleanupHooks{}); err == nil {
		t.Fatal("cross-root plan accepted")
	}
}

func TestRestoreCleanupMutationBarriersAndPartialReplay(t *testing.T) {
	for _, point := range []string{"plan", "receipt", "prepared", "quarantine_pending", "quarantine_create", "quarantine_created", "move_pending", "quarantine_move", "moved", "seal_pending", "seal", "seal_applied", "sealed", "pending", "unlink", "removed", "payload_removed", "quarantine_remove_pending", "quarantine_remove", "quarantine_removed", "completed"} {
		t.Run(point, func(t *testing.T) {
			cfg, attempt, root := cleanupFixture(t, false)
			plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
			if err != nil {
				t.Fatal(err)
			}
			fired := false
			in := RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, Yes: true}
			result, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{beforeMutation: func(p string, index int) error {
				// prepared is represented by publication of the first receipt, before
				// any unlink. Other points inject failures around the append journal.
				if p == point || (point == "prepared" && p == "pending") {
					if !fired {
						fired = true
						return errors.New("injected owned fixture interruption")
					}
				}
				return nil
			}})
			if !fired || err == nil || result.Status == "completed" {
				t.Fatalf("point %s result=%+v err=%v", point, result, err)
			}
			if result.ConfirmedRemoved > plan.Entries {
				t.Fatal("overclaimed removal")
			}
			if _, err := os.Stat(filepath.Join(root, RestoreCleanupPlanFile)); err == nil {
				before, _ := os.ReadFile(filepath.Join(root, RestoreCleanupReceiptFile))
				replay, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{})
				if err == nil || replay.Status == "completed" {
					t.Fatal("partial replay became success")
				}
				after, _ := os.ReadFile(filepath.Join(root, RestoreCleanupReceiptFile))
				if !bytes.Equal(before, after) {
					t.Fatal("partial replay changed receipt")
				}
			}
		})
	}
	for _, replace := range []string{"backup", "attempt", "cloud", "leaf", "receipt", "plan", "failure", "chmod"} {
		t.Run("replace_"+replace, func(t *testing.T) {
			cfg, attempt, root := cleanupFixture(t, false)
			plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
			if err != nil {
				t.Fatal(err)
			}
			fired := false
			_, err = applyRestoreCleanup(context.Background(), cfg, RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, Yes: true}, cleanupHooks{beforeMutation: func(point string, index int) error {
				if point != "unlink" || index != 0 || fired {
					return nil
				}
				fired = true
				path := filepath.Join(root, cleanupQuarantineName, "backup")
				switch replace {
				case "attempt":
					path = root
				case "cloud":
					path = cfg.StateDir
				case "leaf":
					path = filepath.Join(root, cleanupQuarantineName, "backup/.loom-direct-archive/manifest.json")
				case "receipt":
					path = filepath.Join(root, RestoreCleanupReceiptFile)
				case "plan":
					path = filepath.Join(root, RestoreCleanupPlanFile)
				case "failure":
					path = filepath.Join(root, RestoreFailureReceiptFile)
				case "chmod":
					return os.Chmod(filepath.Join(root, cleanupQuarantineName, "backup"), 0777)
				}
				if err := os.Rename(path, path+"-saved"); err != nil {
					return err
				}
				return os.Symlink(path+"-saved", path)
			}})
			if !fired || err == nil {
				t.Fatal("observed replacement accepted", err)
			}
		})
	}
}

func TestRestoreCleanupFetchWriterCustody(t *testing.T) {
	cfg, _, root := cleanupFixture(t, false)
	alias := filepath.Join(filepath.Dir(cfg.StateDir), "alias")
	if err := os.Symlink(filepath.Join(root, "backup"), alias); err != nil {
		t.Fatal(err)
	}
	session, err := openCleanupSession(cfg, filepath.Base(root))
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	alternate := DefaultConfig()
	alternate.StateDir = filepath.Join(filepath.Dir(cfg.StateDir), "alternate")
	for _, dest := range []string{filepath.Join(root, "backup"), filepath.Join(root, "backup", "missing", "nested"), filepath.Join(alias, "missing")} {
		for _, entry := range []string{"public", "borg", "legacy"} {
			input := SnapshotFetchInput{Config: alternate, To: dest}
			var err error
			switch entry {
			case "public":
				_, err = FetchSnapshot(context.Background(), input)
			case "borg":
				_, err = (BorgSnapshotBackend{}).Fetch(context.Background(), input)
			case "legacy":
				_, err = (LegacyTreeSnapshotBackend{}).Fetch(context.Background(), input)
			}
			if err == nil || !strings.Contains(err.Error(), "marked restore attempt") {
				t.Fatalf("%s %s did not refuse at custody gate: %v", entry, dest, err)
			}
		}
	}
	// A moved attempt still carries markers regardless of its path components.
	session.close()
	session.anchors = nil
	session.lockFD = -1
	moved := filepath.Join(filepath.Dir(cfg.StateDir), "moved")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := guardRestoreFetchDestination(SnapshotFetchInput{To: filepath.Join(moved, "backup", "new")}); err == nil {
		t.Fatal("marked attempt bypass")
	}
	// Only a live lifecycle object authorizes the precise destination.
	live := filepath.Join(cfg.StateDir, "restore-drills", "fresh")
	if err := os.Mkdir(live, 0700); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(live, &st); err != nil {
		t.Fatal(err)
	}
	lock, err := startRestoreAttemptLifecycle(live, st)
	if err != nil {
		t.Fatal(err)
	}
	input := SnapshotFetchInput{To: filepath.Join(live, "backup"), restoreLifecycle: lock}
	if err := guardRestoreFetchDestination(input); err != nil {
		t.Fatal(err)
	}
	input.To = filepath.Join(live, "other")
	if err := guardRestoreFetchDestination(input); err == nil {
		t.Fatal("wrong destination authorized")
	}
	input.To = filepath.Join(live, "backup")
	lock.close()
	if err := guardRestoreFetchDestination(input); err == nil {
		t.Fatal("closed lifecycle authorized")
	}
}

func TestRestoreCleanupAdditionalBoundsAndReplay(t *testing.T) {
	for _, kind := range []string{"too_deep", "expired_preflight", "future_preflight", "torn_journal", "extra_journal", "tampered_plan"} {
		t.Run(kind, func(t *testing.T) {
			cfg, attempt, root := cleanupFixture(t, false)

			if kind == "too_deep" {
				p := filepath.Join(root, "backup")
				for n := 0; n <= cleanupMaxDepth; n++ {
					p = filepath.Join(p, "d")
					if err := os.Mkdir(p, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			if strings.HasSuffix(kind, "preflight") {
				b, err := os.ReadFile(filepath.Join(root, RestoreCleanupPreflightFile))
				if err != nil {
					t.Fatal(err)
				}
				var pre RestoreCleanupPreflight
				if err := cleanupDecode(b, &pre); err != nil {
					t.Fatal(err)
				}
				pre.Inactive.ObservedAt = time.Now().UTC().Add(-16 * time.Minute)
				if kind == "future_preflight" {
					pre.Inactive.ObservedAt = time.Now().UTC().Add(time.Minute)
				}
				if err := os.WriteFile(filepath.Join(root, RestoreCleanupPreflightFile), cleanupJSON(pre), 0600); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
			if !strings.Contains(kind, "journal") && kind != "tampered_plan" {
				if err == nil {
					t.Fatal("unsafe plan accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			in := RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, Yes: true}
			if _, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{}); err != nil {
				t.Fatal(err)
			}
			name := RestoreCleanupReceiptFile
			if kind == "tampered_plan" {
				name = RestoreCleanupPlanFile
			}
			path := filepath.Join(root, name)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "torn_journal" {
				b = b[:len(b)-2]
			} else {
				b = append(b, []byte("{}\n")...)
			}
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{}); err == nil {
				t.Fatal("ambiguous replay accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(b, after) {
				t.Fatal("refusal rewrote evidence")
			}
		})
	}
	// Readdir must reject a truncated inventory rather than return its prefix.
	root := cleanupGapFixture(t)
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fd, err := cleanupOpenDir(unix.AT_FDCWD, root)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if _, err := cleanupNames(fd, 1); err == nil {
		t.Fatal("truncated inventory accepted")
	}
}

type cleanupAliasDriver struct {
	recordingSnapshotDriver
	switchAlias func()
	destination string
}

func (d *cleanupAliasDriver) List(context.Context, string) ([]RemoteEntry, error) {
	d.switchAlias()
	return []RemoteEntry{{Path: "fixture", IsDir: true}}, nil
}
func (d *cleanupAliasDriver) CopyFromRemote(_ context.Context, _, local string, _ CopyOptions) (CopyResult, error) {
	d.destination = local
	return CopyResult{}, errors.New("fixture stopped after destination observation")
}

func TestRestoreCleanupFetchAliasSwitchDuringResolution(t *testing.T) {
	for _, backend := range []string{"borg", "legacy"} {
		t.Run(backend, func(t *testing.T) {
			cfg, attempt, protected := cleanupFixture(t, false)
			session, err := openCleanupSession(cfg, attempt)
			if err != nil {
				t.Fatal(err)
			}
			defer session.close()
			ordinary := filepath.Join(filepath.Dir(cfg.StateDir), "ordinary", "restore-drills", "manual")
			if err := os.MkdirAll(ordinary, 0700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(filepath.Dir(cfg.StateDir), "outside")
			if err := os.Mkdir(outside, 0777); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(outside, 0777); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(outside, "alias")
			if err := os.Symlink(ordinary, alias); err != nil {
				t.Fatal(err)
			}
			switched := false
			switchAlias := func() {
				if switched {
					return
				}
				switched = true
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(protected, "backup"), alias); err != nil {
					t.Fatal(err)
				}
			}
			input := SnapshotFetchInput{Config: cfg, Ref: "latest", To: filepath.Join(alias, "new")}
			observed := ""
			if backend == "borg" {
				// Use the existing bounded synthetic backend configuration, no subprocess.
				fixture := newDirectArchiveCloudFixture(t)
				input.Config = fixture.cfg
				b := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, DisableRemoteLock: true, Exec: func(_ context.Context, _ string, command BorgCommand, _ []string) ([]byte, error) {
					if command.Name() == "list" {
						switchAlias()
						return []byte(`{"archives":[{"name":"main-fixture","time":"2026-09-06T10:00:00.000000"}]}`), nil
					}
					if command.Name() == "extract" {
						observed = command.Dir
						return nil, errors.New("fixture stopped after destination observation")
					}
					t.Fatalf("unexpected command %s", command.Name())
					return nil, nil
				}}}
				_, err = b.Fetch(context.Background(), input)
			} else {
				d := &cleanupAliasDriver{switchAlias: switchAlias}
				input.Driver = d
				_, err = (LegacyTreeSnapshotBackend{Driver: d}).Fetch(context.Background(), input)
				observed = d.destination
			}
			if err == nil || !switched || observed != filepath.Join(ordinary, "new") {
				t.Fatalf("alias custody %s switched=%t destination=%s error=%v", backend, switched, observed, err)
			}
			if _, err := os.Lstat(filepath.Join(protected, "backup", "new")); !os.IsNotExist(err) {
				t.Fatal("standalone fetch wrote protected attempt")
			}
			if _, err := os.Stat(filepath.Join(ordinary, "new")); err != nil {
				t.Fatal("fixture did not reach destination mutation")
			}
		})
	}
}

// Public documentation uses <cloud-state>/restore-drills/manual/backup. The
// directory name alone does not identify a lifecycle-owned restore attempt.
func TestRestoreCleanupFetchUnmarkedDestinations(t *testing.T) {
	for _, manual := range []bool{true, false} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("manual=%t/existing=%t", manual, existing), func(t *testing.T) {
				cfg, _, _ := cleanupFixture(t, false)
				dest := filepath.Join(cfg.StateDir, "restore-drills", "manual", "backup")
				if !manual {
					dest = filepath.Join(filepath.Dir(cfg.StateDir), "unrelated", "restore-drills", "ordinary", "backup")
				}
				parent := filepath.Dir(dest)
				if existing {
					parent = dest
				}
				if err := os.MkdirAll(parent, 0700); err != nil {
					t.Fatal(err)
				}
				driver := &cleanupAliasDriver{switchAlias: func() {}}
				cfg.Snapshots.Backend = SnapshotBackendLegacyTree
				_, err := FetchSnapshot(context.Background(), SnapshotFetchInput{Config: cfg, Driver: driver, Ref: "latest", To: dest})
				if err == nil || err.Error() != "fixture stopped after destination observation" || driver.destination != dest {
					t.Fatalf("supported destination did not reach fetch: destination=%q error=%v", driver.destination, err)
				}
				if info, err := os.Stat(dest); err != nil || !info.IsDir() {
					t.Fatal("fetch did not prepare ordinary destination", err)
				}
				for _, marker := range []string{restoreAttemptLockFile, RestoreFailureReceiptFile, RestoreCleanupPreflightFile, RestoreCleanupPlanFile, RestoreCleanupReceiptFile, RestoreCleanupLegacyFile} {
					if _, err := os.Lstat(filepath.Join(filepath.Dir(dest), marker)); !os.IsNotExist(err) {
						t.Fatal("ordinary fetch manufactured attempt marker", marker, err)
					}
				}
			})
		}
	}
}

func TestRestoreCleanupFetchMarkedAncestors(t *testing.T) {
	for _, marker := range []string{restoreAttemptLockFile, RestoreFailureReceiptFile, RestoreCleanupLegacyFile, RestoreCleanupPreflightFile, RestoreCleanupPlanFile, RestoreCleanupReceiptFile} {
		t.Run(marker, func(t *testing.T) {
			cfg, _, _ := cleanupFixture(t, false)
			// No restore-drills component: only the actual control entry identifies
			// this ancestor as a protected attempt, even under a different config.
			protected := filepath.Join(filepath.Dir(cfg.StateDir), "marked")
			if err := os.Mkdir(protected, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(protected, marker), []byte("marker fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(filepath.Dir(cfg.StateDir), "marked-alias")
			if err := os.Symlink(protected, alias); err != nil {
				t.Fatal(err)
			}
			alternate := cfg
			alternate.StateDir = filepath.Join(filepath.Dir(cfg.StateDir), "alternate")
			for _, config := range []Config{cfg, alternate} {
				for _, path := range []string{protected, alias} {
					for _, entry := range []string{"public", "borg", "legacy"} {
						input := SnapshotFetchInput{Config: config, To: filepath.Join(path, "missing", "backup")}
						var err error
						switch entry {
						case "public":
							_, err = FetchSnapshot(context.Background(), input)
						case "borg":
							_, err = (BorgSnapshotBackend{}).Fetch(context.Background(), input)
						case "legacy":
							_, err = (LegacyTreeSnapshotBackend{}).Fetch(context.Background(), input)
						}
						if err == nil || !strings.Contains(err.Error(), "marked restore attempt") {
							t.Fatalf("%s marker bypass through %s: %v", entry, path, err)
						}
					}
				}
			}
			if _, err := os.Lstat(filepath.Join(protected, "missing")); !os.IsNotExist(err) {
				t.Fatal("refused fetch wrote protected destination")
			}
		})
	}
}

type cleanupACLTestEntry struct {
	tag, perm uint16
	id        uint32
}

func cleanupACLTestBytes(entries ...cleanupACLTestEntry) []byte {
	raw := make([]byte, 4+8*len(entries))
	binary.LittleEndian.PutUint32(raw, 2)
	for n, e := range entries {
		b := raw[4+n*8:]
		binary.LittleEndian.PutUint16(b, e.tag)
		binary.LittleEndian.PutUint16(b[2:], e.perm)
		binary.LittleEndian.PutUint32(b[4:], e.id)
	}
	return raw
}
func cleanupACLTestGrant(owner, named, group, mask, other uint16) []byte {
	return cleanupACLTestBytes(cleanupACLTestEntry{1, owner, 0xffffffff}, cleanupACLTestEntry{2, named, 1001}, cleanupACLTestEntry{4, group, 0xffffffff}, cleanupACLTestEntry{16, mask, 0xffffffff}, cleanupACLTestEntry{32, other, 0xffffffff})
}
func TestRestoreCleanupPOSIXACLDecoder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		raw        []byte
		defaults   bool
		mode       uint32
		write, bad bool
	}{
		{"directory_read_traverse", cleanupACLTestGrant(7, 5, 5, 5, 0), false, 0750, false, false},
		{"manifest_0650", cleanupACLTestGrant(6, 5, 5, 5, 0), false, 0650, false, false},
		{"payload_0674", cleanupACLTestGrant(6, 7, 7, 7, 4), false, 0674, true, false},
		{"masked_raw_write", cleanupACLTestGrant(7, 7, 7, 5, 0), false, 0750, false, false},
		{"mask_write_without_grant", cleanupACLTestGrant(7, 5, 5, 7, 0), false, 0770, false, false},
		{"other_write_unmasked", cleanupACLTestGrant(7, 0, 0, 0, 2), false, 0702, true, false},
		{"default_read_traverse", cleanupACLTestGrant(7, 5, 5, 5, 0), true, 0750, false, false},
		{"default_independent_write", cleanupACLTestGrant(7, 7, 0, 7, 0), true, 0770, true, false},
		{"default_own_mask", cleanupACLTestGrant(7, 7, 7, 5, 0), true, 0750, false, false},
		{"private_control_mask", cleanupACLTestGrant(6, 5, 5, 0, 0), false, 0600, false, false},
		{"named_owner_preempted", cleanupACLTestGrant(6, 7, 0, 7, 0), false, 0670, false, false},
		{"default_named_not_future_owner", cleanupACLTestGrant(6, 7, 0, 7, 0), true, 0670, true, false},
		{"empty_default", cleanupACLTestBytes(), true, 0, false, false},
		{"empty_access", cleanupACLTestBytes(), false, 0, false, true},
		{"missing_mask", cleanupACLTestBytes(cleanupACLTestEntry{1, 7, 0xffffffff}, cleanupACLTestEntry{2, 5, 1001}, cleanupACLTestEntry{4, 5, 0xffffffff}, cleanupACLTestEntry{32, 0, 0xffffffff}), false, 0, false, true},
		{"unknown_tag", cleanupACLTestBytes(cleanupACLTestEntry{128, 7, 0xffffffff}), false, 0, false, true},
		{"invalid_permission", cleanupACLTestGrant(8, 5, 5, 5, 0), false, 0, false, true},
		{"base_order", cleanupACLTestBytes(cleanupACLTestEntry{4, 5, 0xffffffff}, cleanupACLTestEntry{1, 7, 0xffffffff}, cleanupACLTestEntry{32, 0, 0xffffffff}), false, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := uint32(1000)
			if strings.Contains(tc.name, "named_owner") || tc.name == "default_named_not_future_owner" {
				owner = 1001
			}
			got, err := cleanupParsePOSIXACL(tc.raw, owner, tc.defaults)
			if (err != nil) != tc.bad || (!tc.bad && (got.mode != tc.mode || got.nonOwnerWrite != tc.write)) {
				t.Fatalf("got %+v %v", got, err)
			}
		})
	}
	good := cleanupACLTestGrant(7, 5, 5, 5, 0)
	for _, kind := range []string{"version", "truncated", "oversized", "base_id", "named_undefined", "duplicate_user", "unsorted_user", "duplicate_group", "duplicate_mask", "trailing_entry"} {
		t.Run(kind, func(t *testing.T) {
			raw := append([]byte(nil), good...)
			switch kind {
			case "version":
				raw[0] = 3
			case "truncated":
				raw = raw[:len(raw)-1]
			case "oversized":
				raw = make([]byte, 65540)
			case "base_id":
				binary.LittleEndian.PutUint32(raw[8:], 1001)
			case "named_undefined":
				binary.LittleEndian.PutUint32(raw[16:], 0xffffffff)
			case "duplicate_user", "unsorted_user":
				extra := append([]byte(nil), raw[12:20]...)
				if kind == "unsorted_user" {
					binary.LittleEndian.PutUint32(extra[4:], 999)
				}
				raw = append(append(append([]byte(nil), raw[:20]...), extra...), raw[20:]...)
			case "duplicate_group":
				e := cleanupACLTestEntry{8, 5, 1002}
				raw = cleanupACLTestBytes(cleanupACLTestEntry{1, 7, 0xffffffff}, cleanupACLTestEntry{4, 5, 0xffffffff}, e, e, cleanupACLTestEntry{16, 5, 0xffffffff}, cleanupACLTestEntry{32, 0, 0xffffffff})
			case "duplicate_mask":
				raw = append(append(append([]byte(nil), raw[:36]...), raw[28:36]...), raw[36:]...)
			case "trailing_entry":
				raw = append(raw, raw[4:12]...)
			}
			if _, err := cleanupParsePOSIXACL(raw, 1000, false); err == nil {
				t.Fatal("malformed ACL accepted")
			}
		})
	}
}

func TestRestoreCleanupRetainedExtractModes(t *testing.T) {
	cfg, attempt, root := cleanupFixture(t, true)
	for _, path := range []string{root, filepath.Join(root, "backup"), filepath.Join(root, "backup/sub"), filepath.Join(root, "backup/.loom-direct-archive")} {
		if err := os.Chmod(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	manifest := filepath.Join(root, "backup/.loom-direct-archive/manifest.json")
	if err := os.Chmod(manifest, 0650); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "backup/a")
	if err := os.Chmod(payload, 0674); err != nil {
		t.Fatal(err)
	}
	// An open descriptor survives namespace removal; cleanup makes no secure
	// erasure or stable-content promise to other holders of the same inode.
	held, err := os.Open(payload)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyRestoreCleanup(context.Background(), cfg, RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, Yes: true}, cleanupHooks{})
	if err != nil || result.Status != "completed" {
		t.Fatalf("observed modes: %+v %v", result, err)
	}
	b := make([]byte, 32)
	if n, err := held.ReadAt(b, 0); n == 0 || err != nil && err != io.EOF {
		t.Fatal("held inode unexpectedly unavailable", err)
	}
	for _, name := range []string{RestoreCleanupPlanFile, RestoreCleanupReceiptFile} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("private control mode", err)
		}
	}
}

func TestRestoreCleanupPayloadEvidenceRefusals(t *testing.T) {
	for _, kind := range []string{"write_mode", "hardlink", "symlink", "fifo", "oversize", "wrong_owner", "wrong_type", "substitution"} {
		t.Run(kind, func(t *testing.T) {
			cfg, attempt, root := cleanupFixture(t, false)
			manifest := filepath.Join(root, "backup/.loom-direct-archive/manifest.json")
			switch kind {
			case "write_mode":
				if err := os.Chmod(manifest, 0660); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(manifest, manifest+"-link"); err != nil {
					t.Fatal(err)
				}
			case "symlink", "fifo":
				if err := os.Rename(manifest, manifest+"-saved"); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(manifest+"-saved", manifest); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := unix.Mkfifo(manifest, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "oversize":
				if err := os.Truncate(manifest, 8<<20+1); err != nil {
					t.Fatal(err)
				}
			case "wrong_owner", "wrong_type", "substitution":
				parent, err := cleanupOpenDir(unix.AT_FDCWD, filepath.Dir(manifest))
				if err != nil {
					t.Fatal(err)
				}
				defer unix.Close(parent)
				blob, fd, err := cleanupReadPayloadEvidence(parent, filepath.Base(manifest), 8<<20)
				if err != nil {
					t.Fatal(err)
				}
				defer unix.Close(fd)
				if kind == "substitution" {
					if err := os.Rename(manifest, manifest+"-saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(manifest, blob.Bytes, 0650); err != nil {
						t.Fatal(err)
					}
					if err := cleanupCheckEvidence(parent, fd, blob, false); err == nil {
						t.Fatal("substitution accepted")
					}
					return
				}
				identity := blob.Identity
				if kind == "wrong_owner" {
					identity.UID++
				} else {
					identity.Mode = unix.S_IFDIR | 0650
				}
				if err := cleanupEvidenceAuthority(fd, identity, false); err == nil {
					t.Fatal("invalid evidence authority accepted")
				}
				return
			}
			if _, err := planRestoreCleanup(context.Background(), cfg, attempt); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
			if _, err := os.Stat(filepath.Join(root, "backup/a")); err != nil {
				t.Fatal("refusal changed payload")
			}
		})
	}
}

func TestRestoreCleanupQuarantineSeals(t *testing.T) {
	for _, mode := range []os.FileMode{0575, 0775} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			cfg, attempt, root := cleanupFixture(t, true)
			sub := filepath.Join(root, "backup/sub")
			if err := os.Chmod(sub, mode); err != nil {
				t.Fatal(err)
			}
			// Fixture-only restoration allows the testing harness to remove an aborted fixture.
			t.Cleanup(func() {
				for _, p := range []string{sub, filepath.Join(root, cleanupQuarantineName, "backup/sub")} {
					_ = os.Chmod(p, 0700)
				}
			})
			fd, err := unix.Open(sub, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			before, err := cleanupStat(fd)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
			if err != nil {
				t.Fatal(err)
			}
			in := RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, DryRun: true}
			if _, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{}); err != nil {
				t.Fatal(err)
			}
			now, _ := cleanupStat(fd)
			if now != before {
				t.Fatal("dry-run sealed payload")
			}
			if err := cleanupTestAbsent(root, cleanupQuarantineName); err != nil {
				t.Fatal(err)
			}
			in.DryRun = false
			in.Yes = true
			sawSeal, sawUnlink := false, false
			result, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{beforeMutation: func(point string, index int) error {
				if point == "seal" {
					sawSeal = true
					if err := cleanupTestAbsent(root, "backup"); err != nil {
						return err
					}
					info, e := os.Stat(filepath.Join(root, cleanupQuarantineName))
					if e != nil || info.Mode().Perm() != 0700 {
						return fmt.Errorf("seal outside private quarantine")
					}
					raw, e := os.ReadFile(filepath.Join(root, RestoreCleanupReceiptFile))
					if e != nil || !bytes.Contains(raw, []byte(`"status":"seal_pending"`)) {
						return fmt.Errorf("seal has no durable intent")
					}
				}
				if point == "unlink" {
					sawUnlink = true
					raw, e := os.ReadFile(filepath.Join(root, RestoreCleanupReceiptFile))
					if e != nil || !bytes.Contains(raw, []byte(`"status":"sealed"`)) {
						return fmt.Errorf("unlink before seal acknowledgement")
					}
				}
				return nil
			}})
			if err != nil || result.Status != "completed" || !sawSeal || !sawUnlink {
				t.Fatalf("seal result %+v %v", result, err)
			}
			after, _ := cleanupStat(fd)
			if after.Mode&07777 != 0755 || after.Inode != before.Inode || after.UID != before.UID || after.GID != before.GID {
				t.Fatal("unexpected seal identity", after)
			}
			if err := cleanupTestAbsent(root, "backup"); err != nil {
				t.Fatal(err)
			}
			if err := cleanupTestAbsent(root, cleanupQuarantineName); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(root, RestoreCleanupPlanFile))
			if err != nil {
				t.Fatal(err)
			}
			var stored cleanupPlan
			if err := cleanupDecode(raw, &stored); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range stored.Entries {
				if entry.Path == "backup/sub" {
					found = true
					if entry.Identity != before {
						t.Fatal("original directory metadata lost")
					}
				}
			}
			if !found {
				t.Fatal("missing original directory")
			}
			replay, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{})
			if err != nil || replay.Status != "completed" {
				t.Fatal("sealed replay", err)
			}
		})
	}
}
func cleanupTestAbsent(root, name string) error {
	if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("unexpected entry %s", name)
	}
	return nil
}
func TestRestoreCleanupQuarantineHostility(t *testing.T) {
	for _, kind := range []string{"precreated_directory", "precreated_symlink", "rename_collision", "quarantine_substitution", "open_fd_write", "special_bits", "stale_seal", "quarantine_extra", "payload_recreated", "completed_recreated"} {
		t.Run(kind, func(t *testing.T) {
			cfg, attempt, root := cleanupFixture(t, false)
			q := filepath.Join(root, cleanupQuarantineName)
			if kind == "precreated_directory" {
				if err := os.Mkdir(q, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "precreated_symlink" {
				if err := os.Symlink("backup", q); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "special_bits" {
				if err := unix.Chmod(filepath.Join(root, "backup/sub"), 02775); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := planRestoreCleanup(context.Background(), cfg, attempt)
			if strings.HasPrefix(kind, "precreated") || kind == "special_bits" {
				if err == nil {
					t.Fatal("unsealable/precreated fixture planned")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			held, err := os.Open(filepath.Join(root, "backup/sub"))
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			fired := false
			in := RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: plan.PlanDigest, Yes: true}
			result, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{beforeMutation: func(point string, index int) error {
				if fired {
					return nil
				}
				switch kind {
				case "rename_collision":
					if point == "quarantine_move" {
						fired = true
						return os.Mkdir(filepath.Join(q, "backup"), 0700)
					}
				case "quarantine_substitution":
					if point == "moved" {
						fired = true
						if e := os.Rename(q, q+"-saved"); e != nil {
							return e
						}
						return os.Symlink(q+"-saved", q)
					}
				case "open_fd_write":
					if point == "moved" {
						fired = true
						fd, e := unix.Openat(int(held.Fd()), "added", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
						if e == nil {
							unix.Close(fd)
						}
						return e
					}
				case "stale_seal":
					if point == "seal" && index > 0 {
						fired = true
						return os.WriteFile(filepath.Join(q, "backup/sub/changed"), []byte("fixture"), 0600)
					}
				case "payload_recreated":
					if point == "payload_removed" {
						fired = true
						return os.Mkdir(filepath.Join(q, "backup"), 0700)
					}
				case "quarantine_extra":
					if point == "quarantine_remove" {
						fired = true
						return os.WriteFile(filepath.Join(q, "unexpected"), []byte("fixture"), 0600)
					}
				}
				return nil
			}})
			if kind == "completed_recreated" {
				if err != nil || result.Status != "completed" {
					t.Fatal(err)
				}
				fired = true
				if err := os.Mkdir(q, 0700); err != nil {
					t.Fatal(err)
				}
			} else if !fired || err == nil || result.Status == "completed" {
				t.Fatal("hostility accepted", kind, err)
			}
			if kind == "rename_collision" {
				if _, e := os.Stat(filepath.Join(root, "backup/sub/b")); e != nil {
					t.Fatal("no-replace source lost")
				}
			}
			if kind == "open_fd_write" || kind == "stale_seal" {
				if result.ConfirmedRemoved != 0 {
					t.Fatal("deleted before full sealed inventory")
				}
			}
			before, _ := os.ReadFile(filepath.Join(root, RestoreCleanupReceiptFile))
			if kind == "payload_recreated" && bytes.Contains(before, []byte(`"status":"payload_removed"`)) {
				t.Fatal("inaccurate payload absence acknowledgement")
			}
			replay, e := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{})
			if e == nil || replay.Status == "completed" {
				t.Fatal("ambiguous replay accepted")
			}
			after, _ := os.ReadFile(filepath.Join(root, RestoreCleanupReceiptFile))
			if !bytes.Equal(before, after) {
				t.Fatal("replay rewrote receipt")
			}
		})
	}
}
func TestRestoreCleanupQuarantineRenameNoReplace(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "source"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "target"), 0700); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := cleanupRenameNoReplace(fd, "source", fd, "target"); err == nil {
		t.Fatal("no-replace overwrote an existing empty directory")
	}
	for _, name := range []string{"source", "target"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
}

// A tiny inode with the production-observed shape; every byte/name is synthetic.
func cleanupHardlinkFixture(t *testing.T, count int) (Config, string, string, []string) {
	t.Helper()
	cfg, attempt, root := cleanupFixture(t, false)
	names := []string{"backup/a"}
	if err := os.WriteFile(filepath.Join(root, names[0]), bytes.Repeat([]byte("h"), 118), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, names[0]), 0640); err != nil {
		t.Fatal(err)
	}
	for n := 1; n < count; n++ {
		name := fmt.Sprintf("backup/sub/alias-%d", n)
		if err := os.Link(filepath.Join(root, names[0]), filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return cfg, attempt, root, names
}

func cleanupHardlinkReviewed(t *testing.T, cfg Config, attempt string) (RestoreCleanupApplyInput, cleanupPlan) {
	t.Helper()
	s, err := openCleanupSession(cfg, attempt)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	p, err := s.buildPlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return RestoreCleanupApplyInput{Attempt: attempt, ConfirmDigest: cleanupDigest(cleanupJSON(p)), Yes: true}, p
}

func cleanupHardlinkJournal(t *testing.T, root string) ([]byte, []cleanupJournal) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, RestoreCleanupReceiptFile))
	if err != nil {
		t.Fatal(err)
	}
	var events []cleanupJournal
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var event cleanupJournal
		if err := cleanupDecode(line, &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return raw, events
}

func TestRestoreCleanupHardlinkComplete(t *testing.T) {
	for _, count := range []int{2, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			cfg, attempt, root, names := cleanupHardlinkFixture(t, count)
			in, p := cleanupHardlinkReviewed(t, cfg, attempt)
			held := cleanupGapOpen(t, unix.AT_FDCWD, filepath.Join(root, names[0]), false)
			defer unix.Close(held)
			original, err := cleanupStat(held)
			if err != nil || original.Links != uint64(count) || original.Size != 118 || original.Mode&0777 != 0640 {
				t.Fatalf("fixture identity: %+v %v", original, err)
			}
			dry := in
			dry.DryRun = true
			if result, err := applyRestoreCleanup(context.Background(), cfg, dry, cleanupHooks{}); err != nil || result.ConfirmedRemoved != 0 {
				t.Fatalf("dry run: %+v %v", result, err)
			}
			observed := 0
			result, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{beforeMutation: func(point string, index int) error {
				if point == "hardlink_unlinked" {
					observed++
					got, err := cleanupStat(held)
					if err != nil || got.Links != uint64(count-observed) {
						return fmt.Errorf("actual held link count: %+v %v", got, err)
					}
				}
				return nil
			}})
			if err != nil || result.Status != "completed" || result.ConfirmedRemoved != len(p.Entries) || observed != count || cleanupGapStat(t, held).Nlink != 0 {
				t.Fatalf("completed hardlink truth: %+v observed=%d %v", result, observed, err)
			}
			raw, events := cleanupHardlinkJournal(t, root)
			remaining := uint64(count)
			current := original
			for _, e := range events {
				if e.Status != "removed" || e.Entry == nil {
					continue
				}
				if e.After == nil || e.Entry.Identity != current || e.Entry.Identity.Links != remaining || !cleanupHardlinkTransition(*e.Entry, *e.After) {
					t.Fatalf("invalid actual transition: %+v", e)
				}
				remaining--
				current = e.After.Identity
			}
			if remaining != 0 {
				t.Fatal("missing final zero-link observation")
			}
			stored, err := os.ReadFile(filepath.Join(root, RestoreCleanupPlanFile))
			if err != nil || !bytes.Equal(stored, cleanupJSON(p)) {
				t.Fatal("original reviewed group changed")
			}
			replay, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{})
			after, _ := cleanupHardlinkJournal(t, root)
			if err != nil || replay.Status != "completed" || !bytes.Equal(raw, after) {
				t.Fatalf("exact completed replay: %+v %v", replay, err)
			}
		})
	}
}

func TestRestoreCleanupHardlinkAccountingRefusals(t *testing.T) {
	cfg, attempt, _, _ := cleanupHardlinkFixture(t, 2)
	_, plan := cleanupHardlinkReviewed(t, cfg, attempt)
	var pair []cleanupEntry
	for _, e := range plan.Entries {
		if e.Identity.Mode&unix.S_IFMT == unix.S_IFREG && e.Identity.Links == 2 {
			pair = append(pair, e)
		}
	}
	for _, kind := range []string{"missing", "extra", "duplicate", "outside", "unclean", "zero", "links", "device", "inode", "mount", "mode", "uid", "gid", "size", "mtime", "ctime", "acl", "link"} {
		t.Run(kind, func(t *testing.T) {
			entries := append([]cleanupEntry(nil), pair...)
			e := &entries[1]
			switch kind {
			case "missing":
				entries = entries[:1]
			case "extra":
				extra := entries[0]
				extra.Path = "backup/extra"
				entries = append(entries, extra)
			case "duplicate":
				e.Path = entries[0].Path
			case "outside":
				e.Path = "sibling"
			case "unclean":
				e.Path = "backup/sub/../alias"
			case "zero":
				e.Identity.Links = 0
			case "links":
				e.Identity.Links++
			case "device":
				e.Identity.Device++
			case "inode":
				e.Identity.Inode++
			case "mount":
				e.Identity.Mount += "other"
			case "mode":
				e.Identity.Mode ^= 0040
			case "uid":
				e.Identity.UID++
			case "gid":
				e.Identity.GID++
			case "size":
				e.Identity.Size++
			case "mtime":
				e.Identity.MTime++
			case "ctime":
				e.Identity.CTime++
			case "acl":
				e.ACL = &cleanupACLState{}
			case "link":
				e.Link = "target"
			}
			if _, err := cleanupHardlinkGroups(entries); err == nil {
				t.Fatal("incomplete/inconsistent group accepted")
			}
		})
	}
	var huge []cleanupEntry
	for n := 0; n < 1000; n++ {
		e := pair[0]
		e.Path, e.Identity.Links = fmt.Sprintf("backup/alias-%d", n), 1000
		huge = append(huge, e)
	}
	if _, err := cleanupHardlinkGroups(huge); err == nil || !strings.Contains(err.Error(), "work bound") {
		t.Fatal("quadratic alias work not preflight bounded", err)
	}
}

func TestRestoreCleanupHardlinkMutationRefusals(t *testing.T) {
	for _, phase := range []string{"before_plan", "after_plan", "moved", "sealed", "unlink", "hardlink_unlinked", "removed"} {
		for _, mutation := range []string{"external", "remove", "substitute", "write"} {
			t.Run(phase+"/"+mutation, func(t *testing.T) {
				cfg, attempt, root, names := cleanupHardlinkFixture(t, 2)
				held := cleanupGapOpen(t, unix.AT_FDCWD, filepath.Join(root, names[0]), false)
				defer unix.Close(held)
				mutate := func(moved bool) error {
					base := root
					if moved {
						base = filepath.Join(root, cleanupQuarantineName)
					}
					path := filepath.Join(base, names[0]) // Last alias survives the first deepest unlink.
					switch mutation {
					case "external":
						return os.Link(path, filepath.Join(root, "external-alias"))
					case "remove":
						return os.Remove(path)
					case "substitute":
						if err := os.Remove(path); err != nil {
							return err
						}
						return os.WriteFile(path, []byte("replacement"), 0600)
					case "write":
						return os.WriteFile(path, []byte("changed"), 0640)
					}
					return nil
				}
				if phase == "before_plan" {
					// A fully new inventory can accept ordinary changed content or a
					// now-single-linked tree. Only unaccounted names must refuse here.
					if mutation != "external" {
						return
					}
					if err := mutate(false); err != nil {
						t.Fatal(err)
					}
					if _, err := planRestoreCleanup(context.Background(), cfg, attempt); err == nil {
						t.Fatal("external link accepted")
					}
					for _, name := range []string{RestoreCleanupPlanFile, RestoreCleanupReceiptFile, cleanupQuarantineName} {
						if err := cleanupTestAbsent(root, name); err != nil {
							t.Fatal(err)
						}
					}
					return
				}
				in, p := cleanupHardlinkReviewed(t, cfg, attempt)
				first := -1
				for index, e := range cleanupRemovalOrder(p.Entries) {
					if e.Identity.Mode&unix.S_IFMT == unix.S_IFREG && e.Identity.Links == 2 {
						first = index
						break
					}
				}
				fired := false
				if phase == "after_plan" {
					if err := mutate(false); err != nil {
						t.Fatal(err)
					}
					fired = true
				}
				result, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{beforeMutation: func(point string, index int) error {
					if !fired && point == phase && (index == -1 || index == first) {
						fired = true
						if err := mutate(true); err != nil {
							t.Fatal(err)
						}
					}
					return nil
				}})
				if !fired || err == nil || result.Status == "completed" {
					t.Fatalf("mutation accepted: %+v %v fired=%v", result, err, fired)
				}
				if phase == "after_plan" {
					if err := cleanupTestAbsent(root, cleanupQuarantineName); err != nil {
						t.Fatal(err)
					}
					if err := cleanupTestAbsent(root, RestoreCleanupReceiptFile); err != nil {
						t.Fatal(err)
					}
					return
				}
				want := 0
				if phase == "unlink" || phase == "hardlink_unlinked" || phase == "removed" {
					want = first
				}
				if result.ConfirmedRemoved != want {
					t.Fatalf("unobserved removal acknowledged: got %d want %d", result.ConfirmedRemoved, want)
				}
				raw, events := cleanupHardlinkJournal(t, root)
				for _, e := range events {
					if e.Status == "removed" && e.Index == first {
						t.Fatal("changed group acknowledged")
					}
				}
				replay, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{})
				after, _ := cleanupHardlinkJournal(t, root)
				if err == nil || replay.Status == "completed" || !bytes.Equal(raw, after) {
					t.Fatal("partial journal resumed or changed")
				}
			})
		}
	}
}

func TestRestoreCleanupHardlinkInterruptedAndTamperedJournal(t *testing.T) {
	for _, phase := range []string{"unlink", "hardlink_unlinked", "removed"} {
		for _, member := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/%d", phase, member), func(t *testing.T) {
				cfg, attempt, root, names := cleanupHardlinkFixture(t, 2)
				in, p := cleanupHardlinkReviewed(t, cfg, attempt)
				held := cleanupGapOpen(t, unix.AT_FDCWD, filepath.Join(root, names[0]), false)
				defer unix.Close(held)
				var indices []int
				for index, e := range cleanupRemovalOrder(p.Entries) {
					if e.Identity.Mode&unix.S_IFMT == unix.S_IFREG && e.Identity.Links == 2 {
						indices = append(indices, index)
					}
				}
				fired := false
				result, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{beforeMutation: func(point string, index int) error {
					if point == phase && index == indices[member] {
						fired = true
						return errors.New("owned interruption")
					}
					return nil
				}})
				wantLinks := 2 - member
				if phase != "unlink" {
					wantLinks--
				}
				if !fired || err == nil || result.ConfirmedRemoved != indices[member] || uint64(cleanupGapStat(t, held).Nlink) != uint64(wantLinks) {
					t.Fatalf("false partial truth: %+v %v", result, err)
				}
				raw, events := cleanupHardlinkJournal(t, root)
				confirmed := 0
				for _, e := range events {
					if e.Status == "removed" {
						confirmed++
					}
				}
				if confirmed != result.ConfirmedRemoved {
					t.Fatal("journal/result removal disagreement")
				}
				replay, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{})
				after, _ := cleanupHardlinkJournal(t, root)
				if err == nil || replay.Status == "completed" || !bytes.Equal(raw, after) {
					t.Fatal("interrupted group resumed")
				}
			})
		}
	}
	for _, kind := range []string{"missing_after", "wrong_count", "wrong_before", "wrong_path", "broken_ctime_chain"} {
		t.Run(kind, func(t *testing.T) {
			cfg, attempt, root, _ := cleanupHardlinkFixture(t, 2)
			in, _ := cleanupHardlinkReviewed(t, cfg, attempt)
			if _, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{}); err != nil {
				t.Fatal(err)
			}
			_, events := cleanupHardlinkJournal(t, root)
			for n := range events {
				e := &events[n]
				if e.Status != "removed" || e.Entry == nil {
					continue
				}
				switch kind {
				case "missing_after":
					e.After = nil
				case "wrong_count":
					e.After.Identity.Links++
				case "wrong_before":
					e.Entry.Identity.Links++
				case "wrong_path":
					e.After.Path = "backup/outside"
				case "broken_ctime_chain":
					e.After.Identity.CTime++
				}
				break
			}
			var raw []byte
			for _, e := range events {
				raw = append(raw, cleanupJSON(e)...)
			}
			if err := os.WriteFile(filepath.Join(root, RestoreCleanupReceiptFile), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if result, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{}); err == nil || result.Status == "completed" {
				t.Fatal("forged group transition accepted")
			}
			after, _ := cleanupHardlinkJournal(t, root)
			if !bytes.Equal(raw, after) {
				t.Fatal("tampered journal rewritten")
			}
		})
	}
}

func TestRestoreCleanupHardlinkFinalAcknowledgementMutation(t *testing.T) {
	for _, kind := range []string{"unlinked_open_fd", "removed_open_fd", "removed_name_recreated"} {
		t.Run(kind, func(t *testing.T) {
			cfg, attempt, root, names := cleanupHardlinkFixture(t, 2)
			in, p := cleanupHardlinkReviewed(t, cfg, attempt)
			fd, err := unix.Open(filepath.Join(root, names[0]), unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			last := -1
			for index, e := range cleanupRemovalOrder(p.Entries) {
				if e.Path == names[0] {
					last = index
				}
			}
			fired := false
			result, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{beforeMutation: func(point string, index int) error {
				phase := "removed"
				if kind == "unlinked_open_fd" {
					phase = "hardlink_unlinked"
				}
				if point != phase || index != last || fired {
					return nil
				}
				fired = true
				if cleanupGapStat(t, fd).Nlink != 0 {
					t.Fatal("last inode was not unlinked")
				}
				if kind == "removed_name_recreated" {
					if err := os.WriteFile(filepath.Join(root, cleanupQuarantineName, names[0]), []byte("unreviewed replacement"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					// An open writer can outlive the final name. The held descriptor
					// must still catch observed drift before acknowledging that unlink.
					if err := unix.Ftruncate(fd, 117); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}})
			if !fired || err == nil || result.ConfirmedRemoved != last {
				t.Fatalf("last mutation acknowledged: %+v %v", result, err)
			}
			raw, events := cleanupHardlinkJournal(t, root)
			for _, e := range events {
				if e.Status == "removed" && e.Index == last {
					t.Fatal("false final alias acknowledgement")
				}
			}
			replay, err := applyRestoreCleanup(context.Background(), cfg, in, cleanupHooks{})
			after, _ := cleanupHardlinkJournal(t, root)
			if err == nil || replay.Status == "completed" || !bytes.Equal(raw, after) {
				t.Fatal("ambiguous final removal replayed")
			}
		})
	}
}
