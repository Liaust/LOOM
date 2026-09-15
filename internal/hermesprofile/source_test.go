package hermesprofile

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func sourceFixture(t *testing.T) (*sourceTree, string) {
	t.Helper()
	in, _ := fixture(t)
	root := filepath.Join(in.Workspace, ".hermes")
	for _, n := range []string{"cron/executions.db", "logs/agent.log", ".backup.lock", "cache.pyc"} {
		mustWriteSource(t, filepath.Join(root, n), "synthetic")
	}
	f, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	s, err := scanSource(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	return s, root
}
func mustWriteSource(t *testing.T, name, data string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func mustRenameSource(t *testing.T, a, b string) {
	t.Helper()
	if err := os.Rename(a, b); err != nil {
		t.Fatal(err)
	}
}
func sidecarChurn(t *testing.T, root string) {
	t.Helper()
	for _, n := range []string{"state.db-wal", "state.db-shm", "cron/executions.db-wal", "cron/executions.db-shm"} {
		mustWriteSource(t, filepath.Join(root, n), "sidecar")
	}
}

func runtimeSocketFixture(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "loom-hermes-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root := filepath.Join(base, ".hermes")
	if err := os.MkdirAll(filepath.Join(root, "state"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"SOUL.md": "fixture", "config.yaml": "memory: {}\n", "state.db": "fixture"} {
		mustWriteSource(t, filepath.Join(root, name), data)
	}
	return root
}

func bindRuntimeSocket(t *testing.T, name string, mode os.FileMode) int {
	t.Helper()
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: name}); err != nil {
		unix.Close(fd)
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		unix.Close(fd)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Close(fd)
		_ = os.Remove(name)
	})
	return fd
}

func scanRuntimeSocketFixture(t *testing.T, root string) *sourceTree {
	t.Helper()
	f, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := scanSource(context.Background(), f)
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.close()
		f.Close()
	})
	return s
}

func TestSourceAcceptsOnlyBoundStableHermesRuntimeSockets(t *testing.T) {
	t.Run("exact live sockets", func(t *testing.T) {
		root := runtimeSocketFixture(t)
		bindRuntimeSocket(t, filepath.Join(root, "gateway.sock"), 0600)
		bindRuntimeSocket(t, filepath.Join(root, "state", "gateway.loop-tick.123.sock"), 0700)
		s := scanRuntimeSocketFixture(t, root)
		if err := s.revalidate(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	for _, scenario := range []string{"unknown-name", "zero-pid", "wrong-root-mode", "wrong-loop-mode", "regular-reserved-name"} {
		t.Run(scenario, func(t *testing.T) {
			root := runtimeSocketFixture(t)
			name := filepath.Join(root, "gateway.sock")
			mode := os.FileMode(0600)
			switch scenario {
			case "unknown-name":
				name = filepath.Join(root, "state", "unrelated.sock")
			case "zero-pid":
				name = filepath.Join(root, "state", "gateway.loop-tick.0.sock")
			case "wrong-root-mode":
				mode = 0700
			case "wrong-loop-mode":
				name = filepath.Join(root, "state", "gateway.loop-tick.123.sock")
				mode = 0600
			case "regular-reserved-name":
				mustWriteSource(t, name, "not a socket")
			}
			if scenario != "regular-reserved-name" {
				bindRuntimeSocket(t, name, mode)
			}
			f, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if s, err := scanSource(context.Background(), f); err == nil {
				s.close()
				t.Fatal("invalid runtime socket accepted")
			}
		})
	}

	t.Run("replacement", func(t *testing.T) {
		root := runtimeSocketFixture(t)
		name := filepath.Join(root, "gateway.sock")
		fd := bindRuntimeSocket(t, name, 0600)
		s := scanRuntimeSocketFixture(t, root)
		if err := unix.Close(fd); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		bindRuntimeSocket(t, name, 0600)
		if err := s.revalidate(context.Background()); err == nil {
			t.Fatal("runtime socket replacement accepted")
		}
	})

	t.Run("new second loop socket", func(t *testing.T) {
		root := runtimeSocketFixture(t)
		bindRuntimeSocket(t, filepath.Join(root, "state", "gateway.loop-tick.123.sock"), 0700)
		s := scanRuntimeSocketFixture(t, root)
		bindRuntimeSocket(t, filepath.Join(root, "state", "gateway.loop-tick.124.sock"), 0700)
		if err := s.revalidate(context.Background()); err == nil {
			t.Fatal("runtime socket insertion accepted")
		}
	})
}
func TestSourceSidecarNamespaceAcceptance(t *testing.T) {
	s, root := sourceFixture(t)
	ctx := context.Background()
	if err := s.revalidate(ctx); err != nil {
		t.Fatal(err)
	}
	sidecarChurn(t, root)
	if err := s.revalidate(ctx); err != nil {
		t.Fatal(err)
	}
	// Existing sidecars may change or disappear, and every validation uses fresh
	// namespace offsets; a prior successful validation is not cached evidence.
	mustWriteSource(t, filepath.Join(root, "state.db-wal"), "later WAL")
	for _, n := range []string{"state.db-shm", "cron/executions.db-wal"} {
		if err := os.Remove(filepath.Join(root, n)); err != nil {
			t.Fatal(err)
		}
	}
	mustWriteSource(t, filepath.Join(root, "cron/executions.db-journal"), "journal")
	if err := s.revalidate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.revalidate(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestSourceSidecarNamespaceRefusals(t *testing.T) {
	scenarios := []string{"directory-aba", "ordinary-aba", "database-aba", "log-aba", "root-aba", "static-aba", "file-inode-swap", "directory-inode-swap", "unknown-sidecar", "wrong-parent", "sidecar-directory", "sidecar-symlink", "sidecar-hardlink", "sidecar-fifo", "sidecar-special-mode", "sidecar-oversize", "ordinary-insert", "ordinary-remove", "static-insert", "static-remove", "nested-profiles", "ordinary-mode-aba", "directory-mode-aba", "db-mode", "log-mode", "sidecar-move"}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			s, root := sourceFixture(t)
			target := filepath.Join(root, "SOUL.md")
			switch scenario {
			case "directory-aba":
				target = filepath.Join(root, "cron")
			case "database-aba":
				target = filepath.Join(root, "state.db")
			case "log-aba":
				target = filepath.Join(root, "logs/agent.log")
			case "root-aba":
				target = root
			case "static-aba":
				target = filepath.Join(root, "cache.pyc")
			}
			switch scenario {
			case "directory-aba", "ordinary-aba", "database-aba", "log-aba", "root-aba", "static-aba":
				mustRenameSource(t, target, target+"-away")
				mustRenameSource(t, target+"-away", target)
			case "file-inode-swap":
				mustRenameSource(t, target, target+"-away")
				mustWriteSource(t, target, "Fixture personality")
			case "directory-inode-swap":
				target = filepath.Join(root, "cron")
				mustRenameSource(t, target, target+"-away")
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			case "unknown-sidecar":
				mustWriteSource(t, filepath.Join(root, "unknown.db-wal"), "unknown")
			case "wrong-parent":
				mustWriteSource(t, filepath.Join(root, "memories/state.db-wal"), "wrong parent")
			case "ordinary-insert":
				mustWriteSource(t, filepath.Join(root, "unexpected"), "unexpected")
			case "ordinary-remove":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			case "static-insert":
				mustWriteSource(t, filepath.Join(root, "new.pyc"), "unexpected")
			case "static-remove":
				if err := os.Remove(filepath.Join(root, "cache.pyc")); err != nil {
					t.Fatal(err)
				}
			case "nested-profiles":
				if err := os.Mkdir(filepath.Join(root, "cron/profiles"), 0700); err != nil {
					t.Fatal(err)
				}
			case "ordinary-mode-aba", "directory-mode-aba":
				mode := os.FileMode(0600)
				if scenario == "directory-mode-aba" {
					target = filepath.Join(root, "cron")
					mode = 0700
				}
				if err := os.Chmod(target, 0400); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(target, mode); err != nil {
					t.Fatal(err)
				}
			case "db-mode", "log-mode":
				target = filepath.Join(root, "state.db")
				if scenario == "log-mode" {
					target = filepath.Join(root, "logs/agent.log")
				}
				mustWriteSource(t, target, "content write plus metadata")
				if err := os.Chmod(target, 0400); err != nil {
					t.Fatal(err)
				}
			}
			sidecarChurn(t, root)
			sidecar := filepath.Join(root, "state.db-journal")
			switch scenario {
			case "sidecar-directory":
				if err := os.Mkdir(sidecar, 0700); err != nil {
					t.Fatal(err)
				}
			case "sidecar-symlink":
				if err := os.Symlink(target, sidecar); err != nil {
					t.Fatal(err)
				}
			case "sidecar-hardlink":
				if err := os.Link(target, sidecar); err != nil {
					t.Fatal(err)
				}
			case "sidecar-fifo":
				if err := unix.Mkfifo(sidecar, 0600); err != nil {
					t.Fatal(err)
				}
			case "sidecar-special-mode":
				mustWriteSource(t, sidecar, "bad mode")
				if err := os.Chmod(sidecar, 0600|os.ModeSetuid); err != nil {
					t.Fatal(err)
				}
			case "sidecar-oversize":
				mustWriteSource(t, sidecar, "")
				if err := os.Truncate(sidecar, maxBytes+1); err != nil {
					t.Fatal(err)
				}
			case "sidecar-move":
				// Bind an initial sidecar before renaming it, so vnode monitoring observes
				// the move even when allowed endpoint names could otherwise conceal it.
				if err := s.revalidate(context.Background()); err != nil {
					t.Fatal(err)
				}
				// Linux reports all name moves. Darwin tests the already-bound variant in
				// TestSourceInitialSidecar; this newly-created move has a persistent alias.
				mustRenameSource(t, filepath.Join(root, "state.db-wal"), filepath.Join(root, "moved-wal"))
			}
			if err := s.revalidate(context.Background()); err == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
}
func TestSourceInitialSidecar(t *testing.T) {
	for _, scenario := range []string{"valid", "unknown", "case-alias", "bad-mode", "directory", "move"} {
		t.Run(scenario, func(t *testing.T) {
			in, _ := fixture(t)
			root := filepath.Join(in.Workspace, ".hermes")
			name := "state.db-wal"
			if scenario == "unknown" {
				name = "undeclared.db-wal"
			}
			if scenario == "case-alias" {
				name = "state.DB-WAL"
			}
			file := filepath.Join(root, name)
			if scenario == "directory" {
				if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				mustWriteSource(t, file, "sidecar")
			}
			if scenario == "bad-mode" {
				if err := os.Chmod(file, 0600|os.ModeSetuid); err != nil {
					t.Fatal(err)
				}
			}
			f, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			s, err := scanSource(context.Background(), f)
			if scenario != "valid" && scenario != "move" {
				if err == nil {
					s.close()
					t.Fatal("invalid initial sidecar accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			if scenario == "move" {
				mustRenameSource(t, file, file+"-away")
				mustRenameSource(t, file+"-away", file)
				if s.revalidate(context.Background()) == nil {
					t.Fatal("sidecar rename ABA accepted")
				}
				return
			}
			mustWriteSource(t, file, "later bytes")
			if err := s.revalidate(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if err := s.revalidate(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestSourceSidecarMetadataBounds(t *testing.T) {
	base := sourceFile{initial: Identity{Owner: 123}}
	valid := Identity{Mode: unix.S_IFREG | 0600, Links: 1, Owner: 123, Size: 8}
	if !safeSidecar(valid, base) {
		t.Fatal("safe metadata refused")
	}
	for _, mutate := range []func(*Identity){func(i *Identity) { i.Owner++ }, func(i *Identity) { i.Links++ }, func(i *Identity) { i.Size = -1 }, func(i *Identity) { i.Size = maxBytes + 1 }, func(i *Identity) { i.Mode |= 04000 }, func(i *Identity) { i.Mode = unix.S_IFLNK | 0600 }} {
		bad := valid
		mutate(&bad)
		if safeSidecar(bad, base) {
			t.Fatal("unsafe sidecar metadata accepted")
		}
	}
}
func TestSourceMonitorSetupRace(t *testing.T) {
	for _, dir := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "directory"}[dir], func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "bound")
			if dir {
				if err := os.Mkdir(name, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				mustWriteSource(t, name, "fixture")
			}
			f, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			before, err := statFD(f)
			if err != nil {
				t.Fatal(err)
			}
			mustRenameSource(t, name, name+"-away")
			mustRenameSource(t, name+"-away", name)
			// Force distinguishable timestamps without sleeps or dependence on granularity.
			if err := os.Chtimes(name, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
				t.Fatal(err)
			}
			m, err := newSourceMonitor()
			if err != nil {
				t.Fatal(err)
			}
			defer m.close()
			if m.add(f, before, sourceWatch{dir: f, kind: watchDirectory}) == nil {
				t.Fatal("pre-registration change accepted")
			}
		})
	}
}

func TestSourceSidecarChurnCannotHideZIPChanges(t *testing.T) {
	for _, scenario := range []string{"transient-entry", "database-mode", "log-mode"} {
		t.Run(scenario, func(t *testing.T) {
			in, _ := fixture(t)
			mustWriteSource(t, filepath.Join(in.Workspace, ".hermes/logs/agent.log"), "initial log")
			original := in.Runner
			in.Runner = func(ctx context.Context, b, h, o string) (string, error) {
				if scenario == "transient-entry" {
					mustWriteSource(t, filepath.Join(h, "transient-extra"), "must not be packaged")
				}
				sha, err := original(ctx, b, h, o)
				if err != nil {
					return sha, err
				}
				if scenario == "transient-entry" {
					if err := os.Remove(filepath.Join(h, "transient-extra")); err != nil {
						t.Fatal(err)
					}
				} else {
					target := "state.db"
					if scenario == "log-mode" {
						target = "logs/agent.log"
					}
					rewriteFixtureZIPMode(t, o, target, 0400)
					// Coalesce an allowed content write with a restored metadata change. The
					// monitor's write allowance cannot excuse a mismatching captured ZIP mode.
					file := filepath.Join(h, target)
					if err := os.Chmod(file, 0400); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(file, 0600); err != nil {
						t.Fatal(err)
					}
					mustWriteSource(t, file, "later content")
				}
				sidecarChurn(t, h)
				return sha, nil
			}
			// Declare the nested online DB before inventory.
			mustWriteSource(t, filepath.Join(in.Workspace, ".hermes/cron/executions.db"), "synthetic DB")
			if _, err := Publish(context.Background(), in); err == nil {
				t.Fatal("package-affecting change accepted")
			}
		})
	}
}

func rewriteFixtureZIPMode(t *testing.T, name, target string, mode os.FileMode) {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for _, entry := range r.File {
		header := entry.FileHeader
		if entry.Name == target {
			header.SetMode(mode)
		}
		dst, err := w.CreateHeader(&header)
		if err != nil {
			t.Fatal(err)
		}
		src, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(dst, src); err != nil {
			t.Fatal(err)
		}
		src.Close()
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceMonitorInitFailure(t *testing.T) {
	if os.Getenv("LOOM_TEST_MONITOR_EXHAUST") == "1" {
		// Exhaust descriptors only inside this disposable child; never alter the
		// operator's limits or global syscall hooks in the test process.
		limit := unix.Rlimit{Cur: 64, Max: 64}
		if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 128; i++ {
			fd, err := unix.Open("/dev/null", unix.O_RDONLY, 0)
			if err != nil {
				break
			}
			_ = fd
		}
		if m, err := newSourceMonitor(); err == nil {
			m.close()
			t.Fatal("monitor init succeeded without descriptors")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSourceMonitorInitFailure$", "-test.count=1")
	cmd.Env = append(os.Environ(), "LOOM_TEST_MONITOR_EXHAUST=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestSourceNativeManagedLogChmod(t *testing.T) {
	in, _ := fixture(t)
	root := filepath.Join(in.Workspace, ".hermes")
	file := filepath.Join(root, "logs/agent.log")
	mustWriteSource(t, file, "native log")
	if err := os.Chmod(file, 0660); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := scanSource(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	// Pinned _ManagedRotatingFileHandler reapplies 0660 in _open even when
	// errors.log receives no bytes. Only ctime changes on that same inode.
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, before.Mode()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("chmod changed log bytes or mtime")
	}
	if err := s.revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSourceMutableLogPersistentLinks(t *testing.T) {
	for _, inside := range []bool{false, true} {
		t.Run(map[bool]string{false: "outside", true: "inside"}[inside], func(t *testing.T) {
			s, root := sourceFixture(t)
			target := filepath.Join(t.TempDir(), "external-link")
			if inside {
				target = filepath.Join(root, "unexpected-link")
			}
			if err := os.Link(filepath.Join(root, "logs/agent.log"), target); err != nil {
				t.Fatal(err)
			}
			if s.revalidate(context.Background()) == nil {
				t.Fatal("persistent log hardlink accepted")
			}
		})
	}
}

type sourceCheckBarrier struct {
	sourceMonitor
	count  int
	mutate func()
}

func (m *sourceCheckBarrier) check(s *sourceTree) (map[*os.File]uint64, error) {
	m.count++
	if m.count == 2 {
		m.mutate()
	}
	return m.sourceMonitor.check(s)
}
func TestSourceLateMutableMetadataRefusal(t *testing.T) {
	for _, name := range []string{"logs/agent.log", "state.db", "state.db-shm"} {
		t.Run(name, func(t *testing.T) {
			in, _ := fixture(t)
			root := filepath.Join(in.Workspace, ".hermes")
			for _, file := range []string{"logs/agent.log", "state.db-shm"} {
				mustWriteSource(t, filepath.Join(root, file), "initial")
			}
			f, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			s, err := scanSource(context.Background(), f)
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			s.monitor = &sourceCheckBarrier{sourceMonitor: s.monitor, mutate: func() {
				file := filepath.Join(root, name)
				mustWriteSource(t, file, "late permitted class content")
				if err := os.Chmod(file, 0400|os.ModeSetuid); err != nil {
					t.Fatal(err)
				}
			}}
			if s.revalidate(context.Background()) == nil {
				t.Fatal("late unsafe metadata accepted")
			}
		})
	}
}
