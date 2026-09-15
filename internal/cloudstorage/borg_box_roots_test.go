package cloudstorage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backup"
	"loom.local/loom/internal/backupstrategy"
)

func TestBorgDirectArchiveV2RawBoxRootsCompatibility(t *testing.T) {
	for _, realBorg := range []bool{false, true} {
		t.Run(fmt.Sprintf("real_borg_%t", realBorg), func(t *testing.T) {
			fixture := newDirectArchiveCloudFixture(t)
			fake := newFakeDirectBorg(t)
			backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: fixture.cfg, Exec: fake.exec, StreamExec: fake.streamExec, DisableRemoteLock: true}}
			if realBorg {
				binary := os.Getenv("LOOM_TEST_BORG_BINARY")
				if binary == "" {
					t.Skip("set LOOM_TEST_BORG_BINARY for disposable Borg 1.4.3 raw-root acceptance")
				}
				version, err := exec.Command(binary, "--version").CombinedOutput()
				if err != nil || strings.TrimSpace(string(version)) != "borg 1.4.3" {
					t.Fatalf("Borg version %q: %v", version, err)
				}
				fixture.cfg.Snapshots.Borg.Binary = binary
				fixture.cfg.Snapshots.Borg.Repository = filepath.Join(t.TempDir(), "repository")
				runner := NewBorgCommandRunner(fixture.cfg)
				if _, err := runner.Run(context.Background(), BorgCommand{Args: []string{"init", "--encryption", "none"}}); err != nil {
					t.Fatal(err)
				}
				backend = BorgSnapshotBackend{Runner: runner}
			}
			ctx := context.Background()
			oldRequest := directArchiveV2Request(fixture)
			oldRequest.ArchiveRef = "history-before-raw-box-roots"
			old, err := backend.ArchiveCanonicalRoots(ctx, DirectArchiveInput{Config: fixture.cfg, RequestV2: &oldRequest})
			if err != nil || !old.Committed {
				t.Fatalf("old archive: %v", err)
			}

			box := t.TempDir()
			topics, library := filepath.Join(box, "Topics"), filepath.Join(box, "Library")
			for _, dir := range []string{topics, library, filepath.Join(topics, ".loom-acceptance"), filepath.Join(library, "empty")} {
				if err := os.Mkdir(dir, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			payloads := map[string][]byte{
				filepath.Join(topics, ".loom-acceptance", "draft.md"): []byte("brainstorm, not accepted provenance\n"),
				filepath.Join(library, "unindexed.bin"):               {0, 255, 1, 128, 4},
			}
			stamp := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
			for name, body := range payloads {
				if err := os.WriteFile(name, body, 0o640); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(name, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Link(filepath.Join(library, "unindexed.bin"), filepath.Join(library, "hardlink.bin")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("unindexed.bin", filepath.Join(library, "source-link")); err != nil {
				t.Fatal(err)
			}
			request := oldRequest
			request.Roots = append(append([]backupstrategy.DirectArchiveRoot(nil), oldRequest.Roots...),
				backupstrategy.DirectArchiveRoot{Name: "box_topics", Path: topics},
				backupstrategy.DirectArchiveRoot{Name: "box_library", Path: library})
			conflict, err := backend.ArchiveCanonicalRoots(ctx, DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
			if err == nil || conflict.Committed || conflict.BorgCommandCounts["create"] != 0 || conflict.BorgCommandCounts["rename"] != 0 {
				t.Fatalf("changed roots reused old identity: %#v err=%v", conflict, err)
			}
			request.ArchiveRef = "history-with-raw-box-roots"
			current, err := backend.ArchiveCanonicalRoots(ctx, DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
			if err != nil || !current.Committed || len(current.ManifestV2.Roots) != 3 {
				t.Fatalf("new archive: %#v err=%v", current, err)
			}
			for _, command := range []string{"export-tar", "prune", "compact", "delete"} {
				if current.BorgCommandCounts[command] != 0 {
					t.Fatalf("routine path issued %s", command)
				}
			}
			for _, archived := range []DirectArchiveResult{old, current} {
				target := fetchAndRestoreRawBoxFixture(t, backend, fixture, archived)
				for name, body := range payloads {
					extracted := filepath.Join(target, strings.TrimPrefix(filepath.ToSlash(name), "/"))
					actual, err := os.ReadFile(extracted)
					if archived.Archive == old.Archive {
						if !os.IsNotExist(err) {
							t.Fatalf("historical archive acquired new roots: %s, %v", name, err)
						}
						continue
					}
					if err != nil || !bytes.Equal(actual, body) {
						t.Fatalf("raw file mismatch %s: %v", name, err)
					}
					info, err := os.Lstat(extracted)
					if err != nil || info.Mode().Perm() != 0o640 || !info.ModTime().Equal(stamp) {
						t.Fatalf("raw file metadata mismatch %s: %v", name, err)
					}
				}
				if archived.Archive == current.Archive {
					base := filepath.Join(target, strings.TrimPrefix(filepath.ToSlash(library), "/"))
					a, err := os.Stat(filepath.Join(base, "unindexed.bin"))
					if err != nil {
						t.Fatal(err)
					}
					b, err := os.Stat(filepath.Join(base, "hardlink.bin"))
					if err != nil || !os.SameFile(a, b) {
						t.Fatalf("hardlink lost: %v", err)
					}
					if link, err := os.Readlink(filepath.Join(base, "source-link")); err != nil || link != "unindexed.bin" {
						t.Fatalf("symlink lost: %q %v", link, err)
					}
					if entries, err := os.ReadDir(filepath.Join(base, "empty")); err != nil || len(entries) != 0 {
						t.Fatalf("empty directory lost: %v", err)
					}
				}
			}

			// A new snapshot records removal of the final child, without altering history.
			for _, area := range []string{topics, library} {
				entries, err := os.ReadDir(area)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if err := os.RemoveAll(filepath.Join(area, entry.Name())); err != nil {
						t.Fatal(err)
					}
				}
			}
			request.ArchiveRef = "history-empty-raw-box-roots"
			empty, err := backend.ArchiveCanonicalRoots(ctx, DirectArchiveInput{Config: fixture.cfg, RequestV2: &request})
			if err != nil || !empty.Committed {
				t.Fatalf("empty roots archive: %v", err)
			}
			target := fetchAndRestoreRawBoxFixture(t, backend, fixture, empty)
			for _, area := range []string{topics, library} {
				entries, err := os.ReadDir(filepath.Join(target, strings.TrimPrefix(filepath.ToSlash(area), "/")))
				if err != nil || len(entries) != 0 {
					t.Fatalf("empty root missing: %s %v", area, err)
				}
			}
			// Restore is archive-bound even after the live new roots became empty.
			fetchAndRestoreRawBoxFixture(t, backend, fixture, current)
		})
	}
}

func fetchAndRestoreRawBoxFixture(t *testing.T, backend BorgSnapshotBackend, fixture directArchiveCloudFixture, archived DirectArchiveResult) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "fetch")
	fetched, err := backend.Fetch(context.Background(), SnapshotFetchInput{Config: fixture.cfg, NodeID: "loom-main", Ref: archived.Archive, To: target})
	if err != nil || fetched.DirectArchiveManifestSHA256 != archived.ManifestSHA256 || fetched.ExtractionVerification == nil || fetched.ExtractionVerification.Status != SnapshotStatusSucceeded {
		t.Fatalf("strict raw-root fetch: %#v err=%v", fetched, err)
	}
	input := backup.DirectArchiveRestoreDrillInput{
		ArchiveRoot: target, ExpectedManifestSHA256: archived.ManifestSHA256,
		ExpectedRepository: fixture.cfg.Snapshots.Borg.Repository, ExpectedArchive: archived.Archive,
		V2UserSymlinkTargets: fetched.V2UserSymlinkTargets,
		OperationalTarget:    "loom_restore_drill_raw_box", ActiveDatabase: "loom_main", Owner: "loom",
		Authority: &cloudRestoreAuthorityStub{}, ProvenanceRestore: exactRecoveryStep,
		Runner: func(_ context.Context, name string, _ []string, _ io.Reader) ([]byte, error) {
			if name != "psql" {
				return nil, fmt.Errorf("unexpected verification command %q", name)
			}
			return []byte(`{"nodes":1,"worker_instances":1}`), nil
		},
	}
	if restored, err := backup.RunDirectArchiveRestoreDrill(context.Background(), input); err != nil || restored.Status != SnapshotStatusSucceeded {
		t.Fatalf("strict raw-root restore: %#v err=%v", restored, err)
	}
	return target
}
